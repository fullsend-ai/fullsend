package gitlab

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func clearURLEnv(t *testing.T) {
	t.Helper()
	for _, key := range URLEnvVars {
		t.Setenv(key, "")
	}
}

func TestResolveForgeHostPort(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		wantHost string
		wantPort string
	}{
		{
			name:     "CI_SERVER_URL default port",
			env:      map[string]string{"CI_SERVER_URL": "https://gitlab.cee.redhat.com"},
			wantHost: "gitlab.cee.redhat.com",
			wantPort: "443",
		},
		{
			name:     "non-standard port",
			env:      map[string]string{"CI_SERVER_URL": "https://gitlab.company.com:8443"},
			wantHost: "gitlab.company.com",
			wantPort: "8443",
		},
		{
			name:     "FULLSEND_GITLAB_URL takes precedence",
			env:      map[string]string{"FULLSEND_GITLAB_URL": "https://gitlab.company.com", "CI_SERVER_URL": "https://gitlab.other.com"},
			wantHost: "gitlab.company.com",
			wantPort: "443",
		},
		{
			name:     "GITLAB_API_URL takes precedence over CI_SERVER_URL",
			env:      map[string]string{"GITLAB_API_URL": "https://gitlab.api.example.com/api/v4", "CI_SERVER_URL": "https://gitlab.other.com"},
			wantHost: "gitlab.api.example.com",
			wantPort: "443",
		},
		{
			name:     "no env vars",
			env:      map[string]string{},
			wantHost: "",
			wantPort: "",
		},
		{
			name:     "whitespace-only value is unset",
			env:      map[string]string{"FULLSEND_GITLAB_URL": "  ", "CI_SERVER_URL": "https://gitlab.com"},
			wantHost: "gitlab.com",
			wantPort: "443",
		},
		{
			name:     "non-standard port from FULLSEND_GITLAB_URL",
			env:      map[string]string{"FULLSEND_GITLAB_URL": "https://gitlab.internal:9443"},
			wantHost: "gitlab.internal",
			wantPort: "9443",
		},
		{
			name:     "http on localhost defaults to port 80",
			env:      map[string]string{"CI_SERVER_URL": "http://localhost"},
			wantHost: "localhost",
			wantPort: "80",
		},
		{
			name:     "http on a loopback IP with explicit port",
			env:      map[string]string{"CI_SERVER_URL": "http://127.0.0.1:3000"},
			wantHost: "127.0.0.1",
			wantPort: "3000",
		},
		{
			name:     "http on IPv6 loopback",
			env:      map[string]string{"CI_SERVER_URL": "http://[::1]:8080"},
			wantHost: "::1",
			wantPort: "8080",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearURLEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			host, port, err := ResolveForgeHostPort()
			require.NoError(t, err)
			assert.Equal(t, tc.wantHost, host)
			assert.Equal(t, tc.wantPort, port)
		})
	}
}

func TestResolveURL(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		clearURLEnv(t)
		raw, env, err := ResolveURL()
		require.NoError(t, err)
		assert.Empty(t, raw)
		assert.Empty(t, env)
	})

	t.Run("first non-empty wins and is trimmed", func(t *testing.T) {
		clearURLEnv(t)
		t.Setenv("GITLAB_API_URL", " https://gitlab.example.com/api/v4 ")
		t.Setenv("CI_SERVER_URL", "https://gitlab.other.com")
		raw, env, err := ResolveURL()
		require.NoError(t, err)
		assert.Equal(t, "https://gitlab.example.com/api/v4", raw)
		assert.Equal(t, "GITLAB_API_URL", env)
	})

	for _, tc := range []struct {
		name, value, wantErr string
	}{
		{"http on a non-loopback host", "http://gitlab.internal", "allowed only for a loopback host"},
		{"http on a private IP", "http://10.0.0.5", "allowed only for a loopback host"},
		{"other scheme", "ftp://gitlab.example.com", "must use https"},
		{"no scheme", "gitlab.example.com", "has no host"},
		{"no host", "https://", "has no host"},
		{"non-numeric port", "https://gitlab.example.com:notaport", "not a valid URL"},
		{"port zero", "https://gitlab.example.com:0", "outside 1-65535"},
		{"port too large", "https://gitlab.example.com:70000", "outside 1-65535"},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			clearURLEnv(t)
			t.Setenv("FULLSEND_GITLAB_URL", tc.value)
			// A valid fallback must not be selected instead.
			t.Setenv("CI_SERVER_URL", "https://gitlab.com")
			raw, env, err := ResolveURL()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Contains(t, err.Error(), "FULLSEND_GITLAB_URL")
			assert.Equal(t, "FULLSEND_GITLAB_URL", env)
			assert.Empty(t, raw)

			host, port, err := ResolveForgeHostPort()
			require.Error(t, err)
			assert.Empty(t, host)
			assert.Empty(t, port)
		})
	}

	t.Run("https scheme is case-insensitive", func(t *testing.T) {
		clearURLEnv(t)
		t.Setenv("CI_SERVER_URL", "HTTPS://gitlab.example.com")
		host, port, err := ResolveForgeHostPort()
		require.NoError(t, err)
		assert.Equal(t, "gitlab.example.com", host)
		assert.Equal(t, "443", port)
	})
}
