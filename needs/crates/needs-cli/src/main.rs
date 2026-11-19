//! `needs`: the needs backbone's command line. File discovery and output;
//! all logic is in needs-core, reached through the same JSON API the
//! browser uses.
//!
//! ```text
//! needs lint  [-c needs.toml] [--repo DIR | --files FILES.json] [--base DIR] [--json] PATH...
//! needs build [same as lint] -o needs.json
//! needs query [same as lint] [--type T] [--status S] [--field K=V]...
//!             [--has-link L] [--without-link L] [--has-incoming L] [--without-incoming L]
//! needs diff  [-c needs.toml] OLD.json NEW.json
//! needs api   < request.json
//! ```
//!
//! Exit status: 0 clean, 1 lint errors (or a non-empty diff with --exit-code), 2 usage or I/O error.

use std::collections::BTreeMap;
use std::fs;
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use std::process::ExitCode;

use needs_core::api;
use serde_json::{Value, json};

const USAGE: &str = "usage: needs lint|build|query|diff|api [options] [paths]  (see --help)";

struct Opts {
    config: PathBuf,
    repo: Option<PathBuf>,
    files: Option<PathBuf>,
    base: PathBuf,
    json: bool,
    out: Option<PathBuf>,
    exit_code: bool,
    query: serde_json::Map<String, Value>,
    paths: Vec<PathBuf>,
}

fn main() -> ExitCode {
    match run() {
        Ok(code) => code,
        Err(e) => {
            eprintln!("needs: {e}");
            ExitCode::from(2)
        }
    }
}

fn run() -> Result<ExitCode, String> {
    let mut args = std::env::args().skip(1);
    let cmd = args.next().ok_or(USAGE)?;
    if cmd == "--help" || cmd == "-h" {
        println!(
            "{}",
            include_str!("main.rs")
                .lines()
                .skip(4)
                .take(9)
                .map(|l| l.trim_start_matches("//! "))
                .collect::<Vec<_>>()
                .join("\n")
        );
        return Ok(ExitCode::SUCCESS);
    }
    if cmd == "--version" {
        println!("{}", needs_core::build::TOOL);
        return Ok(ExitCode::SUCCESS);
    }
    let o = parse_opts(args.collect())?;
    match cmd.as_str() {
        "api" => {
            let mut req = String::new();
            std::io::stdin()
                .read_to_string(&mut req)
                .map_err(|e| e.to_string())?;
            println!("{}", api::handle(&req));
            Ok(ExitCode::SUCCESS)
        }
        "lint" | "build" | "query" => lint_like(&cmd, &o),
        "diff" => diff(&o),
        _ => Err(USAGE.into()),
    }
}

fn parse_opts(args: Vec<String>) -> Result<Opts, String> {
    let mut o = Opts {
        config: "needs.toml".into(),
        repo: None,
        files: None,
        base: ".".into(),
        json: false,
        out: None,
        exit_code: false,
        query: serde_json::Map::new(),
        paths: vec![],
    };
    let mut fields = serde_json::Map::new();
    let mut it = args.into_iter();
    while let Some(a) = it.next() {
        let mut val = |name: &str| it.next().ok_or(format!("{name} needs a value"));
        match a.as_str() {
            "-c" | "--config" => o.config = val(&a)?.into(),
            "--repo" => o.repo = Some(val(&a)?.into()),
            "--files" => o.files = Some(val(&a)?.into()),
            "--base" => o.base = val(&a)?.into(),
            "-o" | "--out" => o.out = Some(val(&a)?.into()),
            "--json" => o.json = true,
            "--exit-code" => o.exit_code = true,
            "--type" => {
                o.query.insert("type".into(), val(&a)?.into());
            }
            "--status" => {
                o.query.insert("status".into(), val(&a)?.into());
            }
            "--has-link" | "--without-link" | "--has-incoming" | "--without-incoming" => {
                o.query.insert(
                    a.trim_start_matches("--").replace('-', "_"),
                    val(&a)?.into(),
                );
            }
            "--field" => {
                let kv = val(&a)?;
                let (k, v) = kv.split_once('=').ok_or("--field needs KEY=VALUE")?;
                fields.insert(k.into(), v.into());
            }
            s if s.starts_with('-') => return Err(format!("unknown option {s}")),
            _ => o.paths.push(a.into()),
        }
    }
    if !fields.is_empty() {
        o.query.insert("fields".into(), Value::Object(fields));
    }
    Ok(o)
}

fn read(p: &Path) -> Result<String, String> {
    fs::read_to_string(p).map_err(|e| format!("{}: {e}", p.display()))
}

/// Path of `p` relative to `base`, with forward slashes.
fn rel(base: &Path, p: &Path) -> String {
    let (b, q) = (
        base.canonicalize().unwrap_or(base.into()),
        p.canonicalize().unwrap_or(p.into()),
    );
    q.strip_prefix(&b)
        .unwrap_or(&q)
        .to_string_lossy()
        .replace('\\', "/")
}

fn walk(dir: &Path, out: &mut Vec<PathBuf>, keep: &dyn Fn(&Path) -> bool) -> Result<(), String> {
    let mut ents: Vec<_> = fs::read_dir(dir)
        .map_err(|e| format!("{}: {e}", dir.display()))?
        .filter_map(Result::ok)
        .map(|e| e.path())
        .collect();
    ents.sort();
    for p in ents {
        if p.is_dir() {
            if p.file_name().is_some_and(|n| n == ".git") {
                continue;
            }
            walk(&p, out, keep)?;
        } else if keep(&p) {
            out.push(p);
        }
    }
    Ok(())
}

