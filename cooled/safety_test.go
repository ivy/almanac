package cooled_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/cooled"
)

// Rust's Cooled could be neither decoded nor cloned. Go decodes one into the zero value, which
// proves nothing, and copies any value. A model cannot reach either: the proof is never a fact,
// and the engine keeps it in memory for the fixer step.
var _ = Describe("The cooldown proof, for the Rust compile-fail cases", func() {
	fixer := agent.NewFixer("fixer", agent.MustPrompt(agent.ParsePrompt("fixer.md", "Fix it.")))

	It("proves nothing when decoded from data, and a fixer refuses it (cooled_cannot_be_deserialized)", func() {
		// Decoding into a cooled.Cooled itself is refused by staticcheck (SA9005) in make
		// go-lint; a record that holds one decodes.
		var record struct{ Proof cooled.Cooled }
		Expect(json.Unmarshal([]byte(`{"Proof": {"proven": true}}`), &record)).To(Succeed())
		Expect(record.Proof.IsZero()).To(BeTrue())

		_, err := fixer.Repair(agent.Vars{}, nil, record.Proof)
		Expect(err).To(MatchError(ContainSubstring("only after it has cooled")))
	})

	It("copies, and a copy proves what the check proved (cooled_cannot_be_copied)", func() {
		proof, ok := cooled.Check(at(2026, 11, 1, 12, 0), days3, now)
		Expect(ok).To(BeTrue())
		again := proof

		Expect(again.IsZero()).To(BeFalse())
		_, err := fixer.Repair(agent.Vars{}, nil, again)
		Expect(err).NotTo(HaveOccurred())
	})
})
