package layers

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

var testFiles = []forge.TreeFile{
	{Path: ".github/workflows/ci.yml", Content: []byte("ci"), Mode: "100644"},
}

func newTestPrinter() (*ui.Printer, *bytes.Buffer) {
	var buf bytes.Buffer
	return ui.New(&buf), &buf
}

// testMeta returns a ScaffoldPRMetadata with the given values and an optional
// branch name. Reduces boilerplate in tests that previously passed 4 separate
// string parameters.
func testMeta(commitMsg, prTitle, prBody string, branch ...string) repos.ScaffoldPRMetadata {
	m := repos.ScaffoldPRMetadata{
		CommitMsg: commitMsg,
		PRTitle:   prTitle,
		PRBody:    prBody,
	}
	if len(branch) > 0 {
		m.Branch = branch[0]
	}
	return m
}

func TestCommitScaffoldViaPR_OwnerPushesDirect(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	printer, _ := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
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
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
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
	client.BranchRefs["acme/widget/main"] = "upstream-sha"
	printer, buf := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "Using existing fork")
	assert.Empty(t, client.CreatedForks, "should not create a new fork")

	// Branch created on fork, not upstream.
	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "contributor/widget/fullsend/scaffold-install", client.CreatedBranches[0])

	// PR should be cross-fork.
	require.Len(t, client.CreatedProposals, 1)
}

func TestCommitScaffoldViaPR_WriteAccessPushesDirect(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.CollaboratorPermissions = map[string]string{
		"acme/widget/contributor": "write",
	}
	printer, buf := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "has write access")
	assert.Empty(t, client.CreatedForks, "should not fork when user has write access")
	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "acme/widget/fullsend/scaffold-install", client.CreatedBranches[0])
}

func TestCommitScaffoldViaPR_AdminAccessPushesDirect(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.CollaboratorPermissions = map[string]string{
		"acme/widget/contributor": "admin",
	}
	printer, _ := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.NoError(t, err)

	assert.Empty(t, client.CreatedForks)
	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "acme/widget/fullsend/scaffold-install", client.CreatedBranches[0])
}

func TestCommitScaffoldViaPR_MaintainAccessPushesDirect(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.CollaboratorPermissions = map[string]string{
		"acme/widget/contributor": "maintain",
	}
	printer, buf := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "has write access")
	assert.Empty(t, client.CreatedForks)
	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "acme/widget/fullsend/scaffold-install", client.CreatedBranches[0])
}

func TestCommitScaffoldViaPR_WriteAccessTakesPrecedenceOverFork(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.CollaboratorPermissions = map[string]string{
		"acme/widget/contributor": "write",
	}
	client.ExistingForks = map[string]string{
		"acme/widget": "contributor",
	}
	printer, buf := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "has write access")
	assert.NotContains(t, buf.String(), "Using existing fork")
	assert.Empty(t, client.CreatedForks)
	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "acme/widget/fullsend/scaffold-install", client.CreatedBranches[0],
		"write access should push to upstream, not the fork")
}

func TestCommitScaffoldViaPR_ReadAccessFallsThrough(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.TokenScopes = []string{"repo", "workflow"}
	client.CollaboratorPermissions = map[string]string{
		"acme/widget/contributor": "read",
	}
	client.Repos = append(client.Repos, forge.Repository{
		FullName: "contributor/widget", DefaultBranch: "main",
	})
	client.BranchRefs["acme/widget/main"] = "upstream-sha"
	printer, _ := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.NoError(t, err)

	require.Len(t, client.CreatedForks, 1, "read-only user should fork")
}

