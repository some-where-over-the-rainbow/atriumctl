package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// counter counts activations per node/item and tracks concurrency.
type counter struct {
	mu      sync.Mutex
	calls   map[string]int
	cur     int
	peak    int
	byNode  map[string]int
	peakBy  map[string]int
	starts  int
	ends    int
	unequal []string
}

func newCounter() *counter {
	return &counter{calls: map[string]int{}, byNode: map[string]int{}, peakBy: map[string]int{}}
}

func (c *counter) enter(key, node string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[key]++
	c.cur++
	c.byNode[node]++
	c.peak = max(c.peak, c.cur)
	c.peakBy[node] = max(c.peakBy[node], c.byNode[node])
}

func (c *counter) leave(node string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur--
	c.byNode[node]--
}

func (c *counter) n(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[key]
}

func (c *counter) hook(ev Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch ev.Kind {
	case "start":
		c.starts++
	case "end":
		c.ends++
	}
}

func sleepy(c *counter, node string, d time.Duration, out func(Input) any) Fn {
	return func(ctx context.Context, in Input) (any, error) {
		key := node
		if in.Index >= 0 {
			key = fmt.Sprintf("%s[%d]", node, in.Index)
		}
		c.enter(key, node)
		defer c.leave(node)
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return out(in), nil
	}
}

// slice1 is slice 1's graph shape with stand-in bodies:
//
//	probe ────────────────────────────────────────┐
//	question -> gate -> plan -> worker(map) -> synth
//	synth -> question  (back-edge, when synth says "more", at most once)
func slice1(c *counter, workerDelay time.Duration, synthMore *atomic.Bool) *Graph {
	return &Graph{
		Nodes: []Node{
			{Name: "probe", Fn: sleepy(c, "probe", 30*time.Millisecond, func(Input) any { return "baseline" })},
			{Name: "question", Fn: sleepy(c, "question", time.Millisecond, func(in Input) any {
				if in.Feedback != nil {
					return []string{"Q_001", "Q_002", "Q_003", "Q_004"}
				}
				return []string{"Q_001", "Q_002", "Q_003"}
			})},
			{Name: "gate", Kind: Gate},
			{Name: "plan", Fn: sleepy(c, "plan", time.Millisecond, func(in Input) any {
				var approved []string
				_ = json.Unmarshal(in.From["gate"], &approved)
				return approved
			})},
			{Name: "worker", Kind: Map, Cap: 2, Fn: sleepy(c, "worker", workerDelay, func(in Input) any {
				var q string
				_ = json.Unmarshal(in.Item, &q)
				return "F_" + q
			})},
			{Name: "synth", Fn: sleepy(c, "synth", time.Millisecond, func(in Input) any {
				var facts []string
				_ = json.Unmarshal(in.From["worker"], &facts)
				more := synthMore != nil && synthMore.Load() && in.Attempt == 1
				return map[string]any{"facts": facts, "more": more}
			})},
		},
		Edges: []Edge{
			{From: "question", To: "gate"},
			{From: "gate", To: "plan"},
			{From: "plan", To: "worker"},
			{From: "worker", To: "synth"},
			{From: "synth", To: "question", Back: true, Max: 1, When: func(out json.RawMessage) bool {
				var v struct{ More bool }
				_ = json.Unmarshal(out, &v)
				return v.More
			}},
		},
	}
}

func newEngine(t *testing.T, g *Graph, j Journal, c *counter) *Engine {
	t.Helper()
	e, err := New(g, j)
	if err != nil {
		t.Fatal(err)
	}
	e.Hook = c.hook
	return e
}

func mustRun(t *testing.T, e *Engine, want RunState) Status {
	t.Helper()
	s, err := e.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if s.State != want {
		t.Fatalf("state = %s, want %s (status %+v)", s.State, want, s)
	}
	return s
}

func TestSlice1HappyPathWithRestartAtGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.journal")
	c := newCounter()

	e := newEngine(t, slice1(c, 20*time.Millisecond, nil), &FileJournal{Path: path}, c)
	s := mustRun(t, e, Blocked)
	if len(s.Gates) != 1 || s.Gates[0].Node != "gate" || !strings.Contains(string(s.Gates[0].Input), "Q_003") {
		t.Fatalf("gate not open with the questions: %+v", s.Gates)
	}
	e.J.(*FileJournal).Close()

	// "Restart": a new engine on the same file answers and continues.
	e2 := newEngine(t, slice1(c, 20*time.Millisecond, nil), &FileJournal{Path: path}, c)
	if err := e2.Answer("gate", []string{"Q_001", "Q_003"}); err != nil {
		t.Fatal(err)
	}
	s = mustRun(t, e2, Done)
	if got := string(s.Output["synth"]); !strings.Contains(got, `"facts":["F_Q_001","F_Q_003"]`) {
		t.Fatalf("synth output = %s", got)
	}
	if string(s.Output["probe"]) != `"baseline"` {
		t.Fatalf("probe output = %s", s.Output["probe"])
	}
	for _, k := range []string{"probe", "question", "plan", "synth", "worker[0]", "worker[1]"} {
		if c.n(k) != 1 {
			t.Errorf("%s ran %d times, want 1", k, c.n(k))
		}
	}
	if c.starts != c.ends {
		t.Errorf("hook starts %d != ends %d", c.starts, c.ends)
	}
}

func TestFanOutCapsAndRunWideCap(t *testing.T) {
	c := newCounter()
	g := slice1(c, 40*time.Millisecond, nil)
	g.Nodes[0].Fn = sleepy(c, "probe", 200*time.Millisecond, func(Input) any { return "baseline" })
	e := newEngine(t, g, &MemJournal{}, c)
	e.MaxParallel = 2
	mustRun(t, e, Blocked)
	ids := []string{"Q_001", "Q_002", "Q_003", "Q_004", "Q_005", "Q_006", "Q_007"}
	_ = e.Answer("gate", ids)
	mustRun(t, e, Done)
	if c.peakBy["worker"] > 2 {
		t.Errorf("map cap 2 exceeded: %d", c.peakBy["worker"])
	}
	if c.peak > 2 {
		t.Errorf("run-wide cap 2 exceeded: %d", c.peak)
	}
	for i := range ids {
		if n := c.n(fmt.Sprintf("worker[%d]", i)); n != 1 {
			t.Errorf("worker[%d] ran %d times", i, n)
		}
	}
}

func TestStopMidFanOutThenContinue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.journal")
	c := newCounter()
	e := newEngine(t, slice1(c, 50*time.Millisecond, nil), &FileJournal{Path: path}, c)
	mustRun(t, e, Blocked)
	ids := []string{"Q_001", "Q_002", "Q_003", "Q_004", "Q_005", "Q_006"}
	_ = e.Answer("gate", ids)

	// Stop the kernel once two items have finished.
	ctx, cancel := context.WithCancel(context.Background())
	var ended atomic.Int32
	e.Hook = func(ev Event) {
		c.hook(ev)
		if ev.Kind == "end" && ev.Node == "worker" && ev.Err == nil && ended.Add(1) == 2 {
			go func() { time.Sleep(5 * time.Millisecond); cancel() }()
		}
	}
	s, err := e.Run(ctx, nil)
	if !errors.Is(err, context.Canceled) || s.State != Stopped {
		t.Fatalf("want stopped, got %s, %v", s.State, err)
	}
	e.J.(*FileJournal).Close()

	// A torn last line, as after a crash during a write.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"k":"item","n":"wor`)
	f.Close()

	before := map[string]int{}
	for i := range ids {
		before[fmt.Sprintf("worker[%d]", i)] = c.n(fmt.Sprintf("worker[%d]", i))
	}
	e2 := newEngine(t, slice1(c, 50*time.Millisecond, nil), &FileJournal{Path: path}, c)
	s = mustRun(t, e2, Done)
	rerun := 0
	for k, b := range before {
		if b > 0 && c.n(k) > b {
			rerun++
		}
		if c.n(k) == 0 {
			t.Errorf("%s never ran", k)
		}
	}
	// At least the two finished items must not run again; items that were
	// in flight may (at-least-once).
	if rerun > len(ids)-2 {
		t.Errorf("%d items re-ran; finished items should be skipped", rerun)
	}
	if !strings.Contains(string(s.Output["synth"]), "F_Q_006") {
		t.Errorf("output: %s", s.Output["synth"])
	}
}

