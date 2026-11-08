package config_test

import (
	"math/rand/v2"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/config"
)

// randomLimits draws limits across the ranges an almanac could declare or a
// config could set.
func randomLimits(r *rand.Rand) config.Limits {
	limit := func() config.Cap {
		if r.IntN(2) == 0 {
			return config.Cap{}
		}
		return config.CapAt(r.IntN(10_000))
	}
	return config.Limits{
		MaxChainDepth:      r.IntN(100),
		RunsPerHour:        limit(),
		OverallRunsPerHour: limit(),
		Concurrency:        1 + r.IntN(63),
		ScanInterval:       time.Duration(1+r.IntN(99_999)) * time.Second,
	}
}

// noWider holds when after is the same ceiling as before, or a lower one.
func noWider(after, before config.Cap) bool {
	a, aSet := after.Value()
	b, bSet := before.Value()
	switch {
	case !bSet:
		return true
	case !aSet:
		return false
	default:
		return a <= b
	}
}

func expectNoWider(after, before config.Limits) {
	GinkgoHelper()
	Expect(after.MaxChainDepth).To(BeNumerically("<=", before.MaxChainDepth))
	Expect(after.Concurrency).To(BeNumerically("<=", before.Concurrency))
	Expect(noWider(after.RunsPerHour, before.RunsPerHour)).To(BeTrue(), "runs per hour widened")
	Expect(noWider(after.OverallRunsPerHour, before.OverallRunsPerHour)).To(BeTrue(), "overall runs per hour widened")
	Expect(after.ScanInterval).To(BeNumerically(">=", before.ScanInterval))
}

var _ = Describe("Limits", func() {
	It("tighten never widens any limit", func() {
		seed := uint64(GinkgoRandomSeed())
		r := rand.New(rand.NewPCG(seed, seed))
		for range 1_000 {
			base, other, more := randomLimits(r), randomLimits(r), randomLimits(r)
			once := base.Tighten(other)
			expectNoWider(once, base)
			expectNoWider(once.Tighten(more), once)
		}
	})

	It("a config tightens only what it sets", func() {
		seed := uint64(GinkgoRandomSeed())
		r := rand.New(rand.NewPCG(seed, seed+1))
		for range 1_000 {
			declared := randomLimits(r)
			Expect(config.Config{}.Tighten(declared)).To(Equal(declared))
			c := config.Config{Concurrency: config.CapAt(r.IntN(100)), ScanInterval: time.Duration(r.IntN(100)) * time.Minute}
			expectNoWider(c.Tighten(declared), declared)
		}
	})

	It("any ceiling is lower than none", func() {
		Expect(config.Cap{}.Tighten(config.CapAt(5))).To(Equal(config.CapAt(5)))
		Expect(config.CapAt(5).Tighten(config.Cap{})).To(Equal(config.CapAt(5)))
		Expect(config.Cap{}.Tighten(config.Cap{})).To(Equal(config.Cap{}))
		Expect(config.CapAt(3).Tighten(config.CapAt(5))).To(Equal(config.CapAt(3)))
	})
})
