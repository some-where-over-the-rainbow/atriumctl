# Graph Engine Spike: Findings

**Date:** 2026-11-18
**Status:** Done. ADK for Go and an own minimal engine both meet the five §6.3 needs. Only the own engine continues a run after a crash in the middle of a stage. Eino was assessed from source and not built. **Decided by the owner (decision 44): the own minimal engine.** Code: `spikes/engine/`.
**Companions:** `docs/slice-1-question-research.md` (§6 defines the needs and the spike), `docs/spike-claude-code.md`

## 1. Scenario

Slice 1's shape, with stand-ins for agents:

```
question (makes N question ids) → gate (pause; the architect approves a subset)
  → worker fan-out over the approved ids, cap 3 (each starts a child process, 0.4 s)
  → join → synthesize
```

Every candidate implements the same command-line contract (`run`, `resume`, `continue`, `timeout`). An engine-neutral checker (`spikes/engine/check`) runs each command as a separate process and reads a trace log synced after every record, so a SIGKILL loses nothing. Node start and end are recorded by our own wrapper around each node body, not by the engine. That wrapper is the hook point the kernel will use for every engine.

## 2. Results

| Need (§6.1 / §6.3) | ADK for Go v2.8.0 | Own minimal engine |
|---|---|---|
| 1. Function nodes | **Pass**: `NewFunctionNode` | **Pass** |
| 2. Fan-out with run-time width, cap, join | **Pass**: `ParallelWorker` (cap 3); 5 of 9 approved; at most 3 concurrent; `synthesize` started 18 ms after the last worker | **Pass**: same figures, 9 ms |
| 3. Pause, resume after the process was killed | **Pass**: SIGKILL at the gate; a new process resumed from SQLite; `question` did not re-run | **Pass**: same, from the journal file |
| 4. Checkpoints in a local file | **Pass**: SQLite through ADK's session service (gorm, pure Go driver) | **Pass**: an append-only JSON-lines journal, synced per record |
| 5. Node timeout cancels cleanly, child included | **Pass**: `NodeConfig.Timeout` 1 s; node ended at 1004 ms; `sleep 30` child gone | **Pass**: 1004 ms; child gone |
| 6. Node start and end observable | **Pass**: through our wrapper (ADK has OpenTelemetry spans but no public node hook) | **Pass**: hooks in the engine |
| Beyond §6.3: crash in the middle of the fan-out, then continue | **Fail**: no way to continue. Re-sending the approval yields `no waiting node matched the supplied responses`; any other message restarts the graph from the start | **Pass**: finished items are skipped, in-flight items run again (at-least-once), the run completes |
| Binary, stripped | 20.6 MB, 46 modules (genai, gorm, sqlite, OpenTelemetry, …) | 2.9 MB, standard library only |
| Code we own | Scenario only (~185 lines) | Engine ~255 lines + scenario (~120) |

## 3. ADK for Go: notes

- **Fits the six needs out of the box**, with routes, retries (`RetryConfig`), `JoinNode`, a run-wide `WithMaxConcurrency` and human-in-the-loop pause (`ResumeOrRequestInput`, `RerunOnResume`). Apache 2.0, active (v2.8.0, 2026-11-16).
- **Durability is tied to the human-in-the-loop pause.** ADK rebuilds run state from session events, but `Resume` only acts on a node that is *waiting for input*. A run interrupted by a crash or restart is not resumable: the in-flight node is not waiting, so the answer matches nothing. The kernel would have to make nodes idempotent and restart the whole graph, keeping its own record of finished work. That is most of what our own engine's journal does anyway.
- **The impedance is noticeable.** The run is driven through `runner.Runner`, a session service and `genai.Content` messages. A resume is a `genai.FunctionResponse` named `adk_request_input`. Its user, session and app ids have no meaning for us.
- **A wait at any node** (the subscription window, slice §11) would need every node to be able to raise a request-input interrupt that the kernel answers when the window resets. That's possible, but it uses the human-in-the-loop path for a machine wait.
- **No node hook API.** Tracing goes through our wrapper, which is fine, or through ADK's OpenTelemetry spans.

## 4. Eino (CloudWeGo, v0.9.23): assessed from source, not built

- Checkpoints are written only on interrupt (`graph_run.go`), as in ADK, so there is the same crash gap.
- Graphs are compiled with a fixed shape: parallel branches are wired in advance, so there is **no fan-out with run-time width** and no concurrency cap. Both would live in our own node code.
- **No per-node timeout**: only a graph-wide interrupt timeout (`WithGraphInterruptTimeout`).
- Not yet 1.0.

Three of the six needs would be our own code inside Eino nodes, so it offers less than ADK and was not built.

## 5. Recommendation (taken as decision 44)

**Own minimal engine**, behind the engine interface §6.1 already asks for.

- It is the only candidate that continues after a kernel crash mid-stage. Slice 1's agents run for minutes and may wait hours for a subscription window, so a restart mid-run is a normal event, not an edge case.
- Its state is a plain journal, one line per finished node or item. It is easy to inspect, to diff and to align with the trace store. It can move into SQLite next to the trace store without changing the model.
- It is about 7 times smaller as a binary and has no third-party dependencies.

**What it costs.** We own the durability bugs, which §6.2 already named. The spike engine is a chain with gates and fan-out only. Slice 1 also needs:
- a parallel static branch (the baseline probe alongside `question`);
- a conditional, counted back-edge (`research.* → question`);
- a generic *wait* at any node (subscription window) and *exhausted* (turn or time cap).

My estimate is 500–800 lines with tests. The journal design already supports a generic wait: a node returns a wait error, and the journal records it the same way as a gate.

**When ADK would be the better choice:** if crash recovery mid-stage is not needed in slice 1, and routes, retries and joins from a maintained library are worth 20 MB and the session-event model. The ADK scenario is kept so the choice can be revisited.

## 6. Still open

- If the own engine is chosen: DAG with static parallel branches, counted back-edges, generic wait and exhausted states, and moving the journal into the SQLite trace store.
- At-least-once semantics: an item in flight during a crash runs again. For agents that means a repeated `claude -p` and repeated gateway writes. The tool gateway's compare-and-swap writes (slice §8.3) keep a repeated write from silently overwriting newer work, but the trace will show the repeat. That is acceptable for slice 1.
