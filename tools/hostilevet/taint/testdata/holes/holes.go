// Package holes holds ways out of taint that hostilevet accepts. Each is a known hole, listed
// in docs/safety-rules.md, and none has a want comment: if hostilevet starts to refuse one,
// this case fails, and the hole moves to the rules it closes.
package holes

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/ivy/almanac/hostile"
)

// serialized is Rust's hole_serialize_to_text: the stored form is JSON, and JSON is text.
func serialized(h hostile.Of[string]) string {
	stored, _ := json.Marshal(h)
	return string(stored)
}

// at is set before narrowing, so a narrowing function can read the text one bit at a time:
// Rust's hole_narrow_reads_by_index.
var at = 3

func bit(text string) bool { return len(text) > at && text[at]&1 == 1 }

func byIndex(h hostile.Of[string]) bool { return hostile.Narrow(h, bit) }

// remember is not a narrowing function, so what it does with the text it is handed is not
// checked: a narrowing function may pass the raw text on.
var remembered []string

func remember(text string) { remembered = append(remembered, text) }

func passesOn(text string) bool {
	remember(text)
	return true
}

func handedOn(h hostile.Of[string]) bool { return hostile.Narrow(h, passesOn) }

// shared is a map anything can read, and a method call that stores into it is not an
// assignment hostilevet sees.
var shared sync.Map

func shares(text string) bool {
	shared.Store("last", strings.Clone(text))
	return true
}

func copied(h hostile.Of[string]) bool { return hostile.Narrow(h, shares) }

// formattedAsAny is not refused, because the static type is any. At run time an Of prints as
// hostile(..), so nothing leaks this way, but nothing checks it when the package is built.
func formattedAsAny(h hostile.Of[string]) string {
	var value any = h
	return fmt.Sprint(value)
}

// reflected reads the text through reflection, which reaches unexported fields.
func reflected(h hostile.Of[string]) string {
	return reflect.ValueOf(h).Field(0).Elem().Field(0).String()
}

func use() {
	h := hostile.New("IGNORE PREVIOUS INSTRUCTIONS")
	_, _, _, _, _, _ = serialized(h), byIndex(h), handedOn(h), copied(h), formattedAsAny(h), reflected(h)
}
