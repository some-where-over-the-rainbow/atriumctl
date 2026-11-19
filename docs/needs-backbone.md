# Needs Backbone: Symbolic Product Model in Rust

**Date:** 2026-11-18 (revised the same day: enters in slice 1; Rust confirmed for WebAssembly)
**Status:** Direction decided, design proposed. Enters in slice 1 with a subset (section 5).
**Companions:** `claude/handoff-2026-11-16.md` (entry point), `claude/slice-1-question-research.md`, `claude/autonomy-risk-audit.md`

## 1. Decisions

Taken by the owner on 2026-11-18:

- **31. A symbolic representation of the key items that are decided or defined**, usable from the spec definition phase onward and throughout the process, as a lintable backbone and common ground between human and machine. Modelled on Sphinx-Needs.
- **32. Only product items.** The backbone holds what the swarm decides and builds. The swarm definition itself (stages, leases, sandboxes, autonomy) stays in the definition model.
- **33. No Python.** Sphinx and Sphinx-Needs themselves are not run.
- **34. No ubc / ubCode.** Instead, an own **Rust** backbone.
- **35. The backbone enters in slice 1.**
- **36. Rust is kept, because WebAssembly is a strong fit:** the same linter runs natively for agents and checks, and in the browser in the architect's console.

## 2. Consequence: we build a needs engine, we keep the Sphinx-Needs model

Sphinx-Needs is a Python extension to Sphinx, so "no Python" means implementing the parts we need ourselves. What we keep from Sphinx-Needs is the **contract**, not the code:

| Kept as contract | Why |
|---|---|
| The need model: id, type, title, status, tags, typed fields, typed links with incoming names | Proven in safety domains (Eclipse S-CORE); familiar to engineers |
| Declarative configuration of fields and links with JSON-schema types (the Sphinx-Needs 7/8 `[needs.fields.*]`, `[needs.links.*]` shape in a TOML file) | Readable for humans; validatable itself |
| Schema validation, local and network (validation across links) | This is the lint |
| `needs.json` as the machine export | Agents and the kernel read one file; existing tools can consume it |
| Directive syntax in the source files | Escape hatch: the same files would still build in real Sphinx-Needs if ever wanted |

Sphinx-Needs moved its own core validation from the Python `jsonschema` package to `jsonschema-rs` in version 6.2. The Rust `jsonschema` crate is therefore the same validator the reference implementation uses. Sphinx-Needs' own rules on top (type injection, select filters, network validation) are reimplemented by us.

## 3. The Rust tool

Working name: `needs`.

### 3.1 Crate layout, shaped by WebAssembly

| Crate | Contents | Targets |
|---|---|---|
| `needs-core` | Parser, model, schema and network validation, query, diff. **No file system, no clock, no network**: input is a set of (path, text) pairs plus the configuration; output is the model and findings | native and `wasm32` |
| `needs-cli` | File discovery, `needs.json` writing, command line | native |
| `needs-wasm` | Thin bindings for the browser: lint a document set, query, render | `wasm32` |

The I/O-free core is what makes the native and browser results identical: one implementation, two hosts. It also makes the core easy to test and keeps it safe to run anywhere.

### 3.2 Commands

| Command | Does |
|---|---|
| `needs lint` | Parse sources, validate fields and links against the schema, report findings with file and line. Fast enough to run after every agent write |
| `needs build` | Lint, then write `needs.json` (reproducible, sorted, with schema and source hash) |
| `needs query` | Filter and traverse: "requirements of level L3 without a verifying test", "decisions superseded since commit X" |
| `needs diff` | Change between two `needs.json`: added, removed, status changes, broken links |
| `needs render` | Minimal HTML or Markdown views for the architect: tables, trace matrix, a link graph as Mermaid or PlantUML text |

**Deliberately not built:** a full reStructuredText or Sphinx implementation, themes, extensions. The tool understands need blocks and nothing else; the surrounding prose is passed through.

### 3.3 Source format: Markdown with fenced need blocks

QRSPI artifacts are Markdown, and Pi and the models write Markdown best. Need items are written as MyST-style fenced directives:

````
```{q} Where are leases charged today?
:id: Q_014
:status: proposed
:area: billing

Which component decrements a budget when a model call completes?
```
````

- Parsed with a CommonMark parser crate with source positions (comrak, or pulldown-cmark); only fenced blocks whose info string is a configured need type are interpreted. Both compile to WebAssembly.
- Field lines (`:name: value`) are parsed by our own small parser; the body is kept as Markdown.
- reStructuredText input is out of scope. Existing Rust RST parsers are partial, and Sphinx Ultra (a Rust Sphinx builder) is young, single-maintainer and not Sphinx-Needs aware, so neither is a base.

### 3.4 Configuration

One `needs.toml`, in the Sphinx-Needs 7/8 shape: need types, `[needs.fields.*]` and `[needs.links.*]` with schema, id pattern, plus network rules. Validated itself against a meta-schema before use.

