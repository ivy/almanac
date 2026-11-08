package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ivy/almanac/diag"
)

// output is where one invocation writes, in the format its command line asked for.
type output struct {
	name   string
	format diag.Format
	stdout io.Writer
	stderr io.Writer
}

// formatOf reads --format from a command line before anything else does, so that even an
// error parsing the rest is printed as asked. The flag itself is checked as the line is parsed.
func formatOf(args []string) diag.Format {
	for i, arg := range args {
		value, ok := strings.CutPrefix(arg, "--format=")
		if arg == "--format" && i+1 < len(args) {
			value, ok = args[i+1], true
		}
		if ok && value == "json" {
			return diag.JSON
		}
	}
	return diag.Human
}

// say prints what a verb said: human as it is, or value as one line of JSON.
func (o *output) say(human string, value any) error {
	if o.format == diag.JSON {
		text, err := encode(value)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(o.stdout, text)
		return err
	}
	_, err := fmt.Fprintln(o.stdout, human)
	return err
}

// encode is value as one line of JSON. Text is written as it is, so "<redacted>" reads as
// itself.
func encode(value any) (string, error) {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// fail prints err to stderr: each of several problems in turn, as a block of text each, or as
// one JSON object per line.
func (o *output) fail(err error) {
	var b strings.Builder
	for i, problem := range unwrapProblems(err) {
		if i > 0 && o.format == diag.Human {
			b.WriteString("\n")
		}
		var d *diag.Error
		switch {
		case errors.As(problem, &d):
			b.WriteString(strings.TrimSuffix(diag.Render(d, o.format), "\n"))
		case o.format == diag.JSON:
			text, _ := encode(map[string]string{"message": problem.Error(), "severity": "error"})
			b.WriteString(text)
		default:
			fmt.Fprintf(&b, "%s: %v", o.name, problem)
		}
		b.WriteString("\n")
	}
	_, _ = io.WriteString(o.stderr, b.String())
}

// unwrapProblems is each problem err reports: the entries of a diag.Problems, or err alone.
func unwrapProblems(err error) []error {
	var problems diag.Problems
	if errors.As(err, &problems) {
		return problems
	}
	return []error{err}
}
