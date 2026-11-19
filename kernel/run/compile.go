package run

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"text/template"
	"time"

	"atriumctl/kernel/def"
	"atriumctl/kernel/engine"
	"atriumctl/kernel/executor"
	"atriumctl/kernel/gateway"
	"atriumctl/kernel/qrspi"
)

// Question is a q need as stages see it.
type Question struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Body   string `json:"body,omitempty"`
	Status string `json:"status"`
}

// Batch is one research worker's share of the approved questions.
type Batch struct {
	Questions []Question `json:"questions"`
}

// AgentOutput is what an agent stage passes on.
type AgentOutput struct {
	Agent     string     `json:"agent"`
	Text      string     `json:"text"`
	Questions []Question `json:"questions,omitempty"` // fan-out: the batch it answered
}

// GateAnswer is the architect's answer at the gate. Edits replace a
// proposed question's whole need block (its status is set by the kernel).
type GateAnswer struct {
	Approve []string          `json:"approve"`
	Reject  []string          `json:"reject"`
	Edits   map[string]string `json:"edits,omitempty"`
}

// promptData is what prompt templates get. Task is set only for roles
// whose sandbox mounts task.md: the research barrier holds structurally.
type promptData struct {
	Task         string
	Feedback     string
	Questions    []Question
	Reports      []AgentOutput
	Artifact     string
	LintFindings string
	Request      string
}

func (r *Run) compile() (*engine.Graph, error) {
	g := &engine.Graph{}
	for _, st := range r.Def.Stages {
		n := engine.Node{Name: st.Name}
		switch st.Kind {
		case def.Agent:
			n.Fn = r.agentStage(st)
		case def.Fanout:
			n.Kind, n.Cap, n.Fn = engine.Map, st.Cap, r.fanoutStage(st)
		case def.Gate:
			n.Kind = engine.Gate
		case def.Kernel:
			fn, ok := map[string]engine.Fn{
				"probe":           r.probeStage(st),
				"needs-build":     r.needsBuildStage(st.Name),
				"apply-gate":      r.applyGate,
				"batch-questions": r.batchQuestions,
			}[st.Func]
			if !ok {
				return nil, fmt.Errorf("stage %s: unknown kernel function %q", st.Name, st.Func)
			}
			n.Fn = fn
		}
		g.Nodes = append(g.Nodes, n)
	}
	for _, e := range r.Def.Edges {
		ge := engine.Edge{From: e.From, To: e.To, Back: e.Back, Max: e.Max}
		if e.Back {
			cond, ok := map[string]func(json.RawMessage) bool{"requestion": requestion}[e.When]
			if !ok {
				return nil, fmt.Errorf("edge %s->%s: unknown condition %q", e.From, e.To, e.When)
			}
			ge.When = cond
		}
		g.Edges = append(g.Edges, ge)
	}
	return g, nil
}

// requestion: the research agent's reply starts with "REQUESTION:".
func requestion(out json.RawMessage) bool {
	var o AgentOutput
	_ = json.Unmarshal(out, &o)
	return strings.HasPrefix(strings.TrimSpace(o.Text), "REQUESTION:")
}

func render(tmpl string, d promptData) (string, error) {
	t, err := template.New("prompt").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}

func (r *Run) agentID(base string) string {
	r.seqMu.Lock()
	defer r.seqMu.Unlock()
	r.seq[base]++
	if r.seq[base] == 1 {
		return base
	}
	return fmt.Sprintf("%s.%d", base, r.seq[base])
}

func (r *Run) model(tier string) string {
	if m, ok := r.Cfg.Models[tier]; ok {
		return m
	}
	return r.Def.Tiers[tier]
}

func (r *Run) taskFor(role def.Role) string {
	if r.Def.Sandboxes[role.Sandbox].Artifacts["task.md"] != def.None {
		return r.Cfg.Task
	}
	return ""
}

