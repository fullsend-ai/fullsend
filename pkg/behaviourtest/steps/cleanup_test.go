package steps

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func TestShouldRemoveArtifactDir(t *testing.T) {
	t.Parallel()

	ciRoot := "/tmp/behaviour-artifacts"
	assert.False(t, shouldRemoveArtifactDir(ciRoot, ciRoot))
	assert.False(t, shouldRemoveArtifactDir(ciRoot+"/run-123", ciRoot))
	assert.True(t, shouldRemoveArtifactDir("/tmp/behaviour-artifacts-evil/run-123", ciRoot))
	assert.True(t, shouldRemoveArtifactDir("/var/tmp/local-run", ciRoot))
	assert.True(t, shouldRemoveArtifactDir("/tmp/local-run", ""))
}

func TestArtifactDirUnderCIRoot(t *testing.T) {
	t.Parallel()

	ciRoot := "/tmp/behaviour-artifacts"
	assert.True(t, artifactDirUnderCIRoot(ciRoot, ciRoot))
	assert.True(t, artifactDirUnderCIRoot(ciRoot+"/run-456", ciRoot))
	assert.False(t, artifactDirUnderCIRoot("/tmp/behaviour-artifacts-evil/run", ciRoot))
}

func TestCleanupScenario_ClosesForkPR(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner:    "org",
		RepoName:     "repo",
		ForkPRNumber: 42,
		SCM:          scmDriver,
	}
	CleanupScenario(w)
	require.Len(t, scmDriver.closedIssues, 1)
	assert.Equal(t, "org", scmDriver.closedIssues[0].owner)
	assert.Equal(t, "repo", scmDriver.closedIssues[0].repo)
	assert.Equal(t, 42, scmDriver.closedIssues[0].number)
}

func TestCleanupScenario_ClosesForkPR_Error(t *testing.T) {
	t.Parallel()

	var logged []string
	scmDriver := &fakeCleanupSCM{closeIssueErr: fmt.Errorf("close failed")}
	w := &world.World{
		RepoOwner:    "org",
		RepoName:     "repo",
		ForkPRNumber: 42,
		SCM:          scmDriver,
		Logf:         func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	CleanupScenario(w)
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "close fork PR #42")
}

func TestCleanupScenario_SkipsForkCleanupWhenNotSet(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		SCM:       scmDriver,
	}
	CleanupScenario(w)
	assert.Empty(t, scmDriver.closedIssues)
}

func TestCleanupScenario_DeletesForkBranch(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner:    "org",
		RepoName:     "repo",
		ForkPRNumber: 10,
		ForkOwner:    "org",
		ForkRepo:     "fork-repo",
		ForkPRBranch: "test-branch",
		SCM:          scmDriver,
	}
	CleanupScenario(w)

	require.Len(t, scmDriver.closedIssues, 1)
	assert.Equal(t, 10, scmDriver.closedIssues[0].number)

	require.Len(t, scmDriver.deletedBranches, 1)
	assert.Equal(t, "org", scmDriver.deletedBranches[0].owner)
	assert.Equal(t, "fork-repo", scmDriver.deletedBranches[0].repo)
	assert.Equal(t, "test-branch", scmDriver.deletedBranches[0].branch)
}

func TestCleanupScenario_DeleteBranchNotFound_SilentlyIgnored(t *testing.T) {
	t.Parallel()

	var logged []string
	scmDriver := &fakeCleanupSCM{deleteBranchErr: fmt.Errorf("delete branch: %w", forge.ErrNotFound)}
	w := &world.World{
		RepoOwner:    "org",
		RepoName:     "repo",
		ForkOwner:    "org",
		ForkRepo:     "fork-repo",
		ForkPRBranch: "gone-branch",
		SCM:          scmDriver,
		Logf:         func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	CleanupScenario(w)

	// 404/ErrNotFound is silently ignored — no log output for branch deletion.
	for _, msg := range logged {
		assert.NotContains(t, msg, "fork branch", "ErrNotFound should be silently ignored")
	}
}

func TestCleanupScenario_DeleteBranchError_Logged(t *testing.T) {
	t.Parallel()

	var logged []string
	scmDriver := &fakeCleanupSCM{deleteBranchErr: fmt.Errorf("server error")}
	w := &world.World{
		RepoOwner:    "org",
		RepoName:     "repo",
		ForkOwner:    "org",
		ForkRepo:     "fork-repo",
		ForkPRBranch: "bad-branch",
		SCM:          scmDriver,
		Logf:         func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	CleanupScenario(w)

	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "delete fork branch bad-branch")
	assert.Contains(t, logged[0], "server error")
}

func TestCleanupScenario_DeletesForkRepo(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		ForkOwner: "org",
		ForkRepo:  "repo-fork",
		SCM:       scmDriver,
	}
	CleanupScenario(w)

	require.Len(t, scmDriver.deletedRepos, 1)
	assert.Equal(t, "org", scmDriver.deletedRepos[0].owner)
	assert.Equal(t, "repo-fork", scmDriver.deletedRepos[0].repo)
}

