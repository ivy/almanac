package store

import (
	"bytes"
	"encoding/json"
	"errors"
)

// nul and replacement are U+0000 and U+FFFD as JSON escapes.
var (
	nul         = []byte(`\u0000`)
	replacement = []byte(`�`)
)

// Storable is value as a store keeps it: a copy, with every U+0000 in its
// strings and keys replaced by U+FFFD. PostgreSQL's jsonb refuses U+0000, and
// payloads and recordings hold hostile text, so one crafted title would
// otherwise fail its delivery, or a run's recording, forever. Both stores keep
// the same value, so neither can be told apart by it. Anything that is not
// JSON is refused.
func Storable(value json.RawMessage) (json.RawMessage, error) {
	if !json.Valid(value) {
		return nil, errors.New("refused a value that is not JSON")
	}
	// Valid JSON holds U+0000 only as the escape \u0000, and only inside a
	// string; an escaped backslash before "u0000" is not one.
	out := make([]byte, 0, len(value))
	inString := false
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c == '"':
			inString = !inString
		case inString && c == '\\':
			if bytes.HasPrefix(value[i:], nul) {
				out = append(out, replacement...)
				i += len(nul) - 1
				continue
			}
			// Keep the escape and the character it escapes.
			out = append(out, c, value[i+1])
			i++
			continue
		}
		out = append(out, c)
	}
	return out, nil
}
