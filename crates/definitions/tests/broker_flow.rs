//! The broker, the log, and two almanacs, on a real PostgreSQL. `make test-db` runs this.
use almanac::consumer::Consumer;
use almanac::prelude::*;
use almanac::runtime::{Outcome, Runtime};
use almanac::store::{PostgresStore, Store};
use broker::{GithubSource, PostgresLog, router};
use github::PullRequestOpened;
use simforge::{Pull, Repo, SimForge};
use sqlx::postgres::PgPool;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;
use testcontainers_modules::postgres::Postgres;
use testcontainers_modules::testcontainers::ContainerAsync;
use testcontainers_modules::testcontainers::runners::AsyncRunner;

const REPO: &str = "owner/repo";
const SECRET: &str = "a webhook secret";

#[workflow(on = PullRequestOpened, key = "{repo}:{head_branch}", intent = "Note a pull request.", may = [Await], runs_per_hour = 1000, decide = wait)]
async fn acts(_cx: &Cx, _pr: &PullRequestOpened) -> Result<(), Halt> {
    Ok(())
}

fn wait(_: &()) -> Vec<Effect> {
    vec![Effect::Await { until: "nothing" }]
}

struct World {
    _container: ContainerAsync<Postgres>,
    url: String,
    pool: PgPool,
    sim: SimForge,
}

async fn world() -> World {
    let container = Postgres::default().start().await.unwrap();
    let port = container.get_host_port_ipv4(5432).await.unwrap();
    let url = format!("postgres://postgres:postgres@127.0.0.1:{port}/postgres");
    PostgresStore::connect(&url, "migrate").await.unwrap();
    let pool = PgPool::connect(&url).await.unwrap();
    deliveries::migrate(&pool).await.unwrap();
    let app = router(GithubSource::new(SECRET), PostgresLog(pool.clone()));
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let hook = format!("http://{}/webhooks/github", listener.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(listener, app).await });
    let sim = SimForge::start().await;
    sim.add_repo(Repo::public(REPO));
    sim.deliver_to(&hook, SECRET);
    World {
        _container: container,
        url,
        pool,
        sim,
    }
}

impl World {
    fn pull(&self, branch: &str) -> u64 {
        self.sim.add_pull(REPO, Pull::renovate(branch, "Update"))
    }

    async fn opened(&self, branch: &str) {
        let number = self.pull(branch);
        self.sim.pull_request(REPO, number, "opened").await;
    }

    async fn almanac(
        &self,
        name: &str,
        workflows: Vec<Box<dyn Registered>>,
    ) -> (Consumer, Runtime) {
        let store: Arc<dyn Store> = Arc::new(PostgresStore::lazy(&self.url, name).unwrap());
        let runtime = Runtime::new(workflows, Providers::new()).with_store(store);
        (Consumer::connect(&self.url, name).await.unwrap(), runtime)
    }

    async fn logged(&self) -> i64 {
        sqlx::query_scalar("SELECT count(*) FROM deliveries")
            .fetch_one(&self.pool)
            .await
            .unwrap()
    }
}

fn runs(outcomes: &[Outcome]) -> usize {
    let ran = |o: &&Outcome| matches!(o, Outcome::Ran { .. });
    outcomes.iter().filter(ran).count()
}

#[tokio::test]
async fn acknowledgement_never_waits_on_any_almanac() {
    let w = world().await;
    w.opened("renovate/a").await;
    assert_eq!(w.logged().await, 1, "acknowledged with no almanac running");
}

#[tokio::test]
async fn two_almanacs_both_see_a_delivery_and_only_a_matching_one_acts() {
    let w = world().await;
    let (a, runtime_a) = w.almanac("a", vec![Box::new(Acts)]).await;
    let (b, runtime_b) = w.almanac("b", vec![]).await;
    w.opened("renovate/a").await;

    assert_eq!(a.catch_up(&runtime_a).await.unwrap(), 1);
    assert_eq!(b.catch_up(&runtime_b).await.unwrap(), 1);
    assert_eq!(runs(&runtime_a.drain().await.unwrap()), 1);
    assert_eq!(runs(&runtime_b.drain().await.unwrap()), 0);

    assert_eq!(
        a.catch_up(&runtime_a).await.unwrap(),
        0,
        "each sees it once"
    );
    assert_eq!(
        b.catch_up(&runtime_b).await.unwrap(),
        0,
        "each sees it once"
    );
}

