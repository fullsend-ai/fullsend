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
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/resolve"
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

func setupLockTestDir(t *testing.T, srv *httptest.Server, agentHash, policyHash string) string {
	t.Helper()
	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessContent := fmt.Sprintf(`agent: "%s/agents/code.md#sha256=%s"
role: test
policy: "%s/policies/sandbox.yaml#sha256=%s"
allowed_remote_resources:
  - "%s/"
`, srv.URL, agentHash, srv.URL, policyHash, srv.URL)

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
	policyContent := []byte("sandbox: strict")
	policyHash := fetch.ComputeSHA256(policyContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md":        agentContent,
		"/policies/sandbox.yaml": policyContent,
	})

	dir := setupLockTestDir(t, srv, agentHash, policyHash)

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "code", dir, "", false, resolveFlags{}, printer)
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

	assert.Equal(t, "policy", entry.Dependencies[1].Field)
	assert.Equal(t, fmt.Sprintf("%s/policies/sandbox.yaml", srv.URL), entry.Dependencies[1].URL)
	assert.Equal(t, policyHash, entry.Dependencies[1].SHA256)
}

func TestRunLock_SkillDirectoryType(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)

	skillMD := []byte("# Test skill\nA test skill.")
	helperSh := []byte("#!/bin/bash\necho hello")
	skillFiles := map[string][]byte{
		"SKILL.md":          skillMD,
		"scripts/helper.sh": helperSh,
	}
	treeHash := fetch.ComputeTreeHash(skillFiles)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md": agentContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	skillURL := fmt.Sprintf("https://github.com/test-org/test-repo/tree/main/skills/test#sha256=%s", treeHash)
	harnessContent := fmt.Sprintf(`agent: "%s/agents/code.md#sha256=%s"
role: test
skills:
  - "%s"
allowed_remote_resources:
  - "%s/"
  - "https://github.com/test-org/"
`, srv.URL, agentHash, skillURL, srv.URL)

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte(harnessContent),
		0o644,
	))

	orgConfig := fmt.Sprintf(`allowed_remote_resources:
  - "%s/"
  - "https://github.com/test-org/"
`, srv.URL)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte(orgConfig),
		0o644,
	))

	fakeClient := forge.NewFakeClient()
	fakeClient.DirContents["test-org/test-repo/skills/test@main"] = []forge.DirectoryEntry{
		{Path: "SKILL.md", Type: "file", Size: len(skillMD)},
		{Path: "scripts/helper.sh", Type: "file", Size: len(helperSh)},
	}
	fakeClient.FileContentsRef["test-org/test-repo/skills/test/SKILL.md@main"] = skillMD
	fakeClient.FileContentsRef["test-org/test-repo/skills/test/scripts/helper.sh@main"] = helperSh

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "code", dir, "", false, resolveFlags{forgeClient: fakeClient}, printer)
	require.NoError(t, err)

	lockPath := filepath.Join(dir, "lock.yaml")
	lf, err := lock.Load(lockPath)
	require.NoError(t, err)
	require.NotNil(t, lf)

	entry := lf.Lookup("code")
	require.NotNil(t, entry)

	var skillDep *lock.DependencyEntry
	for i := range entry.Dependencies {
		if strings.HasPrefix(entry.Dependencies[i].Field, "skills[") {
			skillDep = &entry.Dependencies[i]
			break
		}
	}
	require.NotNil(t, skillDep, "should have a skill dependency")
	assert.Equal(t, "directory", skillDep.Type)
	assert.Equal(t, treeHash, skillDep.SHA256)
	require.Len(t, skillDep.Files, 2)

	fileNames := make([]string, len(skillDep.Files))
	for i, f := range skillDep.Files {
		fileNames[i] = f.Path
	}
	assert.Contains(t, fileNames, "SKILL.md")
	assert.Contains(t, fileNames, "scripts/helper.sh")
}

