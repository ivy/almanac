//! The scripted state, and how it renders as GitHub's JSON.
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::collections::{BTreeMap, VecDeque};

const TIME: &str = "2026-11-05T12:00:00Z";

#[derive(Clone, Debug)]
pub struct Repo {
    pub full_name: String,
    pub private: bool,
    pub archived: bool,
}

impl Repo {
    pub fn public(full_name: &str) -> Self {
        Self {
            full_name: full_name.to_string(),
            private: false,
            archived: false,
        }
    }

    pub fn owner(&self) -> &str {
        self.full_name.split('/').next().unwrap_or_default()
    }

    pub fn name(&self) -> &str {
        self.full_name.split('/').nth(1).unwrap_or_default()
    }
}

#[derive(Clone, Debug)]
pub struct Pull {
    pub number: u64,
    pub title: String,
    pub body: Option<String>,
    pub author: String,
    pub head_ref: String,
    pub head_sha: String,
    pub base_ref: String,
    pub labels: Vec<String>,
    pub state: String,
    pub draft: bool,
    pub mergeable_state: String,
}

impl Pull {
    /// A Renovate pull request on `head_ref`. `number` and `head_sha` are set when added.
    pub fn renovate(head_ref: &str, title: &str) -> Self {
        Self {
            number: 0,
            title: title.to_string(),
            body: Some("This PR contains the following updates:".to_string()),
            author: crate::RENOVATE.to_string(),
            head_ref: head_ref.to_string(),
            head_sha: String::new(),
            base_ref: "main".to_string(),
            labels: vec![],
            state: "open".to_string(),
            draft: false,
            mergeable_state: "clean".to_string(),
        }
    }
}

/// A check suite, with the single check run it reports through.
#[derive(Clone, Debug)]
pub struct Suite {
    pub id: u64,
    pub sha: String,
    pub branch: String,
    pub status: String,
    pub conclusion: Option<String>,
    pub logs: String,
}

impl Suite {
    pub fn finished(sha: &str, branch: &str, conclusion: &str, logs: &str) -> Self {
        Self {
            id: 0,
            sha: sha.to_string(),
            branch: branch.to_string(),
            status: "completed".to_string(),
            conclusion: Some(conclusion.to_string()),
            logs: logs.to_string(),
        }
    }
}

#[derive(Clone, Debug)]
pub struct Status {
    pub sha: String,
    pub context: String,
    pub state: String,
    pub description: Option<String>,
}

#[derive(Clone, Debug)]
pub struct Issue {
    pub number: u64,
    pub title: String,
    pub body: Option<String>,
    pub labels: Vec<String>,
}

/// One received write, for assertions.
#[derive(Clone, Debug, PartialEq)]
pub struct Write {
    pub method: String,
    pub path: String,
    pub body: Value,
}

#[derive(Clone, Debug, PartialEq)]
pub enum Failure {
    RateLimited { retry_after_seconds: u64 },
    ServerError(u16),
}

#[derive(Clone, Debug)]
pub struct Delivery {
    pub id: u64,
    pub guid: String,
    pub event: String,
    pub action: Option<String>,
    pub payload: Value,
    pub status_code: u16,
    pub redelivery: bool,
    pub repository_id: u64,
}

#[derive(Default)]
pub struct World {
    pub base_url: String,
    pub repos: BTreeMap<String, Repo>,
    pub pulls: BTreeMap<(String, u64), Pull>,
    pub suites: BTreeMap<String, Vec<Suite>>,
    pub statuses: BTreeMap<String, Vec<Status>>,
    pub issues: BTreeMap<String, Vec<Issue>>,
    pub refs: BTreeMap<(String, String), String>,
    pub writes: Vec<Write>,
    pub failures: VecDeque<(String, Failure)>,
    pub deliveries: Vec<Delivery>,
    pub held: Vec<Delivery>,
    pub fates: VecDeque<crate::Fate>,
    pub target: Option<(String, String)>,
    next: u64,
}

