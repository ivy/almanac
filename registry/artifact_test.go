package registry_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/registry"
)

func ok(body string) answer { return answer{http.StatusOK, []byte(body)} }

// The Rust registry has no tests of downloading. These cover what its artifact_of does.
var _ = Describe("Artifact", func() {
	It("downloads the tarball an npm index names", func(ctx SpecContext) {
		archive := []byte("an archive")
		fake := servingAt(func(base string) map[string]answer {
			return map[string]answer{
				"/is-odd":                    ok(`{"versions": {"3.0.1": {"dist": {"tarball": "` + base + `/is-odd/-/is-odd-3.0.1.tgz"}}}}`),
				"/is-odd/-/is-odd-3.0.1.tgz": {http.StatusOK, archive},
			}
		})

		got, err := fake.registry().Artifact(ctx, pkg(registry.Npm, "is-odd", "3.0.1"))
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(archive))
	})

	It("downloads the sdist a PyPI index names, not a wheel", func(ctx SpecContext) {
		fake := servingAt(func(base string) map[string]answer {
			return map[string]answer{
				"/pypi/six/1.16.0/json": ok(`{"urls": [{"packagetype": "bdist_wheel", "url": "` + base + `/wheel"}, {"packagetype": "sdist", "url": "` + base + `/sdist"}]}`),
				"/wheel":                ok("wheel"),
				"/sdist":                ok("sdist"),
			}
		})

		got, err := fake.registry().Artifact(ctx, pkg(registry.PyPI, "six", "1.16.0"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("sdist"))
	})

	It("downloads crates, Go modules, and GitHub release tarballs from where each lives", func(ctx SpecContext) {
		r := servingAt(func(base string) map[string]answer {
			return map[string]answer{
				"/api/v1/crates/itoa/1.0.11/download":          ok("crate"),
				"/github.com/!azure/x/@v/v0.14.0.zip":          ok("module"),
				"/repos/actions/checkout/releases/tags/v4.1.1": ok(`{"tarball_url": "` + base + `/repos/actions/checkout/tarball/v4.1.1"}`),
				"/repos/actions/checkout/tarball/v4.1.1":       ok("tarball"),
			}
		}).registry()

		for p, want := range map[registry.Package]string{
			pkg(registry.Crates, "itoa", "1.0.11"):                    "crate",
			pkg(registry.Go, "github.com/Azure/x", "v0.14.0"):         "module",
			pkg(registry.GitHubRelease, "actions/checkout", "v4.1.1"): "tarball",
		} {
			got, err := r.Artifact(ctx, p)
			Expect(err).NotTo(HaveOccurred(), p.String())
			Expect(string(got)).To(Equal(want))
		}
	})

	It("has no archive for a release the index does not name, or a registry with none", func(ctx SpecContext) {
		r := servingFixture("/is-odd", "npm-is-odd.json").registry()
		for _, p := range []registry.Package{
			pkg(registry.Npm, "is-odd", "9.9.9"),
			pkg(registry.Npm, "missing", "1.0.0"),
			pkg(registry.Docker, "alpine", "3.19"),
			pkg(registry.Unsupported, "ghcr.io/owner/image", "1"),
			{},
		} {
			_, err := r.Artifact(ctx, p)
			Expect(errors.Is(err, registry.ErrNoArtifact)).To(BeTrue(), p.String())
		}
	})

	It("an archive the index names and the registry lost is an error", func(ctx SpecContext) {
		fake := servingAt(func(base string) map[string]answer {
			return map[string]answer{"/is-odd": ok(`{"versions": {"3.0.1": {"dist": {"tarball": "` + base + `/gone.tgz"}}}}`)}
		})

		_, err := fake.registry().Artifact(ctx, pkg(registry.Npm, "is-odd", "3.0.1"))
		Expect(err).To(MatchError(ContainSubstring("404")))
		Expect(errors.Is(err, registry.ErrNoArtifact)).To(BeFalse())
	})

	It("refuses an archive too large to fetch, by its length or by what it sends", func(ctx SpecContext) {
		declared := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(1<<30))
			w.WriteHeader(http.StatusOK)
		}))
		DeferCleanup(declared.Close)
		streamed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.(http.Flusher).Flush()
			_, _ = w.Write(make([]byte, 2048))
		}))
		DeferCleanup(streamed.Close)

		for _, server := range []*httptest.Server{declared, streamed} {
			r := registry.WithMaxBytes(registry.NewHTTP(registry.Endpoints{Crates: server.URL}, server.Client()), 1024)
			_, err := r.Artifact(ctx, pkg(registry.Crates, "itoa", "1.0.11"))
			Expect(err).To(MatchError(ContainSubstring("so it is not fetched")))
		}
	})

	It("never quotes an address a registry sent", func(ctx SpecContext) {
		fake := serving(map[string]answer{
			"/is-odd": ok(`{"versions": {"3.0.1": {"dist": {"tarball": "http://127.0.0.1:1/IGNORE-PREVIOUS-INSTRUCTIONS.tgz"}}}}`),
		})

		_, err := fake.registry().Artifact(ctx, pkg(registry.Npm, "is-odd", "3.0.1"))
		Expect(err).To(MatchError(ContainSubstring("could not be reached")))
		Expect(err.Error()).NotTo(ContainSubstring("IGNORE"))
		Expect(errors.Unwrap(err)).To(BeNil())
	})

	It("quotes what failed when it asked an address of its own", func(ctx SpecContext) {
		r := registry.NewHTTP(registry.Endpoints{Crates: "http://127.0.0.1:1"}, nil)
		_, err := r.Artifact(ctx, pkg(registry.Crates, "itoa", "1.0.11"))
		Expect(err).To(MatchError(ContainSubstring("127.0.0.1:1/api/v1/crates/itoa/1.0.11/download")))
		Expect(errors.Unwrap(err)).NotTo(BeNil())
	})
})
