package github

import (
	"context"
	"net/http"
	"time"
)

// Post and Patch are the writes Forge, Issues and the sweep make, for specs of the client
// itself. They are not exported, so nothing outside this package can write.
func (c *Client) Post(ctx context.Context, path string, body any) error {
	return c.write(ctx, http.MethodPost, path, body, nil)
}

func (c *Client) Patch(ctx context.Context, path string, body any) error {
	return c.write(ctx, http.MethodPatch, path, body, nil)
}

func (c *Client) Redeliver(ctx context.Context, id uint64) error { return c.redeliver(ctx, id) }

// StallAfter replaces how long a request may wait for a byte, so a spec of a stalled forge
// need not wait half a minute per attempt.
func StallAfter(d time.Duration) Option {
	return func(c *Client) { c.http = stallingClient(d) }
}
