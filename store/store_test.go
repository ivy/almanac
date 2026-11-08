package store_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/store"
)

var _ = Describe("Storable", func() {
	DescribeTable("replaces U+0000 in strings and keys, and nothing else",
		func(given, stored string) {
			Expect(store.Storable(json.RawMessage(given))).To(BeEquivalentTo(stored))
		},
		Entry("in a string", `"a\u0000b"`, `"a�b"`),
		Entry("in a key", `{"\u0000": 1}`, `{"�": 1}`),
		Entry("nested", `[{"t": ["\u0000\u0000"]}]`, `[{"t": ["��"]}]`),
		Entry("an escaped backslash before u0000", `"\\u0000"`, `"\\u0000"`),
		Entry("an escaped quote before it", `"\"\u0000"`, `"\"�"`),
		Entry("other escapes", `"\u0001\n�"`, `"\u0001\n�"`),
		Entry("no strings", `[1, true, null]`, `[1, true, null]`),
	)

	It("refuses anything that is not JSON", func() {
		for _, given := range []string{"", "{", "nope", `"\x"`} {
			_, err := store.Storable(json.RawMessage(given))
			Expect(err).To(HaveOccurred(), given)
		}
	})

	It("returns a copy", func() {
		given := json.RawMessage(`"x"`)
		stored, err := store.Storable(given)
		Expect(err).NotTo(HaveOccurred())
		stored[1] = 'y'
		Expect(string(given)).To(Equal(`"x"`))
	})
})

var _ = Describe("Stored names", func() {
	It("reads back every origin, and refuses others", func() {
		for _, o := range []store.Origin{store.Webhook, store.Scan, store.Simulated} {
			Expect(store.ParseOrigin(o.String())).To(Equal(o))
		}
		_, err := store.ParseOrigin("Webhook")
		Expect(err).To(HaveOccurred())
	})

	It("claims a scanned event after any live one", func() {
		Expect(store.Webhook.Lane()).To(Equal(store.Simulated.Lane()))
		Expect(store.Scan.Lane()).To(BeNumerically(">", store.Webhook.Lane()))
	})

	It("reads back every outbox status, and refuses others", func() {
		for _, s := range []store.Status{store.Pending, store.Applied, store.WouldApply, store.Failed} {
			Expect(store.ParseStatus(string(s))).To(Equal(s))
		}
		_, err := store.ParseStatus("delivered")
		Expect(err).To(HaveOccurred())
	})

	It("stores a dry run's effects as would-apply, never pending", func() {
		Expect(store.StatusIn(effect.DryRun)).To(Equal(store.WouldApply))
		Expect(store.StatusIn(effect.Mode(0))).To(Equal(store.WouldApply))
		Expect(store.StatusIn(effect.Live)).To(Equal(store.Pending))
	})
})
