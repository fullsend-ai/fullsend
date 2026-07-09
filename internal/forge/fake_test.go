package forge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFakeClient_ListOrgRepos(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{
		Repos: []Repository{
			{Name: "active", FullName: "org/active"},
			{Name: "archived", FullName: "org/archived", Archived: true},
			{Name: "forked", FullName: "org/forked", Fork: true},
			{Name: "also-active", FullName: "org/also-active"},
		},
	}

	repos, err := fc.ListOrgRepos(ctx, "org")
	require.NoError(t, err)
	assert.Len(t, repos, 2)
	assert.Equal(t, "active", repos[0].Name)
	assert.Equal(t, "also-active", repos[1].Name)
}

func TestFakeClient_CreateRepo(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{}

	repo, err := fc.CreateRepo(ctx, "org", "new-repo", "a description", true)
	require.NoError(t, err)
	assert.Equal(t, "new-repo", repo.Name)
	assert.Equal(t, "org/new-repo", repo.FullName)
	assert.True(t, repo.Private)
	assert.Equal(t, "main", repo.DefaultBranch)

	require.Len(t, fc.CreatedRepos, 1)
	assert.Equal(t, "new-repo", fc.CreatedRepos[0].Name)
}

func TestFakeClient_CreateFile(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{}

	content := []byte("hello world")
	err := fc.CreateFile(ctx, "owner", "repo", "README.md", "initial commit", content)
	require.NoError(t, err)

	require.Len(t, fc.CreatedFiles, 1)
	rec := fc.CreatedFiles[0]
	assert.Equal(t, "owner", rec.Owner)
	assert.Equal(t, "repo", rec.Repo)
	assert.Equal(t, "README.md", rec.Path)
	assert.Equal(t, "initial commit", rec.Message)
	assert.Equal(t, content, rec.Content)
	assert.Empty(t, rec.Branch)
}

func TestFakeClient_CreateFileOnBranch(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{}

	content := []byte("branch content")
	err := fc.CreateFileOnBranch(ctx, "owner", "repo", "feature", "file.txt", "add file", content)
	require.NoError(t, err)

	require.Len(t, fc.CreatedFiles, 1)
	assert.Equal(t, "feature", fc.CreatedFiles[0].Branch)
}

func TestFakeClient_DeleteFiles(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{
		FileContents: map[string][]byte{
			"owner/repo/a.txt": []byte("a"),
			"owner/repo/b.txt": []byte("b"),
		},
	}

	deleted, err := fc.DeleteFiles(ctx, "owner", "repo", "cleanup", []string{"a.txt", "missing.txt", "b.txt"})
	require.NoError(t, err)
	assert.Equal(t, 2, deleted)
	assert.Len(t, fc.DeletedFiles, 2)
	_, ok := fc.FileContents["owner/repo/a.txt"]
	assert.False(t, ok)
}

func TestFakeClient_GetWorkflow(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{
		Workflows: map[string]*Workflow{
			"owner/repo/ci.yml": {Name: "CI", Path: ".github/workflows/ci.yml", State: "active"},
		},
	}

	wf, err := fc.GetWorkflow(ctx, "owner", "repo", "ci.yml")
	require.NoError(t, err)
	assert.Equal(t, "CI", wf.Name)

	wf, err = fc.GetWorkflow(ctx, "owner", "repo", "other.yml")
	require.NoError(t, err)
	assert.Equal(t, "other.yml", wf.Name)
	assert.Equal(t, "active", wf.State)
}

func TestFakeClient_GetFileContent(t *testing.T) {
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		fc := &FakeClient{
			FileContents: map[string][]byte{
				"owner/repo/config.yaml": []byte("key: value"),
			},
		}

		data, err := fc.GetFileContent(ctx, "owner", "repo", "config.yaml")
		require.NoError(t, err)
		assert.Equal(t, []byte("key: value"), data)
	})

	t.Run("not found", func(t *testing.T) {
		fc := &FakeClient{
			FileContents: map[string][]byte{},
		}

		_, err := fc.GetFileContent(ctx, "owner", "repo", "missing.txt")
		require.Error(t, err)
		assert.True(t, IsNotFound(err), "expected IsNotFound to be true")
	})
}

