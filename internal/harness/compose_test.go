package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func computeHash(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}

func writeTestHarness(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

func TestLoadWithBase_NoBase(t *testing.T) {
	dir := t.TempDir()
	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/test.md
role: test
model: opus
`)

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)
	assert.Equal(t, "agents/test.md", h.Agent)
	assert.Equal(t, "opus", h.Model)
	assert.Empty(t, deps)
	assert.Empty(t, h.Base)
}

func TestLoadWithBase_LocalBase_ScalarOverride(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/base.md
role: test
model: sonnet
image: base-image
timeout_minutes: 30
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
agent: agents/child.md
role: test
model: opus
`)

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	// Child overrides base
	assert.Equal(t, "agents/child.md", h.Agent)
	assert.Equal(t, "opus", h.Model)
	// Base values inherited
	assert.Equal(t, "base-image", h.Image)
	assert.Equal(t, 30, h.TimeoutMinutes)
	// No URL deps
	assert.Empty(t, deps)
	// Base field consumed
	assert.Empty(t, h.Base)
}

func TestLoadWithBase_LocalBase_SkillsConcat(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/test.md
role: test
skills:
  - skill-a
  - skill-b
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
skills:
  - skill-c
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	// Skills concatenated: base + child
	assert.Equal(t, []string{"skill-a", "skill-b", "skill-c"}, h.Skills)
}

func TestLoadWithBase_LocalBase_RunnerEnvMerge(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/test.md
role: test
runner_env:
  KEY1: base-value1
  KEY2: base-value2
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
runner_env:
  KEY2: child-value2
  KEY3: child-value3
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	// RunnerEnv merged: base + child, child wins on conflict
	assert.Equal(t, map[string]string{
		"KEY1": "base-value1",
		"KEY2": "child-value2",
		"KEY3": "child-value3",
	}, h.RunnerEnv)
}

func TestLoadWithBase_LocalBase_HostFilesDedup(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/test.md
role: test
host_files:
  - src: base-src1
    dest: /dest1
  - src: base-src2
    dest: /dest2
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
host_files:
  - src: child-src2
    dest: /dest2
  - src: child-src3
    dest: /dest3
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	// HostFiles: base + child, child overrides same Dest
	require.Len(t, h.HostFiles, 3)
	assert.Equal(t, "base-src1", h.HostFiles[0].Src)
	assert.Equal(t, "/dest1", h.HostFiles[0].Dest)
	assert.Equal(t, "child-src2", h.HostFiles[1].Src) // overridden
	assert.Equal(t, "/dest2", h.HostFiles[1].Dest)
	assert.Equal(t, "child-src3", h.HostFiles[2].Src)
	assert.Equal(t, "/dest3", h.HostFiles[2].Dest)
}

func TestLoadWithBase_LocalBase_ValidationLoopReplace(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/test.md
role: test
validation_loop:
  script: base-script.sh
  max_iterations: 5
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
validation_loop:
  script: child-script.sh
  max_iterations: 3
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	// ValidationLoop: child replaces entirely
	require.NotNil(t, h.ValidationLoop)
	assert.Equal(t, "child-script.sh", h.ValidationLoop.Script)
	assert.Equal(t, 3, h.ValidationLoop.MaxIterations)
}

func TestLoadWithBase_LocalBase_ValidationLoopInherit(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/test.md
role: test
validation_loop:
  script: base-script.sh
  max_iterations: 5
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
model: opus
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	// ValidationLoop: inherited from base when child is nil
	require.NotNil(t, h.ValidationLoop)
	assert.Equal(t, "base-script.sh", h.ValidationLoop.Script)
	assert.Equal(t, 5, h.ValidationLoop.MaxIterations)
}

