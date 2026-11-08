package patch

import "errors"

// Rejection is why the policy refused a patch. It names a reason and never a path, because
// paths are hostile. It is an error, so a refusal can be compared: errors.Is(err, patch.Denied).
type Rejection int

const (
	// Empty is a patch that changes nothing.
	Empty Rejection = iota + 1
	// UnsafePath is a path that cannot be trusted to mean what it appears to mean.
	UnsafePath
	// Denied is a path the policy keeps out of reach.
	Denied
	// Symlink is a symbolic link.
	Symlink
	// Binary is a file no one can review.
	Binary
	// TooManyFiles is more files than the policy allows.
	TooManyFiles
	// TooLarge is more content than the policy allows.
	TooLarge
)

var rejections = map[Rejection]struct{ name, reason string }{
	Empty:        {"Empty", "the patch changes nothing"},
	UnsafePath:   {"UnsafePath", "a path in the patch cannot be trusted to mean what it appears to mean"},
	Denied:       {"Denied", "the patch touches a path the policy keeps out of reach, such as CI or Renovate configuration"},
	Symlink:      {"Link", "the patch makes a symbolic link, which could point a later write elsewhere"},
	Binary:       {"Binary", "the patch writes a binary file, which no one can review"},
	TooManyFiles: {"TooManyFiles", "the patch changes more files than the policy allows"},
	TooLarge:     {"TooLarge", "the patch writes more than the policy allows"},
}

// Error says why the patch was refused. A person reads the patch to decide what to do next.
func (r Rejection) Error() string {
	if known, ok := rejections[r]; ok {
		return "patch refused: " + known.reason
	}
	return "patch refused"
}

// String is the rejection's name, as a run records it.
func (r Rejection) String() string {
	if known, ok := rejections[r]; ok {
		return known.name
	}
	return "invalid rejection"
}

// MarshalText writes the rejection's name.
func (r Rejection) MarshalText() ([]byte, error) {
	if _, ok := rejections[r]; !ok {
		return nil, errors.New("patch: an invalid rejection has no stored form")
	}
	return []byte(r.String()), nil
}

// UnmarshalText reads a rejection's name, and refuses any other.
func (r *Rejection) UnmarshalText(text []byte) error {
	for rejection, known := range rejections {
		if known.name == string(text) {
			*r = rejection
			return nil
		}
	}
	return errors.New("patch: not a rejection's name")
}
