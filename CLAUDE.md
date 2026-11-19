# CLAUDE.md

Design documents are in `docs/`; `docs/handoff.md` is the entry point and lists decisions 1–43. Read it before proposing changes.

- Paths in the docs use a `claude/` prefix (e.g. `claude/needs-backbone.md`); here they are `docs/needs-backbone.md`.
- Two languages only: Go (`kernel/`) and Rust (`needs/`). No Python in the system (decision 33); Python appears only as the target repository's test probe.
- Models only via the Claude Code subscription, run headless as the executor (decision 41). No model gateways or other providers.
- The owner decides; flag tensions instead of deciding. Keep replies short (often read on a phone); detail belongs in `docs/`.
- All notation in the docs is illustrative; no syntax has been chosen.
