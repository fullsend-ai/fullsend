package repos

import (
	"context"
	"fmt"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPollerPipelineUserIDs(t *testing.T) {
	ids := PollerPipelineUserIDs([]ProjectAccessToken{
		{Name: gitlabroles.PollerTokenName, Active: true, UserID: 11},
		{Name: gitlabroles.SharedTokenName, Active: true, UserID: 12},
		{Name: gitlabroles.AnalystTokenName, Active: true, UserID: 13},
		{Name: gitlabroles.CoderTokenName, Active: true, UserID: 14},
		{Name: gitlabroles.PollerTokenName, Active: false, UserID: 15},
		{Name: gitlabroles.PollerTokenName, Active: true, Revoked: true, UserID: 16},
		{Name: gitlabroles.PollerTokenName, Active: true, UserID: 0},
		{Name: gitlabroles.PollerTokenName, Active: true, UserID: 11}, // duplicate
	})
	assert.Equal(t, []int{11, 12}, ids)
}

func TestPollerCanCreatePipeline(t *testing.T) {
	t.Parallel()
	poller := []int{99}

	t.Run("unprotected", func(t *testing.T) {
		assert.True(t, PollerCanCreatePipeline(nil, poller))
	})
	t.Run("developer merge", func(t *testing.T) {
		rule := &forge.ProtectedBranchRule{
			MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: 30}},
		}
		assert.True(t, PollerCanCreatePipeline(rule, nil))
	})
	t.Run("reporter merge includes developer", func(t *testing.T) {
		rule := &forge.ProtectedBranchRule{
			MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: 20}},
		}
		assert.True(t, PollerCanCreatePipeline(rule, nil))
	})
	t.Run("developer push", func(t *testing.T) {
		rule := &forge.ProtectedBranchRule{
			PushAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: 30}},
		}
		assert.True(t, PollerCanCreatePipeline(rule, nil))
	})
	t.Run("maintainer only", func(t *testing.T) {
		rule := &forge.ProtectedBranchRule{
			MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: 40}},
			PushAccessLevels:  []forge.ProtectedBranchAccess{{AccessLevel: 40}},
		}
		assert.False(t, PollerCanCreatePipeline(rule, poller))
	})
	t.Run("no one", func(t *testing.T) {
		rule := &forge.ProtectedBranchRule{
			MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: 0}},
			PushAccessLevels:  []forge.ProtectedBranchAccess{{AccessLevel: 0}},
		}
		assert.False(t, PollerCanCreatePipeline(rule, poller))
	})
	t.Run("poller user merge", func(t *testing.T) {
		rule := &forge.ProtectedBranchRule{
			MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: 40}, {UserID: 99}},
			PushAccessLevels:  []forge.ProtectedBranchAccess{{AccessLevel: 40}},
		}
		assert.True(t, PollerCanCreatePipeline(rule, poller))
	})
	t.Run("poller user push", func(t *testing.T) {
		rule := &forge.ProtectedBranchRule{
			MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: 40}},
			PushAccessLevels:  []forge.ProtectedBranchAccess{{UserID: 99}},
		}
		assert.True(t, PollerCanCreatePipeline(rule, poller))
	})
	t.Run("other user does not count", func(t *testing.T) {
		rule := &forge.ProtectedBranchRule{
			MergeAccessLevels: []forge.ProtectedBranchAccess{{UserID: 7}},
		}
		assert.False(t, PollerCanCreatePipeline(rule, poller))
	})
	t.Run("group grant does not count", func(t *testing.T) {
		rule := &forge.ProtectedBranchRule{
			MergeAccessLevels: []forge.ProtectedBranchAccess{{GroupID: 5, AccessLevel: 30}},
		}
		assert.False(t, PollerCanCreatePipeline(rule, poller))
	})
}

func maintainerOnlyRule(branch string) *forge.ProtectedBranchRule {
	return &forge.ProtectedBranchRule{
		Name:              branch,
		MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: 40}},
		PushAccessLevels:  []forge.ProtectedBranchAccess{{AccessLevel: 40}},
	}
}

func seedRepo(fc *forge.FakeClient, owner, repo, branch string) {
	fc.Repos = []forge.Repository{{
		Name: repo, FullName: owner + "/" + repo, DefaultBranch: branch,
	}}
}

func TestEnsureGitLabPollerPipelineAccess_Unprotected(t *testing.T) {
	fc := forge.NewFakeClient()
	seedRepo(fc, "group", "project", "main")

	res, err := EnsureGitLabPollerPipelineAccess(context.Background(), fc, "group", "project", []int{99}, false)
	require.NoError(t, err)
	assert.Equal(t, "main", res.Branch)
	assert.Empty(t, res.Action)
	assert.Empty(t, fc.GrantedProtectedBranchMergeUsers)
}

