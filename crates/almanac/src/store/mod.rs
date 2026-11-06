//! Everything an almanac must remember across a restart. Both stores pass the suite in
//! `testing::conformance`, so the runtime cannot tell them apart.
mod memory;
mod postgres;

pub use memory::MemoryStore;
pub use postgres::PostgresStore;

use crate::runtime::Ingest;
use crate::workflow::Subject;
use futures::future::BoxFuture;
use serde_json::Value;

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
}

/// How many runs a workflow may start.
#[derive(Clone, Copy, Debug)]
pub struct Budget {
    pub workflow: &'static str,
    pub limit: u32,
}

#[derive(Debug, PartialEq)]
pub enum Claim {
    Run(Run),
    /// A workflow just reached its budget. Reported once.
    Tripped {
        workflow: String,
        held: usize,
    },
    Idle,
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

    /// Starts the oldest claimable event. Only workflows in `budgets` are considered.
    fn claim<'a>(&'a self, budgets: &'a [Budget]) -> BoxFuture<'a, Stored<Claim>>;

    /// Ends a run and links what it learned. A pending event for its key becomes claimable.
    fn release<'a>(&'a self, run_id: &'a str, links: &'a [Subject]) -> BoxFuture<'a, Stored<()>>;

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
