package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Event is what the Hook sees: for tracing and the console.
// Kinds: start, end, gate, answer, wait, exhausted, decide, back, fail, done.
type Event struct {
	Kind    string
	Node    string
	Attempt int
	Item    int // map item index, or -1
	Err     error
	At      time.Time
}

type Engine struct {
	G           *Graph
	J           Journal
	MaxParallel int         // run-wide cap on node bodies running at once (tasks and items); 0 = none
	Hook        func(Event) // optional; called from several goroutines
	Now         func() time.Time

	mu sync.Mutex // one Run, Answer or Decide at a time
}

func New(g *Graph, j Journal) (*Engine, error) {
	if err := g.Compile(); err != nil {
		return nil, err
	}
	return &Engine{G: g, J: j, Now: time.Now}, nil
}

func (e *Engine) hook(ev Event) {
	if e.Hook != nil {
		ev.At = e.Now()
		e.Hook(ev)
	}
}

func (e *Engine) load() (*state, error) {
	st, err := e.replay()
	if err != nil {
		return nil, err
	}
	switch {
	case st.hash == "" && !st.hasInput:
		if err := e.write(st, Record{Kind: "header", Hash: e.G.Hash()}); err != nil {
			return nil, err
		}
	case st.hash != e.G.Hash():
		return nil, ErrGraphChanged
	}
	return st, nil
}

func (e *Engine) write(st *state, r Record) error {
	r.At = e.Now()
	if err := e.J.Append(r); err != nil {
		return err
	}
	st.apply(r)
	return nil
}

type result struct {
	node    string
	attempt int
	out     json.RawMessage
	err     error
}

// Run starts the run, or continues it from the journal (input is used only
// the first time). It returns when nothing more can run: done, blocked on
// the architect or a wait, failed, aborted, or ctx ended.
func (e *Engine) Run(ctx context.Context, input any) (Status, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, err := e.load()
	if err != nil {
		return Status{}, err
	}
	if !st.hasInput {
		b, err := json.Marshal(input)
		if err != nil {
			return Status{}, err
		}
		if err := e.write(st, Record{Kind: "input", Out: b}); err != nil {
			return Status{}, err
		}
	}
	if st.aborted {
		return e.status(st), nil
	}
	st.failed = map[string]string{} // failures of an earlier Run are retried

	runCtx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()
	var sem chan struct{}
	if e.MaxParallel > 0 {
		sem = make(chan struct{}, e.MaxParallel)
	}
	running := map[string]context.CancelFunc{}
	results := make(chan result)
	waited := map[string]bool{}
	stopping := false

	for {
		if !stopping && ctx.Err() == nil {
			if err := e.schedule(runCtx, st, sem, running, waited, results); err != nil {
				stopping = true
				cancelAll()
				for len(running) > 0 {
					r := <-results
					delete(running, r.node)
				}
				return e.status(st), err
			}
		}
		if len(running) == 0 {
			break
		}
		r := <-results
		if c, ok := running[r.node]; ok {
			c()
			delete(running, r.node)
		}
		if ctx.Err() != nil {
			stopping = true // the caller stopped us: launch nothing more, record nothing
		}
		if stopping || r.attempt != st.att(r.node) {
			continue // shutting down, or superseded by a back-edge
		}
		var we *WaitError
		if errors.As(r.err, &we) {
			waited[r.node] = true
		}
		if err := e.finish(st, r, running); err != nil {
			return e.status(st), err
		}
		if _, failed := st.failed[r.node]; failed {
			stopping = true // fail fast: cancel the rest, record nothing for them
			cancelAll()
		}
	}
	// Map items were journaled by their goroutines: report from the journal.
	if fresh, err := e.replay(); err == nil {
		st = fresh
	}
	s := e.status(st)
	if ctx.Err() != nil && s.State != Done {
		s.State = Stopped
		return s, ctx.Err()
	}
	return s, nil
}

