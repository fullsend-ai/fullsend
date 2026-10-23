package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// noGetRefClient fails the test if the agents ref is resolved over the
// network, proving offline resolution never calls the forge.
type noGetRefClient struct {
	*forge.FakeClient
	t *testing.T
}

func (c noGetRefClient) GetRef(context.Context, string, string, string) (string, error) {
	c.t.Fatalf("GetRef must not be called in offline mode")
	return "", nil
}

// cacheAgentsRepoFile stores content under a SHA-pinned agents-repo URL with
// the given fetch time, as a previous online run would have.
func cacheAgentsRepoFile(t *testing.T, root, sha, relPath string, content []byte, fetched time.Time) string {
	t.Helper()
	rawURL := defaultAgentsRepoURLPrefix + sha + "/" + relPath
	require.NoError(t, fetch.CachePut(root, rawURL, content))
	hash := fetch.ComputeSHA256(content)
	dir, err := fetch.CachePath(root, hash)
	require.NoError(t, err)
	meta, err := json.Marshal(fetch.CacheEntry{URL: rawURL, FetchTime: fetched, SHA256: hash})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "metadata.json"), meta, 0o600))
	return rawURL
}

func setAgentsRepoPrefix(t *testing.T, prefix string) {
	t.Helper()
	orig := defaultAgentsRepoURLPrefix
	defaultAgentsRepoURLPrefix = prefix
	t.Cleanup(func() { defaultAgentsRepoURLPrefix = orig })
}

func TestTryAgentsRepoFallback_OfflineCacheHit_NewestRevisionWins(t *testing.T) {
	setAgentsRepoPrefix(t, "https://agents.example.com/")
	root := t.TempDir()
	oldSHA := strings.Repeat("a", 40)
	newSHA := strings.Repeat("b", 40)
	now := time.Now().UTC()
	cacheAgentsRepoFile(t, root, oldSHA, "harness/triage.yaml", []byte("role: old\n"), now.Add(-2*time.Hour))
	newURL := cacheAgentsRepoFile(t, root, newSHA, "harness/triage.yaml", []byte("role: new\n"), now.Add(-time.Hour))
	// Same revision, different file: must not be picked for triage.
	cacheAgentsRepoFile(t, root, strings.Repeat("c", 40), "harness/code.yaml", []byte("role: code\n"), now)

	auditPath := filepath.Join(root, "audit.jsonl")
	var out bytes.Buffer
	opts := harness.ComposeOpts{
		WorkspaceRoot: root,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
		OrgAllowlist:  []string{"https://agents.example.com/"},
		AuditLogPath:  auditPath,
		TraceID:       "trace-1",
	}
	client := noGetRefClient{FakeClient: forge.NewFakeClient(), t: t}

	path, deps, ok := tryAgentsRepoFallback(context.Background(), "Triage", client, opts, ui.New(&out))
	require.True(t, ok, "expected offline cache hit; output: %s", out.String())
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "role: new\n", string(got))

	require.Len(t, deps, 1)
	assert.Equal(t, newURL, deps[0].URL)
	assert.Equal(t, path, deps[0].LocalPath)
	assert.Equal(t, fetch.ComputeSHA256([]byte("role: new\n")), deps[0].SHA256)
	assert.True(t, deps[0].CacheHit)
	assert.Equal(t, "file", deps[0].Type)
	assert.Contains(t, out.String(), "resolved from cache")
	assert.Contains(t, out.String(), newSHA[:12])

	audit, err := os.ReadFile(auditPath)
	require.NoError(t, err)
	var entry fetch.FetchAuditEntry
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(audit), &entry))
	assert.Equal(t, newURL, entry.URL)
	assert.True(t, entry.CacheHit)
	assert.Equal(t, "trace-1", entry.TraceID)
	assert.Equal(t, "https://agents.example.com/", entry.AllowedBy)
}

func TestTryAgentsRepoFallback_OfflineNoForgeClient(t *testing.T) {
	setAgentsRepoPrefix(t, "https://agents.example.com/")
	root := t.TempDir()
	cacheAgentsRepoFile(t, root, strings.Repeat("a", 40), "harness/review.yaml", []byte("role: review\n"), time.Now())

	opts := harness.ComposeOpts{
		WorkspaceRoot: root,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
		OrgAllowlist:  []string{"https://agents.example.com/"},
	}
	path, _, ok := tryAgentsRepoFallback(context.Background(), "review", nil, opts, ui.New(&bytes.Buffer{}))
	require.True(t, ok)
	assert.NotEmpty(t, path)
}

func TestTryAgentsRepoFallback_OfflineEmptyCache(t *testing.T) {
	var out bytes.Buffer
	opts := harness.ComposeOpts{WorkspaceRoot: t.TempDir(), FetchPolicy: fetch.FetchPolicy{Offline: true}}
	client := noGetRefClient{FakeClient: forge.NewFakeClient(), t: t}
	_, _, ok := tryAgentsRepoFallback(context.Background(), "triage", client, opts, ui.New(&out))
	assert.False(t, ok)
	assert.Contains(t, out.String(), "No cached agent triage")
}