fn request(op: &str, o: &Opts) -> Result<Value, String> {
    let config = read(&o.config)?;
    let mut files = vec![];
    for p in &o.paths {
        if p.is_dir() {
            walk(p, &mut files, &|f| f.extension().is_some_and(|e| e == "md"))?;
        } else {
            files.push(p.clone());
        }
    }
    let mut sources = vec![];
    for f in &files {
        sources.push(json!({"path": rel(&o.base, f), "text": read(f)?}));
    }
    let mut context = json!({});
    if let Some(repo) = &o.repo {
        let mut all = vec![];
        walk(repo, &mut all, &|_| true)?;
        let mut m = BTreeMap::new();
        for f in all {
            let lines = fs::read(&f)
                .map(|b| {
                    let n = b.iter().filter(|c| **c == b'\n').count();
                    if b.last().is_some_and(|c| *c != b'\n') {
                        n + 1
                    } else {
                        n
                    }
                })
                .unwrap_or(0);
            m.insert(rel(repo, &f), lines);
        }
        context = json!({"files": m});
    } else if let Some(fl) = &o.files {
        let m: Value =
            serde_json::from_str(&read(fl)?).map_err(|e| format!("{}: {e}", fl.display()))?;
        context = json!({"files": m});
    }
    Ok(
        json!({"op": op, "config": config, "sources": sources, "context": context, "query": o.query}),
    )
}

fn lint_like(op: &str, o: &Opts) -> Result<ExitCode, String> {
    if o.paths.is_empty() {
        return Err("no paths given".into());
    }
    let resp = api::handle(&request(op, o)?.to_string());
    if let Some(e) = resp.get("error") {
        return Err(e.to_string());
    }
    let findings = resp
        .pointer("/report/findings")
        .or_else(|| resp.get("findings"))
        .cloned()
        .unwrap_or(json!([]));
    let errors = findings
        .as_array()
        .map_or(0, |a| a.iter().filter(|f| f["severity"] == "error").count());
    let warnings = findings.as_array().map_or(0, |a| {
        a.iter().filter(|f| f["severity"] == "warning").count()
    });
    let mut stdout = std::io::stdout().lock();
    if o.json {
        let _ = writeln!(stdout, "{resp}");
    } else {
        for f in findings.as_array().into_iter().flatten() {
            let need = f["need"]
                .as_str()
                .map(|n| format!(" {n}"))
                .unwrap_or_default();
            let _ = writeln!(
                stdout,
                "{}:{}: {}[{}]{need}: {}",
                f["path"].as_str().unwrap_or(""),
                f["line"],
                f["severity"].as_str().unwrap_or(""),
                f["rule"].as_str().unwrap_or(""),
                f["message"].as_str().unwrap_or("")
            );
        }
        if op == "query" {
            for id in resp["ids"].as_array().into_iter().flatten() {
                let _ = writeln!(stdout, "{}", id.as_str().unwrap_or(""));
            }
        } else {
            let n = resp
                .pointer("/report/needs")
                .and_then(Value::as_array)
                .map_or(0, Vec::len);
            let _ = writeln!(stdout, "{n} needs, {errors} errors, {warnings} warnings");
        }
    }
    if op == "build" {
        let nj = serde_json::to_string_pretty(&resp["needs_json"]).map_err(|e| e.to_string())?;
        let out = o.out.clone().unwrap_or("needs.json".into());
        fs::write(&out, nj + "\n").map_err(|e| format!("{}: {e}", out.display()))?;
    }
    Ok(if errors > 0 && op != "query" {
        ExitCode::from(1)
    } else {
        ExitCode::SUCCESS
    })
}

fn diff(o: &Opts) -> Result<ExitCode, String> {
    let [old, new] = o.paths.as_slice() else {
        return Err("diff needs OLD.json NEW.json".into());
    };
    let parse = |p: &Path| -> Result<Value, String> {
        serde_json::from_str(&read(p)?).map_err(|e| format!("{}: {e}", p.display()))
    };
    let req =
        json!({"op": "diff", "config": read(&o.config)?, "old": parse(old)?, "new": parse(new)?});
    let resp = api::handle(&req.to_string());
    if let Some(e) = resp
        .get("error")
        .or_else(|| resp.pointer("/report/findings/0/message"))
    {
        return Err(e.to_string());
    }
    let d = &resp["diff"];
    if o.json {
        println!("{d}");
    } else {
        for id in d["added"].as_array().into_iter().flatten() {
            println!("+ {}", id.as_str().unwrap_or(""));
        }
        for id in d["removed"].as_array().into_iter().flatten() {
            println!("- {}", id.as_str().unwrap_or(""));
        }
        for c in d["status_changed"].as_array().into_iter().flatten() {
            println!(
                "~ {} status {} -> {}",
                c[0].as_str().unwrap_or(""),
                c[1],
                c[2]
            );
        }
        for c in d["changed"].as_array().into_iter().flatten() {
            println!("~ {} {}", c[0].as_str().unwrap_or(""), c[1]);
        }
        for c in d["broken_links"].as_array().into_iter().flatten() {
            println!(
                "! {} :{}: {} missing",
                c[0].as_str().unwrap_or(""),
                c[1].as_str().unwrap_or(""),
                c[2].as_str().unwrap_or("")
            );
        }
    }
    let empty = [
        "added",
        "removed",
        "status_changed",
        "changed",
        "broken_links",
    ]
    .iter()
    .all(|k| d[k].as_array().is_none_or(Vec::is_empty));
    Ok(if o.exit_code && !empty {
        ExitCode::from(1)
    } else {
        ExitCode::SUCCESS
    })
}
