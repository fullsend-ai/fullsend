package cli

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newTestPrinter() *ui.Printer {
	return ui.New(&discardWriter{})
}

type discardWriter struct{}

func (d *discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func validConfig(client forge.Client) gateCodeConfig {
	return gateCodeConfig{
		IssueNumber:  "42",
		RepoFullName: "test-org/test-repo",
		IssueURL:     "https://github.com/test-org/test-repo/issues/42",
		Client:       client,
	}
}

func TestValidateGateCodeInputs_Valid(t *testing.T) {
	errs := validateGateCodeInputs("42", "test-org/test-repo", "https://github.com/test-org/test-repo/issues/42")
	assert.Empty(t, errs)
}

func TestValidateGateCodeInputs_InvalidNumber(t *testing.T) {
	errs := validateGateCodeInputs("0", "test-org/test-repo", "https://github.com/test-org/test-repo/issues/42")
	assert.Len(t, errs, 2) // invalid number + cross-validation mismatch
}

func TestValidateGateCodeInputs_InvalidRepo(t *testing.T) {
	errs := validateGateCodeInputs("42", "bad repo!", "https://github.com/test-org/test-repo/issues/42")
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0], "REPO_FULL_NAME")
}

func TestValidateGateCodeInputs_InvalidURL(t *testing.T) {
	errs := validateGateCodeInputs("42", "test-org/test-repo", "http://evil.com/issues/42")
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0], "GITHUB_ISSUE_URL")
}

func TestValidateGateCodeInputs_CrossValidationMismatch(t *testing.T) {
	errs := validateGateCodeInputs("42", "other-org/other-repo", "https://github.com/test-org/test-repo/issues/42")
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0], "does not match")
}

func TestValidateGateCodeInputs_MultipleErrors(t *testing.T) {
	errs := validateGateCodeInputs("", "", "")
	assert.True(t, len(errs) >= 3, "expected at least 3 errors, got %d", len(errs))
}

func TestRunGateCode_NoToken(t *testing.T) {
	cfg := validConfig(nil)
	err := runGateCode(context.Background(), cfg, newTestPrinter())
	assert.NoError(t, err)
}

func TestRunGateCode_ForceOverride(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.TimelineEvents = map[string][]forge.TimelineEvent{
		"test-org/test-repo/42": {
			{PRNumber: 99, PRState: "open", PRAuthor: "human-dev", PRURL: "https://github.com/test-org/test-repo/pull/99"},
		},
	}
	cfg := validConfig(fc)
	cfg.Force = true
	err := runGateCode(context.Background(), cfg, newTestPrinter())
	assert.NoError(t, err)
	assert.Empty(t, fc.AddedComments)
}

func TestRunGateCode_HumanPRFound_WritesSkipOutput(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.TimelineEvents = map[string][]forge.TimelineEvent{
		"test-org/test-repo/42": {
			{PRNumber: 99, PRState: "open", PRAuthor: "human-dev", PRURL: "https://github.com/test-org/test-repo/pull/99"},
		},
	}
	outFile := t.TempDir() + "/github-output"
	cfg := validConfig(fc)
	cfg.OutputFile = outFile
	err := runGateCode(context.Background(), cfg, newTestPrinter())
	assert.NoError(t, err)
	data, err := os.ReadFile(outFile)
	require.NoError(t, err)
	assert.Contains(t, string(data), "skip=true")
}

func TestRunGateCode_NoExistingPRs_NoSkipOutput(t *testing.T) {
	fc := forge.NewFakeClient()
	outFile := t.TempDir() + "/github-output"
	cfg := validConfig(fc)
	cfg.OutputFile = outFile
	err := runGateCode(context.Background(), cfg, newTestPrinter())
	assert.NoError(t, err)
	_, err = os.ReadFile(outFile)
	assert.True(t, os.IsNotExist(err), "output file should not exist when no PRs found")
}

func TestRunGateCode_HumanPRFound_AppliesLabel(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.TimelineEvents = map[string][]forge.TimelineEvent{
		"test-org/test-repo/42": {
			{PRNumber: 99, PRState: "open", PRAuthor: "human-dev", PRURL: "https://github.com/test-org/test-repo/pull/99"},
		},
	}
	cfg := validConfig(fc)
	err := runGateCode(context.Background(), cfg, newTestPrinter())
	assert.NoError(t, err)
	require.Len(t, fc.EnsuredLabels, 1)
	assert.Equal(t, []string{"pr-open"}, fc.EnsuredLabels[0].Labels)
	require.Len(t, fc.AddedIssueLabels, 1)
	assert.Equal(t, []string{"pr-open"}, fc.AddedIssueLabels[0].Labels)
	assert.Equal(t, 42, fc.AddedIssueLabels[0].Number)
}

