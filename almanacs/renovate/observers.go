package renovate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ivy/almanac/advisory"
	"github.com/ivy/almanac/clock"
	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/github"
	"github.com/ivy/almanac/github/events"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/registry"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/workflow"
)

// Pull is what GitHub says now about the Renovate pull request on a build's branch. An event is
// only a hint that something changed (R-36).
type Pull struct {
	Found  bool         `json:"found"`
	Repo   hostile.Slug `json:"repo"`
	Number uint64       `json:"number"`
	Head   hostile.Sha  `json:"head"`
	// Conflicted is a pull request GitHub says cannot merge.
	Conflicted bool `json:"conflicted"`
	Failed     bool `json:"failed"`
	Running    bool `json:"running"`
	// RepairPassed is a repair of Almanac's that is held, and whose checks have passed.
	RepairPassed bool                 `json:"repair_passed"`
	Text         hostile.Of[PullText] `json:"text"`
}

// Line is a line an update added, and the file it is in.
type Line = github.AddedLine

// SecurityView is what GitHub says now about a security pull request.
type SecurityView struct {
	// Found is an open pull request by Renovate.
	Found bool        `json:"found"`
	Head  hostile.Sha `json:"head"`
	// Tested is something other than Almanac or a cooldown having reported on the head.
	Tested bool `json:"tested"`
	// Untestable is nothing going to report: the repository has no workflows, or they had
	// CIPatience and stayed quiet.
	Untestable bool                 `json:"untestable"`
	Text       hostile.Of[PullText] `json:"text"`
}

// Advisory is whether an advisory names the update as its fix.
type Advisory struct {
	NamesTheFix bool `json:"names_the_fix"`
}

// PackageDiff is what changed in the packages an update installs, between the release each
// replaces and the release it installs.
type PackageDiff struct {
	// Readable is every package's two releases having been found and diffed.
	Readable           bool `json:"readable"`
	InstallScriptAdded bool `json:"install_script_added"`
	// Truncated is part of the diff cut to fit, so nothing has read the whole of this update.
	Truncated bool `json:"truncated"`
	// Unshown names the files that changed and that nothing can read, one to a line.
	Unshown hostile.Of[string] `json:"unshown"`
	Diff    hostile.Of[string] `json:"diff"`
}

// The observers: what a workflow reads from GitHub, registries and advisory feeds.
var (
	PullForBranch = vocab.Observer(Vocabulary, "renovate.pull_for_branch",
		"Renovate's open pull request on the build's branch, as GitHub shows it now",
		readPull, needsGitHub, needsTarget)
	FailureLogs = vocab.Observer(Vocabulary, "renovate.failure_logs",
		"the end of the log of each job that failed on the pull request's head",
		readLogs, needsGitHub)
	UpdatedLines = vocab.Observer(Vocabulary, "renovate.updated_lines",
		"the lines the pull request adds, and the files they are in",
		readLines, needsGitHub)
	SecurityPR = vocab.Observer(Vocabulary, "renovate.security_pull_view",
		"the security pull request as GitHub shows it now",
		readSecurity, needsGitHub, needsTarget, needsClock)
	NamesTheFix = vocab.Observer(Vocabulary, "renovate.advisory",
		"whether an advisory names every package the update installs as the fix for the version it replaces",
		readAdvisory, needsAdvisories)
	ReleaseDiff = vocab.Observer(Vocabulary, "renovate.package_diff",
		"what changed in each updated package, between the release it replaces and the release it installs",
		readDiff, needsRegistry)
	NewestRelease = vocab.PublishTime(Vocabulary, "renovate.newest_release",
		"when the newest release the update installs was published",
		published, needsRegistry)
)

