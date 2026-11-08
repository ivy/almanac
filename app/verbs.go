package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/ivy/almanac/config"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/store/postgres"
)

// verbs runs the verbs every almanac has, for one invocation.
type verbs struct {
	a      *Almanac
	out    *output
	dryRun bool
	repo   string
}

// synth prints each workflow's declaration. It needs nothing set up, and reads no config.
func (v *verbs) synth(*cobra.Command, []string) error {
	workflows, err := v.a.workflows()
	if err != nil {
		return err
	}
	manifests := make([]any, len(workflows))
	for i, w := range workflows {
		manifests[i] = w.Manifest()
	}
	pretty, err := json.MarshalIndent(manifests, "", "  ")
	if err != nil {
		return err
	}
	return v.out.say(string(pretty), manifests)
}

// explain prints the long form of an error. It needs nothing set up.
func (v *verbs) explain(_ *cobra.Command, args []string) error {
	page, err := diag.Explain(args[0])
	if err != nil {
		return err
	}
	return v.out.say(strings.TrimSuffix(page, "\n"), map[string]string{"code": args[0], "page": page})
}

// own runs one of the almanac's own verbs.
func (v *verbs) own(verb Verb) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		setting := Setting{Name: v.a.Name, Config: cfg, Switches: runtime.SwitchesFrom(false, os.LookupEnv)}
		if verb.Flags != nil {
			setting.Flags = cmd.Flags()
		}
		said, err := verb.Run(cmd.Context(), setting, args)
		if err != nil {
			return err
		}
		return v.out.say(said.Human, said.JSON)
	}
}

// OpenStore opens the almanac's store in the database DATABASE_URL names, in mode's scope, for
// a verb of the almanac's own that reads what runs recorded, such as a comparison reading a dry
// run. It does not bring the schema up to date, so it never changes the database. Call done
// once the verb is done with the store.
func (s Setting) OpenStore(ctx context.Context, mode effect.Mode) (st store.Store, done func(), err error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return nil, nil, diag.MissingEnv("DATABASE_URL", "the database the almanac keeps its runs in")
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, nil, diag.Store(err)
	}
	return postgres.New(pool, mode.Scope(s.Name)), pool.Close, nil
}

// opened is a runtime set up for one invocation, and the database it uses, if any.
type opened struct {
	rt   *runtime.Runtime
	pool *pgxpool.Pool
}

func (o opened) close() {
	if o.pool != nil {
		o.pool.Close()
	}
}

// open loads config, sets up providers, and makes a runtime that passes Validate. Its state
// is in the database DATABASE_URL names, or in memory without one. Nothing connects until
// Validate has passed.
func (v *verbs) open(ctx context.Context, logger *slog.Logger) (opened, error) {
	cfg, err := config.Load()
	if err != nil {
		return opened{}, err
	}
	switches := runtime.SwitchesFrom(v.dryRun, os.LookupEnv)
	workflows, err := v.a.workflows()
	if err != nil {
		return opened{}, err
	}
	set, err := v.a.providers(ctx, Setting{Name: v.a.Name, Config: cfg, Switches: switches})
	if err != nil {
		return opened{}, err
	}
	options := []runtime.Option{
		runtime.Switched(switches),
		runtime.Tightened(cfg.Tighten(v.a.limits())),
		runtime.WithLogger(logger),
	}
	var pool *pgxpool.Pool
	if url := os.Getenv("DATABASE_URL"); url != "" {
		// A pool connects when first used, so nothing reaches the database before Validate.
		pool, err = pgxpool.New(ctx, url)
		if err != nil {
			return opened{}, diag.Store(err)
		}
		options = append(options, runtime.WithStore(postgres.New(pool, switches.Mode.Scope(v.a.Name))))
	}
	o := opened{rt: runtime.New(workflows, set, options...), pool: pool}
	if err := o.rt.Validate(); err != nil {
		o.close()
		return opened{}, err
	}
	if pool != nil {
		if err := postgres.Migrate(ctx, pool); err != nil {
			o.close()
			return opened{}, err
		}
	}
	return o, nil
}

// quiet is the logger of a verb that prints its own report: the runtime's log would only
// repeat it, and stderr is kept for errors.
func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// check confirms the providers are set up, and the database answers if one is named.
func (v *verbs) check(cmd *cobra.Command, _ []string) error {
	o, err := v.open(cmd.Context(), quiet())
	if err != nil {
		return err
	}
	defer o.close()
	workflows := len(o.rt.Manifest())
	mode := modeOf(o.rt)
	limits := limitsOf(o.rt.Limits())
	human := fmt.Sprintf("%s: ok, %d workflow(s), %s, limits: %s", v.a.Name, workflows, mode, limits)
	return v.out.say(human, map[string]any{
		"almanac": v.a.Name, "ok": true, "workflows": workflows,
		"mode": o.rt.Mode(), "read_only": o.rt.IsReadOnly(), "limits": limits,
	})
}

