package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atriumctl/kernel/trace"
)

// needsBin is the needs command built from ../needs; tests that need it
// skip when it has not been built.
func needsBin(t *testing.T) string {
	t.Helper()
	p, _ := filepath.Abs("../../needs/target/release/needs")
	if _, err := os.Stat(p); err != nil {
		t.Skip("needs binary not built: cargo build --release -p needs (in needs/)")
	}
	return p
}

// world is slice 1's layout: a repository, and an artifact directory
// holding task.md, questions.md, research.md and needs.toml.
type world struct {
	repo, art string
	gw        *Gateway
	srv       *httptest.Server
	sink      *trace.Memory
}

func newWorld(t *testing.T) *world {
	t.Helper()
	d := t.TempDir()
	w := &world{repo: filepath.Join(d, "repo"), art: filepath.Join(d, "artifacts"), sink: &trace.Memory{}}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(w.repo, "src/ui"), 0o755))
	must(os.MkdirAll(w.art, 0o755))
	must(os.WriteFile(filepath.Join(w.repo, "src/ui/player.py"), []byte("class PlayerBar:\n    track = None\n"), 0o644))
	must(os.WriteFile(filepath.Join(w.repo, "src/ui/router.py"), []byte("TABS = ['home']\n"), 0o644))
	must(os.WriteFile(filepath.Join(w.repo, "CLAUDE.md"), []byte("repo instructions\n"), 0o644))
	must(os.WriteFile(filepath.Join(w.art, "task.md"), []byte("SECRET TASK: reset the UI\n"), 0o644))
	cfg, err := os.ReadFile("../../needs/fixtures/slice1/needs.toml")
	must(err)
	must(os.WriteFile(filepath.Join(w.art, "needs.toml"), cfg, 0o644))
	must(os.Symlink("/etc/hostname", filepath.Join(w.repo, "escape")))
	w.gw = New(w.sink)
	w.srv = httptest.NewServer(w.gw)
	t.Cleanup(w.srv.Close)
	return w
}

func (w *world) mounts(questions, research, task string) []Mount {
	m := []Mount{{At: "", Host: w.repo}, {At: ".atriumctl/needs.toml", Host: filepath.Join(w.art, "needs.toml")}}
	add := func(name, mode string) {
		if mode == "" {
			return
		}
		host := filepath.Join(w.art, name)
		if _, err := os.Stat(host); err != nil {
			_ = os.WriteFile(host, nil, 0o644)
		}
		m = append(m, Mount{At: ".atriumctl/" + name, Host: host, Writable: mode == "rw"})
	}
	add("task.md", task)
	add("questions.md", questions)
	add("research.md", research)
	return m
}

type client struct {
	t   *testing.T
	url string
}

func (w *world) register(t *testing.T, a *Agent) *client {
	t.Helper()
	p, err := w.gw.Register(a)
	if err != nil {
		t.Fatal(err)
	}
	return &client{t: t, url: w.srv.URL + p}
}

func (c *client) rpc(method string, params any) map[string]any {
	c.t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	resp, err := http.Post(c.url, "application/json", bytes.NewReader(b))
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		c.t.Fatalf("%s: status %d: %v", method, resp.StatusCode, err)
	}
	return out
}

// call returns the tool's text and whether it was an error.
func (c *client) call(tool string, args map[string]any) (string, bool) {
	c.t.Helper()
	r := c.rpc("tools/call", map[string]any{"name": tool, "arguments": args})
	res := r["result"].(map[string]any)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	return text, res["isError"].(bool)
}

func (c *client) ok(tool string, args map[string]any) string {
	c.t.Helper()
	text, isErr := c.call(tool, args)
	if isErr {
		c.t.Fatalf("%s %v: %s", tool, args, text)
	}
	return text
}

func (c *client) denied(tool string, args map[string]any, layer string) {
	c.t.Helper()
	text, isErr := c.call(tool, args)
	if !isErr || !strings.HasPrefix(text, "denied ("+layer+")") {
		c.t.Fatalf("%s %v: want denied (%s), got %q", tool, args, layer, text)
	}
}

var readTools = []string{"read", "ls", "glob", "grep"}

