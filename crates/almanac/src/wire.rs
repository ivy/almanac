//! The boundary between raw payloads and typed events. Parsing is separate from the
//! record format, so a provider's JSON shape never has to match what the log stores.
use crate::taint::{Clean, Observed, Untrusted};
use serde::de::DeserializeOwned;
use serde_json::Value;

/// A raw payload. Reads produce `Clean` values or tainted text, never a bare `String`.
pub struct Payload<'a>(&'a Value);

impl<'a> Payload<'a> {
    pub(crate) fn new(value: &'a Value) -> Self {
        Self(value)
    }

    fn at<T: DeserializeOwned>(&self, pointer: &str) -> Result<T, String> {
        let found = self.0.pointer(pointer).cloned().unwrap_or(Value::Null);
        serde_json::from_value(found).map_err(|e| format!("{pointer}: {e}"))
    }

    pub fn field<T: Clean + DeserializeOwned>(&self, pointer: &str) -> Result<T, String> {
        self.at(pointer)
    }

    pub fn text(&self, pointer: &str) -> Result<Untrusted<String>, String> {
        self.at(pointer).map(Untrusted::new)
    }

    pub fn optional_text(&self, pointer: &str) -> Result<Untrusted<Option<String>>, String> {
        self.at(pointer).map(Untrusted::new)
    }
}

/// A type a workflow can trigger on. Generated from a spec where one exists, and
/// hand-written otherwise.
pub trait Event: Observed + Send + Sync + 'static {
    const KIND: &'static str;
    fn parse(payload: &Payload<'_>) -> Result<Self, String>
    where
        Self: Sized;
}
