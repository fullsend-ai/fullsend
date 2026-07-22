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
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/gitfetch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func computeHash(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}

func fakeTreeFetcher(files map[string][]byte) gitfetch.TreeFetchFunc {
	return func(_ context.Context, _, _, _, _ string) (map[string][]byte, error) {
		return files, nil
	}
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

	// Skills concatenated: base + child (no name collision)
	assert.Equal(t, []string{"skill-a", "skill-b", "skill-c"}, h.Skills)
}

// TestLoadWithBase_ChildSkillOverridesBaseByBasename verifies that a child
// skill whose directory basename matches a base skill replaces the base entry
// instead of producing a duplicate that trips duplicateDestinationNameError
// at bootstrap time (see #5408).
func TestLoadWithBase_ChildSkillOverridesBaseByBasename(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/test.md
role: test
skills:
  - /cache/sha256/abc123/code-implementation
  - /cache/sha256/def456/pr-review
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
skills:
  - skills/code-implementation
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	// Child's code-implementation replaces base's, pr-review stays
	require.Len(t, h.Skills, 2)
	assert.Equal(t, "skills/code-implementation", h.Skills[0])
	assert.Equal(t, "/cache/sha256/def456/pr-review", h.Skills[1])
}

// TestLoadWithBase_ChildSkillOverride_PreservesOrder verifies that when a
// child overrides multiple base skills, the merged list preserves base
// ordering for non-overridden entries and replaces overridden entries
// in-place.
func TestLoadWithBase_ChildSkillOverride_PreservesOrder(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/test.md
role: test
skills:
  - /cache/skill-a
  - /cache/skill-b
  - /cache/skill-c
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
skills:
  - local/skill-b
  - local/skill-d
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	// skill-b replaced in-place, skill-d appended
	assert.Equal(t, []string{
		"/cache/skill-a",
		"local/skill-b",
		"/cache/skill-c",
		"local/skill-d",
	}, h.Skills)
}

// TestMergeSkills verifies the mergeSkills helper directly.
func TestMergeSkills(t *testing.T) {
	tests := []struct {
		name  string
		base  []string
		child []string
		want  []string
	}{
		{
			name:  "no overlap appends",
			base:  []string{"/base/skill-a"},
			child: []string{"/child/skill-b"},
			want:  []string{"/base/skill-a", "/child/skill-b"},
		},
		{
			name:  "child overrides base by basename",
			base:  []string{"/base/skill-a", "/base/skill-b"},
			child: []string{"/child/skill-a"},
			want:  []string{"/child/skill-a", "/base/skill-b"},
		},
		{
			name:  "nil base",
			base:  nil,
			child: []string{"/child/skill-a"},
			want:  []string{"/child/skill-a"},
		},
		{
			name:  "nil child",
			base:  []string{"/base/skill-a"},
			child: nil,
			want:  []string{"/base/skill-a"},
		},
		{
			name:  "both nil",
			base:  nil,
			child: nil,
			want:  []string{},
		},
		{
			name:  "full override",
			base:  []string{"/cache/sha256/abc/code-implementation"},
			child: []string{"skills/code-implementation"},
			want:  []string{"skills/code-implementation"},
		},
		{
			name:  "duplicate child basename deduplicates",
			base:  []string{"/base/skill-a"},
			child: []string{"/child1/skill-b", "/child2/skill-b"},
			want:  []string{"/base/skill-a", "/child2/skill-b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeSkills(tt.base, tt.child)
			assert.Equal(t, tt.want, got)
		})
	}
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
	// Base provides model
	assert.Equal(t, "sonnet", h.Model)

	// Dependencies: 1 base + 1 agent resource
	require.Len(t, deps, 2)
	assert.Equal(t, "base", deps[0].Field)
	assert.Equal(t, server.URL+"/base.yaml", deps[0].URL)
	assert.Equal(t, hash, deps[0].SHA256)
	assert.Equal(t, "agent", deps[1].Field)
	assert.Equal(t, "resource", deps[1].Type)
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
`)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/grandparent.yaml" {
			w.WriteHeader(http.StatusOK)
			w.Write(grandparentContent)
		} else if r.URL.Path == "/parent.yaml" {
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
		} else if strings.HasPrefix(r.URL.Path, "/agents/") {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("# test resource"))
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

	// Dependencies: parent base + parent agent +
	// grandparent base + grandparent agent
	require.Len(t, deps, 4)
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
	// Pre-populate agent resource for resolveBaseResources
	agentContent := []byte("# test agent")
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/agents/remote.md", agentContent))
	agentHash := fetch.ComputeSHA256(agentContent)
	require.NoError(t, urlIndexPut(cacheDir, "https://example.com/agents/remote.md", agentHash))

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

	// Dependencies show cache hits
	require.Len(t, deps, 2)
	assert.True(t, deps[0].CacheHit)
	assert.True(t, deps[1].CacheHit, "agent resource should be cache hit")
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

func TestLoadWithBase_ProfilesConcat(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/base.md
role: test
openshell:
  profiles:
  - "https://github.com/org/repo/tree/main/profiles/claude-code.yaml#sha256=`+strings.Repeat("a", 64)+`"
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
openshell:
  profiles:
  - "https://github.com/org/repo/tree/main/profiles/vertex-ai.yaml#sha256=`+strings.Repeat("b", 64)+`"
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	require.Len(t, h.OpenShellProfiles(), 2)
	assert.Contains(t, h.OpenShellProfiles()[0], "claude-code")
	assert.Contains(t, h.OpenShellProfiles()[1], "vertex-ai")
}

func TestLoadWithBase_ProfilesChildOnlyInheritsBase(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/base.md
role: test
openshell:
  profiles:
  - "https://github.com/org/repo/tree/main/profiles/claude-code.yaml#sha256=`+strings.Repeat("a", 64)+`"
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	require.Len(t, h.OpenShellProfiles(), 1)
	assert.Contains(t, h.OpenShellProfiles()[0], "claude-code")
}

func TestLoadWithBase_ProfilesChildOnlyNoBase(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/base.md
role: test
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
openshell:
  profiles:
  - "https://github.com/org/repo/tree/main/profiles/vertex-ai.yaml#sha256=`+strings.Repeat("b", 64)+`"
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	require.Len(t, h.OpenShellProfiles(), 1)
	assert.Contains(t, h.OpenShellProfiles()[0], "vertex-ai")
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

func TestURLParentDirPrefix(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			"https://raw.githubusercontent.com/org/repo/sha/harness/triage.yaml#sha256=abc123",
			"https://raw.githubusercontent.com/org/repo/sha/",
		},
		{
			"https://example.com/path/to/file.yaml",
			"https://example.com/path/",
		},
		{
			// File at domain root: parent of "/" is still "/"
			"https://example.com/file.yaml#sha256=0000000000000000000000000000000000000000000000000000000000000000",
			"https://example.com/",
		},
		{
			"not-a-url",
			"",
		},
	}
	for _, tt := range tests {
		got := urlParentDirPrefix(tt.input)
		assert.Equal(t, tt.want, got, "urlParentDirPrefix(%q)", tt.input)
	}
}

func setupScriptTestServer(t *testing.T, harnessContent []byte, files map[string][]byte) (*httptest.Server, fetch.FetchPolicy) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/harness/triage.yaml" {
			w.WriteHeader(http.StatusOK)
			w.Write(harnessContent)
			return
		}
		if content, ok := files[r.URL.Path]; ok {
			w.WriteHeader(http.StatusOK)
			w.Write(content)
			return
		}
		// Serve default content for declarative resource paths so
		// resolveBaseResources succeeds in tests focused on scripts.
		if strings.HasPrefix(r.URL.Path, "/agents/") ||
			strings.HasPrefix(r.URL.Path, "/policies/") ||
			strings.HasSuffix(r.URL.Path, "/SKILL.md") {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("# test resource"))
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

	// Scripts at /scripts/ (sibling to /harness/), matching real scaffold layout.
	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/scripts/pre.sh":  preScript,
		"/scripts/post.sh": postScript,
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

	// Dependencies: 1 base + 2 scripts + 1 agent resource
	require.Len(t, deps, 4)
	assert.Equal(t, "base", deps[0].Field)
	scriptFields := map[string]bool{}
	for _, d := range deps[1:] {
		if d.Type == "script" {
			scriptFields[d.Field] = true
			assert.False(t, d.CacheHit)
		}
	}
	assert.True(t, scriptFields["pre_script"])
	assert.True(t, scriptFields["post_script"])
	assert.Equal(t, "agent", deps[3].Field)
	assert.Equal(t, "resource", deps[3].Type)
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
		"/scripts/validate.sh": validateScript,
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

	// 1 base + 1 validation script + 1 agent resource
	require.Len(t, deps, 3)
	assert.Equal(t, "validation_loop.script", deps[1].Field)
	assert.Equal(t, "script", deps[1].Type)
	assert.Equal(t, "agent", deps[2].Field)
	assert.Equal(t, "resource", deps[2].Type)
}

func TestLoadWithBase_URLBase_ValidationLoopSchemaFetched(t *testing.T) {
	validateScript := []byte("#!/bin/bash\necho validate")
	schemaContent := []byte(`{"type":"object","properties":{"action":{"type":"string"}}}`)

	baseContent := []byte(`
agent: agents/triage.md
role: test
validation_loop:
  script: scripts/validate.sh
  schema: schemas/result.schema.json
  max_iterations: 2
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/scripts/validate.sh":        validateScript,
		"/schemas/result.schema.json": schemaContent,
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
	assert.True(t, filepath.IsAbs(h.ValidationLoop.Schema))
	assert.Equal(t, 2, h.ValidationLoop.MaxIterations)

	content, err := os.ReadFile(h.ValidationLoop.Schema)
	require.NoError(t, err)
	assert.Equal(t, schemaContent, content)

	// 1 base + 1 validation script + 1 schema + 1 agent resource
	require.Len(t, deps, 4)
	assert.Equal(t, "validation_loop.script", deps[1].Field)
	assert.Equal(t, "script", deps[1].Type)
	assert.Equal(t, "validation_loop.schema", deps[2].Field)
	assert.Equal(t, "resource", deps[2].Type)
	assert.Equal(t, "agent", deps[3].Field)
	assert.Equal(t, "resource", deps[3].Type)
}

