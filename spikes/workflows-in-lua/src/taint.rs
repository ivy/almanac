//! What a script holds instead of text. Each type here is userdata: a script can pass it along and
//! name a host function to apply to it, and that is all. The invariant the rest of the host rests
//! on is that no function here returns observed text as a Lua string, so every Lua string a script
//! can hold is either its own literal or a `Clean` value.
use almanac::prelude::*;
use almanac_renovate::update::{PullText, Update, update_of};
use mlua::{Error, MetaMethod, UserData, UserDataMethods};
use serde_json::Value;

/// Observed content in every shape a host function can hand a script. A closed enum, so a new
/// shape is a change to the trusted host and shows up in review.
#[derive(Clone)]
pub enum Tainted {
    Text(Untrusted<String>),
    Pull(Untrusted<PullText>),
    Update(Untrusted<Update>),
    Patch(Untrusted<Patch>),
    /// What a `Quarantined` agent said.
    Answer(Untrusted<Value>),
}

fn diff_of(patch: &Patch) -> String {
    patch.diff.clone()
}

// `Untrusted::narrow` takes `fn(&T)`, and here `T` is `String`.
#[allow(clippy::ptr_arg)]
fn is_blank(text: &String) -> bool {
    text.trim().is_empty()
}

impl Tainted {
    /// `Untrusted::map`, with the function named rather than passed. A Lua function is always a
    /// closure, so accepting one would hand it the text and let it capture it.
    fn map(&self, name: &str) -> Result<Tainted, String> {
        match (self, name) {
            (Self::Pull(text), "update_of") => Ok(Self::Update(text.map(update_of))),
            (Self::Patch(patch), "diff") => Ok(Self::Text(patch.map(diff_of))),
            (_, name) => Err(format!(
                "no map named `{name}` for this value: maps are Rust functions in the host's \
                 catalog, so add one there if a workflow needs it"
            )),
        }
    }

    /// `Untrusted::narrow`, by name for the same reason. The catalog's parameters are fixed in
    /// Rust: a narrow that took a script's string would answer "does the text contain this?",
    /// and enough such questions read the text back out.
    fn narrow(&self, name: &str) -> Result<bool, String> {
        match (self, name) {
            (Self::Text(text), "is_blank") => Ok(text.narrow(is_blank)),
            (_, name) => Err(format!(
                "no narrow named `{name}` for this value: narrows are Rust functions in the \
                 host's catalog, so add one there if a workflow needs it"
            )),
        }
    }

    /// The form agents receive, with the taint tag intact.
    pub fn tagged(&self) -> Result<Value, serde_json::Error> {
        match self {
            Self::Text(t) => serde_json::to_value(t),
            Self::Pull(t) => serde_json::to_value(t),
            Self::Update(t) => serde_json::to_value(t),
            Self::Patch(t) => serde_json::to_value(t),
            Self::Answer(t) => serde_json::to_value(t),
        }
    }
}

impl UserData for Tainted {
    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_method("map", |lua, this, name: String| {
            let mapped = this.map(&name).map_err(Error::runtime)?;
            lua.create_userdata(mapped)
        });
        methods.add_method("narrow", |_, this, name: String| {
            this.narrow(&name).map_err(Error::runtime)
        });
        // Refused rather than rendered as a placeholder: a title reading "Untrusted(..)" is a bug
        // that would otherwise reach a person.
        methods.add_meta_method(MetaMethod::ToString, |_, _, ()| -> mlua::Result<String> {
            Err(Error::runtime(
                "Untrusted has no string form: pass it on whole, or narrow it to a closed value",
            ))
        });
    }
}

/// A `Cooled` proof, held for a script by the driver of its run. This is only a ticket: the proof
/// itself never enters the VM, so it stays uncopyable and dies with the run.
#[derive(Clone, Copy)]
pub struct Ticket(pub usize);

impl UserData for Ticket {
    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_meta_method(MetaMethod::ToString, |_, _, ()| Ok("Cooled(..)"));
    }
}

/// A patch the policy accepted. Only `check_patch` makes one.
#[derive(Clone)]
pub struct CheckedPatch(pub Checked);

impl UserData for CheckedPatch {
    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_meta_method(MetaMethod::ToString, |_, _, ()| Ok("Checked(..)"));
    }
}
