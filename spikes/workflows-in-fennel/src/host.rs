//! What the trusted side offers a workflow: the events it can name, the prompts built into the
//! binary, and the providers it can ask for. Everything here is Rust and is compiled in.
use crate::value::Script;
use almanac::prelude::*;
use almanac::providers::Requirement;
use almanac_renovate::events::BuildChanged;
use almanac_renovate::update::Ages;
use almanac_renovate::workflows::build_failures::ScanTarget;
use github::client::GithubClient;
use serde::Serialize;
use serde_json::Value;

const FIXER_PROMPT: &str = include_str!("../../../almanacs/renovate/prompts/fixer.md");

/// Whether a field of an event may reach a key or a subject.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Field {
    Clean,
    Tainted,
}

/// An event a workflow can trigger on. The event itself stays a Rust type, so `observed!` and
/// `Payload` still decide what is tainted.
#[derive(Clone)]
pub struct EventDef {
    pub name: &'static str,
    pub kind: &'static str,
    pub also: &'static [&'static str],
    pub fields: &'static [(&'static str, Field)],
    pub parse: fn(&Value) -> Result<Value, Malformed>,
}

fn parse<E: Event + Serialize>(payload: &Value) -> Result<Value, Malformed> {
    let event = E::from_json(payload)?;
    serde_json::to_value(event).map_err(|_| Malformed {
        pointer: "/".to_string(),
    })
}

impl EventDef {
    pub fn of<E: Event + Serialize>(
        name: &'static str,
        fields: &'static [(&'static str, Field)],
    ) -> Self {
        Self {
            name,
            kind: E::KIND,
            also: E::ALSO,
            fields,
            parse: parse::<E>,
        }
    }
}

/// A provider a workflow can ask for with `cx.get`.
#[derive(Clone)]
pub struct Need {
    pub name: &'static str,
    pub requirement: Requirement,
    /// What `cx.get` hands the script. Providers that act through steps hand it nothing.
    pub resolve: fn(&Cx) -> Result<Script, StepError>,
}

#[derive(Clone)]
pub struct Host {
    pub events: Vec<EventDef>,
    pub prompts: Vec<(&'static str, &'static str)>,
    pub needs: Vec<Need>,
}

fn nothing_to_hand_over<T: ?Sized + Send + Sync + 'static>(cx: &Cx) -> Result<Script, StepError> {
    cx.get::<T>().map(|_| Script::Nil)
}

fn scan_target(cx: &Cx) -> Result<Script, StepError> {
    let target = cx.get::<ScanTarget>()?;
    Ok(Script::map([
        ("owner", Script::text(&target.owner)),
        ("author", Script::text(&target.author)),
    ]))
}

impl Host {
    /// What `almanacs/renovate` offers.
    pub fn renovate() -> Self {
        use Field::Clean;
        let fields: &'static [(&'static str, Field)] = &[
            ("repo", Clean),
            ("branch", Clean),
            ("sha", Clean),
            ("failed", Clean),
            ("finished", Clean),
            ("patched", Clean),
            ("bookkeeping", Clean),
        ];
        Self {
            events: vec![EventDef::of::<BuildChanged>("BuildChanged", fields)],
            prompts: vec![("fixer.md", FIXER_PROMPT)],
            needs: vec![
                Need {
                    name: "github",
                    requirement: Requirement::of::<GithubClient>(),
                    resolve: nothing_to_hand_over::<GithubClient>,
                },
                Need {
                    name: "ages",
                    requirement: Requirement::of::<dyn Ages>(),
                    resolve: nothing_to_hand_over::<dyn Ages>,
                },
                Need {
                    name: "scan-target",
                    requirement: Requirement::of::<ScanTarget>(),
                    resolve: scan_target,
                },
            ],
        }
    }
}
