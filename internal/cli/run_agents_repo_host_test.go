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

// useConfiguredGitHubHost points the run at a GitHub instance served from
// serverURL with its API under /api/v3: GITHUB_API_URL names that API, and
// githubServerBaseURLFn stands in for GITHUB_SERVER_URL, because
// config.GitHubServerBaseURL accepts only HTTPS and httptest.NewServer is
// plain HTTP.
func useConfiguredGitHubHost(t *testing.T, serverURL string) {
	t.Helper()
	t.Setenv("GITHUB_API_URL", serverURL+"/api/v3")
	old := githubServerBaseURLFn
	githubServerBaseURLFn = func() string { return serverURL }
	t.Cleanup(func() { githubServerBaseURLFn = old })
}

func TestGitHubAPIURLForServer(t *testing.T) {
	tests := []struct {
		name   string
		server string
		api    string
		want   string
	}{
		{name: "no server configured", server: "", api: "https://ghe.example.com/api/v3", want: ""},
		{name: "enterprise server", server: "https://ghe.example.com", api: "https://ghe.example.com/api/v3", want: "https://ghe.example.com/api/v3"},
		{name: "enterprise server, trailing slash", server: "https://ghe.example.com", api: "https://ghe.example.com/api/v3/", want: "https://ghe.example.com/api/v3"},
		{name: "enterprise server, mixed case", server: "https://ghe.example.com", api: "https://GHE.Example.com/api/v3", want: "https://ghe.example.com/api/v3"},
		{name: "enterprise server with port", server: "https://ghe.example.com:8443", api: "https://ghe.example.com:8443/api/v3", want: "https://ghe.example.com:8443/api/v3"},
		{name: "GHE.com", server: "https://octo.ghe.com", api: "https://api.octo.ghe.com", want: "https://api.octo.ghe.com"},
		{name: "GHE.com, mixed case", server: "https://octo.ghe.com", api: "https://API.Octo.ghe.com/", want: "https://api.octo.ghe.com"},
		{name: "disagreeing host", server: "https://ghe.example.com", api: "https://evil.example.net/api/v3", want: ""},
		{name: "github.com API with a GHES server", server: "https://ghe.example.com", api: "https://api.github.com", want: ""},
		{name: "same host without the API path", server: "https://ghe.example.com", api: "https://ghe.example.com", want: ""},
		{name: "same host, other path", server: "https://ghe.example.com", api: "https://ghe.example.com/proxy/api/v3", want: ""},
		{name: "GHE.com style with a path", server: "https://octo.ghe.com", api: "https://api.octo.ghe.com/api/v3", want: ""},
		{name: "port differs", server: "https://ghe.example.com", api: "https://ghe.example.com:8443/api/v3", want: ""},
		{name: "scheme differs", server: "https://ghe.example.com", api: "http://ghe.example.com/api/v3", want: ""},
		{name: "userinfo", server: "https://ghe.example.com", api: "https://u:p@ghe.example.com/api/v3", want: ""},
		{name: "query", server: "https://ghe.example.com", api: "https://ghe.example.com/api/v3?x=1", want: ""},
		{name: "empty API URL", server: "https://ghe.example.com", api: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, githubAPIURLForServer(tt.server, tt.api))
		})
	}
}

