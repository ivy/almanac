//! A workflow written in Fennel, as the runtime sees it. `Registered` is the erased form the
//! runtime holds, so routing, budgets, the outbox, and replay are Almanac's own, unchanged.
use crate::compile::{LoadError, compile};
use crate::gather;
use crate::host::Host;
use crate::manifest::{Declaration, Piece, SubjectDecl, declare};
use crate::pure;
use crate::value::Script;
use almanac::cx::{Cx, Halt, StepError};
use almanac::effect::EffectKind;
use almanac::prelude::*;
use almanac::providers::Requirement;
use almanac::workflow::{Decided, Found, Identity, Registered, Scanner, Subject};
use futures::future::BoxFuture;
use serde_json::Value;

pub struct FennelWorkflow {
    declaration: Declaration,
    lua: String,
}

impl FennelWorkflow {
    /// Reads, checks, and compiles a workflow. Nothing in `source` runs: the first time any of it
    /// runs is the first event.
    pub fn load(host: &Host, filename: &str, source: &str) -> Result<Self, LoadError> {
        let compiled = compile(filename, source)?;
        Ok(Self {
            declaration: declare(host, &compiled.analysis)?,
            lua: compiled.lua,
        })
    }

    pub fn declaration(&self) -> &Declaration {
        &self.declaration
    }

    pub fn lua(&self) -> &str {
        &self.lua
    }

    fn event(&self, payload: &Value) -> Result<Script, Malformed> {
        let parsed = (self.declaration.event.parse)(payload)?;
        Script::from_record(&parsed).map_err(|_| Malformed {
            pointer: "/".to_string(),
        })
    }

    fn key(&self, event: &Script) -> String {
        let mut key = String::new();
        for piece in &self.declaration.key {
            match piece {
                Piece::Text(text) => key.push_str(text),
                Piece::Var(field) => match event.get(field) {
                    Some(Script::Text(text)) => key.push_str(text),
                    Some(Script::Int(n)) => key.push_str(&n.to_string()),
                    Some(Script::Bool(b)) => key.push_str(&b.to_string()),
                    _ => {}
                },
            }
        }
        key
    }

    fn slug(event: &Script, field: &str) -> Result<Slug, Malformed> {
        let malformed = || Malformed {
            pointer: format!("/{field}"),
        };
        match event.get(field) {
            Some(Script::Text(text)) => text.parse().map_err(|_| malformed()),
            _ => Err(malformed()),
        }
    }

    fn subjects(&self, event: &Script) -> Result<Vec<Subject>, Malformed> {
        let subject = |declared: &SubjectDecl| match declared {
            SubjectDecl::Repo(repo) => Ok(Subject::repo(&Self::slug(event, repo)?)),
            SubjectDecl::Package(repo, package) => Ok(Subject::package(
                &Self::slug(event, repo)?,
                &Self::slug(event, package)?,
            )),
        };
        self.declaration.subjects.iter().map(subject).collect()
    }
}

impl Registered for FennelWorkflow {
    fn name(&self) -> &'static str {
        self.declaration.name
    }

    fn intent(&self) -> &'static str {
        self.declaration.intent
    }

    fn kind(&self) -> &'static str {
        self.declaration.event.kind
    }

    fn handles(&self, kind: &str) -> bool {
        let event = &self.declaration.event;
        kind == event.kind || event.also.contains(&kind)
    }

    fn runs_per_hour(&self) -> u32 {
        self.declaration.runs_per_hour
    }

    fn may(&self) -> &'static [EffectKind] {
        self.declaration.may
    }

    fn agent_names(&self) -> Vec<&'static str> {
        self.declaration.agents.values().map(|a| a.name).collect()
    }

    fn requirements(&self) -> Vec<Requirement> {
        self.declaration.requirements()
    }

    /// A `when` that fails rejects the event: nothing can be said about whether it is wanted, and
    /// the error names the workflow and repeats nothing from the payload.
    fn identify(&self, payload: &Value) -> Result<Identity, Malformed> {
        let event = self.event(payload)?;
        let wanted = pure::wanted(self, &event).map_err(|_| Malformed {
            pointer: format!("`:when` of {}", self.name()),
        })?;
        Ok(Identity {
            key: self.key(&event),
            subjects: self.subjects(&event)?,
            wanted,
        })
    }

    fn execute<'a>(
        &'a self,
        cx: &'a Cx,
        payload: &'a Value,
    ) -> BoxFuture<'a, Result<Decided, Halt>> {
        Box::pin(async move {
            let event = self.event(payload).map_err(StepError::from)?;
            let facts = gather::run(self, cx, &event).await?;
            let effects = pure::decide(self, &facts).map_err(|e| StepError(e.to_string()))?;
            Ok(Decided {
                effects,
                links: vec![],
            })
        })
    }

    /// This spike does not port `scan`, so its manifest says `scans: false`.
    fn scan<'a>(&'a self, _: &'a Scanner) -> BoxFuture<'a, Result<Vec<Found>, StepError>> {
        Box::pin(async { Ok(vec![]) })
    }

    fn manifest(&self) -> Value {
        self.declaration.manifest()
    }
}
