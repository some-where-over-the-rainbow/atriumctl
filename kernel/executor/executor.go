// Package executor runs agents as headless Claude Code processes
// (decision 41): one `claude -p` per agent, driven over stream-json, either
// on the host or in a harness container (runner.go).
//
// Built-in tools are off, setting sources are empty and the tool gateway is
// the only MCP server (docs/spike-claude-code.md). Outcomes are read from
// terminal_reason and the API error status, never from subtype.
package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"atriumctl/kernel/engine"
	"atriumctl/kernel/trace"
)

type Spec struct {
	AgentID            string
	ParentID           string
	RunID              string
	Stage              string
	StageAttempt       int
	Role               string
	Model              string // tier alias: opus, sonnet, haiku
	Prompt             string
	AppendSystemPrompt string // decision 46
	MaxTurns           int
	Timeout            time.Duration // wall time; reaching it is "exhausted"
	GatewayPath        string        // /mcp/<token> from gateway.Register
	Tools              []string      // gateway tool names the agent may call
	Telemetry          bool          // export OpenTelemetry to the kernel
	Env                []string      // extra environment for claude
}

type Outcome string

const (
	Completed  Outcome = "completed"
	MaxTurns   Outcome = "max_turns"
	Timeout    Outcome = "timeout"
	Aborted    Outcome = "aborted"
	UsageLimit Outcome = "usage_limit" // subscription window: wait for ResetsAt
	AuthError  Outcome = "auth_error"
	APIError   Outcome = "api_error"
	Crashed    Outcome = "crashed" // exited without a result event
)

// RateLimit is the latest rate_limit_event of a run.
type RateLimit struct {
	Status      string
	Type        string
	ResetsAt    time.Time
	Utilization map[string]float64 // per window: five_hour, seven_day
}

type Result struct {
	AgentID        string
	Outcome        Outcome
	Text           string // the final result text (partial when exhausted)
	TerminalReason string
	APIErrorStatus int
	NumTurns       int
	SessionID      string
	Model          string
	CostUSD        float64
	Usage          map[string]any
	ToolUses       int // tool_use blocks in the stream, for reconciliation with the gateway
	Tools          []string
	RateLimit      *RateLimit
	Start, End     time.Time
	ExitCode       int
	Stderr         string
	Container      string
}

// Runner starts claude somewhere: on the host or in a container.
type Runner interface {
	Command(spec *Spec, args []string) (*exec.Cmd, string, error) // cmd, container name or ""
	GatewayURL(path string) string
	TelemetryEndpoint() string
	Gone(container string) bool // the container (if any) no longer exists
}

type Executor struct {
	Runner Runner
	Trace  trace.Sink
	Now    func() time.Time
	// OnEvent sees every stream event, for the console.
	OnEvent func(agentID string, ev map[string]any)
}

func (x *Executor) args(s *Spec) []string {
	a := []string{
		"-p", "--output-format", "stream-json", "--input-format", "stream-json", "--verbose",
		"--model", s.Model,
		"--tools", "",
		"--strict-mcp-config",
		"--setting-sources", "",
		"--disable-slash-commands",
		"--no-session-persistence",
		"--permission-prompts", "none",
	}
	if s.GatewayPath != "" {
		cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
			"gw": map[string]any{"type": "http", "url": x.Runner.GatewayURL(s.GatewayPath)},
		}})
		allowed := make([]string, len(s.Tools))
		for i, t := range s.Tools {
			allowed[i] = "mcp__gw__" + t
		}
		a = append(a, "--mcp-config", string(cfg))
		if len(allowed) > 0 {
			a = append(a, "--allowedTools", strings.Join(allowed, ","))
		}
	}
	if s.MaxTurns > 0 {
		a = append(a, "--max-turns", strconv.Itoa(s.MaxTurns))
	}
	if s.AppendSystemPrompt != "" {
		a = append(a, "--append-system-prompt", s.AppendSystemPrompt)
	}
	return a
}

