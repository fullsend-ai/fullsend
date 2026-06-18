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
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harness"
)

// --- test helpers for forge-based skill resolution ---

const (
	testForgeOwner = "test-org"
	testForgeRepo  = "test-repo"
	testForgeRef   = "main"
	testForgeBase  = "https://github.com/" + testForgeOwner + "/" + testForgeRepo + "/"
)

func forgeSkillURL(path, treeHash string) string {
	return fmt.Sprintf("https://github.com/%s/%s/tree/%s/%s#sha256=%s",
		testForgeOwner, testForgeRepo, testForgeRef, path, treeHash)
}

func forgeSkillCleanURL(path string) string {
	return fmt.Sprintf("https://github.com/%s/%s/tree/%s/%s",
		testForgeOwner, testForgeRepo, testForgeRef, path)
}

// registerSkillDir sets up a skill directory in the FakeClient and returns the tree hash.
func registerSkillDir(fc *forge.FakeClient, path string, files map[string][]byte) string {
	treeHash := fetch.ComputeTreeHash(files)

	dirKey := fmt.Sprintf("%s/%s/%s@%s", testForgeOwner, testForgeRepo, path, testForgeRef)

	entries := make([]forge.DirectoryEntry, 0, len(files))
	for relPath, content := range files {
		entries = append(entries, forge.DirectoryEntry{
			Path: relPath,
			Type: "file",
			Size: len(content),
		})
	}

	if fc.DirContents == nil {
		fc.DirContents = make(map[string][]forge.DirectoryEntry)
	}
	if fc.FileContentsRef == nil {
		fc.FileContentsRef = make(map[string][]byte)
	}

	fc.DirContents[dirKey] = entries
	for relPath, content := range files {
		fileKey := fmt.Sprintf("%s/%s/%s/%s@%s", testForgeOwner, testForgeRepo, path, relPath, testForgeRef)
		fc.FileContentsRef[fileKey] = content
	}

	return treeHash
}

// skillFrontmatter returns SKILL.md content with the given YAML frontmatter fields
// and optional body text after the closing delimiter.
func skillFrontmatter(fields, body string) []byte {
	return []byte("---\n" + fields + "---\n" + body)
}

// --- test helpers for HTTP-served single-file resources (agents, policies) ---

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

// --- Tests ---

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
	assert.Equal(t, "file", deps[0].Type)

	assert.True(t, strings.HasSuffix(h.Agent, "/content"))
	assert.False(t, harness.IsURL(h.Agent))

	got, err := os.ReadFile(h.Agent)
	require.NoError(t, err)
	assert.Equal(t, agentContent, got)
}

func TestResolveHarness_DependencyField(t *testing.T) {
	agentContent := []byte("You are an agent.")
	agentHash := fetch.ComputeSHA256(agentContent)
	policyContent := []byte("policy: readonly")
	policyHash := fetch.ComputeSHA256(policyContent)
	skillMD := []byte("# Skill\nA skill.")

	srv, fetchPolicy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agents/code.md":
			w.Write(agentContent)
		case "/policies/ro.yaml":
			w.Write(policyContent)
		default:
			http.NotFound(w, r)
		}
	}))

	fc := &forge.FakeClient{}
	skillHash := registerSkillDir(fc, "skills/rust", map[string][]byte{"SKILL.md": skillMD})

	root := t.TempDir()
	h := &harness.Harness{
		Agent:                  fmt.Sprintf("%s/agents/code.md#sha256=%s", srv.URL, agentHash),
		Policy:                 fmt.Sprintf("%s/policies/ro.yaml#sha256=%s", srv.URL, policyHash),
		Skills:                 []string{forgeSkillURL("skills/rust", skillHash)},
		AllowedRemoteResources: []string{srv.URL + "/", testForgeBase},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: root,
		FetchPolicy:   fetchPolicy,
		ForgeClient:   fc,
	})
	require.NoError(t, err)
	require.Len(t, deps, 3)

	assert.Equal(t, "agent", deps[0].Field)
	assert.Equal(t, "file", deps[0].Type)
	assert.Equal(t, "policy", deps[1].Field)
	assert.Equal(t, "file", deps[1].Type)
	assert.Equal(t, "skills[0]", deps[2].Field)
	assert.Equal(t, "directory", deps[2].Type)
}