func TestLoadWithBase_URLBase_ValidationLoopSchemaFetchError(t *testing.T) {
	validateScript := []byte("#!/bin/bash\necho validate")

	baseContent := []byte(`
agent: agents/triage.md
role: test
validation_loop:
  script: scripts/validate.sh
  schema: schemas/missing.schema.json
  max_iterations: 2
`)

	// The server does NOT serve /schemas/missing.schema.json
	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/scripts/validate.sh": validateScript,
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

	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validation_loop.schema")
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
		"/scripts/gh-pre.sh":  forgePre,
		"/scripts/gh-post.sh": forgePost,
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

	// 1 base + 2 forge scripts + 1 agent resource
	require.Len(t, deps, 4)
	for _, d := range deps[1:3] {
		assert.Equal(t, "script", d.Type)
		assert.Contains(t, d.Field, "forge.github.")
	}
	assert.Equal(t, "agent", deps[3].Field)
	assert.Equal(t, "resource", deps[3].Type)
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
		"/scripts/base-pre.sh":  preScript,
		"/scripts/base-post.sh": postScript,
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

	// 1 base + 2 scripts + 1 agent resource: all are fetched BEFORE merge,
	// so pre_script is fetched even though the child overrides it afterward.
	require.Len(t, deps, 4)
}

