//! What an almanac's workflows depend on, set up once in the binary.
use crate::agent::{AgentSpec, Classifier, Harness};
use serde::Serialize;
use std::any::{type_name, Any, TypeId};
use std::collections::HashMap;
use std::sync::Arc;

/// Something a workflow needs. Checked when the binary starts, before any run.
#[derive(Clone, Copy, Debug, Serialize)]
pub struct Requirement {
    #[serde(skip)]
    id: TypeId,
    name: &'static str,
}

impl Requirement {
    pub fn of<T: ?Sized + 'static>() -> Self {
        Self { id: TypeId::of::<T>(), name: type_name::<T>() }
    }
    pub fn name(&self) -> &'static str {
        self.name
    }
}

#[derive(Default)]
pub struct Providers {
    items: HashMap<TypeId, Box<dyn Any + Send + Sync>>,
    harness: Option<Arc<dyn Harness>>,
    agent_harnesses: HashMap<&'static str, Arc<dyn Harness>>,
}

impl Providers {
    pub fn new() -> Self {
        Self::default()
    }

    /// Provide an implementation, usually of a trait: `.provide::<dyn Registry>(Arc::new(r))`.
    pub fn provide<T: ?Sized + Send + Sync + 'static>(mut self, value: Arc<T>) -> Self {
        self.items.insert(TypeId::of::<T>(), Box::new(value));
        self
    }

    /// The harness every agent uses unless it has its own.
    pub fn harness(mut self, harness: impl Harness) -> Self {
        self.harness = Some(Arc::new(harness));
        self
    }

    pub fn harness_for<P, T>(mut self, agent: &AgentSpec<P, T>, harness: impl Harness) -> Self {
        self.agent_harnesses.insert(agent.name(), Arc::new(harness));
        self
    }

    pub fn classifier(self, classifier: impl Classifier) -> Self {
        let classifier: Arc<dyn Classifier> = Arc::new(classifier);
        self.provide(classifier)
    }

    pub(crate) fn get<T: ?Sized + Send + Sync + 'static>(&self) -> Option<Arc<T>> {
        self.items.get(&TypeId::of::<T>())?.downcast_ref::<Arc<T>>().cloned()
    }

    pub(crate) fn has(&self, requirement: &Requirement) -> bool {
        self.items.contains_key(&requirement.id)
    }

    pub(crate) fn harness_of(&self, agent: &str) -> Option<Arc<dyn Harness>> {
        self.agent_harnesses.get(agent).or(self.harness.as_ref()).cloned()
    }
}
