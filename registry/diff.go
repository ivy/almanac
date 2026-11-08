package registry

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/aymanbagabas/go-udiff"

	"github.com/ivy/almanac/hostile"
)

// PackageDiff is what changed between two releases. The facts are clean and the text is hostile.
type PackageDiff struct {
	// InstallScriptAdded is build-time code a package manager runs for you, new in this release.
	InstallScriptAdded bool `json:"install_script_added"`
	// FilesChanged counts every file added, removed or changed, shown or not.
	FilesChanged uint64 `json:"files_changed"`
	// Truncated says part of Diff was cut to fit Limits.MaxDiffBytes, so nothing that reads
	// it has read the whole of this release.
	Truncated bool `json:"truncated"`
	// Unshown names the files that changed and could not be shown, in name order. A NUL makes
	// a file binary here, and one byte does that to a source file too, so these are
	// unreviewed and not merely dull.
	Unshown hostile.Of[[]string] `json:"unshown"`
	// Diff is the text changes, as unified diffs in name order.
	Diff hostile.Of[string] `json:"diff"`
}

// DiffArchives compares two archives of a package, a tar.gz or a zip each, in memory.
func DiffArchives(old, new []byte, limits Limits) (PackageDiff, error) {
	before, err := readArchive(old, limits)
	if err != nil {
		return PackageDiff{}, err
	}
	after, err := readArchive(new, limits)
	if err != nil {
		return PackageDiff{}, err
	}
	before, after = withoutTop(before), withoutTop(after)

	var text strings.Builder
	var changed uint64
	truncated := false
	unshown := []string{}
	for _, name := range names(before, after) {
		was, is := before[name], after[name]
		if bytes.Equal(was, is) {
			continue
		}
		changed++
		if binary(was) || binary(is) {
			unshown = append(unshown, name)
			continue
		}
		if truncated {
			continue
		}
		shown := shownChange(name, was, is)
		if text.Len()+len(shown) > limits.MaxDiffBytes {
			truncated = true
			continue
		}
		text.WriteString(shown)
	}
	return PackageDiff{
		InstallScriptAdded: installScriptAdded(before, after),
		FilesChanged:       changed,
		Truncated:          truncated,
		Unshown:            hostile.New(unshown),
		Diff:               hostile.New(text.String()),
	}, nil
}

// names are the files in either archive, in order.
func names(before, after files) []string {
	all := slices.Collect(maps.Keys(before))
	for name := range after {
		if _, ok := before[name]; !ok {
			all = append(all, name)
		}
	}
	slices.Sort(all)
	return all
}

// moduleRoot is the prefix to remove from a Go module zip, which nests everything under
// module/path@version/ and so shares no path between two versions. ok is false for any other
// layout.
func moduleRoot(found files) (root string, ok bool) {
	if len(found) == 0 {
		return "", false
	}
	first := slices.Min(slices.Collect(maps.Keys(found)))
	at := strings.IndexByte(first, '@')
	if at < 0 {
		return "", false
	}
	slash := strings.IndexByte(first[at:], '/')
	if slash < 0 {
		return "", false
	}
	root = first[:at+slash+1]
	for name := range found {
		if !strings.HasPrefix(name, root) {
			return "", false
		}
	}
	return root, true
}

// withoutTop removes the one top directory archives usually wrap everything in, which changes
// with each version.
func withoutTop(found files) files {
	if root, ok := moduleRoot(found); ok {
		inside := files{}
		for name, content := range found {
			inside[name[len(root):]] = content
		}
		return inside
	}
	tops := map[string]bool{}
	for name := range found {
		top, _, nested := strings.Cut(name, "/")
		if !nested {
			return found
		}
		tops[top] = true
	}
	if len(tops) != 1 {
		return found
	}
	inside := files{}
	for name, content := range found {
		_, rest, _ := strings.Cut(name, "/")
		inside[rest] = content
	}
	return inside
}

var npmHooks = []string{"preinstall", "install", "postinstall"}

// hooks are package.json's install scripts, each as hook:script. ok is false when there is a
// package.json and it cannot be read as an object.
func hooks(found files) (all []string, ok bool) {
	content, exists := found["package.json"]
	if !exists {
		return nil, true
	}
	// npm reads a manifest that starts with a byte order mark, so this does too.
	content = bytes.TrimPrefix(content, []byte("\uFEFF"))
	// A map, not a struct, because a struct field matches "Scripts" too, and the last of
	// two such keys would hide the one npm reads.
	var manifest map[string]any
	if json.Unmarshal(content, &manifest) != nil {
		return nil, false
	}
	for _, hook := range npmHooks {
		if script, isText := dig(manifest, "scripts", hook).(string); isText {
			all = append(all, hook+":"+script)
		}
	}
	return all, true
}

// installScriptAdded reports build-time code a package manager runs for you, new or changed in
// after: an npm install hook, build.rs, or setup.py. A changed package.json that cannot be read
// counts, because what it runs cannot be known.
func installScriptAdded(before, after files) bool {
	was, _ := hooks(before)
	is, readable := hooks(after)
	if !readable && !bytes.Equal(before["package.json"], after["package.json"]) {
		return true
	}
	for _, hook := range is {
		if !slices.Contains(was, hook) {
			return true
		}
	}
	for _, script := range []string{"build.rs", "setup.py"} {
		_, had := before[script]
		_, has := after[script]
		if has && !had {
			return true
		}
	}
	return false
}

func binary(content []byte) bool { return bytes.IndexByte(content, 0) >= 0 }

// shownChange is a text change as a reviewer reads it.
func shownChange(name string, before, after []byte) string {
	return udiff.Unified("a/"+name, "b/"+name, lossy(before), lossy(after))
}

// lossy reads bytes as UTF-8, replacing what is not.
func lossy(content []byte) string { return strings.ToValidUTF8(string(content), "\uFFFD") }
