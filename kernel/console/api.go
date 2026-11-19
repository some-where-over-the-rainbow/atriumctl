package console

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"atriumctl/kernel/engine"
	"atriumctl/kernel/run"
)

var runID = regexp.MustCompile(`^[0-9]{8}-[0-9]{4}-[0-9a-f]{4}$`)

func (s *Server) open(w http.ResponseWriter, r *http.Request) *run.Run {
	id := r.PathValue("id")
	if !runID.MatchString(id) {
		httpError(w, http.StatusNotFound, "no such run")
		return nil
	}
	rn, err := run.Open(filepath.Join(s.DataDir, id), s.Def())
	if err != nil {
		httpError(w, http.StatusNotFound, err.Error())
		return nil
	}
	return rn
}

type runSummary struct {
	ID      string    `json:"id"`
	Repo    string    `json:"repo"`
	Commit  string    `json:"commit"`
	Task    string    `json:"task"`
	Created time.Time `json:"created"`
	State   string    `json:"state"`
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	ents, _ := os.ReadDir(s.DataDir)
	out := []runSummary{}
	for _, e := range ents {
		if !runID.MatchString(e.Name()) {
			continue
		}
		var c run.Config
		b, err := os.ReadFile(filepath.Join(s.DataDir, e.Name(), "run.json"))
		if err != nil || json.Unmarshal(b, &c) != nil {
			continue
		}
		state := "incompatible"
		if rn, err := run.Open(filepath.Join(s.DataDir, e.Name()), s.Def()); err == nil {
			if st, err := rn.Status(); err == nil {
				state = string(st.State)
			}
			if rn.Driver() != 0 {
				state = "running"
			}
			rn.Close()
		}
		out = append(out, runSummary{ID: c.ID, Repo: filepath.Base(c.Repo), Commit: c.Commit, Task: c.Task, Created: c.Created, State: state})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	writeJSON(w, out)
}

type stageView struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Attempt int    `json:"attempt"`
	State   string `json:"state"` // done, running, gate, exhausted, waiting, failed, pending
}

type agentView struct {
	ID        string  `json:"id"`
	Role      string  `json:"role"`
	Stage     string  `json:"stage"`
	Attempt   int     `json:"attempt"`
	Parent    string  `json:"parent,omitempty"`
	Model     string  `json:"model,omitempty"`
	Outcome   string  `json:"outcome"` // running or the executor's outcome
	Turns     int     `json:"turns"`
	ToolCalls int     `json:"tool_calls"`
	ToolUses  int     `json:"tool_uses"` // self-reported, for reconciliation
	Denied    int     `json:"denied"`
	Model_    int     `json:"model_calls"`
	Cost      float64 `json:"cost"`
	WallMS    int64   `json:"wall_ms"`
	Started   string  `json:"started"`
}

type traceLine struct {
	Type         string         `json:"type"`
	TsStart      time.Time      `json:"ts_start"`
	Stage        string         `json:"stage"`
	StageAttempt int            `json:"stage_attempt"`
	Role         string         `json:"role"`
	AgentID      string         `json:"agent_id"`
	Parent       string         `json:"parent_agent_id"`
	Fields       map[string]any `json:"fields"`
}

