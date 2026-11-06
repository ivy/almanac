//! Signed webhook delivery, with the ways it goes wrong in production.
use crate::SimForge;
use crate::world::{Delivery, World};
use hmac::{Hmac, Mac};
use serde_json::{Value, json};
use sha2::Sha256;
use std::sync::{Arc, Mutex};

/// What happens to the next emitted event. The default is `Delivered`.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Fate {
    Delivered,
    /// Recorded as failed and never sent, as when the receiver is down.
    Lost,
    /// Sent twice with one delivery ID, as a redelivery is.
    Duplicated,
    /// Kept until `release_held`, which sends the newest first.
    Held,
}

/// The value of `X-Hub-Signature-256`.
pub fn sign(secret: &str, body: &[u8]) -> String {
    let mut mac = Hmac::<Sha256>::new_from_slice(secret.as_bytes()).expect("any key length");
    mac.update(body);
    format!("sha256={}", hex::encode(mac.finalize().into_bytes()))
}

async fn send(client: &reqwest::Client, target: &(String, String), d: &Delivery) -> u16 {
    let body = serde_json::to_vec(&d.payload).expect("JSON");
    let response = client
        .post(&target.0)
        .header("Content-Type", "application/json")
        .header("User-Agent", "GitHub-Hookshot/simforge")
        .header("X-GitHub-Event", &d.event)
        .header("X-GitHub-Delivery", &d.guid)
        .header("X-Hub-Signature-256", sign(&target.1, &body))
        .body(body)
        .send()
        .await;
    response.map_or(599, |r| r.status().as_u16())
}

/// GitHub keeps the delivery ID on a redelivery and records a new attempt.
pub(crate) async fn redeliver(world: Arc<Mutex<World>>, original: Delivery) {
    let client = reqwest::Client::new();
    let target = world.lock().unwrap().target.clone();
    let Some(target) = target else {
        return;
    };
    let status_code = send(&client, &target, &original).await;
    let mut world = world.lock().unwrap();
    let id = world.next_id();
    world.deliveries.push(Delivery {
        id,
        status_code,
        redelivery: true,
        ..original
    });
}

impl SimForge {
    /// Where webhooks go, and the secret that signs them.
    pub fn deliver_to(&self, url: &str, secret: &str) {
        self.world.lock().unwrap().target = Some((url.to_string(), secret.to_string()));
    }

    /// Scripts the fates of the next events, in order.
    pub fn next_deliveries(&self, fates: &[Fate]) {
        self.world.lock().unwrap().fates.extend(fates);
    }

    /// Delivers one event, subject to its fate. Returns the delivery ID.
    pub async fn emit(&self, event: &str, action: Option<&str>, payload: Value) -> String {
        let (delivery, fate, target) = {
            let mut world = self.world.lock().unwrap();
            let id = world.next_id();
            let repository = payload["repository"]["full_name"].as_str().unwrap_or("");
            let repository_id = world.repo_id(repository);
            let delivery = Delivery {
                id,
                guid: format!("00000000-0000-4000-8000-{id:012}"),
                event: event.to_string(),
                action: action.map(str::to_string),
                payload,
                status_code: 0,
                redelivery: false,
                repository_id,
            };
            let fate = world.fates.pop_front().unwrap_or(Fate::Delivered);
            (delivery, fate, world.target.clone())
        };
        let guid = delivery.guid.clone();
        match (fate, target) {
            (Fate::Held, _) => self.world.lock().unwrap().held.push(delivery),
            (Fate::Lost, _) | (_, None) => self.record(delivery, 502),
            (Fate::Delivered, Some(target)) => {
                let code = send(&self.client, &target, &delivery).await;
                self.record(delivery, code);
            }
            (Fate::Duplicated, Some(target)) => {
                let code = send(&self.client, &target, &delivery).await;
                let again = send(&self.client, &target, &delivery).await;
                self.record(delivery.clone(), code);
                let mut world = self.world.lock().unwrap();
                let id = world.next_id();
                world.deliveries.push(Delivery {
                    id,
                    status_code: again,
                    redelivery: true,
                    ..delivery
                });
            }
        }
        guid
    }

    fn record(&self, mut delivery: Delivery, status_code: u16) {
        delivery.status_code = status_code;
        self.world.lock().unwrap().deliveries.push(delivery);
    }

    /// Sends held events, newest first, so they arrive out of order.
    pub async fn release_held(&self) {
        let (held, target) = {
            let mut world = self.world.lock().unwrap();
            (std::mem::take(&mut world.held), world.target.clone())
        };
        let Some(target) = target else {
            return;
        };
        for delivery in held.into_iter().rev() {
            let code = send(&self.client, &target, &delivery).await;
            self.record(delivery, code);
        }
    }

    fn pull_event(&self, repo: &str, number: u64, action: &str) -> Value {
        let world = self.world.lock().unwrap();
        let r = &world.repos[repo];
        let p = world.pull(repo, number);
        let mut payload = json!({
            "action": action,
            "number": number,
            "pull_request": world.pull_json(r, p),
            "repository": world.repo_json(r),
            "sender": world.user_json(&p.author),
        });
        if action == "labeled" {
            payload["label"] = json!({"name": p.labels.last()});
        }
        payload
    }

    pub async fn pull_request(&self, repo: &str, number: u64, action: &str) -> String {
        let payload = self.pull_event(repo, number, action);
        self.emit("pull_request", Some(action), payload).await
    }

    /// `check_suite.completed` for a suite added with `add_suite`.
    pub async fn suite_completed(&self, repo: &str, suite_id: u64) -> String {
        let payload = {
            let world = self.world.lock().unwrap();
            let suites = world.suites.get(repo).expect("a repository with suites");
            let suite = suites.iter().find(|s| s.id == suite_id).expect("a suite");
            json!({
                "action": "completed",
                "check_suite": world.suite_json(suite),
                "repository": world.repo_json(&world.repos[repo]),
                "sender": world.user_json("github-actions[bot]"),
            })
        };
        self.emit("check_suite", Some("completed"), payload).await
    }

    /// A `status` event, as a commit status webhook.
    pub async fn status_changed(
        &self,
        repo: &str,
        sha: &str,
        context: &str,
        state: &str,
    ) -> String {
        let payload = {
            let world = self.world.lock().unwrap();
            json!({
                "sha": sha, "context": context, "state": state,
                "repository": world.repo_json(&world.repos[repo]),
                "sender": world.user_json("github-actions[bot]"),
            })
        };
        self.emit("status", None, payload).await
    }
}
