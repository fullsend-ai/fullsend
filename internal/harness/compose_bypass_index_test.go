package harness

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
)

// Warm-cache regression tests for BypassURLIndex and the ResolvedKeys
// bookkeeping in the skill-tree and script-directory fetch paths
// (fetchBaseSkill, fetchBaseScriptOrDir): `fullsend lock --update` must
// re-resolve a warm URL from its source and record the changed tree, while
// normal resolution keeps serving the cached tree.

// seedScriptDirIndex caches a script directory tree and points both URL
// index keys at it: the per-file key and the "scriptdir:" key.
func seedScriptDirIndex(t *testing.T, cacheDir, scriptFileURL, scriptDirURL string, files map[string][]byte) string {
	t.Helper()
	treeHash := seedLockedTree(t, cacheDir, scriptFileURL, files, true)
	require.NoError(t, urlIndexPut(cacheDir, scriptFileURL, treeHash))
	require.NoError(t, urlIndexPut(cacheDir, "scriptdir:"+scriptDirURL, treeHash))
	return treeHash
}

// TestFetchBaseSkill_BypassURLIndexRecordsChangedUpstreamTree verifies the
// skill-tree arm of the index bypass: with a warm cache whose URL index
// points at the old skill tree, BypassURLIndex skips the index entries,
// fetches the changed upstream tree and returns it (its contents and its
// dependency hash), while normal resolution on the same warm cache keeps
// serving the cached tree.
func TestFetchBaseSkill_BypassURLIndexRecordsChangedUpstreamTree(t *testing.T) {
	const (
		baseURLDir = "https://raw.githubusercontent.com/org/repo/ref1/"
		skillPath  = "skills/pr-review"
	)
	skillFileURL := baseURLDir + skillPath + "/SKILL.md"
	allowlist := []string{"https://raw.githubusercontent.com/org/repo/"}

	// The upstream tree changed; the fetcher counts its calls.
	upstream := map[string][]byte{
		"SKILL.md":      []byte("# PR Review v2, changed upstream"),
		"sub-agents.md": []byte("# sub-agents"),
	}
	// The expected hash comes from the fixture alone, never from a write
	// into the cache under test: the tree the bypass records must be the
	// one the code under test caches, not one the test placed there.
	newTreeHash := fetch.ComputeTreeHash(upstream)

	// seedWarmSkillCache builds the warm cache: the index points at the
	// old skill tree, and the fetcher fails the test if it runs.
	seedWarmSkillCache := func(t *testing.T) (cacheDir string, oldTreeHash string) {
		t.Helper()
		cacheDir = filepath.Join(t.TempDir(), "cache")
		var err error
		oldTreeHash, err = fetch.CachePutDir(cacheDir, skillFileURL,
			map[string][]byte{"SKILL.md": []byte("# PR Review v1")}, fetch.DirCachePutOpts{FullListing: true})
		require.NoError(t, err)
		require.NoError(t, urlIndexPut(cacheDir, skillFileURL, oldTreeHash))
		require.NoError(t, urlIndexPut(cacheDir, "skill:"+skillFileURL, oldTreeHash))
		require.NotEqual(t, oldTreeHash, newTreeHash, "the changed upstream tree must hash differently")
		return cacheDir, oldTreeHash
	}

	t.Run("normal resolution keeps the cached tree", func(t *testing.T) {
		cacheDir, oldTreeHash := seedWarmSkillCache(t)
		dep, localDir, err := fetchBaseSkill(context.Background(), "skills[0]", baseURLDir,
			skillPath, allowlist, ComposeOpts{
				WorkspaceRoot: cacheDir,
				TreeFetcher: func(_ context.Context, _, _, _, _ string) (map[string][]byte, error) {
					t.Error("the tree fetcher must not run without the bypass")
					return nil, errUnexpectedFetch
				},
			})
		require.NoError(t, err)
		assert.True(t, dep.CacheHit, "without the bypass the index entry must be served")
		assert.Equal(t, oldTreeHash, dep.SHA256)
		assert.Equal(t, "# PR Review v1", readFile(t, filepath.Join(localDir, "SKILL.md")))
	})

	t.Run("bypass re-resolves and records the changed tree", func(t *testing.T) {
		cacheDir, _ := seedWarmSkillCache(t)
		calls := 0
		dep, localDir, err := fetchBaseSkill(context.Background(), "skills[0]", baseURLDir,
			skillPath, allowlist, ComposeOpts{
				WorkspaceRoot:  cacheDir,
				BypassURLIndex: true,
				TreeFetcher: func(_ context.Context, _, _, _, _ string) (map[string][]byte, error) {
					calls++
					return upstream, nil
				},
			})
		require.NoError(t, err)
		assert.Equal(t, 1, calls, "the tree fetcher must run once for the bypass")
		assert.False(t, dep.CacheHit, "the bypass must not serve the index's cached tree")
		assert.Equal(t, newTreeHash, dep.SHA256, "the dependency hash must be the changed upstream tree's")
		assert.Equal(t, "# PR Review v2, changed upstream", readFile(t, filepath.Join(localDir, "SKILL.md")))
		// The companion file is fetched with the tree, not just SKILL.md.
		assert.Equal(t, "# sub-agents", readFile(t, filepath.Join(localDir, "sub-agents.md")))
		// The fresh entries are recorded: both index keys point at the new tree.
		for _, key := range []string{skillFileURL, "skill:" + skillFileURL} {
			got, ok := urlIndexLookup(cacheDir, key)
			require.True(t, ok, "index key %s", key)
			assert.Equal(t, newTreeHash, got, "index key %s must point at the new tree", key)
		}
	})
}

