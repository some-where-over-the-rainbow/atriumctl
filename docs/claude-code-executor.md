# Claude Code as the Agent Executor

**Date:** 2026-11-18
**Status:** Design. Replaces `claude/pi-executor-integration.md` as the executor design; that document is kept for reference (Pi with an API key could return later behind the same executor interface). One spike remains before building (section 9).
**Companions:** `claude/handoff-2026-11-16.md` (entry point), `claude/slice-1-question-research.md`, `claude/architect-console.md`

## 1. Decisions

Taken by the owner on 2026-11-18:

- **41. Models only through the Claude Code subscription, nothing else.**
- **Consequence, confirmed: Claude Code itself is the executor**, replacing Pi (decision 27).
- **42. The spend cap is the subscription's own usage limits.**

**Why Pi cannot stay.** Anthropic's Claude Code legal page reserves subscription OAuth for ordinary use of Claude Code and Anthropic's native applications; developers building products, including with the Agent SDK, are to use API keys. Since February 2026 the terms name third-party harnesses explicitly, and since 4 April 2026 third-party harnesses no longer draw on subscription limits. Pi on the subscription is therefore excluded.

**Use stays personal.** The subscription's advertised limits assume ordinary, individual use. The swarm runs for the owner on their own repository, with modest parallelism (section 6).

## 2. What carries over from the Pi design

- **Placement:** the agent thinks in a harness container and acts in a work sandbox through the tool gateway.
- **Forcing tools through our gateway**, with built-in tools switched off.
- **Harness-neutral hook points** in the definition, mapped to the executor's events.
- **Write layers** (grant, lease, mount, need status) and compare-and-swap at the tool gateway.
- **The executor interface:** start an agent with role, prompt and limits; stream its events; abort; collect the result.

## 3. What changes

| Topic | With Pi (before) | With Claude Code (now) |
|---|---|---|
| Model access | Through our model gateway with a leased virtual key | Directly from the harness container to Anthropic, with the subscription credential |
| Model-call trace | Authoritative, from the gateway | **Self-reported**, from Claude Code's stream and telemetry |
| Budget | Money lease per run, stage, worker | Subscription usage windows, plus turn and time caps per agent |
| Providers | OpenAI, Anthropic, Gemini | Anthropic only |
| Tool bridge | Our TypeScript Pi extension | Our tool gateway as Claude Code's **only MCP server**; no extension code |
| Sub-agents | Pi has none; our `spawn` | Claude Code's own sub-agent tool switched off; our `spawn` via the gateway |

**Not needed now:** Bifrost or any model gateway, the TypeScript bridge, three-provider profiles. The system shrinks to two languages: Go (kernel, tool gateway, console server) and Rust (needs backbone).

## 4. Running Claude Code

One `claude -p` process per agent, in a harness container, driven by the kernel in streaming JSON mode.

```
claude -p
  --output-format stream-json --input-format stream-json --verbose
  --model <alias for the role's tier>
  --setting-sources ""                 # no user, project or local settings
  --strict-mcp-config
  --mcp-config <tool gateway only>     # our tools, nothing else
  --tools ""   or  --disallowedTools <all built-ins>
  --append-system-prompt <stage prompt>   (or --system-prompt, open)
  --max-turns <from the definition>
```

Environment: `CLAUDE_CONFIG_DIR` on a read-only path, `CLAUDE_CODE_OAUTH_TOKEN` (a long-lived token created once with `claude setup-token`), non-essential traffic off, OpenTelemetry export on, pointed at the kernel.

Exact flag names are to be confirmed against the pinned Claude Code version in the spike; the CLI changes often.

## 5. Containment

| Mechanism | Class |
|---|---|
| Built-in tools off, only our MCP server, settings sources empty | soft (inside the process) |
| Tool gateway checks: grant, lease, mount, need status | hard |
| Harness container: no repository, no artifacts, read-only config | hard |
| Harness network: Anthropic API and the tool gateway only | hard |
| Work sandbox: no credential, no network | hard |

**The credential is weaker than before.** The harness container holds the subscription token, not a short-lived leased key. So the harness container holds nothing else, cannot reach anything but Anthropic and the tool gateway, and the token is mounted per run and never written to artifacts or traces. The repository's own `CLAUDE.md` never reaches the agent: the repository is not in the harness container, and settings sources are empty.

## 6. Budget: the subscription as the lease

- **Per agent:** maximum turns and wall time from the definition. These are hard: the kernel aborts.
- **Per run:** maximum parallel agents (default 3), so the swarm stays close to ordinary individual use.
- **Subscription window:** when Claude Code reports a usage limit, the lease is **exhausted with "wait" semantics**: the run pauses, the console shows when the window resets, and the run resumes afterwards. This is the first lease whose exhaustion means waiting rather than stopping.
- **Proxy figures** for the profile: tokens and the cost estimate Claude Code reports, recorded per call. They measure consumption; they do not bill anything.

## 7. Trace

- **`model_call`** now comes from the harness: Claude Code's telemetry events (tokens, cache tokens, model, duration, estimated cost) and its stream (`result` with usage and turn count). Marked `source = harness`, non-authoritative.
- **`tool_call`** stays authoritative from the tool gateway.
- **Reconciliation:** tool calls counted by the gateway must equal tool uses in the stream. Model calls can only be cross-checked between Claude Code's two own reports (telemetry against stream).
- **New `limit_event`:** usage window reached, wait start, resume.

The audit view must show that model data is self-reported. This is the price of the subscription-only decision.

## 8. Hook mapping

| Our hook point | Claude Code mechanism |
|---|---|
| agent start | process start; stream `system` init event |
| before tool call | our tool gateway (all tools are ours); Claude Code hooks are not needed for routing |
| after tool call | tool gateway |
| model call | telemetry events; stream `assistant` messages |
| compaction | stream and telemetry compaction events |
| exhausted | usage-limit message; kernel pauses the run |
| timeout | kernel abort, then container stop |
| agent end | stream `result` event |

Claude Code's own hooks (shell commands on tool and session events) are not used: they would need executable configuration inside the harness, and our tool gateway already sees every tool call.

## 9. Claude Code spike (short, before building)

1. Drive `claude -p` in streaming JSON mode from Go: prompt, events, abort.
2. With built-in tools off and only our MCP server, a request for a shell command or a file read outside our tools fails.
3. Settings sources empty: a `CLAUDE.md` or `.claude/` directory placed in the working directory has no effect.
4. The token works from the harness container; nothing else leaves the container.
5. Telemetry delivers per-call tokens, cache tokens and model id to the kernel.
6. A usage-limit response is recognisable in the stream, so the kernel can pause.
7. Memory and start time for one process and for three in parallel.
8. Built-in sub-agents unavailable; `spawn` through our gateway works.

## Sources

- [Claude Code: legal and compliance](https://code.claude.com/docs/en/legal-and-compliance)
- [The Register: Anthropic clarifies ban on third-party harnesses with Claude subscriptions (2026-02-20)](https://www.theregister.com/2026/02/20/anthropic_clarifies_ban_third_party_claude_access)
- [Third-party harnesses and subscription limits after 4 April 2026](https://paddo.dev/blog/subscription-arbitrage-endgame/)
- [Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference)
- [Claude Code CLI flags, stream-json notes](https://docs.rs/crate/claude-code/0.1.2/source/docs/claude-cli.md)
