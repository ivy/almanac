package effect_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/patch"
)

// Rust refused decoding a checked patch when the workflow compiled. Go decodes one into the zero
// value, which proves nothing, and the outbox refuses it when the effect is planned. A model
// cannot reach this at all: the engine keeps the checked patch in memory, and the checker
// refuses a push of anything but what a check_patch step accepted.
var _ = Describe("Effects, for the Rust compile-fail cases", func() {
	It("plans no push of a checked patch decoded from data (checked_cannot_be_deserialized)", func() {
		// Decoding into a patch.Checked itself is refused by staticcheck (SA9005) in make
		// go-lint; a record that holds one decodes.
		var record struct{ Patch patch.Checked }
		Expect(json.Unmarshal([]byte(`{"Patch": {"patch": {"$untrusted": {}}, "checked": true}}`), &record)).To(Succeed())
		Expect(record.Patch.IsZero()).To(BeTrue())

		_, err := effect.Plan(effect.Push{Repo: repo, Pull: 1, Branch: branch, Message: "m", Patch: record.Patch})
		Expect(err).To(MatchError(ContainSubstring("patch.Policy.Check")))
	})
})