func TestFakeClient_CreateOrUpdateFile(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{}

	content := []byte("updated")
	err := fc.CreateOrUpdateFile(ctx, "owner", "repo", "file.txt", "update", content)
	require.NoError(t, err)

	// Should be recorded.
	require.Len(t, fc.CreatedFiles, 1)

	// Should also be stored in FileContents for later retrieval.
	data, err := fc.GetFileContent(ctx, "owner", "repo", "file.txt")
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestFakeClient_DeleteRepo(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{}

	err := fc.DeleteRepo(ctx, "owner", "repo")
	require.NoError(t, err)
	assert.Equal(t, []string{"owner/repo"}, fc.DeletedRepos)
}

func TestFakeClient_CreateBranch(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{}

	err := fc.CreateBranch(ctx, "owner", "repo", "feature-branch")
	require.NoError(t, err)
	assert.Equal(t, []string{"owner/repo/feature-branch"}, fc.CreatedBranches)
}

func TestFakeClient_CreateChangeProposal(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{}

	cp, err := fc.CreateChangeProposal(ctx, "owner", "repo", "title", "body", "head", "main")
	require.NoError(t, err)
	assert.Equal(t, 1, cp.Number)
	assert.Equal(t, "title", cp.Title)
	assert.Contains(t, cp.URL, "owner/repo/pull/1")

	// Second proposal gets incremented number.
	cp2, err := fc.CreateChangeProposal(ctx, "owner", "repo", "title2", "body2", "head2", "main")
	require.NoError(t, err)
	assert.Equal(t, 2, cp2.Number)

	assert.Len(t, fc.CreatedProposals, 2)
}

func TestFakeClient_GetAuthenticatedUser(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{AuthenticatedUser: "test-bot"}

	user, err := fc.GetAuthenticatedUser(ctx)
	require.NoError(t, err)
	assert.Equal(t, "test-bot", user)
}

func TestFakeClient_GetAuthenticatedUserIdentity(t *testing.T) {
	ctx := context.Background()

	t.Run("returns configured identity", func(t *testing.T) {
		fc := &FakeClient{
			AuthenticatedUserIdentity: &UserIdentity{Name: "Test User", Email: "test@example.com"},
		}
		id, err := fc.GetAuthenticatedUserIdentity(ctx)
		require.NoError(t, err)
		assert.Equal(t, "Test User", id.Name)
		assert.Equal(t, "test@example.com", id.Email)
	})

	t.Run("returns ErrNotFound when not configured", func(t *testing.T) {
		fc := &FakeClient{}
		_, err := fc.GetAuthenticatedUserIdentity(ctx)
		require.Error(t, err)
		assert.True(t, IsNotFound(err))
	})
}

func TestFakeClient_Secrets(t *testing.T) {
	ctx := context.Background()

	t.Run("create", func(t *testing.T) {
		fc := &FakeClient{}
		err := fc.CreateRepoSecret(ctx, "owner", "repo", "TOKEN", "s3cret")
		require.NoError(t, err)
		require.Len(t, fc.CreatedSecrets, 1)
		assert.Equal(t, "TOKEN", fc.CreatedSecrets[0].Name)
		assert.Equal(t, "s3cret", fc.CreatedSecrets[0].Value)
	})

	t.Run("exists", func(t *testing.T) {
		fc := &FakeClient{
			Secrets: map[string]bool{"owner/repo/TOKEN": true},
		}
		exists, err := fc.RepoSecretExists(ctx, "owner", "repo", "TOKEN")
		require.NoError(t, err)
		assert.True(t, exists)

		exists, err = fc.RepoSecretExists(ctx, "owner", "repo", "MISSING")
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("exists nil map", func(t *testing.T) {
		fc := &FakeClient{}
		exists, err := fc.RepoSecretExists(ctx, "owner", "repo", "TOKEN")
		require.NoError(t, err)
		assert.False(t, exists)
	})
}

func TestFakeClient_Variables(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{}

	err := fc.CreateOrUpdateRepoVariable(ctx, "owner", "repo", "ENV", "production")
	require.NoError(t, err)
	require.Len(t, fc.Variables, 1)
	assert.Equal(t, "ENV", fc.Variables[0].Name)
	assert.Equal(t, "production", fc.Variables[0].Value)
}

func TestFakeClient_WorkflowRuns(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{
		WorkflowRuns: map[string]*WorkflowRun{
			"owner/repo/ci.yml": {
				ID:         42,
				Name:       "CI",
				Status:     "completed",
				Conclusion: "success",
			},
		},
	}

	t.Run("get latest", func(t *testing.T) {
		run, err := fc.GetLatestWorkflowRun(ctx, "owner", "repo", "ci.yml")
		require.NoError(t, err)
		assert.Equal(t, 42, run.ID)
		assert.Equal(t, "success", run.Conclusion)
	})

	t.Run("get latest not found", func(t *testing.T) {
		_, err := fc.GetLatestWorkflowRun(ctx, "owner", "repo", "missing.yml")
		require.Error(t, err)
	})

	t.Run("get by id", func(t *testing.T) {
		run, err := fc.GetWorkflowRun(ctx, "owner", "repo", 42)
		require.NoError(t, err)
		assert.Equal(t, "CI", run.Name)
	})

	t.Run("get by id not found", func(t *testing.T) {
		_, err := fc.GetWorkflowRun(ctx, "owner", "repo", 999)
		require.Error(t, err)
	})
}

func TestFakeClient_Installations(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{
		Installations: []Installation{
			{ID: 1, AppID: 100, AppSlug: "fullsend-bot"},
		},
	}

	installs, err := fc.ListOrgInstallations(ctx, "org")
	require.NoError(t, err)
	require.Len(t, installs, 1)
	assert.Equal(t, "fullsend-bot", installs[0].AppSlug)
}

func TestFakeClient_GetAppClientID(t *testing.T) {
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		fc := &FakeClient{
			AppClientIDs: map[string]string{
				"myorg-fullsend": "Iv1.abc123",
			},
		}
		clientID, err := fc.GetAppClientID(ctx, "myorg-fullsend")
		require.NoError(t, err)
		assert.Equal(t, "Iv1.abc123", clientID)
	})

	t.Run("not found", func(t *testing.T) {
		fc := &FakeClient{}
		_, err := fc.GetAppClientID(ctx, "nonexistent")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("error injection", func(t *testing.T) {
		fc := &FakeClient{
			Errors: map[string]error{"GetAppClientID": errors.New("api down")},
		}
		_, err := fc.GetAppClientID(ctx, "myorg-fullsend")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "api down")
	})
}

func TestFakeClient_OrgSecretExists(t *testing.T) {
	ctx := context.Background()

	t.Run("exists", func(t *testing.T) {
		fc := &FakeClient{
			OrgSecrets: map[string]bool{"myorg/TOKEN": true},
		}
		exists, err := fc.OrgSecretExists(ctx, "myorg", "TOKEN")
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("not exists", func(t *testing.T) {
		fc := &FakeClient{
			OrgSecrets: map[string]bool{},
		}
		exists, err := fc.OrgSecretExists(ctx, "myorg", "MISSING")
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("nil map", func(t *testing.T) {
		fc := &FakeClient{}
		exists, err := fc.OrgSecretExists(ctx, "myorg", "TOKEN")
		require.NoError(t, err)
		assert.False(t, exists)
	})
}

func TestFakeClient_CreateOrgSecret(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{}

	err := fc.CreateOrgSecret(ctx, "myorg", "DISPATCH_TOKEN", "secret-value", []int64{100, 200})
	require.NoError(t, err)

	// Should be recorded.
	require.Len(t, fc.CreatedOrgSecrets, 1)
	assert.Equal(t, "myorg", fc.CreatedOrgSecrets[0].Org)
	assert.Equal(t, "DISPATCH_TOKEN", fc.CreatedOrgSecrets[0].Name)
	assert.Equal(t, "secret-value", fc.CreatedOrgSecrets[0].Value)
	assert.Equal(t, []int64{100, 200}, fc.CreatedOrgSecrets[0].RepoIDs)

	// Should be queryable.
	exists, err := fc.OrgSecretExists(ctx, "myorg", "DISPATCH_TOKEN")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestFakeClient_OrgVariableExists(t *testing.T) {
	ctx := context.Background()

	t.Run("exists", func(t *testing.T) {
		fc := &FakeClient{
			OrgVariables: map[string]bool{"myorg/DISPATCH_URL": true},
		}
		exists, err := fc.OrgVariableExists(ctx, "myorg", "DISPATCH_URL")
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("not exists", func(t *testing.T) {
		fc := &FakeClient{
			OrgVariables: map[string]bool{},
		}
		exists, err := fc.OrgVariableExists(ctx, "myorg", "MISSING")
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("nil map", func(t *testing.T) {
		fc := &FakeClient{}
		exists, err := fc.OrgVariableExists(ctx, "myorg", "VAR")
		require.NoError(t, err)
		assert.False(t, exists)
	})
}

func TestFakeClient_CreateOrUpdateOrgVariable(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{}

	err := fc.CreateOrUpdateOrgVariable(ctx, "myorg", "DISPATCH_URL", "https://func.example.com", []int64{100, 200})
	require.NoError(t, err)

	// Should be recorded.
	require.Len(t, fc.CreatedOrgVariables, 1)
	assert.Equal(t, "myorg", fc.CreatedOrgVariables[0].Org)
	assert.Equal(t, "DISPATCH_URL", fc.CreatedOrgVariables[0].Name)
	assert.Equal(t, "https://func.example.com", fc.CreatedOrgVariables[0].Value)
	assert.Equal(t, []int64{100, 200}, fc.CreatedOrgVariables[0].RepoIDs)

	// Should be queryable.
	exists, err := fc.OrgVariableExists(ctx, "myorg", "DISPATCH_URL")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestFakeClient_GetOrgVariable(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{
		OrgVariables:      map[string]bool{"myorg/FOREIGN": true},
		OrgVariableValues: map[string]string{"myorg/FOREIGN": "caller/repo"},
	}

	value, exists, err := fc.GetOrgVariable(ctx, "myorg", "FOREIGN")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, "caller/repo", value)

	_, exists, err = fc.GetOrgVariable(ctx, "myorg", "MISSING")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestFakeClient_ListOrgVariables(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{
		OrgVariables:      map[string]bool{"myorg/A": true, "myorg/B": true, "other/C": true},
		OrgVariableValues: map[string]string{"myorg/A": "1", "myorg/B": "2"},
	}

	vars, err := fc.ListOrgVariables(ctx, "myorg")
	require.NoError(t, err)
	require.Len(t, vars, 2)
}

func TestFakeClient_IsInstallationToken(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{InstallationToken: true}
	ok, err := fc.IsInstallationToken(ctx)
	require.NoError(t, err)
	assert.True(t, ok)

	fc.InstallationToken = false
	ok, err = fc.IsInstallationToken(ctx)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestFakeClient_CreateOrUpdateOrgVariableAll(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{}
	require.NoError(t, fc.CreateOrUpdateOrgVariableAll(ctx, "myorg", "FOREIGN", "caller"))
	exists, err := fc.OrgVariableExists(ctx, "myorg", "FOREIGN")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestFakeClient_DeleteOrgVariable(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{}

	err := fc.DeleteOrgVariable(ctx, "myorg", "DISPATCH_URL")
	require.NoError(t, err)

	require.Len(t, fc.DeletedOrgVariables, 1)
	assert.Equal(t, "myorg/DISPATCH_URL", fc.DeletedOrgVariables[0])
}

func TestFakeClient_DeleteRepoVariable(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{
		VariableValues: map[string]string{
			"myorg/myrepo/FULLSEND_PER_REPO_INSTALL": "true",
		},
		VariablesExist: map[string]bool{
			"myorg/myrepo/FULLSEND_PER_REPO_INSTALL": true,
		},
	}

	err := fc.DeleteRepoVariable(ctx, "myorg", "myrepo", "FULLSEND_PER_REPO_INSTALL")
	require.NoError(t, err)

	// Verify deletion from both maps
	_, existsInValues := fc.VariableValues["myorg/myrepo/FULLSEND_PER_REPO_INSTALL"]
	assert.False(t, existsInValues)
	_, existsInExists := fc.VariablesExist["myorg/myrepo/FULLSEND_PER_REPO_INSTALL"]
	assert.False(t, existsInExists)
}

func TestFakeClient_ListRepoVariables(t *testing.T) {
	ctx := context.Background()

	t.Run("returns matching variables", func(t *testing.T) {
		fc := &FakeClient{
			VariableValues: map[string]string{
				"org/repo/FOO":       "bar",
				"org/repo/BAZ":       "qux",
				"org/other-repo/FOO": "other",
			},
		}

		vars, err := fc.ListRepoVariables(ctx, "org", "repo")
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"FOO": "bar", "BAZ": "qux"}, vars)
	})

	t.Run("empty when no variables", func(t *testing.T) {
		fc := &FakeClient{}
		vars, err := fc.ListRepoVariables(ctx, "org", "repo")
		require.NoError(t, err)
		assert.Empty(t, vars)
	})

	t.Run("error injection", func(t *testing.T) {
		fc := &FakeClient{Errors: map[string]error{"ListRepoVariables": errors.New("fail")}}
		_, err := fc.ListRepoVariables(ctx, "org", "repo")
		assert.Error(t, err)
	})
}

func TestFakeClient_DeleteRepoSecret(t *testing.T) {
	ctx := context.Background()

	t.Run("deletes existing secret", func(t *testing.T) {
		fc := &FakeClient{
			Secrets: map[string]bool{
				"org/repo/MY_SECRET": true,
			},
		}

		err := fc.DeleteRepoSecret(ctx, "org", "repo", "MY_SECRET")
		require.NoError(t, err)
		assert.False(t, fc.Secrets["org/repo/MY_SECRET"])
		require.Len(t, fc.DeletedSecrets, 1)
		assert.Equal(t, "MY_SECRET", fc.DeletedSecrets[0].Name)
	})

	t.Run("idempotent when nil secrets map", func(t *testing.T) {
		fc := &FakeClient{}
		err := fc.DeleteRepoSecret(ctx, "org", "repo", "NONEXISTENT")
		require.NoError(t, err)
		require.Len(t, fc.DeletedSecrets, 1)
	})

	t.Run("error injection", func(t *testing.T) {
		fc := &FakeClient{Errors: map[string]error{"DeleteRepoSecret": errors.New("fail")}}
		err := fc.DeleteRepoSecret(ctx, "org", "repo", "SECRET")
		assert.Error(t, err)
	})
}

func TestFakeClient_CreateThenListVariables(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{}

	err := fc.CreateOrUpdateRepoVariable(ctx, "org", "repo", "FOO", "bar")
	require.NoError(t, err)
	err = fc.CreateOrUpdateRepoVariable(ctx, "org", "repo", "BAZ", "qux")
	require.NoError(t, err)

	vars, err := fc.ListRepoVariables(ctx, "org", "repo")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"FOO": "bar", "BAZ": "qux"}, vars)
}

func TestFakeClient_ErrorInjection(t *testing.T) {
	ctx := context.Background()
	injected := errors.New("injected error")

	methods := []struct {
		name string
		call func(fc *FakeClient) error
	}{
		{"ListOrgRepos", func(fc *FakeClient) error { _, err := fc.ListOrgRepos(ctx, "org"); return err }},
		{"CreateRepo", func(fc *FakeClient) error { _, err := fc.CreateRepo(ctx, "o", "r", "d", false); return err }},
		{"DeleteRepo", func(fc *FakeClient) error { return fc.DeleteRepo(ctx, "o", "r") }},
		{"CreateFile", func(fc *FakeClient) error { return fc.CreateFile(ctx, "o", "r", "p", "m", nil) }},
		{"CreateOrUpdateFile", func(fc *FakeClient) error { return fc.CreateOrUpdateFile(ctx, "o", "r", "p", "m", nil) }},
		{"GetFileContent", func(fc *FakeClient) error { _, err := fc.GetFileContent(ctx, "o", "r", "p"); return err }},
		{"CreateBranch", func(fc *FakeClient) error { return fc.CreateBranch(ctx, "o", "r", "b") }},
		{"CreateFileOnBranch", func(fc *FakeClient) error { return fc.CreateFileOnBranch(ctx, "o", "r", "b", "p", "m", nil) }},
		{"CreateChangeProposal", func(fc *FakeClient) error {
			_, err := fc.CreateChangeProposal(ctx, "o", "r", "t", "b", "h", "base")
			return err
		}},
		{"ListRepoPullRequests", func(fc *FakeClient) error { _, err := fc.ListRepoPullRequests(ctx, "o", "r"); return err }},
		{"GetAuthenticatedUser", func(fc *FakeClient) error { _, err := fc.GetAuthenticatedUser(ctx); return err }},
		{"GetAuthenticatedUserIdentity", func(fc *FakeClient) error {
			fc.AuthenticatedUserIdentity = &UserIdentity{Name: "n", Email: "e"}
			_, err := fc.GetAuthenticatedUserIdentity(ctx)
			return err
		}},
		{"CreateRepoSecret", func(fc *FakeClient) error { return fc.CreateRepoSecret(ctx, "o", "r", "n", "v") }},
		{"RepoSecretExists", func(fc *FakeClient) error { _, err := fc.RepoSecretExists(ctx, "o", "r", "n"); return err }},
		{"CreateOrUpdateRepoVariable", func(fc *FakeClient) error {
			return fc.CreateOrUpdateRepoVariable(ctx, "o", "r", "n", "v")
		}},
		{"GetLatestWorkflowRun", func(fc *FakeClient) error {
			_, err := fc.GetLatestWorkflowRun(ctx, "o", "r", "w")
			return err
		}},
		{"GetWorkflowRun", func(fc *FakeClient) error { _, err := fc.GetWorkflowRun(ctx, "o", "r", 1); return err }},
		{"ListOrgInstallations", func(fc *FakeClient) error {
			_, err := fc.ListOrgInstallations(ctx, "org")
			return err
		}},
		{"CreateOrgSecret", func(fc *FakeClient) error {
			return fc.CreateOrgSecret(ctx, "o", "n", "v", nil)
		}},
		{"OrgSecretExists", func(fc *FakeClient) error {
			_, err := fc.OrgSecretExists(ctx, "o", "n")
			return err
		}},
		{"DeleteOrgSecret", func(fc *FakeClient) error { return fc.DeleteOrgSecret(ctx, "o", "n") }},
		{"SetOrgSecretRepos", func(fc *FakeClient) error {
			return fc.SetOrgSecretRepos(ctx, "o", "n", nil)
		}},
		{"CommitFiles", func(fc *FakeClient) error {
			_, err := fc.CommitFiles(ctx, "o", "r", "m", nil)
			return err
		}},
		{"CreateOrUpdateOrgVariable", func(fc *FakeClient) error {
			return fc.CreateOrUpdateOrgVariable(ctx, "o", "n", "v", nil)
		}},
		{"OrgVariableExists", func(fc *FakeClient) error {
			_, err := fc.OrgVariableExists(ctx, "o", "n")
			return err
		}},
		{"GetOrgVariable", func(fc *FakeClient) error { _, _, err := fc.GetOrgVariable(ctx, "o", "n"); return err }},
		{"ListOrgVariables", func(fc *FakeClient) error { _, err := fc.ListOrgVariables(ctx, "o"); return err }},
		{"IsInstallationToken", func(fc *FakeClient) error { _, err := fc.IsInstallationToken(ctx); return err }},
		{"DeleteOrgVariable", func(fc *FakeClient) error {
			return fc.DeleteOrgVariable(ctx, "o", "n")
		}},
		{"DeleteRepoVariable", func(fc *FakeClient) error {
			return fc.DeleteRepoVariable(ctx, "o", "r", "n")
		}},
		{"SetOrgVariableRepos", func(fc *FakeClient) error {
			return fc.SetOrgVariableRepos(ctx, "o", "n", nil)
		}},
		{"GetOrgVariableRepos", func(fc *FakeClient) error {
			_, err := fc.GetOrgVariableRepos(ctx, "o", "n")
			return err
		}},
		{"DeleteIssueComment", func(fc *FakeClient) error {
			return fc.DeleteIssueComment(ctx, "o", "r", 1)
		}},
		{"ListDirectoryContents", func(fc *FakeClient) error {
			_, err := fc.ListDirectoryContents(ctx, "o", "r", "p", "main", false)
			return err
		}},
		{"ListRepositoryFiles", func(fc *FakeClient) error {
			_, err := fc.ListRepositoryFiles(ctx, "o", "r")
			return err
		}},
		{"GetFileContentAtRef", func(fc *FakeClient) error {
			_, err := fc.GetFileContentAtRef(ctx, "o", "r", "p", "main")
			return err
		}},
		{"DeleteRepoSecret", func(fc *FakeClient) error {
			return fc.DeleteRepoSecret(ctx, "o", "r", "n")
		}},
		{"ListRepoVariables", func(fc *FakeClient) error {
			_, err := fc.ListRepoVariables(ctx, "o", "r")
			return err
		}},
	}

	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			fc := &FakeClient{
				Errors: map[string]error{m.name: injected},
			}
			err := m.call(fc)
			assert.ErrorIs(t, err, injected)
		})
	}
}

func TestFakeClient_ThreadSafety(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{
		Repos: []Repository{
			{Name: "repo1", FullName: "org/repo1"},
		},
		FileContents: map[string][]byte{
			"o/r/file.txt": []byte("content"),
		},
		AuthenticatedUser: "bot",
		WorkflowRuns: map[string]*WorkflowRun{
			"o/r/ci.yml": {ID: 1, Status: "completed", Conclusion: "success"},
		},
		Installations: []Installation{{ID: 1, AppSlug: "app"}},
		Secrets:       map[string]bool{"o/r/secret": true},
		OrgSecrets:    map[string]bool{"o/secret": true},
		OrgVariables:  map[string]bool{"o/var": true},
	}

	var wg sync.WaitGroup
	const goroutines = 20

	// Run many concurrent operations to trigger the race detector.
	for i := range goroutines {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, _ = fc.ListOrgRepos(ctx, "org")
			_, _ = fc.CreateRepo(ctx, "org", "r", "d", false)
			_ = fc.DeleteRepo(ctx, "o", "r")
			_ = fc.CreateFile(ctx, "o", "r", "p", "m", []byte("data"))
			_ = fc.CreateOrUpdateFile(ctx, "o", "r", "p", "m", []byte("data"))
			_, _ = fc.GetFileContent(ctx, "o", "r", "file.txt")
			_ = fc.CreateBranch(ctx, "o", "r", "b")
			_ = fc.CreateFileOnBranch(ctx, "o", "r", "b", "p", "m", []byte("data"))
			_, _ = fc.CreateChangeProposal(ctx, "o", "r", "t", "b", "h", "base")
			_, _ = fc.ListRepoPullRequests(ctx, "o", "r")
			_, _ = fc.GetAuthenticatedUser(ctx)
			_, _ = fc.GetAuthenticatedUserIdentity(ctx)
			_ = fc.CreateRepoSecret(ctx, "o", "r", "n", "v")
			_, _ = fc.RepoSecretExists(ctx, "o", "r", "secret")
			_ = fc.CreateOrUpdateRepoVariable(ctx, "o", "r", "n", "v")
			_, _ = fc.GetLatestWorkflowRun(ctx, "o", "r", "ci.yml")
			_, _ = fc.GetWorkflowRun(ctx, "o", "r", 1)
			_, _ = fc.ListOrgInstallations(ctx, "org")
			_ = fc.CreateOrgSecret(ctx, "o", "n", "v", []int64{1})
			_, _ = fc.OrgSecretExists(ctx, "o", "secret")
			_ = fc.DeleteOrgSecret(ctx, "o", "n")
			_ = fc.SetOrgSecretRepos(ctx, "o", "n", []int64{1, 2})
			_, _ = fc.CommitFiles(ctx, "o", "r", "m", []TreeFile{{Path: "p", Content: []byte("c"), Mode: "100644"}})
			_ = fc.CreateOrUpdateOrgVariable(ctx, "o", "n", "v", []int64{1})
			_, _ = fc.OrgVariableExists(ctx, "o", "var")
			_ = fc.DeleteOrgVariable(ctx, "o", "n")
			_ = fc.SetOrgVariableRepos(ctx, "o", "n", []int64{1, 2})
			_, _ = fc.GetOrgVariableRepos(ctx, "o", "n")
			_ = fc.DeleteIssueComment(ctx, "o", "r", 1)
			_, _ = fc.ListDirectoryContents(ctx, "o", "r", "p", "main", false)
			_, _ = fc.ListRepositoryFiles(ctx, "o", "r")
			_, _ = fc.GetFileContentAtRef(ctx, "o", "r", "p", "main")
			_ = fc.DeleteRepoSecret(ctx, "o", "r", "n")
			_, _ = fc.ListRepoVariables(ctx, "o", "r")
		}(i)
	}

	wg.Wait()
}

func TestFakeClient_FindExistingFork(t *testing.T) {
	ctx := context.Background()

	t.Run("returns fork owner and repo when entry exists", func(t *testing.T) {
		fc := &FakeClient{
			ExistingForks: map[string]string{
				"upstream/repo": "contributor",
			},
		}
		forkOwner, forkRepo, err := fc.FindExistingFork(ctx, "upstream", "repo")
		require.NoError(t, err)
		assert.Equal(t, "contributor", forkOwner)
		assert.Equal(t, "repo", forkRepo)
	})

	t.Run("returns empty when no entry exists", func(t *testing.T) {
		fc := &FakeClient{}
		forkOwner, forkRepo, err := fc.FindExistingFork(ctx, "upstream", "repo")
		require.NoError(t, err)
		assert.Empty(t, forkOwner)
		assert.Empty(t, forkRepo)
	})

	t.Run("returns error when injected", func(t *testing.T) {
		fc := &FakeClient{
			Errors: map[string]error{
				"FindExistingFork": errors.New("api error"),
			},
		}
		_, _, err := fc.FindExistingFork(ctx, "upstream", "repo")
		require.Error(t, err)
	})
}

func TestFakeClient_CreateFork(t *testing.T) {
	ctx := context.Background()

	t.Run("uses ForkOwner when set", func(t *testing.T) {
		fc := &FakeClient{
			ForkOwner: "org-fork",
		}
		forkOwner, forkRepo, err := fc.CreateFork(ctx, "upstream", "repo")
		require.NoError(t, err)
		assert.Equal(t, "org-fork", forkOwner)
		assert.Equal(t, "repo", forkRepo)
		assert.Equal(t, []string{"upstream/repo"}, fc.CreatedForks)
	})

	t.Run("falls back to AuthenticatedUser", func(t *testing.T) {
		fc := &FakeClient{
			AuthenticatedUser: "contributor",
		}
		forkOwner, forkRepo, err := fc.CreateFork(ctx, "upstream", "repo")
		require.NoError(t, err)
		assert.Equal(t, "contributor", forkOwner)
		assert.Equal(t, "repo", forkRepo)
		assert.Equal(t, []string{"upstream/repo"}, fc.CreatedForks)
	})

	t.Run("returns error when injected", func(t *testing.T) {
		fc := &FakeClient{
			Errors: map[string]error{
				"CreateFork": errors.New("api error"),
			},
		}
		_, _, err := fc.CreateFork(ctx, "upstream", "repo")
		require.Error(t, err)
	})
}

func TestFakeClient_CreateBranch_PerRepoErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("returns per-repo error over generic error", func(t *testing.T) {
		fc := &FakeClient{
			CreateBranchErrors: map[string]error{
				"upstream/repo": ErrForbidden,
			},
			Errors: map[string]error{
				"CreateBranch": errors.New("generic"),
			},
		}
		err := fc.CreateBranch(ctx, "upstream", "repo", "branch")
		require.Error(t, err)
		assert.True(t, IsForbidden(err))
	})

	t.Run("falls through to generic error", func(t *testing.T) {
		fc := &FakeClient{
			Errors: map[string]error{
				"CreateBranch": ErrAlreadyExists,
			},
		}
		err := fc.CreateBranch(ctx, "upstream", "repo", "branch")
		require.Error(t, err)
		assert.True(t, IsAlreadyExists(err))
	})
}

func TestFakeClient_GetRef(t *testing.T) {
	ctx := context.Background()

	t.Run("returns SHA for existing ref", func(t *testing.T) {
		fc := &FakeClient{
			Refs: map[string]string{
				"owner/repo/tags/v0": "abc123",
			},
		}
		sha, err := fc.GetRef(ctx, "owner", "repo", "tags/v0")
		require.NoError(t, err)
		assert.Equal(t, "abc123", sha)
	})

	t.Run("returns ErrNotFound for missing ref", func(t *testing.T) {
		fc := &FakeClient{
			Refs: map[string]string{},
		}
		_, err := fc.GetRef(ctx, "owner", "repo", "tags/v0")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("returns injected error", func(t *testing.T) {
		fc := &FakeClient{
			Errors: map[string]error{
				"GetRef": errors.New("boom"),
			},
			Refs: map[string]string{
				"owner/repo/tags/v0": "abc123",
			},
		}
		_, err := fc.GetRef(ctx, "owner", "repo", "tags/v0")
		require.Error(t, err)
		assert.Equal(t, "boom", err.Error())
	})
}

func TestFakeClient_GetBranchRef_DelegatesToGetRef(t *testing.T) {
	ctx := context.Background()

	t.Run("BranchRefs takes priority", func(t *testing.T) {
		fc := &FakeClient{
			BranchRefs: map[string]string{
				"owner/repo/main": "branch-sha",
			},
			Refs: map[string]string{
				"owner/repo/heads/main": "ref-sha",
			},
		}
		sha, err := fc.GetBranchRef(ctx, "owner", "repo", "main")
		require.NoError(t, err)
		assert.Equal(t, "branch-sha", sha)
	})

	t.Run("falls through to GetRef when not in BranchRefs", func(t *testing.T) {
		fc := &FakeClient{
			Refs: map[string]string{
				"owner/repo/heads/main": "ref-sha",
			},
		}
		sha, err := fc.GetBranchRef(ctx, "owner", "repo", "main")
		require.NoError(t, err)
		assert.Equal(t, "ref-sha", sha)
	})

	t.Run("GetBranchRef error injection works independently", func(t *testing.T) {
		fc := &FakeClient{
			Errors: map[string]error{
				"GetBranchRef": errors.New("branch error"),
			},
			BranchRefs: map[string]string{
				"owner/repo/main": "sha",
			},
		}
		_, err := fc.GetBranchRef(ctx, "owner", "repo", "main")
		require.Error(t, err)
		assert.Equal(t, "branch error", err.Error())
	})
}

func TestIsForbidden(t *testing.T) {
	assert.True(t, IsForbidden(ErrForbidden))
	assert.True(t, IsForbidden(errors.Join(errors.New("wrapper"), ErrForbidden)))
	assert.False(t, IsForbidden(errors.New("some error")))
	assert.False(t, IsForbidden(nil))
}

func TestIsNonFastForward(t *testing.T) {
	assert.True(t, IsNonFastForward(ErrNonFastForward))
	assert.True(t, IsNonFastForward(errors.Join(errors.New("wrapper"), ErrNonFastForward)))
	assert.False(t, IsNonFastForward(errors.New("some error")))
	assert.False(t, IsNonFastForward(nil))
}

func TestFakeClient_GetIssue(t *testing.T) {
	fc := NewFakeClient()
	issue, err := fc.CreateIssue(context.Background(), "org", "repo", "t", "b")
	require.NoError(t, err)

	got, err := fc.GetIssue(context.Background(), "org", "repo", issue.Number)
	require.NoError(t, err)
	assert.Equal(t, issue.Number, got.Number)

	_, err = fc.GetIssue(context.Background(), "org", "repo", 999)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestFakeClient_AddIssueLabels(t *testing.T) {
	fc := NewFakeClient()
	issue, err := fc.CreateIssue(context.Background(), "org", "repo", "t", "b")
	require.NoError(t, err)

	require.NoError(t, fc.AddIssueLabels(context.Background(), "org", "repo", issue.Number, "ready-for-triage"))
	got, err := fc.GetIssue(context.Background(), "org", "repo", issue.Number)
	require.NoError(t, err)
	assert.Contains(t, got.Labels, "ready-for-triage")

	require.Error(t, fc.AddIssueLabels(context.Background(), "org", "repo", 999, "x"))
}

func TestFakeClient_ListRecentWorkflowRuns(t *testing.T) {
	fc := NewFakeClient()
	fc.RecentWorkflowRuns = map[string][]WorkflowRun{
		"org/repo": {
			{ID: 1, Name: "Triage Agent", Status: "completed", Conclusion: "success", CreatedAt: "2024-01-01T00:00:00Z"},
		},
	}
	runs, err := fc.ListRecentWorkflowRuns(context.Background(), "org", "repo", 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
}

func TestFakeClient_ListWorkflowRunArtifacts(t *testing.T) {
	fc := NewFakeClient()
	fc.WorkflowRunArtifacts = map[int][]WorkflowArtifact{
		42: {{ID: 5, Name: "fullsend-triage"}},
	}

	arts, err := fc.ListWorkflowRunArtifacts(context.Background(), "org", "repo", 42)
	require.NoError(t, err)
	require.Len(t, arts, 1)
	assert.Equal(t, "fullsend-triage", arts[0].Name)

	arts, err = fc.ListWorkflowRunArtifacts(context.Background(), "org", "repo", 99)
	require.NoError(t, err)
	assert.Nil(t, arts)
}

func TestFakeClient_DownloadWorkflowRunArtifact(t *testing.T) {
	fc := NewFakeClient()
	fc.WorkflowArtifactContents = map[int][]byte{
		9: []byte("artifact-bytes"),
	}

	data, err := fc.DownloadWorkflowRunArtifact(context.Background(), "org", "repo", 9)
	require.NoError(t, err)
	assert.Equal(t, []byte("artifact-bytes"), data)

	_, err = fc.DownloadWorkflowRunArtifact(context.Background(), "org", "repo", 99)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestFakeClient_ListRepositoryArtifacts(t *testing.T) {
	fc := NewFakeClient()
	fc.RepositoryArtifacts = map[string][]RepositoryArtifact{
		"org/repo": {
			{ID: 1, Name: "a"},
			{ID: 2, Name: "b"},
		},
	}

	arts, err := fc.ListRepositoryArtifacts(context.Background(), "org", "repo", 1)
	require.NoError(t, err)
	require.Len(t, arts, 1)
	assert.Equal(t, 1, arts[0].ID)

	arts, err = fc.ListRepositoryArtifacts(context.Background(), "org", "repo", 0)
	require.NoError(t, err)
	assert.Len(t, arts, 2)
}

func TestFakeClient_AddIssueLabels_Idempotent(t *testing.T) {
	fc := NewFakeClient()
	issue, err := fc.CreateIssue(context.Background(), "org", "repo", "t", "b")
	require.NoError(t, err)

	require.NoError(t, fc.AddIssueLabels(context.Background(), "org", "repo", issue.Number, "ready-for-triage", "ready-for-triage"))
	got, err := fc.GetIssue(context.Background(), "org", "repo", issue.Number)
	require.NoError(t, err)
	assert.Equal(t, []string{"ready-for-triage"}, got.Labels)
}

func TestIsNotSupported(t *testing.T) {
	assert.True(t, IsNotSupported(ErrNotSupported))
	assert.True(t, IsNotSupported(fmt.Errorf("wrap: %w", ErrNotSupported)))
	assert.False(t, IsNotSupported(ErrNotFound))
	assert.False(t, IsNotSupported(nil))
}

func TestNewFakeClient_MapsInitialized(t *testing.T) {
	fc := NewFakeClient()
	assert.NotNil(t, fc.ProtectedBranches)
	assert.NotNil(t, fc.PipelineSchedules)
}

func TestFakeClient_PipelineScheduleRoundTrip(t *testing.T) {
	ctx := context.Background()
	fc := NewFakeClient()

	id, err := fc.CreatePipelineSchedule(ctx, "org", "repo", "main", "nightly", "0 0 * * *", nil)
	require.NoError(t, err)
	assert.Equal(t, int64(1), id)

	schedules, err := fc.ListPipelineSchedules(ctx, "org", "repo")
	require.NoError(t, err)
	require.Len(t, schedules, 1)
	assert.Equal(t, "nightly", schedules[0].Description)
	assert.Equal(t, "main", schedules[0].Ref)
	assert.Equal(t, "0 0 * * *", schedules[0].Cron)
	assert.True(t, schedules[0].Active)

	err = fc.DeletePipelineSchedule(ctx, "org", "repo", id)
	require.NoError(t, err)

	schedules, err = fc.ListPipelineSchedules(ctx, "org", "repo")
	require.NoError(t, err)
	assert.Empty(t, schedules)

	assert.Equal(t, []int64{id}, fc.DeletedScheduleIDs)
}

func TestFakeClient_UpdateCIVariable_RecordsProtected(t *testing.T) {
	ctx := context.Background()
	fc := NewFakeClient()

	err := fc.UpdateCIVariable(ctx, "org", "repo", "KEY", "val", true)
	require.NoError(t, err)
	require.Len(t, fc.UpdatedVariables, 1)
	assert.True(t, fc.UpdatedVariables[0].Protected)
	assert.Equal(t, "KEY", fc.UpdatedVariables[0].Name)

	err = fc.UpdateCIVariable(ctx, "org", "repo", "KEY2", "val2", false)
	require.NoError(t, err)
	require.Len(t, fc.UpdatedVariables, 2)
	assert.False(t, fc.UpdatedVariables[1].Protected)
}

func TestFakeClient_IsProtectedBranch(t *testing.T) {
	ctx := context.Background()
	fc := NewFakeClient()
	fc.ProtectedBranches["org/repo/main"] = true

	protected, err := fc.IsProtectedBranch(ctx, "org", "repo", "main")
	require.NoError(t, err)
	assert.True(t, protected)

	protected, err = fc.IsProtectedBranch(ctx, "org", "repo", "dev")
	require.NoError(t, err)
	assert.False(t, protected)
}

func TestFakeClient_CreateProtectedCIVariable(t *testing.T) {
	ctx := context.Background()
	fc := NewFakeClient()

	err := fc.CreateProtectedCIVariable(ctx, "org", "repo", "SECRET_KEY", "secret-val")
	require.NoError(t, err)
	require.Len(t, fc.CreatedProtectedVars, 1)
	assert.Equal(t, "SECRET_KEY", fc.CreatedProtectedVars[0].Name)
	assert.Equal(t, "secret-val", fc.CreatedProtectedVars[0].Value)
	assert.True(t, fc.CreatedProtectedVars[0].Protected)
}

func TestFakeClient_CommitFilesErrSeq(t *testing.T) {
	ctx := context.Background()
	files := []TreeFile{{Path: "f.txt", Content: []byte("x"), Mode: "100644"}}

	t.Run("first call errors, second succeeds", func(t *testing.T) {
		fc := &FakeClient{
			CommitFilesErrSeq: []error{ErrNonFastForward},
		}
		_, err := fc.CommitFiles(ctx, "o", "r", "m", files)
		require.ErrorIs(t, err, ErrNonFastForward)

		changed, err := fc.CommitFiles(ctx, "o", "r", "m", files)
		require.NoError(t, err)
		assert.True(t, changed)
	})

	t.Run("nil entry means no error for that call", func(t *testing.T) {
		fc := &FakeClient{
			CommitFilesErrSeq: []error{nil, ErrNonFastForward},
		}
		changed, err := fc.CommitFiles(ctx, "o", "r", "m", files)
		require.NoError(t, err)
		assert.True(t, changed)

		_, err = fc.CommitFiles(ctx, "o", "r", "m", files)
		require.ErrorIs(t, err, ErrNonFastForward)
	})
}

func TestFakeClient_GetRepo(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{
		Repos: []Repository{{Name: "repo", FullName: "org/repo"}},
	}

	repo, err := fc.GetRepo(ctx, "org", "repo")
	require.NoError(t, err)
	assert.Equal(t, "org/repo", repo.FullName)

	_, err = fc.GetRepo(ctx, "org", "missing")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestFakeClient_GetOrgPlan(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{}

	plan, err := fc.GetOrgPlan(ctx, "org")
	require.NoError(t, err)
	assert.Equal(t, "free", plan)

	fc.OrgPlan = "enterprise"
	plan, err = fc.GetOrgPlan(ctx, "org")
	require.NoError(t, err)
	assert.Equal(t, "enterprise", plan)
}

func TestFakeClient_ListWorkflowRuns(t *testing.T) {
	ctx := context.Background()
	run := WorkflowRun{ID: 42, Status: "completed", Conclusion: "success"}
	fc := &FakeClient{
		WorkflowRuns: map[string]*WorkflowRun{
			"org/repo/ci.yml": &run,
		},
	}

	runs, err := fc.ListWorkflowRuns(ctx, "org", "repo", "ci.yml")
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, 42, runs[0].ID)

	runs, err = fc.ListWorkflowRuns(ctx, "org", "repo", "missing.yml")
	require.NoError(t, err)
	assert.Nil(t, runs)
}

func TestFakeClient_DispatchWorkflow(t *testing.T) {
	ctx := context.Background()
	fc := &FakeClient{}
	require.NoError(t, fc.DispatchWorkflow(ctx, "org", "repo", "ci.yml", "main", map[string]string{"k": "v"}))
}

func TestIsTreeTruncated(t *testing.T) {
	assert.True(t, IsTreeTruncated(ErrTreeTruncated))
	assert.True(t, IsTreeTruncated(errors.Join(errors.New("wrapper"), ErrTreeTruncated)))
	assert.False(t, IsTreeTruncated(errors.New("some other error")))
	assert.False(t, IsTreeTruncated(nil))
}

func TestFakeClient_ListRepositoryFiles(t *testing.T) {
	ctx := context.Background()

	t.Run("returns files matching owner/repo prefix", func(t *testing.T) {
		fc := &FakeClient{
			FileContents: map[string][]byte{
				"org/repo/file1.go": []byte("a"),
				"org/repo/dir/f2":   []byte("b"),
				"org/other/nope":    []byte("c"),
			},
		}
		paths, err := fc.ListRepositoryFiles(ctx, "org", "repo")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"file1.go", "dir/f2"}, paths)
	})

	t.Run("returns nil for empty repo", func(t *testing.T) {
		fc := &FakeClient{FileContents: map[string][]byte{}}
		paths, err := fc.ListRepositoryFiles(ctx, "org", "repo")
		require.NoError(t, err)
		assert.Empty(t, paths)
	})

	t.Run("returns injected error", func(t *testing.T) {
		fc := &FakeClient{
			Errors: map[string]error{
				"ListRepositoryFiles": ErrTreeTruncated,
			},
		}
		_, err := fc.ListRepositoryFiles(ctx, "org", "repo")
		require.ErrorIs(t, err, ErrTreeTruncated)
	})
}

func TestFakeClient_ListRepositoryFiles_ConcurrentSafe(t *testing.T) {
	fc := &FakeClient{
		FileContents: map[string][]byte{
			"org/repo/a": []byte("a"),
			"org/repo/b": []byte("b"),
		},
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := fc.ListRepositoryFiles(context.Background(), "org", "repo")
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
}
