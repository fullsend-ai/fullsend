package steps

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// --- givenReactionsEnabled tests ---

func TestGivenReactionsEnabled_PreservesExistingCommentSettings(t *testing.T) {
	t.Parallel()

	scm := &fakeReactionSCM{
		fileContent: []byte("version: \"1\"\nroles:\n  - triage\nstatus_notifications:\n  comment:\n    start: enabled\n    completion: on_failure\n"),
	}
	install := &fakeReactionInstall{owner: "org", repo: "repo"}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		Install:   install,
		SCM:       scm,
	}

	err := givenReactionsEnabled(w)
	require.NoError(t, err)
	require.True(t, scm.commitFileCalled, "should commit updated config")

	// The committed YAML should contain both the preserved comment
	// settings and the newly enabled reaction settings.
	yaml := string(scm.committedContent)
	assert.Contains(t, yaml, "on_failure", "existing comment.completion should be preserved")
	assert.Contains(t, yaml, "reaction", "reaction block should be present")
}

func TestGivenReactionsEnabled_NoExistingConfig(t *testing.T) {
	t.Parallel()

	scm := &fakeReactionSCM{
		fileContent: []byte("version: \"1\"\nroles:\n  - triage\n"),
	}
	install := &fakeReactionInstall{owner: "org", repo: "repo"}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		Install:   install,
		SCM:       scm,
	}

	err := givenReactionsEnabled(w)
	require.NoError(t, err)
	require.True(t, scm.commitFileCalled)

	yaml := string(scm.committedContent)
	assert.Contains(t, yaml, "reaction")
}

func TestGivenReactionsEnabled_GetFileError(t *testing.T) {
	t.Parallel()

	scm := &fakeReactionSCM{
		getFileErr: fmt.Errorf("not found"),
	}
	install := &fakeReactionInstall{owner: "org", repo: "repo"}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		Install:   install,
		SCM:       scm,
	}

	err := givenReactionsEnabled(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

// --- DisableReactionNotifications tests ---

func TestDisableReactionNotifications_PreservesCommentSettings(t *testing.T) {
	t.Parallel()

	scm := &fakeReactionSCM{
		fileContent: []byte("version: \"1\"\nroles:\n  - triage\nstatus_notifications:\n  comment:\n    start: enabled\n    completion: on_failure\n  reaction:\n    start: enabled\n    completion: enabled\n"),
	}
	install := &fakeReactionInstall{owner: "org", repo: "repo"}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		Install:   install,
		SCM:       scm,
	}

	err := DisableReactionNotifications(w)
	require.NoError(t, err)
	require.True(t, scm.commitFileCalled)

	yaml := string(scm.committedContent)
	assert.Contains(t, yaml, "on_failure", "existing comment.completion should be preserved")
	assert.NotContains(t, yaml, "reaction", "reaction block should be cleared")
}

func TestDisableReactionNotifications_NilsWhenNoCommentSettings(t *testing.T) {
	t.Parallel()

	scm := &fakeReactionSCM{
		fileContent: []byte("version: \"1\"\nroles:\n  - triage\nstatus_notifications:\n  reaction:\n    start: enabled\n    completion: enabled\n"),
	}
	install := &fakeReactionInstall{owner: "org", repo: "repo"}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		Install:   install,
		SCM:       scm,
	}

	err := DisableReactionNotifications(w)
	require.NoError(t, err)
	require.True(t, scm.commitFileCalled)

	yaml := string(scm.committedContent)
	assert.NotContains(t, yaml, "status_notifications", "whole block should be removed when no comment settings")
}

func TestDisableReactionNotifications_NoExistingConfig(t *testing.T) {
	t.Parallel()

	scm := &fakeReactionSCM{
		fileContent: []byte("version: \"1\"\nroles:\n  - triage\n"),
	}
	install := &fakeReactionInstall{owner: "org", repo: "repo"}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		Install:   install,
		SCM:       scm,
	}

	err := DisableReactionNotifications(w)
	require.NoError(t, err)
	require.True(t, scm.commitFileCalled)
}

