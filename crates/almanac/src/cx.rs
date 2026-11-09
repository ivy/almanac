//! A run's context. Every outside read and model call goes through it, so a replay
//! returns recorded results instead of doing the work again.
use crate::agent::{AgentSpec, Bound, Classifier, Harness, Pass};
use crate::clock::Clock;
use crate::cooled::{Age, Cooled, Published};
use crate::effect::{AttestState, EffectKind, Forge, Mode};
use crate::providers::Providers;
use crate::store::{Store, StoreError};
use crate::taint::{Clean, Invalid, Observed, Sha, Slug, Untrusted};
use crate::wire::Malformed;
use serde::{Deserialize, Serialize, de::DeserializeOwned};
use std::collections::HashMap;
use std::future::Future;
use std::sync::{Arc, Mutex};
use std::time::Duration;
use time::OffsetDateTime;

pub trait Record: Serialize + DeserializeOwned + Send + Sync + 'static {}
impl<T: Serialize + DeserializeOwned + Send + Sync + 'static> Record for T {}

/// How long a patch counts against a key. A branch is reused for later versions of a package, so
/// old attempts must age out.
const ATTEMPT_WINDOW: Duration = Duration::from_secs(14 * 86400);

/// What has already been tried on a key.
#[derive(Clone, Copy, Debug, PartialEq, Serialize, Deserialize)]
pub struct Attempts {
    pub used: u32,
    pub asked: bool,
}

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

impl From<crate::diag::Error> for StepError {
    fn from(e: crate::diag::Error) -> Self {
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

/// What a run can reach, handed over by the runtime.
pub(crate) struct World {
    pub store: Arc<dyn Store>,
    pub clock: Arc<dyn Clock>,
    pub mode: Mode,
    pub providers: Arc<Providers>,
}

/// What a workflow declared it may do.
pub(crate) struct Declared {
    pub may: &'static [EffectKind],
    pub agents: Vec<&'static str>,
}

pub struct Cx {
    run_id: String,
    store: Arc<dyn Store>,
    clock: Arc<dyn Clock>,
    mode: Mode,
    may: &'static [EffectKind],
    /// The workflow and key of this run.
    slot: (String, String),
    counts: Mutex<HashMap<&'static str, usize>>,
    agents: Vec<&'static str>,
    providers: Arc<Providers>,
}

impl Cx {
    pub(crate) fn new(
        run_id: String,
        slot: (String, String),
        declared: Declared,
        world: World,
    ) -> Self {
        Self {
            run_id,
            store: world.store,
            clock: world.clock,
            mode: world.mode,
            may: declared.may,
            slot,
            counts: Mutex::default(),
            agents: declared.agents,
            providers: world.providers,
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

    /// Records a `Clean` value worked out from something tainted, such as a check that says only
    /// yes or no.
    pub async fn record_fact<T: Clean + Record>(
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
        Ok(match self.age(min_age, read).await? {
            Age::Passed(cooled) => Some(cooled),
            Age::Young | Age::Unknown => None,
        })
    }

    /// As `cooled`, for a caller that must tell a release to wait for from one nothing can date.
    pub async fn age(
        &self,
        min_age: Duration,
        read: impl Future<Output = Result<Published, StepError>> + Send,
    ) -> Result<Age, StepError> {
        let check = async {
            Ok(CooldownCheck {
                published: read.await?,
                checked_at: self.clock.now(),
            })
        };
        let CooldownCheck {
            published,
            checked_at,
        } = self.record("cooldown", check).await?;
        let dated = matches!(published, Published::At(_));
        Ok(match Cooled::check(published, min_age, checked_at) {
            Some(cooled) => Age::Passed(cooled),
            None if dated => Age::Young,
            None => Age::Unknown,
        })
    }

    /// How many patches this key has had recently, and whether a person was already asked. A
    /// cap on attempts is separate from chain depth: it decides when to stop and ask.
    pub async fn attempts(&self) -> Result<Attempts, StepError> {
        let (workflow, key) = &self.slot;
        let since = self.clock.now() - ATTEMPT_WINDOW;
        let count = async {
            let traces = self.store.traces(workflow, key, since).await?;
            let kind = |name: &str| traces.iter().filter(|t| t.kind == name).count();
            Ok(Attempts {
                used: kind("Push") as u32,
                asked: kind("Ask") > 0,
            })
        };
        self.record("attempts", count).await
    }

    /// Posts a `pending` status now, before an agent starts, so nothing can merge while the work
    /// is under way. Effects apply only when a run ends, which is too late for this. If the run
    /// dies, its requeued run posts the final status over this one.
    pub async fn hold(
        &self,
        context: &'static str,
        repo: &Slug,
        sha: &Sha,
    ) -> Result<(), StepError> {
        if !self.may.contains(&EffectKind::Attest) {
            return Err(StepError(
                "this workflow may not Attest, so it cannot hold".to_string(),
            ));
        }
        let key = format!("{}:hold:{context}", self.run_id);
        let post = async {
            if self.mode == Mode::Live {
                let forge = self.get::<dyn Forge>()?;
                forge
                    .attest(&key, repo, sha, context, AttestState::Pending)
                    .await?;
            }
            Ok(true)
        };
        let _: bool = self.record("hold", post).await?;
        Ok(())
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
        // An answer that is not a probability is a veto, and as a number it can be recorded.
        let answer = async move {
            let p = with.yes_probability(&Pass::new(), question, state).await?;
            Ok(if (0.0..=1.0).contains(&p) { p } else { 0.0 })
        };
        let p: f64 = self.record("screen", answer).await?;
        if p > above {
            Ok(())
        } else {
            Err(Halt::Stop("below screen threshold".to_string()))
        }
    }
}
