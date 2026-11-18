package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/gitfetch"
)

// Tests for ComposeOpts.LockedPins (#8979): a lock entry pins the
// relative files of a URL base, so a locked load resolves each pinned URL
// to the locked content, not to whatever url-index.json points at.

// seedLockedFile caches content and returns its hash without touching the
// URL index.
func seedLockedFile(t *testing.T, cacheDir, rawURL string, content []byte) string {
	t.Helper()
	require.NoError(t, fetch.CachePut(cacheDir, rawURL, content))
	return fetch.ComputeSHA256(content)
}

// seedLockedTree caches a directory tree and returns its tree hash without
// touching the URL index.
func seedLockedTree(t *testing.T, cacheDir, rawURL string, files map[string][]byte, full bool) string {
	t.Helper()
	treeHash, err := fetch.CachePutDir(cacheDir, rawURL, files, fetch.DirCachePutOpts{FullListing: full})
	require.NoError(t, err)
	return treeHash
}

func filePin(hash string) LockedPin { return LockedPin{SHA256: hash, Type: "resource"} }

func dirPin(hash string) LockedPin { return LockedPin{SHA256: hash, Type: "directory"} }

func readFile(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(data)
}

func failingTreeFetcher(t *testing.T) func(context.Context, string, string, string, string) (map[string][]byte, error) {
	return func(context.Context, string, string, string, string) (map[string][]byte, error) {
		t.Error("tree fetcher must not be called for locked content in cache")
		return nil, errors.New("unexpected tree fetch")
	}
}

