package app_test

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanactest"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
)

var _ = Describe("simulate", func() {
	var w *world
	BeforeEach(func() { w = newWorld() })

	It("goes through the production path, and a dry run applies nothing", func() {
		file := fixture(pull(1))
		text := execute(w.almanac(holding), "simulate", opened, file)
		Expect(text.code).To(Equal(0), text.stderr)
		Expect(text.stdout).To(MatchRegexp(`^simulate: queued\nran holding \(\S+\): recorded would-apply\n$`))

		out := execute(w.almanac(holding), "--format", "json", "simulate", opened, file, "--dry-run")
		Expect(out.code).To(Equal(0), out.stderr)
		result := decoded(out.stdout)
		Expect(result).To(HaveKeyWithValue("simulate", "queued"))
		Expect(result).To(HaveKeyWithValue("why", BeNil()))
		Expect(result["outcomes"]).To(ConsistOf(And(
			HaveKeyWithValue("result", "ran"),
			HaveKeyWithValue("workflow", "holding"),
			HaveKeyWithValue("run_id", Not(BeEmpty())),
			HaveKeyWithValue("recorded", []any{"would-apply"}),
		)))
		Expect(w.forge.Calls()).To(BeEmpty(), "a dry run holds nothing and applies nothing")
		Expect(w.harness.Calls()).To(Equal(2), "the agent still runs in a dry run")
	})

	It("applies effects when ALMANAC_MODE is live", func() {
		setenv("ALMANAC_MODE", "live")
		out := execute(w.almanac(holding), "--format", "json", "simulate", opened, fixture(pull(1)))
		Expect(out.code).To(Equal(0), out.stderr)
		Expect(decoded(out.stdout)["outcomes"]).To(ConsistOf(HaveKeyWithValue("recorded", []any{"applied"})))
		Expect(w.forge.Statuses()).To(Equal([]almanactest.Status{{On: held, State: effect.Success}}))
	})

	It("--dry-run wins over ALMANAC_MODE=live", func() {
		setenv("ALMANAC_MODE", "live")
		out := execute(w.almanac(holding), "--format", "json", "simulate", opened, fixture(pull(1)), "--dry-run")
		Expect(out.code).To(Equal(0), out.stderr)
		Expect(decoded(out.stdout)["outcomes"]).To(ConsistOf(HaveKeyWithValue("recorded", []any{"would-apply"})))
		Expect(w.forge.Calls()).To(BeEmpty())
	})

	DescribeTable("says why an event started nothing, and exits 0",
		func(kind string, body []byte, ingest, why string) {
			text := execute(w.almanac(holding), "simulate", kind, fixture(body))
			Expect(text.code).To(Equal(0), text.stderr)
			Expect(text.stdout).To(Equal("simulate: " + ingest + ": " + why + "\n"))

			out := execute(w.almanac(holding), "--format", "json", "simulate", kind, fixture(body))
			Expect(out.code).To(Equal(0), out.stderr)
			Expect(out.stdout).To(MatchJSON(`{"simulate": "` + ingest + `", "why": "` + why + `", "outcomes": []}`))
		},
		Entry("simulating a kind nothing handles says so", "no.such.kind", pull(1), "unrouted",
			"no workflow is triggered by `no.such.kind`; `synth` lists each workflow's trigger"),
		Entry("a file that does not parse as the kind", opened, []byte(`{"number": 1}`), "rejected",
			"the file does not parse as `"+opened+"`; check it has every field the event declares"),
		Entry("an event the workflow does not want", opened, pullOn("owner/repo", "feature", 1), "ignored",
			"the workflow's `when` filter turned this event away"),
	)

	It("refuses a file it cannot read, or that is not JSON", func() {
		out := execute(w.almanac(holding), "--format", "json", "simulate", opened, filepath.Join(GinkgoT().TempDir(), "none.json"))
		Expect(out.code).To(Equal(1))
		Expect(decoded(out.stderr)).To(HaveKeyWithValue("code", "almanac::input::unreadable"))

		out = execute(w.almanac(holding), "--format", "json", "simulate", opened, fixture([]byte("{nope")))
		Expect(out.code).To(Equal(1))
		Expect(decoded(out.stderr)).To(HaveKeyWithValue("code", "almanac::input::invalid_json"))
	})
})

var _ = Describe("backfill", func() {
	var w *world
	BeforeEach(func() { w = newWorld() })

	It("runs each workflow's scan once, then runs what it found", func() {
		text := execute(w.almanac(watching), "backfill")
		Expect(text.code).To(Equal(0), text.stderr)
		Expect(text.stdout).To(MatchRegexp(`^backfill: found 1, queued 1, unchanged 0, rejected 0\nran watching \(\S+\): recorded would-apply\n$`))

		out := execute(w.almanac(watching), "--format", "json", "backfill", "--dry-run")
		Expect(out.code).To(Equal(0), out.stderr)
		result := decoded(out.stdout)
		Expect(result["backfill"]).To(Equal(map[string]any{"found": 1.0, "queued": 1.0, "unchanged": 0.0, "rejected": 0.0, "skipped": false}))
		Expect(result).To(HaveKeyWithValue("held", 0.0))
		Expect(result["outcomes"]).To(ConsistOf(HaveKeyWithValue("result", "ran")))
	})

	It("a backfill of a repository it can see finds its pull requests", func() {
		w.fleet.repos = append(w.fleet.repos, hostile.MustParseSlug("owner/other"))
		out := execute(w.almanac(watching), "--format", "json", "backfill", "--repo", "owner/repo", "--dry-run")
		Expect(out.code).To(Equal(0), out.stderr)
		Expect(decoded(out.stdout)["backfill"]).To(HaveKeyWithValue("found", 1.0))
	})

	It("a backfill of a repository it cannot see fails, because finding nothing would read as all clear", func() {
		out := execute(w.almanac(watching), "--format", "json", "backfill", "--repo", "owner/unseen", "--dry-run")
		Expect(out.code).To(Equal(1))
		Expect(decoded(out.stderr)).To(HaveKeyWithValue("code", "almanac::scan::failed"))
		Expect(out.stderr).To(ContainSubstring("owner/unseen"))
	})

	It("refuses a repository that is not one", func() {
		out := execute(w.almanac(watching), "--format", "json", "backfill", "--repo", "not a slug")
		Expect(out.code).To(Equal(1))
		Expect(decoded(out.stderr)).To(And(HaveKeyWithValue("code", "almanac::usage"),
			HaveKeyWithValue("message", "--repo not a slug is not a repository, OWNER/NAME")))
	})

	It("a read-only pass writes nothing and calls no model", func() {
		setenv("ALMANAC_READ_ONLY", "1")
		setenv("ALMANAC_MODE", "live")
		out := execute(w.almanac(watching), "--format", "json", "backfill")
		Expect(out.code).To(Equal(0), out.stderr)
		result := decoded(out.stdout)
		Expect(result["backfill"]).To(HaveKeyWithValue("found", 1.0))
		Expect(result["outcomes"]).To(ConsistOf(And(HaveKeyWithValue("result", "failed"),
			HaveKeyWithValue("reason", ContainSubstring("ALMANAC_READ_ONLY")))))
		Expect(w.forge.Calls()).To(BeEmpty(), "nothing reached the forge")
		Expect(w.harness.Calls()).To(BeZero(), "no model was called")
	})
})
