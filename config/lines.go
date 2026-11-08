package config

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/ivy/almanac/diag"
)

// The TOML decoder says whether a file is valid and what it holds, but not
// where each key is written. These lines say where, so an error can point at
// a key and a secret can be hidden before anything shows the file. They read
// TOML only as far as that needs, and they work on a file that does not parse.

// secretWords are the words that make a key look like a credential.
var secretWords = []string{"token", "secret", "password", "credential", "key"}

// inlineSecret finds a secret-looking key assigned inside a value, as in
// `github = { token = "..." }`.
var inlineSecret = regexp.MustCompile(`(?i)[\w\-"']*(token|secret|password|credential|key)[\w\-"']*\s*=`)

func looksSecret(key string) bool {
	key = strings.ToLower(key)
	for _, word := range secretWords {
		if strings.Contains(key, word) {
			return true
		}
	}
	return false
}

// region is a run of bytes within a line.
type region struct{ from, to int }

// line is one line of a config file.
type line struct {
	text   string // without its line ending
	offset int    // of the line's first byte in the file
	// path is the key the line assigns, or the table it opens, with every
	// table above it. Nil for any other line.
	path   []string
	header bool
	key    region // the key or table name as written
	value  region // the value as written, without a trailing comment
	// secret is set on a line whose value is, holds, or continues a secret.
	secret bool
	// secretKey and secretAt name and mark the secret this line assigns,
	// if it assigns one.
	secretKey string
	secretAt  region
	// continued is set on a line inside a value that began on an earlier line.
	continued bool
}

// mark is where r is in the whole file.
func (l line) mark(r region) diag.Span {
	return diag.Span{Offset: l.offset + r.from, Length: r.to - r.from}
}

// lines splits a config file into lines, noting the keys and secrets on each.
func lines(text string) []line {
	var (
		out    []line
		table  []string
		open   continuation // what a value begun on an earlier line still waits for
		secret bool         // whether that value is a secret
		offset int
	)
	for raw := range strings.SplitAfterSeq(text, "\n") {
		l := line{text: strings.TrimRight(raw, "\r\n"), offset: offset}
		offset += len(raw)
		if raw == "" {
			continue
		}
		trimmed := strings.TrimSpace(l.text)
		switch {
		case open.waiting():
			l.continued, l.secret = true, secret
			open.feed(l.text)
		case trimmed == "" || strings.HasPrefix(trimmed, "#"):
		case strings.HasPrefix(trimmed, "["):
			l.header = true
			l.key = trim(l.text, region{strings.IndexByte(l.text, '['), closing(l.text)})
			table = keyPath(l.text[l.key.from:l.key.to])
			l.path = table
		default:
			eq := equals(l.text)
			if eq < 0 {
				break
			}
			l.key = trim(l.text, region{0, eq})
			l.value = trim(l.text, region{eq + 1, comment(l.text, eq+1)})
			l.path = append(append([]string{}, table...), keyPath(l.text[l.key.from:l.key.to])...)
			l.secretKey, l.secretAt = secretIn(l)
			l.secret = l.secretKey != ""
			open = continuation{}
			open.feed(l.text[eq+1:])
			secret = l.secret
		}
		out = append(out, l)
	}
	return out
}

// secretIn finds the secret an assignment line holds: its own key, a table
// above it, or a key inside an inline table or an array of them.
func secretIn(l line) (string, region) {
	for _, part := range l.path {
		if looksSecret(part) {
			return strings.Join(l.path, "."), l.key
		}
	}
	value := l.text[l.value.from:l.value.to]
	if !strings.HasPrefix(value, "{") && !strings.HasPrefix(value, "[") {
		return "", region{}
	}
	if m := inlineSecret.FindStringIndex(value); m != nil {
		at := trim(l.text, region{l.value.from + m[0], l.value.from + m[1] - 1})
		inner := keyPath(l.text[at.from:at.to])
		return strings.Join(append(append([]string{}, l.path...), inner...), "."), at
	}
	return "", region{}
}

