package hostile

// Closed marks a type whose values form a closed set: the only thing [Classify] sorts hostile
// text into. Embed it in a struct with unexported fields, and declare each value once, as a
// package-level variable:
//
//	type Verdict struct {
//		hostile.Closed
//		name string
//	}
//
//	var (
//		NoConcerns = Verdict{name: "no concerns"}
//		Concerns   = Verdict{name: "concerns"}
//	)
//
// tools/hostilevet refuses an exported field in such a struct, a composite literal of it
// anywhere but a package-level variable, and assigning to one of its fields, so no code can
// make a value outside the declared set. A closed type that is decoded from JSON implements
// json.Unmarshaler by choosing among its declared values.
type Closed struct{}

func (Closed) closed() {}

// Finite is the constraint a closed set satisfies: a comparable type that embeds [Closed].
type Finite interface {
	comparable
	closed()
}
