# Slice 1: Question and Research, with the Architect Console

**Date:** 2026-11-16 (revised 2026-11-17: harness container, write tools; 2026-11-18: needs backbone; Claude Code on the subscription as executor; one repository; console first)
**Status:** Specification for the first buildable slice. Two short spikes and a few open points remain before building (section 15).
**Companions:** `claude/handoff-2026-11-16.md` (entry point), `claude/claude-code-executor.md` (the executor), `claude/architect-console.md` (the console), `claude/needs-backbone.md` (the symbolic product model), `claude/qrspi-seed-scenario.md` (the workflow), `claude/realisation-architecture.md` (the target system)

## 1. Goal

Run the first two stages of QRSPI (Question and Research) on `tidal-family`, with every tool call passing our gateway and being traced, with questions and findings captured as lintable need items, and with the architect following and deciding through a first version of the console. Produce one initial profile signal on the side.

**Why this slice**
- The console is what drives the work (decision 43), and Question and Research are where the first decisions appear.
- They read the repository and write only their own artifacts, so no dev environment or microVM is needed.
- Research contains the one real fan-out and the information barrier (Research must not see the task).
- They have exactly one human gate, enough to prove pause, resume and decisions from the console.

## 2. Decisions for this slice

From 2026-11-16 and 2026-11-17 (still in force): Go; Go-native graph engine; rootless Podman with crun; the tool gateway forced into the executor with own hooks where needed; write tools (`write`, `edit`) through the tool gateway, agents write their own artifacts.

Taken by the owner on 2026-11-18:

1. **Needs backbone in this slice**, as an own Rust tool (no Python, no ubc), with a WebAssembly build for the browser.
2. **Lease unit: money or a proxy.** With the subscription, the proxy is Claude Code's own reported tokens and cost estimate; the actual limit is the subscription's usage window.
3. **One repository: `tidal-family`**, branch `claude/tidal-family-pwa`, commit `91bc49f`.
4. **Task:** "Reset the current UIs. I want almost feature parity with Tidal. Lean over to Spotify if in doubt."
5. **Profiling: one initial signal is enough.**
6. **Models: the Claude Code subscription and nothing else.** Claude Code is the executor; Pi, the model gateway and other providers are out.
7. **Spend cap: the subscription's own limits.**
8. **The architect console drives the work.**

Superseded for this slice: the Pi executor, Bifrost, three providers, three profiling repositories, the money lease.

## 3. In and out of scope

| In | Out (later slices) |
|---|---|
| Definition as a typed data model | A parser and concrete syntax |
| Compiler to a graph on a Go-native engine | Design, Structure, Plan, Worktree, Implement, PR |
| Claude Code as executor, headless | Other executors |
| Tool gateway for read and write tools, `needs_*`, `spawn` | Agent code execution (`bash`), microVM, SSH runners |
| Writes to artifact paths | Writes to the repository |
| Needs tool: `q` and `fact`, lint, build, query, diff; native and WebAssembly | Other need types; test-to-need links |
| Console v1: run, gate, needs, trace graph, baseline coverage | Implementation and coverage-over-time views |
| Kernel-run test probe for the baseline | Probes after implementation steps |
| Trace store; one initial profile | Simulator; multi-repository profiles |
| Harness containers and work sandboxes | Wayfinder, grilling, prototype |

## 4. Components

1. **Definition model.** Typed Go structures for swarm, stage, role, sandbox, limits, gate, check, hook, probe. No parser; definitions are written as data.
2. **Compiler.** Turns a definition into a graph for the engine.
3. **Graph engine.** Go-native, behind a narrow interface of our own (section 6).
4. **Executor: Claude Code.** One `claude -p` process per agent in a harness container, driven over streaming JSON. Behind our executor interface. See `claude/claude-code-executor.md`.
5. **Tool gateway.** Claude Code's only MCP server. Serves `read`, `grep`, `glob`, `ls`, `write`, `edit`, `needs_lint`, `needs_query`, `spawn`; checks grant, lease, mount and need status; executes in the role's work sandbox; writes tool-call events. Go, inside the kernel. Tool names follow the QRSPI prompts directly (`glob`), since we define the tools ourselves.
6. **Needs tool.** Rust: I/O-free core (native and WebAssembly), command line in the work sandbox, browser bindings for the console. See `claude/needs-backbone.md`.
7. **Probe runner.** Runs declared commands (here: `pytest` with coverage) in a work sandbox, kernel-initiated only.
8. **Sandbox manager.** Rootless Podman containers (harness and work) with fixed mounts and network policy; samples CPU and memory.
9. **Trace store.** One append-only SQLite file. Authoritative: tool gateway, sandbox manager, needs build, probes, kernel. Self-reported: Claude Code's stream and telemetry.
10. **Console server.** Part of the kernel binary: web pages, server-sent events, the WebAssembly module, gate actions. See `claude/architect-console.md`.
11. **Command line.** `run`, `gate`, `needs`, `profile`, `console`.

