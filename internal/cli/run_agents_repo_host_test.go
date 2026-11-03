package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestAgentsRepoHostURLPrefix(t *testing.T) {
	tests := []struct {
		name   string
		client forge.Client
		want   string
	}{
		{name: "nil client", client: nil, want: ""},
		{name: "non-GitHub client", client: forge.NewFakeClient(), want: ""},
		{name: "github.com default", client: gh.New("t"), want: ""},
		{name: "github.com explicit", client: gh.New("t").WithBaseURL("https://api.github.com/"), want: ""},
		{
			name:   "enterprise server API base",
			client: gh.New("t").WithBaseURL("https://ghe.example.com/api/v3"),
			want:   "https://ghe.example.com/fullsend-ai/agents/raw/",
		},
		{
			name:   "host without API path",
			client: gh.New("t").WithBaseURL("https://emulator.example.test/"),
			want:   "https://emulator.example.test/fullsend-ai/agents/raw/",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, agentsRepoHostURLPrefix(tt.client))
		})
	}
}

// agentsRepoHostServer emulates a GitHub host other than github.com with
// its API under /api/v3. It serves the agents ref and the contents of the
// files in files, and records every request path.
type agentsRepoHostServer struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
	auth  []string
}

func newAgentsRepoHostServer(t *testing.T, sha string, files map[string][]byte) *agentsRepoHostServer {
	t.Helper()
	s := &agentsRepoHostServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.RequestURI())
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v3/repos/fullsend-ai/agents/git/ref/heads/main" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]string{"sha": sha, "type": "commit"},
			})
			return
		}
		if relPath, ok := strings.CutPrefix(r.URL.Path, "/api/v3/repos/fullsend-ai/agents/contents/"); ok && r.URL.Query().Get("ref") == sha {
			if content, ok := files[relPath]; ok {
				_ = json.NewEncoder(w).Encode(map[string]string{
					"content":  base64.StdEncoding.EncodeToString(content),
					"encoding": "base64",
				})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *agentsRepoHostServer) requests() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...), append([]string(nil), s.auth...)
}

// TestTryAgentsRepoFallback_OtherGitHubHost covers #9038: with
// GITHUB_API_URL naming a host other than github.com, the ref lookup and the
// harness fetch both go to that host, and the harness is recorded under the
// host's raw-content URL.
func TestTryAgentsRepoFallback_OtherGitHubHost(t *testing.T) {
	sha := "abcdef1234567890abcdef1234567890abcdef12"
	harnessContent := []byte("agent: agents/triage.md\nrole: test\n")
	srv := newAgentsRepoHostServer(t, sha, map[string][]byte{"harness/triage.yaml": harnessContent})
	t.Setenv("GITHUB_API_URL", srv.URL+"/api/v3")

	client := newGitHubLiveClient("host-token", "")
	hostPrefix := srv.URL + "/fullsend-ai/agents/raw/"
	opts := harness.ComposeOpts{
		WorkspaceRoot: t.TempDir(),
		// No domain is allowed for static fetches, so a fetch from
		// raw.githubusercontent.com (or anywhere else) would fail.
		FetchPolicy:  fetch.FetchPolicy{MaxSizeBytes: 1 << 20},
		OrgAllowlist: []string{hostPrefix},
	}

	path, deps, ok := tryAgentsRepoFallback(context.Background(), "triage", client, opts, ui.New(io.Discard))
	require.True(t, ok, "expected fallback to succeed on the configured host")
	require.Len(t, deps, 1)
	assert.Equal(t, hostPrefix+sha+"/harness/triage.yaml", deps[0].URL)
	assert.Equal(t, fetch.ComputeSHA256(harnessContent), deps[0].SHA256)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, harnessContent, got)

	paths, auth := srv.requests()
	assert.Equal(t, []string{
		"/api/v3/repos/fullsend-ai/agents/git/ref/heads/main",
		"/api/v3/repos/fullsend-ai/agents/contents/harness/triage.yaml?ref=" + sha,
	}, paths)
	for _, a := range auth {
		assert.Contains(t, a, "host-token", "requests to the configured host carry its token")
	}
}

