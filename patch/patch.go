// Package patch is what a Fixer hands back, and the deterministic check that decides whether it
// may be pushed (R-20). A patch is a list of file changes, not diff text, so nothing parses
// hostile diff syntax, and a rename is a delete and a write, so both of its paths are checked.
package patch

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// Patch is a set of file changes. An agent writes it, so it is always held as a hostile.Of
// until [Policy.Check] accepts it.
type Patch struct {
	Changes []Change `json:"changes"`
	// Diff is the same changes as a diff, for people to read. It is never applied.
	Diff string `json:"diff"`
	// Answer is what the agent said it did, for people to read. An agent that changed nothing
	// says why here.
	Answer json.RawMessage `json:"answer,omitempty"`
	// Usage is what the session used, kept in the run log so its cost can be read later.
	Usage json.RawMessage `json:"usage,omitempty"`
}

// Op is what a change does to a file.
type Op int

const (
	// Write sets a file's content.
	Write Op = iota + 1
	// Delete removes a file.
	Delete
	// Link makes a symbolic link. It is always refused: a link can point a later write
	// somewhere else.
	Link
)

var opNames = map[Op]string{Write: "Write", Delete: "Delete", Link: "Link"}

func (o Op) String() string {
	if name, ok := opNames[o]; ok {
		return name
	}
	return "invalid op"
}

// Change is one file's new state. Its path and content are hostile.
type Change struct {
	Op   Op
	Path string
	// Content is the file's new content in base64, because a patch can change binary files.
	// Only a Write has content.
	Content    string
	Executable bool
}

// changeJSON is a change's stored form: an object named by its op.
type changeJSON struct {
	Path       string  `json:"path"`
	Content    *string `json:"content,omitempty"`
	Executable *bool   `json:"executable,omitempty"`
}

// MarshalJSON writes a change as {"Write": {"path": ..., "content": ..., "executable": ...}},
// {"Delete": {"path": ...}} or {"Link": {"path": ...}}.
func (c Change) MarshalJSON() ([]byte, error) {
	body := changeJSON{Path: c.Path}
	if c.Op == Write {
		body.Content, body.Executable = &c.Content, &c.Executable
	}
	name, ok := opNames[c.Op]
	if !ok {
		return nil, errors.New("patch: a change with no op has no stored form")
	}
	return json.Marshal(map[string]changeJSON{name: body})
}

// errNotAChange never quotes the JSON it refused, because an agent wrote it.
var errNotAChange = errors.New(`patch: a change is an object holding one of "Write", "Delete" or "Link"`)

// UnmarshalJSON reads what MarshalJSON writes, and refuses anything else.
func (c *Change) UnmarshalJSON(data []byte) error {
	var named map[string]changeJSON
	if err := json.Unmarshal(data, &named); err != nil || len(named) != 1 {
		return errNotAChange
	}
	for op, name := range opNames {
		body, ok := named[name]
		if !ok {
			continue
		}
		*c = Change{Op: op, Path: body.Path}
		if op == Write {
			if body.Content == nil {
				return errNotAChange
			}
			c.Content = *body.Content
			c.Executable = body.Executable != nil && *body.Executable
		}
		return nil
	}
	return errNotAChange
}

// Keeps reports whether line is still in the file at path after this patch. A file the patch
// leaves alone keeps its lines, and a line that only moved, gained indentation or lost a
// trailing comma is kept.
func (p Patch) Keeps(path, line string) bool {
	for _, change := range p.Changes {
		if change.Path != path {
			continue
		}
		if change.Op != Write || !holdsLine(change.Content, line) {
			return false
		}
	}
	return true
}

func holdsLine(content, line string) bool {
	decoded, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return false
	}
	want := bareLine(line)
	for kept := range strings.Lines(strings.ToValidUTF8(string(decoded), "�")) {
		if bareLine(kept) == want {
			return true
		}
	}
	return false
}

// bareLine is a line as a comparison sees it: indentation and a trailing comma move with the
// lines around it.
func bareLine(line string) string {
	return strings.TrimRight(strings.TrimSpace(line), ",")
}
