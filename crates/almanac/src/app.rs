//! What an almanac's `main()` calls. One binary, a few verbs.
use crate::config::Config;
use crate::providers::Providers;
use crate::runtime::{Envelope, Runtime};
use crate::workflow::Registered;
use std::process::ExitCode;

const USAGE: &str = "usage: <almanac> synth | check | ingest KIND FILE | serve";

type Workflows = fn() -> Vec<Box<dyn Registered>>;
type Setup = fn(&Config) -> Providers;

pub fn main(name: &'static str, workflows: Workflows, setup: Setup) -> ExitCode {
    match run(name, workflows, setup) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("{name}: {e}");
            ExitCode::FAILURE
        }
    }
}

fn run(name: &'static str, workflows: Workflows, setup: Setup) -> Result<(), String> {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let config = Config::load()?;
    let runtime = Runtime::new(workflows(), setup(&config)).tightened(config.limits());
    runtime.validate()?;

    match args.iter().map(String::as_str).collect::<Vec<_>>().as_slice() {
        ["synth"] => {
            let manifest = serde_json::to_string_pretty(&runtime.manifest()).map_err(|e| e.to_string())?;
            println!("{manifest}");
        }
        ["check"] => {
            println!("{name}: ok, {} workflow(s), {:?}", runtime.manifest().len(), runtime.limits());
        }
        ["ingest", kind, file] => {
            let text = std::fs::read_to_string(file).map_err(|e| format!("{file}: {e}"))?;
            let payload = serde_json::from_str(&text).map_err(|e| format!("{file}: {e}"))?;
            let envelope = Envelope {
                source: "cli".into(),
                delivery_id: (*file).to_string(),
                kind: (*kind).to_string(),
                depth: 0,
                payload,
            };
            println!("ingest: {:?}", runtime.ingest(envelope));
            let rt = tokio::runtime::Builder::new_current_thread().build().map_err(|e| e.to_string())?;
            for outcome in rt.block_on(runtime.drain()) {
                println!("{outcome:?}");
            }
        }
        ["serve"] => return Err("serve is not implemented in this sketch".to_string()),
        _ => return Err(USAGE.to_string()),
    }
    Ok(())
}
