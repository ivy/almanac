//! What an almanac's `main()` calls. One binary, a few verbs.
use crate::config::Config;
use crate::consumer::Consumer;
use crate::daemon::{GRACE, Health, serve_health, with_backoff};
use crate::diag::{Error, Format, explain, render};
use crate::effect::Mode;
use crate::providers::Providers;
use crate::runtime::{Envelope, Ingest, Origin, Outcome, Runtime};
use crate::store::PostgresStore;
use crate::workflow::Registered;
use futures::future::BoxFuture;
use serde_json::json;
use std::io::IsTerminal;
use std::process::ExitCode;
use std::sync::Arc;
use tokio::signal::unix::{SignalKind, signal};

const USAGE: &str = "usage: <almanac> [--format json] synth | check | run | \
                     backfill [--repo R] [--dry-run] | simulate KIND FILE [--dry-run] | explain CODE";

/// Where a daemon answers health checks, unless `listen` says otherwise.
const HEALTH_LISTEN: &str = "127.0.0.1:8788";

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

/// What a verb has to say, for people and for agents.
pub struct Said {
    pub human: String,
    pub json: serde_json::Value,
}

type VerbFn = for<'a> fn(&'a [String], &'a Config) -> BoxFuture<'a, Result<Said, Error>>;

/// A verb an almanac adds of its own, given the arguments after its name.
pub struct Verb {
    pub name: &'static str,
    pub run: VerbFn,
}

pub fn main(name: &'static str, workflows: Workflows, setup: Setup) -> ExitCode {
    main_with(name, workflows, setup, &[])
}

