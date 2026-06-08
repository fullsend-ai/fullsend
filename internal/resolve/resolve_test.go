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

// skillFrontmatter returns SKILL.md content with the given YAML frontmatter fields
// and optional body text after the closing delimiter.
func skillFrontmatter(fields, body string) []byte {
	return []byte("---\n" + fields + "---\n" + body)
}

// TestResolveHarness_TransitiveChain verifies A→B→C transitive resolution:
// all three dependencies are fetched and added to h.Skills.
func TestResolveHarness_TransitiveChain(t *testing.T) {
	cContent := []byte("Skill C content — leaf node")
	cHash := fetch.ComputeSHA256(cContent)

	var bContent, aContent []byte

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills/a.md":
			w.Write(aContent)
		case "/skills/b.md":
			w.Write(bContent)
		case "/skills/c.md":
			w.Write(cContent)
		}
	}))

	cURL := fmt.Sprintf("%s/skills/c.md#sha256=%s", srv.URL, cHash)
	bContent = skillFrontmatter(fmt.Sprintf("dependencies:\n  - %s\n", cURL), "Skill B content")
	bHash := fetch.ComputeSHA256(bContent)

	bURL := fmt.Sprintf("%s/skills/b.md#sha256=%s", srv.URL, bHash)
	aContent = skillFrontmatter(fmt.Sprintf("dependencies:\n  - %s\n", bURL), "Skill A content")
	aHash := fetch.ComputeSHA256(aContent)

	aURL := fmt.Sprintf("%s/skills/a.md#sha256=%s", srv.URL, aHash)
	h := &harness.Harness{
		Skills:                 []string{aURL},
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   policy,
		MaxDepth:      -1,
	})
	require.NoError(t, err)
	assert.Len(t, deps, 3)
	assert.Len(t, h.Skills, 3)

	urls := make(map[string]bool)
	for _, d := range deps {
		urls[d.URL] = true
	}
	assert.True(t, urls[srv.URL+"/skills/a.md"])
	assert.True(t, urls[srv.URL+"/skills/b.md"])
	assert.True(t, urls[srv.URL+"/skills/c.md"])
}

// TestResolveHarness_DiamondDedup verifies that a diamond graph (A→C, B→C) resolves C
// exactly once and produces no duplicate entries in deps or h.Skills.
func TestResolveHarness_DiamondDedup(t *testing.T) {
	cContent := []byte("Skill C content — shared dep")
	cHash := fetch.ComputeSHA256(cContent)

	var aContent, bContent []byte
	var fetchCount atomic.Int32

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills/a.md":
			w.Write(aContent)
		case "/skills/b.md":
			w.Write(bContent)
		case "/skills/c.md":
			fetchCount.Add(1)
			w.Write(cContent)
		}
	}))

	cURL := fmt.Sprintf("%s/skills/c.md#sha256=%s", srv.URL, cHash)
	aContent = skillFrontmatter(fmt.Sprintf("dependencies:\n  - %s\n", cURL), "Skill A")
	aHash := fetch.ComputeSHA256(aContent)
	bContent = skillFrontmatter(fmt.Sprintf("dependencies:\n  - %s\n", cURL), "Skill B")
	bHash := fetch.ComputeSHA256(bContent)

	h := &harness.Harness{
		Skills: []string{
			fmt.Sprintf("%s/skills/a.md#sha256=%s", srv.URL, aHash),
			fmt.Sprintf("%s/skills/b.md#sha256=%s", srv.URL, bHash),
		},
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   policy,
		MaxDepth:      -1,
	})
	require.NoError(t, err)
	assert.Len(t, deps, 3)  // C, A, B — each exactly once
	assert.Len(t, h.Skills, 3)
	assert.Equal(t, int32(1), fetchCount.Load()) // C fetched only once

	urls := make(map[string]bool)
	for _, d := range deps {
		assert.False(t, urls[d.URL], "duplicate dep URL %s", d.URL)
		urls[d.URL] = true
	}
}