func TestCleanupScenario_DeleteForkRepoNotFound_SilentlyIgnored(t *testing.T) {
	t.Parallel()

	var logged []string
	scmDriver := &fakeCleanupSCM{deleteRepoErr: fmt.Errorf("delete repo: %w", forge.ErrNotFound)}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		ForkOwner: "org",
		ForkRepo:  "repo-fork",
		SCM:       scmDriver,
		Logf:      func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	CleanupScenario(w)

	for _, msg := range logged {
		assert.NotContains(t, msg, "fork repo", "ErrNotFound should be silently ignored")
	}
}

func TestCleanupScenario_DeleteForkRepoError_Logged(t *testing.T) {
	t.Parallel()

	var logged []string
	scmDriver := &fakeCleanupSCM{deleteRepoErr: fmt.Errorf("server error")}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		ForkOwner: "org",
		ForkRepo:  "repo-fork",
		SCM:       scmDriver,
		Logf:      func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	CleanupScenario(w)

	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "delete fork repo org/repo-fork")
	assert.Contains(t, logged[0], "server error")
}

func TestCleanupScenario_SkipsForkRepoDelete_WhenForkRepoEqualsRepoName(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		ForkOwner: "org",
		ForkRepo:  "repo", // same as RepoName — must not be deleted
		SCM:       scmDriver,
	}
	CleanupScenario(w)

	assert.Empty(t, scmDriver.deletedRepos, "repo deletion should be skipped when ForkRepo == RepoName")
}

func TestCleanupScenario_SkipsForkRepoDelete_WhenFieldsMissing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		world *world.World
	}{
		{
			name: "missing ForkOwner",
			world: &world.World{
				RepoOwner: "org",
				RepoName:  "repo",
				ForkRepo:  "repo-fork",
				SCM:       &fakeCleanupSCM{},
			},
		},
		{
			name: "missing ForkRepo",
			world: &world.World{
				RepoOwner: "org",
				RepoName:  "repo",
				ForkOwner: "org",
				SCM:       &fakeCleanupSCM{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scm := tt.world.SCM.(*fakeCleanupSCM)
			CleanupScenario(tt.world)
			assert.Empty(t, scm.deletedRepos, "repo deletion should be skipped when fields are missing")
		})
	}
}

func TestCleanupScenario_SkipsBranchDelete_WhenFieldsMissing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		world *world.World
	}{
		{
			name: "missing ForkPRBranch",
			world: &world.World{
				RepoOwner: "org",
				RepoName:  "repo",
				ForkOwner: "org",
				ForkRepo:  "fork-repo",
				SCM:       &fakeCleanupSCM{},
			},
		},
		{
			name: "missing ForkOwner",
			world: &world.World{
				RepoOwner:    "org",
				RepoName:     "repo",
				ForkRepo:     "fork-repo",
				ForkPRBranch: "branch",
				SCM:          &fakeCleanupSCM{},
			},
		},
		{
			name: "missing ForkRepo",
			world: &world.World{
				RepoOwner:    "org",
				RepoName:     "repo",
				ForkOwner:    "org",
				ForkPRBranch: "branch",
				SCM:          &fakeCleanupSCM{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scm := tt.world.SCM.(*fakeCleanupSCM)
			CleanupScenario(tt.world)
			assert.Empty(t, scm.deletedBranches, "branch deletion should be skipped when fields are missing")
		})
	}
}

// --- URL harness hosting repo cleanup tests ---

func TestCleanupScenario_DeletesHostingRepo(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner:           "org",
		RepoName:            "repo",
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "test-repo-07-url-harness-host",
		SCM:                 scmDriver,
	}
	CleanupScenario(w)

	require.Len(t, scmDriver.deletedRepos, 1)
	assert.Equal(t, "org", scmDriver.deletedRepos[0].owner)
	assert.Equal(t, "test-repo-07-url-harness-host", scmDriver.deletedRepos[0].repo)
}

