package cli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newLockTestServer(t *testing.T, contents map[string][]byte) (*httptest.Server, fetch.FetchPolicy) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if data, ok := contents[r.URL.Path]; ok {
			w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	hostPort := strings.TrimPrefix(srv.URL, "https://")
	hostname, port, _ := net.SplitHostPort(hostPort)

	tlsCfg := srv.TLS.Clone()
	tlsCfg.InsecureSkipVerify = true

	return srv, fetch.NewTestPolicy(tlsCfg, []string{hostname}, []string{port})
}

func setupLockTestDir(t *testing.T, srv *httptest.Server, agentHash, skillHash string) string {
	t.Helper()
	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessContent := fmt.Sprintf(`agent: "%s/agents/code.md#sha256=%s"
skills:
  - "%s/skills/rust/SKILL.md#sha256=%s"
allowed_remote_resources:
  - "%s/"
`, srv.URL, agentHash, srv.URL, skillHash, srv.URL)

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte(harnessContent),
		0o644,
	))

	orgConfig := fmt.Sprintf(`allowed_remote_resources:
  - "%s/"
`, srv.URL)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte(orgConfig),
		0o644,
	))

	return dir
}

func TestRunLock_GeneratesLockFile(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)
	skillContent := []byte("# Rust skill\nRust development skill.")
	skillHash := fetch.ComputeSHA256(skillContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md":       agentContent,
		"/skills/rust/SKILL.md": skillContent,
	})

	dir := setupLockTestDir(t, srv, agentHash, skillHash)

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "code", dir, false, resolveFlags{}, printer)
	require.NoError(t, err)

	lockPath := filepath.Join(dir, "lock.yaml")
	lf, err := lock.Load(lockPath)
	require.NoError(t, err)
	require.NotNil(t, lf)

	entry := lf.Lookup("code")
	require.NotNil(t, entry)
	assert.Equal(t, "harness/code.yaml", entry.Source)
	assert.NotEmpty(t, entry.SHA256)
	require.Len(t, entry.Dependencies, 2)

	assert.Equal(t, "agent", entry.Dependencies[0].Field)
	assert.Equal(t, fmt.Sprintf("%s/agents/code.md", srv.URL), entry.Dependencies[0].URL)
	assert.Equal(t, agentHash, entry.Dependencies[0].SHA256)

	assert.Equal(t, "skills[0]", entry.Dependencies[1].Field)
	assert.Equal(t, fmt.Sprintf("%s/skills/rust/SKILL.md", srv.URL), entry.Dependencies[1].URL)
	assert.Equal(t, skillHash, entry.Dependencies[1].SHA256)
}

func TestRunLock_NoURLReferences(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessContent := `agent: agents/code.md
skills:
  - skills/rust
`
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "local.yaml"),
		[]byte(harnessContent),
		0o644,
	))

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "local", dir, false, resolveFlags{}, printer)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(dir, "lock.yaml"))
	assert.True(t, os.IsNotExist(err), "lock file should not be created for local-only harness")
}

func TestRunLock_AlreadyUpToDate(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)
	skillContent := []byte("# Rust skill")
	skillHash := fetch.ComputeSHA256(skillContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md":       agentContent,
		"/skills/rust/SKILL.md": skillContent,
	})

	dir := setupLockTestDir(t, srv, agentHash, skillHash)

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)

	// First lock.
	require.NoError(t, runLock(context.Background(), "code", dir, false, resolveFlags{}, printer))

	// Second lock without --update should detect it's current.
	require.NoError(t, runLock(context.Background(), "code", dir, false, resolveFlags{}, printer))

	// Verify lock file still exists and is valid.
	lf, err := lock.Load(filepath.Join(dir, "lock.yaml"))
	require.NoError(t, err)
	require.NotNil(t, lf.Lookup("code"))
}

func TestRunLock_UpdateForceReResolve(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)
	skillContent := []byte("# Rust skill")
	skillHash := fetch.ComputeSHA256(skillContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md":       agentContent,
		"/skills/rust/SKILL.md": skillContent,
	})

	dir := setupLockTestDir(t, srv, agentHash, skillHash)

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)

	// First lock.
	require.NoError(t, runLock(context.Background(), "code", dir, false, resolveFlags{}, printer))

	lf1, _ := lock.Load(filepath.Join(dir, "lock.yaml"))
	entry1 := lf1.Lookup("code")
	resolvedAt1 := entry1.ResolvedAt

	// Second lock with --update should re-resolve.
	require.NoError(t, runLock(context.Background(), "code", dir, true, resolveFlags{}, printer))

	lf2, _ := lock.Load(filepath.Join(dir, "lock.yaml"))
	entry2 := lf2.Lookup("code")

	assert.True(t, entry2.ResolvedAt.After(resolvedAt1) || entry2.ResolvedAt.Equal(resolvedAt1))
}

func TestResolveFromLock_Success(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/agents/code.md", agentContent))

	entry := &lock.HarnessLock{
		Source: "harness/code.yaml",
		SHA256: "abc",
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "agent",
				URL:    "https://example.com/agents/code.md",
				SHA256: agentHash,
			},
		},
	}

	h := &harness.Harness{
		Agent: "https://example.com/agents/code.md#sha256=" + agentHash,
	}

	printer := ui.New(os.Stdout)
	deps, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, deps, 1)

	assert.Equal(t, "agent", deps[0].Field)
	assert.Equal(t, agentHash, deps[0].SHA256)
	assert.True(t, deps[0].CacheHit)
	assert.True(t, strings.HasSuffix(h.Agent, "/content"))
}

