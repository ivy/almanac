package renovate_test

import (
	"context"
	"encoding/json"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/hostile"
)

func raw(text string) hostile.Of[json.RawMessage] { return hostile.New(json.RawMessage(text)) }

func fixture(name string) hostile.Of[json.RawMessage] {
	content, err := os.ReadFile("fixtures/" + name)
	Expect(err).NotTo(HaveOccurred())
	return hostile.New(json.RawMessage(content))
}

func checksOf(suites, statuses hostile.Of[json.RawMessage]) renovate.Checks {
	checks, err := renovate.ChecksOf(suites, statuses)
	Expect(err).NotTo(HaveOccurred())
	return checks
}

func trustOf(statuses string) renovate.Trust {
	trust, err := renovate.TrustOf(raw(statuses))
	Expect(err).NotTo(HaveOccurred())
	return trust
}

var noSuites = raw(`{"total_count": 0, "check_suites": []}`)

var _ = Describe("Where the checks on a commit stand", func() {
	It("a real commit's suites read as green despite apps that never ran", func() {
		Expect(checksOf(fixture("check-suites-with-ghosts.json"), raw(`{"statuses": []}`))).To(Equal(renovate.ChecksGreen))
	})

	It("a cooldown or Almanac's own word is not a check", func() {
		statuses := raw(`{"statuses": [
			{"context": "renovate/stability-days", "state": "success"},
			{"context": "almanac/trust", "state": "success"}
		]}`)
		Expect(checksOf(noSuites, statuses)).To(Equal(renovate.ChecksNone))
	})

	DescribeTable("combines suites and statuses, a failure first and then anything still running",
		func(suites, statuses string, want renovate.Checks) {
			Expect(checksOf(raw(suites), raw(statuses))).To(Equal(want))
		},
		Entry("a failed suite",
			`{"check_suites": [{"status": "completed", "conclusion": "startup_failure"}]}`, `{"statuses": []}`, renovate.ChecksRed),
		Entry("an errored status, while a suite runs",
			`{"check_suites": [{"status": "in_progress", "conclusion": null}]}`, `{"statuses": [{"context": "ci", "state": "error"}]}`, renovate.ChecksRed),
		Entry("a suite still running",
			`{"check_suites": [{"status": "queued", "conclusion": null, "latest_check_runs_count": 1}]}`, `{"statuses": []}`, renovate.ChecksPending),
		Entry("a pending status",
			`{"check_suites": []}`, `{"statuses": [{"context": "ci", "state": "pending"}]}`, renovate.ChecksPending),
		Entry("a status with no context, which tests something",
			`{"check_suites": []}`, `{"statuses": [{"state": "success"}]}`, renovate.ChecksGreen),
		Entry("Almanac's own failure, which is not a check",
			`{"check_suites": []}`, `{"statuses": [{"context": "almanac/trust", "state": "failure"}]}`, renovate.ChecksNone),
		Entry("a conclusion GitHub adds later, which is not a failure",
			`{"check_suites": [{"status": "completed", "conclusion": "something_new"}]}`, `{"statuses": []}`, renovate.ChecksGreen),
	)

	It("refuses an answer that is not a list of suites, instead of reading it as no checks", func() {
		_, err := renovate.ChecksOf(raw(`{"check_suites": "nothing to see"}`), raw(`{"statuses": []}`))
		Expect(err).To(MatchError(ContainSubstring("check suites")))
	})
})

var _ = Describe("What Almanac last said about a commit", func() {
	DescribeTable("is read from its own status",
		func(statuses string, want renovate.Trust) {
			Expect(trustOf(statuses)).To(Equal(want))
		},
		Entry("nothing", `{"statuses": [{"context": "ci", "state": "success"}]}`, renovate.TrustAbsent),
		Entry("a hold", `{"statuses": [{"context": "almanac/trust", "state": "pending"}]}`, renovate.TrustPending),
		Entry("a pass", `{"statuses": [{"context": "almanac/trust", "state": "success"}]}`, renovate.TrustSuccess),
		Entry("a failure", `{"statuses": [{"context": "almanac/trust", "state": "failure"}]}`, renovate.TrustFailure),
		Entry("an error, which is a failure", `{"statuses": [{"context": "almanac/trust", "state": "error"}]}`, renovate.TrustFailure),
	)

	It("is final only once it passed or failed", func() {
		Expect(renovate.TrustAbsent.IsFinal()).To(BeFalse())
		Expect(renovate.TrustPending.IsFinal()).To(BeFalse())
		Expect(renovate.TrustSuccess.IsFinal()).To(BeTrue())
		Expect(renovate.TrustFailure.IsFinal()).To(BeTrue())
	})
})

var _ = Describe("How old a pull request's update is, for a scan", func() {
	It("applies the cooldown a workflow applies, and is unknown when nothing dates it", func() {
		reg := &archives{published: map[string]time.Time{}, files: map[string][]byte{}}
		pull := func(body string) hostile.Of[json.RawMessage] {
			encoded, err := json.Marshal(map[string]any{"body": body})
			Expect(err).NotTo(HaveOccurred())
			return hostile.New(json.RawMessage(encoded))
		}
		body := npmRow("lodash", "4.17.20", "4.17.21")
		Expect(renovate.CooldownOf(context.Background(), reg, pull(body), now)).To(Equal(cooled.Unknown))
		reg.release("lodash", "4.17.21", now.Add(-renovate.MinReleaseAge+time.Hour), nil)
		Expect(renovate.CooldownOf(context.Background(), reg, pull(body), now)).To(Equal(cooled.Young))
		Expect(renovate.CooldownOf(context.Background(), reg, pull(body), now.Add(time.Hour))).To(Equal(cooled.Passed))
	})
})
