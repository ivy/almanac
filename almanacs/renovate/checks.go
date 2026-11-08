package renovate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/github"
	"github.com/ivy/almanac/github/events"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/registry"
	"github.com/ivy/almanac/workflow"
)

// Checks is where the checks on a commit stand, Almanac's own left out.
type Checks struct {
	hostile.Closed
	name string
}

// The ways checks can stand.
var (
	// ChecksNone is nothing having run, so nothing can fail.
	ChecksNone    = Checks{name: "none"}
	ChecksPending = Checks{name: "pending"}
	ChecksGreen   = Checks{name: "green"}
	ChecksRed     = Checks{name: "red"}
)

func (c Checks) String() string { return c.name }

// Trust is what Almanac last said about a commit, in its own status.
type Trust struct {
	hostile.Closed
	name string
}

// The verdicts Almanac's status can hold.
var (
	// TrustAbsent is a commit Almanac has said nothing about.
	TrustAbsent  = Trust{name: "absent"}
	TrustPending = Trust{name: "pending"}
	TrustSuccess = Trust{name: "success"}
	TrustFailure = Trust{name: "failure"}
)

func (t Trust) String() string { return t.name }

// IsFinal reports whether Almanac reached a verdict.
func (t Trust) IsFinal() bool { return t == TrustSuccess || t == TrustFailure }

// suites is a commit's check suites as GitHub lists them.
type suites struct {
	CheckSuites []struct {
		Status     suiteStatus                          `json:"status"`
		Conclusion events.CheckSuiteCompletedConclusion `json:"conclusion"`
		// Runs is how many check runs the suite has, or nil when GitHub does not say.
		Runs *uint64 `json:"latest_check_runs_count"`
	} `json:"check_suites"`
}

// statuses is a commit's combined status.
type statuses struct {
	Statuses []struct {
		Context statusContext      `json:"context"`
		State   events.StatusState `json:"state"`
	} `json:"statuses"`
}

// suiteStatus is whether a check suite has finished.
type suiteStatus struct {
	hostile.Closed
	name string
}

var (
	suiteCompleted = suiteStatus{name: "completed"}
	suiteOpen      = suiteStatus{name: "open"}
)

// UnmarshalJSON reads "completed" as finished, and anything else as still open.
func (s *suiteStatus) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return err
	}
	*s = suiteOpen
	if name == suiteCompleted.name {
		*s = suiteCompleted
	}
	return nil
}

// statusContext is who a commit status is from, as far as the checks are concerned.
type statusContext struct {
	hostile.Closed
	name string
}

var (
	// fromAlmanac is Almanac's own status.
	fromAlmanac = statusContext{name: "almanac"}
	// fromRenovate is one of Renovate's, such as its cooldown.
	fromRenovate = statusContext{name: "renovate"}
	// fromTests is anything else, which tests something.
	fromTests = statusContext{name: "tests"}
)

// UnmarshalJSON sorts a status context into who it is from.
func (c *statusContext) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return err
	}
	switch {
	case name == TrustContext:
		*c = fromAlmanac
	case strings.HasPrefix(name, renovateBranch):
		*c = fromRenovate
	default:
		*c = fromTests
	}
	return nil
}

func isFailedSuite(c events.CheckSuiteCompletedConclusion) bool {
	switch c {
	case events.CheckSuiteCompletedConclusionFailure, events.CheckSuiteCompletedConclusionTimedOut,
		events.CheckSuiteCompletedConclusionActionRequired, events.CheckSuiteCompletedConclusionStartupFailure,
		events.CheckSuiteCompletedConclusionCancelled:
		return true
	}
	return false
}

