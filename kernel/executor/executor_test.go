package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"atriumctl/kernel/engine"
	"atriumctl/kernel/trace"
)

// The test binary doubles as a fake `claude`: with FAKE_CLAUDE set it reads
// the prompt from stdin and plays a scenario on stdout.
func TestMain(m *testing.M) {
	if sc := os.Getenv("FAKE_CLAUDE"); sc != "" {
		fakeClaude(sc)
		return
	}
	os.Exit(m.Run())
}

func emit(v map[string]any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func fakeClaude(scenario string) {
	if p := os.Getenv("FAKE_ARGS_OUT"); p != "" {
		b, _ := json.Marshal(os.Args[1:])
		_ = os.WriteFile(p, b, 0o600)
	}
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buf)
		if n == 0 || err != nil || buf[0] == '\n' {
			break
		}
		line = append(line, buf[0])
	}
	emit(map[string]any{"type": "system", "subtype": "init", "model": "claude-haiku-5-5",
		"tools": []string{"mcp__gw__read", "mcp__gw__write"}})
	rl := func(status string) {
		emit(map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{
			"status": status, "rateLimitType": "five_hour", "resetsAt": 1795002000.0,
			"unifiedWindows": map[string]any{"five_hour": map[string]any{"utilization": 0.97}}}})
	}
	toolUse := func() {
		emit(map[string]any{"type": "assistant", "message": map[string]any{"id": "m1", "content": []any{
			map[string]any{"type": "tool_use", "name": "mcp__gw__read"}}}})
	}
	result := func(m map[string]any) {
		m["type"] = "result"
		m["session_id"] = "s-1"
		emit(m)
	}
	switch scenario {
	case "ok":
		rl("allowed")
		toolUse()
		result(map[string]any{"subtype": "success", "is_error": false, "terminal_reason": "completed",
			"result": "echo: " + string(line), "num_turns": 2.0, "total_cost_usd": 0.001})
	case "max_turns":
		toolUse()
		result(map[string]any{"subtype": "error_max_turns", "is_error": true, "terminal_reason": "max_turns", "result": "half done", "num_turns": 2.0})
	case "usage_limit":
		rl("rejected")
		result(map[string]any{"subtype": "success", "is_error": true, "terminal_reason": "api_error", "api_error_status": 429.0, "result": "limit"})
	case "auth":
		result(map[string]any{"subtype": "success", "is_error": true, "terminal_reason": "api_error", "api_error_status": 401.0,
			"result": "Failed to authenticate. API Error: 401"})
	case "crash":
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(3)
	case "hang":
		if p := os.Getenv("FAKE_CHILD_OUT"); p != "" {
			// A grandchild in the same process group, like a tool process.
			_, _ = syscall.ForkExec("/bin/sleep", []string{"sleep", "60"}, &syscall.ProcAttr{})
			_ = os.WriteFile(p, []byte("started"), 0o600)
		}
		time.Sleep(time.Minute)
	}
}

func fakeExec(t *testing.T, sink trace.Sink) *Executor {
	t.Helper()
	return &Executor{Runner: &HostRunner{Binary: os.Args[0], BaseURL: "http://127.0.0.1:1", Dir: t.TempDir()}, Trace: sink}
}

func spec(scenario string, extra ...string) *Spec {
	return &Spec{AgentID: "q-1", RunID: "r1", Stage: "question", Role: "question", Model: "haiku",
		Prompt: "hello", AppendSystemPrompt: "stage prompt", MaxTurns: 5, GatewayPath: "/mcp/tok",
		Tools: []string{"read", "write"}, Env: append([]string{"FAKE_CLAUDE=" + scenario}, extra...)}
}

