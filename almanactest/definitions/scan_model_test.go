package definitions_test

import (
	"context"

	"github.com/ivy/almanac/github/events"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/workflow"

	. "github.com/ivy/almanac/dsl"
)

// logDir and pullsClient are providers a scan reaches, as LogDir and GithubClient are in
// crates/definitions/tests/origins.rs. A run reaches the client too.
type (
	logDir      struct{}
	pullsClient struct{}
)

// scanning is a vocabulary of one scan that needs both providers, and one observer that needs
// the client.
var scanning = vocab.New()

var (
	scanOpened = vocab.Event[events.PullRequestOpened](scanning, "github.pull_request_opened",
		"a pull request was opened")
	needsADirectory = vocab.Scan(scanning, "scan.needs_a_directory",
		"lists nothing, through a log directory and the client",
		listNothing, providers.Require[logDir](), providers.Require[pullsClient]())
	readAgain = vocab.Observer(scanning, "client.pull",
		"the pull request, read again through the client",
		readPull, providers.Require[pullsClient]())
)

// seen is what a run notes of a pull request.
type seen struct {
	Draft bool `json:"draft"`
}

func listNothing(context.Context, *workflow.Scanner) ([]workflow.Found, error) { return nil, nil }

func readPull(_ context.Context, from workflow.Lookup, pr events.PullRequestOpened) (seen, error) {
	if _, err := workflow.Get[pullsClient](from); err != nil {
		return seen{}, err
	}
	return seen{Draft: pr.Draft}, nil
}

// scanOnlyNeeds scans with needsADirectory, and its run reads through the client the scan
// also needs.
var scanOnlyNeeds = Define("scan_only_needs",
	Intent("Note what a pull request looks like."),
	On(scanOpened),
	Key(scannedRepo, scannedBranch),
	May(Awaits),
	Limits(PerHour(100)),
	Scan(needsADirectory),

	Observe("seen", "Read the pull request again", noted, readAgain, scannedPR),
	Decide("wait", "Wait",
		Otherwise("waits", "Wait for nothing", Await("nothing"))),
)

var (
	scannedPR     = EventOf(scanOpened)
	scannedRepo   = Field[hostile.Slug](scannedPR, "repo")
	scannedBranch = Field[hostile.Slug](scannedPR, "head_branch")
	noted         = Fact[seen]("seen")
)
