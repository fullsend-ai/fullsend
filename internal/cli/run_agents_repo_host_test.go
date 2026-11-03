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
	"path/filepath"
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

// TestAgentsRepoHostFallback_LoadsCompanionFiles checks that, after the
// fallback fetches a harness from another GitHub host, LoadWithBase resolves
// the harness's relative agent, policy, and script files through that host
// with the options runAgent builds: commit-pinned, allowlisted, and fetched
// with the host's token rather than the github.com-only default policy.
func TestAgentsRepoHostFallback_LoadsCompanionFiles(t *testing.T) {
	sha := "abcdef1234567890abcdef1234567890abcdef12"
	harnessContent := []byte("role: triage\nslug: triage\nagent: agents/triage.md\npolicy: policies/triage.yaml\npre_script: scripts/pre-triage.sh\n")
	srv := newAgentsRepoHostServer(t, sha, map[string][]byte{
		"harness/triage.yaml":   harnessContent,
		"agents/triage.md":      []byte("# triage agent"),
		"policies/triage.yaml":  []byte("# triage policy"),
		"scripts/pre-triage.sh": []byte("#!/bin/bash\necho pre\n"),
	})
	t.Setenv("GITHUB_API_URL", srv.URL+"/api/v3")

	client := newGitHubLiveClient("host-token", "")
	hostPrefix := srv.URL + "/fullsend-ai/agents/raw/"
	opts := harness.ComposeOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   fetch.FetchPolicy{MaxSizeBytes: 1 << 20},
		OrgAllowlist:  []string{hostPrefix},
	}

	path, deps, ok := tryAgentsRepoFallback(context.Background(), "triage", client, opts, ui.New(io.Discard))
	require.True(t, ok)
	opts.SourceURL = deps[0].URL
	useAgentsRepoHostFetch(&opts, client)

	h, loaded, err := harness.LoadWithBase(context.Background(), path, opts)
	require.NoError(t, err)
	got, err := os.ReadFile(h.Agent)
	require.NoError(t, err)
	assert.Equal(t, "# triage agent", string(got))
	got, err = os.ReadFile(h.Policy)
	require.NoError(t, err)
	assert.Equal(t, "# triage policy", string(got))
	assert.FileExists(t, h.PreScript)
	var urls []string
	for _, d := range loaded {
		urls = append(urls, d.URL)
	}
	assert.Contains(t, urls, hostPrefix+sha+"/agents/triage.md")

	paths, auth := srv.requests()
	assert.Contains(t, paths, "/api/v3/repos/fullsend-ai/agents/contents/agents/triage.md?ref="+sha)
	for _, a := range auth {
		assert.Contains(t, a, "host-token")
	}
}

// TestAgentsRepoHostFallback_DirectoryResourcesUnsupported documents the
// documented limit of the configured-host transport: it reads single files
// only. A relative skill directory cannot be fetched from the host, and a
// script with a directory part is fetched as its entry file alone, without
// the adjacent helpers.
func TestAgentsRepoHostFallback_DirectoryResourcesUnsupported(t *testing.T) {
	sha := "abcdef1234567890abcdef1234567890abcdef12"
	setup := func(t *testing.T, harnessContent string, files map[string][]byte) (string, harness.ComposeOpts, forge.Client) {
		t.Helper()
		files["harness/triage.yaml"] = []byte(harnessContent)
		srv := newAgentsRepoHostServer(t, sha, files)
		t.Setenv("GITHUB_API_URL", srv.URL+"/api/v3")
		client := newGitHubLiveClient("host-token", "")
		opts := harness.ComposeOpts{
			WorkspaceRoot: t.TempDir(),
			FetchPolicy:   fetch.FetchPolicy{MaxSizeBytes: 1 << 20},
			OrgAllowlist:  []string{srv.URL + "/fullsend-ai/agents/raw/"},
		}
		path, deps, ok := tryAgentsRepoFallback(context.Background(), "triage", client, opts, ui.New(io.Discard))
		require.True(t, ok)
		opts.SourceURL = deps[0].URL
		useAgentsRepoHostFetch(&opts, client)
		return path, opts, client
	}

	t.Run("skill directory fails", func(t *testing.T) {
		path, opts, _ := setup(t,
			"role: triage\nslug: triage\nagent: agents/triage.md\nskills:\n  - skills/review\n",
			map[string][]byte{
				"agents/triage.md":       []byte("# triage agent"),
				"skills/review/SKILL.md": []byte("# review"),
			})
		_, _, err := harness.LoadWithBase(context.Background(), path, opts)
		require.Error(t, err)
	})

	t.Run("script helper is not fetched", func(t *testing.T) {
		path, opts, _ := setup(t,
			"role: triage\nslug: triage\nagent: agents/triage.md\npre_script: scripts/pre-triage.sh\n",
			map[string][]byte{
				"agents/triage.md":      []byte("# triage agent"),
				"scripts/pre-triage.sh": []byte("#!/bin/bash\n. \"$(dirname \"$0\")/helper.sh\"\n"),
				"scripts/helper.sh":     []byte("echo helper\n"),
			})
		h, _, err := harness.LoadWithBase(context.Background(), path, opts)
		require.NoError(t, err)
		assert.FileExists(t, h.PreScript)
		assert.NoFileExists(t, filepath.Join(filepath.Dir(h.PreScript), "helper.sh"))
	})
}