func num(v any) float64 { f, _ := v.(float64); return f }

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	rn := s.open(w, r)
	if rn == nil {
		return
	}
	defer rn.Close()
	st, err := rn.Status()
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Agents and running stages from the trace.
	agents := map[string]*agentView{}
	var order []string
	running := map[string]bool{}
	denials := map[string]int{}
	egressDenied := 0
	if f, err := os.Open(filepath.Join(rn.Dir, "trace.jsonl")); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			var t traceLine
			if json.Unmarshal(sc.Bytes(), &t) != nil {
				continue
			}
			switch t.Type {
			case "agent":
				a := agents[t.AgentID]
				if a == nil {
					a = &agentView{ID: t.AgentID, Role: t.Role, Stage: t.Stage, Attempt: t.StageAttempt, Parent: t.Parent,
						Outcome: "running", Started: t.TsStart.Format(time.RFC3339)}
					agents[t.AgentID] = a
					order = append(order, t.AgentID)
				}
				if t.Fields["event"] == "start" {
					a.Model, _ = t.Fields["model"].(string)
				} else {
					a.Outcome, _ = t.Fields["outcome"].(string)
					a.Turns, a.ToolUses = int(num(t.Fields["num_turns"])), int(num(t.Fields["tool_uses"]))
					a.Cost, a.WallMS = num(t.Fields["cost_estimate"]), int64(num(t.Fields["wall_ms"]))
					if m, _ := t.Fields["model"].(string); m != "" {
						a.Model = m
					}
				}
			case "tool_call":
				if a := agents[t.AgentID]; a != nil {
					a.ToolCalls++
					if t.Fields["status"] == "denied" {
						a.Denied++
						denials[fmt.Sprint(t.Fields["deny_reason"])]++
					}
				}
			case "model_call":
				if a := agents[t.AgentID]; a != nil {
					a.Model_++
				}
			case "egress":
				if t.Fields["allowed"] != true {
					egressDenied++
				}
			case "stage":
				key := fmt.Sprintf("%s#%d", t.Stage, t.StageAttempt)
				switch t.Fields["event"] {
				case "start":
					running[key] = true
				case "end", "done", "fail", "exhausted", "wait":
					running[key] = false
				}
			}
		}
		f.Close()
	}
	driver := rn.Driver()
	done := map[string]bool{}
	for _, n := range st.Done {
		done[n] = true
	}
	set := func(list []string, n string) bool {
		for _, x := range list {
			if x == n {
				return true
			}
		}
		return false
	}
	var gates, exh, waits, failed []string
	for _, g := range st.Gates {
		gates = append(gates, g.Node)
	}
	for _, x := range st.Exhausted {
		exh = append(exh, x.Node)
	}
	for _, x := range st.Waits {
		waits = append(waits, x.Node)
	}
	for _, x := range st.Failed {
		failed = append(failed, x.Node)
	}
	var stages []stageView
	for _, d := range rn.Stages() {
		v := stageView{Name: d.Name, Kind: string(d.Kind), Attempt: st.Attempts[d.Name], State: "pending"}
		switch {
		case done[d.Name]:
			v.State = "done"
		case set(gates, d.Name):
			v.State = "gate"
		case set(exh, d.Name):
			v.State = "exhausted"
		case set(failed, d.Name):
			v.State = "failed"
		case set(waits, d.Name):
			v.State = "waiting"
		case driver != 0 && running[fmt.Sprintf("%s#%d", d.Name, v.Attempt)]:
			v.State = "running"
		}
		stages = append(stages, v)
	}
	list := []*agentView{}
	for _, id := range order {
		a := agents[id]
		if a.Outcome == "running" && driver == 0 {
			a.Outcome = "stopped"
		}
		list = append(list, a)
	}
	state := string(st.State)
	if driver != 0 && st.State != engine.Done {
		state = "running"
	}
	s.mu.Lock()
	dr := s.drivers[rn.Cfg.ID]
	s.mu.Unlock()
	lastErr := ""
	if dr != nil {
		lastErr = dr.lastErr()
	}
	writeJSON(w, map[string]any{
		"id": rn.Cfg.ID, "repo": rn.Cfg.Repo, "commit": rn.Cfg.Commit, "task": rn.Cfg.Task, "gate_mode": rn.Cfg.GateMode,
		"created": rn.Cfg.Created, "models": rn.Cfg.Models, "state": state, "engine_state": st.State,
		"driver_pid": driver, "driven_here": dr != nil && dr.active(), "last_error": lastErr,
		"stages": stages, "agents": list, "gates": st.Gates, "exhausted": st.Exhausted, "waits": st.Waits,
		"failed": st.Failed, "next_wake": st.NextWake, "denials": denials, "egress_denied": egressDenied,
		"artifacts": rn.Def.Artifacts,
	})
}