func TestCompletedAndArguments(t *testing.T) {
	argsOut := t.TempDir() + "/args.json"
	sink := &trace.Memory{}
	r := fakeExec(t, sink).Run(context.Background(), spec("ok", "FAKE_ARGS_OUT="+argsOut))
	if r.Outcome != Completed || r.Text != `echo: {"message":{"content":"hello","role":"user"},"type":"user"}` {
		t.Fatalf("%+v", r)
	}
	if r.ToolUses != 1 || r.NumTurns != 2 || r.Model != "claude-haiku-5-5" || r.RateLimit.Utilization["five_hour"] != 0.97 {
		t.Fatalf("%+v", r)
	}
	if r.EngineError() != nil {
		t.Fatal(r.EngineError())
	}
	b, _ := os.ReadFile(argsOut)
	var args []string
	_ = json.Unmarshal(b, &args)
	joined := strings.Join(args, " ")
	for _, want := range []string{"--tools  --strict-mcp-config --setting-sources  --disable-slash-commands",
		"--allowedTools mcp__gw__read,mcp__gw__write", "--max-turns 5", "--append-system-prompt stage prompt",
		`"url":"http://127.0.0.1:1/mcp/tok"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("args lack %q:\n%s", want, joined)
		}
	}
	ev := sink.Events("agent")
	if len(ev) != 2 || ev[1].Fields["outcome"] != "completed" || ev[1].Fields["tool_uses"] != 1 {
		t.Fatalf("agent events: %+v", ev)
	}
}

func TestOutcomes(t *testing.T) {
	for _, c := range []struct {
		scenario string
		want     Outcome
		check    func(error) bool
	}{
		{"max_turns", MaxTurns, func(err error) bool {
			var x *engine.ExhaustedError
			return errors.As(err, &x) && x.Reason == "turns" && x.Partial == "half done"
		}},
		{"usage_limit", UsageLimit, func(err error) bool {
			var w *engine.WaitError
			return errors.As(err, &w) && w.Until.Equal(time.Unix(1795002000, 0))
		}},
		{"auth", AuthError, func(err error) bool { return err != nil && strings.Contains(err.Error(), "auth_error") }},
		{"crash", Crashed, func(err error) bool { return err != nil && strings.Contains(err.Error(), "boom") }},
	} {
		sink := &trace.Memory{}
		r := fakeExec(t, sink).Run(context.Background(), spec(c.scenario))
		if r.Outcome != c.want || !c.check(r.EngineError()) {
			t.Errorf("%s: outcome %s, engine error %v", c.scenario, r.Outcome, r.EngineError())
		}
		if c.scenario == "usage_limit" && len(sink.Events("limit_event")) != 2 {
			t.Errorf("usage limit events: %+v", sink.Events("limit_event"))
		}
	}
}

func TestTimeoutAndAbortKillTheProcessGroup(t *testing.T) {
	for _, mode := range []string{"timeout", "abort"} {
		marker := t.TempDir() + "/child"
		s := spec("hang", "FAKE_CHILD_OUT="+marker)
		ctx, cancel := context.WithCancel(context.Background())
		if mode == "timeout" {
			s.Timeout = 300 * time.Millisecond
		} else {
			go func() {
				for i := 0; i < 100; i++ {
					if _, err := os.Stat(marker); err == nil {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				cancel()
			}()
		}
		t0 := time.Now()
		r := fakeExec(t, nil).Run(ctx, s)
		cancel()
		want := map[string]Outcome{"timeout": Timeout, "abort": Aborted}[mode]
		if r.Outcome != want {
			t.Fatalf("%s: outcome %s", mode, r.Outcome)
		}
		if d := time.Since(t0); d > 3*time.Second {
			t.Fatalf("%s: took %s", mode, d)
		}
		var x *engine.ExhaustedError
		if mode == "timeout" && !(errors.As(r.EngineError(), &x) && x.Reason == "time") {
			t.Fatalf("timeout engine error: %v", r.EngineError())
		}
		if mode == "abort" && !errors.Is(r.EngineError(), context.Canceled) {
			t.Fatalf("abort engine error: %v", r.EngineError())
		}
	}
}

func TestContainerArguments(t *testing.T) {
	c := &ContainerRunner{Image: "atriumctl-harness:test", RunDir: "/run/x", TokenFile: "/tok", ConfigDir: "/cfg"}
	x := &Executor{Runner: c}
	s := spec("ok")
	s.Telemetry = true
	s.Env = x.env(s)
	cmd, name, err := c.Command(s, x.args(s))
	if err != nil || !strings.HasPrefix(name, "atriumctl-q-1-") {
		t.Fatal(err, name)
	}
	j := strings.Join(cmd.Args, " ")
	for _, want := range []string{"--network none", "--read-only", "--cap-drop all", "/tok:/run/secrets/claude-token:ro",
		"/cfg:/config:ro", "OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:8080", "atriumctl.agent_id=q-1",
		`"url":"http://127.0.0.1:8080/mcp/tok"`} {
		if !strings.Contains(j, want) {
			t.Errorf("podman args lack %q", want)
		}
	}
	if strings.Contains(j, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Error("token passed as an environment variable")
	}
}