**Deployment shape:** one Go binary (kernel, tool gateway, console, command line), Podman, a harness image with a pinned Claude Code version, a work-sandbox image with the `needs` binary and the repository's test dependencies. No daemon of ours stays running between runs except the console while it is open.

The stage and subagent prompts come from the QRSPI repository (MIT licence, with attribution). One recorded deviation: the stage prompts also instruct writing questions and findings as need blocks.

## 5. Executor in short

- **Placement:** Claude Code runs in a harness container with no repository and no artifacts; its tools act in the work sandbox through the tool gateway.
- **Tools:** built-in tools off; our tool gateway is the only MCP server; settings sources empty, so no repository `CLAUDE.md` or project settings apply.
- **Credential:** the subscription token, mounted per run; the harness network reaches only Anthropic's API and the tool gateway.
- **Limits:** turns and wall time per agent (hard), at most 3 agents in parallel, and the subscription window with wait semantics (section 12).
- **Trace honesty:** model-call data is self-reported by Claude Code; tool calls stay authoritative.

## 6. Graph engine

### 6.1 What the slice needs from it

1. Nodes that are plain Go functions
2. A fan-out whose width is decided at run time, with a join and a parallelism cap
3. Pause at a node and resume later, surviving a restart of the process
4. Checkpoints in a local file
5. Cancellation and a timeout per node
6. A hook on every node start and end, for tracing and for the console

These six become our own engine interface; the engine stays replaceable.

### 6.2 Candidates

| Candidate | For | Against |
|---|---|---|
| **Google ADK for Go 2.x**, workflow engine | Graph of function nodes; parallel workers with aggregation; durable pause; Apache 2.0 | Examples centre on Gemini; larger dependency; its model agents are unused here |
| **Eino** (CloudWeGo) | Graph, parallel nodes, interrupt and checkpoint with our own store; Apache 2.0 | Before version 1.0 |
| **Own minimal engine** | Full control; a few hundred lines for these six needs | We own every durability bug |

**Proposed order:** with Claude Code doing all agent work, no framework agent is used at all. The smallest engine that meets the six needs wins; try ADK for Go with function nodes only, then Eino, then our own.

### 6.3 Engine spike (short, before building)

- Fan-out width at run time, with a cap; join waits for all
- A paused run resumes after the process was killed, from a local file
- A node timeout cancels cleanly, including a child process
- Node start and end observable
- Size and dependencies of the binary

## 7. Models

- **The definition names tiers:** `large` for the stages, `small` for the subagents, as in QRSPI.
- **Mapping to Claude Code model aliases,** pinned at build: proposed `large` → Opus, `small` → Sonnet (Haiku for the locator if the window is tight).
- **The trace records the exact model id** that Claude Code reports.

## 8. Containers

### 8.1 Harness container (one per agent)

| Holds | Does not hold | Network |
|---|---|---|
| Claude Code (pinned), read-only config with our MCP server only, the subscription token | Repository, artifacts, anything a generated command could act on | Anthropic API and the tool gateway only |

### 8.2 Work sandboxes

| Sandbox | Used by | Repository | `task.md` | `questions.md` | `research.md` | `needs.toml` | Network |
|---|---|---|---|---|---|---|---|
| `scribe-question` | Question stage | read | write | write | none | read | none |
| `reader` | Workers | read | not mounted | read | none | read | none |
| `scribe-research` | Research stage | read | **not mounted** | read | write | read | none |
| `probe` | Kernel only | read (copy) | not mounted | none | none | none | none |

**As built (2026-11-18):** for the file tools, the tool gateway enforces each sandbox's mount table itself (path resolution through the mounts, with symlink checks; read-only and writable mounts; directories that hold mounts resolve only through them), instead of executing each call in a work-sandbox container. The layout and the denials are the same; containers become necessary with `bash` in slice 2. The owner to confirm.

