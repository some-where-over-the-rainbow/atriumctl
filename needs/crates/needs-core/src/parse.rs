//! Need blocks in Markdown: MyST-style fenced directives.
//!
//! ````text
//! ```{q} Where are leases charged today?
//! :id: Q_014
//! :status: proposed
//! :area: billing
//!
//! Which component decrements a budget when a model call completes?
//! ```
//! ````
//!
//! Only fenced blocks whose info string is `{<configured type>} Title` are
//! interpreted; everything else in the file is passed over.

use std::collections::BTreeMap;

use pulldown_cmark::{CodeBlockKind, Event, Parser, Tag, TagEnd};

/// A need block as written, before typing and validation.
#[derive(Debug, Clone)]
pub struct RawNeed {
    pub directive: String,
    pub title: String,
    /// Option name -> (value, line).
    pub options: BTreeMap<String, (String, usize)>,
    /// Options given more than once: (name, line).
    pub duplicates: Vec<(String, usize)>,
    pub body: String,
    pub path: String,
    pub line: usize,
}

pub fn blocks(path: &str, text: &str) -> Vec<RawNeed> {
    let mut out = Vec::new();
    let mut cur: Option<(String, String, usize, String)> = None; // directive, title, line, content
    for (ev, range) in Parser::new(text).into_offset_iter() {
        match ev {
            Event::Start(Tag::CodeBlock(CodeBlockKind::Fenced(info))) => {
                if let Some((dir, title)) = directive(&info) {
                    let line = text[..range.start].matches('\n').count() + 1;
                    cur = Some((dir, title, line, String::new()));
                }
            }
            Event::Text(t) => {
                if let Some(c) = cur.as_mut() {
                    c.3.push_str(&t);
                }
            }
            Event::End(TagEnd::CodeBlock) => {
                if let Some((directive, title, line, content)) = cur.take() {
                    out.push(split(path, directive, title, line, &content));
                }
            }
            _ => {}
        }
    }
    out
}

/// `{q} Title` -> ("q", "Title").
fn directive(info: &str) -> Option<(String, String)> {
    let info = info.trim();
    let rest = info.strip_prefix('{')?;
    let end = rest.find('}')?;
    let name = &rest[..end];
    if name.is_empty()
        || !name
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || c == '_' || c == '-')
    {
        return None;
    }
    Some((name.to_string(), rest[end + 1..].trim().to_string()))
}

fn split(path: &str, directive: String, title: String, line: usize, content: &str) -> RawNeed {
    let mut options = BTreeMap::new();
    let mut duplicates = Vec::new();
    let mut body_start = 0;
    let lines: Vec<&str> = content.split('\n').collect();
    for (i, l) in lines.iter().enumerate() {
        match option(l) {
            Some((k, v)) => {
                let at = line + 1 + i;
                match options.entry(k) {
                    std::collections::btree_map::Entry::Occupied(e) => {
                        duplicates.push((e.key().clone(), at))
                    }
                    std::collections::btree_map::Entry::Vacant(e) => {
                        e.insert((v, at));
                    }
                }
                body_start = i + 1;
            }
            None => break,
        }
    }
    let body = lines[body_start.min(lines.len())..]
        .join("\n")
        .trim()
        .to_string();
    RawNeed {
        directive,
        title,
        options,
        duplicates,
        body,
        path: path.to_string(),
        line,
    }
}

/// `:name: value` -> ("name", "value").
fn option(line: &str) -> Option<(String, String)> {
    let rest = line.trim_end().strip_prefix(':')?;
    let end = rest.find(':')?;
    let name = &rest[..end];
    if name.is_empty()
        || !name
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || c == '_' || c == '-')
    {
        return None;
    }
    Some((name.to_string(), rest[end + 1..].trim().to_string()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_block_options_body_and_lines() {
        let text = "# Questions\n\nIntro.\n\n```{q} Where are leases charged?\n:id: Q_014\n:status: proposed\n:area: billing\n\nWhich component\ndecrements a budget?\n```\n\n```python\nprint(1)\n```\n";
        let b = blocks("questions.md", text);
        assert_eq!(b.len(), 1);
        let n = &b[0];
        assert_eq!(
            (n.directive.as_str(), n.title.as_str(), n.line),
            ("q", "Where are leases charged?", 5)
        );
        assert_eq!(n.options["id"], ("Q_014".to_string(), 6));
        assert_eq!(n.options["area"].1, 8);
        assert_eq!(n.body, "Which component\ndecrements a budget?");
    }

    #[test]
    fn ignores_non_need_fences_and_reports_duplicates() {
        let text = "```{bad name} x\n```\n```{fact} F\n:id: F_001\n:id: F_002\n```\n";
        let b = blocks("r.md", text);
        assert_eq!(b.len(), 1);
        assert_eq!(b[0].duplicates, vec![("id".to_string(), 5)]);
    }
}
