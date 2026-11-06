//! What an almanac's `main()` calls. One binary, a few verbs.
use crate::config::Config;
use crate::diag::{Error, Format, explain, render};
use crate::effect::Mode;
use crate::providers::Providers;
use crate::runtime::{Envelope, Origin, Outcome, Runtime};
use crate::store::PostgresStore;
use crate::workflow::Registered;
use serde_json::json;
use std::process::ExitCode;
use std::sync::Arc;

const USAGE: &str = "usage: <almanac> [--format json] synth | check | \
                     backfill [--repo R] [--dry-run] | simulate KIND FILE [--dry-run] | explain CODE";

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

/// Takes `flag` out of the arguments, reporting whether it was there.
fn flag(args: &mut Vec<String>, flag: &str) -> bool {
    let found = args.iter().position(|a| a == flag);
    found.map(|at| args.remove(at)).is_some()
}

/// Takes `flag VALUE` out of the arguments.
fn option(args: &mut Vec<String>, flag: &str) -> Option<String> {
    let at = args.iter().position(|a| a == flag)?;
    let value = args.get(at + 1)?.clone();
    args.drain(at..at + 2);
    Some(value)
}

/// Going live is deliberate: it is set in the environment for the run, and has no config key,
/// because config only tightens. `--dry-run` always wins.
fn mode(dry_run: bool) -> Mode {
    match (dry_run, std::env::var("ALMANAC_MODE").as_deref()) {
        (false, Ok("live")) => Mode::Live,
        _ => Mode::DryRun,
    }
}

/// Runs what is queued and applies it, reporting what happened.
async fn settle(runtime: &Runtime) -> Result<Vec<String>, Error> {
    let outcomes = runtime.drain().await?;
    runtime.deliver().await?;
    let mut shown = vec![];
    for outcome in &outcomes {
        let line = match outcome {
            Outcome::Ran { run_id, .. } => {
                let effects = runtime.effects_of(run_id).await?;
                let statuses: Vec<_> = effects.iter().map(|e| e.status).collect();
                format!("{outcome:?} recorded as {statuses:?}")
            }
            other => format!("{other:?}"),
        };
        shown.push(line);
    }
    Ok(shown)
}

fn print(format: Format, human: String, json: serde_json::Value) {
    match format {
        Format::Human => println!("{human}"),
        Format::Json => println!("{json}"),
    }
}

fn run(
    name: &'static str,
    mut args: Vec<String>,
    format: Format,
    workflows: Workflows,
    setup: Setup,
) -> Result<(), Error> {
    let dry_run = flag(&mut args, "--dry-run");
    let repo = option(&mut args, "--repo");
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
    let mut runtime = Runtime::new(workflows(), setup(&config))
        .tightened(config.limits())
        .in_mode(mode(dry_run));
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
        ["backfill"] => {
            let repo = match repo {
                Some(text) => Some(text.parse().map_err(|_| Error::Usage {
                    given: text,
                    usage: USAGE.to_string(),
                })?),
                None => None,
            };
            let (report, shown) = rt.block_on(async {
                runtime.recover().await?;
                let report = runtime.backfill(repo.as_ref()).await?;
                Ok::<_, Error>((report, settle(&runtime).await?))
            })?;
            print(
                format,
                format!("backfill: {report:?}\n{}", shown.join("\n")),
                json!({"backfill": report, "outcomes": shown}),
            );
        }
        ["simulate", kind, file] => {
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
                source: "simulate".into(),
                delivery_id: format!("{file}:{}", text.len()),
                kind: (*kind).to_string(),
                depth: 0,
                origin: Origin::Simulated,
                payload,
            };
            let (ingest, shown) = rt.block_on(async {
                runtime.recover().await?;
                let ingest = runtime.ingest(envelope).await?;
                Ok::<_, Error>((ingest, settle(&runtime).await?))
            })?;
            print(
                format,
                format!("simulate: {ingest:?}\n{}", shown.join("\n")),
                json!({"simulate": format!("{ingest:?}"), "outcomes": shown}),
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
