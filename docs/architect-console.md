# Architect Console

**Date:** 2026-11-18
**Status:** Design proposal. The owner's main driver for the project (decision 43). A first version is part of slice 1.
**Companions:** `claude/handoff-2026-11-16.md` (entry point), `claude/needs-backbone.md`, `claude/slice-1-question-research.md`, `claude/autonomy-risk-audit.md`

## 1. Purpose

The one interface for the human (decision 10). It shows, live, what the swarm has proposed and what the architect has decided, how the implementation stands against those decisions, and where test coverage is. It is also where the architect acts: approve, reject, edit, steer.

Decision 43 (the owner, 2026-11-18): **the architect console drives the work**: the needs backbone with its WebAssembly linter inside QRSPI, and a live view of implementation and test coverage. Profiling is secondary: one initial signal is enough.

## 2. Views

| View | Shows | Source | From slice |
|---|---|---|---|
| **Run** | Stages, agents, current step, waits (gate, usage window), turns used, wall time | Trace store | 1 |
| **Gate** | Items awaiting decision, item by item: approve, reject, edit, with live lint while editing | Needs model; WebAssembly linter | 1 |
| **Needs** | All items by type and status; links; findings; filters (open questions, unanswered, proposed vs approved) | `needs.json` per snapshot | 1 |
| **Trace graph** | Links as a graph: question → fact; later decision → requirement → spec → test | Needs model, rendered in the browser | 1 |
| **Baseline** | The repository at the run's commit: test count, pass/fail, line and branch coverage per file | Kernel-run probe (section 4) | 1 |
| **Implementation** | Per requirement and spec: which code changed, which tests verify it, whether they pass, coverage of the touched code | Worktree diff, test reports, needs links | 2 |
| **Coverage over time** | Coverage and test results per commit of the worktree, against the baseline | Probe history | 2 |
| **History** | Any view as of an earlier snapshot; `needs diff` between snapshots | Snapshots | 1 (needs), 2 (code) |

## 3. Shape

- **Served by the kernel** (Go) as a local web application: plain HTML, a little JavaScript, and the needs core as a WebAssembly module.
- **Live updates** by server-sent events from the trace store; no polling of agents.
- **One linter everywhere.** The browser runs the same needs core as the agents and the checks, so a finding in the console is a finding at the gate.
- **Writes from the console** go through the kernel under the architect's identity: status changes, edits, gate answers. They become artifact versions and trace events like any other write.
- **Responsive**, since the owner often works from a phone. Reached on the home network only (decision 47).
- **Token login over TLS** (decision 49). On first start the kernel generates a random token and writes it to a file only the user can read (`~/.config/atriumctl/console-token`). Signing in with it sets a `Secure`, `HttpOnly`, `SameSite=Strict` session cookie. Every page and every action requires that cookie, and state-changing requests are POST only. Rotating the token ends all sessions.
- **Self-signed certificate** (decision 49). Proposal for the details: on first start the kernel also generates an ECDSA P-256 key and a self-signed certificate (`~/.config/atriumctl/console-cert.pem` and `console-key.pem`, readable only by the user), valid for the machine's hostname, its home-network addresses and `localhost`. The kernel prints the certificate's SHA-256 fingerprint at start, so the first browser warning on the phone can be checked against it. Installing the certificate on the phone as trusted removes the warning. A new home-network address means regenerating the certificate. The console listens only on HTTPS; there is no plain-HTTP listener.
- **Read-only by default for anything the swarm owns**; the console never edits code.

## 4. Test and coverage probes

Coverage must come from running the tests, not from an agent's account of them.

- A **probe** is a fixed command declared in the definition and run by the kernel in a work sandbox, never by an agent. For `tidal-family`: `pytest` with coverage, writing JSON coverage and a JUnit report.
- In slice 1 it runs once per run, on the pinned commit, as the baseline.
- From slice 2 it runs after each implementation step on the worktree.
- Each probe writes a `probe_result` event: tests, passed, failed, skipped, line and branch coverage, per-file coverage, duration.

## 5. Linking tests to needs

For the implementation view, tests must point at the items they verify.

- **Proposal:** a pytest marker (`@pytest.mark.need("REQ_L4_012")`) or a docstring tag, collected by the probe into the JUnit report.
- The needs tool turns each tagged test into a `test` item with a `verifies` link, so coverage and pass/fail become properties of requirements.
- Lint rule (slice 2): every approved requirement has at least one passing verifying test, unless a deviation statement covers the gap.

This mirrors what Sphinx-Needs users do with test-report imports, kept within our own tool.

## 6. As built (2026-11-18)

`atriumctl console` (`kernel/console`). Views: Run (stage pipeline, agents with outcome, turns, gateway calls against stream tool uses, denials and cost; waits, exhausted stages with accept, retry and abort; live activity), Gate (each proposed question: approve, reject or leave undecided; edit the whole need block with live lint in the browser), Needs (filters, open questions, findings), Trace graph (questions to findings, as SVG), History (needs diff between snapshots, computed in the browser), Baseline (tests passed, failed and skipped, line and branch coverage, failing tests, coverage per file with missing lines).

- **One driver per run.** A file lock on the run directory: the console and the command line never drive the same run at once; the console drives a run after it answers its gate or a decision, or on Resume, and shows which process drives it otherwise.
- **Security.** HTTPS only on loopback and private addresses; token of 100 bits in five groups of four (typable on a phone); failed sign-ins limited to five a minute; sessions stored hashed and kept across restarts; `--rotate-token` ends them all. State-changing requests need a custom header and a same-origin Origin. A strict Content Security Policy, and every text from agents is inserted as text, never as HTML.
- **Reach.** The console also listens on container-bridge addresses on the machine (private, local only).

## 7. Open points

- ~~Reach from a phone.~~ Decided (47): home network only.
- Whether diagrams for the TOGAF control-plane view (decision 9) are rendered in the console or exported.
- How much of the audit view (artifacts, processes, KPIs) joins the console in slice 2.
