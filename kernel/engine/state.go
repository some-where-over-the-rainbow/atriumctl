package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// WaitError: the node cannot proceed before Until (e.g. the subscription
// window). The run pauses; a later Run after Until re-runs the node.
type WaitError struct {
	Until  time.Time
	Reason string
}

func (e *WaitError) Error() string {
	return fmt.Sprintf("wait until %s: %s", e.Until.Format(time.RFC3339), e.Reason)
}

// ExhaustedError: the node hit a cap (turns, time). Partial is what it got
// done. The run pauses for the architect: Decide accept, retry or abort.
// A node's own Timeout is turned into an ExhaustedError with reason "timeout".
type ExhaustedError struct {
	Partial any
	Reason  string
}

func (e *ExhaustedError) Error() string { return "exhausted: " + e.Reason }

var (
	ErrGraphChanged = errors.New("engine: journal was written for a different graph")
	ErrNotWaiting   = errors.New("engine: node is not waiting for this")
)

// state is the run as replayed from the journal.
type state struct {
	g         *Graph
	hash      string
	input     json.RawMessage
	hasInput  bool
	attempt   map[string]int
	done      map[string]json.RawMessage
	items     map[string]map[int]json.RawMessage
	itemx     map[string]map[int]json.RawMessage
	failed    map[string]string
	gateIn    map[string]json.RawMessage
	answer    map[string]json.RawMessage
	wait      map[string]Record
	exhausted map[string]Record
	decision  map[string]string
	feedback  map[string]json.RawMessage
	backCount map[string]int
	aborted   bool
}

func newState(g *Graph) *state {
	return &state{
		g: g, attempt: map[string]int{}, done: map[string]json.RawMessage{},
		items: map[string]map[int]json.RawMessage{}, itemx: map[string]map[int]json.RawMessage{},
		failed: map[string]string{}, gateIn: map[string]json.RawMessage{}, answer: map[string]json.RawMessage{},
		wait: map[string]Record{}, exhausted: map[string]Record{}, decision: map[string]string{},
		feedback: map[string]json.RawMessage{}, backCount: map[string]int{},
	}
}

func (s *state) att(node string) int { return s.attempt[node] + 1 }

func backKey(from, to string) string { return from + ">" + to }

// clearNode forgets everything about a node's current attempt.
func (s *state) clearNode(n string) {
	delete(s.done, n)
	delete(s.items, n)
	delete(s.itemx, n)
	delete(s.failed, n)
	delete(s.gateIn, n)
	delete(s.answer, n)
	delete(s.wait, n)
	delete(s.exhausted, n)
	delete(s.decision, n)
	delete(s.feedback, n)
}

func (s *state) apply(r Record) {
	switch r.Kind {
	case "header":
		s.hash = r.Hash
		return
	case "input":
		s.input, s.hasInput = r.Out, true
		return
	case "abort":
		s.aborted = true
		return
	case "back":
		s.backCount[backKey(r.Node, r.To)] = r.Count
		for n := range s.g.region(r.To) {
			s.clearNode(n)
			s.attempt[n]++
		}
		s.feedback[r.To] = r.Out
		return
	}
	if r.Attempt != s.att(r.Node) {
		return // belongs to an attempt that a back-edge superseded
	}
	n := r.Node
	switch r.Kind {
	case "done":
		s.done[n] = r.Out
		delete(s.failed, n)
		delete(s.wait, n)
		delete(s.exhausted, n)
		delete(s.decision, n)
	case "item":
		if s.items[n] == nil {
			s.items[n] = map[int]json.RawMessage{}
		}
		s.items[n][r.Item] = r.Out
	case "itemx":
		if s.itemx[n] == nil {
			s.itemx[n] = map[int]json.RawMessage{}
		}
		s.itemx[n][r.Item] = r.Out
	case "failed":
		s.failed[n] = r.Err
	case "gate":
		s.gateIn[n] = r.Out
	case "answer":
		s.answer[n] = r.Out
	case "wait":
		s.wait[n] = r
		delete(s.failed, n)
	case "exhausted":
		s.exhausted[n] = r
		delete(s.decision, n)
		delete(s.failed, n)
	case "decide":
		switch r.Reason {
		case "retry":
			delete(s.exhausted, n)
			delete(s.itemx, n)
		case "accept":
			s.decision[n] = "accept"
		case "abort":
			s.aborted = true
		}
	}
}

func (s *state) predsDone(n string) bool {
	for _, p := range s.g.preds[n] {
		if _, ok := s.done[p]; !ok {
			return false
		}
	}
	return true
}

func (s *state) inputFor(n string) Input {
	in := Input{Run: s.input, From: map[string]json.RawMessage{}, Feedback: s.feedback[n], Attempt: s.att(n)}
	for _, p := range s.g.preds[n] {
		in.From[p] = s.done[p]
	}
	return in
}