pub fn main_with(
    name: &'static str,
    workflows: Workflows,
    setup: Setup,
    verbs: &'static [Verb],
) -> ExitCode {
    let mut args: Vec<String> = std::env::args().skip(1).collect();
    let format = format_of(&mut args);
    if args.iter().any(|a| a == "--help" || a == "-h") {
        println!("{USAGE}");
        return ExitCode::SUCCESS;
    }
    match run(name, args, format, workflows, setup, verbs) {
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
/// Every outcome as a line, and how many events a budget is holding.
async fn settle(runtime: &Runtime) -> Result<(Vec<String>, usize), Error> {
    let outcomes = runtime.settle().await?;
    let mut shown = vec![];
    let mut held = 0;
    for outcome in &outcomes {
        if let Outcome::BudgetTripped { held: waiting, .. } = outcome {
            held += waiting;
        }
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
    Ok((shown, held))
}

/// Finishes when the service manager asks this process to stop.
async fn terminated() {
    let Ok(mut term) = signal(SignalKind::terminate()) else {
        return;
    };
    tokio::select! {
        _ = term.recv() => {}
        _ = tokio::signal::ctrl_c() => {}
    }
}

fn start_logging() {
    let json = !std::io::stderr().is_terminal();
    let logs = tracing_subscriber::fmt().with_writer(std::io::stderr);
    let installed = if json {
        logs.json().try_init()
    } else {
        logs.try_init()
    };
    drop(installed);
}

fn tell_manager(state: &[sd_notify::NotifyState<'_>]) {
    if let Err(error) = sd_notify::notify(state) {
        tracing::warn!(%error, "could not notify the service manager");
    }
}

/// `run`: stays up, consuming the broker's log. An outage is waited out, so a supervisor never
/// sees a crash loop for a database that is restarting.
async fn daemon(name: &'static str, runtime: Runtime, config: &Config) -> Result<(), Error> {
    start_logging();
    let url = std::env::var("DATABASE_URL").map_err(|_| Error::EnvMissing {
        name: "DATABASE_URL".to_string(),
        purpose: "the PostgreSQL database shared with the broker".to_string(),
    })?;
    let scope = runtime.mode().scope(name);
    let health = Health::default();
    let address = config.listen.as_deref().unwrap_or(HEALTH_LISTEN);
    if let Ok(address) = address.parse() {
        let health = health.clone();
        tokio::spawn(async move {
            if let Err(error) = serve_health(address, health).await {
                tracing::warn!(%error, "no health endpoint");
            }
        });
    }
    let (stopping, stopped) = tokio::sync::watch::channel(false);
    let stop = async move {
        terminated().await;
        let _ = stopping.send(true);
    };
    tokio::pin!(stop);
    let connecting = async {
        let store = with_backoff("connecting to the store", stopped.clone(), || {
            PostgresStore::connect(&url, &scope)
        })
        .await?;
        let consumer = with_backoff("connecting to the log", stopped.clone(), || {
            Consumer::connect(&url, &scope)
        })
        .await?;
        Some((store, consumer))
    };
    let connected = tokio::select! {
        connected = connecting => connected,
        () = &mut stop => None,
    };
    let Some((store, consumer)) = connected else {
        return Ok(());
    };
    let runtime = runtime.with_store(Arc::new(store));
    let requeued = with_backoff("recovering", stopped.clone(), || runtime.recover()).await;
    tracing::info!(almanac = name, ?requeued, "running");
    tell_manager(&[sd_notify::NotifyState::Ready]);
    let result = crate::daemon::run(&runtime, &consumer, &health, GRACE, stop).await;
    tell_manager(&[sd_notify::NotifyState::Stopping]);
    result
}

fn print(format: Format, human: String, json: serde_json::Value) {
    match format {
        Format::Human => println!("{human}"),
        Format::Json => println!("{json}"),
    }
}

/// Why a simulated event started nothing, for the outcomes a mistake usually causes.
fn why_nothing_ran(ingest: &Ingest, kind: &str) -> Option<String> {
    match ingest {
        Ingest::Unrouted => Some(format!(
            "no workflow is triggered by `{kind}`; `synth` lists each workflow's trigger"
        )),
        Ingest::Rejected => Some(format!(
            "the file does not parse as `{kind}`; check it has every field the event declares"
        )),
        Ingest::Ignored => Some("the workflow's `when` filter turned this event away".to_string()),
        _ => None,
    }
}

fn run(
    name: &'static str,
    mut args: Vec<String>,
    format: Format,
    workflows: Workflows,
    setup: Setup,
    verbs: &'static [Verb],
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
    if let Some(verb) = verbs.iter().find(|v| Some(v.name) == verb.first().copied()) {
        let said = rt.block_on((verb.run)(&args[1..], &config))?;
        print(format, said.human, said.json);
        return Ok(());
    }
    let mut runtime = Runtime::new(workflows(), setup(&config))
        .tightened(config.limits())
        .in_mode(mode(dry_run));
    // A manifest describes the binary, so it needs nothing set up.
    if verb.as_slice() == ["synth"] {
        let manifest = json!(runtime.manifest());
        let pretty = serde_json::to_string_pretty(&manifest).unwrap_or_default();
        print(format, pretty, manifest);
        return Ok(());
    }
    // Before anything connects, so a binary missing a provider fails at once with a diagnostic.
    runtime.validate()?;
    if verb.as_slice() == ["run"] {
        return rt.block_on(daemon(name, runtime, &config));
    }
    if let Ok(url) = std::env::var("DATABASE_URL") {
        let scope = runtime.mode().scope(name);
        let store = rt.block_on(PostgresStore::connect(&url, &scope))?;
        runtime = runtime.with_store(Arc::new(store));
    }

    match verb.as_slice() {
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
            let (report, (shown, held)) = rt.block_on(async {
                let report = runtime.backfill(repo.as_ref()).await?;
                Ok::<_, Error>((report, settle(&runtime).await?))
            })?;
            let mut human = format!("backfill: {report:?}\n{}", shown.join("\n"));
            if held > 0 {
                human.push_str(&format!(
                    "\n{held} event(s) held by a run budget, resuming as the hourly window opens"
                ));
            }
            print(
                format,
                human,
                json!({"backfill": report, "held": held, "outcomes": shown}),
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
            let (ingest, (shown, _)) = rt.block_on(async {
                let ingest = runtime.ingest(envelope).await?;
                Ok::<_, Error>((ingest, settle(&runtime).await?))
            })?;
            let why = why_nothing_ran(&ingest, kind);
            let mut human = format!("simulate: {ingest:?}\n{}", shown.join("\n"));
            if let Some(why) = &why {
                human = format!("simulate: {ingest:?}: {why}");
            }
            print(
                format,
                human,
                json!({"simulate": format!("{ingest:?}"), "why": why, "outcomes": shown}),
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
