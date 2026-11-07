use almanac::prelude::*;

pub use github::PullRequestOpened;

#[prompt("reviewer.md")]
pub struct ReviewerPrompt {
    pub repo: Slug,
}

pub const REVIEWER: AgentSpec<Quarantined, ReviewerPrompt> = AgentSpec::quarantined("reviewer");

clean! {
    pub enum CiState { Pending, Green, Red }
}

clean! {
    pub enum Verdict { NoConcerns, Concerns }
}

observed! {
    /// What the registry and forge told us. Facts are typed; the diff stays tainted.
    pub struct UpdateEvidence {
        pub repo: Slug,
        pub sha: Sha,
        pub package: Slug,
        pub install_script_added: bool,
        pub publisher_changed: bool,
        pub provenance_verified: bool,
        pub ci: CiState,
        pub diff: Untrusted<String>,
    }
}

output! {
    /// The reviewer's answer. Only `verdict` can leave taint, because it is an enum.
    pub struct Review {
        pub verdict: Verdict,
        pub notes: String,
    }
}

pub struct Facts {
    pub evidence: UpdateEvidence,
    pub review: Untrusted<Review>,
}

/// Fetches both artifacts from the package registry and compares them.
pub trait Registry: Send + Sync + 'static {
    fn evidence<'a>(
        &'a self,
        pr: &'a PullRequestOpened,
    ) -> BoxFuture<'a, Result<UpdateEvidence, StepError>>;
}

#[workflow(
    on = PullRequestOpened,
    key = "{repo}:{head_branch}",
    intent = "Report whether a dependency update is safe, and request a fix if its build broke.",
    may = [Attest, Await, File, Ask],
    runs_per_hour = 10,
    subjects = [repo(repo)],
    links = [package(evidence.repo, evidence.package)],
    decide = policy,
)]
pub async fn dependency_updates(cx: &Cx, pr: &PullRequestOpened) -> Result<Facts, Halt> {
    let registry = cx.get::<dyn Registry>()?;
    let evidence = cx.observe("registry", registry.evidence(pr)).await?;
    let prompt = ReviewerPrompt {
        repo: pr.repo.clone(),
    };
    let review = cx.agent(REVIEWER).read(prompt, &evidence).await?;
    Ok(Facts { evidence, review })
}

/// The model can only veto: it cannot pass an artifact that failed the deterministic checks.
pub fn policy(Facts { evidence, review }: &Facts) -> Vec<Effect> {
    let checks_pass = evidence.provenance_verified
        && !evidence.install_script_added
        && !evidence.publisher_changed;
    let no_concerns = review.narrow(|r| r.verdict) == Verdict::NoConcerns;
    let body = review.map(|r| r.notes.clone());

    let attest = |state| Effect::Attest {
        context: "almanac/trust",
        state,
        repo: evidence.repo.clone(),
        sha: evidence.sha.clone(),
    };

    if !checks_pass || !no_concerns {
        return vec![
            attest(AttestState::Failure),
            Effect::Ask {
                question: title!("This dependency update failed trust checks"),
                target: evidence.repo.clone(),
                pull: None,
                body,
            },
        ];
    }
    match evidence.ci {
        CiState::Pending => vec![Effect::Await {
            until: "CI to finish",
        }],
        CiState::Green => vec![attest(AttestState::Success)],
        CiState::Red => vec![
            attest(AttestState::Success),
            Effect::File {
                title: title!("Fix the build on this dependency update"),
                target: evidence.repo.clone(),
                body,
            },
        ],
    }
}