// schedule launches every node that can run, and settles gates and
// accepted exhaustions, until nothing changes.
func (e *Engine) schedule(ctx context.Context, st *state, sem chan struct{}, running map[string]context.CancelFunc, waited map[string]bool, results chan<- result) error {
	for changed := true; changed; {
		changed = false
		for _, name := range e.G.topo {
			if _, ok := running[name]; ok {
				continue
			}
			if _, ok := st.done[name]; ok || !st.predsDone(name) {
				continue
			}
			n := e.G.node(name)
			att := st.att(name)
			if x, ok := st.exhausted[name]; ok {
				if st.decision[name] != "accept" {
					continue // waiting for the architect
				}
				out := x.Out
				if n.Kind == Map {
					out = assemble(st, name)
				}
				if err := e.write(st, Record{Kind: "done", Node: name, Attempt: att, Out: out}); err != nil {
					return err
				}
				e.hook(Event{Kind: "done", Node: name, Attempt: att, Item: -1})
				changed = true
				continue
			}
			if n.Kind == Gate {
				if a, ok := st.answer[name]; ok {
					if err := e.write(st, Record{Kind: "done", Node: name, Attempt: att, Out: a}); err != nil {
						return err
					}
					e.hook(Event{Kind: "done", Node: name, Attempt: att, Item: -1})
					changed = true
				} else if _, open := st.gateIn[name]; !open {
					in, _ := json.Marshal(st.inputFor(name).From)
					if err := e.write(st, Record{Kind: "gate", Node: name, Attempt: att, Out: in}); err != nil {
						return err
					}
					e.hook(Event{Kind: "gate", Node: name, Attempt: att, Item: -1})
				}
				continue
			}
			if w, ok := st.wait[name]; ok && e.Now().Before(w.Until) {
				continue
			}
			if waited[name] {
				continue // a wait already returned in this Run; the caller runs again later
			}
			nctx, cancel := context.WithCancel(ctx)
			running[name] = cancel
			in := st.inputFor(name)
			go func() {
				out, err := e.exec(nctx, n, in, sem)
				results <- result{node: name, attempt: att, out: out, err: err}
			}()
		}
	}
	return nil
}

// finish records a node's outcome and takes back-edges.
func (e *Engine) finish(st *state, r result, running map[string]context.CancelFunc) error {
	att := r.attempt
	var we *WaitError
	var xe *ExhaustedError
	switch {
	case r.err == nil:
		if err := e.write(st, Record{Kind: "done", Node: r.node, Attempt: att, Out: r.out}); err != nil {
			return err
		}
		e.hook(Event{Kind: "done", Node: r.node, Attempt: att, Item: -1})
		return e.takeBackEdges(st, r.node, r.out, running)
	case errors.As(r.err, &we):
		e.hook(Event{Kind: "wait", Node: r.node, Attempt: att, Item: -1, Err: r.err})
		return e.write(st, Record{Kind: "wait", Node: r.node, Attempt: att, Until: we.Until, Reason: we.Reason})
	case errors.As(r.err, &xe):
		e.hook(Event{Kind: "exhausted", Node: r.node, Attempt: att, Item: -1, Err: r.err})
		out := r.out
		if out == nil {
			out, _ = json.Marshal(xe.Partial)
		}
		return e.write(st, Record{Kind: "exhausted", Node: r.node, Attempt: att, Out: out, Reason: xe.Reason})
	default:
		e.hook(Event{Kind: "fail", Node: r.node, Attempt: att, Item: -1, Err: r.err})
		return e.write(st, Record{Kind: "failed", Node: r.node, Attempt: att, Err: r.err.Error()})
	}
}