func TestLoadWithBase_ChainedBases(t *testing.T) {
	dir := t.TempDir()

	// A → B → C: C is the root, B extends C, A extends B
	writeTestHarness(t, dir, "c.yaml", `
agent: agents/c.md
role: test
model: c-model
image: c-image
skills:
  - skill-c
`)

	writeTestHarness(t, dir, "b.yaml", `
base: c.yaml
model: b-model
skills:
  - skill-b
`)

	path := writeTestHarness(t, dir, "a.yaml", `
base: b.yaml
agent: agents/a.md
role: test
skills:
  - skill-a
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	// A overrides agent
	assert.Equal(t, "agents/a.md", h.Agent)
	// B overrides model
	assert.Equal(t, "b-model", h.Model)
	// C provides image (inherited through B to A)
	assert.Equal(t, "c-image", h.Image)
	// Skills concatenated: c + b + a
	assert.Equal(t, []string{"skill-c", "skill-b", "skill-a"}, h.Skills)
}

func TestLoadWithBase_CycleDetection(t *testing.T) {
	dir := t.TempDir()

	// A → B → A (cycle)
	writeTestHarness(t, dir, "a.yaml", `
agent: agents/a.md
role: test
base: b.yaml
`)

	writeTestHarness(t, dir, "b.yaml", `
agent: agents/b.md
role: test
base: a.yaml
`)

	path := filepath.Join(dir, "a.yaml")
	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circular base reference")
}

func TestLoadWithBase_SelfReference(t *testing.T) {
	dir := t.TempDir()

	// A → A (self-reference)
	path := writeTestHarness(t, dir, "a.yaml", `
agent: agents/a.md
role: test
base: a.yaml
`)

	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circular base reference")
}

func TestLoadWithBase_LocalBase_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	require.NoError(t, os.MkdirAll(subdir, 0755))

	// Child in subdir tries to reference base outside workspace root via ../
	path := writeTestHarness(t, subdir, "child.yaml", `
agent: agents/child.md
role: test
base: ../../../etc/passwd
`)

	// WorkspaceRoot is subdir, so ../../../etc/passwd escapes it
	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: subdir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes workspace root")
}

func TestLoadWithBase_LocalBase_PathTraversal_NoWorkspaceRoot(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	require.NoError(t, os.MkdirAll(subdir, 0755))

	// Child in subdir tries to reference base outside via ../
	path := writeTestHarness(t, subdir, "child.yaml", `
agent: agents/child.md
role: test
base: ../outside.yaml
`)

	// No WorkspaceRoot set, so childDir is used as containment root
	// ../outside.yaml escapes subdir
	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes workspace root")
}

func TestLoadWithBase_DepthExceeded(t *testing.T) {
	dir := t.TempDir()

	// Create a chain deeper than MaxBaseDepth
	for i := MaxBaseDepth + 2; i >= 0; i-- {
		var content string
		if i == MaxBaseDepth+2 {
			content = `agent: agents/root.md`
		} else {
			content = fmt.Sprintf("agent: agents/test.md\nbase: h%d.yaml", i+1)
		}
		writeTestHarness(t, dir, fmt.Sprintf("h%d.yaml", i), content)
	}

	path := filepath.Join(dir, "h0.yaml")
	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeded maximum base depth")
}

func TestLoadWithBase_ForgeBlockMerge(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/test.md
role: test
forge:
  github:
    pre_script: base-pre.sh
    skills:
      - gh-skill-base
    runner_env:
      GH_KEY1: base-value1
  gitlab:
    pre_script: gitlab-pre.sh
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
forge:
  github:
    post_script: child-post.sh
    skills:
      - gh-skill-child
    runner_env:
      GH_KEY2: child-value2
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		ForgePlatform: "github",
	})
	require.NoError(t, err)

	// GitHub forge merged, then resolved
	assert.Equal(t, "base-pre.sh", h.PreScript)    // from base forge
	assert.Equal(t, "child-post.sh", h.PostScript) // from child forge
	assert.Contains(t, h.Skills, "gh-skill-base")  // base skills
	assert.Contains(t, h.Skills, "gh-skill-child") // child skills
	assert.Equal(t, "base-value1", h.RunnerEnv["GH_KEY1"])
	assert.Equal(t, "child-value2", h.RunnerEnv["GH_KEY2"])

	// Forge map consumed after ResolveForge
	assert.Nil(t, h.Forge)
}

func TestLoadWithBase_ForgeInheritPlatform(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/test.md
role: test
forge:
  github:
    pre_script: gh-pre.sh
  gitlab:
    pre_script: gl-pre.sh
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
model: opus
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		ForgePlatform: "gitlab",
	})
	require.NoError(t, err)

	// GitLab forge inherited from base
	assert.Equal(t, "gl-pre.sh", h.PreScript)
}

func TestLoadWithBase_URLBase(t *testing.T) {
	baseContent := []byte(`
agent: agents/remote.md
role: test
model: sonnet
skills:
  - remote-skill
`)
	hash := computeHash(baseContent)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(baseContent)
	}))
	defer server.Close()

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	baseURL := server.URL + "/base.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+baseURL+`
allowed_remote_resources:
  - `+server.URL+`/
`)

	policy := fetch.NewTestPolicy(
		server.Client().Transport.(*http.Transport).TLSClientConfig,
		[]string{"127.0.0.1"},
		[]string{server.Listener.Addr().String()[len("127.0.0.1:"):]},
	)

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
	})
	require.NoError(t, err)

	// Child overrides agent
	assert.Equal(t, "agents/child.md", h.Agent)
	// Base provides model and skills
	assert.Equal(t, "sonnet", h.Model)
	assert.Contains(t, h.Skills, "remote-skill")

	// One dependency for the URL base
	require.Len(t, deps, 1)
	assert.Equal(t, "base", deps[0].Field)
	assert.Equal(t, server.URL+"/base.yaml", deps[0].URL)
	assert.Equal(t, hash, deps[0].SHA256)
}

func TestLoadWithBase_ChainedURLBases(t *testing.T) {
	// Test URL base whose own base is also a URL
	grandparentContent := []byte(`
agent: agents/grandparent.md
role: test
model: opus
`)
	grandparentHash := computeHash(grandparentContent)

	parentContent := []byte(`
agent: agents/parent.md
role: test
skills:
  - parent-skill
`)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/grandparent.yaml" {
			w.WriteHeader(http.StatusOK)
			w.Write(grandparentContent)
		} else if r.URL.Path == "/parent.yaml" {
			// Parent's base field will be set dynamically
			w.WriteHeader(http.StatusOK)
			w.Write(parentContent)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	// Now create parent content with base pointing to grandparent
	parentContentWithBase := []byte(fmt.Sprintf(`
agent: agents/parent.md
role: test
base: %s/grandparent.yaml#sha256=%s
skills:
  - parent-skill
`, server.URL, grandparentHash))
	parentHash := computeHash(parentContentWithBase)

	// Update server to serve the correct parent content
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/grandparent.yaml" {
			w.WriteHeader(http.StatusOK)
			w.Write(grandparentContent)
		} else if r.URL.Path == "/parent.yaml" {
			w.WriteHeader(http.StatusOK)
			w.Write(parentContentWithBase)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	})

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	parentURL := server.URL + "/parent.yaml#sha256=" + parentHash

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+parentURL+`
skills:
  - child-skill
`)

	policy := fetch.NewTestPolicy(
		server.Client().Transport.(*http.Transport).TLSClientConfig,
		[]string{"127.0.0.1"},
		[]string{server.Listener.Addr().String()[len("127.0.0.1:"):]},
	)

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
	})
	require.NoError(t, err)

	// Child overrides agent
	assert.Equal(t, "agents/child.md", h.Agent)
	// Grandparent provides model
	assert.Equal(t, "opus", h.Model)
	// Skills concatenated: grandparent (none) + parent + child
	assert.Contains(t, h.Skills, "parent-skill")
	assert.Contains(t, h.Skills, "child-skill")

	// Two dependencies for the chained URL bases
	require.Len(t, deps, 2)
}

func TestLoadWithBase_URLBase_HashMismatch(t *testing.T) {
	baseContent := []byte(`agent: agents/remote.md`)
	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(baseContent)
	}))
	defer server.Close()

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	baseURL := server.URL + "/base.yaml#sha256=" + wrongHash

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+baseURL+`
allowed_remote_resources:
  - `+server.URL+`/
`)

	policy := fetch.NewTestPolicy(
		server.Client().Transport.(*http.Transport).TLSClientConfig,
		[]string{"127.0.0.1"},
		[]string{server.Listener.Addr().String()[len("127.0.0.1:"):]},
	)

	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "integrity check failed")
}

func TestLoadWithBase_URLBase_NotInAllowlist(t *testing.T) {
	baseContent := []byte(`agent: agents/remote.md`)
	hash := computeHash(baseContent)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(baseContent)
	}))
	defer server.Close()

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	baseURL := server.URL + "/base.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+baseURL+`
allowed_remote_resources:
  - https://other.example.com/
`)

	policy := fetch.NewTestPolicy(
		server.Client().Transport.(*http.Transport).TLSClientConfig,
		[]string{"127.0.0.1"},
		[]string{server.Listener.Addr().String()[len("127.0.0.1:"):]},
	)

	// allowSelfAllowlist lets us use child's list, but base URL doesn't match it
	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot:      cacheDir,
		FetchPolicy:        policy,
		allowSelfAllowlist: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in allowed_remote_resources")
}

func TestLoadWithBase_URLBase_NoOrgAllowlist(t *testing.T) {
	dir := t.TempDir()

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: https://example.com/base.yaml#sha256=0000000000000000000000000000000000000000000000000000000000000000
`)

	// No OrgAllowlist and allowSelfAllowlist is false (default)
	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "URL base requires org-level allowed_remote_resources")
}

