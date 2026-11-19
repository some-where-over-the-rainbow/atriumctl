package main

// The executor: one `claude -p` process per agent, driven over stream-json.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type AgentSpec struct {
	ID             string
	Model          string // tier alias: haiku, sonnet, opus
	Prompt         string
	AppendSystem   string
	MaxTurns       int
	Cwd            string
	MCPURL         string   // tool gateway URL for this agent; empty = no MCP server
	SettingSources *string  // nil = flag omitted; "" = no sources
	ExtraEnv       []string // e.g. telemetry
	ExtraArgs      []string
	Timeout        time.Duration
	Container      *Container // nil = run claude on the host
	// OnEvent sees every stream event; returning true aborts the agent.
	OnEvent func(ev map[string]any) bool
}

type AgentRun struct {
	Spec          *AgentSpec       `json:"-"`
	Args          []string         `json:"args"`
	Events        []map[string]any `json:"-"`
	Init          map[string]any   `json:"init,omitempty"`
	Result        map[string]any   `json:"result,omitempty"`
	RateLimits    []map[string]any `json:"rate_limits,omitempty"`
	ToolUses      []string         `json:"tool_uses"` // names from assistant tool_use blocks
	AssistantText string           `json:"assistant_text"`
	ToInit        time.Duration    `json:"to_init_ms"`
	ToResult      time.Duration    `json:"to_result_ms"`
	ToExit        time.Duration    `json:"to_exit_ms"`
	PeakRSSKB     int64            `json:"peak_rss_kb"`
	Aborted       bool             `json:"aborted"`
	ExitErr       string           `json:"exit_err,omitempty"`
	Stderr        string           `json:"stderr,omitempty"`
	LeftoverPIDs  []int            `json:"leftover_pids,omitempty"`
	ID            string           `json:"id"`
	ContainerName string           `json:"container,omitempty"`
	CgroupPeakKB  int64            `json:"cgroup_peak_kb,omitempty"`
	ContainerLeft bool             `json:"container_left,omitempty"`
}

// baseEnv is deliberately small: what a harness container would carry.
func baseEnv() []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"USER=" + os.Getenv("USER"),
		"LANG=C.UTF-8",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	}
	// The spike uses a long-lived token when one is given; otherwise the
	// logged-in credentials under $HOME/.claude.
	if t := os.Getenv("SPIKE_OAUTH_TOKEN"); t != "" {
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+t)
	}
	if d := os.Getenv("SPIKE_CONFIG_DIR"); d != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+d)
	}
	return env
}

func (s *AgentSpec) args() []string {
	a := []string{
		"-p",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--model", s.Model,
		"--tools", "",
		"--strict-mcp-config",
		"--disable-slash-commands",
		"--no-session-persistence",
		"--permission-prompts", "none",
	}
	if s.SettingSources != nil {
		a = append(a, "--setting-sources", *s.SettingSources)
	}
	if s.MCPURL != "" {
		cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
			"gw": map[string]any{"type": "http", "url": s.MCPURL},
		}})
		a = append(a, "--mcp-config", string(cfg), "--allowedTools", "mcp__gw__read,mcp__gw__ls,mcp__gw__spawn")
	}
	if s.MaxTurns > 0 {
		a = append(a, "--max-turns", strconv.Itoa(s.MaxTurns))
	}
	if s.AppendSystem != "" {
		a = append(a, "--append-system-prompt", s.AppendSystem)
	}
	return append(a, s.ExtraArgs...)
}

