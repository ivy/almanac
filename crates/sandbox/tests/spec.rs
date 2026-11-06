use sandbox::{Egress, FakeSandbox, Sandbox, SandboxSpec};
use std::time::Duration;

#[tokio::test]
async fn the_fake_refuses_what_a_real_provider_refuses() {
    let mut spec = SandboxSpec::agent("image");
    spec.egress = Egress::Domains(vec![]);
    assert!(FakeSandbox::default().create(&spec).await.is_err());
}

#[tokio::test]
async fn a_fetch_sandbox_may_have_egress() {
    let spec = SandboxSpec::fetch("image", vec!["crates.io".to_string()]);
    assert!(FakeSandbox::default().create(&spec).await.is_ok());
}

#[tokio::test]
async fn the_fake_tracks_its_lifetime() {
    let fake = FakeSandbox::default();
    let id = fake.create(&SandboxSpec::agent("image")).await.unwrap();
    assert_eq!(fake.alive(), 1);
    fake.destroy(&id).await.unwrap();
    assert_eq!(fake.alive(), 0);
    assert!(
        fake.exec(&id, "true", Duration::from_secs(1))
            .await
            .is_err()
    );
}