func TestCleanupScenario_SkipsHostingRepoDelete_WhenEqualsRepoName(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner:           "org",
		RepoName:            "repo",
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "repo", // same as RepoName — must not be deleted
		SCM:                 scmDriver,
	}
	CleanupScenario(w)

	assert.Empty(t, scmDriver.deletedRepos, "repo deletion should be skipped when URLHarnessRepoName == RepoName")
}

func TestCleanupScenario_SkipsHostingRepoDelete_WhenFieldsMissing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		world *world.World
	}{
		{
			name: "missing URLHarnessRepoOwner",
			world: &world.World{
				RepoOwner:          "org",
				RepoName:           "repo",
				URLHarnessRepoName: "host-repo",
				SCM:                &fakeCleanupSCM{},
			},
		},
		{
			name: "missing URLHarnessRepoName",
			world: &world.World{
				RepoOwner:           "org",
				RepoName:            "repo",
				URLHarnessRepoOwner: "org",
				SCM:                 &fakeCleanupSCM{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scm := tt.world.SCM.(*fakeCleanupSCM)
			CleanupScenario(tt.world)
			assert.Empty(t, scm.deletedRepos, "repo deletion should be skipped when hosting repo fields are missing")
		})
	}
}

func TestCleanupScenario_DeleteHostingRepoNotFound_SilentlyIgnored(t *testing.T) {
	t.Parallel()

	var logged []string
	scmDriver := &fakeCleanupSCM{deleteRepoErr: fmt.Errorf("delete repo: %w", forge.ErrNotFound)}
	w := &world.World{
		RepoOwner:           "org",
		RepoName:            "repo",
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "host-repo",
		SCM:                 scmDriver,
		Logf:                func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	CleanupScenario(w)

	for _, msg := range logged {
		assert.NotContains(t, msg, "harness-hosting repo", "ErrNotFound should be silently ignored")
	}
}

func TestCleanupScenario_DeleteHostingRepoError_Logged(t *testing.T) {
	t.Parallel()

	var logged []string
	scmDriver := &fakeCleanupSCM{deleteRepoErr: fmt.Errorf("server error")}
	w := &world.World{
		RepoOwner:           "org",
		RepoName:            "repo",
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "host-repo",
		SCM:                 scmDriver,
		Logf:                func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	CleanupScenario(w)

	// The shared deleteRepoErr causes log messages for both hosting repo
	// (no fork fields set, so only hosting repo cleanup fires).
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "delete harness-hosting repo org/host-repo")
	assert.Contains(t, logged[0], "server error")
}

// fakeCleanupSCM implements scm.Driver for cleanup unit tests.
type fakeCleanupSCM struct {
	closedIssues     []closedIssueRecord
	closeIssueErr    error
	deletedBranches  []deletedBranchRecord
	deleteBranchErr  error
	deletedRepos     []deletedRepoRecord
	deleteRepoErr    error
	commitFileCalled bool
	commitFileErr    error
	fileContent      []byte
	getFileErr       error
	openPRs          []forge.ChangeProposal
}

type closedIssueRecord struct {
	owner  string
	repo   string
	number int
}

type deletedBranchRecord struct {
	owner  string
	repo   string
	branch string
}

type deletedRepoRecord struct {
	owner string
	repo  string
}

func (f *fakeCleanupSCM) CloseIssue(_ context.Context, owner, repo string, number int) error {
	if f.closeIssueErr != nil {
		return f.closeIssueErr
	}
	f.closedIssues = append(f.closedIssues, closedIssueRecord{owner: owner, repo: repo, number: number})
	return nil
}

func (f *fakeCleanupSCM) DeleteBranch(_ context.Context, owner, repo, branch string) error {
	if f.deleteBranchErr != nil {
		return f.deleteBranchErr
	}
	f.deletedBranches = append(f.deletedBranches, deletedBranchRecord{owner: owner, repo: repo, branch: branch})
	return nil
}

func (f *fakeCleanupSCM) DeleteRepo(_ context.Context, owner, repo string) error {
	if f.deleteRepoErr != nil {
		return f.deleteRepoErr
	}
	f.deletedRepos = append(f.deletedRepos, deletedRepoRecord{owner: owner, repo: repo})
	return nil
}

// Unused scm.Driver methods — required for interface satisfaction.

func (f *fakeCleanupSCM) CreateIssue(context.Context, string, string, string, string, ...string) (*forge.Issue, error) {
	return nil, nil
}

func (f *fakeCleanupSCM) AddIssueLabels(context.Context, string, string, int, ...string) error {
	return nil
}

func (f *fakeCleanupSCM) AddComment(context.Context, string, string, int, string) (*forge.IssueComment, error) {
	return nil, nil
}

func (f *fakeCleanupSCM) GetIssue(context.Context, string, string, int) (*forge.Issue, error) {
	return nil, nil
}

func (f *fakeCleanupSCM) GetFileContent(context.Context, string, string, string) ([]byte, error) {
	return f.fileContent, f.getFileErr
}

func (f *fakeCleanupSCM) CommitFile(_ context.Context, _, _, _, _ string, _ []byte) error {
	f.commitFileCalled = true
	return f.commitFileErr
}

func (f *fakeCleanupSCM) CreateBranch(context.Context, string, string, string) error {
	return nil
}

func (f *fakeCleanupSCM) CommitFileToBranch(context.Context, string, string, string, string, string, []byte) error {
	return nil
}

func (f *fakeCleanupSCM) CreateChangeProposal(context.Context, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, nil
}

func (f *fakeCleanupSCM) SubmitPullRequestReview(context.Context, string, string, int, string) error {
	return nil
}

func (f *fakeCleanupSCM) CreateRepo(context.Context, string, string, string) error {
	return nil
}

func (f *fakeCleanupSCM) ListOpenChangeProposals(context.Context, string, string) ([]forge.ChangeProposal, error) {
	return f.openPRs, nil
}

func (f *fakeCleanupSCM) ListComments(context.Context, string, string, int) ([]forge.IssueComment, error) {
	return nil, nil
}

func (f *fakeCleanupSCM) EnsureRepoPublic(context.Context, string, string) error {
	return nil
}

func (f *fakeCleanupSCM) GetDefaultBranch(context.Context, string, string) (string, error) {
	return "main", nil
}

func (f *fakeCleanupSCM) GetBranchRef(context.Context, string, string, string) (string, error) {
	return "abc123", nil
}

func (f *fakeCleanupSCM) CreateFork(context.Context, string, string, string) (string, error) {
	return "", nil
}

func (f *fakeCleanupSCM) CommitFileToFork(context.Context, string, string, string, string, string, []byte) error {
	return nil
}

func (f *fakeCleanupSCM) CreateForkChangeProposal(context.Context, string, string, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, nil
}

func (f *fakeCleanupSCM) ListIssueReactions(context.Context, string, string, int) ([]forge.Reaction, error) {
	return nil, nil
}

// --- Issue cleanup tests ---

func TestCleanupScenario_ClosesIssue(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner:   "org",
		RepoName:    "repo",
		IssueNumber: 10,
		SCM:         scmDriver,
	}
	CleanupScenario(w)
	require.Len(t, scmDriver.closedIssues, 1)
	assert.Equal(t, "org", scmDriver.closedIssues[0].owner)
	assert.Equal(t, "repo", scmDriver.closedIssues[0].repo)
	assert.Equal(t, 10, scmDriver.closedIssues[0].number)
}

