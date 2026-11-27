package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/jirapoll"
)

// These tests drive newCELMatcher through the production built-in resolver
// (newJiraBuiltinResolverFn is NOT replaced). Only the external GitHub
// boundary is faked: an httptest server that answers the agents-repo ref
// lookup and contents reads, reached through GITHUB_SERVER_URL and
// GITHUB_API_URL as on a GitHub Enterprise Server host. The coverage is
// fixture-based; it is not a live Jira or live GitHub validation.

const (
	jiraProdMainSHA = "1111111111111111111111111111111111111111"
	jiraProdTagSHA  = "2222222222222222222222222222222222222222"
)

// jiraProdGitHub emulates the GitHub API for fullsend-ai/agents: refs maps
// a ref path (heads/main, tags/v0.85.0) to a commit SHA, and files maps a
// commit SHA to its files. Every request path is recorded; anything it does
// not serve is a 404.
type jiraProdGitHub struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
}

func newJiraProdGitHub(t *testing.T, refs map[string]string, files map[string]map[string][]byte) *jiraProdGitHub {
	t.Helper()
	s := &jiraProdGitHub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.RequestURI())
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		const apiRepo = "/api/v3/repos/fullsend-ai/agents/"
		if ref, ok := strings.CutPrefix(r.URL.Path, apiRepo+"git/ref/"); ok {
			if sha, found := refs[ref]; found {
				_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": sha, "type": "commit"}})
				return
			}
		}
		if rel, ok := strings.CutPrefix(r.URL.Path, apiRepo+"contents/"); ok {
			if content, found := files[r.URL.Query().Get("ref")][rel]; found {
				_ = json.NewEncoder(w).Encode(map[string]string{
					"type":     "file",
					"path":     rel,
					"sha":      fetch.ComputeSHA256(content),
					"content":  base64.StdEncoding.EncodeToString(content),
					"encoding": "base64",
				})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	t.Cleanup(s.Close)
	useConfiguredGitHubHost(t, s.URL)
	useProductionFetchPolicy(t)
	return s
}

func (s *jiraProdGitHub) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...)
}

// jiraProdAgentsFiles returns the built-in harness and agent files of one
// agents-repo commit, as the agents release ships them: triage and code
// carry Jira command triggers, the rest carry none. model tells commits
// apart.
func jiraProdAgentsFiles(model string) map[string][]byte {
	triggers := defaultJiraBuiltinTriggers()
	files := map[string][]byte{}
	for name := range jiraBuiltinTestRoles {
		harnessYAML := strings.Replace(jiraBuiltinTestHarness(name, triggers[name]), "model: opus", "model: "+model, 1)
		files["harness/"+name+".yaml"] = []byte(harnessYAML)
		files["agents/"+name+".md"] = []byte("# " + name + "\n")
	}
	return files
}

// jiraProdAllowlist is the allowed_remote_resources entry for the fixture
// host's agents-repo raw-content URLs, which the default list does not name.
func jiraProdAllowlist(s *jiraProdGitHub) string {
	return "allowed_remote_resources:\n  - " + s.URL + "/fullsend-ai/agents/raw/\n"
}

// jiraProdRecords matches a Jira comment with command and returns the
// dispatch records.
func jiraProdRecords(t *testing.T, m *celMatcher, command string) []jirapoll.DispatchRecord {
	t.Helper()
	records, err := m.Match(context.Background(), jiraCommandEvent(t, command))
	require.NoError(t, err)
	return records
}

// useProductionFetchPolicy restores the production default fetch policy
// for the test. Other tests in the package overwrite fetch.DefaultPolicy
// and leave it zeroed, and newCELMatcher reads the global, so a zero size
// bound would otherwise fail every fixture read.
func useProductionFetchPolicy(t *testing.T) {
	t.Helper()
	orig := fetch.DefaultPolicy
	fetch.DefaultPolicy = fetch.FetchPolicy{
		AllowedDomains: []string{"github.com", "raw.githubusercontent.com"},
		MaxSizeBytes:   10 * 1024 * 1024,
		Timeout:        30 * time.Second,
	}
	t.Cleanup(func() { fetch.DefaultPolicy = orig })
}

