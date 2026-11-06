use renovate_triage::{providers, workflows};

fn main() -> std::process::ExitCode {
    almanac::main("renovate-triage", workflows::all, providers::all)
}
