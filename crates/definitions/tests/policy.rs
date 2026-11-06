use EffectKind as K;
use almanac::prelude::*;
use definitions::dependency_updates::{CiState::*, *};

fn effects(checks_pass: bool, verdict: Verdict, ci: CiState) -> Vec<EffectKind> {
    let evidence = UpdateEvidence {
        repo: "ivy/thicket".parse().unwrap(),
        sha: "0".repeat(40).parse().unwrap(),
        package: "lodash".parse().unwrap(),
        install_script_added: !checks_pass,
        publisher_changed: false,
        provenance_verified: true,
        ci,
        diff: Untrusted::new(String::new()),
    };
    let review = Untrusted::new(Review {
        verdict,
        notes: String::new(),
    });
    let facts = Facts { evidence, review };
    policy(&facts).iter().map(Effect::kind).collect()
}

#[test]
fn the_policy_table() {
    assert_eq!(effects(true, Verdict::NoConcerns, Green), [K::Attest]);
    assert_eq!(effects(true, Verdict::NoConcerns, Pending), [K::Await]);
    assert_eq!(
        effects(true, Verdict::NoConcerns, Red),
        [K::Attest, K::File]
    );
    assert_eq!(effects(true, Verdict::Concerns, Green), [K::Attest, K::Ask]);
    assert_eq!(
        effects(false, Verdict::NoConcerns, Green),
        [K::Attest, K::Ask]
    );
    assert_eq!(effects(false, Verdict::Concerns, Red), [K::Attest, K::Ask]);
}
