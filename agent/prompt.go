package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"strings"

	"github.com/ivy/almanac/hostile"
)

// Prompt is an agent's instructions: Markdown with {{ variable }} placeholders, usually
// embedded in the binary. Every placeholder is a declared variable and every declared variable
// is used, checked when the prompt is parsed, so a typo cannot reach a model as text. A
// variable's value must be clean, so hostile text never reaches a prompt's instructions
// (principle 3). Write \{{ for a literal {{.
type Prompt struct {
	name   string
	pieces []piece
	vars   []string
}

// piece is literal text, or a variable when isVar is set.
type piece struct {
	text  string
	isVar bool
}

// Vars are the values a prompt's variables take, by name.
type Vars map[string]any

// ParsePrompt reads a prompt's text and checks it against the variables it declares. Name is
// how the manifest shows it, such as the file it came from.
func ParsePrompt(name, text string, vars ...string) (Prompt, error) {
	pieces, err := parseTemplate(text)
	if err != nil {
		return Prompt{}, fmt.Errorf("prompt %s: %w", name, err)
	}
	var used []string
	for _, p := range pieces {
		if p.isVar && !slices.Contains(used, p.text) {
			used = append(used, p.text)
		}
	}
	for _, variable := range used {
		if !slices.Contains(vars, variable) {
			return Prompt{}, fmt.Errorf("prompt %s uses {{ %s }}, which is not declared: declare it, or remove it from the prompt", name, variable)
		}
	}
	for _, variable := range vars {
		if !slices.Contains(used, variable) {
			return Prompt{}, fmt.Errorf("prompt %s never uses {{ %s }}: use it in the prompt, or stop declaring it", name, variable)
		}
	}
	return Prompt{name: name, pieces: pieces, vars: used}, nil
}

// LoadPrompt is [ParsePrompt] for the file name in fsys, usually an embed.FS of prompts/.
func LoadPrompt(fsys fs.FS, name string, vars ...string) (Prompt, error) {
	text, err := fs.ReadFile(fsys, name)
	if err != nil {
		return Prompt{}, fmt.Errorf("prompt %s cannot be read: %w", name, err)
	}
	return ParsePrompt(name, string(text), vars...)
}

// MustPrompt panics if err is not nil, so that a prompt declared as a package variable fails
// the binary when it starts:
//
//	var fixer = agent.MustPrompt(agent.LoadPrompt(prompts, "fixer.md", "repo"))
func MustPrompt(p Prompt, err error) Prompt {
	if err != nil {
		panic(err)
	}
	return p
}

// Name is how the manifest shows the prompt.
func (p Prompt) Name() string { return p.name }

// Vars are the variables the prompt uses, in the order it first uses them.
func (p Prompt) Vars() []string { return slices.Clone(p.vars) }

// Render fills the prompt's variables. It needs a value for each variable and no others, and
// each value must be clean ([hostile.CheckClean]).
func (p Prompt) Render(values Vars) (string, error) {
	if p.pieces == nil {
		return "", errors.New("agent: a prompt must be made by ParsePrompt or LoadPrompt")
	}
	for name, value := range values {
		if !slices.Contains(p.vars, name) {
			return "", fmt.Errorf("prompt %s has no variable %s: pass only %s", p.name, name, strings.Join(p.vars, ", "))
		}
		if value == nil {
			return "", fmt.Errorf("prompt %s: %s has no value", p.name, name)
		}
		if err := hostile.CheckClean(reflect.TypeOf(value)); err != nil {
			return "", fmt.Errorf("prompt %s: %s: %w", p.name, name, err)
		}
	}
	var out strings.Builder
	for _, piece := range p.pieces {
		if !piece.isVar {
			out.WriteString(piece.text)
			continue
		}
		value, ok := values[piece.text]
		if !ok {
			return "", fmt.Errorf("prompt %s needs a value for %s", p.name, piece.text)
		}
		fmt.Fprint(&out, value)
	}
	return out.String(), nil
}

// parseTemplate splits text into literal text and variables. {{ name }} is a variable and
// \{{ a literal {{. Anything else in braces is an error.
func parseTemplate(text string) ([]piece, error) {
	line := func(at int) int { return strings.Count(text[:at], "\n") + 1 }
	var pieces []piece
	var literal strings.Builder
	at := 0
	for {
		found := strings.Index(text[at:], "{{")
		if found < 0 {
			break
		}
		open := at + found
		if strings.HasSuffix(text[:open], `\`) {
			literal.WriteString(text[at : open-1])
			literal.WriteString("{{")
			at = open + 2
			continue
		}
		literal.WriteString(text[at:open])
		length := strings.Index(text[open:], "}}")
		if length < 0 {
			return nil, fmt.Errorf("line %d: unclosed {{", line(open))
		}
		name := strings.TrimSpace(text[open+2 : open+length])
		if !isVariableName(name) {
			return nil, fmt.Errorf("line %d: {{ %s }} is not a variable name", line(open), name)
		}
		pieces = append(pieces, piece{text: literal.String()}, piece{text: name, isVar: true})
		literal.Reset()
		at = open + length + 2
	}
	literal.WriteString(text[at:])
	return append(pieces, piece{text: literal.String()}), nil
}

func isVariableName(name string) bool {
	if name == "" || isDigit(name[0]) {
		return false
	}
	for i := range len(name) {
		if !isLetter(name[i]) && !isDigit(name[i]) && name[i] != '_' {
			return false
		}
	}
	return true
}

func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

func isDigit(c byte) bool { return '0' <= c && c <= '9' }