impl World {
    pub fn next_id(&mut self) -> u64 {
        self.next += 1;
        self.next
    }

    pub fn next_sha(&mut self) -> String {
        let n = self.next_id();
        hex::encode(Sha256::digest(n.to_be_bytes()))[..40].to_string()
    }

    pub fn add_repo(&mut self, repo: Repo) {
        self.repos.insert(repo.full_name.clone(), repo);
    }

    pub fn add_pull(&mut self, repo: &str, mut pull: Pull) -> u64 {
        let taken = self.pulls.keys().filter(|(r, _)| r == repo).count() as u64;
        let issues = self.issues.get(repo).map_or(0, Vec::len) as u64;
        pull.number = taken + issues + 1;
        if pull.head_sha.is_empty() {
            pull.head_sha = self.next_sha();
        }
        let key = (repo.to_string(), pull.head_ref.clone());
        self.refs.insert(key, pull.head_sha.clone());
        let number = pull.number;
        self.pulls.insert((repo.to_string(), number), pull);
        number
    }

    pub fn add_suite(&mut self, repo: &str, mut suite: Suite) -> u64 {
        suite.id = self.next_id();
        let id = suite.id;
        self.suites.entry(repo.to_string()).or_default().push(suite);
        id
    }

    pub fn pull(&self, repo: &str, number: u64) -> &Pull {
        &self.pulls[&(repo.to_string(), number)]
    }

    pub fn pull_mut(&mut self, repo: &str, number: u64) -> &mut Pull {
        self.pulls
            .get_mut(&(repo.to_string(), number))
            .expect("a scripted pull request")
    }

    pub fn repo_id(&self, full_name: &str) -> u64 {
        self.repos
            .keys()
            .position(|name| name == full_name)
            .map_or(0, |i| i as u64 + 1)
    }

    fn url(&self, path: &str) -> String {
        format!("{}{path}", self.base_url)
    }

    pub fn user_json(&self, login: &str) -> Value {
        let kind = if login.ends_with("[bot]") {
            "Bot"
        } else {
            "User"
        };
        json!({
            "login": login, "id": 1, "node_id": "U_1", "type": kind, "site_admin": false,
            "avatar_url": self.url("/avatar"), "gravatar_id": "",
            "url": self.url(&format!("/users/{login}")),
            "html_url": self.url(&format!("/{login}")),
        })
    }

    pub fn repo_json(&self, repo: &Repo) -> Value {
        let path = format!("/repos/{}", repo.full_name);
        json!({
            "id": self.repo_id(&repo.full_name), "node_id": "R_1",
            "name": repo.name(), "full_name": repo.full_name,
            "owner": self.user_json(repo.owner()),
            "private": repo.private, "archived": repo.archived, "disabled": false, "fork": false,
            "visibility": if repo.private { "private" } else { "public" },
            "default_branch": "main",
            "description": null,
            "url": self.url(&path),
            "html_url": self.url(&format!("/{}", repo.full_name)),
            "created_at": TIME, "updated_at": TIME, "pushed_at": TIME,
        })
    }

    fn branch_json(&self, repo: &Repo, label: &str, branch: &str, sha: &str) -> Value {
        json!({
            "label": format!("{label}:{branch}"), "ref": branch, "sha": sha,
            "user": self.user_json(repo.owner()), "repo": self.repo_json(repo),
        })
    }

    fn label_json(name: &str) -> Value {
        json!({
            "id": 1, "node_id": "L_1", "url": "", "name": name, "description": "",
            "color": "ededed", "default": false,
        })
    }