// mounts builds the agent's mount table from its role's sandbox.
func (r *Run) mounts(sb def.Sandbox) ([]gateway.Mount, []string) {
	var m []gateway.Mount
	if sb.Repo != def.None {
		m = append(m, gateway.Mount{At: "", Host: r.path("repo")})
	}
	var sources []string
	names := make([]string, 0, len(sb.Artifacts))
	for a := range sb.Artifacts {
		names = append(names, a)
	}
	sort.Strings(names)
	for _, a := range names {
		acc := sb.Artifacts[a]
		if acc == def.None {
			continue
		}
		m = append(m, gateway.Mount{At: ".atriumctl/" + a, Host: r.path("artifacts", a), Writable: acc == def.Write})
		if strings.HasSuffix(a, ".md") && a != "task.md" {
			sources = append(sources, ".atriumctl/"+a)
		}
	}
	return m, sources
}

type agentRun struct {
	stage   string
	attempt int
	roleN   string
	prompt  string
	parent  string
	writes  []string // artifacts leased rw for this agent
}

// runAgent registers an agent with the gateway, runs it and unregisters it.
func (r *Run) runAgent(ctx context.Context, a agentRun) (*executor.Result, string) {
	role := r.Def.Roles[a.roleN]
	sb := r.Def.Sandboxes[role.Sandbox]
	id := r.agentID(fmt.Sprintf("%s.a%d.%s", a.stage, a.attempt, a.roleN))
	mounts, sources := r.mounts(sb)
	ga := &gateway.Agent{ID: id, ParentID: a.parent, Role: a.roleN, RunID: r.Cfg.ID, Stage: a.stage, StageAttempt: a.attempt,
		Tools: role.Tools, Mounts: mounts}
	until := time.Now().Add(role.Limits.WallTime + time.Minute)
	for _, w := range a.writes {
		ga.Leases = append(ga.Leases, gateway.Lease{At: ".atriumctl/" + w, Mode: "rw", Until: until})
	}
	if _, ok := sb.Artifacts["needs.toml"]; ok && len(sources) > 0 {
		ga.Needs = &gateway.Needs{Config: ".atriumctl/needs.toml", Sources: sources}
	}
	if role.Spawns != "" {
		ga.Spawn = func(ctx context.Context, parent *gateway.Agent, request string) (string, error) {
			p, err := render(qrspi.Prompt("locator.md"), promptData{Request: request})
			if err != nil {
				return "", err
			}
			res, _ := r.runAgent(ctx, agentRun{stage: a.stage, attempt: a.attempt, roleN: role.Spawns, prompt: p, parent: parent.ID})
			if err := res.EngineError(); err != nil {
				return res.Text, err
			}
			return res.Text, nil
		}
	}
	path, err := r.gw.Register(ga)
	if err != nil {
		return &executor.Result{AgentID: id, Outcome: executor.Crashed, Stderr: err.Error()}, id
	}
	defer r.gw.Unregister(path)
	res := r.x.Run(ctx, &executor.Spec{
		AgentID: id, ParentID: a.parent, RunID: r.Cfg.ID, Stage: a.stage, StageAttempt: a.attempt, Role: a.roleN,
		Model: r.model(role.Tier), Prompt: a.prompt, AppendSystemPrompt: role.System,
		MaxTurns: role.Limits.MaxTurns, Timeout: role.Limits.WallTime, GatewayPath: path, Tools: role.Tools, Telemetry: true,
	})
	return res, id
}