func TestCleanupScenario_ClosesIssue_Error(t *testing.T) {
	t.Parallel()

	var logged []string
	scmDriver := &fakeCleanupSCM{closeIssueErr: fmt.Errorf("close failed")}
	w := &world.World{
		RepoOwner:   "org",
		RepoName:    "repo",
		IssueNumber: 7,
		SCM:         scmDriver,
		Logf:        func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	CleanupScenario(w)
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "close issue #7")
}

// --- Artifact cleanup tests ---

func TestCleanupScenario_RemovesArtifactDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner:   "org",
		RepoName:    "repo",
		ArtifactDir: dir,
		SCM:         scmDriver,
	}
	CleanupScenario(w)
	// Verify the directory no longer exists.
	assert.NoDirExists(t, dir)
}

// --- Dummy script cleanup tests ---

func TestCleanupScenario_ClearsDummyOps(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		Org:       "org",
		RepoOwner: "org",
		RepoName:  "repo",
		DummyOps:  []runtime.BehaviourOperation{{Op: "echo", Args: "hello"}},
		SCM:       scmDriver,
	}
	CleanupScenario(w)
	assert.True(t, scmDriver.commitFileCalled, "should commit empty ops to clear dummy script")
}

func TestCleanupScenario_ClearsDummyOps_Error(t *testing.T) {
	t.Parallel()

	var logged []string
	scmDriver := &fakeCleanupSCM{commitFileErr: fmt.Errorf("commit failed")}
	w := &world.World{
		Org:       "org",
		RepoOwner: "org",
		RepoName:  "repo",
		DummyOps:  []runtime.BehaviourOperation{{Op: "echo", Args: "hello"}},
		SCM:       scmDriver,
		Logf:      func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	CleanupScenario(w)
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "clear dummy script")
}

