// Package advisory reads vulnerability advisories, and checks whether an update really is the
// fix one names (R-10). Summaries from a feed may be model-written, so all text stays hostile:
// only identifiers, ranges and flags are clean.
package advisory

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ivy/almanac/hostile"
)

// Range is affected from Introduced up to, but not including, Fixed. A nil bound is open.
type Range struct {
	Introduced *hostile.Slug `json:"introduced"`
	Fixed      *hostile.Slug `json:"fixed"`
}

// Affected is a package an advisory names, and the versions of it that are affected.
type Affected struct {
	Ecosystem hostile.Slug `json:"ecosystem"`
	Package   hostile.Slug `json:"package"`
	Ranges    []Range      `json:"ranges"`
}

// Advisory is one vulnerability, as a feed describes it.
type Advisory struct {
	ID       hostile.Slug       `json:"id"`
	Aliases  []hostile.Slug     `json:"aliases"`
	Severity Severity           `json:"severity"`
	KEV      bool               `json:"kev"`
	Affected []Affected         `json:"affected"`
	Summary  hostile.Of[string] `json:"summary"`
}

// Severity is how bad an advisory says its vulnerability is. The zero value is [Unknown].
type Severity int

const (
	// Unknown is an advisory with no score.
	Unknown Severity = iota
	// Low is a CVSS score below 4.
	Low
	// Medium is a CVSS score from 4 up to 7.
	Medium
	// High is a CVSS score from 7 up to 9.
	High
	// Critical is a CVSS score of 9 or more.
	Critical
)

var severities = []string{Unknown: "unknown", Low: "low", Medium: "medium", High: "high", Critical: "critical"}

func (s Severity) String() string { return name(severities, int(s)) }

// MarshalText writes the severity by name.
func (s Severity) MarshalText() ([]byte, error) { return marshalName(severities, int(s)) }

// UnmarshalText reads a severity written by MarshalText.
func (s *Severity) UnmarshalText(text []byte) error {
	i, err := unmarshalName(severities, text, "severity")
	*s = Severity(i)
	return err
}

// severityOf rates a CVSS score.
func severityOf(cvss float64) Severity {
	switch {
	case cvss >= 9:
		return Critical
	case cvss >= 7:
		return High
	case cvss >= 4:
		return Medium
	default:
		return Low
	}
}

// Validation is whether an update does what an advisory says fixes it. Only [Valid] proves
// anything; the zero value is [Incomparable].
type Validation int

const (
	// Incomparable is versions that could not be compared, so nothing is proved.
	Incomparable Validation = iota
	// Valid is an update from an affected version to the version that fixes it.
	Valid
	// PackageNotListed is a package the advisory does not name.
	PackageNotListed
	// NotAffected is a current version outside every affected range.
	NotAffected
	// TargetStillAffected is a target still inside an affected range, or below its fix.
	TargetStillAffected
	// NotThePatchedVersion is a target past the patched version, so more than the fix.
	NotThePatchedVersion
)

var validations = []string{
	Incomparable:         "incomparable",
	Valid:                "valid",
	PackageNotListed:     "package not listed",
	NotAffected:          "not affected",
	TargetStillAffected:  "target still affected",
	NotThePatchedVersion: "not the patched version",
}

func (v Validation) String() string { return name(validations, int(v)) }

// MarshalText writes the validation by name.
func (v Validation) MarshalText() ([]byte, error) { return marshalName(validations, int(v)) }

// UnmarshalText reads a validation written by MarshalText.
func (v *Validation) UnmarshalText(text []byte) error {
	i, err := unmarshalName(validations, text, "validation")
	*v = Validation(i)
	return err
}

