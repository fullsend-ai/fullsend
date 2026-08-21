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
	"github.com/fullsend-ai/fullsend/internal/gitfetch"
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

	fakeFetcher := func(_ context.Context, _, _, _, _ string) (map[string][]byte, error) {
		return skillFiles, nil
	}

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "code", dir, "", false, resolveFlags{treeFetcher: gitfetch.TreeFetchFunc(fakeFetcher)}, printer)
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

	fakeFetcher := func(_ context.Context, _, _, _, _ string) (map[string][]byte, error) {
		return skillFiles, nil
	}

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)

	// Step 1: Generate the lock file.
	err := runLock(context.Background(), "code", dir, "", false, resolveFlags{treeFetcher: gitfetch.TreeFetchFunc(fakeFetcher)}, printer)
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

	lockResult, err := resolveFromLock(h2, entry, dir, printer)
	require.NoError(t, err)

	// Verify the round-trip: agent resolved as file, skill resolved as directory.
	require.Len(t, lockResult.Deps, 2)

	var agentDep, skillDep *resolve.Dependency
	for i := range lockResult.Deps {
		switch {
		case lockResult.Deps[i].Field == "agent":
			agentDep = &lockResult.Deps[i]
		case strings.HasPrefix(lockResult.Deps[i].Field, "skills["):
			skillDep = &lockResult.Deps[i]
		}
	}
	require.NotNil(t, agentDep, "should have agent dependency")
	require.NotNil(t, skillDep, "should have skill dependency")

	assert.Equal(t, "file", agentDep.Type)
	assert.True(t, agentDep.CacheHit)

	assert.Equal(t, "directory", skillDep.Type)
	assert.Equal(t, treeHash, skillDep.SHA256)
	assert.True(t, skillDep.CacheHit)
	assert.Equal(t, "test", filepath.Base(h2.Skills[0].Source), "skill path basename should be the skill directory name")
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
		Agent:                  "https://example.com/agents/code.md#sha256=" + agentHash,
		AllowedRemoteResources: []string{"https://example.com/", "https://github.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 1)

	assert.Equal(t, "agent", lockResult.Deps[0].Field)
	assert.Equal(t, agentHash, lockResult.Deps[0].SHA256)
	assert.True(t, lockResult.Deps[0].CacheHit)
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
		Agent:                  "https://example.com/agents/code.md#sha256=0000000000000000000000000000000000000000000000000000000000000000",
		AllowedRemoteResources: []string{"https://example.com/", "https://github.com/"},
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
		Agent:                  "agents/code.md",
		Skills:                 []harness.SkillEntry{{Source: "https://example.com/skills/a#sha256=" + hashA}, {Source: "https://example.com/skills/b#sha256=" + hashB}},
		AllowedRemoteResources: []string{"https://example.com/", "https://github.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 2)

	assert.True(t, strings.HasSuffix(h.Skills[0].Source, "/content"))
	assert.True(t, strings.HasSuffix(h.Skills[1].Source, "/content"))
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
		Agent:                  "agents/code.md",
		Skills:                 []harness.SkillEntry{},
		AllowedRemoteResources: []string{"https://example.com/", "https://github.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 1)

	// Transitive deps are appended as new skill entries.
	require.Len(t, h.Skills, 1)
	assert.True(t, strings.HasSuffix(h.Skills[0].Source, "/content"))
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
		Skills: []harness.SkillEntry{
			{Source: "https://example.com/skills/shared.md#sha256=" + sharedHash},
		},
		AllowedRemoteResources: []string{"https://example.com/", "https://github.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 1)

	// The direct URL reference should be filtered out.
	// Only the transitive dep (appended) should remain.
	require.Len(t, h.Skills, 1)
	assert.True(t, strings.HasSuffix(h.Skills[0].Source, "/content"))
}

func TestResolveFromLock_OverrideSlots(t *testing.T) {
	overrideContent := []byte("custom security agent")
	overrideHash := fetch.ComputeSHA256(overrideContent)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/overrides/security.md", overrideContent))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "skills[0].overrides[sub-agents/security.md]",
				URL:    "https://example.com/overrides/security.md",
				SHA256: overrideHash,
			},
		},
	}

	h := &harness.Harness{
		Agent:                  "agents/code.md",
		Skills:                 []harness.SkillEntry{{Source: "skills/pr-review"}},
		AllowedRemoteResources: []string{"https://example.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 1)

	require.NotNil(t, h.Skills[0].Overrides)
	require.Contains(t, h.Skills[0].Overrides, "sub-agents/security.md")
	assert.True(t, strings.HasSuffix(*h.Skills[0].Overrides["sub-agents/security.md"], "/content"))
}

func TestResolveFromLock_OverrideSlotsPreservesExisting(t *testing.T) {
	overrideA := []byte("override A")
	hashA := fetch.ComputeSHA256(overrideA)
	overrideB := []byte("override B")
	hashB := fetch.ComputeSHA256(overrideB)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/overrides/a.md", overrideA))
	require.NoError(t, fetch.CachePut(root, "https://example.com/overrides/b.md", overrideB))

	existing := "local/existing.md"
	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{Field: "skills[0].overrides[sub-agents/a.md]", URL: "https://example.com/overrides/a.md", SHA256: hashA},
			{Field: "skills[0].overrides[sub-agents/b.md]", URL: "https://example.com/overrides/b.md", SHA256: hashB},
		},
	}

	h := &harness.Harness{
		Agent: "agents/code.md",
		Skills: []harness.SkillEntry{{
			Source: "skills/pr-review",
			Overrides: map[string]*string{
				"sub-agents/local.md": &existing,
			},
		}},
		AllowedRemoteResources: []string{"https://example.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 2)

	require.Len(t, h.Skills[0].Overrides, 3)
	assert.Equal(t, "local/existing.md", *h.Skills[0].Overrides["sub-agents/local.md"])
	assert.True(t, strings.HasSuffix(*h.Skills[0].Overrides["sub-agents/a.md"], "/content"))
	assert.True(t, strings.HasSuffix(*h.Skills[0].Overrides["sub-agents/b.md"], "/content"))
}

func TestResolveFromLock_OverrideDoesNotCorruptSource(t *testing.T) {
	overrideContent := []byte("override content")
	overrideHash := fetch.ComputeSHA256(overrideContent)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/overrides/x.md", overrideContent))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{Field: "skills[0].overrides[sub-agents/x.md]", URL: "https://example.com/overrides/x.md", SHA256: overrideHash},
		},
	}

	h := &harness.Harness{
		Agent:                  "agents/code.md",
		Skills:                 []harness.SkillEntry{{Source: "skills/pr-review"}},
		AllowedRemoteResources: []string{"https://example.com/"},
	}

	printer := ui.New(os.Stdout)
	_, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)

	assert.Equal(t, "skills/pr-review", h.Skills[0].Source)
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
		Agent:                  "agents/code.md",
		Skills:                 []harness.SkillEntry{{Source: "https://github.com/org/repo/tree/main/skills/test#sha256=" + treeHash}},
		AllowedRemoteResources: []string{"https://example.com/", "https://github.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 1)

	assert.Equal(t, "directory", lockResult.Deps[0].Type)
	assert.Equal(t, treeHash, lockResult.Deps[0].SHA256)
	assert.True(t, lockResult.Deps[0].CacheHit)
	assert.Equal(t, "test", filepath.Base(h.Skills[0].Source), "skill basename must be the real skill name, not 'tree'")
}

