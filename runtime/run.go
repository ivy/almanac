package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/workflow"
)

// Result is how a run ended, or that a budget held one back.
type Result int

const (
	// Ran is a run that decided. Its allowed effects are recorded.
	Ran Result = iota + 1
	// Stopped is a run that decided to stop, which is not a failure.
	Stopped
	// Failed is a run that could not finish.
	Failed
	// BudgetTripped is a budget reached with work waiting. It is reported once per window.
	BudgetTripped
)

var resultNames = map[Result]string{Ran: "ran", Stopped: "stopped", Failed: "failed", BudgetTripped: "budget-tripped"}

func (r Result) String() string {
	if name, ok := resultNames[r]; ok {
		return name
	}
	return fmt.Sprintf("Result(%d)", int(r))
}

// MarshalText writes the result's name, such as "ran".
func (r Result) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// Outcome is what came of one claim: a run, or a budget that held runs back.
type Outcome struct {
	Result Result `json:"result"`
	RunID  string `json:"run_id,omitempty"`
	// Workflow is the run's workflow, or the one whose budget tripped. It is empty for the
	// overall budget.
	Workflow string `json:"workflow,omitempty"`
	// Effects are what a run that Ran decided and was allowed to do.
	Effects []effect.Effect `json:"-"`
	// Denied are what a run decided and was not allowed to do: effects its workflow did not
	// declare, and effects the guard refused.
	Denied []effect.Summary `json:"denied,omitempty"`
	// Reason is why a run Stopped, or the error it Failed with.
	Reason string `json:"reason,omitempty"`
	// Held is how many keys a tripped budget holds.
	Held int `json:"held,omitempty"`
}

// Guard reviews a run's effects knowing only the workflow's intent and summaries that hold no
// text. Its signature is the defense: hostile text cannot reach it. It answers whether each
// action may go ahead, in order; a missing answer is a refusal.
type Guard interface {
	Review(ctx context.Context, intent string, actions []effect.Summary) ([]bool, error)
}

// allowDeclared allows every effect a workflow declared.
type allowDeclared struct{}

func (allowDeclared) Review(_ context.Context, _ string, actions []effect.Summary) ([]bool, error) {
	verdicts := make([]bool, len(actions))
	for i := range verdicts {
		verdicts[i] = true
	}
	return verdicts, nil
}

// Drain runs claimable work until none is left, running independent keys concurrently, up to
// the concurrency limit. It returns every outcome, in no particular order.
func (r *Runtime) Drain(ctx context.Context) ([]Outcome, error) {
	var (
		mu       sync.Mutex
		outcomes []Outcome
		errs     = make([]error, r.limits.Concurrency)
		wg       sync.WaitGroup
	)
	budgets := r.budgets()
	for i := range r.limits.Concurrency {
		wg.Go(func() {
			errs[i] = r.work(ctx, budgets, func(o Outcome) {
				mu.Lock()
				defer mu.Unlock()
				outcomes = append(outcomes, o)
			})
		})
	}
	wg.Wait()
	return outcomes, errors.Join(errs...)
}

// Settle runs and applies until nothing is left. A key waits for its last run's effects, so a
// rerun that was held back starts as soon as they land.
func (r *Runtime) Settle(ctx context.Context) ([]Outcome, error) {
	var all []Outcome
	for {
		outcomes, err := r.Drain(ctx)
		all = append(all, outcomes...)
		if err != nil {
			return all, err
		}
		delivered, err := r.Deliver(ctx)
		if err != nil {
			return all, err
		}
		applied := slices.ContainsFunc(delivered, func(d Delivery) bool { return d.Err == nil })
		if len(outcomes) == 0 && !applied {
			return all, nil
		}
	}
}

// budgets are each workflow's own runs per hour, lowered by config. Every workflow is listed,
// because a store claims only for listed workflows.
func (r *Runtime) budgets() []store.Budget {
	budgets := make([]store.Budget, len(r.workflows))
	for i, w := range r.workflows {
		limit := w.RunsPerHour()
		if ceiling, ok := r.limits.RunsPerHour.Value(); ok {
			limit = min(limit, ceiling)
		}
		budgets[i] = store.Budget{Workflow: w.Name(), Limit: limit}
	}
	return budgets
}