// TestConfiguredHostGitHubClients checks the clients that carry a token
// GITHUB_API_URL must not redirect on its own — the write-scoped status
// client, and the agents-repo fallback client fullsend run and dispatch
// --route build (newConfiguredHostGitHubClient). They follow GITHUB_API_URL
// only when GITHUB_SERVER_URL names another GitHub instance and the API URL
// is that instance's API; otherwise they stay on api.github.com.
func TestConfiguredHostGitHubClients(t *testing.T) {
	tests := []struct {
		name   string
		server string
		api    string
		want   string
	}{
		{name: "nothing set", want: ""},
		{name: "API URL alone", api: "https://ghe.example.com/api/v3", want: ""},
		{name: "github.com server, other API", server: "https://github.com", api: "https://ghe.example.com/api/v3", want: ""},
		{name: "github.com server and API", server: "https://github.com", api: "https://api.github.com", want: ""},
		{name: "enterprise server", server: "https://ghe.example.com", api: "https://ghe.example.com/api/v3", want: "https://ghe.example.com/api/v3"},
		{name: "GHE.com", server: "https://Octo.ghe.com", api: "https://api.octo.ghe.com", want: "https://api.octo.ghe.com"},
		{name: "server and API disagree", server: "https://ghe.example.com", api: "https://other.example.com/api/v3", want: ""},
		{name: "malformed server", server: "http://ghe.example.com", api: "http://ghe.example.com/api/v3", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GITHUB_SERVER_URL", tt.server)
			t.Setenv("GITHUB_API_URL", tt.api)
			assert.Equal(t, tt.want, configuredGitHubAPIURL())
			want := tt.want
			if want == "" {
				want = "https://api.github.com"
			}
			status, ok := statusNewForgeClientFn("t").(*gh.LiveClient)
			require.True(t, ok)
			assert.Equal(t, want, status.BaseURL(), "status client")
			assert.Equal(t, want, newConfiguredHostGitHubClient("t").BaseURL(), "fallback client")
			assert.Equal(t, want, newConfiguredHostGitHubClient("").BaseURL(), "unauthenticated route client")
		})
	}
}

func TestAgentsRepoHostURLPrefix(t *testing.T) {
	tests := []struct {
		name   string
		server string
		client forge.Client
		want   string
	}{
		{name: "nil client", server: "https://ghe.example.com", client: nil, want: ""},
		{name: "non-GitHub client", server: "https://ghe.example.com", client: forge.NewFakeClient(), want: ""},
		{name: "github.com default", client: gh.New("t"), want: ""},
		{name: "github.com server", server: "https://github.com", client: gh.New("t").WithBaseURL("https://ghe.example.com/api/v3"), want: ""},
		{name: "API URL without a server URL", client: gh.New("t").WithBaseURL("https://ghe.example.com/api/v3"), want: ""},
		{
			name:   "enterprise server API base",
			server: "https://ghe.example.com",
			client: gh.New("t").WithBaseURL("https://ghe.example.com/api/v3"),
			want:   "https://ghe.example.com/fullsend-ai/agents/raw/",
		},
		{
			name:   "GHE.com API host",
			server: "https://octo.ghe.com",
			client: gh.New("t").WithBaseURL("https://api.octo.ghe.com"),
			want:   "https://octo.ghe.com/fullsend-ai/agents/raw/",
		},
		{
			name:   "mixed-case URLs use the lowercased server host",
			server: "https://GHE.Example.com",
			client: gh.New("t").WithBaseURL("https://ghe.EXAMPLE.com/api/v3"),
			want:   "https://ghe.example.com/fullsend-ai/agents/raw/",
		},
		{
			name:   "client API disagrees with the server",
			server: "https://ghe.example.com",
			client: gh.New("t").WithBaseURL("https://other.example.com/api/v3"),
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GITHUB_SERVER_URL", tt.server)
			assert.Equal(t, tt.want, agentsRepoHostURLPrefix(tt.client))
		})
	}
}

