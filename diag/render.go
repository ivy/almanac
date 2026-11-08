package diag

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Format is how an error is written. Agents ask for JSON.
type Format int

const (
	// Human is text without colour, so it is stable and safe to log.
	Human Format = iota
	// JSON is one object per error, for a program to read.
	JSON
)

// width is where human output wraps.
const width = 100

// Render writes e in format. Both formats carry the same facts: the code, what
// happened, what lies underneath, where in a file, why it matters, and what to
// do next.
func Render(e *Error, format Format) string {
	if format == JSON {
		return renderJSON(e)
	}
	return renderHuman(e)
}

func causes(e *Error) []string {
	if e.Cause == nil || e.Cause.Error() == e.Message {
		return []string{}
	}
	return []string{e.Cause.Error()}
}

func renderHuman(e *Error) string {
	var b strings.Builder
	b.WriteString(string(e.Code) + "\n\n")
	wrap(&b, "  × ", "  │ ", e.Message)
	for _, cause := range causes(e) {
		wrap(&b, "  ╰─▶ ", "      ", cause)
	}
	if e.At != nil {
		snippet(&b, e.At)
	}
	if why := e.Why(); why != "" {
		wrap(&b, "  why: ", "       ", why)
	}
	wrap(&b, "  help: ", "        ", e.Help)
	return b.String()
}

// wrap writes text in lines no wider than width, the first after first and the
// rest after rest.
func wrap(b *strings.Builder, first, rest, text string) {
	prefix := first
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && utf8.RuneCountInString(prefix+line+" "+word) > width {
			b.WriteString(prefix + line + "\n")
			prefix, line = rest, ""
		}
		if line == "" {
			line = word
		} else {
			line += " " + word
		}
	}
	b.WriteString(prefix + line + "\n")
}

// snippet draws the labelled line with one line either side:
//
//	  ╭─[almanac.toml:2:1]
//	1 │ listen = "127.0.0.1:1"
//	2 │ max_runs = 3
//	  · ────┬───
//	  ·     ╰── Almanac does not read this
//	  ╰────
func snippet(b *strings.Builder, at *Label) {
	lines := strings.Split(strings.TrimSuffix(at.Source.Text, "\n"), "\n")
	row, col, length := locate(lines, at.Span)
	first, last := max(row-1, 0), min(row+1, len(lines)-1)
	gutter := len(fmt.Sprint(last + 1))
	margin := strings.Repeat(" ", gutter+2)

	fmt.Fprintf(b, "%s╭─[%s:%d:%d]\n", margin, at.Source.Name, row+1, col+1)
	for i := first; i <= last; i++ {
		b.WriteString(strings.TrimRight(fmt.Sprintf(" %*d │ %s", gutter, i+1, lines[i]), " ") + "\n")
		if i != row {
			continue
		}
		mid := length / 2
		indent := strings.Repeat(" ", col)
		underline := strings.Repeat("─", mid) + "┬" + strings.Repeat("─", length-mid-1)
		fmt.Fprintf(b, "%s· %s%s\n", margin, indent, underline)
		fmt.Fprintf(b, "%s· %s%s╰── %s\n", margin, indent, strings.Repeat(" ", mid), at.Text)
	}
	b.WriteString(margin + "╰────\n")
}

// locate turns a byte span into a line, a column and a length in characters.
// A span that runs past its line is cut at the line's end, and an empty one
// still gets a mark.
func locate(lines []string, span Span) (row, col, length int) {
	start := 0
	for row = range lines {
		end := start + len(lines[row])
		if span.Offset <= end || row == len(lines)-1 {
			break
		}
		start = end + 1
	}
	line := lines[row]
	from := min(max(span.Offset-start, 0), len(line))
	to := min(from+max(span.Length, 0), len(line))
	col = utf8.RuneCountInString(line[:from])
	length = max(utf8.RuneCountInString(line[from:to]), 1)
	return row, col, length
}

type jsonError struct {
	Code     Code        `json:"code"`
	Message  string      `json:"message"`
	Severity string      `json:"severity"`
	Causes   []string    `json:"causes"`
	Filename string      `json:"filename"`
	Labels   []jsonLabel `json:"labels"`
	Why      string      `json:"why"`
	Help     string      `json:"help"`
}

type jsonLabel struct {
	Label string   `json:"label"`
	Span  jsonSpan `json:"span"`
}

// jsonSpan carries the line and column as well as the offset, so a reader can
// find the place without counting bytes.
type jsonSpan struct {
	Offset int `json:"offset"`
	Length int `json:"length"`
	Line   int `json:"line"`
	Column int `json:"column"`
}

func renderJSON(e *Error) string {
	out := jsonError{
		Code:     e.Code,
		Message:  e.Message,
		Severity: "error",
		Causes:   causes(e),
		Labels:   []jsonLabel{},
		Why:      e.Why(),
		Help:     e.Help,
	}
	if e.At != nil {
		lines := strings.Split(strings.TrimSuffix(e.At.Source.Text, "\n"), "\n")
		row, col, _ := locate(lines, e.At.Span)
		out.Filename = e.At.Source.Name
		out.Labels = append(out.Labels, jsonLabel{
			Label: e.At.Text,
			Span:  jsonSpan{Offset: e.At.Span.Offset, Length: e.At.Span.Length, Line: row + 1, Column: col + 1},
		})
	}
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	// "<redacted>" should read as itself, not as <redacted>.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(out); err != nil {
		return fmt.Sprintf(`{"code":%q,"message":%q}`, e.Code, e.Message)
	}
	return strings.TrimSuffix(b.String(), "\n")
}