func TestDisableReactionNotifications_GetFileError(t *testing.T) {
	t.Parallel()

	scm := &fakeReactionSCM{
		getFileErr: fmt.Errorf("not found"),
	}
	install := &fakeReactionInstall{owner: "org", repo: "repo"}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		Install:   install,
		SCM:       scm,
	}

	err := DisableReactionNotifications(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

// --- thenIssueHasReaction tests ---

func TestThenIssueHasReaction_Found(t *testing.T) {
	t.Parallel()

	scm := &fakeReactionSCM{
		reactions: []forge.Reaction{
			{ID: 1, Content: "+1", User: "bot"},
			{ID: 2, Content: "eyes", User: "bot"},
		},
	}
	w := &world.World{
		RepoOwner:   "org",
		RepoName:    "repo",
		IssueNumber: 42,
		SCM:         scm,
	}

	err := thenIssueHasReaction(w, "+1")
	assert.NoError(t, err)
}

func TestThenIssueHasReaction_NotFound(t *testing.T) {
	t.Parallel()

	scm := &fakeReactionSCM{
		reactions: []forge.Reaction{
			{ID: 1, Content: "eyes", User: "bot"},
		},
	}
	w := &world.World{
		RepoOwner:   "org",
		RepoName:    "repo",
		IssueNumber: 42,
		SCM:         scm,
	}

	err := thenIssueHasReaction(w, "+1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "do not include \"+1\"")
}

func TestThenIssueHasReaction_NoIssue(t *testing.T) {
	t.Parallel()

	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
	}

	err := thenIssueHasReaction(w, "+1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no issue created")
}

// --- thenIssueDoesNotHaveReaction tests ---

func TestThenIssueDoesNotHaveReaction_Absent(t *testing.T) {
	t.Parallel()

	scm := &fakeReactionSCM{
		reactions: []forge.Reaction{
			{ID: 1, Content: "+1", User: "bot"},
		},
	}
	w := &world.World{
		RepoOwner:   "org",
		RepoName:    "repo",
		IssueNumber: 42,
		SCM:         scm,
	}

	err := thenIssueDoesNotHaveReaction(w, "eyes")
	assert.NoError(t, err)
}

func TestThenIssueDoesNotHaveReaction_Present(t *testing.T) {
	t.Parallel()

	scm := &fakeReactionSCM{
		reactions: []forge.Reaction{
			{ID: 1, Content: "eyes", User: "bot"},
		},
	}
	w := &world.World{
		RepoOwner:   "org",
		RepoName:    "repo",
		IssueNumber: 42,
		SCM:         scm,
	}

	err := thenIssueDoesNotHaveReaction(w, "eyes")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpectedly has \"eyes\"")
}

func TestThenIssueDoesNotHaveReaction_NoIssue(t *testing.T) {
	t.Parallel()

	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
	}

	err := thenIssueDoesNotHaveReaction(w, "eyes")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no issue created")
}

// --- reactionsEnabledInConfig tests ---

func TestReactionsEnabledInConfig_StartEnabled(t *testing.T) {
	t.Parallel()

	scm := &fakeReactionSCM{
		fileContent: []byte("version: \"1\"\nroles:\n  - triage\nstatus_notifications:\n  reaction:\n    start: enabled\n"),
	}
	install := &fakeReactionInstall{owner: "org", repo: "repo"}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		Install:   install,
		SCM:       scm,
	}

	assert.True(t, reactionsEnabledInConfig(w))
}

func TestReactionsEnabledInConfig_CompletionOnly(t *testing.T) {
	t.Parallel()

	scm := &fakeReactionSCM{
		fileContent: []byte("version: \"1\"\nroles:\n  - triage\nstatus_notifications:\n  reaction:\n    completion: enabled\n"),
	}
	install := &fakeReactionInstall{owner: "org", repo: "repo"}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		Install:   install,
		SCM:       scm,
	}

	assert.True(t, reactionsEnabledInConfig(w))
}

func TestReactionsEnabledInConfig_Disabled(t *testing.T) {
	t.Parallel()

	scm := &fakeReactionSCM{
		fileContent: []byte("version: \"1\"\nroles:\n  - triage\nstatus_notifications:\n  reaction:\n    start: disabled\n    completion: disabled\n"),
	}
	install := &fakeReactionInstall{owner: "org", repo: "repo"}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		Install:   install,
		SCM:       scm,
	}

	assert.False(t, reactionsEnabledInConfig(w))
}

func TestReactionsEnabledInConfig_NoNotifications(t *testing.T) {
	t.Parallel()

	scm := &fakeReactionSCM{
		fileContent: []byte("version: \"1\"\nroles:\n  - triage\n"),
	}
	install := &fakeReactionInstall{owner: "org", repo: "repo"}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		Install:   install,
		SCM:       scm,
	}

	assert.False(t, reactionsEnabledInConfig(w))
}

