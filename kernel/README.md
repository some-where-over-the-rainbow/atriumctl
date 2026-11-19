# kernel

Go module for the kernel (decision 22): kernel, tool gateway, console server and command line in one binary. Standard library only.

| Package | What it is |
|---|---|
| `cmd/atriumctl` | The command line: `run`, `resume`, `status`, `gate`, `decide`, `runs`, `console` |
| `console` | The architect console: HTTPS with a self-signed certificate, token login, listening only on loopback and home-network addresses; run, gate (approve, reject, edit with live lint), needs, trace graph, history and baseline views; lint and diff in the browser on `needs_wasm.wasm`; drives runs it answers |
| `def` | The swarm definition model: roles, sandboxes, stages, edges, limits, as typed data |
| `qrspi` | Slice 1's definition and its prompts, adapted from QRSPI (MIT, see `qrspi/prompts/README.md`) |
| `run` | A run: directory, repository snapshot, services, and the compiler from definition to engine graph with its stage bodies (agents, needs-clean checks with fix rounds, needs snapshots, gate application, question batching) |
| `engine` | Graph engine (decision 44): DAG with counted back-edges, gates, map fan-out with per-node and run-wide caps, per-node timeouts, *wait* and *exhausted* states; append-only journal, continues after a crash |
| `gateway` | Tool gateway, Claude Code's only MCP server: `read`, `ls`, `glob`, `grep`, `write`, `edit`, `needs_lint`, `needs_query`, `spawn`. One endpoint per agent with a random token. Writes pass grant, lease, mount, compare-and-swap and the need-status rule; every call is a `tool_call` trace event |
| `executor` | One `claude -p` per agent on the host or in a harness container; outcomes from `terminal_reason` and the API error status; `EngineError` maps them to the engine's wait and exhausted |
| `probe` | Kernel-run test probes: builds the probe image (base plus the repository's test dependencies, once per content hash), runs pytest with coverage on a copy of the snapshot in a container without network, parses JUnit and coverage.py JSON |
| `egress` | The harness containers' only way out: CONNECT proxy, allowlist `api.anthropic.com:443` |
| `telemetry` | OTLP receiver: Claude Code's `api_request` events become `model_call` trace events, with account identity dropped |
| `trace` | Trace events (slice §13) and sinks; the SQLite store comes later |
| `cmd/atriumctl-shim` | The harness container's entrypoint: bridges loopback ports to the host's proxy and kernel sockets, loads the token file |
| `harness/` | `build.sh` builds `atriumctl-harness:<claude version>` |
| `integration` | Live test with real agents in containers (`ATRIUMCTL_LIVE=1`) |

```
go build -o bin/atriumctl ./cmd/atriumctl
atriumctl run --repo ../tidal-family --commit 91bc49f --task "..." [--gate auto] [--models large=haiku,small=haiku]
atriumctl gate RUN --approve all            # or item by item: --approve Q_001,Q_002 --reject Q_003
atriumctl status RUN; atriumctl resume RUN; atriumctl decide RUN STAGE accept|retry|abort
atriumctl console                           # https://<home address>:8443/, token in ~/.config/atriumctl/console-token

go test -race ./...                                   # needs ../needs built for the gateway's needs tests
./harness/build.sh                                    # harness image
ATRIUMCTL_LIVE=1 go test -v ./integration/                  # real agents; a little subscription usage (haiku)
```
