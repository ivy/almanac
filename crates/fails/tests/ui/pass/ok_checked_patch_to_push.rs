use fails::*;

fn push(patch: &Untrusted<Patch>, base: &Sha) -> Result<Effect, Rejection> {
    Ok(Effect::Push {
        repo: "a/b".parse().unwrap(),
        pull: 1,
        branch: "renovate/x".parse().unwrap(),
        message: "fix",
        hold: None,
        patch: DEFAULT_POLICY.check(patch, base)?,
    })
}

fn main() { let _ = push; }
