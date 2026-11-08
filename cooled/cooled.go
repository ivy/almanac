// Package cooled proves that a release has aged past a cooldown. Most malicious releases are
// found within days, so no agent reads an update until it has cooled (R-22). A status check
// can be set by anything with write access, so the proof rests only on a publish time read
// from the registry.
package cooled

import (
	"encoding/json"
	"errors"
	"time"
)

// Published is when a release was published, as a registry reports it. Many have no time.
// The zero Published is unknown.
type Published struct {
	at    time.Time
	known bool
}

// PublishedAt is a release published at t.
func PublishedAt(t time.Time) Published { return Published{at: t, known: true} }

// At is when the release was published, and false if nothing says.
func (p Published) At() (time.Time, bool) { return p.at, p.known }

// MarshalJSON writes the time in RFC 3339, or null when it is unknown.
func (p Published) MarshalJSON() ([]byte, error) {
	if !p.known {
		return []byte("null"), nil
	}
	return json.Marshal(p.at.Format(time.RFC3339Nano))
}

var errNotATime = errors.New("cooled: a publish time is an RFC 3339 time or null")

// UnmarshalJSON reads what MarshalJSON writes.
func (p *Published) UnmarshalJSON(data []byte) error {
	var text *string
	if err := json.Unmarshal(data, &text); err != nil {
		return errNotATime
	}
	if text == nil {
		*p = Published{}
		return nil
	}
	at, err := time.Parse(time.RFC3339Nano, *text)
	if err != nil {
		return errNotATime
	}
	*p = PublishedAt(at)
	return nil
}

// Cooled is proof that a release is older than a cooldown. Only [Check] and [Measure] make
// one: its field is unexported, so no other package can, and it has no JSON form, so a
// recorded or model-written value cannot stand in for it. Its zero value proves nothing, and
// whatever requires a proof refuses it.
type Cooled struct{ proven bool }

// IsZero reports whether c proves nothing, because it was not made by a check.
func (c Cooled) IsZero() bool { return !c.proven }

// Check proves the release older than minAge at now, or returns false. A release published in
// the future is not cooled, and neither is one with no publish time.
func Check(published Published, minAge time.Duration, now time.Time) (Cooled, bool) {
	at, known := published.At()
	if !known {
		return Cooled{}, false
	}
	age := now.Sub(at)
	if age <= 0 || age < minAge {
		return Cooled{}, false
	}
	return Cooled{proven: true}, true
}

// Age is what is known about how old a release is.
type Age int

const (
	// Young is a release that has not aged past the cooldown yet. Waiting will help.
	Young Age = iota
	// Unknown is a release with no publish time, so waiting will not help.
	Unknown
	// Passed is a release older than the cooldown.
	Passed
)

func (a Age) String() string {
	switch a {
	case Young:
		return "young"
	case Unknown:
		return "unknown"
	case Passed:
		return "passed"
	}
	return "invalid age"
}

// Measure is [Check] for a caller that must tell a release to wait for from one nothing can
// date. The proof is not zero only when the age is Passed.
func Measure(published Published, minAge time.Duration, now time.Time) (Age, Cooled) {
	if proof, ok := Check(published, minAge, now); ok {
		return Passed, proof
	}
	if _, known := published.At(); known {
		return Young, Cooled{}
	}
	return Unknown, Cooled{}
}