func (x *Executor) env(s *Spec) []string {
	env := append([]string{}, s.Env...)
	if s.Telemetry {
		attrs := []string{"atriumctl.agent_id=" + s.AgentID, "atriumctl.run_id=" + s.RunID, "atriumctl.stage=" + s.Stage,
			"atriumctl.stage_attempt=" + strconv.Itoa(s.StageAttempt), "atriumctl.role=" + s.Role, "atriumctl.parent_agent_id=" + s.ParentID}
		env = append(env,
			"CLAUDE_CODE_ENABLE_TELEMETRY=1", "OTEL_LOGS_EXPORTER=otlp", "OTEL_METRICS_EXPORTER=none",
			"OTEL_EXPORTER_OTLP_PROTOCOL=http/json", "OTEL_EXPORTER_OTLP_ENDPOINT="+x.Runner.TelemetryEndpoint(),
			"OTEL_LOGS_EXPORT_INTERVAL=1000", "OTEL_RESOURCE_ATTRIBUTES="+strings.Join(attrs, ","))
	}
	return env
}

func (x *Executor) now() time.Time {
	if x.Now != nil {
		return x.Now()
	}
	return time.Now()
}

func (x *Executor) emit(s *Spec, typ string, at time.Time, f map[string]any) {
	if x.Trace == nil {
		return
	}
	x.Trace.Emit(trace.Event{Type: typ, SchemaVersion: trace.SchemaVersion, Source: trace.SourceKernel, TsStart: at,
		RunID: s.RunID, Stage: s.Stage, StageAttempt: s.StageAttempt, Role: s.Role, AgentID: s.AgentID,
		ParentAgentID: s.ParentID, Fields: f})
}

// Run runs one agent to its end. ctx ending aborts it; Spec.Timeout bounds it.
func (x *Executor) Run(ctx context.Context, s *Spec) *Result {
	s.Env = x.env(s)
	r := &Result{AgentID: s.AgentID, Start: x.now()}
	cmd, container, err := x.Runner.Command(s, x.args(s))
	r.Container = container
	if err != nil {
		r.Outcome, r.Stderr = Crashed, err.Error()
		return r
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr tailBuffer
	cmd.Stderr = &stderr

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var timedOut atomic.Bool
	if s.Timeout > 0 {
		timer := time.AfterFunc(s.Timeout, func() { timedOut.Store(true); cancel() })
		defer timer.Stop()
	}
	if err := cmd.Start(); err != nil {
		r.Outcome, r.Stderr = Crashed, err.Error()
		return r
	}
	x.emit(s, "agent", r.Start, map[string]any{"event": "start", "model": s.Model, "container": container})
	pgid := cmd.Process.Pid
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
			time.AfterFunc(5*time.Second, func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
		})
	}
	go func() { <-runCtx.Done(); stop() }()

	msg, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": s.Prompt}})
	_, _ = stdin.Write(append(msg, '\n'))

	var final map[string]any
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var ev map[string]any
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if x.OnEvent != nil {
			x.OnEvent(s.AgentID, ev)
		}
		switch ev["type"] {
		case "system":
			if ev["subtype"] == "init" {
				r.Model, _ = ev["model"].(string)
				for _, t := range asSlice(ev["tools"]) {
					r.Tools = append(r.Tools, fmt.Sprint(t))
				}
			}
		case "assistant":
			m, _ := ev["message"].(map[string]any)
			for _, c := range asSlice(m["content"]) {
				if b, _ := c.(map[string]any); b["type"] == "tool_use" {
					r.ToolUses++
				}
			}
		case "rate_limit_event":
			r.RateLimit = rateLimit(ev)
			if r.RateLimit != nil && r.RateLimit.Status != "allowed" {
				x.emit(s, "limit_event", x.now(), map[string]any{"kind": "window", "action": "observed",
					"status": r.RateLimit.Status, "window": r.RateLimit.Type, "resets_at": r.RateLimit.ResetsAt})
			}
		case "result":
			final = ev
			_ = stdin.Close()
		}
	}
	err = cmd.Wait()
	r.End = x.now()
	r.Stderr = stderr.String()
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
		r.ExitCode = ee.ExitCode()
	}
	if final != nil {
		r.Text, _ = final["result"].(string)
		r.TerminalReason, _ = final["terminal_reason"].(string)
		r.SessionID, _ = final["session_id"].(string)
		r.CostUSD, _ = final["total_cost_usd"].(float64)
		r.Usage, _ = final["usage"].(map[string]any)
		if n, ok := final["num_turns"].(float64); ok {
			r.NumTurns = int(n)
		}
		if n, ok := final["api_error_status"].(float64); ok {
			r.APIErrorStatus = int(n)
		}
	}
	switch {
	case timedOut.Load():
		r.Outcome = Timeout
	case ctx.Err() != nil:
		r.Outcome = Aborted
	case final == nil:
		r.Outcome = Crashed
	default:
		r.Outcome = outcome(final, r)
	}
	f := map[string]any{"event": "end", "outcome": string(r.Outcome), "terminal_reason": r.TerminalReason,
		"num_turns": r.NumTurns, "tool_uses": r.ToolUses, "cost_estimate": r.CostUSD, "wall_ms": r.End.Sub(r.Start).Milliseconds(),
		"session_id": r.SessionID, "model": r.Model, "exit_code": r.ExitCode}
	if r.APIErrorStatus != 0 {
		f["api_error_status"] = r.APIErrorStatus
	}
	if container != "" {
		f["container"] = container
		f["container_gone"] = x.Runner.Gone(container)
	}
	x.emit(s, "agent", r.End, f)
	if r.Outcome == UsageLimit {
		x.emit(s, "limit_event", r.End, map[string]any{"kind": "window", "action": "wait", "resets_at": r.ResetsAt()})
	}
	if r.Outcome == MaxTurns || r.Outcome == Timeout {
		x.emit(s, "limit_event", r.End, map[string]any{"kind": map[Outcome]string{MaxTurns: "turns", Timeout: "time"}[r.Outcome], "action": "abort"})
	}
	return r
}

