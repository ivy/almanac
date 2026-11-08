package reveal_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/hostile/reveal"
)

var _ = Describe("Value", func() {
	It("returns what a hostile value holds", func() {
		Expect(reveal.Value(hostile.New("body text"))).To(Equal("body text"))
		Expect(reveal.Value(hostile.New([]string{"a", "b"}))).To(Equal([]string{"a", "b"}))
	})

	It("returns the zero value from the zero hostile value", func() {
		var h hostile.Of[int]
		Expect(reveal.Value(h)).To(BeZero())
	})
})