func TestLoadWithBase_LockedPins_URLBaseFilesUseLockedContent(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	const (
		agentURL    = "https://example.com/agents/triage.md"
		policyURL   = "https://example.com/policies/sandbox.yaml"
		overrideURL = "https://example.com/overrides/review.md"
		skillURL    = "https://example.com/skills/common/SKILL.md"
		pluginURL   = "https://example.com/plugins/gopls-lsp/"
		scriptURL   = "https://example.com/scripts/pre.sh"
		scriptDir   = "https://example.com/scripts"
	)

	baseContent := []byte(`
agent: agents/triage.md
policy: policies/sandbox.yaml
role: test
pre_script: scripts/pre.sh
skills:
  - skills/common:
      sub-agents/review.md: overrides/review.md
plugins:
  - plugins/gopls-lsp
`)
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/harness/triage.yaml", baseContent))
	baseURL := "https://example.com/harness/triage.yaml#sha256=" + computeHash(baseContent)

	// The content the lock recorded for every relative file.
	locked := map[string]LockedPin{
		agentURL:    filePin(seedLockedFile(t, cacheDir, agentURL, []byte("locked agent"))),
		policyURL:   filePin(seedLockedFile(t, cacheDir, policyURL, []byte("locked: policy"))),
		overrideURL: filePin(seedLockedFile(t, cacheDir, overrideURL, []byte("locked override"))),
		skillURL:    dirPin(seedLockedTree(t, cacheDir, skillURL, map[string][]byte{"SKILL.md": []byte("# locked skill")}, true)),
		pluginURL:   dirPin(seedLockedTree(t, cacheDir, pluginURL, map[string][]byte{"plugin.json": []byte(`{"name":"locked"}`)}, true)),
		scriptURL:   dirPin(seedLockedTree(t, cacheDir, scriptURL, map[string][]byte{"pre.sh": []byte("#!/bin/sh\necho locked")}, true)),
	}

	// Later content that the URL index moved to after locking, for example
	// because a branch-ref base was re-fetched online.
	require.NoError(t, urlIndexPut(cacheDir, agentURL, seedLockedFile(t, cacheDir, agentURL, []byte("moved agent"))))
	require.NoError(t, urlIndexPut(cacheDir, policyURL, seedLockedFile(t, cacheDir, policyURL, []byte("moved: policy"))))
	require.NoError(t, urlIndexPut(cacheDir, overrideURL, seedLockedFile(t, cacheDir, overrideURL, []byte("moved override"))))
	movedSkill := seedLockedTree(t, cacheDir, skillURL, map[string][]byte{"SKILL.md": []byte("# moved skill")}, true)
	require.NoError(t, urlIndexPut(cacheDir, skillURL, movedSkill))
	require.NoError(t, urlIndexPut(cacheDir, "skill:"+skillURL, movedSkill))
	seedPluginTreeCache(t, cacheDir, pluginURL, map[string][]byte{"plugin.json": []byte(`{"name":"moved"}`)})
	movedScript := seedLockedTree(t, cacheDir, scriptURL, map[string][]byte{"pre.sh": []byte("#!/bin/sh\necho moved")}, true)
	require.NoError(t, urlIndexPut(cacheDir, scriptURL, movedScript))
	require.NoError(t, urlIndexPut(cacheDir, "scriptdir:"+scriptDir, movedScript))

	path := writeTestHarness(t, dir, "child.yaml", `
role: test
base: `+baseURL+`
`)

	load := func(t *testing.T, pins map[string]LockedPin) *Harness {
		t.Helper()
		h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
			WorkspaceRoot: cacheDir,
			FetchPolicy:   fetch.FetchPolicy{Offline: true},
			OrgAllowlist:  []string{"https://example.com/"},
			TreeFetcher:   failingTreeFetcher(t),
			LockedPins:    pins,
		})
		require.NoError(t, err)
		if pins != nil {
			for _, d := range deps {
				if want, ok := pins[d.URL]; ok {
					assert.Equal(t, want.SHA256, d.SHA256, "dependency %s (%s) must report the locked hash", d.Field, d.URL)
				}
			}
		}
		require.Len(t, h.Skills, 1)
		require.Len(t, h.Plugins, 1)
		return h
	}

	t.Run("locked load uses the locked content for every file", func(t *testing.T) {
		h := load(t, locked)
		assert.Equal(t, "locked agent", readFile(t, h.Agent))
		assert.Equal(t, "locked: policy", readFile(t, h.Policy))
		assert.Equal(t, "locked override", readFile(t, *h.Skills[0].Overrides["sub-agents/review.md"]))
		assert.Equal(t, "# locked skill", readFile(t, filepath.Join(h.Skills[0].Source, "SKILL.md")))
		assert.Equal(t, `{"name":"locked"}`, readFile(t, filepath.Join(h.Plugins[0].Path, "plugin.json")))
		assert.Equal(t, "#!/bin/sh\necho locked", readFile(t, h.PreScript))
	})

	t.Run("unlocked load still follows the URL index", func(t *testing.T) {
		h := load(t, nil)
		assert.Equal(t, "moved agent", readFile(t, h.Agent))
		assert.Equal(t, "moved: policy", readFile(t, h.Policy))
		assert.Equal(t, "moved override", readFile(t, *h.Skills[0].Overrides["sub-agents/review.md"]))
		assert.Equal(t, "# moved skill", readFile(t, filepath.Join(h.Skills[0].Source, "SKILL.md")))
		assert.Equal(t, `{"name":"moved"}`, readFile(t, filepath.Join(h.Plugins[0].Path, "plugin.json")))
		assert.Equal(t, "#!/bin/sh\necho moved", readFile(t, h.PreScript))
	})
}