// TestResolveHarness_CycleDetection verifies that A→B→A is rejected with a cycle error.
// The cycle is detected via the inProgress DFS stack before any hash check on the repeat visit.
func TestResolveHarness_CycleDetection(t *testing.T) {
	// Use a placeholder hash for A in B's dep — cycle is detected before integrity check.
	placeholderHash := strings.Repeat("a", 64)

	var aContent, bContent []byte

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills/a.md":
			w.Write(aContent)
		case "/skills/b.md":
			w.Write(bContent)
		}
	}))

	aURL := fmt.Sprintf("%s/skills/a.md", srv.URL)

	// B references A with a placeholder hash; cycle fires before hash validation.
	bContent = skillFrontmatter(fmt.Sprintf("dependencies:\n  - %s#sha256=%s\n", aURL, placeholderHash), "Skill B")
	bHash := fetch.ComputeSHA256(bContent)

	bURL := fmt.Sprintf("%s/skills/b.md#sha256=%s", srv.URL, bHash)
	aContent = skillFrontmatter(fmt.Sprintf("dependencies:\n  - %s\n", bURL), "Skill A")
	aHash := fetch.ComputeSHA256(aContent)

	h := &harness.Harness{
		Skills:                 []string{fmt.Sprintf("%s#sha256=%s", aURL, aHash)},
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   policy,
		MaxDepth:      -1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circular dependency")
}

// TestResolveHarness_MaxDepthExceeded verifies that a chain A→B→C fails when MaxDepth=1,
// allowing one level of transitive resolution (B) but blocking the second (C).
func TestResolveHarness_MaxDepthExceeded(t *testing.T) {
	cContent := []byte("Skill C — should not be reached")
	cHash := fetch.ComputeSHA256(cContent)

	var aContent, bContent []byte

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills/a.md":
			w.Write(aContent)
		case "/skills/b.md":
			w.Write(bContent)
		case "/skills/c.md":
			w.Write(cContent)
		}
	}))

	cURL := fmt.Sprintf("%s/skills/c.md#sha256=%s", srv.URL, cHash)
	bContent = skillFrontmatter(fmt.Sprintf("dependencies:\n  - %s\n", cURL), "Skill B")
	bHash := fetch.ComputeSHA256(bContent)

	bURL := fmt.Sprintf("%s/skills/b.md#sha256=%s", srv.URL, bHash)
	aContent = skillFrontmatter(fmt.Sprintf("dependencies:\n  - %s\n", bURL), "Skill A")
	aHash := fetch.ComputeSHA256(aContent)

	h := &harness.Harness{
		Skills:                 []string{fmt.Sprintf("%s/skills/a.md#sha256=%s", srv.URL, aHash)},
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   policy,
		MaxDepth:      1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeded maximum dependency depth")
}

// TestResolveHarness_MaxResourcesExceeded verifies that resolution stops when the
// resource count reaches MaxResources, returning an error on the next fetch attempt.
func TestResolveHarness_MaxResourcesExceeded(t *testing.T) {
	bContent := []byte("Skill B content")
	bHash := fetch.ComputeSHA256(bContent)

	var aContent []byte

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills/a.md":
			w.Write(aContent)
		case "/skills/b.md":
			w.Write(bContent)
		}
	}))

	bURL := fmt.Sprintf("%s/skills/b.md#sha256=%s", srv.URL, bHash)
	aContent = skillFrontmatter(fmt.Sprintf("dependencies:\n  - %s\n", bURL), "Skill A")
	aHash := fetch.ComputeSHA256(aContent)

	h := &harness.Harness{
		Skills:                 []string{fmt.Sprintf("%s/skills/a.md#sha256=%s", srv.URL, aHash)},
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	// MaxResources=1: A consumes the single slot; B is rejected.
	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   policy,
		MaxDepth:      -1,
		MaxResources:  1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeded maximum resource count")
}

