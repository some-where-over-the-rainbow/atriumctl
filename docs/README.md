# atriumctl: design documents

Exported from the claude.ai Project "Agentic system language" on 2026-11-18.

Start with `handoff.md`. It is the entry point and lists every decision.

| File | Read it for |
|---|---|
| `handoff.md` | Entry point: where the project stands, decisions 1–43, technology picture, open questions, next steps |
| `slice-1-question-research.md` | The first buildable slice: scope, components, limits, trace schema, spikes, acceptance criteria |
| `architect-console.md` | The console: views, test and coverage probes, linking tests to needs |
| `claude-code-executor.md` | The executor: Claude Code headless on the subscription, containment, limits, trace |
| `spike-engine.md` | Findings of the graph engine spike (code in `spikes/engine/`) |
| `spike-claude-code.md` | Findings of the Claude Code executor spike (code in `spikes/claude-code/`) |
| `needs-backbone.md` | The symbolic product model: need types, links, lint rules, the Rust tool and its WebAssembly build |

Not exported (still in the Project, older or background): the July conceptual handoff, the state-of-the-art survey, the QRSPI seed scenario, the realisation architecture, the autonomy and audit design, and the superseded Pi executor design. The handoff explains how to read their stale passages.

Paths inside the documents use the Project's `claude/` prefix, for example `claude/slice-1-question-research.md`. Here the same files sit directly in this folder.
