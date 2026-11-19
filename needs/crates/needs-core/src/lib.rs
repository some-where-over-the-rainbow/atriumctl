//! The needs backbone's core: parse need blocks from Markdown, validate them
//! against `needs.toml`, and build, diff and query the model.
//!
//! No file system, clock or network: input is text, output is data. The same
//! code runs natively (`needs-cli`) and in the browser (`needs-wasm`), both
//! through [`api::handle`], so they report identical findings.

pub mod api;
pub mod build;
pub mod config;
pub mod lint;
pub mod parse;
pub mod report;

pub use build::{Diff, Query, diff, needs_json, query};
pub use config::Config;
pub use lint::{Context, Source, lint};
pub use report::{Finding, Need, Report, Severity};
