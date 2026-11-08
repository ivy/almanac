package diag_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/docs"
)

// samples builds one error of each code. A code added without a sample fails
// "every error has a page, a code and help".
func samples() []*diag.Error {
	cause := errors.New("x")
	source := diag.Source{Name: "almanac.toml"}
	return []*diag.Error{
		diag.ReadingConfig("p", cause),
		diag.InvalidConfig("m", source, diag.Span{}),
		diag.UnknownConfigKey("k", source, diag.Span{}),
		diag.SecretInConfig("k", source, diag.Span{}),
		diag.InvalidEnv("n"),
		diag.MissingEnv("n", "p"),
		diag.NotLoopback("S", "a", "e"),
		diag.NotACommand("g", "u"),
		diag.MissingProviders(nil),
		diag.Store(errors.New("s")),
		diag.UnreadableInput("p", cause),
		diag.InvalidJSON("p", cause),
		diag.Startup(cause),
		diag.NoSuchCode("c"),
		diag.Scan("w", "r"),
		diag.RateLimited("a", time.Second),
		diag.Refused("a", 403),
		diag.Unreachable("a", "r"),
		diag.ReadOnly("a"),
		diag.PushRefused("b", "r"),
		diag.UnknownName("w", "predicate", "n"),
		diag.FactBeforeObserved("w", "r"),
		diag.EffectNotAllowed("w", "asks"),
		diag.HostileFact("w", "r", "a key"),
		diag.WrongType("w", "r", "a key", "slug", "string"),
		diag.TitleNotClean("w", "r", "string"),
		diag.AgentInputHostile("w", "a", "i"),
		diag.PromptVars("w", "a", "p"),
		diag.PushUnchecked("w", "r"),
		diag.FixerBeforeMeasure("w", "a"),
		diag.FixerUncooled("w", "a", "young"),
		diag.NoEnding("w", "p"),
		diag.UnreachableStep("w", "p"),
		diag.DuplicateID("w", "i"),
		diag.DuplicateFact("w", "f"),
		diag.Unlabelled("w", "label"),
		diag.BadDeclaration("w", "p"),
		diag.DuplicateVocabulary("n"),
		diag.InvalidVocabulary("n", "p"),
	}
}

func decode(rendered string) map[string]any {
	var out map[string]any
	Expect(json.Unmarshal([]byte(rendered), &out)).To(Succeed(), rendered)
	return out
}

var _ = Describe("Diagnostics", func() {
	It("every error has a page, a code and help", func() {
		var codes []diag.Code
		for _, e := range samples() {
			codes = append(codes, e.Code)
			page, err := diag.Explain(string(e.Code))
			Expect(err).NotTo(HaveOccurred(), "%s has no page", e.Code)
			Expect(page).To(ContainSubstring("## What to do next"), string(e.Code))
			Expect(strings.TrimSpace(e.Help)).NotTo(BeEmpty(), string(e.Code))
			Expect(e.Why()).NotTo(BeEmpty(), string(e.Code))
		}
		Expect(codes).To(ConsistOf(diag.Codes()))
	})

	It("every page on disk is listed", func() {
		pages, err := fs.Glob(docs.Errors, "errors/*.md")
		Expect(err).NotTo(HaveOccurred())
		goOnly, err := fs.Glob(docs.Errors, "errors-go/*.md")
		Expect(err).NotTo(HaveOccurred())
		pages = append(pages, goOnly...)
		var listed []string
		for _, code := range diag.Codes() {
			listed = append(listed, diag.PagePath(code))
		}
		Expect(pages).To(ConsistOf(listed))
	})

	It("an unknown code is itself explained", func() {
		_, err := diag.Explain("nonsense")
		var e *diag.Error
		Expect(errors.As(err, &e)).To(BeTrue())
		Expect(e.Code).To(Equal(diag.UnknownCode))
	})

	It("explain prints the page as written", func() {
		page, err := diag.Explain("almanac::store::failed")
		Expect(err).NotTo(HaveOccurred())
		Expect(page).To(HavePrefix("# The store could not be reached or refused a write\n"))
		Expect(page).To(ContainSubstring("## Why it matters"))
	})

	It("an agent can read which provider is missing", func() {
		// The runtime names what each workflow needs; see R-40 for the startup check itself.
		e := diag.MissingProviders([]string{"needs_a_provider needs a provider for string"})
		out := decode(diag.Render(e, diag.JSON))
		Expect(out["code"]).To(Equal("almanac::provider::missing"))
		Expect(out["message"]).To(ContainSubstring("needs a provider for"))
		Expect(out["message"]).To(ContainSubstring("string"))
	})

	Describe("rendering", func() {
		It("says the same things to a person and to a program", func() {
			e := diag.ReadingConfig("/etc/almanac.toml", errors.New("permission denied"))
			human := diag.Render(e, diag.Human)
			out := decode(diag.Render(e, diag.JSON))

			Expect(out["code"]).To(Equal("almanac::config::read"))
			for _, fact := range []any{out["code"], out["message"], out["why"], out["help"], out["causes"].([]any)[0]} {
				Expect(strings.Join(strings.Fields(human), " ")).To(ContainSubstring(fact.(string)))
			}
			Expect(out["severity"]).To(Equal("error"))
			Expect(out["labels"]).To(BeEmpty())
		})

		It("wraps long help at a hundred columns, under its label", func() {
			e := diag.NotACommand("frob", strings.Repeat("usage words ", 20))
			for _, line := range strings.Split(diag.Render(e, diag.Human), "\n") {
				Expect(len([]rune(line))).To(BeNumerically("<=", 100), line)
			}
			Expect(diag.Render(e, diag.Human)).To(ContainSubstring("\n        usage words"))
		})

		It("does not repeat a cause that is the message", func() {
			e := diag.Store(errors.New("connection refused"))
			Expect(e.Message).To(Equal("connection refused"))
			Expect(decode(diag.Render(e, diag.JSON))["causes"]).To(BeEmpty())
			Expect(errors.Unwrap(e)).To(MatchError("connection refused"))
		})

		It("still marks a span that runs off the end of the file", func() {
			source := diag.Source{Name: "almanac.toml", Text: "listen = \"x"}
			e := diag.InvalidConfig("unexpected EOF", source, diag.Span{Offset: 11, Length: 4})
			Expect(diag.Render(e, diag.Human)).To(ContainSubstring("╭─[almanac.toml:1:12]"))
		})
	})
})
