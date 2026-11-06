//! A source with no published webhook schema: the event is written by hand.
use almanac::prelude::*;

#[prompt("investigator.md")]
pub struct InvestigatorPrompt;

pub const INVESTIGATOR: AgentSpec<Quarantined, InvestigatorPrompt> =
    AgentSpec::quarantined("investigator");

observed! {
    pub struct IssueEvent {
        pub issue_id: u64,
        pub repo: Slug,
        pub message: Untrusted<String>,
    }
}

impl Event for IssueEvent {
    const KIND: &'static str = "sentry.issue";
    fn parse(p: &Payload<'_>) -> Result<Self, String> {
        Ok(Self {
            issue_id: p.field("/issue_id")?,
            repo: p.field("/repo")?,
            message: p.text("/message")?,
        })
    }
}

clean! {
    pub enum Confidence { Low, High }
}

output! {
    pub struct Findings {
        pub confidence: Confidence,
        pub summary: String,
    }
}

pub struct Facts {
    pub repo: Slug,
    pub findings: Untrusted<Findings>,
}

#[workflow(
    on = IssueEvent,
    key = "{issue_id}",
    intent = "File a bug for application errors worth fixing.",
    may = [File, Ask],
    runs_per_hour = 10,
    subjects = [repo(repo)],
    decide = triage,
)]
pub async fn error_triage(cx: &Cx, event: &IssueEvent) -> Result<Facts, Halt> {
    cx.screen(
        "Is this error worth an engineer's attention?",
        &event.message,
        0.7,
    )
    .await?;
    let findings = cx
        .agent(INVESTIGATOR)
        .read(InvestigatorPrompt, &event.message)
        .await?;
    Ok(Facts {
        repo: event.repo.clone(),
        findings,
    })
}

pub fn triage(Facts { repo, findings }: &Facts) -> Vec<Effect> {
    let body = findings.map(|f| f.summary.clone());
    match findings.narrow(|f| f.confidence) {
        Confidence::High => vec![Effect::File {
            title: "Fix this application error",
            target: repo.clone(),
            body,
        }],
        Confidence::Low => vec![Effect::Ask {
            question: "Is this error worth fixing?",
            body,
        }],
    }
}