func TestLoadWithBase_URLBase_MissingHash(t *testing.T) {
	dir := t.TempDir()

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: https://example.com/base.yaml
allowed_remote_resources:
  - https://example.com/
`)

	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		OrgAllowlist: []string{"https://example.com/"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must include #sha256=")
}

func TestLoadWithBase_URLBase_OfflineMode_CacheMiss(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: https://example.com/base.yaml#sha256=0000000000000000000000000000000000000000000000000000000000000000
allowed_remote_resources:
  - https://example.com/
`)

	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy: fetch.FetchPolicy{
			Offline: true,
		},
		OrgAllowlist: []string{"https://example.com/"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "offline mode is enabled")
}

func TestLoadWithBase_URLBase_OfflineMode_CacheHit(t *testing.T) {
	baseContent := []byte(`
agent: agents/remote.md
role: test
model: sonnet
`)
	hash := computeHash(baseContent)

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	// Pre-populate cache
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/base.yaml", baseContent))

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: https://example.com/base.yaml#sha256=`+hash+`
allowed_remote_resources:
  - https://example.com/
`)

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy: fetch.FetchPolicy{
			Offline: true,
		},
		OrgAllowlist: []string{"https://example.com/"},
	})
	require.NoError(t, err)

	assert.Equal(t, "agents/child.md", h.Agent)
	assert.Equal(t, "sonnet", h.Model)

	// Dependency shows cache hit
	require.Len(t, deps, 1)
	assert.True(t, deps[0].CacheHit)
}

func TestLoadWithBase_RoleSlugInheritance(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/base.md
role: triage
slug: fullsend-ai-triage
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
agent: agents/child.md
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	// Role and slug inherited from base
	assert.Equal(t, "triage", h.Role)
	assert.Equal(t, "fullsend-ai-triage", h.Slug)
}

func TestLoadWithBase_AllowedRemoteResourcesNotMerged(t *testing.T) {
	// AllowedRemoteResources is NOT merged from base to prevent privilege escalation
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/base.md
role: test
allowed_remote_resources:
  - https://example.com/base/
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
allowed_remote_resources:
  - https://example.com/child/
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	// Only child's AllowedRemoteResources, not merged with base
	assert.Equal(t, []string{"https://example.com/child/"}, h.AllowedRemoteResources)
}

func TestMergeHostFiles(t *testing.T) {
	base := []HostFile{
		{Src: "base1", Dest: "/dest1"},
		{Src: "base2", Dest: "/dest2"},
	}
	child := []HostFile{
		{Src: "child2", Dest: "/dest2"}, // override
		{Src: "child3", Dest: "/dest3"}, // new
	}

	result := mergeHostFiles(base, child)

	require.Len(t, result, 3)
	assert.Equal(t, "base1", result[0].Src)
	assert.Equal(t, "/dest1", result[0].Dest)
	assert.Equal(t, "child2", result[1].Src) // overridden
	assert.Equal(t, "/dest2", result[1].Dest)
	assert.Equal(t, "child3", result[2].Src)
	assert.Equal(t, "/dest3", result[2].Dest)
}

func TestMergeForgeBlocks(t *testing.T) {
	base := map[string]*ForgeConfig{
		"github": {
			PreScript: "base-pre.sh",
			Skills:    []string{"base-skill"},
			RunnerEnv: map[string]string{"KEY1": "base1"},
		},
		"gitlab": {
			PreScript: "gitlab-pre.sh",
		},
	}
	child := map[string]*ForgeConfig{
		"github": {
			PostScript: "child-post.sh",
			Skills:     []string{"child-skill"},
			RunnerEnv:  map[string]string{"KEY2": "child2"},
		},
	}

	result := mergeForgeBlocks(base, child)

	// GitHub merged
	gh := result["github"]
	require.NotNil(t, gh)
	assert.Equal(t, "base-pre.sh", gh.PreScript)    // inherited
	assert.Equal(t, "child-post.sh", gh.PostScript) // from child
	assert.Equal(t, []string{"base-skill", "child-skill"}, gh.Skills)
	assert.Equal(t, "base1", gh.RunnerEnv["KEY1"])  // inherited
	assert.Equal(t, "child2", gh.RunnerEnv["KEY2"]) // from child

	// GitLab inherited
	gl := result["gitlab"]
	require.NotNil(t, gl)
	assert.Equal(t, "gitlab-pre.sh", gl.PreScript)
}

func TestMergeForgeBlocks_NilChild(t *testing.T) {
	base := map[string]*ForgeConfig{
		"github": {
			PreScript: "base-pre.sh",
		},
	}

	result := mergeForgeBlocks(base, nil)

	require.NotNil(t, result)
	assert.Equal(t, "base-pre.sh", result["github"].PreScript)
}

func TestMergeForgeBlocks_NilChildPlatform(t *testing.T) {
	base := map[string]*ForgeConfig{
		"github": {
			PreScript: "base-pre.sh",
		},
	}
	child := map[string]*ForgeConfig{
		"github": nil, // explicitly nil — should NOT inherit from base
	}

	result := mergeForgeBlocks(base, child)

	// Child explicitly nulled github, so it stays nil
	assert.Nil(t, result["github"])
}

func TestMergeForgeConfigInto_NilBase(t *testing.T) {
	child := &ForgeConfig{
		PreScript: "child-pre.sh",
	}

	// Should not panic with nil base
	mergeForgeConfigInto(nil, child)

	assert.Equal(t, "child-pre.sh", child.PreScript)
}

func TestMergeForgeConfigInto_ValidationLoop(t *testing.T) {
	base := &ForgeConfig{
		ValidationLoop: &ValidationLoop{
			Script:        "base-validate.sh",
			MaxIterations: 5,
		},
	}
	child := &ForgeConfig{
		PreScript: "child-pre.sh",
		// No ValidationLoop — should inherit from base
	}

	mergeForgeConfigInto(base, child)

	require.NotNil(t, child.ValidationLoop)
	assert.Equal(t, "base-validate.sh", child.ValidationLoop.Script)
	assert.Equal(t, 5, child.ValidationLoop.MaxIterations)
}

func TestLoadWithBase_InvalidForgeAfterMerge(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/base.md
role: test
forge:
  invalid_platform:
    pre_script: test.sh
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
model: opus
`)

	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid harness")
}

