//! The whole almanac: the broker and daemon binaries as processes, PostgreSQL, the Claude CLI, and
//! rootless Podman. Only the model and GitHub are simulated. `make test-e2e` runs this.
use axum::Router;
use axum::extract::Path;
use axum::routing::get;
use claude::stub::{StubModel, Turn, tool};
use flate2::Compression;
use flate2::write::GzEncoder;
use serde_json::{Value, json};
use simforge::{Failure, Fate, Pull, RENOVATE, Repo, SimForge, Suite};
use sqlx::postgres::PgPool;
use std::collections::{BTreeSet, HashMap};
use std::path::PathBuf;
use std::process::Stdio;
use std::time::{Duration, Instant};
use testcontainers_modules::postgres::Postgres;
use testcontainers_modules::testcontainers::ContainerAsync;
use testcontainers_modules::testcontainers::runners::AsyncRunner;
use tokio::process::{Child, Command};

const OWNER: &str = "fleet";
const SECRET: &str = "a webhook secret";
const IMAGE: &str = "localhost/almanac-fixer:test";
const WAIT: Duration = Duration::from_secs(180);

fn free_port() -> u16 {
    let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    listener.local_addr().unwrap().port()
}

fn broker_binary() -> PathBuf {
    let dir = concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../target/debug/almanac-broker"
    );
    let path = PathBuf::from(dir);
    assert!(path.exists(), "run `cargo build --package broker` first");
    path
}

/// A registry that dates a package by its name: `young-` means published just now.
async fn registry() -> String {
    let answer = |Path(name): Path<String>| async move {
        let age = if name.starts_with("young-") {
            time::Duration::hours(1)
        } else {
            time::Duration::days(30)
        };
        let at = time::OffsetDateTime::now_utc() - age;
        let at = at
            .format(&time::format_description::well_known::Rfc3339)
            .unwrap();
        axum::Json(json!({"time": {"2.0.0": at}}))
    };
    let app = Router::new().route("/{name}", get(answer));
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", listener.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(listener, app).await });
    url
}

fn source_tarball() -> Vec<u8> {
    let mut builder = tar::Builder::new(GzEncoder::new(vec![], Compression::fast()));
    let text = "print('broken')\n";
    let mut header = tar::Header::new_gnu();
    header.set_size(text.len() as u64);
    header.set_mode(0o644);
    header.set_cksum();
    builder
        .append_data(&mut header, "repo-abc/src/main.py", text.as_bytes())
        .unwrap();
    builder.into_inner().unwrap().finish().unwrap()
}

