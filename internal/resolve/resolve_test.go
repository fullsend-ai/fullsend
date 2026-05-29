package resolve

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
)

func newTestServer(t *testing.T, handler http.Handler) (*httptest.Server, fetch.FetchPolicy) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)

	hostPort := strings.TrimPrefix(srv.URL, "https://")
	hostname, port, _ := net.SplitHostPort(hostPort)

	tlsCfg := srv.TLS.Clone()
	tlsCfg.InsecureSkipVerify = true

	return srv, fetch.NewTestPolicy(tlsCfg, []string{hostname}, []string{port})
}

func TestResolveHarness_LocalPassThrough(t *testing.T) {
	h := &harness.Harness{
		Agent:  "/abs/path/agents/test.md",
		Policy: "/abs/path/policies/readonly.yaml",
		Skills: []string{"/abs/path/skills/local-skill"},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
	})
	require.NoError(t, err)
	assert.Empty(t, deps)
	assert.Equal(t, "/abs/path/agents/test.md", h.Agent)
	assert.Equal(t, "/abs/path/policies/readonly.yaml", h.Policy)
	assert.Equal(t, "/abs/path/skills/local-skill", h.Skills[0])
}

func TestResolveHarness_URLFetchAndCache(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(agentContent)
	}))

	root := t.TempDir()
	agentURL := fmt.Sprintf("%s/agents/code.md#sha256=%s", srv.URL, agentHash)
	h := &harness.Harness{
		Agent:                  agentURL,
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: root,
		FetchPolicy:   policy,
	})
	require.NoError(t, err)
	require.Len(t, deps, 1)

	assert.Equal(t, fmt.Sprintf("%s/agents/code.md", srv.URL), deps[0].URL)
	assert.Equal(t, agentHash, deps[0].SHA256)
	assert.False(t, deps[0].CacheHit)

	// Verify the harness field was replaced with a local path.
	assert.True(t, strings.HasSuffix(h.Agent, "/content"))
	assert.False(t, harness.IsURL(h.Agent))

	// Verify the cached file exists and has the right content.
	got, err := os.ReadFile(h.Agent)
	require.NoError(t, err)
	assert.Equal(t, agentContent, got)
}

func TestResolveHarness_CacheHit(t *testing.T) {
	agentContent := []byte("cached agent definition")
	agentHash := fetch.ComputeSHA256(agentContent)

	var fetchCount atomic.Int32
	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCount.Add(1)
		w.Write(agentContent)
	}))

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, srv.URL+"/agents/code.md", agentContent))

	agentURL := fmt.Sprintf("%s/agents/code.md#sha256=%s", srv.URL, agentHash)
	h := &harness.Harness{
		Agent:                  agentURL,
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: root,
		FetchPolicy:   policy,
	})
	require.NoError(t, err)
	require.Len(t, deps, 1)
	assert.True(t, deps[0].CacheHit)
	assert.Equal(t, int32(0), fetchCount.Load())
}

func TestResolveHarness_HashMismatch(t *testing.T) {
	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("wrong content"))
	}))

	wrongHash := fetch.ComputeSHA256([]byte("expected content"))
	agentURL := fmt.Sprintf("%s/agents/code.md#sha256=%s", srv.URL, wrongHash)
	h := &harness.Harness{
		Agent:                  agentURL,
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   policy,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "integrity check failed")
}

func TestResolveHarness_URLNotInAllowlist(t *testing.T) {
	agentContent := []byte("agent")
	agentHash := fetch.ComputeSHA256(agentContent)

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(agentContent)
	}))

	agentURL := fmt.Sprintf("%s/agents/code.md#sha256=%s", srv.URL, agentHash)
	h := &harness.Harness{
		Agent:                  agentURL,
		AllowedRemoteResources: []string{"https://other-domain.com/"},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   policy,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in allowed_remote_resources")
}

func TestResolveHarness_MissingHash(t *testing.T) {
	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("agent"))
	}))

	h := &harness.Harness{
		Agent:                  srv.URL + "/agents/code.md",
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   policy,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "integrity hash")
}

func TestResolveHarness_OfflineMiss(t *testing.T) {
	agentHash := fetch.ComputeSHA256([]byte("agent"))

	h := &harness.Harness{
		Agent:                  fmt.Sprintf("https://example.com/agents/code.md#sha256=%s", agentHash),
		AllowedRemoteResources: []string{"https://example.com/"},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "offline")
}

func TestResolveHarness_OfflineHit(t *testing.T) {
	agentContent := []byte("cached agent for offline")
	agentHash := fetch.ComputeSHA256(agentContent)
	root := t.TempDir()

	require.NoError(t, fetch.CachePut(root, "https://example.com/agents/code.md", agentContent))

	h := &harness.Harness{
		Agent:                  fmt.Sprintf("https://example.com/agents/code.md#sha256=%s", agentHash),
		AllowedRemoteResources: []string{"https://example.com/"},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: root,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
	})
	require.NoError(t, err)
	require.Len(t, deps, 1)
	assert.True(t, deps[0].CacheHit)

	got, err := os.ReadFile(h.Agent)
	require.NoError(t, err)
	assert.Equal(t, agentContent, got)
}

