package patch

import (
	"bytes"
	"encoding/base64"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/hostile/reveal"
)

// Policy is which paths a patch may not touch, and how large it may be. A workflow declares
// it as data. Names match whatever their case.
type Policy struct {
	// DeniedNames are refused at any depth, as a directory or a file.
	DeniedNames []string
	// DeniedAtRoot are refused at the repository's root only.
	DeniedAtRoot []string
	// MaxFiles is how many files a patch may change.
	MaxFiles int
	// MaxBytes is how much content a patch may write, counted in base64.
	MaxBytes int
}

// DefaultPolicy keeps everything that configures CI, the toolchain or Renovate out of reach,
// because a pushed patch runs in CI, which may hold secrets. It is a new value each time, so
// no workflow can loosen another's.
func DefaultPolicy() Policy {
	return Policy{
		DeniedNames: []string{
			".git",
			".github",
			".gitlab-ci.yml",
			".circleci",
			".buildkite",
			".husky",
			".envrc",
			".gitmodules",
			".gitattributes",
		},
		DeniedAtRoot: []string{
			"renovate.json",
			"renovate.json5",
			".renovaterc",
			".renovaterc.json",
			".renovaterc.json5",
			"mise.toml",
			".mise.toml",
			"mise.local.toml",
			".tool-versions",
		},
		MaxFiles: 50,
		MaxBytes: 1024 * 1024,
	}
}

// Checked is a patch the policy accepted, for a known base commit. Only [Policy.Check] makes
// one: its fields are unexported and it has no stored form, so stored or model-written data
// cannot pose as checked. Its zero value is not checked, and whatever requires a Checked
// refuses it.
type Checked struct {
	patch   hostile.Of[Patch]
	base    hostile.Sha
	checked bool
}

// Patch is the patch that was checked. It is still hostile: its paths and contents were
// checked by rule, not made safe to read.
func (c Checked) Patch() hostile.Of[Patch] { return c.patch }

// Base is the commit the patch was checked against. A forge refuses the push unless the branch
// is still there.
func (c Checked) Base() hostile.Sha { return c.base }

// IsZero reports whether c was not made by [Policy.Check].
func (c Checked) IsZero() bool { return !c.checked }

// Check is the only way to a [Checked]. A forge later refuses it unless base is still the
// branch's head, which is how a patch is known to apply cleanly. A refusal is a [Rejection].
func (p Policy) Check(h hostile.Of[Patch], base hostile.Sha) (Checked, error) {
	candidate := reveal.Value(h)
	// The checked copy is the one kept, so nothing that still holds h can change it.
	candidate.Changes = slices.Clone(candidate.Changes)
	if rejection := p.review(candidate); rejection != nil {
		return Checked{}, rejection
	}
	return Checked{patch: hostile.New(candidate), base: base, checked: true}, nil
}

func (p Policy) review(candidate Patch) error {
	if len(candidate.Changes) == 0 {
		return Empty
	}
	if len(candidate.Changes) > p.MaxFiles {
		return TooManyFiles
	}
	written := 0
	for _, change := range candidate.Changes {
		parts, ok := components(change.Path)
		if !ok {
			return UnsafePath
		}
		if !p.allows(parts) {
			return Denied
		}
		switch change.Op {
		case Link:
			return Symlink
		case Write:
			if isBinary(change.Content) {
				return Binary
			}
			written += len(change.Content)
		case Delete:
		default:
			return UnsafePath
		}
	}
	if written > p.MaxBytes {
		return TooLarge
	}
	return nil
}

func (p Policy) allows(parts []string) bool {
	matches := func(names []string, part string) bool {
		return slices.ContainsFunc(names, func(name string) bool { return strings.EqualFold(name, part) })
	}
	for _, part := range parts {
		if matches(p.DeniedNames, part) {
			return false
		}
	}
	return !matches(p.DeniedAtRoot, parts[0])
}

// components splits a path into its parts, or returns false if it cannot be trusted to mean
// what it appears to mean.
func components(path string) ([]string, bool) {
	forbidden := func(r rune) bool { return unicode.IsControl(r) || r == '\\' || r == ':' }
	if path == "" || strings.HasPrefix(path, "/") || !utf8.ValidString(path) || strings.ContainsFunc(path, forbidden) {
		return nil, false
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		// Some filesystems drop trailing dots and spaces, so ".github." could mean ".github".
		if part == "" || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return nil, false
		}
	}
	return parts, true
}

// isBinary uses git's own test: a NUL byte means binary. A pushed patch runs in CI, and no one
// can review bytes.
func isBinary(content string) bool {
	decoded, err := base64.StdEncoding.DecodeString(content)
	return err == nil && bytes.IndexByte(decoded, 0) >= 0
}
