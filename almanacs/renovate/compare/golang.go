package compare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/wire"
	"github.com/ivy/almanac/workflow"
)

// goRun is one run of the Go version's dry run, and what it decided.
type goRun struct {
	ID       string `json:"run"`
	Workflow string `json:"workflow"`
	// Sha is the commit the run's event was about.
	Sha string `json:"sha"`
	// Outcome is how the run ended: "would act", "decided nothing", "stopped where an agent
	// would run", "stopped" or "did not finish".
	Outcome string `json:"outcome"`
	// Reason is why a run stopped or did not finish, from a replay of its log.
	Reason string `json:"reason,omitempty"`

	repo, branch string
	actions      []Action
}

// The outcomes of a Go run.
const (
	outcomeActs    = "would act"
	outcomeNothing = "decided nothing"
	outcomeAgent   = "stopped where an agent would run"
	outcomeStopped = "stopped"
	outcomeFailed  = "did not finish"
)

// The kinds of action, on either side.
const (
	// kindPass and kindFail are almanac/trust set to success or failure.
	kindPass = "pass"
	kindFail = "fail"
	// kindHold is almanac/trust set to pending.
	kindHold = "hold"
	// kindPush is a repair pushed onto a commit; on the Rust side, a commit carrying the
	// Almanac-Key trailer.
	kindPush = "push"
	// kindRepair is a read-only Go run stopping where the fixer would start a repair.
	kindRepair = "repair"
	// kindReview is a read-only Go run stopping where another agent, such as the reviewer,
	// would read the update.
	kindReview = "review"
	// kindAsk is a person asked about the pull request.
	kindAsk = "ask"
	// kindWait is a Go run waiting for something to change. It leaves nothing on GitHub.
	kindWait = "wait"
	// kindFile is an issue filed.
	kindFile = "file"
	// kindStatus is a status other than almanac/trust.
	kindStatus = "status"
)

// goDecisions reads what the Go version's dry run decided in every run started at or after
// since. A run with no effects is replayed from its log, to learn whether it stopped where
// an agent would run. The replay reads only what the run recorded and calls no agent, and the
// store refuses it every write.
func goDecisions(ctx context.Context, st store.Store, workflows []workflow.Registered, since time.Time) ([]goRun, error) {
	runs, err := st.RunsSince(ctx, since)
	if err != nil {
		return nil, err
	}
	byName := map[string]workflow.Registered{}
	for _, w := range workflows {
		byName[w.Name()] = w
	}
	replay := newReplayer(st, workflows)
	var out []goRun
	for _, run := range runs {
		w, ok := byName[run.Workflow]
		if !ok {
			continue
		}
		decided, ok := subjectOf(w, run)
		if !ok {
			continue
		}
		entries, err := st.Outbox(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			action, err := actionOf(entry)
			if err != nil {
				return nil, diag.Store(fmt.Errorf("effect %s of run %s does not read as an effect: %w", entry.Key, run.ID, err))
			}
			action.Run = run.ID
			decided.actions = append(decided.actions, action)
		}
		if len(decided.actions) > 0 {
			decided.Outcome = outcomeActs
		} else if err := replay.classify(ctx, &decided); err != nil {
			return nil, err
		}
		out = append(out, decided)
	}
	return out, nil
}

// subjectOf is a run with the repository, branch and commit its event names, read as the
// workflow reads it. A run of a workflow whose event names none is not one this comparison
// can place.
func subjectOf(w workflow.Registered, run store.Run) (goRun, bool) {
	decided := goRun{ID: run.ID, Workflow: run.Workflow}
	switch {
	case w.Handles(renovate.BuildEvent{}.Kind()):
		event, err := wire.Parse[renovate.BuildEvent](run.Payload)
		if err != nil {
			return goRun{}, false
		}
		decided.repo, decided.branch, decided.Sha = event.Repo.String(), event.Branch.String(), event.Sha.String()
	case w.Handles(renovate.SecurityEvent{}.Kind()):
		event, err := wire.Parse[renovate.SecurityEvent](run.Payload)
		if err != nil {
			return goRun{}, false
		}
		decided.repo, decided.branch, decided.Sha = event.Repo.String(), event.Branch.String(), event.Sha.String()
	default:
		return goRun{}, false
	}
	return decided, true
}

