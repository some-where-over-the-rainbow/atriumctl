//! `needs.json` (build), `needs diff` and `needs query`.

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value, json};
use sha2::{Digest, Sha256};

use crate::config::Config;
use crate::lint::Source;
use crate::report::{Need, Report};

pub const TOOL: &str = concat!("atriumctl-needs ", env!("CARGO_PKG_VERSION"));

fn hash(parts: &[(&str, &str)]) -> String {
    let mut h = Sha256::new();
    for (a, b) in parts {
        h.update((a.len() as u64).to_le_bytes());
        h.update(a.as_bytes());
        h.update((b.len() as u64).to_le_bytes());
        h.update(b.as_bytes());
    }
    h.finalize().iter().map(|b| format!("{b:02x}")).collect()
}

/// The machine export, in the Sphinx-Needs `needs.json` shape
/// (`versions[""].needs[id]`), reproducible: sorted keys, no timestamps.
pub fn needs_json(cfg: &Config, cfg_text: &str, sources: &[Source], report: &Report) -> Value {
    let mut srcs: Vec<(&str, &str)> = sources
        .iter()
        .map(|s| (s.path.as_str(), s.text.as_str()))
        .collect();
    srcs.sort();
    let mut needs = Map::new();
    for n in &report.needs {
        needs.insert(n.id.clone(), need_value(cfg, n));
    }
    let mut counts: BTreeMap<String, BTreeMap<String, usize>> = BTreeMap::new();
    for n in &report.needs {
        *counts
            .entry(n.kind.clone())
            .or_default()
            .entry(n.status.clone().unwrap_or_default())
            .or_default() += 1;
    }
    json!({
        "created_by": TOOL,
        "project": cfg.project,
        "current_version": "",
        "config_hash": hash(&[("needs.toml", cfg_text)]),
        "source_hash": hash(&srcs),
        "errors": report.errors(),
        "warnings": report.warnings(),
        "counts": counts,
        "versions": { "": { "needs_amount": report.needs.len(), "needs": needs } },
    })
}

fn need_value(cfg: &Config, n: &Need) -> Value {
    let mut m = Map::new();
    m.insert("id".into(), n.id.clone().into());
    m.insert("type".into(), n.kind.clone().into());
    m.insert(
        "type_name".into(),
        cfg.need_type(&n.kind)
            .map(|t| t.title.clone())
            .unwrap_or_default()
            .into(),
    );
    m.insert("title".into(), n.title.clone().into());
    m.insert(
        "status".into(),
        n.status.clone().map(Value::from).unwrap_or(Value::Null),
    );
    m.insert("content".into(), n.body.clone().into());
    m.insert("docname".into(), n.path.clone().into());
    m.insert("lineno".into(), n.line.into());
    for (k, v) in &n.fields {
        m.insert(k.clone(), v.clone());
    }
    for name in cfg.links.keys() {
        m.insert(
            name.clone(),
            n.links.get(name).cloned().unwrap_or_default().into(),
        );
    }
    for l in cfg.links.values() {
        m.insert(
            l.incoming.clone(),
            n.incoming
                .get(&l.incoming)
                .cloned()
                .unwrap_or_default()
                .into(),
        );
    }
    Value::Object(m)
}

/// The needs of a `needs.json`, by id.
pub fn needs_of(doc: &Value) -> BTreeMap<String, Value> {
    doc.get("versions")
        .and_then(|v| v.get(""))
        .and_then(|v| v.get("needs"))
        .and_then(Value::as_object)
        .map(|m| m.iter().map(|(k, v)| (k.clone(), v.clone())).collect())
        .unwrap_or_default()
}

#[derive(Debug, Clone, Default, PartialEq, Deserialize, Serialize)]
pub struct Diff {
    pub added: Vec<String>,
    pub removed: Vec<String>,
    /// (id, from, to)
    pub status_changed: Vec<(String, Option<String>, Option<String>)>,
    /// (id, changed keys other than status and position)
    pub changed: Vec<(String, Vec<String>)>,
    /// (id, link name, missing target) in the new snapshot
    pub broken_links: Vec<(String, String, String)>,
}