func TestResolveHarness_SkillDirFetchAndCache(t *testing.T) {
	skillMD := []byte("---\nname: review\n---\n# Code Review skill")
	helperSh := []byte("#!/bin/bash\necho hello")

	fc := &forge.FakeClient{}
	treeHash := registerSkillDir(fc, "skills/review", map[string][]byte{
		"SKILL.md":          skillMD,
		"scripts/helper.sh": helperSh,
	})

	root := t.TempDir()
	h := &harness.Harness{
		Skills:                 []string{forgeSkillURL("skills/review", treeHash)},
		AllowedRemoteResources: []string{testForgeBase},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: root,
		ForgeClient:   fc,
	})
	require.NoError(t, err)
	require.Len(t, deps, 1)
	assert.Equal(t, "directory", deps[0].Type)
	assert.Equal(t, treeHash, deps[0].SHA256)
	assert.False(t, deps[0].CacheHit)

	// Verify h.Skills[0] is a directory path (the tree/ subdirectory).
	info, err := os.Stat(h.Skills[0])
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	// Verify SKILL.md is inside the cached directory.
	got, err := os.ReadFile(filepath.Join(h.Skills[0], "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, skillMD, got)

	// Verify companion file is inside the cached directory.
	got, err = os.ReadFile(filepath.Join(h.Skills[0], "scripts", "helper.sh"))
	require.NoError(t, err)
	assert.Equal(t, helperSh, got)
}

func TestResolveHarness_SkillDirCacheHit(t *testing.T) {
	skillMD := []byte("# Cached skill")

	fc := &forge.FakeClient{}
	files := map[string][]byte{"SKILL.md": skillMD}
	treeHash := registerSkillDir(fc, "skills/cached", files)

	root := t.TempDir()
	// Pre-populate the directory cache.
	_, err := fetch.CachePutDir(root, forgeSkillCleanURL("skills/cached"), files)
	require.NoError(t, err)

	h := &harness.Harness{
		Skills:                 []string{forgeSkillURL("skills/cached", treeHash)},
		AllowedRemoteResources: []string{testForgeBase},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: root,
		ForgeClient:   fc,
	})
	require.NoError(t, err)
	require.Len(t, deps, 1)
	assert.True(t, deps[0].CacheHit)
}

func TestResolveHarness_SkillDirHashMismatch(t *testing.T) {
	fc := &forge.FakeClient{}
	registerSkillDir(fc, "skills/tampered", map[string][]byte{"SKILL.md": []byte("wrong content")})

	wrongHash := fetch.ComputeTreeHash(map[string][]byte{"SKILL.md": []byte("expected content")})

	h := &harness.Harness{
		Skills:                 []string{forgeSkillURL("skills/tampered", wrongHash)},
		AllowedRemoteResources: []string{testForgeBase},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		ForgeClient:   fc,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "integrity check failed")
}

func TestResolveHarness_SkillNonForgeURLRejected(t *testing.T) {
	srv, fetchPolicy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("skill content"))
	}))

	fakeHash := strings.Repeat("a", 64)
	h := &harness.Harness{
		Skills:                 []string{fmt.Sprintf("%s/skills/review#sha256=%s", srv.URL, fakeHash)},
		AllowedRemoteResources: []string{srv.URL + "/"},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   fetchPolicy,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "supported forge")
}

func TestResolveHarness_DiamondDependency(t *testing.T) {
	fc := &forge.FakeClient{}

	sharedMD := []byte("---\ndependencies: []\n---\n# Shared skill")
	sharedHash := registerSkillDir(fc, "skills/shared", map[string][]byte{"SKILL.md": sharedMD})

	parentMD := skillFrontmatter(
		fmt.Sprintf("dependencies:\n  - shared#sha256=%s\n", sharedHash),
		"# Parent skill",
	)
	parentHash := registerSkillDir(fc, "skills/parent", map[string][]byte{"SKILL.md": parentMD})

	root := t.TempDir()
	h := &harness.Harness{
		Skills: []string{
			forgeSkillURL("skills/parent", parentHash),
			forgeSkillURL("skills/shared", sharedHash),
		},
		AllowedRemoteResources: []string{testForgeBase},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: root,
		ForgeClient:   fc,
		MaxDepth:      5,
	})
	require.NoError(t, err)

	sharedCleanURL := forgeSkillCleanURL("skills/shared")
	var sharedFields []string
	for _, d := range deps {
		if d.URL == sharedCleanURL {
			sharedFields = append(sharedFields, d.Field)
		}
	}
	require.Len(t, sharedFields, 1)
	assert.Contains(t, sharedFields[0], "dep0")

	require.Len(t, h.Skills, 2)
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