func TestLoadWithBase_URLBase_ScriptNotInAllowlist(t *testing.T) {
	baseContent := []byte(`
agent: agents/triage.md
role: test
pre_script: scripts/pre.sh
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/scripts/pre.sh": []byte("#!/bin/bash"),
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

	// Allowlist only covers /harness/triage.yaml, not /scripts/
	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/harness/triage.yaml"},
	})
	// The allowlist check is prefix-based, so /harness/triage.yaml as prefix
	// does NOT cover /scripts/pre.sh
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
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/scripts/pre.sh", preScript))
	// Add URL index entry for script
	scriptHash := fetch.ComputeSHA256(preScript)
	require.NoError(t, urlIndexPut(cacheDir, "https://example.com/scripts/pre.sh", scriptHash))
	// Pre-populate agent resource for resolveBaseResources
	agentRes := []byte("# test agent")
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/agents/triage.md", agentRes))
	agentResHash := fetch.ComputeSHA256(agentRes)
	require.NoError(t, urlIndexPut(cacheDir, "https://example.com/agents/triage.md", agentResHash))

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

	// All deps should be cache hits
	require.Len(t, deps, 3)
	assert.True(t, deps[0].CacheHit, "base should be cache hit")
	assert.True(t, deps[1].CacheHit, "script should be cache hit")
	assert.True(t, deps[2].CacheHit, "agent resource should be cache hit")
}

func TestLoadWithBase_URLBase_ScriptExecutablePermission(t *testing.T) {
	scriptContent := []byte("#!/bin/bash\necho executable")

	baseContent := []byte(`
agent: agents/triage.md
role: test
pre_script: scripts/pre.sh
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/scripts/pre.sh": scriptContent,
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

	// 1 base + 1 agent resource (no scripts)
	require.Len(t, deps, 2)
	assert.Equal(t, "base", deps[0].Field)
	assert.Equal(t, "agent", deps[1].Field)
	assert.Equal(t, "resource", deps[1].Type)
}

func TestLoadWithBase_URLBase_AuditLogForScripts(t *testing.T) {
	preScript := []byte("#!/bin/bash\necho pre")

	baseContent := []byte(`
agent: agents/triage.md
role: test
pre_script: scripts/pre.sh
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/scripts/pre.sh": preScript,
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
		"/scripts/gh-validate.sh": forgeValidate,
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

	// 1 base + 1 forge validation_loop script + 1 agent resource
	require.Len(t, deps, 3)
	assert.Equal(t, "forge.github.validation_loop.script", deps[1].Field)
}

func TestLoadWithBase_URLBase_ForgeValidationLoopSchemaFetched(t *testing.T) {
	forgeValidate := []byte("#!/bin/bash\necho forge-validate")
	schemaContent := []byte(`{"type":"object"}`)

	baseContent := []byte(`
agent: agents/triage.md
role: test
forge:
  github:
    validation_loop:
      script: scripts/gh-validate.sh
      schema: schemas/result.schema.json
      max_iterations: 2
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/scripts/gh-validate.sh":     forgeValidate,
		"/schemas/result.schema.json": schemaContent,
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
	assert.True(t, filepath.IsAbs(h.ValidationLoop.Schema))

	content, err := os.ReadFile(h.ValidationLoop.Schema)
	require.NoError(t, err)
	assert.Equal(t, schemaContent, content)

	// 1 base + 1 forge validation_loop script + 1 forge schema + 1 agent resource
	require.Len(t, deps, 4)
	assert.Equal(t, "forge.github.validation_loop.script", deps[1].Field)
	assert.Equal(t, "forge.github.validation_loop.schema", deps[2].Field)
	assert.Equal(t, "resource", deps[2].Type)
}

func TestLoadWithBase_URLBase_ForgeValidationLoopSchemaFetchError(t *testing.T) {
	forgeValidate := []byte("#!/bin/bash\necho forge-validate")

	baseContent := []byte(`
agent: agents/triage.md
role: test
forge:
  github:
    validation_loop:
      script: scripts/gh-validate.sh
      schema: schemas/missing.schema.json
      max_iterations: 2
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/scripts/gh-validate.sh": forgeValidate,
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

	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
		ForgePlatform: "github",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validation_loop.schema")
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

	// 1 base + 1 agent resource, no agent_input dep
	require.Len(t, deps, 2)
	assert.Equal(t, "base", deps[0].Field)
	assert.Equal(t, "agent", deps[1].Field)
	assert.Equal(t, "resource", deps[1].Type)
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
		"/scripts/pre.sh":      preScript,
		"/scripts/post.sh":     postScript,
		"/scripts/validate.sh": validateScript,
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

	// 1 base + 3 scripts + 1 agent resource
	require.Len(t, deps, 5)
	depFields := map[string]bool{}
	for _, d := range deps[1:] {
		if d.Type == "script" {
			depFields[d.Field] = true
		}
	}
	assert.True(t, depFields["pre_script"])
	assert.True(t, depFields["post_script"])
	assert.True(t, depFields["validation_loop.script"])
	assert.Equal(t, "agent", deps[4].Field)
	assert.Equal(t, "resource", deps[4].Type)
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

func TestResolveBaseScripts_RejectsTraversalInValidationLoopSchema(t *testing.T) {
	base := &Harness{
		ValidationLoop: &ValidationLoop{
			Schema: "../../../etc/shadow",
		},
	}
	_, err := resolveBaseScripts(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain path traversal")
	assert.Contains(t, err.Error(), "validation_loop.schema")
}

func TestResolveBaseScripts_RejectsTraversalInForgeValidationLoopSchema(t *testing.T) {
	base := &Harness{
		Forge: map[string]*ForgeConfig{
			"github": {
				ValidationLoop: &ValidationLoop{
					Schema: "../escape.json",
				},
			},
		},
	}
	_, err := resolveBaseScripts(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain path traversal")
	assert.Contains(t, err.Error(), "forge.github.validation_loop.schema")
}

func TestResolveBaseScripts_RejectsAbsoluteValidationLoopSchema(t *testing.T) {
	base := &Harness{
		ValidationLoop: &ValidationLoop{
			Schema: "/etc/schema.json",
		},
	}
	_, err := resolveBaseScripts(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a relative path, not an absolute path")
	assert.Contains(t, err.Error(), "validation_loop.schema")
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

func TestValidateBaseRelPath_AllowsDotsInFilename(t *testing.T) {
	err := validateBaseRelPath("pre_script", "scripts/foo..bar.sh")
	assert.NoError(t, err)
}

func TestResolveBaseScripts_InvalidBaseURL(t *testing.T) {
	base := &Harness{PreScript: "scripts/pre.sh"}
	_, err := resolveBaseScripts(context.Background(), base, "not-a-valid-url", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot determine directory")
}

// TestLoadWithBase_URLBase_ScriptsRelativeToScaffoldRoot verifies that URL
// base script resolution matches local resolution: scripts are relative to
// the scaffold root (parent of harness/), not to the YAML file's directory.
// This mirrors the real scaffold layout where harness/ and scripts/ are siblings.
func TestLoadWithBase_URLBase_ScriptsRelativeToScaffoldRoot(t *testing.T) {
	preScript := []byte("#!/bin/bash\necho pre")
	postScript := []byte("#!/bin/bash\necho post")

	baseContent := []byte(`
agent: agents/triage.md
role: test
model: opus
pre_script: scripts/pre.sh
post_script: scripts/post.sh
`)

	// Mount scripts at /scripts/ (sibling to /harness/), matching real layout.
	// The YAML lives at /harness/triage.yaml, so urlDirPrefix gives /harness/.
	// Scripts should resolve relative to / (the scaffold root), not /harness/.
	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/scripts/pre.sh":  preScript,
		"/scripts/post.sh": postScript,
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

	// Scripts resolved to local cache paths
	assert.NotEmpty(t, h.PreScript, "pre_script should be resolved")
	assert.NotEmpty(t, h.PostScript, "post_script should be resolved")
	assert.True(t, filepath.IsAbs(h.PreScript), "pre_script should be absolute cache path")
	assert.True(t, filepath.IsAbs(h.PostScript), "post_script should be absolute cache path")

	// Verify cached content matches
	preContent, err := os.ReadFile(h.PreScript)
	require.NoError(t, err)
	assert.Equal(t, preScript, preContent)

	postContent, err := os.ReadFile(h.PostScript)
	require.NoError(t, err)
	assert.Equal(t, postScript, postContent)

	// Dependencies: 1 base + 2 scripts + 1 agent resource
	require.Len(t, deps, 4)
}

func TestLoadWithBase_URLBase_AgentAndPolicyFetched(t *testing.T) {
	baseContent := []byte(`
agent: agents/triage.md
policy: policies/sandbox.yaml
role: test
model: sonnet
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

	// Child overrides agent, base policy is inherited
	assert.Equal(t, "agents/child.md", h.Agent)
	assert.True(t, filepath.IsAbs(h.Policy), "policy should be resolved to cache path")

	// 1 base + 1 agent resource + 1 policy resource
	require.Len(t, deps, 3)
	assert.Equal(t, "base", deps[0].Field)

	resourceFields := map[string]string{}
	for _, d := range deps[1:] {
		resourceFields[d.Field] = d.Type
	}
	assert.Equal(t, "resource", resourceFields["agent"])
	assert.Equal(t, "resource", resourceFields["policy"])
}

func TestLoadWithBase_URLBase_SkillFetchedAndCachedAsDir(t *testing.T) {
	skillContent := []byte("# Test skill\nThis is a test skill.")

	baseContent := []byte(`
agent: agents/triage.md
role: test
skills:
  - skills/triage-labels
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/skills/triage-labels/SKILL.md": skillContent,
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

	// The test server URL is not a raw.githubusercontent.com URL, so the
	// forge URL parser cannot extract a clone URL and skill resolution
	// errors out.
	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a raw.githubusercontent.com URL")
}

func TestLoadWithBase_URLBase_SkillOfflineCacheHit(t *testing.T) {
	skillContent := []byte("# Cached skill")

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	baseContent := []byte(`
agent: agents/triage.md
role: test
skills:
  - skills/common
`)
	hash := computeHash(baseContent)

	// Pre-populate base in cache
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/harness/triage.yaml", baseContent))

	// Pre-populate agent resource
	agentRes := []byte("# triage agent")
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/agents/triage.md", agentRes))
	require.NoError(t, urlIndexPut(cacheDir, "https://example.com/agents/triage.md", fetch.ComputeSHA256(agentRes)))

	// Pre-populate the skill SKILL.md in cache and URL index
	skillFileURL := "https://example.com/skills/common/SKILL.md"
	require.NoError(t, fetch.CachePut(cacheDir, skillFileURL, skillContent))
	skillFileHash := fetch.ComputeSHA256(skillContent)
	require.NoError(t, urlIndexPut(cacheDir, skillFileURL, skillFileHash))

	// Cache the skill directory tree
	files := map[string][]byte{"SKILL.md": skillContent}
	treeHash, err := fetch.CachePutDir(cacheDir, skillFileURL, files)
	require.NoError(t, err)
	require.NoError(t, urlIndexPut(cacheDir, "skill:"+skillFileURL, treeHash))

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

	// Skill resolved from cache
	require.Len(t, h.Skills, 1)
	assert.True(t, filepath.IsAbs(h.Skills[0]))

	// Verify content from cache
	cachedSkillMD := filepath.Join(h.Skills[0], "SKILL.md")
	content, err := os.ReadFile(cachedSkillMD)
	require.NoError(t, err)
	assert.Equal(t, skillContent, content)

	// 1 base + 1 agent + 1 skill, all cache hits
	require.Len(t, deps, 3)
	assert.True(t, deps[0].CacheHit, "base should be cache hit")
	assert.True(t, deps[1].CacheHit, "agent should be cache hit")
	assert.True(t, deps[2].CacheHit, "skill should be cache hit")
	assert.Equal(t, "directory", deps[2].Type)
}

func TestLoadWithBase_URLBase_ResourceOfflineCacheHit(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	baseContent := []byte(`
agent: agents/triage.md
policy: policies/sandbox.yaml
role: test
`)
	hash := computeHash(baseContent)

	// Pre-populate base
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/harness/triage.yaml", baseContent))

	// Pre-populate agent resource
	agentContent := []byte("You are a triage agent.")
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/agents/triage.md", agentContent))
	require.NoError(t, urlIndexPut(cacheDir, "https://example.com/agents/triage.md", fetch.ComputeSHA256(agentContent)))

	// Pre-populate policy resource
	policyContent := []byte("deny: all")
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/policies/sandbox.yaml", policyContent))
	require.NoError(t, urlIndexPut(cacheDir, "https://example.com/policies/sandbox.yaml", fetch.ComputeSHA256(policyContent)))

	baseURL := "https://example.com/harness/triage.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/local.md
role: test
base: `+baseURL+`
`)

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
		OrgAllowlist:  []string{"https://example.com/"},
	})
	require.NoError(t, err)

	// Child overrides agent, base policy is resolved from cache
	assert.Equal(t, "agents/local.md", h.Agent)
	assert.True(t, filepath.IsAbs(h.Policy))

	// Verify policy content from cache
	policyData, err := os.ReadFile(h.Policy)
	require.NoError(t, err)
	assert.Equal(t, policyContent, policyData)

	// 1 base + 1 agent + 1 policy, all cache hits
	require.Len(t, deps, 3)
	for _, d := range deps {
		assert.True(t, d.CacheHit, "%s should be cache hit", d.Field)
	}
}

func TestLoadWithBase_URLBase_SkillNotInAllowlist(t *testing.T) {
	baseContent := []byte(`
agent: agents/triage.md
role: test
skills:
  - skills/restricted
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

	// Allowlist only covers /harness/ and /agents/ — skills at /skills/ not covered
	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/harness/", server.URL + "/agents/"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in allowed_remote_resources")
	assert.Contains(t, err.Error(), "skills[0]")
}

func TestLoadWithBase_URLBase_SkillOfflineNoCache(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	baseContent := []byte(`
agent: agents/triage.md
role: test
skills:
  - skills/uncached
`)
	hash := computeHash(baseContent)

	// Pre-populate base and agent, but NOT the skill
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/harness/triage.yaml", baseContent))
	agentRes := []byte("# agent")
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/agents/triage.md", agentRes))
	require.NoError(t, urlIndexPut(cacheDir, "https://example.com/agents/triage.md", fetch.ComputeSHA256(agentRes)))

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
	assert.Contains(t, err.Error(), "skills[0]")
}

func TestResolveBaseResources_SkipsURLAndAbsFields(t *testing.T) {
	base := &Harness{
		Agent:  "https://example.com/agents/remote.md",
		Policy: "/absolute/path/policy.yaml",
		Skills: []string{"https://example.com/skills/foo"},
	}
	deps, err := resolveBaseResources(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.NoError(t, err)

	// URL and absolute path fields are skipped — no deps, no modification
	assert.Empty(t, deps)
	assert.Equal(t, "https://example.com/agents/remote.md", base.Agent)
	assert.Equal(t, "/absolute/path/policy.yaml", base.Policy)
	assert.Equal(t, "https://example.com/skills/foo", base.Skills[0])
}

func TestResolveBaseResources_RejectsAbsolutePath(t *testing.T) {
	base := &Harness{Agent: "/etc/passwd"}
	_, err := resolveBaseResources(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.NoError(t, err)
	// Absolute paths are skipped (not an error — they may be set by earlier resolution)
	assert.Equal(t, "/etc/passwd", base.Agent)
}

func TestResolveBaseResources_RejectsPathTraversal(t *testing.T) {
	base := &Harness{Agent: "../../etc/passwd"}
	_, err := resolveBaseResources(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain path traversal")
	assert.Contains(t, err.Error(), "agent")
}

func TestResolveBaseResources_RejectsNullBytesInPolicy(t *testing.T) {
	base := &Harness{Policy: "policies/test\x00.yaml"}
	_, err := resolveBaseResources(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain null bytes")
	assert.Contains(t, err.Error(), "policy")
}

func TestResolveBaseResources_RejectsTraversalInSkill(t *testing.T) {
	base := &Harness{Skills: []string{"../escape"}}
	_, err := resolveBaseResources(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain path traversal")
	assert.Contains(t, err.Error(), "skills[0]")
}

func TestLoadWithBase_URLBase_ResourceNotInAllowlist(t *testing.T) {
	baseContent := []byte(`
agent: agents/triage.md
role: test
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{})

	hash := computeHash(baseContent)
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	baseURL := server.URL + "/harness/triage.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
role: test
base: `+baseURL+`
`)

	// Allowlist only covers /harness/ — agent at /agents/ is not covered
	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/harness/"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in allowed_remote_resources")
	assert.Contains(t, err.Error(), "agent")
}

func TestLoadWithBase_URLBase_ResourceAuditLog(t *testing.T) {
	baseContent := []byte(`
agent: agents/triage.md
policy: policies/sandbox.yaml
role: test
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{})

	hash := computeHash(baseContent)
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	auditLog := filepath.Join(dir, "audit.jsonl")
	baseURL := server.URL + "/harness/triage.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
role: test
base: `+baseURL+`
`)

	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
		AuditLogPath:  auditLog,
		TraceID:       "resource-audit-test",
	})
	require.NoError(t, err)

	auditData, err := os.ReadFile(auditLog)
	require.NoError(t, err)
	auditStr := string(auditData)
	assert.Contains(t, auditStr, "base_resource")
	assert.Contains(t, auditStr, "resource-audit-test")
	assert.Contains(t, auditStr, "agents/triage.md")
	assert.Contains(t, auditStr, "policies/sandbox.yaml")
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

func brokenAuditPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "notadir")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	return filepath.Join(blocker, "audit.jsonl")
}

func TestFetchBaseFile_CacheHit_AuditError(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	content := []byte("# agent")
	fileURL := "https://example.com/agents/triage.md"
	require.NoError(t, fetch.CachePut(cacheDir, fileURL, content))
	hash := fetch.ComputeSHA256(content)
	require.NoError(t, urlIndexPut(cacheDir, fileURL, hash))

	_, _, err := fetchBaseFile(context.Background(), "agent", "https://example.com/",
		"agents/triage.md", []string{"https://example.com/"}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			AuditLogPath:  brokenAuditPath(t),
		}, "resource", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "audit")
}

func TestFetchBaseFile_OnlineFetch_AuditError(t *testing.T) {
	content := []byte("# agent")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	t.Cleanup(server.Close)

	policy := fetch.NewTestPolicy(
		server.Client().Transport.(*http.Transport).TLSClientConfig,
		[]string{"127.0.0.1"},
		[]string{server.Listener.Addr().String()[len("127.0.0.1:"):]},
	)

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	_, _, err := fetchBaseFile(context.Background(), "agent", server.URL+"/",
		"agents/triage.md", []string{server.URL + "/"}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			FetchPolicy:   policy,
			AuditLogPath:  brokenAuditPath(t),
		}, "resource", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "audit")
}

func TestFetchBaseFile_FetchURLError(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	policy := fetch.NewTestPolicy(
		server.Client().Transport.(*http.Transport).TLSClientConfig,
		[]string{"127.0.0.1"},
		[]string{server.Listener.Addr().String()[len("127.0.0.1:"):]},
	)

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	_, _, err := fetchBaseFile(context.Background(), "agent", server.URL+"/",
		"agents/triage.md", []string{server.URL + "/"}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			FetchPolicy:   policy,
		}, "resource", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching")
}

func TestFetchBaseSkill_CacheHit_AuditError(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	skillContent := []byte("# skill")
	skillFileURL := "https://example.com/skills/common/SKILL.md"

	require.NoError(t, fetch.CachePut(cacheDir, skillFileURL, skillContent))
	fileHash := fetch.ComputeSHA256(skillContent)
	require.NoError(t, urlIndexPut(cacheDir, skillFileURL, fileHash))

	files := map[string][]byte{"SKILL.md": skillContent}
	treeHash, err := fetch.CachePutDir(cacheDir, skillFileURL, files)
	require.NoError(t, err)
	require.NoError(t, urlIndexPut(cacheDir, "skill:"+skillFileURL, treeHash))

	_, _, err = fetchBaseSkill(context.Background(), "skills[0]", "https://example.com/",
		"skills/common", []string{"https://example.com/"}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			FetchPolicy:   fetch.FetchPolicy{Offline: true},
			AuditLogPath:  brokenAuditPath(t),
		})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "audit")
}

func TestFetchBaseSkill_CacheHit_UsesSkillNameNotTree(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	skillFileURL := "https://raw.githubusercontent.com/org/repo/ref1/skills/pr-review/SKILL.md"
	allowlist := []string{"https://raw.githubusercontent.com/org/repo/"}

	files := map[string][]byte{"SKILL.md": []byte("# PR Review")}
	treeHash, err := fetch.CachePutDir(cacheDir, skillFileURL, files, fetch.DirCachePutOpts{FullListing: true})
	require.NoError(t, err)
	require.NoError(t, urlIndexPut(cacheDir, skillFileURL, treeHash))
	require.NoError(t, urlIndexPut(cacheDir, "skill:"+skillFileURL, treeHash))

	dep, localDir, err := fetchBaseSkill(context.Background(), "skills[0]",
		"https://raw.githubusercontent.com/org/repo/ref1/",
		"skills/pr-review", allowlist, ComposeOpts{
			WorkspaceRoot: cacheDir,
			FetchPolicy:   fetch.FetchPolicy{Offline: true},
		})
	require.NoError(t, err)
	assert.True(t, dep.CacheHit)
	assert.Equal(t, "pr-review", filepath.Base(localDir), "cache-hit path should return skill name, not 'tree'")
	assert.FileExists(t, filepath.Join(localDir, "SKILL.md"))
}

func TestFetchBaseSkill_DefaultTreeFetcherUsed(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	// With no TreeFetcher set, the default gitfetch.FetchTree is used.
	// It will fail because there's no real repo, but the error proves
	// the default fetcher was invoked.
	_, _, err := fetchBaseSkill(context.Background(), "skills[0]",
		"https://raw.githubusercontent.com/org/repo/ref/",
		"skills/common", []string{"https://raw.githubusercontent.com/org/repo/"}, ComposeOpts{
			WorkspaceRoot: cacheDir,
		})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching skill directory")
}

func TestFetchBaseSkill_TreeFetchError(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	failFetcher := func(_ context.Context, _, _, _, _ string) (map[string][]byte, error) {
		return nil, fmt.Errorf("git fetch failed")
	}

	_, _, err := fetchBaseSkill(context.Background(), "skills[0]",
		"https://raw.githubusercontent.com/org/repo/ref/",
		"skills/common", []string{"https://raw.githubusercontent.com/org/repo/"}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   failFetcher,
		})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching skill directory")
}

func TestFetchBaseSkill_PartialIndexHit_RefetchesViaTreeFetcher(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	skillFileURL := "https://raw.githubusercontent.com/org/repo/ref/skills/common/SKILL.md"
	content := []byte("# skill")
	require.NoError(t, fetch.CachePut(cacheDir, skillFileURL, content))
	fileHash := fetch.ComputeSHA256(content)
	require.NoError(t, urlIndexPut(cacheDir, skillFileURL, fileHash))
	// Deliberately omit the "skill:" tree hash entry to trigger partial index hit

	fetcher := fakeTreeFetcher(map[string][]byte{"SKILL.md": []byte("# skill")})

	dep, _, err := fetchBaseSkill(context.Background(), "skills[0]",
		"https://raw.githubusercontent.com/org/repo/ref/",
		"skills/common", []string{"https://raw.githubusercontent.com/org/repo/"}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   fetcher,
		})
	require.NoError(t, err)
	assert.False(t, dep.CacheHit)
}

func TestResolveBaseResources_InvalidBaseURL(t *testing.T) {
	base := &Harness{Agent: "agents/test.md"}
	_, err := resolveBaseResources(context.Background(), base, "", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot determine directory")
}

func TestFetchBaseSkill_AuditError(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	fetcher := fakeTreeFetcher(map[string][]byte{"SKILL.md": []byte("# skill")})

	_, _, err := fetchBaseSkill(context.Background(), "skills[0]",
		"https://raw.githubusercontent.com/org/repo/ref/",
		"skills/common", []string{"https://raw.githubusercontent.com/org/repo/"}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   fetcher,
			AuditLogPath:  brokenAuditPath(t),
		})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "audit")
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

func TestMergeBaseIntoChild_Env(t *testing.T) {
	base := &Harness{
		Env: &EnvConfig{
			Runner:  map[string]string{"BASE_R": "r1"},
			Sandbox: map[string]string{"BASE_S": "s1"},
		},
	}
	child := &Harness{
		Env: &EnvConfig{
			Sandbox: map[string]string{"CHILD_S": "s2"},
		},
	}

	mergeBaseIntoChild(base, child)

	require.NotNil(t, child.Env)
	assert.Equal(t, "r1", child.Env.Runner["BASE_R"])
	assert.Equal(t, "s1", child.Env.Sandbox["BASE_S"])
	assert.Equal(t, "s2", child.Env.Sandbox["CHILD_S"])
}

func TestMergeBaseIntoChild_EnvChildWins(t *testing.T) {
	base := &Harness{
		Env: &EnvConfig{
			Runner: map[string]string{"KEY": "base"},
		},
	}
	child := &Harness{
		Env: &EnvConfig{
			Runner: map[string]string{"KEY": "child"},
		},
	}

	mergeBaseIntoChild(base, child)
	assert.Equal(t, "child", child.Env.Runner["KEY"])
}

func TestMergeBaseIntoChild_EnvInheritedWhenChildNil(t *testing.T) {
	base := &Harness{
		Env: &EnvConfig{
			Runner:  map[string]string{"R": "val"},
			Sandbox: map[string]string{"S": "val"},
		},
	}
	child := &Harness{}

	mergeBaseIntoChild(base, child)

	require.NotNil(t, child.Env)
	assert.Equal(t, "val", child.Env.Runner["R"])
	assert.Equal(t, "val", child.Env.Sandbox["S"])
}

func TestFetchBaseSkill_FullDirectory(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	fetcher := fakeTreeFetcher(map[string][]byte{
		"SKILL.md":                  []byte("# PR Review Skill"),
		"meta-prompt.md":            []byte("meta prompt content"),
		"sub-agents/code-review.md": []byte("sub-agent content"),
	})

	baseURLDir := "https://raw.githubusercontent.com/fullsend-ai/fullsend/abc123/"
	allowlist := []string{"https://raw.githubusercontent.com/fullsend-ai/fullsend/"}

	dep, localDir, err := fetchBaseSkill(context.Background(), "skills[0]", baseURLDir,
		"skills/pr-review", allowlist, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   fetcher,
		})
	require.NoError(t, err)

	assert.NotEmpty(t, localDir)
	assert.Equal(t, "directory", dep.Type)
	assert.Empty(t, dep.Warning)
	assert.False(t, dep.CacheHit)
	assert.Equal(t, "pr-review", filepath.Base(localDir), "fresh-fetch path should return skill name, not 'tree'")

	// Verify all companion files exist in the cached directory.
	skillMD, err := os.ReadFile(filepath.Join(localDir, "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "# PR Review Skill", string(skillMD))

	metaPrompt, err := os.ReadFile(filepath.Join(localDir, "meta-prompt.md"))
	require.NoError(t, err)
	assert.Equal(t, "meta prompt content", string(metaPrompt))

	subAgent, err := os.ReadFile(filepath.Join(localDir, "sub-agents", "code-review.md"))
	require.NoError(t, err)
	assert.Equal(t, "sub-agent content", string(subAgent))
}

func TestFetchBaseSkill_ParseError(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	_, _, err := fetchBaseSkill(context.Background(), "skills[0]",
		"https://example.com/",
		"skills/common", []string{"https://example.com/"}, ComposeOpts{
			WorkspaceRoot: cacheDir,
		})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing raw URL")
}

func TestFetchBaseSkill_NoSKILLMD(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	fetcher := fakeTreeFetcher(map[string][]byte{
		"meta-prompt.md": []byte("no skill file"),
	})

	_, _, err := fetchBaseSkill(context.Background(), "skills[0]",
		"https://raw.githubusercontent.com/org/repo/ref1/",
		"skills/broken", []string{"https://raw.githubusercontent.com/org/repo/"}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   fetcher,
		})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no SKILL.md")
}

func TestFetchBaseSkill_TreeFetchPartialError(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	failFetcher := func(_ context.Context, _, _, _, _ string) (map[string][]byte, error) {
		return nil, fmt.Errorf("failed to read meta-prompt.md: permission denied")
	}

	allowlist := []string{"https://raw.githubusercontent.com/org/repo/"}

	_, _, err := fetchBaseSkill(context.Background(), "skills[0]",
		"https://raw.githubusercontent.com/org/repo/ref1/",
		"skills/common", allowlist, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   failFetcher,
		})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching skill directory")
	assert.Contains(t, err.Error(), "meta-prompt.md")
}

func TestFetchBaseSkill_StaleCacheInvalidation(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	baseURLDir := "https://raw.githubusercontent.com/org/repo/ref1/"
	skillFileURL := baseURLDir + "skills/common/SKILL.md"
	allowlist := []string{"https://raw.githubusercontent.com/org/repo/"}

	// Pre-populate cache with a v0.22.0-style single-file entry (no FullListing).
	oldFiles := map[string][]byte{"SKILL.md": []byte("# old")}
	oldHash, err := fetch.CachePutDir(cacheDir, skillFileURL, oldFiles)
	require.NoError(t, err)
	require.NoError(t, urlIndexPut(cacheDir, skillFileURL, oldHash))
	require.NoError(t, urlIndexPut(cacheDir, "skill:"+skillFileURL, oldHash))

	fetcher := fakeTreeFetcher(map[string][]byte{
		"SKILL.md":       []byte("# new skill"),
		"meta-prompt.md": []byte("meta"),
	})

	dep, localDir, err := fetchBaseSkill(context.Background(), "skills[0]",
		baseURLDir, "skills/common", allowlist, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   fetcher,
		})
	require.NoError(t, err)
	assert.False(t, dep.CacheHit, "stale cache should be bypassed")

	// Verify both files exist.
	assert.FileExists(t, filepath.Join(localDir, "SKILL.md"))
	assert.FileExists(t, filepath.Join(localDir, "meta-prompt.md"))

	// Second call should hit cache (FullListing=true, no re-fetch).
	dep2, _, err := fetchBaseSkill(context.Background(), "skills[0]",
		baseURLDir, "skills/common", allowlist, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   fetcher,
		})
	require.NoError(t, err)
	assert.True(t, dep2.CacheHit, "re-fetched entry should be cached")
}

func TestFetchBaseSkill_StaleCacheOfflineServesStale(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	baseURLDir := "https://raw.githubusercontent.com/org/repo/ref1/"
	skillFileURL := baseURLDir + "skills/common/SKILL.md"
	allowlist := []string{"https://raw.githubusercontent.com/org/repo/"}

	// Pre-populate cache with a v0.22.0-style single-file entry.
	oldFiles := map[string][]byte{"SKILL.md": []byte("# old")}
	oldHash, err := fetch.CachePutDir(cacheDir, skillFileURL, oldFiles)
	require.NoError(t, err)
	require.NoError(t, urlIndexPut(cacheDir, skillFileURL, oldHash))
	require.NoError(t, urlIndexPut(cacheDir, "skill:"+skillFileURL, oldHash))

	// Offline mode — should serve stale cache, not error.
	dep, localDir, err := fetchBaseSkill(context.Background(), "skills[0]",
		baseURLDir, "skills/common", allowlist, ComposeOpts{
			WorkspaceRoot: cacheDir,
			FetchPolicy:   fetch.FetchPolicy{Offline: true},
		})
	require.NoError(t, err)
	assert.True(t, dep.CacheHit)
	assert.FileExists(t, filepath.Join(localDir, "SKILL.md"))
}

func TestFetchBaseSkill_NarrowAllowlist(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	fetcher := fakeTreeFetcher(map[string][]byte{
		"SKILL.md":       []byte("# Skill"),
		"meta-prompt.md": []byte("meta"),
	})

	// Allowlist covers SKILL.md specifically but not the directory prefix.
	narrowAllowlist := []string{"https://raw.githubusercontent.com/org/repo/ref1/skills/pr-review/SKILL.md"}

	_, _, err := fetchBaseSkill(context.Background(), "skills[0]",
		"https://raw.githubusercontent.com/org/repo/ref1/",
		"skills/pr-review", narrowAllowlist, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   fetcher,
		})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in allowed_remote_resources")
}

// --- resolveBaseHostFiles tests ---

func TestLoadWithBase_URLBase_HostFilesFetched(t *testing.T) {
	envContent := []byte("GCP_PROJECT=test-project\n")
	triageEnv := []byte("TRIAGE_MODE=auto\n")

	baseContent := []byte(`
agent: agents/triage.md
role: test
host_files:
  - src: env/gcp-vertex.env
    dest: /sandbox/workspace/.env.d/gcp-vertex.env
    expand: true
  - src: env/triage.env
    dest: /sandbox/workspace/.env.d/triage.env
    expand: true
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/env/gcp-vertex.env": envContent,
		"/env/triage.env":     triageEnv,
	})

	hash := computeHash(baseContent)
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	baseURL := server.URL + "/harness/triage.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
role: test
base: `+baseURL+`
`)

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
	})
	require.NoError(t, err)

	// Host files resolved to local cache paths
	require.Len(t, h.HostFiles, 2)
	for i, hf := range h.HostFiles {
		assert.True(t, filepath.IsAbs(hf.Src), "host_files[%d].src should be absolute cache path", i)
		assert.False(t, IsURL(hf.Src), "host_files[%d].src should not be a URL", i)
	}

	// Verify cached content
	content0, err := os.ReadFile(h.HostFiles[0].Src)
	require.NoError(t, err)
	assert.Equal(t, envContent, content0)

	content1, err := os.ReadFile(h.HostFiles[1].Src)
	require.NoError(t, err)
	assert.Equal(t, triageEnv, content1)

	// Dest and expand preserved
	assert.Equal(t, "/sandbox/workspace/.env.d/gcp-vertex.env", h.HostFiles[0].Dest)
	assert.True(t, h.HostFiles[0].Expand)

	// Dependencies include host_files
	hostFileDeps := []Dependency{}
	for _, d := range deps {
		if strings.HasPrefix(d.Field, "host_files[") {
			hostFileDeps = append(hostFileDeps, d)
		}
	}
	assert.Len(t, hostFileDeps, 2)
	for _, d := range hostFileDeps {
		assert.Equal(t, "resource", d.Type)
	}
}

func TestLoadWithBase_URLBase_HostFilesMixedEnvVarAndRelative(t *testing.T) {
	envContent := []byte("KEY=value\n")

	baseContent := []byte(`
