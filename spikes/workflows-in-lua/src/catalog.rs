//! What the trusted host offers a script, by name. Scripts choose from these lists and cannot add
//! to them, so everything a script can read or trigger on is Rust that went through review.
use crate::value::Node;
use almanac::cooled::Cooled;
use almanac::prelude::*;
use almanac::providers::Requirement;
use almanac::workflow::Found;
use futures::future::BoxFuture;
use serde::Serialize;
use serde_json::Value;
use std::collections::BTreeMap;

/// What a script may do with an event's field.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Field {
    /// A `Clean` value: fit for a key.
    Clean,
    /// `Slug` in particular: fit for a subject too.
    Slug,
    /// `Untrusted`: a handle only.
    Tainted,
}

pub type Parse = fn(&Value) -> Result<Value, Malformed>;

/// A type a workflow can trigger on. Events stay in Rust: parsing a payload is where text becomes
/// tainted, so it cannot be done by something that can read text.
#[derive(Clone, Copy)]
pub struct EventSpec {
    pub kind: &'static str,
    pub also: &'static [&'static str],
    pub fields: &'static [(&'static str, Field)],
    pub parse: Parse,
}

/// Parses to `Node`-ready JSON. `Untrusted` fields keep their tag.
pub fn parse_event<E: Event + Serialize>(payload: &Value) -> Result<Value, Malformed> {
    let event = E::from_json(payload)?;
    serde_json::to_value(&event).map_err(|_| Malformed {
        pointer: "/".to_string(),
    })
}

pub type Read =
    for<'a> fn(&'a Cx, Vec<Node>, &'a mut Vec<Cooled>) -> BoxFuture<'a, Result<Node, Halt>>;

/// A recorded read. Being a Rust function is what lets it decide which text is tainted.
#[derive(Clone, Copy)]
pub struct ReadSpec {
    pub requires: fn() -> Vec<Requirement>,
    pub run: Read,
}

pub type Scan = for<'a> fn(&'a Scanner) -> BoxFuture<'a, Result<Vec<Found>, StepError>>;

/// A way to list current subjects, for backfill and catch-up.
#[derive(Clone, Copy)]
pub struct ScanSpec {
    pub event: &'static str,
    pub requires: fn() -> Vec<Requirement>,
    pub run: Scan,
}

#[derive(Clone, Default)]
pub struct Catalog {
    pub events: BTreeMap<&'static str, EventSpec>,
    pub reads: BTreeMap<&'static str, ReadSpec>,
    pub scans: BTreeMap<&'static str, ScanSpec>,
    /// Embedded in the binary. A script names one and never supplies the text.
    pub prompts: BTreeMap<&'static str, &'static str>,
}
