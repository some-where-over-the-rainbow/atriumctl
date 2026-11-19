package run

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"atriumctl/kernel/def"
	"atriumctl/kernel/engine"
	"atriumctl/kernel/qrspi"
)

func TestResearchNeverGetsTheTask(t *testing.T) {
	d := qrspi.Slice1()
	r := &Run{Def: d, Cfg: Config{Task: "SECRET TASK"}}
	for name, role := range d.Roles {
		got := r.taskFor(role)
		mounts := d.Sandboxes[role.Sandbox].Artifacts["task.md"] != def.None
		if (got != "") != mounts {
			t.Errorf("role %s: task given %v, task.md mounted %v", name, got != "", mounts)
		}
	}
	for _, st := range d.Stages {
		if st.Kind == def.Agent || st.Kind == def.Fanout {
			if r.taskFor(d.Roles[st.Role]) == "" && strings.Contains(st.Prompt, "{{.Task}}") {
				t.Errorf("stage %s uses .Task but its role has no task.md", st.Name)
			}
		}
	}
	if r.taskFor(d.Roles["research"]) != "" || r.taskFor(d.Roles["worker"]) != "" {
		t.Fatal("research roles get the task")
	}
}

func TestPromptsRender(t *testing.T) {
	d := qrspi.Slice1()
	data := promptData{Task: "t", Feedback: "f", Questions: []Question{{ID: "Q_001", Title: "x"}},
		Reports: []AgentOutput{{Text: "r", Questions: []Question{{ID: "Q_001"}}}}, Artifact: "a", LintFindings: "l", Request: "q"}
	for _, st := range d.Stages {
		if st.Prompt != "" {
			if _, err := render(st.Prompt, data); err != nil {
				t.Errorf("%s: %v", st.Name, err)
			}
		}
	}
	for _, p := range []string{"fix.md", "locator.md"} {
		if _, err := render(qrspi.Prompt(p), data); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSetStatus(t *testing.T) {
	text := "# Q\n\n```{q} A\n:id: Q_001\n:status: proposed\n:area: x\n```\n\n```{q} B\n:id: Q_002\n:area: y\n```\n"
	out, err := setStatus(text, "Q_001", "approved")
	if err != nil || !strings.Contains(out, ":id: Q_001\n:status: approved\n") {
		t.Fatalf("%v %q", err, out)
	}
	out, err = setStatus(out, "Q_002", "rejected")
	if err != nil || !strings.Contains(out, ":id: Q_002\n:status: rejected\n:area: y") {
		t.Fatalf("%v %q", err, out)
	}
	if _, err := setStatus(text, "Q_009", "approved"); err == nil {
		t.Fatal("missing id accepted")
	}
}

func TestBatchesAndRequestion(t *testing.T) {
	qs, _ := json.Marshal([]Question{{ID: "Q_001"}, {ID: "Q_002"}, {ID: "Q_003"}})
	r := &Run{}
	v, err := r.batchQuestions(context.Background(), engine.Input{From: map[string]json.RawMessage{"approve": qs}})
	if err != nil {
		t.Fatal(err)
	}
	b := v.([]Batch)
	if len(b) != 2 || len(b[0].Questions) != 2 || b[1].Questions[0].ID != "Q_003" {
		t.Fatalf("%+v", b)
	}
	yes, _ := json.Marshal(AgentOutput{Text: "  REQUESTION: questions miss the templates"})
	no, _ := json.Marshal(AgentOutput{Text: "12 findings"})
	if !requestion(yes) || requestion(no) {
		t.Fatal("requestion")
	}
}

func TestMounts(t *testing.T) {
	d := qrspi.Slice1()
	r := &Run{Def: d, Dir: "/runs/x"}
	m, sources := r.mounts(d.Sandboxes["scribe-research"])
	var at []string
	for _, x := range m {
		at = append(at, x.At+map[bool]string{true: "(rw)", false: ""}[x.Writable])
	}
	if strings.Join(at, " ") != " .atriumctl/needs.toml .atriumctl/questions.md .atriumctl/research.md(rw)" {
		t.Fatalf("mounts %v", at)
	}
	if strings.Join(sources, " ") != ".atriumctl/questions.md .atriumctl/research.md" {
		t.Fatalf("sources %v", sources)
	}
	m, _ = r.mounts(d.Sandboxes["reader"])
	if len(m) != 1 {
		t.Fatalf("reader mounts %v", m)
	}
}

func TestApplyEdits(t *testing.T) {
	text := "# Q\n\n```{q} A\n:id: Q_001\n:status: proposed\n:area: x\n```\n\n```{q} B\n:id: Q_002\n:status: proposed\n:area: y\n```\n"
	out, err := applyEdits(text, map[string]string{"Q_002": "```{q} B, sharper\n:id: Q_002\n:status: approved\n:area: y\n\nMore.\n```\n"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "```{q} B, sharper\n:id: Q_002\n:status: proposed\n:area: y\n\nMore.\n```") || !strings.Contains(out, "```{q} A\n:id: Q_001") {
		t.Fatalf("%q", out)
	}
}
