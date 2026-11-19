# atriumctl — Handoff Summary

**Date:** 2026-11-16 (revised 2026-11-17 and 2026-11-18; latest: Claude Code on the subscription as executor, one repository, the architect console as driver)
**Entry point for new sessions.** It supersedes `agentic-system-language-handoff.md` (2026-07-03) as the place to start. The July document remains the conceptual foundation and is not replaced.
**Status:** The scope has narrowed and become concrete. Slice 1 is specified; nothing is implemented yet.

## 1. Where the project stands

In July the project was a concept collection for a general language describing agentic systems, built around asymptotic determinism and the observer problem.

It is now a **definition language for agent swarms created through code**, together with the system that runs, contains, simulates and audits such swarms. One concrete workflow (QRSPI, extended) is the seed scenario. The owner's main driver is the **architect console**: a live, lintable view of what is proposed and decided, and of implementation and test coverage.

The next concrete work is **slice 1**: QRSPI's Question and Research on `tidal-family`, with Claude Code (subscription) as executor, a Rust needs backbone, and console v1.

## 2. Document map

| Document | Read it for |
|---|---|
| `agentic-system-language-handoff.md` | The conceptual foundation: asymptotic determinism, the observer problem, named gaps |
| `claude/sota-architecture-formal-methods-proof-systems-2026-11.md` | State of the art: architecture as code, provable safety languages, proof systems |
| `claude/qrspi-seed-scenario.md` | The seed scenario: QRSPI, the architect, three directions, TOGAF control plane |
| `claude/realisation-architecture.md` | The system: control machine, gateways, leases, SSH runners, dev environment |
| `claude/autonomy-risk-audit.md` | Risk classes, autonomy profile, shortcuts, audit and KPIs |
| `claude/slice-1-question-research.md` | **The first buildable slice**: scope, limits, trace schema, spikes, acceptance criteria |
| `claude/architect-console.md` | **The console**: views, probes, linking tests to needs |
| `claude/claude-code-executor.md` | **The executor**: Claude Code headless on the subscription, containment, limits, trace |
| `claude/needs-backbone.md` | The symbolic product model: need types, links, lint rules, the Rust tool and its WebAssembly build |
| `claude/pi-executor-integration.md` | Superseded executor design (Pi). Kept for reference; Pi with an API key could return behind the same interface |