func questionAgent(w *world, nb string) *Agent {
	w.gw.NeedsBin = nb
	return &Agent{
		ID: "question-1", Role: "question", Stage: "question", RunID: "r1",
		Tools:  append(append([]string{}, readTools...), "write", "edit", "needs_lint", "needs_query"),
		Mounts: w.mounts("rw", "", "rw"),
		Leases: []Lease{{At: ".atriumctl/questions.md", Mode: "rw"}, {At: ".atriumctl/task.md", Mode: "rw"}},
		Needs:  &Needs{Config: ".atriumctl/needs.toml", Sources: []string{".atriumctl/questions.md"}},
	}
}

func TestResearchCannotSeeTask(t *testing.T) {
	w := newWorld(t)
	worker := w.register(t, &Agent{ID: "w1", Role: "worker", Tools: readTools, Mounts: w.mounts("ro", "", "")})
	worker.denied("read", map[string]any{"path": ".atriumctl/task.md"}, "mount")
	worker.denied("read", map[string]any{"path": "/.atriumctl/task.md"}, "mount")
	if ls := worker.ok("ls", map[string]any{"path": ".atriumctl"}); strings.Contains(ls, "task") || !strings.Contains(ls, "questions.md") {
		t.Fatalf("ls .atriumctl = %q", ls)
	}
	if g := worker.ok("grep", map[string]any{"pattern": "SECRET"}); strings.Contains(g, "SECRET") {
		t.Fatalf("grep found the task: %q", g)
	}
	// The question stage, which has it mounted, can read it.
	q := w.register(t, questionAgent(w, needsBin(t)))
	if !strings.Contains(q.ok("read", map[string]any{"path": ".atriumctl/task.md"}), "SECRET TASK") {
		t.Fatal("question stage cannot read task.md")
	}
}

func TestNothingOutsideTheTree(t *testing.T) {
	w := newWorld(t)
	c := w.register(t, &Agent{ID: "w1", Role: "worker", Tools: readTools, Mounts: w.mounts("ro", "", "")})
	host, _ := os.ReadFile("/etc/hostname")
	for _, p := range []string{"../artifacts/task.md", "/etc/hostname", "../../../../etc/passwd", "escape"} {
		text, _ := c.call("read", map[string]any{"path": p})
		if strings.Contains(text, "SECRET") || (len(host) > 1 && strings.Contains(text, strings.TrimSpace(string(host)))) || strings.Contains(text, "root:") {
			t.Fatalf("read %s leaked: %q", p, text)
		}
	}
	c.denied("read", map[string]any{"path": "escape"}, "mount")
	if l := c.ok("ls", map[string]any{"path": ""}); strings.Contains(l, ".git") || !strings.Contains(l, ".atriumctl/") {
		t.Fatalf("ls root = %q", l)
	}
}

func TestWritesAreConfinedWithTheLayerNamed(t *testing.T) {
	w := newWorld(t)
	nb := needsBin(t)
	q := w.register(t, questionAgent(w, nb))
	worker := w.register(t, &Agent{ID: "w1", Role: "worker", Tools: readTools, Mounts: w.mounts("ro", "", "")})
	research := w.register(t, &Agent{ID: "r1", Role: "research", Tools: append(append([]string{}, readTools...), "write"),
		Mounts: w.mounts("ro", "rw", ""), Leases: []Lease{{At: ".atriumctl/research.md", Mode: "rw"}},
		Needs: &Needs{Config: ".atriumctl/needs.toml", Sources: []string{".atriumctl/questions.md", ".atriumctl/research.md"}}})
	// A role whose lease covers the repository, which is still mounted read-only.
	leaky := w.register(t, &Agent{ID: "x1", Role: "x", Tools: []string{"write"}, Mounts: w.mounts("", "", ""),
		Leases: []Lease{{At: "", Mode: "rw"}}})
	expired := w.register(t, &Agent{ID: "x2", Role: "x", Tools: []string{"write"}, Mounts: w.mounts("rw", "", ""),
		Leases: []Lease{{At: ".atriumctl/questions.md", Mode: "rw", Until: time.Now().Add(-time.Minute)}}})

	worker.denied("write", map[string]any{"path": ".atriumctl/questions.md", "content": "x"}, "grant")
	q.denied("write", map[string]any{"path": "src/ui/player.py", "content": "x"}, "lease")
	research.denied("write", map[string]any{"path": ".atriumctl/questions.md", "content": "x"}, "lease")
	leaky.denied("write", map[string]any{"path": "src/ui/player.py", "content": "x"}, "mount")
	expired.denied("write", map[string]any{"path": ".atriumctl/questions.md", "content": "x"}, "lease")
	q.denied("bash", map[string]any{"command": "ls"}, "grant")

	b, _ := os.ReadFile(filepath.Join(w.repo, "src/ui/player.py"))
	if !strings.HasPrefix(string(b), "class PlayerBar") {
		t.Fatal("repository changed")
	}
	var reasons []string
	for _, e := range w.sink.Events("tool_call") {
		if e.Fields["status"] == "denied" {
			reasons = append(reasons, fmt.Sprint(e.Fields["deny_reason"]))
		}
	}
	if strings.Join(reasons, ",") != "grant,lease,lease,mount,lease,grant" {
		t.Fatalf("trace deny reasons: %v", reasons)
	}
}