func TestEnsureGitLabPollerPipelineAccess_DeveloperPreset(t *testing.T) {
	fc := forge.NewFakeClient()
	seedRepo(fc, "group", "project", "main")
	fc.ProtectedBranches["group/project/main"] = true

	res, err := EnsureGitLabPollerPipelineAccess(context.Background(), fc, "group", "project", nil, false)
	require.NoError(t, err)
	assert.Empty(t, res.Action)
	assert.Empty(t, fc.GrantedProtectedBranchMergeUsers)
}

func TestEnsureGitLabPollerPipelineAccess_GrantsPollerUser(t *testing.T) {
	fc := forge.NewFakeClient()
	seedRepo(fc, "group", "project", "release")
	fc.ProtectedBranchRules["group/project/release"] = maintainerOnlyRule("release")

	res, err := EnsureGitLabPollerPipelineAccess(context.Background(), fc, "group", "project", []int{99, 100}, false)
	require.NoError(t, err)
	assert.Equal(t, pipelineAccessGrant, res.Action)
	assert.Equal(t, "release", res.Branch)
	assert.Equal(t, []int{99, 100}, res.UserIDs)
	require.Len(t, fc.GrantedProtectedBranchMergeUsers, 2)
	assert.Equal(t, 99, fc.GrantedProtectedBranchMergeUsers[0].UserID)
	assert.Equal(t, 100, fc.GrantedProtectedBranchMergeUsers[1].UserID)

	rule, err := fc.GetProtectedBranch(context.Background(), "group", "project", "release")
	require.NoError(t, err)
	assert.True(t, PollerCanCreatePipeline(rule, []int{99}))
}

func TestEnsureGitLabPollerPipelineAccess_DryRun(t *testing.T) {
	fc := forge.NewFakeClient()
	seedRepo(fc, "group", "project", "main")
	fc.ProtectedBranchRules["group/project/main"] = maintainerOnlyRule("main")

	res, err := EnsureGitLabPollerPipelineAccess(context.Background(), fc, "group", "project", []int{99}, true)
	require.NoError(t, err)
	assert.Equal(t, pipelineAccessWouldGrant, res.Action)
	assert.Contains(t, res.Detail, "Would grant")
	assert.Empty(t, fc.GrantedProtectedBranchMergeUsers)
}

func TestEnsureGitLabPollerPipelineAccess_DryRunNoUserIDs(t *testing.T) {
	fc := forge.NewFakeClient()
	seedRepo(fc, "group", "project", "main")
	fc.ProtectedBranchRules["group/project/main"] = maintainerOnlyRule("main")

	res, err := EnsureGitLabPollerPipelineAccess(context.Background(), fc, "group", "project", nil, true)
	require.NoError(t, err)
	assert.Equal(t, pipelineAccessWouldGrant, res.Action)
}

