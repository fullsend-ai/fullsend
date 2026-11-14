package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// configuredHostServer stands in for a GitHub host other than github.com:
// it serves raw content from itself at /<sha>/<path>, the shape the
// agents-repo fallback and base: chains on such a host use.
type configuredHostServer struct {
	*httptest.Server
	sha string
}

func newConfiguredHostServer(t *testing.T, sha string, files map[string][]byte) *configuredHostServer {
	t.Helper()
	s := &configuredHostServer{sha: sha}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if content, ok := files[strings.TrimPrefix(r.URL.Path, "/"+sha+"/")]; ok && strings.HasPrefix(r.URL.Path, "/"+sha+"/") {
			_, _ = w.Write(content)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "not found")
	}))
	t.Cleanup(s.Close)
	return s
}

// policyFor returns a fetch policy like the ones dispatch builds
// (newDispatchFetchPolicy), adapted for the test server's self-signed
// certificate and loopback address.
func (s *configuredHostServer) policyFor(t *testing.T) *fetch.FetchPolicy {
	t.Helper()
	u, err := url.Parse(s.URL)
	require.NoError(t, err)
	tlsCfg := s.TLS.Clone()
	tlsCfg.InsecureSkipVerify = true
	policy := fetch.NewTestPolicy(tlsCfg, []string{u.Hostname()}, []string{u.Port()})
	return &policy
}

func TestNewDispatchFetchPolicy(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")

	t.Run("github.com keeps the default domains", func(t *testing.T) {
		t.Setenv("GITHUB_SERVER_URL", "")
		t.Setenv("GITHUB_API_URL", "")
		policy := newDispatchFetchPolicy("")
		assert.Equal(t, fetch.DefaultPolicy.AllowedDomains, policy.AllowedDomains)
	})

	t.Run("an API URL alone admits nothing", func(t *testing.T) {
		t.Setenv("GITHUB_SERVER_URL", "")
		t.Setenv("GITHUB_API_URL", "https://ghe.example.com/api/v3")
		policy := newDispatchFetchPolicy("")
		assert.Equal(t, fetch.DefaultPolicy.AllowedDomains, policy.AllowedDomains)
	})

	t.Run("the configured host is admitted", func(t *testing.T) {
		t.Setenv("GITHUB_SERVER_URL", "https://ghe.example.com")
		t.Setenv("GITHUB_API_URL", "https://ghe.example.com/api/v3")
		policy := newDispatchFetchPolicy("")
		assert.Equal(t, append([]string{}, fetch.DefaultPolicy.AllowedDomains...), policy.AllowedDomains[:len(fetch.DefaultPolicy.AllowedDomains)])
		assert.Contains(t, policy.AllowedDomains, "ghe.example.com")
	})
}

