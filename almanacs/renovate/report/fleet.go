package report

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/github"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/registry"
)

const (
	// mergeableReads and mergeablePause are how long a report waits for GitHub to work out
	// whether a pull request can merge.
	mergeableReads = 4
	mergeablePause = 2 * time.Second

	securityLabel       = "security"
	vulnerabilitySuffix = "-vulnerability"
	unlabeled           = "set vulnerabilityAlerts.labels, so security updates are labelled"
)

// defaultBranch is the branch a repository that names none is taken to have.
var defaultBranch = hostile.MustParseSlug("main")

// Pull is one pull request in a report.
type Pull struct {
	Number uint64       `json:"number"`
	Branch hostile.Slug `json:"branch"`
	Verdict
}

// Repository is one repository's open Renovate pull requests.
type Repository struct {
	Repo  hostile.Slug `json:"repo"`
	Pulls []Pull       `json:"pulls"`
}

// Fleet is where every open Renovate pull request stands, by repository.
type Fleet struct {
	RepositoriesScanned int           `json:"repositories_scanned"`
	Totals              map[State]int `json:"totals"`
	// Unreadable are open Renovate pull requests, as owner/repo#number, whose branch or head is
	// outside what Almanac accepts, so no workflow would see them. Each is a gap to look at.
	Unreadable   []string     `json:"unreadable"`
	Repositories []Repository `json:"repositories"`
}