func TestLoadWithBase_ValidationErrorAfterMerge(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/base.md
role: test
`)

	// Child clears the agent field (empty string doesn't override)
	// but then the merged result is invalid because agent is required
	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
agent: ""
`)

	// This should work because empty string doesn't override
	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)
	assert.Equal(t, "agents/base.md", h.Agent)
}

func TestLoadWithBase_BaseFileNotFound(t *testing.T) {
	dir := t.TempDir()

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: nonexistent.yaml
`)

	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading base chain")
}

func TestLoadWithBase_URLBase_NonHTTPS(t *testing.T) {
	dir := t.TempDir()

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: http://example.com/base.yaml#sha256=0000000000000000000000000000000000000000000000000000000000000000
allowed_remote_resources:
  - http://example.com/
`)

	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		OrgAllowlist: []string{"http://example.com/"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "https")
}

func TestLoadWithBase_SecurityInheritance(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/base.md
role: test
security:
  fail_mode: closed
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
model: opus
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	require.NotNil(t, h.Security)
	assert.Equal(t, "closed", h.Security.FailMode)
}

func TestLoadWithBase_SecurityChildOverrides(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/base.md
role: test
security:
  fail_mode: closed
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
security:
  fail_mode: open
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	require.NotNil(t, h.Security)
	assert.Equal(t, "open", h.Security.FailMode)
}

func TestLoadWithBase_APIServersConcat(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/base.md
role: test
api_servers:
  - name: base-api
    script: base-api.sh
    port: 8080
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
api_servers:
  - name: child-api
    script: child-api.sh
    port: 9090
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	require.Len(t, h.APIServers, 2)
	assert.Equal(t, "base-api", h.APIServers[0].Name)
	assert.Equal(t, "child-api", h.APIServers[1].Name)
}