func TestResolveFromLock_DirectoryTypeScript(t *testing.T) {
	scriptContent := []byte("#!/bin/bash\necho running")
	helperContent := []byte("#!/bin/bash\necho helper")
	files := map[string][]byte{
		"pre-code.sh": scriptContent,
		"helper.sh":   helperContent,
	}
	treeHash := fetch.ComputeTreeHash(files)

	root := t.TempDir()
	_, err := fetch.CachePutDir(root, "https://raw.githubusercontent.com/org/repo/main/scripts", files)
	require.NoError(t, err)

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "pre_script",
				URL:    "https://raw.githubusercontent.com/org/repo/main/scripts/pre-code.sh",
				SHA256: treeHash,
				Type:   "directory",
				Files: []lock.FileEntry{
					{Path: "pre-code.sh", SHA256: fetch.ComputeSHA256(scriptContent)},
					{Path: "helper.sh", SHA256: fetch.ComputeSHA256(helperContent)},
				},
			},
		},
	}

	h := &harness.Harness{
		AllowedRemoteResources: []string{"https://raw.githubusercontent.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 1)

	assert.Equal(t, "directory", lockResult.Deps[0].Type)
	assert.Equal(t, treeHash, lockResult.Deps[0].SHA256)
	assert.True(t, lockResult.Deps[0].CacheHit)

	// The harness field must point to the specific script file, not the tree root.
	assert.True(t, strings.HasSuffix(h.PreScript, "/pre-code.sh"),
		"expected PreScript to end with /pre-code.sh, got %s", h.PreScript)

	// The script file must be executable.
	info, err := os.Stat(h.PreScript)
	require.NoError(t, err)
	assert.True(t, info.Mode()&0o111 != 0, "script should be executable")
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
		Agent:                  "agents/code.md",
		Skills:                 []harness.SkillEntry{{Source: "https://example.com/skills/a#sha256=" + hash}},
		AllowedRemoteResources: []string{"https://example.com/", "https://github.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 1)
	assert.Equal(t, "file", lockResult.Deps[0].Type, "empty Type should default to file for backward compatibility")
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
		Agent:                  "agents/code.md",
		Skills:                 []harness.SkillEntry{},
		AllowedRemoteResources: []string{"https://example.com/", "https://github.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 1)

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
		Agent:                  originalAgent,
		Policy:                 "https://example.com/policies/ro.yaml#sha256=0000000000000000000000000000000000000000000000000000000000000000",
		AllowedRemoteResources: []string{"https://example.com/", "https://github.com/"},
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
		Agent:                  "https://example.com/agents/code.md#sha256=" + agentHash,
		Skills:                 []harness.SkillEntry{{Source: "https://example.com/skills/a#sha256=" + skillHash}},
		AllowedRemoteResources: []string{"https://example.com/", "https://github.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)

	// All three deps should be returned (base, agent, skill).
	require.Len(t, lockResult.Deps, 3)

	// Agent should be resolved to a cache path.
	assert.True(t, strings.HasSuffix(h.Agent, "/content"), "agent should be resolved to cache path")

	// Skills should have exactly one entry (the resolved skill), not two.
	// The base dep must NOT be appended to skills.
	require.Len(t, h.Skills, 1, "base dep must not be appended to skills")
	assert.True(t, strings.HasSuffix(h.Skills[0].Source, "/content"), "skill should be resolved to cache path")

	// Verify the base dep has the correct field and is a cache hit.
	var baseDep *resolve.Dependency
	for i := range lockResult.Deps {
		if lockResult.Deps[i].Field == "base" {
			baseDep = &lockResult.Deps[i]
			break
		}
	}
	require.NotNil(t, baseDep, "should have a base dependency in returned deps")
	assert.Equal(t, "https://example.com/base.yaml", baseDep.URL)
	assert.True(t, baseDep.CacheHit)
}

func TestResolveFromLock_AgentSourceNoOp(t *testing.T) {
	// A lock entry with an "agent_source" field dependency should not corrupt
	// skills or other harness fields. The agent_source dep is informational —
	// the harness is already loaded from the resolved path.
	//
	// The agent_source URL deliberately uses a different domain
	// (org-registry.example.com) that is NOT in the harness's own
	// AllowedRemoteResources. Agent source URLs are validated against the
	// org-level allowlist during lock creation, so resolveFromLock must
	// skip the harness-level allowlist check for agent_source entries.
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)
	harnessSource := []byte("agent: agents/code.md\nrole: test\n")
	harnessSourceHash := fetch.ComputeSHA256(harnessSource)
	skillContent := []byte("# Skill A")
	skillHash := fetch.ComputeSHA256(skillContent)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/agents/code.md", agentContent))
	require.NoError(t, fetch.CachePut(root, "https://org-registry.example.com/harness/code.yaml", harnessSource))
	require.NoError(t, fetch.CachePut(root, "https://example.com/skills/a", skillContent))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{Field: "agent_source", URL: "https://org-registry.example.com/harness/code.yaml", SHA256: harnessSourceHash},
			{Field: "agent", URL: "https://example.com/agents/code.md", SHA256: agentHash},
			{Field: "skills[0]", URL: "https://example.com/skills/a", SHA256: skillHash},
		},
	}

	h := &harness.Harness{
		Agent:                  "https://example.com/agents/code.md#sha256=" + agentHash,
		Skills:                 []harness.SkillEntry{{Source: "https://example.com/skills/a#sha256=" + skillHash}},
		AllowedRemoteResources: []string{"https://example.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)

	// All three deps should be returned.
	require.Len(t, lockResult.Deps, 3)

	// Skills should have exactly one entry — the agent_source dep must NOT
	// be appended to skills.
	require.Len(t, h.Skills, 1, "agent_source dep must not be appended to skills")
	assert.True(t, strings.HasSuffix(h.Skills[0].Source, "/content"), "skill should be resolved to cache path")
}

func TestResolveFromLock_ValidationLoopSchema(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)
	schemaContent := []byte(`{"type":"object"}`)
	schemaHash := fetch.ComputeSHA256(schemaContent)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/agents/code.md", agentContent))
	require.NoError(t, fetch.CachePut(root, "https://example.com/schemas/result.json", schemaContent))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{Field: "agent", URL: "https://example.com/agents/code.md", SHA256: agentHash},
			{Field: "validation_loop.schema", URL: "https://example.com/schemas/result.json", SHA256: schemaHash},
		},
	}

	h := &harness.Harness{
		Agent: "https://example.com/agents/code.md#sha256=" + agentHash,
		ValidationLoop: &harness.ValidationLoop{
			Script: "scripts/validate.sh",
			Schema: "https://example.com/schemas/result.json#sha256=" + schemaHash,
		},
		AllowedRemoteResources: []string{"https://example.com/", "https://github.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 2)

	assert.Equal(t, "validation_loop.schema", lockResult.Deps[1].Field)
	assert.True(t, lockResult.Deps[1].CacheHit)
	assert.True(t, strings.HasSuffix(h.ValidationLoop.Schema, "/content"))
}

func TestRunLock_URLBaseOnlyDeps(t *testing.T) {
	// A child harness with a URL base and no other URL references.
	// The baseDeps conversion loop runs and the base-only-deps path is taken
	// (skip ResolveHarness, still record deps in lock file).
	baseContent := []byte("agent: agents/shared.md\nrole: test\n")
	baseHash := fetch.ComputeSHA256(baseContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/base.yaml":        baseContent,
		"/agents/shared.md": []byte("# shared agent"),
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

	// Dependencies: base + agent resource
	require.Len(t, entry.Dependencies, 2)
	assert.Equal(t, "base", entry.Dependencies[0].Field)
	assert.Equal(t, fmt.Sprintf("%s/base.yaml", srv.URL), entry.Dependencies[0].URL)
	assert.Equal(t, baseHash, entry.Dependencies[0].SHA256)
	assert.Equal(t, "file", entry.Dependencies[0].Type)

	assert.Equal(t, "agent", entry.Dependencies[1].Field)
	assert.Equal(t, fmt.Sprintf("%s/agents/shared.md", srv.URL), entry.Dependencies[1].URL)
	assert.Equal(t, "resource", entry.Dependencies[1].Type)
}

func TestRunLock_URLBaseOnlyDepsWithPlatform(t *testing.T) {
	// Same as above but with a forge platform set, exercising the platform != "" branch
	// in the base-only-deps logging path.
	baseContent := []byte("agent: agents/shared.md\nrole: test\n")
	baseHash := fetch.ComputeSHA256(baseContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/base.yaml":        baseContent,
		"/agents/shared.md": []byte("# shared agent"),
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
	require.Len(t, entry.Dependencies, 2)
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
	assert.Contains(t, err.Error(), "URL-referenced resources require a config.yaml")
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
	assert.Contains(t, err.Error(), "parsing config.yaml")
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
	assert.Contains(t, err.Error(), "parsing config.yaml")
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

func TestResolveFromLock_ProfileReconstruction(t *testing.T) {
	profileContent := []byte("id: anthropic\nname: Anthropic\n")
	profileHash := fetch.ComputeSHA256(profileContent)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/profiles/anthropic.yaml", profileContent))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "openshell.profiles[0]",
				URL:    "https://example.com/profiles/anthropic.yaml",
				SHA256: profileHash,
			},
		},
	}

	h := &harness.Harness{
		Agent: "agents/code.md",
		OpenShell: &harness.OpenShellConfig{
			Profiles: []string{"https://example.com/profiles/anthropic.yaml#sha256=" + profileHash},
		},
		AllowedRemoteResources: []string{"https://example.com/", "https://github.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 1)
	require.Len(t, lockResult.Profiles, 1)
	assert.Equal(t, "anthropic", lockResult.Profiles[0].ID)
	assert.True(t, lockResult.Deps[0].CacheHit)
	assert.True(t, strings.HasSuffix(lockResult.Profiles[0].LocalPath, ".yaml"),
		"profile LocalPath should end with .yaml for openshell compatibility, got %s",
		lockResult.Profiles[0].LocalPath)
	assert.Equal(t, "anthropic.yaml", filepath.Base(lockResult.Profiles[0].LocalPath),
		"profile LocalPath basename should be <id>.yaml")
	assert.Equal(t, lockResult.Profiles[0].LocalPath, lockResult.Deps[0].LocalPath,
		"Dependency.LocalPath should match the renamed profile path, not the pre-rename cache path")

	// The named path must be a symlink (not a copy) so its relative "content"
	// target keeps resolving after the cache dir is bind-mounted into the sandbox.
	info, err := os.Lstat(lockResult.Profiles[0].LocalPath)
	require.NoError(t, err)
	assert.True(t, info.Mode()&os.ModeSymlink != 0,
		"profile LocalPath should be a symlink, got mode %s", info.Mode())

	// Verify the symlink target is readable and contains the profile content.
	got, err := os.ReadFile(lockResult.Profiles[0].LocalPath)
	require.NoError(t, err)
	assert.Equal(t, profileContent, got)
}

// TestResolveFromLock_ProfileSymlinkError covers the error branch in
// resolveFromLock when CacheNamedSymlink fails: the cache is populated but its
// directory is made unwritable, so naming the cached profile returns a wrapped
// error rather than a bare path.
func TestResolveFromLock_ProfileSymlinkError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory permission checks")
	}
	profileContent := []byte("id: anthropic\nname: Anthropic\n")
	profileHash := fetch.ComputeSHA256(profileContent)

	root := t.TempDir()
	url := "https://example.com/profiles/anthropic.yaml"
	require.NoError(t, fetch.CachePut(root, url, profileContent))

	cacheDir, err := fetch.CachePath(root, profileHash)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(cacheDir, 0o500)) // read+execute, no write
	t.Cleanup(func() { _ = os.Chmod(cacheDir, 0o700) })

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{Field: "openshell.profiles[0]", URL: url, SHA256: profileHash},
		},
	}
	h := &harness.Harness{
		Agent: "agents/code.md",
		OpenShell: &harness.OpenShellConfig{
			Profiles: []string{url + "#sha256=" + profileHash},
		},
		AllowedRemoteResources: []string{"https://example.com/"},
	}

	printer := ui.New(os.Stdout)
	_, err = resolveFromLock(h, entry, root, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "naming cached profile")
}

