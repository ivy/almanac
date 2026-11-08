//! Every operation against the simulated forge. Nothing here reaches GitHub.
use almanac::diag::Error;
use almanac::prelude::*;
use github::client::{Auth, GithubClient, Retry};
use github::forge::{CommitAuthor, GithubForge};
use serde_json::{Value, json};
use simforge::{Failure, Fate, Pull, RENOVATE, Repo, SimForge, Suite};
use std::time::{Duration, Instant};

const REPO: &str = "owner/repo";
const BRANCH: &str = "renovate/lodash-4.x";

fn quick() -> Retry {
    Retry {
        attempts: 4,
        backoff: Duration::from_millis(1),
        longest_wait: Duration::from_secs(5),
    }
}

fn client(sim: &SimForge) -> GithubClient {
    GithubClient::new(sim.base_url(), Auth::Token("token".into())).retrying(quick())
}

async fn scripted() -> (SimForge, u64) {
    let sim = SimForge::start().await;
    sim.add_repo(Repo::public(REPO));
    let number = sim.add_pull(REPO, Pull::renovate(BRANCH, "Update lodash"));
    (sim, number)
}

#[tokio::test]
async fn discovery_skips_archived_repositories_and_other_owners() {
    let sim = SimForge::start().await;
    sim.add_repo(Repo::public("owner/live"));
    sim.add_repo(Repo {
        private: true,
        ..Repo::public("owner/private")
    });
    sim.add_repo(Repo {
        archived: true,
        ..Repo::public("owner/old")
    });
    sim.add_repo(Repo::public("someone/else"));
    let found = client(&sim).repositories("owner").await.unwrap();
    let names: Vec<&str> = found
        .iter()
        .filter_map(|r| r["full_name"].as_str())
        .collect();
    assert_eq!(names, ["owner/live", "owner/private"]);
}

#[tokio::test]
async fn an_app_finds_every_repository_past_the_first_page() {
    let key = std::process::Command::new("openssl")
        .args(["genrsa", "2048"])
        .output()
        .unwrap()
        .stdout;
    let sim = SimForge::start().await;
    for i in 0..130 {
        sim.add_repo(Repo::public(&format!("owner/repo-{i}")));
    }
    let auth = Auth::App {
        app_id: "1".to_string(),
        private_key: String::from_utf8(key).unwrap(),
        installation_id: 7,
    };
    let c = GithubClient::new(sim.base_url(), auth).retrying(quick());
    assert_eq!(c.repositories("owner").await.unwrap().len(), 130);
}

#[tokio::test]
async fn pull_requests_are_listed_by_author_across_pages() {
    let sim = SimForge::start().await;
    sim.add_repo(Repo::public(REPO));
    for i in 0..120 {
        sim.add_pull(REPO, Pull::renovate(&format!("renovate/p{i}"), "Update"));
    }
    let mut human = Pull::renovate("feature", "A person's change");
    human.author = "someone".to_string();
    sim.add_pull(REPO, human);
    let pulls = client(&sim).pulls_by(REPO, RENOVATE).await.unwrap();
    assert_eq!(pulls.len(), 120);
}

#[tokio::test]
async fn a_429_backs_off_and_succeeds() {
    let (sim, number) = scripted().await;
    let limited = Failure::RateLimited {
        retry_after_seconds: 1,
    };
    sim.fail_requests("/pulls/", &[limited]);
    let started = Instant::now();
    let pull = client(&sim).pull(REPO, number).await.unwrap();
    assert_eq!(pull["number"], number);
    assert!(
        started.elapsed() >= Duration::from_secs(1),
        "it must wait as asked"
    );
}

#[tokio::test]
async fn a_long_rate_limit_is_reported_and_not_waited_out() {
    let (sim, number) = scripted().await;
    let limited = Failure::RateLimited {
        retry_after_seconds: 3600,
    };
    sim.fail_requests("/pulls/", &[limited]);
    let error = client(&sim).pull(REPO, number).await.unwrap_err();
    assert!(
        matches!(
            error,
            Error::RateLimited {
                wait_seconds: 3600,
                ..
            }
        ),
        "{error:?}"
    );
}