func TestLoadWithBase_LockedPins_SharedContentIsKeyedByURL(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	const (
		agentURL  = "https://example.com/agents/triage.md"
		policyURL = "https://example.com/policies/sandbox.yaml"
	)

	baseContent := []byte(`
agent: agents/triage.md
policy: policies/sandbox.yaml
role: test
`)
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/harness/triage.yaml", baseContent))
	baseURL := "https://example.com/harness/triage.yaml#sha256=" + computeHash(baseContent)

	// Both URLs served identical content when the lock was taken, so they
	// share one cache entry.
	shared := seedLockedFile(t, cacheDir, agentURL, []byte("shared"))
	require.Equal(t, shared, seedLockedFile(t, cacheDir, policyURL, []byte("shared")))

	// Each URL's index entry then moved to different content.
	require.NoError(t, urlIndexPut(cacheDir, agentURL, seedLockedFile(t, cacheDir, agentURL, []byte("agent v2"))))
	require.NoError(t, urlIndexPut(cacheDir, policyURL, seedLockedFile(t, cacheDir, policyURL, []byte("policy v2"))))

	path := writeTestHarness(t, dir, "child.yaml", `
role: test
base: `+baseURL+`
`)

	load := func(t *testing.T, pins map[string]LockedPin) *Harness {
		t.Helper()
		h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
			WorkspaceRoot: cacheDir,
			FetchPolicy:   fetch.FetchPolicy{Offline: true},
			OrgAllowlist:  []string{"https://example.com/"},
			LockedPins:    pins,
		})
		require.NoError(t, err)
		return h
	}

	t.Run("both URLs pinned to the same content", func(t *testing.T) {
		h := load(t, map[string]LockedPin{agentURL: filePin(shared), policyURL: filePin(shared)})
		assert.Equal(t, "shared", readFile(t, h.Agent))
		assert.Equal(t, "shared", readFile(t, h.Policy))
	})

	t.Run("pinning one URL does not pin another with the same content", func(t *testing.T) {
		h := load(t, map[string]LockedPin{agentURL: filePin(shared)})
		assert.Equal(t, "shared", readFile(t, h.Agent))
		assert.Equal(t, "policy v2", readFile(t, h.Policy))
	})
}

func TestLoadWithBase_LockedPins_URLSourcedChildOwnFiles(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	const (
		childAgentURL  = "https://example.com/agents/child.md"
		childPolicyURL = "https://example.com/policies/child.yaml"
		baseAgentURL   = "https://example.com/agents/triage.md"
	)

	lockedChildAgent := seedLockedFile(t, cacheDir, childAgentURL, []byte("locked child agent"))
	require.NoError(t, urlIndexPut(cacheDir, childAgentURL, seedLockedFile(t, cacheDir, childAgentURL, []byte("moved child agent"))))
	lockedChildPolicy := seedLockedFile(t, cacheDir, childPolicyURL, []byte("locked: child"))
	require.NoError(t, urlIndexPut(cacheDir, childPolicyURL, seedLockedFile(t, cacheDir, childPolicyURL, []byte("moved: child"))))
	lockedBaseAgent := seedLockedFile(t, cacheDir, baseAgentURL, []byte("locked base agent"))
	require.NoError(t, urlIndexPut(cacheDir, baseAgentURL, seedLockedFile(t, cacheDir, baseAgentURL, []byte("moved base agent"))))

	opts := ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
		OrgAllowlist:  []string{"https://example.com/"},
		SourceURL:     "https://example.com/harness/child.yaml",
		LockedPins: map[string]LockedPin{
			childAgentURL:  filePin(lockedChildAgent),
			childPolicyURL: filePin(lockedChildPolicy),
			baseAgentURL:   filePin(lockedBaseAgent),
		},
	}

	t.Run("no base", func(t *testing.T) {
		path := writeTestHarness(t, dir, "nobase/child.yaml", `
agent: agents/child.md
policy: policies/child.yaml
role: test
`)
		h, _, err := LoadWithBase(context.Background(), path, opts)
		require.NoError(t, err)
		assert.Equal(t, "locked child agent", readFile(t, h.Agent))
		assert.Equal(t, "locked: child", readFile(t, h.Policy))
	})

	t.Run("with base", func(t *testing.T) {
		baseContent := []byte(`
agent: agents/triage.md
role: test
`)
		require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/harness/triage.yaml", baseContent))
		path := writeTestHarness(t, dir, "withbase/child.yaml", `
policy: policies/child.yaml
role: test
base: https://example.com/harness/triage.yaml#sha256=`+computeHash(baseContent)+`
`)
		h, _, err := LoadWithBase(context.Background(), path, opts)
		require.NoError(t, err)
		assert.Equal(t, "locked base agent", readFile(t, h.Agent))
		assert.Equal(t, "locked: child", readFile(t, h.Policy))
	})
}

