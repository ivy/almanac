//! Every field the simulator returns must be one the pinned spec declares, with the declared
//! type. A provider tested here therefore reads nothing GitHub would not send.
use serde_json::Value;
use simforge::{Pull, Repo, SimForge, Suite};

const REPO: &str = "owner/repo";

fn spec() -> Value {
    let text = std::fs::read_to_string(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../specs/github/api.github.com.json"
    ))
    .unwrap();
    serde_json::from_str(&text).unwrap()
}

fn resolve<'a>(spec: &'a Value, schema: &'a Value) -> &'a Value {
    match schema["$ref"].as_str() {
        Some(pointer) => resolve(spec, spec.pointer(&pointer[1..]).unwrap()),
        None => schema,
    }
}

fn type_matches(declared: &Value, value: &Value) -> bool {
    let one = |t: &str| match t {
        "string" => value.is_string(),
        "integer" => value.is_i64() || value.is_u64(),
        "number" => value.is_number(),
        "boolean" => value.is_boolean(),
        "object" => value.is_object(),
        "array" => value.is_array(),
        "null" => value.is_null(),
        _ => false,
    };
    match declared {
        Value::String(t) => one(t),
        Value::Array(ts) => ts.iter().any(|t| one(t.as_str().unwrap())),
        _ => true,
    }
}

/// Problems with `value` against `schema`, each with its path. Empty means it conforms.
fn problems(spec: &Value, schema: &Value, value: &Value, at: &str) -> Vec<String> {
    let schema = resolve(spec, schema);
    for key in ["anyOf", "oneOf"] {
        if let Some(options) = schema[key].as_array() {
            let mut best: Option<Vec<String>> = None;
            for option in options {
                let found = problems(spec, option, value, at);
                if found.is_empty() {
                    return vec![];
                }
                if best.as_ref().is_none_or(|b| found.len() < b.len()) {
                    best = Some(found);
                }
            }
            return best.unwrap_or_default();
        }
    }
    if let Some(parts) = schema["allOf"].as_array() {
        let mut merged = serde_json::Map::new();
        for part in parts {
            let part = resolve(spec, part);
            if let Some(properties) = part["properties"].as_object() {
                merged.extend(properties.clone());
            }
        }
        let synthetic = serde_json::json!({"type": "object", "properties": merged});
        return problems(spec, &synthetic, value, at);
    }
    if value.is_null() {
        let nullable = schema["nullable"].as_bool().unwrap_or(false);
        let typed = !schema["type"].is_null();
        return if nullable || !typed || type_matches(&schema["type"], value) {
            vec![]
        } else {
            vec![format!("{at} is null but not nullable")]
        };
    }
    if !schema["type"].is_null() && !type_matches(&schema["type"], value) {
        return vec![format!(
            "{at} is {value} but the spec says {}",
            schema["type"]
        )];
    }
    let mut found = vec![];
    if let (Some(fields), Some(properties)) = (value.as_object(), schema["properties"].as_object())
    {
        for (name, field) in fields {
            match properties.get(name) {
                Some(declared) => {
                    found.extend(problems(spec, declared, field, &format!("{at}.{name}")));
                }
                None => found.push(format!("{at}.{name} is not in the spec")),
            }
        }
    }
    if let (Some(items), Some(declared)) = (value.as_array(), schema.get("items")) {
        for (i, item) in items.iter().enumerate() {
            found.extend(problems(spec, declared, item, &format!("{at}[{i}]")));
        }
    }
    found
}

async fn scenario() -> (SimForge, String, u64) {
    let sim = SimForge::start().await;
    sim.add_repo(Repo::public(REPO));
    let mut pull = Pull::renovate("renovate/lodash-4.x", "Update lodash");
    pull.labels.push("dependencies".to_string());
    let number = sim.add_pull(REPO, pull);
    let sha = sim.pull(REPO, number).head_sha;
    sim.add_suite(
        REPO,
        Suite::finished(&sha, "renovate/lodash-4.x", "failure", "boom"),
    );
    (sim, sha, number)
}