#[tokio::test]
async fn server_errors_are_retried_then_reported() {
    let (sim, number) = scripted().await;
    sim.fail_requests("/pulls/", &[Failure::ServerError(502)]);
    client(&sim).pull(REPO, number).await.unwrap();

    let many = vec![Failure::ServerError(503); 4];
    sim.fail_requests("/pulls/", &many);
    let error = client(&sim).pull(REPO, number).await.unwrap_err();
    assert!(matches!(error, Error::ForgeUnreachable { .. }), "{error:?}");
}

#[tokio::test]
async fn a_refusal_is_not_retried() {
    let (sim, _) = scripted().await;
    let error = client(&sim).pull(REPO, 999).await.unwrap_err();
    assert!(
        matches!(error, Error::ForgeRefused { status: 404, .. }),
        "{error:?}"
    );
}

#[tokio::test]
async fn mergeable_is_read_again_until_github_has_computed_it() {
    let (sim, number) = scripted().await;
    sim.mergeable_after(2);
    let pause = Duration::from_millis(1);
    let c = client(&sim);
    let pull = c.pull_settled(REPO, number, 5, pause).await.unwrap();
    assert_eq!(pull["mergeable"], true);

    sim.mergeable_after(9);
    let pull = c.pull_settled(REPO, number, 2, pause).await.unwrap();
    assert_eq!(
        pull["mergeable"],
        Value::Null,
        "still unknown means waiting"
    );
}

#[tokio::test]
async fn checks_statuses_and_logs_can_be_read() {
    let (sim, number) = scripted().await;
    let sha = sim.pull(REPO, number).head_sha;
    sim.add_suite(
        REPO,
        Suite::finished(&sha, BRANCH, "failure", "error: boom"),
    );
    let c = client(&sim);
    let suites = c.check_suites(REPO, &sha).await.unwrap();
    assert_eq!(suites["check_suites"][0]["conclusion"], "failure");
    let runs = c.check_runs(REPO, &sha).await.unwrap();
    let job = runs["check_runs"][0]["id"].as_u64().unwrap();
    assert_eq!(c.job_logs(REPO, job).await.unwrap(), "error: boom");
    let status = c.combined_status(REPO, &sha).await.unwrap();
    assert_eq!(status["state"], "pending");
}

#[tokio::test]
async fn deliveries_can_be_listed_and_redelivered() {
    let (sim, number) = scripted().await;
    sim.next_deliveries(&[Fate::Lost]);
    sim.pull_request(REPO, number, "opened").await;
    let c = client(&sim);
    let listed = c.deliveries().await.unwrap();
    assert_eq!(listed[0]["status_code"], 502);
    c.redeliver(listed[0]["id"].as_u64().unwrap())
        .await
        .unwrap();
}

#[tokio::test]
async fn a_sweep_redelivers_what_failed_and_only_once() {
    let (sim, number) = scripted().await;
    let received = std::sync::Arc::new(std::sync::Mutex::new(0));
    let count = received.clone();
    let app = axum::Router::new().route(
        "/",
        axum::routing::post(move || {
            let count = count.clone();
            async move { *count.lock().unwrap() += 1 }
        }),
    );
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}/", listener.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(listener, app).await });
    sim.deliver_to(&url, "s");
    sim.next_deliveries(&[Fate::Lost]);
    sim.pull_request(REPO, number, "opened").await;
    sim.pull_request(REPO, number, "edited").await;
    let c = client(&sim);
    assert_eq!(c.redeliver_failures().await.unwrap(), 1);
    assert_eq!(
        c.redeliver_failures().await.unwrap(),
        0,
        "it succeeded the second time"
    );
    assert_eq!(*received.lock().unwrap(), 2);
}