// MarshalJSON writes the report with every object's keys in order, as the Rust report prints
// them, so the two can be compared byte for byte.
func (f Fleet) MarshalJSON() ([]byte, error) {
	type fields Fleet
	plain, err := json.Marshal(fields(f))
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.UseNumber()
	var sorted any
	if err := decoder.Decode(&sorted); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(sorted); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// Sources are what a report reads: GitHub, and the registries that date an update's releases.
type Sources struct {
	GitHub   *github.Client
	Registry registry.Registry
	// Now is the time cooldowns are measured at.
	Now time.Time
}

// Read reports every open pull request author has opened across owner's repositories. It only
// ever reads.
func Read(ctx context.Context, from Sources, owner, author string) (Fleet, error) {
	listed, err := from.GitHub.Repositories(ctx, owner)
	if err != nil {
		return Fleet{}, err
	}
	out := Fleet{
		RepositoriesScanned: len(listed),
		Totals:              map[State]int{},
		Unreadable:          []string{},
		Repositories:        []Repository{},
	}
	for _, listing := range listed {
		repo, branch, err := repository(listing, owner)
		if err != nil {
			return Fleet{}, err
		}
		pulls, err := from.GitHub.PullsBy(ctx, repo, author)
		if err != nil {
			return Fleet{}, err
		}
		if len(pulls) == 0 {
			continue
		}
		config, err := readConfig(ctx, from.GitHub, repo, branch)
		if err != nil {
			return Fleet{}, err
		}
		reported := Repository{Repo: repo, Pulls: []Pull{}}
		for _, item := range pulls {
			var numbered struct {
				Number uint64 `json:"number"`
			}
			if err := hostile.DecodeClean(item, &numbered); err != nil {
				return Fleet{}, diag.Unreachable("listing the pull requests in "+repo.String(), "GitHub gave a pull request without a number")
			}
			pull, ok, err := observe(ctx, from, repo, numbered.Number, config)
			if err != nil {
				return Fleet{}, err
			}
			if !ok {
				out.Unreadable = append(out.Unreadable, repo.String()+"#"+strconv.FormatUint(numbered.Number, 10))
				continue
			}
			out.Totals[pull.State]++
			reported.Pulls = append(reported.Pulls, pull)
		}
		out.Repositories = append(out.Repositories, reported)
	}
	return out, nil
}

// repository is a listed repository's name, and its default branch, or main where it names
// none Almanac can read.
func repository(listing hostile.Of[json.RawMessage], owner string) (repo, branch hostile.Slug, err error) {
	var named struct {
		FullName hostile.Slug `json:"full_name"`
	}
	if err := hostile.DecodeClean(listing, &named); err != nil {
		return hostile.Slug{}, hostile.Slug{}, diag.Unreachable("listing the repositories "+owner+" owns",
			"GitHub lists one whose name is not a slug, so its pull requests cannot be reported")
	}
	var branched struct {
		DefaultBranch hostile.Slug `json:"default_branch"`
	}
	if hostile.DecodeClean(listing, &branched) != nil || branched.DefaultBranch == (hostile.Slug{}) {
		return named.FullName, defaultBranch, nil
	}
	return named.FullName, branched.DefaultBranch, nil
}

// head is the part of a pull request that names it in a report, and what it can merge.
type head struct {
	Mergeable *bool `json:"mergeable"`
	Head      struct {
		Ref hostile.Slug `json:"ref"`
		Sha hostile.Sha  `json:"sha"`
	} `json:"head"`
}

// observe is where one pull request stands, or false if its branch or head is not one Almanac
// can read.
func observe(ctx context.Context, from Sources, repo hostile.Slug, number uint64, config Config) (Pull, bool, error) {
	client := from.GitHub
	raw, err := client.PullSettled(ctx, repo, number, mergeableReads, mergeablePause)
	if err != nil {
		return Pull{}, false, err
	}
	var pull head
	if hostile.DecodeClean(raw, &pull) != nil {
		return Pull{}, false, nil
	}
	sha := pull.Head.Sha
	suites, err := client.CheckSuites(ctx, repo, sha)
	if err != nil {
		return Pull{}, false, err
	}
	statuses, err := client.CombinedStatus(ctx, repo, sha)
	if err != nil {
		return Pull{}, false, err
	}
	messages, err := client.PullCommitMessages(ctx, repo, number)
	if err != nil {
		return Pull{}, false, err
	}
	checks, err := renovate.ChecksOf(suites, statuses)
	if err != nil {
		return Pull{}, false, diag.Unreachable(fmt.Sprintf("reading the checks on %s#%d", repo, number), err.Error())
	}
	trust, err := renovate.TrustOf(statuses)
	if err != nil {
		return Pull{}, false, diag.Unreachable(fmt.Sprintf("reading Almanac's status on %s#%d", repo, number), err.Error())
	}
	var attempts int
	if err := hostile.DecodeClean(hostile.Map(messages, patches), &attempts); err != nil {
		return Pull{}, false, err
	}
	labeled := hostile.Narrow(raw, labeledSecurity)
	facts := Facts{
		Conflict: conflictOf(pull.Mergeable),
		Checks:   checks,
		Trust:    trust,
		Security: labeled || strings.HasSuffix(pull.Head.Ref.String(), vulnerabilitySuffix),
		Attempts: attempts,
		Cooldown: renovate.CooldownOf(ctx, from.Registry, raw, from.Now),
		Config:   config,
	}
	v := Derive(facts)
	if facts.Security && !labeled && v.Note == "" {
		v.Note = unlabeled
	}
	return Pull{Number: number, Branch: pull.Head.Ref, Verdict: v}, true, nil
}

func conflictOf(mergeable *bool) Conflict {
	switch {
	case mergeable == nil:
		return ConflictUnknown
	case *mergeable:
		return NoConflict
	}
	return Conflicted
}

// patches counts the commit messages that carry Almanac's trailer, as JSON, so that the count
// can be decoded out of the hostile messages as a number.
func patches(messages []string) json.RawMessage {
	count := 0
	for _, message := range messages {
		if strings.Contains(message, renovate.PatchTrailer) {
			count++
		}
	}
	return json.RawMessage(strconv.Itoa(count))
}

// labeledSecurity reports whether a pull request, as the REST API gives it, is labelled as a
// security update.
func labeledSecurity(pull json.RawMessage) bool {
	var labelled struct {
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if json.Unmarshal(pull, &labelled) != nil {
		return false
	}
	for _, label := range labelled.Labels {
		if label.Name == securityLabel {
			return true
		}
	}
	return false
}

// Render is a report as people read it.
func Render(f Fleet) string {
	var b strings.Builder
	for _, repo := range f.Repositories {
		fmt.Fprintf(&b, "%s\n", repo.Repo)
		for _, pull := range repo.Pulls {
			fmt.Fprintf(&b, "  #%-5d %-18s %s  %s\n", pull.Number, pull.State, pull.Branch, pull.Reason)
			if pull.Note != "" {
				fmt.Fprintf(&b, "         note: %s\n", pull.Note)
			}
		}
	}
	for _, gap := range f.Unreadable {
		fmt.Fprintf(&b, "unreadable: %s\n", gap)
	}
	var totals []string
	for state := Mergeable; state <= BlockedByConfig; state++ {
		if count, ok := f.Totals[state]; ok {
			totals = append(totals, fmt.Sprintf("%d %s", count, state))
		}
	}
	fmt.Fprintf(&b, "%d repositories scanned: %s\n", f.RepositoriesScanned, strings.Join(totals, ", "))
	return b.String()
}
