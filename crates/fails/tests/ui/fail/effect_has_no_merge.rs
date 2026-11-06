use fails::*;

fn main() { let _ = Effect::Merge { repo: "a/b".parse::<Slug>().unwrap() }; }
