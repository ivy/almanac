package report_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/almanacs/renovate/report"
	"github.com/ivy/almanac/github"
	"github.com/ivy/almanac/hostile"
)

// files is a GitHub that serves the files in it, by repo/path, and a default branch of each
// repository requiring required.
type files struct {
	mu       sync.Mutex
	files    map[string]string
	required []string
	asked    []string
}

func (f *files) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/repos/")
	f.asked = append(f.asked, path)
	if strings.Contains(path, "/branches/") {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"protection": map[string]any{"required_status_checks": map[string]any{"contexts": f.required}},
		})
		return
	}
	owner, rest, _ := strings.Cut(path, "/")
	repo, file, _ := strings.Cut(rest, "/contents/")
	text, ok := f.files[owner+"/"+repo+"/"+file]
	if !ok {
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"content": base64.StdEncoding.EncodeToString([]byte(text))})
}

var _ = Describe("Reading a repository's Renovate setup", func() {
	read := func(served *files) report.Config {
		server := httptest.NewServer(served)
		DeferCleanup(server.Close)
		client := github.New(server.URL, github.Token("t"), github.ReadOnly())
		config, err := report.ReadConfig(context.Background(), client, hostile.MustParseSlug("owner/repo"), hostile.MustParseSlug("main"))
		Expect(err).NotTo(HaveOccurred())
		return config
	}

	It("takes what its own config leaves unset from the presets it extends, earlier presets first", func() {
		served := &files{files: map[string]string{
			"owner/repo/.github/renovate.json5": `{extends: ["github>owner/first", "github>owner/second:base"], automerge: true}`,
			"owner/first/default.json":          `{"automerge": false, "gitIgnoredAuthors": ["almanac@example.com"]}`,
			"owner/second/default.json":         `{"gitIgnoredAuthors": [], "platformAutomerge": false}`,
		}}
		Expect(read(served)).To(Equal(report.Config{
			Automerge:         report.On,
			PlatformAutomerge: report.Off,
			GitIgnoredAuthors: report.On,
		}))
	})

	It("reads the first config file Renovate would, and no other", func() {
		served := &files{files: map[string]string{
			"owner/repo/renovate.json5": `{automerge: false}`,
			"owner/repo/.renovaterc":    `{"automerge": true}`,
		}}
		Expect(read(served).Automerge).To(Equal(report.Off))
		Expect(served.asked).NotTo(ContainElement("owner/repo/contents/.renovaterc"))
	})

	It("sets nothing for a repository with no config, and requires trust as its branch does", func() {
		served := &files{files: map[string]string{}, required: []string{"ci", renovate.TrustContext}}
		Expect(read(served)).To(Equal(report.Config{TrustRequired: true}))
	})
})
