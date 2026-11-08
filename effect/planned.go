package effect

import (
	"encoding/json"
	"errors"

	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/patch"
)

// Planned is an effect as the outbox stores it, and exactly one of its fields is set. Its JSON
// names the kind, as {"Push": {...}}, and hostile bodies and patches stay tagged in it.
type Planned struct {
	Attest *Attest   `json:"Attest,omitempty"`
	Await  *Await    `json:"Await,omitempty"`
	File   *Issue    `json:"File,omitempty"`
	Ask    *Question `json:"Ask,omitempty"`
	Push   *Commit   `json:"Push,omitempty"`
}

// Issue is work for a tracker to file.
type Issue struct {
	Title  string             `json:"title"`
	Target hostile.Slug       `json:"target"`
	Body   hostile.Of[string] `json:"body"`
}

// Question is a question for a tracker to put to a person.
type Question struct {
	Question string       `json:"question"`
	Target   hostile.Slug `json:"target"`
	// Pull is the pull request the question is about, or 0.
	Pull uint64             `json:"pull,omitempty"`
	Body hostile.Of[string] `json:"body"`
}

// Commit is a commit for a forge to make: a checked patch, on a pull request's own branch,
// on top of the commit it was checked against.
type Commit struct {
	Repo    hostile.Slug `json:"repo"`
	Pull    uint64       `json:"pull"`
	Branch  hostile.Slug `json:"branch"`
	Message string       `json:"message"`
	// Hold is a status to set pending on the new commit before the branch moves, or empty.
	Hold string `json:"hold,omitempty"`
	// Base is the commit the patch was checked against. The forge refuses the push if the
	// branch has moved from it.
	Base hostile.Sha `json:"base"`
	// Patch stays tagged as hostile when stored. It was checked before it was planned.
	Patch hostile.Of[patch.Patch] `json:"patch"`
}

// Plan is e as the outbox stores it. It refuses an effect missing what it needs, such as a
// Push whose patch no policy checked.
func Plan(e Effect) (Planned, error) {
	switch e := e.(type) {
	case Attest:
		if e.Context == "" || stateNames[e.State] == "" {
			return Planned{}, errors.New("effect: an Attest needs a context and a state: set both where the workflow decides it")
		}
		return Planned{Attest: &e}, nil
	case Await:
		if e.Until == "" {
			return Planned{}, errors.New("effect: an Await needs the fact it waits on: name it where the workflow decides it")
		}
		return Planned{Await: &e}, nil
	case File:
		if e.Title.String() == "" {
			return Planned{}, errors.New("effect: a File needs a title: make one with hostile.Titlef where the workflow decides it")
		}
		return Planned{File: &Issue{Title: e.Title.String(), Target: e.Target, Body: e.Body}}, nil
	case Ask:
		if e.Question.String() == "" {
			return Planned{}, errors.New("effect: an Ask needs a question: make one with hostile.Titlef where the workflow decides it")
		}
		return Planned{Ask: &Question{Question: e.Question.String(), Target: e.Target, Pull: e.Pull, Body: e.Body}}, nil
	case Push:
		if e.Patch.IsZero() {
			return Planned{}, errors.New("effect: a Push needs a patch that patch.Policy.Check accepted: check it where the workflow decides, and ask a person if the policy refuses it")
		}
		if e.Message == "" {
			return Planned{}, errors.New("effect: a Push needs a commit message: set one where the workflow decides it")
		}
		return Planned{Push: &Commit{
			Repo:    e.Repo,
			Pull:    e.Pull,
			Branch:  e.Branch,
			Message: e.Message,
			Hold:    e.Hold,
			Base:    e.Patch.Base(),
			Patch:   e.Patch.Patch(),
		}}, nil
	}
	return Planned{}, errors.New("effect: no effect to plan")
}

// Kind is the kind of the effect p holds.
func (p Planned) Kind() Kind {
	switch {
	case p.Attest != nil:
		return KindAttest
	case p.Await != nil:
		return KindAwait
	case p.File != nil:
		return KindFile
	case p.Ask != nil:
		return KindAsk
	case p.Push != nil:
		return KindPush
	}
	return 0
}

// UnmarshalJSON reads what Plan stores, and refuses anything that holds no effect or more
// than one.
func (p *Planned) UnmarshalJSON(data []byte) error {
	type stored Planned
	var read stored
	if err := json.Unmarshal(data, &read); err != nil {
		return err
	}
	set := 0
	for _, isSet := range []bool{read.Attest != nil, read.Await != nil, read.File != nil, read.Ask != nil, read.Push != nil} {
		if isSet {
			set++
		}
	}
	if set != 1 {
		return errors.New(`effect: a planned effect holds exactly one of "Attest", "Await", "File", "Ask" or "Push"`)
	}
	*p = Planned(read)
	return nil
}
