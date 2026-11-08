package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/store"
)

// Shown is one outcome as a verb reports it: a run, or a budget that held runs back.
type Shown struct {
	runtime.Outcome
	// Recorded is where each effect a run recorded is in delivery, such as "would-apply" in a
	// dry run.
	Recorded []store.Status `json:"recorded,omitempty"`
}

// String is the outcome as one line for a person.
func (s Shown) String() string {
	run := fmt.Sprintf("%s (%s)", s.Workflow, s.RunID)
	switch s.Result {
	case runtime.Ran:
		recorded := "nothing"
		if len(s.Recorded) > 0 {
			names := make([]string, len(s.Recorded))
			for i, status := range s.Recorded {
				names[i] = string(status)
			}
			recorded = strings.Join(names, ", ")
		}
		line := fmt.Sprintf("ran %s: recorded %s", run, recorded)
		if len(s.Denied) > 0 {
			line += fmt.Sprintf("; denied %d it did not declare or the guard refused", len(s.Denied))
		}
		return line
	case runtime.Stopped:
		return fmt.Sprintf("stopped %s: %s", run, s.Reason)
	case runtime.Failed:
		return fmt.Sprintf("failed %s: %s", run, s.Reason)
	case runtime.BudgetTripped:
		budget := "the overall budget"
		if s.Workflow != "" {
			budget = s.Workflow + "'s budget"
		}
		return fmt.Sprintf("%s tripped, holding %d", budget, s.Held)
	default:
		return s.Result.String()
	}
}

// settled is what settling a runtime came to.
type settled struct {
	outcomes []Shown
	// held is how many keys a tripped budget holds.
	held int
}

// settle runs and applies everything queued, and reports each outcome.
func settle(ctx context.Context, rt *runtime.Runtime) (settled, error) {
	outcomes, err := rt.Settle(ctx)
	if err != nil {
		return settled{}, err
	}
	s := settled{outcomes: make([]Shown, 0, len(outcomes))}
	for _, outcome := range outcomes {
		shown := Shown{Outcome: outcome}
		switch outcome.Result {
		case runtime.BudgetTripped:
			s.held += outcome.Held
		case runtime.Ran:
			entries, err := rt.EffectsOf(ctx, outcome.RunID)
			if err != nil {
				return settled{}, err
			}
			for _, entry := range entries {
				shown.Recorded = append(shown.Recorded, entry.Status)
			}
		}
		s.outcomes = append(s.outcomes, shown)
	}
	return s, nil
}

func (s settled) lines() []string {
	lines := make([]string, len(s.outcomes))
	for i, outcome := range s.outcomes {
		lines[i] = outcome.String()
	}
	return lines
}
