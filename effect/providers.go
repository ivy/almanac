package effect

import "context"

// Forge is where statuses and checked patches go. Key is stable across retries, so a repeat
// must change nothing.
type Forge interface {
	// Attest publishes a status check.
	Attest(ctx context.Context, key string, status Attest) error
	// Push commits a patch to commit.Branch, which must be the head of pull request
	// commit.Pull in commit.Repo and still at commit.Base. Anything else is an error and
	// changes nothing.
	Push(ctx context.Context, key string, commit Commit) error
}

// Tracker is where work and questions go. Key is stable across retries, so a repeat must change
// nothing.
type Tracker interface {
	File(ctx context.Context, key string, issue Issue) error
	Ask(ctx context.Context, key string, question Question) error
}

// Sweep recovers deliveries the webhook source failed to send, where it can.
type Sweep interface {
	// Sweep asks for failed deliveries to be sent again, and returns how many.
	Sweep(ctx context.Context) (int, error)
}
