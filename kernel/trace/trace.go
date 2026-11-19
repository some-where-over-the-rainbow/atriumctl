// Package trace carries the kernel's trace events (slice §13). Sources are
// authoritative (tool gateway, sandbox manager, kernel, needs build, probe)
// or self-reported (harness). The SQLite store comes later; for now events
// go to a Sink.
package trace

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

const SchemaVersion = 1

const (
	SourceGateway = "tool_gateway"
	SourceKernel  = "kernel"
	SourceHarness = "harness" // self-reported by Claude Code
	SourceEgress  = "egress"
	SourceNeeds   = "needs_build"
	SourceProbe   = "probe"
)

// Event is one trace row: the common fields plus type-specific ones.
type Event struct {
	Type          string         `json:"type"` // tool_call, model_call, turn, limit_event, egress, agent, ...
	SchemaVersion int            `json:"schema_version"`
	Source        string         `json:"source"`
	TsStart       time.Time      `json:"ts_start"`
	TsEnd         time.Time      `json:"ts_end,omitzero"`
	RunID         string         `json:"run_id,omitempty"`
	Stage         string         `json:"stage,omitempty"`
	StageAttempt  int            `json:"stage_attempt,omitempty"`
	Role          string         `json:"role,omitempty"`
	AgentID       string         `json:"agent_id,omitempty"`
	ParentAgentID string         `json:"parent_agent_id,omitempty"`
	Fields        map[string]any `json:"fields,omitempty"`
}

type Sink interface {
	Emit(Event)
}

// Discard drops events.
type Discard struct{}

func (Discard) Emit(Event) {}

// Memory keeps events, for tests.
type Memory struct {
	mu     sync.Mutex
	events []Event
}

func (m *Memory) Emit(e Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
}

func (m *Memory) Events(typ string) []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Event
	for _, e := range m.events {
		if typ == "" || e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// File appends events as JSON lines.
type File struct {
	mu sync.Mutex
	f  *os.File
}

func OpenFile(path string) (*File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &File{f: f}, nil
}

func (s *File) Emit(e Event) {
	if e.SchemaVersion == 0 {
		e.SchemaVersion = SchemaVersion
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.f.Write(append(b, '\n'))
}

func (s *File) Close() error { return s.f.Close() }