agent: agents/triage.md
role: test
host_files:
  - src: env/app.env
    dest: /sandbox/.env.d/app.env
  - src: ${GOOGLE_APPLICATION_CREDENTIALS}
    dest: /tmp/.gcp-credentials.json
`)

	server, policy := setupScriptTestServer(t, baseContent, map[string][]byte{
		"/env/app.env": envContent,
	})

	hash := computeHash(baseContent)
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	baseURL := server.URL + "/harness/triage.yaml#sha256=" + hash

	path := writeTestHarness(t, dir, "child.yaml", `
role: test
base: `+baseURL+`
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
	})
	require.NoError(t, err)

	require.Len(t, h.HostFiles, 2)

	// Relative src resolved to cache path
	assert.True(t, filepath.IsAbs(h.HostFiles[0].Src), "relative src should be resolved")

	// ${VAR} src left unchanged
	assert.Equal(t, "${GOOGLE_APPLICATION_CREDENTIALS}", h.HostFiles[1].Src)
}

func TestResolveBaseHostFiles_SkipsEnvVarPaths(t *testing.T) {
	base := &Harness{
		HostFiles: []HostFile{
			{Src: "${HOME}/file.txt", Dest: "/sandbox/file.txt"},
		},
	}
	deps, err := resolveBaseHostFiles(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.NoError(t, err)
	assert.Empty(t, deps)
	assert.Equal(t, "${HOME}/file.txt", base.HostFiles[0].Src)
}

func TestResolveBaseHostFiles_SkipsAbsolutePaths(t *testing.T) {
	base := &Harness{
		HostFiles: []HostFile{
			{Src: "/absolute/path/file.txt", Dest: "/sandbox/file.txt"},
		},
	}
	deps, err := resolveBaseHostFiles(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.NoError(t, err)
	assert.Empty(t, deps)
	assert.Equal(t, "/absolute/path/file.txt", base.HostFiles[0].Src)
}

func TestResolveBaseHostFiles_SkipsEmptySrc(t *testing.T) {
	base := &Harness{
		HostFiles: []HostFile{
			{Src: "", Dest: "/sandbox/file.txt"},
		},
	}
	deps, err := resolveBaseHostFiles(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.NoError(t, err)
	assert.Empty(t, deps)
}

func TestResolveBaseHostFiles_RejectsPathTraversal(t *testing.T) {
	base := &Harness{
		HostFiles: []HostFile{
			{Src: "../../etc/passwd", Dest: "/sandbox/passwd"},
		},
	}
	_, err := resolveBaseHostFiles(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain path traversal")
	assert.Contains(t, err.Error(), "host_files[0].src")
}

func TestResolveBaseHostFiles_RejectsNullBytes(t *testing.T) {
	base := &Harness{
		HostFiles: []HostFile{
			{Src: "env/test\x00.env", Dest: "/sandbox/.env"},
		},
	}
	_, err := resolveBaseHostFiles(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain null bytes")
	assert.Contains(t, err.Error(), "host_files[0].src")
}

func TestResolveBaseHostFiles_InvalidBaseURL(t *testing.T) {
	base := &Harness{
		HostFiles: []HostFile{
			{Src: "env/test.env", Dest: "/sandbox/.env"},
		},
	}
	_, err := resolveBaseHostFiles(context.Background(), base, "", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot determine directory")
}

func TestResolveBaseHostFiles_EmptyHostFiles(t *testing.T) {
	base := &Harness{}
	deps, err := resolveBaseHostFiles(context.Background(), base, "https://example.com/harness/triage.yaml#sha256=abc", nil, ComposeOpts{})
	require.NoError(t, err)
	assert.Empty(t, deps)
}

// --- Tests for URL-sourced harnesses without base: field (SourceURL) ---

func TestLoadWithBase_SourceURL_ResolvesResources(t *testing.T) {
	// A URL-sourced harness with no base: field should have its relative
	// resource paths resolved against the source URL (ADR-0045).
	agentContent := []byte("# triage agent definition")
	policyContent := []byte("# triage policy")
	preScript := []byte("#!/bin/bash\necho pre")
	postScript := []byte("#!/bin/bash\necho post")

	harnessContent := []byte(`
role: triage
slug: triage
agent: agents/triage.md
policy: policies/triage.yaml
pre_script: scripts/pre-triage.sh
post_script: scripts/post-triage.sh
`)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/harness/triage.yaml":
			w.Write(harnessContent)
		case "/agents/triage.md":
			w.Write(agentContent)
		case "/policies/triage.yaml":
			w.Write(policyContent)
		case "/scripts/pre-triage.sh":
			w.Write(preScript)
		case "/scripts/post-triage.sh":
			w.Write(postScript)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	policy := fetch.NewTestPolicy(
		server.Client().Transport.(*http.Transport).TLSClientConfig,
		[]string{"127.0.0.1"},
		[]string{server.Listener.Addr().String()[len("127.0.0.1:"):]},
	)

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	// Write the harness locally (simulating FetchAgentHarness caching it)
	path := writeTestHarness(t, dir, "triage.yaml", string(harnessContent))

	sourceURL := server.URL + "/harness/triage.yaml"

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
		SourceURL:     sourceURL,
	})
	require.NoError(t, err)

	// All resource paths should be resolved to local cache paths
	assert.True(t, filepath.IsAbs(h.Agent), "agent should be absolute cache path, got %s", h.Agent)
	assert.True(t, filepath.IsAbs(h.Policy), "policy should be absolute cache path, got %s", h.Policy)
	assert.True(t, filepath.IsAbs(h.PreScript), "pre_script should be absolute cache path")
	assert.True(t, filepath.IsAbs(h.PostScript), "post_script should be absolute cache path")

	// Verify cached content matches
	gotAgent, err := os.ReadFile(h.Agent)
	require.NoError(t, err)
	assert.Equal(t, agentContent, gotAgent)

	gotPolicy, err := os.ReadFile(h.Policy)
	require.NoError(t, err)
	assert.Equal(t, policyContent, gotPolicy)

	gotPre, err := os.ReadFile(h.PreScript)
	require.NoError(t, err)
	assert.Equal(t, preScript, gotPre)

	gotPost, err := os.ReadFile(h.PostScript)
	require.NoError(t, err)
	assert.Equal(t, postScript, gotPost)

	// Dependencies should include scripts and resources
	assert.NotEmpty(t, deps)
	fieldNames := map[string]bool{}
	for _, d := range deps {
		fieldNames[d.Field] = true
	}
	assert.True(t, fieldNames["pre_script"], "should have pre_script dep")
	assert.True(t, fieldNames["post_script"], "should have post_script dep")
	assert.True(t, fieldNames["agent"], "should have agent dep")
	assert.True(t, fieldNames["policy"], "should have policy dep")
}

func TestLoadWithBase_SourceURL_NoRelativePaths(t *testing.T) {
	// A URL-sourced harness with no relative paths should be a no-op.
	harnessContent := []byte(`
role: test
slug: test-agent
agent: /absolute/path/agent.md
`)

	dir := t.TempDir()
	path := writeTestHarness(t, dir, "test.yaml", string(harnessContent))

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
		SourceURL: "https://example.com/harness/test.yaml",
	})
	require.NoError(t, err)
	assert.Empty(t, deps)
	assert.Equal(t, "test", h.Role)
}

func TestLoadWithBase_SourceURL_ScriptResolutionError(t *testing.T) {
	// When resolveBaseScripts fails (e.g., script URL not in allowlist),
	// LoadWithBase should return the error.
	harnessContent := []byte(`
role: triage
slug: triage
pre_script: scripts/pre-triage.sh
`)

	dir := t.TempDir()
	path := writeTestHarness(t, dir, "triage.yaml", string(harnessContent))

	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		SourceURL:    "https://example.com/harness/triage.yaml",
		OrgAllowlist: []string{"https://other.example.com/"}, // not matching
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving URL-sourced scripts")
}

func TestLoadWithBase_SourceURL_ResourceResolutionError(t *testing.T) {
	// When resolveBaseResources fails (e.g., agent URL not in allowlist),
	// LoadWithBase should return the error.
	harnessContent := []byte(`
role: triage
slug: triage
agent: agents/triage.md
`)

	dir := t.TempDir()
	path := writeTestHarness(t, dir, "triage.yaml", string(harnessContent))

	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		SourceURL:    "https://example.com/harness/triage.yaml",
		OrgAllowlist: []string{"https://other.example.com/"}, // not matching
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving URL-sourced resources")
}

func TestLoadWithBase_SourceURL_HostFiles(t *testing.T) {
	// A URL-sourced harness with no base: field should have its relative
	// host_files src paths resolved against the source URL.
	envContent := []byte("KEY=value")

	agentContent := []byte("# triage agent definition")

	harnessContent := []byte(`
role: triage
slug: triage
agent: agents/triage.md
host_files:
  - src: env/triage.env
    dest: /sandbox/.env
  - src: ${HOME}/.config/app.env
    dest: /sandbox/app.env
  - src: /absolute/path/file.env
    dest: /sandbox/abs.env
`)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/harness/triage.yaml":
			w.Write(harnessContent)
		case "/agents/triage.md":
			w.Write(agentContent)
		case "/env/triage.env":
			w.Write(envContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	policy := fetch.NewTestPolicy(
		server.Client().Transport.(*http.Transport).TLSClientConfig,
		[]string{"127.0.0.1"},
		[]string{server.Listener.Addr().String()[len("127.0.0.1:"):]},
	)

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	path := writeTestHarness(t, dir, "triage.yaml", string(harnessContent))

	sourceURL := server.URL + "/harness/triage.yaml"

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
		SourceURL:     sourceURL,
	})
	require.NoError(t, err)

	// The relative host_files src should be resolved to a local cache path
	assert.True(t, filepath.IsAbs(h.HostFiles[0].Src),
		"relative host_files src should be resolved to absolute cache path, got %s", h.HostFiles[0].Src)

	// Verify cached content matches
	gotEnv, err := os.ReadFile(h.HostFiles[0].Src)
	require.NoError(t, err)
	assert.Equal(t, envContent, gotEnv)

	// ${VAR} entries should be left unchanged
	assert.Equal(t, "${HOME}/.config/app.env", h.HostFiles[1].Src,
		"host_files with ${VAR} should be left unchanged")

	// Absolute paths should be left unchanged
	assert.Equal(t, "/absolute/path/file.env", h.HostFiles[2].Src,
		"host_files with absolute paths should be left unchanged")

	// Dependencies should include the host file
	fieldNames := map[string]bool{}
	for _, d := range deps {
		fieldNames[d.Field] = true
	}
	assert.True(t, fieldNames["host_files[0].src"], "should have host_files dep")
}

func TestLoadWithBase_SourceURL_HostFilesResolutionError(t *testing.T) {
	// When resolveBaseHostFiles fails (e.g., host_files URL not in allowlist),
	// LoadWithBase should return the error.
	harnessContent := []byte(`
role: triage
slug: triage
host_files:
  - src: env/triage.env
    dest: /sandbox/.env
`)

	dir := t.TempDir()
	path := writeTestHarness(t, dir, "triage.yaml", string(harnessContent))

	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		SourceURL:    "https://example.com/harness/triage.yaml",
		OrgAllowlist: []string{"https://other.example.com/"}, // not matching
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving URL-sourced host_files")
}

func TestLoadWithBase_NoSourceURL_NoResolution(t *testing.T) {
	// Without SourceURL, a no-base harness should not attempt URL resolution
	// (original behavior preserved).
	harnessContent := []byte(`
role: test
agent: agents/test.md
`)

	dir := t.TempDir()
	path := writeTestHarness(t, dir, "test.yaml", string(harnessContent))

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)
	assert.Empty(t, deps)
	assert.Equal(t, "agents/test.md", h.Agent, "agent should remain relative without SourceURL")
}