func TestLoadWithBase_LockedPins_OfflineMissingLockedContent(t *testing.T) {
	missing := computeHash([]byte("never cached"))

	tests := []struct {
		name      string
		baseYAML  string
		lockedURL string
		field     string
		pinType   string
	}{
		{"agent", "agent: agents/triage.md\nrole: test\n", "https://example.com/agents/triage.md", "agent", "resource"},
		{"skill", "agent: agents/triage.md\nrole: test\nskills:\n  - skills/common\n", "https://example.com/skills/common/SKILL.md", "skills[0]", "directory"},
		{"plugin", "agent: agents/triage.md\nrole: test\nplugins:\n  - plugins/gopls-lsp\n", "https://example.com/plugins/gopls-lsp/", "plugins[0]", "directory"},
		{"script directory", "agent: agents/triage.md\nrole: test\npre_script: scripts/pre.sh\n", "https://example.com/scripts/pre.sh", "pre_script", "directory"},
		{"script single file", "agent: agents/triage.md\nrole: test\npre_script: scripts/pre.sh\n", "https://example.com/scripts/pre.sh", "pre_script", "script"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			cacheDir := filepath.Join(dir, "cache")

			baseContent := []byte(tt.baseYAML)
			require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/harness/triage.yaml", baseContent))

			// Unlocked files resolve through the index as before.
			agentURL := "https://example.com/agents/triage.md"
			require.NoError(t, urlIndexPut(cacheDir, agentURL, seedLockedFile(t, cacheDir, agentURL, []byte("agent"))))

			// The locked URL's index entry points at cached content, but
			// the pinned content itself is not in the cache.
			if tt.lockedURL != agentURL {
				require.NoError(t, urlIndexPut(cacheDir, tt.lockedURL, seedLockedFile(t, cacheDir, tt.lockedURL, []byte("indexed"))))
			}

			path := writeTestHarness(t, dir, "child.yaml", "role: test\nbase: https://example.com/harness/triage.yaml#sha256="+computeHash(baseContent)+"\n")
			_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
				WorkspaceRoot: cacheDir,
				FetchPolicy:   fetch.FetchPolicy{Offline: true},
				OrgAllowlist:  []string{"https://example.com/"},
				LockedPins:    map[string]LockedPin{tt.lockedURL: {SHA256: missing, Type: tt.pinType}},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "base "+tt.field+":")
			assert.Contains(t, err.Error(), "pinned in the lock file with sha256="+missing)
			assert.Contains(t, err.Error(), "run 'fullsend lock --update' first")
		})
	}
}

