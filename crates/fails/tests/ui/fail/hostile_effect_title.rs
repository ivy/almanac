use fails::*;

fn main() {
    let t = String::from("x");
    let _ = Effect::Attest {
        context: &t,
        state: AttestState::Success,
        repo: "a/b".parse().unwrap(),
        sha: "0".repeat(40).parse().unwrap(),
    };
}