func TestCompareAndSwapAndEdit(t *testing.T) {
	w := newWorld(t)
	q := w.register(t, questionAgent(w, needsBin(t)))
	host := filepath.Join(w.art, "questions.md")
	_ = os.WriteFile(host, []byte("# Questions\n"), 0o644) // exists before the agent starts

	q.denied("write", map[string]any{"path": ".atriumctl/questions.md", "content": "new"}, "cas")
	q.ok("read", map[string]any{"path": ".atriumctl/questions.md"})
	q.ok("write", map[string]any{"path": ".atriumctl/questions.md", "content": "# Questions\n\nOne.\nOne.\n"})
	// Someone else changes the file: the agent's view is stale.
	_ = os.WriteFile(host, []byte("# Questions\n\nOne.\nOne.\nTwo.\n"), 0o644)
	q.denied("edit", map[string]any{"path": ".atriumctl/questions.md", "old_string": "Two.", "new_string": "2"}, "cas")
	q.ok("read", map[string]any{"path": ".atriumctl/questions.md"})
	if text, isErr := q.call("edit", map[string]any{"path": ".atriumctl/questions.md", "old_string": "One.", "new_string": "1"}); !isErr || !strings.Contains(text, "occurs 2 times") {
		t.Fatalf("ambiguous edit: %q", text)
	}
	q.ok("edit", map[string]any{"path": ".atriumctl/questions.md", "old_string": "One.", "new_string": "1", "replace_all": true})
	q.ok("edit", map[string]any{"path": ".atriumctl/questions.md", "old_string": "Two.", "new_string": "2"})
	b, _ := os.ReadFile(host)
	if string(b) != "# Questions\n\n1\n1\n2\n" {
		t.Fatalf("content %q", b)
	}
	arts := w.sink.Events("artifact")
	if len(arts) != 3 || arts[2].Fields["hash"] == "" {
		t.Fatalf("artifact events: %+v", arts)
	}
}

const qBlock = "```{q} %s\n:id: %s\n:status: %s\n:area: player\n```\n\n"

func TestStatusRule(t *testing.T) {
	w := newWorld(t)
	q := w.register(t, questionAgent(w, needsBin(t)))
	p := ".atriumctl/questions.md"
	q.ok("read", map[string]any{"path": p})
	q.denied("write", map[string]any{"path": p, "content": fmt.Sprintf(qBlock, "A", "Q_001", "approved")}, "status")
	q.ok("write", map[string]any{"path": p, "content": fmt.Sprintf(qBlock, "A", "Q_001", "proposed")})

	// The architect approves Q_001 (the kernel writes it, outside the gateway).
	host := filepath.Join(w.art, "questions.md")
	_ = os.WriteFile(host, []byte(fmt.Sprintf(qBlock, "A", "Q_001", "approved")), 0o644)
	q.ok("read", map[string]any{"path": p})
	q.denied("edit", map[string]any{"path": p, "old_string": ":status: approved", "new_string": ":status: proposed"}, "status")
	q.denied("write", map[string]any{"path": p, "content": "# empty\n"}, "status")
	// Adding a proposed question next to the approved one is fine.
	q.ok("write", map[string]any{"path": p, "content": fmt.Sprintf(qBlock, "A", "Q_001", "approved") + fmt.Sprintf(qBlock, "B", "Q_002", "proposed")})
}

