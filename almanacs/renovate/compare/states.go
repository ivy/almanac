// Package compare sets the Go almanac-renovate's decisions beside the Rust one's for the same
// pull requests, as evidence for a cutover. It compares two things: the state each version's
// report gives every open Renovate pull request, and the effects the Go version's dry run
// would apply beside what the Rust version did, as GitHub shows it.
//
// docs/comparing.md says how to produce both, run them back to back, and read them.
package compare

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/ivy/almanac/almanacs/renovate/report"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/hostile"
)

// Standing is where one report says a pull request stands.
type Standing struct {
	Branch string `json:"branch"`
	// State is one of the report's states, or "unreadable" for a pull request the report lists
	// as one it could not read.
	State  string `json:"state"`
	Reason string `json:"reason"`
	Note   string `json:"note,omitempty"`
}

// unreadable is the state of a pull request a report lists as unreadable.
const unreadable = "unreadable"

// Agreement is what comparing two standings found.
type Agreement string

const (
	// Agree is the same branch, state, reason and note in both reports.
	Agree Agreement = "agree"
	// Differ is a pull request in both reports that they place differently.
	Differ Agreement = "differ"
	// RustInProgress is a pull request the Rust report has in progress and the Go one does
	// not. The Rust version is working on it now, so GitHub does not yet show where its run
	// will leave it, and the Go version, with no run of its own, reports what GitHub shows.
	// It is not counted as a difference.
	RustInProgress Agreement = "rust-in-progress"
	// OnlyRust is a pull request only the Rust report lists.
	OnlyRust Agreement = "only-rust"
	// OnlyGo is a pull request only the Go report lists.
	OnlyGo Agreement = "only-go"
)

// agreements are in the order a comparison totals them.
var agreements = []Agreement{Agree, Differ, RustInProgress, OnlyRust, OnlyGo}

// PullStates is one pull request's standing in each report.
type PullStates struct {
	// Pull is the pull request, as owner/repo#number.
	Pull      string    `json:"pull"`
	Agreement Agreement `json:"agreement"`
	// Differences are the fields the reports disagree on: branch, state, reason and note.
	Differences []string  `json:"differences"`
	Rust        *Standing `json:"rust"`
	Go          *Standing `json:"go"`

	repo   string
	number uint64
}

// Scanned is how many repositories each report scanned.
type Scanned struct {
	Rust int `json:"rust"`
	Go   int `json:"go"`
}

// States is two reports compared, pull request by pull request.
type States struct {
	Totals              map[Agreement]int `json:"totals"`
	RepositoriesScanned Scanned           `json:"repositories_scanned"`
	Pulls               []PullStates      `json:"pulls"`
}

// Differs reports whether anything in the comparison is a difference: a pull request the
// reports place differently or that only one lists, or a different count of repositories.
func (s States) Differs() bool {
	return s.Totals[Differ]+s.Totals[OnlyRust]+s.Totals[OnlyGo] > 0 || s.RepositoriesScanned.Rust != s.RepositoriesScanned.Go
}

// reportFile is a report as `report --format json` prints it.
type reportFile struct {
	RepositoriesScanned *int     `json:"repositories_scanned"`
	Unreadable          []string `json:"unreadable"`
	Repositories        []struct {
		Repo  string `json:"repo"`
		Pulls []struct {
			Number uint64 `json:"number"`
			Branch string `json:"branch"`
			State  string `json:"state"`
			Reason string `json:"reason"`
			Note   string `json:"note"`
		} `json:"pulls"`
	} `json:"repositories"`
}

// Report is a report read: how many repositories it scanned, and each pull request's standing.
type Report struct {
	scanned int
	pulls   map[pullID]Standing
}

type pullID struct {
	repo   string
	number uint64
}

func (p pullID) String() string { return p.repo + "#" + strconv.FormatUint(p.number, 10) }

// states are the report's states, and unreadable.
var states = func() []string {
	var names []string
	for s := report.Mergeable; s <= report.BlockedByConfig; s++ {
		names = append(names, s.String())
	}
	return append(names, unreadable)
}()

// ReadReport reads what `report --format json` printed, named path for errors. Every
// repository and branch must be a valid slug and every state one a report gives, so nothing
// else reaches a comparison or a terminal.
func ReadReport(path string, text []byte) (Report, error) {
	var file reportFile
	decoder := json.NewDecoder(bytes.NewReader(text))
	if err := decoder.Decode(&file); err != nil {
		return Report{}, diag.NotAReport(path, "it is not the JSON a report prints: "+err.Error())
	}
	if file.RepositoriesScanned == nil {
		return Report{}, diag.NotAReport(path, "it has no repositories_scanned")
	}
	out := Report{scanned: *file.RepositoriesScanned, pulls: map[pullID]Standing{}}
	add := func(id pullID, s Standing) error {
		if _, ok := out.pulls[id]; ok {
			return diag.NotAReport(path, "it lists "+id.String()+" twice")
		}
		out.pulls[id] = s
		return nil
	}
	for _, repo := range file.Repositories {
		if _, err := hostile.ParseSlug(repo.Repo); err != nil {
			return Report{}, diag.NotAReport(path, fmt.Sprintf("it names a repository %q that is not a slug", repo.Repo))
		}
		for _, pull := range repo.Pulls {
			id := pullID{repo.Repo, pull.Number}
			if _, err := hostile.ParseSlug(pull.Branch); err != nil {
				return Report{}, diag.NotAReport(path, fmt.Sprintf("%s has a branch %q that is not a slug", id, pull.Branch))
			}
			if !slices.Contains(states[:len(states)-1], pull.State) {
				return Report{}, diag.NotAReport(path, fmt.Sprintf("%s has a state %q no report gives", id, pull.State))
			}
			if err := add(id, Standing{Branch: pull.Branch, State: pull.State, Reason: printable(pull.Reason), Note: printable(pull.Note)}); err != nil {
				return Report{}, err
			}
		}
	}
	for _, gap := range file.Unreadable {
		id, ok := parsePull(gap)
		if !ok {
			return Report{}, diag.NotAReport(path, fmt.Sprintf("it lists %q as unreadable, which is not owner/repo#number", gap))
		}
		if err := add(id, Standing{State: unreadable, Reason: "its branch or head is outside what Almanac accepts"}); err != nil {
			return Report{}, err
		}
	}
	return out, nil
}