func TestResolveFromLock_ProfileEmptyID(t *testing.T) {
	profileContent := []byte("name: NoID\n")
	profileHash := fetch.ComputeSHA256(profileContent)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/profiles/noid.yaml", profileContent))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "openshell.profiles[0]",
				URL:    "https://example.com/profiles/noid.yaml",
				SHA256: profileHash,
			},
		},
	}

	h := &harness.Harness{
		Agent: "agents/code.md",
		OpenShell: &harness.OpenShellConfig{
			Profiles: []string{"https://example.com/profiles/noid.yaml#sha256=" + profileHash},
		},
		AllowedRemoteResources: []string{"https://example.com/", "https://github.com/"},
	}

	printer := ui.New(os.Stdout)
	_, err := resolveFromLock(h, entry, root, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no id")
}

func TestResolveFromLock_ProviderReconstruction(t *testing.T) {
	providerContent := []byte("name: my-provider\ntype: anthropic\ncredentials:\n  API_KEY: ${ANTHROPIC_KEY}\n")
	providerHash := fetch.ComputeSHA256(providerContent)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/providers/my.yaml", providerContent))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "providers[0]",
				URL:    "https://example.com/providers/my.yaml",
				SHA256: providerHash,
			},
		},
	}

	h := &harness.Harness{
		Agent:                  "agents/code.md",
		Providers:              []string{"https://example.com/providers/my.yaml#sha256=" + providerHash},
		AllowedRemoteResources: []string{"https://example.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Providers, 1)
	assert.Equal(t, "my-provider", lockResult.Providers[0].Def.Name)
	assert.Equal(t, "anthropic", lockResult.Providers[0].Def.Type)
	assert.Empty(t, lockResult.Deps[0].Warning)
}

func TestResolveFromLock_ProviderMissingName(t *testing.T) {
	providerContent := []byte("type: anthropic\n")
	providerHash := fetch.ComputeSHA256(providerContent)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/providers/noname.yaml", providerContent))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "providers[0]",
				URL:    "https://example.com/providers/noname.yaml",
				SHA256: providerHash,
			},
		},
	}

	h := &harness.Harness{
		Agent:                  "agents/code.md",
		Providers:              []string{"https://example.com/providers/noname.yaml#sha256=" + providerHash},
		AllowedRemoteResources: []string{"https://example.com/"},
	}

	printer := ui.New(os.Stdout)
	_, err := resolveFromLock(h, entry, root, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no name")
}

