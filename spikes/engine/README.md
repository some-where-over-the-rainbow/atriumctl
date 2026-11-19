# Graph engine spike

Checks from `docs/slice-1-question-research.md` §6.3, run against candidate engines. Findings: `docs/spike-engine.md`.

```
go build -o bin/adk ./adk && go run ./check -bin bin/adk -pkg ./adk
go build -o bin/mini ./mini && go run ./check -bin bin/mini -pkg ./mini
```

- `common/`: the engine-neutral parts: trace log (JSON lines, synced per record), child-process helper with process-group kill, the scenario's node bodies, and the contract every candidate implements (`run`, `resume`, `continue`, `timeout`)
- `adk/`: the scenario on Google ADK for Go v2 (`workflow` package, function nodes only), state in SQLite through ADK's session service
- `mini/`: the scenario on an own minimal engine (`mini/engine`, ~255 lines, standard library only), state in an append-only journal file
- `check/`: the checks, run as separate processes so they can be killed with SIGKILL

The scenario is slice 1's shape: `question` → `gate` (human approval) → `worker` fan-out over the approved questions (cap 3) → `synthesize`.
