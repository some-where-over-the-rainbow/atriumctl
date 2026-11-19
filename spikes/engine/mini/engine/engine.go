// Package engine is a minimal graph engine for the spike: a chain of
// nodes, where a node is a function, a gate (pause for a human answer) or a
// map (fan-out over a list with a concurrency cap, joined before the next
// node). State is an append-only journal file, synced after every record,
// so a run continues after a crash at node or item granularity.
package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

type Kind int

const (
	Func Kind = iota
	Gate
	Map
)

type Fn func(ctx context.Context, in json.RawMessage) (any, error)

type Node struct {
	Name    string
	Kind    Kind
	Fn      Fn            // Func: the body; Map: the per-item body
	Cap     int           // Map: max items in parallel (0 = unlimited)
	Timeout time.Duration // per activation (per item for Map)
}

type Hooks struct {
	Start func(node, item string)
	End   func(node, item string, err error)
}

// ErrPaused is returned by Run when a gate waits for an answer.
var ErrPaused = errors.New("engine: paused at gate")

type rec struct {
	Kind string          `json:"k"` // done, item, wait, answer
	Node string          `json:"n"`
	Item int             `json:"i,omitempty"`
	Out  json.RawMessage `json:"o,omitempty"`
}

type Engine struct {
	Nodes []Node
	Hooks Hooks
	Path  string // journal file

	mu   sync.Mutex
	f    *os.File
	done map[string]json.RawMessage         // node -> output
	item map[string]map[int]json.RawMessage // map node -> item -> output
	wait map[string]bool
	ans  map[string]json.RawMessage
}

func (e *Engine) load() error {
	e.done, e.item = map[string]json.RawMessage{}, map[string]map[int]json.RawMessage{}
	e.wait, e.ans = map[string]bool{}, map[string]json.RawMessage{}
	if f, err := os.Open(e.Path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			var r rec
			if json.Unmarshal(sc.Bytes(), &r) != nil {
				continue // a torn last line after a crash
			}
			e.apply(r)
		}
		f.Close()
	}
	f, err := os.OpenFile(e.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	e.f = f
	return err
}

func (e *Engine) apply(r rec) {
	switch r.Kind {
	case "done":
		e.done[r.Node] = r.Out
	case "item":
		if e.item[r.Node] == nil {
			e.item[r.Node] = map[int]json.RawMessage{}
		}
		e.item[r.Node][r.Item] = r.Out
	case "wait":
		e.wait[r.Node] = true
	case "answer":
		e.ans[r.Node] = r.Out
	}
}

func (e *Engine) write(r rec) error {
	b, _ := json.Marshal(r)
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.f.Write(append(b, '\n')); err != nil {
		return err
	}
	if err := e.f.Sync(); err != nil {
		return err
	}
	e.apply(r)
	return nil
}

// Answer records a human answer for a gate; the next Run continues past it.
func (e *Engine) Answer(gate string, v any) error {
	if err := e.load(); err != nil {
		return err
	}
	defer e.f.Close()
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return e.write(rec{Kind: "answer", Node: gate, Out: b})
}

// Run starts or continues the run recorded in the journal.
func (e *Engine) Run(ctx context.Context, input any) (json.RawMessage, error) {
	if err := e.load(); err != nil {
		return nil, err
	}
	defer e.f.Close()
	cur, _ := json.Marshal(input)
	for _, n := range e.Nodes {
		if out, ok := e.done[n.Name]; ok {
			cur = out
			continue
		}
		var out json.RawMessage
		var err error
		switch n.Kind {
		case Func:
			out, err = e.call(ctx, n, "", cur)
		case Gate:
			if e.Hooks.Start != nil {
				e.Hooks.Start(n.Name, "")
			}
			a, ok := e.ans[n.Name]
			if !ok {
				if e.Hooks.End != nil {
					e.Hooks.End(n.Name, "", ErrPaused)
				}
				if !e.wait[n.Name] {
					if err := e.write(rec{Kind: "wait", Node: n.Name, Out: cur}); err != nil {
						return nil, err
					}
				}
				return nil, ErrPaused
			}
			out = a
			if e.Hooks.End != nil {
				e.Hooks.End(n.Name, "", nil)
			}
		case Map:
			out, err = e.fanout(ctx, n, cur)
		}
		if err != nil {
			return nil, fmt.Errorf("node %s: %w", n.Name, err)
		}
		if err := e.write(rec{Kind: "done", Node: n.Name, Out: out}); err != nil {
			return nil, err
		}
		cur = out
	}
	return cur, nil
}

// Waiting reports the gate a run is paused at, and the gate's input.
func (e *Engine) Waiting() (string, json.RawMessage) {
	for _, n := range e.Nodes {
		if _, ok := e.done[n.Name]; !ok && n.Kind == Gate && e.wait[n.Name] {
			return n.Name, nil
		}
	}
	return "", nil
}

func (e *Engine) call(ctx context.Context, n Node, item string, in json.RawMessage) (json.RawMessage, error) {
	if n.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, n.Timeout)
		defer cancel()
	}
	if e.Hooks.Start != nil {
		e.Hooks.Start(n.Name, item)
	}
	v, err := n.Fn(ctx, in)
	if e.Hooks.End != nil {
		e.Hooks.End(n.Name, item, err)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

func (e *Engine) fanout(ctx context.Context, n Node, in json.RawMessage) (json.RawMessage, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(in, &items); err != nil {
		return nil, fmt.Errorf("map input is not a list: %w", err)
	}
	outs := make([]json.RawMessage, len(items))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	limit := n.Cap
	if limit <= 0 {
		limit = len(items)
	}
	sem := make(chan struct{}, max(limit, 1))
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error
	for i, it := range items {
		if o, ok := e.item[n.Name][i]; ok { // finished before a crash
			outs[i] = o
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			var label string
			_ = json.Unmarshal(it, &label)
			o, err := e.call(ctx, n, label, it)
			if err == nil {
				err = e.write(rec{Kind: "item", Node: n.Name, Item: i, Out: o})
			}
			if err != nil {
				once.Do(func() { firstErr = err; cancel() })
				return
			}
			outs[i] = o
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return json.Marshal(outs)
}