// readPull is the Renovate pull request on the event's branch. A branch name proves nothing:
// only Renovate's own pull requests are Almanac's to touch.
func readPull(ctx context.Context, from workflow.Lookup, e BuildEvent) (Pull, error) {
	client, err := workflow.Get[*github.Client](from)
	if err != nil {
		return Pull{}, err
	}
	target, err := workflow.Get[Target](from)
	if err != nil {
		return Pull{}, err
	}
	gone := Pull{Repo: e.Repo, Head: e.Sha, Text: hostile.New(PullText{})}
	listed, found, err := client.PullForBranch(ctx, e.Repo, e.Branch)
	if err != nil {
		return Pull{}, err
	}
	if !found {
		return gone, nil
	}
	number, ours := openedBy(listed, target.Author)
	if !ours {
		return gone, nil
	}
	raw, err := client.Pull(ctx, e.Repo, number)
	if err != nil {
		return Pull{}, err
	}
	var pull struct {
		Head struct {
			Sha hostile.Sha `json:"sha"`
		} `json:"head"`
		Mergeable *bool `json:"mergeable"`
	}
	if err := hostile.DecodeClean(raw, &pull); err != nil {
		return Pull{}, unreadablePull(e.Repo, number, err)
	}
	if pull.Head.Sha == (hostile.Sha{}) {
		return Pull{}, unreadablePull(e.Repo, number, errNoHead)
	}
	text, err := hostile.Decode[PullText](raw)
	if err != nil {
		return Pull{}, unreadablePull(e.Repo, number, err)
	}
	checks, trust, err := checksAt(ctx, client, e.Repo, pull.Head.Sha)
	if err != nil {
		return Pull{}, err
	}
	repaired := false
	if trust == TrustPending && checks == ChecksGreen {
		messages, err := client.PullCommitMessages(ctx, e.Repo, number)
		if err != nil {
			return Pull{}, err
		}
		repaired = hostile.Narrow(messages, anyCarriesPatch)
	}
	return Pull{
		Found:        true,
		Repo:         e.Repo,
		Number:       number,
		Head:         pull.Head.Sha,
		Conflicted:   pull.Mergeable != nil && !*pull.Mergeable,
		Failed:       checks == ChecksRed,
		Running:      checks == ChecksPending,
		RepairPassed: repaired,
		Text:         text,
	}, nil
}

// openedBy is the number of a pull request author opened, and false if someone else opened it.
func openedBy(pull hostile.Of[json.RawMessage], author string) (uint64, bool) {
	var listed struct {
		Number uint64 `json:"number"`
		User   struct {
			Login hostile.Slug `json:"login"`
		} `json:"user"`
	}
	if hostile.DecodeClean(pull, &listed) != nil {
		return 0, false
	}
	return listed.Number, listed.User.Login.String() == author
}

func anyCarriesPatch(messages []string) bool {
	for _, message := range messages {
		if strings.Contains(message, PatchTrailer) {
			return true
		}
	}
	return false
}

var errNoHead = errors.New("it names no head commit")

func unreadablePull(repo hostile.Slug, number uint64, err error) error {
	return fmt.Errorf("pull request #%d in %s does not read as a pull request, so nothing about it can be decided; GitHub's answer may have changed shape: %w", number, repo, err)
}

// checksAt is where the checks on sha stand, and what Almanac last said about it.
func checksAt(ctx context.Context, client *github.Client, repo hostile.Slug, sha hostile.Sha) (Checks, Trust, error) {
	suites, err := client.CheckSuites(ctx, repo, sha)
	if err != nil {
		return Checks{}, Trust{}, err
	}
	statuses, err := client.CombinedStatus(ctx, repo, sha)
	if err != nil {
		return Checks{}, Trust{}, err
	}
	checks, err := ChecksOf(suites, statuses)
	if err != nil {
		return Checks{}, Trust{}, err
	}
	trust, err := TrustOf(statuses)
	if err != nil {
		return Checks{}, Trust{}, err
	}
	return checks, trust, nil
}

