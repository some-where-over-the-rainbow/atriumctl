package engine

import (
	"encoding/json"
	"time"
)

type RunState string

const (
	Done    RunState = "done"
	Blocked RunState = "blocked" // a gate or an exhausted node needs the architect
	Waiting RunState = "waiting" // only waits remain; run again after NextWake
	Failed  RunState = "failed"  // a node failed; Run again retries it
	Aborted RunState = "aborted"
	Stopped RunState = "stopped" // the caller's context ended
	Pending RunState = "pending" // not started, or more to run
)

type GateInfo struct {
	Node    string
	Attempt int
	Input   json.RawMessage // outputs of the gate's predecessors
}

type ExhaustedInfo struct {
	Node    string
	Attempt int
	Reason  string
	Partial json.RawMessage
}

type WaitInfo struct {
	Node   string
	Until  time.Time
	Reason string
}

type FailInfo struct {
	Node string
	Err  string
}

type Status struct {
	State     RunState
	Gates     []GateInfo
	Exhausted []ExhaustedInfo
	Waits     []WaitInfo
	Failed    []FailInfo
	Done      []string                   // nodes finished in their current attempt, in graph order
	NextWake  time.Time                  // earliest wait end
	Attempts  map[string]int             // current attempt per node
	Output    map[string]json.RawMessage // sink outputs, when done
}

func (e *Engine) status(st *state) Status {
	s := Status{Attempts: map[string]int{}}
	allDone := true
	for _, name := range e.G.topo {
		s.Attempts[name] = st.att(name)
		if _, ok := st.done[name]; ok {
			s.Done = append(s.Done, name)
			continue
		}
		allDone = false
		if in, open := st.gateIn[name]; open {
			if _, answered := st.answer[name]; !answered {
				s.Gates = append(s.Gates, GateInfo{Node: name, Attempt: st.att(name), Input: in})
			}
		}
		if x, ok := st.exhausted[name]; ok && st.decision[name] != "accept" {
			p := x.Out
			if e.G.node(name).Kind == Map {
				p = assemble(st, name)
			}
			s.Exhausted = append(s.Exhausted, ExhaustedInfo{Node: name, Attempt: st.att(name), Reason: x.Reason, Partial: p})
		}
		if w, ok := st.wait[name]; ok {
			s.Waits = append(s.Waits, WaitInfo{Node: name, Until: w.Until, Reason: w.Reason})
			if s.NextWake.IsZero() || w.Until.Before(s.NextWake) {
				s.NextWake = w.Until
			}
		}
		if f, ok := st.failed[name]; ok {
			s.Failed = append(s.Failed, FailInfo{Node: name, Err: f})
		}
	}
	switch {
	case st.aborted:
		s.State = Aborted
	case allDone:
		s.State = Done
		s.Output = map[string]json.RawMessage{}
		for _, n := range e.G.sinks() {
			s.Output[n] = st.done[n]
		}
	case len(s.Failed) > 0:
		s.State = Failed
	case len(s.Gates) > 0 || len(s.Exhausted) > 0:
		s.State = Blocked
	case len(s.Waits) > 0:
		s.State = Waiting
	default:
		s.State = Pending
	}
	return s
}