func TestBackEdgeReentersRegionWithFeedback(t *testing.T) {
	c := newCounter()
	more := &atomic.Bool{}
	more.Store(true)
	e := newEngine(t, slice1(c, time.Millisecond, more), &MemJournal{}, c)
	mustRun(t, e, Blocked)
	_ = e.Answer("gate", []string{"Q_001"})

	// synth says "more": the back-edge re-enters question; the gate asks again.
	s := mustRun(t, e, Blocked)
	if s.Attempts["question"] != 2 || s.Attempts["gate"] != 2 || s.Attempts["synth"] != 2 || s.Attempts["probe"] != 1 {
		t.Fatalf("attempts: %v", s.Attempts)
	}
	if !strings.Contains(string(s.Gates[0].Input), "Q_004") {
		t.Fatalf("question did not get feedback: %s", s.Gates[0].Input)
	}
	_ = e.Answer("gate", []string{"Q_001", "Q_004"})
	s = mustRun(t, e, Done) // Max 1: no second back-edge
	if c.n("probe") != 1 || c.n("question") != 2 || c.n("synth") != 2 {
		t.Fatalf("runs: probe %d question %d synth %d", c.n("probe"), c.n("question"), c.n("synth"))
	}
	if !strings.Contains(string(s.Output["synth"]), "F_Q_004") {
		t.Fatalf("output: %s", s.Output["synth"])
	}
}

func TestWaitThenResumeAfterWindow(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	reset := now.Add(3 * time.Hour)
	var calls atomic.Int32
	g := &Graph{Nodes: []Node{{Name: "agent", Fn: func(ctx context.Context, in Input) (any, error) {
		if calls.Add(1) == 1 {
			return nil, &WaitError{Until: reset, Reason: "five_hour window"}
		}
		return "ok", nil
	}}}}
	c := newCounter()
	e := newEngine(t, g, &MemJournal{}, c)
	e.Now = func() time.Time { return now }
	s := mustRun(t, e, Waiting)
	if !s.NextWake.Equal(reset) || s.Waits[0].Reason != "five_hour window" {
		t.Fatalf("wait: %+v", s)
	}
	mustRun(t, e, Waiting) // still before the reset: nothing runs
	if calls.Load() != 1 {
		t.Fatalf("ran during the wait")
	}
	now = reset.Add(time.Second)
	mustRun(t, e, Done)
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestTimeoutIsExhaustedThenDecisions(t *testing.T) {
	var calls atomic.Int32
	mk := func() *Graph {
		return &Graph{Nodes: []Node{{Name: "agent", Timeout: 30 * time.Millisecond, Fn: func(ctx context.Context, in Input) (any, error) {
			n := calls.Add(1)
			if n >= 3 {
				return "full", nil
			}
			<-ctx.Done()
			return "partial", ctx.Err()
		}}}}
	}
	for _, tc := range []struct {
		d     Decision
		state RunState
		out   string
	}{
		{Accept, Done, `"partial"`},
		{Abort, Aborted, ""},
	} {
		calls.Store(0)
		c := newCounter()
		e := newEngine(t, mk(), &MemJournal{}, c)
		s := mustRun(t, e, Blocked)
		if len(s.Exhausted) != 1 || s.Exhausted[0].Reason != "timeout" || string(s.Exhausted[0].Partial) != `"partial"` {
			t.Fatalf("exhausted: %+v", s.Exhausted)
		}
		if err := e.Decide("agent", tc.d); err != nil {
			t.Fatal(err)
		}
		s = mustRun(t, e, tc.state)
		if tc.out != "" && string(s.Output["agent"]) != tc.out {
			t.Fatalf("%s: output %s", tc.d, s.Output["agent"])
		}
	}
	// Retry runs the node again.
	calls.Store(1)
	c := newCounter()
	e := newEngine(t, mk(), &MemJournal{}, c)
	mustRun(t, e, Blocked)
	_ = e.Decide("agent", Retry)
	s := mustRun(t, e, Done)
	if string(s.Output["agent"]) != `"full"` {
		t.Fatalf("retry output %s", s.Output["agent"])
	}
}

func TestMapItemExhaustedRetryRerunsOnlyThoseItems(t *testing.T) {
	c := newCounter()
	var failOnce sync.Map
	g := &Graph{
		Nodes: []Node{
			{Name: "list", Fn: func(context.Context, Input) (any, error) { return []int{0, 1, 2, 3}, nil }},
			{Name: "work", Kind: Map, Fn: func(ctx context.Context, in Input) (any, error) {
				c.enter(fmt.Sprintf("work[%d]", in.Index), "work")
				defer c.leave("work")
				if in.Index%2 == 1 {
					if _, seen := failOnce.LoadOrStore(in.Index, true); !seen {
						return fmt.Sprintf("half-%d", in.Index), &ExhaustedError{Reason: "turns"}
					}
				}
				return fmt.Sprintf("full-%d", in.Index), nil
			}},
		},
		Edges: []Edge{{From: "list", To: "work"}},
	}
	e := newEngine(t, g, &MemJournal{}, c)
	s := mustRun(t, e, Blocked)
	if len(s.Exhausted) != 1 || string(s.Exhausted[0].Partial) != `["full-0","half-1","full-2","half-3"]` {
		t.Fatalf("exhausted: %+v", s.Exhausted)
	}
	_ = e.Decide("work", Retry)
	s = mustRun(t, e, Done)
	if string(s.Output["work"]) != `["full-0","full-1","full-2","full-3"]` {
		t.Fatalf("output %s", s.Output["work"])
	}
	if c.n("work[0]") != 1 || c.n("work[2]") != 1 || c.n("work[1]") != 2 || c.n("work[3]") != 2 {
		t.Fatalf("calls %v", c.calls)
	}
}

func TestFailureFailsFastAndRetries(t *testing.T) {
	var flaky atomic.Int32
	c := newCounter()
	g := &Graph{Nodes: []Node{
		{Name: "slow", Fn: sleepy(c, "slow", 500*time.Millisecond, func(Input) any { return 1 })},
		{Name: "flaky", Fn: func(context.Context, Input) (any, error) {
			if flaky.Add(1) == 1 {
				return nil, errors.New("boom")
			}
			return 2, nil
		}},
	}}
	e := newEngine(t, g, &MemJournal{}, c)
	t0 := time.Now()
	s, err := e.Run(context.Background(), nil)
	if err != nil || s.State != Failed || s.Failed[0].Node != "flaky" || s.Failed[0].Err != "boom" {
		t.Fatalf("got %s %+v %v", s.State, s.Failed, err)
	}
	if time.Since(t0) > 300*time.Millisecond {
		t.Fatalf("did not fail fast")
	}
	if len(s.Failed) != 1 {
		t.Fatalf("the cancelled sibling must not be recorded as failed: %+v", s.Failed)
	}
	mustRun(t, e, Done)
}

func TestGraphChangeIsRefused(t *testing.T) {
	j := &MemJournal{}
	c := newCounter()
	e := newEngine(t, slice1(c, time.Millisecond, nil), j, c)
	mustRun(t, e, Blocked)
	g := slice1(c, time.Millisecond, nil)
	g.Nodes[4].Cap = 5
	e2 := newEngine(t, g, j, c)
	if _, err := e2.Run(context.Background(), nil); !errors.Is(err, ErrGraphChanged) {
		t.Fatalf("err = %v", err)
	}
}

func TestAnswerAndDecideGuards(t *testing.T) {
	c := newCounter()
	e := newEngine(t, slice1(c, time.Millisecond, nil), &MemJournal{}, c)
	if err := e.Answer("gate", nil); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("answer before open: %v", err)
	}
	mustRun(t, e, Blocked)
	if err := e.Answer("gate", []string{"Q_001"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Answer("gate", []string{"Q_002"}); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("double answer: %v", err)
	}
	if err := e.Decide("plan", Accept); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("decide on a node that is not exhausted: %v", err)
	}
}

