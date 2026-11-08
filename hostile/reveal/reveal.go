// Package reveal reads the value inside a hostile.Of. It is the deliberate, greppable way
// out of taint, for the boundary packages that hand hostile text to something outside
// Almanac: a forge's API, an issue body quoted as inert text, a patch checked by policy.
//
// tools/hostilevet refuses importing this package anywhere but those boundaries, which it
// lists by name. A package that needs it is added there, in a reviewed change.
package reveal

import (
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/hostile/internal/seal"
)

// Value returns the value inside h. What it returns is hostile, whatever its type says.
func Value[T any](h hostile.Of[T]) T {
	return seal.Open(h).(T)
}