func outcome(final map[string]any, r *Result) Outcome {
	isErr, _ := final["is_error"].(bool)
	switch r.TerminalReason {
	case "max_turns":
		return MaxTurns
	case "api_error":
		switch {
		case r.APIErrorStatus == 429 || (r.RateLimit != nil && r.RateLimit.Status != "allowed" && r.RateLimit.Status != ""):
			return UsageLimit
		case r.APIErrorStatus == 401 || r.APIErrorStatus == 403:
			return AuthError
		}
		return APIError
	case "completed":
		if !isErr {
			return Completed
		}
	}
	if isErr {
		return APIError
	}
	return Completed
}

// ResetsAt is when a usage-limited agent may run again: the window's reset
// time, or five minutes from the end if the stream did not say.
func (r *Result) ResetsAt() time.Time {
	if r.RateLimit != nil && !r.RateLimit.ResetsAt.IsZero() {
		return r.RateLimit.ResetsAt
	}
	return r.End.Add(5 * time.Minute)
}

// EngineError turns a result into what a graph node returns: nil when
// completed, a wait for the usage window, "exhausted" for turn and time
// caps (with the partial text), and an error otherwise.
func (r *Result) EngineError() error {
	switch r.Outcome {
	case Completed:
		return nil
	case UsageLimit:
		return &engine.WaitError{Until: r.ResetsAt(), Reason: "subscription usage window"}
	case MaxTurns:
		return &engine.ExhaustedError{Partial: r.Text, Reason: "turns"}
	case Timeout:
		return &engine.ExhaustedError{Partial: r.Text, Reason: "time"}
	case Aborted:
		return context.Canceled
	}
	return fmt.Errorf("agent %s: %s (terminal_reason %q, api status %d): %s", r.AgentID, r.Outcome,
		r.TerminalReason, r.APIErrorStatus, firstLine(r.Text+" "+r.Stderr))
}

func rateLimit(ev map[string]any) *RateLimit {
	info, _ := ev["rate_limit_info"].(map[string]any)
	if info == nil {
		return nil
	}
	rl := &RateLimit{Utilization: map[string]float64{}}
	rl.Status, _ = info["status"].(string)
	rl.Type, _ = info["rateLimitType"].(string)
	if t, ok := info["resetsAt"].(float64); ok {
		rl.ResetsAt = time.Unix(int64(t), 0)
	}
	if w, ok := info["unifiedWindows"].(map[string]any); ok {
		for name, v := range w {
			if m, ok := v.(map[string]any); ok {
				if u, ok := m["utilization"].(float64); ok {
					rl.Utilization[name] = u
				}
			}
		}
	}
	return rl
}

func asSlice(v any) []any { s, _ := v.([]any); return s }

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// tailBuffer keeps the last 8 KiB written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 8<<10 {
		t.buf = t.buf[len(t.buf)-8<<10:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

var _ io.Writer = (*tailBuffer)(nil)
