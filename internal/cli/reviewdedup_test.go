package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestReviewDedupHelpers(t *testing.T) {
	owner, repo, err := splitReviewRepo("owner/repo")
	require.NoError(t, err)
	assert.Equal(t, "owner", owner)
	assert.Equal(t, "repo", repo)
	_, _, err = splitReviewRepo("invalid")
	assert.Error(t, err)

	var stdout, stderr bytes.Buffer
	cmd := newReviewDedupCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	writeReviewDedupOutput(cmd, reviewDedupResult{Skip: true, PriorURL: "https://example.test/review"})
	assert.Contains(t, stdout.String(), "prior_url=https://example.test/review")
	assert.Contains(t, stdout.String(), "skip=true")
}

func TestReviewDedupCommandValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "invalid repository", args: []string{"--repo", "invalid", "--pr", "1"}},
		{name: "non-positive pull request", args: []string{"--repo", "o/r", "--pr", "0"}},
		{name: "unsupported forge", args: []string{"--repo", "o/r", "--pr", "1", "--forge", "unsupported"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newReviewDedupCmd()
			cmd.SetArgs(tt.args)
			assert.Error(t, cmd.Execute())
		})
	}
}

func TestPublishReviewSkip_CreatesAndUpdatesVerifiedNote(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-ai-review[bot]"
	fc.IssueComments = map[string][]forge.IssueComment{}
	require.NoError(t, publishReviewSkip(ctx, fc, "o", "r", 1, "https://example.test/review", "client"))
	require.Len(t, fc.IssueComments["o/r/1"], 1)
	assert.Contains(t, fc.IssueComments["o/r/1"][0].Body, "unchanged")

	fc.IssueComments["o/r/1"][0].AppClientID = "client"
	require.NoError(t, publishReviewSkip(ctx, fc, "o", "r", 1, "", "client"))
	require.Len(t, fc.IssueComments["o/r/1"], 1)
	assert.NotContains(t, fc.IssueComments["o/r/1"][0].Body, "https://example.test/review")
}

func TestPublishReviewSkip_UpdatesNewestVerifiedNote(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-ai-review[bot]"
	fc.IssueComments = map[string][]forge.IssueComment{
		"o/r/1": {
			{ID: 10, Author: "fullsend-ai-review[bot]", AppClientID: "client", CreatedAt: "2026-01-01T00:00:00Z", Body: reviewSkipMarker + "\nold"},
			{ID: 11, Author: "fullsend-ai-review[bot]", AppClientID: "client", CreatedAt: "2026-01-02T00:00:00Z", Body: reviewSkipMarker + "\nnewest"},
		},
	}

	require.NoError(t, publishReviewSkip(context.Background(), fc, "o", "r", 1, "", "client"))
	require.Len(t, fc.UpdatedComments, 1)
	assert.Equal(t, 11, fc.UpdatedComments[0].CommentID)
	assert.Contains(t, fc.IssueComments["o/r/1"][1].Body, "unchanged")
	assert.Contains(t, fc.IssueComments["o/r/1"][0].Body, "old")
}

type reviewDedupInfoSequenceClient struct {
	*forge.FakeClient
	infos []forge.PullRequestInfo
	index int
}

func (c *reviewDedupInfoSequenceClient) GetPullRequestInfo(_ context.Context, _, _ string, _ int) (*forge.PullRequestInfo, error) {
	info := c.infos[c.index]
	if c.index < len(c.infos)-1 {
		c.index++
	}
	return &info, nil
}

func TestReviewHashAndHead_IgnoresStickyHistory(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	head := "0123456789012345678901234567890123456789"
	body := "<!-- fullsend:review-agent -->\n" +
		"<!-- fullsend:review-diff-hash:" + hash + " -->\n" +
		"<!-- fullsend:review-head:" + head + " -->\nfindings\n" +
		"<details><summary>Previous run</summary><!-- sticky:history-start -->\n" +
		"<!-- fullsend:review-diff-hash:" + "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" + " -->\n<!-- sticky:history-end --></details>"

	gotHash, gotHead := reviewHashAndHead(body)
	assert.Equal(t, hash, gotHash)
	assert.Equal(t, head, gotHead)
}

