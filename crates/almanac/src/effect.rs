//! The closed set of ways a workflow touches the world. There is no merge, push, or close.
use crate::taint::{Slug, Untrusted};

crate::clean! {
    pub enum EffectKind { Attest, Await, File, Ask }
}

#[derive(Clone, Debug)]
pub enum Effect {
    /// Publish a status check. The repository's rules decide what it blocks.
    Attest { context: &'static str, passed: bool },
    /// Do nothing until a named fact changes.
    Await { until: &'static str },
    /// Hand work to a tracker. `body` keeps its taint all the way to the adapter.
    File {
        title: &'static str,
        target: Slug,
        body: Untrusted<String>,
    },
    /// Ask a person, and block agents until they answer.
    Ask {
        question: &'static str,
        body: Untrusted<String>,
    },
}

crate::clean! {
    /// What a guard sees. Contains no event text, so a guard cannot be injected by it.
    pub struct ActionSummary { pub kind: EffectKind, pub passed: Option<bool> }
}

impl Effect {
    pub fn kind(&self) -> EffectKind {
        match self {
            Self::Attest { .. } => EffectKind::Attest,
            Self::Await { .. } => EffectKind::Await,
            Self::File { .. } => EffectKind::File,
            Self::Ask { .. } => EffectKind::Ask,
        }
    }

    pub fn summary(&self) -> ActionSummary {
        let passed = match self {
            Self::Attest { passed, .. } => Some(*passed),
            _ => None,
        };
        ActionSummary {
            kind: self.kind(),
            passed,
        }
    }
}
