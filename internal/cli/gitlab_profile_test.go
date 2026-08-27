package cli

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateGitLabForgeProfile(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		wantEmpty   bool
		wantErr     string
		wantHost    string
		wantPort    string
		notContains string
	}{
		{
			name:     "default port from CI_SERVER_URL",
			env:      map[string]string{"CI_SERVER_URL": "https://gitlab.cee.redhat.com"},
			wantHost: "host: gitlab.cee.redhat.com",
			wantPort: "port: 443",
		},
		{
			name:     "non-standard port",
			env:      map[string]string{"CI_SERVER_URL": "https://gitlab.company.com:8443"},
			wantHost: "host: gitlab.company.com",
			wantPort: "port: 8443",
		},
		{
			name:      "no env vars",
			env:       map[string]string{},
			wantEmpty: true,
		},
		{
			name:        "FULLSEND_GITLAB_URL takes precedence",
			env:         map[string]string{"FULLSEND_GITLAB_URL": "https://gitlab.company.com", "CI_SERVER_URL": "https://gitlab.other.com"},
			wantHost:    "host: gitlab.company.com",
			wantPort:    "port: 443",
			notContains: "gitlab.other.com",
		},
		{
			name:     "gitlab.com",
			env:      map[string]string{"CI_SERVER_URL": "https://gitlab.com"},
			wantHost: "host: gitlab.com",
			wantPort: "port: 443",
		},
		{
			name:     "http scheme defaults to port 80",
			env:      map[string]string{"CI_SERVER_URL": "http://gitlab.internal"},
			wantHost: "host: gitlab.internal",
			wantPort: "port: 80",
		},
		{
			name:      "invalid URL yields empty result",
			env:       map[string]string{"CI_SERVER_URL": "https://gitlab.company.com:notaport"},
			wantEmpty: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{"FULLSEND_GITLAB_URL", "GITLAB_API_URL", "CI_SERVER_URL"} {
				t.Setenv(key, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			profilePath, cleanup, err := generateGitLabForgeProfile()
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)

			if tc.wantEmpty {
				assert.Empty(t, profilePath)
				assert.Nil(t, cleanup)
				return
			}

			require.NotEmpty(t, profilePath)
			defer cleanup()

			data, err := os.ReadFile(profilePath)
			require.NoError(t, err)
			content := string(data)
			assert.Contains(t, content, "id: fullsend-gitlab-forge")
			assert.Contains(t, content, tc.wantHost)
			assert.Contains(t, content, tc.wantPort)
			assert.Contains(t, content, "category: source_control")
			assert.Contains(t, content, "**/node")
			if tc.notContains != "" {
				assert.NotContains(t, content, tc.notContains)
			}
		})
	}
}
