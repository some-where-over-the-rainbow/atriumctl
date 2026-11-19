// Architect console. Everything agents wrote is inserted as text, never as
// HTML. Lint, query and diff run in the browser on needs_wasm.wasm: the same
// core the agents and checks use.
"use strict";

const app = document.getElementById("app");
const crumb = document.getElementById("crumb");
const logoutBtn = document.getElementById("logout");

// ---- DOM helper ---------------------------------------------------------

function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "class") el.className = v;
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const kid of kids.flat()) {
    if (kid === null || kid === undefined || kid === false) continue;
    el.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
  }
  return el;
}
const svgNS = "http://www.w3.org/2000/svg";
function s(tag, attrs, ...kids) {
  const el = document.createElementNS(svgNS, tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v);
  }
  for (const kid of kids.flat()) if (kid !== null && kid !== undefined) el.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
  return el;
}
const badge = (state, label) => h("span", { class: "badge s-" + state }, label || state);
const fmtTime = (t) => { const d = new Date(t); return isNaN(d) || d.getFullYear() < 2000 ? "" : d.toLocaleString([], { dateStyle: "short", timeStyle: "short" }); };
const fmtClock = (t) => new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
const fmtMs = (ms) => ms >= 60000 ? `${Math.floor(ms / 60000)}m ${Math.round((ms % 60000) / 1000)}s` : `${Math.round(ms / 1000)}s`;
const short = (str, n) => str.length > n ? str.slice(0, n - 1) + "…" : str;

// ---- API ------------------------------------------------------------------

class AuthError extends Error {}

async function api(path, opts = {}) {
  const init = { method: opts.method || "GET", headers: { "X-Atriumctl": "1" }, credentials: "same-origin" };
  if (opts.body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(opts.body);
  }
  const r = await fetch(path, init);
  if (r.status === 401 && path !== "/login") { showLogin(); throw new AuthError("sign in"); }
  const type = r.headers.get("Content-Type") || "";
  const data = type.includes("json") ? await r.json() : await r.text();
  if (!r.ok) throw new Error((data && data.error) || r.statusText);
  return data;
}

// ---- needs core (WebAssembly) ---------------------------------------------

let needsP = null;
function needs() {
  if (!needsP) {
    needsP = (async () => {
      const resp = await fetch("needs_wasm.wasm", { credentials: "same-origin" });
      if (!resp.ok) throw new Error("needs_wasm.wasm is not available on this console");
      const { instance } = await WebAssembly.instantiate(await resp.arrayBuffer(), {});
      const { memory, needs_alloc, needs_handle, needs_free } = instance.exports;
      const enc = new TextEncoder(), dec = new TextDecoder();
      return {
        handle(req) {
          const bytes = enc.encode(JSON.stringify(req));
          const ptr = needs_alloc(bytes.length);
          new Uint8Array(memory.buffer, ptr, bytes.length).set(bytes);
          const packed = needs_handle(ptr, bytes.length);
          const rptr = Number(packed >> 32n), rlen = Number(packed & 0xffffffffn);
          const text = dec.decode(new Uint8Array(memory.buffer, rptr, rlen));
          needs_free(rptr, rlen);
          return JSON.parse(text);
        },
      };
    })();
    needsP.catch(() => { needsP = null; });
  }
  return needsP;
}

// The run's need sources, as agents see them, for linting in the browser.
async function needsInput(id, override = {}) {
  const [config, files, q, r] = await Promise.all([
    api(`/api/runs/${id}/artifacts/needs.toml`), api(`/api/runs/${id}/files`),
    api(`/api/runs/${id}/artifacts/questions.md`), api(`/api/runs/${id}/artifacts/research.md`),
  ]);
  const text = { "questions.md": q, "research.md": r, ...override };
  return {
    config, files, text,
    sources: [{ path: ".atriumctl/questions.md", text: text["questions.md"] }, { path: ".atriumctl/research.md", text: text["research.md"] }],
  };
}

async function lint(id, override) {
  const inp = await needsInput(id, override);
  const core = await needs();
  const resp = core.handle({ op: "lint", config: inp.config, sources: inp.sources, context: { files: inp.files } });
  return { ...inp, report: resp.report };
}

