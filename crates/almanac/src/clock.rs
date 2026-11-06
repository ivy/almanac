//! The time, as something a test can control.
use std::sync::{Arc, Mutex};
use std::time::Duration;
use time::OffsetDateTime;

pub trait Clock: Send + Sync {
    fn now(&self) -> OffsetDateTime;
}

pub struct SystemClock;

impl Clock for SystemClock {
    fn now(&self) -> OffsetDateTime {
        OffsetDateTime::now_utc()
    }
}

/// A clock that moves only when told. Clones share one time.
#[derive(Clone)]
pub struct FakeClock(Arc<Mutex<OffsetDateTime>>);

impl FakeClock {
    pub fn at(now: OffsetDateTime) -> Self {
        Self(Arc::new(Mutex::new(now)))
    }

    pub fn advance(&self, by: Duration) {
        *self.0.lock().unwrap() += by;
    }
}

impl Clock for FakeClock {
    fn now(&self) -> OffsetDateTime {
        *self.0.lock().unwrap()
    }
}