// --- Empty identity guard tests ---

func TestCleanupScenario_ClearsDummyOps_EmptyIdentity(t *testing.T) {
	t.Parallel()

	var logged []string
	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		// Org deliberately not set — should log instead of calling SCM.
		DummyOps: []runtime.BehaviourOperation{{Op: "echo", Args: "hello"}},
		SCM:      scmDriver,
		Logf:     func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	CleanupScenario(w)
	assert.False(t, scmDriver.commitFileCalled, "should not call CommitFile with empty Org")
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "no repo configured")
}

// --- Kill switch cleanup tests ---

func TestCleanupScenario_DeactivatesKillSwitch(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{
		fileContent: []byte("version: \"1\"\nkill_switch: true\nroles:\n  - triage\n"),
	}
	w := &world.World{
		Org:                 "org",
		RepoOwner:           "org",
		RepoName:            "repo",
		KillSwitchActivated: true,
		SCM:                 scmDriver,
	}
	CleanupScenario(w)
	assert.True(t, scmDriver.commitFileCalled, "should commit config to deactivate kill switch")
}

func TestCleanupScenario_SkipsKillSwitchWhenNotActivated(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner:           "org",
		RepoName:            "repo",
		KillSwitchActivated: false,
		SCM:                 scmDriver,
	}
	CleanupScenario(w)
	assert.False(t, scmDriver.commitFileCalled, "should not commit when kill switch was not activated")
}

func TestCleanupScenario_DeactivateKillSwitch_Error(t *testing.T) {
	t.Parallel()

	var logged []string
	scmDriver := &fakeCleanupSCM{
		fileContent:   []byte("version: \"1\"\nkill_switch: true\nroles:\n  - triage\n"),
		commitFileErr: fmt.Errorf("commit failed"),
	}
	w := &world.World{
		Org:                 "org",
		RepoOwner:           "org",
		RepoName:            "repo",
		KillSwitchActivated: true,
		SCM:                 scmDriver,
		Logf:                func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	CleanupScenario(w)
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "deactivate kill switch")
}

// --- Allowed remote resources cleanup tests ---

func TestCleanupScenario_RestoresAllowedResources(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{
		fileContent: []byte("version: \"1\"\nallowed_remote_resources:\n  - \"https://raw.githubusercontent.com/org/host/\"\nroles:\n  - triage\n"),
	}
	w := &world.World{
		Org:                        "org",
		RepoOwner:                  "org",
		RepoName:                   "repo",
		AllowedResourcesOverridden: true,
		AllowedResourcesOriginal:   []string{"https://raw.githubusercontent.com/fullsend-ai/fullsend/"},
		SCM:                        scmDriver,
	}
	CleanupScenario(w)
	assert.True(t, scmDriver.commitFileCalled, "should commit config to restore allowed_remote_resources")
}

func TestCleanupScenario_SkipsAllowedResourcesWhenNotOverridden(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner:                  "org",
		RepoName:                   "repo",
		AllowedResourcesOverridden: false,
		SCM:                        scmDriver,
	}
	CleanupScenario(w)
	assert.False(t, scmDriver.commitFileCalled, "should not commit when allowed resources were not overridden")
}

func TestCleanupScenario_RestoreAllowedResources_Error(t *testing.T) {
	t.Parallel()

	var logged []string
	scmDriver := &fakeCleanupSCM{
		fileContent:   []byte("version: \"1\"\nallowed_remote_resources:\n  - \"https://example.com/\"\nroles:\n  - triage\n"),
		commitFileErr: fmt.Errorf("commit failed"),
	}
	w := &world.World{
		Org:                        "org",
		RepoOwner:                  "org",
		RepoName:                   "repo",
		AllowedResourcesOverridden: true,
		AllowedResourcesOriginal:   []string{},
		SCM:                        scmDriver,
		Logf:                       func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	CleanupScenario(w)
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "restore allowed_remote_resources")
}