func (s *Server) artifact(w http.ResponseWriter, r *http.Request) {
	rn := s.open(w, r)
	if rn == nil {
		return
	}
	defer rn.Close()
	name := r.PathValue("name")
	ok := name == "needs.toml"
	for _, a := range rn.Def.Artifacts {
		ok = ok || a == name
	}
	if !ok {
		httpError(w, http.StatusNotFound, "no such artifact")
		return
	}
	b, err := os.ReadFile(filepath.Join(rn.Dir, "artifacts", name))
	if err != nil {
		httpError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(b)
}

var probeName = regexp.MustCompile(`^[a-z0-9-]+$`)

// probe serves a probe result (probe/<name>.json), or 404 before it ran.
func (s *Server) probe(w http.ResponseWriter, r *http.Request) {
	rn := s.open(w, r)
	if rn == nil {
		return
	}
	defer rn.Close()
	name := r.PathValue("name")
	if !probeName.MatchString(name) {
		httpError(w, http.StatusNotFound, "no such probe")
		return
	}
	b, err := os.ReadFile(filepath.Join(rn.Dir, "probe", name+".json"))
	if err != nil {
		httpError(w, http.StatusNotFound, "the probe has not run yet")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func (s *Server) files(w http.ResponseWriter, r *http.Request) {
	rn := s.open(w, r)
	if rn == nil {
		return
	}
	defer rn.Close()
	writeJSON(w, rn.RepoFiles())
}

var snapName = regexp.MustCompile(`^[a-z0-9-]+-a[0-9]+\.json$`)

func (s *Server) snapshots(w http.ResponseWriter, r *http.Request) {
	rn := s.open(w, r)
	if rn == nil {
		return
	}
	defer rn.Close()
	ents, _ := os.ReadDir(filepath.Join(rn.Dir, "needs"))
	type snap struct {
		Name string    `json:"name"`
		Time time.Time `json:"time"`
	}
	out := []snap{}
	for _, e := range ents {
		if info, err := e.Info(); err == nil && snapName.MatchString(e.Name()) {
			out = append(out, snap{e.Name(), info.ModTime()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	writeJSON(w, out)
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	rn := s.open(w, r)
	if rn == nil {
		return
	}
	defer rn.Close()
	name := r.PathValue("name")
	if !snapName.MatchString(name) {
		httpError(w, http.StatusNotFound, "no such snapshot")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	http.ServeFile(w, r, filepath.Join(rn.Dir, "needs", name))
}

// events streams new trace lines as server-sent events.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	rn := s.open(w, r)
	if rn == nil {
		return
	}
	path := filepath.Join(rn.Dir, "trace.jsonl")
	rn.Close()
	fl, ok := w.(http.Flusher)
	if !ok {
		httpError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return
	}
	br := bufio.NewReader(f)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	keep := time.NewTicker(20 * time.Second)
	defer keep.Stop()
	fmt.Fprint(w, "retry: 3000\n\n")
	fl.Flush()
	var partial []byte
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keep.C:
			fmt.Fprint(w, ": keepalive\n\n")
			fl.Flush()
		case <-tick.C:
			sent := false
			for {
				line, err := br.ReadBytes('\n')
				partial = append(partial, line...)
				if err != nil {
					break
				}
				fmt.Fprintf(w, "event: trace\ndata: %s\n\n", strings.TrimSpace(string(partial)))
				partial = partial[:0]
				sent = true
			}
			if sent {
				fl.Flush()
			}
		}
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		httpError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return false
	}
	return true
}

// gate records the architect's answer, then drives the run on.
func (s *Server) gate(w http.ResponseWriter, r *http.Request) {
	var a run.GateAnswer
	if !readJSON(w, r, &a) {
		return
	}
	s.act(w, r, func(rn *run.Run) error { return rn.Answer(a, "console") })
}

func (s *Server) decide(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Stage    string `json:"stage"`
		Decision string `json:"decision"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	s.act(w, r, func(rn *run.Run) error { return rn.Decide(body.Stage, engine.Decision(body.Decision)) })
}

func (s *Server) resume(w http.ResponseWriter, r *http.Request) {
	s.act(w, r, func(rn *run.Run) error { return rn.Lock() })
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	dr := s.drivers[id]
	s.mu.Unlock()
	if dr == nil || !dr.active() {
		httpError(w, http.StatusConflict, "the console is not driving this run")
		return
	}
	dr.cancel()
	writeJSON(w, map[string]any{"ok": true})
}

// act opens the run, takes its lock through f (answer, decide or lock),
// and on success drives the run in the background.
func (s *Server) act(w http.ResponseWriter, r *http.Request, f func(*run.Run) error) {
	id := r.PathValue("id")
	s.mu.Lock()
	if dr := s.drivers[id]; dr != nil && dr.active() {
		s.mu.Unlock()
		httpError(w, http.StatusConflict, "the console is already driving this run")
		return
	}
	s.mu.Unlock()
	rn := s.open(w, r)
	if rn == nil {
		return
	}
	if err := f(rn); err != nil {
		rn.Close()
		code := http.StatusBadRequest
		if errors.Is(err, run.ErrLocked) {
			code = http.StatusConflict
		}
		httpError(w, code, err.Error())
		return
	}
	s.drive(rn)
	writeJSON(w, map[string]any{"ok": true})
}