// readLogs is the end of each failed job's log, where a build's error usually is.
func readLogs(ctx context.Context, from workflow.Lookup, p Pull) (hostile.Of[string], error) {
	client, err := workflow.Get[*github.Client](from)
	if err != nil {
		return hostile.Of[string]{}, err
	}
	raw, err := client.CheckRuns(ctx, p.Repo, p.Head)
	if err != nil {
		return hostile.Of[string]{}, err
	}
	// A check run concludes as a check suite does, so it reads as one.
	var runs struct {
		CheckRuns []struct {
			ID         *uint64                              `json:"id"`
			Conclusion events.CheckSuiteCompletedConclusion `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := hostile.DecodeClean(raw, &runs); err != nil {
		return hostile.Of[string]{}, fmt.Errorf("the check runs on %s in %s do not read as a list of check runs: %w", p.Head, p.Repo, err)
	}
	text := hostile.New("")
	for _, run := range runs.CheckRuns {
		if run.Conclusion != events.CheckSuiteCompletedConclusionFailure || run.ID == nil {
			continue
		}
		// A log that cannot be read leaves the fixer less to go on, not nothing.
		log, err := client.JobLogs(ctx, p.Repo, *run.ID)
		if err != nil {
			continue
		}
		text = hostile.Map(hostile.Zip(text, log), withTail)
	}
	return text, nil
}

// withTail adds the end of a log to the logs read so far, on a line of its own.
func withTail(logs hostile.Pair[string, string]) string {
	log := logs.Second
	start := max(len(log)-logLimit, 0)
	for start < len(log) && !utf8.RuneStart(log[start]) {
		start++
	}
	return logs.First + log[start:] + "\n"
}

// readLines is what the update itself added.
func readLines(ctx context.Context, from workflow.Lookup, p Pull) (hostile.Of[[]Line], error) {
	client, err := workflow.Get[*github.Client](from)
	if err != nil {
		return hostile.Of[[]Line]{}, err
	}
	return client.PullAddedLines(ctx, p.Repo, p.Number)
}

// readSecurity is the security pull request as GitHub shows it now.
func readSecurity(ctx context.Context, from workflow.Lookup, e SecurityEvent) (SecurityView, error) {
	client, err := workflow.Get[*github.Client](from)
	if err != nil {
		return SecurityView{}, err
	}
	target, err := workflow.Get[Target](from)
	if err != nil {
		return SecurityView{}, err
	}
	now, err := workflow.Get[clock.Clock](from)
	if err != nil {
		return SecurityView{}, err
	}
	pull, err := client.Pull(ctx, e.Repo, e.Number)
	if err != nil {
		return SecurityView{}, err
	}
	var about struct {
		State events.PullRequestOpenedState `json:"state"`
		User  struct {
			Login hostile.Slug `json:"login"`
		} `json:"user"`
	}
	found := hostile.DecodeClean(pull, &about) == nil &&
		about.State == events.PullRequestOpenedStateOpen && about.User.Login.String() == target.Author
	head := e.Sha
	var moved struct {
		Head struct {
			Sha hostile.Sha `json:"sha"`
		} `json:"head"`
	}
	if hostile.DecodeClean(pull, &moved) == nil && moved.Head.Sha != (hostile.Sha{}) {
		head = moved.Head.Sha
	}
	checks, _, err := checksAt(ctx, client, e.Repo, head)
	if err != nil {
		return SecurityView{}, err
	}
	workflows, err := client.Exists(ctx, e.Repo, workflowsDirectory)
	if err != nil {
		return SecurityView{}, err
	}
	text, err := hostile.Decode[PullText](pull)
	if err != nil {
		return SecurityView{}, unreadablePull(e.Repo, e.Number, err)
	}
	tested := checks != ChecksNone
	return SecurityView{
		Found:      found,
		Head:       head,
		Tested:     tested,
		Untestable: !tested && (!workflows || gaveUpOnCI(pull, now.Now())),
		Text:       text,
	}, nil
}

// readAdvisory is whether an advisory names every package the update installs as its fix, from
// an affected version.
func readAdvisory(ctx context.Context, from workflow.Lookup, text hostile.Of[PullText]) (Advisory, error) {
	feed, err := workflow.Get[advisory.Advisories](from)
	if err != nil {
		return Advisory{}, err
	}
	packages, ok := open(hostile.Map(text, UpdateOf))
	if !ok || len(packages) == 0 {
		return Advisory{}, nil
	}
	for _, p := range packages {
		fixed, err := fixedBy(ctx, feed, p)
		if err != nil {
			return Advisory{}, err
		}
		if !fixed {
			return Advisory{}, nil
		}
	}
	return Advisory{NamesTheFix: true}, nil
}

// fixedBy reports whether an advisory names p's target as the fix for the version it replaces.
func fixedBy(ctx context.Context, feed advisory.Advisories, p opened) (bool, error) {
	if p.Datasource.feed == "" || p.Current == nil {
		return false, nil
	}
	ecosystem, err := hostile.ParseSlug(p.Datasource.feed)
	if err != nil {
		return false, nil
	}
	found, err := feed.ByPackage(ctx, ecosystem, p.Name)
	if err != nil {
		return false, err
	}
	for _, a := range found {
		if advisory.Validate(a, p.Datasource.feed, p.Name.String(), p.Current.String(), p.Version.String()) == advisory.Valid {
			return true, nil
		}
	}
	return false, nil
}

// readDiff diffs each package's two releases. A package whose releases cannot be found or read
// makes the whole update unreadable, which a person must look at.
func readDiff(ctx context.Context, from workflow.Lookup, text hostile.Of[PullText]) (PackageDiff, error) {
	reg, err := workflow.Get[registry.Registry](from)
	if err != nil {
		return PackageDiff{}, err
	}
	unreadable := PackageDiff{Unshown: hostile.New(""), Diff: hostile.New("")}
	packages, ok := open(hostile.Map(text, UpdateOf))
	if !ok || len(packages) == 0 {
		return unreadable, nil
	}
	all := PackageDiff{Readable: true, Diff: hostile.New("")}
	unshown := hostile.New([]string(nil))
	for _, p := range packages {
		found, ok, err := diffOne(ctx, reg, p)
		if err != nil {
			return PackageDiff{}, err
		}
		if !ok {
			return unreadable, nil
		}
		all.InstallScriptAdded = all.InstallScriptAdded || found.InstallScriptAdded
		all.Truncated = all.Truncated || found.Truncated
		unshown = hostile.Map(hostile.Zip(unshown, found.Unshown), withUnshown)
		all.Diff = hostile.Map(hostile.Zip(all.Diff, found.Diff), concatenated)
	}
	all.Unshown = hostile.Map(unshown, oneToALine)
	return all, nil
}

// diffOne diffs one package's two releases, and is false if they cannot be named or read, or
// no registry Almanac reads publishes them. A registry that fails is an error, which a retry may
// get past.
func diffOne(ctx context.Context, reg registry.Registry, p opened) (registry.PackageDiff, bool, error) {
	if p.Datasource.ecosystem == registry.Unsupported {
		return registry.PackageDiff{}, false, nil
	}
	old, okOld := p.release(p.Current)
	updated, okNew := p.release(&p.Version)
	if !okOld || !okNew {
		return registry.PackageDiff{}, false, nil
	}
	before, err := reg.Artifact(ctx, old)
	if err != nil {
		return registry.PackageDiff{}, false, err
	}
	after, err := reg.Artifact(ctx, updated)
	if err != nil {
		return registry.PackageDiff{}, false, err
	}
	found, err := registry.DiffArchives(before, after, registry.DefaultLimits())
	if err != nil {
		return registry.PackageDiff{}, false, nil
	}
	return found, true, nil
}

// withUnshown adds a package's unshown files to the list. It stays hostile, so that no step can
// lose it.
func withUnshown(names hostile.Pair[[]string, []string]) []string {
	return append(append([]string(nil), names.First...), names.Second...)
}

func concatenated(texts hostile.Pair[string, string]) string { return texts.First + texts.Second }

func oneToALine(names []string) string { return strings.Join(names, "\n") }

// published is when the newest release the update installs was published.
func published(ctx context.Context, from workflow.Lookup, text hostile.Of[PullText]) (cooled.Published, error) {
	reg, err := workflow.Get[registry.Registry](from)
	if err != nil {
		return cooled.Published{}, err
	}
	return Newest(ctx, reg, hostile.Map(text, UpdateOf))
}
