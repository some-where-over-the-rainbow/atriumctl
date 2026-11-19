# Claude Code Spike: Findings

**Date:** 2026-11-18
**Status:** Done, with one partial item. Every §9 check passes on the host and in a rootless Podman harness container. The exception is the shape of a rejected usage window, which could not be provoked. Code: `spikes/claude-code/`.
**Companions:** `docs/claude-code-executor.md` (§9 defines the spike), `docs/slice-1-question-research.md`

## 1. Setup

- Claude Code **2.1.327**, model alias `haiku` (reported as `claude-haiku-5-5`), Go 1.27.4, Ubuntu 24.04, 4 cores, 7 GB RAM, Podman 4.9.3 rootless with **runc** (crun is not installed; decision 24 asks for crun).
- A Go tool gateway served as the only MCP server, over streamable HTTP, at one URL per agent (`/mcp/<agent>`), so the gateway knows the caller without trusting the model.
- **Host mode:** agents ran as host processes with a minimal environment and the logged-in credentials.
- **Container mode:** see §3. It uses a long-lived token from `claude setup-token`.

Flags used for every agent:

```
claude -p --output-format stream-json --input-format stream-json --verbose
  --model haiku --tools "" --strict-mcp-config --setting-sources ""
  --disable-slash-commands --no-session-persistence --permission-prompts none
  --mcp-config '{"mcpServers":{"gw":{"type":"http","url":".../mcp/<agent>"}}}'
  --allowedTools mcp__gw__read,mcp__gw__ls,mcp__gw__spawn
  --append-system-prompt <role> --max-turns <n>
```

## 2. Results against §9

| § | Check | Host | Container | Evidence |
|---|---|---|---|---|
| 9.1 | Drive from Go: prompt, events, abort | **Pass** | **Pass** | Result after ~2 s (host) or ~2.5 s (container). Abort: SIGTERM to the process group; the host process is gone in 34 ms, the container in 263 ms and removed; no `result` event |
| 9.2 | Built-ins off, gateway only | **Pass** | **Pass** | `init.tools` = only `mcp__gw__*`. Shell: no tool. `/etc/hostname` and `../outside.txt` denied at the gateway (`mount`). The canary outside the tree never appeared |
| 9.3 | `CLAUDE.md`, `.claude/`, `.mcp.json` in cwd have no effect | **Pass** | n/a (empty cwd) | With `--setting-sources ""`: no code word, no project agent. A control run with `project` shows both, so the canaries work. `.mcp.json` was ignored in both (`--strict-mcp-config`) |
| 9.4 | Token in a container; nothing else leaves it | — | **Pass** | Direct TCP and DNS from the container are unreachable. All egress (33 CONNECTs in one full run) went to `api.anthropic.com:443` through the proxy; nothing else was attempted. The token is in no stream event, stderr, gateway call, telemetry record, egress log or work-tree file |
| 9.5 | Telemetry: per-call tokens, cache, model | **Pass** | **Pass** | One `api_request` log event per model call; token sums equal the stream's `modelUsage` exactly |
| 9.6 | Usage limit recognisable in the stream | **Partial** | — | `rate_limit_event` arrives on every run with status and utilization per window (§4.4). A rejected window was not provoked |
| 9.7 | Memory and start time, 1 and 3 parallel | **Pass** | **Pass** | Host: ~235 MB process RSS, init 0.45 s. Container: ~130–140 MB cgroup peak, init ~0.95 s; three in parallel init ~1.6 s, 393 MB total, 3.3 s wall |
| 9.8 | No built-in sub-agents; `spawn` through the gateway | **Pass** | **Pass** | No `Task`/`Agent` tool; the parent called `spawn`, the kernel started a child agent (a container in container mode) and the parent reported its answer |
| slice 16.6 | Turn cap ends the agent with a defined outcome | **Pass** | — | `--max-turns 1`: `subtype=error_max_turns`, `terminal_reason=max_turns`, `is_error=true` |

The host RSS and container cgroup figures measure different things (process RSS against cgroup memory), so they are not directly comparable.

## 3. Container shape that worked

```
host                                         harness container (--network none)
─────────────────────────────────────        ──────────────────────────────────────
kernel.sock  (gateway + OTLP receiver) ◄──── 127.0.0.1:8080 ┐
proxy.sock   (CONNECT allowlist:        ◄──── 127.0.0.1:3128 ┤ atriumctl-shim (PID 1)
              api.anthropic.com:443)                         │   loads token from
token file   (mounted :ro) ────────────────► /run/secrets/   │   /run/secrets, sets
empty dir    (mounted :ro) ────────────────► /config         │   HTTPS_PROXY, execs
                                                             └─► claude -p …
```

