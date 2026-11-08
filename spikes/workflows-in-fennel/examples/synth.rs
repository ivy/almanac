//! Prints what `synth` would for the ported workflow, and the Rust original beside it. Neither
//! runs a workflow: `cargo run --example synth`.
use almanac::prelude::*;
use almanac_renovate::workflows::build_failures::BuildFailures;
use workflows_in_fennel::build_failures;

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let fennel = build_failures()?.manifest();
    let rust = BuildFailures.manifest();
    println!("fennel: {}", serde_json::to_string_pretty(&fennel)?);
    println!("rust: {}", serde_json::to_string_pretty(&rust)?);
    Ok(())
}