func TestCommitScaffoldViaPR_NonInteractiveForksByDefault(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.TokenScopes = []string{"repo", "workflow"}
	client.Repos = append(client.Repos, forge.Repository{
		FullName: "contributor/widget", DefaultBranch: "main",
	})
	client.BranchRefs["acme/widget/main"] = "upstream-sha"
	printer, buf := newTestPrinter()

	// nil reader = non-interactive → auto-fork.
	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
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
	client.BranchRefs["acme/widget/main"] = "upstream-sha"
	printer, _ := newTestPrinter()

	// Simulate user pressing enter (default = fork).
	in := strings.NewReader("\n")
	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, in)
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
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, in)
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
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, in)
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
	client.BranchRefs["acme/widget/main"] = "upstream-sha"
	printer, _ := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.NoError(t, err)

	// Verify the PR was created on upstream with cross-fork head.
	require.Len(t, client.CreatedProposals, 1)
	assert.Equal(t, "contributor:fullsend/scaffold-install", client.CreatedProposals[0].Head)
	assert.Equal(t, "main", client.CreatedProposals[0].Base)
	// CommitFilesToBranch is called on the fork.
	require.Len(t, client.CommittedFilesToBranch, 1)
	assert.Equal(t, "contributor", client.CommittedFilesToBranch[0].Owner)
}

func TestCommitScaffoldViaPR_CrossForkUsesUpstreamSHA(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.ExistingForks = map[string]string{
		"acme/widget": "contributor",
	}
	// Set the upstream branch ref so we can verify it is used.
	client.BranchRefs["acme/widget/main"] = "upstream-sha-abc123"
	printer, _ := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.NoError(t, err)

	// The scaffold branch on the fork must be created from the upstream SHA,
	// not from the fork's (potentially stale) default branch.
	require.Len(t, client.CreatedBranchSHAs, 1, "cross-fork should use CreateBranchFromSHA")
	assert.Equal(t, "contributor", client.CreatedBranchSHAs[0].Owner)
	assert.Equal(t, "widget", client.CreatedBranchSHAs[0].Repo)
	assert.Equal(t, "fullsend/scaffold-install", client.CreatedBranchSHAs[0].Branch)
	assert.Equal(t, "upstream-sha-abc123", client.CreatedBranchSHAs[0].SHA,
		"branch should be based on upstream HEAD, not fork's default branch")
}

func TestCommitScaffoldViaPR_SameRepoUsesCreateBranch(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	printer, _ := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.NoError(t, err)

	// Same-repo path should use CreateBranch (not CreateBranchFromSHA).
	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "acme/widget/fullsend/scaffold-install", client.CreatedBranches[0])
	assert.Empty(t, client.CreatedBranchSHAs, "same-repo should not use CreateBranchFromSHA")
}

func TestCommitScaffoldViaPR_CrossForkCreateBranchFromSHAForbidden(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.ExistingForks = map[string]string{
		"acme/widget": "contributor",
	}
	client.BranchRefs["acme/widget/main"] = "upstream-sha"
	client.CreateBranchErrors = map[string]error{
		"contributor/widget": fmt.Errorf("API error: %w", forge.ErrForbidden),
	}
	printer, _ := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403 forbidden")
}

func TestCommitScaffoldViaPR_CrossForkGetBranchRefError(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.ExistingForks = map[string]string{
		"acme/widget": "contributor",
	}
	// No BranchRefs entry → GetBranchRef returns ErrNotFound.
	client.Errors = map[string]error{
		"GetBranchRef": fmt.Errorf("API timeout"),
	}
	printer, _ := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting upstream branch ref")
	assert.Contains(t, err.Error(), "acme/widget@main")
}

func TestCommitScaffoldViaPR_CrossForkBranchAlreadyExists(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.ExistingForks = map[string]string{
		"acme/widget": "contributor",
	}
	client.BranchRefs["acme/widget/main"] = "upstream-sha"
	// Simulate the scaffold branch already existing on the fork.
	client.Errors = map[string]error{
		"CreateBranchFromSHA": fmt.Errorf("branch exists: %w", forge.ErrAlreadyExists),
	}
	printer, buf := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.NoError(t, err)

	// Should fall through and commit files even though the branch already exists.
	require.Len(t, client.CommittedFilesToBranch, 1)
	assert.Equal(t, "contributor", client.CommittedFilesToBranch[0].Owner)
	assert.NotContains(t, buf.String(), "Failed to create scaffold branch")
}

func TestCommitScaffoldViaPR_CrossForkCreateBranchGenericError(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.ExistingForks = map[string]string{
		"acme/widget": "contributor",
	}
	client.BranchRefs["acme/widget/main"] = "upstream-sha"
	// Simulate a generic API error (not forbidden, not already exists).
	client.Errors = map[string]error{
		"CreateBranchFromSHA": fmt.Errorf("internal server error"),
	}
	printer, buf := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating scaffold branch")
	assert.Contains(t, buf.String(), "Failed to create scaffold branch")
}

