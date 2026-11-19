// Package gateway is the tool gateway: Claude Code's only MCP server. It
// serves the file, needs and spawn tools, checks every call against the
// agent's grant, leases, mounts and the need-status rule, and writes a
// tool_call trace event for each.
//
// Each agent gets its own endpoint, /mcp/<token>, with a random token: the
// gateway knows the caller from the URL, never from what the model says,
// and one agent cannot call tools as another.
//
// File tools resolve paths through the agent's mount table (the work
// sandbox's layout) in the gateway itself, with symlink checks. Real
// work-sandbox containers come with `bash` in slice 2.
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"atriumctl/kernel/trace"
)

// Mount places a host file or directory at a path the agent sees.
// At "" is the root.
type Mount struct {
	At       string
	Host     string
	Writable bool
}

// Lease grants an access mode on a path (a file, or a directory prefix)
// until a time. Writes need an unexpired "rw" lease.
type Lease struct {
	At    string
	Mode  string // "rw" or "ro"
	Until time.Time
}

// Needs configures the needs tools and the status rule for an agent.
type Needs struct {
	Config  string   // path of needs.toml, as the agent sees it
	Sources []string // need sources the agent may lint, as the agent sees them
}

// SpawnFunc starts a sub-agent and returns its final answer.
type SpawnFunc func(ctx context.Context, parent *Agent, prompt string) (string, error)

type Agent struct {
	ID           string
	ParentID     string
	Role         string
	RunID        string
	Stage        string
	StageAttempt int
	Tools        []string // the grant
	Mounts       []Mount
	Leases       []Lease
	Needs        *Needs
	Spawn        SpawnFunc
}

func (a *Agent) granted(tool string) bool {
	for _, t := range a.Tools {
		if t == tool {
			return true
		}
	}
	return false
}

// agentState is an agent as the gateway tracks it.
type agentState struct {
	*Agent
	token string

	mu       sync.Mutex
	readHash map[string]string // virtual path -> hash of the content last read or written
}

func (s *agentState) lastSeen(vpath string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.readHash[vpath]
	return h, ok
}

func (s *agentState) saw(vpath, hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readHash[vpath] = hash
}

// Gateway serves the tools. Create with New; register agents before
// starting them.
type Gateway struct {
	Trace     trace.Sink
	NeedsBin  string                // the `needs` command; required for agents with Needs
	RepoFiles func() map[string]int // repository files and line counts at the run's commit, for evidence checks
	Now       func() time.Time

	mu     sync.Mutex
	agents map[string]*agentState // by token

	locksMu sync.Mutex
	locks   map[string]*sync.Mutex // per host path, for compare-and-swap
}

func New(sink trace.Sink) *Gateway {
	if sink == nil {
		sink = trace.Discard{}
	}
	return &Gateway{Trace: sink, Now: time.Now, agents: map[string]*agentState{}, locks: map[string]*sync.Mutex{}}
}

// Register adds an agent and returns its endpoint path, /mcp/<token>.
func (g *Gateway) Register(a *Agent) (string, error) {
	for _, t := range a.Tools {
		if _, ok := tools[t]; !ok {
			return "", fmt.Errorf("agent %s: unknown tool %q", a.ID, t)
		}
	}
	if a.Needs != nil && g.NeedsBin == "" {
		return "", fmt.Errorf("agent %s: needs tools need Gateway.NeedsBin", a.ID)
	}
	for i := range a.Mounts {
		a.Mounts[i].At = clean(a.Mounts[i].At)
	}
	for i := range a.Leases {
		a.Leases[i].At = clean(a.Leases[i].At)
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.agents[tok] = &agentState{Agent: a, token: tok, readHash: map[string]string{}}
	return "/mcp/" + tok, nil
}

// Unregister ends an agent's access.
func (g *Gateway) Unregister(path string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.agents, strings.TrimPrefix(path, "/mcp/"))
}

func (g *Gateway) agent(token string) *agentState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.agents[token]
}

func (g *Gateway) lock(hostPath string) func() {
	g.locksMu.Lock()
	m, ok := g.locks[hostPath]
	if !ok {
		m = &sync.Mutex{}
		g.locks[hostPath] = m
	}
	g.locksMu.Unlock()
	m.Lock()
	return m.Unlock
}

// lease reports whether the agent holds an unexpired lease of mode on vpath.
func (g *Gateway) lease(a *Agent, vpath, mode string) bool {
	now := g.Now()
	for _, l := range a.Leases {
		if l.Mode != mode || (!l.Until.IsZero() && now.After(l.Until)) {
			continue
		}
		if l.At == "" || vpath == l.At || strings.HasPrefix(vpath, l.At+"/") {
			return true
		}
	}
	return false
}
