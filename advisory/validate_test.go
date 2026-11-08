package advisory_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/advisory"
	"github.com/ivy/almanac/hostile"
)

// These specs port crates/advisory/tests/validate.rs case for case, under the same names.

const sdk = "@modelcontextprotocol/sdk"

// fixture is a real cve.now entry, shared with the Rust crate.
const fixture = "../crates/advisory/fixtures/cve-now-entry.json"

// feed serves the fixture as cve.now would, and 404 for anything it does not know.
func feed() *advisory.CveNow {
	body, err := os.ReadFile(fixture)
	Expect(err).NotTo(HaveOccurred())
	mux := http.NewServeMux()
	entry := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
	mux.HandleFunc("GET /api/v1/entries/GHSA-6QXP-VCCF-F47H", entry)
	mux.HandleFunc("GET /api/v1/entries/CVE-2026-104850", entry)
	mux.HandleFunc("GET /api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("package") != sdk {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entries": [{"id": "CVE-2026-104850"}]}`))
	})
	server := httptest.NewServer(mux)
	DeferCleanup(server.Close)
	return advisory.NewCveNow(server.URL)
}

func known(ctx context.Context) advisory.Advisory {
	a, ok, err := feed().ByID(ctx, hostile.MustParseSlug("GHSA-6qxp-vccf-f47h"))
	Expect(err).NotTo(HaveOccurred())
	Expect(ok).To(BeTrue())
	return a
}

var _ = Describe("Validate", func() {
	It("a real entry reads as clean facts with its text tainted", func(ctx SpecContext) {
		a := known(ctx)
		Expect(a.ID.String()).To(Equal("CVE-2026-104850"))
		Expect(a.Aliases[0].String()).To(Equal("GHSA-6QXP-VCCF-F47H"))
		Expect(a.Severity).To(Equal(advisory.High))
		Expect(a.KEV).To(BeFalse())
		Expect(a.Affected).To(HaveLen(2), "a vendor-only entry is left out")
		// The summary is hostile in type, and stays tagged hostile when a run is recorded.
		Expect(reflect.TypeOf(a.Summary)).To(Equal(reflect.TypeFor[hostile.Of[string]]()))
		stored, err := json.Marshal(a.Summary)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(stored)).To(Equal(`{"$untrusted":""}`))
	})

	It("a lookup by package finds the same advisory", func(ctx SpecContext) {
		found, err := feed().ByPackage(ctx, hostile.MustParseSlug("npm"), hostile.MustParseSlug(sdk))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(HaveLen(1))
	})

	It("an unknown advisory is none", func(ctx SpecContext) {
		id := hostile.MustParseSlug("CVE-2099-0001")
		_, ok, err := feed().ByID(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
		_, ok, err = advisory.NewFake().ByID(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
	})

	It("an outage is an error and not a clean bill of health", func(ctx SpecContext) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/entries/CVE-2099-0001" {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			http.NotFound(w, r)
		}))
		DeferCleanup(server.Close)
		_, _, err := advisory.NewCveNow(server.URL).ByID(ctx, hostile.MustParseSlug("CVE-2099-0001"))
		Expect(err).To(HaveOccurred())
	})

	It("a target equal to the patched version validates", func(ctx SpecContext) {
		Expect(advisory.Validate(known(ctx), "npm", sdk, "1.20.0", "1.31.0")).To(Equal(advisory.Valid))
	})

	It("a target below the patched version fails", func(ctx SpecContext) {
		Expect(advisory.Validate(known(ctx), "npm", sdk, "1.20.0", "1.30.9")).To(Equal(advisory.TargetStillAffected))
	})

	It("a target past the patched version is not the fix", func(ctx SpecContext) {
		Expect(advisory.Validate(known(ctx), "npm", sdk, "1.20.0", "1.32.0")).To(Equal(advisory.NotThePatchedVersion))
	})

	It("a current version outside the range is not affected", func(ctx SpecContext) {
		a := known(ctx)
		Expect(advisory.Validate(a, "npm", sdk, "1.11.0", "1.31.0")).To(Equal(advisory.NotAffected))
		Expect(advisory.Validate(a, "npm", sdk, "1.31.0", "1.31.1")).To(Equal(advisory.NotAffected))
	})

	It("another package is not listed", func(ctx SpecContext) {
		Expect(advisory.Validate(known(ctx), "npm", "left-pad", "1.20.0", "1.31.0")).To(Equal(advisory.PackageNotListed))
	})

	It("versions that cannot be compared prove nothing", func(ctx SpecContext) {
		Expect(advisory.Validate(known(ctx), "npm", sdk, "not-a-version", "1.31.0")).To(Equal(advisory.Incomparable))
	})
})

var _ = Describe("Compare", func() {
	It("semver pre releases sort before their release", func() {
		Expect(order("npm", "1.31.0-rc.1", "1.31.0")).To(Equal(-1))
	})

	It("go versions keep their prefix", func() {
		Expect(order("Go", "v1.2.3", "v1.10.0")).To(Equal(-1))
	})

	It("shortened versions are padded", func() {
		Expect(order("npm", "1.2", "1.2.0")).To(Equal(0))
	})

	It("pypi compares plain releases and refuses the rest", func() {
		Expect(order("PyPI", "1.26", "1.26.0")).To(Equal(0))
		Expect(order("PyPI", "1.9", "1.10")).To(Equal(-1))
		Expect(order("PyPI", "1.0rc1", "1.0")).To(Equal("incomparable"))
	})

	It("an ecosystem without rules only knows equality", func() {
		Expect(order("Maven", "1.0", "1.0")).To(Equal(0))
		Expect(order("Maven", "1.0", "2.0")).To(Equal("incomparable"))
	})

	// The properties below draw versions from 0.0.0 to 49.49.49, as the Rust proptests do, with
	// the suite's random seed so a failure can be repeated.
	Context("for any two versions", func() {
		const cases = 1000
		var random *rand.Rand

		BeforeEach(func() {
			seed := uint64(GinkgoRandomSeed())
			random = rand.New(rand.NewPCG(seed, seed))
		})

		version := func() [3]int { return [3]int{random.IntN(50), random.IntN(50), random.IntN(50)} }
		text := func(v [3]int) string { return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]) }
		numeric := func(x, y [3]int) int {
			for i := range x {
				if x[i] != y[i] {
					if x[i] < y[i] {
						return -1
					}
					return 1
				}
			}
			return 0
		}

		It("semver order is numeric and not textual", func() {
			for range cases {
				x, y := version(), version()
				Expect(order("npm", text(x), text(y))).To(Equal(numeric(x, y)), "%s vs %s", text(x), text(y))
			}
		})

		It("order reverses when the versions swap", func() {
			for range cases {
				x, y := version(), version()
				forward, ok := advisory.Compare("crates.io", text(x), text(y))
				Expect(ok).To(BeTrue())
				backward, ok := advisory.Compare("crates.io", text(y), text(x))
				Expect(ok).To(BeTrue())
				Expect(forward).To(Equal(-backward), "%s vs %s", text(x), text(y))
			}
		})

		It("a v prefix never changes the order", func() {
			for range cases {
				x, y := version(), version()
				plain := order("go", text(x), text(y))
				prefixed := order("go", "v"+text(x), "v"+text(y))
				Expect(prefixed).To(Equal(plain), "%s vs %s", text(x), text(y))
			}
		})
	})
})