func TestFetchBaseFile_LockedPins_Online(t *testing.T) {
	served := []byte("served agent")
	server, policy := setupScriptTestServer(t, nil, map[string][]byte{"/agents/triage.md": served})
	fileURL := server.URL + "/agents/triage.md"
	allowlist := []string{server.URL + "/"}

	t.Run("re-fetched content matching the pin is used", func(t *testing.T) {
		cacheDir := filepath.Join(t.TempDir(), "cache")
		pin := fetch.ComputeSHA256(served)
		dep, contentPath, err := fetchBaseFile(context.Background(), "agent", server.URL+"/", "agents/triage.md", allowlist, ComposeOpts{
			WorkspaceRoot: cacheDir,
			FetchPolicy:   policy,
			LockedPins:    map[string]LockedPin{fileURL: filePin(pin)},
		}, "resource", false)
		require.NoError(t, err)
		assert.False(t, dep.CacheHit)
		assert.Equal(t, pin, dep.SHA256)
		assert.Equal(t, string(served), readFile(t, contentPath))
	})

	t.Run("re-fetched content not matching the pin is rejected", func(t *testing.T) {
		cacheDir := filepath.Join(t.TempDir(), "cache")
		pin := computeHash([]byte("locked agent"))
		_, _, err := fetchBaseFile(context.Background(), "agent", server.URL+"/", "agents/triage.md", allowlist, ComposeOpts{
			WorkspaceRoot: cacheDir,
			FetchPolicy:   policy,
			LockedPins:    map[string]LockedPin{fileURL: filePin(pin)},
		}, "resource", false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the lock file pins sha256="+pin)
		// Neither the cache nor the URL index takes the unpinned content.
		content, _, cErr := fetch.CacheGet(cacheDir, fetch.ComputeSHA256(served))
		require.NoError(t, cErr)
		assert.Nil(t, content)
		_, indexed := urlIndexLookup(cacheDir, fileURL)
		assert.False(t, indexed)
	})
}

func TestFetchBaseSkill_LockedPins_Online(t *testing.T) {
	const (
		baseDir   = "https://raw.githubusercontent.com/org/repo/ref/"
		skillURL  = baseDir + "skills/common/SKILL.md"
		allowlist = "https://raw.githubusercontent.com/org/repo/"
	)
	files := map[string][]byte{"SKILL.md": []byte("# skill")}

	t.Run("pinned partial listing is served without a re-fetch", func(t *testing.T) {
		cacheDir := filepath.Join(t.TempDir(), "cache")
		pin := seedLockedTree(t, cacheDir, skillURL, files, false)
		dep, _, err := fetchBaseSkill(context.Background(), "skills[0]", baseDir, "skills/common", []string{allowlist}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   failingTreeFetcher(t),
			LockedPins:    map[string]LockedPin{skillURL: dirPin(pin)},
		})
		require.NoError(t, err)
		assert.True(t, dep.CacheHit)
		assert.Equal(t, pin, dep.SHA256)
	})

	t.Run("re-fetched tree matching the pin is used", func(t *testing.T) {
		cacheDir := filepath.Join(t.TempDir(), "cache")
		pin := fetch.ComputeTreeHash(files)
		dep, _, err := fetchBaseSkill(context.Background(), "skills[0]", baseDir, "skills/common", []string{allowlist}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   fakeTreeFetcher(files),
			LockedPins:    map[string]LockedPin{skillURL: dirPin(pin)},
		})
		require.NoError(t, err)
		assert.False(t, dep.CacheHit)
		assert.Equal(t, pin, dep.SHA256)
	})

	t.Run("re-fetched tree not matching the pin is rejected", func(t *testing.T) {
		cacheDir := filepath.Join(t.TempDir(), "cache")
		pin := fetch.ComputeTreeHash(map[string][]byte{"SKILL.md": []byte("# locked")})
		_, _, err := fetchBaseSkill(context.Background(), "skills[0]", baseDir, "skills/common", []string{allowlist}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   fakeTreeFetcher(files),
			LockedPins:    map[string]LockedPin{skillURL: dirPin(pin)},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the lock file pins sha256="+pin)
		_, indexed := urlIndexLookup(cacheDir, "skill:"+skillURL)
		assert.False(t, indexed)
	})
}

func TestFetchBasePlugin_LockedPins_Online(t *testing.T) {
	const (
		baseDir   = "https://raw.githubusercontent.com/org/repo/ref/"
		pluginURL = baseDir + "plugins/gopls-lsp/"
		allowlist = "https://raw.githubusercontent.com/org/repo/"
	)
	files := map[string][]byte{"plugin.json": []byte(`{"name":"gopls-lsp"}`)}

	t.Run("pinned partial listing is served without a re-fetch", func(t *testing.T) {
		cacheDir := filepath.Join(t.TempDir(), "cache")
		pin := seedLockedTree(t, cacheDir, pluginURL, files, false)
		dep, _, err := fetchBasePlugin(context.Background(), "plugins[0]", baseDir, "plugins/gopls-lsp", []string{allowlist}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   failingTreeFetcher(t),
			LockedPins:    map[string]LockedPin{pluginURL: dirPin(pin)},
		})
		require.NoError(t, err)
		assert.True(t, dep.CacheHit)
		assert.Equal(t, pin, dep.SHA256)
	})

	t.Run("re-fetched tree not matching the pin is rejected", func(t *testing.T) {
		cacheDir := filepath.Join(t.TempDir(), "cache")
		pin := fetch.ComputeTreeHash(map[string][]byte{"plugin.json": []byte(`{"name":"locked"}`)})
		_, _, err := fetchBasePlugin(context.Background(), "plugins[0]", baseDir, "plugins/gopls-lsp", []string{allowlist}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   fakeTreeFetcher(files),
			LockedPins:    map[string]LockedPin{pluginURL: dirPin(pin)},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the lock file pins sha256="+pin)
	})
}

