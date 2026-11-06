//! A run's context. Every outside read and model call goes through it, so a replay
//! returns recorded results instead of doing the work again.
use crate::agent::{AgentSpec, Bound, Classifier, Harness, Pass};
use crate::providers::Providers;
use crate::taint::{Observed, Untrusted};
use serde::{Serialize, de::DeserializeOwned};
use serde_json::Value;
use std::collections::HashMap;
use std::future::Future;
use std::sync::{Arc, Mutex};

pub trait Record: Serialize + DeserializeOwned + Send + Sync + 'static {}
impl<T: Serialize + DeserializeOwned + Send + Sync + 'static> Record for T {}

#[derive(Debug)]
pub struct StepError(pub String);

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

pub(crate) type RunLog = Arc<Mutex<HashMap<(String, String, usize), Value>>>;

pub struct Cx {
    run_id: String,
    log: RunLog,
    counts: Mutex<HashMap<&'static str, usize>>,
    agents: Vec<&'static str>,
    providers: Arc<Providers>,
}

impl Cx {
    pub(crate) fn new(
        run_id: String,
        log: RunLog,
        agents: Vec<&'static str>,
        providers: Arc<Providers>,
    ) -> Self {
        Self {
            run_id,
            log,
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
        let key = (self.run_id.clone(), name.to_string(), occurrence);
        let recorded = self.log.lock().unwrap().get(&key).cloned();
        if let Some(value) = recorded {
            return Ok(serde_json::from_value(value)?);
        }
        let out = work.await?;
        let value = serde_json::to_value(&out)?;
        self.log.lock().unwrap().insert(key, value);
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
