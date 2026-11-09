package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
		t.Setenv("GITHUB_API_URL", "")
		policy := newDispatchFetchPolicy("")
		assert.Equal(t, fetch.DefaultPolicy.AllowedDomains, policy.AllowedDomains)
	})

	t.Run("the configured host is admitted", func(t *testing.T) {
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
