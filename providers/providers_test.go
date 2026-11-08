package providers_test

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
)

type forge struct{ name string }

func (forge) Attest(context.Context, string, effect.Attest) error { return nil }
func (forge) Push(context.Context, string, effect.Commit) error   { return nil }

type harness struct{ name string }

func (harness) Run(context.Context, agent.Pass, string, agent.Grant, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

var _ = Describe("Providers", func() {
	It("finds a provider under the type it was provided as", func() {
		set := providers.Provide[effect.Forge](providers.New(), forge{name: "simforge"})

		found, ok := providers.Lookup[effect.Forge](set)

		Expect(ok).To(BeTrue())
		Expect(found).To(Equal(forge{name: "simforge"}))
		Expect(set.Has(providers.Require[effect.Forge]())).To(BeTrue())
	})

	It("does not find a provider under another type", func() {
		set := providers.Provide(providers.New(), forge{})

		_, ok := providers.Lookup[effect.Forge](set)

		Expect(ok).To(BeFalse())
		Expect(set.Has(providers.Require[effect.Tracker]())).To(BeFalse())
	})

	It("removes a provider set to nil", func() {
		set := providers.Provide[effect.Forge](providers.New(), forge{})
		providers.Provide[effect.Forge](set, nil)

		Expect(set.Has(providers.Require[effect.Forge]())).To(BeFalse())
	})

	It("names a requirement as Go writes its type, for the manifest", func() {
		need := providers.Require[effect.Forge]()
		Expect(need.Name()).To(Equal("effect.Forge"))

		listed, err := json.Marshal([]providers.Requirement{need})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(listed)).To(Equal(`["effect.Forge"]`))
	})

	It("gives an agent its own harness, or the one every agent uses", func() {
		set := providers.New()
		_, ok := set.HarnessFor("reviewer")
		Expect(ok).To(BeFalse())

		set.UseHarness(harness{name: "claude"}).UseHarnessFor("repairer", harness{name: "stub"})

		reviewer, _ := set.HarnessFor("reviewer")
		repairer, _ := set.HarnessFor("repairer")
		Expect(reviewer).To(Equal(harness{name: "claude"}))
		Expect(repairer).To(Equal(harness{name: "stub"}))
	})

	It("holds who the almanac acts as, and where a tripped budget asks", func() {
		ops := hostile.MustParseSlug("octo-org/ops")
		set := providers.New().ActAs("almanac[bot]").AlertTo(ops)

		Expect(set.Identity()).To(Equal([]string{"almanac[bot]"}))
		alerts, ok := set.Alerts()
		Expect(ok).To(BeTrue())
		Expect(alerts).To(Equal(ops))
		_, ok = providers.New().Alerts()
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("A cloned set", func() {
	It("holds what the set held, and changing it leaves the set as it was", func() {
		set := providers.Provide[effect.Forge](providers.New(), forge{name: "simforge"})
		set.UseHarnessFor("reviewer", harness{name: "reviewer"}).ActAs("almanac[bot]").AlertTo(hostile.MustParseSlug("owner/ops"))

		clone := set.Clone()
		providers.Provide[effect.Forge](clone, forge{name: "refusing"})
		clone.UseHarnessFor("reviewer", harness{name: "refusing"}).ActAs("someone")

		found, _ := providers.Lookup[effect.Forge](clone)
		Expect(found).To(Equal(forge{name: "refusing"}))
		original, _ := providers.Lookup[effect.Forge](set)
		Expect(original).To(Equal(forge{name: "simforge"}))
		reviewer, _ := set.HarnessFor("reviewer")
		Expect(reviewer).To(Equal(harness{name: "reviewer"}))
		Expect(set.Identity()).To(Equal([]string{"almanac[bot]"}))
		target, ok := clone.Alerts()
		Expect(ok).To(BeTrue())
		Expect(target).To(Equal(hostile.MustParseSlug("owner/ops")))
	})
})
