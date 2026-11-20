package e2etest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitHubEmulatorMode(t *testing.T) {
	t.Setenv("ENVIRONMENT", "dev")
	assert.False(t, GitHubEmulatorMode())
	t.Setenv("ENVIRONMENT", " github-emulator ")
	assert.True(t, GitHubEmulatorMode())
	t.Setenv("ENVIRONMENT", "emulator")
	assert.False(t, GitHubEmulatorMode(), "only the GitHub emulator is selected")
	t.Setenv("ENVIRONMENT", " github-emulator ")
	assert.True(t, GitHubEmulatorMode())
}

func TestConfigureGitHubEmulatorForge_SetsServerAndAPIURL(t *testing.T) {
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_API_URL", "https://api.github.com")
	t.Setenv(GitHubEmulatorURLEnv, " http://127.0.0.1:18302/ ")

	root, err := ConfigureGitHubEmulatorForge()
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:18302", root)
	assert.Equal(t, "http://127.0.0.1:18302", os.Getenv("GITHUB_SERVER_URL"))
	assert.Equal(t, "http://127.0.0.1:18302/api/v3", os.Getenv("GITHUB_API_URL"))
	assert.Equal(t, "http://127.0.0.1:18302/api/v3", NewLiveClient("t").BaseURL())
}

func TestConfigureGitHubEmulatorForge_RejectsBadRoots(t *testing.T) {
	for _, raw := range []string{"", "127.0.0.1:18302", "ftp://host", "http://host/api/v3", "http://host?x=1"} {
		t.Setenv(GitHubEmulatorURLEnv, raw)
		t.Setenv("GITHUB_API_URL", "https://api.github.com")
		_, err := ConfigureGitHubEmulatorForge()
		require.Error(t, err, raw)
		assert.Equal(t, "https://api.github.com", os.Getenv("GITHUB_API_URL"), "a rejected root leaves the env alone")
	}
}

func TestSetUpGitHubEmulatorMode(t *testing.T) {
	t.Run("other environments are left alone", func(t *testing.T) {
		t.Setenv("ENVIRONMENT", "dev")
		t.Setenv(GitHubEmulatorURLEnv, "https://ghemu.test")
		t.Setenv("GITHUB_API_URL", "https://api.github.com")
		on, err := setUpGitHubEmulatorMode()
		require.NoError(t, err)
		assert.False(t, on)
		assert.Equal(t, "https://api.github.com", os.Getenv("GITHUB_API_URL"))
	})
	t.Run("a bad root is an error", func(t *testing.T) {
		t.Setenv("ENVIRONMENT", GitHubEmulatorEnvironment)
		t.Setenv(GitHubEmulatorURLEnv, "")
		t.Setenv("GH_TOKEN", "emu-token")
		on, err := setUpGitHubEmulatorMode()
		assert.True(t, on)
		require.ErrorContains(t, err, GitHubEmulatorURLEnv)
	})
	t.Run("GH_TOKEN is required", func(t *testing.T) {
		t.Setenv("ENVIRONMENT", GitHubEmulatorEnvironment)
		t.Setenv(GitHubEmulatorURLEnv, "https://ghemu.test")
		t.Setenv("GH_TOKEN", " ")
		t.Setenv("GITHUB_TOKEN", "github-com-token")
		on, err := setUpGitHubEmulatorMode()
		assert.True(t, on)
		require.ErrorContains(t, err, "GH_TOKEN", "GITHUB_TOKEN never stands in for the emulator token")
	})
	t.Run("selected", func(t *testing.T) {
		t.Setenv("ENVIRONMENT", GitHubEmulatorEnvironment)
		t.Setenv(GitHubEmulatorURLEnv, "https://ghemu.test")
		t.Setenv("GH_TOKEN", "emu-token")
		on, err := setUpGitHubEmulatorMode()
		require.NoError(t, err)
		assert.True(t, on)
		assert.Equal(t, "https://ghemu.test/api/v3", os.Getenv("GITHUB_API_URL"))
	})
}

// The behaviour and playback suites load their config through
// LoadEnvConfig: in emulator mode it points them at the emulator and
// uses GH_TOKEN, never the mint's e2e role, even on an Actions runner.
func TestLoadEnvConfig_GitHubEmulator(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("ENVIRONMENT", GitHubEmulatorEnvironment)
	t.Setenv(GitHubEmulatorURLEnv, "https://ghemu.test/")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_API_URL", "https://api.github.com")
	t.Setenv("GH_TOKEN", "emu-token")
	t.Setenv("GITHUB_TOKEN", "github-com-token")

	cfg := LoadEnvConfig(t)
	assert.False(t, cfg.UseMint, "no mint e2e role on the emulator")
	assert.Equal(t, "https://ghemu.test", os.Getenv("GITHUB_SERVER_URL"))
	assert.Equal(t, "https://ghemu.test/api/v3", os.Getenv("GITHUB_API_URL"))
	assert.Equal(t, "https://ghemu.test/api/v3", NewLiveClient("t").BaseURL())

	token, err := TokenForOrg(context.Background(), cfg, "halfsend")
	require.NoError(t, err)
	assert.Equal(t, "emu-token", token)
}

func TestEnsureGitHubEmulatorOrg(t *testing.T) {
	var created []map[string]string
	existing := map[string]bool{"present": true}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/orgs/present":
			_, _ = w.Write([]byte(`{"login":"present"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/orgs/missing":
			if existing["missing"] {
				_, _ = w.Write([]byte(`{"login":"missing"}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/admin/organizations":
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			created = append(created, body)
			existing[body["login"]] = true
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/orgs/broken":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL+"/api/v3")
	ctx := context.Background()

	lookups := 0
	admin := func(context.Context) (string, error) { lookups++; return "admin", nil }
	noViewer := func(context.Context) (string, error) { return "", errors.New("GET /user: 403") }

	require.NoError(t, EnsureGitHubEmulatorOrg(ctx, "tok", "present", noViewer), "an existing org needs no viewer login")
	assert.Empty(t, created, "an existing org is left alone")

	require.Error(t, EnsureGitHubEmulatorOrg(ctx, "tok", "missing", noViewer), "a missing org needs an owner")
	assert.Empty(t, created)

	require.NoError(t, EnsureGitHubEmulatorOrg(ctx, "tok", "missing", admin))
	require.NoError(t, EnsureGitHubEmulatorOrg(ctx, "tok", "missing", admin))
	assert.Equal(t, []map[string]string{{"login": "missing", "admin": "admin"}}, created, "created once, owned by admin")
	assert.Equal(t, 1, lookups, "the owner is looked up only for the create")

	require.Error(t, EnsureGitHubEmulatorOrg(ctx, "tok", "broken", admin))
}

func TestGitHubEmulatorViewerLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v3/user", r.URL.Path)
		_, _ = w.Write([]byte(`{"login":"admin"}`))
	}))
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL+"/api/v3")

	login, err := GitHubEmulatorViewerLogin(context.Background(), "tok")
	require.NoError(t, err)
	assert.Equal(t, "admin", login)
}

func TestGitHubEmulatorViewerLogin_Errors(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"installation token": {status: http.StatusForbidden, body: `{"message":"Resource not accessible by integration"}`},
		"no login":           {status: http.StatusOK, body: `{}`},
		"not json":           {status: http.StatusOK, body: `nope`},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			t.Setenv("GITHUB_API_URL", srv.URL+"/api/v3")

			_, err := GitHubEmulatorViewerLogin(context.Background(), "tok")
			require.ErrorContains(t, err, "reading the token's user")
		})
	}
}