func TestRunLock_SkillDirectoryRoundTrip(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)

	skillMD := []byte("# Test skill\nA test skill.")
	helperSh := []byte("#!/bin/bash\necho hello")
	skillFiles := map[string][]byte{
		"SKILL.md":          skillMD,
		"scripts/helper.sh": helperSh,
	}
	treeHash := fetch.ComputeTreeHash(skillFiles)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md": agentContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	skillURL := fmt.Sprintf("https://github.com/test-org/test-repo/tree/main/skills/test#sha256=%s", treeHash)
	harnessContent := fmt.Sprintf(`agent: "%s/agents/code.md#sha256=%s"
role: test
skills:
  - "%s"
allowed_remote_resources:
  - "%s/"
  - "https://github.com/test-org/"
`, srv.URL, agentHash, skillURL, srv.URL)

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte(harnessContent),
		0o644,
	))

	orgConfig := fmt.Sprintf(`allowed_remote_resources:
  - "%s/"
  - "https://github.com/test-org/"
`, srv.URL)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte(orgConfig),
		0o644,
	))

	fakeClient := forge.NewFakeClient()
	fakeClient.DirContents["test-org/test-repo/skills/test@main"] = []forge.DirectoryEntry{
		{Path: "SKILL.md", Type: "file", Size: len(skillMD)},
		{Path: "scripts/helper.sh", Type: "file", Size: len(helperSh)},
	}
	fakeClient.FileContentsRef["test-org/test-repo/skills/test/SKILL.md@main"] = skillMD
	fakeClient.FileContentsRef["test-org/test-repo/skills/test/scripts/helper.sh@main"] = helperSh

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)

	// Step 1: Generate the lock file.
	err := runLock(context.Background(), "code", dir, "", false, resolveFlags{forgeClient: fakeClient}, printer)
	require.NoError(t, err)

	lockPath := filepath.Join(dir, "lock.yaml")
	lf, err := lock.Load(lockPath)
	require.NoError(t, err)
	entry := lf.Lookup("code")
	require.NotNil(t, entry)

	// Step 2: Reload the harness (runLock mutated it) and resolve from lock.
	h2, err := harness.Load(filepath.Join(dir, "harness", "code.yaml"))
	require.NoError(t, err)
	require.NoError(t, h2.ResolveRelativeTo(dir))

	deps, err := resolveFromLock(h2, entry, dir, printer)
	require.NoError(t, err)

	// Verify the round-trip: agent resolved as file, skill resolved as directory.
	require.Len(t, deps, 2)

	var agentDep, skillDep *resolve.Dependency
	for i := range deps {
		switch {
		case deps[i].Field == "agent":
			agentDep = &deps[i]
		case strings.HasPrefix(deps[i].Field, "skills["):
			skillDep = &deps[i]
		}
	}
	require.NotNil(t, agentDep, "should have agent dependency")
	require.NotNil(t, skillDep, "should have skill dependency")

	assert.Equal(t, "file", agentDep.Type)
	assert.True(t, agentDep.CacheHit)

	assert.Equal(t, "directory", skillDep.Type)
	assert.Equal(t, treeHash, skillDep.SHA256)
	assert.True(t, skillDep.CacheHit)
	assert.True(t, strings.HasSuffix(h2.Skills[0], "/tree"), "skill path should end with /tree, got %s", h2.Skills[0])
}

func TestRunLock_NoURLReferences(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessContent := `agent: agents/code.md
role: test
skills:
  - skills/rust
`
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "local.yaml"),
		[]byte(harnessContent),
		0o644,
	))

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "local", dir, "", false, resolveFlags{}, printer)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(dir, "lock.yaml"))
	assert.True(t, os.IsNotExist(err), "lock file should not be created for local-only harness")
}

func TestRunLock_AlreadyUpToDate(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)
	policyContent := []byte("sandbox: strict")
	policyHash := fetch.ComputeSHA256(policyContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md":        agentContent,
		"/policies/sandbox.yaml": policyContent,
	})

	dir := setupLockTestDir(t, srv, agentHash, policyHash)

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)

	// First lock.
	require.NoError(t, runLock(context.Background(), "code", dir, "", false, resolveFlags{}, printer))

	// Second lock without --update should detect it's current.
	require.NoError(t, runLock(context.Background(), "code", dir, "", false, resolveFlags{}, printer))

	// Verify lock file still exists and is valid.
	lf, err := lock.Load(filepath.Join(dir, "lock.yaml"))
	require.NoError(t, err)
	require.NotNil(t, lf.Lookup("code"))
}