func TestResolveFromLock_ProviderMissingType(t *testing.T) {
	providerContent := []byte("name: my-provider\n")
	providerHash := fetch.ComputeSHA256(providerContent)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/providers/notype.yaml", providerContent))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "providers[0]",
				URL:    "https://example.com/providers/notype.yaml",
				SHA256: providerHash,
			},
		},
	}

	h := &harness.Harness{
		Agent:                  "agents/code.md",
		Providers:              []string{"https://example.com/providers/notype.yaml#sha256=" + providerHash},
		AllowedRemoteResources: []string{"https://example.com/"},
	}

	printer := ui.New(os.Stdout)
	_, err := resolveFromLock(h, entry, root, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no type")
}

func TestResolveFromLock_ProviderLiteralCredentialWarning(t *testing.T) {
	providerContent := []byte("name: my-provider\ntype: anthropic\ncredentials:\n  API_KEY: sk-hardcoded-secret\n")
	providerHash := fetch.ComputeSHA256(providerContent)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/providers/literal.yaml", providerContent))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "providers[0]",
				URL:    "https://example.com/providers/literal.yaml",
				SHA256: providerHash,
			},
		},
	}

	h := &harness.Harness{
		Agent:                  "agents/code.md",
		Providers:              []string{"https://example.com/providers/literal.yaml#sha256=" + providerHash},
		AllowedRemoteResources: []string{"https://example.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 1)
	assert.NotEmpty(t, lockResult.Deps[0].Warning)
	assert.Contains(t, lockResult.Deps[0].Warning, "API_KEY")
}

func TestResolveFromLock_RejectsDisallowedURL(t *testing.T) {
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
		Agent:                  "https://example.com/agents/code.md#sha256=" + agentHash,
		AllowedRemoteResources: []string{"https://other-domain.com/"},
	}

	printer := ui.New(os.Stdout)
	_, err := resolveFromLock(h, entry, root, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no longer in allowed_remote_resources")
	assert.Contains(t, err.Error(), "example.com")
}

func TestResolveFromLock_EmptyAllowlistDeniesURLs(t *testing.T) {
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
		Agent:                  "https://example.com/agents/code.md#sha256=" + agentHash,
		AllowedRemoteResources: nil,
	}

	printer := ui.New(os.Stdout)
	_, err := resolveFromLock(h, entry, root, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no longer in allowed_remote_resources")
}

func TestResolveFromLock_PluginMalformedFieldError(t *testing.T) {
	// A lock file with a malformed plugins[N] field (e.g. from a hand-edited
	// or merge-conflicted lock.yaml) should return an explicit error, not
	// silently drop the plugin.
	manifestJSON := []byte(`{"name": "gopls-lsp"}`)
	pluginFiles := map[string][]byte{"plugin.json": manifestJSON}
	treeHash := fetch.ComputeTreeHash(pluginFiles)

	root := t.TempDir()
	_, err := fetch.CachePutDir(root, "https://github.com/org/repo/tree/main/plugins/gopls-lsp", pluginFiles)
	require.NoError(t, err)

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "plugins[bad]", // malformed index
				URL:    "https://github.com/org/repo/tree/main/plugins/gopls-lsp",
				SHA256: treeHash,
				Type:   "directory",
				Files: []lock.FileEntry{
					{Path: "plugin.json", SHA256: fetch.ComputeSHA256(manifestJSON)},
				},
			},
		},
	}

	h := &harness.Harness{
		Agent:                  "agents/code.md",
		Plugins:                []string{"https://github.com/org/repo/tree/main/plugins/gopls-lsp#sha256=" + treeHash},
		AllowedRemoteResources: []string{"https://github.com/"},
	}

	printer := ui.New(os.Stdout)
	_, err = resolveFromLock(h, entry, root, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot parse plugin index")
}

