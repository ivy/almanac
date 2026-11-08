package registry

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"strconv"
	"strings"

	"github.com/ivy/almanac/hostile"
)

// Limits bound what an archive may expand to, against decompression bombs.
type Limits struct {
	// MaxTotalBytes bounds the content of every file in one archive, together.
	MaxTotalBytes int64
	// MaxFileBytes bounds one file's content.
	MaxFileBytes int64
	// MaxEntries bounds an archive's entries: files, directories, and anything else.
	MaxEntries int
	// MaxDiffBytes bounds the diff text. A diff that would pass it is truncated.
	MaxDiffBytes int
}

// DefaultLimits are the Rust registry's: 256 MiB in all, 16 MiB a file, 50,000 entries, and
// 512 KiB of diff.
func DefaultLimits() Limits {
	return Limits{
		MaxTotalBytes: 256 << 20,
		MaxFileBytes:  16 << 20,
		MaxEntries:    50_000,
		MaxDiffBytes:  512 << 10,
	}
}

// Refused is an archive this package will not read: corrupt, a bomb, a path that walks out, or
// a link. Nothing reviews a refused archive, so a person must.
type Refused struct {
	// Reason says what was wrong, in words this package chose.
	Reason string
	// Entry is the name of the entry to blame, when there is one. It is the archive's text, so
	// it is hostile and never part of the message.
	Entry hostile.Of[string]
}

func (e *Refused) Error() string {
	return "archive refused: " + e.Reason + "; nothing can review this release, so a person must"
}

func refused(reason string) *Refused {
	return &Refused{Reason: reason, Entry: hostile.New("")}
}

// files are an archive's regular files, by name.
type files map[string][]byte

// budget counts what an archive has expanded to, trusting no size the archive claims.
type budget struct {
	limits  Limits
	total   int64
	entries int
}

func (b *budget) entry() error {
	b.entries++
	if b.entries > b.limits.MaxEntries {
		return refused("too many entries: more than " + strconv.Itoa(b.limits.MaxEntries))
	}
	return nil
}

func (b *budget) read(content io.Reader) ([]byte, error) {
	limit := b.limits.MaxFileBytes + 1
	data, err := io.ReadAll(io.LimitReader(content, limit))
	if err != nil {
		return nil, corrupt(err)
	}
	if int64(len(data)) >= limit {
		return nil, refused("a file is too large: over " + strconv.FormatInt(b.limits.MaxFileBytes, 10) + " bytes")
	}
	b.total += int64(len(data))
	if b.total > b.limits.MaxTotalBytes {
		return nil, refused("it expands too far: over " + strconv.FormatInt(b.limits.MaxTotalBytes, 10) + " bytes")
	}
	return data, nil
}

// corrupt is an archive its format's reader gave up on. The reader's error is not quoted,
// because an archive's text could reach it.
func corrupt(error) *Refused {
	return refused("it is corrupt or truncated")
}

// safePath refuses a name that is absolute or has a .. segment. Nothing is written to disk, so
// this guards whatever reads the names later. A backslash counts as a separator, as it does
// to a Windows reader.
func safePath(name string, entry int) error {
	unsafe := strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`)
	for part := range strings.FieldsFuncSeq(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		unsafe = unsafe || part == ".."
	}
	if unsafe {
		return &Refused{Reason: "unsafe path at entry " + strconv.Itoa(entry), Entry: hostile.New(name)}
	}
	return nil
}

func link(name string, entry int) error {
	return &Refused{Reason: "link at entry " + strconv.Itoa(entry) + ", which could point outside the archive", Entry: hostile.New(name)}
}

// readArchive reads a tar.gz or a zip, by its first bytes.
func readArchive(data []byte, limits Limits) (files, error) {
	switch {
	case bytes.HasPrefix(data, []byte{0x1f, 0x8b}):
		return readTar(data, limits)
	case bytes.HasPrefix(data, []byte("PK")):
		return readZip(data, limits)
	default:
		return nil, refused("not a tar.gz or zip")
	}
}

func readTar(data []byte, limits Limits) (files, error) {
	unzipped, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, corrupt(err)
	}
	archive := tar.NewReader(unzipped)
	spent := budget{limits: limits}
	found := files{}
	for entry := 1; ; entry++ {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return found, nil
		}
		if err != nil && !errors.Is(err, tar.ErrInsecurePath) {
			return nil, corrupt(err)
		}
		if err := spent.entry(); err != nil {
			return nil, err
		}
		if err := safePath(header.Name, entry); err != nil {
			return nil, err
		}
		switch header.Typeflag {
		case tar.TypeSymlink, tar.TypeLink:
			return nil, link(header.Name, entry)
		case tar.TypeDir, tar.TypeXGlobalHeader, tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			// No content.
		default:
			// A regular file, or a kind some readers extract as one: contiguous, sparse, or
			// one this reader does not name. Each is read, so none is hidden from the diff.
			content, err := spent.read(archive)
			if err != nil {
				return nil, err
			}
			found[header.Name] = content
		}
	}
}

func readZip(data []byte, limits Limits) (files, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return nil, corrupt(err)
	}
	spent := budget{limits: limits}
	found := files{}
	for i, file := range archive.File {
		entry := i + 1
		if err := spent.entry(); err != nil {
			return nil, err
		}
		if err := safePath(file.Name, entry); err != nil {
			return nil, err
		}
		if file.Mode()&fs.ModeSymlink != 0 {
			return nil, link(file.Name, entry)
		}
		if strings.HasSuffix(file.Name, "/") {
			continue
		}
		content, err := readZipped(file, &spent)
		if err != nil {
			return nil, err
		}
		found[file.Name] = content
	}
	return found, nil
}

func readZipped(file *zip.File, spent *budget) ([]byte, error) {
	content, err := file.Open()
	if err != nil {
		return nil, corrupt(err)
	}
	defer func() { _ = content.Close() }()
	return spent.read(content)
}
