package app

import (
	"time"
)

// WithGrace is a, giving an in-flight run grace once run is asked to stop, so a spec need not
// wait DefaultGrace to see a run abandoned.
func WithGrace(a Almanac, grace time.Duration) Almanac {
	a.grace = grace
	return a
}

// HealthAddress is where a daemon with cfg answers health checks.
var HealthAddress = healthAddress
