//! Exists so `cargo run -- synth` prints a Lua workflow's manifest through Almanac's own verb. It
//! sets up no providers, so `run` and `backfill` fail `check` and nothing here can go live.
use almanac::config::Config;
use almanac::prelude::*;
use std::process::ExitCode;
use workflows_in_lua::renovate::build_failures;

fn workflows() -> Vec<Box<dyn Registered>> {
    match build_failures() {
        Ok(workflow) => vec![Box::new(workflow)],
        Err(error) => {
            eprintln!("{error}");
            std::process::exit(1);
        }
    }
}

fn no_providers(_: &Config) -> Providers {
    Providers::new()
}

fn main() -> ExitCode {
    almanac::main("almanac-lua", workflows, no_providers)
}