// TestResolveHarness_TransitiveNotInAllowlist verifies that a transitive dep whose
// URL does not match allowed_remote_resources is rejected.
func TestResolveHarness_TransitiveNotInAllowlist(t *testing.T) {
	bContent := []byte("Skill B content")
	bHash := fetch.ComputeSHA256(bContent)

	var aContent []byte

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills/a.md":
			w.Write(aContent)
		case "/skills/b.md":
			w.Write(bContent)
		}
	}))

	bURL := fmt.Sprintf("%s/skills/b.md#sha256=%s", srv.URL, bHash)
	aContent = skillFrontmatter(fmt.Sprintf("dependencies:\n  - %s\n", bURL), "Skill A")
	aHash := fetch.ComputeSHA256(aContent)

	h := &harness.Harness{
		Skills: []string{fmt.Sprintf("%s/skills/a.md#sha256=%s", srv.URL, aHash)},
		// Only /skills/a.md is allowed; /skills/b.md (the transitive dep) is not.
		AllowedRemoteResources: []string{srv.URL + "/skills/a.md"},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   policy,
		MaxDepth:      -1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in allowed_remote_resources")
}

// TestResolveHarness_TransitiveHashMismatch verifies that a transitive dep whose
// fetched content does not match the declared SHA256 hash is rejected.
func TestResolveHarness_TransitiveHashMismatch(t *testing.T) {
	// Declare B with the hash of "expected content" but serve "tampered content".
	expectedBContent := []byte("expected B content")
	bHash := fetch.ComputeSHA256(expectedBContent)

	var aContent []byte

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills/a.md":
			w.Write(aContent)
		case "/skills/b.md":
			w.Write([]byte("tampered B content"))
		}
	}))

	bURL := fmt.Sprintf("%s/skills/b.md#sha256=%s", srv.URL, bHash)
	aContent = skillFrontmatter(fmt.Sprintf("dependencies:\n  - %s\n", bURL), "Skill A")
	aHash := fetch.ComputeSHA256(aContent)

	h := &harness.Harness{
		Skills:                 []string{fmt.Sprintf("%s/skills/a.md#sha256=%s", srv.URL, aHash)},
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   policy,
		MaxDepth:      -1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "integrity check failed")
}

// TestResolveHarness_TransitiveRelativeURL verifies that a relative dependency reference
// in skill frontmatter is resolved against the parent skill's URL via RFC 3986.
func TestResolveHarness_TransitiveRelativeURL(t *testing.T) {
	bContent := []byte("Skill B — resolved via relative URL")
	bHash := fetch.ComputeSHA256(bContent)

	var aContent []byte

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills/a.md":
			w.Write(aContent)
		case "/common/b.md":
			w.Write(bContent)
		}
	}))

	// A is at /skills/a.md; the relative dep "../common/b.md" resolves to /common/b.md.
	relDep := fmt.Sprintf("../common/b.md#sha256=%s", bHash)
	aContent = skillFrontmatter(fmt.Sprintf("dependencies:\n  - %s\n", relDep), "Skill A")
	aHash := fetch.ComputeSHA256(aContent)

	h := &harness.Harness{
		Skills:                 []string{fmt.Sprintf("%s/skills/a.md#sha256=%s", srv.URL, aHash)},
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   policy,
		MaxDepth:      -1,
	})
	require.NoError(t, err)
	assert.Len(t, deps, 2)

	urls := make(map[string]bool)
	for _, d := range deps {
		urls[d.URL] = true
	}
	assert.True(t, urls[srv.URL+"/common/b.md"], "relative URL should resolve to /common/b.md")
}

