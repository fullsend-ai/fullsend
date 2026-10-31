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

// forgeTriageHarness has a forge.github companion that an online run with
// no forge does not fetch.
const forgeTriageHarness = "role: triage\nagent: agents/triage.md\npolicy: policies/triage.yaml\nforge:\n  github:\n    policy: policies/github.yaml\n"

var githubPolicy = []byte("version: 1\n# github\n")

// cacheIndexedFile caches content under rawURL and records it in the URL
// index, as an online run does for a harness and its companions.
func cacheIndexedFile(t *testing.T, root, rawURL string, content []byte) {
	t.Helper()
	require.NoError(t, fetch.CachePut(root, rawURL, content))
	require.NoError(t, harness.RecordURLIndex(root, rawURL, fetch.ComputeSHA256(content)))
}

// recordAgentsRef records sha as the revision this build's agents ref
// resolved to, as the online path does.
func recordAgentsRef(t *testing.T, root, sha string) {
	t.Helper()
	_, gitRef := resolveAgentsRef()
	require.NoError(t, harness.RecordURLIndex(root, agentsRefIndexKey(gitRef), sha))
}

// seedTriageRevision caches the triage harness at sha with the companions
// every forge uses, plus the forge.github policy when withGitHub is set.
func seedTriageRevision(t *testing.T, root, prefix, sha, harnessYAML string, withGitHub bool) {
	t.Helper()
	base := prefix + sha + "/"
	cacheIndexedFile(t, root, base+"harness/triage.yaml", []byte(harnessYAML))
	cacheIndexedFile(t, root, base+"agents/triage.md", []byte("# Triage agent\n"))
	cacheIndexedFile(t, root, base+"policies/triage.yaml", []byte("version: 1\n"))
	if withGitHub {
		cacheIndexedFile(t, root, base+"policies/github.yaml", githubPolicy)
	}
}

func offlineOpts(root, prefix, forgePlatform string) harness.ComposeOpts {
	return harness.ComposeOpts{
		WorkspaceRoot: root,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
		OrgAllowlist:  []string{prefix},
		ForgePlatform: forgePlatform,
	}
}

// TestTryAgentsRepoFallback_OfflineSkipsRevisionMissingForgeCompanions is the
// issue's scenario: an online run with --forge github caches SHA1 in full,
// then agents moves to SHA2 with an unchanged harness and an online run with
// no forge rewrites the harness metadata to SHA2 without the forge.github
// companions. An offline --forge github run must pick SHA1.
func TestTryAgentsRepoFallback_OfflineSkipsRevisionMissingForgeCompanions(t *testing.T) {
	prefix := "https://agents.example.com/"
	setAgentsRepoPrefix(t, prefix)
	root := t.TempDir()
	sha1 := strings.Repeat("1", 40)
	sha2 := strings.Repeat("2", 40)
	seedTriageRevision(t, root, prefix, sha1, forgeTriageHarness, true)
	seedTriageRevision(t, root, prefix, sha2, forgeTriageHarness, false)
	recordAgentsRef(t, root, sha2)

	t.Run("forge github falls back to the complete revision", func(t *testing.T) {
		var out bytes.Buffer
		opts := offlineOpts(root, prefix, "github")
		path, deps, ok := tryAgentsRepoFallback(context.Background(), "triage", nil, opts, ui.New(&out))
		require.True(t, ok, "output: %s", out.String())
		require.Len(t, deps, 1)
		assert.Equal(t, prefix+sha1+"/harness/triage.yaml", deps[0].URL)
		assert.Contains(t, out.String(), "missing companion files")
		assert.Contains(t, out.String(), sha2[:12])
		assert.Contains(t, out.String(), "falling back to fullsend-ai/agents@"+sha1[:12])

		h, _, err := harness.LoadWithBase(context.Background(), path, agentComposeOpts(opts, deps))
		require.NoError(t, err)
		gotPolicy, err := os.ReadFile(h.Policy)
		require.NoError(t, err)
		assert.Equal(t, githubPolicy, gotPolicy)
	})

	t.Run("no forge keeps the build ref revision", func(t *testing.T) {
		var out bytes.Buffer
		_, deps, ok := tryAgentsRepoFallback(context.Background(), "triage", nil, offlineOpts(root, prefix, ""), ui.New(&out))
		require.True(t, ok, "output: %s", out.String())
		require.Len(t, deps, 1)
		assert.Equal(t, prefix+sha2+"/harness/triage.yaml", deps[0].URL)
		assert.NotContains(t, out.String(), "falling back")
	})
}