func jiraProdSetVersion(t *testing.T, ver, commit string) {
	t.Helper()
	origVersion, origSHA := version, commitSHA
	version, commitSHA = ver, commit
	t.Cleanup(func() { version, commitSHA = origVersion, origSHA })
}

func TestNewCELMatcher_ProductionResolver(t *testing.T) {
	const (
		mainRef = "heads/main"
		tagRef  = "tags/v0.85.0"
	)
	refs := map[string]string{mainRef: jiraProdMainSHA, tagRef: jiraProdTagSHA}
	files := map[string]map[string][]byte{
		jiraProdMainSHA: jiraProdAgentsFiles("haiku"),
		jiraProdTagSHA:  jiraProdAgentsFiles("opus"),
	}

	// A dev build reads main. No agents: configuration and shorthand
	// runtime overrides (no source) both dispatch /fs-triage and /fs-code
	// from the built-in harnesses.
	for name, agents := range map[string]string{
		"no agents configuration": "",
		"shorthand overrides without source": `agents:
  - name: triage
    runtime: claude
  - name: code
    effort: low
`,
	} {
		t.Run(name, func(t *testing.T) {
			jiraProdSetVersion(t, "dev", "")
			srv := newJiraProdGitHub(t, refs, files)
			dir := t.TempDir()
			writeMatcherConfig(t, dir, "version: \"1\"\n"+jiraProdAllowlist(srv)+agents)

			m, err := newCELMatcher(context.Background(), dir)
			require.NoError(t, err)
			assert.Equal(t, []string{"triage", "code"}, jiraCandidateNames(m))
			for _, c := range m.Candidates {
				assert.Equal(t, "haiku", c.Harness.Model, "dev build reads the main commit")
			}

			triage := jiraProdRecords(t, m, "/fs-triage")
			require.Len(t, triage, 1)
			assert.Equal(t, "triage", triage[0].Agent)
			assert.Equal(t, "triage", triage[0].Role)
			code := jiraProdRecords(t, m, "/fs-code")
			require.Len(t, code, 1)
			assert.Equal(t, "code", code[0].Agent)
			assert.Equal(t, "coder", code[0].Role)
			assert.Empty(t, jiraProdRecords(t, m, "/fs-unknown"))

			paths := srv.requests()
			require.NotEmpty(t, paths)
			assert.Equal(t, "/api/v3/repos/fullsend-ai/agents/git/ref/"+mainRef, paths[0])
			assert.Equal(t, 1, countPrefix(paths, "/api/v3/repos/fullsend-ai/agents/git/ref/"), "the agents ref is resolved once per matcher")
			for _, p := range paths {
				assert.Contains(t, p, "/api/v3/repos/fullsend-ai/agents/", "only the agents repository is read")
				if strings.Contains(p, "/contents/") {
					assert.Contains(t, p, "ref="+jiraProdMainSHA, "every file is read from the one pinned commit")
				}
			}
		})
	}

	t.Run("release build pins to its release tag", func(t *testing.T) {
		jiraProdSetVersion(t, "0.85.0", "abc123def456")
		srv := newJiraProdGitHub(t, refs, files)
		dir := t.TempDir()
		writeMatcherConfig(t, dir, "version: \"1\"\n"+jiraProdAllowlist(srv))

		m, err := newCELMatcher(context.Background(), dir)
		require.NoError(t, err)
		assert.Equal(t, []string{"triage", "code"}, jiraCandidateNames(m))
		for _, c := range m.Candidates {
			assert.Equal(t, "opus", c.Harness.Model, "a release build reads the commit its tag points at")
		}
		assert.Len(t, jiraProdRecords(t, m, "/fs-triage"), 1)
		assert.Len(t, jiraProdRecords(t, m, "/fs-code"), 1)

		paths := srv.requests()
		assert.Equal(t, "/api/v3/repos/fullsend-ai/agents/git/ref/"+tagRef, paths[0])
		assert.NotContains(t, strings.Join(paths, "\n"), mainRef, "the main ref is never read by a release build")
		assert.NotContains(t, strings.Join(paths, "\n"), jiraProdMainSHA)
	})

	t.Run("custom source replaces the built-in harness", func(t *testing.T) {
		jiraProdSetVersion(t, "dev", "")
		srv := newJiraProdGitHub(t, refs, files)
		dir := t.TempDir()
		writeMatcherConfig(t, dir, "version: \"1\"\n"+jiraProdAllowlist(srv)+"agents:\n  - name: code\n    source: harness/code.yaml\n")
		customHarness := strings.Replace(jiraBuiltinTestHarness("code", jiraCommandTrigger("/fs-implement")), "model: opus", "model: sonnet", 1)
		writeHarnessFile(t, dir, "harness/code.yaml", customHarness)

		m, err := newCELMatcher(context.Background(), dir)
		require.NoError(t, err)
		assert.Equal(t, []string{"code", "triage"}, jiraCandidateNames(m))
		assert.Equal(t, "sonnet", m.Candidates[0].Harness.Model)
		assert.Empty(t, jiraProdRecords(t, m, "/fs-code"), "the built-in code trigger is replaced")
		code := jiraProdRecords(t, m, "/fs-implement")
		require.Len(t, code, 1)
		assert.Equal(t, "code", code[0].Agent)
		assert.Len(t, jiraProdRecords(t, m, "/fs-triage"), 1)
		assert.NotContains(t, strings.Join(srv.requests(), "\n"), "contents/harness/code.yaml", "the built-in code harness is not fetched")
	})

	t.Run("authorization still gates dispatch", func(t *testing.T) {
		jiraProdSetVersion(t, "dev", "")
		srv := newJiraProdGitHub(t, refs, files)
		dir := t.TempDir()
		writeMatcherConfig(t, dir, "version: \"1\"\n"+jiraProdAllowlist(srv))
		m, err := newCELMatcher(context.Background(), dir)
		require.NoError(t, err)

		event := jiraCommandEvent(t, "/fs-code")
		event.Actor.Role = "read"
		records, err := m.Match(context.Background(), event)
		require.NoError(t, err)
		assert.Empty(t, records, "an actor below the role threshold dispatches nothing")

		event = jiraCommandEvent(t, "/fs-code")
		event.Actor.Kind = "bot"
		records, err = m.Match(context.Background(), event)
		require.NoError(t, err)
		assert.Empty(t, records, "the built-in triggers match human actors only")
	})

	t.Run("allowed_remote_resources still restricts the fetch", func(t *testing.T) {
		jiraProdSetVersion(t, "dev", "")
		srv := newJiraProdGitHub(t, refs, files)
		dir := t.TempDir()
		// The fixture host is not allowlisted, so no harness is fetched and
		// the poll fails rather than matching against nothing.
		writeMatcherConfig(t, dir, "version: \"1\"\nallowed_remote_resources:\n  - https://raw.githubusercontent.com/fullsend-ai/agents/\n")

		_, err := newCELMatcher(context.Background(), dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no agents could be loaded")
		for _, p := range srv.requests() {
			assert.NotContains(t, p, "/contents/", "no file is read from a host the allowlist excludes")
		}
	})

	t.Run("an unavailable agents ref fails the poll", func(t *testing.T) {
		jiraProdSetVersion(t, "dev", "")
		srv := newJiraProdGitHub(t, nil, nil)
		dir := t.TempDir()
		writeMatcherConfig(t, dir, "version: \"1\"\n"+jiraProdAllowlist(srv))

		_, err := newCELMatcher(context.Background(), dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no agents could be loaded")
		assert.Equal(t, 1, countPrefix(srv.requests(), "/api/v3/repos/fullsend-ai/agents/git/ref/"), "the failed lookup is not retried per built-in")
	})

	t.Run("kill switch reads nothing", func(t *testing.T) {
		jiraProdSetVersion(t, "dev", "")
		srv := newJiraProdGitHub(t, refs, files)
		dir := t.TempDir()
		writeMatcherConfig(t, dir, "version: \"1\"\nkill_switch: true\n"+jiraProdAllowlist(srv))

		m, err := newCELMatcher(context.Background(), dir)
		require.NoError(t, err)
		names, suppressed := m.DispatchCandidates()
		assert.Empty(t, names)
		assert.True(t, suppressed)
		assert.Empty(t, jiraProdRecords(t, m, "/fs-triage"))
		assert.Empty(t, srv.requests(), "the kill switch makes no GitHub request")
	})
}

func countPrefix(paths []string, prefix string) int {
	n := 0
	for _, p := range paths {
		if strings.HasPrefix(p, prefix) {
			n++
		}
	}
	return n
}

func writeHarnessFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
