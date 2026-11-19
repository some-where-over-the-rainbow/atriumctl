// Package def is the swarm definition model (slice §4.1): typed data for
// roles, sandboxes, stages, edges and limits. There is no parser; a
// definition is written as Go data and compiled to an engine graph by
// package run.
package def

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"atriumctl/kernel/probe"
)

type Access string

const (
	None  Access = ""
	Read  Access = "read"
	Write Access = "write" // read and write; still needs a lease and a grant
)

// Sandbox is a work sandbox's mount layout: the repository and each
// artifact, by name. Artifacts appear to agents at .atriumctl/<name>.
type Sandbox struct {
	Repo      Access
	Artifacts map[string]Access
}

type Limits struct {
	MaxTurns int
	WallTime time.Duration
}

// Role is what an agent may do and see.
type Role struct {
	Tier    string   // model tier: large, small
	Tools   []string // gateway tools (the grant)
	Sandbox string
	Limits  Limits
	System  string // appended to Claude Code's system prompt (decision 46)
	Spawns  string // role of sub-agents started with the spawn tool
}

type StageKind string

const (
	Agent  StageKind = "agent"  // one agent of Role, prompt from Prompt
	Fanout StageKind = "fanout" // one agent of Role per item of the predecessor's output
	Gate   StageKind = "gate"   // the architect decides
	Kernel StageKind = "kernel" // a kernel function, by name
)

type Stage struct {
	Name   string
	Kind   StageKind
	Role   string
	Prompt string // template; see package run for the data it gets
	Func   string // Kernel: function name
	Probe  string // Kernel "probe": which of Swarm.Probes
	Cap    int    // Fanout: agents in parallel
	// Writes are the artifacts the stage's agent must write through the
	// gateway; with Check "needs-clean" they must also lint clean.
	Writes []string
	Check  string
	// FixRounds: how often an agent is re-run to fix lint findings before
	// the stage is exhausted.
	FixRounds int
}

type Edge struct {
	From, To string
	Back     bool
	When     string // Back: named condition, see package run
	Max      int
}

type Swarm struct {
	Name        string
	MaxParallel int               // run-wide cap on agents (and kernel functions) at once
	Tiers       map[string]string // tier -> Claude Code model alias
	Sandboxes   map[string]Sandbox
	Roles       map[string]Role
	Stages      []Stage
	Edges       []Edge
	Artifacts   []string // artifact files of a run, created empty
	NeedsConfig string   // needs.toml text, written as the artifact needs.toml
	Probes      map[string]probe.Spec
}

// Validate checks references between the parts.
func (s *Swarm) Validate() error {
	stages := map[string]bool{}
	for _, st := range s.Stages {
		if stages[st.Name] {
			return fmt.Errorf("duplicate stage %q", st.Name)
		}
		stages[st.Name] = true
		switch st.Kind {
		case Agent, Fanout:
			r, ok := s.Roles[st.Role]
			if !ok {
				return fmt.Errorf("stage %s: unknown role %q", st.Name, st.Role)
			}
			if _, ok := s.Tiers[r.Tier]; !ok {
				return fmt.Errorf("role %s: unknown tier %q", st.Role, r.Tier)
			}
			if st.Prompt == "" {
				return fmt.Errorf("stage %s: no prompt", st.Name)
			}
		case Kernel:
			if st.Func == "" {
				return fmt.Errorf("stage %s: no function", st.Name)
			}
			if st.Func == "probe" {
				if _, ok := s.Probes[st.Probe]; !ok {
					return fmt.Errorf("stage %s: unknown probe %q", st.Name, st.Probe)
				}
			}
		case Gate:
		default:
			return fmt.Errorf("stage %s: unknown kind %q", st.Name, st.Kind)
		}
	}
	for name, r := range s.Roles {
		if _, ok := s.Sandboxes[r.Sandbox]; !ok {
			return fmt.Errorf("role %s: unknown sandbox %q", name, r.Sandbox)
		}
		if r.Spawns != "" {
			if _, ok := s.Roles[r.Spawns]; !ok {
				return fmt.Errorf("role %s: spawns unknown role %q", name, r.Spawns)
			}
		}
	}
	for _, e := range s.Edges {
		if !stages[e.From] || !stages[e.To] {
			return fmt.Errorf("edge %s->%s: unknown stage", e.From, e.To)
		}
	}
	return nil
}

// Hash identifies the definition, for the trace (definition_hash).
func (s *Swarm) Hash() string {
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}