// actionOf is an effect a dry run recorded, as an action.
func actionOf(entry store.Entry) (Action, error) {
	var planned effect.Planned
	if err := json.Unmarshal(entry.Effect, &planned); err != nil {
		return Action{}, err
	}
	switch {
	case planned.Attest != nil:
		a := planned.Attest
		kind := map[effect.AttestState]string{effect.Success: kindPass, effect.Failure: kindFail, effect.Pending: kindHold}[a.State]
		if a.Context != renovate.TrustContext {
			return Action{Kind: kindStatus, Sha: a.Sha.String(), Detail: a.Context + " " + a.State.String()}, nil
		}
		return Action{Kind: kind, Sha: a.Sha.String()}, nil
	case planned.Push != nil:
		return Action{Kind: kindPush, Sha: planned.Push.Base.String()}, nil
	case planned.Ask != nil:
		return Action{Kind: kindAsk, Detail: planned.Ask.Question}, nil
	case planned.Await != nil:
		return Action{Kind: kindWait, Detail: planned.Await.Until}, nil
	case planned.File != nil:
		return Action{Kind: kindFile, Detail: planned.File.Title}, nil
	}
	return Action{}, errors.New("it holds no effect")
}

// replayer replays runs from their logs, with every agent standing in by a harness that
// notes it was reached and answers nothing.
type replayer struct {
	rt      *runtime.Runtime
	reached *agent.Info
}

func newReplayer(st store.Store, workflows []workflow.Registered) *replayer {
	r := &replayer{}
	set := providers.New()
	for _, w := range workflows {
		for _, info := range w.Agents() {
			set.UseHarnessFor(info.Name, reachedAgent{info: info, at: &r.reached})
		}
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	r.rt = runtime.New(workflows, set, runtime.WithStore(readOnlyStore{st}), runtime.WithLogger(quiet))
	return r
}

// classify replays a run that recorded no effects, and says how it ended.
func (r *replayer) classify(ctx context.Context, run *goRun) error {
	r.reached = nil
	outcome, err := r.rt.Replay(ctx, run.ID)
	if err != nil {
		return err
	}
	switch {
	case r.reached != nil:
		run.Outcome = outcomeAgent
		kind := kindReview
		if r.reached.Profile == "fixer" {
			kind = kindRepair
		}
		run.actions = append(run.actions, Action{Kind: kind, Sha: run.Sha, Run: run.ID, Detail: r.reached.Name})
	case outcome.Result == runtime.Ran:
		run.Outcome = outcomeNothing
	case outcome.Result == runtime.Stopped:
		run.Outcome, run.Reason = outcomeStopped, printable(outcome.Reason)
	default:
		run.Outcome, run.Reason = outcomeFailed, printable(outcome.Reason)
	}
	return nil
}

// errReached is what a replay's agent answers: the run's log ends before any agent ran.
var errReached = errors.New("an agent would run here")

// reachedAgent stands in for an agent in a replay. It notes that the replay reached it, and
// answers nothing.
type reachedAgent struct {
	info agent.Info
	at   **agent.Info
}

func (a reachedAgent) Run(context.Context, agent.Pass, string, agent.Grant, json.RawMessage) (json.RawMessage, error) {
	info := a.info
	*a.at = &info
	return nil, errReached
}

// readOnlyStore is a store that refuses every write, so a replay cannot change what the Go
// version recorded.
type readOnlyStore struct{ store.Store }

func refused() error { return diag.ReadOnly("writing to the Go almanac's store while comparing") }

func (readOnlyStore) Admit(context.Context, string, string, store.Admission) (store.Ingest, error) {
	return "", refused()
}

func (readOnlyStore) Claim(context.Context, store.ClaimRequest) (store.Claim, error) {
	return store.Claim{}, refused()
}

func (readOnlyStore) Remember(context.Context, string, string, store.Trace) error { return refused() }

func (readOnlyStore) Note(context.Context, string, json.RawMessage, effect.Mode) error {
	return refused()
}

func (readOnlyStore) Release(context.Context, string, []workflow.Subject, []json.RawMessage, effect.Mode) error {
	return refused()
}

func (readOnlyStore) Delivered(context.Context, string) error { return refused() }

func (readOnlyStore) DeliveryFailed(context.Context, string, string, int) error { return refused() }

func (readOnlyStore) Recover(context.Context) (int, error) { return 0, refused() }

func (readOnlyStore) Record(context.Context, string, string, int, json.RawMessage) error {
	return refused()
}
