package advisory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ivy/almanac/hostile"
)

const (
	userAgent   = "almanac"
	searchLimit = "20"
	// maxBody bounds what one answer from the feed may hold.
	maxBody = 8 << 20
)

// CveNow reads advisories from cve.now's API. There is no spec to generate its types from, so
// each entry is read field by field (R-39). The API takes uppercase IDs.
type CveNow struct {
	client  *http.Client
	baseURL string
}

// NewCveNow reads from the API at baseURL, such as https://cve.now.
func NewCveNow(baseURL string) *CveNow {
	return &CveNow{client: &http.Client{}, baseURL: strings.TrimRight(baseURL, "/")}
}

var _ Advisories = (*CveNow)(nil)

// FeedError is an advisory feed that did not answer, or answered with something unreadable.
// Nothing about an update is proved without the feed, so a check that meets one fails.
type FeedError struct {
	// URL is what was asked for.
	URL string
	// Problem says what went wrong. It never quotes the feed's answer, which may be hostile.
	Problem string
	// Cause is what failed underneath, if anything did.
	Cause error
}

func (e *FeedError) Error() string {
	return fmt.Sprintf("advisory feed: GET %s %s, so no update can be shown to fix an advisory; "+
		"the check fails until the feed answers, so retry later or check the feed's address", e.URL, e.Problem)
}

func (e *FeedError) Unwrap() error { return e.Cause }

// ByID finds an advisory by GHSA or CVE ID.
func (c *CveNow) ByID(ctx context.Context, id hostile.Slug) (Advisory, bool, error) {
	return c.entry(ctx, id.String())
}

// ByPackage finds the advisories that name a package. An entry the search lists and the feed
// then does not know is left out.
func (c *CveNow) ByPackage(ctx context.Context, ecosystem, pkg hostile.Slug) ([]Advisory, error) {
	query := url.Values{
		"package":   {pkg.String()},
		"ecosystem": {ecosystem.String()},
		"limit":     {searchLimit},
	}
	found, _, err := c.get(ctx, "/api/v1/search?"+query.Encode())
	if err != nil {
		return nil, err
	}
	var advisories []Advisory
	for _, e := range list(field(found, "entries")) {
		id, ok := slug(field(e, "id"))
		if !ok {
			continue
		}
		a, ok, err := c.entry(ctx, id.String())
		if err != nil {
			return nil, err
		}
		if ok {
			advisories = append(advisories, a)
		}
	}
	return advisories, nil
}

func (c *CveNow) entry(ctx context.Context, id string) (Advisory, bool, error) {
	path := "/api/v1/entries/" + url.PathEscape(strings.ToUpper(id))
	found, ok, err := c.get(ctx, path)
	if err != nil || !ok {
		return Advisory{}, false, err
	}
	a, ok := read(found)
	if !ok {
		return Advisory{}, false, &FeedError{URL: c.baseURL + path, Problem: "answered with an entry that has no usable ID"}
	}
	return a, true, nil
}

// get fetches path as JSON. It returns false when the feed answers 404.
func (c *CveNow) get(ctx context.Context, path string) (any, bool, error) {
	target := c.baseURL + path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, false, &FeedError{URL: target, Problem: "could not be asked", Cause: err}
	}
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return nil, false, &FeedError{URL: target, Problem: "got no answer: " + err.Error(), Cause: err}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, false, &FeedError{URL: target, Problem: "answered " + response.Status}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return nil, false, &FeedError{URL: target, Problem: "answered, then failed: " + err.Error(), Cause: err}
	}
	if len(body) > maxBody {
		return nil, false, &FeedError{URL: target, Problem: fmt.Sprintf("answered with more than %d bytes", maxBody)}
	}
	var found any
	if err := json.Unmarshal(body, &found); err != nil {
		return nil, false, &FeedError{URL: target, Problem: "answered with something that is not JSON", Cause: errNotJSON}
	}
	return found, true, nil
}

// errNotJSON stands for the decoder's error, which may quote the answer.
var errNotJSON = errors.New("not JSON")

// read reads an entry as an advisory. It is false if the entry has no usable ID.
func read(entry any) (Advisory, bool) {
	id, ok := slug(field(entry, "id"))
	if !ok {
		return Advisory{}, false
	}
	a := Advisory{ID: id, Severity: Unknown}
	for _, alias := range list(field(entry, "aliases")) {
		if s, ok := slug(alias); ok {
			a.Aliases = append(a.Aliases, s)
		}
	}
	if cvss, ok := field(field(entry, "severity"), "cvss").(float64); ok {
		a.Severity = severityOf(cvss)
	}
	// Any value but null marks the entry as known to be exploited.
	a.KEV = field(field(entry, "exploitation"), "kev") != nil
	for _, value := range list(field(entry, "affected")) {
		if affected, ok := readAffected(value); ok {
			a.Affected = append(a.Affected, affected)
		}
	}
	summary, _ := field(entry, "summary").(string)
	a.Summary = hostile.New(summary)
	return a, true
}

// readAffected reads one affected package. An entry naming a vendor and product, and not a
// package, says nothing about a dependency, so it is false.
func readAffected(value any) (Affected, bool) {
	ranges, ok := field(value, "ranges").([]any)
	if !ok {
		return Affected{}, false
	}
	ecosystem, ok := slug(field(value, "ecosystem"))
	if !ok {
		return Affected{}, false
	}
	pkg, ok := slug(field(value, "package"))
	if !ok {
		return Affected{}, false
	}
	affected := Affected{Ecosystem: ecosystem, Package: pkg, Ranges: []Range{}}
	for _, r := range ranges {
		affected.Ranges = append(affected.Ranges, Range{
			Introduced: optionalSlug(field(r, "introduced")),
			Fixed:      optionalSlug(field(r, "fixed")),
		})
	}
	return affected, true
}

// field is the member key of an object, or nil if value is not an object or lacks it.
func field(value any, key string) any {
	object, _ := value.(map[string]any)
	return object[key]
}

// list is value's items, or none if it is not a list.
func list(value any) []any {
	items, _ := value.([]any)
	return items
}

// slug reads value as a slug, if it is a string with a slug's shape.
func slug(value any) (hostile.Slug, bool) {
	text, ok := value.(string)
	if !ok {
		return hostile.Slug{}, false
	}
	s, err := hostile.ParseSlug(text)
	return s, err == nil
}

// optionalSlug is a range bound: nil, which leaves the range open, unless value is a slug.
func optionalSlug(value any) *hostile.Slug {
	s, ok := slug(value)
	if !ok {
		return nil
	}
	return &s
}