// A config-registered harness whose base: chain sits on the configured
// GitHub host must load in the dispatch route, so its role: sets the
// authorization threshold instead of the route failing closed to write
// with "domain not in allowlist" (the #9038 dispatch-path symptom).
func TestRouteBuiltinAgent_BaseChainOnConfiguredHost(t *testing.T) {
	commentOnly := strings.TrimPrefix(routeTestCommentOnlyTrigger, "trigger: >\n")

	// role: code needs write, so a triage actor is denied only when the
	// base harness (and its role:) actually loaded.
	base := []byte("agent: agents/review.md\nrole: code\n")
	sum := sha256.Sum256(base)
	sha := strings.Repeat("a1b2c3d4", 5) // 40 hex chars
	srv := newConfiguredHostServer(t, sha, map[string][]byte{
		"harness/base.yaml": base,
		"agents/review.md":  []byte("# review\n"),
	})
	baseURL := srv.URL + "/" + sha + "/harness/base.yaml#sha256=" + hex.EncodeToString(sum[:])

	// fixture returns a fresh config dir: the fetch cache lives under it,
	// so every subtest starts cold.
	fixture := func(t *testing.T) string {
		t.Helper()
		return writeRouteFixture(t, routeFixture{
			allowlist: []string{srv.URL + "/" + sha + "/"},
			agents: []config.AgentEntry{{
				Name:    "review",
				Source:  "harness/review.yaml",
				Trigger: commentOnly,
			}},
			files: map[string]string{
				"harness/review.yaml": "base: " + baseURL + "\n",
			},
		})
	}

	t.Run("host-admitting policy loads the base chain", func(t *testing.T) {
		got, err := routeBuiltinAgent(context.Background(), routeOpts{
			agent: "review", configDir: fixture(t), event: routeEvent(t, routeTriageReviewComment),
			fetchPolicy: srv.policyFor(t), printer: ui.New(io.Discard),
		})
		require.NoError(t, err)
		assert.Equal(t, routeSkip, got.Decision)
		assert.Equal(t, "actor not authorized", got.Reason,
			"the base harness's role: was read, so the triage actor is denied")
	})

	t.Run("a write actor runs once the role is known", func(t *testing.T) {
		got, err := routeBuiltinAgent(context.Background(), routeOpts{
			agent: "review", configDir: fixture(t), event: routeEvent(t, routeReviewComment),
			fetchPolicy: srv.policyFor(t), printer: ui.New(io.Discard),
		})
		require.NoError(t, err)
		assert.Equal(t, routeRun, got.Decision)
	})

	t.Run("the default policy still refuses the host", func(t *testing.T) {
		// The failure mode the fix removes: without the configured host in
		// the policy the harness cannot load, and the route fails closed.
		policy := srv.policyFor(t)
		policy.AllowedDomains = []string{"unrelated.example.com"}
		got, err := routeBuiltinAgent(context.Background(), routeOpts{
			agent: "review", configDir: fixture(t), event: routeEvent(t, routeTriageReviewComment),
			fetchPolicy: policy, printer: ui.New(io.Discard),
		})
		require.NoError(t, err)
		assert.Equal(t, routeSkip, got.Decision)
		assert.Equal(t, "harness unavailable; actor below write", got.Reason)
	})
}

// The dispatch matrix path resolves and loads the same config-registered
// harnesses through ListTriggeredHarnesses, so the configured host must be
// admitted there too: the harness-dispatch job of reusable-dispatch.yml
// loads every registered harness before any agent job is reached, and the
// default policy refusing the host is exactly the #9038 dispatch-path
// symptom ("domain not in allowlist", every agent skipped).
func TestListTriggeredHarnesses_BaseChainOnConfiguredHost(t *testing.T) {
	base := []byte("agent: agents/issue-ping.md\nrole: ping\n")
	sum := sha256.Sum256(base)
	sha := strings.Repeat("c3d4e5f6", 5)
	srv := newConfiguredHostServer(t, sha, map[string][]byte{
		"harness/base.yaml":    base,
		"agents/issue-ping.md": []byte("# issue-ping\n"),
	})
	baseURL := srv.URL + "/" + sha + "/harness/base.yaml#sha256=" + hex.EncodeToString(sum[:])

	// fixture returns a fresh config dir and its loaded config: the fetch
	// cache lives under the dir, so every subtest starts cold.
	fixture := func(t *testing.T) (string, config.ConfigReader) {
		t.Helper()
		dir := filepath.Join(t.TempDir(), ".fullsend")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "issue-ping.yaml"),
			[]byte("base: "+baseURL+"\ntrigger: event.transition.kind == \"label_changed\"\n"), 0o644))
		cfg := config.NewPerRepoConfig(nil, "fullsend-ai/demo")
		cfg.SetAgents([]config.AgentEntry{{Name: "issue-ping", Source: "harness/issue-ping.yaml"}})
		cfg.SetAllowedRemoteResources([]string{srv.URL + "/" + sha + "/"})
		data, err := yaml.Marshal(cfg)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))
		dirCfg, err := config.LoadConfig(dir, config.LoadOpts{MissingOK: false})
		require.NoError(t, err)
		return dir, dirCfg
	}

	t.Run("host-admitting policy loads the base chain", func(t *testing.T) {
		dir, dirCfg := fixture(t)
		out, err := harnessdispatch.ListTriggeredHarnesses(context.Background(), dir, dirCfg, srv.policyFor(t))
		require.NoError(t, err)
		require.Len(t, out, 1)
		assert.Equal(t, "issue-ping", out[0].Name)
	})

	t.Run("the default policy still refuses the host", func(t *testing.T) {
		dir, dirCfg := fixture(t)
		policy := srv.policyFor(t)
		policy.AllowedDomains = []string{"unrelated.example.com"}
		_, err := harnessdispatch.ListTriggeredHarnesses(context.Background(), dir, dirCfg, policy)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "domain not in allowlist")
	})
}

