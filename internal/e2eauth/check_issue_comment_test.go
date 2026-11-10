package e2eauth

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const testHeadSHA = "1111111111111111111111111111111111111111"

func (h *harness) setHeadSHA(sha string) {
	h.fc.PullRequestHeadSHA = sha
}

func (h *harness) putComment(id, issueNum int, author, body string) {
	if h.fc.IssueComments == nil {
		h.fc.IssueComments = make(map[string][]forge.IssueComment)
	}
	key := fmt.Sprintf("%s/%s/%d", testOwner, testRepo, issueNum)
	h.fc.IssueComments[key] = append(h.fc.IssueComments[key], forge.IssueComment{
		ID:          id,
		Body:        body,
		Author:      author,
		IssueNumber: issueNum,
	})
}

func issueCommentInput(commentID int, commenter string) Input {
	in := baseInput()
	in.EventName = "issue_comment"
	in.CommentID = commentID
	in.CommenterLogin = commenter
	return in
}

func TestCheckIssueComment_OkToTestAuthorizes(t *testing.T) {
	h := newHarness()
	h.setHeadSHA(testHeadSHA)
	h.putComment(900, testPR, "maint", "/ok-to-test "+testHeadSHA)
	h.setRole("maint", "write")

	got := Check(context.Background(), h.gate, issueCommentInput(900, "maint"))
	assert.True(t, got.Authorized)
	assert.Equal(t, "ok_to_test", got.Reason)
	assert.Equal(t, testHeadSHA, got.HeadSHA)
	assert.False(t, got.LabelRemoved)
}

func TestCheckIssueComment_UntrustedCommenter(t *testing.T) {
	h := newHarness()
	h.setHeadSHA(testHeadSHA)
	h.putComment(900, testPR, "maint", "/ok-to-test "+testHeadSHA)
	h.setRole("maint", "read")

	got := Check(context.Background(), h.gate, issueCommentInput(900, "maint"))
	assert.False(t, got.Authorized)
	assert.Equal(t, "untrusted_commenter", got.Reason)
	assert.Empty(t, got.HeadSHA)
}

func TestCheckIssueComment_MissingSHA(t *testing.T) {
	h := newHarness()
	h.setHeadSHA(testHeadSHA)
	h.putComment(900, testPR, "maint", "/ok-to-test")
	h.setRole("maint", "write")

	got := Check(context.Background(), h.gate, issueCommentInput(900, "maint"))
	assert.False(t, got.Authorized)
	assert.Equal(t, "missing_sha", got.Reason)
	assert.Equal(t, testHeadSHA, got.HeadSHA)
}

func TestCheckIssueComment_StaleSHA(t *testing.T) {
	h := newHarness()
	h.setHeadSHA(testHeadSHA)
	other := "2222222222222222222222222222222222222222"
	h.putComment(900, testPR, "maint", "/ok-to-test "+other)
	h.setRole("maint", "write")

	got := Check(context.Background(), h.gate, issueCommentInput(900, "maint"))
	assert.False(t, got.Authorized)
	assert.Equal(t, "stale_ok_to_test", got.Reason)
	assert.Equal(t, testHeadSHA, got.HeadSHA)
}

func TestCheckIssueComment_UppercaseSHA(t *testing.T) {
	h := newHarness()
	lower := "abcdef0123456789abcdef0123456789abcdef01"
	h.setHeadSHA(lower)
	h.putComment(900, testPR, "maint", "/ok-to-test ABCDEF0123456789ABCDEF0123456789ABCDEF01")
	h.setRole("maint", "write")

	got := Check(context.Background(), h.gate, issueCommentInput(900, "maint"))
	require.True(t, got.Authorized)
	assert.Equal(t, "ok_to_test", got.Reason)
}

func TestCheckIssueComment_CommentIdentityMismatch(t *testing.T) {
	h := newHarness()
	h.setHeadSHA(testHeadSHA)
	h.putComment(900, testPR, "someone-else", "/ok-to-test "+testHeadSHA)
	h.setRole("maint", "write")

	got := Check(context.Background(), h.gate, issueCommentInput(900, "maint"))
	assert.Equal(t, "error", got.Reason)
	assert.Error(t, got.Err)
}

func TestCheckIssueComment_WrongIssue(t *testing.T) {
	h := newHarness()
	h.setHeadSHA(testHeadSHA)
	h.putComment(900, 43, "maint", "/ok-to-test "+testHeadSHA)
	h.setRole("maint", "write")

	got := Check(context.Background(), h.gate, issueCommentInput(900, "maint"))
	assert.Equal(t, "error", got.Reason)
}

func TestCheckIssueComment_MissingCommentID(t *testing.T) {
	h := newHarness()
	in := issueCommentInput(0, "maint")
	got := Check(context.Background(), h.gate, in)
	assert.Equal(t, "error", got.Reason)
	assert.Error(t, got.Err)
}

func TestCheckIssueComment_InfersCommenterFromAPI(t *testing.T) {
	h := newHarness()
	h.setHeadSHA(testHeadSHA)
	h.putComment(900, testPR, "maint", "/ok-to-test "+testHeadSHA)
	h.setRole("maint", "write")

	in := issueCommentInput(900, "")
	got := Check(context.Background(), h.gate, in)
	assert.True(t, got.Authorized)
}

func TestParseOkToTestNamedSHA(t *testing.T) {
	tests := []struct {
		body string
		want string
	}{
		{"/ok-to-test " + testHeadSHA, testHeadSHA},
		{"/ok-to-test   " + testHeadSHA + "   ", testHeadSHA},
		{"/ok-to-test " + testHeadSHA + "\r\nmore", testHeadSHA},
		{"/ok-to-test", ""},
		{"/ok-to-test " + testHeadSHA[:7], ""},
		{"/ok-to-test\n" + testHeadSHA, ""},
		{"/ok-to-test" + testHeadSHA[:7], ""},
	}
	for _, tc := range tests {
		got := parseOkToTestNamedSHA(tc.body)
		if tc.want == "" {
			assert.Empty(t, got, tc.body)
		} else {
			assert.Equal(t, strings.ToLower(tc.want), got, tc.body)
		}
	}
}
