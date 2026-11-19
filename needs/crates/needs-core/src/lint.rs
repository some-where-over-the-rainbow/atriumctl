//! Typing, local validation (fields, ids, links) and network rules.

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::config::{CORE_FIELDS, Config, Rule};
use crate::parse::{self, RawNeed};
use crate::report::{Finding, Need, Report, Severity};

/// One source file: a path (as the needs refer to it) and its text.
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct Source {
    pub path: String,
    pub text: String,
}

/// What the I/O-free core is told about the world.
#[derive(Debug, Clone, Default, Deserialize, Serialize)]
pub struct Context {
    /// Files of the repository at the run's commit, with their line counts
    /// (0 = unknown). `None` skips `file-exists` rules.
    #[serde(default)]
    pub files: Option<BTreeMap<String, usize>>,
}

pub fn lint(cfg: &Config, sources: &[Source], ctx: &Context) -> Report {
    let mut findings = Vec::new();
    let mut needs = Vec::new();
    let mut sorted: Vec<&Source> = sources.iter().collect();
    sorted.sort_by(|a, b| a.path.cmp(&b.path));
    for s in sorted {
        for raw in parse::blocks(&s.path, &s.text) {
            if let Some(n) = typed(cfg, raw, &mut findings) {
                needs.push(n);
            }
        }
    }
    unique_ids(&needs, &mut findings);
    links(cfg, &mut needs, &mut findings);
    for r in &cfg.rules {
        rule(cfg, r, &needs, ctx, &mut findings);
    }
    findings.sort();
    findings.dedup();
    Report { needs, findings }
}

fn f(
    n: &RawNeed,
    line: usize,
    sev: Severity,
    rule: &str,
    need: Option<&str>,
    msg: String,
) -> Finding {
    Finding {
        path: n.path.clone(),
        line,
        severity: sev,
        rule: rule.into(),
        need: need.map(String::from),
        message: msg,
    }
}

fn typed(cfg: &Config, raw: RawNeed, out: &mut Vec<Finding>) -> Option<Need> {
    let Some(t) = cfg.need_type(&raw.directive) else {
        out.push(f(
            &raw,
            raw.line,
            Severity::Warning,
            "unknown-type",
            None,
            format!("unknown need type {{{}}}; block ignored", raw.directive),
        ));
        return None;
    };
    let Some((id, id_line)) = raw.options.get("id").cloned() else {
        out.push(f(
            &raw,
            raw.line,
            Severity::Error,
            "id-missing",
            None,
            format!("{{{}}} block has no :id:", raw.directive),
        ));
        return None;
    };
    let idr = Some(id.as_str());
    if let Some(re) = &t.id_regex {
        let re = regex::Regex::new(&format!("^(?:{re})$")).expect("checked with the config");
        if !re.is_match(&id) {
            out.push(f(
                &raw,
                id_line,
                Severity::Error,
                "id-format",
                idr,
                format!(
                    "id {id:?} does not match {}",
                    t.id_regex.as_deref().unwrap_or("")
                ),
            ));
        }
    }
    if raw.title.is_empty() {
        out.push(f(
            &raw,
            raw.line,
            Severity::Error,
            "title-missing",
            idr,
            "need has no title".into(),
        ));
    }
    for (k, line) in &raw.duplicates {
        out.push(f(
            &raw,
            *line,
            Severity::Error,
            "option-duplicate",
            idr,
            format!(":{k}: given more than once"),
        ));
    }
    let mut need = Need {
        id: id.clone(),
        kind: t.directive.clone(),
        title: raw.title.clone(),
        status: None,
        fields: BTreeMap::new(),
        links: BTreeMap::new(),
        incoming: BTreeMap::new(),
        body: raw.body.clone(),
        path: raw.path.clone(),
        line: raw.line,
    };
    for (k, (v, line)) in &raw.options {
        if k == "id" {
            continue;
        }
        if cfg.links.contains_key(k) {
            let ids: Vec<String> = v
                .split(',')
                .map(|s| s.trim().to_string())
                .filter(|s| !s.is_empty())
                .collect();
            need.links.insert(k.clone(), ids);
            continue;
        }
        let Some(field) = cfg.fields.get(k) else {
            if k == "status" {
                need.status = Some(v.clone());
            } else {
                out.push(f(
                    &raw,
                    *line,
                    Severity::Error,
                    "field-unknown",
                    idr,
                    format!("unknown field :{k}:"),
                ));
            }
            continue;
        };
        let value = coerce(v, field.schema.as_ref());
        if let Some(schema) = &field.schema {
            let validator = jsonschema::validator_for(schema).expect("checked with the config");
            for e in validator.iter_errors(&value) {
                out.push(f(
                    &raw,
                    *line,
                    Severity::Error,
                    "field-schema",
                    idr,
                    format!(":{k}: {e}"),
                ));
            }
        }
        if k == "status" {
            need.status = Some(v.clone());
        } else {
            need.fields.insert(k.clone(), value);
        }
    }
    for req in &t.required {
        let present = match req.as_str() {
            "status" => need.status.is_some(),
            r if CORE_FIELDS.contains(&r) => true,
            r => need.fields.contains_key(r) || need.links.get(r).is_some_and(|l| !l.is_empty()),
        };
        if !present {
            out.push(f(
                &raw,
                raw.line,
                Severity::Error,
                "field-required",
                idr,
                format!("required field :{req}: is missing"),
            ));
        }
    }
    Some(need)
}