Outside the project documents: a whiteboard of the three directions, [Swarm with a human architect](https://claude.ai/artifact/TBTKHfiBVARvoDG2jNfBAp).

**Stale passages.** The scenario, architecture and autonomy documents predate decisions 22 to 43. Where they say LangGraph, read "Go-native graph engine"; Docker, Docker Sandboxes, OpenShell, LiteLLM, Agent Inbox or a model gateway: see section 4; "agents think on the control machine": "in a harness container on the control machine"; money budgets: "subscription windows and turn and time caps". The slice, console, executor and needs documents are current.

## 3. Decisions taken by the owner

**Scope and language**
1. Address agent swarm creation through code, with a definition language covering control, models, maximum times, token budgets, synchronisation times, fine-grained system and tool access, priming, orchestration, checks and integration stages.
2. ~~LangGraph is the runtime, with extensions.~~ Superseded by 23.
3. Special emphasis on sandboxing.
4. The system is to be simulated for tokens, CPU, memory, wait-time bottlenecks, system and internet request exhaustion and coordination context filling, based on collected profiles of similar tasks. (Now secondary, see 40.)

**Scenario**
5. QRSPI is the seed scenario, and work stays on it first.
6. The Matt Pocock skills `wayfinder` and `grilling` are added to the question phase, and `prototype` is added as well.
7. The swarm co-works with a human architect.
8. It drives three directions: down into the software V-model; across each level of detail into a prototype; and overall implementation governance through architecture diagrams, specifications and tests.
9. The control plane view uses TOGAF with UML diagrams.

**Realisation**
10. One interface for the human, and one overall runtime that initiates and controls the swarm.
11. Code needs a docker-compose-like dev environment.
12. It runs mainly on one machine as the control, is portable, and can control other remote runtimes via SSH.
13. All Linux, no Windows.
14. Central services: files and git; a secret store with per-task leases; internet access with policy and traces; generic tool access with leases, read/write policies and traces, adaptable to any third party.
15. The lease is a first-class concept of the language.

**Autonomy and audit**
16. The degree of autonomy is definable and risk-driven, with speed, thoroughness, validation coverage and requests for human interaction adaptable.
17. All artifacts, processes and KPIs are auditable at any point in time.
18. Shortcuts are explicitly allowed, each covered by a deviation reasoning statement.
19. Shortcut availability by risk class: any at R0; any except skipping verification at R1; only with the architect's approval at R2; none at R3. (The autonomy document still labels this "to confirm"; it is decided.)

**First slice and technology**
20. No task profiles exist. Instrumentation comes first.
21. The first slice is QRSPI's Question and Research stages, fully traced.
22. Implementation language: Go.
23. A Go-native graph engine, replacing LangGraph.
24. Container runtime with the least overhead: rootless Podman with crun.
25. ~~Model gateway: reuse and extend an existing one.~~ Not needed under 41; may return with API-key executors.
26. ~~Models through an OpenAI-compatible interface, including OpenAI, Anthropic and Gemini.~~ Superseded by 41.

**Agent executor**
27. ~~Pi is the agent executor from slice 1.~~ Superseded by 41 (Pi on the subscription is excluded by Anthropic's terms).
28. The tool gateway and the credentials are forced into the executor: it may not run tools or hold keys on its own terms.
29. Own hooks where the executor's are not enough.
30. Write tools as well: `write` and `edit` are in slice 1, through the tool gateway. Stage agents write their own artifacts. (`bash` stays out until slice 2.)

**Needs backbone**
31. A symbolic representation of the key decided or defined items, from the spec definition phase onward, as a lintable backbone and common ground between human and machine, modelled on Sphinx-Needs.
32. Only product items. The swarm definition stays in its own model.
33. No Python: Sphinx and Sphinx-Needs are not run.
34. No ubc or ubCode; an own Rust backbone instead.
35. The backbone enters in slice 1.
36. Rust is kept over Go because WebAssembly is a strong fit.

**Re-scoping (2026-11-18)**
37. Lease unit: money or a proxy. Under the subscription, the proxy is Claude Code's reported tokens and cost estimate; the real limit is the usage window.
38. One repository: `tidal-family` (branch `claude/tidal-family-pwa`, commit `91bc49f`). The earlier three-repository plan is dropped; no large size class for now.
39. Task: "Reset the current UIs. I want almost feature parity with Tidal. Lean over to Spotify if in doubt."
40. Profiling: one initial signal is enough; it can be improved later.
41. Models only through the Claude Code subscription, nothing else. Claude Code itself (headless) is the executor.
42. The spend cap is the subscription's own limits.
43. The architect console drives the work: the needs backbone with WebAssembly in QRSPI, and a live view of implementation and test coverage.

**After the spikes (2026-11-18)**
44. Graph engine: our own minimal engine, with its state in a journal and continuation after a crash in the middle of a stage (`docs/spike-engine.md`). ADK for Go and Eino are not used.
45. Code home: this repository (`atriumctl`) as the monorepo, with `kernel/` (Go) and `needs/` (Rust) beside `docs/` and `spikes/`.
46. System prompt: the stage prompt is appended to Claude Code's own (`--append-system-prompt`).
47. Console reach: home network only; no VPN or overlay network.
48. Control machine: everything runs on the current machine for now (Ubuntu 24.04, 4 cores, 7 GB RAM, rootless Podman with runc). A dedicated machine is deferred.
49. The console requires a token login, served over TLS with a self-signed certificate (`docs/architect-console.md` §3).

## 4. Technology picture

| Need | Choice | State |
|---|---|---|
| Kernel, tool gateway, console server | Go, one binary | Decided |
| Executor | Claude Code, headless (`claude -p`, streaming JSON), one process per agent in a harness container; our tool gateway as its only MCP server | Decided (41), spike done (`docs/spike-claude-code.md`) |
| Needs backbone | Rust (`needs/`): I/O-free core for native and WebAssembly; Markdown need blocks; Sphinx-Needs-shaped TOML; JSON-schema validation; declarative network rules; `needs.json` | Decided (31–36); core, command line and WebAssembly built, parity tested |
| Console | Served by the kernel; HTML, server-sent events, the needs WebAssembly module | Decided in direction (43), design proposed |
| Graph engine | Own minimal engine (`kernel/engine`): DAG, counted back-edges, gates, map fan-out with caps, wait and exhausted states, journal of finished nodes and items | Decided (44), built and tested |
| Containers | Rootless Podman with crun; krun for microVMs from slice 2 | Decided (24) |
| Trace store, checkpoints | SQLite | Proposed |
| Test probe | Kernel-run `pytest` with coverage in a work sandbox | Proposed |
| Forge, secret store | Forgejo, OpenBao | Later slices |

No longer in the picture: LangGraph, LiteLLM, Docker Sandboxes, NVIDIA OpenShell, Bifrost and GoModel, Pi and its TypeScript bridge, OpenAI and Gemini, Sphinx and ubCode, an own agent loop. The system now uses two languages: Go and Rust.

## 5. Working proposals, not yet explicitly confirmed

- Every clause carries **exhaustion semantics** and an **enforcement class** (hard, soft, observed). Exhaustion now has three kinds: abort, return partial, and **wait** (subscription window).
- **One definition, four projections:** engine graph, sandbox policy, simulator model, trace schema.
- **Reading of the three directions:** down is V-model levels, across is a throwaway prototype per level, governance is the persistent triple of diagram, specification and test.
- **Agents think in a harness container and act in a work sandbox** through the tool gateway.
- **Inside the executor is soft; gateway, mounts and network are hard.**
- **Harness-neutral hook points** in the language, mapped to the executor's events.
- **Write layers:** grant, lease, mount, need status; compare-and-swap; provenance from the gateway.
- **Self-reported model data is labelled as such**; tool data stays authoritative; reconciliation between the gateway and the executor's stream.
- **Probes:** test and coverage figures come only from kernel-run commands, never from agents.
- **Linking tests to needs** with a pytest marker or docstring tag, turned into `test` items with `verifies` links.
- **Needs details:** `q` and `fact` in slice 1; `dec`, `req` (L1–L5), `spec`, `test`, `risk`, `dev`, `disp`, `proto` later; agents set `draft` or `proposed`, only the architect sets `approved`, `rejected`, `superseded`; gate decisions item by item; `task.md` outside the backbone.
- **At most 3 agents in parallel**, to keep subscription use ordinary and individual.
- **Model tiers:** `large` → Opus, `small` → Sonnet (Haiku for the locator if the window is tight).
- **Slice order:** 1 Question and Research with console v1; 2 Design through Implement with live implementation and coverage views; 3 wayfinder, grilling, prototype, risk and audit; simulator later.
- **Isolation classes**, risk classes R0–R3, shortcut against dispensation, TOGAF mapping: as in the earlier documents.
- **All notation so far is illustrative.** No syntax has been chosen.
- **Name and distribution (proposed 2026-11-19).** The project is named and ships as **`atriumctl`**. Users install the whole system with `uv tool install atriumctl` from PyPI. Each wheel holds only prebuilt binaries: the Go kernel and the Rust needs tool. This is the same pattern `ruff` and `uv` use. Wheels are built for Linux x86_64 and arm64 only (decision 13).
  - **Name check (2026-11-19).** `atriumctl` is free on PyPI, crates.io, Go and as a GitHub organisation. The one clash is a helper tool called `atriumctl` inside `mKonic/atrium`, a Wayland compositor with 0 stars. Plain `atrium` is taken on PyPI (an unrelated "MX API" package) and on crates.io (the Bluesky AT Protocol libraries). Nothing has been reserved yet.
  - **Tension with 33.** No Python code runs, but PyPI becomes the release channel and a wheel-packaging step enters the build. If accepted, record this as a decision that narrows 33: no Python at runtime, PyPI used for delivery only.
  - **Open points.**
    - Renamed from `asl` and "Agentic System Language" to `atriumctl` in the code and docs on 2026-11-19. The local checkout directory still needs renaming.
    - Whether to reserve the PyPI name `atriumctl` with a placeholder upload.
    - Runtime prerequisites a wheel cannot ship: Podman (24) and Claude Code (41).

## 6. Where the July concepts went

| July concept | Now appears as |
|---|---|
| Resources buy convergence | Limits and leases; the subscription window as a lease with wait semantics |
| Repetition axis | The validation knob: repeated and independent verification, scaled by risk |
| Granularity axis | V-model levels and vertical slices; `level` on requirement and test needs |
| Observer signature | Provenance from gateway writes; the `source` field separating authoritative from self-reported data |
| Trust chains, verifier regress | Verifier independence; auditor role; architect sampling as human terminator |
| Goodhart, observer effect | KPIs from gateways and probes, never from agent self-report; reconciliation |
| Reversible against irreversible | Risk class floors and gates; the push outside as the one irreversible step |
| Composition with degradation | Integration stages and contracts (degradation itself still unmodelled) |
| Gap: the human | The architect console; the needs status lifecycle as the visible line between proposed and decided |
| Gap: purpose | The wayfinder destination (partly) |
| Gap: meaning | Partly: decided items have a shared symbolic form; their semantics remain prose |
| Gaps: strategic interaction, identity and memory, fallacy taxonomy | Still open |

## 7. Open questions

**Before slice 1 can be built**
- ~~Claude Code spike and engine spike.~~ Both done (`docs/spike-claude-code.md`, `docs/spike-engine.md`).
- ~~Control machine, code home, system prompt, console reach.~~ Decided (45–48).

**Needs and console**
- Rendering depth; network rules as JSON schema or own rule language; test-to-need link syntax.
- Whether the TOGAF control-plane diagrams live in the console.

**Language**
- Host form of the definition language; policy language it compiles to; whether machine-checked proof is still a goal.

**Scenario**
- Should research blindness also hold for wayfinder research tickets? Variant B (parallel implementers) is parked.

**Architecture**
- Runners own or rented; models on runners; internet gateway at TLS or domain level; one harness container per agent or per stage.
- Executor terms: if Anthropic's subscription terms change, the executor decision is revisited.

**Autonomy**
- Boilerplate deviation reasons; presets or sliders; risk classification remains the weakest point.

**Not yet touched**
- Simulator design; a consolidated grammar; more than one architect; the fallacy taxonomy.

## 8. Next steps

1. ~~Claude Code spike, then engine spike.~~ Done; own engine chosen (44).
2. ~~Settle control machine, code home, system prompt, console reach.~~ Decided (45–48).
3. Build slice 1: kernel, tool gateway, needs core with WebAssembly, console v1, baseline probe. Done so far: the graph engine (`kernel/engine`), the needs core with command line and WebAssembly (`needs/`), the tool gateway and the executor with egress proxy, telemetry receiver, shim and harness image (`kernel/`), tested live with real agents in containers; the `atriumctl` binary that runs slice 1's graph (first full run on `tidal-family` done, see slice §16 Status). the architect console (`atriumctl console`); the baseline probe. Next: the trace store and the profile command.
4. Run it on `tidal-family` with the given task; read the first signal in the console.
5. Slice 2: Design through Implement with live implementation and coverage views.

## 9. Ways of working

- Iterative and dialogue-driven. The owner sets pace and scope; Claude acts as a thinking partner and flags tensions instead of deciding.
- Conversation in English. The owner often works from a phone, so replies are short and the detail goes into project documents.
- Handoff summaries like this one preserve continuity between sessions.
