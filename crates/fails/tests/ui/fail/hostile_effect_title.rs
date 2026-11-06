use fails::*;

fn main() { let t = String::from("x"); let _ = Effect::Attest { context: &t, passed: true }; }
