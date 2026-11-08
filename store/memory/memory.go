// Package memory is a store.Store held in memory, for tests and one-shot
// commands. It forgets everything when dropped.
package memory

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/store"
	"github.com/ivy/almanac/workflow"
)

// slot is a workflow's key: at most one run is active, and one event waits,
// per slot.
type slot struct{ workflow, key string }

type waiting struct {
	seq   int
	lane  int
	event store.Event
}

type recording struct {
	run, name  string
	occurrence int
}

// overall is the tripped scope for the budget across all workflows.
const overall = ""

// Store is one almanac's state, in memory. Its zero value is not ready: use New.
type Store struct {
	mu sync.Mutex

	seen     map[[2]string]bool
	pending  map[slot]*waiting
	running  map[slot]string
	started  []start
	tripped  map[string]time.Time
	runs     map[string]store.Run
	subjects map[workflow.Subject][]string
	log      map[recording]json.RawMessage
	outbox   []store.Entry
	ledger   map[slot][]store.Trace
	nextSeq  int
	nextRun  int
}

type start struct {
	workflow string
	at       time.Time
}

var _ store.Store = (*Store)(nil)

// New is an empty store.
func New() *Store {
	return &Store{
		seen:     map[[2]string]bool{},
		pending:  map[slot]*waiting{},
		running:  map[slot]string{},
		tripped:  map[string]time.Time{},
		runs:     map[string]store.Run{},
		subjects: map[workflow.Subject][]string{},
		log:      map[recording]json.RawMessage{},
		ledger:   map[slot][]store.Trace{},
	}
}

