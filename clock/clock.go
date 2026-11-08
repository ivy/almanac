// Package clock is the time, as something a test can control.
package clock

import (
	"sync"
	"time"
)

// Clock tells the time. Everything that decides by time reads it from a Clock, so that a test
// can move it.
type Clock interface {
	Now() time.Time
}

// System is the host's clock, in UTC.
type System struct{}

func (System) Now() time.Time { return time.Now().UTC() }

// Fake is a clock that moves only when told. Everything holding the same *Fake shares one time.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake is a clock stopped at now.
func NewFake(now time.Time) *Fake { return &Fake{now: now} }

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the clock forward by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}