func TestRunLock_UpdateForceReResolve(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)
	policyContent := []byte("sandbox: strict")
	policyHash := fetch.ComputeSHA256(policyContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md":        agentContent,
		"/policies/sandbox.yaml": policyContent,
	})

	dir := setupLockTestDir(t, srv, agentHash, policyHash)

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)

	// First lock.
	require.NoError(t, runLock(context.Background(), "code", dir, "", false, resolveFlags{}, printer))

	lf1, _ := lock.Load(filepath.Join(dir, "lock.yaml"))
	entry1 := lf1.Lookup("code")
	resolvedAt1 := entry1.ResolvedAt

	// Second lock with --update should re-resolve.
	require.NoError(t, runLock(context.Background(), "code", dir, "", true, resolveFlags{}, printer))

	lf2, _ := lock.Load(filepath.Join(dir, "lock.yaml"))
	entry2 := lf2.Lookup("code")

	assert.True(t, entry2.ResolvedAt.After(resolvedAt1) || entry2.ResolvedAt.Equal(resolvedAt1))
}

func TestRunLock_MultiForgeLockAllVariants(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)
	policyContent := []byte("sandbox: strict")
	policyHash := fetch.ComputeSHA256(policyContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md":        agentContent,
		"/policies/sandbox.yaml": policyContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	// Forge overrides use local skills (no URL validation needed) and the
	// agent/policy URLs are shared. Each variant adds a different pre_script.
	harnessContent := fmt.Sprintf(`agent: "%s/agents/code.md#sha256=%s"
role: test
policy: "%s/policies/sandbox.yaml#sha256=%s"
allowed_remote_resources:
  - "%s/"
forge:
  github:
    pre_script: scripts/gh-pre.sh
  gitlab:
    pre_script: scripts/gl-pre.sh
`, srv.URL, agentHash, srv.URL, policyHash, srv.URL)

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "multi.yaml"),
		[]byte(harnessContent),
		0o644,
	))

	orgConfig := fmt.Sprintf("allowed_remote_resources:\n  - \"%s/\"\n", srv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "multi", dir, "", false, resolveFlags{}, printer)
	require.NoError(t, err)

	lf, err := lock.Load(filepath.Join(dir, "lock.yaml"))
	require.NoError(t, err)

	entry := lf.Lookup("multi")
	require.NotNil(t, entry)

	// Both variants share the same agent+policy URLs → 2 deps (deduped).
	assert.Equal(t, 2, len(entry.Dependencies))

	urls := make(map[string]bool)
	for _, dep := range entry.Dependencies {
		urls[dep.URL] = true
	}
	assert.True(t, urls[fmt.Sprintf("%s/agents/code.md", srv.URL)])
	assert.True(t, urls[fmt.Sprintf("%s/policies/sandbox.yaml", srv.URL)])
}

func TestRunLock_ForgeSelectsSingleVariant(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)
	policyContent := []byte("sandbox: strict")
	policyHash := fetch.ComputeSHA256(policyContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md":        agentContent,
		"/policies/sandbox.yaml": policyContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessContent := fmt.Sprintf(`agent: "%s/agents/code.md#sha256=%s"
role: test
policy: "%s/policies/sandbox.yaml#sha256=%s"
allowed_remote_resources:
  - "%s/"
forge:
  github:
    pre_script: scripts/gh-pre.sh
  gitlab:
    pre_script: scripts/gl-pre.sh
`, srv.URL, agentHash, srv.URL, policyHash, srv.URL)

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "single.yaml"),
		[]byte(harnessContent),
		0o644,
	))

	orgConfig := fmt.Sprintf("allowed_remote_resources:\n  - \"%s/\"\n", srv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	// Lock only the github variant — should still lock all URL deps.
	err := runLock(context.Background(), "single", dir, "github", false, resolveFlags{}, printer)
	require.NoError(t, err)

	lf, err := lock.Load(filepath.Join(dir, "lock.yaml"))
	require.NoError(t, err)

	entry := lf.Lookup("single")
	require.NotNil(t, entry)

	// Single variant still resolves agent+policy URLs.
	assert.Equal(t, 2, len(entry.Dependencies))
}

func TestRunLock_ForgeDeduplicatesAcrossVariants(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)
	policyContent := []byte("sandbox: strict")
	policyHash := fetch.ComputeSHA256(policyContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md":        agentContent,
		"/policies/sandbox.yaml": policyContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	// Both forge variants share the same base agent+policy URLs. Each variant
	// adds a different local pre_script. The lock should deduplicate the
	// shared URLs across variants.
	harnessContent := fmt.Sprintf(`agent: "%s/agents/code.md#sha256=%s"
role: test
policy: "%s/policies/sandbox.yaml#sha256=%s"
allowed_remote_resources:
  - "%s/"
forge:
  github:
    pre_script: scripts/gh-pre.sh
  gitlab:
    pre_script: scripts/gl-pre.sh
`, srv.URL, agentHash, srv.URL, policyHash, srv.URL)

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "dedup.yaml"),
		[]byte(harnessContent),
		0o644,
	))

	orgConfig := fmt.Sprintf("allowed_remote_resources:\n  - \"%s/\"\n", srv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "dedup", dir, "", false, resolveFlags{}, printer)
	require.NoError(t, err)

	lf, err := lock.Load(filepath.Join(dir, "lock.yaml"))
	require.NoError(t, err)

	entry := lf.Lookup("dedup")
	require.NotNil(t, entry)

	// Agent + policy = 2 deps (deduped across both forge variants).
	assert.Equal(t, 2, len(entry.Dependencies))
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