#[tokio::test]
async fn reads_match_the_spec() {
    let (sim, sha, number) = scenario().await;
    let client = reqwest::Client::new();
    let post = |path: &str, body: Value| {
        client
            .post(format!("{}/repos/{REPO}{path}", sim.base_url()))
            .bearer_auth("t")
            .json(&body)
            .send()
    };
    let status = serde_json::json!({"state": "pending", "context": "almanac/trust"});
    post(&format!("/statuses/{sha}"), status).await.unwrap();
    let issue = serde_json::json!({"title": "t", "body": "b", "labels": ["almanac"]});
    post("/issues", issue).await.unwrap();

    let spec = spec();
    let endpoints = [
        ("/users/{username}/repos", "/users/owner/repos".to_string()),
        ("/user/repos", "/user/repos".to_string()),
        (
            "/installation/repositories",
            "/installation/repositories".to_string(),
        ),
        ("/repos/{owner}/{repo}", format!("/repos/{REPO}")),
        (
            "/repos/{owner}/{repo}/pulls",
            format!("/repos/{REPO}/pulls"),
        ),
        (
            "/repos/{owner}/{repo}/pulls/{pull_number}",
            format!("/repos/{REPO}/pulls/{number}"),
        ),
        (
            "/repos/{owner}/{repo}/pulls/{pull_number}/commits",
            format!("/repos/{REPO}/pulls/{number}/commits"),
        ),
        (
            "/repos/{owner}/{repo}/commits/{ref}/check-suites",
            format!("/repos/{REPO}/commits/{sha}/check-suites"),
        ),
        (
            "/repos/{owner}/{repo}/commits/{ref}/check-runs",
            format!("/repos/{REPO}/commits/{sha}/check-runs"),
        ),
        (
            "/repos/{owner}/{repo}/commits/{ref}/status",
            format!("/repos/{REPO}/commits/{sha}/status"),
        ),
        (
            "/repos/{owner}/{repo}/issues",
            format!("/repos/{REPO}/issues"),
        ),
        ("/app/hook/deliveries", "/app/hook/deliveries".to_string()),
    ];
    let mut all = vec![];
    for (template, path) in endpoints {
        let value: Value = client
            .get(format!("{}{path}", sim.base_url()))
            .bearer_auth("t")
            .send()
            .await
            .unwrap()
            .json()
            .await
            .unwrap();
        let schema = &spec["paths"][template]["get"]["responses"]["200"]["content"]["application/json"]
            ["schema"];
        assert!(!schema.is_null(), "{template} is not in the spec");
        all.extend(problems(&spec, schema, &value, template));
    }
    assert!(all.is_empty(), "{}", all.join("\n"));
}

#[tokio::test]
async fn writes_match_the_spec() {
    let (sim, sha, _) = scenario().await;
    let spec = spec();
    let client = reqwest::Client::new();
    let writes = [
        (
            "/repos/{owner}/{repo}/statuses/{sha}",
            format!("/repos/{REPO}/statuses/{sha}"),
            serde_json::json!({"state": "success", "context": "c"}),
        ),
        (
            "/repos/{owner}/{repo}/git/blobs",
            format!("/repos/{REPO}/git/blobs"),
            serde_json::json!({"content": "x"}),
        ),
        (
            "/repos/{owner}/{repo}/git/commits",
            format!("/repos/{REPO}/git/commits"),
            serde_json::json!({"message": "m", "tree": "a"}),
        ),
        (
            "/repos/{owner}/{repo}/issues",
            format!("/repos/{REPO}/issues"),
            serde_json::json!({"title": "t"}),
        ),
    ];
    let mut all = vec![];
    for (template, path, body) in writes {
        let value: Value = client
            .post(format!("{}{path}", sim.base_url()))
            .bearer_auth("t")
            .json(&body)
            .send()
            .await
            .unwrap()
            .json()
            .await
            .unwrap();
        let schema = &spec["paths"][template]["post"]["responses"]["201"]["content"]["application/json"]
            ["schema"];
        all.extend(problems(&spec, schema, &value, template));
    }
    assert!(all.is_empty(), "{}", all.join("\n"));
}