The barrier is enforced by the mount layout: `task.md` never reaches a research sandbox or any harness container. `task.md` stays plain prose outside the needs backbone.

The probe sandbox works on a copy of the repository, so test runs cannot change the pinned tree. Its image carries the repository's test dependencies, installed at image build, so it needs no network at run time.

### 8.3 What decides a write

| Layer | Question | Slice-1 setting |
|---|---|---|
| Grant | Does the role have `write` or `edit`? | Stage agents yes; workers no |
| Lease | Does the agent hold mode `rw` on this path? | Only the stage's own artifact files |
| Mount | Is the path writable in the work sandbox? | Repository read-only; artifacts as in 8.2 |
| Status | May the writer set this need status? | Agents: `draft`, `proposed`. Architect only: `approved`, `rejected`, `superseded` |

`edit` is a read plus a full-file write with compare-and-swap. Every artifact version is a traced gateway write.

## 9. Needs backbone in this slice

| Stage | Writes into | Need type | Fields | Links |
|---|---|---|---|---|
| Question | `questions.md` | `q` | id `Q_nnn`, title, status `proposed`, `area` | — |
| Research | `research.md` | `fact` | id `F_nnn`, title, status `proposed`, `evidence` (file and line) | `answers` → `q` |

Rules: id pattern and uniqueness; every `fact` answers an approved `q`; every approved `q` is answered or listed as open; `evidence` names a file that exists at the pinned commit.

It runs three ways: as read-only tools for agents (`needs_lint`, `needs_query`); as the `needs-clean` check that completes a stage; and as `needs build` after each stage, whose `needs.json` is the only model the kernel and console trust. The console runs the same core in WebAssembly.

## 10. Console v1

Detail is in `claude/architect-console.md`. In this slice:

- **Run view:** stages, agents, waits, turns, wall time, live.
- **Gate view:** the proposed questions, item by item; approve, reject or edit, with live lint while editing.
- **Needs view and trace graph:** questions and findings with their links and findings.
- **Baseline view:** the probe's test results and coverage for `tidal-family` at the pinned commit.
- **History:** needs diff between snapshots of the run.

Gate answers from the console and from the command line are equivalent; both go through the kernel under the architect's identity.

## 11. Graph

```
probe(baseline) ─────────────────────────────────────────────┐ (parallel, kernel)
question -> needs build -> gate(approve per question) -> research.plan
research.plan -> fan-out: one worker per 1-2 approved questions (max 3 parallel)
workers -> join(all) -> research.synthesize -> needs build -> end

research.* -> question          # back-edge, conditional, counted
any node  -> waiting            # subscription window reached: pause, resume after reset
any node  -> exhausted          # turn or time cap reached: return partial -> architect
```

A stage is complete only when its artifacts were written through the gateway and are needs-clean. `gate mode auto` approves all proposed questions for unattended runs.

## 12. Limits in this slice

| Limit | Value | On reaching it |
|---|---|---|
| Turns per agent | from the definition | abort, return partial |
| Wall time per agent and per stage | from the definition | abort, return partial |
| Parallel agents per run | 3 | further workers queue |
| Subscription usage window | the plan's own | **wait**: pause the run, show reset time in the console, resume |
| Artifact files, mode `rw` | the stage's duration | further writes denied |
| Containers | time to live, heartbeat | container stops |

Proxy figures (tokens, cost estimate) are recorded per call for the profile; nothing is billed.

## 13. Trace schema

One table per event type, field names after the OpenTelemetry GenAI conventions where they exist, versioned by us.

**Common fields:** `event_id`, `schema_version`, `source` (tool gateway, sandbox manager, kernel, needs build, probe, or harness: self-reported), `ts_start`, `ts_end`, `run_id`, `stage`, `stage_attempt`, `role`, `agent_id`, `parent_agent_id`, `task_id`, `definition_hash`.