func TestCommitScaffoldViaPR_CrossForkCommitFilesProtected(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.ExistingForks = map[string]string{
		"acme/widget": "contributor",
	}
	client.BranchRefs["acme/widget/main"] = "upstream-sha"
	client.Errors = map[string]error{
		"CommitFilesToBranch": fmt.Errorf("branch protected: %w", forge.ErrBranchProtected),
	}
	printer, buf := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scaffold branch")
	assert.Contains(t, err.Error(), "protected")
	assert.Contains(t, buf.String(), "protected")
}

func TestCommitScaffoldViaPR_CrossForkNoChanges(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.ExistingForks = map[string]string{
		"acme/widget": "contributor",
	}
	client.BranchRefs["acme/widget/main"] = "upstream-sha"
	client.Errors = map[string]error{
		"CreateChangeProposal": fmt.Errorf("no diff: %w", forge.ErrNoChanges),
	}
	printer, buf := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.NoError(t, err)

	// Should report up to date without error.
	assert.Contains(t, buf.String(), "up to date")
	assert.Empty(t, client.CreatedProposals)
}

func TestCommitScaffoldViaPR_CrossForkPRAlreadyExistsUpdated(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.ExistingForks = map[string]string{
		"acme/widget": "contributor",
	}
	client.BranchRefs["acme/widget/main"] = "upstream-sha"
	// CommitFilesToBranch returns changed=true by default.
	client.Errors = map[string]error{
		"CreateChangeProposal": fmt.Errorf("pr exists: %w", forge.ErrAlreadyExists),
	}
	printer, buf := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.NoError(t, err)

	// Verify the "updated with new files" message when PR exists but files changed.
	assert.Contains(t, buf.String(), "updated with new files")
	assert.Contains(t, buf.String(), "Merge the PR")
}

func TestCommitScaffoldViaPR_CrossForkPRAlreadyExistsNoUpdate(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.ExistingForks = map[string]string{
		"acme/widget": "contributor",
	}
	client.BranchRefs["acme/widget/main"] = "upstream-sha"
	noChange := false
	client.CommitFilesChanged = &noChange
	client.Errors = map[string]error{
		"CreateChangeProposal": fmt.Errorf("pr exists: %w", forge.ErrAlreadyExists),
	}
	printer, buf := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.NoError(t, err)

	// When PR exists and no files changed, should report up to date.
	assert.Contains(t, buf.String(), "up to date")
	assert.NotContains(t, buf.String(), "updated with new files")
}

func TestCommitScaffoldViaPR_NewForkUsesCreateBranchFromSHA(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.TokenScopes = []string{"repo", "workflow"}
	// No ExistingForks → will create a new fork.
	client.Repos = append(client.Repos, forge.Repository{
		FullName: "contributor/widget", DefaultBranch: "main",
	})
	client.BranchRefs["acme/widget/main"] = "upstream-sha-new-fork"
	printer, _ := newTestPrinter()

	// nil reader = non-interactive → auto-fork.
	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.NoError(t, err)

	// Verify a fork was created.
	require.Len(t, client.CreatedForks, 1)
	assert.Equal(t, "acme/widget", client.CreatedForks[0])

	// Verify the cross-fork path used CreateBranchFromSHA with the upstream SHA.
	require.Len(t, client.CreatedBranchSHAs, 1, "new fork path should use CreateBranchFromSHA")
	assert.Equal(t, "contributor", client.CreatedBranchSHAs[0].Owner)
	assert.Equal(t, "widget", client.CreatedBranchSHAs[0].Repo)
	assert.Equal(t, "upstream-sha-new-fork", client.CreatedBranchSHAs[0].SHA)
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
	client.BranchRefs["acme/widget/main"] = "upstream-sha"
	printer, buf := newTestPrinter()

	// Should warn but proceed (auto-fork since in=nil).
	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
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
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
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
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
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
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, true, nil)
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
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, true, nil)
	require.NoError(t, err)
	assert.True(t, committed)
	assert.Contains(t, buf.String(), "Concurrent modification race")
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
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, true, nil)
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
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, in)
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
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream delivery declined")
}