func TestResolveHarness_SkillDirOfflineMiss(t *testing.T) {
	fc := &forge.FakeClient{}
	skillHash := registerSkillDir(fc, "skills/offline", map[string][]byte{"SKILL.md": []byte("# Skill")})

	h := &harness.Harness{
		Skills:                 []string{forgeSkillURL("skills/offline", skillHash)},
		AllowedRemoteResources: []string{testForgeBase},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		ForgeClient:   fc,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "offline")
}

func TestResolveHarness_SkillDirOfflineHit(t *testing.T) {
	fc := &forge.FakeClient{}
	files := map[string][]byte{"SKILL.md": []byte("# Cached skill for offline")}
	skillHash := registerSkillDir(fc, "skills/offline", files)

	root := t.TempDir()
	_, err := fetch.CachePutDir(root, forgeSkillCleanURL("skills/offline"), files)
	require.NoError(t, err)

	h := &harness.Harness{
		Skills:                 []string{forgeSkillURL("skills/offline", skillHash)},
		AllowedRemoteResources: []string{testForgeBase},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: root,
		ForgeClient:   fc,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
	})
	require.NoError(t, err)
	require.Len(t, deps, 1)
	assert.True(t, deps[0].CacheHit)
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
	fc := &forge.FakeClient{}
	skill1MD := []byte("# Skill one")
	skill2MD := []byte("# Skill two")

	skill1Hash := registerSkillDir(fc, "skills/one", map[string][]byte{"SKILL.md": skill1MD})
	skill2Hash := registerSkillDir(fc, "skills/two", map[string][]byte{"SKILL.md": skill2MD})

	root := t.TempDir()
	h := &harness.Harness{
		Agent: "/local/agents/test.md",
		Skills: []string{
			"/local/skills/debug",
			forgeSkillURL("skills/one", skill1Hash),
			forgeSkillURL("skills/two", skill2Hash),
		},
		AllowedRemoteResources: []string{testForgeBase},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: root,
		ForgeClient:   fc,
	})
	require.NoError(t, err)
	require.Len(t, deps, 2)

	assert.Equal(t, "/local/skills/debug", h.Skills[0])
	assert.False(t, harness.IsURL(h.Skills[1]))
	assert.False(t, harness.IsURL(h.Skills[2]))

	// Verify skills resolve to directories with SKILL.md inside.
	got1, err := os.ReadFile(filepath.Join(h.Skills[1], "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, skill1MD, got1)

	got2, err := os.ReadFile(filepath.Join(h.Skills[2], "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, skill2MD, got2)
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

// TestResolveHarness_TransitiveChain verifies A→B→C transitive resolution:
// all three skill directories are fetched and added to h.Skills.
func TestResolveHarness_TransitiveChain(t *testing.T) {
	fc := &forge.FakeClient{}

	cMD := []byte("# Skill C — leaf node")
	cHash := registerSkillDir(fc, "skills/c", map[string][]byte{"SKILL.md": cMD})

	bMD := skillFrontmatter(
		fmt.Sprintf("dependencies:\n  - c#sha256=%s\n", cHash),
		"# Skill B",
	)
	bHash := registerSkillDir(fc, "skills/b", map[string][]byte{"SKILL.md": bMD})

	aMD := skillFrontmatter(
		fmt.Sprintf("dependencies:\n  - b#sha256=%s\n", bHash),
		"# Skill A",
	)
	aHash := registerSkillDir(fc, "skills/a", map[string][]byte{"SKILL.md": aMD})

	h := &harness.Harness{
		Skills:                 []string{forgeSkillURL("skills/a", aHash)},
		AllowedRemoteResources: []string{testForgeBase},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		ForgeClient:   fc,
		MaxDepth:      -1,
	})
	require.NoError(t, err)
	assert.Len(t, deps, 3)
	assert.Len(t, h.Skills, 3)

	urls := make(map[string]bool)
	for _, d := range deps {
		urls[d.URL] = true
	}
	assert.True(t, urls[forgeSkillCleanURL("skills/a")])
	assert.True(t, urls[forgeSkillCleanURL("skills/b")])
	assert.True(t, urls[forgeSkillCleanURL("skills/c")])
}

// TestResolveHarness_DiamondDedup verifies that a diamond graph (A→C, B→C) resolves C
// exactly once and produces no duplicate entries in deps or h.Skills.
func TestResolveHarness_DiamondDedup(t *testing.T) {
	fc := &forge.FakeClient{}

	cMD := []byte("# Skill C — shared dep")
	cHash := registerSkillDir(fc, "skills/c", map[string][]byte{"SKILL.md": cMD})

	aMD := skillFrontmatter(
		fmt.Sprintf("dependencies:\n  - c#sha256=%s\n", cHash),
		"# Skill A",
	)
	aHash := registerSkillDir(fc, "skills/a", map[string][]byte{"SKILL.md": aMD})

	bMD := skillFrontmatter(
		fmt.Sprintf("dependencies:\n  - c#sha256=%s\n", cHash),
		"# Skill B",
	)
	bHash := registerSkillDir(fc, "skills/b", map[string][]byte{"SKILL.md": bMD})

	h := &harness.Harness{
		Skills: []string{
			forgeSkillURL("skills/a", aHash),
			forgeSkillURL("skills/b", bHash),
		},
		AllowedRemoteResources: []string{testForgeBase},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		ForgeClient:   fc,
		MaxDepth:      -1,
	})
	require.NoError(t, err)
	assert.Len(t, deps, 3)
	assert.Len(t, h.Skills, 3)

	urls := make(map[string]bool)
	for _, d := range deps {
		assert.False(t, urls[d.URL], "duplicate dep URL %s", d.URL)
		urls[d.URL] = true
	}
}

// TestResolveHarness_CycleDetection verifies that A→B→A is rejected with a cycle error.
func TestResolveHarness_CycleDetection(t *testing.T) {
	fc := &forge.FakeClient{}

	placeholderHash := strings.Repeat("a", 64)

	// B references A with a placeholder hash; cycle fires before hash validation.
	bMD := skillFrontmatter(
		fmt.Sprintf("dependencies:\n  - a#sha256=%s\n", placeholderHash),
		"# Skill B",
	)
	bHash := registerSkillDir(fc, "skills/b", map[string][]byte{"SKILL.md": bMD})

	aMD := skillFrontmatter(
		fmt.Sprintf("dependencies:\n  - b#sha256=%s\n", bHash),
		"# Skill A",
	)
	aHash := registerSkillDir(fc, "skills/a", map[string][]byte{"SKILL.md": aMD})

	h := &harness.Harness{
		Skills:                 []string{forgeSkillURL("skills/a", aHash)},
		AllowedRemoteResources: []string{testForgeBase},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		ForgeClient:   fc,
		MaxDepth:      -1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circular dependency")
}

// TestResolveHarness_MaxDepthExceeded verifies that a chain A→B→C fails when MaxDepth=1.
func TestResolveHarness_MaxDepthExceeded(t *testing.T) {
	fc := &forge.FakeClient{}

	cMD := []byte("# Skill C — should not be reached")
	cHash := registerSkillDir(fc, "skills/c", map[string][]byte{"SKILL.md": cMD})

	bMD := skillFrontmatter(
		fmt.Sprintf("dependencies:\n  - c#sha256=%s\n", cHash),
		"# Skill B",
	)
	bHash := registerSkillDir(fc, "skills/b", map[string][]byte{"SKILL.md": bMD})

	aMD := skillFrontmatter(
		fmt.Sprintf("dependencies:\n  - b#sha256=%s\n", bHash),
		"# Skill A",
	)
	aHash := registerSkillDir(fc, "skills/a", map[string][]byte{"SKILL.md": aMD})

	h := &harness.Harness{
		Skills:                 []string{forgeSkillURL("skills/a", aHash)},
		AllowedRemoteResources: []string{testForgeBase},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		ForgeClient:   fc,
		MaxDepth:      1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeded maximum dependency depth")
}

// TestResolveHarness_MaxResourcesExceeded verifies that resolution stops when the
// resource count reaches MaxResources.
func TestResolveHarness_MaxResourcesExceeded(t *testing.T) {
	fc := &forge.FakeClient{}

	bMD := []byte("# Skill B")
	bHash := registerSkillDir(fc, "skills/b", map[string][]byte{"SKILL.md": bMD})

	aMD := skillFrontmatter(
		fmt.Sprintf("dependencies:\n  - b#sha256=%s\n", bHash),
		"# Skill A",
	)
	aHash := registerSkillDir(fc, "skills/a", map[string][]byte{"SKILL.md": aMD})

	h := &harness.Harness{
		Skills:                 []string{forgeSkillURL("skills/a", aHash)},
		AllowedRemoteResources: []string{testForgeBase},
	}

	// MaxResources=1: A consumes the single slot; B is rejected.
	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		ForgeClient:   fc,
		MaxDepth:      -1,
		MaxResources:  1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeded maximum resource count")
}

// TestResolveHarness_TransitiveNotInAllowlist verifies that a transitive dep whose
// URL does not match allowed_remote_resources is rejected.
func TestResolveHarness_TransitiveNotInAllowlist(t *testing.T) {
	fc := &forge.FakeClient{}

	bMD := []byte("# Skill B")
	bHash := registerSkillDir(fc, "skills/b", map[string][]byte{"SKILL.md": bMD})

	aMD := skillFrontmatter(
		fmt.Sprintf("dependencies:\n  - b#sha256=%s\n", bHash),
		"# Skill A",
	)
	aHash := registerSkillDir(fc, "skills/a", map[string][]byte{"SKILL.md": aMD})

	h := &harness.Harness{
		Skills: []string{forgeSkillURL("skills/a", aHash)},
		// Only skill A's exact path is allowed; skill B (the transitive dep) is not.
		AllowedRemoteResources: []string{forgeSkillCleanURL("skills/a")},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		ForgeClient:   fc,
		MaxDepth:      -1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in allowed_remote_resources")
}

// TestResolveHarness_TransitiveHashMismatch verifies that a transitive dep whose
// fetched content does not match the declared tree hash is rejected.
func TestResolveHarness_TransitiveHashMismatch(t *testing.T) {
	fc := &forge.FakeClient{}

	// Register B with content that doesn't match the hash A declares.
	registerSkillDir(fc, "skills/b", map[string][]byte{"SKILL.md": []byte("tampered B content")})

	// A declares B with the hash of "expected B content".
	expectedBHash := fetch.ComputeTreeHash(map[string][]byte{"SKILL.md": []byte("expected B content")})
	aMD := skillFrontmatter(
		fmt.Sprintf("dependencies:\n  - b#sha256=%s\n", expectedBHash),
		"# Skill A",
	)
	aHash := registerSkillDir(fc, "skills/a", map[string][]byte{"SKILL.md": aMD})

	h := &harness.Harness{
		Skills:                 []string{forgeSkillURL("skills/a", aHash)},
		AllowedRemoteResources: []string{testForgeBase},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		ForgeClient:   fc,
		MaxDepth:      -1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "integrity check failed")
}

// TestResolveHarness_TransitiveRelativeURL verifies that a relative dependency reference
// in skill frontmatter is resolved against the parent skill's URL.
func TestResolveHarness_TransitiveRelativeURL(t *testing.T) {
	fc := &forge.FakeClient{}

	bMD := []byte("# Skill B — resolved via relative URL")
	bHash := registerSkillDir(fc, "common/b", map[string][]byte{"SKILL.md": bMD})

	// A is at skills/a; the relative dep "../common/b" resolves to common/b.
	aMD := skillFrontmatter(
		fmt.Sprintf("dependencies:\n  - ../common/b#sha256=%s\n", bHash),
		"# Skill A",
	)
	aHash := registerSkillDir(fc, "skills/a", map[string][]byte{"SKILL.md": aMD})

	h := &harness.Harness{
		Skills:                 []string{forgeSkillURL("skills/a", aHash)},
		AllowedRemoteResources: []string{testForgeBase},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		ForgeClient:   fc,
		MaxDepth:      -1,
	})
	require.NoError(t, err)
	assert.Len(t, deps, 2)

	urls := make(map[string]bool)
	for _, d := range deps {
		urls[d.URL] = true
	}
	assert.True(t, urls[forgeSkillCleanURL("common/b")], "relative URL should resolve to common/b")
}

// TestResolveHarness_ConflictingHashesForSameURL verifies that two skills declaring the
// same transitive dep URL with different tree hashes is rejected.
func TestResolveHarness_ConflictingHashesForSameURL(t *testing.T) {
	fc := &forge.FakeClient{}

	dMD := []byte("# Skill D")
	dHash := registerSkillDir(fc, "skills/d", map[string][]byte{"SKILL.md": dMD})
	fakeHash := strings.Repeat("b", 64)

	dURL := forgeSkillCleanURL("skills/d")

	aMD := skillFrontmatter(
		fmt.Sprintf("dependencies:\n  - d#sha256=%s\n", dHash),
		"# Skill A",
	)
	aHash := registerSkillDir(fc, "skills/a", map[string][]byte{"SKILL.md": aMD})

	bMD := skillFrontmatter(
		fmt.Sprintf("dependencies:\n  - d#sha256=%s\n", fakeHash),
		"# Skill B",
	)
	bHash := registerSkillDir(fc, "skills/b", map[string][]byte{"SKILL.md": bMD})

	_ = dURL // referenced only to clarify the test setup

	h := &harness.Harness{
		Skills: []string{
			forgeSkillURL("skills/a", aHash),
			forgeSkillURL("skills/b", bHash),
		},
		AllowedRemoteResources: []string{testForgeBase},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		ForgeClient:   fc,
		MaxDepth:      -1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflicting integrity hashes")
}

// TestResolveHarness_SkillPolicyLeafNode verifies that a skill-level policy reference
// is fetched as a single file and recorded in deps but is NOT appended to h.Skills.
func TestResolveHarness_SkillPolicyLeafNode(t *testing.T) {
	policyContent := []byte("sandbox: strict")
	policyHash := fetch.ComputeSHA256(policyContent)

	srv, fetchPolicy := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/policies/sandbox.yaml":
			w.Write(policyContent)
		}
	}))

	fc := &forge.FakeClient{}

	policyURL := fmt.Sprintf("%s/policies/sandbox.yaml#sha256=%s", srv.URL, policyHash)
	aMD := skillFrontmatter(
		fmt.Sprintf("policy: %s\n", policyURL),
		"# Skill A content",
	)
	aHash := registerSkillDir(fc, "skills/a", map[string][]byte{"SKILL.md": aMD})

	h := &harness.Harness{
		Skills:                 []string{forgeSkillURL("skills/a", aHash)},
		AllowedRemoteResources: []string{testForgeBase, srv.URL + "/"},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   fetchPolicy,
		ForgeClient:   fc,
		MaxDepth:      -1,
	})
	require.NoError(t, err)
	assert.Len(t, deps, 2) // skill A + its policy
	assert.Len(t, h.Skills, 1)

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
	fc := &forge.FakeClient{}

	bMD := []byte("# Skill B — must not be fetched")
	bHash := registerSkillDir(fc, "skills/b", map[string][]byte{"SKILL.md": bMD})

	aMD := skillFrontmatter(
		fmt.Sprintf("dependencies:\n  - b#sha256=%s\n", bHash),
		"# Skill A",
	)
	aHash := registerSkillDir(fc, "skills/a", map[string][]byte{"SKILL.md": aMD})

	h := &harness.Harness{
		Skills:                 []string{forgeSkillURL("skills/a", aHash)},
		AllowedRemoteResources: []string{testForgeBase},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		ForgeClient:   fc,
		MaxDepth:      0, // disabled
	})
	require.NoError(t, err)
	assert.Len(t, deps, 1)     // only A
	assert.Len(t, h.Skills, 1) // only A
}