func TestLoadWithBase_PluginsConcat(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/base.md
role: test
plugins:
  - plugin-a
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
plugins:
  - plugin-b
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	assert.Equal(t, []string{"plugin-a", "plugin-b"}, h.Plugins)
}

func TestLoadWithBase_ProvidersConcat(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/base.md
role: test
providers:
  - provider-a
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
providers:
  - provider-b
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	assert.Equal(t, []string{"provider-a", "provider-b"}, h.Providers)
}

func TestLoadWithBase_TimeoutInheritance(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/base.md
role: test
timeout_minutes: 30
sandbox_timeout_seconds: 600
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
model: opus
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	assert.Equal(t, 30, h.TimeoutMinutes)
	assert.Equal(t, 600, h.SandboxTimeoutSeconds)
}

func TestLoadWithBase_RunnerEnvNilBase(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/base.md
role: test
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
runner_env:
  KEY1: value1
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	assert.Equal(t, map[string]string{"KEY1": "value1"}, h.RunnerEnv)
}

func TestURLDirPrefix(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			"https://raw.githubusercontent.com/org/repo/sha/harness/triage.yaml#sha256=abc123",
			"https://raw.githubusercontent.com/org/repo/sha/harness/",
		},
		{
			"https://example.com/path/to/file.yaml",
			"https://example.com/path/to/",
		},
		{
			"https://example.com/file.yaml#sha256=0000000000000000000000000000000000000000000000000000000000000000",
			"https://example.com/",
		},
		{
			"not-a-url",
			"",
		},
	}
	for _, tt := range tests {
		got := urlDirPrefix(tt.input)
		assert.Equal(t, tt.want, got, "urlDirPrefix(%q)", tt.input)
	}
}

func setupScriptTestServer(t *testing.T, harnessContent []byte, scripts map[string][]byte) (*httptest.Server, fetch.FetchPolicy) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/harness/triage.yaml" {
			w.WriteHeader(http.StatusOK)
			w.Write(harnessContent)
			return
		}
		if content, ok := scripts[r.URL.Path]; ok {
			w.WriteHeader(http.StatusOK)
			w.Write(content)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	policy := fetch.NewTestPolicy(
		server.Client().Transport.(*http.Transport).TLSClientConfig,
		[]string{"127.0.0.1"},
		[]string{server.Listener.Addr().String()[len("127.0.0.1:"):]},
	)
	return server, policy
}

func TestLoadWithBase_URLBase_ScriptsFetched(t *testing.T) {
	preScript := []byte("#!/bin/bash\necho pre")
	postScript := []byte("#!/bin/bash\necho post")

	baseContent := []byte(`
agent: agents/triage.md
role: test
model: opus
pre_script: scripts/pre.sh
post_script: scripts/post.sh
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/harness/scripts/pre.sh":  preScript,
		"/harness/scripts/post.sh": postScript,
	})

	hash := computeHash(baseContent)
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	baseURL := server.URL + "/harness/triage.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+baseURL+`
`)

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
	})
	require.NoError(t, err)

	assert.Equal(t, "agents/child.md", h.Agent)
	assert.Equal(t, "opus", h.Model)

	// Scripts resolved to local cache paths
	assert.NotEmpty(t, h.PreScript)
	assert.NotEmpty(t, h.PostScript)
	assert.True(t, filepath.IsAbs(h.PreScript), "pre_script should be absolute cache path")
	assert.True(t, filepath.IsAbs(h.PostScript), "post_script should be absolute cache path")
	assert.False(t, IsURL(h.PreScript), "pre_script should not be a URL")
	assert.False(t, IsURL(h.PostScript), "post_script should not be a URL")

	// Verify cached content
	preContent, err := os.ReadFile(h.PreScript)
	require.NoError(t, err)
	assert.Equal(t, preScript, preContent)

	postContent, err := os.ReadFile(h.PostScript)
	require.NoError(t, err)
	assert.Equal(t, postScript, postContent)

	// Dependencies: 1 for base harness + 2 for scripts
	require.Len(t, deps, 3)
	assert.Equal(t, "base", deps[0].Field)
	scriptDeps := deps[1:]
	scriptFields := map[string]bool{}
	for _, d := range scriptDeps {
		scriptFields[d.Field] = true
		assert.Equal(t, "script", d.Type)
		assert.False(t, d.CacheHit)
	}
	assert.True(t, scriptFields["pre_script"])
	assert.True(t, scriptFields["post_script"])
}

func TestLoadWithBase_URLBase_ValidationLoopScriptFetched(t *testing.T) {
	validateScript := []byte("#!/bin/bash\necho validate")

	baseContent := []byte(`
agent: agents/triage.md
role: test
validation_loop:
  script: scripts/validate.sh
  max_iterations: 3
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/harness/scripts/validate.sh": validateScript,
	})

	hash := computeHash(baseContent)
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	baseURL := server.URL + "/harness/triage.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+baseURL+`
`)

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
	})
	require.NoError(t, err)

	require.NotNil(t, h.ValidationLoop)
	assert.True(t, filepath.IsAbs(h.ValidationLoop.Script))
	assert.Equal(t, 3, h.ValidationLoop.MaxIterations)

	content, err := os.ReadFile(h.ValidationLoop.Script)
	require.NoError(t, err)
	assert.Equal(t, validateScript, content)

	// 1 base + 1 validation script
	require.Len(t, deps, 2)
	assert.Equal(t, "validation_loop.script", deps[1].Field)
	assert.Equal(t, "script", deps[1].Type)
}

func TestLoadWithBase_URLBase_ForgeScriptsFetched(t *testing.T) {
	forgePre := []byte("#!/bin/bash\necho forge-pre")
	forgePost := []byte("#!/bin/bash\necho forge-post")

	baseContent := []byte(`
