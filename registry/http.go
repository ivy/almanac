package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const userAgent = "almanac"

// maxArtifactBytes bounds anything fetched from a registry: an archive, or a JSON index.
// It is the Rust registry's MAX_ARTIFACT_BYTES, and matches Limits.MaxTotalBytes, so an
// archive that fits here can still be refused for what it expands to.
const maxArtifactBytes = 256 << 20

// Endpoints is where each registry lives. Specs point these at a local server.
type Endpoints struct {
	Npm    string
	Crates string
	PyPI   string
	Go     string
	GitHub string
	Docker string
}

// DefaultEndpoints are the public registries.
func DefaultEndpoints() Endpoints {
	return Endpoints{
		Npm:    "https://registry.npmjs.org",
		Crates: "https://crates.io",
		PyPI:   "https://pypi.org",
		Go:     "https://proxy.golang.org",
		GitHub: "https://api.github.com",
		Docker: "https://hub.docker.com",
	}
}

// HTTP reads each registry's API over HTTP.
type HTTP struct {
	client    *http.Client
	endpoints Endpoints
	maxBytes  int64
}

// NewHTTP reads the registries at endpoints with client, or with a client of its own when
// client is nil.
func NewHTTP(endpoints Endpoints, client *http.Client) *HTTP {
	if client == nil {
		client = &http.Client{}
	}
	return &HTTP{client: client, endpoints: endpoints, maxBytes: maxArtifactBytes}
}

var _ Registry = (*HTTP)(nil)

// Published reads the release's publish time from its registry. A release the registry does
// not know, or a time it does not give, is unknown.
func (r *HTTP) Published(ctx context.Context, p Package) (time.Time, bool, error) {
	if !p.valid() {
		return time.Time{}, false, nil
	}
	e := r.endpoints
	name, version := p.name.String(), p.version.String()
	var address string
	var pick func(any) (time.Time, bool)
	switch p.ecosystem {
	case Npm:
		address = e.Npm + "/" + npmEscaped(name)
		pick = func(v any) (time.Time, bool) { return timestamp(dig(v, "time", version)) }
	case Crates:
		address = e.Crates + "/api/v1/crates/" + name + "/" + version
		pick = func(v any) (time.Time, bool) { return timestamp(dig(v, "version", "created_at")) }
	case PyPI:
		address = e.PyPI + "/pypi/" + name + "/" + version + "/json"
		pick = newestUpload
	case Go:
		address = e.Go + "/" + goEscaped(name) + "/@v/" + version + ".info"
		pick = func(v any) (time.Time, bool) { return timestamp(dig(v, "Time")) }
	case GitHubRelease:
		address = e.GitHub + "/repos/" + name + "/releases/tags/" + version
		pick = func(v any) (time.Time, bool) { return timestamp(dig(v, "published_at")) }
	case Docker:
		address = e.Docker + "/v2/repositories/" + dockerRepository(name) + "/tags/" + version
		pick = func(v any) (time.Time, bool) { return timestamp(dig(v, "tag_last_pushed")) }
	default:
		return time.Time{}, false, nil
	}
	found, ok, err := r.json(ctx, p, "read the publish time", address)
	if err != nil || !ok {
		return time.Time{}, false, err
	}
	at, known := pick(found)
	return at, known, nil
}