// TestResolveHarness_MaxDepthDefaultApplied verifies that MaxDepth<0 uses DefaultMaxDepth
// and enables transitive resolution.
func TestResolveHarness_MaxDepthDefaultApplied(t *testing.T) {
	fc := &forge.FakeClient{}

	bMD := []byte("# Skill B")
	bHash := registerSkillDir(fc, "skills/b", map[string][]byte{"SKILL.md": bMD})

	aMD := skillFrontmatter(
		fmt.Sprintf("dependencies:\n  - b#sha256=%s\n", bHash),
		"# Skill A",
	)
	aHash := registerSkillDir(fc, "skills/a", map[string][]byte{"SKILL.md": aMD})

	h := &harness.Harness{
		Skills:                 []string{forgeSkillURL("skills/a", aHash)},
		AllowedRemoteResources: []string{testForgeBase},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		ForgeClient:   fc,
		MaxDepth:      -1, // uses DefaultMaxDepth
	})
	require.NoError(t, err)
	assert.Len(t, deps, 2) // A and B both resolved
}

// TestResolveHarness_NonHTTPSSchemeRejected verifies that resolveSkillDirURL rejects URLs
// whose scheme is not https.
func TestResolveHarness_NonHTTPSSchemeRejected(t *testing.T) {
	fc := &forge.FakeClient{}

	bHash := fetch.ComputeTreeHash(map[string][]byte{"SKILL.md": []byte("# B")})

	// Embed an http:// (non-HTTPS) transitive dep in A's frontmatter.
	httpDepURL := fmt.Sprintf("http://github.com/%s/%s/tree/%s/skills/b#sha256=%s",
		testForgeOwner, testForgeRepo, testForgeRef, bHash)
	aMD := skillFrontmatter(
		fmt.Sprintf("dependencies:\n  - %s\n", httpDepURL),
		"# Skill A",
	)
	aHash := registerSkillDir(fc, "skills/a", map[string][]byte{"SKILL.md": aMD})

	h := &harness.Harness{
		Skills:                 []string{forgeSkillURL("skills/a", aHash)},
		AllowedRemoteResources: []string{testForgeBase, "http://github.com/"},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		ForgeClient:   fc,
		MaxDepth:      -1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scheme must be https")
}

// TestResolveHarness_DirectAndTransitiveOverlap verifies that a skill appearing both as a
// direct harness skill and as a transitive dep of another skill is deduplicated.
func TestResolveHarness_DirectAndTransitiveOverlap(t *testing.T) {
	fc := &forge.FakeClient{}

	bMD := []byte("# Skill B — shared skill")
	bHash := registerSkillDir(fc, "skills/b", map[string][]byte{"SKILL.md": bMD})

	aMD := skillFrontmatter(
		fmt.Sprintf("dependencies:\n  - b#sha256=%s\n", bHash),
		"# Skill A",
	)
	aHash := registerSkillDir(fc, "skills/a", map[string][]byte{"SKILL.md": aMD})

	bURL := forgeSkillURL("skills/b", bHash)

	// Both A and B are direct harness skills; A also depends on B transitively.
	h := &harness.Harness{
		Skills: []string{
			forgeSkillURL("skills/a", aHash),
			bURL,
		},
		AllowedRemoteResources: []string{testForgeBase},
	}

	deps, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		ForgeClient:   fc,
		MaxDepth:      -1,
	})
	require.NoError(t, err)
	assert.Len(t, deps, 2)     // A and B, each exactly once
	assert.Len(t, h.Skills, 2) // A's path and B's path, B deduped

	// B must not appear twice in h.Skills.
	seen := make(map[string]bool)
	for _, s := range h.Skills {
		assert.False(t, seen[s], "h.Skills contains duplicate entry %s", s)
		seen[s] = true
	}
}

// TestResolveHarness_NilForgeClientWithSkillURL verifies that a skill URL without
// a ForgeClient produces a clear error.
func TestResolveHarness_NilForgeClientWithSkillURL(t *testing.T) {
	fakeHash := strings.Repeat("a", 64)
	h := &harness.Harness{
		Skills:                 []string{forgeSkillURL("skills/test", fakeHash)},
		AllowedRemoteResources: []string{testForgeBase},
	}

	_, err := ResolveHarness(context.Background(), h, ResolveOpts{
		WorkspaceRoot: t.TempDir(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ForgeClient is required")
}