func TestDecideReviewDedup_SkipsMatchingApprovedReview(t *testing.T) {
	const head = "0123456789012345678901234567890123456789"
	const base = "abcdefabcdefabcdefabcdefabcdefabcdefabcdef"
	fc := forge.NewFakeClient()
	fc.PullRequestInfos = map[string]forge.PullRequestInfo{}
	fc.PRFileDiffs = map[string][]forge.PullRequestFileDiff{}
	fc.IssueComments = map[string][]forge.IssueComment{}
	fc.PRReviews = map[string][]forge.PullRequestReview{}
	fc.PullRequestInfos["o/r/1"] = forge.PullRequestInfo{Number: 1, BaseSHA: base, HeadSHA: head, ChangedFiles: 1}
	fc.PRFileDiffs["o/r/1"] = []forge.PullRequestFileDiff{{
		Path: "file.go", Status: "modified", Patch: "@@ -1 +1 @@\n-old\n+new", OldMode: "100644", NewMode: "100644", ModesVerified: true,
	}}
	fc.IssueComments["o/r/1"] = []forge.IssueComment{{
		HTMLURL: "https://github.com/o/r/issues/1#issuecomment-1", Author: "fullsend-ai-review[bot]",
		AppClientID: "client", CreatedAt: "2026-01-01T00:00:00Z",
		Body: "<!-- fullsend:review-agent -->\n<!-- fullsend:review-diff-hash:" + "" + " -->",
	}}
	// Fill the hash from the same canonicalizer used by post-review.
	diffs, err := fc.ListPullRequestFileDiffs(context.Background(), "o", "r", 1)
	require.NoError(t, err)
	hash, err := pullRequestDiffHashFromDiffs(diffs, nil)
	require.NoError(t, err)
	fc.IssueComments["o/r/1"][0].Body = "<!-- fullsend:review-agent -->\n<!-- fullsend:review-diff-hash:" + hash + " -->\n<!-- fullsend:review-head:" + head + " -->"
	fc.PRReviews["o/r/1"] = []forge.PullRequestReview{{User: "fullsend-ai-review[bot]", State: "APPROVED", CommitID: head, SubmittedAt: "2026-01-01T00:00:01Z"}}

	client := &reviewDedupInfoSequenceClient{FakeClient: fc, infos: []forge.PullRequestInfo{
		{Number: 1, BaseSHA: base, HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ChangedFiles: 1},
		{Number: 1, BaseSHA: base, HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ChangedFiles: 1},
		{Number: 1, BaseSHA: base, HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ChangedFiles: 1},
	}}
	result := decideReviewDedup(context.Background(), client, "o", "r", 1, "fullsend-ai", "client")
	assert.True(t, result.Skip)
	assert.Equal(t, "https://github.com/o/r/issues/1#issuecomment-1", result.PriorURL)

	client.index = 0
	client.infos[2].HeadSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	result = decideReviewDedup(context.Background(), client, "o", "r", 1, "fullsend-ai", "client")
	assert.False(t, result.Skip, "a head change before the final check must fail open")
}

func TestHasApprovedReview_UsesLatestBotReviewWithoutAppMetadata(t *testing.T) {
	const head = "0123456789012345678901234567890123456789"
	reviews := []forge.PullRequestReview{
		{User: "fullsend-ai-review[bot]", State: "APPROVED", CommitID: head, SubmittedAt: "2026-01-01T00:00:01Z"},
		{User: "fullsend-ai-review[bot]", State: "CHANGES_REQUESTED", CommitID: head, SubmittedAt: "2026-01-01T00:00:01Z"},
	}
	assert.False(t, hasApprovedReview(reviews, "fullsend-ai", "fullsend-ai", head))
	reviews[1].State = "APPROVED"
	assert.True(t, hasApprovedReview(reviews, "fullsend-ai", "fullsend-ai", head))
}

func TestDecideReviewDedup_FailsOpenWhenModesAreUnverified(t *testing.T) {
	const head = "0123456789012345678901234567890123456789"
	fc := forge.NewFakeClient()
	fc.PullRequestInfos = map[string]forge.PullRequestInfo{}
	fc.PRFileDiffs = map[string][]forge.PullRequestFileDiff{}
	fc.PRFileDiffModes = map[string]bool{"o/r/1": false}
	fc.PullRequestInfos["o/r/1"] = forge.PullRequestInfo{Number: 1, BaseSHA: "base", HeadSHA: head, ChangedFiles: 1}
	fc.PRFileDiffs["o/r/1"] = []forge.PullRequestFileDiff{{Path: "file.go", Status: "modified", Patch: "@@ -1 +1 @@", ModesVerified: false}}

	result := decideReviewDedup(context.Background(), fc, "o", "r", 1, "fullsend-ai", "client")
	assert.False(t, result.Skip)
}