// TestResolveHarness_ConflictingHashesForSameURL verifies that two skills declaring the
// same transitive dep URL with different SHA256 hashes is rejected.
func TestResolveHarness_ConflictingHashesForSameURL(t *testing.T) {
	dContent := []byte("Skill D content")
	dHash := fetch.ComputeSHA256(dContent)
	fakeHash := strings.Repeat("b", 64)

	var aContent, bContent []byte

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills/a.md":
			w.Write(aContent)
		case "/skills/b.md":
			w.Write(bContent)
		case "/skills/d.md":
			w.Write(dContent)
		}
	}))

	dURL := srv.URL + "/skills/d.md"
	aContent = skillFrontmatter(fmt.Sprintf("dependencies:\n  - %s#sha256=%s\n", dURL, dHash), "Skill A")
	aHash := fetch.ComputeSHA256(aContent)
	bContent = skillFrontmatter(fmt.Sprintf("dependencies:\n  - %s#sha256=%s\n", dURL, fakeHash), "Skill B")
	bHash := fetch.ComputeSHA256(bContent)

	h := &harness.Harness{
		Skills: []string{
			fmt.Sprintf("%s/skills/a.md#sha256=%s", srv.URL, aHash),
			fmt.Sprintf("%s/skills/b.md#sha256=%s", srv.URL, bHash),
		},
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   policy,
		MaxDepth:      -1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflicting integrity hashes")
}

// TestResolveHarness_SkillPolicyLeafNode verifies that a skill-level policy reference
// is fetched and recorded in deps but is NOT appended to h.Skills.
func TestResolveHarness_SkillPolicyLeafNode(t *testing.T) {
	policyContent := []byte("sandbox: strict")
	policyHash := fetch.ComputeSHA256(policyContent)

	var aContent []byte

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills/a.md":
			w.Write(aContent)
		case "/policies/sandbox.yaml":
			w.Write(policyContent)
		}
	}))

	policyURL := fmt.Sprintf("%s/policies/sandbox.yaml#sha256=%s", srv.URL, policyHash)
	aContent = skillFrontmatter(fmt.Sprintf("policy: %s\n", policyURL), "Skill A content")
	aHash := fetch.ComputeSHA256(aContent)

	h := &harness.Harness{
		Skills:                 []string{fmt.Sprintf("%s/skills/a.md#sha256=%s", srv.URL, aHash)},
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   policy,
		MaxDepth:      -1,
	})
	require.NoError(t, err)
	assert.Len(t, deps, 2) // skill A + its policy
	assert.Len(t, h.Skills, 1) // policy is NOT added to h.Skills

	depURLs := make(map[string]bool)
	for _, d := range deps {
		depURLs[d.URL] = true
	}
	assert.True(t, depURLs[srv.URL+"/policies/sandbox.yaml"], "policy should be in deps")

	for _, s := range h.Skills {
		assert.NotContains(t, s, "sandbox.yaml", "policy path must not appear in h.Skills")
	}
}

// TestResolveHarness_ZeroMaxDepthDisablesTransitive verifies that MaxDepth=0 prevents
// any transitive dependency resolution even when skills declare dependencies.
func TestResolveHarness_ZeroMaxDepthDisablesTransitive(t *testing.T) {
	bContent := []byte("Skill B — must not be fetched")
	bHash := fetch.ComputeSHA256(bContent)

	var aContent []byte
	var bFetched atomic.Int32

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills/a.md":
			w.Write(aContent)
		case "/skills/b.md":
			bFetched.Add(1)
			w.Write(bContent)
		}
	}))

	bURL := fmt.Sprintf("%s/skills/b.md#sha256=%s", srv.URL, bHash)
	aContent = skillFrontmatter(fmt.Sprintf("dependencies:\n  - %s\n", bURL), "Skill A")
	aHash := fetch.ComputeSHA256(aContent)

	h := &harness.Harness{
		Skills:                 []string{fmt.Sprintf("%s/skills/a.md#sha256=%s", srv.URL, aHash)},
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   policy,
		MaxDepth:      0, // disabled
	})
	require.NoError(t, err)
	assert.Len(t, deps, 1)    // only A
	assert.Len(t, h.Skills, 1) // only A
	assert.Equal(t, int32(0), bFetched.Load()) // B never fetched
}

