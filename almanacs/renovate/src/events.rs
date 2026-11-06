//! A build finishing. It arrives as a check suite or a commit status, and they mean the same
//! thing here, so both parse to one event.
use almanac::prelude::*;
use serde_json::{Value, json};

/// Set on every commit Almanac makes.
pub const PATCH_TRAILER: &str = "Almanac-Key:";

observed! {
    pub struct BuildChanged {
        pub repo: Slug,
        pub branch: Slug,
        pub sha: Sha,
        pub failed: bool,
        pub finished: bool,
        // The head commit carries an Almanac patch.
        pub patched: bool,
        // A status that is not about the build, so it says nothing here.
        pub bookkeeping: bool,
    }
}

fn is_failed_conclusion(conclusion: &Option<String>) -> bool {
    matches!(
        conclusion.as_deref(),
        Some("failure" | "timed_out" | "action_required" | "startup_failure" | "cancelled")
    )
}

fn is_failed_state(state: &String) -> bool {
    state == "failure" || state == "error"
}

fn is_pending_state(state: &String) -> bool {
    state == "pending"
}

fn carries_patch(message: &Option<String>) -> bool {
    message
        .as_deref()
        .is_some_and(|m| m.contains(PATCH_TRAILER))
}

/// Almanac's own status and Renovate's say nothing about whether the build works.
fn is_bookkeeping(context: &String) -> bool {
    context == "almanac/trust" || context.starts_with("renovate/")
}

impl Event for BuildChanged {
    const KIND: &'static str = "github.check_suite.completed";
    const ALSO: &'static [&'static str] = &["github.status"];

    fn parse(p: &Payload<'_>) -> Result<Self, Malformed> {
        let repo = p.field("/repository/full_name")?;
        if let Some(sha) = p.field::<Option<Sha>>("/check_suite/head_sha")? {
            let conclusion = p.optional_text("/check_suite/conclusion")?;
            let message = p.optional_text("/check_suite/head_commit/message")?;
            return Ok(Self {
                repo,
                branch: p.field("/check_suite/head_branch")?,
                sha,
                failed: conclusion.narrow(is_failed_conclusion),
                finished: true,
                patched: message.narrow(carries_patch),
                bookkeeping: false,
            });
        }
        let state = p.text("/state")?;
        let context = p.text("/context")?;
        let message = p.optional_text("/commit/commit/message")?;
        Ok(Self {
            repo,
            branch: p.field("/branches/0/name")?,
            sha: p.field("/sha")?,
            failed: state.narrow(is_failed_state),
            finished: !state.narrow(is_pending_state),
            patched: message.narrow(carries_patch),
            bookkeeping: context.narrow(is_bookkeeping),
        })
    }
}

/// A check suite payload, for a scan: a scanned event takes the same path as a webhook.
pub fn suite_payload(repo: &Slug, branch: &Slug, sha: &Sha, failed: bool, patched: bool) -> Value {
    let message = if patched { PATCH_TRAILER } else { "" };
    let conclusion = if failed { "failure" } else { "success" };
    json!({
        "repository": {"full_name": repo},
        "check_suite": {
            "head_sha": sha,
            "head_branch": branch,
            "conclusion": conclusion,
            "head_commit": {"message": message},
        },
    })
}
