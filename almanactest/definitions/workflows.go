package definitions

import (
	"github.com/ivy/almanac/hostile"

	. "github.com/ivy/almanac/dsl"
)

// ErrorTriage files a bug for an application error worth fixing. A cheap classifier screens
// the error first, so most errors cost one call; an investigator reads the rest. Work is filed
// in the repository the report names, never one the investigator names.
var ErrorTriage = Define("error_triage",
	Intent("File a bug for application errors worth fixing."),
	On(ErrorReported),
	Key(issueID),
	Subjects(RepoSubject(issueRepo)),
	May(Files, Asks),
	Limits(PerHour(10)),

	Screen("worth_it", "Is the error worth a look?", "Is this error worth an engineer's attention?", issueMessage, 0.7),
	Read("investigate", "Investigate the error", findings, Investigator, Input("message", issueMessage)),
	Decide("triage", "How sure is the investigator?",
		Rule("confident", "Sure enough to file", findings.Is(Confident),
			File(issueRepo, Title("Fix this application error")).With(Showing(findings, Summary))),
		Otherwise("unsure", "Ask whether to fix it",
			Ask(issueRepo, Title("Is this error worth fixing?")).With(Showing(findings, Summary))),
	),
)

// The facts of ErrorTriage: the report, whose message is hostile, and the findings.
var (
	issue        = EventOf(ErrorReported)
	issueID      = Field[uint64](issue, "issue_id")
	issueRepo    = Field[hostile.Slug](issue, "repo")
	issueMessage = HostileField[string](issue, "message")
	findings     = Hostile[Findings]("findings")
)

// DependencyUpdates reports whether a dependency update is safe, and asks for a fix if its
// build broke. The reviewer can only veto: no answer it gives passes an update that failed the
// registry's checks.
var DependencyUpdates = Define("dependency_updates",
	Intent("Report whether a dependency update is safe, and request a fix if its build broke."),
	On(PullRequestOpened),
	Key(prRepo, prBranch),
	Subjects(RepoSubject(prRepo)),
	Links(PackageSubject(evRepo, evPackage)),
	May(Attests, Awaits, Files, Asks),
	Limits(PerHour(10)),

	Observe("registry", "Compare the package's two releases", evidence, RegistryEvidence, pr),
	Read("review", "Have the reviewer read the diff", review, Reviewer,
		Input("diff", evDiff), Var("repo", prRepo)),
	Decide("policy", "Is the update safe?",
		Rule("untrusted", "Failed the trust checks, or the reviewer objected",
			Any(Not(evidence.Is(TrustChecksPass)), review.Is(RaisedConcerns)),
			Attest(trust, Failure, evRepo, evSha),
			Ask(evRepo, Title("This dependency update failed trust checks")).With(Showing(review, ReviewNotes))),
		Rule("pending", "Trusted, and CI is still running", evidence.Is(CIRunning), Await("CI to finish")),
		Rule("green", "Trusted, and CI passed", evidence.Is(CIPassed), Attest(trust, Success, evRepo, evSha)),
		Otherwise("red", "Trusted, and CI failed",
			Attest(trust, Success, evRepo, evSha),
			File(evRepo, Title("Fix the build on this dependency update")).With(Showing(review, ReviewNotes))),
	),
)

// The facts of DependencyUpdates: the pull request, the registry's evidence, whose diff is
// hostile, and the review.
var (
	pr        = EventOf(PullRequestOpened)
	prRepo    = Field[hostile.Slug](pr, "repo")
	prBranch  = Field[hostile.Slug](pr, "head_branch")
	evidence  = Fact[UpdateEvidence]("evidence")
	evRepo    = Field[hostile.Slug](evidence, "repo")
	evSha     = Field[hostile.Sha](evidence, "sha")
	evPackage = Field[hostile.Slug](evidence, "package")
	evDiff    = HostileField[string](evidence, "diff")
	review    = Hostile[Review]("review")
)

// trust is the status DependencyUpdates sets on the updated commit.
const trust = "almanac/trust"