// TestFetchBaseScriptOrDir_BypassSharesResolvedKeysAcrossPasses verifies the
// script-tree ResolvedKeys bookkeeping: with a counting tree fetcher and one
// ResolvedKeys set shared across compose passes (as lock's per-forge loop
// shares it through ComposeOpts), the changed script directory is fetched
// once for the whole invocation and every pass receives the refreshed tree.
func TestFetchBaseScriptOrDir_BypassSharesResolvedKeysAcrossPasses(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	const (
		baseURLDir = "https://raw.githubusercontent.com/org/repo/ref1/"
		relPath    = "scripts/pre-code.sh"
	)
	scriptFileURL := baseURLDir + relPath
	scriptDirURL := baseURLDir + "scripts"
	allowlist := []string{"https://raw.githubusercontent.com/org/repo/"}

	// The warm cache points at the old script directory.
	seedScriptDirIndex(t, cacheDir, scriptFileURL, scriptDirURL,
		map[string][]byte{"pre-code.sh": []byte("#!/bin/sh\necho v1")})

	// The upstream directory changed; the fetcher counts its calls.
	calls := 0
	fetcher := func(_ context.Context, _, _, _, _ string) (map[string][]byte, error) {
		calls++
		return map[string][]byte{
			"pre-code.sh": []byte("#!/bin/sh\necho v2, changed upstream"),
			"helper.sh":   []byte("#!/bin/sh\necho helper"),
		}, nil
	}

	// One ResolvedKeys set shared by every pass of the invocation, exactly
	// as lock's per-forge compose loop shares it through ComposeOpts.
	resolved := map[string]bool{}
	pass := func(field string) (Dependency, string) {
		t.Helper()
		dep, contentPath, err := fetchBaseScriptOrDir(context.Background(), field, baseURLDir,
			relPath, allowlist, ComposeOpts{
				WorkspaceRoot:  cacheDir,
				BypassURLIndex: true,
				ResolvedKeys:   resolved,
				TreeFetcher:    fetcher,
			})
		require.NoError(t, err)
		return dep, contentPath
	}

	dep1, path1 := pass("pre_script")
	assert.False(t, dep1.CacheHit, "the first pass must re-resolve, not serve the index's old tree")
	assert.Equal(t, "#!/bin/sh\necho v2, changed upstream", readFile(t, path1))

	dep2, path2 := pass("post_script")
	assert.True(t, dep2.CacheHit, "the second pass must take the fresh index entry the first pass wrote")
	assert.Equal(t, 1, calls, "the directory must be fetched once for the whole invocation, not once per pass")
	assert.Equal(t, dep1.SHA256, dep2.SHA256, "both passes must receive the refreshed tree")
	assert.Equal(t, "#!/bin/sh\necho v2, changed upstream", readFile(t, path2))
	assert.Equal(t, filepath.Dir(path1), filepath.Dir(path2))
}

