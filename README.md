# atriumctl

A definition language for agent swarms created through code, and the system that runs, contains, traces and audits them, driven from an architect console.

**Status:** early. Slice 1 is built and has run end to end on a real repository. That slice is the Question and Research stages of an adapted [QRSPI](kernel/qrspi/prompts/README.md) workflow, plus a baseline test probe and console v1. The interfaces and the definition notation are not stable yet. The design lives in [`docs/`](docs/); start with [`docs/handoff.md`](docs/handoff.md).

## How it works

- A **swarm definition** describes roles, sandboxes, stages, edges and limits as typed data. It compiles into a graph that a crash-safe engine runs, with gates, fan-out and caps.
- Each agent is **Claude Code run headless** (`claude -p`) on a Claude subscription, optionally in a harness container. Its only tools come from the kernel's **tool gateway** (MCP). The gateway enforces mounts, leases and write rules and records every call in the trace. The container's only route out is an egress proxy that allows `api.anthropic.com` and nothing else.
- A **needs backbone** keeps the symbolic product model: questions, findings, requirements and tests, written as typed items in Markdown. The backbone lints them and links them together. The same Rust core runs natively for agents and as WebAssembly in the browser.
- The **architect console** is a web UI for steering the work. It shows runs, gates (approve, reject, or edit with live lint), needs, the trace graph, history, and baseline test coverage.

## Layout

| Path | Language | Contents |
|---|---|---|
| [`kernel/`](kernel/) | Go | One binary, `atriumctl`: engine, compiler, tool gateway, executor, egress proxy, telemetry receiver, test probe, console, command line |
| [`needs/`](needs/) | Rust | Needs backbone: `needs-core` (I/O-free, native and wasm32), `needs-cli`, `needs-wasm` |
| [`spikes/`](spikes/) | Go | The Claude Code executor spike and the engine spike that informed the design |
| [`docs/`](docs/) | Markdown | Design documents and decisions |

Build and usage are described in [`kernel/README.md`](kernel/README.md) and [`needs/README.md`](needs/README.md).

## Next

Slice 2: the stages from Design through Implement, live views of implementation and coverage, and `bash` in work-sandbox containers. See [`docs/handoff.md`](docs/handoff.md).

## License

[Apache-2.0](LICENSE). The prompts in `kernel/qrspi/prompts/` are adapted from QRSPI under MIT; see that folder's README.