func TestRunGateCode_HumanPRFound_PostsComment(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.TimelineEvents = map[string][]forge.TimelineEvent{
		"test-org/test-repo/42": {
			{PRNumber: 99, PRState: "open", PRAuthor: "human-dev", PRURL: "https://github.com/test-org/test-repo/pull/99"},
		},
	}
	cfg := validConfig(fc)
	err := runGateCode(context.Background(), cfg, newTestPrinter())
	assert.NoError(t, err)
	require.Len(t, fc.AddedComments, 1)
	assert.Contains(t, fc.AddedComments[0].Body, "An open PR already addresses")
	assert.Contains(t, fc.AddedComments[0].Body, "#99 by @human-dev")
}

func TestRunGateCode_BotPRFiltered(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.TimelineEvents = map[string][]forge.TimelineEvent{
		"test-org/test-repo/42": {
			{PRNumber: 99, PRState: "open", PRAuthor: "fullsend-ai[bot]", PRURL: "https://github.com/test-org/test-repo/pull/99"},
		},
	}
	cfg := validConfig(fc)
	err := runGateCode(context.Background(), cfg, newTestPrinter())
	assert.NoError(t, err)
	assert.Empty(t, fc.AddedComments)
	assert.Empty(t, fc.EnsuredLabels)
}

func TestRunGateCode_MultiplePRs(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.TimelineEvents = map[string][]forge.TimelineEvent{
		"test-org/test-repo/42": {
			{PRNumber: 50, PRState: "open", PRAuthor: "dev-a", PRURL: "https://github.com/test-org/test-repo/pull/50"},
			{PRNumber: 51, PRState: "open", PRAuthor: "dev-b", PRURL: "https://github.com/test-org/test-repo/pull/51"},
		},
	}
	cfg := validConfig(fc)
	err := runGateCode(context.Background(), cfg, newTestPrinter())
	assert.NoError(t, err)
	require.Len(t, fc.AddedComments, 1)
	assert.Contains(t, fc.AddedComments[0].Body, "#50 by @dev-a")
	assert.Contains(t, fc.AddedComments[0].Body, "#51 by @dev-b")
}

func TestRunGateCode_CommentIdempotency_SkipsDuplicate(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.TimelineEvents = map[string][]forge.TimelineEvent{
		"test-org/test-repo/42": {
			{PRNumber: 99, PRState: "open", PRAuthor: "human-dev", PRURL: "https://github.com/test-org/test-repo/pull/99"},
		},
	}
	fc.IssueComments = map[string][]forge.IssueComment{
		"test-org/test-repo/42": {
			{ID: 1, Body: "An open PR already addresses this issue — skipping automated implementation.", Author: "fullsend-ai[bot]"},
		},
	}
	cfg := validConfig(fc)
	err := runGateCode(context.Background(), cfg, newTestPrinter())
	assert.NoError(t, err)
	assert.Empty(t, fc.AddedComments)
}

func TestRunGateCode_CommentIdempotency_PostsNew(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.TimelineEvents = map[string][]forge.TimelineEvent{
		"test-org/test-repo/42": {
			{PRNumber: 99, PRState: "open", PRAuthor: "human-dev", PRURL: "https://github.com/test-org/test-repo/pull/99"},
		},
	}
	fc.IssueComments = map[string][]forge.IssueComment{
		"test-org/test-repo/42": {},
	}
	cfg := validConfig(fc)
	err := runGateCode(context.Background(), cfg, newTestPrinter())
	assert.NoError(t, err)
	require.Len(t, fc.AddedComments, 1)
}

func TestRunGateCode_TimelineAPIFailure(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["ListIssueTimeline"] = fmt.Errorf("API error")
	cfg := validConfig(fc)
	err := runGateCode(context.Background(), cfg, newTestPrinter())
	assert.NoError(t, err)
}

func TestRunGateCode_InvalidBotLogin(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := validConfig(fc)
	cfg.BotLogin = "evil$(whoami)"
	err := runGateCode(context.Background(), cfg, newTestPrinter())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid characters")
}

func TestRunGateCode_ValidCustomBotLogin(t *testing.T) {
	fc := forge.NewFakeClient()
	cfg := validConfig(fc)
	cfg.BotLogin = "my-bot[bot]"
	err := runGateCode(context.Background(), cfg, newTestPrinter())
	assert.NoError(t, err)
}

func TestRunGateCode_ClosedPRIgnored(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.TimelineEvents = map[string][]forge.TimelineEvent{
		"test-org/test-repo/42": {
			{PRNumber: 99, PRState: "closed", PRAuthor: "human-dev", PRURL: "https://github.com/test-org/test-repo/pull/99"},
		},
	}
	cfg := validConfig(fc)
	err := runGateCode(context.Background(), cfg, newTestPrinter())
	assert.NoError(t, err)
	assert.Empty(t, fc.AddedComments)
}

func TestRunGateCode_ValidationFailure(t *testing.T) {
	cfg := gateCodeConfig{
		IssueNumber:  "bad",
		RepoFullName: "bad!",
		IssueURL:     "bad",
	}
	err := runGateCode(context.Background(), cfg, newTestPrinter())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "validation failed")
}
