package sandbox

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// AbandonLimit is how long ending a sandbox may take once its run has been abandoned. A stop
// with no grace period takes about a second, and a stopping daemon may end several sandboxes in
// turn within its own stop timeout: a run's fetch sandbox, then its agent's.
const AbandonLimit = 3 * time.Second

// errAbandonLimit is why a destroy was cut short.
var errAbandonLimit = errors.New("abandon limit")

// End destroys id once its owner is done with it, whether the work succeeded, failed, or was
// abandoned. It destroys even when ctx has ended, as it has for a run a stopping daemon gave
// up on, and returns only once the provider has finished, so a process that exits next does
// not take the stop down with it.
//
// Once ctx has ended, the destroy gets at most AbandonLimit. A sandbox still stopping then
// ends itself when its lifetime runs out.
func End(ctx context.Context, sb Sandbox, id ID) error { return end(ctx, sb, id, AbandonLimit) }

func end(ctx context.Context, sb Sandbox, id ID, limit time.Duration) error {
	ending, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	defer cancel(nil)
	// The limit starts when ctx ends, which may be before or during the destroy.
	stopLimiting := context.AfterFunc(ctx, func() {
		timer := time.NewTimer(limit)
		defer timer.Stop()
		select {
		case <-timer.C:
			cancel(errAbandonLimit)
		case <-ending.Done():
		}
	})
	defer stopLimiting()
	err := sb.Destroy(ending, id)
	if err != nil && errors.Is(context.Cause(ending), errAbandonLimit) {
		return &Error{Op: "destroy", Sandbox: id, Err: fmt.Errorf(
			"still stopping %s after its run was abandoned; it ends itself when its lifetime runs out, "+
				"and until then it holds the resources its spec gave it", limit)}
	}
	return err
}
