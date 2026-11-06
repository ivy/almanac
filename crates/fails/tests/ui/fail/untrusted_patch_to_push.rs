use fails::*;

fn push(patch: Untrusted<Patch>) -> Effect {
    Effect::Push {
        repo: "a/b".parse().unwrap(),
        pull: 1,
        branch: "renovate/x".parse().unwrap(),
        message: "fix",
        hold: None,
        patch,
    }
}

fn main() { let _ = push; }