// The need block with :id: id: [first line, last line] (same rule as the kernel).
function blockRange(lines, id) {
  let start = -1, has = false;
  for (let i = 0; i < lines.length; i++) {
    const t = lines[i].trim();
    if (t.startsWith("```{") && start < 0) { start = i; has = false; }
    else if (t === "```" && start >= 0) { if (has) return [start, i]; start = -1; }
    else if (start >= 0 && t === ":id: " + id) has = true;
  }
  return null;
}
function blockOf(text, id) {
  const lines = text.split("\n"), r = blockRange(lines, id);
  return r ? lines.slice(r[0], r[1] + 1).join("\n") : "";
}
function replaceBlock(text, id, block) {
  const lines = text.split("\n"), r = blockRange(lines, id);
  if (!r) return text;
  lines.splice(r[0], r[1] - r[0] + 1, ...block.replace(/\n+$/, "").split("\n"));
  return lines.join("\n");
}

// ---- login ----------------------------------------------------------------

function showLogin() {
  closeEvents();
  logoutBtn.hidden = true;
  crumb.textContent = "";
  app.replaceChildren(document.getElementById("login-tpl").content.cloneNode(true));
  const form = document.getElementById("login-form");
  const err = document.getElementById("login-error");
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    err.textContent = "";
    try {
      await api("/login", { method: "POST", body: { token: form.token.value } });
      route();
    } catch (x) { err.textContent = x.message; }
  });
  form.token.focus();
}
logoutBtn.addEventListener("click", async () => { await api("/logout", { method: "POST", body: {} }).catch(() => {}); showLogin(); });

// ---- routing ----------------------------------------------------------------

let current = null; // { id, tab, state }
let events = null;

function closeEvents() { if (events) { events.close(); events = null; } }

