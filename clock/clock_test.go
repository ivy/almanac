package clock_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/clock"
)

var _ = Describe("Clock", func() {
	It("tells the host's time in UTC", func() {
		Expect(clock.System{}.Now().Location()).To(Equal(time.UTC))
		Expect(clock.System{}.Now()).To(BeTemporally("~", time.Now(), time.Second))
	})

	It("moves a fake only when told, for everything sharing it", func() {
		start := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
		fake := clock.NewFake(start)
		var shared clock.Clock = fake

		Expect(shared.Now()).To(Equal(start))
		fake.Advance(time.Hour)
		Expect(shared.Now()).To(Equal(start.Add(time.Hour)))
	})
})