func TestResolveFromLock_MissingCache(t *testing.T) {
	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "agent",
				URL:    "https://example.com/agents/code.md",
				SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
			},
		},
	}

	h := &harness.Harness{
		Agent: "https://example.com/agents/code.md#sha256=0000000000000000000000000000000000000000000000000000000000000000",
	}

	printer := ui.New(os.Stdout)
	_, err := resolveFromLock(h, entry, t.TempDir(), printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in cache")
}

func TestResolveFromLock_SkillSlots(t *testing.T) {
	skillA := []byte("skill A content")
	hashA := fetch.ComputeSHA256(skillA)
	skillB := []byte("skill B content")
	hashB := fetch.ComputeSHA256(skillB)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/skills/a", skillA))
	require.NoError(t, fetch.CachePut(root, "https://example.com/skills/b", skillB))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{Field: "skills[0]", URL: "https://example.com/skills/a", SHA256: hashA},
			{Field: "skills[1]", URL: "https://example.com/skills/b", SHA256: hashB},
		},
	}

	h := &harness.Harness{
		Agent:  "agents/code.md",
		Skills: []string{"https://example.com/skills/a#sha256=" + hashA, "https://example.com/skills/b#sha256=" + hashB},
	}

	printer := ui.New(os.Stdout)
	deps, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, deps, 2)

	assert.True(t, strings.HasSuffix(h.Skills[0], "/content"))
	assert.True(t, strings.HasSuffix(h.Skills[1], "/content"))
}

func TestResolveFromLock_TransitiveDeps(t *testing.T) {
	skillContent := []byte("transitive skill")
	skillHash := fetch.ComputeSHA256(skillContent)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/skills/transitive", skillContent))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{Field: "skills[https://example.com/main:dep0]", URL: "https://example.com/skills/transitive", SHA256: skillHash},
		},
	}

	h := &harness.Harness{
		Agent:  "agents/code.md",
		Skills: []string{},
	}

	printer := ui.New(os.Stdout)
	deps, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, deps, 1)

	// Transitive deps are appended as new skill entries.
	require.Len(t, h.Skills, 1)
	assert.True(t, strings.HasSuffix(h.Skills[0], "/content"))
}

func TestResolveFromLock_DiamondDependency(t *testing.T) {
	// Same URL appears as transitive dep in the lock but also as a direct
	// skill URL in the harness. The direct URL should be filtered out
	// (the transitive dep covers it).
	sharedContent := []byte("shared skill content")
	sharedHash := fetch.ComputeSHA256(sharedContent)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/skills/shared.md", sharedContent))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{Field: "skills[https://example.com/parent:dep0]", URL: "https://example.com/skills/shared.md", SHA256: sharedHash},
		},
	}

	h := &harness.Harness{
		Agent: "agents/code.md",
		Skills: []string{
			"https://example.com/skills/shared.md#sha256=" + sharedHash,
		},
	}

	printer := ui.New(os.Stdout)
	deps, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, deps, 1)

	// The direct URL reference should be filtered out.
	// Only the transitive dep (appended) should remain.
	require.Len(t, h.Skills, 1)
	assert.True(t, strings.HasSuffix(h.Skills[0], "/content"))
}

func TestResolveFromLock_TransitivePolicySkipped(t *testing.T) {
	policyContent := []byte("transitive policy content")
	policyHash := fetch.ComputeSHA256(policyContent)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/policies/transitive.yaml", policyContent))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{Field: "policy[https://example.com/skills/main]", URL: "https://example.com/policies/transitive.yaml", SHA256: policyHash},
		},
	}

	h := &harness.Harness{
		Agent:  "agents/code.md",
		Skills: []string{},
	}

	printer := ui.New(os.Stdout)
	deps, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, deps, 1)

	// Transitive policy should NOT be appended to skills.
	assert.Empty(t, h.Skills)
	// Policy field should remain unchanged (transitive policies are leaf nodes).
	assert.Empty(t, h.Policy)
}

func TestResolveFromLock_NoPartialMutation(t *testing.T) {
	// First dep is in cache, second is not. Harness should remain unchanged.
	agentContent := []byte("agent content")
	agentHash := fetch.ComputeSHA256(agentContent)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/agents/code.md", agentContent))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{Field: "agent", URL: "https://example.com/agents/code.md", SHA256: agentHash},
			{Field: "policy", URL: "https://example.com/policies/ro.yaml", SHA256: "0000000000000000000000000000000000000000000000000000000000000000"},
		},
	}

	originalAgent := "https://example.com/agents/code.md#sha256=" + agentHash
	h := &harness.Harness{
		Agent:  originalAgent,
		Policy: "https://example.com/policies/ro.yaml#sha256=0000000000000000000000000000000000000000000000000000000000000000",
	}

	printer := ui.New(os.Stdout)
	_, err := resolveFromLock(h, entry, root, printer)
	require.Error(t, err)

	// Harness should be unchanged — no partial mutations.
	assert.Equal(t, originalAgent, h.Agent)
}