func TestFetchBaseScriptOrDir_LockedPins_Online(t *testing.T) {
	const (
		baseDir   = "https://raw.githubusercontent.com/org/repo/ref/"
		scriptURL = baseDir + "scripts/pre.sh"
		allowlist = "https://raw.githubusercontent.com/org/repo/"
	)
	files := map[string][]byte{"pre.sh": []byte("#!/bin/sh\necho hi")}

	t.Run("pinned single file is served without a directory fetch", func(t *testing.T) {
		cacheDir := filepath.Join(t.TempDir(), "cache")
		content := []byte("#!/bin/sh\necho locked")
		pin := seedLockedFile(t, cacheDir, scriptURL, content)
		dep, contentPath, err := fetchBaseScriptOrDir(context.Background(), "pre_script", baseDir, "scripts/pre.sh", []string{allowlist}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   failingTreeFetcher(t),
			LockedPins:    map[string]LockedPin{scriptURL: {SHA256: pin, Type: "script"}},
		})
		require.NoError(t, err)
		assert.True(t, dep.CacheHit)
		assert.Equal(t, pin, dep.SHA256)
		assert.Equal(t, string(content), readFile(t, contentPath))
	})

	t.Run("re-fetched directory matching the pin is used", func(t *testing.T) {
		cacheDir := filepath.Join(t.TempDir(), "cache")
		pin := fetch.ComputeTreeHash(files)
		dep, _, err := fetchBaseScriptOrDir(context.Background(), "pre_script", baseDir, "scripts/pre.sh", []string{allowlist}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   fakeTreeFetcher(files),
			LockedPins:    map[string]LockedPin{scriptURL: dirPin(pin)},
		})
		require.NoError(t, err)
		assert.False(t, dep.CacheHit)
		assert.Equal(t, pin, dep.SHA256)
	})

	t.Run("uncached single-file pin matching the fetched script is restored", func(t *testing.T) {
		cacheDir := filepath.Join(t.TempDir(), "cache")
		// The lock pinned the script's single-file hash, but that file is no
		// longer in the cache while the remote tree fetch now succeeds.
		pin := fetch.ComputeSHA256(files["pre.sh"])
		dep, contentPath, err := fetchBaseScriptOrDir(context.Background(), "pre_script", baseDir, "scripts/pre.sh", []string{allowlist}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   fakeTreeFetcher(files),
			LockedPins:    map[string]LockedPin{scriptURL: {SHA256: pin, Type: "script"}},
		})
		require.NoError(t, err)
		assert.True(t, dep.CacheHit)
		assert.Equal(t, pin, dep.SHA256)
		assert.Equal(t, string(files["pre.sh"]), readFile(t, contentPath))
	})

	t.Run("re-fetched directory not matching the pin is rejected", func(t *testing.T) {
		cacheDir := filepath.Join(t.TempDir(), "cache")
		pin := fetch.ComputeTreeHash(map[string][]byte{"pre.sh": []byte("#!/bin/sh\necho locked")})
		_, _, err := fetchBaseScriptOrDir(context.Background(), "pre_script", baseDir, "scripts/pre.sh", []string{allowlist}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   fakeTreeFetcher(files),
			LockedPins:    map[string]LockedPin{scriptURL: dirPin(pin)},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the lock file pins sha256="+pin)
	})
}