#[tokio::test]
async fn a_read_only_client_refuses_every_write() {
    let (sim, number) = scripted().await;
    let c = client(&sim).read_only();
    c.pull(REPO, number).await.unwrap();
    let error = c
        .post("/repos/owner/repo/issues", &json!({}))
        .await
        .unwrap_err();
    assert!(matches!(error, Error::ReadOnly { .. }), "{error:?}");
    let error = c
        .patch("/repos/owner/repo/git/refs/heads/x", &json!({}))
        .await
        .unwrap_err();
    assert!(matches!(error, Error::ReadOnly { .. }), "{error:?}");
    assert_eq!(sim.writes(), vec![]);
}

#[tokio::test]
async fn an_app_authenticates_with_a_signed_token_then_an_installation_token() {
    let key = std::process::Command::new("openssl")
        .args(["genrsa", "2048"])
        .output()
        .unwrap()
        .stdout;
    let (sim, _) = scripted().await;
    let auth = Auth::App {
        app_id: "1".to_string(),
        private_key: String::from_utf8(key).unwrap(),
        installation_id: 7,
    };
    let c = GithubClient::new(sim.base_url(), auth).retrying(quick());
    let found = c.repositories("owner").await.unwrap();
    assert_eq!(found.len(), 1);
    c.repositories("owner").await.unwrap();
    let minted: Vec<_> = sim
        .writes()
        .into_iter()
        .filter(|w| w.path.contains("access_tokens"))
        .collect();
    assert_eq!(minted.len(), 1, "the token is cached");
}

#[tokio::test]
async fn a_private_key_may_arrive_on_one_line() {
    let key = std::process::Command::new("openssl")
        .args(["genrsa", "2048"])
        .output()
        .unwrap()
        .stdout;
    let (sim, _) = scripted().await;
    let auth = Auth::App {
        app_id: "1".to_string(),
        private_key: String::from_utf8(key).unwrap().replace('\n', "\\n"),
        installation_id: 7,
    };
    let c = GithubClient::new(sim.base_url(), auth).retrying(quick());
    assert_eq!(c.repositories("owner").await.unwrap().len(), 1);
}

fn forge(sim: &SimForge) -> GithubForge {
    GithubForge::new(client(sim), CommitAuthor::default())
}

fn push_of(sim: &SimForge, number: u64, branch: &str) -> Push {
    let base = sim.pull(REPO, number).head_sha;
    Push {
        repo: REPO.parse().unwrap(),
        pull: number,
        branch: branch.parse().unwrap(),
        message: "fix: repair the build".to_string(),
        hold: None,
        base: base.parse().unwrap(),
        patch: Untrusted::new(Patch {
            changes: vec![
                Change::Write {
                    path: "src/main.rs".to_string(),
                    content: "eA==".to_string(),
                    executable: false,
                },
                Change::Delete {
                    path: "src/old.rs".to_string(),
                },
            ],
            diff: String::new(),
            answer: Default::default(),
            usage: Default::default(),
        }),
    }
}

#[tokio::test]
async fn a_status_is_posted() {
    let (sim, number) = scripted().await;
    let sha: Sha = sim.pull(REPO, number).head_sha.parse().unwrap();
    let repo: Slug = REPO.parse().unwrap();
    forge(&sim)
        .attest("k", &repo, &sha, "almanac/trust", AttestState::Pending)
        .await
        .unwrap();
    let writes = sim.writes();
    assert_eq!(writes[0].body["state"], "pending");
    assert_eq!(writes[0].body["context"], "almanac/trust");
}

