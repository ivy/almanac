package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"time"
)

// FetchTimeout is how long the fetch script may run.
const FetchTimeout = 15 * time.Minute

// commandTimeout is how long one of Almanac's own commands, such as unpacking
// an archive, may run.
const commandTimeout = time.Minute

const (
	// sourceArchive is where a checkout's tarball is uploaded, before it is
	// unpacked inside the sandbox.
	sourceArchive = ".almanac/source.tar.gz"
	// dependenciesArchive is what the fetch script leaves behind.
	dependenciesArchive = ".dependencies.tar"
)

// FetchScript fetches what a checkout's lockfiles name, into .cargo and .cache
// under [Workdir], and archives them. Each command only downloads: none runs
// code from a package. It is not configurable, because a different script
// could run package code in a sandbox that has network. See ADR 1.
const FetchScript = `set -e
if [ -f Cargo.lock ]; then cargo fetch --locked; fi
find . -name go.sum -not -path './node_modules/*' -not -path './.cache/*' | while read -r sum; do
  (cd "$(dirname "$sum")" && GOWORK=off go mod download)
done
if [ -f pnpm-lock.yaml ]; then pnpm fetch --ignore-scripts --store-dir /work/.cache/pnpm; fi
if [ -f package-lock.json ]; then
  npm install --ignore-scripts --no-audit --no-fund --cache /work/.cache/npm ||
    echo 'npm install failed: the cache holds what it reached' >&2
fi
mkdir -p .cargo .cache
tar -cf ` + dependenciesArchive + ` .cargo .cache`

// ErrFetchFailed is a fetch whose commands failed, as opposed to a provider
// that could not run them.
var ErrFetchFailed = errors.New("fetching dependencies failed: the checkout did not unpack, a lockfile names " +
	"something no registry has, or a registry was unreachable; if it keeps failing, the repair needs a person")

// Dependencies is the dependency cache a fetch produced, as an archive ready
// for [Install].
type Dependencies []byte

// FetchDependencies is the dependency fetch step from ADR 1. In a new sandbox
// made from spec, which must have the [Fetch] purpose, it unpacks tarball, a
// checkout as a gzipped tar with one top directory, runs [FetchScript], and
// returns the cache. The sandbox is destroyed before it returns.
//
// A hostile lockfile can make the fetch download from a server of its
// choosing. That sandbox holds nothing to steal, and what it fetches runs
// only in an agent's sandbox, which has no network.
func FetchDependencies(ctx context.Context, sb Sandbox, spec Spec, tarball []byte) (Dependencies, error) {
	failed := func(id ID, err error) error {
		var command *CommandFailed
		if errors.As(err, &command) {
			err = fmt.Errorf("%w: %w", ErrFetchFailed, err)
		}
		return &Error{Op: "fetch", Sandbox: id, Err: err}
	}
	if spec.Purpose != Fetch {
		return nil, &Error{Op: "fetch", Err: fmt.Errorf("%w: dependencies are fetched only in a sandbox with the fetch purpose; build its spec with sandbox.FetchSpec", ErrRefused)}
	}
	id, err := sb.Create(ctx, spec)
	if err != nil {
		return nil, failed("", err)
	}
	deps, err := fetchIn(ctx, sb, id, tarball)
	// Clean up even when ctx was cancelled.
	if destroyErr := sb.Destroy(context.WithoutCancel(ctx), id); destroyErr != nil {
		err = errors.Join(err, destroyErr)
	}
	if err != nil {
		return nil, failed(id, err)
	}
	return deps, nil
}

func fetchIn(ctx context.Context, sb Sandbox, id ID, tarball []byte) (Dependencies, error) {
	if err := Unpack(ctx, sb, id, tarball); err != nil {
		return nil, err
	}
	if err := run(ctx, sb, id, FetchScript, FetchTimeout); err != nil {
		return nil, err
	}
	archive, err := sb.Download(ctx, id, dependenciesArchive)
	if err != nil {
		return nil, err
	}
	return Dependencies(archive), nil
}

// Unpack puts a checkout into a sandbox's [Workdir]. tarball is a gzipped tar
// with one top directory, as a forge serves a commit. It is unpacked inside
// the sandbox, so a hostile archive is never read on the host.
func Unpack(ctx context.Context, sb Sandbox, id ID, tarball []byte) error {
	wrapped, err := wrap(sourceArchive, tarball)
	if err != nil {
		return err
	}
	if err := sb.Upload(ctx, id, wrapped); err != nil {
		return err
	}
	unpack := fmt.Sprintf("tar -xzf %s --strip-components=1 && rm -rf .almanac", sourceArchive)
	return run(ctx, sb, id, unpack, commandTimeout)
}

// Install unpacks a dependency cache from [FetchDependencies] into an agent's
// sandbox, so a build there finds what it needs with no network.
func Install(ctx context.Context, sb Sandbox, id ID, deps Dependencies) error {
	if err := sb.Upload(ctx, id, deps); err != nil {
		return err
	}
	unpack := fmt.Sprintf("tar -xf %[1]s && rm %[1]s", dependenciesArchive)
	return run(ctx, sb, id, unpack, commandTimeout)
}

// wrap is a tar archive holding one file, data, at name. Its directory is
// written first, so any provider can unpack it.
func wrap(name string, data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if dir := path.Dir(name); dir != "." {
		if err := w.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: dir + "/", Mode: 0o755}); err != nil {
			return nil, err
		}
	}
	if err := w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
