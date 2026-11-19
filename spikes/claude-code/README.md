# Claude Code executor spike

Checks from `docs/claude-code-executor.md` §9 against the local `claude` binary. Findings: `docs/spike-claude-code.md`.

```
go run . [drive abort contain settings telemetry ratelimit turncap resources spawn]
```

- `gateway.go`: minimal MCP tool gateway (streamable HTTP, JSON responses), one URL per agent, tools `read`, `ls`, `spawn`, confined to a temp tree
- `executor.go`: one `claude -p` per agent over stream-json, clean environment, process-group abort, RSS sampling
- `otel.go`: OTLP/HTTP-JSON receiver for Claude Code telemetry
- `checks.go`: the checks; each prints PASS, FAIL or PARTIAL with notes

Optional environment: `SPIKE_TOKEN_FILE` (container token file; default `~/.config/atriumctl/claude-token`, from `claude setup-token`), `SPIKE_OAUTH_TOKEN` / `SPIKE_CONFIG_DIR` (host mode), `SPIKE_TMP` (temp parent), `SPIKE_KEEP=1` (keep the temp dir), `SPIKE_CLAUDE_ARGS` (extra flags, e.g. `--debug-file /run/atriumctl/claude-debug.log`), `SPIKE_STDERR_DIR`. Container checks (`ctr-*`) need the image: `./image/build.sh`.