agent: agents/triage.md
role: test
forge:
  github:
    pre_script: scripts/gh-pre.sh
    post_script: scripts/gh-post.sh
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/harness/scripts/gh-pre.sh":  forgePre,
		"/harness/scripts/gh-post.sh": forgePost,
	})

	hash := computeHash(baseContent)
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	baseURL := server.URL + "/harness/triage.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+baseURL+`
`)

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
		ForgePlatform: "github",
	})
	require.NoError(t, err)

	// After forge resolution, scripts are promoted to top level
	assert.True(t, filepath.IsAbs(h.PreScript))
	assert.True(t, filepath.IsAbs(h.PostScript))

	preContent, err := os.ReadFile(h.PreScript)
	require.NoError(t, err)
	assert.Equal(t, forgePre, preContent)

	// 1 base + 2 forge scripts
	require.Len(t, deps, 3)
	forgeScriptDeps := deps[1:]
	for _, d := range forgeScriptDeps {
		assert.Equal(t, "script", d.Type)
		assert.Contains(t, d.Field, "forge.github.")
	}
}

func TestLoadWithBase_URLBase_ChildOverridesScript(t *testing.T) {
	baseContent := []byte(`
agent: agents/triage.md
role: test
pre_script: scripts/base-pre.sh
post_script: scripts/base-post.sh
`)
	preScript := []byte("#!/bin/bash\necho base-pre")
	postScript := []byte("#!/bin/bash\necho base-post")

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/harness/scripts/base-pre.sh":  preScript,
		"/harness/scripts/base-post.sh": postScript,
	})

	hash := computeHash(baseContent)
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	baseURL := server.URL + "/harness/triage.yaml#sha256=" + hash

	// Child overrides pre_script; both base scripts are still fetched
	// before merge (we can't know which fields the child overrides yet).
	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+baseURL+`
pre_script: local-pre.sh
`)

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
	})
	require.NoError(t, err)

	// Child's pre_script wins
	assert.Equal(t, "local-pre.sh", h.PreScript)
	// Base's post_script fetched from remote
	assert.True(t, filepath.IsAbs(h.PostScript))

	// 1 base + 2 scripts: both are fetched BEFORE merge, so pre_script is
	// fetched even though the child overrides it afterward.
	require.Len(t, deps, 3)
}

func TestLoadWithBase_URLBase_ScriptNotInAllowlist(t *testing.T) {
	baseContent := []byte(`
agent: agents/triage.md
role: test
pre_script: scripts/pre.sh
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/harness/scripts/pre.sh": []byte("#!/bin/bash"),
	})

	hash := computeHash(baseContent)
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	baseURL := server.URL + "/harness/triage.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+baseURL+`
`)

	// Allowlist only covers /harness/triage.yaml, not /harness/scripts/
	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/harness/triage.yaml"},
	})
	// The allowlist check is prefix-based, so /harness/triage.yaml as prefix
	// does NOT cover /harness/scripts/pre.sh
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in allowed_remote_resources")
}

func TestLoadWithBase_URLBase_ScriptFetchFails(t *testing.T) {
	baseContent := []byte(`
agent: agents/triage.md
role: test
pre_script: scripts/missing.sh
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{})

	hash := computeHash(baseContent)
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	baseURL := server.URL + "/harness/triage.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+baseURL+`
`)

	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pre_script")
}

func TestLoadWithBase_URLBase_ScriptsOffline_NoCacheError(t *testing.T) {
	baseContent := []byte(`
agent: agents/triage.md
role: test
pre_script: scripts/pre.sh
`)
	hash := computeHash(baseContent)
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	// Pre-populate base harness in cache so it can be loaded offline
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/harness/triage.yaml", baseContent))

	baseURL := "https://example.com/harness/triage.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+baseURL+`
`)

	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
		OrgAllowlist:  []string{"https://example.com/"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "offline mode")
	assert.Contains(t, err.Error(), "fullsend lock")
}

func TestLoadWithBase_URLBase_ScriptsOffline_CacheHit(t *testing.T) {
	preScript := []byte("#!/bin/bash\necho cached-pre")

	baseContent := []byte(`
agent: agents/triage.md
role: test
pre_script: scripts/pre.sh
`)
	hash := computeHash(baseContent)
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	// Pre-populate base harness in cache
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/harness/triage.yaml", baseContent))
	// Pre-populate script in cache
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/harness/scripts/pre.sh", preScript))
	// Add URL index entry
	scriptHash := fetch.ComputeSHA256(preScript)
	require.NoError(t, urlIndexPut(cacheDir, "https://example.com/harness/scripts/pre.sh", scriptHash))

	baseURL := "https://example.com/harness/triage.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+baseURL+`