// TestTryAgentsRepoFallback_OfflineIdenticalContentUnderTwoSHAs covers a
// harness cached unchanged under two revisions: the cache metadata records
// only the last URL, so the other revision is found through the URL index.
func TestTryAgentsRepoFallback_OfflineIdenticalContentUnderTwoSHAs(t *testing.T) {
	prefix := "https://agents.example.com/"
	setAgentsRepoPrefix(t, prefix)
	shaA := strings.Repeat("a", 40)
	shaB := strings.Repeat("b", 40)
	harnessYAML := "role: triage\nagent: agents/triage.md\npolicy: policies/triage.yaml\n"

	tests := []struct {
		name    string
		refSHA  string
		wantSHA string
		wantOut string
	}{
		{name: "build ref is the earlier URL", refSHA: shaA, wantSHA: shaA},
		{name: "build ref is the last URL", refSHA: shaB, wantSHA: shaB},
		{name: "build ref not cached", refSHA: strings.Repeat("c", 40), wantOut: "No cached agent triage from fullsend-ai/agents@cccccccccccc"},
		{name: "no build ref recorded", wantOut: "from the fullsend-ai/agents revision this build pins"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			seedTriageRevision(t, root, prefix, shaA, harnessYAML, false)
			seedTriageRevision(t, root, prefix, shaB, harnessYAML, false)
			if tt.refSHA != "" {
				recordAgentsRef(t, root, tt.refSHA)
			}

			var out bytes.Buffer
			opts := offlineOpts(root, prefix, "github")
			path, deps, ok := tryAgentsRepoFallback(context.Background(), "triage", nil, opts, ui.New(&out))
			require.True(t, ok, "output: %s", out.String())
			require.Len(t, deps, 1)
			if tt.wantSHA != "" {
				assert.Equal(t, prefix+tt.wantSHA+"/harness/triage.yaml", deps[0].URL)
				assert.NotContains(t, out.String(), "this build pins")
				assert.NotContains(t, out.String(), "falling back")
			}
			if tt.wantOut != "" {
				assert.Contains(t, out.String(), tt.wantOut)
			}
			_, _, err := harness.LoadWithBase(context.Background(), path, agentComposeOpts(opts, deps))
			require.NoError(t, err)
		})
	}
}

// TestTryAgentsRepoFallback_OfflineKeepsRevisionWithNonMissLoadError checks
// that only a missing companion moves selection to another revision: a
// build-ref harness that fails to load for another reason is still chosen,
// so the caller's load reports that error instead of hiding it.
func TestTryAgentsRepoFallback_OfflineKeepsRevisionWithNonMissLoadError(t *testing.T) {
	prefix := "https://agents.example.com/"
	setAgentsRepoPrefix(t, prefix)
	root := t.TempDir()
	good := strings.Repeat("1", 40)
	broken := strings.Repeat("2", 40)
	seedTriageRevision(t, root, prefix, good, forgeTriageHarness, true)
	cacheIndexedFile(t, root, prefix+broken+"/harness/triage.yaml", []byte("role: [\n"))
	recordAgentsRef(t, root, broken)

	var out bytes.Buffer
	_, deps, ok := tryAgentsRepoFallback(context.Background(), "triage", nil, offlineOpts(root, prefix, "github"), ui.New(&out))
	require.True(t, ok, "output: %s", out.String())
	require.Len(t, deps, 1)
	assert.Equal(t, prefix+broken+"/harness/triage.yaml", deps[0].URL)
}

func TestAgentComposeOpts(t *testing.T) {
	setAgentsRepoPrefix(t, "https://agents.example.com/")
	agentsURL := "https://agents.example.com/" + strings.Repeat("a", 40) + "/harness/triage.yaml"

	opts := agentComposeOpts(harness.ComposeOpts{FetchPolicy: fetch.FetchPolicy{Offline: true}}, []harness.Dependency{{URL: agentsURL}})
	assert.Equal(t, agentsURL, opts.SourceURL)
	assert.Equal(t, agentsRepoOfflineMissHint, opts.OfflineMissHint)

	opts = agentComposeOpts(harness.ComposeOpts{}, []harness.Dependency{{URL: agentsURL}})
	assert.Equal(t, agentsURL, opts.SourceURL)
	assert.Empty(t, opts.OfflineMissHint, "online loads keep the default hint")

	opts = agentComposeOpts(harness.ComposeOpts{FetchPolicy: fetch.FetchPolicy{Offline: true}}, []harness.Dependency{{URL: "https://example.com/harness/triage.yaml"}})
	assert.Equal(t, "https://example.com/harness/triage.yaml", opts.SourceURL)
	assert.Empty(t, opts.OfflineMissHint, "config-registered URLs keep the fullsend lock hint")

	opts = agentComposeOpts(harness.ComposeOpts{SourceURL: "keep"}, nil)
	assert.Equal(t, "keep", opts.SourceURL)
}

