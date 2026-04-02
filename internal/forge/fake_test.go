package forge

import (
	"context"
	"errors"
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
		assert.Contains(t, err.Error(), "file not found")
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
			_ = fc.CreateRepoSecret(ctx, "o", "r", "n", "v")
			_, _ = fc.RepoSecretExists(ctx, "o", "r", "secret")
			_ = fc.CreateOrUpdateRepoVariable(ctx, "o", "r", "n", "v")
			_, _ = fc.GetLatestWorkflowRun(ctx, "o", "r", "ci.yml")
			_, _ = fc.GetWorkflowRun(ctx, "o", "r", 1)
			_, _ = fc.ListOrgInstallations(ctx, "org")
		}(i)
	}

	wg.Wait()
}