// Artifact downloads the release's source archive. The address of an npm tarball, a PyPI
// sdist or a GitHub tarball is read from the registry's index first.
func (r *HTTP) Artifact(ctx context.Context, p Package) ([]byte, error) {
	if !p.valid() {
		return nil, noArtifact(p)
	}
	e := r.endpoints
	name, version := p.name.String(), p.version.String()
	var index string
	var pick func(any) (string, bool)
	switch p.ecosystem {
	case Npm:
		index = e.Npm + "/" + npmEscaped(name)
		pick = func(v any) (string, bool) { return text(dig(v, "versions", version, "dist", "tarball")) }
	case PyPI:
		index = e.PyPI + "/pypi/" + name + "/" + version + "/json"
		pick = sdist
	case GitHubRelease:
		index = e.GitHub + "/repos/" + name + "/releases/tags/" + version
		pick = func(v any) (string, bool) { return text(dig(v, "tarball_url")) }
	case Crates:
		return r.download(ctx, p, e.Crates+"/api/v1/crates/"+name+"/"+version+"/download", true)
	case Go:
		return r.download(ctx, p, e.Go+"/"+goEscaped(name)+"/@v/"+version+".zip", true)
	default:
		return nil, noArtifact(p)
	}
	found, ok, err := r.json(ctx, p, "find the archive", index)
	if err != nil {
		return nil, err
	}
	address, named := pick(found)
	if !ok || !named {
		return nil, noArtifact(p)
	}
	return r.download(ctx, p, address, false)
}

func noArtifact(p Package) error {
	return &Error{
		Package: p,
		Doing:   "find the archive",
		Reason:  "the registry has none for it",
		Help:    "nothing can diff this release, so a person must review it",
		Err:     ErrNoArtifact,
	}
}

// json reads the registry's JSON document at address, which was built here. ok is false when
// the registry does not know the release.
func (r *HTTP) json(ctx context.Context, p Package, doing, address string) (found any, ok bool, err error) {
	body, status, err := r.get(ctx, p, doing, address, true)
	if err != nil || status == http.StatusNotFound {
		return nil, false, err
	}
	// The decoding error is not kept: a syntax error quotes the registry's text.
	if json.Unmarshal(body, &found) != nil {
		return nil, false, &Error{
			Package: p,
			Doing:   doing,
			Reason:  "the registry's answer is not JSON",
			Help:    "the registry may be failing, or answering through a proxy; the step fails and a later run tries again",
		}
	}
	return found, true, nil
}

// download fetches an archive. A 404 is an error here, because the archive was named.
func (r *HTTP) download(ctx context.Context, p Package, address string, ours bool) ([]byte, error) {
	const doing = "download the archive"
	body, status, err := r.get(ctx, p, doing, address, ours)
	if err == nil && status == http.StatusNotFound {
		err = refusedStatus(p, doing, status)
	}
	return body, err
}

// get fetches address, reading at most maxBytes of its body. A 404 comes back as a status, and
// any other status that is not a success as an error.
//
// ours says whether address was built here, from an endpoint and a validated package, or read
// from a registry's answer. An error quotes the failure underneath only when it is about an
// address built here, never one a registry sent or redirected to.
func (r *HTTP) get(ctx context.Context, p Package, doing, address string, ours bool) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, http.NoBody)
	if err != nil {
		if ours {
			return nil, 0, unreachable(ctx, p, doing, address, err)
		}
		return nil, 0, &Error{
			Package: p,
			Doing:   doing,
			Reason:  "the registry named an archive at an address that is not a URL",
			Help:    "nothing can diff this release, so a person must review it",
		}
	}
	request.Header.Set("User-Agent", userAgent)
	response, err := r.client.Do(request)
	if err != nil {
		return nil, 0, unreachable(ctx, p, doing, quotable(address, ours), err)
	}
	defer func() { _ = response.Body.Close() }()
	switch {
	case response.StatusCode == http.StatusNotFound:
		return nil, response.StatusCode, nil
	case response.StatusCode < 200 || response.StatusCode > 299:
		return nil, response.StatusCode, refusedStatus(p, doing, response.StatusCode)
	case response.ContentLength > r.maxBytes:
		return nil, 0, r.tooLarge(p, doing)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, r.maxBytes+1))
	if err != nil {
		return nil, 0, unreachable(ctx, p, doing, quotable(address, ours), err)
	}
	if int64(len(body)) > r.maxBytes {
		return nil, 0, r.tooLarge(p, doing)
	}
	return body, response.StatusCode, nil
}

