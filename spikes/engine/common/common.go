// Package common holds the engine-neutral parts of the engine spike: the
// trace log every candidate writes, the child-process helper, and the
// scenario's node bodies.
//
// Candidate binaries implement three subcommands:
//
//	run     -db D -log L -width N [-crash]  question -> gate; stop at the pause (-crash: SIGKILL self there)
//	resume  -db D -log L -approve q1,q3     answer the gate; workers (cap 3) -> join -> synthesize
//	timeout -log L                          one node with a 1 s timeout that starts `sleep 30`
package common

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type Rec struct {
	T     int64  `json:"t"` // unix nanoseconds
	PID   int    `json:"pid"`
	Ev    string `json:"ev"` // start, end, paused, done, child
	Node  string `json:"node,omitempty"`
	Item  string `json:"item,omitempty"`
	Err   string `json:"err,omitempty"`
	Conc  int64  `json:"conc,omitempty"`
	Data  any    `json:"data,omitempty"`
	Child int    `json:"child,omitempty"`
}

// Log is an append-only JSON-lines file, synced per record so a SIGKILL
// loses nothing already written.
type Log struct {
	mu sync.Mutex
	f  *os.File
}

func OpenLog(path string) *Log {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		panic(err)
	}
	return &Log{f: f}
}

func (l *Log) Write(r Rec) {
	r.T, r.PID = time.Now().UnixNano(), os.Getpid()
	b, _ := json.Marshal(r)
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.f.Write(append(b, '\n'))
	_ = l.f.Sync()
}

func ReadLog(path string) []Rec {
	b, _ := os.ReadFile(path)
	var out []Rec
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var r Rec
		if json.Unmarshal([]byte(line), &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

// Traced wraps a node body with start and end records: the hook the
// kernel needs for tracing and the console, independent of the engine.
func Traced[IN, OUT any](l *Log, node string, fn func(context.Context, IN) (OUT, error)) func(context.Context, IN) (OUT, error) {
	return func(ctx context.Context, in IN) (OUT, error) {
		item := ""
		if s, ok := any(in).(string); ok {
			item = s
		}
		l.Write(Rec{Ev: "start", Node: node, Item: item})
		out, err := fn(ctx, in)
		r := Rec{Ev: "end", Node: node, Item: item}
		if err != nil {
			r.Err = err.Error()
		}
		l.Write(r)
		return out, err
	}
}

// RunChild runs a command in its own process group and kills the whole
// group when ctx ends: the way a node starts `claude -p` or a probe.
func RunChild(ctx context.Context, l *Log, node string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	l.Write(Rec{Ev: "child", Node: node, Child: cmd.Process.Pid})
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return ctx.Err()
	}
}

// Scenario node bodies, shared by all candidates.

func Questions(width int) []string {
	q := make([]string, width)
	for i := range q {
		q[i] = fmt.Sprintf("Q_%03d", i+1)
	}
	return q
}

var running, started atomic.Int64

// dieAt, when set, crashes the process when that worker starts: a kernel
// crash in the middle of a fan-out. With a cap of 3, worker 5 starts only
// after two others have finished.
var dieAt, _ = strconv.Atoi(os.Getenv("SPIKE_DIE_AT_WORKER"))

// Worker simulates a research worker: a child process that takes a while.
func Worker(l *Log) func(context.Context, string) (string, error) {
	return func(ctx context.Context, q string) (string, error) {
		if k := started.Add(1); dieAt > 0 && k >= int64(dieAt) {
			l.Write(Rec{Ev: "crash", Node: "worker", Item: q})
			Die()
		}
		n := running.Add(1)
		defer running.Add(-1)
		l.Write(Rec{Ev: "conc", Node: "worker", Item: q, Conc: n})
		if err := RunChild(ctx, l, "worker", "sleep", "0.4"); err != nil {
			return "", err
		}
		return "F_for_" + q, nil
	}
}

func Synthesize(facts []string) string {
	return fmt.Sprintf("%d facts: %s", len(facts), strings.Join(facts, ","))
}

// Approved turns the gate's reply into a list of question ids.
func Approved(reply any) []string {
	var out []string
	switch v := reply.(type) {
	case []any:
		for _, x := range v {
			out = append(out, fmt.Sprint(x))
		}
	case []string:
		out = v
	case string:
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// Die simulates a crash: SIGKILL, no deferred cleanup.
func Die() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	select {}
}