| Event | Source | Key fields |
|---|---|---|
| `run` | kernel | `repo_id`, `repo_commit`, `repo_files`, `repo_loc`, `task_ref`, `executor`, `executor_version`, `needs_version`, `gate_mode`, `outcome`, `wall_ms` |
| `stage` | kernel | `status`, `back_edge_to`, `wall_ms`, `model_calls`, `tool_calls`, `subagents` |
| `model_call` | harness (self-reported) | `gen_ai.request.model`, `model_tier`, input, output, cache read and write tokens, `cost_estimate`, `duration_ms`, `step_index`, `purpose` (turn, compaction), `stop_reason` |
| `tool_call` | tool gateway | `tool`, `primitive`, `path_class`, args size and digest, result size, `bytes_written`, hashes before and after, `exec_ms`, `latency_ms`, `sandbox_id`, `status`, `deny_reason` (grant, lease, mount, status), lint counts, child agent for `spawn` |
| `turn` | harness | `turn_index`, `context_tokens`, `context_fill`, `tool_calls`, `compacted` |
| `limit_event` | kernel | `kind` (turns, time, window), `action` (abort, wait, resume), `resets_at` |
| `gate` | kernel | mode, opened, answered, `wait_ms`, answer, needs approved and rejected, `channel` (console, command line) |
| `sandbox_sample` | sandbox manager | `kind` (harness, work, probe), CPU, memory current and peak, pids |
| `artifact` | tool gateway | path, version, writing tool call, size, lines, hash, check results |
| `needs_snapshot` | needs build | source hash, counts per type and status, errors, warnings, open questions, diff counts |
| `probe_result` | probe | command, tests, passed, failed, skipped, line and branch coverage, per-file coverage, duration |

**Content:** prompt and response text in a separate content table keyed by hash, switchable off. Claude Code's session files count as content.

**Reconciliation:** tool calls counted by the gateway equal tool uses in Claude Code's stream; model calls are compared between Claude Code's stream and its telemetry. A mismatch is a finding.

**Deferred:** hash-chaining of the trace.

## 14. The initial profile

From the trace, per stage and role: model calls and tokens per agent; context growth by step; compactions; tool calls by tool; wall time per stage; gate wait; subagents per Research run; lint rounds; questions and facts per run; CPU and memory per container; window waits. **Plan:** a handful of runs on the one task, enough for orders of magnitude. More is optional and later.

## 15. Still to settle

1. **Claude Code spike.** Done (`docs/spike-claude-code.md`).
2. **Engine spike.** Done (`docs/spike-engine.md`); own engine (44).
3. **Control machine.** Decided (48): the current machine for now. Earlier proposal: a dedicated Linux box or VM, 8 cores, 32 GB RAM, SSD, current Fedora Server or Debian 13, rootless Podman with cgroup v2.
4. **Code home.** Decided (45): this repository as the monorepo, with `kernel/` (Go, including console) and `needs/` (Rust); no licence while private, Apache-2.0 if opened.
5. **System prompt.** Decided (46): append the stage prompt to Claude Code's own.
6. **Console reach.** Decided (47): home network only.

## 16. Done when

1. One command (or the console) runs both stages on `tidal-family` at the pinned commit; the agents write `task.md`, `questions.md` and `research.md` through the tool gateway.
2. The run pauses at the gate; the architect approves or rejects question by question in the console, with live lint; the run resumes, also after a restart of the control process.
3. A research agent that tries to read `task.md` fails, and a test proves it.
4. No tool acts except through the tool gateway: a request for a shell command or a read outside our tools fails, and a `CLAUDE.md` in the working directory has no effect. Tests prove both.
5. Writes are confined: repository, worker and cross-stage writes are each denied with the layer named; an agent setting a question to `approved` is denied by the status rule.
6. A deliberately small turn cap ends an agent with the defined outcome and a `limit_event`; a simulated window limit pauses the run and resumes it.
7. Each stage ends needs-clean; the run yields `needs.json` and a snapshot per stage; the console's WebAssembly linter reports the same findings as the native build, on the same fixtures.
8. The baseline probe shows `tidal-family`'s test results and coverage in the console.
9. The subscription token appears in no artifact, trace row or work sandbox.
10. The reconciliation of section 13 holds on every run.
11. The profile command prints the initial profile.

### Status (2026-11-18)

First full run on `tidal-family` at `91bc49f` with the decided task, both tiers on haiku, manual gate: Question wrote 7 neutral questions; the gate (from a new process) approved 5, rejected 1, left 1 open; 3 workers in parallel and the synthesis wrote 61 findings; all 66 needs lint clean with evidence checked against the snapshot. Cost estimate $0.07.