// agentStage runs a stage's agent, then its checks: the artifacts were
// written through the gateway and lint clean, with fix rounds.
func (r *Run) agentStage(st def.Stage) engine.Fn {
	return func(ctx context.Context, in engine.Input) (any, error) {
		role := r.Def.Roles[st.Role]
		d := promptData{Task: r.taskFor(role)}
		if in.Feedback != nil {
			var fb AgentOutput
			_ = json.Unmarshal(in.Feedback, &fb)
			d.Feedback = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(fb.Text), "REQUESTION:"))
		}
		for _, v := range in.From {
			var reports []AgentOutput
			if json.Unmarshal(v, &reports) == nil {
				d.Reports = append(d.Reports, reports...)
			}
		}
		prompt, err := render(st.Prompt, d)
		if err != nil {
			return nil, err
		}
		agents := map[string]bool{}
		res, id := r.runAgent(ctx, agentRun{stage: st.Name, attempt: in.Attempt, roleN: st.Role, prompt: prompt, writes: st.Writes})
		agents[id] = true
		if err := res.EngineError(); err != nil {
			return res.Text, err
		}
		out := AgentOutput{Agent: id, Text: res.Text}
		for _, w := range st.Writes {
			if !r.sink.writtenBy(".atriumctl/"+w, agents) {
				return out, &engine.ExhaustedError{Partial: out, Reason: "artifact " + w + " was not written through the gateway"}
			}
		}
		if st.Check != "needs-clean" {
			return out, nil
		}
		for round := 0; ; round++ {
			findings, err := r.lintErrors(st.Writes)
			if err != nil {
				return nil, err
			}
			if findings == "" {
				return out, nil
			}
			if round >= st.FixRounds {
				return out, &engine.ExhaustedError{Partial: out, Reason: "needs not clean:\n" + findings}
			}
			fp, err := render(qrspi.Prompt("fix.md"), promptData{Artifact: ".atriumctl/" + strings.Join(st.Writes, ", .atriumctl/"), LintFindings: findings})
			if err != nil {
				return nil, err
			}
			res, id := r.runAgent(ctx, agentRun{stage: st.Name, attempt: in.Attempt, roleN: st.Role, prompt: fp, writes: st.Writes})
			if err := res.EngineError(); err != nil {
				return out, err
			}
			agents[id] = true
		}
	}
}

func (r *Run) fanoutStage(st def.Stage) engine.Fn {
	return func(ctx context.Context, in engine.Input) (any, error) {
		var b Batch
		if err := json.Unmarshal(in.Item, &b); err != nil {
			return nil, err
		}
		prompt, err := render(st.Prompt, promptData{Questions: b.Questions})
		if err != nil {
			return nil, err
		}
		res, id := r.runAgent(ctx, agentRun{stage: st.Name, attempt: in.Attempt, roleN: st.Role, prompt: prompt})
		out := AgentOutput{Agent: id, Text: res.Text, Questions: b.Questions}
		if err := res.EngineError(); err != nil {
			return out, err
		}
		return out, nil
	}
}

// --- needs ---------------------------------------------------------------

type needsReport struct {
	Error  string `json:"error"`
	Report struct {
		Needs []struct {
			ID     string  `json:"id"`
			Type   string  `json:"type"`
			Title  string  `json:"title"`
			Status *string `json:"status"`
			Body   string  `json:"body"`
		} `json:"needs"`
		Findings []struct {
			Path, Severity, Rule, Need, Message string
			Line                                int
		} `json:"findings"`
	} `json:"report"`
	NeedsJSON json.RawMessage `json:"needs_json"`
}

func (r *Run) needsAPI(op string) (*needsReport, error) { return r.needsCall(op, nil) }

func (r *Run) needsAPIWith(override map[string]string) (*needsReport, error) {
	return r.needsCall("lint", override)
}

func (r *Run) needsCall(op string, override map[string]string) (*needsReport, error) {
	var sources []map[string]string
	for _, a := range r.Def.Artifacts {
		if !strings.HasSuffix(a, ".md") || a == "task.md" {
			continue
		}
		text, ok := override[a]
		if !ok {
			b, err := os.ReadFile(r.path("artifacts", a))
			if err != nil {
				return nil, err
			}
			text = string(b)
		}
		sources = append(sources, map[string]string{"path": ".atriumctl/" + a, "text": text})
	}
	req, _ := json.Marshal(map[string]any{"op": op, "config": r.Def.NeedsConfig, "sources": sources,
		"context": map[string]any{"files": r.repoFiles}})
	cmd := exec.Command(r.Cfg.NeedsBin, "api")
	cmd.Stdin = bytes.NewReader(req)
	outb, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("needs api: %w", err)
	}
	var out needsReport
	if err := json.Unmarshal(outb, &out); err != nil {
		return nil, err
	}
	if out.Error != "" {
		return nil, fmt.Errorf("needs api: %s", out.Error)
	}
	return &out, nil
}

// lintErrors returns the error findings in the given artifacts, one per line.
func (r *Run) lintErrors(artifacts []string) (string, error) {
	rep, err := r.needsAPI("lint")
	if err != nil {
		return "", err
	}
	want := map[string]bool{}
	for _, a := range artifacts {
		want[".atriumctl/"+a] = true
	}
	var b strings.Builder
	for _, f := range rep.Report.Findings {
		if f.Severity == "error" && want[f.Path] {
			fmt.Fprintf(&b, "%s:%d: %s[%s] %s: %s\n", f.Path, f.Line, f.Severity, f.Rule, f.Need, f.Message)
		}
	}
	return b.String(), nil
}