/// Field text to a JSON value, by the schema's declared type.
fn coerce(v: &str, schema: Option<&Value>) -> Value {
    let ty = schema.and_then(|s| s.get("type")).and_then(Value::as_str);
    match ty {
        Some("integer") => v
            .parse::<i64>()
            .map(Value::from)
            .unwrap_or_else(|_| Value::String(v.into())),
        Some("number") => v
            .parse::<f64>()
            .ok()
            .and_then(serde_json::Number::from_f64)
            .map(Value::Number)
            .unwrap_or_else(|| Value::String(v.into())),
        Some("boolean") => match v {
            "true" => Value::Bool(true),
            "false" => Value::Bool(false),
            _ => Value::String(v.into()),
        },
        Some("array") => Value::Array(
            v.split(',')
                .map(|s| s.trim())
                .filter(|s| !s.is_empty())
                .map(|s| Value::String(s.into()))
                .collect(),
        ),
        _ => Value::String(v.into()),
    }
}

fn unique_ids(needs: &[Need], out: &mut Vec<Finding>) {
    let mut first: BTreeMap<&str, &Need> = BTreeMap::new();
    for n in needs {
        if let Some(p) = first.get(n.id.as_str()) {
            out.push(Finding {
                path: n.path.clone(),
                line: n.line,
                severity: Severity::Error,
                rule: "id-duplicate".into(),
                need: Some(n.id.clone()),
                message: format!("id {} already used at {}:{}", n.id, p.path, p.line),
            });
        } else {
            first.insert(&n.id, n);
        }
    }
}

fn links(cfg: &Config, needs: &mut [Need], out: &mut Vec<Finding>) {
    // With duplicate ids, the first occurrence is the need; later ones are
    // reported by unique_ids and take no part in links.
    let mut kinds: BTreeMap<String, String> = BTreeMap::new();
    for n in needs.iter() {
        kinds.entry(n.id.clone()).or_insert_with(|| n.kind.clone());
    }
    let mut incoming: BTreeMap<String, BTreeMap<String, Vec<String>>> = BTreeMap::new();
    for n in needs.iter() {
        for (name, targets) in &n.links {
            let l = &cfg.links[name];
            for t in targets {
                let Some(kind) = kinds.get(t) else {
                    out.push(Finding {
                        path: n.path.clone(),
                        line: n.line,
                        severity: Severity::Error,
                        rule: "link-broken".into(),
                        need: Some(n.id.clone()),
                        message: format!(":{name}: {t} does not exist"),
                    });
                    continue;
                };
                if !l.targets.is_empty() && !l.targets.contains(kind) {
                    out.push(Finding {
                        path: n.path.clone(),
                        line: n.line,
                        severity: Severity::Error,
                        rule: "link-target-type".into(),
                        need: Some(n.id.clone()),
                        message: format!(
                            ":{name}: {t} is a {kind}, expected {}",
                            l.targets.join(" or ")
                        ),
                    });
                }
                incoming
                    .entry(t.clone())
                    .or_default()
                    .entry(l.incoming.clone())
                    .or_default()
                    .push(n.id.clone());
            }
        }
    }
    let mut seen = std::collections::BTreeSet::new();
    for n in needs.iter_mut() {
        if !seen.insert(n.id.clone()) {
            continue;
        }
        if let Some(inc) = incoming.remove(&n.id) {
            n.incoming = inc;
            for v in n.incoming.values_mut() {
                v.sort();
                v.dedup();
            }
        }
    }
}

