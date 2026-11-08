package engine

import (
	"fmt"
	"slices"

	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/workflow"
)

// reach is what one part of the vocabulary may read through: only the providers its
// registration says it needs. Those are what the manifest lists and start-up checks, so a part
// that reached for anything else would read through a provider no one reviewed.
type reach struct {
	from workflow.Lookup
	part *vocab.Entry
}

// Provider implements workflow.Lookup.
func (r reach) Provider(need providers.Requirement) (any, error) {
	if !slices.Contains(r.part.Needs, need) {
		return nil, fmt.Errorf("%s reached for %s, which its registration does not say it needs: name it among the part's needs where the vocabulary registers it, so the manifest shows it and start-up checks it", r.part.Name, need.Name())
	}
	return r.from.Provider(need)
}