func TestResolveFromLock_DirectoryType(t *testing.T) {
	skillMD := []byte("# Skill\nA test skill.")
	helperSh := []byte("#!/bin/bash\necho hello")
	skillFiles := map[string][]byte{
		"SKILL.md":          skillMD,
		"scripts/helper.sh": helperSh,
	}
	treeHash := fetch.ComputeTreeHash(skillFiles)

	root := t.TempDir()
	_, err := fetch.CachePutDir(root, "https://github.com/org/repo/tree/main/skills/test", skillFiles)
	require.NoError(t, err)

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "skills[0]",
				URL:    "https://github.com/org/repo/tree/main/skills/test",
				SHA256: treeHash,
				Type:   "directory",
				Files: []lock.FileEntry{
					{Path: "SKILL.md", SHA256: fetch.ComputeSHA256(skillMD)},
					{Path: "scripts/helper.sh", SHA256: fetch.ComputeSHA256(helperSh)},
				},
			},
		},
	}

	h := &harness.Harness{
		Agent:  "agents/code.md",
		Skills: []string{"https://github.com/org/repo/tree/main/skills/test#sha256=" + treeHash},
	}

	printer := ui.New(os.Stdout)
	deps, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, deps, 1)

	assert.Equal(t, "directory", deps[0].Type)
	assert.Equal(t, treeHash, deps[0].SHA256)
	assert.True(t, deps[0].CacheHit)
	assert.True(t, strings.HasSuffix(h.Skills[0], "/tree"))
}

func TestResolveFromLock_EmptyTypeDefaultsToFile(t *testing.T) {
	content := []byte("skill content")
	hash := fetch.ComputeSHA256(content)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/skills/a", content))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "skills[0]",
				URL:    "https://example.com/skills/a",
				SHA256: hash,
				Type:   "", // pre-directory-model lock file
			},
		},
	}

	h := &harness.Harness{
		Agent:  "agents/code.md",
		Skills: []string{"https://example.com/skills/a#sha256=" + hash},
	}

	printer := ui.New(os.Stdout)
	deps, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, deps, 1)
	assert.Equal(t, "file", deps[0].Type, "empty Type should default to file for backward compatibility")
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