// TestAgentsRepoHostAgreesWithAllowlist covers the #9038 review finding: the
// fallback's host prefix and fetch-policy host come from the same
// GITHUB_SERVER_URL value as the default allowed_remote_resources entries,
// so on GHE.com (API at api.<host>) or with a mixed-case URL the fallback
// URL is still allowlisted and its host still admitted.
func TestAgentsRepoHostAgreesWithAllowlist(t *testing.T) {
	sha := "abcdef1234567890abcdef1234567890abcdef12"
	tests := []struct {
		name     string
		server   string
		api      string
		wantHost string
	}{
		{name: "enterprise server", server: "https://ghe.example.com", api: "https://GHE.example.com/api/v3", wantHost: "ghe.example.com"},
		{name: "GHE.com", server: "https://Octo.ghe.com", api: "https://api.octo.ghe.com", wantHost: "octo.ghe.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GITHUB_SERVER_URL", tt.server)
			t.Setenv("GITHUB_API_URL", tt.api)
			client := newConfiguredHostGitHubClient("t")
			prefix := agentsRepoHostURLPrefix(client)
			policy := withAgentsRepoHostAllowed(fetch.DefaultPolicy, client)
			require.NotEmpty(t, prefix)
			assert.Contains(t, policy.AllowedDomains, tt.wantHost)
			assert.Equal(t, "https://"+tt.wantHost+"/fullsend-ai/agents/raw/", prefix)
			rawURL := prefix + sha + "/harness/triage.yaml"
			assert.NotEmpty(t, harness.MatchingAllowedPrefixInList(rawURL, config.DefaultAllowedRemoteResources()),
				"the fallback URL must be covered by the default allowlist built from the same GITHUB_SERVER_URL")
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
					"type":     "file",
					"path":     relPath,
					"sha":      fetch.ComputeSHA256(content),
					"content":  base64.StdEncoding.EncodeToString(content),
					"encoding": "base64",
				})
				return
			}
			// A directory: list its immediate files and subdirectories.
			listed := map[string]map[string]any{}
			for p, content := range files {
				rest, ok := strings.CutPrefix(p, relPath+"/")
				if !ok {
					continue
				}
				name, _, nested := strings.Cut(rest, "/")
				entry := map[string]any{"name": name, "path": relPath + "/" + name, "sha": fetch.ComputeSHA256(content), "type": "file", "size": len(content)}
				if nested {
					entry["type"], entry["size"] = "dir", 0
				}
				listed[name] = entry
			}
			if len(listed) > 0 {
				var entries []map[string]any
				for _, e := range listed {
					entries = append(entries, e)
				}
				_ = json.NewEncoder(w).Encode(entries)
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
// GITHUB_SERVER_URL and GITHUB_API_URL naming a host other than github.com, the ref lookup and the
// harness fetch both go to that host, and the harness is recorded under the
// host's raw-content URL.
func TestTryAgentsRepoFallback_OtherGitHubHost(t *testing.T) {
	sha := "abcdef1234567890abcdef1234567890abcdef12"
	harnessContent := []byte("agent: agents/triage.md\nrole: test\n")
	srv := newAgentsRepoHostServer(t, sha, map[string][]byte{"harness/triage.yaml": harnessContent})
	useConfiguredGitHubHost(t, srv.URL)

	client := newConfiguredHostGitHubClient("host-token")
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
	useConfiguredGitHubHost(t, srv.URL)

	var buf bytes.Buffer
	opts := harness.ComposeOpts{
		WorkspaceRoot: t.TempDir(),
		OrgAllowlist:  []string{srv.URL + "/fullsend-ai/agents/raw/"},
	}
	_, ok := tryAgentsRepoMeasurementManifest(context.Background(), "triage", newConfiguredHostGitHubClient("t"), opts, ui.New(&buf))
	assert.False(t, ok)
	assert.Contains(t, buf.String(), "HTTP 404")
	assert.NotContains(t, buf.String(), "Failed to fetch")
}

// TestTryAgentsRepoFallback_OtherGitHubHostSizeLimit checks that the contents
// read on another host honours FetchPolicy.MaxSizeBytes like fetch.FetchURL.
func TestTryAgentsRepoFallback_OtherGitHubHostSizeLimit(t *testing.T) {
	sha := "abcdef1234567890abcdef1234567890abcdef12"
	srv := newAgentsRepoHostServer(t, sha, map[string][]byte{"harness/triage.yaml": []byte("agent: agents/triage.md\n")})
	useConfiguredGitHubHost(t, srv.URL)

	var buf bytes.Buffer
	opts := harness.ComposeOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   fetch.FetchPolicy{MaxSizeBytes: 4},
		OrgAllowlist:  []string{srv.URL + "/fullsend-ai/agents/raw/"},
	}
	_, _, ok := tryAgentsRepoFallback(context.Background(), "triage", newConfiguredHostGitHubClient("t"), opts, ui.New(&buf))
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
	useConfiguredGitHubHost(t, srv.URL)

	var buf bytes.Buffer
	opts := harness.ComposeOpts{
		WorkspaceRoot: t.TempDir(),
		OrgAllowlist:  config.DefaultAllowedRemoteResources(),
	}
	_, _, ok := tryAgentsRepoFallback(context.Background(), "triage", newConfiguredHostGitHubClient("t"), opts, ui.New(&buf))
	assert.False(t, ok)
	assert.Contains(t, buf.String(), "not in allowed_remote_resources")
	paths, _ := srv.requests()
	assert.Equal(t, []string{"/api/v3/repos/fullsend-ai/agents/git/ref/heads/main"}, paths)
}