fn matches(n: &Need, filter: &BTreeMap<String, String>) -> bool {
    filter.iter().all(|(k, v)| match k.as_str() {
        "type" => &n.kind == v,
        "status" => n.status.as_ref() == Some(v),
        "id" => &n.id == v,
        _ => match n.fields.get(k) {
            Some(Value::String(s)) => s == v,
            Some(other) => &other.to_string() == v,
            None => false,
        },
    })
}

fn rule(cfg: &Config, r: &Rule, needs: &[Need], ctx: &Context, out: &mut Vec<Finding>) {
    let mut by_id: BTreeMap<&str, &Need> = BTreeMap::new();
    for n in needs {
        by_id.entry(n.id.as_str()).or_insert(n);
    }
    let selected = needs.iter().filter(|n| matches(n, &r.select));
    let finding = |n: &Need, detail: String| Finding {
        path: n.path.clone(),
        line: n.line,
        severity: r.severity,
        rule: r.id.clone(),
        need: Some(n.id.clone()),
        message: if r.message.is_empty() {
            detail
        } else {
            format!("{}: {detail}", r.message)
        },
    };
    if let Some(field) = &r.field {
        let Some(files) = &ctx.files else { return };
        for n in selected {
            let Some(Value::String(v)) = n.fields.get(field) else {
                continue;
            };
            let (path, lines) = v.split_once(':').unwrap_or((v.as_str(), ""));
            match files.get(path) {
                None => out.push(finding(
                    n,
                    format!(":{field}: {path} does not exist at the run's commit"),
                )),
                Some(&count) if count > 0 => {
                    let last = lines
                        .split('-')
                        .filter_map(|s| s.parse::<usize>().ok())
                        .max()
                        .unwrap_or(0);
                    if last > count {
                        out.push(finding(
                            n,
                            format!(":{field}: {path} has {count} lines, not {last}"),
                        ));
                    }
                }
                Some(_) => {}
            }
        }
        return;
    }
    let (min, max) = (r.min.unwrap_or(0), r.max.unwrap_or(usize::MAX));
    for n in selected {
        let (count, what) = if let Some(link) = &r.link {
            let c = n.links.get(link).map_or(0, |ts| {
                ts.iter()
                    .filter(|t| {
                        by_id
                            .get(t.as_str())
                            .is_some_and(|tn| matches(tn, &r.target))
                    })
                    .count()
            });
            (c, format!(":{link}: links"))
        } else {
            let link = r.incoming.as_ref().expect("checked with the config");
            let inc = &cfg.links[link].incoming;
            let c = n.incoming.get(inc).map_or(0, |ss| {
                ss.iter()
                    .filter(|s| {
                        by_id
                            .get(s.as_str())
                            .is_some_and(|sn| matches(sn, &r.target))
                    })
                    .count()
            });
            (c, format!("incoming :{link}: links"))
        };
        let filter = if r.target.is_empty() {
            String::new()
        } else {
            format!(" to {}", describe(&r.target))
        };
        if count < min {
            out.push(finding(
                n,
                format!("{count} {what}{filter}, at least {min} required"),
            ));
        } else if count > max {
            out.push(finding(
                n,
                format!("{count} {what}{filter}, at most {max} allowed"),
            ));
        }
    }
}

fn describe(filter: &BTreeMap<String, String>) -> String {
    filter
        .iter()
        .map(|(k, v)| format!("{k}={v}"))
        .collect::<Vec<_>>()
        .join(", ")
}
