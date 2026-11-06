//! Everything an almanac must remember across a restart. Both stores pass the suite in
//! `testing::conformance`, so the runtime cannot tell them apart.
mod memory;
mod postgres;

pub use memory::MemoryStore;
pub use postgres::PostgresStore;

use crate::runtime::{Ingest, Origin};
use crate::workflow::Subject;
use futures::future::BoxFuture;
use serde_json::Value;
use std::time::Duration;
use time::OffsetDateTime;

#[derive(Debug, Clone, PartialEq)]
pub struct StoreError(pub String);

impl std::fmt::Display for StoreError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "store: {}", self.0)
    }
}

impl std::error::Error for StoreError {}

pub type Stored<T> = Result<T, StoreError>;

/// An event waiting for its key to be free.
#[derive(Clone, Debug)]
pub struct Pending {
    pub workflow: String,
    pub key: String,
    pub payload: Value,
    pub subjects: Vec<Subject>,
    pub origin: Origin,
}

/// What to do with a delivery, decided before it reaches the store.
#[derive(Clone, Debug)]
pub enum Admission {
    /// Remember the delivery and stop, reporting this.
    Drop(Ingest),
    Queue(Pending),
}

#[derive(Clone, Debug, PartialEq)]
pub struct Run {
    pub id: String,
    pub workflow: String,
    pub key: String,
    pub payload: Value,
    pub origin: Origin,
}

/// How many runs a workflow may start in a window.
#[derive(Clone, Copy, Debug)]
pub struct Budget {
    pub workflow: &'static str,
    pub limit: u32,
}

/// What a claim may start, and when it is being asked.
#[derive(Clone, Copy, Debug)]
pub struct ClaimRequest<'a> {
    pub budgets: &'a [Budget],
    /// Runs all workflows together may start in a window.
    pub overall: Option<u32>,
    pub window: Duration,
    pub now: OffsetDateTime,
}

#[derive(Debug, PartialEq)]
pub enum Claim {
    Run(Run),
    /// A budget was reached with work waiting. Reported once per window, so a person is asked
    /// once. `workflow` is `None` for the overall budget.
    Tripped {
        workflow: Option<String>,
        held: usize,
    },
    Idle,
}

/// An effect recorded by a finished run.
#[derive(Clone, Debug, PartialEq)]
pub struct Entry {
    /// `run-id:index`, stable across retries.
    pub key: String,
    /// `None` for an effect no run produced, such as a budget alert.
    pub run_id: Option<String>,
    pub effect: Value,
    pub status: EntryStatus,
    pub attempts: u32,
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub enum EntryStatus {
    Pending,
    Applied,
    /// Recorded by a dry run, and never delivered.
    WouldApply,
    /// Gave up after repeated failures.
    Failed,
}

impl EntryStatus {
    pub(crate) fn as_str(self) -> &'static str {
        match self {
            Self::Pending => "pending",
            Self::Applied => "applied",
            Self::WouldApply => "would-apply",
            Self::Failed => "failed",
        }
    }

    pub(crate) fn parse(text: &str) -> Stored<Self> {
        match text {
            "pending" => Ok(Self::Pending),
            "applied" => Ok(Self::Applied),
            "would-apply" => Ok(Self::WouldApply),
            "failed" => Ok(Self::Failed),
            other => Err(StoreError(format!("unknown outbox status `{other}`"))),
        }
    }
}

pub trait Store: Send + Sync {
    /// Marks a delivery seen and queues its work as one atomic step, so a failure leaves
    /// nothing behind and a redelivery is accepted.
    fn admit<'a>(
        &'a self,
        source: &'a str,
        delivery_id: &'a str,
        admission: Admission,
    ) -> BoxFuture<'a, Stored<Ingest>>;

    /// Starts the next claimable event: live events before scanned ones, then oldest first.
    /// Only workflows in `budgets` are considered.
    fn claim<'a>(&'a self, request: ClaimRequest<'a>) -> BoxFuture<'a, Stored<Claim>>;

    /// Records an effect no run produced. A repeated `key` changes nothing.
    fn note<'a>(
        &'a self,
        key: &'a str,
        effect: Value,
        status: EntryStatus,
    ) -> BoxFuture<'a, Stored<()>>;

    /// Ends a run, links what it learned, and records its effects, as one atomic step.
    /// A pending event for its key becomes claimable.
    fn release<'a>(
        &'a self,
        run_id: &'a str,
        links: &'a [Subject],
        effects: Vec<Value>,
        status: EntryStatus,
    ) -> BoxFuture<'a, Stored<()>>;

    /// Effects waiting to be applied, oldest first.
    fn undelivered(&self) -> BoxFuture<'_, Stored<Vec<Entry>>>;

    /// A run's recorded effects, in order.
    fn outbox<'a>(&'a self, run_id: &'a str) -> BoxFuture<'a, Stored<Vec<Entry>>>;

    fn delivered<'a>(&'a self, key: &'a str) -> BoxFuture<'a, Stored<()>>;

    /// Counts a failed attempt. At `give_up` attempts the entry stops being retried.
    fn delivery_failed<'a>(
        &'a self,
        key: &'a str,
        error: &'a str,
        give_up: u32,
    ) -> BoxFuture<'a, Stored<()>>;

    /// Requeues runs a crash left running. Returns how many.
    fn recover(&self) -> BoxFuture<'_, Stored<usize>>;

    fn run<'a>(&'a self, run_id: &'a str) -> BoxFuture<'a, Stored<Option<Run>>>;

    fn recorded<'a>(
        &'a self,
        run_id: &'a str,
        name: &'a str,
        occurrence: usize,
    ) -> BoxFuture<'a, Stored<Option<Value>>>;

    fn record<'a>(
        &'a self,
        run_id: &'a str,
        name: &'a str,
        occurrence: usize,
        value: Value,
    ) -> BoxFuture<'a, Stored<()>>;

    /// Run IDs linked to a subject, oldest first.
    fn runs_for<'a>(&'a self, subject: &'a Subject) -> BoxFuture<'a, Stored<Vec<String>>>;
}