func TestResolveFromLock_PluginOutOfRangeError(t *testing.T) {
	// A lock file with an out-of-range plugins[N] index should return an
	// explicit error, not silently drop the plugin.
	manifestJSON := []byte(`{"name": "gopls-lsp"}`)
	pluginFiles := map[string][]byte{"plugin.json": manifestJSON}
	treeHash := fetch.ComputeTreeHash(pluginFiles)

	root := t.TempDir()
	_, err := fetch.CachePutDir(root, "https://github.com/org/repo/tree/main/plugins/gopls-lsp", pluginFiles)
	require.NoError(t, err)

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "plugins[5]", // out of range (only 1 plugin in harness)
				URL:    "https://github.com/org/repo/tree/main/plugins/gopls-lsp",
				SHA256: treeHash,
				Type:   "directory",
				Files: []lock.FileEntry{
					{Path: "plugin.json", SHA256: fetch.ComputeSHA256(manifestJSON)},
				},
			},
		},
	}

	h := &harness.Harness{
		Agent:                  "agents/code.md",
		Plugins:                []string{"https://github.com/org/repo/tree/main/plugins/gopls-lsp#sha256=" + treeHash},
		AllowedRemoteResources: []string{"https://github.com/"},
	}

	printer := ui.New(os.Stdout)
	_, err = resolveFromLock(h, entry, root, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")
}

