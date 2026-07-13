package layers

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

var testFiles = []forge.TreeFile{
	{Path: ".github/workflows/ci.yml", Content: []byte("ci"), Mode: "100644"},
}

func newTestPrinter() (*ui.Printer, *bytes.Buffer) {
	var buf bytes.Buffer
	return ui.New(&buf), &buf
}

func TestCommitScaffoldViaPR_OwnerPushesDirect(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	printer, _ := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", "msg", "title", "body", testFiles, false, nil)
	require.NoError(t, err)

	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "acme/widget/fullsend/scaffold-install", client.CreatedBranches[0])

	require.Len(t, client.CreatedProposals, 1)
	assert.Equal(t, "fullsend/scaffold-install", client.CreatedProposals[0].Head)
	assert.Equal(t, "main", client.CreatedProposals[0].Base)
}

func TestCommitScaffoldViaPR_OwnerCaseInsensitive(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "Acme"
	printer, _ := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", "msg", "title", "body", testFiles, false, nil)
	require.NoError(t, err)

	// Should push to acme/widget directly (same-repo PR).
	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "acme/widget/fullsend/scaffold-install", client.CreatedBranches[0])
	assert.Empty(t, client.CreatedForks)
}

func TestCommitScaffoldViaPR_ExistingForkReused(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.ExistingForks = map[string]string{
		"acme/widget": "contributor",
	}
	printer, buf := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", "msg", "title", "body", testFiles, false, nil)
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "Using existing fork")
	assert.Empty(t, client.CreatedForks, "should not create a new fork")

	// Branch created on fork, not upstream.
	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "contributor/widget/fullsend/scaffold-install", client.CreatedBranches[0])

	// PR should be cross-fork.
	require.Len(t, client.CreatedProposals, 1)
}

func TestCommitScaffoldViaPR_NonInteractiveForksByDefault(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.TokenScopes = []string{"repo", "workflow"}
	client.Repos = append(client.Repos, forge.Repository{
		FullName: "contributor/widget", DefaultBranch: "main",
	})
	printer, buf := newTestPrinter()

	// nil reader = non-interactive → auto-fork.
	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", "msg", "title", "body", testFiles, false, nil)
	require.NoError(t, err)

	require.Len(t, client.CreatedForks, 1)
	assert.Equal(t, "acme/widget", client.CreatedForks[0])
	assert.Contains(t, buf.String(), "Non-interactive mode")
	assert.Contains(t, buf.String(), "Fork created")
}

func TestCommitScaffoldViaPR_InteractiveForkChoice(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.TokenScopes = []string{"repo", "workflow"}
	client.Repos = append(client.Repos, forge.Repository{
		FullName: "contributor/widget", DefaultBranch: "main",
	})
	printer, _ := newTestPrinter()

	// Simulate user pressing enter (default = fork).
	in := strings.NewReader("\n")
	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", "msg", "title", "body", testFiles, false, in)
	require.NoError(t, err)

	require.Len(t, client.CreatedForks, 1)
}

func TestCommitScaffoldViaPR_InteractiveUpstreamChoice(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.TokenScopes = []string{"repo", "workflow"}
	printer, _ := newTestPrinter()

	// Simulate user choosing upstream.
	in := strings.NewReader("u\n")
	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", "msg", "title", "body", testFiles, false, in)
	require.NoError(t, err)

	assert.Empty(t, client.CreatedForks, "should not fork")
	// Branch created on upstream.
	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "acme/widget/fullsend/scaffold-install", client.CreatedBranches[0])
}

func TestCommitScaffoldViaPR_UpstreamForbidden(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.TokenScopes = []string{"repo", "workflow"}
	client.CreateBranchErrors = map[string]error{
		"acme/widget": fmt.Errorf("API error: %w", forge.ErrForbidden),
	}
	printer, _ := newTestPrinter()

	in := strings.NewReader("u\n")
	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", "msg", "title", "body", testFiles, false, in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403 forbidden")
	assert.Contains(t, err.Error(), "fork option")
}