func TestStatusNewForgeClientFn_HonoursGitHubAPIURL(t *testing.T) {
	t.Setenv("GITHUB_SERVER_URL", "https://ghe.example.com")
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
	useConfiguredGitHubHost(t, srv.URL)

	client := newConfiguredHostGitHubClient("host-token")
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

// TestAgentsRepoHostFallback_LoadsDirectoryResources checks that directory
// resources of a fallback harness on another GitHub host are read through that
// host too: a relative skill directory loads with its companion files, and a
// script is fetched together with the helpers next to it. Both stay pinned to
// the harness's commit and use the host's token.
func TestAgentsRepoHostFallback_LoadsDirectoryResources(t *testing.T) {
	sha := "abcdef1234567890abcdef1234567890abcdef12"
	setup := func(t *testing.T, harnessContent string, files map[string][]byte) (string, harness.ComposeOpts, forge.Client) {
		t.Helper()
		files["harness/triage.yaml"] = []byte(harnessContent)
		srv := newAgentsRepoHostServer(t, sha, files)
		useConfiguredGitHubHost(t, srv.URL)
		client := newConfiguredHostGitHubClient("host-token")
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

	t.Run("skill directory loads", func(t *testing.T) {
		path, opts, _ := setup(t,
			"role: triage\nslug: triage\nagent: agents/triage.md\nskills:\n  - skills/review\n",
			map[string][]byte{
				"agents/triage.md":                  []byte("# triage agent"),
				"skills/review/SKILL.md":            []byte("# review"),
				"skills/review/checks/lint.md":      []byte("# lint"),
				"skills/unrelated/SKILL.md":         []byte("# unrelated"),
				"skills/review-extra/SKILL.md":      []byte("# sibling with a shared name prefix"),
				"skills/review-extra/checks/not.md": []byte("# not part of the skill"),
			})
		h, _, err := harness.LoadWithBase(context.Background(), path, opts)
		require.NoError(t, err)
		require.Len(t, h.Skills, 1)
		skillDir := h.Skills[0].Source
		got, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
		require.NoError(t, err)
		assert.Equal(t, "# review", string(got))
		got, err = os.ReadFile(filepath.Join(skillDir, "checks", "lint.md"))
		require.NoError(t, err)
		assert.Equal(t, "# lint", string(got))
		assert.NoFileExists(t, filepath.Join(skillDir, "checks", "not.md"))
	})

	t.Run("script helper is fetched", func(t *testing.T) {
		path, opts, _ := setup(t,
			"role: triage\nslug: triage\nagent: agents/triage.md\npre_script: scripts/pre-triage.sh\n",
			map[string][]byte{
				"agents/triage.md":      []byte("# triage agent"),
				"scripts/pre-triage.sh": []byte("#!/bin/bash\n. \"$(dirname \"$0\")/helper.sh\"\n"),
				"scripts/helper.sh":     []byte("echo helper\n"),
			})
		h, loaded, err := harness.LoadWithBase(context.Background(), path, opts)
		require.NoError(t, err)
		assert.FileExists(t, h.PreScript)
		got, err := os.ReadFile(filepath.Join(filepath.Dir(h.PreScript), "helper.sh"))
		require.NoError(t, err)
		assert.Equal(t, "echo helper\n", string(got))
		var urls []string
		for _, d := range loaded {
			urls = append(urls, d.URL)
		}
		assert.Contains(t, urls, strings.TrimSuffix(opts.SourceURL, "harness/triage.yaml")+"scripts/pre-triage.sh")
	})

}

// TestAgentsRepoHostTreeFetcher checks the directory hook: it lists and reads
// a directory of the agents repo at the pinned commit through the configured
// host, rejects unpinned or traversing paths without a request, applies the
// size bound, and hands any other repository to the next fetcher.
func TestAgentsRepoHostTreeFetcher(t *testing.T) {
	sha := "abcdef1234567890abcdef1234567890abcdef12"
	srv := newAgentsRepoHostServer(t, sha, map[string][]byte{
		"skills/review/SKILL.md":       []byte("# review"),
		"skills/review/checks/lint.md": []byte("# lint"),
	})
	useConfiguredGitHubHost(t, srv.URL)
	client := newConfiguredHostGitHubClient("host-token")
	prefix := srv.URL + "/fullsend-ai/agents/raw/"
	info, err := agentsRepoHostForgeInfo(prefix, sha, "skills/review")
	require.NoError(t, err)

	var delegated []string
	next := func(_ context.Context, cloneURL, _, _, _ string) (map[string][]byte, error) {
		delegated = append(delegated, cloneURL)
		return map[string][]byte{"x": nil}, nil
	}
	fetcher := agentsRepoHostTreeFetcher(client, prefix, fetch.FetchPolicy{MaxSizeBytes: 1 << 20}, next)

	files, err := fetcher(context.Background(), info.CloneURL(), info.Path, sha, "")
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{
		"SKILL.md":       []byte("# review"),
		"checks/lint.md": []byte("# lint"),
	}, files)
	assert.Empty(t, delegated)
	_, auth := srv.requests()
	for _, a := range auth {
		assert.Contains(t, a, "host-token")
	}

	_, err = fetcher(context.Background(), "https://github.com/some/repo.git", "skills/x", "main", "tok")
	require.NoError(t, err)
	assert.Equal(t, []string{"https://github.com/some/repo.git"}, delegated)

	before, _ := srv.requests()
	for _, tc := range []struct{ dir, ref string }{
		{"skills/review", "main"},
		{"skills/../review", sha},
		{"", sha},
	} {
		_, err := fetcher(context.Background(), info.CloneURL(), tc.dir, tc.ref, "")
		assert.ErrorContains(t, err, "unsupported agents repo directory", tc.dir)
	}
	after, _ := srv.requests()
	assert.Equal(t, before, after, "rejected directories make no request")

	small := agentsRepoHostTreeFetcher(client, prefix, fetch.FetchPolicy{MaxSizeBytes: 4}, next)
	_, err = small(context.Background(), info.CloneURL(), info.Path, sha, "")
	assert.ErrorContains(t, err, "size limit")
}

// TestAgentsRepoHostURLParser checks the directory URL hook: URLs under the
// host prefix describe the agents repo on that host at the named commit, and
// anything else is parsed as a raw.githubusercontent.com URL.
func TestAgentsRepoHostURLParser(t *testing.T) {
	sha := "abcdef1234567890abcdef1234567890abcdef12"
	prefix := "https://ghe.example.com/fullsend-ai/agents/raw/"
	parse := agentsRepoHostURLParser(prefix)

	info, err := parse(prefix + sha + "/skills/review")
	require.NoError(t, err)
	assert.Equal(t, "skills/review", info.Path)
	assert.Equal(t, sha, info.Ref)
	assert.Equal(t, "https://ghe.example.com/fullsend-ai/agents.git", info.CloneURL())

	info, err = parse("https://raw.githubusercontent.com/o/r/main/skills/x")
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/o/r.git", info.CloneURL())

	for _, u := range []string{
		prefix + "main/skills/review",
		prefix + sha + "/skills/../x",
		prefix + sha + "/",
	} {
		_, err := parse(u)
		assert.ErrorContains(t, err, "unsupported agents repo URL", u)
	}
	_, err = parse("https://other.example.com/o/r/main/x")
	assert.Error(t, err)
}

// TestAgentsRepoHostFileFetcher_Rejects checks the hook only reads files
// pinned to a full commit SHA under the host prefix and honours the size limit.
func TestAgentsRepoHostFileFetcher_Rejects(t *testing.T) {
	sha := "abcdef1234567890abcdef1234567890abcdef12"
	srv := newAgentsRepoHostServer(t, sha, map[string][]byte{"agents/a.md": []byte("0123456789")})
	useConfiguredGitHubHost(t, srv.URL)
	client := newConfiguredHostGitHubClient("t")
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
		server      string
		client      forge.Client
		wantDomains []string
		wantPorts   []string
	}{
		{name: "nil client", client: nil, wantDomains: defaultDomains},
		{name: "non-GitHub client", client: forge.NewFakeClient(), wantDomains: defaultDomains},
		{name: "github.com default", client: gh.New("t"), wantDomains: defaultDomains},
		{
			name:        "enterprise server",
			server:      "https://ghe.example.com",
			client:      gh.New("t").WithBaseURL("https://ghe.example.com/api/v3"),
			wantDomains: append(append([]string(nil), defaultDomains...), "ghe.example.com"),
		},
		{
			name:        "host with port",
			server:      "https://ghe.example.com:8443",
			client:      gh.New("t").WithBaseURL("https://ghe.example.com:8443/api/v3"),
			wantDomains: append(append([]string(nil), defaultDomains...), "ghe.example.com"),
			wantPorts:   []string{"443", "8443"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GITHUB_SERVER_URL", tt.server)
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
		t.Setenv("GITHUB_SERVER_URL", "https://ghe.example.invalid")
		client := gh.New("t").WithBaseURL("https://ghe.example.invalid/api/v3")

		_, err := fetch.FetchURL(context.Background(), rawURL, fetch.DefaultPolicy)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "domain not in allowlist")

		_, err = fetch.FetchURL(context.Background(), rawURL, withAgentsRepoHostAllowed(fetch.DefaultPolicy, client))
		require.Error(t, err) // the .invalid host does not resolve
		assert.NotContains(t, err.Error(), "domain not in allowlist")
	})
}