// A harness the route resolves through the agents-repo fallback on the
// configured host reads its relative companion files and directory
// resources through that host, so the route can read the harness's real
// role: threshold instead of failing closed to write.
func TestRouteBuiltinAgent_FallbackCompanionsOnConfiguredHost(t *testing.T) {
	commentOnly := strings.TrimPrefix(routeTestCommentOnlyTrigger, "trigger: >\n")

	sha := strings.Repeat("d4e5f6a7", 5)
	srv := newAgentsRepoHostServer(t, sha, map[string][]byte{
		"harness/review.yaml": []byte(
			"agent: agents/review.md\nrole: code\nskills:\n  - skills/issue-labels\n"),
		"agents/review.md":               []byte("# review\n"),
		"skills/issue-labels/SKILL.md":   []byte("# issue labels\n"),
		"skills/issue-labels/labels.txt": []byte("bug\ndocumentation\n"),
	})
	useConfiguredGitHubHost(t, srv.URL)

	// A fresh config dir per case: the fetch cache lives under it, so the
	// second case starts cold and its requests are attributable.
	route := func(t *testing.T, rawEvent string) routeDecision {
		t.Helper()
		dir := writeRouteFixture(t, routeFixture{
			allowlist: []string{srv.URL + "/fullsend-ai/agents/raw/"},
			agents: []config.AgentEntry{{
				Name:    "review",
				Trigger: commentOnly,
			}},
		})
		got, err := routeBuiltinAgent(context.Background(), routeOpts{
			agent: "review", configDir: dir, event: routeEvent(t, rawEvent),
			printer: ui.New(io.Discard),
		})
		require.NoError(t, err)
		return got
	}

	t.Run("triage actor is denied by the harness role", func(t *testing.T) {
		got := route(t, routeTriageReviewComment)
		assert.Equal(t, routeSkip, got.Decision)
		assert.Equal(t, "actor not authorized", got.Reason,
			"the fallback harness's companions loaded from the configured host, so its role: was read and the triage actor is denied")
	})

	t.Run("write actor runs, proving the role was read", func(t *testing.T) {
		got := route(t, routeReviewComment)
		assert.Equal(t, routeRun, got.Decision)
	})

	// The companions and the skill directory must have been read through
	// the configured host's contents API, never through a raw fetch.
	paths, _ := srv.requests()
	require.NotEmpty(t, paths, "the configured host's API should have served the harness and its resources")
	for _, p := range paths {
		assert.True(t, strings.HasPrefix(p, "/api/v3/repos/fullsend-ai/agents/"),
			"every request must go through the host's contents API, got %s", p)
	}
	joined := strings.Join(paths, "\n")
	assert.Contains(t, joined, "/contents/agents/review.md")
	assert.Contains(t, joined, "/contents/skills/issue-labels")
}