// TestLoadWithBase_LockedPins_ScriptAndHostFileShareURL covers one URL used
// as a script (directory dependency) and as a host_files src (file). The lock
// keeps one entry per URL, the script's tree hash, so the host file is served
// from that tree even after the URL index moved on.
func TestLoadWithBase_LockedPins_ScriptAndHostFileShareURL(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	const (
		agentURL  = "https://example.com/agents/triage.md"
		scriptURL = "https://example.com/scripts/pre.sh"
		scriptDir = "https://example.com/scripts"
	)
	script := []byte("#!/bin/sh\necho locked")

	baseContent := []byte(`
agent: agents/triage.md
role: test
pre_script: scripts/pre.sh
host_files:
  - src: scripts/pre.sh
    dest: /tmp/pre.sh
`)
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/harness/triage.yaml", baseContent))
	require.NoError(t, urlIndexPut(cacheDir, agentURL, seedLockedFile(t, cacheDir, agentURL, []byte("agent"))))

	treeHash := seedLockedTree(t, cacheDir, scriptURL, map[string][]byte{"pre.sh": script}, true)
	require.NoError(t, urlIndexPut(cacheDir, "scriptdir:"+scriptDir, treeHash))
	// A later unlocked run moved the URL index to newer content.
	newer := []byte("#!/bin/sh\necho NEWER")
	require.NoError(t, urlIndexPut(cacheDir, scriptURL, seedLockedFile(t, cacheDir, scriptURL, newer)))

	path := writeTestHarness(t, dir, "child.yaml", "role: test\nbase: https://example.com/harness/triage.yaml#sha256="+computeHash(baseContent)+"\n")
	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
		OrgAllowlist:  []string{"https://example.com/"},
		LockedPins:    map[string]LockedPin{scriptURL: dirPin(treeHash)},
	})
	require.NoError(t, err)
	assert.Equal(t, string(script), readFile(t, h.PreScript))
	require.Len(t, h.HostFiles, 1)
	assert.Equal(t, string(script), readFile(t, h.HostFiles[0].Src))
}

// TestFetchBaseTree_FilePinIgnored checks that a file-kind pin, as a legacy
// lock may hold for a skill or plugin URL, does not apply to a tree fetch.
func TestFetchBaseTree_FilePinIgnored(t *testing.T) {
	const baseDir = "https://example.com/"
	legacyPin := computeHash([]byte("single file"))

	t.Run("skill", func(t *testing.T) {
		cacheDir := filepath.Join(t.TempDir(), "cache")
		skillURL := baseDir + "skills/common/SKILL.md"
		tree := seedLockedTree(t, cacheDir, skillURL, map[string][]byte{"SKILL.md": []byte("# skill")}, true)
		require.NoError(t, urlIndexPut(cacheDir, skillURL, tree))
		require.NoError(t, urlIndexPut(cacheDir, "skill:"+skillURL, tree))
		dep, _, err := fetchBaseSkill(context.Background(), "skills[0]", baseDir, "skills/common", []string{baseDir}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			FetchPolicy:   fetch.FetchPolicy{Offline: true},
			LockedPins:    map[string]LockedPin{skillURL: {SHA256: legacyPin}},
		})
		require.NoError(t, err)
		assert.Equal(t, tree, dep.SHA256)
	})

	t.Run("plugin", func(t *testing.T) {
		cacheDir := filepath.Join(t.TempDir(), "cache")
		pluginURL := baseDir + "plugins/gopls-lsp/"
		files := map[string][]byte{"plugin.json": []byte(`{"name":"gopls-lsp"}`)}
		seedPluginTreeCache(t, cacheDir, pluginURL, files)
		tree := fetch.ComputeTreeHash(files)
		dep, _, err := fetchBasePlugin(context.Background(), "plugins[0]", baseDir, "plugins/gopls-lsp", []string{baseDir}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			FetchPolicy:   fetch.FetchPolicy{Offline: true},
			LockedPins:    map[string]LockedPin{pluginURL: {SHA256: legacyPin}},
		})
		require.NoError(t, err)
		assert.Equal(t, tree, dep.SHA256)
	})
}