## 4. Product need types

| Type | Holds | Typical links | From slice |
|---|---|---|---|
| `q` | An open question (QRSPI Question stage; later grilling and wayfinder) | — | 1 |
| `fact` | A finding about the existing system, with file and line evidence (QRSPI Research) | answers (q) | 1 |
| `dec` | A decision with its reason | supersedes, resolves (q), based-on (fact) | later |
| `req` | A requirement, with `level` L1 to L5 | derives (dec, req of the level above) | later |
| `spec` | A specification element (interface, contract, component) | satisfies (req) | later |
| `test` | A test or check, with `level` | verifies (req, spec) | later |
| `risk` | A risk with class R0 to R3 | concerns (req, spec) | later |
| `dev` | A deviation statement for a shortcut | deviates-from (the rule), compensated-by (test) | later |
| `disp` | A dispensation granted by the architect | covers (dev) | later |
| `proto` | A prototype finding from the across direction | informs (dec, req) | later |

Example network rules for later slices, each a lint finding when broken:
- Every `req` at L2 and below derives from a `req` one level up or from a `dec`.
- Every approved `req` has at least one `test` that verifies it, unless a `dev` covers the gap.
- A `dev` at risk class R2 is covered by a `disp`.
- A superseded `dec` is not the target of a new `derives` link.

These rules are where the V-model, the governance triple and the shortcut policy become checkable.

## 5. In slice 1

| Stage | Writes | Needs |
|---|---|---|
| Question | `questions.md` | One `q` per question, status `proposed` |
| Gate | — | The architect's approval sets the approved questions to `approved`; rejected ones to `rejected`. Written by the kernel under the architect's identity |
| Research | `research.md` | `fact` items, each with an `answers` link to a `q` and an `evidence` field (file and line) |

Rules in slice 1:
- Ids follow `Q_nnn` and `F_nnn`; agents assign them; lint checks pattern and uniqueness.
- Every `fact` answers at least one approved `q`.
- Every approved `q` is answered by at least one `fact`, or the stage reports it as open.
- `evidence` points at a file that exists in the repository at the run's commit.
- `task.md` stays plain prose, outside the backbone, so the research barrier is unaffected: research agents see `q` items, never the task.

Original QRSPI artifacts are plain Markdown; ours carry need blocks. This changes what the agents write and is recorded as a deviation from the seed workflow when comparing profiles.

## 6. Place in the system

- **Files** live with the artifacts; agents write them through the tool gateway like any artifact.
- **Lint as a check.** The swarm definition gets a check class `needs-clean` (zero errors, warnings as configured). A stage is complete only when its artifacts are needs-clean.
- **Lint as a tool.** Agents get read-only `needs_lint` and `needs_query` tools, executed by the tool gateway in the work sandbox. Self-correction happens before a gate, not at it.
- **Authority.** `needs.json` is produced by `needs build` in a work sandbox, never written by an agent. The kernel reads only that file.
- **Status is guarded.** Field-level write policy at the tool gateway: agents may set `draft` and `proposed`; only the architect sets `approved`, `rejected` or `superseded`. The backbone thereby shows exactly what is decided and what is only proposed.
- **Trace.** Each `needs build` writes a `needs_snapshot` event (source hash, counts per type and status, findings). `needs diff` between snapshots feeds the audit's artifact view.
- **Console (later).** `needs-wasm` lints and renders in the browser while the architect edits at a gate, with exactly the findings the agents and the checks see.

## 7. Open points

- Rendering depth for the architect: tables and graph text only, or a browsable site.
- Whether network rules stay in JSON-schema form or get a small rule language of their own. **Built as a small declarative form** (`[[needs.rules]]`: select, then a link count, an incoming-link count, or a file check), which covers slice 1's rules. Open for the owner to confirm; JSON-schema network validation in the Sphinx-Needs 8 style can still be added.
- Whether the gate edits of the architect go through the same tool gateway path as agent writes, or a separate kernel path (slice 1 assumes the kernel path).

## Sources

- [Sphinx-Needs schema validation (8.5)](https://sphinx-needs.readthedocs.io/en/8.5.0/schema/index.html)
- [Sphinx-Needs changelog (7.0, 6.2: switch to jsonschema-rs)](https://sphinx-needs.readthedocs.io/en/7.0.0/changelog.html)
- [Sphinx-Needs builders and needs.json](https://sphinx-needs.readthedocs.io/en/8.0.0/builders.html)
- [jsonschema crate (Rust)](https://docs.rs/jsonschema)
- [Sphinx Ultra (Rust Sphinx builder)](https://docs.rs/crate/sphinx-ultra/latest/source/README.md)
- [rst_parser crate](https://lib.rs/crates/rst_parser)
- [Eclipse S-CORE docs-as-code: schema generation for Sphinx-Needs](https://github.com/eclipse-score/docs-as-code/pull/394)
