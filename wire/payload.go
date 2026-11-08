// Package wire is the boundary between raw payloads and typed events, and the only place
// outside text becomes a Go value. A payload's text comes out hostile; only numbers, bools,
// validated text and closed sets come out as they are.
package wire

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/ivy/almanac/hostile"
)

// Malformed is a payload field that was missing or the wrong shape. It names the field and
// never repeats its value, which may be hostile.
type Malformed struct {
	// Pointer is the field's JSON pointer (RFC 6901). A list item's field is written
	// list[]field. It is empty when the payload as a whole was not JSON.
	Pointer string
}

func (m *Malformed) Error() string {
	field := m.Pointer
	if field == "" {
		field = "the payload"
	}
	return field + " is missing or the wrong shape"
}

// Payload is a raw payload being read into an event. Each read returns a clean value or
// hostile text, never a plain string. The first read that fails is kept as [Payload.Err], and
// every read after it returns a zero value, so a parser reads each field and checks once.
type Payload struct {
	root any
	err  error
}

func newPayload(raw []byte) (*Payload, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return nil, &Malformed{}
	}
	return &Payload{root: root}, nil
}

// Err is the first read that failed, as a *[Malformed], or nil.
func (p *Payload) Err() error { return p.err }

// Text reads the string at pointer, as hostile text.
func (p *Payload) Text(pointer string) hostile.Of[string] {
	return hostile.New(read[string](p, p.root, pointer, pointer))
}

// OptionalText reads the string at pointer, as hostile text. A missing or null value is nil.
func (p *Payload) OptionalText(pointer string) hostile.Of[*string] {
	found, ok := lookup(p.root, pointer)
	if !ok || found == nil {
		return hostile.New[*string](nil)
	}
	text := read[string](p, p.root, pointer, pointer)
	return hostile.New(&text)
}

// TextList reads field from each item of the list at pointer, as hostile text. A missing list
// is empty.
func (p *Payload) TextList(pointer, field string) hostile.Of[[]string] {
	return hostile.New(readList[string](p, pointer, field))
}

// Field reads the value at pointer, which must be a number, a bool, or validated text.
func Field[T hostile.Clean](p *Payload, pointer string) T {
	return read[T](p, p.root, pointer, pointer)
}

// List reads field from each item of the list at pointer. A missing list is empty.
func List[T hostile.Clean](p *Payload, pointer, field string) []T {
	return readList[T](p, pointer, field)
}

// Member reads the value at pointer into a closed set. C decodes itself, choosing among its
// declared values.
func Member[C hostile.Finite, P interface {
	*C
	json.Unmarshaler
}](p *Payload, pointer string) C {
	return read[C](p, p.root, pointer, pointer)
}

// read decodes the value at pointer below from, naming the field as name if it fails.
func read[T any](p *Payload, from any, pointer, name string) T {
	var value T
	if p.err != nil {
		return value
	}
	found, ok := lookup(from, pointer)
	if !ok || found == nil || decode(found, &value) != nil {
		p.err = &Malformed{Pointer: name}
		var zero T
		return zero
	}
	return value
}

func readList[T any](p *Payload, pointer, field string) []T {
	found, _ := lookup(p.root, pointer)
	items, _ := found.([]any)
	values := make([]T, 0, len(items))
	for _, item := range items {
		values = append(values, read[T](p, item, field, pointer+"[]"+field))
	}
	if p.err != nil {
		return nil
	}
	return values
}

// decode reads a value decoded as any into a typed one, through JSON, so each type's own
// decoding and validation apply.
func decode(found, into any) error {
	encoded, err := json.Marshal(found)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, into)
}

// lookup finds the value at a JSON pointer (RFC 6901).
func lookup(root any, pointer string) (any, bool) {
	if pointer == "" {
		return root, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	current := root
	for _, token := range tokens(pointer) {
		switch node := current.(type) {
		case map[string]any:
			next, ok := node[token]
			if !ok {
				return nil, false
			}
			current = next
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(node) || token != strconv.Itoa(index) {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	return current, true
}

// unescape undoes a JSON pointer's escapes for '/' and '~'.
var unescape = strings.NewReplacer("~1", "/", "~0", "~")

// tokens splits a JSON pointer into its unescaped reference tokens.
func tokens(pointer string) []string {
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	for i, part := range parts {
		parts[i] = unescape.Replace(part)
	}
	return parts
}
