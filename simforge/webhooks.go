package simforge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// Fate is what happens to an emitted event, the ways delivery goes wrong in production.
type Fate int

const (
	// Delivered sends the event once. It is the fate of an event with none scripted.
	Delivered Fate = iota
	// Lost records the delivery as failed and never sends it, as when the receiver is down.
	Lost
	// Duplicated sends the event twice with one delivery ID, as a redelivery does.
	Duplicated
	// Held keeps the event until ReleaseHeld, which sends the newest first.
	Held
)

// unreachable is the status recorded for a delivery that got no HTTP answer.
const unreachable = 599

// Sign is the value of X-Hub-Signature-256 for body under secret, as GitHub computes it.
// See https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// DeliverTo sets where webhooks go and the secret that signs them. Until it is called, every
// event is recorded as failed and nothing is sent.
func (f *Forge) DeliverTo(url, secret string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.world.target = &target{url: url, secret: secret}
}

// NextDeliveries scripts the fates of the next events, in order.
func (f *Forge) NextDeliveries(fates ...Fate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.world.fates = append(f.world.fates, fates...)
}

// Emit delivers one event subject to its fate, and returns its delivery ID. An empty action
// is listed as null. The event's repository is read from payload's repository.full_name.
// Emit returns once the receiver has answered. It keeps payload, so change it no further.
func (f *Forge) Emit(ctx context.Context, event, action string, payload map[string]any) string {
	f.mu.Lock()
	id := f.world.nextID()
	repo, _ := payload["repository"].(map[string]any)
	name, _ := repo["full_name"].(string)
	d := delivery{
		id:           id,
		guid:         fmt.Sprintf("00000000-0000-4000-8000-%012d", id),
		event:        event,
		action:       action,
		payload:      payload,
		repositoryID: f.world.repoID(name),
	}
	fate := Delivered
	if len(f.world.fates) > 0 {
		fate, f.world.fates = f.world.fates[0], f.world.fates[1:]
	}
	to := f.world.target
	if fate == Held {
		f.world.held = append(f.world.held, d)
	}
	f.mu.Unlock()

	switch {
	case fate == Held:
		// ReleaseHeld sends it.
	case fate == Lost || to == nil:
		f.record(d, http.StatusBadGateway, false)
	case fate == Duplicated:
		first := f.send(ctx, *to, d)
		again := f.send(ctx, *to, d)
		f.record(d, first, false)
		f.record(d, again, true)
	default:
		f.record(d, f.send(ctx, *to, d), false)
	}
	return d.guid
}

// ReleaseHeld sends held events, newest first, so they arrive out of order. With nowhere to
// deliver them, each is recorded as failed.
func (f *Forge) ReleaseHeld(ctx context.Context) {
	f.mu.Lock()
	held, to := f.world.held, f.world.target
	f.world.held = nil
	f.mu.Unlock()
	for i := len(held) - 1; i >= 0; i-- {
		if to == nil {
			f.record(held[i], http.StatusBadGateway, false)
			continue
		}
		f.record(held[i], f.send(ctx, *to, held[i]), false)
	}
}

// redeliverAttempt sends a delivery again, keeping its delivery ID, and records the attempt
// under a new ID as GitHub does. With no target it does nothing.
func (f *Forge) redeliverAttempt(ctx context.Context, original delivery) {
	f.mu.Lock()
	to := f.world.target
	f.mu.Unlock()
	if to == nil {
		return
	}
	f.record(original, f.send(ctx, *to, original), true)
}

// record logs a delivery attempt. A redelivery is logged under a new ID.
func (f *Forge) record(d delivery, statusCode int, redelivery bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if redelivery {
		d.id = f.world.nextID()
	}
	d.statusCode, d.redelivery = statusCode, redelivery
	f.world.deliveries = append(f.world.deliveries, d)
}

// send posts a delivery with GitHub's headers, signed over the exact body sent. It returns
// the receiver's status, or 599 when there was no answer.
func (f *Forge) send(ctx context.Context, to target, d delivery) int {
	body := encode(d.payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, to.url, bytes.NewReader(body))
	if err != nil {
		return unreachable
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "GitHub-Hookshot/simforge")
	request.Header.Set("X-GitHub-Event", d.event)
	request.Header.Set("X-GitHub-Delivery", d.guid)
	request.Header.Set("X-Hub-Signature-256", Sign(to.secret, body))
	response, err := f.client.Do(request)
	if err != nil {
		return unreachable
	}
	_ = response.Body.Close()
	return response.StatusCode
}

// event builds a payload from the world under the lock, so a panic over a missing repository
// or pull request leaves the forge usable.
func (f *Forge) event(build func(w *world) object) object {
	f.mu.Lock()
	defer f.mu.Unlock()
	return build(f.world)
}

// PullRequest emits a pull_request event with action for a pull request as it stands now.
// A "labeled" event names the pull request's last label. A "synchronize" event's before is
// the head before the branch last moved, or forty zeros if it never has.
func (f *Forge) PullRequest(ctx context.Context, repo string, number int, action string) string {
	payload := f.event(func(w *world) object {
		r, p := w.mustRepo(repo), w.mustPull(repo, number)
		payload := object{
			"action":       action,
			"number":       number,
			"pull_request": w.pullJSON(r, p),
			"repository":   w.repoJSON(r),
			"sender":       w.userJSON(p.Author),
		}
		switch action {
		case "labeled":
			var last any
			if len(p.Labels) > 0 {
				last = p.Labels[len(p.Labels)-1]
			}
			payload["label"] = object{"name": last}
		case "synchronize":
			payload["before"], payload["after"] = p.before, p.HeadSHA
			if p.before == "" {
				payload["before"] = strings.Repeat("0", 40)
			}
		}
		return payload
	})
	return f.Emit(ctx, "pull_request", action, payload)
}

// SuiteCompleted emits check_suite.completed for a suite added with AddSuite.
func (f *Forge) SuiteCompleted(ctx context.Context, repo string, suiteID int64) string {
	payload := f.event(func(w *world) object {
		r := w.mustRepo(repo)
		at := slices.IndexFunc(w.suites[repo], func(s Suite) bool { return s.ID == suiteID })
		if at < 0 {
			panic(fmt.Sprintf("simforge: no check suite %d was added to %s", suiteID, repo))
		}
		return object{
			"action":      "completed",
			"check_suite": w.suiteJSON(w.suites[repo][at]),
			"repository":  w.repoJSON(r),
			"sender":      w.userJSON("github-actions[bot]"),
		}
	})
	return f.Emit(ctx, "check_suite", "completed", payload)
}

// StatusChanged emits a status event, as a commit status webhook. Its branches are those of
// the repository's pull requests whose head is sha.
func (f *Forge) StatusChanged(ctx context.Context, repo, sha, statusContext, state string) string {
	payload := f.event(func(w *world) object {
		r := w.mustRepo(repo)
		branches := []any{}
		for _, p := range w.pulls[repo] {
			if p.HeadSHA == sha {
				branches = append(branches, object{"name": p.HeadRef})
			}
		}
		message, ok := w.commits[sha]
		if !ok {
			message = "Update"
		}
		return object{
			"sha": sha, "context": statusContext, "state": state,
			"branches":   branches,
			"commit":     object{"commit": object{"message": message}},
			"repository": w.repoJSON(r),
			"sender":     w.userJSON("github-actions[bot]"),
		}
	})
	return f.Emit(ctx, "status", "", payload)
}