func TestResolveFromLock_PluginExecutablePermissions(t *testing.T) {
	// Plugin files resolved from the lock path should have executable
	// permissions (0755), not the cache default of 0600.
	initSh := []byte("#!/bin/bash\necho init")
	manifestJSON := []byte(`{"name": "exec-plugin"}`)
	pluginFiles := map[string][]byte{
		"plugin.json":     manifestJSON,
		"scripts/init.sh": initSh,
	}
	treeHash := fetch.ComputeTreeHash(pluginFiles)

	root := t.TempDir()
	_, err := fetch.CachePutDir(root, "https://github.com/org/repo/tree/main/plugins/exec-plugin", pluginFiles)
	require.NoError(t, err)

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "plugins[0]",
				URL:    "https://github.com/org/repo/tree/main/plugins/exec-plugin",
				SHA256: treeHash,
				Type:   "directory",
				Files: []lock.FileEntry{
					{Path: "plugin.json", SHA256: fetch.ComputeSHA256(manifestJSON)},
					{Path: "scripts/init.sh", SHA256: fetch.ComputeSHA256(initSh)},
				},
			},
		},
	}

	h := &harness.Harness{
		Agent:                  "agents/code.md",
		Plugins:                []string{"https://github.com/org/repo/tree/main/plugins/exec-plugin#sha256=" + treeHash},
		AllowedRemoteResources: []string{"https://github.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 1)

	// Verify plugin files have executable permissions.
	scriptPath := filepath.Join(h.Plugins[0], "scripts", "init.sh")
	info, statErr := os.Stat(scriptPath)
	require.NoError(t, statErr)
	assert.True(t, info.Mode()&0o100 != 0,
		"plugin script should have executable permission, got %s", info.Mode())
}

func TestResolveFromLock_PluginSlots(t *testing.T) {
	manifestJSON := []byte(`{"name": "gopls-lsp"}`)
	initSh := []byte("#!/bin/bash\necho init")
	pluginFiles := map[string][]byte{
		"plugin.json":     manifestJSON,
		"scripts/init.sh": initSh,
	}
	treeHash := fetch.ComputeTreeHash(pluginFiles)

	root := t.TempDir()
	_, err := fetch.CachePutDir(root, "https://github.com/org/repo/tree/main/plugins/gopls-lsp", pluginFiles)
	require.NoError(t, err)

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "plugins[0]",
				URL:    "https://github.com/org/repo/tree/main/plugins/gopls-lsp",
				SHA256: treeHash,
				Type:   "directory",
				Files: []lock.FileEntry{
					{Path: "plugin.json", SHA256: fetch.ComputeSHA256(manifestJSON)},
					{Path: "scripts/init.sh", SHA256: fetch.ComputeSHA256(initSh)},
				},
			},
		},
	}

	h := &harness.Harness{
		Agent:                  "agents/code.md",
		Plugins:                []string{"https://github.com/org/repo/tree/main/plugins/gopls-lsp#sha256=" + treeHash},
		AllowedRemoteResources: []string{"https://github.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 1)

	assert.Equal(t, "directory", lockResult.Deps[0].Type)
	assert.Equal(t, "gopls-lsp", filepath.Base(h.Plugins[0]), "plugin basename must be the real plugin name, not 'tree'")
	assert.False(t, harness.IsURL(h.Plugins[0]))
}

func TestResolveFromLock_PluginSharedURLWithSkill(t *testing.T) {
	// When a plugin and skill share the same URL, the lock file deduplicates
	// by URL and records only skills[0]. The plugin should still be resolved
	// using the shared dep's local path, not silently dropped.
	manifestJSON := []byte(`{"name": "shared-dir"}`)
	pluginFiles := map[string][]byte{"plugin.json": manifestJSON}
	treeHash := fetch.ComputeTreeHash(pluginFiles)
	sharedURL := "https://github.com/org/repo/tree/main/shared-dir"

	root := t.TempDir()
	_, err := fetch.CachePutDir(root, sharedURL, pluginFiles)
	require.NoError(t, err)

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "skills[0]",
				URL:    sharedURL,
				SHA256: treeHash,
				Type:   "directory",
				Files: []lock.FileEntry{
					{Path: "plugin.json", SHA256: fetch.ComputeSHA256(manifestJSON)},
				},
			},
		},
	}

	h := &harness.Harness{
		Agent:                  "agents/code.md",
		Skills:                 []harness.SkillEntry{{Source: sharedURL + "#sha256=" + treeHash}},
		Plugins:                []string{sharedURL + "#sha256=" + treeHash},
		AllowedRemoteResources: []string{"https://github.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 1)

	assert.Len(t, h.Skills, 1, "skill should survive lock replay")
	assert.Len(t, h.Plugins, 1, "plugin should survive lock replay when sharing URL with skill")
	assert.False(t, harness.IsURL(h.Plugins[0]), "plugin should be resolved to a local path")
	assert.Equal(t, "shared-dir", filepath.Base(h.Plugins[0]))
}

func TestResolveFromLock_PluginRawContentURL(t *testing.T) {
	// Base-composed plugins use raw.githubusercontent.com URLs ending in
	// /plugin.json. resolveFromLock must parse these via ParseRawContentURL
	// (not ParseForgeURL, which rejects non-github.com hosts) to extract
	// the plugin directory name.
	manifestJSON := []byte(`{"name": "gopls-lsp"}`)
	pluginFiles := map[string][]byte{
		"plugin.json": manifestJSON,
	}
	treeHash := fetch.ComputeTreeHash(pluginFiles)

	root := t.TempDir()
	pluginFileURL := "https://raw.githubusercontent.com/fullsend-ai/agents/abc123/plugins/gopls-lsp/plugin.json"
	_, err := fetch.CachePutDir(root, pluginFileURL, pluginFiles)
	require.NoError(t, err)

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "plugins[0]",
				URL:    pluginFileURL,
				SHA256: treeHash,
				Type:   "directory",
				Files: []lock.FileEntry{
					{Path: "plugin.json", SHA256: fetch.ComputeSHA256(manifestJSON)},
				},
			},
		},
	}

	h := &harness.Harness{
		Agent:                  "agents/code.md",
		Plugins:                []string{"plugins/gopls-lsp"},
		AllowedRemoteResources: []string{"https://raw.githubusercontent.com/fullsend-ai/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 1)

	assert.Equal(t, "gopls-lsp", filepath.Base(h.Plugins[0]),
		"plugin basename must be derived from the URL directory, not the marker file")
	assert.False(t, harness.IsURL(h.Plugins[0]))
	assert.FileExists(t, filepath.Join(h.Plugins[0], "plugin.json"))
}

func TestResolveFromLock_SkillRawContentURL(t *testing.T) {
	// Base-composed skills use raw.githubusercontent.com URLs ending in
	// /SKILL.md (see fetchBaseSkill). resolveFromLock must strip the marker
	// file to derive the skill directory name — otherwise every skill is
	// materialized under a directory literally named "SKILL.md", which
	// becomes the sandbox upload basename and collides across skills. Two
	// skills reproduce the collision the fix exists to prevent.
	putSkill := func(t *testing.T, root, slug string) lock.DependencyEntry {
		t.Helper()
		skillMD := []byte("---\nname: " + slug + "\n---\n")
		skillFiles := map[string][]byte{"SKILL.md": skillMD}
		skillFileURL := "https://raw.githubusercontent.com/fullsend-ai/agents/abc123/skills/" + slug + "/SKILL.md"
		_, err := fetch.CachePutDir(root, skillFileURL, skillFiles)
		require.NoError(t, err)
		return lock.DependencyEntry{
			URL:    skillFileURL,
			SHA256: fetch.ComputeTreeHash(skillFiles),
			Type:   "directory",
			Files: []lock.FileEntry{
				{Path: "SKILL.md", SHA256: fetch.ComputeSHA256(skillMD)},
			},
		}
	}

	root := t.TempDir()
	depA := putSkill(t, root, "pr-review")
	depA.Field = "skills[0]"
	depB := putSkill(t, root, "code-review")
	depB.Field = "skills[1]"

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{depA, depB},
	}

	h := &harness.Harness{
		Agent:                  "agents/code.md",
		Skills:                 []harness.SkillEntry{{Source: "skills/pr-review"}, {Source: "skills/code-review"}},
		AllowedRemoteResources: []string{"https://raw.githubusercontent.com/fullsend-ai/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 2)
	require.Len(t, h.Skills, 2)

	assert.Equal(t, "pr-review", filepath.Base(h.Skills[0].Source),
		"skill basename must be derived from the URL directory, not the SKILL.md marker file")
	assert.Equal(t, "code-review", filepath.Base(h.Skills[1].Source),
		"skills must keep distinct basenames — identical ones collide on sandbox upload")
	for _, s := range h.Skills {
		assert.False(t, harness.IsURL(s.Source))
		assert.FileExists(t, filepath.Join(s.Source, "SKILL.md"))
	}
}

func TestResolveFromLock_ForgeScopedSkillNoMutation(t *testing.T) {
	// Forge-scoped base skills are locked under forge.<platform>.skills[N]
	// (see resolveBaseResources). Their paths were already merged into
	// h.Skills by ResolveForge during LoadWithBase, so resolveFromLock must
	// verify the cache entry but leave h.Skills alone — appending would
	// duplicate the skill under the cache's internal tree name.
	skillMD := []byte("---\nname: pr-review\n---\n")
	skillFiles := map[string][]byte{"SKILL.md": skillMD}
	treeHash := fetch.ComputeTreeHash(skillFiles)

	root := t.TempDir()
	skillFileURL := "https://raw.githubusercontent.com/fullsend-ai/agents/abc123/skills/pr-review/SKILL.md"
	_, err := fetch.CachePutDir(root, skillFileURL, skillFiles)
	require.NoError(t, err)
	treePath, _, err := fetch.CacheGetDir(root, treeHash)
	require.NoError(t, err)
	mergedPath, err := fetch.CacheNamedSymlink(treePath, "pr-review")
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(mergedPath),
		"merged path must live in the cache, not the test working directory")

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "forge.github.skills[0]",
				URL:    skillFileURL,
				SHA256: treeHash,
				Type:   "directory",
				Files: []lock.FileEntry{
					{Path: "SKILL.md", SHA256: fetch.ComputeSHA256(skillMD)},
				},
			},
		},
	}

	h := &harness.Harness{
		Agent:                  "agents/code.md",
		Skills:                 []harness.SkillEntry{{Source: mergedPath}},
		AllowedRemoteResources: []string{"https://raw.githubusercontent.com/fullsend-ai/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 1)

	require.Len(t, h.Skills, 1, "forge-scoped skill lock entries must not append to h.Skills")
	assert.Equal(t, mergedPath, h.Skills[0].Source)
}

func TestResolveFromLock_OverlayScopedSkillNoMutation(t *testing.T) {
	// Overlay-scoped skills are locked under overlays[N].skills[M]
	// (see resolveBaseResources). Their paths were already merged into
	// h.Skills by ResolveForge during LoadWithBase, so resolveFromLock must
	// verify the cache entry but leave h.Skills alone — appending would
	// duplicate the skill under the cache's internal tree name.
	skillMD := []byte("---\nname: pr-review\n---\n")
	skillFiles := map[string][]byte{"SKILL.md": skillMD}
	treeHash := fetch.ComputeTreeHash(skillFiles)

	root := t.TempDir()
	skillFileURL := "https://raw.githubusercontent.com/fullsend-ai/agents/abc123/skills/pr-review/SKILL.md"
	_, err := fetch.CachePutDir(root, skillFileURL, skillFiles)
	require.NoError(t, err)
	treePath, _, err := fetch.CacheGetDir(root, treeHash)
	require.NoError(t, err)
	mergedPath, err := fetch.CacheNamedSymlink(treePath, "pr-review")
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(mergedPath),
		"merged path must live in the cache, not the test working directory")

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "overlays[0].skills[0]",
				URL:    skillFileURL,
				SHA256: treeHash,
				Type:   "directory",
				Files: []lock.FileEntry{
					{Path: "SKILL.md", SHA256: fetch.ComputeSHA256(skillMD)},
				},
			},
		},
	}

	h := &harness.Harness{
		Agent:                  "agents/code.md",
		Skills:                 []harness.SkillEntry{{Source: mergedPath}},
		AllowedRemoteResources: []string{"https://raw.githubusercontent.com/fullsend-ai/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 1)

	require.Len(t, h.Skills, 1, "overlay-scoped skill lock entries must not append to h.Skills")
	assert.Equal(t, mergedPath, h.Skills[0].Source)
}

func TestResolveFromLock_OverlayScriptNoMutation(t *testing.T) {
	// Overlay scripts (pre_script, post_script, validation_loop.script) are
	// resolved during LoadWithBase and already set in the harness, so
	// resolveFromLock must verify the cache entry but not mutate the harness.
	script := []byte("#!/bin/bash\necho overlay\n")
	scriptHash := fetch.ComputeSHA256(script)

	root := t.TempDir()
	scriptURL := "https://raw.githubusercontent.com/fullsend-ai/agents/abc123/overlays/pre.sh"
	require.NoError(t, fetch.CachePut(root, scriptURL, script))

	cachePath, err := fetch.CachePath(root, scriptHash)
	require.NoError(t, err)
	cachePath = filepath.Join(cachePath, "content")

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "overlays[0].pre_script",
				URL:    scriptURL,
				SHA256: scriptHash,
				Type:   "file",
			},
		},
	}

	h := &harness.Harness{
		Agent:                  "agents/code.md",
		PreScript:              cachePath,
		AllowedRemoteResources: []string{"https://raw.githubusercontent.com/fullsend-ai/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 1)

	// PreScript should remain unchanged — overlay scripts are already resolved
	assert.Equal(t, cachePath, h.PreScript)
	// Should not be appended to Skills
	assert.Len(t, h.Skills, 0)
}

func TestResolveFromLock_OverlayProviderParsed(t *testing.T) {
	// Overlay providers must be parsed and added to ResolvedProvider list,
	// not incorrectly appended as skills.
	providerYAML := []byte("name: test\ntype: openai\n")
	providerHash := fetch.ComputeSHA256(providerYAML)

	root := t.TempDir()
	providerURL := "https://raw.githubusercontent.com/fullsend-ai/agents/abc123/overlays/provider.yaml"
	require.NoError(t, fetch.CachePut(root, providerURL, providerYAML))

	cachePath, err := fetch.CachePath(root, providerHash)
	require.NoError(t, err)
	cachePath = filepath.Join(cachePath, "content")

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "overlays[0].providers[0]",
				URL:    providerURL,
				SHA256: providerHash,
				Type:   "file",
			},
		},
	}

	h := &harness.Harness{
		Agent:                  "agents/code.md",
		Providers:              []string{providerURL},
		AllowedRemoteResources: []string{"https://raw.githubusercontent.com/fullsend-ai/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 1)

	// Should be in ResolvedProvider list
	require.Len(t, lockResult.Providers, 1)
	assert.Equal(t, "test", lockResult.Providers[0].Def.Name)
	assert.Equal(t, "openai", lockResult.Providers[0].Def.Type)
	assert.Equal(t, cachePath, lockResult.Providers[0].LocalPath)

	// Should not be appended to Skills
	assert.Len(t, h.Skills, 0)
}

func TestResolveFromLock_OverlayProfileParsed(t *testing.T) {
	// Overlay profiles must be parsed and added to ResolvedProfile list,
	// not incorrectly appended as skills.
	profileYAML := []byte("id: test-profile\nshell: bash\n")
	profileHash := fetch.ComputeSHA256(profileYAML)

	root := t.TempDir()
	profileURL := "https://raw.githubusercontent.com/fullsend-ai/agents/abc123/overlays/profile.yaml"
	require.NoError(t, fetch.CachePut(root, profileURL, profileYAML))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "overlays[0].openshell.profiles[0]",
				URL:    profileURL,
				SHA256: profileHash,
				Type:   "file",
			},
		},
	}

	h := &harness.Harness{
		Agent: "agents/code.md",
		OpenShell: &harness.OpenShellConfig{
			Profiles: []string{profileURL},
		},
		AllowedRemoteResources: []string{"https://raw.githubusercontent.com/fullsend-ai/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Deps, 1)

	// Should be in ResolvedProfile list
	require.Len(t, lockResult.Profiles, 1)
	assert.Equal(t, "test-profile", lockResult.Profiles[0].ID)
	assert.True(t, lockResult.Profiles[0].FromURL)

	// Should not be appended to Skills
	assert.Len(t, h.Skills, 0)
}

func TestResolveFromLock_SkillRepoRootURLRejected(t *testing.T) {
	// A skills[N] lock entry whose URL is a forge repo root has no directory
	// segment to name the skill after; resolveFromLock must surface the
	// lockTreeDirName error instead of materializing a misnamed skill.
	skillMD := []byte("---\nname: x\n---\n")
	skillFiles := map[string][]byte{"SKILL.md": skillMD}

	root := t.TempDir()
	repoRootURL := "https://github.com/fullsend-ai/agents/tree/main"
	_, err := fetch.CachePutDir(root, repoRootURL, skillFiles)
	require.NoError(t, err)

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "skills[0]",
				URL:    repoRootURL,
				SHA256: fetch.ComputeTreeHash(skillFiles),
				Type:   "directory",
				Files: []lock.FileEntry{
					{Path: "SKILL.md", SHA256: fetch.ComputeSHA256(skillMD)},
				},
			},
		},
	}

	h := &harness.Harness{
		Agent:                  "agents/code.md",
		Skills:                 []harness.SkillEntry{{Source: "skills/x"}},
		AllowedRemoteResources: []string{"https://github.com/fullsend-ai/"},
	}

	printer := ui.New(os.Stdout)
	_, err = resolveFromLock(h, entry, root, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "skills[0]: URL must point to a directory inside the repo, not the repo root")
}