func questions(rep *needsReport) []Question {
	var qs []Question
	for _, n := range rep.Report.Needs {
		if n.Type != "q" {
			continue
		}
		s := ""
		if n.Status != nil {
			s = *n.Status
		}
		qs = append(qs, Question{ID: n.ID, Title: n.Title, Body: n.Body, Status: s})
	}
	return qs
}

// needsBuildStage writes a needs.json snapshot and passes on the questions.
func (r *Run) needsBuildStage(stage string) engine.Fn {
	return func(_ context.Context, in engine.Input) (any, error) {
		rep, err := r.needsAPI("build")
		if err != nil {
			return nil, err
		}
		name := fmt.Sprintf("%s-a%d.json", stage, in.Attempt)
		if err := os.WriteFile(r.path("needs", name), rep.NeedsJSON, 0o600); err != nil {
			return nil, err
		}
		var nj struct {
			Errors   int            `json:"errors"`
			Warnings int            `json:"warnings"`
			Counts   map[string]any `json:"counts"`
			Hash     string         `json:"source_hash"`
		}
		_ = json.Unmarshal(rep.NeedsJSON, &nj)
		r.emit("needs_snapshot", stage, in.Attempt, map[string]any{"file": "needs/" + name, "errors": nj.Errors,
			"warnings": nj.Warnings, "counts": nj.Counts, "source_hash": nj.Hash})
		return map[string]any{"snapshot": "needs/" + name, "questions": questions(rep), "errors": nj.Errors, "warnings": nj.Warnings}, nil
	}
}

// Proposed lists the proposed questions waiting at the gate.
func (r *Run) Proposed() ([]Question, error) {
	rep, err := r.needsAPI("lint")
	if err != nil {
		return nil, err
	}
	var out []Question
	for _, q := range questions(rep) {
		if q.Status == "proposed" {
			out = append(out, q)
		}
	}
	return out, nil
}

// Answer validates and records the architect's gate answer.
func (r *Run) Answer(a GateAnswer, channel string) error {
	if err := r.Lock(); err != nil {
		return err
	}
	prop, err := r.Proposed()
	if err != nil {
		return err
	}
	ok := map[string]bool{}
	for _, q := range prop {
		ok[q.ID] = true
	}
	seen := map[string]bool{}
	for _, id := range append(append([]string{}, a.Approve...), a.Reject...) {
		if !ok[id] {
			return fmt.Errorf("%s is not a proposed question", id)
		}
		if seen[id] {
			return fmt.Errorf("%s is both approved and rejected", id)
		}
		seen[id] = true
	}
	for id := range a.Edits {
		if !ok[id] {
			return fmt.Errorf("edit of %s: not a proposed question", id)
		}
	}
	if len(a.Edits) > 0 {
		if err := r.checkEdits(a.Edits); err != nil {
			return err
		}
	}
	if a.Approve == nil {
		a.Approve = []string{}
	}
	if a.Reject == nil {
		a.Reject = []string{}
	}
	edited := []string{}
	for id := range a.Edits {
		edited = append(edited, id)
	}
	sort.Strings(edited)
	r.emit("gate", "gate", 0, map[string]any{"event": "answer", "channel": channel, "approved": a.Approve, "rejected": a.Reject, "edited": edited})
	return r.eng.Answer("gate", a)
}

// applyGate writes the architect's statuses into questions.md, then passes
// on every approved question (also those approved before a back-edge).
func (r *Run) applyGate(_ context.Context, in engine.Input) (any, error) {
	var a GateAnswer
	if err := json.Unmarshal(in.From["gate"], &a); err != nil {
		return nil, err
	}
	p := r.path("artifacts", "questions.md")
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	text, err := applyEdits(string(b), a.Edits)
	if err != nil {
		return nil, err
	}
	for status, ids := range map[string][]string{"approved": a.Approve, "rejected": a.Reject} {
		for _, id := range ids {
			if text, err = setStatus(text, id, status); err != nil {
				return nil, err
			}
		}
	}
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		return nil, err
	}
	r.emit("artifact", "approve", in.Attempt, map[string]any{"path": ".atriumctl/questions.md", "size": len(text),
		"writer": "architect", "channel": "kernel"})
	rep, err := r.needsAPI("lint")
	if err != nil {
		return nil, err
	}
	var approved []Question
	for _, q := range questions(rep) {
		if q.Status == "approved" {
			approved = append(approved, q)
		}
	}
	return approved, nil
}

