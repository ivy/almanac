package views_test

import (
	"html"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/almanacs/renovate/buildfailures"
	"github.com/ivy/almanac/almanacs/renovate/securityfixes"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/views"
)

var (
	styleBlock = regexp.MustCompile(`(?s)<style>.*?</style>`)
	anyTag     = regexp.MustCompile(`<[^>]*>`)
	spaces     = regexp.MustCompile(`\s+`)
	word       = regexp.MustCompile(`[A-Za-z]+`)
)

// visible is the page's text as a reader meets it, tooltips included, with tags and styles
// gone and every run of space made one.
func visible(page []byte) string {
	text := styleBlock.ReplaceAllString(string(page), " ")
	text = anyTag.ReplaceAllString(text, " ")
	return spaces.ReplaceAllString(html.UnescapeString(text), " ")
}

func render(w model.Workflow, opts views.Options) []byte {
	page, err := views.Page(renovate.Vocabulary, w, opts)
	Expect(err).NotTo(HaveOccurred())
	return page
}

// words are the lowercased words in each text.
func words(texts ...string) map[string]bool {
	out := map[string]bool{}
	for _, t := range texts {
		for _, w := range word.FindAllString(t, -1) {
			out[strings.ToLower(w)] = true
		}
	}
	return out
}

// known are the only words a page may hold: those of the model, its vocabulary and the views
// package's own source. A value a run read is in none of them.
func known(ws ...model.Workflow) map[string]bool {
	var texts []string
	for _, w := range ws {
		canonical, err := model.Canonical(w)
		Expect(err).NotTo(HaveOccurred())
		digest, err := model.Digest(w)
		Expect(err).NotTo(HaveOccurred())
		texts = append(texts, string(canonical), digest)
	}
	for _, e := range renovate.Vocabulary.Entries() {
		texts = append(texts, e.Name, e.Label, strings.Join(e.Values, " "))
		for _, t := range append(slices.Clone(e.Params), e.Result) {
			texts = append(texts, typeNames(t, map[reflect.Type]bool{})...)
		}
	}
	sources, err := filepath.Glob("*.go")
	Expect(err).NotTo(HaveOccurred())
	for _, path := range append(sources, "page.html.tmpl") {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		texts = append(texts, string(raw))
	}
	return words(texts...)
}

// typeNames are the names of t and of every type inside it, as a part's types name them.
func typeNames(t reflect.Type, seen map[reflect.Type]bool) []string {
	if t == nil || seen[t] {
		return nil
	}
	seen[t] = true
	names := []string{t.String()}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		names = append(names, typeNames(t.Elem(), seen)...)
	case reflect.Struct:
		for i := range t.NumField() {
			names = append(names, typeNames(t.Field(i).Type, seen)...)
		}
	}
	if held, ok := hostile.Held(t); ok {
		names = append(names, typeNames(held, seen)...)
	}
	return names
}

var _ = Describe("Page", func() {
	DescribeTable("shows every step and every ending by its label",
		func(w model.Workflow) {
			text := visible(render(w, views.Options{}))
			for _, step := range w.Flow {
				Expect(text).To(ContainSubstring(step.Label), step.ID)
			}
			for _, e := range w.Endings() {
				Expect(text).To(ContainSubstring(e.Label), e.Step+"/"+e.Rule)
			}
			Expect(text).To(ContainSubstring(w.Intent))
		},
		Entry("build_failures", buildfailures.Model),
		Entry("security_fixes", securityfixes.Model),
	)

	DescribeTable("holds only words from the model, its vocabulary and this package, so no fact's value",
		func(w model.Workflow, old *model.Workflow) {
			var opts views.Options
			allowed := known(w)
			if old != nil {
				s, err := views.Outcomes(renovate.Vocabulary, *old)
				Expect(err).NotTo(HaveOccurred())
				opts.Old = &s
				allowed = known(w, *old)
			}
			var unknown []string
			for found := range words(visible(render(w, opts))) {
				if !allowed[found] {
					unknown = append(unknown, found)
				}
			}
			Expect(unknown).To(BeEmpty())
		},
		Entry("build_failures", buildfailures.Model, nil),
		Entry("security_fixes", securityfixes.Model, nil),
		Entry("a change to build_failures", proposed(), &buildfailures.Model),
	)

	It("is self-contained: no script, and nothing loaded from anywhere", func() {
		page := string(render(securityfixes.Model, views.Options{}))
		Expect(page).To(HavePrefix("<!doctype html>"))
		for _, outside := range []string{"<script", "<link", "<img", "<iframe", "@import", "url(", "http://", "https://", "src="} {
			Expect(page).NotTo(ContainSubstring(outside))
		}
		Expect(page).To(ContainSubstring("<svg class=\"flow\""))
		Expect(page).To(ContainSubstring("<svg class=\"taint\""))
	})

	It("is readable in light and dark", func() {
		page := string(render(buildfailures.Model, views.Options{}))
		Expect(page).To(ContainSubstring("@media (prefers-color-scheme: dark)"))
		Expect(page).To(ContainSubstring("body { margin: 0; background: var(--bg); color: var(--ink);"))
	})

	It("escapes a label, so even markup in the model is shown as text", func() {
		w := buildfailures.Model.Clone()
		w.Flow[0].Label = `<script>alert("pwned")</script>`
		w.Flow[1].Decide[0].Label = `<img src=x onerror=alert(1)>`
		page := string(render(w, views.Options{}))
		Expect(page).NotTo(ContainSubstring("<script"))
		Expect(page).NotTo(ContainSubstring("<img"))
		Expect(visible([]byte(page))).To(ContainSubstring(`<script>alert("pwned")</script>`))
	})

	It("lists where hostile facts go", func() {
		text := visible(render(securityfixes.Model, views.Options{}))
		for _, want := range []string{"pull.text", "diff.unshown", "diff.diff", "review", "the reviewer's notes", "input “diff” of reviewer"} {
			Expect(text).To(ContainSubstring(want))
		}
	})

	It("draws no changes section without an old version", func() {
		page := string(render(buildfailures.Model, views.Options{}))
		Expect(page).NotTo(ContainSubstring(`id="changes"`))
		Expect(page).NotTo(ContainSubstring(`class="flag added"`))
	})

	It("lists the outcome diff, and marks what was added and changed on the flow", func() {
		old, err := views.Outcomes(renovate.Vocabulary, buildfailures.Model)
		Expect(err).NotTo(HaveOccurred())
		page := string(render(proposed(), views.Options{Old: &old}))
		text := visible([]byte(page))

		Expect(page).To(ContainSubstring(`id="changes"`))
		Expect(text).To(ContainSubstring("Endings added CI is not failing stop/not_failing"))
		Expect(text).To(ContainSubstring("Endings removed The repair took back the update decide/undid"))
		Expect(text).To(ContainSubstring("Endings changed Out of attempts, and a person was asked attempts/asked"))
		Expect(text).To(MatchRegexp(`attempts\s*: 3 → 2`))

		// The new step and its ending, and the endings whose condition changed.
		Expect(page).To(MatchRegexp(`<g class="step added[^"]*">\s*<title>Stop on a passing build`))
		Expect(page).To(MatchRegexp(`<g class="end at2 added">`))
		Expect(page).To(MatchRegexp(`<g class="end at3 changed">`))
		Expect(strings.Count(page, `class="flag changed"`)).To(Equal(3)) // two endings and the last decide
	})
})