// redact hides the value of every secret line, keeping every key where it was,
// so an error can show the file without the secret it warns about.
func redact(all []line) string {
	var b strings.Builder
	for _, l := range all {
		switch {
		case l.continued && l.secret:
			b.WriteString("<redacted>")
		case l.secret:
			// Keep the secret's key, even inside an inline table, so the
			// error's mark still lands on it.
			cut := l.value.from
			if l.secretAt != l.key {
				cut = l.secretAt.to
			}
			b.WriteString(strings.TrimRight(l.text[:cut], " \t="))
			b.WriteString(" = <redacted>")
		default:
			b.WriteString(l.text)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// keyPath splits a TOML key such as `a."b.c".d` into its parts.
func keyPath(key string) []string {
	var parts []string
	var quote byte
	start := 0
	for i := 0; i < len(key); i++ {
		switch c := key[i]; {
		case quote != 0 && c == '\\' && quote == '"':
			i++
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
		case c == '"' || c == '\'':
			quote = c
		case c == '.':
			parts = append(parts, unquote(key[start:i]))
			start = i + 1
		}
	}
	return append(parts, unquote(key[start:]))
}

func unquote(part string) string {
	part = strings.TrimSpace(part)
	if len(part) >= 2 && part[0] == '\'' && part[len(part)-1] == '\'' {
		return part[1 : len(part)-1]
	}
	if strings.HasPrefix(part, `"`) {
		if s, err := strconv.Unquote(part); err == nil {
			return s
		}
	}
	return part
}

// equals finds the `=` that ends a line's key, skipping any inside a quoted key.
func equals(text string) int {
	var quote byte
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
		case c == '"' || c == '\'':
			quote = c
		case c == '=':
			return i
		}
	}
	return -1
}

// closing finds the `]` that ends a table header, or the end of the line.
func closing(text string) int {
	if i := strings.IndexByte(text, ']'); i >= 0 {
		return i
	}
	return len(text)
}

// comment finds where a trailing comment begins after from, or the end of the line.
func comment(text string, from int) int {
	var quote byte
	for i := from; i < len(text); i++ {
		switch c := text[i]; {
		case quote == '"' && c == '\\':
			i++
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			return i
		}
	}
	return len(text)
}

// trim narrows r to leave out the spaces and brackets around it.
func trim(text string, r region) region {
	for r.from < r.to && strings.ContainsRune(" \t[", rune(text[r.from])) {
		r.from++
	}
	for r.to > r.from && strings.ContainsRune(" \t]", rune(text[r.to-1])) {
		r.to--
	}
	return r
}

// continuation follows a value that may run over several lines: a multi-line string,
// or an array or inline table left open.
type continuation struct {
	delimiter string // `"""` or `'''` while inside a multi-line string
	depth     int    // brackets and braces left open
}

func (v continuation) waiting() bool { return v.delimiter != "" || v.depth > 0 }

// feed reads text that continues the value.
func (v *continuation) feed(text string) {
	for i := 0; i < len(text); i++ {
		if v.delimiter != "" {
			end := strings.Index(text[i:], v.delimiter)
			if end < 0 {
				return
			}
			i += end + len(v.delimiter) - 1
			v.delimiter = ""
			continue
		}
		switch rest := text[i:]; {
		case strings.HasPrefix(rest, `"""`), strings.HasPrefix(rest, `'''`):
			v.delimiter = rest[:3]
			i += 2
		case rest[0] == '"' || rest[0] == '\'':
			end := closingQuote(rest)
			if end < 0 {
				return
			}
			i += end
		case rest[0] == '#':
			return
		case rest[0] == '[' || rest[0] == '{':
			v.depth++
		case rest[0] == ']' || rest[0] == '}':
			v.depth = max(v.depth-1, 0)
		}
	}
}

// closingQuote finds the quote that ends the single-line string text begins with.
func closingQuote(text string) int {
	quote := text[0]
	for i := 1; i < len(text); i++ {
		switch {
		case quote == '"' && text[i] == '\\':
			i++
		case text[i] == quote:
			return i
		}
	}
	return -1
}
