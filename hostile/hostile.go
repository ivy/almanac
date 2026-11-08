// Package hostile keeps text an attacker may have written apart from code that acts on it
// (principle 3 in docs/core-principles.md).
//
// Everything read from outside is an [Of]. It has no exported field and no accessor, and it
// prints as a placeholder. It leaves taint only through [Narrow], which answers a yes-or-no
// question, or [Classify], which answers with a value from a [Closed] set. [Map], [Zip] and
// [Join] transform hostile values and keep them hostile.
//
// The functions that read a hostile value take a named function, not a closure, and that
// function reads the raw text, so it is trusted code that is reviewed like any other
// (docs/keeping-agents-safe.md). tools/hostilevet enforces both, and refuses formatting a
// hostile value.
package hostile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/ivy/almanac/hostile/internal/seal"
)

// tag is the JSON key a hostile value is stored under, so that a recorded run decodes back
// into an Of and never into a plain string.
const tag = "$untrusted"

// placeholder is what a hostile value prints as, whatever the verb.
const placeholder = "hostile(..)"

// Of is a value read from outside, of type T. The zero value holds T's zero value.
type Of[T any] struct {
	// The value sits behind a pointer so that fmt, reaching an Of through a struct field it
	// cannot call methods on, prints an address and not the text.
	box *box[T]
}

type box[T any] struct{ value T }

func init() {
	seal.Open = func(h any) any { return h.(opener).open() }
}

// opener is the one way inside an Of, reachable only through hostile/reveal.
type opener interface{ open() any }

func (h Of[T]) open() any { return h.value() }

func (h Of[T]) value() T {
	if h.box == nil {
		var zero T
		return zero
	}
	return h.box.value
}

// New marks value as hostile. Wrapping only widens taint, so anything may do it.
func New[T any](value T) Of[T] {
	return Of[T]{box: &box[T]{value: value}}
}

// Map transforms a hostile value. The result is hostile too.
func Map[T, U any](h Of[T], f func(T) U) Of[U] {
	return New(f(h.value()))
}

// Narrow answers a yes-or-no question about a hostile value. The answer is clean.
func Narrow[T any](h Of[T], f func(T) bool) bool {
	return f(h.value())
}

// Classify sorts a hostile value into a closed set of values. The answer is clean.
func Classify[T any, C Finite](h Of[T], f func(T) C) C {
	return f(h.value())
}

// Pair is two values held together, so that one narrowing function can compare them.
type Pair[A, B any] struct {
	First  A
	Second B
}

// Zip holds two hostile values together. The pair is hostile.
func Zip[A, B any](a Of[A], b Of[B]) Of[Pair[A, B]] {
	return New(Pair[A, B]{First: a.value(), Second: b.value()})
}

// Join puts clean text before hostile text. The whole is hostile.
func Join(clean string, h Of[string]) Of[string] {
	return New(clean + h.value())
}

// Format prints a placeholder for every verb, so fmt never shows the text.
func (h Of[T]) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, placeholder)
}

// LogValue logs a placeholder, so slog never shows the text.
func (h Of[T]) LogValue() slog.Value {
	return slog.StringValue(placeholder)
}

// MarshalJSON stores the value under a tag, so that a stored run decodes back into an Of.
func (h Of[T]) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]T{tag: h.value()})
}

// errNotTagged never quotes the JSON it refused, because that JSON may be hostile.
var errNotTagged = errors.New(`hostile value: want an object holding only "` + tag + `"`)

// UnmarshalJSON accepts only the tagged form MarshalJSON writes.
func (h *Of[T]) UnmarshalJSON(data []byte) error {
	var tagged map[string]json.RawMessage
	if err := json.Unmarshal(data, &tagged); err != nil || tagged == nil {
		return errNotTagged
	}
	raw, ok := tagged[tag]
	if !ok || len(tagged) != 1 {
		return errNotTagged
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("hostile value: the tagged value is not a %T", value)
	}
	*h = New(value)
	return nil
}
