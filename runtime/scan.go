package runtime

import (
	"context"

	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/workflow"
)

// Backfill is what a scan found.
type Backfill struct {
	Found  int `json:"found"`
	Queued int `json:"queued"`
	// Unchanged were already seen, so nothing about them has changed since the last scan.
	Unchanged int `json:"unchanged"`
	// Rejected did not parse. These should be looked at: a subject here is being missed.
	Rejected int `json:"rejected"`
	// Skipped is whether another scan was already running, so this one did nothing.
	Skipped bool `json:"skipped"`
}

// Backfill lists every workflow's current subjects, in repo alone when it is given, and
// ingests them as scanned events. Scanning twice is harmless, because a scanned event has a
// stable delivery ID. A scan that fails, such as one naming a repository it cannot see, is an
// almanac::scan::failed error.
func (r *Runtime) Backfill(ctx context.Context, repo *hostile.Slug) (Backfill, error) {
	var report Backfill
	if !r.scanning.TryLock() {
		report.Skipped = true
		return report, nil
	}
	defer r.scanning.Unlock()
	scanner := workflow.NewScanner(r.providers)
	if repo != nil {
		scanner = scanner.Only(*repo)
	}
	for _, w := range r.workflows {
		found, err := w.Scan(ctx, scanner)
		if err != nil {
			return report, diag.Scan(w.Name(), err.Error())
		}
		for _, event := range found {
			report.Found++
			ingest, err := r.Ingest(ctx, Envelope{
				Source:     "scan",
				DeliveryID: event.DeliveryID,
				Kind:       event.Kind,
				Origin:     store.Scan,
				Payload:    event.Payload,
			})
			if err != nil {
				return report, err
			}
			switch ingest {
			case store.Duplicate:
				report.Unchanged++
			case store.Rejected:
				report.Rejected++
			default:
				report.Queued++
			}
		}
	}
	return report, nil
}

// Tick runs a scheduled sweep and scan if one is due, and reports whether it did. Call it
// often: it does nothing until the scan interval has passed since the last, and the first call
// is due. A sweep that fails is logged, and the scan goes ahead.
func (r *Runtime) Tick(ctx context.Context) (Backfill, bool, error) {
	if !r.scanDue() {
		return Backfill{}, false, nil
	}
	if sweep, ok := providers.Lookup[effect.Sweep](r.providers); ok {
		if _, err := sweep.Sweep(ctx); err != nil {
			r.log.Warn("could not sweep for failed deliveries", "error", err)
		}
	}
	report, err := r.Backfill(ctx, nil)
	return report, true, err
}

// scanDue reports whether a scheduled scan is due, and if so marks it started.
func (r *Runtime) scanDue() bool {
	r.lastScan.Lock()
	defer r.lastScan.Unlock()
	now := r.clock.Now()
	if !r.lastScan.at.IsZero() && now.Sub(r.lastScan.at) < r.limits.ScanInterval {
		return false
	}
	r.lastScan.at = now
	return true
}