func TestCommitScaffoldViaPR_FineGrainedNonInteractive(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	printer, buf := newTestPrinter()

	// nil reader = non-interactive → should auto-upstream (not fork).
	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
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

	err := waitForFork(context.Background(), client, printer,
		"contributor", "widget", realClock{}, forkWaitTimeout)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication failed")
}

func TestWaitForFork_ImmediateSuccess(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = append(client.Repos, forge.Repository{
		FullName: "contributor/widget", DefaultBranch: "main",
	})
	printer, buf := newTestPrinter()
	clk := &recordingClock{}

	err := waitForFork(context.Background(), client, printer,
		"contributor", "widget", clk, forkWaitTimeout)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Fork is ready")
	assert.Empty(t, clk.intervals, "should not poll when fork is immediately available")
}

// recordingClock records the duration passed to each After call and
// resolves the returned channel immediately so the polling loop runs
// without wall-clock delays.
type recordingClock struct {
	intervals []time.Duration
	// addRepoAfter, when > 0, adds the named repo to client after that
	// many After() calls so subsequent GetRepo calls succeed.
	addRepoAfter int
	callCount    int
	client       *forge.FakeClient
	repoFullName string
}

func (c *recordingClock) After(d time.Duration) <-chan time.Time {
	c.intervals = append(c.intervals, d)
	c.callCount++
	if c.addRepoAfter > 0 && c.callCount == c.addRepoAfter {
		c.client.Repos = append(c.client.Repos, forge.Repository{
			FullName:      c.repoFullName,
			DefaultBranch: "main",
		})
	}
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch
}

func TestWaitForFork_ExponentialBackoff(t *testing.T) {
	client := forge.NewFakeClient()
	printer, _ := newTestPrinter()
	clk := &recordingClock{
		addRepoAfter: 4,
		client:       client,
		repoFullName: "contributor/widget",
	}

	err := waitForFork(context.Background(), client, printer,
		"contributor", "widget", clk, forkWaitTimeout)
	require.NoError(t, err)

	// Verify exponential backoff: 3s → 6s → 12s → 24s.
	require.Len(t, clk.intervals, 4)
	assert.Equal(t, 3*time.Second, clk.intervals[0])
	assert.Equal(t, 6*time.Second, clk.intervals[1])
	assert.Equal(t, 12*time.Second, clk.intervals[2])
	assert.Equal(t, 24*time.Second, clk.intervals[3])
}

func TestWaitForFork_BackoffCapsAtMax(t *testing.T) {
	client := forge.NewFakeClient()
	printer, _ := newTestPrinter()
	clk := &recordingClock{
		addRepoAfter: 7,
		client:       client,
		repoFullName: "contributor/widget",
	}

	err := waitForFork(context.Background(), client, printer,
		"contributor", "widget", clk, forkWaitTimeout)
	require.NoError(t, err)

	// After 3s → 6s → 12s → 24s → 30s (capped) → 30s → 30s.
	require.Len(t, clk.intervals, 7)
	assert.Equal(t, 3*time.Second, clk.intervals[0])
	assert.Equal(t, 6*time.Second, clk.intervals[1])
	assert.Equal(t, 12*time.Second, clk.intervals[2])
	assert.Equal(t, 24*time.Second, clk.intervals[3])
	assert.Equal(t, forkWaitMaxInterval, clk.intervals[4], "should cap at max interval")
	assert.Equal(t, forkWaitMaxInterval, clk.intervals[5], "should stay at cap")
	assert.Equal(t, forkWaitMaxInterval, clk.intervals[6], "should stay at cap")
}

// blockingClock never resolves After, forcing the select to pick
// ctx.Done() instead.
type blockingClock struct{}

func (blockingClock) After(time.Duration) <-chan time.Time {
	return make(chan time.Time) // blocks forever
}

