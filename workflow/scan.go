package workflow

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/wire"
)

// Scanner is what a scan can reach. It lists subjects: deciding about them is a run's job.
type Scanner struct {
	providers *providers.Set
	only      *hostile.Slug
}

// NewScanner is a scanner over every repository the providers can see.
func NewScanner(set *providers.Set) *Scanner { return &Scanner{providers: set} }

// Only is a scanner for the one repository a backfill named.
func (s *Scanner) Only(repo hostile.Slug) *Scanner {
	return &Scanner{providers: s.providers, only: &repo}
}

// Provider is the provider need names, set up in the binary.
func (s *Scanner) Provider(need providers.Requirement) (any, error) {
	found, ok := s.providers.Get(need)
	if !ok {
		return nil, fmt.Errorf("no provider for %s: provide one where the almanac sets up its providers, and require it so start-up checks it", need.Name())
	}
	return found, nil
}

// Narrow is what to look at out of the repositories a scan can see: the one a backfill named,
// or all of them. A named repository missing from visible is an error, because scanning
// nothing would read as all clear.
func (s *Scanner) Narrow(visible []hostile.Slug) ([]hostile.Slug, error) {
	if s.only == nil {
		return visible, nil
	}
	if slices.Contains(visible, *s.only) {
		return []hostile.Slug{*s.only}, nil
	}
	return nil, fmt.Errorf("%s is not among the %d repositories this scan can see: check its name, that it is not archived, and that the credentials reach it, such as a GitHub App installed on it", s.only, len(visible))
}

// Found is an event a scan found. It reaches a workflow by the same path as a webhook.
type Found struct {
	// DeliveryID must be stable, because that is what makes scanning twice harmless.
	DeliveryID string          `json:"delivery_id"`
	Kind       string          `json:"kind"`
	Payload    json.RawMessage `json:"payload"`
}

// Scanned is an event of type E a scan found, with a stable delivery ID.
func Scanned[E any, P interface {
	*E
	wire.Event
}](deliveryID string, payload json.RawMessage) Found {
	return Found{DeliveryID: deliveryID, Kind: P(new(E)).Kind(), Payload: payload}
}

// ScannedPull is a pull request read from the REST API, as an event of type E. It is
// identified by repository, number and head, and shaped like the webhook E is parsed from.
func ScannedPull[E any, P interface {
	*E
	wire.FromRest
}](repo hostile.Slug, number uint64, head hostile.Sha, rest []byte) (Found, error) {
	payload, err := wire.WebhookPayload(rest, P(new(E)).Projections())
	if err != nil {
		return Found{}, err
	}
	id := fmt.Sprintf("scan:%s#%d@%s", repo, number, head)
	return Scanned[E, P](id, payload), nil
}

// AtState adds something that changes when the subject should be looked at again, such as the
// state of its checks. Without it, a scan of an unchanged commit is dropped as a duplicate,
// and a run that decided to wait would never be reconsidered.
func (f Found) AtState(state string) Found {
	f.DeliveryID += "~" + state
	return f
}