// backfill runs every workflow's scan once, then runs what it found.
func (v *verbs) backfill(cmd *cobra.Command, args []string) error {
	var repo *hostile.Slug
	if v.repo != "" {
		slug, err := hostile.ParseSlug(v.repo)
		if err != nil {
			e := diag.NotACommand("--repo "+v.repo, v.a.usage())
			e.Message = fmt.Sprintf("--repo %s is not a repository, OWNER/NAME", v.repo)
			return e
		}
		repo = &slug
	}
	ctx := cmd.Context()
	o, err := v.open(ctx, quiet())
	if err != nil {
		return err
	}
	defer o.close()
	report, err := o.rt.Backfill(ctx, repo)
	if err != nil {
		return err
	}
	settled, err := settle(ctx, o.rt)
	if err != nil {
		return err
	}
	lines := []string{fmt.Sprintf("backfill: found %d, queued %d, unchanged %d, rejected %d",
		report.Found, report.Queued, report.Unchanged, report.Rejected)}
	if report.Skipped {
		lines[0] = "backfill: skipped, because another scan is running"
	}
	lines = append(lines, settled.lines()...)
	if settled.held > 0 {
		lines = append(lines, fmt.Sprintf("%d event(s) held by a run budget, resuming as the hourly window opens", settled.held))
	}
	return v.out.say(strings.Join(lines, "\n"), map[string]any{
		"backfill": report, "held": settled.held, "outcomes": settled.outcomes,
	})
}

// simulate sends a file through the production path as an event of a kind.
func (v *verbs) simulate(cmd *cobra.Command, args []string) error {
	kind, file := args[0], args[1]
	text, err := os.ReadFile(file)
	if err != nil {
		return diag.UnreadableInput(file, err)
	}
	var payload json.RawMessage
	if err := json.Unmarshal(text, &payload); err != nil {
		return diag.InvalidJSON(file, err)
	}
	ctx := cmd.Context()
	o, err := v.open(ctx, quiet())
	if err != nil {
		return err
	}
	defer o.close()
	ingest, err := o.rt.Ingest(ctx, runtime.Envelope{
		Source:     "simulate",
		DeliveryID: fmt.Sprintf("%s:%d", file, len(text)),
		Kind:       kind,
		Origin:     store.Simulated,
		Payload:    payload,
	})
	if err != nil {
		return err
	}
	settled, err := settle(ctx, o.rt)
	if err != nil {
		return err
	}
	why, nothing := whyNothingRan(ingest, kind)
	human := strings.Join(append([]string{"simulate: " + string(ingest)}, settled.lines()...), "\n")
	result := map[string]any{"simulate": ingest, "why": nil, "outcomes": settled.outcomes}
	if nothing {
		human = fmt.Sprintf("simulate: %s: %s", ingest, why)
		result["why"] = why
	}
	return v.out.say(human, result)
}

// whyNothingRan says why a simulated event started nothing, for what a mistake usually causes.
func whyNothingRan(ingest store.Ingest, kind string) (string, bool) {
	switch ingest {
	case store.Unrouted:
		return fmt.Sprintf("no workflow is triggered by `%s`; `synth` lists each workflow's trigger", kind), true
	case store.Rejected:
		return fmt.Sprintf("the file does not parse as `%s`; check it has every field the event declares", kind), true
	case store.Ignored:
		return "the workflow's `when` filter turned this event away", true
	case store.ChainStopped:
		return "recent runs on this key reached the chain limit; it clears as they age past two hours", true
	case store.Echo:
		return "the event was sent by one of this almanac's own logins, so it is an echo of its own work", true
	case store.Duplicate:
		return "this file was simulated before with the same length, and a delivery is taken once", true
	default:
		return "", false
	}
}

// run runs the almanac as a daemon until ctx ends.
func (v *verbs) run(cmd *cobra.Command, _ []string) error {
	return v.a.daemon(cmd.Context(), v.out.stderr, v.dryRun)
}

// modeOf says how far a runtime reaches, as a person reads it.
func modeOf(rt *runtime.Runtime) string {
	if rt.IsReadOnly() {
		return "read-only " + rt.Mode().String()
	}
	return rt.Mode().String()
}

// shownLimits are limits as check prints them.
type shownLimits struct {
	MaxChainDepth         int  `json:"max_chain_depth"`
	RunsPerHourCap        *int `json:"runs_per_hour_cap"`
	OverallRunsPerHourCap *int `json:"overall_runs_per_hour_cap"`
	Concurrency           int  `json:"concurrency"`
	ScanIntervalMinutes   int  `json:"scan_interval_minutes"`
}

func limitsOf(l config.Limits) shownLimits {
	capOf := func(c config.Cap) *int {
		if n, ok := c.Value(); ok {
			return &n
		}
		return nil
	}
	return shownLimits{
		MaxChainDepth:         l.MaxChainDepth,
		RunsPerHourCap:        capOf(l.RunsPerHour),
		OverallRunsPerHourCap: capOf(l.OverallRunsPerHour),
		Concurrency:           l.Concurrency,
		ScanIntervalMinutes:   int(l.ScanInterval.Minutes()),
	}
}

func (l shownLimits) String() string {
	capText := func(n *int) string {
		if n == nil {
			return "none"
		}
		return fmt.Sprint(*n)
	}
	return fmt.Sprintf("max_chain_depth=%d runs_per_hour_cap=%s overall_runs_per_hour_cap=%s concurrency=%d scan_interval_minutes=%d",
		l.MaxChainDepth, capText(l.RunsPerHourCap), capText(l.OverallRunsPerHourCap), l.Concurrency, l.ScanIntervalMinutes)
}