#[tokio::test]
async fn a_patch_lands_as_one_commit_by_the_stable_author() {
    let (sim, number) = scripted().await;
    let push = push_of(&sim, number, BRANCH);
    let base = push.base.to_string();
    forge(&sim).push("run-1:0", &push).await.unwrap();

    let writes = sim.writes();
    let paths: Vec<&str> = writes
        .iter()
        .map(|w| w.path.rsplit('/').next().unwrap())
        .collect();
    assert_eq!(paths, ["blobs", "trees", "commits", "lodash-4.x"]);
    let tree = &writes[1].body;
    assert_eq!(tree["base_tree"], base.as_str());
    assert_eq!(tree["tree"][1]["sha"], Value::Null, "a delete");
    let commit = &writes[2].body;
    assert_eq!(commit["author"]["name"], "almanac[bot]");
    assert_eq!(commit["parents"][0], base.as_str());
    assert!(
        commit["message"]
            .as_str()
            .unwrap()
            .contains("Almanac-Key: run-1:0")
    );
    assert_eq!(writes[3].body["force"], false);
    assert_ne!(sim.pull(REPO, number).head_sha, base);
}

#[tokio::test]
async fn pushing_to_another_branch_is_refused() {
    let (sim, number) = scripted().await;
    let push = push_of(&sim, number, "main");
    let error = forge(&sim).push("k", &push).await.unwrap_err();
    assert!(
        error.0.contains("not this pull request's branch"),
        "{}",
        error.0
    );
    assert_eq!(sim.writes(), vec![]);
}

#[tokio::test]
async fn pushing_to_a_fork_is_refused() {
    let sim = SimForge::start().await;
    sim.add_repo(Repo::public(REPO));
    let mut pull = Pull::renovate(BRANCH, "From a fork");
    pull.head_repo = Some("stranger/repo".to_string());
    let number = sim.add_pull(REPO, pull);
    let push = push_of(&sim, number, BRANCH);
    let error = forge(&sim).push("k", &push).await.unwrap_err();
    assert!(error.0.contains("fork"), "{}", error.0);
    assert_eq!(sim.writes(), vec![]);
}

#[tokio::test]
async fn a_branch_that_moved_refuses_the_push() {
    let (sim, number) = scripted().await;
    let push = push_of(&sim, number, BRANCH);
    sim.move_branch(REPO, number, &"b".repeat(40), "someone else's commit");
    let error = forge(&sim).push("k", &push).await.unwrap_err();
    assert!(error.0.contains("moved"), "{}", error.0);
    assert_eq!(sim.writes(), vec![]);
}

#[tokio::test]
async fn a_retry_after_a_crash_recognises_its_own_commit() {
    let (sim, number) = scripted().await;
    let push = push_of(&sim, number, BRANCH);
    let f = forge(&sim);
    f.push("run-1:0", &push).await.unwrap();
    let writes = sim.writes().len();
    f.push("run-1:0", &push).await.unwrap();
    assert_eq!(sim.writes().len(), writes, "applying twice is a no-op");
}

#[tokio::test]
async fn a_pull_requests_added_lines_are_read_from_its_diffs() {
    let sim = SimForge::start().await;
    sim.add_repo(Repo::public("owner/repo"));
    let mut pull = Pull::renovate("renovate/x", "Update");
    pull.files = vec!["package.json".to_string(), "big.lock".to_string()];
    let diff = "@@ -1 +1 @@\n-old\n+new".to_string();
    pull.diffs.insert("package.json".to_string(), diff);
    let number = sim.add_pull("owner/repo", pull);
    let client = GithubClient::new(sim.base_url(), Auth::Token("t".to_string()));
    let added = client.pull_added_lines("owner/repo", number).await.unwrap();
    assert_eq!(added, [("package.json".to_string(), "new".to_string())]);
}

/// Accepts connections and never answers, like a forge that stalled mid-request.
async fn silent_forge() -> String {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    tokio::spawn(async move {
        let mut held = Vec::new();
        while let Ok((socket, _)) = listener.accept().await {
            held.push(socket);
        }
    });
    format!("http://{address}")
}

#[tokio::test(start_paused = true)]
async fn a_forge_that_stops_answering_is_unreachable_rather_than_a_hang() {
    let c = GithubClient::new(silent_forge().await, Auth::Token("token".into())).retrying(quick());
    let answer = c.exists(REPO, ".github/workflows").await;
    assert!(
        matches!(answer, Err(Error::ForgeUnreachable { .. })),
        "{answer:?}"
    );
}
