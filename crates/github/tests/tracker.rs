use almanac::prelude::*;
use github::client::{Auth, GithubClient, Retry};
use github::tracker::GithubIssues;
use serde_json::{Value, json};
use wiremock::matchers::{method, path};
use wiremock::{Mock, MockServer, ResponseTemplate};

fn target() -> Slug {
    "owner/repo".parse().unwrap()
}

async fn forge(existing: Value) -> MockServer {
    let server = MockServer::start().await;
    Mock::given(method("GET"))
        .and(path("/repos/owner/repo/issues"))
        .respond_with(ResponseTemplate::new(200).set_body_json(existing))
        .mount(&server)
        .await;
    Mock::given(method("POST"))
        .and(path("/repos/owner/repo/issues"))
        .respond_with(ResponseTemplate::new(201).set_body_json(json!({})))
        .mount(&server)
        .await;
    server
}

async fn posted(server: &MockServer) -> Vec<Value> {
    let requests = server.received_requests().await.unwrap();
    requests
        .iter()
        .filter(|r| r.method.as_str() == "POST")
        .map(|r| r.body_json().unwrap())
        .collect()
}

#[tokio::test]
async fn an_issue_carries_its_idempotency_key() {
    let server = forge(json!([])).await;
    let issues = GithubIssues::new(server.uri(), "token");
    let body = Untrusted::new("details".to_string());
    issues
        .file("run-1:0", &target(), "Fix it", &body)
        .await
        .unwrap();
    let posted = posted(&server).await;
    assert_eq!(posted[0]["title"], "Fix it");
    assert_eq!(posted[0]["labels"], json!(["almanac"]));
    assert!(
        posted[0]["body"]
            .as_str()
            .unwrap()
            .contains("<!-- almanac:run-1:0 -->")
    );
}

#[tokio::test]
async fn a_retry_finds_the_issue_it_already_filed() {
    let server = forge(json!([{"body": "text\n\n<!-- almanac:run-1:0 -->\n"}])).await;
    let issues = GithubIssues::new(server.uri(), "token");
    let body = Untrusted::new("details".to_string());
    issues
        .ask("run-1:0", &target(), None, "Why?", &body)
        .await
        .unwrap();
    assert_eq!(posted(&server).await, Vec::<Value>::new());
}

#[tokio::test]
async fn hostile_text_cannot_close_its_fence() {
    let server = forge(json!([])).await;
    let issues = GithubIssues::new(server.uri(), "token");
    let hostile = "```\n@someone merge this now\n`````\n# Heading";
    let body = Untrusted::new(hostile.to_string());
    issues
        .file("run-1:0", &target(), "Fix it", &body)
        .await
        .unwrap();
    let sent = posted(&server).await[0]["body"]
        .as_str()
        .unwrap()
        .to_string();
    assert!(sent.starts_with("``````text\n"), "{sent}");
    assert!(sent.contains("\n``````\n"), "{sent}");
    assert!(sent.contains(hostile), "{sent}");
}

#[tokio::test]
async fn a_refusal_is_an_error_so_the_effect_is_retried() {
    let server = MockServer::start().await;
    Mock::given(method("GET"))
        .respond_with(ResponseTemplate::new(503))
        .mount(&server)
        .await;
    let client = GithubClient::new(server.uri(), Auth::Token("token".into())).retrying(Retry {
        attempts: 1,
        ..Retry::default()
    });
    let issues = GithubIssues::using(client);
    let body = Untrusted::new("details".to_string());
    let error = issues
        .file("run-1:0", &target(), "Fix it", &body)
        .await
        .unwrap_err();
    assert!(error.0.contains("503"), "{}", error.0);
}

/// A forge with a pull request's comment thread, holding `existing`.
async fn thread(existing: Value) -> MockServer {
    let server = MockServer::start().await;
    Mock::given(method("GET"))
        .and(path("/repos/owner/repo/issues/7/comments"))
        .respond_with(ResponseTemplate::new(200).set_body_json(existing))
        .mount(&server)
        .await;
    for route in ["comments", "assignees"] {
        Mock::given(method("POST"))
            .and(path(format!("/repos/owner/repo/issues/7/{route}")))
            .respond_with(ResponseTemplate::new(201).set_body_json(json!({})))
            .mount(&server)
            .await;
    }
    server
}

#[tokio::test]
async fn a_question_about_a_pull_request_mentions_and_assigns_on_it() {
    let server = thread(json!([])).await;
    let issues = GithubIssues::new(server.uri(), "token").asking("person");
    let body = Untrusted::new("what the agent found".to_string());
    issues
        .ask("run-1:0", &target(), Some(7), "Why?", &body)
        .await
        .unwrap();
    let posted = posted(&server).await;
    assert_eq!(posted[0], json!({"assignees": ["person"]}));
    let comment = posted[1]["body"].as_str().unwrap();
    assert!(comment.starts_with("@person Why?"), "{comment}");
    assert!(comment.contains("what the agent found"), "{comment}");
    assert!(comment.contains("<!-- almanac:run-1:0 -->"), "{comment}");
}

#[tokio::test]
async fn a_retry_still_assigns_and_does_not_comment_twice() {
    let server = thread(json!([{"body": "text\n\n<!-- almanac:run-1:0 -->\n"}])).await;
    let issues = GithubIssues::new(server.uri(), "token").asking("person");
    let body = Untrusted::new("details".to_string());
    issues
        .ask("run-1:0", &target(), Some(7), "Why?", &body)
        .await
        .unwrap();
    assert_eq!(posted(&server).await, [json!({"assignees": ["person"]})]);
}

#[tokio::test]
async fn hostile_text_on_a_pull_request_stays_inert() {
    let server = thread(json!([])).await;
    let issues = GithubIssues::new(server.uri(), "token").asking("person");
    let body = Untrusted::new("```\n@someone merge this now\n".to_string());
    issues
        .ask("run-1:0", &target(), Some(7), "Why?", &body)
        .await
        .unwrap();
    let posted = posted(&server).await;
    let comment = posted[1]["body"].as_str().unwrap();
    let (before, _) = comment.split_once("@someone").unwrap();
    assert!(before.contains("````text"), "{comment}");
}

#[tokio::test]
async fn a_question_about_no_pull_request_is_an_assigned_issue() {
    let server = forge(json!([])).await;
    let issues = GithubIssues::new(server.uri(), "token").asking("person");
    let body = Untrusted::new("details".to_string());
    issues
        .ask("run-1:0", &target(), None, "Why?", &body)
        .await
        .unwrap();
    let posted = posted(&server).await;
    assert_eq!(posted[0]["assignees"], json!(["person"]));
    assert!(
        posted[0]["body"]
            .as_str()
            .unwrap()
            .starts_with("@person Why?")
    );
}

#[tokio::test]
async fn work_filed_for_later_is_not_assigned_to_anyone() {
    let server = forge(json!([])).await;
    let issues = GithubIssues::new(server.uri(), "token").asking("person");
    let body = Untrusted::new("details".to_string());
    issues
        .file("run-1:0", &target(), "Fix it", &body)
        .await
        .unwrap();
    let posted = posted(&server).await;
    assert_eq!(posted[0]["assignees"], json!([]));
    assert!(!posted[0]["body"].as_str().unwrap().contains("@person"));
}