func (e *Engine) takeBackEdges(st *state, from string, out json.RawMessage, running map[string]context.CancelFunc) error {
	for _, bi := range e.G.back {
		be := e.G.Edges[bi]
		if be.From != from {
			continue
		}
		k := backKey(be.From, be.To)
		if st.backCount[k] >= be.Max || !be.When(out) {
			continue
		}
		for n := range e.G.region(be.To) {
			if c, ok := running[n]; ok {
				c() // its result will carry a stale attempt and be ignored
			}
		}
		if err := e.write(st, Record{Kind: "back", Node: be.From, To: be.To, Count: st.backCount[k] + 1, Out: out}); err != nil {
			return err
		}
		e.hook(Event{Kind: "back", Node: be.To, Attempt: st.att(be.To), Item: -1})
		return nil // one back-edge per completion
	}
	return nil
}

// exec runs one activation of a task or map node.
func (e *Engine) exec(ctx context.Context, n Node, in Input, sem chan struct{}) (json.RawMessage, error) {
	if n.Kind == Task {
		in.Index = -1
		return e.call(ctx, n, in, sem)
	}
	// Items finished in an earlier activation (before a crash, a wait or a
	// retry) are not run again.
	prev, err := e.replay()
	if err != nil {
		return nil, err
	}
	if prev.att(n.Name) != in.Attempt {
		return nil, context.Canceled
	}
	prevItems, prevItemx := prev.items[n.Name], prev.itemx[n.Name]
	var items []json.RawMessage
	for _, v := range in.From {
		if err := json.Unmarshal(v, &items); err != nil {
			return nil, fmt.Errorf("map %s: input is not a JSON array: %w", n.Name, err)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var capSem chan struct{}
	if n.Cap > 0 {
		capSem = make(chan struct{}, n.Cap)
	}
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		until    time.Time
		reason   string
		anyWait  bool
		anyX     bool
	)
	for i, it := range items {
		if _, ok := prevItems[i]; ok {
			continue
		}
		if _, ok := prevItemx[i]; ok {
			anyX = true
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !acquire(ctx, capSem) {
				return
			}
			defer release(capSem)
			iin := in
			iin.Item, iin.Index = it, i
			out, err := e.call(ctx, n, iin, sem)
			var we *WaitError
			var xe *ExhaustedError
			rec := Record{Node: n.Name, Attempt: in.Attempt, Item: i, At: e.Now()}
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				rec.Kind, rec.Out = "item", out
			case errors.As(err, &we):
				anyWait = true
				if we.Until.After(until) {
					until, reason = we.Until, we.Reason
				}
				return
			case errors.As(err, &xe):
				anyX = true
				rec.Kind, rec.Out = "itemx", out
				if out == nil {
					rec.Out, _ = json.Marshal(xe.Partial)
				}
			default:
				if firstErr == nil && ctx.Err() == nil {
					firstErr = fmt.Errorf("item %d: %w", i, err)
					cancel()
				}
				return
			}
			if werr := e.J.Append(rec); werr != nil && firstErr == nil {
				firstErr = werr
				cancel()
			}
		}()
	}
	wg.Wait()
	switch {
	case firstErr != nil:
		return nil, firstErr
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case anyWait:
		return nil, &WaitError{Until: until, Reason: reason}
	case anyX:
		return nil, &ExhaustedError{Reason: "items exhausted"}
	}
	// All items done: read them back from the journal's view.
	return e.collect(n.Name, in.Attempt, len(items))
}

// collect assembles a map node's output from the journal.
func (e *Engine) collect(name string, att, count int) (json.RawMessage, error) {
	st, err := e.replay()
	if err != nil {
		return nil, err
	}
	if st.att(name) != att {
		return nil, context.Canceled // superseded meanwhile
	}
	outs := make([]json.RawMessage, count)
	for i := range outs {
		o, ok := st.items[name][i]
		if !ok {
			return nil, fmt.Errorf("map %s: item %d missing", name, i)
		}
		outs[i] = o
	}
	return json.Marshal(outs)
}

