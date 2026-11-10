package e2eauth

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

var okToTestCommentRE = regexp.MustCompile(`(?i)^/ok-to-test[[:space:]]+([0-9a-fA-F]{40})[[:space:]]*$`)

func checkIssueComment(ctx context.Context, gh GitHubGate, in Input) Result {
	owner, repo := in.Owner, in.Repo
	prNumber := in.PRNumber

	if in.CommentID <= 0 {
		return errorResult(fmt.Errorf("COMMENT_ID must be a numeric comment id"))
	}

	comment, err := gh.GetIssueComment(ctx, owner, repo, in.CommentID)
	if err != nil {
		return errorResult(err)
	}

	commentLogin := strings.TrimSpace(comment.Author)
	eventCommenter := strings.TrimSpace(in.CommenterLogin)
	if eventCommenter == "" {
		eventCommenter = commentLogin
	}
	if commentLogin == "" ||
		comment.IssueNumber != prNumber ||
		eventCommenter != commentLogin {
		return errorResult(fmt.Errorf("comment %d does not match PR %d / %s", in.CommentID, prNumber, eventCommenter))
	}
	commenter := commentLogin

	commenterRole, err := collaboratorRole(ctx, gh, owner, repo, commenter)
	if err != nil {
		return errorResult(err)
	}
	if !isWriteRole(commenterRole) {
		return Result{Reason: "untrusted_commenter"}
	}

	headSHA, err := gh.GetPullRequestHeadSHA(ctx, owner, repo, prNumber)
	if err != nil {
		return errorResult(err)
	}

	res := Result{HeadSHA: headSHA}
	namedSHA := parseOkToTestNamedSHA(comment.Body)
	if namedSHA == "" {
		res.Reason = "missing_sha"
		return res
	}
	if strings.EqualFold(headSHA, namedSHA) {
		res.Authorized = true
		res.Reason = "ok_to_test"
		return res
	}
	res.Reason = "stale_ok_to_test"
	return res
}

func parseOkToTestNamedSHA(body string) string {
	line := firstCommentLine(body)
	m := okToTestCommentRE.FindStringSubmatch(line)
	if len(m) != 2 {
		return ""
	}
	return strings.ToLower(m[1])
}

func firstCommentLine(body string) string {
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[:i]
	}
	return strings.TrimSuffix(body, "\r")
}
