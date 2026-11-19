//! `needs.toml`: need types, fields with JSON-schema, links, and network rules.
//!
//! The shape follows Sphinx-Needs 7/8 (`[[needs.types]]`, `[needs.fields.*]`,
//! `[needs.links.*]`). `[[needs.rules]]` is our own small declarative form for
//! validation across links.

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::report::{Finding, Severity};

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct File {
    pub needs: Config,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Config {
    #[serde(default)]
    pub project: String,
    pub types: Vec<NeedType>,
    #[serde(default)]
    pub fields: BTreeMap<String, Field>,
    #[serde(default)]
    pub links: BTreeMap<String, Link>,
    #[serde(default)]
    pub rules: Vec<Rule>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct NeedType {
    pub directive: String,
    pub title: String,
    #[serde(default)]
    pub prefix: String,
    /// Regular expression every id of this type must match (whole id).
    #[serde(default)]
    pub id_regex: Option<String>,
    /// Fields that must be present.
    #[serde(default)]
    pub required: Vec<String>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Field {
    /// JSON schema for the field's value. Values are strings unless the
    /// schema's `type` says integer, number, boolean or array.
    #[serde(default)]
    pub schema: Option<Value>,
    #[serde(default)]
    pub description: String,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Link {
    /// Name of the link seen from the target ("answered_by").
    pub incoming: String,
    /// Allowed target types; empty means any.
    #[serde(default)]
    pub targets: Vec<String>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Rule {
    pub id: String,
    #[serde(default = "default_severity")]
    pub severity: Severity,
    #[serde(default)]
    pub message: String,
    /// Which needs the rule applies to: `type`, `status` or any field, by equality.
    #[serde(default)]
    pub select: BTreeMap<String, String>,
    /// Count outgoing links of this name …
    #[serde(default)]
    pub link: Option<String>,
    /// … or incoming links of this (outgoing) link name …
    #[serde(default)]
    pub incoming: Option<String>,
    /// … whose other end matches this filter …
    #[serde(default)]
    pub target: BTreeMap<String, String>,
    /// … at least `min` and at most `max` times.
    #[serde(default)]
    pub min: Option<usize>,
    #[serde(default)]
    pub max: Option<usize>,
    /// Or check a field: `file-exists` for `path:line[-line]` values.
    #[serde(default)]
    pub field: Option<String>,
    #[serde(default)]
    pub check: Option<String>,
}

fn default_severity() -> Severity {
    Severity::Error
}

/// The fields every need has, outside `[needs.fields]`.
pub const CORE_FIELDS: &[&str] = &["id", "status"];

impl Config {
    pub fn parse(text: &str) -> Result<Config, Vec<Finding>> {
        let file: File =
            toml::from_str(text).map_err(|e| vec![Finding::config(format!("needs.toml: {e}"))])?;
        let cfg = file.needs;
        let errs = cfg.check();
        if errs.is_empty() { Ok(cfg) } else { Err(errs) }
    }

    pub fn need_type(&self, directive: &str) -> Option<&NeedType> {
        self.types.iter().find(|t| t.directive == directive)
    }

    /// Validates the configuration itself, before any source is read.
    fn check(&self) -> Vec<Finding> {
        let mut out = Vec::new();
        let mut seen = std::collections::BTreeSet::new();
        if self.types.is_empty() {
            out.push(Finding::config("no need types defined".into()));
        }
        for t in &self.types {
            if !seen.insert(&t.directive) {
                out.push(Finding::config(format!(
                    "duplicate need type {:?}",
                    t.directive
                )));
            }
            if let Some(r) = &t.id_regex
                && let Err(e) = regex::Regex::new(&format!("^(?:{r})$"))
            {
                out.push(Finding::config(format!(
                    "type {}: id_regex: {e}",
                    t.directive
                )));
            }
            for f in &t.required {
                if !self.fields.contains_key(f)
                    && !CORE_FIELDS.contains(&f.as_str())
                    && !self.links.contains_key(f)
                {
                    out.push(Finding::config(format!(
                        "type {}: required field {f:?} is not defined",
                        t.directive
                    )));
                }
            }
        }
        for (name, f) in &self.fields {
            if self.links.contains_key(name) {
                out.push(Finding::config(format!(
                    "{name:?} is both a field and a link"
                )));
            }
            if let Some(s) = &f.schema
                && let Err(e) = jsonschema::validator_for(s)
            {
                out.push(Finding::config(format!("field {name}: schema: {e}")));
            }
        }
        for (name, l) in &self.links {
            for t in &l.targets {
                if self.need_type(t).is_none() {
                    out.push(Finding::config(format!(
                        "link {name}: unknown target type {t:?}"
                    )));
                }
            }
        }
        let mut rule_ids = std::collections::BTreeSet::new();
        for r in &self.rules {
            if !rule_ids.insert(&r.id) {
                out.push(Finding::config(format!("duplicate rule id {:?}", r.id)));
            }
            let kinds = [r.link.is_some(), r.incoming.is_some(), r.field.is_some()]
                .iter()
                .filter(|b| **b)
                .count();
            if kinds != 1 {
                out.push(Finding::config(format!(
                    "rule {}: set exactly one of link, incoming, field",
                    r.id
                )));
            }
            for l in r.link.iter().chain(r.incoming.iter()) {
                if !self.links.contains_key(l) {
                    out.push(Finding::config(format!(
                        "rule {}: unknown link {l:?}",
                        r.id
                    )));
                }
            }
            if r.field.is_some() && r.check.as_deref() != Some("file-exists") {
                out.push(Finding::config(format!(
                    "rule {}: field rules need check = \"file-exists\"",
                    r.id
                )));
            }
            if (r.link.is_some() || r.incoming.is_some()) && r.min.is_none() && r.max.is_none() {
                out.push(Finding::config(format!(
                    "rule {}: link rules need min or max",
                    r.id
                )));
            }
        }
        out
    }
}