// --- Agents cleanup tests ---

func TestCleanupScenario_RestoresAgents(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{
		fileContent: []byte("version: \"1\"\nagents:\n  - name: custom\n    source: harness/custom.yaml\nroles:\n  - triage\n"),
	}
	w := &world.World{
		Org:              "org",
		RepoOwner:        "org",
		RepoName:         "repo",
		AgentsOverridden: true,
		AgentsOriginal:   nil, // restore to empty (install-time default)
		SCM:              scmDriver,
	}
	CleanupScenario(w)
	assert.True(t, scmDriver.commitFileCalled, "should commit config to restore agents")
}

func TestCleanupScenario_SkipsAgentsWhenNotOverridden(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		RepoOwner:        "org",
		RepoName:         "repo",
		AgentsOverridden: false,
		SCM:              scmDriver,
	}
	CleanupScenario(w)
	assert.False(t, scmDriver.commitFileCalled, "should not commit when agents were not overridden")
}

func TestCleanupScenario_RestoreAgents_Error(t *testing.T) {
	t.Parallel()

	var logged []string
	scmDriver := &fakeCleanupSCM{
		fileContent:   []byte("version: \"1\"\nagents:\n  - name: custom\n    source: harness/custom.yaml\nroles:\n  - triage\n"),
		commitFileErr: fmt.Errorf("commit failed"),
	}
	w := &world.World{
		Org:              "org",
		RepoOwner:        "org",
		RepoName:         "repo",
		AgentsOverridden: true,
		AgentsOriginal:   nil,
		SCM:              scmDriver,
		Logf:             func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	CleanupScenario(w)
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "restore agents")
}

func TestCleanupScenario_BranchScenarioSweep(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{openPRs: []forge.ChangeProposal{
		{Number: 71, Head: "agent/7-impl"},        // applier PR for this scenario's issue — swept
		{Number: 72, Head: "agent/8-other-issue"}, // different issue's namespace — untouched
	}}
	w := &world.World{
		RepoOwner:       "org",
		RepoName:        "repo",
		IssueNumber:     7,
		SCM:             scmDriver,
		CreatedBranches: []string{"agent/990000099-decoy"},
		CreatedPRNumbers: []int{
			70, // decoy PR tracked at Given time
		},
	}
	CleanupScenario(w)

	var closed []int
	for _, rec := range scmDriver.closedIssues {
		closed = append(closed, rec.number)
	}
	// Issue #7 itself is closed too (IssueNumber > 0 path).
	assert.ElementsMatch(t, []int{7, 70, 71}, closed)

	var deleted []string
	for _, rec := range scmDriver.deletedBranches {
		deleted = append(deleted, rec.branch)
	}
	assert.ElementsMatch(t, []string{"agent/990000099-decoy", "agent/7-impl"}, deleted)
}

func TestCleanupScenario_BranchScenarioSweep_DedupesAlreadyTrackedPR(t *testing.T) {
	t.Parallel()

	// Mirrors the shipped "renamed into the issue namespace" scenario:
	// the head-match assertion already tracked the applier PR before
	// cleanup runs, so the sweep must not close/delete it a second time.
	scmDriver := &fakeCleanupSCM{openPRs: []forge.ChangeProposal{
		{Number: 71, Head: "agent/7-impl"},
	}}
	w := &world.World{
		RepoOwner:        "org",
		RepoName:         "repo",
		IssueNumber:      7,
		SCM:              scmDriver,
		CreatedBranches:  []string{"agent/7-impl"},
		CreatedPRNumbers: []int{71},
	}
	CleanupScenario(w)

	closedCount := 0
	for _, rec := range scmDriver.closedIssues {
		if rec.number == 71 {
			closedCount++
		}
	}
	assert.Equal(t, 1, closedCount, "PR #71 must be closed exactly once")

	deletedCount := 0
	for _, rec := range scmDriver.deletedBranches {
		if rec.branch == "agent/7-impl" {
			deletedCount++
		}
	}
	assert.Equal(t, 1, deletedCount, "branch must be deleted exactly once")
}

