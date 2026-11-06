//! A run's context. Every outside read and model call goes through it, so a replay
//! returns recorded results instead of doing the work again.
use crate::agent::{AgentSpec, Bound, Classifier, Harness, Pass};
use crate::cooled::{Cooled, Published};
use crate::providers::Providers;
use crate::store::{Store, StoreError};
use crate::taint::{Invalid, Observed, Untrusted};
use crate::wire::Malformed;
use serde::{Deserialize, Serialize, de::DeserializeOwned};
use std::collections::HashMap;
use std::future::Future;
use std::sync::{Arc, Mutex};
use std::time::Duration;
use time::OffsetDateTime;

pub trait Record: Serialize + DeserializeOwned + Send + Sync + 'static {}
impl<T: Serialize + DeserializeOwned + Send + Sync + 'static> Record for T {}

#[derive(Serialize, Deserialize)]
struct CooldownCheck {
    published: Published,
    #[serde(with = "time::serde::rfc3339")]
    checked_at: OffsetDateTime,
}

#[derive(Debug)]
pub struct StepError(pub String);

impl From<Malformed> for StepError {
    fn from(e: Malformed) -> Self {
        Self(e.to_string())
    }
}

impl From<Invalid> for StepError {
    fn from(e: Invalid) -> Self {
        Self(e.to_string())
    }
}

impl From<StoreError> for StepError {
    fn from(e: StoreError) -> Self {
        Self(e.to_string())
    }
}

impl From<serde_json::Error> for StepError {
    fn from(e: serde_json::Error) -> Self {
        Self(e.to_string())
    }
}

/// Why `gather` ended early: a recorded decision to stop, or a failure.
#[derive(Debug)]
pub enum Halt {
    Stop(String),
    Failed(StepError),
}

impl<E: Into<StepError>> From<E> for Halt {
    fn from(e: E) -> Self {
        Self::Failed(e.into())
    }
}

pub struct Cx {
    run_id: String,
    store: Arc<dyn Store>,
    counts: Mutex<HashMap<&'static str, usize>>,
    agents: Vec<&'static str>,
    providers: Arc<Providers>,
}

impl Cx {
    pub(crate) fn new(
        run_id: String,
        store: Arc<dyn Store>,
        agents: Vec<&'static str>,
        providers: Arc<Providers>,
    ) -> Self {
        Self {
            run_id,
            store,
            counts: Mutex::default(),
            agents,
            providers,
        }
    }

    /// A provider set up in the binary. `#[workflow]` checks these at startup.
    pub fn get<T: ?Sized + Send + Sync + 'static>(&self) -> Result<Arc<T>, StepError> {
        let missing = || StepError(format!("no provider for {}", std::any::type_name::<T>()));
        self.providers.get::<T>().ok_or_else(missing)
    }

    /// An agent for this run. Whether it can `read` or `act` comes from its profile.
    pub fn agent<P, T>(&self, spec: AgentSpec<P, T>) -> Bound<'_, P, T> {
        Bound { cx: self, spec }
    }

    pub(crate) fn harness_of(&self, agent: &str) -> Result<Arc<dyn Harness>, StepError> {
        self.providers
            .harness_of(agent)
            .ok_or_else(|| StepError(format!("no harness for agent `{agent}`")))
    }

    /// Keyed by name and occurrence, so a branch in one place cannot shift another's result.
    pub(crate) async fn record<T: Record>(
        &self,
        name: &'static str,
        work: impl Future<Output = Result<T, StepError>> + Send,
    ) -> Result<T, StepError> {
        let occurrence = {
            let mut counts = self.counts.lock().unwrap();
            let n = counts.entry(name).or_default();
            *n += 1;
            *n
        };
        if let Some(value) = self.store.recorded(&self.run_id, name, occurrence).await? {
            return Ok(serde_json::from_value(value)?);
        }
        let out = work.await?;
        let value = serde_json::to_value(&out)?;
        self.store
            .record(&self.run_id, name, occurrence, value)
            .await?;
        Ok(out)
    }

    pub(crate) fn ensure_declared(&self, agent: &'static str) -> Result<(), StepError> {
        match self.agents.contains(&agent) {
            true => Ok(()),
            false => Err(StepError(format!(
                "agent `{agent}` is not declared by this workflow"
            ))),
        }
    }

    /// Read from the outside world. The result must be `Observed`, so its text is tainted.
    pub async fn observe<T: Observed + Record>(
        &self,
        name: &'static str,
        read: impl Future<Output = Result<T, StepError>> + Send,
    ) -> Result<T, StepError> {
        self.record(name, read).await
    }

    /// Whether a release has aged past `min_age`, from a publish time read now. Both the
    /// publish time and the moment of the check are recorded, so a replay reaches the same
    /// answer.
    pub async fn cooled(
        &self,
        min_age: Duration,
        read: impl Future<Output = Result<Published, StepError>> + Send,
    ) -> Result<Option<Cooled>, StepError> {
        let check = async {
            Ok(CooldownCheck {
                published: read.await?,
                checked_at: OffsetDateTime::now_utc(),
            })
        };
        let CooldownCheck {
            published,
            checked_at,
        } = self.record("cooldown", check).await?;
        Ok(Cooled::check(published, min_age, checked_at))
    }

    /// Stop unless a cheap classifier clears `above`. This gates spending and never safety,
    /// because adversarial text can move a classifier.
    pub async fn screen(
        &self,
        question: &'static str,
        text: &Untrusted<String>,
        above: f64,
    ) -> Result<(), Halt> {
        let with = self.get::<dyn Classifier>()?;
        let state = serde_json::to_value(text).map_err(StepError::from)?;
        let p: f64 = self
            .record(
                "screen",
                with.yes_probability(&Pass::new(), question, state),
            )
            .await?;
        if p > above {
            Ok(())
        } else {
            Err(Halt::Stop("below screen threshold".to_string()))
        }
    }
}