func TestCompileRejectsBadGraphs(t *testing.T) {
	fn := func(context.Context, Input) (any, error) { return nil, nil }
	yes := func(json.RawMessage) bool { return true }
	cases := map[string]*Graph{
		"cycle": {Nodes: []Node{{Name: "a", Fn: fn}, {Name: "b", Fn: fn}},
			Edges: []Edge{{From: "a", To: "b"}, {From: "b", To: "a"}}},
		"back not downstream": {Nodes: []Node{{Name: "a", Fn: fn}, {Name: "b", Fn: fn}},
			Edges: []Edge{{From: "a", To: "b", Back: true, Max: 1, When: yes}}},
		"map two preds": {Nodes: []Node{{Name: "a", Fn: fn}, {Name: "b", Fn: fn}, {Name: "m", Kind: Map, Fn: fn}},
			Edges: []Edge{{From: "a", To: "m"}, {From: "b", To: "m"}}},
		"duplicate": {Nodes: []Node{{Name: "a", Fn: fn}, {Name: "a", Fn: fn}}},
		"no fn":     {Nodes: []Node{{Name: "a"}}},
		"back no max": {Nodes: []Node{{Name: "a", Fn: fn}, {Name: "b", Fn: fn}},
			Edges: []Edge{{From: "a", To: "b"}, {From: "b", To: "a", Back: true, When: yes}}},
	}
	names := make([]string, 0, len(cases))
	for n := range cases {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := cases[n].Compile(); err == nil {
			t.Errorf("%s: accepted", n)
		}
	}
}
