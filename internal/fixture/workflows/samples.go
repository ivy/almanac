package workflows

import (
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/internal/fixture/sample"

	. "github.com/ivy/almanac/dsl"
)

// The facts of ErrorTriage.
var (
	issue        = EventOf(sample.ErrorReported)
	issueID      = Field[uint64](issue, "issue_id")
	issueRepo    = Field[hostile.Slug](issue, "repo")
	issueMessage = HostileField[string](issue, "message")
	findings     = Hostile[sample.Findings]("findings")
)

// ErrorTriage files a bug for an application error worth fixing. A cheap classifier screens
// the error first, so most errors cost one call; an investigator reads the rest.
var ErrorTriage = Define("error_triage",
	Intent("File a bug for application errors worth fixing."),
	On(sample.ErrorReported),
	Key(issueID),
	Subjects(RepoSubject(issueRepo)),
	May(Files, Asks),
	Limits(PerHour(10)),

	Screen("worth_it", "Is the error worth a look?", "Is this error worth an engineer's attention?", issueMessage, 0.7),
	Read("investigate", "Investigate the error", findings, sample.Investigator, Input("message", issueMessage)),
	Decide("triage", "How sure is the investigator?",
		Rule("confident", "Sure enough to file", findings.Is(sample.Confident),
			File(issueRepo, Title("Fix this application error")).With(Showing(findings, sample.Summary))),
		Otherwise("unsure", "Ask whether to fix it",
			Ask(issueRepo, Title("Is this error worth fixing?")).With(Showing(findings, sample.Summary))),
	),
)

// The facts of DependencyUpdates.
var (
	pr        = EventOf(sample.PullRequestOpened)
	prRepo    = Field[hostile.Slug](pr, "repo")
	prBranch  = Field[hostile.Slug](pr, "head_branch")
	evidence  = Fact[sample.UpdateEvidence]("evidence")
	evRepo    = Field[hostile.Slug](evidence, "repo")
	evSha     = Field[hostile.Sha](evidence, "sha")
	evPackage = Field[hostile.Slug](evidence, "package")
	verdict   = Hostile[sample.Review]("review")
)

// DependencyUpdates reports whether a dependency update is safe, and asks for a fix if its
// build broke. The model can only veto: it cannot pass an update that failed the deterministic
// checks.
var DependencyUpdates = Define("dependency_updates",
	Intent("Report whether a dependency update is safe, and request a fix if its build broke."),
	On(sample.PullRequestOpened),
	Key(prRepo, prBranch),
	Subjects(RepoSubject(prRepo)),
	Links(PackageSubject(evRepo, evPackage)),
	May(Attests, Awaits, Files, Asks),
	Limits(PerHour(10)),

	Observe("registry", "Compare the package's releases", evidence, sample.RegistryEvidence, pr),
	Read("review", "Have a reviewer read the evidence", verdict, sample.Reviewer,
		Input("evidence", evidence), Var("repo", prRepo)),
	Decide("policy", "Is the update safe?",
		Rule("untrusted", "Failed the trust checks, or the reviewer objected",
			Any(Not(evidence.Is(sample.TrustChecksPass)), verdict.Is(sample.RaisedConcerns)),
			Attest(trust, Failure, evRepo, evSha),
			Ask(evRepo, Title("This dependency update failed trust checks")).With(Showing(verdict, sample.ReviewNotes))),
		Rule("pending", "CI is still running", evidence.Is(sample.CIRunning), Await("CI to finish")),
		Rule("green", "Trusted, and CI passed", evidence.Is(sample.CIPassed), Attest(trust, Success, evRepo, evSha)),
		Otherwise("red", "Trusted, and CI failed",
			Attest(trust, Success, evRepo, evSha),
			File(evRepo, Title("Fix the build on this dependency update")).With(Showing(verdict, sample.ReviewNotes))),
	),
)

const trust = "almanac/trust"