func TestReactionsEnabledInConfig_NilSCM(t *testing.T) {
	t.Parallel()

	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
	}

	assert.False(t, reactionsEnabledInConfig(w))
}

// --- fakeReactionSCM ---

type fakeReactionSCM struct {
	fileContent      []byte
	getFileErr       error
	commitFileCalled bool
	committedContent []byte
	commitFileErr    error
	reactions        []forge.Reaction
}

func (f *fakeReactionSCM) GetFileContent(_ context.Context, _, _, _ string) ([]byte, error) {
	return f.fileContent, f.getFileErr
}

func (f *fakeReactionSCM) CommitFile(_ context.Context, _, _, _, _ string, content []byte) error {
	f.commitFileCalled = true
	f.committedContent = content
	return f.commitFileErr
}

func (f *fakeReactionSCM) ListIssueReactions(_ context.Context, _, _ string, _ int) ([]forge.Reaction, error) {
	return f.reactions, nil
}

// Unused scm.Driver methods — required for interface satisfaction.
func (f *fakeReactionSCM) CreateIssue(context.Context, string, string, string, string, ...string) (*forge.Issue, error) {
	return nil, nil
}
func (f *fakeReactionSCM) AddIssueLabels(context.Context, string, string, int, ...string) error {
	return nil
}
func (f *fakeReactionSCM) AddComment(context.Context, string, string, int, string) (*forge.IssueComment, error) {
	return nil, nil
}
func (f *fakeReactionSCM) GetIssue(context.Context, string, string, int) (*forge.Issue, error) {
	return nil, nil
}
func (f *fakeReactionSCM) CloseIssue(context.Context, string, string, int) error { return nil }
func (f *fakeReactionSCM) DeleteBranch(context.Context, string, string, string) error {
	return nil
}
func (f *fakeReactionSCM) DeleteRepo(context.Context, string, string) error { return nil }
func (f *fakeReactionSCM) CreateBranch(context.Context, string, string, string) error {
	return nil
}
func (f *fakeReactionSCM) CommitFileToBranch(context.Context, string, string, string, string, string, []byte) error {
	return nil
}
func (f *fakeReactionSCM) CreateChangeProposal(context.Context, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, nil
}
func (f *fakeReactionSCM) SubmitPullRequestReview(context.Context, string, string, int, string) error {
	return nil
}
func (f *fakeReactionSCM) ListOpenChangeProposals(context.Context, string, string) ([]forge.ChangeProposal, error) {
	return nil, nil
}
func (f *fakeReactionSCM) ListComments(context.Context, string, string, int) ([]forge.IssueComment, error) {
	return nil, nil
}
func (f *fakeReactionSCM) CreateRepo(context.Context, string, string, string) error { return nil }
func (f *fakeReactionSCM) EnsureRepoPublic(context.Context, string, string) error   { return nil }
func (f *fakeReactionSCM) GetDefaultBranch(context.Context, string, string) (string, error) {
	return "main", nil
}
func (f *fakeReactionSCM) GetBranchRef(context.Context, string, string, string) (string, error) {
	return "abc123", nil
}
func (f *fakeReactionSCM) CreateFork(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (f *fakeReactionSCM) CommitFileToFork(context.Context, string, string, string, string, string, []byte) error {
	return nil
}
func (f *fakeReactionSCM) CreateForkChangeProposal(context.Context, string, string, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, nil
}

// fakeReactionInstall satisfies the install.State interface.
type fakeReactionInstall struct {
	owner string
	repo  string
}

func (f *fakeReactionInstall) Mode() string               { return "per-repo" }
func (f *fakeReactionInstall) TestRepo() string           { return f.repo }
func (f *fakeReactionInstall) ConfigOwner() string        { return f.owner }
func (f *fakeReactionInstall) ConfigRepo() string         { return f.repo }
func (f *fakeReactionInstall) ConfigPathPrefix() string   { return ".fullsend" }
func (f *fakeReactionInstall) TriageWorkflowRepo() string { return f.repo }
func (f *fakeReactionInstall) TriageWorkflowFile() string { return "fullsend.yaml" }
func (f *fakeReactionInstall) AgentWorkflowFile() string  { return "reusable-triage.yml" }
func (f *fakeReactionInstall) AgentArtifactName() string  { return "fullsend-triage" }
