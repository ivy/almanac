package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
)

// Sweep asks GitHub to resend recent deliveries that failed and have not since succeeded,
// because GitHub never does this itself, and returns how many it asked for. A delivery that
// failed while the receiver was down, or while one almanac replaced another, is not lost.
// Each delivery is asked for once a sweep, at most mostRedeliveries of them. See
// https://docs.github.com/en/webhooks/using-webhooks/handling-failed-webhook-deliveries.
func (c *Client) Sweep(ctx context.Context) (int, error) {
	var attempts []struct {
		ID         *uint64 `json:"id"`
		GUID       string  `json:"guid"`
		StatusCode *int    `json:"status_code"`
	}
	if err := c.getJSON(ctx, "/app/hook/deliveries?per_page="+strconv.Itoa(perPage), &attempts); err != nil {
		return 0, err
	}
	succeeded := func(code *int) bool { return code != nil && *code >= 0 && *code < 300 }
	var delivered, asked []string
	for _, attempt := range attempts {
		if succeeded(attempt.StatusCode) {
			delivered = append(delivered, attempt.GUID)
		}
	}
	for _, attempt := range attempts {
		if succeeded(attempt.StatusCode) || slices.Contains(delivered, attempt.GUID) || slices.Contains(asked, attempt.GUID) {
			continue
		}
		if attempt.ID != nil {
			if err := c.redeliver(ctx, *attempt.ID); err != nil {
				return len(asked), err
			}
			asked = append(asked, attempt.GUID)
		}
		if len(asked) >= mostRedeliveries {
			break
		}
	}
	return len(asked), nil
}

// redeliver asks GitHub to send a delivery again.
func (c *Client) redeliver(ctx context.Context, id uint64) error {
	return c.write(ctx, http.MethodPost, fmt.Sprintf("/app/hook/deliveries/%d/attempts", id), json.RawMessage(`{}`), nil)
}
