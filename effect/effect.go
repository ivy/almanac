// Package effect is the closed set of ways a workflow touches the world: attest a status,
// await a change, file work, ask a person, push a checked patch (R-21). Nothing here merges or
// closes: the repository's rules and Renovate do that (principle 8).
//
// [Effect] has an unexported method, so no package can add an effect, and the set is the five
// types below.
package effect

import (
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/patch"
)

// Effect is something a workflow decided to do. The runtime applies it, in live mode only.
type Effect interface {
	Kind() Kind
	effect()
}

// Attest publishes a status check on a commit. The repository's rules decide what it blocks.
type Attest struct {
	Context string       `json:"context"`
	State   AttestState  `json:"state"`
	Repo    hostile.Slug `json:"repo"`
	Sha     hostile.Sha  `json:"sha"`
}

// Await does nothing until a named fact changes.
type Await struct {
	Until string `json:"until"`
}

// File hands work to a tracker. The body keeps its taint all the way to the tracker.
type File struct {
	Title  hostile.Title
	Target hostile.Slug
	Body   hostile.Of[string]
}

// Ask asks a person. A workflow sees that it has asked, and does not ask again.
type Ask struct {
	Question hostile.Title
	Target   hostile.Slug
	// Pull is the pull request the question is about, so it reaches a person there, or 0.
	Pull uint64
	Body hostile.Of[string]
}

// Push commits a checked patch to a pull request's own branch. A forge refuses it unless
// Branch is that pull request's head and has not moved since the patch was checked.
type Push struct {
	Repo    hostile.Slug
	Pull    uint64
	Branch  hostile.Slug
	Message string
	// Hold is a status set pending on the new commit before the branch moves, so nothing can
	// merge between the push and CI finishing. Empty sets none.
	Hold  string
	Patch patch.Checked
}

func (Attest) Kind() Kind { return KindAttest }
func (Await) Kind() Kind  { return KindAwait }
func (File) Kind() Kind   { return KindFile }
func (Ask) Kind() Kind    { return KindAsk }
func (Push) Kind() Kind   { return KindPush }

func (Attest) effect() {}
func (Await) effect()  {}
func (File) effect()   {}
func (Ask) effect()    {}
func (Push) effect()   {}

// Summary is what a guard sees of an effect. It holds no event text, so text cannot inject a
// guard.
type Summary struct {
	Kind Kind `json:"kind"`
	// Attests is the state an Attest sets, and zero for any other effect.
	Attests AttestState `json:"attests,omitzero"`
}

// Summarize is what a guard sees of e.
func Summarize(e Effect) Summary {
	summary := Summary{Kind: e.Kind()}
	if attest, ok := e.(Attest); ok {
		summary.Attests = attest.State
	}
	return summary
}