#[tokio::test]
async fn an_almanac_that_was_stopped_catches_up_from_its_cursor() {
    let w = world().await;
    let (consumer, runtime) = w.almanac("b", vec![Box::new(Acts)]).await;
    drop((consumer, runtime));
    for branch in ["renovate/a", "renovate/b", "renovate/c"] {
        w.opened(branch).await;
    }
    let (consumer, runtime) = w.almanac("b", vec![Box::new(Acts)]).await;
    assert_eq!(consumer.catch_up(&runtime).await.unwrap(), 3);
    assert_eq!(runs(&runtime.drain().await.unwrap()), 3);
}

#[tokio::test]
async fn a_new_almanac_starts_at_the_end_of_the_log() {
    let w = world().await;
    w.opened("renovate/old").await;
    let (consumer, runtime) = w.almanac("new", vec![Box::new(Acts)]).await;
    assert_eq!(consumer.catch_up(&runtime).await.unwrap(), 0);
}

#[tokio::test]
async fn a_body_that_is_not_json_cannot_stop_a_cursor() {
    let w = world().await;
    let (consumer, runtime) = w.almanac("a", vec![Box::new(Acts)]).await;
    let garbage = deliveries::Delivery {
        source: "github".to_string(),
        delivery_id: "garbage".to_string(),
        kind: "github.pull_request.opened".to_string(),
        body: b"not json".to_vec(),
    };
    deliveries::append(&w.pool, &garbage).await.unwrap();
    w.opened("renovate/after").await;
    assert_eq!(consumer.catch_up(&runtime).await.unwrap(), 2);
    assert_eq!(runs(&runtime.drain().await.unwrap()), 1);
}

#[tokio::test]
async fn a_redelivery_reaches_the_log_once() {
    let w = world().await;
    let number = w.pull("renovate/a");
    w.sim.next_deliveries(&[simforge::Fate::Duplicated]);
    w.sim.pull_request(REPO, number, "opened").await;
    assert_eq!(w.logged().await, 1);
}

#[tokio::test]
async fn following_wakes_on_notify_without_waiting_for_a_poll() {
    let w = world().await;
    let (consumer, runtime) = w.almanac("a", vec![Box::new(Acts)]).await;
    let consumer = consumer.polling_every(Duration::from_secs(3600));
    let (done, stopped) = tokio::sync::oneshot::channel::<()>();
    let follower = async {
        let stop = async {
            let _ = stopped.await;
        };
        consumer.follow(&runtime, stop).await.unwrap();
    };
    let driver = async {
        tokio::time::sleep(Duration::from_millis(200)).await;
        w.opened("renovate/a").await;
        for _ in 0..200 {
            let cursor: i64 = sqlx::query_scalar("SELECT seq FROM cursors WHERE almanac = 'a'")
                .fetch_one(&w.pool)
                .await
                .unwrap();
            if cursor > 0 {
                break;
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
        done.send(()).unwrap();
    };
    tokio::join!(follower, driver);
    let cursor: i64 = sqlx::query_scalar("SELECT seq FROM cursors WHERE almanac = 'a'")
        .fetch_one(&w.pool)
        .await
        .unwrap();
    assert!(cursor > 0, "the notification was not acted on");
}

#[tokio::test]
async fn a_reader_racing_concurrent_appends_misses_nothing() {
    let w = world().await;
    let (consumer, runtime) = w.almanac("a", vec![]).await;
    let total = 200;
    let read = AtomicUsize::new(0);
    let writers = (0..total).map(|i| {
        let pool = w.pool.clone();
        async move {
            let delivery = deliveries::Delivery {
                source: "github".to_string(),
                delivery_id: format!("d{i}"),
                kind: "none".to_string(),
                body: b"{}".to_vec(),
            };
            deliveries::append(&pool, &delivery).await.unwrap();
        }
    });
    let reader = async {
        while read.load(Ordering::Relaxed) < total {
            let n = consumer.catch_up(&runtime).await.unwrap();
            read.fetch_add(n, Ordering::Relaxed);
            tokio::task::yield_now().await;
        }
    };
    let timed = tokio::time::timeout(Duration::from_secs(30), async {
        tokio::join!(futures::future::join_all(writers), reader);
    });
    timed.await.expect("a delivery was skipped");
    assert_eq!(read.load(Ordering::Relaxed), total);
}