- `podman run --rm -i --network none --read-only --userns keep-id --cap-drop all --security-opt no-new-privileges --pids-limit 512`, with tmpfs for `HOME`, `/work` and `/tmp`, and `CLAUDE_CONFIG_DIR` on an empty **read-only** mount. Claude Code ran fine with a read-only config directory.
- With no network interface, containment needs no firewall rules, no netavark policy and no root. The only way out is the two Unix sockets, and the host decides what each one reaches.
- The token is a read-only file, not an environment variable passed to `podman run`, so it does not show up in `podman inspect`. The shim reads it into Claude Code's environment only. With `--userns keep-id`, the host user can still read that process's environment, but that user already owns the token.
- Image `atriumctl-harness:2.1.327`: `debian:13-slim`, `ca-certificates`, the Claude Code binary (356 MB, glibc) and the static shim; 462 MB in total. The Claude Code version is a label and a tag. Built by `spikes/claude-code/image/build.sh`.

## 4. Findings that change or sharpen the design

1. **`--max-turns` works but is not in `--help`.** The cap is enforced, but `num_turns` in the result read 2 with a cap of 1, and one tool call still went through. The kernel should enforce caps from its own count (gateway calls, assistant messages) and treat `error_max_turns` as the executor's signal, not as the exact count.
2. **Judge outcomes by `terminal_reason`, not `subtype`.** An invalid token produced `subtype: "success"` with the error as the result text. The reliable fields are `is_error: true`, `terminal_reason: "api_error"`, `api_error_status: 401` and `api_error_type`. The kernel's outcome mapping should key on `terminal_reason` (`completed`, `max_turns`, `api_error`, …) and `api_error_status`. A usage-limit stop will most likely surface the same way, as a 429.
3. **`--bare` cannot be used.** It refuses OAuth and accepts only `ANTHROPIC_API_KEY`, so it is ruled out under decision 41. Containment instead relies on `--tools ""`, `--strict-mcp-config`, `--setting-sources ""` and `--disable-slash-commands`.
4. **The usage window is visible before it runs out.** `rate_limit_event.rate_limit_info` carries `status`, `rateLimitType` (`five_hour`), `resetsAt` (epoch seconds) and, under `unifiedWindows`, `utilization` for `five_hour` and `seven_day`. The kernel can act *before* a rejection: for example, stop starting new agents above a utilization threshold and show `resetsAt` in the console. Proposal: wait semantics trigger on `status != allowed`, on `api_error_status` 429, or on a configurable utilization level.
5. **Built-in plugins still load.** Even with empty setting sources, `init` lists five built-in plugins and five agent definitions, and telemetry shows `plugin_loaded` events. None of them exposes a tool, so the hard layers are unaffected, but the soft layer is not empty. On every Claude Code upgrade, re-check that `init.tools` contains only our gateway tools; a test should fail if it doesn't.
6. **Startup traffic stays inside the allowlist.** At start, Claude Code fetches remote managed settings before telemetry initialises. With `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`, every connection it made went to `api.anthropic.com`, and the proxy denied nothing.
7. **Telemetry carries account identity.** Every `api_request` event includes `user.email`, `user.account_uuid`, `organization.id` and `anthropic.plan`. The token itself is not sent. The trace store should drop these attributes on receipt (or move them to the content table) so they don't spread through traces and profiles.
8. **Telemetry is richer than the stream for `model_call`.** Per call it gives `request_id`, `ttft_ms`, `duration_ms`, `cost_usd`, cache tokens split into 1 h and 5 min, `effort` and `speed`. Proposal: `model_call` rows come from telemetry, and the stream is used for reconciliation. In a container the OTLP endpoint must be the shim's loopback port. The exporter does not go through the proxy.
9. **Reconciliation holds and is cheap.** In every run, stream `tool_use` blocks equalled gateway calls for that agent, and telemetry `api_request` count equalled distinct assistant message ids. Gateway records must be keyed by a unique agent id per run.
10. **The gateway can be small.** Claude Code needed only `initialize`, `notifications/initialized`, `tools/list` and `tools/call` over HTTP POST with plain JSON responses: no SSE, no sessions.
11. **Footprint.** A container adds about 0.5 s to startup (0.95 s against 0.45 s) and fits in ~140 MB per agent. Three parallel agents, the design's cap, use under 0.4 GB. That fits easily on the proposed control machine and on this one.
12. **Abort leaves no `result`.** On abort or timeout the kernel has to synthesise the agent's outcome (`aborted`, partial) itself. `podman run`'s signal proxy and the shim's signal forwarding together stop the container in ~0.26 s, and `--rm` removes it.
13. **`--debug` logs to a file, not stderr.** Use `--debug-file` on a writable mount when diagnosing a container run.

## 5. Still open

- **Rejected usage window**: confirm the exact stream and result shape (expected: `api_error_status` 429 and a `rate_limit_event` whose status is not `allowed`). Capture it the first time it happens naturally; the executor should log both.
- **crun**: install it and re-run the container checks with `--runtime crun`, to match decision 24 and to compare start time.
- **System prompt, append or replace** (slice §15.5): only append was used. Replacing it is a one-flag change and worth an A/B on a real stage prompt.
- **Larger models**: all runs used `haiku`. Repeat drive and spawn once with `sonnet` and `opus` before slice 1 runs.
- **Token lifetime and rotation**: the long-lived token's expiry and how the kernel notices it (a 401 surfaces as in §4.2).