// Admit implements store.Store.
func (s *Store) Admit(_ context.Context, source, deliveryID string, admission store.Admission) (store.Ingest, error) {
	var event store.Event
	if admission.Queue != nil {
		event = *admission.Queue
		payload, err := store.Storable(event.Payload)
		if err != nil {
			return "", diag.Store(err)
		}
		event.Payload = payload
		event.Subjects = slices.Clone(event.Subjects)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delivery := [2]string{source, deliveryID}
	if s.seen[delivery] {
		return store.Duplicate, nil
	}
	s.seen[delivery] = true
	if admission.Queue == nil {
		return admission.Drop, nil
	}
	at := slot{event.Workflow, event.Key}
	_, running := s.running[at]
	ingest := store.Queued
	if w, ok := s.pending[at]; ok {
		w.lane = min(w.lane, event.Origin.Lane())
		event.Depth = max(event.Depth, w.event.Depth)
		w.event = event
		ingest = store.Coalesced
	} else {
		s.enqueue(at, event)
	}
	if running {
		return store.MarkedDirty, nil
	}
	return ingest, nil
}

func (s *Store) enqueue(at slot, event store.Event) {
	s.nextSeq++
	s.pending[at] = &waiting{seq: s.nextSeq, lane: event.Origin.Lane(), event: event}
}

// Claim implements store.Store.
func (s *Store) Claim(_ context.Context, request store.ClaimRequest) (store.Claim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	since := request.Now.Add(-request.Window)
	if limit, capped := request.Overall.Value(); capped && s.startedSince("", since) >= limit {
		held := 0
		for _, b := range request.Budgets {
			held += s.claimable(b.Workflow)
		}
		if held > 0 && s.report(overall, request.Now, since) {
			return store.Claim{Tripped: &store.Tripped{Held: held}}, nil
		}
		return store.Claim{}, nil
	}
	open := map[string]bool{}
	for _, b := range request.Budgets {
		if s.startedSince(b.Workflow, since) < b.Limit {
			open[b.Workflow] = true
			continue
		}
		if held := s.claimable(b.Workflow); held > 0 && s.report(b.Workflow, request.Now, since) {
			return store.Claim{Tripped: &store.Tripped{Workflow: b.Workflow, Held: held}}, nil
		}
	}
	var next *slot
	var first *waiting
	for at, w := range s.pending {
		if !open[at.workflow] || !s.free(at) {
			continue
		}
		if first == nil || w.lane < first.lane || (w.lane == first.lane && w.seq < first.seq) {
			at, w := at, w
			next, first = &at, w
		}
	}
	if next == nil {
		return store.Claim{}, nil
	}
	delete(s.pending, *next)
	s.nextRun++
	run := store.Run{
		ID:       fmt.Sprintf("run-%d", s.nextRun),
		Workflow: next.workflow,
		Key:      next.key,
		Payload:  first.event.Payload,
		Origin:   first.event.Origin,
		Depth:    first.event.Depth,
	}
	s.started = append(s.started, start{workflow: next.workflow, at: request.Now})
	s.running[*next] = run.ID
	s.runs[run.ID] = run
	for _, subject := range first.event.Subjects {
		s.link(subject, run.ID)
	}
	return store.Claim{Run: &run}, nil
}

// free is whether a slot can start a run: none is running, and its last run's
// effects have all been delivered, because a run must see what they changed.
func (s *Store) free(at slot) bool {
	if _, running := s.running[at]; running {
		return false
	}
	for _, e := range s.outbox {
		if e.Status != store.Pending || e.RunID == "" {
			continue
		}
		if run := s.runs[e.RunID]; run.Workflow == at.workflow && run.Key == at.key {
			return false
		}
	}
	return true
}

// claimable is how many of a workflow's keys could start a run now.
func (s *Store) claimable(workflow string) int {
	n := 0
	for at := range s.pending {
		if at.workflow == workflow && s.free(at) {
			n++
		}
	}
	return n
}

// startedSince is how many runs of a workflow, or of every workflow when it is
// empty, started after since.
func (s *Store) startedSince(workflow string, since time.Time) int {
	n := 0
	for _, st := range s.started {
		if (workflow == "" || st.workflow == workflow) && st.at.After(since) {
			n++
		}
	}
	return n
}

// report records a trip, unless one was already reported in this window.
func (s *Store) report(scope string, now, since time.Time) bool {
	if at, ok := s.tripped[scope]; ok && at.After(since) {
		return false
	}
	s.tripped[scope] = now
	return true
}

func (s *Store) link(subject workflow.Subject, runID string) {
	if !slices.Contains(s.subjects[subject], runID) {
		s.subjects[subject] = append(s.subjects[subject], runID)
	}
}

// Remember implements store.Store.
func (s *Store) Remember(_ context.Context, workflow, key string, trace store.Trace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := slot{workflow, key}
	s.ledger[at] = append(s.ledger[at], trace)
	return nil
}

// Traces implements store.Store.
func (s *Store) Traces(_ context.Context, workflow, key string, since time.Time) ([]store.Trace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []store.Trace
	for _, t := range s.ledger[slot{workflow, key}] {
		if t.At.After(since) {
			found = append(found, t)
		}
	}
	slices.SortStableFunc(found, func(a, b store.Trace) int { return a.At.Compare(b.At) })
	return found, nil
}

// Note implements store.Store.
func (s *Store) Note(_ context.Context, key string, value json.RawMessage, mode effect.Mode) error {
	value, err := store.Storable(value)
	if err != nil {
		return diag.Store(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entry(key) == nil {
		s.outbox = append(s.outbox, store.Entry{Key: key, Effect: value, Status: store.StatusIn(mode)})
	}
	return nil
}

func (s *Store) entry(key string) *store.Entry {
	for i := range s.outbox {
		if s.outbox[i].Key == key {
			return &s.outbox[i]
		}
	}
	return nil
}

// Release implements store.Store.
func (s *Store) Release(_ context.Context, runID string, links []workflow.Subject, effects []json.RawMessage, mode effect.Mode) error {
	stored := make([]json.RawMessage, len(effects))
	for i, e := range effects {
		var err error
		if stored[i], err = store.Storable(e); err != nil {
			return diag.Store(err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[runID]; !ok {
		return diag.Store(fmt.Errorf("no run %q to release", runID))
	}
	for at, id := range s.running {
		if id == runID {
			delete(s.running, at)
		}
	}
	for _, subject := range links {
		s.link(subject, runID)
	}
	for i, e := range stored {
		s.outbox = append(s.outbox, store.Entry{
			Key:    fmt.Sprintf("%s:%d", runID, i),
			RunID:  runID,
			Effect: e,
			Status: store.StatusIn(mode),
		})
	}
	return nil
}

// Undelivered implements store.Store.
func (s *Store) Undelivered(context.Context) ([]store.Entry, error) {
	return s.entries(func(e store.Entry) bool { return e.Status == store.Pending }), nil
}

// Outbox implements store.Store.
func (s *Store) Outbox(_ context.Context, runID string) ([]store.Entry, error) {
	return s.entries(func(e store.Entry) bool { return e.RunID == runID }), nil
}

func (s *Store) entries(keep func(store.Entry) bool) []store.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []store.Entry
	for _, e := range s.outbox {
		if keep(e) {
			found = append(found, e)
		}
	}
	return found
}

// Delivered implements store.Store.
func (s *Store) Delivered(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.entry(key); e != nil {
		e.Status = store.Applied
	}
	return nil
}

// DeliveryFailed implements store.Store.
func (s *Store) DeliveryFailed(_ context.Context, key, _ string, giveUp int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.entry(key); e != nil {
		e.Attempts++
		if e.Attempts >= giveUp {
			e.Status = store.Failed
		}
	}
	return nil
}

// Recover implements store.Store.
func (s *Store) Recover(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	interrupted := 0
	// Requeue in the order the runs started, so recovery keeps oldest first.
	ids := make([]string, 0, len(s.running))
	for _, id := range s.running {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int { return cmp.Compare(runNumber(a), runNumber(b)) })
	for _, id := range ids {
		run := s.runs[id]
		at := slot{run.Workflow, run.Key}
		delete(s.running, at)
		interrupted++
		if _, newer := s.pending[at]; newer {
			continue
		}
		var subjects []workflow.Subject
		for subject, runs := range s.subjects {
			if slices.Contains(runs, id) {
				subjects = append(subjects, subject)
			}
		}
		s.enqueue(at, store.Event{
			Workflow: run.Workflow,
			Key:      run.Key,
			Payload:  run.Payload,
			Subjects: subjects,
			Origin:   run.Origin,
			Depth:    run.Depth,
		})
	}
	return interrupted, nil
}

func runNumber(id string) int {
	var n int
	_, _ = fmt.Sscanf(id, "run-%d", &n)
	return n
}

// Run implements store.Store.
func (s *Store) Run(_ context.Context, runID string) (store.Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	return run, ok, nil
}

// Recorded implements store.Store.
func (s *Store) Recorded(_ context.Context, runID, name string, occurrence int) (json.RawMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.log[recording{runID, name, occurrence}]
	return value, ok, nil
}

// Record implements store.Store.
func (s *Store) Record(_ context.Context, runID, name string, occurrence int, value json.RawMessage) error {
	value, err := store.Storable(value)
	if err != nil {
		return diag.Store(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[runID]; !ok {
		return diag.Store(fmt.Errorf("no run %q to record for", runID))
	}
	s.log[recording{runID, name, occurrence}] = value
	return nil
}

// RunsFor implements store.Store.
func (s *Store) RunsFor(_ context.Context, subject workflow.Subject) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.subjects[subject]), nil
}
