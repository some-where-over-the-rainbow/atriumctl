# Prompts

Adapted from QRSPI by Matan Shavit (https://github.com/matanshavit/qrspi), MIT licence, see `QRSPI-LICENSE`. Sources: `.claude/commands/qrspi/1_question.md`, `2_research.md`, and `.claude/agents/codebase-locator.md`, `codebase-analyzer.md`, `codebase-pattern-finder.md`.

Changes, recorded as a deviation from the seed workflow (slice §4, needs-backbone §5):

- Questions and findings are written as need blocks (`q`, `fact`) with ids, statuses and links, and linted with `needs_lint`.
- Artifacts live at `.atriumctl/<name>`; tools are the gateway's (`read`, `ls`, `glob`, `grep`, `write`, `edit`, `needs_lint`, `needs_query`, `spawn`).
- The architect approves questions at the gate, item by item, instead of in conversation.
- Research's fan-out to sub-agents is done by the graph engine (one worker per 1–2 approved questions); the three QRSPI sub-agent roles are combined into one worker prompt.
- "When to go back" is a reply starting with `REQUESTION:`, which takes the back-edge to the Question stage (at most once).

Templates use Go `text/template`. Only stages whose sandbox mounts `task.md` receive `.Task`.
