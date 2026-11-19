//! Findings and the model a lint returns.

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use serde_json::Value;

#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Deserialize, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum Severity {
    Error,
    Warning,
    Info,
}

/// One lint finding, with the source position it refers to.
#[derive(Debug, Clone, PartialEq, Eq, PartialOrd, Ord, Deserialize, Serialize)]
pub struct Finding {
    pub path: String,
    pub line: usize,
    pub severity: Severity,
    pub rule: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub need: Option<String>,
    pub message: String,
}

impl Finding {
    pub fn config(message: String) -> Finding {
        Finding {
            path: "needs.toml".into(),
            line: 0,
            severity: Severity::Error,
            rule: "config".into(),
            need: None,
            message,
        }
    }
}

/// A parsed need item.
#[derive(Debug, Clone, PartialEq, Deserialize, Serialize)]
pub struct Need {
    pub id: String,
    #[serde(rename = "type")]
    pub kind: String,
    pub title: String,
    pub status: Option<String>,
    /// Typed field values (not id, status, or links).
    pub fields: BTreeMap<String, Value>,
    /// Outgoing links by link name.
    pub links: BTreeMap<String, Vec<String>>,
    /// Incoming links by incoming name ("answered_by"), filled after parsing.
    pub incoming: BTreeMap<String, Vec<String>>,
    pub body: String,
    pub path: String,
    pub line: usize,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
pub struct Report {
    pub needs: Vec<Need>,
    pub findings: Vec<Finding>,
}

impl Report {
    pub fn errors(&self) -> usize {
        self.findings
            .iter()
            .filter(|f| f.severity == Severity::Error)
            .count()
    }
    pub fn warnings(&self) -> usize {
        self.findings
            .iter()
            .filter(|f| f.severity == Severity::Warning)
            .count()
    }
    pub fn get(&self, id: &str) -> Option<&Need> {
        self.needs.iter().find(|n| n.id == id)
    }
}
