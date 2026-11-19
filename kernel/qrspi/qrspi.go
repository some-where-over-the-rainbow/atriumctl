// Package qrspi holds slice 1's swarm definition: QRSPI's Question and
// Research stages with a gate, a research fan-out and a back-edge
// (docs/slice-1-question-research.md §11).
package qrspi

import (
	"embed"
	"time"

	"atriumctl/kernel/def"
	"atriumctl/kernel/probe"
)

//go:embed prompts/*.md
var prompts embed.FS

//go:embed needs.toml
var needsConfig string

func prompt(name string) string {
	b, err := prompts.ReadFile("prompts/" + name)
	if err != nil {
		panic(err)
	}
	return string(b)
}

var readTools = []string{"read", "ls", "glob", "grep"}

func tools(extra ...string) []string { return append(append([]string{}, readTools...), extra...) }

// Slice1 is the definition. Tiers map to Claude Code aliases (slice §7):
// large -> opus for stages, small -> sonnet for workers and sub-agents.
func Slice1() *def.Swarm {
	system := prompt("system.md")
	return &def.Swarm{
		Name:        "qrspi-question-research",
		MaxParallel: 3,
		Tiers:       map[string]string{"large": "opus", "small": "sonnet"},
		Artifacts:   []string{"task.md", "questions.md", "research.md"},
		NeedsConfig: needsConfig,
		Probes:      map[string]probe.Spec{"baseline": probe.Pytest()},
		Sandboxes: map[string]def.Sandbox{
			// slice §8.2; needs.toml is readable wherever needs tools run
			"scribe-question": {Repo: def.Read, Artifacts: map[string]def.Access{"task.md": def.Write, "questions.md": def.Write, "needs.toml": def.Read}},
			"reader":          {Repo: def.Read, Artifacts: map[string]def.Access{}},
			"scribe-research": {Repo: def.Read, Artifacts: map[string]def.Access{"questions.md": def.Read, "research.md": def.Write, "needs.toml": def.Read}},
		},
		Roles: map[string]def.Role{
			"question": {Tier: "large", Sandbox: "scribe-question", System: system, Spawns: "locator",
				Tools:  tools("write", "edit", "needs_lint", "needs_query", "spawn"),
				Limits: def.Limits{MaxTurns: 60, WallTime: 20 * time.Minute}},
			"locator": {Tier: "small", Sandbox: "reader", System: system, Tools: tools(),
				Limits: def.Limits{MaxTurns: 25, WallTime: 8 * time.Minute}},
			"worker": {Tier: "small", Sandbox: "reader", System: system, Tools: tools(),
				Limits: def.Limits{MaxTurns: 60, WallTime: 20 * time.Minute}},
			"research": {Tier: "large", Sandbox: "scribe-research", System: system,
				Tools:  tools("write", "edit", "needs_lint", "needs_query"),
				Limits: def.Limits{MaxTurns: 80, WallTime: 25 * time.Minute}},
		},
		Stages: []def.Stage{
			// Kernel-run, in parallel with the stages: tests and coverage at the pinned commit.
			{Name: "baseline", Kind: def.Kernel, Func: "probe", Probe: "baseline"},
			{Name: "question", Kind: def.Agent, Role: "question", Prompt: prompt("question.md"),
				Writes: []string{"task.md", "questions.md"}, Check: "needs-clean", FixRounds: 2},
			{Name: "questions-built", Kind: def.Kernel, Func: "needs-build"},
			{Name: "gate", Kind: def.Gate},
			{Name: "approve", Kind: def.Kernel, Func: "apply-gate"},
			{Name: "plan", Kind: def.Kernel, Func: "batch-questions"},
			{Name: "workers", Kind: def.Fanout, Role: "worker", Prompt: prompt("research-worker.md"), Cap: 3},
			{Name: "synthesize", Kind: def.Agent, Role: "research", Prompt: prompt("research-synthesize.md"),
				Writes: []string{"research.md"}, Check: "needs-clean", FixRounds: 2},
			{Name: "research-built", Kind: def.Kernel, Func: "needs-build"},
		},
		Edges: []def.Edge{
			{From: "question", To: "questions-built"},
			{From: "questions-built", To: "gate"},
			{From: "gate", To: "approve"},
			{From: "approve", To: "plan"},
			{From: "plan", To: "workers"},
			{From: "workers", To: "synthesize"},
			{From: "synthesize", To: "research-built"},
			{From: "synthesize", To: "question", Back: true, When: "requestion", Max: 1},
		},
	}
}

// Prompt returns one of the embedded prompts (fix.md, locator.md).
func Prompt(name string) string { return prompt(name) }