/// The Fixer writes one file and finishes. It answers from the request, so sessions may interleave.
fn agent(request: &Value) -> Turn {
    let messages = request["messages"].as_array().cloned().unwrap_or_default();
    let tool_ran = messages
        .iter()
        .flat_map(|m| m["content"].as_array().cloned().unwrap_or_default())
        .any(|block| block["type"] == "tool_result");
    if tool_ran {
        return Turn::Text(r#"{"summary": "repaired"}"#.to_string());
    }
    let input = json!({"path": "src/main.py", "content": "print('fixed')\n"});
    tool("mcp__sandbox__write_file", input)
}

fn body_for(package: &str) -> String {
    format!(
        "| Package | Change |\n|---|---|\n| [{package}](https://x) | [`1.0.0` → `2.0.0`](https://renovatebot.com/diffs/npm/{package}/1.0.0/2.0.0) |\n"
    )
}

fn pull(branch: &str, package: &str) -> Pull {
    let mut pull = Pull::renovate(branch, "Update");
    pull.body = Some(body_for(package));
    pull
}

struct World {
    _database: ContainerAsync<Postgres>,
    url: String,
    pool: PgPool,
    sim: SimForge,
    model: StubModel,
    registry: String,
    broker: Child,
    hook: String,
    health: u16,
}

async fn world(model: StubModel) -> World {
    let database = Postgres::default().start().await.unwrap();
    let port = database.get_host_port_ipv4(5432).await.unwrap();
    let url = format!("postgres://postgres:postgres@127.0.0.1:{port}/postgres");
    let sim = SimForge::start().await;
    let broker_port = free_port();
    let listen = format!("127.0.0.1:{broker_port}");
    let broker = Command::new(broker_binary())
        .env_clear()
        .env("DATABASE_URL", &url)
        .env("ALMANAC_WEBHOOK_SECRET", SECRET)
        .env("ALMANAC_BROKER_LISTEN", &listen)
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .kill_on_drop(true)
        .spawn()
        .unwrap();
    let hook = format!("http://{listen}/webhooks/github");
    let ready = format!("http://{listen}/readyz");
    wait("the broker", || {
        let ready = ready.clone();
        async move { reqwest::get(ready).await.is_ok_and(|r| r.status() == 200) }
    })
    .await;
    sim.deliver_to(&hook, SECRET);
    let pool = PgPool::connect(&url).await.unwrap();
    World {
        _database: database,
        url,
        pool,
        sim,
        model,
        registry: registry().await,
        broker,
        hook,
        health: free_port(),
    }
}

async fn wait<F, Fut>(what: &str, check: F)
where
    F: Fn() -> Fut,
    Fut: Future<Output = bool>,
{
    let started = Instant::now();
    while started.elapsed() < WAIT {
        if check().await {
            return;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    panic!("timed out waiting for {what}");
}

impl World {
    fn command(&self, args: &[&str]) -> Command {
        let mut command = Command::new(env!("CARGO_BIN_EXE_almanac-renovate"));
        let runtime = format!("/run/user/{}", uid());
        command
            .args(args)
            .env_clear()
            .env("PATH", std::env::var("PATH").unwrap())
            .env("HOME", std::env::var("HOME").unwrap())
            .env("XDG_RUNTIME_DIR", runtime)
            .env("DATABASE_URL", &self.url)
            .env("GITHUB_API_URL", self.sim.base_url())
            .env("GITHUB_TOKEN", "t")
            .env("ALMANAC_OWNER", OWNER)
            .env("ALMANAC_RENOVATE_AUTHOR", RENOVATE)
            .env("ALMANAC_REGISTRY_URL", &self.registry)
            .env("ALMANAC_SANDBOX_IMAGE", IMAGE)
            .env("ALMANAC_LISTEN", format!("127.0.0.1:{}", self.health))
            .env("ALMANAC_ALERTS_REPO", format!("{OWNER}/ops"))
            .env("ALMANAC_CONFIG", "/nonexistent/almanac.toml")
            .env("ANTHROPIC_API_KEY", "stub")
            .env("ANTHROPIC_BASE_URL", &self.model.url)
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .kill_on_drop(true);
        command
    }

    async fn daemon(&self) -> Child {
        let mut command = self.command(&["run"]);
        command.env("ALMANAC_MODE", "live");
        let child = command.spawn().unwrap();
        let url = format!("http://127.0.0.1:{}/healthz", self.health);
        wait("the daemon", || {
            let url = url.clone();
            async move { reqwest::get(url).await.is_ok_and(|r| r.status() == 200) }
        })
        .await;
        child
    }

    /// What `report` says now.
    async fn report(&self) -> Value {
        let mut command = self.command(&["--format", "json", "report"]);
        let out = command.stdout(Stdio::piped()).output().await.unwrap();
        assert!(out.status.success());
        serde_json::from_slice(&out.stdout).unwrap()
    }

    async fn logged(&self) -> i64 {
        sqlx::query_scalar("SELECT count(*) FROM deliveries")
            .fetch_one(&self.pool)
            .await
            .unwrap()
    }

    fn pushes(&self) -> usize {
        let writes = self.sim.writes();
        writes.iter().filter(|w| w.method == "PATCH").count()
    }

    fn pushes_to(&self, repo: &str) -> usize {
        let writes = self.sim.writes();
        let matching = writes
            .iter()
            .filter(|w| w.method == "PATCH" && w.path.contains(repo));
        matching.count()
    }

    /// Questions put to a person on a pull request.
    fn questions(&self) -> Vec<String> {
        let writes = self.sim.writes();
        let asked = writes.iter().filter(|w| w.path.ends_with("/comments"));
        asked
            .map(|w| w.body["body"].as_str().unwrap_or_default().to_string())
            .collect()
    }

    fn assigned(&self) -> Vec<Value> {
        let writes = self.sim.writes();
        let assigned = writes.iter().filter(|w| w.path.ends_with("/assignees"));
        assigned.map(|w| w.body["assignees"].clone()).collect()
    }

    /// CI finishing on a pull request's current head.
    async fn ci(&self, repo: &str, number: u64, conclusion: &str, branch: &str) {
        let sha = self.sim.pull(repo, number).head_sha;
        let suite = Suite::finished(&sha, branch, conclusion, "error: tests failed");
        let id = self.sim.add_suite(repo, suite);
        self.sim.suite_completed(repo, id).await;
    }
}

/// The current user, as the owner of its home directory.
fn uid() -> u32 {
    let meta = std::fs::metadata(std::env::var("HOME").unwrap()).unwrap();
    std::os::unix::fs::MetadataExt::uid(&meta)
}

struct Red {
    repo: String,
    number: u64,
    branch: String,
    fixable: bool,
    seen_heads: BTreeSet<String>,
}

/// Runs CI for each failing pull request: a new head gets a result, as a real pipeline would.
async fn run_ci(w: &World, reds: &mut [Red]) {
    for red in reds.iter_mut() {
        let head = w.sim.pull(&red.repo, red.number).head_sha;
        if red.seen_heads.insert(head) {
            let first = red.seen_heads.len() == 1;
            let conclusion = if first || !red.fixable {
                "failure"
            } else {
                "success"
            };
            w.ci(&red.repo, red.number, conclusion, &red.branch).await;
        }
    }
}

fn state_counts(report: &Value) -> HashMap<String, u64> {
    let totals = report["totals"].as_object().cloned().unwrap_or_default();
    totals
        .into_iter()
        .map(|(state, count)| (state, count.as_u64().unwrap_or(0)))
        .collect()
}

fn all_pulls(report: &Value) -> usize {
    let repos = report["repositories"]
        .as_array()
        .cloned()
        .unwrap_or_default();
    repos
        .iter()
        .map(|r| r["pulls"].as_array().map_or(0, Vec::len))
        .sum()
}

/// 68 repositories and 28 pull requests, the shape of the real fleet, with synthetic names.
fn build_fleet(sim: &SimForge) -> Vec<Red> {
    for i in 0..68 {
        sim.add_repo(Repo::public(&format!("{OWNER}/repo-{i:02}")));
    }
    sim.add_repo(Repo::public(&format!("{OWNER}/ops")));
    let tarball = source_tarball();
    let mut reds = vec![];
    for i in 0..28 {
        let repo = format!("{OWNER}/repo-{:02}", i % 6);
        sim.set_tarball(&repo, tarball.clone());
        let branch = format!("renovate/pkg-{i}");
        let number = sim.add_pull(&repo, pull(&branch, &format!("pkg-{i}")));
        let sha = sim.pull(&repo, number).head_sha;
        match i {
            0..=3 => reds.push(Red {
                repo,
                number,
                branch,
                fixable: true,
                seen_heads: BTreeSet::new(),
            }),
            _ => {
                let suite = Suite::finished(&sha, &branch, "success", "");
                sim.add_suite(&repo, suite);
            }
        }
    }
    reds
}

async fn settle(w: &World, reds: &mut [Red], done: impl Fn(&World) -> bool) {
    let started = Instant::now();
    while started.elapsed() < WAIT {
        run_ci(w, reds).await;
        if done(w) {
            return;
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
    panic!("the fleet did not settle");
}

#[tokio::test(flavor = "multi_thread")]
async fn a_fleet_ends_with_every_pull_request_in_a_final_state() {
    let w = world(StubModel::answering(agent).await).await;
    let mut reds = build_fleet(&w.sim);
    let _daemon = w.daemon().await;
    settle(&w, &mut reds, |w| w.pushes() == 4).await;
    let held_cleared = async {
        let report = w.report().await;
        state_counts(&report).get("mergeable").copied() == Some(28)
    };
    let mut done = held_cleared.await;
    let started = Instant::now();
    while !done && started.elapsed() < WAIT {
        run_ci(&w, &mut reds).await;
        tokio::time::sleep(Duration::from_millis(500)).await;
        done = state_counts(&w.report().await).get("mergeable").copied() == Some(28);
    }
    let report = w.report().await;
    assert_eq!(report["repositories_scanned"], 69);
    assert_eq!(all_pulls(&report), 28);
    assert_eq!(
        state_counts(&report).get("mergeable"),
        Some(&28),
        "{report}"
    );
    assert!(w.questions().is_empty(), "no one needed to be asked");
}

#[tokio::test(flavor = "multi_thread")]
async fn a_bad_day_keeps_healthy_work_moving_and_asks_about_the_rest_once() {
    let w = world(StubModel::answering(agent).await).await;
    let mut reds = build_fleet(&w.sim);
    let doomed = reds[0].repo.clone();
    reds[0].fixable = false;
    // The first webhook is lost before anything is listening, so only a scan can find it.
    w.sim
        .next_deliveries(&[Fate::Lost, Fate::Duplicated, Fate::Duplicated]);
    run_ci(&w, &mut reds).await;
    w.sim.fail_requests(
        "/pulls",
        &[Failure::RateLimited {
            retry_after_seconds: 1,
        }],
    );
    let _daemon = w.daemon().await;

    let asked_once = |w: &World| !w.questions().is_empty();
    settle(&w, &mut reds, asked_once).await;
    let started = Instant::now();
    while started.elapsed() < Duration::from_secs(10) {
        run_ci(&w, &mut reds).await;
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
    let questions = w.questions();
    assert_eq!(questions.len(), 1, "the failing one asks a person once");
    assert!(
        questions[0].starts_with(&format!("@{OWNER} ")),
        "{questions:?}"
    );
    assert_eq!(w.assigned(), [json!([OWNER])]);
    assert_eq!(w.pushes_to(&doomed), 3, "stopped at the attempt cap");
    let report = w.report().await;
    let counts = state_counts(&report);
    assert_eq!(counts.get("needs-person"), Some(&1), "{report}");
    let model_calls = w.model.requests().len();
    assert!(
        model_calls <= 2 * 20,
        "budgets held: {model_calls} model calls"
    );
}

#[tokio::test(flavor = "multi_thread")]
async fn killing_the_process_mid_run_loses_no_effect_and_duplicates_none() {
    let w = world(StubModel::answering(agent).await).await;
    w.model.hold();
    let repo = format!("{OWNER}/repo-00");
    w.sim.add_repo(Repo::public(&repo));
    w.sim.set_tarball(&repo, source_tarball());
    let branch = "renovate/pkg-0";
    let number = w.sim.add_pull(&repo, pull(branch, "pkg-0"));
    let broken = w.sim.pull(&repo, number).head_sha;
    let mut daemon = w.daemon().await;
    w.ci(&repo, number, "failure", branch).await;

    wait("the run to reach its agent", || {
        let requests = w.model.requests().len();
        async move { requests > 0 }
    })
    .await;
    daemon.start_kill().unwrap();
    daemon.wait().await.unwrap();
    assert_eq!(w.pushes(), 0, "killed before it could push");

    w.model.release();
    let _restarted = w.daemon().await;
    wait("the repair to land", || {
        let pushed = w.pushes() == 1;
        async move { pushed }
    })
    .await;
    tokio::time::sleep(Duration::from_secs(3)).await;
    assert_eq!(w.pushes(), 1, "the effect happened exactly once");
    assert_ne!(w.sim.pull(&repo, number).head_sha, broken);
}

#[tokio::test(flavor = "multi_thread")]
async fn a_dry_run_leaves_a_live_run_everything_still_to_do() {
    let w = world(StubModel::answering(agent).await).await;
    let mut reds = build_fleet(&w.sim);
    run_ci(&w, &mut reds).await;
    let mut command = w.command(&["--format", "json", "backfill", "--dry-run"]);
    command.stdout(Stdio::piped()).stderr(Stdio::piped());
    let out = command.output().await.unwrap();
    let err = String::from_utf8_lossy(&out.stderr);
    assert!(out.status.success(), "{err}");
    let dry: Value = serde_json::from_slice(&out.stdout).unwrap();
    assert!(dry["backfill"]["queued"].as_u64().unwrap() > 0, "{dry}");
    let seen_by = |name: &'static str| {
        let pool = w.pool.clone();
        async move {
            sqlx::query_scalar::<_, i64>("SELECT count(*) FROM seen WHERE almanac = $1")
                .bind(name)
                .fetch_one(&pool)
                .await
                .unwrap()
        }
    };
    assert!(seen_by("almanac-renovate.dry-run").await > 0);
    assert_eq!(
        seen_by("almanac-renovate").await,
        0,
        "live has seen nothing"
    );
}

#[tokio::test(flavor = "multi_thread")]
async fn a_second_reader_of_the_log_sees_every_delivery_and_disturbs_nothing() {
    let w = world(StubModel::answering(agent).await).await;
    let mut reds = build_fleet(&w.sim);
    let _daemon = w.daemon().await;
    let cursor_of = |name: &'static str| {
        let pool = w.pool.clone();
        async move {
            sqlx::query_scalar::<_, i64>("SELECT seq FROM cursors WHERE almanac = $1")
                .bind(name)
                .fetch_optional(&pool)
                .await
                .unwrap()
        }
    };
    sqlx::query("INSERT INTO cursors (almanac, seq) VALUES ('second', 0)")
        .execute(&w.pool)
        .await
        .unwrap();
    settle(&w, &mut reds, |w| w.pushes() == 4).await;
    let logged = w.logged().await;
    assert!(logged > 0);
    let first = deliveries::after(&w.pool, 0, 10_000).await.unwrap();
    assert_eq!(
        first.len() as i64,
        logged,
        "everything is there for a second reader"
    );
    assert!(cursor_of("almanac-renovate").await.is_some());
    assert_eq!(cursor_of("second").await, Some(0), "its cursor is its own");
    let _ = &w.hook;
    drop(w.broker);
}