func newValidReviewDedupFixture(t *testing.T) (*forge.FakeClient, string, string) {
	t.Helper()
	const head = "0123456789012345678901234567890123456789"
	const base = "abcdefabcdefabcdefabcdefabcdefabcdefabcdef"
	fc := forge.NewFakeClient()
	fc.PullRequestInfos = map[string]forge.PullRequestInfo{
		"o/r/1": {Number: 1, BaseSHA: base, HeadSHA: head, ChangedFiles: 1},
	}
	fc.PRFileDiffs = map[string][]forge.PullRequestFileDiff{
		"o/r/1": {{Path: "file.go", Status: "modified", Patch: "@@ -1 +1 @@\n-old\n+new", OldMode: "100644", NewMode: "100644", ModesVerified: true}},
	}
	diffs, err := fc.ListPullRequestFileDiffs(context.Background(), "o", "r", 1)
	require.NoError(t, err)
	hash, err := pullRequestDiffHashFromDiffs(diffs, nil)
	require.NoError(t, err)
	fc.IssueComments = map[string][]forge.IssueComment{"o/r/1": {{
		HTMLURL: "https://example.test/review", Author: "fullsend-ai-review[bot]", AppClientID: "client",
		CreatedAt: "2026-01-01T00:00:00Z", Body: "<!-- fullsend:review-agent -->\n<!-- fullsend:review-diff-hash:" + hash + " -->\n<!-- fullsend:review-head:" + head + " -->",
	}}}
	fc.PRReviews = map[string][]forge.PullRequestReview{"o/r/1": {{
		User: "fullsend-ai-review[bot]", State: "APPROVED", CommitID: head, SubmittedAt: "2026-01-01T00:00:01Z",
	}}}
	return fc, base, head
}

func TestDecideReviewDedup_FailsOpenForIncompleteInputs(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*forge.FakeClient)
	}{
		{name: "pull request lookup error", setup: func(fc *forge.FakeClient) { fc.Errors["GetPullRequestInfo"] = assert.AnError }},
		{name: "diff lookup error", setup: func(fc *forge.FakeClient) { fc.Errors["ListPullRequestFileDiffs"] = assert.AnError }},
		{name: "diff count mismatch", setup: func(fc *forge.FakeClient) {
			fc.PullRequestInfos["o/r/1"] = forge.PullRequestInfo{Number: 1, BaseSHA: "base", HeadSHA: "head", ChangedFiles: 2}
		}},
		{name: "comments lookup error", setup: func(fc *forge.FakeClient) { fc.Errors["ListIssueComments"] = assert.AnError }},
		{name: "reviews lookup error", setup: func(fc *forge.FakeClient) { fc.Errors["ListPullRequestReviews"] = assert.AnError }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fc, _, _ := newValidReviewDedupFixture(t)
			tc.setup(fc)
			assert.False(t, decideReviewDedup(context.Background(), fc, "o", "r", 1, "fullsend-ai", "client").Skip)
		})
	}
}

func TestPublishReviewSkip_FailsOpenOnPublicationErrors(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-ai-review[bot]"
	fc.Errors = map[string]error{"ListIssueComments": assert.AnError}
	assert.Error(t, publishReviewSkip(context.Background(), fc, "o", "r", 1, "", "client"))

	fc.Errors = nil
	fc.AuthenticatedUser = ""
	assert.Error(t, publishReviewSkip(context.Background(), fc, "o", "r", 1, "", "client"))
}

func TestSupersedeReviewSkip_UpdatesOnlyVerifiedBotNotes(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.IssueComments = map[string][]forge.IssueComment{
		"o/r/1": {
			{ID: 1, Author: "o-review[bot]", AppClientID: "client", Body: reviewSkipMarker + "\nold"},
			{ID: 2, Author: "o-review[bot]", AppClientID: "other", Body: reviewSkipMarker + "\nleave me alone"},
			{ID: 3, Author: "human", AppClientID: "client", Body: reviewSkipMarker + "\nleave me alone"},
		},
	}

	require.NoError(t, supersedeReviewSkip(context.Background(), fc, "o", "r", 1,
		"0123456789012345678901234567890123456789", "fullsend-ai", "client"))

	comments := fc.IssueComments["o/r/1"]
	assert.Contains(t, comments[0].Body, "superseded by a full review")
	assert.Equal(t, reviewSkipMarker+"\nleave me alone", comments[1].Body)
	assert.Equal(t, reviewSkipMarker+"\nleave me alone", comments[2].Body)
}

func TestSupersedeReviewSkip_WithoutClientIDIsNoop(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{"ListIssueComments": assert.AnError}

	require.NoError(t, supersedeReviewSkip(context.Background(), fc, "o", "r", 1,
		"0123456789012345678901234567890123456789", "fullsend-ai", ""))
	assert.Empty(t, fc.UpdatedComments)
}