// parsePull reads owner/repo#number.
func parsePull(text string) (pullID, bool) {
	repo, number, ok := strings.Cut(text, "#")
	if !ok {
		return pullID{}, false
	}
	if _, err := hostile.ParseSlug(repo); err != nil {
		return pullID{}, false
	}
	n, err := strconv.ParseUint(number, 10, 64)
	return pullID{repo, n}, err == nil
}

// printable is text with every control character shown as a question mark, so a report file
// cannot move a terminal's cursor or change its colours.
func printable(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, text)
}

// fromFleet is a report the Go version made just now.
func fromFleet(f report.Fleet) (Report, error) {
	text, err := json.Marshal(f)
	if err != nil {
		return Report{}, err
	}
	return ReadReport("the Go report", text)
}

// CompareStates sets two reports side by side, pull request by pull request.
func CompareStates(rust, golang Report) States {
	out := States{
		Totals:              map[Agreement]int{Agree: 0, Differ: 0, RustInProgress: 0, OnlyRust: 0, OnlyGo: 0},
		RepositoriesScanned: Scanned{Rust: rust.scanned, Go: golang.scanned},
		Pulls:               []PullStates{},
	}
	ids := make([]pullID, 0, len(rust.pulls)+len(golang.pulls))
	for id := range rust.pulls {
		ids = append(ids, id)
	}
	for id := range golang.pulls {
		if _, ok := rust.pulls[id]; !ok {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, func(a, b pullID) int {
		if c := strings.Compare(a.repo, b.repo); c != 0 {
			return c
		}
		return cmp.Compare(a.number, b.number)
	})
	for _, id := range ids {
		pull := PullStates{Pull: id.String(), Differences: []string{}, repo: id.repo, number: id.number}
		r, inRust := rust.pulls[id]
		g, inGo := golang.pulls[id]
		if inRust {
			pull.Rust = &r
		}
		if inGo {
			pull.Go = &g
		}
		switch {
		case !inGo:
			pull.Agreement = OnlyRust
		case !inRust:
			pull.Agreement = OnlyGo
		default:
			pull.Differences = differences(r, g)
			switch {
			case len(pull.Differences) == 0:
				pull.Agreement = Agree
			case r.State == report.InProgress.String() && g.State != r.State:
				pull.Agreement = RustInProgress
			default:
				pull.Agreement = Differ
			}
		}
		out.Totals[pull.Agreement]++
		out.Pulls = append(out.Pulls, pull)
	}
	return out
}

// differences are the fields two standings disagree on, in the order a report prints them.
func differences(rust, golang Standing) []string {
	var out []string
	if rust.Branch != golang.Branch {
		out = append(out, "branch")
	}
	if rust.State != golang.State {
		out = append(out, "state")
	}
	if rust.Reason != golang.Reason {
		out = append(out, "reason")
	}
	if rust.Note != golang.Note {
		out = append(out, "note")
	}
	if out == nil {
		return []string{}
	}
	return out
}

// RenderStates is a comparison as people read it: a line per pull request that agrees, and
// both standings, marked where they differ, for one that does not.
func RenderStates(s States) string {
	var b strings.Builder
	for _, pull := range s.Pulls {
		switch pull.Agreement {
		case Agree:
			fmt.Fprintf(&b, "%-17s %s  %s\n", pull.Agreement, pull.Pull, standing(*pull.Rust))
			continue
		case Differ:
			fmt.Fprintf(&b, "%-17s %s  differs in %s\n", pull.Agreement, pull.Pull, strings.Join(pull.Differences, ", "))
		case RustInProgress:
			fmt.Fprintf(&b, "%-17s %s  the Rust version is working on it now; not counted\n", pull.Agreement, pull.Pull)
		default:
			fmt.Fprintf(&b, "%-17s %s\n", pull.Agreement, pull.Pull)
		}
		if pull.Rust != nil {
			fmt.Fprintf(&b, "    rust: %s\n", standing(*pull.Rust))
		}
		if pull.Go != nil {
			fmt.Fprintf(&b, "    go:   %s\n", standing(*pull.Go))
		}
	}
	scanned := fmt.Sprintf("%d repositories scanned by both", s.RepositoriesScanned.Rust)
	if s.RepositoriesScanned.Rust != s.RepositoriesScanned.Go {
		scanned = fmt.Sprintf("repositories scanned differ: %d by Rust, %d by Go", s.RepositoriesScanned.Rust, s.RepositoriesScanned.Go)
	}
	fmt.Fprintf(&b, "%s; %d pull requests: %s\n", scanned, len(s.Pulls), totals(s.Totals))
	return b.String()
}

func standing(s Standing) string {
	text := s.State + "  " + s.Branch + "  " + s.Reason
	if s.Branch == "" {
		text = s.State + "  " + s.Reason
	}
	if s.Note != "" {
		text += " (note: " + s.Note + ")"
	}
	return text
}

func totals(counts map[Agreement]int) string {
	parts := make([]string, 0, len(agreements))
	for _, a := range agreements {
		parts = append(parts, fmt.Sprintf("%d %s", counts[a], a))
	}
	return strings.Join(parts, ", ")
}