// TestResolveHarness_MaxDepthDefaultApplied verifies that MaxDepth<0 uses DefaultMaxDepth
// and enables transitive resolution.
func TestResolveHarness_MaxDepthDefaultApplied(t *testing.T) {
	bContent := []byte("Skill B content")
	bHash := fetch.ComputeSHA256(bContent)

	var aContent []byte

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills/a.md":
			w.Write(aContent)
		case "/skills/b.md":
			w.Write(bContent)
		}
	}))

	bURL := fmt.Sprintf("%s/skills/b.md#sha256=%s", srv.URL, bHash)
	aContent = skillFrontmatter(fmt.Sprintf("dependencies:\n  - %s\n", bURL), "Skill A")
	aHash := fetch.ComputeSHA256(aContent)

	h := &harness.Harness{
		Skills:                 []string{fmt.Sprintf("%s/skills/a.md#sha256=%s", srv.URL, aHash)},
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   policy,
		MaxDepth:      -1, // uses DefaultMaxDepth
	})
	require.NoError(t, err)
	assert.Len(t, deps, 2) // A and B both resolved
}

// TestResolveHarness_NonHTTPSSchemeRejected verifies that resolveURL rejects URLs whose
// scheme is not https, providing a defense-in-depth check for transitive deps from frontmatter
// that bypass the harness.IsURL guard applied to direct harness fields.
func TestResolveHarness_NonHTTPSSchemeRejected(t *testing.T) {
	bContent := []byte("Skill B content")
	bHash := fetch.ComputeSHA256(bContent)

	var aContent []byte

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills/a.md":
			w.Write(aContent)
		}
	}))

	// Embed an http:// (non-HTTPS) transitive dep in A's frontmatter.
	httpDepURL := fmt.Sprintf("http://example.com/skills/b.md#sha256=%s", bHash)
	aContent = skillFrontmatter(fmt.Sprintf("dependencies:\n  - %s\n", httpDepURL), "Skill A")
	aHash := fetch.ComputeSHA256(aContent)

	h := &harness.Harness{
		Skills:                 []string{fmt.Sprintf("%s/skills/a.md#sha256=%s", srv.URL, aHash)},
		AllowedRemoteResources: []string{srv.URL + "/", "http://example.com/"},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   policy,
		MaxDepth:      -1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scheme must be https")
}

// TestResolveHarness_DirectAndTransitiveOverlap verifies that a skill appearing both as a
// direct harness skill and as a transitive dep of another skill is deduplicated in h.Skills.
func TestResolveHarness_DirectAndTransitiveOverlap(t *testing.T) {
	bContent := []byte("Skill B — shared skill")
	bHash := fetch.ComputeSHA256(bContent)

	var aContent []byte

	srv, policy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills/a.md":
			w.Write(aContent)
		case "/skills/b.md":
			w.Write(bContent)
		}
	}))

	bURL := fmt.Sprintf("%s/skills/b.md#sha256=%s", srv.URL, bHash)
	aContent = skillFrontmatter(fmt.Sprintf("dependencies:\n  - %s\n", bURL), "Skill A")
	aHash := fetch.ComputeSHA256(aContent)

	// Both A and B are direct harness skills; A also depends on B transitively.
	h := &harness.Harness{
		Skills: []string{
			fmt.Sprintf("%s/skills/a.md#sha256=%s", srv.URL, aHash),
			bURL,
		},
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   policy,
		MaxDepth:      -1,
	})
	require.NoError(t, err)
	assert.Len(t, deps, 2)    // A and B, each exactly once
	assert.Len(t, h.Skills, 2) // A's path and B's path, B deduped

	// B must not appear twice in h.Skills.
	seen := make(map[string]bool)
	for _, s := range h.Skills {
		assert.False(t, seen[s], "h.Skills contains duplicate entry %s", s)
		seen[s] = true
	}
}