func TestCommitScaffoldViaPR_CrossForkPRHead(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.ExistingForks = map[string]string{
		"acme/widget": "contributor",
	}
	printer, _ := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", "msg", "title", "body", testFiles, false, nil)
	require.NoError(t, err)

	// Verify the PR was created on upstream with cross-fork head.
	require.Len(t, client.CreatedProposals, 1)
	assert.Equal(t, "contributor:fullsend/scaffold-install", client.CreatedProposals[0].Head)
	assert.Equal(t, "main", client.CreatedProposals[0].Base)
	// CommitFilesToBranch is called on the fork.
	require.Len(t, client.CommittedFilesToBranch, 1)
	assert.Equal(t, "contributor", client.CommittedFilesToBranch[0].Owner)
}

func TestCommitScaffoldViaPR_FindExistingForkError(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.TokenScopes = []string{"repo", "workflow"}
	client.Errors = map[string]error{
		"FindExistingFork": fmt.Errorf("API error"),
	}
	client.Repos = append(client.Repos, forge.Repository{
		FullName: "contributor/widget", DefaultBranch: "main",
	})
	printer, buf := newTestPrinter()

	// Should warn but proceed (auto-fork since in=nil).
	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", "msg", "title", "body", testFiles, false, nil)
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "Could not check for existing fork")
	require.Len(t, client.CreatedForks, 1)
}

func TestCommitScaffoldViaPR_CreateForkError(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.TokenScopes = []string{"repo", "workflow"}
	client.Errors = map[string]error{
		"CreateFork": fmt.Errorf("rate limited"),
	}
	printer, _ := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", "msg", "title", "body", testFiles, false, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating fork")
}

func TestCommitScaffoldViaPR_GetAuthenticatedUserError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors = map[string]error{
		"GetAuthenticatedUser": fmt.Errorf("token expired"),
	}
	printer, _ := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", "msg", "title", "body", testFiles, false, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting authenticated user")
}

func TestCommitScaffoldDirect_FallbackPreservesIn(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Errors = map[string]error{
		"CommitFiles": fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected),
	}
	printer, buf := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", "msg", "title", "body", testFiles, true, nil)
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "protected")
	// Should have fallen back to PR mode as owner → same-repo PR.
	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "acme/widget/fullsend/scaffold-install", client.CreatedBranches[0])
}

func TestCommitScaffoldDirect_NonFastForwardRetrySucceeds(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.CommitFilesErrSeq = []error{
		fmt.Errorf("%w: not a fast forward", forge.ErrNonFastForward),
	}
	printer, buf := newTestPrinter()

	committed, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", "msg", "title", "body", testFiles, true, nil)
	require.NoError(t, err)
	assert.True(t, committed)
	assert.Contains(t, buf.String(), "auto_init race")
	assert.Len(t, client.CommittedFiles, 1, "retry call should succeed and record")
}

func TestCommitScaffoldDirect_NonFastForwardRetryFails(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.CommitFilesErrSeq = []error{
		fmt.Errorf("%w: not a fast forward", forge.ErrNonFastForward),
		fmt.Errorf("network error"),
	}
	printer, _ := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", "msg", "title", "body", testFiles, true, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network error")
}

func TestCommitScaffoldViaPR_FineGrainedSkipsFork_Interactive(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	// TokenScopes nil = fine-grained PAT
	printer, buf := newTestPrinter()

	// Simulate user confirming upstream.
	in := strings.NewReader("y\n")
	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", "msg", "title", "body", testFiles, false, in)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Fine-grained token detected")
	assert.Contains(t, output, "fork option is not available")
	assert.Contains(t, output, "scaffolding files")
	assert.Empty(t, client.CreatedForks, "should not attempt to fork")
	// Branch created on upstream.
	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "acme/widget/fullsend/scaffold-install", client.CreatedBranches[0])
}

func TestCommitScaffoldViaPR_FineGrainedDeclined(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	printer, _ := newTestPrinter()

	in := strings.NewReader("n\n")
	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", "msg", "title", "body", testFiles, false, in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream delivery declined")
}

func TestCommitScaffoldViaPR_FineGrainedNonInteractive(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	printer, buf := newTestPrinter()

	// nil reader = non-interactive → should auto-upstream (not fork).
	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", "msg", "title", "body", testFiles, false, nil)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "fine-grained token detected")
	assert.Contains(t, output, "pushing to upstream")
	assert.Empty(t, client.CreatedForks, "should not attempt to fork")
	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "acme/widget/fullsend/scaffold-install", client.CreatedBranches[0])
}

