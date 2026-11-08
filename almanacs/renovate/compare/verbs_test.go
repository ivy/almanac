package compare_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate/compare"
	"github.com/ivy/almanac/app"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/internal/pgtest"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/simforge"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/store/postgres"
)

// ran is one command line's exit status and output.
type ran struct {
	code           int
	stdout, stderr string
}

// execute runs the almanac-renovate command line with the comparison verbs, in an environment
// holding exactly vars, compare-effects reading the store open opens.
func execute(vars map[string]string, open compare.Opener, args ...string) ran {
	GinkgoT().Setenv("ALMANAC_CONFIG", filepath.Join(GinkgoT().TempDir(), "missing.toml"))
	env := func(name string) (string, bool) {
		if name == "ALMANAC_REGISTRY_URL" {
			// Nothing here may reach a real registry.
			return "http://127.0.0.1:1", true
		}
		value, ok := vars[name]
		return value, ok
	}
	verbs := []app.Verb{compare.StatesVerb(env), compare.EffectsVerbOn(env, open)}
	if open == nil {
		verbs = compare.Verbs(env)
	}
	almanac := app.Almanac{Name: "almanac-renovate", Verbs: verbs}
	var stdout, stderr bytes.Buffer
	code := almanac.Execute(context.Background(), args, &stdout, &stderr)
	return ran{code, stdout.String(), stderr.String()}
}

// code is the diagnostic code a command printed as JSON on stderr.
func code(r ran) string {
	GinkgoHelper()
	var refused map[string]any
	Expect(json.Unmarshal([]byte(r.stderr), &refused)).To(Succeed(), r.stderr)
	return refused["code"].(string)
}

// writeFile writes text to a file of the spec's own, and returns its path.
func writeFile(name, text string) string {
	GinkgoHelper()
	path := filepath.Join(GinkgoT().TempDir(), name)
	Expect(os.WriteFile(path, []byte(text), 0o600)).To(Succeed())
	return path
}

// rustSnapshot is the Rust report's JSON snapshot of the fleet the Go report specs build.
func rustSnapshot() string {
	GinkgoHelper()
	text, err := os.ReadFile("../tests/snapshots/fleet__a_report_covers_every_pull_request_and_is_stable.snap")
	Expect(err).NotTo(HaveOccurred())
	parts := strings.SplitN(string(text), "---\n", 3)
	Expect(parts).To(HaveLen(3))
	return parts[2]
}

// snapshotFleet is the forge behind that snapshot: a mergeable pull request in a repository
// that does not automerge, and a red one, one in conflict and one with no CI in another,
// beside a repository with no pull requests and an archived one.
func snapshotFleet() *simforge.Forge {
	sim := simforge.Start()
	DeferCleanup(sim.Close)
	alpha := simforge.PublicRepo("owner/alpha")
	alpha.Renovate = `{"automerge": false}`
	sim.AddRepo(alpha)
	sim.AddRepo(simforge.PublicRepo("owner/beta"))
	sim.AddRepo(simforge.PublicRepo("owner/quiet"))
	old := simforge.PublicRepo("owner/old")
	old.Archived = true
	sim.AddRepo(old)
	green := sim.AddPull("owner/alpha", simforge.RenovatePull("renovate/left-pad-2.x", "Update"))
	sim.AddSuite("owner/alpha", simforge.FinishedSuite(sim.Pull("owner/alpha", green).HeadSHA, "renovate/left-pad-2.x", "success", ""))
	red := sim.AddPull("owner/beta", simforge.RenovatePull("renovate/tar-7.x", "Update"))
	sim.AddSuite("owner/beta", simforge.FinishedSuite(sim.Pull("owner/beta", red).HeadSHA, "renovate/tar-7.x", "failure", "error: boom"))
	dirty := simforge.RenovatePull("renovate/ansi-5.x", "Update")
	dirty.MergeableState = "dirty"
	sim.AddPull("owner/beta", dirty)
	sim.AddPull("owner/beta", simforge.RenovatePull("renovate/no-ci-1.x", "Update"))
	return sim
}

// memoryOpener opens st in any scope, so a spec can read a store it ran the Go version on.
func memoryOpener(st store.Store) compare.Opener {
	return func(context.Context, app.Setting) (store.Store, func(), error) { return st, func() {}, nil }
}

