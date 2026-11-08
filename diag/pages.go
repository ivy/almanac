package diag

import (
	"io/fs"
	"strings"

	"github.com/ivy/almanac/docs"
)

// Explain returns the long form of code: what happened, why it matters, and
// what to do next. A code no error has is itself an error, with a code.
func Explain(code string) (string, error) {
	for _, known := range Codes() {
		if string(known) == code {
			if page, ok := Page(known); ok {
				return page, nil
			}
		}
	}
	return "", NoSuchCode(code)
}

// Page returns the page for code, or false if it has none.
func Page(code Code) (string, bool) {
	page, err := fs.ReadFile(docs.Errors, PagePath(code))
	if err != nil {
		return "", false
	}
	return string(page), true
}

// PagePath is where code's page is in [docs.Errors].
func PagePath(code Code) string {
	return "errors/" + strings.ReplaceAll(string(code), "::", ".") + ".md"
}

// why is the paragraph under "## Why it matters" on code's page.
func why(code Code) string {
	page, ok := Page(code)
	if !ok {
		return ""
	}
	_, section, found := strings.Cut(page, "\n## Why it matters\n")
	if !found {
		return ""
	}
	section, _, _ = strings.Cut(section, "\n## ")
	return strings.Join(strings.Fields(section), " ")
}