// TestResolveAgentSource_OfflineForgeCompanionMissHint warms the cache with an
// online run that has no forge, then loads offline with --forge github. The
// forge.github companion was never fetched, and the error must point at an
// online run with the same --forge rather than fullsend lock.
func TestResolveAgentSource_OfflineForgeCompanionMissHint(t *testing.T) {
	fakeSHA := "abcdef1234567890abcdef1234567890abcdef12"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + fakeSHA + "/harness/triage.yaml":
			_, _ = w.Write([]byte(forgeTriageHarness))
		case "/" + fakeSHA + "/agents/triage.md":
			_, _ = w.Write([]byte("# Triage agent\n"))
		case "/" + fakeSHA + "/policies/triage.yaml":
			_, _ = w.Write([]byte("version: 1\n"))
		case "/" + fakeSHA + "/policies/github.yaml":
			_, _ = w.Write(githubPolicy)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	hostname, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	require.NoError(t, err)
	tlsCfg := srv.TLS.Clone()
	tlsCfg.InsecureSkipVerify = true
	prefix := srv.URL + "/"
	setAgentsRepoPrefix(t, prefix)

	root := t.TempDir()
	cfg := config.NewPerRepoConfig(nil, "")
	cfg.SetAgents([]config.AgentEntry{{Name: "triage", Runtime: "dummy"}})
	cfg.SetAllowedRemoteResources([]string{prefix})

	onlineClient := forge.NewFakeClient()
	_, gitRef := resolveAgentsRef()
	onlineClient.Refs["fullsend-ai/agents/"+gitRef] = fakeSHA
	onlineOpts := harness.ComposeOpts{
		WorkspaceRoot: root,
		FetchPolicy:   fetch.NewTestPolicy(tlsCfg, []string{hostname}, []string{port}),
		OrgAllowlist:  []string{prefix},
	}
	path, deps, err := resolveAgentSource(context.Background(), root, "triage", onlineClient, cfg, onlineOpts, ui.New(&bytes.Buffer{}))
	require.NoError(t, err)
	_, _, err = harness.LoadWithBase(context.Background(), path, agentComposeOpts(onlineOpts, deps))
	require.NoError(t, err)
	srv.Close()

	// The online path indexed the harness URL and the ref's SHA.
	index := harness.URLIndexEntries(root)
	harnessURL := prefix + fakeSHA + "/harness/triage.yaml"
	assert.Equal(t, fetch.ComputeSHA256([]byte(forgeTriageHarness)), index[harnessURL])
	assert.Equal(t, fakeSHA, index[agentsRefIndexKey(gitRef)])

	var out bytes.Buffer
	opts := offlineOpts(root, prefix, "github")
	client := noGetRefClient{FakeClient: forge.NewFakeClient(), t: t}
	path, deps, err = resolveAgentSource(context.Background(), root, "triage", client, cfg, opts, ui.New(&out))
	require.NoError(t, err, "output: %s", out.String())
	require.Len(t, deps, 1)
	assert.Equal(t, harnessURL, deps[0].URL)

	_, _, err = harness.LoadWithBase(context.Background(), path, agentComposeOpts(opts, deps))
	require.Error(t, err)
	require.ErrorIs(t, err, harness.ErrOfflineCacheMiss)
	assert.Contains(t, err.Error(), "forge.github.policy")
	assert.Contains(t, err.Error(), "online run using the same --forge")
	assert.NotContains(t, err.Error(), "run 'fullsend lock' first")
}

// TestLintCommand_OfflineThroughRootCommand runs `fullsend lint --offline`
// through the root command against a cache warmed as in the issue's
// scenario, and against a cache that never saw the forge.github companions.
func TestLintCommand_OfflineThroughRootCommand(t *testing.T) {
	stubLintGitHubToken(t, "test-token")
	prefix := "https://agents.example.com/"
	setAgentsRepoPrefix(t, prefix)
	sha1 := strings.Repeat("1", 40)
	sha2 := strings.Repeat("2", 40)
	cfgYAML := "version: \"1\"\nallowed_remote_resources:\n  - " + prefix + "\nagents:\n  - name: triage\n    runtime: claude\n"

	setup := func(t *testing.T, sha1HasGitHub bool) string {
		t.Helper()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfgYAML), 0o644))
		seedTriageRevision(t, dir, prefix, sha1, forgeTriageHarness, sha1HasGitHub)
		seedTriageRevision(t, dir, prefix, sha2, forgeTriageHarness, false)
		recordAgentsRef(t, dir, sha2)
		return dir
	}

	t.Run("complete older revision lints", func(t *testing.T) {
		dir := setup(t, true)
		out, err := executeCmd(newRootCmd(), []string{"lint", "--fullsend-dir", dir, "--offline", "--forge", "github"})
		require.NoError(t, err, "output: %s", out)
		assert.Contains(t, out, "resolved from cache (fullsend-ai/agents@"+sha1[:12]+", offline)")
		assert.Contains(t, out, "falling back")
	})

	t.Run("no complete revision names the online warm-up", func(t *testing.T) {
		dir := setup(t, false)
		out, err := executeCmd(newRootCmd(), []string{"lint", "--fullsend-dir", dir, "--offline", "--forge", "github"})
		require.Error(t, err, "output: %s", out)
		assert.Contains(t, err.Error(), "online run using the same --forge")
		assert.NotContains(t, err.Error(), "run 'fullsend lock' first")
	})
}