func TestPromptUpstreamOnly_Confirm(t *testing.T) {
	printer, buf := newTestPrinter()
	in := strings.NewReader("y\n")
	confirmed, err := promptUpstreamOnly(printer, in, "acme", "widget")
	require.NoError(t, err)
	assert.True(t, confirmed)
	assert.Contains(t, buf.String(), "acme/widget")
	assert.Contains(t, buf.String(), "scaffolding files")
}

func TestPromptUpstreamOnly_Decline(t *testing.T) {
	printer, _ := newTestPrinter()
	in := strings.NewReader("n\n")
	confirmed, err := promptUpstreamOnly(printer, in, "acme", "widget")
	require.NoError(t, err)
	assert.False(t, confirmed)
}

func TestPromptUpstreamOnly_InvalidThenConfirm(t *testing.T) {
	printer, _ := newTestPrinter()
	in := strings.NewReader("x\ny\n")
	confirmed, err := promptUpstreamOnly(printer, in, "acme", "widget")
	require.NoError(t, err)
	assert.True(t, confirmed)
}

func TestPromptUpstreamOnly_MaxRetries(t *testing.T) {
	printer, _ := newTestPrinter()
	in := strings.NewReader("x\nx\nx\nx\nx\n")
	_, err := promptUpstreamOnly(printer, in, "acme", "widget")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too many invalid attempts")
}

func TestIsFineGrainedToken(t *testing.T) {
	t.Run("nil scopes = fine-grained", func(t *testing.T) {
		client := forge.NewFakeClient()
		fg, err := isFineGrainedToken(context.Background(), client)
		require.NoError(t, err)
		assert.True(t, fg)
	})

	t.Run("non-nil scopes = classic PAT", func(t *testing.T) {
		client := forge.NewFakeClient()
		client.TokenScopes = []string{"repo", "workflow"}
		fg, err := isFineGrainedToken(context.Background(), client)
		require.NoError(t, err)
		assert.False(t, fg)
	})

	t.Run("installation token = not fine-grained", func(t *testing.T) {
		client := forge.NewFakeClient()
		client.InstallationToken = true
		fg, err := isFineGrainedToken(context.Background(), client)
		require.NoError(t, err)
		assert.False(t, fg)
	})
}

func TestPromptForkChoice_DefaultIsFork(t *testing.T) {
	printer, _ := newTestPrinter()
	in := strings.NewReader("\n")
	choice, err := promptForkChoice(printer, in)
	require.NoError(t, err)
	assert.True(t, choice, "empty input should default to fork")
}

func TestPromptForkChoice_ExplicitFork(t *testing.T) {
	printer, _ := newTestPrinter()
	in := strings.NewReader("f\n")
	choice, err := promptForkChoice(printer, in)
	require.NoError(t, err)
	assert.True(t, choice)
}

func TestPromptForkChoice_Upstream(t *testing.T) {
	printer, _ := newTestPrinter()
	in := strings.NewReader("u\n")
	choice, err := promptForkChoice(printer, in)
	require.NoError(t, err)
	assert.False(t, choice, "u should select upstream")
}

func TestPromptForkChoice_InvalidThenValid(t *testing.T) {
	printer, _ := newTestPrinter()
	in := strings.NewReader("x\nf\n")
	choice, err := promptForkChoice(printer, in)
	require.NoError(t, err)
	assert.True(t, choice)
}

func TestWaitForFork_FailsOnNonNotFoundError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors = map[string]error{
		"GetRepo": fmt.Errorf("authentication failed"),
	}
	printer, _ := newTestPrinter()

	err := waitForFork(context.Background(), client, printer, "contributor", "widget")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication failed")
}

func TestPromptForkChoice_EOFWithPartialData(t *testing.T) {
	printer, _ := newTestPrinter()
	in := strings.NewReader("u")
	choice, err := promptForkChoice(printer, in)
	require.NoError(t, err)
	assert.False(t, choice, "partial 'u' before EOF should select upstream")
}

func TestPromptForkChoice_MaxRetries(t *testing.T) {
	printer, _ := newTestPrinter()
	in := strings.NewReader("x\nx\nx\nx\nx\n")
	_, err := promptForkChoice(printer, in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too many invalid attempts")
}