func TestFetchBaseSkill_StaleCacheTransientFallback(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	baseURLDir := "https://raw.githubusercontent.com/org/repo/ref1/"
	skillFileURL := baseURLDir + "skills/common/SKILL.md"
	allowlist := []string{"https://raw.githubusercontent.com/org/repo/"}

	oldFiles := map[string][]byte{"SKILL.md": []byte("# old")}
	oldHash, err := fetch.CachePutDir(cacheDir, skillFileURL, oldFiles)
	require.NoError(t, err)
	require.NoError(t, urlIndexPut(cacheDir, skillFileURL, oldHash))
	require.NoError(t, urlIndexPut(cacheDir, "skill:"+skillFileURL, oldHash))

	failFetcher := func(_ context.Context, _, _, _, _ string) (map[string][]byte, error) {
		return nil, &gitfetch.TransientError{Err: fmt.Errorf("connection refused")}
	}

	dep, localDir, err := fetchBaseSkill(context.Background(), "skills[0]",
		baseURLDir, "skills/common", allowlist, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   failFetcher,
		})
	require.NoError(t, err)
	assert.True(t, dep.CacheHit)
	assert.Contains(t, dep.Warning, "using stale cached content")
	assert.Contains(t, dep.Warning, "connection refused")
	assert.FileExists(t, filepath.Join(localDir, "SKILL.md"))
}