func TestResolveFromLock_PluginInvalidBasenameRejected(t *testing.T) {
	// A plugins[N] lock entry whose derived directory name fails
	// ValidPluginBasename must be rejected — CacheNamedSymlink would
	// otherwise silently substitute a reserved internal name.
	manifest := []byte(`{"name": "bad"}`)
	pluginFiles := map[string][]byte{"plugin.json": manifest}

	root := t.TempDir()
	pluginFileURL := "https://raw.githubusercontent.com/fullsend-ai/agents/abc123/plugins/bad.name/plugin.json"
	_, err := fetch.CachePutDir(root, pluginFileURL, pluginFiles)
	require.NoError(t, err)

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "plugins[0]",
				URL:    pluginFileURL,
				SHA256: fetch.ComputeTreeHash(pluginFiles),
				Type:   "directory",
				Files: []lock.FileEntry{
					{Path: "plugin.json", SHA256: fetch.ComputeSHA256(manifest)},
				},
			},
		},
	}

	h := &harness.Harness{
		Agent:                  "agents/code.md",
		Plugins:                []string{"plugins/bad.name"},
		AllowedRemoteResources: []string{"https://raw.githubusercontent.com/fullsend-ai/"},
	}

	printer := ui.New(os.Stdout)
	_, err = resolveFromLock(h, entry, root, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains invalid characters")
}

