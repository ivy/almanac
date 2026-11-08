// Package runtime routes events to workflows, runs them, and applies what they decide, within
// limits. What it must remember across a restart is in a store.Store.
//
// An event is admitted once per delivery, correlated by its workflow's key, and queued: one run
// is active per key, and events during a run cause at most one rerun. Chains of Almanac's own
// effects are cut at a depth, budgets hold work over real time windows, and live events are
// claimed before scanned ones. A run's reads go through its [workflow.Cx], which records them,
// so a replay reaches the same decision without reading or spending again. What a run decides
// goes to an outbox, and only a live runtime applies it, once per effect.
//
// A runtime is a dry run unless it is made live ([InMode]), and a read-only one ([ReadOnly])
// is always a dry run and refuses every write and model call besides.
package runtime

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ivy/almanac/clock"
	"github.com/ivy/almanac/config"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/store/memory"
	"github.com/ivy/almanac/workflow"
)

const (
	// LedgerWindow is how long an effect can explain a later event on its key, such as a push
	// and the CI after it.
	LedgerWindow = 2 * time.Hour
	// BudgetWindow is what a workflow's runs per hour, and every budget, are measured over.
	BudgetWindow = time.Hour
	// MaxDeliveryAttempts is how many times an effect is tried before it is marked failed.
	MaxDeliveryAttempts = 5
)

// Runtime runs one almanac's workflows. Its methods are safe to call concurrently, except
// Recover, which only a starting daemon may call.
type Runtime struct {
	workflows []workflow.Registered
	// providers is what runs and scans reach: the binary's set, with every write and model
	// call refused when the runtime is read-only.
	providers *providers.Set
	guard     Guard
	limits    config.Limits
	mode      effect.Mode
	readOnly  bool
	store     store.Store
	clock     clock.Clock
	log       *slog.Logger

	// stopped is set on shutdown. An in-flight run finishes, and no new one starts.
	stopped atomic.Bool
	// scanning is held while scanning, so two scans cannot overlap.
	scanning sync.Mutex
	lastScan struct {
		sync.Mutex
		at time.Time
	}
}

// Option changes how a runtime is set up.
type Option func(*Runtime)

// WithStore keeps the runtime's state in s. Without it, state is held in memory.
func WithStore(s store.Store) Option { return func(r *Runtime) { r.store = s } }

// WithClock tells the runtime the time with c. Without it, the runtime uses the host's clock.
func WithClock(c clock.Clock) Option { return func(r *Runtime) { r.clock = c } }

// WithLogger logs each finished run and failed sweep to l. Without it, slog.Default.
func WithLogger(l *slog.Logger) Option { return func(r *Runtime) { r.log = l } }

// InMode sets whether effects apply. A runtime is a dry run unless this says Live, and a
// read-only runtime is a dry run whatever this says.
func InMode(m effect.Mode) Option { return func(r *Runtime) { r.mode = m } }

// ReadOnly makes the runtime a dry run that refuses every write and every model call: a
// Forge, Tracker or Sweep it was given refuses each call, as do a Classifier and the harness of
// every agent. A run that reaches one fails there, recording how far it got.
func ReadOnly() Option { return func(r *Runtime) { r.readOnly = true } }

// Switched sets the mode and read-only switch together, as [SwitchesFrom] reads them.
func Switched(s Switches) Option {
	return func(r *Runtime) {
		r.mode = s.Mode
		r.readOnly = r.readOnly || s.ReadOnly
	}
}

// Tightened lowers the runtime's limits to l wherever l is tighter. It can never widen one,
// however often it is applied.
func Tightened(l config.Limits) Option {
	return func(r *Runtime) { r.limits = r.limits.Tighten(l) }
}

// Guarded has g review each run's effects before they are recorded.
func Guarded(g Guard) Option { return func(r *Runtime) { r.guard = g } }

// New is a runtime for workflows, reaching what set provides. Its own logins and where a
// tripped budget asks a person come from set too (providers.Set.ActAs and AlertTo).
func New(workflows []workflow.Registered, set *providers.Set, options ...Option) *Runtime {
	r := &Runtime{
		workflows: workflows,
		guard:     allowDeclared{},
		limits:    config.DefaultLimits(),
		store:     memory.New(),
		clock:     clock.System{},
		log:       slog.Default(),
	}
	for _, option := range options {
		option(r)
	}
	r.providers = set
	if r.readOnly {
		r.mode = effect.DryRun
		r.providers = refusingWrites(set, workflows)
	}
	return r
}

// Mode is whether effects apply. A read-only runtime is always a dry run.
func (r *Runtime) Mode() effect.Mode { return r.mode }

// IsReadOnly reports whether every write and model call is refused.
func (r *Runtime) IsReadOnly() bool { return r.readOnly }

// Limits are the limits the runtime runs within.
func (r *Runtime) Limits() config.Limits { return r.limits }

// Validate checks that the providers cover everything the workflows declared they need, and in
// live mode a Forge and Tracker for the effects they may produce. Call it when a binary starts,
// before anything connects, so a missing provider fails at once with a diagnostic (R-40).
func (r *Runtime) Validate() error {
	return workflow.Validate(r.workflows, r.providers, r.mode)
}

// Manifest is each workflow's declaration, in order. It needs nothing set up.
func (r *Runtime) Manifest() []workflow.Manifest {
	manifests := make([]workflow.Manifest, len(r.workflows))
	for i, w := range r.workflows {
		manifests[i] = w.Manifest()
	}
	return manifests
}

// StopClaiming starts no more runs. A run under way finishes, and queued work stays queued for
// the next start.
func (r *Runtime) StopClaiming() { r.stopped.Store(true) }

// Ping reports whether the store answers.
func (r *Runtime) Ping(ctx context.Context) error {
	_, err := r.store.Undelivered(ctx)
	return err
}

// Recover requeues every run marked running, and returns how many. Only a daemon starting up
// may call it: a command beside a live daemon would requeue the runs it has under way, and they
// would run twice.
func (r *Runtime) Recover(ctx context.Context) (int, error) { return r.store.Recover(ctx) }

// RunsFor is the runs linked to a subject, oldest first.
func (r *Runtime) RunsFor(ctx context.Context, subject workflow.Subject) ([]string, error) {
	return r.store.RunsFor(ctx, subject)
}

// EffectsOf is what a run recorded in the outbox, including what a dry run would have done.
func (r *Runtime) EffectsOf(ctx context.Context, runID string) ([]store.Entry, error) {
	return r.store.Outbox(ctx, runID)
}

func (r *Runtime) workflow(name string) (workflow.Registered, bool) {
	for _, w := range r.workflows {
		if w.Name() == name {
			return w, true
		}
	}
	return nil, false
}
