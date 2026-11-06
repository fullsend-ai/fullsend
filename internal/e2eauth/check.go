package e2eauth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const OkToTestLabel = "ok-to-test"

// TrustedAuthorLogins are PR authors that may run e2e without write access.
// See docs/contributing/bot-identities.md.
var TrustedAuthorLogins = map[string]bool{
	"renovate-fullsend[bot]": true,
	"fullsend-ai-coder[bot]": true,
}

// Input carries workflow event fields for an authorization decision.
type Input struct {
	Owner           string
	Repo            string
	PRNumber        int
	EventAction     string
	PRUpdatedAt     string
	PRAuthorLogin   string
	LabelActorLogin string
	DryRun          bool
}

// Result is the authorization outcome written to GITHUB_OUTPUT by the gate.
type Result struct {
	Authorized   bool
	Reason       string
	LabelRemoved bool
	APIError     bool
	Err          error
}

// Check decides whether a pull request may run credentialed e2e tests in CI.
func Check(ctx context.Context, gh GitHubGate, in Input) Result {
	res := Result{Reason: "unauthorized"}

	owner, repo := in.Owner, in.Repo
	prNumber := in.PRNumber

	var prCtx *forge.PullRequestAuthorizationContext

	authorLogin := strings.TrimSpace(in.PRAuthorLogin)
	if authorLogin == "" {
		fetched, err := gh.GetPullRequestAuthorizationContext(ctx, owner, repo, prNumber)
		if err != nil {
			return errorResult(err)
		}
		prCtx = fetched
		authorLogin = strings.TrimSpace(prCtx.AuthorLogin)
	}
	if authorLogin == "" {
		return Result{Reason: "error", Err: fmt.Errorf("missing PR author login")}
	}

	if TrustedAuthorLogins[authorLogin] {
		res.Authorized = true
		res.Reason = "trusted_bot"
		return res
	}

	authorRole, err := collaboratorRole(ctx, gh, owner, repo, authorLogin)
	if err != nil {
		return errorResult(err)
	}
	if isWriteRole(authorRole) {
		res.Authorized = true
		res.Reason = "trusted_author"
		return res
	}

	if prCtx == nil {
		fetched, err := gh.GetPullRequestAuthorizationContext(ctx, owner, repo, prNumber)
		if err != nil {
			return errorResult(err)
		}
		prCtx = fetched
	}

	if !hasLabel(prCtx.Labels, OkToTestLabel) {
		return res
	}

	eventAction := in.EventAction
	if eventAction != "labeled" {
		events, err := gh.ListIssueLabelEvents(ctx, owner, repo, prNumber)
		if err != nil {
			return errorResult(err)
		}

		okToTestAt := latestOkToTestLabelTime(events)
		lastPushAt := strings.TrimSpace(in.PRUpdatedAt)
		if lastPushAt == "" {
			lastPushAt = strings.TrimSpace(prCtx.UpdatedAt)
		}

		if okToTestAt != "" && lastPushAt != "" && okToTestAt > lastPushAt {
			res.Reason = "fresh_ok_to_test"
		} else {
			removed, err := tryRemoveOkToTestLabel(ctx, gh, in, owner, repo, prNumber)
			if err != nil {
				return errorResult(err)
			}
			res.LabelRemoved = removed
			res.Reason = "stale_ok_to_test"
		}
		return res
	}

	labelerLogin := strings.TrimSpace(in.LabelActorLogin)
	if labelerLogin == "" {
		res.Reason = "untrusted_labeler"
		return res
	}

	labelerRole, err := collaboratorRole(ctx, gh, owner, repo, labelerLogin)
	if err != nil {
		return errorResult(err)
	}

	if !isWriteRole(labelerRole) {
		res.Reason = "untrusted_labeler"
		events, err := gh.ListIssueLabelEvents(ctx, owner, repo, prNumber)
		if err != nil {
			return errorResult(err)
		}
		if latestOkToTestLabeler(events) == labelerLogin {
			removed, err := tryRemoveOkToTestLabel(ctx, gh, in, owner, repo, prNumber)
			if err != nil {
				return errorResult(err)
			}
			res.LabelRemoved = removed
		}
		return res
	}

	res.Authorized = true
	res.Reason = "ok_to_test"
	return res
}

func errorResult(err error) Result {
	return Result{Authorized: false, Reason: "error", APIError: true, Err: err}
}

func hasLabel(labels []string, name string) bool {
	for _, l := range labels {
		if l == name {
			return true
		}
	}
	return false
}

func collaboratorRole(ctx context.Context, gh GitHubGate, owner, repo, username string) (string, error) {
	if username == "" {
		return "", fmt.Errorf("empty username")
	}
	detail, err := gh.GetCollaboratorPermissionDetail(ctx, owner, repo, username)
	if errors.Is(err, forge.ErrNotFound) {
		return "none", nil
	}
	if err != nil {
		return "", err
	}
	return ResolveCollaboratorRole(detail.RoleName, detail.Permission)
}

func latestOkToTestLabelTime(events []forge.IssueLabelEvent) string {
	var max string
	for _, ev := range events {
		if ev.LabelName != OkToTestLabel {
			continue
		}
		if ev.CreatedAt > max {
			max = ev.CreatedAt
		}
	}
	return max
}

func latestOkToTestLabeler(events []forge.IssueLabelEvent) string {
	maxTime := latestOkToTestLabelTime(events)
	if maxTime == "" {
		return ""
	}
	var login string
	seen, ambiguous := false, false
	for _, ev := range events {
		if ev.LabelName != OkToTestLabel || ev.CreatedAt != maxTime {
			continue
		}
		actor := strings.TrimSpace(ev.ActorLogin)
		if actor == "" {
			ambiguous = true // unknown actor at the latest time: keep the label
			continue
		}
		if !seen {
			login, seen = actor, true
		} else if actor != login {
			ambiguous = true
		}
	}
	if ambiguous {
		return ""
	}
	return login
}

// tryRemoveOkToTestLabel removes ok-to-test when not in dry-run. Dry-run still
// reports label_removed=true; HTTP 404 means the label is already gone.
func tryRemoveOkToTestLabel(ctx context.Context, gh GitHubGate, in Input, owner, repo string, prNumber int) (bool, error) {
	if in.DryRun {
		return true, nil
	}
	err := gh.RemoveIssueLabel(ctx, owner, repo, prNumber, OkToTestLabel)
	if errors.Is(err, forge.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