async function route() {
  const m = location.hash.match(/^#\/run\/([0-9a-f-]+)(?:\/(\w+))?/);
  try {
    await api("/api/me");
  } catch (e) { if (e instanceof AuthError) return; }
  logoutBtn.hidden = false;
  if (m) return runView(m[1], m[2] || "run");
  closeEvents();
  current = null;
  return runsView();
}
window.addEventListener("hashchange", route);

function fail(where, e) {
  if (e instanceof AuthError) return;
  where.replaceChildren(h("p", { class: "error" }, e.message));
}

// ---- runs -------------------------------------------------------------------

async function runsView() {
  crumb.textContent = "";
  try {
    const runs = await api("/api/runs");
    app.replaceChildren(
      h("h1", {}, "Runs"),
      runs.length === 0 ? h("p", { class: "muted" }, "No runs yet. Start one with ", h("code", {}, "atriumctl run"), ".") :
        h("div", { class: "runs" }, runs.map((r) =>
          h("a", { class: "card stack", href: `#/run/${r.id}` },
            h("div", { class: "row spread" }, h("span", { class: "mono" }, r.id), badge(r.state)),
            h("div", { class: "task" }, short(r.task, 160)),
            h("div", { class: "muted mono" }, `${r.repo} @ ${r.commit.slice(0, 9)}`)))),
    );
  } catch (e) { fail(app, e); }
}

// ---- one run ------------------------------------------------------------------

const tabs = [["run", "Run"], ["gate", "Gate"], ["needs", "Needs"], ["graph", "Trace graph"], ["history", "History"], ["baseline", "Baseline"]];

async function runView(id, tab) {
  const same = current && current.id === id;
  if (!same) {
    closeEvents();
    current = { id, tab, state: null, feed: [], gate: {} };
    events = new EventSource(`/api/runs/${id}/events`);
    events.addEventListener("trace", (ev) => onTrace(ev.data));
  }
  current.tab = tab;
  crumb.textContent = id;
  await refresh(true);
}

let refreshTimer = null;
function scheduleRefresh() {
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(() => refresh(false), 600);
}

function onTrace(line) {
  let ev;
  try { ev = JSON.parse(line); } catch { return; }
  current.feed.unshift(ev);
  current.feed.length = Math.min(current.feed.length, 200);
  const feed = document.getElementById("feed");
  if (feed) feed.prepend(feedLine(ev));
  if (["stage", "agent", "gate", "needs_snapshot", "limit_event", "run", "decision", "artifact", "probe_result"].includes(ev.type)) scheduleRefresh();
}

// refresh reloads the state; the run tab re-renders, other tabs keep what
// the architect is editing and only update the header.
async function refresh(full) {
  if (!current) return;
  const { id } = current;
  let st;
  try { st = await api(`/api/runs/${id}/state`); } catch (e) { return fail(app, e); }
  if (!current || current.id !== id) return;
  current.state = st;
  if (full || current.tab === "run") {
    const body = h("div", { id: "tab" });
    app.replaceChildren(header(st), tabBar(st), body);
    const fn = { run: tabRun, gate: tabGate, needs: tabNeeds, graph: tabGraph, history: tabHistory, baseline: tabBaseline }[current.tab] || tabRun;
    try { await fn(st, body); } catch (e) { fail(body, e); }
  } else {
    const hd = document.getElementById("run-header");
    if (hd) hd.replaceWith(header(st));
    const tb = document.getElementById("tabbar");
    if (tb) tb.replaceWith(tabBar(st));
  }
}

function header(st) {
  const driver = st.driven_here ? "driven by this console" : st.driver_pid ? `driven by process ${st.driver_pid}` : "idle";
  const canResume = !st.driver_pid && !["done", "aborted"].includes(st.engine_state);
  return h("section", { id: "run-header", class: "card stack" },
    h("div", { class: "row spread" },
      h("div", { class: "row" }, badge(st.state), h("span", { class: "muted" }, driver)),
      h("div", { class: "row" },
        canResume ? h("button", { class: "small", onclick: () => act("resume", {}) }, "Resume") : null,
        st.driven_here ? h("button", { class: "small danger", onclick: () => act("stop", {}) }, "Stop") : null)),
    h("div", { class: "mono muted" }, `${st.repo.split("/").pop()} @ ${st.commit.slice(0, 12)} · gate ${st.gate_mode}` +
      (st.models && Object.keys(st.models).length ? " · " + Object.entries(st.models).map(([k, v]) => `${k}=${v}`).join(" ") : "")),
    h("details", {}, h("summary", { class: "muted" }, "Task"), h("p", { class: "task" }, st.task)),
    st.last_error ? h("p", { class: "error" }, st.last_error) : null);
}

function tabBar(st) {
  const counts = { gate: (st.gates || []).length };
  return h("nav", { id: "tabbar", class: "tabs" }, tabs.map(([k, label]) =>
    h("a", { href: `#/run/${st.id}/${k}`, "aria-current": current.tab === k ? "page" : null },
      label, counts[k] ? h("span", { class: "count" }, "●") : null)));
}

async function act(what, body) {
  try {
    await api(`/api/runs/${current.id}/${what}`, { method: "POST", body });
  } catch (e) { if (!(e instanceof AuthError)) alert(e.message); }
  setTimeout(() => refresh(true), 300);
}

// ---- Run tab ------------------------------------------------------------------

function tabRun(st, el) {
  const parts = [];
  parts.push(h("div", { class: "pipeline" }, st.stages.map((x) =>
    h("span", { class: "step" }, h("span", { class: `stage s-${x.state}`, title: `${x.kind}, ${x.state}` },
      x.name, x.attempt > 1 ? h("small", {}, `#${x.attempt}`) : null)))));
  for (const g of st.gates || []) {
    parts.push(h("div", { class: "card alert row spread" },
      h("span", {}, "The gate waits for your decision on the proposed questions."),
      h("a", { href: `#/run/${st.id}/gate` }, h("button", { class: "primary small" }, "Open gate"))));
  }
  for (const x of st.exhausted || []) {
    parts.push(h("div", { class: "card alert stack" },
      h("h3", {}, `Stage ${x.Node} is exhausted (attempt ${x.Attempt})`),
      h("pre", {}, x.Reason),
      h("div", { class: "row" },
        h("button", { class: "small", onclick: () => act("decide", { stage: x.Node, decision: "accept" }) }, "Accept partial"),
        h("button", { class: "small", onclick: () => act("decide", { stage: x.Node, decision: "retry" }) }, "Retry"),
        h("button", { class: "small danger", onclick: () => confirm("Abort the run?") && act("decide", { stage: x.Node, decision: "abort" }) }, "Abort run"))));
  }
  for (const w of st.waits || []) {
    parts.push(h("div", { class: "card alert wait" }, `Stage ${w.Node} waits until ${fmtTime(w.Until)}: ${w.Reason}`));
  }
  for (const f of st.failed || []) {
    parts.push(h("div", { class: "card alert bad stack" }, h("h3", {}, `Stage ${f.Node} failed`), h("pre", {}, f.Err),
      h("p", { class: "muted" }, "Resume retries it.")));
  }
  const totals = st.agents.reduce((a, x) => ({ cost: a.cost + x.cost, tools: a.tools + x.tool_calls, denied: a.denied + x.denied }), { cost: 0, tools: 0, denied: 0 });
  parts.push(h("h2", {}, `Agents (${st.agents.length})`),
    h("p", { class: "muted" }, `${totals.tools} tool calls, ${totals.denied} denied` +
      (Object.keys(st.denials || {}).length ? ` (${Object.entries(st.denials).map(([k, v]) => `${k} ${v}`).join(", ")})` : "") +
      `, cost estimate $${totals.cost.toFixed(4)} (self-reported), egress denied ${st.egress_denied}`),
    h("div", { class: "table-wrap" }, h("table", {},
      h("thead", {}, h("tr", {}, ["Agent", "Outcome", "Model", "Turns", "Tools", "Calls", "Cost", "Time"].map((c, i) =>
        h("th", { class: i >= 3 ? "num" + (i === 5 ? " hide-sm" : "") : (i === 2 ? "hide-sm" : null) }, c)))),
      h("tbody", {}, st.agents.map((a) => {
        const mismatch = a.outcome !== "running" && a.tool_calls !== a.tool_uses;
        return h("tr", {},
          h("td", {}, h("div", { class: "mono" }, a.id), a.parent ? h("div", { class: "muted" }, "spawned by " + a.parent) : null),
          h("td", {}, badge(a.outcome)),
          h("td", { class: "hide-sm mono" }, a.model || ""),
          h("td", { class: "num" }, a.turns || ""),
          h("td", { class: "num", title: mismatch ? `gateway ${a.tool_calls}, stream ${a.tool_uses}: reconciliation mismatch` : "gateway calls (denied)" },
            `${a.tool_calls}${a.denied ? ` (${a.denied})` : ""}${mismatch ? " ≠" : ""}`),
          h("td", { class: "num hide-sm" }, a.model_calls || ""),
          h("td", { class: "num" }, a.cost ? "$" + a.cost.toFixed(4) : ""),
          h("td", { class: "num" }, a.wall_ms ? fmtMs(a.wall_ms) : ""));
      })))));
  parts.push(h("h2", {}, "Activity"), h("div", { id: "feed", class: "card feed" },
    current.feed.length ? current.feed.map(feedLine) : h("div", { class: "muted" }, "Live events appear here.")));
  el.replaceChildren(...parts);
}

function feedLine(ev) {
  const f = ev.fields || {};
  let what = ev.type;
  switch (ev.type) {
    case "tool_call": what = `${f.tool} ${f.path || ""} ${f.status}${f.deny_reason ? " (" + f.deny_reason + ")" : ""}`; break;
    case "model_call": what = `model ${f["gen_ai.request.model"] || ""} in ${f.input_tokens ?? "?"} out ${f.output_tokens ?? "?"} cache ${f.cache_read_tokens ?? 0}`; break;
    case "agent": what = f.event === "start" ? `agent start ${f.model}` : `agent ${f.outcome} after ${f.num_turns} turns`; break;
    case "stage": what = `stage ${f.event}${f.item !== undefined ? " item " + f.item : ""}${f.error ? ": " + short(f.error, 80) : ""}`; break;
    case "egress": what = `egress ${f.target} ${f.allowed ? "allowed" : "DENIED"}`; break;
    case "needs_snapshot": what = `needs snapshot ${f.file}: ${f.errors} errors, ${f.warnings} warnings`; break;
    case "artifact": what = `wrote ${f.path} (${f.size} bytes)${f.writer ? " as " + f.writer : ""}`; break;
    case "gate": what = `gate ${f.event} via ${f.channel}`; break;
    case "probe_result": what = f.error ? `probe ${f.probe}: ${short(f.error, 80)}` : `probe ${f.probe}: ${f.passed}/${f.tests} passed, lines ${f.line_coverage}%, branches ${f.branch_coverage}%`; break;
    case "limit_event": what = `limit ${f.kind} ${f.action}${f.resets_at ? " until " + fmtTime(f.resets_at) : ""}`; break;
  }
  return h("div", {}, h("span", { class: "t" }, fmtClock(ev.ts_start)), h("span", {}, `${ev.agent_id || ev.stage || ""} `), what);
}

// ---- Gate tab -------------------------------------------------------------------

async function tabGate(st, el) {
  el.replaceChildren(h("p", { class: "muted" }, "Linting…"));
  const open = (st.gates || []).length > 0;
  const { report, text } = await lint(st.id);
  const proposed = report.needs.filter((n) => n.type === "q" && n.status === "proposed");
  const decided = report.needs.filter((n) => n.type === "q" && n.status !== "proposed");
  const g = current.gate;
  const parts = [];
  if (!open) parts.push(h("p", { class: "card muted" }, "The gate is not open: nothing waits for your decision. The questions are shown as they stand."));
  if (open && proposed.length === 0) parts.push(h("p", { class: "card" }, "No proposed questions."));

  const submit = h("button", { class: "primary", disabled: !open || null }, "Submit decisions");
  const summary = h("span", { class: "muted" });
  const updateSummary = () => {
    const c = { approve: 0, reject: 0, leave: 0 };
    for (const q of proposed) c[(g[q.id] && g[q.id].choice) || "leave"]++;
    const edits = proposed.filter((q) => g[q.id] && g[q.id].edit !== undefined).length;
    summary.textContent = `${c.approve} approve, ${c.reject} reject, ${c.leave} undecided${edits ? `, ${edits} edited` : ""}`;
  };

  for (const q of proposed) {
    g[q.id] = g[q.id] || { choice: "leave" };
    const state = g[q.id];
    const seg = h("div", { class: "seg", role: "group", "aria-label": "Decision for " + q.id });
    const setChoice = (c) => {
      state.choice = c;
      for (const b of seg.children) b.setAttribute("aria-pressed", b.dataset.c === c ? "true" : "false");
      updateSummary();
    };
    for (const [c, label] of [["approve", "Approve"], ["reject", "Reject"], ["leave", "Undecided"]]) {
      const b = h("button", { class: c, "data-c": c, "aria-pressed": state.choice === c ? "true" : "false", disabled: !open || null }, label);
      b.addEventListener("click", () => setChoice(c));
      seg.append(b);
    }
    const findings = h("div", { class: "findings" });
    const editor = h("div", { hidden: state.edit === undefined || null, class: "stack" });
    const ta = h("textarea", { "aria-label": "Need block of " + q.id, spellcheck: "false" });
    ta.value = state.edit !== undefined ? state.edit : blockOf(text["questions.md"], q.id);
    let lintTimer = null;
    const relint = async () => {
      const override = { "questions.md": replaceBlock(text["questions.md"], q.id, ta.value) };
      try {
        const r = await lint(st.id, override);
        const fs = r.report.findings.filter((f) => f.need === q.id || (f.path === ".atriumctl/questions.md" && !f.need));
        findings.replaceChildren(...(fs.length ? fs.map((f) => h("div", { class: "f " + f.severity }, `${f.severity} [${f.rule}] ${f.message}`))
          : [h("div", { class: "f muted" }, "No findings.")]));
      } catch (e) { findings.replaceChildren(h("div", { class: "f error" }, e.message)); }
    };
    ta.addEventListener("input", () => { state.edit = ta.value; clearTimeout(lintTimer); lintTimer = setTimeout(relint, 250); updateSummary(); });
    editor.append(ta, findings,
      h("div", { class: "row" }, h("button", { class: "small ghost", onclick: () => {
        delete state.edit; ta.value = blockOf(text["questions.md"], q.id); editor.hidden = true; updateSummary();
      } }, "Discard edit")));
    if (state.edit !== undefined) relint();
    parts.push(h("article", { class: "card question stack" },
      h("div", { class: "row spread" }, h("span", { class: "need-id" }, q.id), h("span", { class: "meta" }, q.fields.area || "")),
      h("h3", {}, q.title),
      q.body ? h("div", { class: "body" }, q.body) : null,
      h("div", { class: "row spread" }, seg,
        h("button", { class: "small ghost", disabled: !open || null, onclick: () => { editor.hidden = false; ta.focus(); relint(); } }, "Edit")),
      editor));
  }
  if (decided.length) {
    parts.push(h("h2", {}, "Decided"), h("div", { class: "table-wrap" }, h("table", {}, h("tbody", {}, decided.map((q) =>
      h("tr", {}, h("td", { class: "need-id" }, q.id), h("td", {}, badge(q.status === "approved" ? "done" : "failed", q.status)), h("td", {}, q.title)))))));
  }
  submit.addEventListener("click", async () => {
    const body = { approve: [], reject: [], edits: {} };
    for (const q of proposed) {
      const st2 = g[q.id];
      if (st2.choice === "approve") body.approve.push(q.id);
      if (st2.choice === "reject") body.reject.push(q.id);
      if (st2.edit !== undefined) body.edits[q.id] = st2.edit;
    }
    if (body.approve.length === 0 && !confirm("No question is approved. Research will have nothing to do. Submit anyway?")) return;
    submit.disabled = true;
    try {
      await api(`/api/runs/${st.id}/gate`, { method: "POST", body });
      current.gate = {};
      location.hash = `#/run/${st.id}/run`;
    } catch (e) {
      if (!(e instanceof AuthError)) alert(e.message);
      submit.disabled = false;
    }
  });
  if (open && proposed.length) parts.push(h("div", { class: "sticky-actions row spread" }, summary, submit));
  updateSummary();
  el.replaceChildren(...parts);
}

// ---- Needs tab ---------------------------------------------------------------------

async function tabNeeds(st, el) {
  el.replaceChildren(h("p", { class: "muted" }, "Linting…"));
  const { report } = await lint(st.id);
  const f = current.needsFilter = current.needsFilter || { type: "", status: "", open: false, q: "" };
  const list = h("div", { class: "table-wrap" });
  const render = () => {
    const rows = report.needs.filter((n) =>
      (!f.type || n.type === f.type) && (!f.status || n.status === f.status) &&
      (!f.open || (n.type === "q" && n.status === "approved" && !(n.incoming.answered_by || []).length)) &&
      (!f.q || (n.id + " " + n.title + " " + n.body).toLowerCase().includes(f.q.toLowerCase())));
    list.replaceChildren(h("table", {},
      h("thead", {}, h("tr", {}, ["Id", "Status", "Title", "Links"].map((c) => h("th", {}, c)))),
      h("tbody", {}, rows.map((n) => h("tr", {},
        h("td", { class: "need-id" }, n.id),
        h("td", {}, n.status ? badge(n.status === "approved" ? "done" : n.status === "rejected" ? "failed" : "gate", n.status) : ""),
        h("td", {}, h("details", {}, h("summary", {}, n.title),
          h("div", { class: "body" }, n.body),
          n.fields.evidence ? h("div", { class: "mono muted" }, "evidence " + n.fields.evidence) : null,
          h("div", { class: "muted mono" }, `${n.path}:${n.line}`))),
        h("td", { class: "mono" }, [
          ...Object.entries(n.links).map(([k, v]) => v.length ? `${k} ${v.join(", ")}` : ""),
          ...Object.entries(n.incoming).map(([k, v]) => `${k} ${v.join(", ")}`)].filter(Boolean).join("; ")))))));
  };
  const sel = (key, opts) => {
    const e = h("select", { "aria-label": key }, opts.map(([v, l]) => h("option", { value: v, selected: f[key] === v || null }, l)));
    e.addEventListener("change", () => { f[key] = e.value; render(); });
    return e;
  };
  const search = h("input", { type: "search", placeholder: "Search", value: f.q, "aria-label": "Search needs" });
  search.addEventListener("input", () => { f.q = search.value; render(); });
  const openBox = h("input", { type: "checkbox", id: "open-only", checked: f.open || null });
  openBox.addEventListener("change", () => { f.open = openBox.checked; render(); });
  const counts = {};
  for (const n of report.needs) counts[n.type] = (counts[n.type] || 0) + 1;
  const errs = report.findings.filter((x) => x.severity === "error").length;
  const warns = report.findings.filter((x) => x.severity === "warning").length;
  el.replaceChildren(
    h("p", {}, `${report.needs.length} needs (${Object.entries(counts).map(([k, v]) => `${v} ${k}`).join(", ")}), `,
      badge(errs ? "error" : "ok", `${errs} errors`), " ", badge(warns ? "warning" : "ok", `${warns} warnings`),
      h("span", { class: "muted" }, " · linted in this browser")),
    report.findings.length ? h("div", { class: "card findings" }, report.findings.map((x) =>
      h("div", { class: "f " + x.severity }, `${x.path}:${x.line} ${x.severity} [${x.rule}] ${x.need || ""} ${x.message}`))) : null,
    h("div", { class: "filters" },
      sel("type", [["", "All types"], ["q", "Questions"], ["fact", "Findings"]]),
      sel("status", [["", "All statuses"], ["proposed", "proposed"], ["approved", "approved"], ["rejected", "rejected"], ["draft", "draft"]]),
      h("label", { class: "row", for: "open-only" }, openBox, "Open questions only"),
      search),
    list);
  render();
}

// ---- Trace graph tab ----------------------------------------------------------------

async function tabGraph(st, el) {
  el.replaceChildren(h("p", { class: "muted" }, "Linting…"));
  const { report } = await lint(st.id);
  const qs = report.needs.filter((n) => n.type === "q");
  const facts = report.needs.filter((n) => n.type === "fact");
  if (!qs.length) { el.replaceChildren(h("p", { class: "muted" }, "No questions yet.")); return; }
  const qIndex = new Map(qs.map((q, i) => [q.id, i]));
  facts.sort((a, b) => (qIndex.get((a.links.answers || [])[0]) ?? 1e9) - (qIndex.get((b.links.answers || [])[0]) ?? 1e9) || a.id.localeCompare(b.id));
  const rowH = 26, qW = 300, fX = 380, fW = 360, pad = 10;
  const fy = new Map(facts.map((f, i) => [f.id, pad + i * rowH]));
  const qy = new Map();
  let next = pad;
  for (const q of qs) {
    const ys = (q.incoming.answered_by || []).map((id) => fy.get(id)).filter((y) => y !== undefined);
    let y = ys.length ? ys.reduce((a, b) => a + b, 0) / ys.length : next;
    y = Math.max(y, next);
    qy.set(q.id, y);
    next = y + rowH + 4;
  }
  const height = Math.max(next, pad + facts.length * rowH) + pad;
  const svg = s("svg", { width: fX + fW + pad, height, role: "img", "aria-label": "Questions and the findings that answer them" });
  const edges = s("g", {});
  const nodes = s("g", {});
  svg.append(edges, nodes);
  const edgeEls = [];
  for (const f of facts) for (const q of f.links.answers || []) {
    if (!qy.has(q)) continue;
    const y1 = qy.get(q) + 10, y2 = fy.get(f.id) + 10;
    const p = s("path", { d: `M${qW + pad} ${y1} C ${(qW + fX) / 2} ${y1}, ${(qW + fX) / 2} ${y2}, ${fX} ${y2}`, "data-q": q, "data-f": f.id });
    edges.append(p);
    edgeEls.push(p);
  }
  const focus = (qid) => {
    for (const p of edgeEls) p.classList.toggle("on", p.dataset.q === qid);
    const on = new Set(edgeEls.filter((p) => p.dataset.q === qid).map((p) => p.dataset.f));
    for (const g of nodes.children) g.classList.toggle("dim", !!qid && g.dataset.id !== qid && !on.has(g.dataset.id));
  };
  for (const q of qs) {
    const open = q.status === "approved" && !(q.incoming.answered_by || []).length;
    nodes.append(s("g", { class: `qnode ${q.status}${open ? " open" : ""}`, "data-id": q.id, tabindex: 0,
      onclick: () => focus(q.id), onfocus: () => focus(q.id) },
      s("title", {}, `${q.id} (${q.status}): ${q.title}`),
      s("rect", { x: pad, y: qy.get(q.id), width: qW, height: 21, rx: 5 }),
      s("text", { x: pad + 8, y: qy.get(q.id) + 15 }, `${q.id} ${short(q.title, 40)}`)));
  }
  for (const f of facts) {
    nodes.append(s("g", { class: "fnode", "data-id": f.id },
      s("title", {}, `${f.id}: ${f.title} (${f.fields.evidence || ""})`),
      s("rect", { x: fX, y: fy.get(f.id), width: fW, height: 21, rx: 5 }),
      s("text", { x: fX + 8, y: fy.get(f.id) + 15 }, `${f.id} ${short(f.title, 48)}`)));
  }
  el.replaceChildren(
    h("p", { class: "muted" }, `${qs.length} questions, ${facts.length} findings. Tap a question to highlight its findings; amber questions are approved but unanswered.`),
    h("div", { class: "row" }, h("button", { class: "small ghost", onclick: () => focus(null) }, "Clear highlight")),
    h("div", { class: "graph" }, svg));
}

// ---- History tab ----------------------------------------------------------------------

async function tabHistory(st, el) {
  const snaps = await api(`/api/runs/${st.id}/snapshots`);
  if (snaps.length < 1) { el.replaceChildren(h("p", { class: "muted" }, "No needs snapshots yet.")); return; }
  const mk = (sel) => h("select", {}, snaps.map((x, i) => h("option", { value: x.name, selected: i === sel || null }, `${x.name} (${fmtTime(x.time)})`)));
  const from = mk(Math.max(0, snaps.length - 2)), to = mk(snaps.length - 1);
  const out = h("div", { class: "card diff stack" });
  const run = async () => {
    try {
      const [config, a, b] = await Promise.all([api(`/api/runs/${st.id}/artifacts/needs.toml`),
        api(`/api/runs/${st.id}/snapshots/${from.value}`), api(`/api/runs/${st.id}/snapshots/${to.value}`)]);
      const d = (await needs()).handle({ op: "diff", config, old: a, new: b }).diff;
      const lines = [
        ...d.added.map((x) => h("div", { class: "add mono" }, "+ " + x)),
        ...d.removed.map((x) => h("div", { class: "del mono" }, "− " + x)),
        ...d.status_changed.map(([id, f, t]) => h("div", { class: "chg mono" }, `~ ${id} status ${f} → ${t}`)),
        ...d.changed.map(([id, keys]) => h("div", { class: "chg mono" }, `~ ${id} ${keys.join(", ")}`)),
        ...d.broken_links.map(([id, l, t]) => h("div", { class: "del mono" }, `! ${id} :${l}: ${t} missing`)),
      ];
      out.replaceChildren(...(lines.length ? lines : [h("div", { class: "muted" }, "No differences.")]));
    } catch (e) { fail(out, e); }
  };
  from.addEventListener("change", run);
  to.addEventListener("change", run);
  el.replaceChildren(h("div", { class: "filters" }, from, h("span", { class: "muted" }, "→"), to), out);
  run();
}

// ---- Baseline tab ------------------------------------------------------------------------

async function tabBaseline(st, el) {
  let res;
  try { res = await api(`/api/runs/${st.id}/probes/baseline`); } catch (e) {
    if (e instanceof AuthError) return;
    const running = st.stages.some((x) => x.name === "baseline" && x.state === "running");
    el.replaceChildren(h("div", { class: "card muted" }, running ? "The baseline probe is running." :
      "No baseline yet: the kernel runs the probe (pytest with coverage, in a sandbox without network) alongside the first stage."));
    return;
  }
  const pctBadge = (p) => badge(p >= 80 ? "ok" : p >= 50 ? "warning" : "error", `${p.toFixed(1)}%`);
  const parts = [h("div", { class: "card stack" },
    h("div", { class: "row spread" }, h("h3", {}, "Tests at the run's commit"),
      h("span", { class: "muted mono" }, `${fmtTime(res.started)} · ${fmtMs(res.duration_ms)}`)),
    h("div", { class: "row" },
      badge(res.failed + res.errors ? "error" : "ok", `${res.passed} passed`),
      res.failed ? badge("error", `${res.failed} failed`) : null,
      res.errors ? badge("error", `${res.errors} errors`) : null,
      res.skipped ? badge("warning", `${res.skipped} skipped`) : null,
      h("span", { class: "muted" }, `of ${res.tests}`)),
    h("div", { class: "row" }, h("span", {}, "Lines"), pctBadge(res.lines.percent), h("span", { class: "muted" }, `${res.lines.covered}/${res.lines.total}`),
      h("span", {}, "Branches"), pctBadge(res.branches.percent), h("span", { class: "muted" }, `${res.branches.covered}/${res.branches.total}`)),
    h("div", { class: "muted mono" }, res.command.join(" ")),
    res.error ? h("pre", { class: "error" }, res.error) : null)];
  if ((res.failures || []).length) {
    parts.push(h("h2", {}, "Failing tests"), h("div", { class: "card findings" }, res.failures.map((f) =>
      h("div", { class: "f error" }, `${f.kind} ${f.test}: ${f.message}`))));
  }
  if ((res.files || []).length) {
    const files = [...res.files].sort((a, b) => a.lines.percent - b.lines.percent || a.path.localeCompare(b.path));
    parts.push(h("h2", {}, `Coverage per file (${files.length})`), h("div", { class: "table-wrap" }, h("table", {},
      h("thead", {}, h("tr", {}, ["File", "Lines", "Branches", "Missing"].map((c, i) => h("th", { class: i === 1 || i === 2 ? "num" : (i === 3 ? "hide-sm" : null) }, c)))),
      h("tbody", {}, files.map((f) => h("tr", {},
        h("td", { class: "mono" }, f.path),
        h("td", { class: "num" }, pctBadge(f.lines.percent), h("div", { class: "muted" }, `${f.lines.covered}/${f.lines.total}`)),
        h("td", { class: "num" }, f.branches.total ? pctBadge(f.branches.percent) : h("span", { class: "muted" }, "–")),
        h("td", { class: "hide-sm mono muted" }, short(ranges(f.missing_lines || []), 120))))))));
  }
  if (res.output) parts.push(h("details", {}, h("summary", { class: "muted" }, "Probe output"), h("pre", { class: "card" }, res.output)));
  el.replaceChildren(...parts);
}

// [1,2,3,7] -> "1-3, 7"
function ranges(ns) {
  const out = [];
  for (let i = 0; i < ns.length; i++) {
    let j = i;
    while (j + 1 < ns.length && ns[j + 1] === ns[j] + 1) j++;
    out.push(i === j ? `${ns[i]}` : `${ns[i]}-${ns[j]}`);
    i = j;
  }
  return out.join(", ");
}

route();