func TestWaitForFork_ContextCancellation(t *testing.T) {
	client := forge.NewFakeClient()
	printer, _ := newTestPrinter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so ctx.Done() fires in the select

	err := waitForFork(ctx, client, printer,
		"contributor", "widget", blockingClock{}, forkWaitTimeout)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestWaitForFork_Timeout(t *testing.T) {
	client := forge.NewFakeClient()
	printer, buf := newTestPrinter()

	err := waitForFork(context.Background(), client, printer,
		"contributor", "widget", fakeClock{}, 50*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not ready after")
	assert.Contains(t, buf.String(), "Timed out")
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

func TestGitlintTitleRegex(t *testing.T) {
	t.Run("no gitlint file", func(t *testing.T) {
		client := forge.NewFakeClient()
		re := gitlintTitleRegex(context.Background(), client, "acme", "widget")
		assert.Nil(t, re)
	})

	t.Run("gitlint with title-match-regex", func(t *testing.T) {
		client := forge.NewFakeClient()
		client.FileContents["acme/widget/.gitlint"] = []byte(
			"[general]\nignore=body-is-missing\n\n[title-match-regex]\nregex=^(feat|fix|chore)(\\(.+\\))?: .+\n")
		re := gitlintTitleRegex(context.Background(), client, "acme", "widget")
		require.NotNil(t, re)
		assert.True(t, re.MatchString("chore: initialize fullsend per-repo installation"))
		assert.False(t, re.MatchString("PROJ-123: add stuff"))
	})

	t.Run("gitlint with custom regex requiring ticket prefix", func(t *testing.T) {
		client := forge.NewFakeClient()
		client.FileContents["acme/widget/.gitlint"] = []byte(
			"[title-match-regex]\nregex=^PROJ-\\d+: .+\n")
		re := gitlintTitleRegex(context.Background(), client, "acme", "widget")
		require.NotNil(t, re)
		assert.False(t, re.MatchString("chore: initialize fullsend per-repo installation"),
			"conventional commit should not match a ticket-prefix regex")
	})

	t.Run("gitlint without title-match-regex section", func(t *testing.T) {
		client := forge.NewFakeClient()
		client.FileContents["acme/widget/.gitlint"] = []byte(
			"[general]\nignore=body-is-missing\n\n[title-max-length]\nline-length=72\n")
		re := gitlintTitleRegex(context.Background(), client, "acme", "widget")
		assert.Nil(t, re)
	})

	t.Run("gitlint with spaces around equals", func(t *testing.T) {
		client := forge.NewFakeClient()
		client.FileContents["acme/widget/.gitlint"] = []byte(
			"[title-match-regex]\nregex = ^fix: .+\n")
		re := gitlintTitleRegex(context.Background(), client, "acme", "widget")
		require.NotNil(t, re)
		assert.True(t, re.MatchString("fix: something"))
	})

	t.Run("gitlint with tabs and extra spaces around equals", func(t *testing.T) {
		client := forge.NewFakeClient()
		client.FileContents["acme/widget/.gitlint"] = []byte(
			"[title-match-regex]\nregex\t=  ^fix: .+\n")
		re := gitlintTitleRegex(context.Background(), client, "acme", "widget")
		require.NotNil(t, re)
		assert.True(t, re.MatchString("fix: something"))
	})

	t.Run("invalid regex is ignored", func(t *testing.T) {
		client := forge.NewFakeClient()
		client.FileContents["acme/widget/.gitlint"] = []byte(
			"[title-match-regex]\nregex=[invalid((\n")
		re := gitlintTitleRegex(context.Background(), client, "acme", "widget")
		assert.Nil(t, re)
	})
}

func TestAdaptCommitMsg(t *testing.T) {
	t.Run("no gitlint warns nothing", func(t *testing.T) {
		client := forge.NewFakeClient()
		printer, buf := newTestPrinter()
		msg := adaptCommitMsg(context.Background(), client, printer, "acme", "widget",
			"chore: initialize fullsend per-repo installation")
		assert.Equal(t, "chore: initialize fullsend per-repo installation", msg)
		assert.NotContains(t, buf.String(), "gitlint")
	})

	t.Run("matching regex warns nothing", func(t *testing.T) {
		client := forge.NewFakeClient()
		client.FileContents["acme/widget/.gitlint"] = []byte(
			"[title-match-regex]\nregex=^(feat|fix|chore): .+\n")
		printer, buf := newTestPrinter()
		msg := adaptCommitMsg(context.Background(), client, printer, "acme", "widget",
			"chore: initialize fullsend per-repo installation")
		assert.Equal(t, "chore: initialize fullsend per-repo installation", msg)
		assert.NotContains(t, buf.String(), "gitlint")
	})

	t.Run("adapts to ci prefix when chore does not match", func(t *testing.T) {
		client := forge.NewFakeClient()
		client.FileContents["acme/widget/.gitlint"] = []byte(
			"[title-match-regex]\nregex=^(ci|build): .+\n")
		printer, buf := newTestPrinter()
		msg := adaptCommitMsg(context.Background(), client, printer, "acme", "widget",
			"chore: initialize fullsend per-repo installation")
		assert.Equal(t, "ci: initialize fullsend per-repo installation", msg)
		assert.Contains(t, buf.String(), "Adapted scaffold commit message")
		assert.NotContains(t, buf.String(), "CI may fail")
	})

	t.Run("adapts to bare description when no prefix matches", func(t *testing.T) {
		client := forge.NewFakeClient()
		client.FileContents["acme/widget/.gitlint"] = []byte(
			"[title-match-regex]\nregex=^[a-z]+ .+\n")
		printer, buf := newTestPrinter()
		msg := adaptCommitMsg(context.Background(), client, printer, "acme", "widget",
			"chore: initialize fullsend per-repo installation")
		assert.Equal(t, "initialize fullsend per-repo installation", msg)
		assert.Contains(t, buf.String(), "Adapted scaffold commit message")
	})

	t.Run("warns when no alternative matches", func(t *testing.T) {
		client := forge.NewFakeClient()
		client.FileContents["acme/widget/.gitlint"] = []byte(
			"[title-match-regex]\nregex=^PROJ-\\d+: .+\n")
		printer, buf := newTestPrinter()
		msg := adaptCommitMsg(context.Background(), client, printer, "acme", "widget",
			"chore: initialize fullsend per-repo installation")
		assert.Equal(t, "chore: initialize fullsend per-repo installation", msg)
		assert.Contains(t, buf.String(), "title-match-regex")
		assert.Contains(t, buf.String(), "commit-lint CI may fail")
	})

	t.Run("adapts non-scaffold commit message", func(t *testing.T) {
		client := forge.NewFakeClient()
		client.FileContents["acme/widget/.gitlint"] = []byte(
			"[title-match-regex]\nregex=^(ci|build): .+\n")
		printer, buf := newTestPrinter()
		msg := adaptCommitMsg(context.Background(), client, printer, "acme", "widget",
			"chore: upgrade fullsend config")
		assert.Equal(t, "ci: upgrade fullsend config", msg)
		assert.Contains(t, buf.String(), "Adapted scaffold commit message")
	})

	t.Run("preserves body when adapting", func(t *testing.T) {
		client := forge.NewFakeClient()
		client.FileContents["acme/widget/.gitlint"] = []byte(
			"[title-match-regex]\nregex=^(ci|build): .+\n")
		printer, _ := newTestPrinter()
		msg := adaptCommitMsg(context.Background(), client, printer, "acme", "widget",
			"chore: initialize fullsend per-repo installation\n\nSigned-off-by: bot")
		assert.Equal(t, "ci: initialize fullsend per-repo installation\n\nSigned-off-by: bot", msg)
	})
}

func TestCloseStaleScaffoldPRs_ClosesOnboardPR(t *testing.T) {
	client := forge.NewFakeClient()
	client.PullRequests = map[string][]forge.ChangeProposal{
		"acme/widget": {
			{Number: 42, Title: "chore: connect to fullsend agent pipeline", Head: "fullsend/onboard", Base: "main", Author: "acme"},
		},
	}
	printer, buf := newTestPrinter()

	closeStaleScaffoldPRs(context.Background(), client, printer,
		"acme", "widget", "fullsend/scaffold-install", "acme")

	assert.Contains(t, client.ClosedProposals, 42)
	assert.Contains(t, client.DeletedRefs, "acme/widget/heads/fullsend/onboard")
	assert.Contains(t, buf.String(), "Closed stale scaffold PR #42")
}

func TestCloseStaleScaffoldPRs_SkipsCurrentBranch(t *testing.T) {
	client := forge.NewFakeClient()
	client.PullRequests = map[string][]forge.ChangeProposal{
		"acme/widget": {
			{Number: 10, Title: "scaffold PR", Head: "fullsend/scaffold-install", Base: "main", Author: "acme"},
		},
	}
	printer, _ := newTestPrinter()

	closeStaleScaffoldPRs(context.Background(), client, printer,
		"acme", "widget", "fullsend/scaffold-install", "acme")

	assert.Empty(t, client.ClosedProposals, "should not close the current branch's PR")
}

func TestCloseStaleScaffoldPRs_SkipsUnrelatedPRs(t *testing.T) {
	client := forge.NewFakeClient()
	client.PullRequests = map[string][]forge.ChangeProposal{
		"acme/widget": {
			{Number: 5, Title: "feat: add feature", Head: "feature/add-feature", Base: "main", Author: "acme"},
		},
	}
	printer, _ := newTestPrinter()

	closeStaleScaffoldPRs(context.Background(), client, printer,
		"acme", "widget", "fullsend/scaffold-install", "acme")

	assert.Empty(t, client.ClosedProposals, "should not close unrelated PRs")
}

func TestCloseStaleScaffoldPRs_ListError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors = map[string]error{
		"ListRepoPullRequests": fmt.Errorf("API rate limit"),
	}
	printer, buf := newTestPrinter()

	closeStaleScaffoldPRs(context.Background(), client, printer,
		"acme", "widget", "fullsend/scaffold-install", "acme")

	assert.Empty(t, client.ClosedProposals)
	assert.Contains(t, buf.String(), "Could not check for stale scaffold PRs")
}

func TestCloseStaleScaffoldPRs_CloseError(t *testing.T) {
	client := forge.NewFakeClient()
	client.PullRequests = map[string][]forge.ChangeProposal{
		"acme/widget": {
			{Number: 42, Title: "stale PR", Head: "fullsend/onboard", Base: "main", Author: "acme"},
		},
	}
	client.Errors = map[string]error{
		"CloseChangeProposal": fmt.Errorf("forbidden"),
	}
	printer, buf := newTestPrinter()

	closeStaleScaffoldPRs(context.Background(), client, printer,
		"acme", "widget", "fullsend/scaffold-install", "acme")

	assert.Empty(t, client.ClosedProposals, "should not record if close failed")
	assert.Contains(t, buf.String(), "Could not close stale PR #42")
}

func TestCommitScaffoldViaPR_ClosesStaleOnboardPR(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.PullRequests = map[string][]forge.ChangeProposal{
		"acme/widget": {
			{Number: 99, Title: "chore: connect to fullsend", Head: "fullsend/onboard", Base: "main", Author: "acme"},
		},
	}
	printer, buf := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.NoError(t, err)

	assert.Contains(t, client.ClosedProposals, 99, "stale onboard PR should be closed")
	assert.Contains(t, buf.String(), "Closed stale scaffold PR #99")

	// Should still create its own branch and PR.
	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "acme/widget/fullsend/scaffold-install", client.CreatedBranches[0])
}

func TestCloseStaleScaffoldPRs_SkipsDifferentAuthor(t *testing.T) {
	client := forge.NewFakeClient()
	client.PullRequests = map[string][]forge.ChangeProposal{
		"acme/widget": {
			{Number: 42, Title: "stale PR", Head: "fullsend/onboard", Base: "main", Author: "external-user"},
		},
	}
	printer, _ := newTestPrinter()

	closeStaleScaffoldPRs(context.Background(), client, printer,
		"acme", "widget", "fullsend/scaffold-install", "acme")

	assert.Empty(t, client.ClosedProposals, "should not close PRs from different author")
}

func TestCloseStaleScaffoldPRs_SkipsEmptyAuthor(t *testing.T) {
	client := forge.NewFakeClient()
	client.PullRequests = map[string][]forge.ChangeProposal{
		"acme/widget": {
			{Number: 42, Title: "stale PR", Head: "fullsend/onboard", Base: "main", Author: ""},
		},
	}
	printer, _ := newTestPrinter()

	closeStaleScaffoldPRs(context.Background(), client, printer,
		"acme", "widget", "fullsend/scaffold-install", "acme")

	assert.Empty(t, client.ClosedProposals, "should not close PRs with empty author (fail-closed)")
}

func TestCloseStaleScaffoldPRs_DeleteRefError(t *testing.T) {
	client := forge.NewFakeClient()
	client.PullRequests = map[string][]forge.ChangeProposal{
		"acme/widget": {
			{Number: 42, Title: "stale PR", Head: "fullsend/onboard", Base: "main", Author: "acme"},
		},
	}
	client.Errors = map[string]error{
		"DeleteRef": fmt.Errorf("ref not found"),
	}
	printer, buf := newTestPrinter()

	closeStaleScaffoldPRs(context.Background(), client, printer,
		"acme", "widget", "fullsend/scaffold-install", "acme")

	assert.Contains(t, client.ClosedProposals, 42, "PR should still be closed even if branch delete fails")
	assert.Contains(t, buf.String(), "Could not delete stale branch fullsend/onboard")
	assert.Contains(t, buf.String(), "Closed stale scaffold PR #42")
}

func TestCommitScaffoldViaPR_SkipsStaleCleanupInForkPath(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "contributor"
	client.ExistingForks = map[string]string{
		"acme/widget": "contributor",
	}
	client.BranchRefs["acme/widget/main"] = "upstream-sha"
	client.PullRequests = map[string][]forge.ChangeProposal{
		"acme/widget": {
			{Number: 99, Title: "chore: connect to fullsend", Head: "fullsend/onboard", Base: "main", Author: "acme"},
		},
	}
	printer, _ := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main", testMeta("msg", "title", "body"), testFiles, false, nil)
	require.NoError(t, err)

	assert.Empty(t, client.ClosedProposals, "should not close upstream PRs when using fork path")
}

func TestIsKnownScaffoldBranch(t *testing.T) {
	assert.True(t, isKnownScaffoldBranch("fullsend/scaffold-install"))
	assert.True(t, isKnownScaffoldBranch("fullsend/onboard"))
	assert.True(t, isKnownScaffoldBranch("fullsend/bump-v0.28.0"))
	assert.True(t, isKnownScaffoldBranch("fullsend/bump-v1.0.0-rc.1"))
	assert.False(t, isKnownScaffoldBranch("feature/add-stuff"))
	assert.False(t, isKnownScaffoldBranch("fullsend/offboard"))
}

func TestCommitScaffoldFiles_CustomBranch(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	printer, _ := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main",
		testMeta("msg", "title", "body", "fullsend/bump-v0.28.0"),
		testFiles, false, nil)
	require.NoError(t, err)

	// Branch should use the custom name, not the default.
	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "acme/widget/fullsend/bump-v0.28.0", client.CreatedBranches[0])

	// PR head should be the custom branch name.
	require.Len(t, client.CreatedProposals, 1)
	assert.Equal(t, "fullsend/bump-v0.28.0", client.CreatedProposals[0].Head)
}