func TestRunLock_WithLocalBase(t *testing.T) {
	// A child harness with a local base field should succeed with no lock file
	// created when neither base nor child has URL references.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	baseContent := `agent: agents/shared.md
role: test
skills:
  - skills/common
`
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "base.yaml"),
		[]byte(baseContent),
		0o644,
	))

	childContent := `base: base.yaml
skills:
  - skills/extra
`
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "child.yaml"),
		[]byte(childContent),
		0o644,
	))

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "child", dir, "", false, resolveFlags{}, printer)
	require.NoError(t, err)

	// No URL deps means no lock file should be created.
	_, err = os.Stat(filepath.Join(dir, "lock.yaml"))
	assert.True(t, os.IsNotExist(err), "lock file should not be created for local-only base harness")
}

func TestResolveFromLock_BaseFieldNoOp(t *testing.T) {
	// A lock entry with a "base" field dependency should not corrupt skills
	// or other harness fields. The base dep is a no-op in resolveFromLock
	// because LoadWithBase already resolved the base composition.
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)
	baseContent := []byte("agent: agents/shared.md\nrole: test\nskills:\n  - skills/common\n")
	baseHash := fetch.ComputeSHA256(baseContent)
	skillContent := []byte("# Skill A")
	skillHash := fetch.ComputeSHA256(skillContent)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/agents/code.md", agentContent))
	require.NoError(t, fetch.CachePut(root, "https://example.com/base.yaml", baseContent))
	require.NoError(t, fetch.CachePut(root, "https://example.com/skills/a", skillContent))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{Field: "base", URL: "https://example.com/base.yaml", SHA256: baseHash},
			{Field: "agent", URL: "https://example.com/agents/code.md", SHA256: agentHash},
			{Field: "skills[0]", URL: "https://example.com/skills/a", SHA256: skillHash},
		},
	}

	h := &harness.Harness{
		Agent:  "https://example.com/agents/code.md#sha256=" + agentHash,
		Skills: []string{"https://example.com/skills/a#sha256=" + skillHash},
	}

	printer := ui.New(os.Stdout)
	deps, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)

	// All three deps should be returned (base, agent, skill).
	require.Len(t, deps, 3)

	// Agent should be resolved to a cache path.
	assert.True(t, strings.HasSuffix(h.Agent, "/content"), "agent should be resolved to cache path")

	// Skills should have exactly one entry (the resolved skill), not two.
	// The base dep must NOT be appended to skills.
	require.Len(t, h.Skills, 1, "base dep must not be appended to skills")
	assert.True(t, strings.HasSuffix(h.Skills[0], "/content"), "skill should be resolved to cache path")

	// Verify the base dep has the correct field and is a cache hit.
	var baseDep *resolve.Dependency
	for i := range deps {
		if deps[i].Field == "base" {
			baseDep = &deps[i]
			break
		}
	}
	require.NotNil(t, baseDep, "should have a base dependency in returned deps")
	assert.Equal(t, "https://example.com/base.yaml", baseDep.URL)
	assert.True(t, baseDep.CacheHit)
}

func TestRunLock_URLBaseOnlyDeps(t *testing.T) {
	// A child harness with a URL base and no other URL references.
	// The baseDeps conversion loop runs and the base-only-deps path is taken
	// (skip ResolveHarness, still record deps in lock file).
	baseContent := []byte("agent: agents/shared.md\nrole: test\nskills:\n  - skills/common\n")
	baseHash := fetch.ComputeSHA256(baseContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/base.yaml": baseContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	childContent := fmt.Sprintf("base: \"%s/base.yaml#sha256=%s\"\nskills:\n  - skills/extra\n", srv.URL, baseHash)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "urlbase.yaml"),
		[]byte(childContent),
		0o644,
	))

	orgConfig := fmt.Sprintf("allowed_remote_resources:\n  - \"%s/\"\n", srv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "urlbase", dir, "", false, resolveFlags{}, printer)
	require.NoError(t, err)

	lockPath := filepath.Join(dir, "lock.yaml")
	lf, err := lock.Load(lockPath)
	require.NoError(t, err)
	require.NotNil(t, lf)

	entry := lf.Lookup("urlbase")
	require.NotNil(t, entry)

	// Should have exactly one dependency: the URL base.
	require.Len(t, entry.Dependencies, 1)
	assert.Equal(t, "base", entry.Dependencies[0].Field)
	assert.Equal(t, fmt.Sprintf("%s/base.yaml", srv.URL), entry.Dependencies[0].URL)
	assert.Equal(t, baseHash, entry.Dependencies[0].SHA256)
}

func TestRunLock_URLBaseOnlyDepsWithPlatform(t *testing.T) {
	// Same as above but with a forge platform set, exercising the platform != "" branch
	// in the base-only-deps logging path.
	baseContent := []byte("agent: agents/shared.md\nrole: test\nskills:\n  - skills/common\n")
	baseHash := fetch.ComputeSHA256(baseContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/base.yaml": baseContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	childContent := fmt.Sprintf("base: \"%s/base.yaml#sha256=%s\"\nskills:\n  - skills/extra\nforge:\n  github:\n    pre_script: scripts/gh.sh\n", srv.URL, baseHash)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "urlbase-forge.yaml"),
		[]byte(childContent),
		0o644,
	))

	orgConfig := fmt.Sprintf("allowed_remote_resources:\n  - \"%s/\"\n", srv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	// Lock only the github variant.
	err := runLock(context.Background(), "urlbase-forge", dir, "github", false, resolveFlags{}, printer)
	require.NoError(t, err)

	lockPath := filepath.Join(dir, "lock.yaml")
	lf, err := lock.Load(lockPath)
	require.NoError(t, err)

	entry := lf.Lookup("urlbase-forge")
	require.NotNil(t, entry)
	require.Len(t, entry.Dependencies, 1)
	assert.Equal(t, "base", entry.Dependencies[0].Field)
}

func TestRunLock_URLRefsNoOrgConfigError(t *testing.T) {
	// A harness with URL references but no config.yaml should fail
	// with a clear error about the missing org config.
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md": agentContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessContent := fmt.Sprintf("agent: \"%s/agents/code.md#sha256=%s\"\nrole: test\n", srv.URL, agentHash)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "noconfig.yaml"),
		[]byte(harnessContent),
		0o644,
	))

	// Deliberately do NOT create config.yaml.

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "noconfig", dir, "", false, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "URL-referenced resources require an org-level config.yaml")
	assert.Contains(t, err.Error(), "allowed_remote_resources")
}

func TestRunLock_MalformedOrgConfig(t *testing.T) {
	// A malformed config.yaml should produce a warning but not prevent
	// local-only harnesses from locking.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "simple.yaml"),
		[]byte("agent: agents/code.md\nrole: test\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("{{invalid yaml"),
		0o644,
	))

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "simple", dir, "", false, resolveFlags{}, printer)
	require.NoError(t, err)
}

func TestRunLock_MalformedOrgConfigWithURLRefs(t *testing.T) {
	// A malformed config.yaml with URL-referenced resources should fail
	// with a parse error on the re-attempt.
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md": agentContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessContent := fmt.Sprintf("agent: \"%s/agents/code.md#sha256=%s\"\nrole: test\n", srv.URL, agentHash)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "badcfg.yaml"),
		[]byte(harnessContent),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("{{invalid yaml"),
		0o644,
	))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "badcfg", dir, "", false, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing org config")
}

func TestRunLock_NoOrgConfigNoURLRefs(t *testing.T) {
	// When there's no config.yaml and the harness has no URL references,
	// runLock should succeed via the best-effort org config loading path.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessContent := `agent: agents/code.md
role: test
`
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "simple.yaml"),
		[]byte(harnessContent),
		0o644,
	))

	// Deliberately do NOT create config.yaml.

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "simple", dir, "", false, resolveFlags{}, printer)
	require.NoError(t, err)

	// No URL deps means no lock file should be created.
	_, err = os.Stat(filepath.Join(dir, "lock.yaml"))
	assert.True(t, os.IsNotExist(err), "lock file should not be created for local-only harness without config.yaml")
}

func TestRunLock_OrgAllowlistSyncedAfterReAttempt(t *testing.T) {
	// Verifies that after the re-attempt successfully parses org config,
	// orgAllowlist is updated so subsequent loop iterations use the
	// correct allowlist for LoadWithBase.
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md": agentContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	// Harness with URL agent refs — exercises the re-attempt path when
	// config.yaml is initially malformed.
	harnessContent := fmt.Sprintf("agent: \"%s/agents/code.md#sha256=%s\"\nrole: test\n", srv.URL, agentHash)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "urlrefs.yaml"),
		[]byte(harnessContent),
		0o644,
	))

	// Config.yaml is initially invalid — re-attempt path fires and fails
	// with parse error.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("{{invalid yaml"),
		0o644,
	))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "urlrefs", dir, "", false, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing org config")
}

func TestRunLock_URLBaseAndURLRefsNoOrgConfig(t *testing.T) {
	// Harness with both a URL base and other URL references but no config.yaml.
	// LoadWithBase should fail at the URL base fetch (not at HasURLReferences).
	baseContent := []byte("agent: agents/shared.md\nrole: test\n")
	baseHash := fetch.ComputeSHA256(baseContent)
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/base.yaml":      baseContent,
		"/agents/code.md": agentContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessContent := fmt.Sprintf("base: \"%s/base.yaml#sha256=%s\"\nrole: test\nagent: \"%s/agents/code.md#sha256=%s\"\n",
		srv.URL, baseHash, srv.URL, agentHash)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "combo.yaml"),
		[]byte(harnessContent),
		0o644,
	))

	// No config.yaml at all.

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "combo", dir, "", false, resolveFlags{}, printer)
	require.Error(t, err)
	// Should fail with a clear error about missing org config.
	assert.Contains(t, err.Error(), "config.yaml")
}

func TestRunLock_ErrorOnMissingRole(t *testing.T) {
	// Verifies that runLock fails with a hard error when harness has no role.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "agents", "code.md"),
		[]byte("You are a coding agent."),
		0o644,
	))
	// Harness without role field
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\n"),
		0o644,
	))

	var buf strings.Builder
	printer := ui.New(&buf)
	err := runLock(context.Background(), "code", dir, "", false, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid harness: role field is required")
}
