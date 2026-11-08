package wire

import (
	"bytes"
	"encoding/json"
)

// FromRest is an event a scan can build from a REST response. Its projections pair where a
// value is in the response with where the event expects it, so a scanned event takes the same
// path as a webhook.
type FromRest interface {
	Event
	// Operation is the REST operation the response comes from, as the API's spec names it.
	Operation() string
	// Projections place the response's fields where the webhook has them.
	Projections() []Projection
}

// Projection moves the value at From in a REST response to To in a webhook-shaped payload.
// Both are JSON pointers.
type Projection struct {
	From string
	To   string
}

// WebhookPayload builds a payload shaped like the webhook from a REST response, holding only
// the projected fields. A field missing from the response is null in the payload.
func WebhookPayload(rest []byte, projections []Projection) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(rest))
	decoder.UseNumber()
	var response any
	if err := decoder.Decode(&response); err != nil {
		return nil, &Malformed{}
	}
	payload := map[string]any{}
	for _, projection := range projections {
		value, _ := lookup(response, projection.From)
		place(payload, projection.To, value)
	}
	return json.Marshal(payload)
}

// place sets the value at pointer, making the objects on the way. It gives up where the way
// is something other than an object.
func place(root map[string]any, pointer string, value any) {
	current := root
	parts := tokens(pointer)
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part]
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		object, ok := next.(map[string]any)
		if !ok {
			return
		}
		current = object
	}
	current[parts[len(parts)-1]] = value
}
