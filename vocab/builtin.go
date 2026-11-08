package vocab

import (
	"errors"
	"slices"

	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/patch"
)

// Attempts is the fact "attempts": what was tried on a run's key before. The engine fills
// Spent from the workflow's attempts limit.
type Attempts struct {
	// Used is how many patches the key has had within the attempt window.
	Used int `json:"used"`
	// Asked is whether a person was already asked about the key.
	Asked bool `json:"asked"`
	// Spent is whether Used has reached the workflow's attempts limit.
	Spent bool `json:"spent"`
}

// PatchOutcome is the fact "checked": what the patch policy made of an agent's patch. The
// checked patch itself stays with the engine, for a push.
type PatchOutcome int

// The outcomes of checking a patch.
const (
	// PatchAccepted is a patch the policy accepted, which a push may take.
	PatchAccepted PatchOutcome = iota + 1
	// PatchEmpty is a patch that changes nothing.
	PatchEmpty
	// PatchRefused is a patch the policy refused for any other reason.
	PatchRefused
)

var outcomeNames = []string{PatchAccepted: "accepted", PatchEmpty: "empty", PatchRefused: "refused"}

func (o PatchOutcome) String() string {
	if o < PatchAccepted || o > PatchRefused {
		return "unknown"
	}
	return outcomeNames[o]
}

// MarshalText writes the outcome's name, such as "refused".
func (o PatchOutcome) MarshalText() ([]byte, error) {
	if o < PatchAccepted || o > PatchRefused {
		return nil, errors.New("vocab: not a patch outcome")
	}
	return []byte(o.String()), nil
}

// UnmarshalText reads an outcome's name, and refuses any other.
func (o *PatchOutcome) UnmarshalText(text []byte) error {
	i := slices.Index(outcomeNames, string(text))
	if i < int(PatchAccepted) {
		return errors.New("vocab: not a patch outcome")
	}
	*o = PatchOutcome(i)
	return nil
}

// OutcomeOf is the outcome of checking a patch, from what patch.Policy.Check returned.
func OutcomeOf(err error) PatchOutcome {
	var rejection patch.Rejection
	switch {
	case err == nil:
		return PatchAccepted
	case errors.As(err, &rejection) && rejection == patch.Empty:
		return PatchEmpty
	default:
		return PatchRefused
	}
}

// PolicyPart is a patch policy.
type PolicyPart struct{ part }

// Policy registers a patch policy, for a check_patch step.
func Policy(v *Vocabulary, name, label string, policy patch.Policy) PolicyPart {
	v.add(&Entry{Kind: PolicyKind, Name: name, Label: label, policy: policy}, "")
	return PolicyPart{part{name}}
}

// The built-in parts every vocabulary holds. They read the built-in facts "age", "attempts"
// and "checked".
var (
	Cooled  = PredicatePart[cooled.Age]{part: part{"age.cooled"}}
	Young   = PredicatePart[cooled.Age]{part: part{"age.young"}}
	Unknown = PredicatePart[cooled.Age]{part: part{"age.unknown"}}

	Spent = PredicatePart[Attempts]{part: part{"attempts.spent"}}
	Asked = PredicatePart[Attempts]{part: part{"attempts.asked"}}

	Accepted = PredicatePart[PatchOutcome]{part: part{"checked.accepted"}}
	Empty    = PredicatePart[PatchOutcome]{part: part{"checked.empty"}}
	Refused  = PredicatePart[PatchOutcome]{part: part{"checked.refused"}}

	DefaultPolicy = PolicyPart{part{"patch.default"}}
)

func builtins(v *Vocabulary) {
	Predicate(v, Cooled.name, "the release is older than the cooldown", isAge(cooled.Passed))
	Predicate(v, Young.name, "the release is younger than the cooldown", isAge(cooled.Young))
	Predicate(v, Unknown.name, "nothing says when the release was published", isAge(cooled.Unknown))
	Predicate(v, Spent.name, "the key's attempts are spent", attemptsSpent)
	Predicate(v, Asked.name, "a person was already asked", attemptsAsked)
	Predicate(v, Accepted.name, "the patch policy accepted the patch", isOutcome(PatchAccepted))
	Predicate(v, Empty.name, "the patch changes nothing", isOutcome(PatchEmpty))
	Predicate(v, Refused.name, "the patch policy refused the patch", isOutcome(PatchRefused))
	Policy(v, DefaultPolicy.name, "the default patch policy", patch.DefaultPolicy())
}

func isAge(want cooled.Age) func(cooled.Age) bool {
	return func(age cooled.Age) bool { return age == want }
}

func isOutcome(want PatchOutcome) func(PatchOutcome) bool {
	return func(outcome PatchOutcome) bool { return outcome == want }
}

func attemptsSpent(a Attempts) bool { return a.Spent }

func attemptsAsked(a Attempts) bool { return a.Asked }