func TestTryAgentsRepoMeasurementManifest_OtherGitHubHost404(t *testing.T) {
	sha := "abcdef1234567890abcdef1234567890abcdef12"
	srv := newAgentsRepoHostServer(t, sha, nil)
	t.Setenv("GITHUB_API_URL", srv.URL+"/api/v3")

	var buf bytes.Buffer
	opts := harness.ComposeOpts{
		WorkspaceRoot: t.TempDir(),
		OrgAllowlist:  []string{srv.URL + "/fullsend-ai/agents/raw/"},
	}
	_, ok := tryAgentsRepoMeasurementManifest(context.Background(), "triage", newGitHubLiveClient("t", ""), opts, ui.New(&buf))
	assert.False(t, ok)
	assert.Contains(t, buf.String(), "HTTP 404")
	assert.NotContains(t, buf.String(), "Failed to fetch")
}

// TestTryAgentsRepoFallback_OtherGitHubHostSizeLimit checks that the contents
// read on another host honours FetchPolicy.MaxSizeBytes like fetch.FetchURL.
func TestTryAgentsRepoFallback_OtherGitHubHostSizeLimit(t *testing.T) {
	sha := "abcdef1234567890abcdef1234567890abcdef12"
	srv := newAgentsRepoHostServer(t, sha, map[string][]byte{"harness/triage.yaml": []byte("agent: agents/triage.md\n")})
	t.Setenv("GITHUB_API_URL", srv.URL+"/api/v3")

	var buf bytes.Buffer
	opts := harness.ComposeOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   fetch.FetchPolicy{MaxSizeBytes: 4},
		OrgAllowlist:  []string{srv.URL + "/fullsend-ai/agents/raw/"},
	}
	_, _, ok := tryAgentsRepoFallback(context.Background(), "triage", newGitHubLiveClient("t", ""), opts, ui.New(&buf))
	assert.False(t, ok)
	assert.Contains(t, buf.String(), "Failed to fetch")
	assert.Contains(t, buf.String(), "size limit")
}

// TestTryAgentsRepoFallback_OtherGitHubHostNotAllowlisted checks that the
// host's raw-content URL is still subject to allowed_remote_resources: the
// default list names only raw.githubusercontent.com, so nothing is fetched.
func TestTryAgentsRepoFallback_OtherGitHubHostNotAllowlisted(t *testing.T) {
	sha := "abcdef1234567890abcdef1234567890abcdef12"
	srv := newAgentsRepoHostServer(t, sha, map[string][]byte{"harness/triage.yaml": []byte("agent: a\n")})
	t.Setenv("GITHUB_API_URL", srv.URL+"/api/v3")

	var buf bytes.Buffer
	opts := harness.ComposeOpts{
		WorkspaceRoot: t.TempDir(),
		OrgAllowlist:  config.DefaultAllowedRemoteResources(),
	}
	_, _, ok := tryAgentsRepoFallback(context.Background(), "triage", newGitHubLiveClient("t", ""), opts, ui.New(&buf))
	assert.False(t, ok)
	assert.Contains(t, buf.String(), "not in allowed_remote_resources")
	paths, _ := srv.requests()
	assert.Equal(t, []string{"/api/v3/repos/fullsend-ai/agents/git/ref/heads/main"}, paths)
}

func TestStatusNewForgeClientFn_HonoursGitHubAPIURL(t *testing.T) {
	t.Setenv("GITHUB_API_URL", "https://ghe.example.com/api/v3")
	client, ok := statusNewForgeClientFn("t").(*gh.LiveClient)
	require.True(t, ok)
	assert.Equal(t, "https://ghe.example.com/api/v3", client.BaseURL())

	t.Setenv("GITHUB_API_URL", "")
	client, ok = statusNewForgeClientFn("t").(*gh.LiveClient)
	require.True(t, ok)
	assert.Equal(t, "https://api.github.com", client.BaseURL())
}
