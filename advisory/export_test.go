package advisory

import (
	"time"

	"github.com/ivy/almanac/internal/stall"
)

// StallAfter replaces how long c waits for a byte, so a spec of a stalled feed need not wait
// half a minute.
func StallAfter(c *CveNow, d time.Duration) *CveNow {
	c.client = stall.After(d)
	return c
}