// TestReadAgentsRepoFileAtRef_RawMediaType checks that the agents-repo host
// path reads through GetRawFileContentAtRef on the GitHub client (raw media
// type, so files over the 1 MiB JSON limit come back), and through
// GetFileContentAtRef on any other client.
func TestReadAgentsRepoFileAtRef_RawMediaType(t *testing.T) {
	sha := "abcdef1234567890abcdef1234567890abcdef12"
	var accept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/vnd.github.raw")
		_, _ = io.WriteString(w, "raw body\n")
	}))
	t.Cleanup(srv.Close)
	useConfiguredGitHubHost(t, srv.URL)

	policy := fetch.FetchPolicy{MaxSizeBytes: 1 << 20}
	got, err := readAgentsRepoFileAtRef(context.Background(), newConfiguredHostGitHubClient("t"), "agents/a.md", sha, policy)
	require.NoError(t, err)
	assert.Equal(t, "raw body\n", string(got))
	assert.Equal(t, "application/vnd.github.raw", accept)

	fake := forge.NewFakeClient()
	fake.FileContentsRef = map[string][]byte{"fullsend-ai/agents/agents/a.md@" + sha: []byte("from fake")}
	got, err = readAgentsRepoFileAtRef(context.Background(), fake, "agents/a.md", sha, policy)
	require.NoError(t, err)
	assert.Equal(t, "from fake", string(got))
}