// TestLockedPins_KindMismatchOnline checks that a directory-pinned file or
// script never falls back to an unpinned single-file fetch.
func TestLockedPins_KindMismatchOnline(t *testing.T) {
	served := []byte("#!/bin/sh\necho served")
	server, policy := setupScriptTestServer(t, nil, map[string][]byte{"/scripts/pre.sh": served})
	scriptURL := server.URL + "/scripts/pre.sh"
	allowlist := []string{server.URL + "/"}
	treePin := fetch.ComputeTreeHash(map[string][]byte{"pre.sh": served})

	t.Run("directory-pinned file without a tree source fails", func(t *testing.T) {
		_, _, err := fetchBaseFile(context.Background(), "host_files[0].src", server.URL+"/", "scripts/pre.sh", allowlist, ComposeOpts{
			WorkspaceRoot: filepath.Join(t.TempDir(), "cache"),
			FetchPolicy:   policy,
			LockedPins:    map[string]LockedPin{scriptURL: dirPin(treePin)},
		}, "resource", false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pinned in the lock file to directory sha256="+treePin)
	})

	t.Run("directory-pinned file is served from the re-fetched tree", func(t *testing.T) {
		const baseDir = "https://raw.githubusercontent.com/org/repo/ref/"
		files := map[string][]byte{"pre.sh": served}
		dep, contentPath, err := fetchBaseFile(context.Background(), "host_files[0].src", baseDir, "scripts/pre.sh", []string{baseDir}, ComposeOpts{
			WorkspaceRoot: filepath.Join(t.TempDir(), "cache"),
			TreeFetcher:   fakeTreeFetcher(files),
			LockedPins:    map[string]LockedPin{baseDir + "scripts/pre.sh": dirPin(treePin)},
		}, "resource", false)
		require.NoError(t, err)
		assert.Equal(t, treePin, dep.SHA256)
		assert.Equal(t, string(served), readFile(t, contentPath))
	})

	t.Run("transient tree fetch error for a directory-pinned script is returned", func(t *testing.T) {
		const baseDir = "https://raw.githubusercontent.com/org/repo/ref/"
		transient := &gitfetch.TransientError{Err: errors.New("connection reset")}
		_, _, err := fetchBaseScriptOrDir(context.Background(), "pre_script", baseDir, "scripts/pre.sh", []string{baseDir}, ComposeOpts{
			WorkspaceRoot: filepath.Join(t.TempDir(), "cache"),
			TreeFetcher: func(context.Context, string, string, string, string) (map[string][]byte, error) {
				return nil, transient
			},
			LockedPins: map[string]LockedPin{baseDir + "scripts/pre.sh": dirPin(treePin)},
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, transient)
		assert.NotContains(t, err.Error(), "re-pin")
	})

	t.Run("directory-pinned script without a tree source fails", func(t *testing.T) {
		_, _, err := fetchBaseScriptOrDir(context.Background(), "pre_script", server.URL+"/", "scripts/pre.sh", allowlist, ComposeOpts{
			WorkspaceRoot: filepath.Join(t.TempDir(), "cache"),
			FetchPolicy:   policy,
			LockedPins:    map[string]LockedPin{scriptURL: dirPin(treePin)},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pinned in the lock file to directory sha256="+treePin)
	})
}

// TestFetchBaseFile_FromPinnedTree covers fetchBaseFileFromPinnedTree
// branches the composed loads do not reach.
func TestFetchBaseFile_FromPinnedTree(t *testing.T) {
	const baseDir = "https://raw.githubusercontent.com/org/repo/ref/"
	fileURL := baseDir + "scripts/pre.sh"
	script := []byte("#!/bin/sh\necho pinned")

	tests := []struct {
		name       string
		tree       map[string][]byte // seeded tree; nil leaves the pin uncached
		executable bool
		wantErr    string
	}{
		{name: "offline with pinned tree missing", wantErr: "is pinned in the lock file with sha256="},
		{name: "executable", tree: map[string][]byte{"pre.sh": script}, executable: true},
		{name: "file missing from pinned tree", tree: map[string][]byte{"other.sh": script}, wantErr: "has no pre.sh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cacheDir := filepath.Join(t.TempDir(), "cache")
			treePin := fetch.ComputeTreeHash(map[string][]byte{"pre.sh": script})
			if tt.tree != nil {
				treePin = seedLockedTree(t, cacheDir, fileURL, tt.tree, true)
			}
			dep, contentPath, err := fetchBaseFile(context.Background(), "host_files[0].src", baseDir, "scripts/pre.sh", []string{baseDir}, ComposeOpts{
				WorkspaceRoot: cacheDir,
				FetchPolicy:   fetch.FetchPolicy{Offline: true},
				TreeFetcher:   failingTreeFetcher(t),
				LockedPins:    map[string]LockedPin{fileURL: dirPin(treePin)},
			}, "resource", tt.executable)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "directory", dep.Type)
			assert.Equal(t, treePin, dep.SHA256)
			info, err := os.Stat(contentPath)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
		})
	}
}