// TestDispatchRouteOpts_ConfiguredHostClient checks the client `fullsend
// dispatch --route` hands the route. With a token resolved, it must not be
// the authenticated client runDispatch builds for event lookups (which
// follows GITHUB_API_URL on its own): a GITHUB_API_URL that GITHUB_SERVER_URL
// does not confirm receives no request and no token, while an agreeing pair
// routes the fallback's ref lookup and reads there with the token.
func TestDispatchRouteOpts_ConfiguredHostClient(t *testing.T) {
	commentOnly := strings.TrimPrefix(routeTestCommentOnlyTrigger, "trigger: >\n")
	sha := strings.Repeat("e5f6a7b8", 5)
	newHost := func(t *testing.T) *agentsRepoHostServer {
		t.Helper()
		return newAgentsRepoHostServer(t, sha, map[string][]byte{
			"harness/review.yaml": []byte("agent: agents/review.md\nrole: code\n"),
			"agents/review.md":    []byte("# review\n"),
		})
	}
	buildOpts := func(t *testing.T, srv *agentsRepoHostServer) routeOpts {
		t.Helper()
		dir := writeRouteFixture(t, routeFixture{
			allowlist: []string{srv.URL + "/fullsend-ai/agents/raw/"},
			agents:    []config.AgentEntry{{Name: "review", Trigger: commentOnly}},
		})
		return newDispatchRouteOpts(
			dispatchOpts{configDir: dir, forgeToken: "route-token"},
			"review", routeEvent(t, routeTriageReviewComment), nil, ui.New(io.Discard))
	}

	for _, tc := range []struct {
		name   string
		server string
	}{
		{name: "GITHUB_API_URL alone", server: ""},
		{name: "GITHUB_SERVER_URL disagrees", server: "https://ghe.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newHost(t)
			t.Setenv("GITHUB_API_URL", srv.URL+"/api/v3")
			old := githubServerBaseURLFn
			githubServerBaseURLFn = func() string { return tc.server }
			t.Cleanup(func() { githubServerBaseURLFn = old })

			opts := buildOpts(t, srv)
			client, ok := opts.forgeClient.(*gh.LiveClient)
			require.True(t, ok)
			assert.Equal(t, "https://api.github.com", client.BaseURL())

			// Route for real, with requests to api.github.com refused
			// locally so the test never reaches the network. The old wiring
			// sent the ref lookup, with the token, to the test server.
			blockGitHubComTransport(t)
			_, _ = routeBuiltinAgent(context.Background(), opts)
			paths, auth := srv.requests()
			assert.Empty(t, paths, "GITHUB_API_URL alone must not receive the route's requests")
			assert.Empty(t, auth, "GITHUB_API_URL alone must not receive the token")
		})
	}

	t.Run("server and API agree", func(t *testing.T) {
		srv := newHost(t)
		useConfiguredGitHubHost(t, srv.URL)

		opts := buildOpts(t, srv)
		client, ok := opts.forgeClient.(*gh.LiveClient)
		require.True(t, ok)
		assert.Equal(t, srv.URL+"/api/v3", client.BaseURL())

		got, err := routeBuiltinAgent(context.Background(), opts)
		require.NoError(t, err)
		assert.Equal(t, "actor not authorized", got.Reason,
			"the fallback harness loaded from the configured host, so its role: denied the triage actor")
		paths, auth := srv.requests()
		assert.Contains(t, paths, "/api/v3/repos/fullsend-ai/agents/git/ref/heads/main")
		require.NotEmpty(t, auth)
		for _, a := range auth {
			assert.Contains(t, a, "route-token")
		}
	})
}

// blockGitHubComTransport makes requests to api.github.com fail locally for
// the rest of the test, so a route that addresses github.com never reaches
// the network; every other host goes through the original transport. The
// LiveClient's http.Client has no Transport of its own, so it uses
// http.DefaultTransport. Not for use from parallel tests.
func blockGitHubComTransport(t *testing.T) {
	t.Helper()
	orig := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.EqualFold(r.URL.Hostname(), "api.github.com") {
			return nil, errors.New("api.github.com blocked in test")
		}
		return orig.RoundTrip(r)
	})
	t.Cleanup(func() { http.DefaultTransport = orig })
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
