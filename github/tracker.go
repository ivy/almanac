package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/hostile/reveal"
)

// label marks every issue Almanac files, so a retry looks only among them.
const label = "almanac"

// Issues is effect.Tracker on GitHub. File opens an issue. Ask reaches a person on the pull
// request it is about, with a comment and an assignment, or as an issue when it is about no
// pull request (R-41).
//
// GitHub offers no idempotency key, so each issue and comment carries its key in a marker, and
// a retry looks for that marker first.
type Issues struct {
	client *Client
	person string
}

var _ effect.Tracker = (*Issues)(nil)

// NewIssues is a tracker that writes through client. Questions go to person, a GitHub login,
// who is mentioned and assigned; with no person they are asked of no one in particular.
func NewIssues(client *Client, person string) *Issues {
	return &Issues{client: client, person: person}
}

func marker(key string) string { return "<!-- almanac:" + key + " -->" }

func (t *Issues) mention() string {
	if t.person == "" {
		return ""
	}
	return "@" + t.person + " "
}

func (t *Issues) assignees() []string {
	if t.person == "" {
		return []string{}
	}
	return []string{t.person}
}

// File opens an issue whose body is quoted as inert text. It is assigned to no one: it is work
// for later, not a question.
func (t *Issues) File(ctx context.Context, key string, issue effect.Issue) error {
	return t.open(ctx, key, issue.Target, issue.Title, quoted(issue.Body), []string{})
}

// Ask puts a question to the person, on the pull request it is about if there is one.
func (t *Issues) Ask(ctx context.Context, key string, question effect.Question) error {
	if question.Pull != 0 {
		return t.askOnPull(ctx, key, question)
	}
	text := t.mention() + question.Question + "\n\n" + quoted(question.Body)
	return t.open(ctx, key, question.Target, question.Question, text, t.assignees())
}

// carries reports whether any item, an issue or a comment, has a body holding mark.
func carries(items []json.RawMessage, mark string) bool {
	for _, item := range items {
		var found struct {
			Body *string `json:"body"`
		}
		if json.Unmarshal(item, &found) == nil && found.Body != nil && strings.Contains(*found.Body, mark) {
			return true
		}
	}
	return false
}

func (t *Issues) open(ctx context.Context, key string, target hostile.Slug, title, text string, assignees []string) error {
	mark := marker(key)
	filed, err := t.client.all(ctx, "/repos/"+target.String()+"/issues?state=all&labels="+label)
	if err != nil {
		return err
	}
	if carries(filed, mark) {
		return nil
	}
	issue := map[string]any{
		"title":     title,
		"body":      text + "\n\n" + mark + "\n",
		"labels":    []string{label},
		"assignees": assignees,
	}
	return t.client.write(ctx, http.MethodPost, "/repos/"+target.String()+"/issues", issue, nil)
}

// askOnPull assigns first, which is safe to repeat, so a retry after a crash between the two
// still assigns, and the marker keeps it to one comment.
func (t *Issues) askOnPull(ctx context.Context, key string, question effect.Question) error {
	thread := fmt.Sprintf("/repos/%s/issues/%d", question.Target, question.Pull)
	if t.person != "" {
		if err := t.client.write(ctx, http.MethodPost, thread+"/assignees", map[string]any{"assignees": t.assignees()}, nil); err != nil {
			return err
		}
	}
	mark := marker(key)
	comments, err := t.client.all(ctx, thread+"/comments")
	if err != nil {
		return err
	}
	if carries(comments, mark) {
		return nil
	}
	text := t.mention() + question.Question + "\n\n" + quoted(question.Body) + "\n\n" + mark + "\n"
	return t.client.write(ctx, http.MethodPost, thread+"/comments", map[string]string{"body": text}, nil)
}

// quoted shows hostile text as inert code. The fence is longer than any run of backticks in
// the text, so the text cannot close it, and mentions and markup inside it do nothing.
func quoted(body hostile.Of[string]) string {
	text := reveal.Value(body)
	if text == "" {
		return ""
	}
	longest := 0
	for run := range strings.FieldsFuncSeq(text, func(r rune) bool { return r != '`' }) {
		longest = max(longest, len(run))
	}
	fence := strings.Repeat("`", max(longest, 2)+1)
	return fence + "text\n" + text + "\n" + fence
}
