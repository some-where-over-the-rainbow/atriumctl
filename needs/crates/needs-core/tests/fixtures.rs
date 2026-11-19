use std::collections::BTreeMap;
use std::fs;
use std::path::Path;

use needs_core::{Config, Context, Query, Source, api, build, lint};
use serde_json::{Value, json};

fn root() -> &'static Path {
    Path::new(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../fixtures/slice1"
    ))
}

fn load(set: &str) -> (Config, String, Vec<Source>, Context) {
    let cfg_text = fs::read_to_string(root().join("needs.toml")).unwrap();
    let cfg = Config::parse(&cfg_text).expect("config is valid");
    let mut sources = vec![];
    for name in ["questions.md", "research.md"] {
        sources.push(Source {
            path: name.into(),
            text: fs::read_to_string(root().join(set).join(name)).unwrap(),
        });
    }
    let files: BTreeMap<String, usize> =
        serde_json::from_str(&fs::read_to_string(root().join(set).join("files.json")).unwrap())
            .unwrap();
    (cfg, cfg_text, sources, Context { files: Some(files) })
}

#[test]
fn clean_set_has_no_findings() {
    let (cfg, _, sources, ctx) = load("clean");
    let r = lint(&cfg, &sources, &ctx);
    assert!(r.findings.is_empty(), "{:#?}", r.findings);
    assert_eq!(r.needs.len(), 5);
    assert_eq!(
        r.get("Q_001").unwrap().incoming["answered_by"],
        vec!["F_001"]
    );
    assert_eq!(
        r.get("F_001").unwrap().fields["evidence"],
        json!("src/ui/player.py:12-40")
    );
}

#[test]
fn broken_set_reports_each_problem_once() {
    let (cfg, _, sources, ctx) = load("broken");
    let r = lint(&cfg, &sources, &ctx);
    let got: Vec<(String, String, usize, String)> = r
        .findings
        .iter()
        .map(|f| {
            (
                f.rule.clone(),
                f.path.clone(),
                f.line,
                format!("{:?}", f.severity),
            )
        })
        .collect();
    let want: Vec<(&str, &str, usize, &str)> = vec![
        ("id-format", "questions.md", 10, "Error"),
        ("field-schema", "questions.md", 11, "Error"), // status "done"
        ("field-schema", "questions.md", 18, "Error"), // empty area
        ("id-duplicate", "questions.md", 15, "Error"),
        ("title-missing", "questions.md", 21, "Error"),
        ("field-unknown", "questions.md", 25, "Error"), // :color:
        ("approved-q-answered", "questions.md", 21, "Warning"), // Q_004
        ("unknown-type", "questions.md", 28, "Warning"),
        ("link-broken", "research.md", 3, "Error"), // Q_009
        ("fact-answers-approved-q", "research.md", 3, "Error"),
        ("evidence-exists", "research.md", 3, "Error"), // missing file
        ("field-schema", "research.md", 12, "Error"), // status approved is allowed by schema? no: see below
        ("link-target-type", "research.md", 10, "Error"), // answers F_001
        ("field-required", "research.md", 10, "Error"), // no evidence
        ("option-duplicate", "research.md", 21, "Error"),
        ("evidence-exists", "research.md", 16, "Error"), // line 500 of 80
    ];
    let mut want: Vec<(String, String, usize, String)> = want
        .into_iter()
        .map(|(a, b, c, d)| (a.into(), b.into(), c, d.into()))
        .collect();
    // status "approved" is a valid enum value: the status guard is the gateway's, not the schema's.
    want.retain(|w| !(w.0 == "field-schema" && w.1 == "research.md"));
    let mut g = got.clone();
    g.sort();
    want.sort();
    assert_eq!(g, want, "\nfindings: {:#?}", r.findings);
}

#[test]
fn config_errors_are_findings() {
    let errs = Config::parse("[needs]\ntypes = []\n[needs.fields.x]\nschema = { type = 5 }\n")
        .unwrap_err();
    let msgs: Vec<&str> = errs.iter().map(|f| f.message.as_str()).collect();
    assert!(msgs.iter().any(|m| m.contains("no need types")), "{msgs:?}");
    assert!(
        msgs.iter().any(|m| m.contains("field x: schema")),
        "{msgs:?}"
    );
    let errs = Config::parse("[needs]\nbogus = 1\ntypes = []\n").unwrap_err();
    assert!(errs[0].message.contains("bogus"), "{errs:?}");
}

#[test]
fn build_is_reproducible_and_sphinx_shaped() {
    let (cfg, text, sources, ctx) = load("clean");
    let r = lint(&cfg, &sources, &ctx);
    let a = build::needs_json(&cfg, &text, &sources, &r);
    let mut rev = sources.clone();
    rev.reverse();
    let b = build::needs_json(&cfg, &text, &rev, &lint(&cfg, &rev, &ctx));
    assert_eq!(
        serde_json::to_string(&a).unwrap(),
        serde_json::to_string(&b).unwrap()
    );
    let q1 = &a["versions"][""]["needs"]["Q_001"];
    assert_eq!(q1["type"], "q");
    assert_eq!(q1["answered_by"], json!(["F_001"]));
    assert_eq!(q1["docname"], "questions.md");
    assert_eq!(a["counts"]["q"]["approved"], 2);
    assert_eq!(a["versions"][""]["needs_amount"], 5);
}

#[test]
fn diff_reports_status_changes_and_broken_links() {
    let (cfg, text, mut sources, ctx) = load("clean");
    let old = build::needs_json(&cfg, &text, &sources, &lint(&cfg, &sources, &ctx));
    sources[0].text = sources[0]
        .text
        .replace(
            ":id: Q_003\n:status: rejected",
            ":id: Q_003\n:status: approved",
        )
        .replace(
            "```{q} How is navigation between views done?\n:id: Q_002",
            "```{q} How is navigation between views done?\n:id: Q_012",
        );
    let new = build::needs_json(&cfg, &text, &sources, &lint(&cfg, &sources, &ctx));
    let d = build::diff(&cfg, &old, &new);
    assert_eq!(d.added, vec!["Q_012"]);
    assert_eq!(d.removed, vec!["Q_002"]);
    assert_eq!(
        d.status_changed,
        vec![(
            "Q_003".into(),
            Some("rejected".into()),
            Some("approved".into())
        )]
    );
    assert_eq!(
        d.broken_links,
        vec![("F_002".into(), "answers".into(), "Q_002".into())]
    );
}

#[test]
fn query_open_questions() {
    let (cfg, _, mut sources, ctx) = load("clean");
    sources[1].text = sources[1]
        .text
        .replace(":answers: Q_002", ":answers: Q_001");
    let r = lint(&cfg, &sources, &ctx);
    let q = Query {
        kind: Some("q".into()),
        status: Some("approved".into()),
        without_incoming: Some("answers".into()),
        ..Default::default()
    };
    let ids: Vec<&str> = build::query(&cfg, &r, &q)
        .iter()
        .map(|n| n.id.as_str())
        .collect();
    assert_eq!(ids, vec!["Q_002"]);
}

#[test]
fn api_matches_direct_calls() {
    let (cfg, text, sources, ctx) = load("broken");
    let req = json!({"op": "lint", "config": text, "sources": sources, "context": ctx});
    let resp = api::handle(&req.to_string());
    let direct = serde_json::to_value(lint(&cfg, &sources, &ctx)).unwrap();
    assert_eq!(resp["report"], direct);
    let bad: Value = api::handle(r#"{"op":"lint","config":"[needs]\ntypes=[]"}"#);
    assert_eq!(bad["report"]["findings"][0]["rule"], "config");
}