func TestFetchBaseSkill_StaleCacheContextDeadlineFallback(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	baseURLDir := "https://raw.githubusercontent.com/org/repo/ref1/"
	skillFileURL := baseURLDir + "skills/common/SKILL.md"
	allowlist := []string{"https://raw.githubusercontent.com/org/repo/"}

	oldFiles := map[string][]byte{"SKILL.md": []byte("# old")}
	oldHash, err := fetch.CachePutDir(cacheDir, skillFileURL, oldFiles)
	require.NoError(t, err)
	require.NoError(t, urlIndexPut(cacheDir, skillFileURL, oldHash))
	require.NoError(t, urlIndexPut(cacheDir, "skill:"+skillFileURL, oldHash))

	failFetcher := func(_ context.Context, _, _, _, _ string) (map[string][]byte, error) {
		return nil, fmt.Errorf("git fetch: %w", context.DeadlineExceeded)
	}

	dep, localDir, err := fetchBaseSkill(context.Background(), "skills[0]",
		baseURLDir, "skills/common", allowlist, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   failFetcher,
		})
	require.NoError(t, err)
	assert.True(t, dep.CacheHit)
	assert.Contains(t, dep.Warning, "using stale cached content")
	assert.FileExists(t, filepath.Join(localDir, "SKILL.md"))
}