func TestNeedsTools(t *testing.T) {
	w := newWorld(t)
	q := w.register(t, questionAgent(w, needsBin(t)))
	w.gw.RepoFiles = func() map[string]int { return map[string]int{"src/ui/player.py": 2} }
	q.ok("read", map[string]any{"path": ".atriumctl/questions.md"})
	q.ok("write", map[string]any{"path": ".atriumctl/questions.md", "content": fmt.Sprintf(qBlock, "A", "Q_001", "proposed") + fmt.Sprintf(qBlock, "B", "Q-2", "proposed")})
	lint := q.ok("needs_lint", nil)
	if !strings.Contains(lint, "id-format") || !strings.Contains(lint, "2 needs, 1 errors") {
		t.Fatalf("lint: %q", lint)
	}
	if ids := q.ok("needs_query", map[string]any{"type": "q", "status": "proposed"}); ids != "Q-2\nQ_001\n" {
		t.Fatalf("query: %q", ids)
	}
	calls := w.sink.Events("tool_call")
	last := calls[len(calls)-2]
	if last.Fields["tool"] != "needs_lint" || last.Fields["lint_errors"] != 1 {
		t.Fatalf("lint trace: %+v", last.Fields)
	}
}

func TestToolListGrantAndTokens(t *testing.T) {
	w := newWorld(t)
	worker := w.register(t, &Agent{ID: "w1", Role: "worker", Tools: readTools, Mounts: w.mounts("ro", "", "")})
	tl := worker.rpc("tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	var names []string
	for _, x := range tl {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "glob,grep,ls,read" {
		t.Fatalf("tools: %v", names)
	}
	for _, x := range tl {
		if req, ok := x.(map[string]any)["inputSchema"].(map[string]any)["required"].([]any); !ok || req == nil {
			t.Fatalf("tool schema without a required list: %v", x)
		}
	}
	worker.denied("write", map[string]any{"path": "x", "content": "y"}, "grant")
	resp, _ := http.Post(w.srv.URL+"/mcp/not-a-token", "application/json", strings.NewReader(`{"id":1,"method":"tools/list"}`))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown token: %d", resp.StatusCode)
	}
	if _, err := w.gw.Register(&Agent{ID: "z", Tools: []string{"bash"}}); err == nil {
		t.Fatal("registered an unknown tool")
	}
}

func TestSearchTools(t *testing.T) {
	w := newWorld(t)
	c := w.register(t, &Agent{ID: "w1", Role: "worker", Tools: readTools, Mounts: w.mounts("ro", "", "")})
	if g := c.ok("glob", map[string]any{"pattern": "**/*.py"}); g != "src/ui/player.py\nsrc/ui/router.py\n" {
		t.Fatalf("glob: %q", g)
	}
	if g := c.ok("grep", map[string]any{"pattern": "playerbar", "ignore_case": true, "glob": "*.py"}); g != "src/ui/player.py:1:class PlayerBar:\n" {
		t.Fatalf("grep: %q", g)
	}
	if r := c.ok("read", map[string]any{"path": "src/ui/player.py", "offset": 2, "limit": 1}); r != "     2\t    track = None\n" {
		t.Fatalf("read: %q", r)
	}
}

func TestSpawnCallsTheKernel(t *testing.T) {
	w := newWorld(t)
	var got string
	c := w.register(t, &Agent{ID: "p1", Role: "research", Tools: []string{"spawn"}, Mounts: w.mounts("", "", ""),
		Spawn: func(_ context.Context, parent *Agent, prompt string) (string, error) {
			got = parent.ID + ": " + prompt
			return "Paris", nil
		}})
	if out := c.ok("spawn", map[string]any{"prompt": "capital of France?"}); out != "Paris" || got != "p1: capital of France?" {
		t.Fatalf("spawn: %q %q", out, got)
	}
}

func TestGlobMatch(t *testing.T) {
	for _, c := range []struct {
		p, n string
		ok   bool
	}{
		{"**/*.py", "a.py", true}, {"**/*.py", "a/b/c.py", true}, {"src/**", "src/a/b", true},
		{"*.py", "a/b.py", false}, {"src/*/x.go", "src/a/x.go", true}, {"src/*/x.go", "src/a/b/x.go", false},
	} {
		if globMatch(c.p, c.n) != c.ok {
			t.Errorf("globMatch(%q, %q) != %v", c.p, c.n, c.ok)
		}
	}
}
