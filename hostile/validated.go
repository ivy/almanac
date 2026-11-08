package hostile

import "strings"

// Clean is a type that cannot carry free text: a number, a bool, or text whose shape was
// validated. A payload field of one of these types can be read as it is.
type Clean interface {
	~bool |
		~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
		~float32 | ~float64 |
		Slug | Sha
}

// Invalid is text that failed validation. It names the shape and never repeats the text,
// which may be hostile.
type Invalid struct {
	// Shape is what the text should have been, such as "slug".
	Shape string
}

func (e Invalid) Error() string { return "not a " + e.Shape }

// Slug is an identifier with a restricted alphabet: a repository name, a branch, a login.
type Slug struct{ text string }

const slugPunctuation = "._/-:@[]"

// ParseSlug validates text as a slug: 1 to 200 ASCII letters, digits, or ._/-:@[] characters.
func ParseSlug(text string) (Slug, error) {
	if text == "" || len(text) > 200 || strings.IndexFunc(text, notSlug) >= 0 {
		return Slug{}, Invalid{Shape: "slug"}
	}
	return Slug{text: text}, nil
}

func notSlug(r rune) bool {
	return !isASCIIAlphanumeric(r) && !strings.ContainsRune(slugPunctuation, r)
}

// MustParseSlug is ParseSlug for text the program itself holds. It panics if text is not a slug.
func MustParseSlug(text string) Slug {
	slug, err := ParseSlug(text)
	if err != nil {
		panic(err)
	}
	return slug
}

func (s Slug) String() string { return s.text }

// MarshalText writes the slug as it is.
func (s Slug) MarshalText() ([]byte, error) { return []byte(s.text), nil }

// UnmarshalText validates the text, so a decoded Slug is always a slug.
func (s *Slug) UnmarshalText(text []byte) error {
	parsed, err := ParseSlug(string(text))
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}

// Sha is a full commit hash: 40 hexadecimal digits.
type Sha struct{ text string }

// ParseSha validates text as a full commit hash.
func ParseSha(text string) (Sha, error) {
	if len(text) != 40 || strings.IndexFunc(text, notHex) >= 0 {
		return Sha{}, Invalid{Shape: "sha"}
	}
	return Sha{text: text}, nil
}

func notHex(r rune) bool {
	return !strings.ContainsRune("0123456789abcdefABCDEF", r)
}

// MustParseSha is ParseSha for text the program itself holds. It panics if text is not a sha.
func MustParseSha(text string) Sha {
	sha, err := ParseSha(text)
	if err != nil {
		panic(err)
	}
	return sha
}

func (s Sha) String() string { return s.text }

// MarshalText writes the hash as it is.
func (s Sha) MarshalText() ([]byte, error) { return []byte(s.text), nil }

// UnmarshalText validates the text, so a decoded Sha is always a full hash.
func (s *Sha) UnmarshalText(text []byte) error {
	parsed, err := ParseSha(string(text))
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}

func isASCIIAlphanumeric(r rune) bool {
	return '0' <= r && r <= '9' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z'
}
