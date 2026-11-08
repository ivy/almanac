package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/providers"
)

// Delivery is what one attempt to apply an effect did.
type Delivery struct {
	// Key is the effect's idempotency key, which providers receive.
	Key string
	// Err is why this attempt failed, and nil when the effect was applied.
	Err error
}

// Deliver applies recorded effects, oldest first, and only in live mode: a dry run applies
// nothing, whatever its store holds. A failure leaves an effect to be retried by a later call,
// up to MaxDeliveryAttempts. Providers receive a stable key, so a crash between applying an
// effect and marking it delivered changes nothing when it is applied again.
func (r *Runtime) Deliver(ctx context.Context) ([]Delivery, error) {
	if r.mode != effect.Live {
		return nil, nil
	}
	entries, err := r.store.Undelivered(ctx)
	if err != nil {
		return nil, err
	}
	report := make([]Delivery, 0, len(entries))
	for _, entry := range entries {
		applied := r.apply(ctx, entry.Key, entry.Effect)
		if applied == nil {
			err = r.store.Delivered(ctx, entry.Key)
		} else {
			err = r.store.DeliveryFailed(ctx, entry.Key, applied.Error(), MaxDeliveryAttempts)
		}
		if err != nil {
			return report, err
		}
		report = append(report, Delivery{Key: entry.Key, Err: applied})
	}
	return report, nil
}

// apply hands one stored effect to its provider. An effect that cannot be read fails only its
// own delivery.
func (r *Runtime) apply(ctx context.Context, key string, stored json.RawMessage) error {
	var planned effect.Planned
	if err := json.Unmarshal(stored, &planned); err != nil {
		return fmt.Errorf("%s: the stored effect cannot be read, so it is not applied: %w", key, err)
	}
	switch {
	case planned.Await != nil:
		return nil
	case planned.Attest != nil:
		forge, err := provided[effect.Forge](r.providers, "Forge")
		if err != nil {
			return err
		}
		return forge.Attest(ctx, key, *planned.Attest)
	case planned.Push != nil:
		forge, err := provided[effect.Forge](r.providers, "Forge")
		if err != nil {
			return err
		}
		return forge.Push(ctx, key, *planned.Push)
	case planned.File != nil:
		tracker, err := provided[effect.Tracker](r.providers, "Tracker")
		if err != nil {
			return err
		}
		return tracker.File(ctx, key, *planned.File)
	case planned.Ask != nil:
		tracker, err := provided[effect.Tracker](r.providers, "Tracker")
		if err != nil {
			return err
		}
		return tracker.Ask(ctx, key, *planned.Ask)
	}
	return fmt.Errorf("%s: the stored effect holds no effect", key)
}

func provided[T any](set *providers.Set, name string) (T, error) {
	found, ok := providers.Lookup[T](set)
	if !ok {
		return found, fmt.Errorf("no %s is provided, so this effect cannot be applied: provide one where the almanac sets up its providers; start-up checks this in live mode", name)
	}
	return found, nil
}