`)

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
		OrgAllowlist:  []string{"https://example.com/"},
	})
	require.NoError(t, err)

	assert.True(t, filepath.IsAbs(h.PreScript))
	content, err := os.ReadFile(h.PreScript)
	require.NoError(t, err)
	assert.Equal(t, preScript, content)

	// Both deps should be cache hits
	require.Len(t, deps, 2)
	assert.True(t, deps[0].CacheHit, "base should be cache hit")
	assert.True(t, deps[1].CacheHit, "script should be cache hit")
}

func TestLoadWithBase_URLBase_ScriptExecutablePermission(t *testing.T) {
	scriptContent := []byte("#!/bin/bash\necho executable")

	baseContent := []byte(`
agent: agents/triage.md
role: test
pre_script: scripts/pre.sh
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/harness/scripts/pre.sh": scriptContent,
	})

	hash := computeHash(baseContent)
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	baseURL := server.URL + "/harness/triage.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+baseURL+`
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
	})
	require.NoError(t, err)

	// Verify the cached script is executable
	info, err := os.Stat(h.PreScript)
	require.NoError(t, err)
	assert.True(t, info.Mode()&0o111 != 0, "cached script should be executable, got mode %o", info.Mode())
}

func TestLoadWithBase_URLBase_NoScripts_NoExtraFetches(t *testing.T) {
	baseContent := []byte(`
agent: agents/remote.md
role: test
model: sonnet
`)
	hash := computeHash(baseContent)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{})

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	baseURL := server.URL + "/harness/triage.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+baseURL+`
`)

	_, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
	})
	require.NoError(t, err)

	// Only 1 dep for the base itself — no scripts
	require.Len(t, deps, 1)
	assert.Equal(t, "base", deps[0].Field)
}

func TestLoadWithBase_URLBase_AuditLogForScripts(t *testing.T) {
	preScript := []byte("#!/bin/bash\necho pre")

	baseContent := []byte(`
agent: agents/triage.md
role: test
pre_script: scripts/pre.sh
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/harness/scripts/pre.sh": preScript,
	})

	hash := computeHash(baseContent)
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	auditLog := filepath.Join(dir, "audit.jsonl")
	baseURL := server.URL + "/harness/triage.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+baseURL+`
`)

	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
		AuditLogPath:  auditLog,
		TraceID:       "test-trace-123",
	})
	require.NoError(t, err)

	// Verify audit log was written
	auditData, err := os.ReadFile(auditLog)
	require.NoError(t, err)
	auditStr := string(auditData)
	assert.Contains(t, auditStr, "base_script")
	assert.Contains(t, auditStr, "test-trace-123")
	assert.Contains(t, auditStr, "scripts/pre.sh")
}

func TestLoadWithBase_URLBase_ForgeValidationLoopScriptFetched(t *testing.T) {
	forgeValidate := []byte("#!/bin/bash\necho forge-validate")

	baseContent := []byte(`
agent: agents/triage.md
role: test
forge:
  github:
    validation_loop:
      script: scripts/gh-validate.sh
      max_iterations: 2
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/harness/scripts/gh-validate.sh": forgeValidate,
	})

	hash := computeHash(baseContent)
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	baseURL := server.URL + "/harness/triage.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+baseURL+`
`)

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
		ForgePlatform: "github",
	})
	require.NoError(t, err)

	require.NotNil(t, h.ValidationLoop)
	assert.True(t, filepath.IsAbs(h.ValidationLoop.Script))
	assert.Equal(t, 2, h.ValidationLoop.MaxIterations)

	content, err := os.ReadFile(h.ValidationLoop.Script)
	require.NoError(t, err)
	assert.Equal(t, forgeValidate, content)

	// 1 base + 1 forge validation_loop script
	require.Len(t, deps, 2)
	assert.Equal(t, "forge.github.validation_loop.script", deps[1].Field)
}

func TestLoadWithBase_URLBase_AgentInputNotFetched(t *testing.T) {
	baseContent := []byte(`
agent: agents/triage.md
role: test
agent_input: data/input
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{})

	hash := computeHash(baseContent)
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	baseURL := server.URL + "/harness/triage.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+baseURL+`
`)

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
	})
	require.NoError(t, err)

	// agent_input is a directory at runtime — it is cleared from URL bases
	// to prevent the relative path resolving against the child's directory
	// where it won't exist.
	assert.Empty(t, h.AgentInput)

	// Only 1 dep for the base harness, no agent_input dep
	require.Len(t, deps, 1)
	assert.Equal(t, "base", deps[0].Field)
}

func TestLoadWithBase_URLBase_ForgeScriptFetchError(t *testing.T) {
	baseContent := []byte(`
agent: agents/triage.md
role: test
forge:
  github:
    pre_script: scripts/missing-forge.sh
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{})

	hash := computeHash(baseContent)
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	baseURL := server.URL + "/harness/triage.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+baseURL+`
`)

	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forge.github.pre_script")
}

func TestLoadWithBase_URLBase_AllScriptTypes(t *testing.T) {
	preScript := []byte("#!/bin/bash\necho pre")
	postScript := []byte("#!/bin/bash\necho post")
	validateScript := []byte("#!/bin/bash\necho validate")

	baseContent := []byte(`