// work claims and runs until nothing is claimable, the runtime stops claiming, or ctx ends.
func (r *Runtime) work(ctx context.Context, budgets []store.Budget, report func(Outcome)) error {
	for !r.stopped.Load() {
		if err := ctx.Err(); err != nil {
			return err
		}
		claim, err := r.store.Claim(ctx, store.ClaimRequest{
			Budgets: budgets,
			Overall: r.limits.OverallRunsPerHour,
			Window:  BudgetWindow,
			Now:     r.clock.Now(),
		})
		switch {
		case err != nil:
			return err
		case claim.Tripped != nil:
			if err := r.askAboutBudget(ctx, *claim.Tripped); err != nil {
				return err
			}
			report(Outcome{Result: BudgetTripped, Workflow: claim.Tripped.Workflow, Held: claim.Tripped.Held})
		case claim.Run != nil:
			outcome, err := r.finish(ctx, *claim.Run)
			if err != nil {
				return err
			}
			report(outcome)
		default:
			return nil
		}
	}
	return nil
}

// finish runs a claimed run and releases it with what it decided. A run cut short because ctx
// ended is left running, so recovery requeues it on the next start.
func (r *Runtime) finish(ctx context.Context, run store.Run) (Outcome, error) {
	w, ok := r.workflow(run.Workflow)
	if !ok {
		return Outcome{}, diag.Store(fmt.Errorf("no workflow named %q in this binary", run.Workflow))
	}
	d := r.execute(ctx, w, run)
	if d.outcome.Result == Failed && ctx.Err() != nil {
		return Outcome{}, ctx.Err()
	}
	if err := r.store.Release(ctx, run.ID, d.links, d.planned, r.mode); err != nil {
		return Outcome{}, err
	}
	if err := r.remember(ctx, run, d.outcome); err != nil {
		return Outcome{}, err
	}
	r.logFinished(d.outcome)
	return d.outcome, nil
}

// decision is what one execution of a run came to.
type decision struct {
	outcome Outcome
	links   []workflow.Subject
	// planned are the allowed effects as the outbox stores them.
	planned []json.RawMessage
}

// execute runs w on a run, then checks what it decided: effects the workflow did not declare
// are denied, every declared one must plan, and the guard, whose verdict is recorded, reviews
// what is left.
func (r *Runtime) execute(ctx context.Context, w workflow.Registered, run store.Run) decision {
	cx := r.newCx(run, w)
	ended := func(result Result, reason string) decision {
		return decision{outcome: Outcome{Result: result, RunID: run.ID, Workflow: w.Name(), Reason: reason}}
	}
	decided, err := w.Execute(ctx, cx, run.Payload)
	var halt *workflow.Halt
	switch {
	case errors.As(err, &halt):
		return ended(Stopped, halt.Reason)
	case err != nil:
		return ended(Failed, err.Error())
	}

	may := w.May()
	var declared []effect.Effect
	var denied []effect.Summary
	for _, e := range decided.Effects {
		if slices.Contains(may, e.Kind()) {
			declared = append(declared, e)
		} else {
			denied = append(denied, effect.Summarize(e))
		}
	}
	plans := make([]effect.Planned, len(declared))
	summaries := make([]effect.Summary, len(declared))
	for i, e := range declared {
		if plans[i], err = effect.Plan(e); err != nil {
			return ended(Failed, err.Error())
		}
		summaries[i] = effect.Summarize(e)
	}
	verdicts, err := r.review(ctx, cx, w.Intent(), summaries)
	if err != nil {
		return ended(Failed, err.Error())
	}

	d := decision{outcome: Outcome{Result: Ran, RunID: run.ID, Workflow: w.Name()}, links: decided.Links}
	for i, e := range declared {
		if i >= len(verdicts) || !verdicts[i] {
			denied = append(denied, summaries[i])
			continue
		}
		planned, err := json.Marshal(plans[i])
		if err != nil {
			return ended(Failed, err.Error())
		}
		d.outcome.Effects = append(d.outcome.Effects, e)
		d.planned = append(d.planned, planned)
	}
	d.outcome.Denied = denied
	return d
}

