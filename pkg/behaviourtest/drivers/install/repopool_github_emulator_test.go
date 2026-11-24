package install

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPoolLayoutForEnvironment_GitHubEmulatorIsOneOrg(t *testing.T) {
	t.Setenv("BEHAVIOUR_POOL_SIZE", "3")
	layout := PoolLayoutForEnvironment("github-emulator", nil)
	assert.Equal(t, []string{StageOrg}, layout.Orgs())
	assert.Len(t, layout.Slots(), 3)
}

func TestNewRepoPoolGitHubEmulator_CreatesMissingOrg(t *testing.T) {
	var createdOrg bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/user":
			_, _ = w.Write([]byte(`{"login":"admin"}`))
		case r.URL.Path == "/api/v3/orgs/"+StageOrg && !createdOrg:
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/api/v3/admin/organizations":
			createdOrg = true
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL+"/api/v3")
	t.Setenv("FULLSEND_MINT_URL", "http://127.0.0.1:1")
	t.Setenv("BEHAVIOUR_POOL_SIZE", "2")

	d, err := NewRepoPoolGitHubEmulator(testPoolConfig(t, PoolLayoutForEnvironment("github-emulator", nil)))
	require.NoError(t, err)
	assert.True(t, createdOrg)
	assert.Equal(t, 2, d.Capacity())

	cd, ok := d.(*composedDriver)
	require.True(t, ok, "emulator pool is a composed driver, got %T", d)
	assert.Nil(t, cd.alloc.orgLock, "the emulator's orgs are private: no e2e-lock bridge")
}

func TestNewRepoPoolGitHubEmulator_NeedsMintURL(t *testing.T) {
	t.Setenv("FULLSEND_MINT_URL", "")
	pool := testPoolConfig(t, PoolLayoutForEnvironment("github-emulator", nil))
	pool.TokenFor = func(context.Context, string) (string, error) {
		t.Error("no org call before the mint URL is checked")
		return "", nil
	}
	_, err := NewRepoPoolGitHubEmulator(pool)
	require.ErrorContains(t, err, "FULLSEND_MINT_URL")
}