func TestCommitScaffoldFiles_EmptyBranchUsesDefault(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	printer, _ := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main",
		testMeta("msg", "title", "body"),
		testFiles, false, nil)
	require.NoError(t, err)

	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "acme/widget/fullsend/scaffold-install", client.CreatedBranches[0])
}

func TestCommitScaffoldFiles_UpgradeBranchClosesOldInstallPR(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.PullRequests = map[string][]forge.ChangeProposal{
		"acme/widget": {
			{Number: 10, Title: "chore: initialize fullsend", Head: "fullsend/scaffold-install", Base: "main", Author: "acme"},
		},
	}
	printer, buf := newTestPrinter()

	_, err := CommitScaffoldFiles(context.Background(), client, printer,
		"acme", "widget", "main",
		testMeta("msg", "title", "body", "fullsend/bump-v0.28.0"),
		testFiles, false, nil)
	require.NoError(t, err)

	// The old install PR should be closed since we're using a different
	// scaffold branch now.
	assert.Contains(t, client.ClosedProposals, 10,
		"old scaffold-install PR should be closed when upgrading via bump branch")
	assert.Contains(t, buf.String(), "Closed stale scaffold PR #10")

	// New branch should be created.
	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "acme/widget/fullsend/bump-v0.28.0", client.CreatedBranches[0])
}