func TestFetchBaseSkill_StaleCacheNonTransientError(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	baseURLDir := "https://raw.githubusercontent.com/org/repo/ref1/"
	skillFileURL := baseURLDir + "skills/common/SKILL.md"
	allowlist := []string{"https://raw.githubusercontent.com/org/repo/"}

	oldFiles := map[string][]byte{"SKILL.md": []byte("# old")}
	oldHash, err := fetch.CachePutDir(cacheDir, skillFileURL, oldFiles)
	require.NoError(t, err)
	require.NoError(t, urlIndexPut(cacheDir, skillFileURL, oldHash))
	require.NoError(t, urlIndexPut(cacheDir, "skill:"+skillFileURL, oldHash))

	failFetcher := func(_ context.Context, _, _, _, _ string) (map[string][]byte, error) {
		return nil, fmt.Errorf("authentication failed: 401 Unauthorized")
	}

	_, _, err = fetchBaseSkill(context.Background(), "skills[0]",
		baseURLDir, "skills/common", allowlist, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   failFetcher,
		})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication failed")
}

func TestFetchBaseSkill_TreeFetchErrorWithToken(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	failFetcher := func(_ context.Context, _, _, _, _ string) (map[string][]byte, error) {
		return nil, fmt.Errorf("git fetch failed")
	}

	_, _, err := fetchBaseSkill(context.Background(), "skills[0]",
		"https://raw.githubusercontent.com/org/repo/ref/",
		"skills/common", []string{"https://raw.githubusercontent.com/org/repo/"}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   failFetcher,
			GitToken:      "ghp_test123",
		})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching skill directory")
	assert.NotContains(t, err.Error(), "hint:")
}

