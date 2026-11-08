package renovate

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ivy/almanac/clock"
	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/github"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/registry"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/wire"
	"github.com/ivy/almanac/workflow"
)

// The scans: what backfill and catch-up find, as events taking the same path as a webhook.
var (
	ScanFailing = vocab.Scan(Vocabulary, "renovate.failing_pulls",
		"Renovate pull requests whose CI is red, or whose repair passed and is still held",
		scanFailing, needsGitHub, needsTarget, needsRegistry, needsClock)
	ScanSecurity = vocab.Scan(Vocabulary, "renovate.security_pulls",
		"Renovate security pull requests Almanac has given no final verdict on",
		scanSecurity, needsGitHub, needsTarget, needsRegistry, needsClock)
)

// scanning is what a scan reads through.
type scanning struct {
	client *github.Client
	target Target
	reg    registry.Registry
	now    clock.Clock
}

func scanningFrom(scanner *workflow.Scanner) (scanning, error) {
	var s scanning
	var err error
	if s.client, err = workflow.Get[*github.Client](scanner); err != nil {
		return s, err
	}
	if s.target, err = workflow.Get[Target](scanner); err != nil {
		return s, err
	}
	if s.reg, err = workflow.Get[registry.Registry](scanner); err != nil {
		return s, err
	}
	s.now, err = workflow.Get[clock.Clock](scanner)
	return s, err
}

// eachPull calls visit with every open Renovate pull request in the scanned repositories.
func (s scanning) eachPull(ctx context.Context, scanner *workflow.Scanner, visit func(repo hostile.Slug, pull hostile.Of[json.RawMessage]) error) error {
	repos, err := ScannedRepositories(ctx, scanner, s.client, s.target.Owner)
	if err != nil {
		return err
	}
	for _, repo := range repos {
		pulls, err := s.client.PullsBy(ctx, repo, s.target.Author)
		if err != nil {
			return err
		}
		for _, pull := range pulls {
			if err := visit(repo, pull); err != nil {
				return err
			}
		}
	}
	return nil
}

// headOf is a pull request's number, branch and head, and false if its branch or head is not
// one Almanac can name, as a webhook for it would not parse either.
func headOf(pull hostile.Of[json.RawMessage]) (number uint64, branch hostile.Slug, sha hostile.Sha, ok bool) {
	var listed struct {
		Number uint64 `json:"number"`
		Head   struct {
			Ref hostile.Slug `json:"ref"`
			Sha hostile.Sha  `json:"sha"`
		} `json:"head"`
	}
	if hostile.DecodeClean(pull, &listed) != nil || listed.Head.Sha == (hostile.Sha{}) || listed.Head.Ref == (hostile.Slug{}) {
		return 0, hostile.Slug{}, hostile.Sha{}, false
	}
	return listed.Number, listed.Head.Ref, listed.Head.Sha, true
}

// scanFailing finds pull requests whose CI is red now, and repairs whose CI passed and that are
// still held.
func scanFailing(ctx context.Context, scanner *workflow.Scanner) ([]workflow.Found, error) {
	s, err := scanningFrom(scanner)
	if err != nil {
		return nil, err
	}
	var found []workflow.Found
	err = s.eachPull(ctx, scanner, func(repo hostile.Slug, pull hostile.Of[json.RawMessage]) error {
		event, ok, err := s.needingARun(ctx, repo, pull)
		if ok {
			found = append(found, event)
		}
		return err
	})
	return found, err
}

// needingARun is a pull request that needs a run, if any. The delivery ID carries what a later
// scan must notice: a pull request waiting out its cooldown stays a duplicate and costs nothing,
// and gets a new ID the moment it cools. A hold with no run behind it does the same, so a
// status left pending by a run that died is cleared.
func (s scanning) needingARun(ctx context.Context, repo hostile.Slug, pull hostile.Of[json.RawMessage]) (workflow.Found, bool, error) {
	number, branch, sha, ok := headOf(pull)
	if !ok {
		return workflow.Found{}, false, nil
	}
	checks, trust, err := checksAt(ctx, s.client, repo, sha)
	if err != nil {
		return workflow.Found{}, false, err
	}
	failed := checks == ChecksRed
	awaiting := trust == TrustPending && checks == ChecksGreen
	if !failed && !awaiting {
		return workflow.Found{}, false, nil
	}
	messages, err := s.client.PullCommitMessages(ctx, repo, number)
	if err != nil {
		return workflow.Found{}, false, err
	}
	patched := hostile.Narrow(messages, anyCarriesPatch)
	// Only a hold on a repair is this workflow's to clear. Any other was placed on purpose, such
	// as security_fixes holding a fix that is too young.
	held := awaiting && patched
	if !failed && !held {
		return workflow.Found{}, false, nil
	}
	cooledNow := CooldownOf(ctx, s.reg, pull, s.now.Now()) == cooled.Passed
	id := fmt.Sprintf("%s%s#%d@%s", scanIDPrefix, repo, number, sha)
	event := workflow.Scanned[BuildEvent](id, SuitePayload(repo, branch, sha, failed, patched)).
		AtState(fmt.Sprintf("cooled=%t,held=%t", cooledNow, held))
	return event, true, nil
}

// scanSecurity finds security pull requests with no final verdict. A fix that was too young
// gets a new delivery ID once it has cooled.
func scanSecurity(ctx context.Context, scanner *workflow.Scanner) ([]workflow.Found, error) {
	s, err := scanningFrom(scanner)
	if err != nil {
		return nil, err
	}
	var found []workflow.Found
	err = s.eachPull(ctx, scanner, func(repo hostile.Slug, pull hostile.Of[json.RawMessage]) error {
		event, ok, err := s.unsettledSecurity(ctx, repo, pull)
		if ok {
			found = append(found, event)
		}
		return err
	})
	return found, err
}

// unsettledSecurity is a security pull request Almanac has given no final verdict on, if pull
// is one.
func (s scanning) unsettledSecurity(ctx context.Context, repo hostile.Slug, pull hostile.Of[json.RawMessage]) (workflow.Found, bool, error) {
	number, branch, sha, ok := headOf(pull)
	if !ok {
		return workflow.Found{}, false, nil
	}
	payload := openedPayload(repo, branch, sha, number, hostile.Narrow(pull, labeledSecurity))
	event, err := wire.Parse[SecurityEvent](payload)
	if err != nil {
		return workflow.Found{}, false, err
	}
	if !isSecurity(event) {
		return workflow.Found{}, false, nil
	}
	checks, trust, err := checksAt(ctx, s.client, repo, sha)
	if err != nil {
		return workflow.Found{}, false, err
	}
	if trust.IsFinal() {
		return workflow.Found{}, false, nil
	}
	now := s.now.Now()
	age := CooldownOf(ctx, s.reg, pull, now)
	id := fmt.Sprintf("%s%s#%d@%s", securityScanIDPrefix, repo, number, sha)
	state := fmt.Sprintf("%s,tested=%t,gave_up=%t", age, checks != ChecksNone, gaveUpOnCI(pull, now))
	return workflow.Scanned[SecurityEvent](id, payload).AtState(state), true, nil
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
