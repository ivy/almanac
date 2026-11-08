// Package closed holds closed types declared as hostile.Closed says, and the ways a value
// outside the declared set could be made.
package closed

import (
	"encoding/json"

	"github.com/ivy/almanac/hostile"
)

type State struct {
	hostile.Closed
	name string
}

var (
	Open    = State{name: "open"}
	Shut    = State{name: "closed"}
	Unknown = State{name: "unknown"}
	known   = []State{Open, Shut, {name: "draft"}}
)

func (s State) String() string { return s.name }

// UnmarshalJSON chooses among the declared values.
func (s *State) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return err
	}
	*s = Unknown
	for _, state := range known {
		if state.name == name {
			*s = state
		}
	}
	return nil
}

type Leaky struct {
	hostile.Closed
	Name string // want `closed type Leaky has exported field Name`
}

// Nested is closed through State.
type Nested struct {
	State
	Note string // want `closed type Nested has exported field Note`
}

type Counted struct {
	hostile.Closed
	count int
}

var made = func() State { return State{name: "made"} }() // want `a value of closed type State is declared once`

func stateOf(text string) State {
	if text == "open" {
		return Open
	}
	return State{name: text} // want `a value of closed type State is declared once`
}

func pointerOf(text string) *State {
	return &State{name: text} // want `a value of closed type State is declared once`
}

func listed(text string) []State {
	return []State{Open, {name: text}} // want `a value of closed type State is declared once`
}

func renamed(text string) State {
	state := Open
	state.name = text // want `a field of closed type State is never assigned`
	return state
}

func counted(text string) Counted {
	var c Counted
	c.count++ // want `a field of closed type Counted is never assigned`
	return c
}

func zero() State { return State{} }

func use() {
	_ = hostile.Classify(hostile.New("open"), stateOf)
	_, _, _, _, _, _ = made, pointerOf, listed, renamed, counted, zero
}
