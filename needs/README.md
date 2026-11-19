# needs

The needs backbone (decisions 31–36, `docs/needs-backbone.md`): need items written as fenced blocks in Markdown, validated against `needs.toml`, exported as `needs.json`. One core, two hosts: native for agents and checks, WebAssembly for the console.

| Crate | Contents |
|---|---|
| `needs-core` | Parser, model, field and network validation, build, query, diff. No file system, clock or network; everything goes through `api::handle` (JSON in, JSON out) |
| `needs-cli` | The `needs` command: file discovery and output |
| `needs-wasm` | The same API as a WebAssembly module with a plain ABI and no imports; `js/needs.js` loads it |

```
cargo test
cargo build --release -p needs
cargo build --release -p needs-wasm --target wasm32-unknown-unknown
node scripts/parity.mjs        # native and WebAssembly give byte-identical answers
```

## Command line

```
needs lint  -c needs.toml [--repo DIR | --files FILES.json] [--base DIR] [--json] PATH...
needs build (as lint) -o needs.json
needs query (as lint) --type q --status approved --without-incoming answers
needs diff  -c needs.toml [--exit-code] OLD.json NEW.json
needs api   < request.json
```

`--repo DIR` (or `--files`, a JSON map of path to line count) is what `file-exists` rules check evidence against: the repository at the run's commit. Lint and build exit with 1 when there are errors.

## Source format

````markdown
```{fact} The player bar is PlayerBar in src/ui/player.py
:id: F_001
:status: proposed
:answers: Q_001
:evidence: src/ui/player.py:12-40

It reads the current track from the shared store.
```
````

## Configuration

`needs.toml` follows the Sphinx-Needs 7/8 shape (`[[needs.types]]`, `[needs.fields.*]` with JSON schema, `[needs.links.*]`), plus `[[needs.rules]]` for validation across links. Slice 1's configuration and fixtures are in `fixtures/slice1/`.

A rule selects needs by `type`, `status` or any field, then checks one thing:

- `link = "answers"`, `target = {status = "approved"}`, `min`/`max`: outgoing links to matching needs
- `incoming = "answers"`, `min`/`max`: incoming links from matching needs
- `field = "evidence"`, `check = "file-exists"`: `path:line[-line]` points into the repository

## WebAssembly notes

- The module has no imports. getrandom (pulled in by jsonschema through ahash) uses a custom backend on wasm32 (`.cargo/config.toml`, `needs-wasm/src/lib.rs`) instead of JavaScript's crypto; it only seeds hash maps.
- Size: 3.4 MB, 1.0 MB gzipped (mostly the JSON-schema validator and regex tables).
