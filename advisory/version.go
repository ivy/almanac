package advisory

import (
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

// Compare orders two versions of a package in ecosystem, named as OSV names it, and returns
// -1, 0 or +1 as a is before, equal to, or after b. It returns false when the order cannot be
// said, which a caller must treat as not proved.
//
// npm, crates.io, Go, Packagist and RubyGems compare as semantic versions by precedence, so
// build metadata is ignored. A leading v is dropped, and a shortened version such as 1.2 is
// read as 1.2.0. PyPI compares plain dotted releases only, padded with zeros. Any other
// ecosystem only knows that two identical versions are equal.
func Compare(ecosystem, a, b string) (int, bool) {
	switch strings.ToLower(ecosystem) {
	case "npm", "crates.io", "go", "packagist", "rubygems":
		x, ok := semanticVersion(a)
		if !ok {
			return 0, false
		}
		y, ok := semanticVersion(b)
		if !ok {
			return 0, false
		}
		return semver.Compare(x, y), true
	case "pypi":
		return compareDotted(a, b)
	default:
		if a == b {
			return 0, true
		}
		return 0, false
	}
}

// semanticVersion returns text in the form golang.org/x/mod/semver reads, if it is a semantic
// version. That package reads v1 and v1.2 as v1.0.0 and v1.2.0, and nothing else shortened.
func semanticVersion(text string) (string, bool) {
	v := "v" + strings.TrimPrefix(text, "v")
	if !semver.IsValid(v) {
		return "", false
	}
	// x/mod/semver reads numbers of any size. A version whose major, minor or patch number does
	// not fit in 64 bits is not compared.
	release, _, _ := strings.Cut(strings.TrimPrefix(semver.Canonical(v), "v"), "-")
	for part := range strings.SplitSeq(release, ".") {
		if _, err := strconv.ParseUint(part, 10, 64); err != nil {
			return "", false
		}
	}
	return v, true
}

// dotted reads a plain dotted release, such as 1.26.0. Each part is a whole number that fits
// in 64 bits, with an optional leading +. Anything fancier is not compared.
func dotted(text string) ([]uint64, bool) {
	var parts []uint64
	for part := range strings.SplitSeq(text, ".") {
		digits := strings.TrimPrefix(part, "+")
		if digits == "" || digits[0] == '+' {
			return nil, false
		}
		n, err := strconv.ParseUint(digits, 10, 64)
		if err != nil {
			return nil, false
		}
		parts = append(parts, n)
	}
	return parts, true
}

func compareDotted(a, b string) (int, bool) {
	x, ok := dotted(a)
	if !ok {
		return 0, false
	}
	y, ok := dotted(b)
	if !ok {
		return 0, false
	}
	width := max(len(x), len(y))
	x = append(x, make([]uint64, width-len(x))...)
	y = append(y, make([]uint64, width-len(y))...)
	return slices.Compare(x, y), true
}
