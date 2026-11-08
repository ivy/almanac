package dsl

import (
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/vocab"
)

// Effect is a template for an effect, filled from facts when its rule matches.
type Effect interface {
	// Model is the template as the model holds it.
	Model() model.Effect
}

// The states an Attest sets.
const (
	Pending = effect.Pending
	Success = effect.Success
	Failure = effect.Failure
)

// Title holds a title's format, which must be a string constant, for Ask and File. Fill its
// verbs, such as %d, with clean facts given after it.
var Title = hostile.NewFormat

type template model.Effect

func (t template) Model() model.Effect { return model.Effect(t) }

// Attest sets the status context to state on sha in repo.
func Attest(context string, state effect.AttestState, repo CleanFact[hostile.Slug], sha CleanFact[hostile.Sha]) Effect {
	return template{Attest: &model.AttestEffect{Context: context, State: state, Repo: repo.name, Sha: sha.name}}
}

// Await does nothing until something changes, for the reason until, such as "CI to finish".
func Await(until string) Effect {
	return template{Await: &model.AwaitEffect{Until: until}}
}

// Body is what an issue or a question says.
type Body struct{ body model.Body }

// Saying is a body of literal text.
func Saying(text string) Body { return Body{model.Body{Text: text}} }

// Showing is a body showing the hostile fact h through m, which keeps it hostile all the way to
// the tracker.
func Showing[T any](h HostileFact[T], m vocab.MapPart[T]) Body {
	return Body{model.Body{Showing: h.name, Map: m.Name()}}
}

// ShowingText is a body showing the hostile text h as it is.
func ShowingText(h HostileFact[string]) Body { return Body{model.Body{Showing: h.name}} }

func title(format hostile.Format, values []Clean) model.Title {
	t := model.Title{Format: format}
	for _, v := range values {
		t.Values = append(t.Values, v.ref())
	}
	return t
}

// AskEffect asks a person.
type AskEffect struct{ ask model.AskEffect }

// Ask asks a person, in the tracker for target, with a title from format and clean values.
func Ask(target CleanFact[hostile.Slug], format hostile.Format, values ...Clean) AskEffect {
	return AskEffect{model.AskEffect{Target: target.name, Title: title(format, values)}}
}

// AskOnce asks as Ask does, unless a person was already asked about the key, when it produces
// nothing, or what ThenAwait names.
func AskOnce(target CleanFact[hostile.Slug], format hostile.Format, values ...Clean) AskEffect {
	a := Ask(target, format, values...)
	a.ask.Once = true
	return a
}

// OnPull asks on the pull request number.
func (a AskEffect) OnPull(number CleanFact[uint64]) AskEffect {
	a.ask.Pull = number.name
	return a
}

// With is what the question says.
func (a AskEffect) With(body Body) AskEffect {
	a.ask.Body = &body.body
	return a
}

// ThenAwait awaits, for the reason until, once a person was already asked.
func (a AskEffect) ThenAwait(until string) AskEffect {
	a.ask.ThenAwait = until
	return a
}

// Model is the template as the model holds it.
func (a AskEffect) Model() model.Effect {
	ask := a.ask
	return model.Effect{Ask: &ask}
}

// FileEffect files work in a tracker.
type FileEffect struct{ file model.FileEffect }

// File files work in the tracker for target, with a title from format and clean values.
func File(target CleanFact[hostile.Slug], format hostile.Format, values ...Clean) FileEffect {
	return FileEffect{model.FileEffect{Target: target.name, Title: title(format, values)}}
}

// With is what the issue says.
func (f FileEffect) With(body Body) FileEffect {
	f.file.Body = &body.body
	return f
}

// Model is the template as the model holds it.
func (f FileEffect) Model() model.Effect {
	file := f.file
	return model.Effect{File: &file}
}

// PushEffect pushes a checked patch.
type PushEffect struct{ push model.PushEffect }

// Push commits the patch checked accepted to branch, the head of pull request pull in repo,
// with message.
func Push(checked CleanFact[vocab.PatchOutcome], repo CleanFact[hostile.Slug], pull CleanFact[uint64], branch CleanFact[hostile.Slug], message string) PushEffect {
	return PushEffect{model.PushEffect{Checked: checked.name, Repo: repo.name, Pull: pull.name, Branch: branch.name, Message: message}}
}

// Holding sets the status context pending on the new commit before the branch moves, so
// nothing can merge between the push and CI finishing.
func (p PushEffect) Holding(context string) PushEffect {
	p.push.Hold = context
	return p
}

// Model is the template as the model holds it.
func (p PushEffect) Model() model.Effect {
	push := p.push
	return model.Effect{Push: &push}
}