// TestFetchBaseScriptOrDir_BypassRecordsChangedScriptDir verifies the
// script-directory arm of the index bypass: with an old script directory and
// both index keys seeded warm, BypassURLIndex re-resolves from the tree
// fetcher and the update records and returns the new script and its
// companion files.
func TestFetchBaseScriptOrDir_BypassRecordsChangedScriptDir(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	const (
		baseURLDir = "https://raw.githubusercontent.com/org/repo/ref1/"
		relPath    = "scripts/pre-code.sh"
	)
	scriptFileURL := baseURLDir + relPath
	scriptDirURL := baseURLDir + "scripts"
	allowlist := []string{"https://raw.githubusercontent.com/org/repo/"}

	oldTreeHash := seedScriptDirIndex(t, cacheDir, scriptFileURL, scriptDirURL,
		map[string][]byte{"pre-code.sh": []byte("#!/bin/sh\necho v1")})

	// The upstream directory changed, and gained a companion file. The
	// expected hash comes from the fixture alone, so the assertions below
	// prove the bypass cached the fetched tree, not one the test placed.
	changed := map[string][]byte{
		"pre-code.sh":     []byte("#!/bin/sh\necho v2, changed upstream"),
		"resolve-tool.py": []byte("#!/usr/bin/env python3\nprint('resolve')"),
	}
	newTreeHash := fetch.ComputeTreeHash(changed)
	require.NotEqual(t, oldTreeHash, newTreeHash)
	calls := 0
	fetcher := func(_ context.Context, _, _, _, _ string) (map[string][]byte, error) {
		calls++
		return changed, nil
	}

	dep, contentPath, err := fetchBaseScriptOrDir(context.Background(), "pre_script", baseURLDir,
		relPath, allowlist, ComposeOpts{
			WorkspaceRoot:  cacheDir,
			BypassURLIndex: true,
			TreeFetcher:    fetcher,
		})
	require.NoError(t, err)

	assert.Equal(t, 1, calls, "the bypass must re-resolve the script directory from the fetcher")
	assert.False(t, dep.CacheHit)
	assert.Equal(t, newTreeHash, dep.SHA256, "the update must record the changed tree's hash")
	assert.Equal(t, "#!/bin/sh\necho v2, changed upstream", readFile(t, contentPath))
	// The companion file is returned beside the script.
	assert.Equal(t, "#!/usr/bin/env python3\nprint('resolve')", readFile(t, filepath.Join(filepath.Dir(contentPath), "resolve-tool.py")))
	// Both index keys are updated to the new tree.
	for _, key := range []string{scriptFileURL, "scriptdir:" + scriptDirURL} {
		got, ok := urlIndexLookup(cacheDir, key)
		require.True(t, ok, "index key %s", key)
		assert.Equal(t, newTreeHash, got, "index key %s must point at the new tree", key)
	}

	// Normal resolution on the now-warm cache serves the new tree.
	dep, contentPath, err = fetchBaseScriptOrDir(context.Background(), "pre_script", baseURLDir,
		relPath, allowlist, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   fetcher,
		})
	require.NoError(t, err)
	assert.True(t, dep.CacheHit)
	assert.Equal(t, 1, calls)
	assert.Equal(t, "#!/bin/sh\necho v2, changed upstream", readFile(t, contentPath))
}

