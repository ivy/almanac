//! Turns what `decide` returned into `Effect`s. There is no way to build one from a script except
//! through here, so this function is the effect set's closure: a table naming anything else stops
//! the run, and nothing it asked for happens.
use crate::value::Node;
use almanac::prelude::*;
use almanac::taint::Title;
use std::collections::BTreeMap;

type Fields = BTreeMap<String, Node>;

/// An effect's fixed text must be something the workflow declared before any event arrived. That
/// keeps what `&'static str` proves in Rust: this text cannot have come from a run.
fn literal(fields: &Fields, name: &str, literals: &[&'static str]) -> Result<&'static str, String> {
    let wanted = fields.get(name).and_then(Node::as_str).ok_or_else(|| {
        format!(
            "`{name}` must be a string, not {}",
            fields.get(name).map_or("nil", Node::shape)
        )
    })?;
    let declared = literals.iter().find(|text| **text == wanted);
    declared.copied().ok_or_else(|| {
        format!("`{name}` must be listed in the workflow's `literals`, so no run can choose it")
    })
}

fn parsed<T: std::str::FromStr>(fields: &Fields, name: &str, shape: &str) -> Result<T, String> {
    let text = fields
        .get(name)
        .and_then(Node::as_str)
        .ok_or_else(|| format!("`{name}` must be a {shape}"))?;
    text.parse()
        .map_err(|_| format!("`{name}` is not a {shape}"))
}

/// A question or title. Hostile text is never a Lua string, so a string here holds only literals
/// and `Clean` values, which is what `title!` requires.
fn title(fields: &Fields, name: &str) -> Result<Title, String> {
    match fields.get(name) {
        Some(Node::Str(text)) => Ok(Title::__compose(text.clone())),
        Some(other) => Err(format!(
            "`{name}` must be a string built from clean values, not {}",
            other.shape()
        )),
        None => Err(format!("`{name}` is missing")),
    }
}

fn body(fields: &Fields) -> Result<Untrusted<String>, String> {
    match fields.get("body") {
        None => Ok(Untrusted::new(String::new())),
        Some(Node::Str(text)) => Ok(Untrusted::new(text.clone())),
        Some(Node::Tainted(crate::taint::Tainted::Text(text))) => Ok(text.clone()),
        Some(other) => Err(format!("`body` must be text, not {}", other.shape())),
    }
}

fn exactly(fields: &Fields, kind: &str, allowed: &[&str]) -> Result<(), String> {
    match fields
        .keys()
        .find(|k| *k != "kind" && !allowed.contains(&k.as_str()))
    {
        Some(extra) => Err(format!("{kind} has no field `{extra}`")),
        None => Ok(()),
    }
}

fn effect(node: &Node, literals: &[&'static str]) -> Result<Effect, String> {
    let Node::Map(fields) = node else {
        return Err(format!("an effect is a table, not {}", node.shape()));
    };
    let kind = fields
        .get("kind")
        .and_then(Node::as_str)
        .unwrap_or("nothing");
    match kind {
        "Attest" => {
            exactly(fields, kind, &["context", "state", "repo", "sha"])?;
            let state = fields.get("state").and_then(Node::as_str).unwrap_or("");
            Ok(Effect::Attest {
                context: literal(fields, "context", literals)?,
                state: serde_json::from_value(state.into())
                    .map_err(|_| "`state` must be Pending, Success, or Failure".to_string())?,
                repo: parsed(fields, "repo", "slug")?,
                sha: parsed(fields, "sha", "full commit hash")?,
            })
        }
        "Await" => {
            exactly(fields, kind, &["awaiting"])?;
            Ok(Effect::Await {
                until: literal(fields, "awaiting", literals)?,
            })
        }
        "File" => {
            exactly(fields, kind, &["title", "target", "body"])?;
            Ok(Effect::File {
                title: title(fields, "title")?,
                target: parsed(fields, "target", "slug")?,
                body: body(fields)?,
            })
        }
        "Ask" => {
            exactly(fields, kind, &["question", "target", "body"])?;
            Ok(Effect::Ask {
                question: title(fields, "question")?,
                target: parsed(fields, "target", "slug")?,
                body: body(fields)?,
            })
        }
        "Push" => {
            exactly(
                fields,
                kind,
                &["repo", "pull", "branch", "message", "hold", "patch"],
            )?;
            let Some(Node::Checked(patch)) = fields.get("patch") else {
                return Err(format!(
                    "Push needs what check_patch returned, not {}",
                    fields.get("patch").map_or("nil", Node::shape)
                ));
            };
            Ok(Effect::Push {
                repo: parsed(fields, "repo", "slug")?,
                pull: fields
                    .get("pull")
                    .and_then(Node::as_u64)
                    .ok_or_else(|| "`pull` must be a pull request's number".to_string())?,
                branch: parsed(fields, "branch", "slug")?,
                message: literal(fields, "message", literals)?,
                hold: match fields.contains_key("hold") {
                    true => Some(literal(fields, "hold", literals)?),
                    false => None,
                },
                patch: patch.clone(),
            })
        }
        other => Err(format!(
            "`{other}` is not an effect: effects are a closed set, Attest, Await, File, Ask, and \
             Push, and merging is the repository's job"
        )),
    }
}

/// All of them or none: one bad effect stops the run before anything is recorded.
pub fn effects_of(node: &Node, literals: &[&'static str]) -> Result<Vec<Effect>, String> {
    let list = node.as_list().ok_or_else(|| {
        format!(
            "`decide` must return a list of effects, not {}",
            node.shape()
        )
    })?;
    list.iter().map(|item| effect(item, literals)).collect()
}
