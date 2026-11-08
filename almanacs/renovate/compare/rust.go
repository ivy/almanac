package compare

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/github"
	"github.com/ivy/almanac/hostile"
)

// openPull is an open Renovate pull request, as GitHub lists it now.
type openPull struct {
	repo   hostile.Slug
	number uint64
	branch hostile.Slug
	head   hostile.Sha
}

func (p openPull) String() string { return fmt.Sprintf("%s#%d", p.repo, p.number) }

// openPulls are the open pull requests author opened across owner's repositories. One whose
// branch or head Almanac cannot name is left out, because no Go run could be about it either.
func openPulls(ctx context.Context, client *github.Client, owner, author string) ([]openPull, error) {
	listed, err := client.Repositories(ctx, owner)
	if err != nil {
		return nil, err
	}
	var out []openPull
	for _, listing := range listed {
		var named struct {
			FullName hostile.Slug `json:"full_name"`
		}
		if err := hostile.DecodeClean(listing, &named); err != nil {
			return nil, diag.Unreachable("listing the repositories "+owner+" owns",
				"GitHub lists one whose name is not a slug, so its pull requests cannot be compared")
		}
		pulls, err := client.PullsBy(ctx, named.FullName, author)
		if err != nil {
			return nil, err
		}
		for _, item := range pulls {
			var pull struct {
				Number uint64 `json:"number"`
				Head   struct {
					Ref hostile.Slug `json:"ref"`
					Sha hostile.Sha  `json:"sha"`
				} `json:"head"`
			}
			if hostile.DecodeClean(item, &pull) != nil || pull.Head.Ref == (hostile.Slug{}) || pull.Head.Sha == (hostile.Sha{}) {
				continue
			}
			out = append(out, openPull{repo: named.FullName, number: pull.Number, branch: pull.Head.Ref, head: pull.Head.Sha})
		}
	}
	return out, nil
}

// rustSide is what the Rust version did to one pull request, as GitHub shows it.
type rustSide struct {
	client *github.Client
	pull   openPull
	trust  map[string]renovate.Trust
}

// trustAt is what Almanac's status says on a commit.
func (r *rustSide) trustAt(ctx context.Context, sha string) (renovate.Trust, error) {
	if trust, ok := r.trust[sha]; ok {
		return trust, nil
	}
	parsed, err := hostile.ParseSha(sha)
	if err != nil {
		return renovate.Trust{}, err
	}
	statuses, err := r.client.CombinedStatus(ctx, r.pull.repo, parsed)
	if err != nil {
		return renovate.Trust{}, err
	}
	trust, err := renovate.TrustOf(statuses)
	if err != nil {
		return renovate.Trust{}, diag.Unreachable(fmt.Sprintf("reading Almanac's status on %s at %s", r.pull, sha), err.Error())
	}
	r.trust[sha] = trust
	return trust, nil
}

// statuses are what almanac/trust says on each commit: success and failure as actions, and
// pending as a hold, except on the head, where pending is a run under way.
func (r *rustSide) statuses(ctx context.Context, shas []string) ([]Action, error) {
	var out []Action
	for _, sha := range shas {
		trust, err := r.trustAt(ctx, sha)
		if err != nil {
			return nil, err
		}
		switch {
		case trust == renovate.TrustSuccess:
			out = append(out, Action{Kind: kindPass, Sha: sha})
		case trust == renovate.TrustFailure:
			out = append(out, Action{Kind: kindFail, Sha: sha})
		case trust == renovate.TrustPending && sha != r.pull.head.String():
			out = append(out, Action{Kind: kindHold, Sha: sha})
		}
	}
	return out, nil
}

// commit is the part of a listed commit a comparison reads that holds no text.
type commit struct {
	Sha     hostile.Sha `json:"sha"`
	Parents []struct {
		Sha hostile.Sha `json:"sha"`
	} `json:"parents"`
}

// zeroSha is what a forge that does not know a commit's SHA lists.
var zeroSha = strings.Repeat("0", 40)