var _ = Describe("The comparison verbs", func() {
	Describe("compare", func() {
		It("compares two report files, for people and as JSON", func() {
			rust, golang := filepath.Join("testdata", "rust.json"), filepath.Join("testdata", "go-differs.json")
			out := execute(nil, nil, "compare", "--rust", rust, "--go", golang)
			Expect(out.code).To(Equal(0), out.stderr)
			Expect(out.stdout).To(ContainSubstring("differ            owner/beta#1  differs in state, reason\n"))
			Expect(out.stdout).To(HaveSuffix("8 pull requests: 0 agree, 5 differ, 1 rust-in-progress, 1 only-rust, 1 only-go\n"))

			out = execute(nil, nil, "--format", "json", "compare", "--rust", rust, "--go", golang)
			Expect(out.code).To(Equal(0), out.stderr)
			var decoded compare.States
			Expect(json.Unmarshal([]byte(out.stdout), &decoded)).To(Succeed())
			Expect(decoded.Totals[compare.Differ]).To(Equal(5))
		})

		It("makes the Go report now without --go, and it agrees with the Rust report of the same fleet", func() {
			sim := snapshotFleet()
			out := execute(map[string]string{
				"GITHUB_TOKEN":   "t",
				"ALMANAC_OWNER":  "owner",
				"GITHUB_API_URL": sim.URL(),
			}, nil, "compare", "--rust", writeFile("rust.json", rustSnapshot()))
			Expect(out.code).To(Equal(0), out.stderr)
			Expect(out.stdout).To(HaveSuffix("3 repositories scanned by both; 4 pull requests: 4 agree, 0 differ, 0 rust-in-progress, 0 only-rust, 0 only-go\n"))
			Expect(sim.Writes()).To(BeEmpty(), "a comparison reads")
		})

		It("needs the Rust report, and a file that is one", func() {
			Expect(code(execute(nil, nil, "--format", "json", "compare"))).To(Equal("almanac::usage"))
			Expect(code(execute(nil, nil, "--format", "json", "compare", "--rust", "testdata/missing.json"))).To(Equal("almanac::input::unreadable"))
			Expect(code(execute(nil, nil, "--format", "json", "compare", "--rust", writeFile("r.json", `{"state": "fine"}`)))).To(Equal("almanac::input::not_a_report"))
		})

		It("needs GitHub to make the Go report", func() {
			out := execute(map[string]string{"ALMANAC_OWNER": "owner"}, nil, "--format", "json", "compare", "--rust", "testdata/rust.json")
			Expect(code(out)).To(Equal("almanac::config::env_missing"))
		})
	})

	Describe("compare-effects", func() {
		It("compares the Go dry run's store with GitHub, for people and as JSON", func() {
			w := newWorld()
			w.failingBuild(100 * time.Hour)
			w.goRuns()
			vars := map[string]string{"GITHUB_TOKEN": "t", "ALMANAC_OWNER": owner, "GITHUB_API_URL": w.sim.URL()}

			out := execute(vars, memoryOpener(w.store), "compare-effects")
			Expect(out.code).To(Equal(0), out.stderr)
			Expect(out.stdout).To(ContainSubstring("    go only    would start a repair on "))
			Expect(out.stdout).To(HaveSuffix("1 open Renovate pull requests, against every Go run: 0 agree, 1 disagree, 0 rust-in-progress, 0 not-compared\n"))

			out = execute(vars, memoryOpener(w.store), "--format", "json", "compare-effects", "--since", "1000000h")
			Expect(out.code).To(Equal(0), out.stderr)
			var decoded compare.Effects
			Expect(json.Unmarshal([]byte(out.stdout), &decoded)).To(Succeed())
			Expect(decoded.Since).NotTo(BeNil())
			Expect(decoded.Totals[compare.Disagreed]).To(Equal(1))
			Expect(w.sim.Writes()).To(BeEmpty())
		})

		It("refuses a window that looks forward, and needs the owner and GitHub", func() {
			st := memoryOpener(newWorld().store)
			Expect(code(execute(nil, st, "--format", "json", "compare-effects", "--since=-1h"))).To(Equal("almanac::usage"))
			Expect(code(execute(nil, st, "--format", "json", "compare-effects", "--since", "soon"))).To(Equal("almanac::usage"))
			Expect(code(execute(nil, st, "--format", "json", "compare-effects"))).To(Equal("almanac::config::env_missing"))
			Expect(execute(map[string]string{"ALMANAC_OWNER": owner}, st, "compare-effects").stderr).To(ContainSubstring("GITHUB_TOKEN"))
		})

		It("needs DATABASE_URL to find the Go dry run's store", func() {
			GinkgoT().Setenv("DATABASE_URL", "")
			out := execute(map[string]string{"GITHUB_TOKEN": "t", "ALMANAC_OWNER": owner}, nil, "compare-effects")
			Expect(out.code).To(Equal(1))
			Expect(out.stderr).To(ContainSubstring("DATABASE_URL"))
		})

		It("reads the dry-run scope of a Go almanac's database", Label("db"), func(ctx SpecContext) {
			url := pgtest.Database()
			pool, err := pgxpool.New(ctx, url)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(pool.Close)
			Expect(postgres.Migrate(ctx, pool)).To(Succeed())

			w := newWorld()
			w.failingBuild(100 * time.Hour)
			rt := runtime.New(w.workflows, w.Set,
				runtime.ReadOnly(),
				runtime.WithStore(postgres.New(pool, effect.DryRun.Scope("almanac-renovate"))),
				runtime.WithClock(w.Clock),
				runtime.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
			)
			_, err = rt.Backfill(ctx, nil)
			Expect(err).NotTo(HaveOccurred())
			_, err = rt.Drain(ctx)
			Expect(err).NotTo(HaveOccurred())
			w.rustRepair(1, repaired)

			GinkgoT().Setenv("DATABASE_URL", url)
			out := execute(map[string]string{
				"GITHUB_TOKEN": "t", "ALMANAC_OWNER": owner, "GITHUB_API_URL": w.sim.URL(),
			}, nil, "compare-effects")
			Expect(out.code).To(Equal(0), out.stderr)
			Expect(out.stdout).To(ContainSubstring("    both       go would start a repair on "))
			Expect(out.stdout).To(HaveSuffix("1 agree, 0 disagree, 0 rust-in-progress, 0 not-compared\n"))
		})
	})
})
