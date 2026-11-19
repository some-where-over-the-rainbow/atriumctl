// Package engine is the kernel's graph engine: a DAG of nodes with
// counted back-edges, gates, fan-out with caps, and durable state in an
// append-only journal. A run continues after a crash at node or item
// granularity (at-least-once for work that was in flight).
//
// Decision 44, docs/spike-engine.md.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Kind int

const (
	Task Kind = iota // runs Fn once
	Gate             // pauses until Answer; the answer is the output
	Map              // runs Fn once per item of its single predecessor's output (a JSON array)
)

func (k Kind) String() string { return [...]string{"task", "gate", "map"}[k] }

// Input is what a node body receives.
type Input struct {
	Run      json.RawMessage            // the run's input
	From     map[string]json.RawMessage // outputs of forward predecessors
	Feedback json.RawMessage            // when re-entered by a back-edge: the output of its source
	Attempt  int                        // 1, then +1 per back-edge re-entry
	Item     json.RawMessage            // Map: the item
	Index    int                        // Map: the item's index
}

// Fn is a node body. It may return *WaitError or *ExhaustedError.
type Fn func(ctx context.Context, in Input) (any, error)

type Node struct {
	Name    string
	Kind    Kind
	Fn      Fn
	Cap     int           // Map: items in parallel (0 = no own cap)
	Timeout time.Duration // per activation (per item for Map); reaching it is "exhausted"
}

// Edge is a forward dependency, or with Back set, a conditional back-edge
// taken at most Max times when When(output of From) is true. Taking it
// re-runs To and everything downstream of To with a new attempt number.
type Edge struct {
	From, To string
	Back     bool
	When     func(out json.RawMessage) bool
	Max      int
}

type Graph struct {
	Nodes []Node
	Edges []Edge

	idx   map[string]int
	preds map[string][]string // forward
	succs map[string][]string // forward
	back  []int               // indexes into Edges
	topo  []string
}

// Compile validates the graph and prepares lookups.
func (g *Graph) Compile() error {
	g.idx = map[string]int{}
	g.preds, g.succs = map[string][]string{}, map[string][]string{}
	g.back = nil
	for i, n := range g.Nodes {
		if n.Name == "" || strings.ContainsAny(n.Name, " \t\n") {
			return fmt.Errorf("node %d: invalid name %q", i, n.Name)
		}
		if _, dup := g.idx[n.Name]; dup {
			return fmt.Errorf("duplicate node %q", n.Name)
		}
		if n.Kind != Gate && n.Fn == nil {
			return fmt.Errorf("node %q: no Fn", n.Name)
		}
		g.idx[n.Name] = i
	}
	for i, e := range g.Edges {
		if _, ok := g.idx[e.From]; !ok {
			return fmt.Errorf("edge %d: unknown node %q", i, e.From)
		}
		if _, ok := g.idx[e.To]; !ok {
			return fmt.Errorf("edge %d: unknown node %q", i, e.To)
		}
		if e.Back {
			if e.Max < 1 || e.When == nil {
				return fmt.Errorf("back-edge %s->%s: needs When and Max >= 1", e.From, e.To)
			}
			g.back = append(g.back, i)
			continue
		}
		g.preds[e.To] = append(g.preds[e.To], e.From)
		g.succs[e.From] = append(g.succs[e.From], e.To)
	}
	// Topological order (Kahn), stable in declaration order.
	in := map[string]int{}
	for _, n := range g.Nodes {
		in[n.Name] = len(g.preds[n.Name])
	}
	g.topo = nil
	for len(g.topo) < len(g.Nodes) {
		progressed := false
		for _, n := range g.Nodes {
			if in[n.Name] == 0 {
				in[n.Name] = -1
				g.topo = append(g.topo, n.Name)
				for _, s := range g.succs[n.Name] {
					in[s]--
				}
				progressed = true
			}
		}
		if !progressed {
			return fmt.Errorf("forward edges contain a cycle; use a back-edge")
		}
	}
	for _, n := range g.Nodes {
		if n.Kind == Map && len(g.preds[n.Name]) != 1 {
			return fmt.Errorf("map node %q needs exactly one predecessor", n.Name)
		}
	}
	for _, bi := range g.back {
		e := g.Edges[bi]
		if !g.region(e.To)[e.From] {
			return fmt.Errorf("back-edge %s->%s: %s is not downstream of %s", e.From, e.To, e.From, e.To)
		}
	}
	return nil
}

// region is To and everything forward-reachable from it.
func (g *Graph) region(to string) map[string]bool {
	r := map[string]bool{to: true}
	stack := []string{to}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, s := range g.succs[n] {
			if !r[s] {
				r[s] = true
				stack = append(stack, s)
			}
		}
	}
	return r
}

// Hash identifies the graph's shape (not its functions). A journal written
// for one shape is refused by another.
func (g *Graph) Hash() string {
	var b strings.Builder
	for _, n := range g.Nodes {
		fmt.Fprintf(&b, "n %s %s %d\n", n.Name, n.Kind, n.Cap)
	}
	for _, e := range g.Edges {
		fmt.Fprintf(&b, "e %s %s %v %d\n", e.From, e.To, e.Back, e.Max)
	}
	h := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(h[:8])
}

func (g *Graph) node(name string) Node { return g.Nodes[g.idx[name]] }

func (g *Graph) sinks() []string {
	var out []string
	for _, n := range g.Nodes {
		if len(g.succs[n.Name]) == 0 {
			out = append(out, n.Name)
		}
	}
	return out
}
