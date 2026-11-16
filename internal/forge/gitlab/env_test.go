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
			// ValidateBaseURL refuses http for a host that is not loopback.
			name:     "http for a host that is not loopback",
			env:      map[string]string{"CI_SERVER_URL": "http://gitlab.example.com"},
			wantHost: "",
			wantPort: "",
		},
		{
			name:     "http for a loopback host defaults to port 80",
			env:      map[string]string{"CI_SERVER_URL": "http://localhost"},
			wantHost: "localhost",
			wantPort: "80",
		},
		{
			name:     "https",
			env:      map[string]string{"FULLSEND_GITLAB_URL": "https://gitlab.example.com"},
			wantHost: "gitlab.example.com",
			wantPort: "443",
		},
		{
			// The clients use the first non-empty variable as is, so a
			// malformed one must not fall through to a later variable.
			name:     "malformed first value does not fall through",
			env:      map[string]string{"FULLSEND_GITLAB_URL": "not a url", "CI_SERVER_URL": "https://gitlab.example.com"},
			wantHost: "",
			wantPort: "",
		},
		{
			name:     "unparsable first value does not fall through",
			env:      map[string]string{"GITLAB_API_URL": "https://gitlab.example.com:notaport", "CI_SERVER_URL": "https://gitlab.example.com"},
			wantHost: "",
			wantPort: "",
		},
		{
			name:     "http for a loopback host with an explicit port",
			env:      map[string]string{"CI_SERVER_URL": "http://127.0.0.1:3000"},
			wantHost: "127.0.0.1",
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

func TestResolveBaseURL(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		env        map[string]string
		wantRaw    string
		wantSource string
	}{
		{name: "nothing set"},
		{name: "configured wins", configured: "https://gitlab.manifest.example",
			env:     map[string]string{"FULLSEND_GITLAB_URL": "https://gitlab.env.example"},
			wantRaw: "https://gitlab.manifest.example", wantSource: ConfiguredURLSource},
		{name: "first non-empty variable", env: map[string]string{"GITLAB_API_URL": "  https://gitlab.api.example ", "CI_SERVER_URL": "https://gitlab.ci.example"},
			wantRaw: "https://gitlab.api.example", wantSource: "GITLAB_API_URL"},
		{name: "malformed first variable is returned as is", env: map[string]string{"FULLSEND_GITLAB_URL": "not a url", "CI_SERVER_URL": "https://gitlab.ci.example"},
			wantRaw: "not a url", wantSource: "FULLSEND_GITLAB_URL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range URLEnvVars {
				t.Setenv(key, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			raw, source := ResolveBaseURL(tc.configured)
			assert.Equal(t, tc.wantRaw, raw)
			assert.Equal(t, tc.wantSource, source)
		})
	}
}

func TestHostPort(t *testing.T) {
	for raw, want := range map[string][2]string{
		"https://gitlab.example.com":      {"gitlab.example.com", "443"},
		"http://gitlab.example.com":       {"gitlab.example.com", "80"},
		"https://gitlab.example.com:8443": {"gitlab.example.com", "8443"},
		"gitlab.example.com":              {"", ""},
		"https://gitlab.example.com:bad":  {"", ""},
	} {
		host, port := HostPort(raw)
		assert.Equal(t, want, [2]string{host, port}, raw)
	}
}
