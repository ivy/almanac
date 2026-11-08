package runtime

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/wire"
	"github.com/ivy/almanac/workflow"
)

// Envelope is a delivery as it reaches the runtime: from a webhook, a scan or a simulation.
type Envelope struct {
	// Source is where the delivery came from, such as "github" or "scan". A delivery ID is
	// unique within its source.
	Source     string
	DeliveryID string
	Kind       string
	// Depth is how many of Almanac's own effects are known to have led here.
	Depth   int
	Origin  store.Origin
	Payload json.RawMessage
}

// Ingest admits a delivery: the first workflow that handles its kind identifies it, and its
// event waits for its key. A delivery is seen only once Ingest returns without error, so after
// an error it can be delivered again.
func (r *Runtime) Ingest(ctx context.Context, e Envelope) (store.Ingest, error) {
	admission, err := r.admission(ctx, e)
	if err != nil {
		return "", err
	}
	return r.store.Admit(ctx, e.Source, e.DeliveryID, admission)
}

func (r *Runtime) admission(ctx context.Context, e Envelope) (store.Admission, error) {
	if e.Depth > r.limits.MaxChainDepth {
		return store.Drop(store.ChainStopped), nil
	}
	if r.isEcho(e.Payload) {
		return store.Drop(store.Echo), nil
	}
	i := slices.IndexFunc(r.workflows, func(w workflow.Registered) bool { return w.Handles(e.Kind) })
	if i < 0 {
		return store.Drop(store.Unrouted), nil
	}
	w := r.workflows[i]
	identity, err := w.Identify(e.Payload)
	if err != nil {
		return store.Drop(store.Rejected), nil
	}
	if !identity.Wanted {
		return store.Drop(store.Ignored), nil
	}
	depth, err := r.inferredDepth(ctx, w.Name(), identity.Key, e.Depth)
	if err != nil {
		return store.Admission{}, err
	}
	if depth > r.limits.MaxChainDepth {
		return store.Drop(store.ChainStopped), nil
	}
	return store.Queue(store.Event{
		Workflow: w.Name(),
		Key:      identity.Key,
		Payload:  e.Payload,
		Subjects: identity.Subjects,
		Origin:   e.Origin,
		Depth:    depth,
	}), nil
}

// inferredDepth is how deep in a chain an event on a key is. A webhook does not say what caused
// it, so its depth comes from what Almanac recently did to the key: CI after a push inherits the
// push's depth, whoever sent the event.
func (r *Runtime) inferredDepth(ctx context.Context, workflow, key string, stated int) (int, error) {
	traces, err := r.store.Traces(ctx, workflow, key, r.clock.Now().Add(-LedgerWindow))
	if err != nil {
		return 0, err
	}
	depth := stated
	for _, t := range traces {
		depth = max(depth, t.Depth+1)
	}
	return depth, nil
}

// isEcho reports whether Almanac sent this: its sender is one of the logins it acts as. The
// comparison is exact, so hostile text cannot match by accident, and only the login is read.
func (r *Runtime) isEcho(payload json.RawMessage) bool {
	sent, err := wire.Parse[sender](payload)
	if err != nil {
		return false
	}
	return hostile.Narrow(hostile.Zip(sent.login, hostile.New(r.providers.Identity())), isOneOf)
}

// isOneOf reports whether a login is present and one of the logins beside it.
func isOneOf(p hostile.Pair[*string, []string]) bool {
	return p.First != nil && slices.Contains(p.Second, *p.First)
}

// sender is who sent a delivery, of any kind.
type sender struct {
	login hostile.Of[*string]
}

func (sender) Kind() string        { return "" }
func (sender) AlsoKinds() []string { return nil }

func (s *sender) Parse(p *wire.Payload) {
	s.login = p.OptionalText("/sender/login")
}