pub fn diff(cfg: &Config, old: &Value, new: &Value) -> Diff {
    let (a, b) = (needs_of(old), needs_of(new));
    let mut d = Diff::default();
    for id in b.keys() {
        if !a.contains_key(id) {
            d.added.push(id.clone());
        }
    }
    for (id, av) in &a {
        let Some(bv) = b.get(id) else {
            d.removed.push(id.clone());
            continue;
        };
        let st = |v: &Value| v.get("status").and_then(Value::as_str).map(String::from);
        if st(av) != st(bv) {
            d.status_changed.push((id.clone(), st(av), st(bv)));
        }
        let (Some(ao), Some(bo)) = (av.as_object(), bv.as_object()) else {
            continue;
        };
        let mut keys: Vec<String> = ao
            .keys()
            .chain(bo.keys())
            .filter(|k| !matches!(k.as_str(), "status" | "lineno" | "docname"))
            .filter(|k| ao.get(*k) != bo.get(*k))
            .cloned()
            .collect();
        keys.sort();
        keys.dedup();
        if !keys.is_empty() {
            d.changed.push((id.clone(), keys));
        }
    }
    for (id, v) in &b {
        for name in cfg.links.keys() {
            for t in v.get(name).and_then(Value::as_array).into_iter().flatten() {
                if let Some(t) = t.as_str()
                    && !b.contains_key(t)
                {
                    d.broken_links
                        .push((id.clone(), name.clone(), t.to_string()));
                }
            }
        }
    }
    d
}

/// A query over a lint report. All conditions must hold.
#[derive(Debug, Clone, Default, Deserialize, Serialize)]
pub struct Query {
    #[serde(default, rename = "type")]
    pub kind: Option<String>,
    #[serde(default)]
    pub status: Option<String>,
    /// field = value
    #[serde(default)]
    pub fields: BTreeMap<String, String>,
    /// Has at least one outgoing link of this name.
    #[serde(default)]
    pub has_link: Option<String>,
    /// Has no outgoing link of this name.
    #[serde(default)]
    pub without_link: Option<String>,
    /// Has at least one incoming link of this (outgoing) link name.
    #[serde(default)]
    pub has_incoming: Option<String>,
    /// Has no incoming link of this (outgoing) link name.
    #[serde(default)]
    pub without_incoming: Option<String>,
}

pub fn query<'a>(cfg: &Config, report: &'a Report, q: &Query) -> Vec<&'a Need> {
    let inc = |link: &str| {
        cfg.links
            .get(link)
            .map(|l| l.incoming.clone())
            .unwrap_or_default()
    };
    let mut out: Vec<&Need> = report
        .needs
        .iter()
        .filter(|n| {
            q.kind.as_ref().is_none_or(|k| &n.kind == k)
                && q.status
                    .as_ref()
                    .is_none_or(|s| n.status.as_ref() == Some(s))
                && q.fields.iter().all(|(k, v)| match n.fields.get(k) {
                    Some(Value::String(s)) => s == v,
                    Some(o) => &o.to_string() == v,
                    None => false,
                })
                && q.has_link
                    .as_ref()
                    .is_none_or(|l| n.links.get(l).is_some_and(|x| !x.is_empty()))
                && q.without_link
                    .as_ref()
                    .is_none_or(|l| n.links.get(l).is_none_or(|x| x.is_empty()))
                && q.has_incoming
                    .as_ref()
                    .is_none_or(|l| n.incoming.get(&inc(l)).is_some_and(|x| !x.is_empty()))
                && q.without_incoming
                    .as_ref()
                    .is_none_or(|l| n.incoming.get(&inc(l)).is_none_or(|x| x.is_empty()))
        })
        .collect();
    out.sort_by(|a, b| a.id.cmp(&b.id));
    out
}
