//! The closed set of ways a workflow touches the world. A patch can be pushed to a pull
//! request's own branch. Nothing here merges or closes: the repository's rules do that.
use crate::cx::StepError;
use crate::patch::{Checked, Patch};
use crate::taint::{Sha, Slug, Untrusted};
use futures::future::BoxFuture;
use serde::{Deserialize, Serialize};

crate::clean! {
    pub enum EffectKind { Attest, Await, File, Ask, Push }
}

crate::clean! {
    /// A status check's state. `Pending` holds a merge as firmly as `Failure`.
    pub enum AttestState { Pending, Success, Failure }
}

#[derive(Clone, Debug)]
pub enum Effect {
    /// Publish a status check on a commit. The repository's rules decide what it blocks.
    Attest {
        context: &'static str,
        state: AttestState,
        repo: Slug,
        sha: Sha,
    },
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
        target: Slug,
        body: Untrusted<String>,
    },
    /// Commit a checked patch to a pull request's own branch. A forge refuses it unless
    /// `branch` is that pull request's head and has not moved since `patch` was checked.
    Push {
        repo: Slug,
        pull: u64,
        branch: Slug,
        message: &'static str,
        /// A status to set `pending` on the new commit before the branch moves, so nothing can
        /// merge between the push and CI finishing.
        hold: Option<&'static str>,
        patch: Checked,
    },
}

crate::clean! {
    /// What a guard sees. Contains no event text, so a guard cannot be injected by it.
    pub struct ActionSummary { pub kind: EffectKind, pub attests: Option<AttestState> }
}

impl Effect {
    pub fn kind(&self) -> EffectKind {
        match self {
            Self::Attest { .. } => EffectKind::Attest,
            Self::Await { .. } => EffectKind::Await,
            Self::File { .. } => EffectKind::File,
            Self::Ask { .. } => EffectKind::Ask,
            Self::Push { .. } => EffectKind::Push,
        }
    }

    pub fn summary(&self) -> ActionSummary {
        let attests = match self {
            Self::Attest { state, .. } => Some(*state),
            _ => None,
        };
        ActionSummary {
            kind: self.kind(),
            attests,
        }
    }
}

/// An effect as the outbox stores it. `Untrusted` bodies stay tagged in the stored JSON.
#[derive(Clone, Debug, Serialize, Deserialize)]
pub enum Planned {
    Attest {
        context: String,
        state: AttestState,
        repo: Slug,
        sha: Sha,
    },
    Await {
        until: String,
    },
    File {
        title: String,
        target: Slug,
        body: Untrusted<String>,
    },
    Ask {
        question: String,
        target: Slug,
        body: Untrusted<String>,
    },
    /// The patch stays tagged as tainted when stored. It was checked before it was recorded.
    Push {
        repo: Slug,
        pull: u64,
        branch: Slug,
        message: String,
        hold: Option<String>,
        base: Sha,
        patch: Untrusted<Patch>,
    },
}

impl From<&Effect> for Planned {
    fn from(effect: &Effect) -> Self {
        match effect.clone() {
            Effect::Attest {
                context,
                state,
                repo,
                sha,
            } => Self::Attest {
                context: context.to_string(),
                state,
                repo,
                sha,
            },
            Effect::Await { until } => Self::Await {
                until: until.to_string(),
            },
            Effect::File {
                title,
                target,
                body,
            } => Self::File {
                title: title.to_string(),
                target,
                body,
            },
            Effect::Ask {
                question,
                target,
                body,
            } => Self::Ask {
                question: question.to_string(),
                target,
                body,
            },
            Effect::Push {
                repo,
                pull,
                branch,
                message,
                hold,
                patch,
            } => Self::Push {
                repo,
                pull,
                branch,
                message: message.to_string(),
                hold: hold.map(str::to_string),
                base: patch.base().clone(),
                patch: Untrusted::new(patch.patch().clone()),
            },
        }
    }
}

/// Whether effects reach the world. Independent of where an event came from.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub enum Mode {
    Live,
    /// Records what would have happened and calls no provider.
    #[default]
    DryRun,
}

/// Where statuses go. `key` is stable across retries, so a repeat must change nothing.
pub trait Forge: Send + Sync {
    fn attest<'a>(
        &'a self,
        key: &'a str,
        repo: &'a Slug,
        sha: &'a Sha,
        context: &'a str,
        state: AttestState,
    ) -> BoxFuture<'a, Result<(), StepError>>;

    /// Commits a patch to `branch`, which must be the head of pull request `pull` in `repo`
    /// and still at `base`. Anything else is an error and changes nothing.
    fn push<'a>(&'a self, key: &'a str, push: &'a Push) -> BoxFuture<'a, Result<(), StepError>>;
}

/// Recovers deliveries the webhook source failed to send, where it can.
pub trait Sweep: Send + Sync {
    /// Asks for failed deliveries to be sent again. Returns how many.
    fn sweep(&self) -> BoxFuture<'_, Result<usize, StepError>>;
}

/// A commit for a forge to make.
#[derive(Clone, Debug)]
pub struct Push {
    pub repo: Slug,
    pub pull: u64,
    pub branch: Slug,
    pub message: String,
    pub hold: Option<String>,
    pub base: Sha,
    pub patch: Untrusted<Patch>,
}

/// Where work and questions go. `key` is stable across retries, so a repeat must change nothing.
pub trait Tracker: Send + Sync {
    fn file<'a>(
        &'a self,
        key: &'a str,
        target: &'a Slug,
        title: &'a str,
        body: &'a Untrusted<String>,
    ) -> BoxFuture<'a, Result<(), StepError>>;

    fn ask<'a>(
        &'a self,
        key: &'a str,
        target: &'a Slug,
        question: &'a str,
        body: &'a Untrusted<String>,
    ) -> BoxFuture<'a, Result<(), StepError>>;
}
