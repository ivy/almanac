package workflow

import (
	"fmt"
	"slices"

	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/providers"
)

// Validate checks, when a binary starts, that set provides everything the workflows declared
// they need (R-40): each requirement, a harness for each agent, and in live mode a Forge for a
// workflow that may Attest or Push and a Tracker for one that may File or Ask. It names every
// gap at once.
func Validate(workflows []Registered, set *providers.Set, mode effect.Mode) error {
	var missing []string
	for _, w := range workflows {
		for _, need := range w.Requirements() {
			if !set.Has(need) {
				missing = append(missing, fmt.Sprintf("%s needs a provider for %s", w.Name(), need.Name()))
			}
		}
		for _, a := range w.Agents() {
			if _, ok := set.HarnessFor(a.Name); !ok {
				missing = append(missing, fmt.Sprintf("%s needs a harness for agent %s", w.Name(), a.Name))
			}
		}
		if mode == effect.Live {
			missing = append(missing, missingForEffects(w, set)...)
		}
	}
	if len(missing) > 0 {
		return diag.MissingProviders(missing)
	}
	return nil
}

func missingForEffects(w Registered, set *providers.Set) []string {
	var missing []string
	may := func(kinds ...effect.Kind) bool {
		return slices.ContainsFunc(kinds, func(k effect.Kind) bool { return slices.Contains(w.May(), k) })
	}
	if may(effect.KindAttest, effect.KindPush) && !set.Has(providers.Require[effect.Forge]()) {
		missing = append(missing, fmt.Sprintf("%s may Attest or Push but no Forge is provided", w.Name()))
	}
	if may(effect.KindFile, effect.KindAsk) && !set.Has(providers.Require[effect.Tracker]()) {
		missing = append(missing, fmt.Sprintf("%s may File or Ask but no Tracker is provided", w.Name()))
	}
	return missing
}
