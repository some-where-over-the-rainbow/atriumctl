//! One JSON request/response API, shared by the command line and WebAssembly.
//!
//! Request: `{"op": "lint"|"build"|"query"|"diff", "config": "<needs.toml text>", ...}`
//!
//! - lint:  `sources: [{path, text}], context?: {files?: {path: lines}}` -> `{report}`
//! - build: as lint -> `{report, needs_json}`
//! - query: as lint, plus `query: {...}` -> `{ids, findings}`
//! - diff:  `old, new` (needs.json values) -> `{diff}`
//!
//! Errors in the configuration come back as findings with rule `config`.

use serde::Deserialize;
use serde_json::{Value, json};

use crate::{Report, build, config::Config, lint};

#[derive(Deserialize)]
struct Request {
    op: String,
    config: String,
    #[serde(default)]
    sources: Vec<lint::Source>,
    #[serde(default)]
    context: lint::Context,
    #[serde(default)]
    query: build::Query,
    #[serde(default)]
    old: Value,
    #[serde(default)]
    new: Value,
}

pub fn handle(request: &str) -> Value {
    let req: Request = match serde_json::from_str(request) {
        Ok(r) => r,
        Err(e) => return json!({"error": format!("bad request: {e}")}),
    };
    let cfg = match Config::parse(&req.config) {
        Ok(c) => c,
        Err(findings) => return json!({"report": Report { needs: vec![], findings }}),
    };
    match req.op.as_str() {
        "lint" => json!({"report": lint::lint(&cfg, &req.sources, &req.context)}),
        "build" => {
            let report = lint::lint(&cfg, &req.sources, &req.context);
            let nj = build::needs_json(&cfg, &req.config, &req.sources, &report);
            json!({"report": report, "needs_json": nj})
        }
        "query" => {
            let report = lint::lint(&cfg, &req.sources, &req.context);
            let ids: Vec<&str> = build::query(&cfg, &report, &req.query)
                .iter()
                .map(|n| n.id.as_str())
                .collect();
            json!({"ids": ids, "findings": report.findings})
        }
        "diff" => json!({"diff": build::diff(&cfg, &req.old, &req.new)}),
        other => json!({"error": format!("unknown op {other:?}")}),
    }
}
