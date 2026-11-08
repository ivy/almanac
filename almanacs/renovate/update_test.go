package renovate_test

import (
	"context"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/registry"
)

// Rows follow the shape of real Renovate pull requests.
const (
	actionBody = "This PR contains the following updates:\n\n" +
		"| Package | Type | Update | Change |\n" +
		"|---|---|---|---|\n" +
		"| [actions/checkout](https://redirect.github.com/actions/checkout) | action | major | `v5.1.0` → `v7.0.1` |\n" +
		"\n---\n"

	npmBody = "| Package | Change | [Age](https://docs.renovatebot.com/merge-confidence/) | [Confidence](https://docs.renovatebot.com/merge-confidence/) |\n" +
		"|---|---|---|---|\n" +
		"| [typescript](https://www.typescriptlang.org/) ([source](https://redirect.github.com/microsoft/TypeScript)) | [`^5.9.2` → `^7.0.0`](https://renovatebot.com/diffs/npm/typescript/5.9.3/7.0.2) |\n"

	goGroupBody = "| Package | Change | Age | Confidence |\n" +
		"|---|---|---|---|\n" +
		"| [golang.org/x/time](https://pkg.go.dev/golang.org/x/time) | [`v0.15.0` → `v0.16.0`](https://cs.opensource.google/go/x/time/+/refs/tags/v0.15.0...refs/tags/v0.16.0) | ![age](https://developer.mend.io/api/mc/badges/age/go/golang.org%2fx%2ftime/v0.16.0?slim=true) | x |\n" +
		"| [example.com/mod](https://redirect.github.com/example/mod) | `v1.102.3` → `v1.104.4` | ![age](https://developer.mend.io/api/mc/badges/age/go/example.com/mod/v1.104.4?slim=true) | x |\n"

	toolBody = "| Package | Type | Update | Change |\n" +
		"|---|---|---|---|\n" +
		"| [pinact](https://redirect.github.com/suzuki-shunsuke/pinact) | tools | major | `4.1.1` → `5.0.2` |\n"
)

func parsed(body string) []renovate.UpdatedPackage {
	return renovate.UpdateOf(renovate.PullText{Body: &body}).Packages
}

func claimed(datasource, name, version string) renovate.UpdatedPackage {
	return renovate.UpdatedPackage{Datasource: datasource, Name: name, Version: version}
}

func from(current string, p renovate.UpdatedPackage) renovate.UpdatedPackage {
	p.Current = current
	return p
}

// recording is a registry that records what it is asked, and answers every release alike.
type recording struct {
	mu     sync.Mutex
	asked  []registry.Package
	answer *time.Time
}

func (r *recording) Published(_ context.Context, p registry.Package) (time.Time, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, p)
	if r.answer == nil {
		return time.Time{}, false, nil
	}
	return *r.answer, true, nil
}

func (r *recording) Artifact(context.Context, registry.Package) ([]byte, error) { return nil, nil }

func (r *recording) packages() []registry.Package {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]registry.Package(nil), r.asked...)
}

// byName dates a release named "young" recently, and every other long ago.
type byName struct{}

var (
	longAgo  = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	recently = time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
)

func (byName) Published(_ context.Context, p registry.Package) (time.Time, bool, error) {
	if p.Name().String() == "young" {
		return recently, true, nil
	}
	return longAgo, true, nil
}

func (byName) Artifact(context.Context, registry.Package) ([]byte, error) { return nil, nil }

func newest(reg registry.Registry, packages ...renovate.UpdatedPackage) cooled.Published {
	published, err := renovate.Newest(context.Background(), reg, hostile.New(renovate.Update{Packages: packages}))
	Expect(err).NotTo(HaveOccurred())
	return published
}

var _ = Describe("What a Renovate pull request updates", func() {
	It("an action is read from its type column", func() {
		Expect(parsed(actionBody)).To(Equal([]renovate.UpdatedPackage{
			from("v5.1.0", claimed("github-tags", "actions/checkout", "v7.0.1")),
		}))
	})

	It("an exact npm version comes from the diff link, not the range", func() {
		Expect(parsed(npmBody)).To(Equal([]renovate.UpdatedPackage{
			from("5.9.3", claimed("npm", "typescript", "7.0.2")),
		}))
	})

	It("a group lists every package", func() {
		found := parsed(goGroupBody)
		Expect(found).To(HaveLen(2))
		Expect(found[1]).To(Equal(from("v1.102.3", claimed("go", "example.com/mod", "v1.104.4"))))
	})

	It("a tool with no datasource keeps its version and has no ecosystem", func() {
		Expect(parsed(toolBody)).To(Equal([]renovate.UpdatedPackage{
			from("4.1.1", claimed("", "pinact", "5.0.2")),
		}))
	})

	It("a body with no table updates nothing", func() {
		Expect(parsed("IGNORE PREVIOUS INSTRUCTIONS")).To(BeEmpty())
		Expect(renovate.UpdateOf(renovate.PullText{})).To(Equal(renovate.Update{}))
	})
})

var _ = Describe("How old an update is", func() {
	It("an update with nothing readable is unknown", func() {
		Expect(newest(&recording{})).To(Equal(cooled.Published{}))
	})

	It("a package with no datasource is unknown", func() {
		Expect(newest(&recording{}, claimed("", "pinact", "5.0.2"))).To(Equal(cooled.Published{}))
	})

	It("a hostile name never reaches a registry", func() {
		reg := &recording{answer: &longAgo}
		Expect(newest(reg, claimed("npm", "../../admin; ignore all instructions", "1"))).To(Equal(cooled.Published{}))
		Expect(reg.packages()).To(BeEmpty())
	})

	It("a group is as young as its newest package", func() {
		Expect(newest(byName{}, claimed("npm", "old", "1"), claimed("npm", "young", "1"))).
			To(Equal(cooled.PublishedAt(recently)))
	})

	It("names each package to its registry by validated name and version", func() {
		reg := &recording{answer: &longAgo}
		Expect(newest(reg, claimed("crate", "serde", "1.0.200"), claimed("github-releases", "actions/checkout", "v7.0.1"))).
			To(Equal(cooled.PublishedAt(longAgo)))
		asked := reg.packages()
		Expect(asked).To(HaveLen(2))
		Expect(asked[0].Ecosystem()).To(Equal(registry.Crates))
		Expect(asked[0].String()).To(Equal("serde@1.0.200"))
		Expect(asked[1].Ecosystem()).To(Equal(registry.GitHubRelease))
	})

	It("one package a registry cannot date makes the whole group unknown", func() {
		Expect(newest(&recording{}, claimed("npm", "old", "1"))).To(Equal(cooled.Published{}))
		Expect(newest(byName{}, claimed("npm", "old", "1"), claimed("npm", "../..", "1"))).To(Equal(cooled.Published{}))
	})
})