func TestEnsureGitLabPollerPipelineAccess_NoUserIDsFails(t *testing.T) {
	fc := forge.NewFakeClient()
	seedRepo(fc, "group", "project", "main")
	fc.ProtectedBranchRules["group/project/main"] = maintainerOnlyRule("main")

	_, err := EnsureGitLabPollerPipelineAccess(context.Background(), fc, "group", "project", nil, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no poller project-access-token user")
	assert.Empty(t, fc.GrantedProtectedBranchMergeUsers)
}

func TestEnsureGitLabPollerPipelineAccess_GrantError(t *testing.T) {
	fc := forge.NewFakeClient()
	seedRepo(fc, "group", "project", "main")
	fc.ProtectedBranchRules["group/project/main"] = maintainerOnlyRule("main")
	fc.Errors["GrantProtectedBranchMergeUser"] = fmt.Errorf("403 forbidden")

	_, err := EnsureGitLabPollerPipelineAccess(context.Background(), fc, "group", "project", []int{99}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403 forbidden")
}

func TestEnsureGitLabPollerPipelineAccess_NotSupported(t *testing.T) {
	fc := forge.NewFakeClient()
	seedRepo(fc, "org", "repo", "main")
	fc.Errors["GetProtectedBranch"] = forge.ErrNotSupported

	res, err := EnsureGitLabPollerPipelineAccess(context.Background(), fc, "org", "repo", []int{99}, false)
	require.NoError(t, err)
	assert.Empty(t, res.Action)
}

func TestEnsureGitLabPollerPipelineAccess_GetRepoError(t *testing.T) {
	fc := forge.NewFakeClient()
	_, err := EnsureGitLabPollerPipelineAccess(context.Background(), fc, "group", "missing", nil, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading repo")
}

func TestEnsureGitLabPollerPipelineAccess_GetProtectedBranchError(t *testing.T) {
	fc := forge.NewFakeClient()
	seedRepo(fc, "group", "project", "main")
	fc.Errors["GetProtectedBranch"] = fmt.Errorf("lookup failed")
	_, err := EnsureGitLabPollerPipelineAccess(context.Background(), fc, "group", "project", []int{99}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading protected branch")
}

func TestEnsureGitLabPollerPipelineAccess_NilClient(t *testing.T) {
	_, err := EnsureGitLabPollerPipelineAccess(context.Background(), nil, "g", "p", nil, false)
	require.Error(t, err)
}

func TestEnsureGitLabPollerPipelineAccess_EmptyDefaultBranch(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{{FullName: "group/project", Name: "project"}}
	fc.ProtectedBranchRules["group/project/main"] = maintainerOnlyRule("main")

	res, err := EnsureGitLabPollerPipelineAccess(context.Background(), fc, "group", "project", []int{99}, true)
	require.NoError(t, err)
	assert.Equal(t, "main", res.Branch)
	assert.Equal(t, pipelineAccessWouldGrant, res.Action)
}

func TestEnsureGitLabPollerPipelineAccess_AlreadyGrantedUser(t *testing.T) {
	fc := forge.NewFakeClient()
	seedRepo(fc, "group", "project", "main")
	fc.ProtectedBranchRules["group/project/main"] = &forge.ProtectedBranchRule{
		Name: "main",
		MergeAccessLevels: []forge.ProtectedBranchAccess{
			{AccessLevel: 40},
			{UserID: 99},
		},
		PushAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: 40}},
	}

	res, err := EnsureGitLabPollerPipelineAccess(context.Background(), fc, "group", "project", []int{99}, false)
	require.NoError(t, err)
	assert.Empty(t, res.Action)
	assert.Empty(t, fc.GrantedProtectedBranchMergeUsers)
}

func TestAppendGitLabPipelineRefStatus(t *testing.T) {
	fc := forge.NewFakeClient()
	seedRepo(fc, "group", "project", "main")
	fc.ProtectedBranchRules["group/project/main"] = maintainerOnlyRule("main")
	status := &RepoStatus{Owner: "group", Repo: "project"}

	added := AppendGitLabPipelineRefStatus(context.Background(), fc, "group", "project", nil, status)
	assert.True(t, added)
	require.Len(t, status.Drifts, 1)
	assert.Equal(t, gitLabPipelineRefDriftField, status.Drifts[0].Field)
	assert.Equal(t, "poller can create pipelines on main", status.Drifts[0].Expected)
	assert.Contains(t, status.Drifts[0].Actual, "maintainer+")

	added = AppendGitLabPipelineRefStatus(context.Background(), fc, "group", "project", nil, status)
	assert.False(t, added)
	assert.Len(t, status.Drifts, 1)
}

func TestAppendGitLabPipelineRefStatus_NoDriftWhenAllowed(t *testing.T) {
	fc := forge.NewFakeClient()
	seedRepo(fc, "group", "project", "main")
	fc.ProtectedBranches["group/project/main"] = true
	status := &RepoStatus{}

	added := AppendGitLabPipelineRefStatus(context.Background(), fc, "group", "project", nil, status)
	assert.False(t, added)
	assert.Empty(t, status.Drifts)
}

func TestAppendGitLabPipelineRefStatus_UserGrantClearsDrift(t *testing.T) {
	fc := forge.NewFakeClient()
	seedRepo(fc, "group", "project", "main")
	fc.ProtectedBranchRules["group/project/main"] = &forge.ProtectedBranchRule{
		Name:              "main",
		MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: 40}, {UserID: 99}},
		PushAccessLevels:  []forge.ProtectedBranchAccess{{AccessLevel: 40}},
	}
	status := &RepoStatus{}

	added := AppendGitLabPipelineRefStatus(context.Background(), fc, "group", "project", []int{99}, status)
	assert.False(t, added)
	assert.Empty(t, status.Drifts)
}

func TestAppendGitLabPipelineRefStatus_SkipsErrors(t *testing.T) {
	assert.False(t, AppendGitLabPipelineRefStatus(context.Background(), nil, "g", "p", nil, &RepoStatus{}))
	assert.False(t, AppendGitLabPipelineRefStatus(context.Background(), forge.NewFakeClient(), "g", "p", nil, nil))

	fc := forge.NewFakeClient()
	assert.False(t, AppendGitLabPipelineRefStatus(context.Background(), fc, "g", "missing", nil, &RepoStatus{}))

	fc2 := forge.NewFakeClient()
	seedRepo(fc2, "org", "repo", "main")
	fc2.Errors["GetProtectedBranch"] = forge.ErrNotSupported
	assert.False(t, AppendGitLabPipelineRefStatus(context.Background(), fc2, "org", "repo", nil, &RepoStatus{}))
}

func TestDescribePipelineAccess(t *testing.T) {
	assert.Equal(t, "unprotected", describePipelineAccess(nil))
	assert.Equal(t, "no merge or push grants", describePipelineAccess(&forge.ProtectedBranchRule{Name: "main"}))
	got := describePipelineAccess(&forge.ProtectedBranchRule{
		MergeAccessLevels: []forge.ProtectedBranchAccess{
			{AccessLevel: 0},
			{UserID: 9},
			{GroupID: 3},
			{AccessLevel: 60},
		},
	})
	assert.Contains(t, got, "no one")
	assert.Contains(t, got, "user:9")
	assert.Contains(t, got, "group:3")
	assert.Contains(t, got, "access_level:60")
}