func TestCleanupScenario_BranchScenarioSweep_RunsWithoutBranchSteps(t *testing.T) {
	t.Parallel()

	// A code-stage scenario that dispatches without any Given branch/PR
	// step (CreatedBranches stays nil) must still sweep the applier's
	// namespace — the sweep is gated on IssueNumber alone.
	scmDriver := &fakeCleanupSCM{openPRs: []forge.ChangeProposal{
		{Number: 71, Head: "agent/7-impl"},
	}}
	w := &world.World{
		RepoOwner:   "org",
		RepoName:    "repo",
		IssueNumber: 7,
		SCM:         scmDriver,
	}
	CleanupScenario(w)

	var closed []int
	for _, rec := range scmDriver.closedIssues {
		closed = append(closed, rec.number)
	}
	assert.Contains(t, closed, 71)
}

// --- Retry helper tests ---

func TestCleanupRetry_SucceedsImmediately(t *testing.T) {
	speedUpCleanupRetries(t)

	calls := 0
	err := cleanupRetry(nil, "test-op", func() error {
		calls++
		return nil
	})
	assert.NoError(t, err)
	assert.Equal(t, 1, calls)
}

func TestCleanupRetry_TransientThenSuccess(t *testing.T) {
	speedUpCleanupRetries(t)

	calls := 0
	transientErr := &fakeTransientError{msg: "503 service unavailable"}
	err := cleanupRetry(nil, "test-op", func() error {
		calls++
		if calls < 3 {
			return transientErr
		}
		return nil
	})
	assert.NoError(t, err)
	assert.Equal(t, 3, calls)
}

func TestCleanupRetry_TransientExhausted(t *testing.T) {
	speedUpCleanupRetries(t)

	transientErr := &fakeTransientError{msg: "503 service unavailable"}
	calls := 0
	var logged []string
	logf := func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	err := cleanupRetry(logf, "test-op", func() error {
		calls++
		return transientErr
	})
	assert.ErrorIs(t, err, transientErr)
	assert.Equal(t, 3, calls) // default cleanupMaxAttempts
	// Should have logged retry attempts (attempts 1 and 2, but not the last)
	assert.Len(t, logged, 2)
	assert.Contains(t, logged[0], "transient error")
	assert.Contains(t, logged[0], "attempt 1/3")
}

func TestCleanupRetry_NonTransientNoRetry(t *testing.T) {
	speedUpCleanupRetries(t)

	nonTransientErr := fmt.Errorf("401 unauthorized")
	calls := 0
	err := cleanupRetry(nil, "test-op", func() error {
		calls++
		return nonTransientErr
	})
	assert.ErrorIs(t, err, nonTransientErr)
	assert.Equal(t, 1, calls, "non-transient error should not be retried")
}

func TestCleanupScenario_RetriesTransientCloseIssue(t *testing.T) {
	speedUpCleanupRetries(t)

	calls := 0
	transientErr := &fakeTransientError{msg: "503"}
	scm := &fakeRetryCleanupSCM{
		closeIssueFn: func(_ context.Context, _, _ string, _ int) error {
			calls++
			if calls == 1 {
				return transientErr
			}
			return nil
		},
	}
	w := &world.World{
		RepoOwner:   "org",
		RepoName:    "repo",
		IssueNumber: 42,
		SCM:         scm,
	}
	CleanupScenario(w)
	assert.Equal(t, 2, calls, "should have retried on transient error")
}

func TestCleanupScenario_RetriesTransientCommitFile(t *testing.T) {
	speedUpCleanupRetries(t)

	calls := 0
	transientErr := &fakeTransientError{msg: "503"}
	scm := &fakeRetryCleanupSCM{
		commitFileFn: func(_ context.Context, _, _, _, _ string, _ []byte) error {
			calls++
			if calls == 1 {
				return transientErr
			}
			return nil
		},
	}
	w := &world.World{
		Org:       "org",
		RepoOwner: "org",
		RepoName:  "repo",
		DummyOps:  []runtime.BehaviourOperation{{Op: "echo", Args: "hello"}},
		SCM:       scm,
	}
	CleanupScenario(w)
	assert.Equal(t, 2, calls, "should have retried commit on transient error")
}

// speedUpCleanupRetries sets cleanupBaseDelay to 1ms for the duration
// of the test so retries don't slow down the test suite.
func speedUpCleanupRetries(t *testing.T) {
	t.Helper()
	origDelay := cleanupBaseDelay
	cleanupBaseDelay = 1 * time.Millisecond
	t.Cleanup(func() { cleanupBaseDelay = origDelay })
}