func TestLockTreeDirName(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		url     string
		want    string
		wantErr string
	}{
		{
			name:  "forge tree URL uses deepest path segment",
			field: "skills[0]",
			url:   "https://github.com/org/repo/tree/main/skills/pr-review",
			want:  "pr-review",
		},
		{
			name:    "forge repo root rejected",
			field:   "skills[0]",
			url:     "https://github.com/org/repo/tree/main",
			wantErr: "must point to a directory inside the repo",
		},
		{
			name:  "raw SKILL.md marker stripped",
			field: "skills[0]",
			url:   "https://raw.githubusercontent.com/org/repo/abc123/skills/pr-review/SKILL.md",
			want:  "pr-review",
		},
		{
			name:  "raw plugin.json marker stripped",
			field: "plugins[0]",
			url:   "https://raw.githubusercontent.com/org/repo/abc123/plugins/gopls-lsp/plugin.json",
			want:  "gopls-lsp",
		},
		{
			name:    "raw marker at ref root rejected",
			field:   "skills[0]",
			url:     "https://raw.githubusercontent.com/org/repo/abc123/SKILL.md",
			wantErr: "must point to a marker file inside a directory",
		},
		{
			name:  "raw non-marker URL keeps last segment",
			field: "skills[0]",
			url:   "https://raw.githubusercontent.com/org/repo/abc123/skills/pr-review",
			want:  "pr-review",
		},
		{
			name:  "unparseable URL falls back to last segment",
			field: "skills[0]",
			url:   "https://example.com/skills/my-skill",
			want:  "my-skill",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := lockTreeDirName(tt.field, tt.url)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveFromLock_LocalPathsSurviveStrip(t *testing.T) {
	// When a harness has URL resources (with lock deps) AND local-path
	// profiles/providers (without lock deps), the lock strip must keep the
	// local-path entries so the second ResolveHarness pass can process them.
	skillContent := []byte("id: my-skill\nname: My Skill\n")
	skillHash := fetch.ComputeSHA256(skillContent)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/skills/my.yaml", skillContent))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "skills[0]",
				URL:    "https://example.com/skills/my.yaml",
				SHA256: skillHash,
			},
		},
	}

	h := &harness.Harness{
		Agent:  "agents/code.md",
		Skills: []harness.SkillEntry{{Source: "https://example.com/skills/my.yaml#sha256=" + skillHash}},
		OpenShell: &harness.OpenShellConfig{
			Profiles: []string{"/workspace/.fullsend/profiles/claude-code.yaml"},
		},
		Providers:              []string{"local-bare-name", "/workspace/.fullsend/providers/custom.yaml"},
		AllowedRemoteResources: []string{"https://example.com/"},
	}

	printer := ui.New(os.Stdout)
	_, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)

	// Local-path profile must survive the strip.
	require.Len(t, h.OpenShell.Profiles, 1)
	assert.Equal(t, "/workspace/.fullsend/profiles/claude-code.yaml", h.OpenShell.Profiles[0])

	// Bare name AND local-path provider must survive; URL was stripped.
	require.Len(t, h.Providers, 2)
	assert.Equal(t, "local-bare-name", h.Providers[0])
	assert.Equal(t, "/workspace/.fullsend/providers/custom.yaml", h.Providers[1])
}

func TestResolveFromLock_ProfileAndProviderReconstruction(t *testing.T) {
	profileContent := []byte("id: anthropic\nname: Anthropic\n")
	profileHash := fetch.ComputeSHA256(profileContent)

	providerContent := []byte("name: my-provider\ntype: anthropic\ncredentials:\n  API_KEY: ${ANTHROPIC_KEY}\n")
	providerHash := fetch.ComputeSHA256(providerContent)

	root := t.TempDir()
	require.NoError(t, fetch.CachePut(root, "https://example.com/profiles/anthropic.yaml", profileContent))
	require.NoError(t, fetch.CachePut(root, "https://example.com/providers/my.yaml", providerContent))

	entry := &lock.HarnessLock{
		Dependencies: []lock.DependencyEntry{
			{
				Field:  "openshell.profiles[0]",
				URL:    "https://example.com/profiles/anthropic.yaml",
				SHA256: profileHash,
			},
			{
				Field:  "providers[0]",
				URL:    "https://example.com/providers/my.yaml",
				SHA256: providerHash,
			},
		},
	}

	h := &harness.Harness{
		Agent: "agents/code.md",
		OpenShell: &harness.OpenShellConfig{
			Profiles: []string{"https://example.com/profiles/anthropic.yaml#sha256=" + profileHash},
		},
		Providers:              []string{"https://example.com/providers/my.yaml#sha256=" + providerHash},
		AllowedRemoteResources: []string{"https://example.com/"},
	}

	printer := ui.New(os.Stdout)
	lockResult, err := resolveFromLock(h, entry, root, printer)
	require.NoError(t, err)
	require.Len(t, lockResult.Profiles, 1)
	assert.Equal(t, "anthropic", lockResult.Profiles[0].ID)
	require.Len(t, lockResult.Providers, 1)
	assert.Equal(t, "my-provider", lockResult.Providers[0].Def.Name)
	assert.Equal(t, "anthropic", lockResult.Providers[0].Def.Type)
	require.Len(t, lockResult.Deps, 2)
}