| # | Criterion | State |
|---|---|---|
| 1 | One command runs both stages; agents write the artifacts through the gateway | Met (`atriumctl run`, `atriumctl gate`) |
| 2 | Pause at the gate, item-by-item answers, resume after a restart | Met from the command line and the console (with edits and live lint) |
| 3 | Research cannot read `task.md`, with a test | Met (gateway tests, live test) |
| 4 | No tool outside the gateway; `CLAUDE.md` has no effect | Met (executor flags, live test) |
| 5 | Confined writes, layer named; status rule | Met (gateway tests, live test) |
| 6 | Turn cap ends an agent; window limit pauses and resumes | Met in engine and executor tests; not yet provoked in a full run |
| 7 | WebAssembly linter equals native | Met (`needs/scripts/parity.mjs`) |
| 8 | Baseline probe in the console | Met: `tidal-family` @ `91bc49f`: 68 of 68 tests pass, lines 81.1 %, branches 70.9 % (tests excluded from coverage); shown in the console's Baseline view |
| 9 | Token in no artifact, trace or sandbox | Met (checked on the run directory) |
| 10 | Reconciliation on every run | Met on this run (gateway calls = stream tool uses for all 6 agents); not yet an automatic check |
| 11 | Profile command | Pending |

**Probe as built:** a kernel stage `baseline` with no edges, so it runs alongside Question; image `atriumctl-probe:<hash>` from `python:3.12-slim` (the repository's own base) plus `requirements.txt`, `requirements-dev.txt`, pytest and pytest-cov, built with network once per content hash; the run itself has no network, no capabilities, a read-only root, and works on a writable copy of the snapshot. A probe that cannot run reports the error in its result and does not fail the run.

**As built, beyond the specification:** research planning is a kernel function (one worker per one or two approved questions, no planning agent); the three QRSPI sub-agent roles are one worker prompt; Question can `spawn` a locator; a stage whose artifact does not lint clean gets up to two fix rounds before it is exhausted.

## 17. Risks

- **Self-reported model data.** Without a gateway, model usage is Claude Code's own account. Tool calls stay authoritative; the audit view labels the difference.
- **Subscription terms.** Use must stay ordinary and individual: one person, own repository, capped parallelism. Changes to the terms can change the executor decision.
- **Fast-moving executor.** Claude Code changes often. The version is pinned; upgrades re-run the spike checks.
- **Hooks mistaken for a boundary.** Restrictions inside the Claude Code process are soft; the gateway, mounts and network are hard.
- **The backbone shapes the work.** Writing need blocks and fixing lint findings costs turns the original QRSPI does not spend; lint rounds are measured.
- **Console scope creep.** The console is the driver, so it will want to grow. Slice 1 keeps it to five views.
- **Thin statistics.** One task, a handful of runs: orders of magnitude only.

## 18. What follows (proposed order)

- **Slice 2:** Design, Structure, Plan, Worktree and Implement on `tidal-family` in a microVM dev environment (Podman with krun), with `bash` through the tool gateway, test probes after each step, need types `dec`, `req`, `spec`, `test`, test-to-need links, and the console's implementation and coverage-over-time views.
- **Slice 3:** wayfinder, grilling and prototype in the question phase; risk classes and the audit view in the console.
- **Later:** the simulator on accumulated profiles; SSH runners; PR stage and forge.

This reorders the earlier plan, where the simulator was slice 2; profiling is now secondary to the console.

## Sources

- [QRSPI workflow](https://github.com/matanshavit/qrspi)
- [Claude Code: legal and compliance](https://code.claude.com/docs/en/legal-and-compliance)
- [Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference)
- [Sphinx-Needs schema validation (8.5)](https://sphinx-needs.readthedocs.io/en/8.5.0/schema/index.html)
- [jsonschema crate (Rust)](https://docs.rs/jsonschema)
- [Announcing ADK Go 2.0 (Google)](https://developers.googleblog.com/en/announcing-adk-go-20/)
- [Eino: interrupt and checkpoint manual](https://cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/checkpoint_interrupt)
- [Podman vs Docker for self-hosting](https://dev.to/selfhostingsh/podman-vs-docker-which-should-you-self-host-with-e57)
- [krun man page](https://www.mankier.com/1/krun)
- [OpenTelemetry GenAI conventions: status in 2026](https://dev.to/azena-ai/opentelemetrys-genai-semantic-conventions-are-not-stable-yet-heres-what-actually-shipped-in-2026-3mke)