// quotable is the address an error may quote failures about: address if it was built here,
// and none otherwise.
func quotable(address string, ours bool) string {
	if ours {
		return address
	}
	return ""
}

// unreachable is a request that got no answer. It quotes err only when err is about ours, an
// address built here, and not one a registry sent or redirected to.
func unreachable(ctx context.Context, p Package, doing, ours string, err error) error {
	failure := &Error{
		Package: p,
		Doing:   doing,
		Reason:  "the registry could not be reached",
		Help:    "check the network and the registry's status; the step fails and a later run tries again",
	}
	var request *url.Error
	switch {
	case ctx.Err() != nil:
		failure.Reason = "the step was cancelled or ran out of time"
		failure.Err = ctx.Err()
	case ours != "" && (!errors.As(err, &request) || request.URL == ours):
		failure.Reason += ": " + err.Error()
		failure.Err = err
	case isTimeout(err):
		failure.Reason = "the registry took too long to answer"
	}
	return failure
}

func (r *HTTP) tooLarge(p Package, doing string) error {
	return &Error{
		Package: p,
		Doing:   doing,
		Reason:  "it is larger than " + strconv.FormatInt(r.maxBytes>>20, 10) + " MiB, so it is not fetched",
		Help:    "nothing can read all of this release, so a person must review it",
	}
}

func refusedStatus(p Package, doing string, status int) error {
	help := "the registry refuses this request; check the registry's address and that the release exists"
	if status == http.StatusTooManyRequests || status >= 500 {
		help = "the registry is failing or limiting requests; the step fails and a later run tries again"
	}
	return &Error{
		Package: p,
		Doing:   doing,
		Reason:  fmt.Sprintf("the registry answered %d %s", status, http.StatusText(status)),
		Help:    help,
	}
}

func isTimeout(err error) bool {
	var timeout net.Error
	return errors.As(err, &timeout) && timeout.Timeout()
}

// dig follows keys through nested JSON objects. It is nil where any key is missing.
func dig(v any, keys ...string) any {
	for _, key := range keys {
		object, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = object[key]
	}
	return v
}

func text(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// timestamp reads an RFC 3339 time. Anything else is unknown.
func timestamp(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return at.UTC(), true
}

// newestUpload is the newest of a PyPI release's upload times. One file without a time makes
// the release unknown, because the file that matters might be that one.
func newestUpload(v any) (time.Time, bool) {
	urls, _ := dig(v, "urls").([]any)
	var newest time.Time
	for _, file := range urls {
		at, ok := timestamp(dig(file, "upload_time_iso_8601"))
		if !ok {
			return time.Time{}, false
		}
		if at.After(newest) {
			newest = at
		}
	}
	return newest, !newest.IsZero()
}

// sdist is the address of a PyPI release's source distribution.
func sdist(v any) (string, bool) {
	urls, _ := dig(v, "urls").([]any)
	i := slices.IndexFunc(urls, func(file any) bool { return dig(file, "packagetype") == "sdist" })
	if i < 0 {
		return "", false
	}
	return text(dig(urls[i], "url"))
}

// npmEscaped writes a scoped name's slash as %2f, as the npm registry expects.
func npmEscaped(name string) string { return strings.ReplaceAll(name, "/", "%2f") }

// goEscaped writes each capital in a module path as ! and the lowercase letter, as the Go
// module proxy expects (https://go.dev/ref/mod#goproxy-protocol).
func goEscaped(module string) string {
	var b strings.Builder
	for _, c := range module {
		if 'A' <= c && c <= 'Z' {
			b.WriteByte('!')
			c += 'a' - 'A'
		}
		b.WriteRune(c)
	}
	return b.String()
}

// dockerRepository is an image's repository on Docker Hub. Official images live under library/.
func dockerRepository(name string) string {
	if strings.Contains(name, "/") {
		return name
	}
	return "library/" + name
}