    /// As listed. A list leaves out what GitHub only computes for a single pull request.
    pub fn pull_simple_json(&self, repo: &Repo, p: &Pull) -> Value {
        let path = format!("/repos/{}/pulls/{}", repo.full_name, p.number);
        let labels: Vec<Value> = p.labels.iter().map(|l| Self::label_json(l)).collect();
        json!({
            "id": p.number, "node_id": "PR_1", "number": p.number, "state": p.state,
            "locked": false, "title": p.title, "body": p.body, "draft": p.draft,
            "user": self.user_json(&p.author),
            "labels": labels,
            "head": self.branch_json(repo, repo.owner(), &p.head_ref, &p.head_sha),
            "base": self.branch_json(repo, repo.owner(), &p.base_ref, &"0".repeat(40)),
            "url": self.url(&path),
            "html_url": self.url(&format!("/{}/pull/{}", repo.full_name, p.number)),
            "created_at": TIME, "updated_at": TIME,
            "closed_at": null, "merged_at": null, "merge_commit_sha": null,
            "assignee": null, "assignees": [], "requested_reviewers": [], "requested_teams": [],
            "milestone": null, "author_association": "NONE", "auto_merge": null,
        })
    }

    pub fn pull_json(&self, repo: &Repo, p: &Pull) -> Value {
        let mut value = self.pull_simple_json(repo, p);
        let clean = p.mergeable_state != "dirty";
        value["mergeable"] = json!(clean);
        value["mergeable_state"] = json!(p.mergeable_state);
        value["merged"] = json!(false);
        value
    }

    pub fn suite_json(&self, s: &Suite) -> Value {
        json!({
            "id": s.id, "node_id": "CS_1", "head_branch": s.branch, "head_sha": s.sha,
            "status": s.status, "conclusion": s.conclusion,
            "url": self.url(&format!("/check-suites/{}", s.id)),
            "before": null, "after": null, "pull_requests": [], "app": null,
            "created_at": TIME, "updated_at": TIME,
            "latest_check_runs_count": 1,
        })
    }

    pub fn check_run_json(&self, repo: &str, s: &Suite) -> Value {
        json!({
            "id": s.id, "node_id": "CR_1", "head_sha": s.sha, "name": "ci", "external_id": "",
            "status": s.status, "conclusion": s.conclusion,
            "url": self.url(&format!("/check-runs/{}", s.id)),
            "html_url": self.url(&format!("/{repo}/runs/{}", s.id)),
            "details_url": self.url(&format!("/{repo}/actions/runs/{}", s.id)),
            "started_at": TIME, "completed_at": TIME,
            "output": { "title": null, "summary": null, "text": null,
                        "annotations_count": 0, "annotations_url": "" },
            "check_suite": { "id": s.id }, "app": null, "pull_requests": [],
        })
    }

    pub fn status_json(&self, s: &Status) -> Value {
        json!({
            "id": 1, "node_id": "S_1", "state": s.state, "context": s.context,
            "description": s.description, "target_url": null, "avatar_url": null,
            "url": self.url(&format!("/statuses/{}", s.sha)),
            "created_at": TIME, "updated_at": TIME,
        })
    }

    pub fn issue_json(&self, repo: &str, i: &Issue) -> Value {
        let labels: Vec<Value> = i.labels.iter().map(|l| Self::label_json(l)).collect();
        let path = format!("/repos/{repo}/issues/{}", i.number);
        json!({
            "id": i.number, "node_id": "I_1", "number": i.number, "title": i.title,
            "body": i.body, "state": "open", "locked": false, "labels": labels,
            "user": null, "assignee": null, "assignees": [], "milestone": null, "comments": 0,
            "url": self.url(&path),
            "html_url": self.url(&format!("/{repo}/issues/{}", i.number)),
            "created_at": TIME, "updated_at": TIME, "closed_at": null,
            "author_association": "NONE",
        })
    }

    pub fn delivery_json(&self, d: &Delivery) -> Value {
        json!({
            "id": d.id, "guid": d.guid, "delivered_at": TIME, "redelivery": d.redelivery,
            "duration": 0.1, "status": if d.status_code < 300 { "OK" } else { "failed" },
            "status_code": d.status_code, "event": d.event, "action": d.action,
            "installation_id": 1, "repository_id": d.repository_id,
        })
    }
}