// repairs are the commits on the pull request's branch that carry Almanac's trailer, each on
// the commit it was pushed onto where GitHub says which that is.
func (r *rustSide) repairs(ctx context.Context) ([]Action, error) {
	items, err := r.client.GetAll(ctx, fmt.Sprintf("/repos/%s/pulls/%d/commits", r.pull.repo, r.pull.number))
	if err != nil {
		return nil, err
	}
	var out []Action
	for _, item := range items {
		if !hostile.Narrow(item, carriesTrailer) {
			continue
		}
		var c commit
		if err := hostile.DecodeClean(item, &c); err != nil {
			return nil, diag.Unreachable("listing the commits on "+r.pull.String(), "GitHub listed a commit whose SHA is not one")
		}
		action := Action{Kind: kindPush, Key: keyOf(hostile.Map(item, trailerKey))}
		if sha := c.Sha.String(); sha != zeroSha {
			action.Commit = sha
		}
		if len(c.Parents) == 1 && c.Parents[0].Sha.String() != zeroSha {
			action.Sha = c.Parents[0].Sha.String()
		}
		if hostile.Narrow(item, byAlmanac) {
			action.Detail = github.DefaultAuthor().Name
		}
		out = append(out, action)
	}
	return out, nil
}

// asks are the comments on the pull request that carry the marker Almanac's tracker puts on a
// question it asks.
func (r *rustSide) asks(ctx context.Context) ([]Action, error) {
	items, err := r.client.GetAll(ctx, fmt.Sprintf("/repos/%s/issues/%d/comments", r.pull.repo, r.pull.number))
	if err != nil {
		return nil, err
	}
	var out []Action
	for _, item := range items {
		if hostile.Narrow(item, carriesAskMarker) {
			out = append(out, Action{Kind: kindAsk, Key: keyOf(hostile.Map(item, askKey))})
		}
	}
	return out, nil
}

const (
	// trailerPrefix starts the line Almanac's forge adds to every commit it makes.
	trailerPrefix = renovate.PatchTrailer + " "
	// askMarkerPrefix and askMarkerSuffix enclose the key Almanac's tracker hides in every
	// question it asks.
	askMarkerPrefix = "<!-- almanac:"
	askMarkerSuffix = " -->"
)

// carriesTrailer reports whether a listed commit's message carries Almanac's trailer.
func carriesTrailer(item json.RawMessage) bool {
	return strings.Contains(messageOf(item), renovate.PatchTrailer)
}

// byAlmanac reports whether a listed commit's author is the one Almanac commits as.
func byAlmanac(item json.RawMessage) bool {
	var listed struct {
		Commit struct {
			Author struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"commit"`
	}
	return json.Unmarshal(item, &listed) == nil && listed.Commit.Author.Name == github.DefaultAuthor().Name
}

func messageOf(item json.RawMessage) string {
	var listed struct {
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	}
	_ = json.Unmarshal(item, &listed)
	return listed.Commit.Message
}

// trailerKey is the key on a listed commit's trailer line, as a JSON string, for keyOf to
// validate.
func trailerKey(item json.RawMessage) json.RawMessage {
	for line := range strings.Lines(messageOf(item)) {
		if key, ok := strings.CutPrefix(strings.TrimSpace(line), trailerPrefix); ok {
			return quoted(key)
		}
	}
	return json.RawMessage("null")
}

// carriesAskMarker reports whether a comment carries the marker of a question Almanac asked.
func carriesAskMarker(item json.RawMessage) bool {
	return strings.Contains(bodyOf(item), askMarkerPrefix)
}

func bodyOf(item json.RawMessage) string {
	var comment struct {
		Body string `json:"body"`
	}
	_ = json.Unmarshal(item, &comment)
	return comment.Body
}

// askKey is the key in a comment's marker, as a JSON string, for keyOf to validate.
func askKey(item json.RawMessage) json.RawMessage {
	_, rest, _ := strings.Cut(bodyOf(item), askMarkerPrefix)
	key, _, ok := strings.Cut(rest, askMarkerSuffix)
	if !ok {
		return json.RawMessage("null")
	}
	return quoted(key)
}

func quoted(text string) json.RawMessage {
	encoded, err := json.Marshal(text)
	if err != nil {
		return json.RawMessage("null")
	}
	return encoded
}

// keyOf is a key read out of hostile text, if it is a slug, which every key Almanac makes is.
// Anything else is shown as unreadable, never as the text.
func keyOf(raw hostile.Of[json.RawMessage]) string {
	var key *hostile.Slug
	if hostile.DecodeClean(raw, &key) != nil || key == nil {
		return "(unreadable)"
	}
	return key.String()
}
