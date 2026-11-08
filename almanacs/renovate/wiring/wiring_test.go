package wiring_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/advisory"
	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/almanacs/renovate/wiring"
	"github.com/ivy/almanac/clock"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/github"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/registry"
	"github.com/ivy/almanac/simforge"
)

// environment is a process environment holding exactly vars.
func environment(vars map[string]string) wiring.Env {
	return func(name string) (string, bool) {
		value, ok := vars[name]
		return value, ok
	}
}

func provided[T any](set *providers.Set) bool { return set.Has(providers.Require[T]()) }

var _ = Describe("The Renovate almanac's providers", func() {
	var sim *simforge.Forge

	BeforeEach(func() {
		sim = simforge.Start()
		DeferCleanup(sim.Close)
		sim.AddRepo(simforge.PublicRepo("owner/repo"))
	})

	It("give a live almanac everything it reaches, from the environment", func() {
		set, err := wiring.Providers(environment(map[string]string{
			"GITHUB_TOKEN":            "token",
			"GITHUB_API_URL":          sim.URL(),
			"ANTHROPIC_API_KEY":       "key",
			"ALMANAC_OWNER":           "owner",
			"ALMANAC_RENOVATE_AUTHOR": "renovate[bot]",
			"ALMANAC_BOT_LOGIN":       "my-almanac[bot]",
			"ALMANAC_ALERTS_REPO":     "owner/alerts",
		}))
		Expect(err).NotTo(HaveOccurred())
		Expect(provided[*github.Client](set)).To(BeTrue())
		Expect(provided[effect.Forge](set)).To(BeTrue())
		Expect(provided[effect.Tracker](set)).To(BeTrue())
		Expect(provided[effect.Sweep](set)).To(BeTrue())
		Expect(provided[registry.Registry](set)).To(BeTrue())
		Expect(provided[advisory.Advisories](set)).To(BeTrue())
		Expect(provided[clock.Clock](set)).To(BeTrue())
		_, ok := set.HarnessFor("fixer")
		Expect(ok).To(BeTrue())
		target, _ := providers.Lookup[renovate.Target](set)
		Expect(target).To(Equal(renovate.Target{Owner: "owner", Author: "renovate[bot]"}))
		Expect(set.Identity()).To(Equal([]string{"my-almanac[bot]"}))
		alerts, ok := set.Alerts()
		Expect(ok).To(BeTrue())
		Expect(alerts.String()).To(Equal("owner/alerts"))
	})

	It("default to Renovate's login and Almanac's own, and alert nowhere", func() {
		set, err := wiring.Providers(environment(map[string]string{}))
		Expect(err).NotTo(HaveOccurred())
		target, _ := providers.Lookup[renovate.Target](set)
		Expect(target.Author).To(Equal(renovate.DefaultAuthor))
		Expect(set.Identity()).To(Equal([]string{github.DefaultAuthor().Name}))
		_, ok := set.Alerts()
		Expect(ok).To(BeFalse())
	})

	It("give a read-only pass no harness and no model credential, and a client that refuses every write", func() {
		set, err := wiring.Providers(environment(map[string]string{
			"GITHUB_TOKEN":      "token",
			"GITHUB_API_URL":    sim.URL(),
			"ANTHROPIC_API_KEY": "key",
			"ALMANAC_READ_ONLY": "1",
		}))
		Expect(err).NotTo(HaveOccurred())
		_, ok := set.HarnessFor("fixer")
		Expect(ok).To(BeFalse(), "the runtime stands in for every harness in a read-only pass")
		forge, ok := providers.Lookup[effect.Forge](set)
		Expect(ok).To(BeTrue())
		head := hostile.MustParseSha(sim.Pull("owner/repo", sim.AddPull("owner/repo", simforge.RenovatePull("renovate/x", "Update x"))).HeadSHA)
		err = forge.Attest(context.Background(), "key", effect.Attest{
			Context: renovate.TrustContext, State: effect.Pending, Repo: hostile.MustParseSlug("owner/repo"), Sha: head,
		})
		var refused *diag.Error
		Expect(errors.As(err, &refused)).To(BeTrue())
		Expect(refused.Code).To(Equal(diag.ForgeReadOnly))
		Expect(sim.Writes()).To(BeEmpty())
	})

	It("give a report a client that refuses every write, whatever ALMANAC_READ_ONLY says", func() {
		client, err := wiring.ReadOnlyClient(environment(map[string]string{
			"GITHUB_TOKEN":      "token",
			"GITHUB_API_URL":    sim.URL(),
			"ALMANAC_READ_ONLY": "0",
		}))
		Expect(err).NotTo(HaveOccurred())
		head := hostile.MustParseSha(sim.Pull("owner/repo", sim.AddPull("owner/repo", simforge.RenovatePull("renovate/x", "Update x"))).HeadSHA)
		err = github.NewForge(client, github.DefaultAuthor()).Attest(context.Background(), "key", effect.Attest{
			Context: renovate.TrustContext, State: effect.Pending, Repo: hostile.MustParseSlug("owner/repo"), Sha: head,
		})
		var refused *diag.Error
		Expect(errors.As(err, &refused)).To(BeTrue())
		Expect(refused.Code).To(Equal(diag.ForgeReadOnly))
		Expect(sim.Writes()).To(BeEmpty())

		none, err := wiring.ReadOnlyClient(environment(map[string]string{}))
		Expect(err).NotTo(HaveOccurred())
		Expect(none).To(BeNil())
	})

	It("leave out GitHub, and the harness that checks out through it, without GitHub credentials", func() {
		set, err := wiring.Providers(environment(map[string]string{"ANTHROPIC_API_KEY": "key"}))
		Expect(err).NotTo(HaveOccurred())
		Expect(provided[*github.Client](set)).To(BeFalse())
		Expect(provided[effect.Forge](set)).To(BeFalse())
		_, ok := set.HarnessFor("fixer")
		Expect(ok).To(BeFalse())
	})

	It("leave out the harness without a model credential", func() {
		set, err := wiring.Providers(environment(map[string]string{"GITHUB_TOKEN": "token"}))
		Expect(err).NotTo(HaveOccurred())
		_, ok := set.HarnessFor("fixer")
		Expect(ok).To(BeFalse())
	})

	It("refuse a GitHub App installation that is not a number, naming the variable", func() {
		_, err := wiring.Providers(environment(map[string]string{
			"GITHUB_APP_ID":              "1",
			"GITHUB_APP_PRIVATE_KEY":     "key",
			"GITHUB_APP_INSTALLATION_ID": "seven",
		}))
		var invalid *diag.Error
		Expect(errors.As(err, &invalid)).To(BeTrue())
		Expect(invalid.Code).To(Equal(diag.ConfigEnvValue))
		Expect(invalid.Message).To(ContainSubstring("GITHUB_APP_INSTALLATION_ID"))
	})

	It("point every registry at ALMANAC_REGISTRY_URL", func() {
		var mu sync.Mutex
		var paths []string
		mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			paths = append(paths, r.URL.Path)
			mu.Unlock()
			http.NotFound(w, r)
		}))
		DeferCleanup(mirror.Close)
		set, err := wiring.Providers(environment(map[string]string{"ALMANAC_REGISTRY_URL": mirror.URL}))
		Expect(err).NotTo(HaveOccurred())
		reg, _ := providers.Lookup[registry.Registry](set)
		for _, ecosystem := range []registry.Ecosystem{registry.Npm, registry.Crates, registry.PyPI} {
			release, err := registry.NewPackage(ecosystem, hostile.MustParseSlug("left-pad"), hostile.MustParseSlug("1.0.0"))
			Expect(err).NotTo(HaveOccurred())
			_, known, _ := reg.Published(context.Background(), release)
			Expect(known).To(BeFalse())
		}
		mu.Lock()
		defer mu.Unlock()
		Expect(paths).To(HaveLen(3))
	})
})