// fakeTransientError implements the transientReporter interface
// that forge.IsTransient checks, making it report as transient.
type fakeTransientError struct {
	msg string
}

func (e *fakeTransientError) Error() string     { return e.msg }
func (e *fakeTransientError) IsTransient() bool { return true }

// fakeRetryCleanupSCM is an scm.Driver implementation that uses
// function callbacks for methods exercised in retry tests. Methods
// without callbacks return nil.
type fakeRetryCleanupSCM struct {
	closeIssueFn func(ctx context.Context, owner, repo string, number int) error
	commitFileFn func(ctx context.Context, owner, repo, path, msg string, content []byte) error
	deleteRepoFn func(ctx context.Context, owner, repo string) error
}

func (f *fakeRetryCleanupSCM) CloseIssue(ctx context.Context, owner, repo string, number int) error {
	if f.closeIssueFn != nil {
		return f.closeIssueFn(ctx, owner, repo, number)
	}
	return nil
}

func (f *fakeRetryCleanupSCM) CommitFile(ctx context.Context, owner, repo, path, msg string, content []byte) error {
	if f.commitFileFn != nil {
		return f.commitFileFn(ctx, owner, repo, path, msg, content)
	}
	return nil
}

func (f *fakeRetryCleanupSCM) DeleteRepo(ctx context.Context, owner, repo string) error {
	if f.deleteRepoFn != nil {
		return f.deleteRepoFn(ctx, owner, repo)
	}
	return nil
}

func (f *fakeRetryCleanupSCM) DeleteBranch(context.Context, string, string, string) error {
	return nil
}

func (f *fakeRetryCleanupSCM) ListOpenChangeProposals(context.Context, string, string) ([]forge.ChangeProposal, error) {
	return nil, nil
}

func (f *fakeRetryCleanupSCM) CreateIssue(context.Context, string, string, string, string, ...string) (*forge.Issue, error) {
	return nil, nil
}

func (f *fakeRetryCleanupSCM) AddIssueLabels(context.Context, string, string, int, ...string) error {
	return nil
}

func (f *fakeRetryCleanupSCM) AddComment(context.Context, string, string, int, string) (*forge.IssueComment, error) {
	return nil, nil
}

func (f *fakeRetryCleanupSCM) GetIssue(context.Context, string, string, int) (*forge.Issue, error) {
	return nil, nil
}

func (f *fakeRetryCleanupSCM) GetFileContent(context.Context, string, string, string) ([]byte, error) {
	return nil, nil
}

func (f *fakeRetryCleanupSCM) CreateBranch(context.Context, string, string, string) error {
	return nil
}

func (f *fakeRetryCleanupSCM) CommitFileToBranch(context.Context, string, string, string, string, string, []byte) error {
	return nil
}

func (f *fakeRetryCleanupSCM) CreateChangeProposal(context.Context, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, nil
}

func (f *fakeRetryCleanupSCM) SubmitPullRequestReview(context.Context, string, string, int, string) error {
	return nil
}

func (f *fakeRetryCleanupSCM) CreateRepo(context.Context, string, string, string) error {
	return nil
}

func (f *fakeRetryCleanupSCM) ListComments(context.Context, string, string, int) ([]forge.IssueComment, error) {
	return nil, nil
}

func (f *fakeRetryCleanupSCM) ListIssueReactions(context.Context, string, string, int) ([]forge.Reaction, error) {
	return nil, nil
}

func (f *fakeRetryCleanupSCM) EnsureRepoPublic(context.Context, string, string) error {
	return nil
}

func (f *fakeRetryCleanupSCM) GetDefaultBranch(context.Context, string, string) (string, error) {
	return "main", nil
}

func (f *fakeRetryCleanupSCM) GetBranchRef(context.Context, string, string, string) (string, error) {
	return "abc123", nil
}

func (f *fakeRetryCleanupSCM) CreateFork(context.Context, string, string, string) (string, error) {
	return "", nil
}

func (f *fakeRetryCleanupSCM) CommitFileToFork(context.Context, string, string, string, string, string, []byte) error {
	return nil
}

func (f *fakeRetryCleanupSCM) CreateForkChangeProposal(context.Context, string, string, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, nil
}