func TestResolveHarness_MixedHarness(t *testing.T) {
	agentContent := []byte("remote agent")
	agentHash := fetch.ComputeSHA256(agentContent)

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(agentContent)
	}))

	root := t.TempDir()
	agentURL := fmt.Sprintf("%s/agents/code.md#sha256=%s", srv.URL, agentHash)
	h := &harness.Harness{
		Agent:                  agentURL,
		Policy:                 "/local/policies/readonly.yaml",
		Skills:                 []string{"/local/skills/debug"},
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: root,
		FetchPolicy:   policy,
	})
	require.NoError(t, err)
	require.Len(t, deps, 1)

	assert.False(t, harness.IsURL(h.Agent))
	assert.Equal(t, "/local/policies/readonly.yaml", h.Policy)
	assert.Equal(t, "/local/skills/debug", h.Skills[0])
}

func TestResolveHarness_AuditEntries(t *testing.T) {
	agentContent := []byte("audited agent")
	agentHash := fetch.ComputeSHA256(agentContent)

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(agentContent)
	}))

	root := t.TempDir()
	auditPath := filepath.Join(root, "audit", "fetch-audit.jsonl")

	agentURL := fmt.Sprintf("%s/agents/code.md#sha256=%s", srv.URL, agentHash)
	h := &harness.Harness{
		Agent:                  agentURL,
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: root,
		FetchPolicy:   policy,
		TraceID:       "test-trace-id",
		AuditLogPath:  auditPath,
	})
	require.NoError(t, err)

	f, err := os.Open(auditPath)
	require.NoError(t, err)
	defer f.Close()

	var entry fetch.FetchAuditEntry
	scanner := bufio.NewScanner(f)
	require.True(t, scanner.Scan())
	require.NoError(t, json.Unmarshal(scanner.Bytes(), &entry))

	assert.Equal(t, "test-trace-id", entry.TraceID)
	assert.Equal(t, fmt.Sprintf("%s/agents/code.md", srv.URL), entry.URL)
	assert.Equal(t, agentHash, entry.SHA256)
	assert.Equal(t, "static", entry.FetchType)
	assert.False(t, entry.CacheHit)
}

func TestResolveHarness_MultipleSkills(t *testing.T) {
	skill1Content := []byte("skill one content")
	skill1Hash := fetch.ComputeSHA256(skill1Content)
	skill2Content := []byte("skill two content")
	skill2Hash := fetch.ComputeSHA256(skill2Content)

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills/one.md":
			w.Write(skill1Content)
		case "/skills/two.md":
			w.Write(skill2Content)
		}
	}))

	root := t.TempDir()
	h := &harness.Harness{
		Agent: "/local/agents/test.md",
		Skills: []string{
			"/local/skills/debug",
			fmt.Sprintf("%s/skills/one.md#sha256=%s", srv.URL, skill1Hash),
			fmt.Sprintf("%s/skills/two.md#sha256=%s", srv.URL, skill2Hash),
		},
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: root,
		FetchPolicy:   policy,
	})
	require.NoError(t, err)
	require.Len(t, deps, 2)

	assert.Equal(t, "/local/skills/debug", h.Skills[0])
	assert.False(t, harness.IsURL(h.Skills[1]))
	assert.False(t, harness.IsURL(h.Skills[2]))

	got1, err := os.ReadFile(h.Skills[1])
	require.NoError(t, err)
	assert.Equal(t, skill1Content, got1)

	got2, err := os.ReadFile(h.Skills[2])
	require.NoError(t, err)
	assert.Equal(t, skill2Content, got2)
}

func TestResolveHarness_PolicyURL(t *testing.T) {
	policyContent := []byte("sandbox policy yaml")
	policyHash := fetch.ComputeSHA256(policyContent)

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(policyContent)
	}))

	root := t.TempDir()
	policyURL := fmt.Sprintf("%s/policies/readonly.yaml#sha256=%s", srv.URL, policyHash)
	h := &harness.Harness{
		Agent:                  "/local/agents/test.md",
		Policy:                 policyURL,
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: root,
		FetchPolicy:   policy,
	})
	require.NoError(t, err)
	require.Len(t, deps, 1)
	assert.Equal(t, policyHash, deps[0].SHA256)

	got, err := os.ReadFile(h.Policy)
	require.NoError(t, err)
	assert.Equal(t, policyContent, got)
}

func TestResolveHarness_NonSHA256Fragment(t *testing.T) {
	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("agent"))
	}))

	h := &harness.Harness{
		Agent:                  srv.URL + "/agents/code.md#section-heading",
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   policy,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "integrity hash")
}

func TestResolveHarness_EmptyFields(t *testing.T) {
	h := &harness.Harness{
		Agent: "/local/agents/test.md",
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
	})
	require.NoError(t, err)
	assert.Empty(t, deps)
}
