package effect

import "errors"

// Kind is one of the five effects. A workflow declares which it may produce, and the runtime
// denies any other.
type Kind int

const (
	KindAttest Kind = iota + 1
	KindAwait
	KindFile
	KindAsk
	KindPush
)

var kindNames = map[Kind]string{KindAttest: "Attest", KindAwait: "Await", KindFile: "File", KindAsk: "Ask", KindPush: "Push"}

// Kinds is every kind of effect, in order.
func Kinds() []Kind { return []Kind{KindAttest, KindAwait, KindFile, KindAsk, KindPush} }

func (k Kind) String() string { return nameOf(kindNames, k, "invalid kind") }

// MarshalText writes the kind's name, such as "Push".
func (k Kind) MarshalText() ([]byte, error) { return marshalName(kindNames, k) }

// UnmarshalText reads a kind's name, and refuses any other.
func (k *Kind) UnmarshalText(text []byte) error { return unmarshalName(kindNames, k, text) }

// AttestState is a status check's state. Pending holds a merge as firmly as Failure.
type AttestState int

const (
	Pending AttestState = iota + 1
	Success
	Failure
)

var stateNames = map[AttestState]string{Pending: "Pending", Success: "Success", Failure: "Failure"}

func (s AttestState) String() string { return nameOf(stateNames, s, "invalid state") }

// MarshalText writes the state's name, such as "Pending".
func (s AttestState) MarshalText() ([]byte, error) { return marshalName(stateNames, s) }

// UnmarshalText reads a state's name, and refuses any other.
func (s *AttestState) UnmarshalText(text []byte) error { return unmarshalName(stateNames, s, text) }

// Mode is whether effects reach the world. It is independent of where an event came from.
// The zero Mode is a dry run, so nothing acts unless something says live.
type Mode int

const (
	// DryRun records what would have happened and applies no effect. Agents still run, so a
	// dry run spends like a live one and keeps its budgets.
	DryRun Mode = iota
	// Live applies effects.
	Live
)

var modeNames = map[Mode]string{DryRun: "dry-run", Live: "live"}

func (m Mode) String() string { return nameOf(modeNames, m, "invalid mode") }

// MarshalText writes "live" or "dry-run".
func (m Mode) MarshalText() ([]byte, error) { return marshalName(modeNames, m) }

// UnmarshalText reads "live" or "dry-run", and refuses anything else.
func (m *Mode) UnmarshalText(text []byte) error { return unmarshalName(modeNames, m, text) }

// Scope is what an almanac's rows are stored under in this mode. A dry run keeps its own,
// because what it has seen and claimed must not stand in for work a live run still has to do.
func (m Mode) Scope(almanac string) string {
	if m == Live {
		return almanac
	}
	return almanac + ".dry-run"
}

func nameOf[T comparable](names map[T]string, value T, invalid string) string {
	if name, ok := names[value]; ok {
		return name
	}
	return invalid
}

func marshalName[T comparable](names map[T]string, value T) ([]byte, error) {
	name, ok := names[value]
	if !ok {
		return nil, errors.New("effect: an invalid value has no stored form")
	}
	return []byte(name), nil
}

// errUnknownName never quotes the text it refused, which may have come from outside.
var errUnknownName = errors.New("effect: not a known name")

func unmarshalName[T comparable](names map[T]string, value *T, text []byte) error {
	for known, name := range names {
		if name == string(text) {
			*value = known
			return nil
		}
	}
	return errUnknownName
}
