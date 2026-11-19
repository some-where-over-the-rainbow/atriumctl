// Slice 1 done-when 7: the WebAssembly build reports exactly what the native
// build reports, on the same fixtures. Sends identical requests to
// `needs api` and to needs_wasm.wasm and compares the responses byte for byte.
//
//   cargo build --release -p needs && cargo build --release -p needs-wasm --target wasm32-unknown-unknown
//   node scripts/parity.mjs

import { readFileSync, readdirSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { join } from "node:path";
import { loadNeeds } from "../crates/needs-wasm/js/needs.js";

const root = new URL("..", import.meta.url).pathname;
const fx = join(root, "fixtures/slice1");
const native = join(root, "target/release/needs");
const wasm = readFileSync(join(root, "target/wasm32-unknown-unknown/release/needs_wasm.wasm"));
const needs = await loadNeeds(wasm);
const config = readFileSync(join(fx, "needs.toml"), "utf8");

const requests = [];
for (const set of readdirSync(fx, { withFileTypes: true }).filter(d => d.isDirectory()).map(d => d.name).sort()) {
  const dir = join(fx, set);
  const sources = readdirSync(dir).filter(f => f.endsWith(".md")).sort()
    .map(f => ({ path: f, text: readFileSync(join(dir, f), "utf8") }));
  const files = JSON.parse(readFileSync(join(dir, "files.json"), "utf8"));
  for (const op of ["lint", "build"]) requests.push([`${set}/${op}`, { op, config, sources, context: { files } }]);
  requests.push([`${set}/query`, { op: "query", config, sources, context: { files },
    query: { type: "q", status: "approved", without_incoming: "answers" } }]);
  requests.push([`${set}/no-files`, { op: "lint", config, sources }]);
}
requests.push(["bad-config", { op: "lint", config: "[needs]\ntypes = []\n", sources: [] }]);

let failed = 0;
for (const [name, req] of requests) {
  const text = JSON.stringify(req);
  const a = execFileSync(native, ["api"], { input: text }).toString().trim();
  const b = needs.handleRaw(text);
  const ok = a === b;
  if (!ok) failed++;
  const r = JSON.parse(b);
  const findings = (r.report?.findings ?? r.findings ?? []).length;
  console.log(`${ok ? "same" : "DIFFERENT"}  ${name.padEnd(18)} ${findings} findings, ${b.length} bytes`);
}
console.log(failed ? `${failed} of ${requests.length} differ` : `all ${requests.length} identical`);
process.exit(failed ? 1 : 0);
