use almanac_renovate::{fleet, providers, workflows};

fn main() -> std::process::ExitCode {
    almanac::main_with(
        "almanac-renovate",
        workflows::all,
        providers::all,
        fleet::VERBS,
    )
}