// TestAgentsRepoHostFileFetcher_Rejects checks the hook only reads files
// pinned to a full commit SHA under the host prefix and honours the size limit.
func TestAgentsRepoHostFileFetcher_Rejects(t *testing.T) {
	sha := "abcdef1234567890abcdef1234567890abcdef12"
	srv := newAgentsRepoHostServer(t, sha, map[string][]byte{"agents/a.md": []byte("0123456789")})
	t.Setenv("GITHUB_API_URL", srv.URL+"/api/v3")
	client := newGitHubLiveClient("t", "")
	prefix := srv.URL + "/fullsend-ai/agents/raw/"
	fetcher := agentsRepoHostFileFetcher(client, prefix, fetch.FetchPolicy{MaxSizeBytes: 4})

	for _, u := range []string{
		prefix + "main/agents/a.md",
		prefix + sha + "/../agents/a.md",
		prefix + sha + "/agents/../a.md",
		prefix + sha + "/",
		prefix + sha + "/agents/a.md?ref=main",
	} {
		_, err := fetcher(context.Background(), u)
		assert.ErrorContains(t, err, "unsupported agents repo URL", u)
	}
	_, err := fetcher(context.Background(), prefix+sha+"/agents/a.md")
	assert.ErrorContains(t, err, "size limit")
	paths, _ := srv.requests()
	assert.Len(t, paths, 1, "rejected URLs make no request")
}

// TestWithAgentsRepoHostAllowed checks that the fetch policy runAgent hands to
// composition admits the configured GitHub host (#9038): the default policy
// names only github.com hosts, so a base harness or companion file served by
// another host would otherwise fail with "domain not in allowlist".
func TestWithAgentsRepoHostAllowed(t *testing.T) {
	defaultDomains := append([]string(nil), fetch.DefaultPolicy.AllowedDomains...)

	tests := []struct {
		name        string
		client      forge.Client
		wantDomains []string
		wantPorts   []string
	}{
		{name: "nil client", client: nil, wantDomains: defaultDomains},
		{name: "non-GitHub client", client: forge.NewFakeClient(), wantDomains: defaultDomains},
		{name: "github.com default", client: gh.New("t"), wantDomains: defaultDomains},
		{
			name:        "enterprise server",
			client:      gh.New("t").WithBaseURL("https://ghe.example.com/api/v3"),
			wantDomains: append(append([]string(nil), defaultDomains...), "ghe.example.com"),
		},
		{
			name:        "host with port",
			client:      gh.New("t").WithBaseURL("https://ghe.example.com:8443/api/v3"),
			wantDomains: append(append([]string(nil), defaultDomains...), "ghe.example.com"),
			wantPorts:   []string{"443", "8443"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := withAgentsRepoHostAllowed(fetch.DefaultPolicy, tt.client)
			assert.Equal(t, tt.wantDomains, got.AllowedDomains)
			assert.Equal(t, tt.wantPorts, got.AllowedPorts)
			// The shared default policy is never modified.
			assert.Equal(t, defaultDomains, fetch.DefaultPolicy.AllowedDomains)
			assert.Empty(t, fetch.DefaultPolicy.AllowedPorts)
		})
	}

	t.Run("host is no longer refused by the policy", func(t *testing.T) {
		const rawURL = "https://ghe.example.invalid/fullsend-ai/agents/raw/abc/harness/triage.yaml"
		client := gh.New("t").WithBaseURL("https://ghe.example.invalid/api/v3")

		_, err := fetch.FetchURL(context.Background(), rawURL, fetch.DefaultPolicy)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "domain not in allowlist")

		_, err = fetch.FetchURL(context.Background(), rawURL, withAgentsRepoHostAllowed(fetch.DefaultPolicy, client))
		require.Error(t, err) // the .invalid host does not resolve
		assert.NotContains(t, err.Error(), "domain not in allowlist")
	})
}
