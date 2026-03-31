package github

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFakeClient_ListOrgRepos(t *testing.T) {
	client := NewFakeClient()
	client.Repos = []Repository{
		{Name: "repo1"},
		{Name: "repo2"},
	}

	repos, err := client.ListOrgRepos(context.Background(), "org")
	require.NoError(t, err)
	assert.Len(t, repos, 2)
}

func TestFakeClient_ListOrgRepos_Error(t *testing.T) {
	client := NewFakeClient()
	client.Errors["ListOrgRepos"] = errors.New("API error")

	_, err := client.ListOrgRepos(context.Background(), "org")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "API error")
}

func TestFakeClient_CreateRepo(t *testing.T) {
	client := NewFakeClient()

	repo, err := client.CreateRepo(context.Background(), "org", ".fullsend", "config", false)
	require.NoError(t, err)

	assert.Equal(t, ".fullsend", repo.Name)
	assert.Equal(t, "org/.fullsend", repo.FullName)
	assert.Equal(t, "main", repo.DefaultBranch)

	assert.Len(t, client.CreatedRepos, 1)
	assert.Equal(t, "org", client.CreatedRepos[0].Org)
	assert.Equal(t, ".fullsend", client.CreatedRepos[0].Name)
}

func TestFakeClient_CreateFile(t *testing.T) {
	client := NewFakeClient()

	err := client.CreateFile(context.Background(), "org", "repo", "config.yaml", "init", []byte("data"))
	require.NoError(t, err)

	assert.Len(t, client.CreatedFiles, 1)
	assert.Equal(t, "config.yaml", client.CreatedFiles[0].Path)
	assert.Equal(t, []byte("data"), client.CreatedFiles[0].Content)
}

func TestFakeClient_CreatePullRequest(t *testing.T) {
	client := NewFakeClient()

	pr, err := client.CreatePullRequest(context.Background(),
		"org", "repo", "title", "body", "branch", "main")
	require.NoError(t, err)

	assert.Equal(t, 1, pr.Number)
	assert.Contains(t, pr.HTMLURL, "org/repo/pull/1")
	assert.Equal(t, "title", pr.Title)

	// Second PR should get number 2
	pr2, err := client.CreatePullRequest(context.Background(),
		"org", "repo2", "title2", "body2", "branch", "main")
	require.NoError(t, err)
	assert.Equal(t, 2, pr2.Number)
}

func TestFakeClient_CreateBranch(t *testing.T) {
	client := NewFakeClient()

	err := client.CreateBranch(context.Background(), "org", "repo", "fullsend/enroll")
	require.NoError(t, err)

	assert.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "fullsend/enroll", client.CreatedBranches[0].BranchName)
}

func TestFakeClient_CreateFileOnBranch(t *testing.T) {
	client := NewFakeClient()

	err := client.CreateFileOnBranch(context.Background(),
		"org", "repo", "feature", "file.txt", "add file", []byte("content"))
	require.NoError(t, err)

	assert.Len(t, client.CreatedFiles, 1)
	assert.Equal(t, "feature", client.CreatedFiles[0].Branch)
}

func TestFakeClient_ErrorInjection(t *testing.T) {
	client := NewFakeClient()
	injectedErr := errors.New("injected")

	tests := []struct {
		fn     func() error
		name   string
		method string
	}{
		{
			name:   "CreateRepo",
			method: "CreateRepo",
			fn: func() error {
				_, err := client.CreateRepo(context.Background(), "o", "n", "d", false)
				return err
			},
		},
		{
			name:   "CreateFile",
			method: "CreateFile",
			fn: func() error {
				return client.CreateFile(context.Background(), "o", "r", "p", "m", nil)
			},
		},
		{
			name:   "CreatePullRequest",
			method: "CreatePullRequest",
			fn: func() error {
				_, err := client.CreatePullRequest(context.Background(), "o", "r", "t", "b", "h", "base")
				return err
			},
		},
		{
			name:   "CreateBranch",
			method: "CreateBranch",
			fn: func() error {
				return client.CreateBranch(context.Background(), "o", "r", "b")
			},
		},
		{
			name:   "CreateFileOnBranch",
			method: "CreateFileOnBranch",
			fn: func() error {
				return client.CreateFileOnBranch(context.Background(), "o", "r", "b", "p", "m", nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client.Errors[tt.method] = injectedErr
			defer delete(client.Errors, tt.method)

			err := tt.fn()
			assert.ErrorIs(t, err, injectedErr)
		})
	}
}
