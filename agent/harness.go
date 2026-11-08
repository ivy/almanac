package agent

import (
	"context"
	"encoding/json"

	"github.com/ivy/almanac/hostile"
)

// Pass shows that a call came through [Call.Run] or [Screen], which only the run's context
// makes, so that every model call is recorded. Only this package issues one.
type Pass struct{ issued bool }

// IsZero reports whether p was not issued by this package. A harness may refuse such a call.
func (p Pass) IsZero() bool { return !p.issued }

// Harness runs an agent session: a prompt, a grant the sandbox enforces, and input as JSON in
// which hostile values are tagged. It returns the agent's answer as JSON.
type Harness interface {
	Run(ctx context.Context, pass Pass, prompt string, grant Grant, input json.RawMessage) (json.RawMessage, error)
}

// Classifier is a cheap model that answers a yes-or-no question about some state with a
// probability. It gates spending and never safety, because hostile text can move it.
type Classifier interface {
	YesProbability(ctx context.Context, pass Pass, question string, state json.RawMessage) (float64, error)
}

// Screen asks c how likely the answer to question is yes, for text. Only the run's context
// should call it, so that the answer is recorded.
func Screen(ctx context.Context, c Classifier, question string, text hostile.Of[string]) (float64, error) {
	state, err := json.Marshal(text)
	if err != nil {
		return 0, err
	}
	return c.YesProbability(ctx, Pass{issued: true}, question, state)
}