// setStatus changes the :status: line of the need block with :id: id.
func setStatus(text, id, status string) (string, error) {
	lines := strings.Split(text, "\n")
	inBlock, found := false, false
	blockStart := -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "```{") && !inBlock:
			inBlock, blockStart = true, i
		case t == "```" && inBlock:
			inBlock = false
		case inBlock && t == ":id: "+id:
			for j := blockStart + 1; j < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[j]), ":"); j++ {
				if strings.HasPrefix(strings.TrimSpace(lines[j]), ":status:") {
					lines[j] = ":status: " + status
					found = true
				}
			}
			if !found {
				lines = append(lines[:i+1], append([]string{":status: " + status}, lines[i+1:]...)...)
				found = true
			}
		}
		if found {
			break
		}
	}
	if !found {
		return text, fmt.Errorf("question %s not found in questions.md", id)
	}
	return strings.Join(lines, "\n"), nil
}

// batchQuestions: one research worker per one or two approved questions.
func (r *Run) batchQuestions(_ context.Context, in engine.Input) (any, error) {
	var qs []Question
	if err := json.Unmarshal(in.From["approve"], &qs); err != nil {
		return nil, err
	}
	batches := []Batch{}
	for i := 0; i < len(qs); i += 2 {
		batches = append(batches, Batch{Questions: qs[i:min(i+2, len(qs))]})
	}
	return batches, nil
}

// blockRange finds the need block with :id: id: first and last line index.
func blockRange(lines []string, id string) (int, int, bool) {
	start := -1
	hasID := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "```{") && start < 0:
			start, hasID = i, false
		case t == "```" && start >= 0:
			if hasID {
				return start, i, true
			}
			start = -1
		case start >= 0 && t == ":id: "+id:
			hasID = true
		}
	}
	return 0, 0, false
}

// applyEdits replaces each edited question's block; the status line is
// forced to proposed (the gate's decision is applied afterwards).
func applyEdits(text string, edits map[string]string) (string, error) {
	ids := make([]string, 0, len(edits))
	for id := range edits {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		lines := strings.Split(text, "\n")
		a, b, ok := blockRange(lines, id)
		if !ok {
			return text, fmt.Errorf("question %s not found in questions.md", id)
		}
		block := strings.Split(strings.TrimRight(edits[id], "\n"), "\n")
		for i, l := range block {
			if strings.HasPrefix(strings.TrimSpace(l), ":status:") {
				block[i] = ":status: proposed"
			}
		}
		lines = append(lines[:a], append(block, lines[b+1:]...)...)
		text = strings.Join(lines, "\n")
	}
	return text, nil
}

// checkEdits lints questions.md with the edits applied: each edited block
// must still be one q need with the same id, and have no errors.
func (r *Run) checkEdits(edits map[string]string) error {
	for id, block := range edits {
		if n := strings.Count(block, "```{"); n != 1 || !strings.Contains(block, "```{q}") {
			return fmt.Errorf("edit of %s: must be exactly one {q} block", id)
		}
		if !strings.Contains(block, "\n:id: "+id+"\n") {
			return fmt.Errorf("edit of %s: the block must keep :id: %s", id, id)
		}
	}
	b, err := os.ReadFile(r.path("artifacts", "questions.md"))
	if err != nil {
		return err
	}
	text, err := applyEdits(string(b), edits)
	if err != nil {
		return err
	}
	rep, err := r.needsAPIWith(map[string]string{"questions.md": text})
	if err != nil {
		return err
	}
	var bad []string
	for _, f := range rep.Report.Findings {
		if _, edited := edits[f.Need]; edited && f.Severity == "error" {
			bad = append(bad, fmt.Sprintf("%s: %s", f.Need, f.Message))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("edited questions do not lint: %s", strings.Join(bad, "; "))
	}
	return nil
}