// ChecksOf combines a commit's check suites with its statuses, leaving out Almanac's own and
// Renovate's, because neither tests anything. Installed Apps leave a suite queued forever when
// they have nothing to report, so a suite with no check runs is left out too.
func ChecksOf(rawSuites, rawStatuses hostile.Of[json.RawMessage]) (Checks, error) {
	var s suites
	if err := hostile.DecodeClean(rawSuites, &s); err != nil {
		return Checks{}, fmt.Errorf("GitHub's check suites do not read as a list of check suites: %w", err)
	}
	var st statuses
	if err := hostile.DecodeClean(rawStatuses, &st); err != nil {
		return Checks{}, fmt.Errorf("GitHub's combined status does not read as a list of statuses: %w", err)
	}
	failed, open, seen := false, false, false
	for _, suite := range s.CheckSuites {
		if suite.Runs != nil && *suite.Runs == 0 {
			continue
		}
		seen = true
		failed = failed || isFailedSuite(suite.Conclusion)
		open = open || suite.Status != suiteCompleted
	}
	for _, status := range st.Statuses {
		if status.Context == fromAlmanac || status.Context == fromRenovate {
			continue
		}
		seen = true
		failed = failed || status.State == events.StatusStateFailure || status.State == events.StatusStateError
		open = open || status.State == events.StatusStatePending
	}
	switch {
	case failed:
		return ChecksRed, nil
	case open:
		return ChecksPending, nil
	case !seen:
		return ChecksNone, nil
	}
	return ChecksGreen, nil
}

// TrustOf is what Almanac last said about a commit, read from its combined status.
func TrustOf(rawStatuses hostile.Of[json.RawMessage]) (Trust, error) {
	var st statuses
	if err := hostile.DecodeClean(rawStatuses, &st); err != nil {
		return Trust{}, fmt.Errorf("GitHub's combined status does not read as a list of statuses: %w", err)
	}
	for _, status := range st.Statuses {
		if status.Context != fromAlmanac {
			continue
		}
		switch status.State {
		case events.StatusStateSuccess:
			return TrustSuccess, nil
		case events.StatusStatePending:
			return TrustPending, nil
		}
		return TrustFailure, nil
	}
	return TrustAbsent, nil
}

// CooldownOf is how old the update a pull request makes is, at now: the same test a workflow
// applies, so that a scan or a report and a run cannot disagree.
func CooldownOf(ctx context.Context, reg registry.Registry, pull hostile.Of[json.RawMessage], now time.Time) cooled.Age {
	text, err := hostile.Decode[PullText](pull)
	if err != nil {
		return cooled.Unknown
	}
	published, err := Newest(ctx, reg, hostile.Map(text, UpdateOf))
	if err != nil {
		return cooled.Unknown
	}
	age, _ := cooled.Measure(published, MinReleaseAge, now)
	return age
}

// ScannedRepositories are the repositories a scan looks at: every one client can see that
// owner owns, unless a backfill named one.
func ScannedRepositories(ctx context.Context, scanner *workflow.Scanner, client *github.Client, owner string) ([]hostile.Slug, error) {
	listed, err := client.Repositories(ctx, owner)
	if err != nil {
		return nil, err
	}
	visible := make([]hostile.Slug, 0, len(listed))
	for _, repository := range listed {
		var named struct {
			FullName hostile.Slug `json:"full_name"`
		}
		if err := hostile.DecodeClean(repository, &named); err != nil {
			return nil, fmt.Errorf("a repository GitHub lists for %s has a name Almanac cannot read, so a scan would miss it: %w", owner, err)
		}
		visible = append(visible, named.FullName)
	}
	return scanner.Narrow(visible)
}

// gaveUpOnCI reports whether CI has had long enough to report on a pull request, at now.
// A workflow can exist and still never run on one, through a path filter or a push-only
// trigger, and a security fix should not wait for that in silence.
func gaveUpOnCI(pull hostile.Of[json.RawMessage], now time.Time) bool {
	var opened struct {
		CreatedAt time.Time `json:"created_at"`
	}
	if hostile.DecodeClean(pull, &opened) != nil || opened.CreatedAt.IsZero() {
		return false
	}
	return now.Sub(opened.CreatedAt) >= CIPatience
}
