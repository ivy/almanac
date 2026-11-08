package engine_test

import (
	"context"
	"encoding/json"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/wire"
	"github.com/ivy/almanac/workflow"
)

// otherScan finds an event of another kind, shaped as that event is.
var otherScan = vocab.Scan(probes, "probe.other_scan", "pull requests, which this workflow does not handle", scanOther)

func scanOther(context.Context, *workflow.Scanner) ([]workflow.Found, error) {
	return []workflow.Found{{DeliveryID: "scan:other", Kind: "github.pull_request.opened", Payload: json.RawMessage(`{"number": 7}`)}}, nil
}

var _ = Describe("The engine, for the Rust compile-fail cases", func() {
	It("finds only events of its own kind, and refuses one of another shape when it is identified (scan_returns_the_wrong_event)", func() {
		w := linked.Clone()
		w.Scan = otherScan.Name()
		e := probing(w)

		found, err := e.Scan(context.Background(), workflow.NewScanner(providers.New()))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(HaveLen(1))
		Expect(found[0].Kind).To(Equal("probe.happened"))

		_, err = e.Identify(found[0].Payload)
		var malformed *wire.Malformed
		Expect(errors.As(err, &malformed)).To(BeTrue())
	})
})