func TestIsTransientFetchError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		transient bool
	}{
		{"context deadline", fmt.Errorf("git fetch: %w", context.DeadlineExceeded), true},
		{"context canceled", fmt.Errorf("git fetch: %w", context.Canceled), true},
		{"transient error type", &gitfetch.TransientError{Err: fmt.Errorf("connection refused")}, true},
		{"wrapped transient", fmt.Errorf("gitfetch: %w", &gitfetch.TransientError{Err: fmt.Errorf("no such host")}), true},
		{"auth error", fmt.Errorf("authentication failed"), false},
		{"generic error", fmt.Errorf("something went wrong"), false},
		{"404 error", fmt.Errorf("not found"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.transient, isTransientFetchError(tt.err))
		})
	}
}

func TestLoadWithBase_URLBase_ProfileResolution(t *testing.T) {
	profileContent := []byte(`id: test-profile
network:
  egress:
    - host: "*.example.com"
`)
	profileHash := computeHash(profileContent)

	baseContent := []byte(`
agent: agents/remote.md
role: test
openshell:
  profiles:
    - profiles/test-profile.yaml
`)
	baseHash := computeHash(baseContent)

	agentContent := []byte("# test agent")

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/base.yaml":
			w.Write(baseContent)
		case "/profiles/test-profile.yaml":
			w.Write(profileContent)
		case "/agents/remote.md":
			w.Write(agentContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+server.URL+`/base.yaml#sha256=`+baseHash+`
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

	// Profile should be rewritten to a local cache path
	require.NotNil(t, h.OpenShell)
	require.Len(t, h.OpenShellProfiles(), 1)
	assert.False(t, IsURL(h.OpenShellProfiles()[0]), "profile should be a local path, not a URL")
	assert.True(t, filepath.IsAbs(h.OpenShellProfiles()[0]), "profile should be absolute (cache path)")

	// Verify cached content matches
	content, err := os.ReadFile(h.OpenShellProfiles()[0])
	require.NoError(t, err)
	assert.Equal(t, profileContent, content)

	// Dependencies: base + agent + profile = 3
	hasDep := false
	for _, d := range deps {
		if d.Field == "openshell.profiles[0]" {
			hasDep = true
			assert.Equal(t, server.URL+"/profiles/test-profile.yaml", d.URL)
			assert.Equal(t, profileHash, d.SHA256)
		}
	}
	assert.True(t, hasDep, "should have a dependency for the profile")
}

func TestLoadWithBase_URLBase_ProviderResolution(t *testing.T) {
	providerContent := []byte(`name: test-provider
type: custom
credentials:
  TEST_KEY: ""
`)
	providerHash := computeHash(providerContent)

	baseContent := []byte(`
agent: agents/remote.md
role: test
providers:
  - providers/test-provider.yaml
`)
	baseHash := computeHash(baseContent)

	agentContent := []byte("# test agent")

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/base.yaml":
			w.Write(baseContent)
		case "/providers/test-provider.yaml":
			w.Write(providerContent)
		case "/agents/remote.md":
			w.Write(agentContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+server.URL+`/base.yaml#sha256=`+baseHash+`
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

	// Provider should be rewritten to a local cache path
	require.Len(t, h.Providers, 1)
	assert.False(t, IsURL(h.Providers[0]), "provider should be a local path, not a URL")
	assert.True(t, filepath.IsAbs(h.Providers[0]), "provider should be absolute (cache path)")

	// Verify cached content matches
	content, err := os.ReadFile(h.Providers[0])
	require.NoError(t, err)
	assert.Equal(t, providerContent, content)

	// Check dependency
	hasDep := false
	for _, d := range deps {
		if d.Field == "providers[0]" {
			hasDep = true
			assert.Equal(t, server.URL+"/providers/test-provider.yaml", d.URL)
			assert.Equal(t, providerHash, d.SHA256)
		}
	}
	assert.True(t, hasDep, "should have a dependency for the provider")
}

func TestLoadWithBase_URLBase_BareProviderNameSkipped(t *testing.T) {
	// A URL-fetched base harness with a bare provider name like "fullsend-github"
	// should NOT trigger a fetch attempt. Only relative paths should be fetched.
	baseContent := []byte(`
agent: agents/remote.md
role: test
providers:
  - fullsend-github
  - providers/custom.yaml
`)
	baseHash := computeHash(baseContent)

	providerContent := []byte(`name: custom-provider
type: custom
credentials:
  CUSTOM_KEY: ""
`)

	agentContent := []byte("# test agent")

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/base.yaml":
			w.Write(baseContent)
		case "/providers/custom.yaml":
			w.Write(providerContent)
		case "/agents/remote.md":
			w.Write(agentContent)
		case "/fullsend-github":
			// If we hit this path, the bare name wasn't skipped
			t.Error("bare provider name 'fullsend-github' was not skipped; tried to fetch as relative path")
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: `+server.URL+`/base.yaml#sha256=`+baseHash+`
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

	// Should have two providers: the bare name (unchanged) and the resolved path
	require.Len(t, h.Providers, 2)
	assert.Equal(t, "fullsend-github", h.Providers[0], "bare provider name should remain unchanged")
	assert.True(t, filepath.IsAbs(h.Providers[1]), "relative provider should be resolved to cache path")

	// Should have one dependency for the relative provider path, none for bare name
	providerDeps := 0
	for _, d := range deps {
		if strings.HasPrefix(d.Field, "providers[") {
			providerDeps++
			assert.Equal(t, "providers[1]", d.Field, "only providers[1] (relative path) should be fetched")
		}
	}
	assert.Equal(t, 1, providerDeps, "should have exactly one provider dependency (for relative path)")
}