// review asks the guard about a run's effects once, recorded under "guard", so a replay does
// not ask again.
func (r *Runtime) review(ctx context.Context, cx *runCx, intent string, actions []effect.Summary) ([]bool, error) {
	recorded, err := cx.Record(ctx, "guard", func(ctx context.Context) (json.RawMessage, error) {
		verdicts, err := r.guard.Review(ctx, intent, actions)
		if err != nil {
			return nil, fmt.Errorf("the guard could not review this run's effects: %w", err)
		}
		return json.Marshal(verdicts)
	})
	if err != nil {
		return nil, err
	}
	var verdicts []bool
	if err := json.Unmarshal(recorded, &verdicts); err != nil {
		return nil, fmt.Errorf("the guard's verdict was recorded as something other than a list of booleans: %w", err)
	}
	return verdicts, nil
}

// remember notes what a live run did to its key, so a later event on it inherits its depth and
// a later run sees its attempts. A dry run changes nothing, so it causes nothing and leaves no
// trace.
func (r *Runtime) remember(ctx context.Context, run store.Run, outcome Outcome) error {
	if r.mode != effect.Live || outcome.Result != Ran {
		return nil
	}
	for _, e := range outcome.Effects {
		if e.Kind() == effect.KindAwait {
			continue
		}
		trace := store.Trace{Kind: e.Kind(), Depth: run.Depth, At: r.clock.Now()}
		if err := r.store.Remember(ctx, run.Workflow, run.Key, trace); err != nil {
			return err
		}
	}
	return nil
}

// askAboutBudget asks a person once a budget trips, where the providers say to. It goes
// through the outbox like any effect, so it is delivered and retried the same way, and only
// when live.
func (r *Runtime) askAboutBudget(ctx context.Context, tripped store.Tripped) error {
	target, ok := r.providers.Alerts()
	if !ok {
		return nil
	}
	scope := tripped.Workflow
	if scope == "" {
		scope = "all workflows"
	}
	ask, err := json.Marshal(effect.Planned{Ask: &effect.Question{
		Question: "A run budget was reached for " + scope,
		Target:   target,
		Body:     hostile.New(fmt.Sprintf("%d event(s) are held and will resume as the hourly window opens.", tripped.Held)),
	}})
	if err != nil {
		return err
	}
	key := fmt.Sprintf("budget:%s:%d", scope, r.clock.Now().Unix())
	return r.store.Note(ctx, key, ask, r.mode)
}

// Replay runs a run again from its log. Recorded reads, model calls and the guard's verdict are
// reused, and the workflow decides again from them. Nothing is released, remembered or applied.
func (r *Runtime) Replay(ctx context.Context, runID string) (Outcome, error) {
	run, ok, err := r.store.Run(ctx, runID)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		return Outcome{}, diag.Store(fmt.Errorf("no run %q in this almanac's store: check the ID, and that the store is the one the run was made in, in the same mode", runID))
	}
	w, ok := r.workflow(run.Workflow)
	if !ok {
		return Outcome{}, diag.Store(fmt.Errorf("run %q is of workflow %q, which is not in this binary", runID, run.Workflow))
	}
	return r.execute(ctx, w, run).outcome, nil
}

// logFinished logs each run, so an operator can follow a daemon without reading the store.
func (r *Runtime) logFinished(o Outcome) {
	switch o.Result {
	case Ran:
		r.log.Info("run finished", "run", o.RunID, "workflow", o.Workflow, "effects", len(o.Effects), "denied", len(o.Denied))
	case Stopped:
		r.log.Info("run stopped", "run", o.RunID, "workflow", o.Workflow, "reason", o.Reason)
	case Failed:
		r.log.Warn("run failed", "run", o.RunID, "workflow", o.Workflow, "error", o.Reason)
	}
}
