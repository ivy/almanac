use renovate_triage::{fleet, providers, workflows};

fn main() -> std::process::ExitCode {
    almanac::main_with(
        "renovate-triage",
        workflows::all,
        providers::all,
        fleet::VERBS,
    )
}
