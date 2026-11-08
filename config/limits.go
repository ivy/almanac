package config

import "time"

// Limits bound what an almanac does. A binary declares them, and config can
// only tighten them: [Limits.Tighten] never widens one, however often it is
// applied.
type Limits struct {
	// MaxChainDepth is how many events can follow from one another.
	MaxChainDepth int
	// RunsPerHour lowers each workflow's own budget.
	RunsPerHour Cap
	// OverallRunsPerHour caps all workflows together.
	OverallRunsPerHour Cap
	// Concurrency is how many runs go at once.
	Concurrency int
	// ScanInterval is how often scheduled scans run. Tightening lengthens it,
	// because scanning costs requests.
	ScanInterval time.Duration
}

// DefaultLimits are the limits an almanac runs with when nothing tightens them.
func DefaultLimits() Limits {
	return Limits{
		MaxChainDepth: 3,
		Concurrency:   4,
		ScanInterval:  15 * time.Minute,
	}
}

// Tighten returns the tighter of l and other in every limit.
func (l Limits) Tighten(other Limits) Limits {
	return Limits{
		MaxChainDepth:      min(l.MaxChainDepth, other.MaxChainDepth),
		RunsPerHour:        l.RunsPerHour.Tighten(other.RunsPerHour),
		OverallRunsPerHour: l.OverallRunsPerHour.Tighten(other.OverallRunsPerHour),
		Concurrency:        min(l.Concurrency, other.Concurrency),
		ScanInterval:       max(l.ScanInterval, other.ScanInterval),
	}
}

// Cap is an upper bound, such as a ceiling on runs per hour. The zero Cap is
// no bound.
type Cap struct {
	bound int
	set   bool
}

// CapAt is a bound of n.
func CapAt(n int) Cap { return Cap{bound: n, set: true} }

// Value is the bound, and false when there is none.
func (c Cap) Value() (int, bool) { return c.bound, c.set }

// Or is the bound, or otherwise when there is none.
func (c Cap) Or(otherwise int) int {
	if c.set {
		return c.bound
	}
	return otherwise
}

// Tighten returns the lower of two bounds. Any bound is lower than none.
func (c Cap) Tighten(other Cap) Cap {
	switch {
	case !c.set:
		return other
	case !other.set:
		return c
	default:
		return CapAt(min(c.bound, other.bound))
	}
}
