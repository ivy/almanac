//! What an almanac's `main()` calls. One binary, a few verbs.
use crate::config::Config;
use crate::diag::{Error, Format, explain, render};
use crate::providers::Providers;
use crate::runtime::{Envelope, Origin, Runtime};
use crate::store::PostgresStore;
use crate::workflow::Registered;
use serde_json::json;
use std::process::ExitCode;
use std::sync::Arc;

const USAGE: &str =
    "usage: <almanac> [--format json] synth | check | ingest KIND FILE | explain CODE";

type Workflows = fn() -> Vec<Box<dyn Registered>>;
type Setup = fn(&Config) -> Providers;

/// Takes `--format` out of the arguments. An unknown format falls through as a usage error.
fn format_of(args: &mut Vec<String>) -> Format {
    let Some(at) = args.iter().position(|a| a == "--format") else {
        return Format::Human;
    };
    if args.get(at + 1).map(String::as_str) != Some("json") {
        return Format::Human;
    }
    args.drain(at..at + 2);
    Format::Json
}

pub fn main(name: &'static str, workflows: Workflows, setup: Setup) -> ExitCode {
    let mut args: Vec<String> = std::env::args().skip(1).collect();
    let format = format_of(&mut args);
    match run(name, args, format, workflows, setup) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("{}", render(&e, format));
            ExitCode::FAILURE
        }
    }
}

fn print(format: Format, human: String, json: serde_json::Value) {
    match format {
        Format::Human => println!("{human}"),
        Format::Json => println!("{json}"),
    }
}

fn run(
    name: &'static str,
    args: Vec<String>,
    format: Format,
    workflows: Workflows,
    setup: Setup,
) -> Result<(), Error> {
    let verb: Vec<&str> = args.iter().map(String::as_str).collect();
    if let ["explain", code] = verb.as_slice() {
        let page = explain(code)?;
        print(
            format,
            page.to_string(),
            json!({"code": code, "page": page}),
        );
        return Ok(());
    }
    let config = Config::load()?;
    let rt = tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
        .map_err(|source| Error::Startup { source })?;
    let mut runtime = Runtime::new(workflows(), setup(&config)).tightened(config.limits());
    if let Ok(url) = std::env::var("DATABASE_URL") {
        let store = rt.block_on(PostgresStore::connect(&url, name))?;
        runtime = runtime.with_store(Arc::new(store));
    }
    runtime.validate()?;

    match verb.as_slice() {
        ["synth"] => {
            let manifest = json!(runtime.manifest());
            let pretty = serde_json::to_string_pretty(&manifest).unwrap_or_default();
            print(format, pretty, manifest);
        }
        ["check"] => {
            let workflows = runtime.manifest().len();
            let limits = runtime.limits();
            print(
                format,
                format!("{name}: ok, {workflows} workflow(s), {limits:?}"),
                json!({"almanac": name, "ok": true, "workflows": workflows}),
            );
        }
        ["ingest", kind, file] => {
            let text = std::fs::read_to_string(file).map_err(|source| Error::InputUnreadable {
                path: (*file).to_string(),
                source,
            })?;
            let payload =
                serde_json::from_str(&text).map_err(|source| Error::InputInvalidJson {
                    path: (*file).to_string(),
                    source,
                })?;
            let envelope = Envelope {
                source: "cli".into(),
                delivery_id: (*file).to_string(),
                kind: (*kind).to_string(),
                depth: 0,
                origin: Origin::Simulated,
                payload,
            };
            let (ingest, outcomes) = rt.block_on(async {
                runtime.recover().await?;
                let ingest = runtime.ingest(envelope).await?;
                let outcomes = runtime.drain().await?;
                runtime.deliver().await?;
                Ok::<_, crate::store::StoreError>((ingest, outcomes))
            })?;
            let shown: Vec<String> = outcomes.iter().map(|o| format!("{o:?}")).collect();
            print(
                format,
                format!("ingest: {ingest:?}\n{}", shown.join("\n")),
                json!({"ingest": format!("{ingest:?}"), "outcomes": shown}),
            );
        }
        _ => {
            return Err(Error::Usage {
                given: args.join(" "),
                usage: USAGE.to_string(),
            });
        }
    }
    Ok(())
}