// TestFetchBaseDir_BypassURLIndexSkipsLegacyPluginIndex pins the plugins
// arm of the URL-index bypass — the one re-resolve path whose cache hit
// can come from the legacy-marker fallback: with a warm cache whose only
// index entries are the legacy `<dirURL>/plugin.json` keys pointing at the
// old plugin tree, normal resolution serves that cached tree, while
// BypassURLIndex (what `fullsend lock --update` sets) skips the legacy
// fallback, fetches the changed upstream tree once and records the fresh
// entries under the current key. Reverting the bypass guard on the legacy
// fallback (`if !indexHit` without `!opts.bypassIndexKey(keyURL)`) makes
// the bypass case fail: the stale legacy entry would be served instead.
func TestFetchBaseDir_BypassURLIndexSkipsLegacyPluginIndex(t *testing.T) {
	const (
		baseURLDir = "https://raw.githubusercontent.com/org/repo/ref1/"
		pluginPath = "plugins/gopls-lsp"
	)
	dirURL := baseURLDir + pluginPath
	keyURL := dirURL + "/" // basePluginKind.keyFile
	legacyKey := dirURL + "/plugin.json"
	allowlist := []string{"https://raw.githubusercontent.com/org/repo/"}

	// The upstream plugin tree changed; the fetcher counts its calls.
	upstream := map[string][]byte{
		"plugin.json": []byte(`{"name":"gopls-lsp","version":"2, changed upstream"}`),
	}
	// The expected hash comes from the fixture alone, never from a write
	// into the cache under test: the tree the bypass records must be the
	// one the code under test caches, not one the test placed there.
	newTreeHash := fetch.ComputeTreeHash(upstream)

	// seedLegacyPluginIndex builds the warm cache: only the legacy
	// plugin.json index keys exist, pointing at the old plugin tree, so a
	// cache hit can only come from the legacy fallback.
	seedLegacyPluginIndex := func(t *testing.T) (cacheDir string, oldTreeHash string) {
		t.Helper()
		cacheDir = filepath.Join(t.TempDir(), "cache")
		oldTreeHash = seedLockedTree(t, cacheDir, legacyKey,
			map[string][]byte{"plugin.json": []byte(`{"name":"gopls-lsp","version":"1"}`)}, true)
		require.NoError(t, urlIndexPut(cacheDir, legacyKey, oldTreeHash))
		require.NoError(t, urlIndexPut(cacheDir, "plugin:"+legacyKey, oldTreeHash))
		require.NotEqual(t, oldTreeHash, newTreeHash, "the changed upstream tree must hash differently")
		return cacheDir, oldTreeHash
	}

	t.Run("normal resolution keeps the cached legacy tree", func(t *testing.T) {
		cacheDir, oldTreeHash := seedLegacyPluginIndex(t)
		dep, localDir, err := fetchBasePlugin(context.Background(), "plugins[0]", baseURLDir,
			pluginPath, allowlist, ComposeOpts{
				WorkspaceRoot: cacheDir,
				TreeFetcher: func(_ context.Context, _, _, _, _ string) (map[string][]byte, error) {
					t.Error("the tree fetcher must not run without the bypass")
					return nil, errUnexpectedFetch
				},
			})
		require.NoError(t, err)
		assert.True(t, dep.CacheHit, "without the bypass the legacy index entry must be served")
		assert.Equal(t, oldTreeHash, dep.SHA256)
		assert.Equal(t, `{"name":"gopls-lsp","version":"1"}`, readFile(t, filepath.Join(localDir, "plugin.json")))
	})

	t.Run("bypass re-resolves and records the current key", func(t *testing.T) {
		cacheDir, _ := seedLegacyPluginIndex(t)
		calls := 0
		dep, localDir, err := fetchBasePlugin(context.Background(), "plugins[0]", baseURLDir,
			pluginPath, allowlist, ComposeOpts{
				WorkspaceRoot:  cacheDir,
				BypassURLIndex: true,
				TreeFetcher: func(_ context.Context, _, _, _, _ string) (map[string][]byte, error) {
					calls++
					return upstream, nil
				},
			})
		require.NoError(t, err)
		assert.Equal(t, 1, calls, "the bypass must re-resolve the plugin directory from the fetcher")
		assert.False(t, dep.CacheHit, "the bypass must not serve the legacy entry's cached tree")
		assert.Equal(t, newTreeHash, dep.SHA256, "the update must record the changed tree's hash")
		assert.Equal(t, `{"name":"gopls-lsp","version":"2, changed upstream"}`, readFile(t, filepath.Join(localDir, "plugin.json")))
		// The fresh entries are recorded under the current key, both of them.
		for _, key := range []string{keyURL, "plugin:" + keyURL} {
			got, ok := urlIndexLookup(cacheDir, key)
			require.True(t, ok, "index key %s", key)
			assert.Equal(t, newTreeHash, got, "index key %s must point at the new tree", key)
		}
	})
}

// errUnexpectedFetch is the error the normal-resolution control returns from
// its tree fetcher, which must never be called.
var errUnexpectedFetch = errors.New("tree fetcher must not be called")
