package renovate

import (
	"context"
	"errors"
	"sync"

	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/hostile"
)

// Reads are what a Fake answers each read with.
type Reads struct {
	Pull      Pull
	Logs      hostile.Of[string]
	Lines     hostile.Of[[]Line]
	Security  SecurityView
	Advisory  Advisory
	Diff      PackageDiff
	Published cooled.Published
}

// Fake is a GitHub that answers from its Reads and counts every read.
type Fake struct {
	mu    sync.Mutex
	reads Reads
	down  bool
	calls int
}

// Answering is a GitHub that answers with reads.
func Answering(reads Reads) *Fake { return &Fake{reads: reads} }

// Down is a GitHub that fails every read, as one that is unreachable does.
func Down() *Fake { return &Fake{down: true} }

// ErrDown is what a Fake that is Down fails with.
var ErrDown = errors.New("the fake GitHub is down")

// Calls is how many reads were attempted, including those that failed.
func (f *Fake) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func read[T any](f *Fake, pick func(Reads) T) (T, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.down {
		var zero T
		return zero, ErrDown
	}
	return pick(f.reads), nil
}

func (f *Fake) PullForBranch(context.Context, BuildEvent) (Pull, error) {
	return read(f, func(r Reads) Pull { return r.Pull })
}

func (f *Fake) FailureLogs(context.Context, Pull) (hostile.Of[string], error) {
	return read(f, func(r Reads) hostile.Of[string] { return r.Logs })
}

func (f *Fake) UpdatedLines(context.Context, Pull) (hostile.Of[[]Line], error) {
	return read(f, func(r Reads) hostile.Of[[]Line] { return r.Lines })
}

func (f *Fake) SecurityPull(context.Context, SecurityEvent) (SecurityView, error) {
	return read(f, func(r Reads) SecurityView { return r.Security })
}

func (f *Fake) Advisory(context.Context, hostile.Of[PullText]) (Advisory, error) {
	return read(f, func(r Reads) Advisory { return r.Advisory })
}

func (f *Fake) PackageDiff(context.Context, hostile.Of[PullText]) (PackageDiff, error) {
	return read(f, func(r Reads) PackageDiff { return r.Diff })
}

func (f *Fake) NewestRelease(context.Context, hostile.Of[PullText]) (cooled.Published, error) {
	return read(f, func(r Reads) cooled.Published { return r.Published })
}
