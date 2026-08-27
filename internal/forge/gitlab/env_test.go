package gitlab

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
			env:      map[string]string{"GITLAB_API_URL": "https://gitlab.api.example.com", "CI_SERVER_URL": "https://gitlab.other.com"},
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
			name:     "gitlab.com",
			env:      map[string]string{"CI_SERVER_URL": "https://gitlab.com"},
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
			name:     "http scheme defaults to port 80",
			env:      map[string]string{"CI_SERVER_URL": "http://gitlab.internal"},
			wantHost: "gitlab.internal",
			wantPort: "80",
		},
		{
			name:     "http scheme explicit port",
			env:      map[string]string{"CI_SERVER_URL": "http://gitlab.internal:3000"},
			wantHost: "gitlab.internal",
			wantPort: "3000",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range URLEnvVars {
				t.Setenv(key, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			host, port := ResolveForgeHostPort()
			assert.Equal(t, tc.wantHost, host)
			assert.Equal(t, tc.wantPort, port)
		})
	}
}
