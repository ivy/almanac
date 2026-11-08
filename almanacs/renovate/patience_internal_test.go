package renovate

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/hostile"
)

// Workflows can exist and never run on a pull request, which must not hold a fix in silence.
var _ = Describe("How long CI gets to report on a pull request", func() {
	now := time.Date(2026, 11, 5, 13, 0, 0, 0, time.UTC)
	openedAgo := func(ago time.Duration) hostile.Of[json.RawMessage] {
		raw, err := json.Marshal(map[string]time.Time{"created_at": now.Add(-ago)})
		Expect(err).NotTo(HaveOccurred())
		return hostile.New(json.RawMessage(raw))
	}

	It("is an hour, and an unknown age is not a long wait", func() {
		Expect(gaveUpOnCI(openedAgo(5*time.Minute), now)).To(BeFalse())
		Expect(gaveUpOnCI(openedAgo(61*time.Minute), now)).To(BeTrue())
		Expect(gaveUpOnCI(hostile.New(json.RawMessage(`{}`)), now)).To(BeFalse(), "an unknown age is not a long wait")
	})
})
