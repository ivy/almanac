//! Exhaustive with no wildcard, so adding a variant to `Effect` breaks this case on purpose.
use fails::*;

fn describe(effect: &Effect) -> &'static str {
    match effect {
        Effect::Attest { .. } => "status",
        Effect::Await { .. } => "wait",
        Effect::File { .. } => "work",
        Effect::Ask { .. } => "question",
        Effect::Push { .. } => "commit",
        Effect::Resolve { .. } => "close",
    }
}

fn main() { let _ = describe; }