// Validate checks an update of package in ecosystem, from current to target, against an
// advisory. It is [Valid] only when current is inside an affected range and target is exactly
// the version that range names as fixed. Anything unclear fails closed.
func Validate(advisory Advisory, ecosystem, pkg, current, target string) Validation {
	i := slices.IndexFunc(advisory.Affected, func(a Affected) bool {
		return equalFoldASCII(a.Ecosystem.String(), ecosystem) && a.Package.String() == pkg
	})
	if i < 0 {
		return PackageNotListed
	}
	for _, r := range advisory.Affected[i].Ranges {
		currentInside, ok := inside(r, ecosystem, current)
		if !ok {
			return Incomparable
		}
		if !currentInside {
			continue
		}
		targetInside, ok := inside(r, ecosystem, target)
		if !ok {
			return Incomparable
		}
		if targetInside || r.Fixed == nil {
			return TargetStillAffected
		}
		order, ok := Compare(ecosystem, target, r.Fixed.String())
		switch {
		case !ok:
			return Incomparable
		case order == 0:
			return Valid
		case order > 0:
			return NotThePatchedVersion
		default:
			return TargetStillAffected
		}
	}
	return NotAffected
}

// inside reports whether version is in r, and false for ok if that cannot be said.
func inside(r Range, ecosystem, version string) (in, ok bool) {
	atOrAfter, beforeFix := true, true
	if r.Introduced != nil {
		order, ok := Compare(ecosystem, version, r.Introduced.String())
		if !ok {
			return false, false
		}
		atOrAfter = order >= 0
	}
	if r.Fixed != nil {
		order, ok := Compare(ecosystem, version, r.Fixed.String())
		if !ok {
			return false, false
		}
		beforeFix = order < 0
	}
	return atOrAfter && beforeFix, true
}

func name(names []string, i int) string {
	if i < 0 || i >= len(names) {
		return "invalid(" + strconv.Itoa(i) + ")"
	}
	return names[i]
}

func marshalName(names []string, i int) ([]byte, error) {
	if i < 0 || i >= len(names) {
		return nil, fmt.Errorf("advisory: %d is not one of %s", i, strings.Join(names, ", "))
	}
	return []byte(names[i]), nil
}

// unmarshalName finds text among names. The error does not repeat text, which may be hostile.
func unmarshalName(names []string, text []byte, what string) (int, error) {
	i := slices.Index(names, string(text))
	if i < 0 {
		return 0, fmt.Errorf("advisory: not a %s; want one of %s", what, strings.Join(names, ", "))
	}
	return i, nil
}

// equalFoldASCII compares two strings ignoring the case of ASCII letters only.
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		if lowerASCII(a[i]) != lowerASCII(b[i]) {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// Advisories is where advisories come from.
type Advisories interface {
	// ByID finds an advisory by GHSA or CVE ID. It returns false when the feed does not know it.
	ByID(ctx context.Context, id hostile.Slug) (Advisory, bool, error)
	// ByPackage finds the advisories that name a package.
	ByPackage(ctx context.Context, ecosystem, pkg hostile.Slug) ([]Advisory, error)
}

// Fake is a feed held in memory, for tests.
type Fake struct {
	mu    sync.Mutex
	known map[string]Advisory
}

// NewFake returns a feed that knows advisories.
func NewFake(advisories ...Advisory) *Fake {
	f := &Fake{known: map[string]Advisory{}}
	for _, a := range advisories {
		f.With(a)
	}
	return f
}

// With adds an advisory to the feed, replacing one with the same ID.
func (f *Fake) With(a Advisory) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.known[a.ID.String()] = a
	return f
}

// ByID finds an advisory by the ID it was added with.
func (f *Fake) ByID(_ context.Context, id hostile.Slug) (Advisory, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.known[id.String()]
	return a, ok, nil
}

// ByPackage finds the advisories that name exactly this ecosystem and package, by ID.
func (f *Fake) ByPackage(_ context.Context, ecosystem, pkg hostile.Slug) ([]Advisory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var found []Advisory
	for _, a := range f.known {
		if slices.ContainsFunc(a.Affected, func(x Affected) bool {
			return x.Ecosystem == ecosystem && x.Package == pkg
		}) {
			found = append(found, a)
		}
	}
	slices.SortFunc(found, func(a, b Advisory) int { return strings.Compare(a.ID.String(), b.ID.String()) })
	return found, nil
}

var _ Advisories = (*Fake)(nil)
