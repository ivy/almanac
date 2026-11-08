package wire

// Event is a type a workflow can trigger on. It is generated from a spec where one exists and
// written by hand otherwise. Kind and AlsoKinds have value receivers and Parse a pointer
// receiver, so a pointer to the event type is the Event:
//
//	func (Opened) Kind() string         { return "github.pull_request.opened" }
//	func (Opened) AlsoKinds() []string  { return nil }
//	func (e *Opened) Parse(p *wire.Payload) {
//		e.Number = wire.Field[uint64](p, "/number")
//		e.Title = p.Text("/pull_request/title")
//	}
type Event interface {
	// Kind is the kind of delivery this event is parsed from.
	Kind() string
	// AlsoKinds are other kinds it is parsed from, for a condition that arrives in several
	// shapes.
	AlsoKinds() []string
	// Parse reads the event's fields from a payload. A read that fails is kept on the
	// payload, and the first one is the parse's error.
	Parse(p *Payload)
}

// Parse reads an event of type E from a raw payload. The event can hold only clean values and
// hostile text. A payload that is not JSON, or a field that is missing or the wrong shape, is
// a *[Malformed].
func Parse[E any, P interface {
	*E
	Event
}](raw []byte) (E, error) {
	var event E
	payload, err := newPayload(raw)
	if err != nil {
		return event, err
	}
	P(&event).Parse(payload)
	if err := payload.Err(); err != nil {
		var zero E
		return zero, err
	}
	return event, nil
}
