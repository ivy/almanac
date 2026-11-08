package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile/reveal"
	"github.com/ivy/almanac/patch"
)

// CommitAuthor is who every commit Almanac makes is by. Renovate's gitIgnoredAuthors matches
// on it, so it must not vary, or automerge stops on every patched branch.
type CommitAuthor struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// DefaultAuthor is almanac[bot].
func DefaultAuthor() CommitAuthor {
	return CommitAuthor{Name: "almanac[bot]", Email: "almanac[bot]@users.noreply.github.com"}
}

// Forge is effect.Forge on GitHub: statuses, and checked patches pushed to a pull request's
// own branch.
type Forge struct {
	client *Client
	author CommitAuthor
}

var _ effect.Forge = (*Forge)(nil)

// NewForge is a forge that writes through client, committing as author.
func NewForge(client *Client, author CommitAuthor) *Forge {
	return &Forge{client: client, author: author}
}

// Attest publishes a status on a commit. Posting the same status again changes nothing.
func (f *Forge) Attest(ctx context.Context, _ string, status effect.Attest) error {
	state, err := stateName(status.State)
	if err != nil {
		return err
	}
	body := map[string]string{"state": state, "context": status.Context}
	return f.client.write(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/statuses/%s", status.Repo, status.Sha), body, nil)
}

func stateName(state effect.AttestState) (string, error) {
	switch state {
	case effect.Pending:
		return "pending", nil
	case effect.Success:
		return "success", nil
	case effect.Failure:
		return "failure", nil
	default:
		return "", fmt.Errorf("github: %v is not a status state", state)
	}
}

// trailer is how a retry finds a commit it already made.
func trailer(key string) string { return "Almanac-Key: " + key }

// Push commits a checked patch to commit.Branch as one commit on top of commit.Base, then
// moves the branch there without forcing. It refuses a branch that is not the pull request's
// head, a pull request from a fork, one that is not open, and a branch that moved after the
// patch was checked, and then writes nothing. A retry with the same key that finds its own
// commit at the head does nothing.
func (f *Forge) Push(ctx context.Context, key string, commit effect.Commit) error {
	done, err := f.verify(ctx, key, commit)
	if err != nil || done {
		return err
	}
	repo := commit.Repo.String()
	checked := reveal.Value(commit.Patch)
	entries := make([]map[string]any, 0, len(checked.Changes))
	for _, change := range checked.Changes {
		entry, err := f.treeEntry(ctx, repo, change)
		if err != nil {
			return err
		}
		entries = append(entries, entry)
	}
	base := commit.Base.String()
	var tree, made struct {
		Sha string `json:"sha"`
	}
	treeBody := map[string]any{"base_tree": base, "tree": entries}
	if err := f.client.write(ctx, http.MethodPost, "/repos/"+repo+"/git/trees", treeBody, &tree); err != nil {
		return err
	}
	commitBody := map[string]any{
		"message":   commit.Message + "\n\n" + trailer(key),
		"tree":      tree.Sha,
		"parents":   []string{base},
		"author":    f.author,
		"committer": f.author,
	}
	if err := f.client.write(ctx, http.MethodPost, "/repos/"+repo+"/git/commits", commitBody, &made); err != nil {
		return err
	}
	// Set before the branch moves, so the new commit is never unheld.
	if commit.Hold != "" {
		hold := map[string]string{"state": "pending", "context": commit.Hold}
		if err := f.client.write(ctx, http.MethodPost, "/repos/"+repo+"/statuses/"+made.Sha, hold, nil); err != nil {
			return err
		}
	}
	// Never forced: if the branch moved in the meantime, GitHub refuses this.
	update := map[string]any{"sha": made.Sha, "force": false}
	return f.client.write(ctx, http.MethodPatch, "/repos/"+repo+"/git/refs/heads/"+commit.Branch.String(), update, nil)
}

// verify checks the push's target, and reports whether this push already happened.
func (f *Forge) verify(ctx context.Context, key string, commit effect.Commit) (bool, error) {
	var pull struct {
		State string `json:"state"`
		Head  struct {
			Ref  string `json:"ref"`
			Sha  string `json:"sha"`
			Repo *struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
	}
	if err := f.client.getJSON(ctx, pullPath(commit.Repo, commit.Pull), &pull); err != nil {
		return false, err
	}
	refuse := func(reason string) error { return diag.PushRefused(commit.Branch.String(), reason) }
	switch {
	case pull.Head.Ref != commit.Branch.String():
		return false, refuse("it is not this pull request's branch")
	case pull.Head.Repo == nil || pull.Head.Repo.FullName != commit.Repo.String():
		return false, refuse("the pull request is from a fork")
	case pull.State != "open":
		return false, refuse("the pull request is not open")
	case pull.Head.Sha == commit.Base.String():
		return false, nil
	}
	if f.committedAlready(ctx, commit, key, pull.Head.Sha) {
		return true, nil
	}
	return false, refuse("the branch moved after the patch was checked")
}

// committedAlready reports whether head is a commit this push made. A commit that cannot be
// read is not one this push can claim, so it counts as someone else's.
func (f *Forge) committedAlready(ctx context.Context, commit effect.Commit, key, head string) bool {
	var made struct {
		Message string `json:"message"`
	}
	path := fmt.Sprintf("/repos/%s/git/commits/%s", commit.Repo, head)
	if f.client.getJSON(ctx, path, &made) != nil {
		return false
	}
	return strings.Contains(made.Message, trailer(key))
}

// errLink is a change a checked patch cannot hold, because the policy refuses every link.
var errLink = errors.New("github: a checked patch cannot hold a link")

func (f *Forge) treeEntry(ctx context.Context, repo string, change patch.Change) (map[string]any, error) {
	switch change.Op {
	case patch.Delete:
		return map[string]any{"path": change.Path, "mode": "100644", "type": "blob", "sha": nil}, nil
	case patch.Write:
		var blob struct {
			Sha string `json:"sha"`
		}
		body := map[string]string{"content": change.Content, "encoding": "base64"}
		if err := f.client.write(ctx, http.MethodPost, "/repos/"+repo+"/git/blobs", body, &blob); err != nil {
			return nil, err
		}
		mode := "100644"
		if change.Executable {
			mode = "100755"
		}
		return map[string]any{"path": change.Path, "mode": mode, "type": "blob", "sha": blob.Sha}, nil
	default:
		return nil, errLink
	}
}