func assemble(st *state, name string) json.RawMessage {
	idx := map[int]json.RawMessage{}
	for i, o := range st.items[name] {
		idx[i] = o
	}
	for i, o := range st.itemx[name] {
		idx[i] = o
	}
	keys := make([]int, 0, len(idx))
	for i := range idx {
		keys = append(keys, i)
	}
	sort.Ints(keys)
	outs := make([]json.RawMessage, 0, len(keys))
	for _, i := range keys {
		outs = append(outs, idx[i])
	}
	b, _ := json.Marshal(outs)
	return b
}

// call runs one node body under the run-wide cap and the node's timeout.
func (e *Engine) call(ctx context.Context, n Node, in Input, sem chan struct{}) (json.RawMessage, error) {
	if !acquire(ctx, sem) {
		return nil, ctx.Err()
	}
	defer release(sem)
	fctx := ctx
	if n.Timeout > 0 {
		var cancel context.CancelFunc
		fctx, cancel = context.WithTimeout(ctx, n.Timeout)
		defer cancel()
	}
	e.hook(Event{Kind: "start", Node: n.Name, Attempt: in.Attempt, Item: in.Index})
	v, err := n.Fn(fctx, in)
	var xe *ExhaustedError
	if err != nil && fctx.Err() == context.DeadlineExceeded && ctx.Err() == nil && !errors.As(err, &xe) {
		err = &ExhaustedError{Partial: v, Reason: "timeout"}
	}
	e.hook(Event{Kind: "end", Node: n.Name, Attempt: in.Attempt, Item: in.Index, Err: err})
	if err != nil {
		if errors.As(err, &xe) && v != nil {
			b, _ := json.Marshal(v)
			return b, err
		}
		return nil, err
	}
	return json.Marshal(v)
}

func acquire(ctx context.Context, sem chan struct{}) bool {
	if sem == nil {
		return ctx.Err() == nil
	}
	select {
	case sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func release(sem chan struct{}) {
	if sem != nil {
		<-sem
	}
}

// replay reads the journal into a fresh state, without writing anything.
func (e *Engine) replay() (*state, error) {
	recs, err := e.J.Load()
	if err != nil {
		return nil, err
	}
	st := newState(e.G)
	for _, r := range recs {
		st.apply(r)
	}
	return st, nil
}

// Answer records the architect's answer at an open gate.
func (e *Engine) Answer(gate string, v any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, err := e.load()
	if err != nil {
		return err
	}
	if _, open := st.gateIn[gate]; !open {
		return fmt.Errorf("%w: gate %q is not open", ErrNotWaiting, gate)
	}
	if _, answered := st.answer[gate]; answered {
		return fmt.Errorf("%w: gate %q is already answered", ErrNotWaiting, gate)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	e.hook(Event{Kind: "answer", Node: gate, Attempt: st.att(gate), Item: -1})
	return e.write(st, Record{Kind: "answer", Node: gate, Attempt: st.att(gate), Out: b})
}

type Decision string

const (
	Accept Decision = "accept" // take the partial output and go on
	Retry  Decision = "retry"  // run the node (or its exhausted items) again
	Abort  Decision = "abort"  // end the run
)

// Decide records the architect's decision on an exhausted node.
func (e *Engine) Decide(node string, d Decision) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, err := e.load()
	if err != nil {
		return err
	}
	if _, ok := st.exhausted[node]; !ok {
		return fmt.Errorf("%w: node %q is not exhausted", ErrNotWaiting, node)
	}
	switch d {
	case Accept, Retry, Abort:
	default:
		return fmt.Errorf("unknown decision %q", d)
	}
	e.hook(Event{Kind: "decide", Node: node, Attempt: st.att(node), Item: -1})
	return e.write(st, Record{Kind: "decide", Node: node, Attempt: st.att(node), Reason: string(d)})
}

// Status reads the run's state from the journal without running anything.
func (e *Engine) Status() (Status, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, err := e.load()
	if err != nil {
		return Status{}, err
	}
	return e.status(st), nil
}