func RunAgent(parent context.Context, s *AgentSpec) *AgentRun {
	run := &AgentRun{Spec: s, Args: s.args(), ID: s.ID}
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if s.Container != nil {
		run.ContainerName = "atriumctl-" + strings.ReplaceAll(s.ID, ".", "-") + "-" + randHex(3)
		cmd = exec.Command("podman", s.Container.runArgs(run.ContainerName, s.ExtraEnv, run.Args)...)
		cmd.Env = os.Environ() // podman needs XDG_RUNTIME_DIR and friends; the container does not inherit it
	} else {
		cmd = exec.Command("claude", run.Args...)
		cmd.Dir = s.Cwd
		cmd.Env = append(baseEnv(), s.ExtraEnv...)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr strings.Builder
	cmd.Stderr = &stderr

	start := time.Now()
	if err := cmd.Start(); err != nil {
		run.ExitErr = err.Error()
		return run
	}
	pgid := cmd.Process.Pid

	// Kill the whole process group on abort or timeout.
	var killOnce sync.Once
	kill := func() {
		killOnce.Do(func() {
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
			time.AfterFunc(3*time.Second, func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
		})
	}
	go func() { <-ctx.Done(); kill() }()

	// Sample memory of the process group (and of the container's cgroup).
	cg := &cgroupWatch{}
	stopSample := make(chan struct{})
	var sampleWG sync.WaitGroup
	sampleWG.Add(1)
	go func() {
		defer sampleWG.Done()
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			if kb := groupRSSKB(pgid); kb > run.PeakRSSKB {
				run.PeakRSSKB = kb
			}
			if run.ContainerName != "" {
				if kb := cg.peakKB(run.ContainerName); kb > run.CgroupPeakKB {
					run.CgroupPeakKB = kb
				}
			}
			select {
			case <-stopSample:
				return
			case <-t.C:
			}
		}
	}()

	msg, _ := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": s.Prompt},
	})
	_, _ = stdin.Write(append(msg, '\n'))

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var ev map[string]any
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		run.Events = append(run.Events, ev)
		switch ev["type"] {
		case "system":
			if ev["subtype"] == "init" && run.Init == nil {
				run.Init = ev
				run.ToInit = time.Since(start)
			}
		case "rate_limit_event":
			run.RateLimits = append(run.RateLimits, ev)
		case "assistant":
			if m, ok := ev["message"].(map[string]any); ok {
				for _, c := range asSlice(m["content"]) {
					b, _ := c.(map[string]any)
					switch b["type"] {
					case "tool_use":
						run.ToolUses = append(run.ToolUses, fmt.Sprint(b["name"]))
					case "text":
						run.AssistantText += fmt.Sprint(b["text"]) + "\n"
					}
				}
			}
		case "result":
			run.Result = ev
			run.ToResult = time.Since(start)
			_ = stdin.Close() // one prompt per agent in this spike
		}
		if s.OnEvent != nil && s.OnEvent(ev) {
			run.Aborted = true
			kill()
		}
	}
	err := cmd.Wait()
	run.ToExit = time.Since(start)
	close(stopSample)
	sampleWG.Wait()
	if err != nil {
		run.ExitErr = err.Error()
	}
	if ctx.Err() == context.DeadlineExceeded {
		run.Aborted = true
		run.ExitErr += " (timeout)"
	}
	run.Stderr = tail(stderr.String(), 2000)
	if d := os.Getenv("SPIKE_STDERR_DIR"); d != "" {
		_ = os.WriteFile(d+"/"+s.ID+".stderr", []byte(stderr.String()), 0o600)
	}
	time.Sleep(200 * time.Millisecond)
	run.LeftoverPIDs = groupPIDs(pgid)
	if run.ContainerName != "" {
		run.ContainerLeft = containerExists(run.ContainerName)
	}
	return run
}

func asSlice(v any) []any { s, _ := v.([]any); return s }

func tail(s string, n int) string {
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

// groupPIDs lists live processes whose process group is pgid.
func groupPIDs(pgid int) []int {
	ents, _ := os.ReadDir("/proc")
	var out []int
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// fields after the ")" of comm: state ppid pgrp ...
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) > 2 && f[0] != "Z" && f[2] == strconv.Itoa(pgid) {
			out = append(out, pid)
		}
	}
	return out
}

func groupRSSKB(pgid int) int64 {
	var total int64
	for _, pid := range groupPIDs(pgid) {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				f := strings.Fields(line)
				if len(f) >= 2 {
					kb, _ := strconv.ParseInt(f[1], 10, 64)
					total += kb
				}
			}
		}
	}
	return total
}