func TestTryAgentsRepoFallback_OfflineSkipsUnpinnedOrForeignURLs(t *testing.T) {
	setAgentsRepoPrefix(t, "https://agents.example.com/")
	root := t.TempDir()
	// A branch name instead of a commit SHA, and a different host.
	require.NoError(t, fetch.CachePut(root, "https://agents.example.com/main/harness/triage.yaml", []byte("role: branch\n")))
	require.NoError(t, fetch.CachePut(root, "https://evil.example.com/"+strings.Repeat("a", 40)+"/harness/triage.yaml", []byte("role: evil\n")))

	opts := harness.ComposeOpts{
		WorkspaceRoot: root,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
		OrgAllowlist:  []string{"https://agents.example.com/", "https://evil.example.com/"},
	}
	_, _, ok := tryAgentsRepoFallback(context.Background(), "triage", nil, opts, ui.New(&bytes.Buffer{}))
	assert.False(t, ok)
}

func TestTryAgentsRepoFallback_OfflineNotAllowlisted(t *testing.T) {
	setAgentsRepoPrefix(t, "https://agents.example.com/")
	root := t.TempDir()
	cacheAgentsRepoFile(t, root, strings.Repeat("a", 40), "harness/triage.yaml", []byte("role: triage\n"), time.Now())

	var out bytes.Buffer
	opts := harness.ComposeOpts{
		WorkspaceRoot: root,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
		OrgAllowlist:  []string{"https://example.com/"},
	}
	_, _, ok := tryAgentsRepoFallback(context.Background(), "triage", nil, opts, ui.New(&out))
	assert.False(t, ok)
	assert.Contains(t, out.String(), "not in allowed_remote_resources")
}

func TestTryAgentsRepoMeasurementManifest_OfflineCacheHit(t *testing.T) {
	setAgentsRepoPrefix(t, "https://agents.example.com/")
	root := t.TempDir()
	manifest := []byte("agent: triage\n")
	cacheAgentsRepoFile(t, root, strings.Repeat("d", 40), "eval/measurements/triage.yaml", manifest, time.Now())

	opts := harness.ComposeOpts{
		WorkspaceRoot: root,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
		OrgAllowlist:  []string{"https://agents.example.com/"},
	}
	path, ok := tryAgentsRepoMeasurementManifest(context.Background(), "triage", nil, opts, ui.New(&bytes.Buffer{}))
	require.True(t, ok)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, manifest, got)
}

// TestResolveAgentSource_OverrideOnlyEntry_OfflineAfterOnlineRun simulates a
// full online run (fallback harness plus companion files fetched and cached,
// with url-index.json entries for the companions), then resolves and loads an
// override-only agent under --offline with the server gone.
func TestResolveAgentSource_OverrideOnlyEntry_OfflineAfterOnlineRun(t *testing.T) {
	fakeSHA := "abcdef1234567890abcdef1234567890abcdef12"
	harnessContent := []byte("role: triage\nagent: agents/triage.md\npolicy: policies/triage.yaml\n")
	agentContent := []byte("# Triage agent\n")
	policyContent := []byte("version: 1\n")

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + fakeSHA + "/harness/triage.yaml":
			_, _ = w.Write(harnessContent)
		case "/" + fakeSHA + "/agents/triage.md":
			_, _ = w.Write(agentContent)
		case "/" + fakeSHA + "/policies/triage.yaml":
			_, _ = w.Write(policyContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	hostname, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	require.NoError(t, err)
	tlsCfg := srv.TLS.Clone()
	tlsCfg.InsecureSkipVerify = true
	setAgentsRepoPrefix(t, srv.URL+"/")

	root := t.TempDir()
	cfg := config.NewPerRepoConfig(nil, "")
	cfg.SetAgents([]config.AgentEntry{{Name: "triage", Runtime: "dummy"}})
	allowlist := []string{srv.URL + "/"}
	cfg.SetAllowedRemoteResources(allowlist)

	// Online run: populates the content-addressed cache and the URL index.
	onlineClient := forge.NewFakeClient()
	onlineClient.Refs["fullsend-ai/agents/heads/main"] = fakeSHA
	onlineOpts := harness.ComposeOpts{
		WorkspaceRoot: root,
		FetchPolicy:   fetch.NewTestPolicy(tlsCfg, []string{hostname}, []string{port}),
		OrgAllowlist:  allowlist,
	}
	path, deps, err := resolveAgentSource(context.Background(), root, "triage", onlineClient, cfg, onlineOpts, ui.New(&bytes.Buffer{}))
	require.NoError(t, err)
	require.Len(t, deps, 1)
	onlineOpts.SourceURL = deps[0].URL
	_, _, err = harness.LoadWithBase(context.Background(), path, onlineOpts)
	require.NoError(t, err)
	srv.Close()

	// Offline run: no network, no ref resolution.
	var out bytes.Buffer
	offlineOpts := harness.ComposeOpts{
		WorkspaceRoot: root,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
		OrgAllowlist:  allowlist,
	}
	client := noGetRefClient{FakeClient: forge.NewFakeClient(), t: t}
	path, deps, err = resolveAgentSource(context.Background(), root, "triage", client, cfg, offlineOpts, ui.New(&out))
	require.NoError(t, err, "output: %s", out.String())
	require.Len(t, deps, 1)
	assert.Equal(t, srv.URL+"/"+fakeSHA+"/harness/triage.yaml", deps[0].URL)
	assert.Contains(t, out.String(), "resolved from cache")

	offlineOpts.SourceURL = deps[0].URL
	h, _, err := harness.LoadWithBase(context.Background(), path, offlineOpts)
	require.NoError(t, err)
	gotAgent, err := os.ReadFile(h.Agent)
	require.NoError(t, err)
	assert.Equal(t, agentContent, gotAgent)
	gotPolicy, err := os.ReadFile(h.Policy)
	require.NoError(t, err)
	assert.Equal(t, policyContent, gotPolicy)
}