agent: agents/triage.md
role: test
pre_script: scripts/pre.sh
post_script: scripts/post.sh
validation_loop:
  script: scripts/validate.sh
  max_iterations: 3
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/harness/scripts/pre.sh":      preScript,
		"/harness/scripts/post.sh":     postScript,
		"/harness/scripts/validate.sh": validateScript,
	})

	hash := computeHash(baseContent)
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	baseURL := server.URL + "/harness/triage.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+baseURL+`
`)

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
	})
	require.NoError(t, err)

	assert.True(t, filepath.IsAbs(h.PreScript))
	assert.True(t, filepath.IsAbs(h.PostScript))
	require.NotNil(t, h.ValidationLoop)
	assert.True(t, filepath.IsAbs(h.ValidationLoop.Script))

	// 1 base + 3 scripts (agent_input excluded — it's a directory)
	require.Len(t, deps, 4)
	depFields := map[string]bool{}
	for _, d := range deps[1:] {
		depFields[d.Field] = true
		assert.Equal(t, "script", d.Type)
	}
	assert.True(t, depFields["pre_script"])
	assert.True(t, depFields["post_script"])
	assert.True(t, depFields["validation_loop.script"])
}

func TestResolveBaseScripts_RejectsAbsolutePath(t *testing.T) {
	base := &Harness{PreScript: "/etc/passwd"}
	_, err := resolveBaseScripts(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a relative path, not an absolute path")
	assert.Contains(t, err.Error(), "pre_script")
}

func TestResolveBaseScripts_RejectsPathTraversal(t *testing.T) {
	base := &Harness{PostScript: "../../../etc/passwd"}
	_, err := resolveBaseScripts(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain path traversal")
	assert.Contains(t, err.Error(), "post_script")
}

func TestResolveBaseScripts_RejectsURLInScriptField(t *testing.T) {
	base := &Harness{PreScript: "https://evil.com/malware.sh"}
	_, err := resolveBaseScripts(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a relative path, not a URL")
}

func TestResolveBaseScripts_RejectsAbsoluteValidationLoopScript(t *testing.T) {
	base := &Harness{
		ValidationLoop: &ValidationLoop{Script: "/usr/bin/evil"},
	}
	_, err := resolveBaseScripts(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a relative path, not an absolute path")
	assert.Contains(t, err.Error(), "validation_loop.script")
}

func TestResolveBaseScripts_RejectsAbsoluteForgeScript(t *testing.T) {
	base := &Harness{
		Forge: map[string]*ForgeConfig{
			"github": {PreScript: "/usr/bin/evil"},
		},
	}
	_, err := resolveBaseScripts(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a relative path, not an absolute path")
	assert.Contains(t, err.Error(), "forge.github.pre_script")
}

func TestResolveBaseScripts_RejectsTraversalInForgeScript(t *testing.T) {
	base := &Harness{
		Forge: map[string]*ForgeConfig{
			"github": {PostScript: "../escape.sh"},
		},
	}
	_, err := resolveBaseScripts(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain path traversal")
	assert.Contains(t, err.Error(), "forge.github.post_script")
}

func TestResolveBaseScripts_RejectsAbsoluteForgeValidationLoop(t *testing.T) {
	base := &Harness{
		Forge: map[string]*ForgeConfig{
			"gitlab": {
				ValidationLoop: &ValidationLoop{Script: "/usr/bin/evil"},
			},
		},
	}
	_, err := resolveBaseScripts(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a relative path, not an absolute path")
	assert.Contains(t, err.Error(), "forge.gitlab.validation_loop.script")
}

func TestResolveBaseScripts_RejectsNullBytes(t *testing.T) {
	base := &Harness{PreScript: "scripts/pre\x00.sh"}
	_, err := resolveBaseScripts(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain null bytes")
}

func TestResolveBaseScripts_RejectsQueryMarker(t *testing.T) {
	base := &Harness{PreScript: "scripts/pre.sh?param=1"}
	_, err := resolveBaseScripts(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain query or fragment markers")
}

func TestResolveBaseScripts_RejectsFragmentMarker(t *testing.T) {
	base := &Harness{PostScript: "scripts/post.sh#anchor"}
	_, err := resolveBaseScripts(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain query or fragment markers")
}

func TestResolveBaseScripts_ClearsAgentInput(t *testing.T) {
	base := &Harness{AgentInput: "data/input"}
	deps, err := resolveBaseScripts(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.NoError(t, err)
	assert.Empty(t, base.AgentInput)
	assert.Empty(t, deps)
}

func TestValidateBaseScriptPath_AllowsDotsInFilename(t *testing.T) {
	err := validateBaseScriptPath("pre_script", "scripts/foo..bar.sh")
	assert.NoError(t, err)
}

func TestResolveBaseScripts_InvalidBaseURL(t *testing.T) {
	base := &Harness{PreScript: "scripts/pre.sh"}
	_, err := resolveBaseScripts(context.Background(), base, "not-a-valid-url", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot determine directory")
}

func TestURLIndexPut_EmptyWorkspaceRoot(t *testing.T) {
	err := urlIndexPut("", "https://example.com/script.sh", "abc123")
	assert.NoError(t, err)
}

func TestURLIndexLookup_EmptyWorkspaceRoot(t *testing.T) {
	hash, ok := urlIndexLookup("", "https://example.com/script.sh")
	assert.False(t, ok)
	assert.Empty(t, hash)
}

func TestLoadWithBase_RuntimeFetchFieldsNotInherited(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/test.md
role: test
allowed_remote_resources:
  - https://example.com/
allow_runtime_fetch: true
max_runtime_fetches: 50
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	assert.False(t, h.AllowRuntimeFetch)
	assert.Nil(t, h.MaxRuntimeFetches)
	assert.Empty(t, h.AllowedRemoteResources)
}
