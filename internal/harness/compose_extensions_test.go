package harness

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
)

func TestLoadWithBase_ExtensionsConcat(t *testing.T) {
	dir := t.TempDir()
	writeTestHarness(t, dir, "base.yaml", `
agent: agents/base.md
role: test
extensions:
  - extensions/from-base
`)
	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: base.yaml
extensions:
  - path: extensions/from-child
    args: ["--fff-mode", "x"]
    env:
      CHILD_FLAG: "1"
`)
	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)
	require.Len(t, h.Extensions, 2, "base + child, base first (same as plugins)")
	assert.Equal(t, "extensions/from-base", h.Extensions[0].Path)
	assert.Equal(t, "extensions/from-child", h.Extensions[1].Path)
	assert.Equal(t, []string{"--fff-mode", "x"}, h.Extensions[1].Args)
	assert.Equal(t, map[string]string{"CHILD_FLAG": "1"}, h.Extensions[1].Env)

	// A child without extensions inherits the base list; a base without
	// extensions leaves the child's untouched.
	path = writeTestHarness(t, dir, "child2.yaml", "agent: agents/child.md\nrole: test\nbase: base.yaml\n")
	h, _, err = LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)
	assert.Equal(t, []ExtensionSpec{{Path: "extensions/from-base"}}, h.Extensions)

	writeTestHarness(t, dir, "bare-base.yaml", "agent: agents/base.md\nrole: test\n")
	path = writeTestHarness(t, dir, "child3.yaml", `
agent: agents/child.md
role: test
base: bare-base.yaml
extensions:
  - extensions/from-child
`)
	h, _, err = LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)
	assert.Equal(t, []ExtensionSpec{{Path: "extensions/from-child"}}, h.Extensions)
}

func TestFetchBaseExtension_FreshFetch(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	fetcher := fakeTreeFetcher(map[string][]byte{
		"index.js":  []byte("export default function () {}"),
		"lib/x.js":  []byte("//"),
		"README.md": []byte("# ext"),
	})
	dep, localDir, err := fetchBaseExtension(context.Background(), "extensions[0]",
		"https://raw.githubusercontent.com/org/repo/ref/",
		"extensions/go-diagnostics", []string{"https://raw.githubusercontent.com/org/repo/"}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   fetcher,
		})
	require.NoError(t, err)
	assert.False(t, dep.CacheHit)
	assert.Equal(t, "directory", dep.Type)
	assert.Equal(t, "extensions[0]", dep.Field)
	assert.Equal(t, "https://raw.githubusercontent.com/org/repo/ref/extensions/go-diagnostics/", dep.URL)
	assert.Equal(t, "go-diagnostics", filepath.Base(localDir))
	assert.FileExists(t, filepath.Join(localDir, "index.js"))
	assert.FileExists(t, filepath.Join(localDir, "lib", "x.js"))

	// The fetched tree passes the same loadability rule as a local dir.
	h := &Harness{Agent: filepath.Join(localDir, "index.js"), Extensions: []ExtensionSpec{{Path: localDir}}}
	require.NoError(t, h.ValidateFilesExist())

	// Second call is a full cache hit.
	dep, localDir2, err := fetchBaseExtension(context.Background(), "extensions[0]",
		"https://raw.githubusercontent.com/org/repo/ref/",
		"extensions/go-diagnostics", []string{"https://raw.githubusercontent.com/org/repo/"}, ComposeOpts{
			WorkspaceRoot: cacheDir,
		})
	require.NoError(t, err)
	assert.True(t, dep.CacheHit)
	assert.Equal(t, localDir, localDir2)
}

func TestFetchBaseExtension_NotLoadable(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	fetcher := fakeTreeFetcher(map[string][]byte{
		"README.md":   []byte("# ext"),
		"src/main.js": []byte("//"),
	})
	_, _, err := fetchBaseExtension(context.Background(), "extensions[0]",
		"https://raw.githubusercontent.com/org/repo/ref/",
		"extensions/broken", []string{"https://raw.githubusercontent.com/org/repo/"}, ComposeOpts{
			WorkspaceRoot: cacheDir,
			TreeFetcher:   fetcher,
		})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pi would fail to load it")
}

func TestFetchBaseExtension_AllowlistAndOffline(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	_, _, err := fetchBaseExtension(context.Background(), "extensions[0]",
		"https://raw.githubusercontent.com/org/repo/ref/",
		"extensions/x", []string{"https://raw.githubusercontent.com/other/"}, ComposeOpts{WorkspaceRoot: cacheDir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in allowed_remote_resources")

	_, _, err = fetchBaseExtension(context.Background(), "extensions[0]",
		"https://raw.githubusercontent.com/org/repo/ref/",
		"extensions/x", []string{"https://raw.githubusercontent.com/org/repo/"}, ComposeOpts{
			WorkspaceRoot: cacheDir, FetchPolicy: fetch.FetchPolicy{Offline: true},
		})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "offline mode")
}

func TestResolveBaseExtensions_Validation(t *testing.T) {
	baseURL := "https://raw.githubusercontent.com/org/repo/ref/harness/triage.yaml"
	allow := []string{"https://raw.githubusercontent.com/org/repo/"}

	_, err := resolveBaseExtensions(context.Background(), &Harness{Extensions: []ExtensionSpec{{Path: "extensions/x"}}}, "", nil, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot determine directory")

	_, err = resolveBaseExtensions(context.Background(), &Harness{Extensions: []ExtensionSpec{{Path: "../../etc"}}}, baseURL, allow, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path traversal")

	_, err = resolveBaseExtensions(context.Background(), &Harness{Extensions: []ExtensionSpec{{Path: "/abs/ext"}}}, baseURL, allow, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an absolute path")

	_, err = resolveBaseExtensions(context.Background(), &Harness{Extensions: []ExtensionSpec{{Path: "extensions/bad name"}}}, baseURL, allow, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "valid extension basename")

	// Empty and already-cached entries are skipped; no extensions is a no-op.
	cacheDir := filepath.Join(t.TempDir(), "cache")
	base := &Harness{Extensions: []ExtensionSpec{
		{Path: ""},
		{Path: filepath.Join(cacheDir, ".fullsend-cache/sha256/abc/my-ext")},
	}}
	deps, err := resolveBaseExtensions(context.Background(), base, baseURL, nil, ComposeOpts{WorkspaceRoot: cacheDir})
	require.NoError(t, err)
	assert.Empty(t, deps)
	deps, err = resolveBaseExtensions(context.Background(), &Harness{}, "", nil, ComposeOpts{})
	require.NoError(t, err)
	assert.Empty(t, deps)
}

// seedExtensionCache pre-populates the content-addressed cache and URL
// index the way a prior online fetch would have, so LoadWithBase can run
// offline against it.
func seedExtensionCache(t *testing.T, cacheDir, dirURL string, files map[string][]byte) {
	t.Helper()
	treeHash, err := fetch.CachePutDir(cacheDir, dirURL, files, fetch.DirCachePutOpts{FullListing: true})
	require.NoError(t, err)
	require.NoError(t, urlIndexPut(cacheDir, dirURL, treeHash))
	require.NoError(t, urlIndexPut(cacheDir, "extension:"+dirURL, treeHash))
}

func TestLoadWithBase_URLBase_ExtensionOfflineCacheHit(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")

	baseContent := []byte(`
agent: agents/triage.md
role: test
extensions:
  - path: extensions/go-diagnostics
    args: ["--strict"]
`)
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/harness/triage.yaml", baseContent))
	agentRes := []byte("# triage agent")
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/agents/triage.md", agentRes))
	require.NoError(t, urlIndexPut(cacheDir, "https://example.com/agents/triage.md", fetch.ComputeSHA256(agentRes)))
	extFiles := map[string][]byte{"index.js": []byte("export default function () {}")}
	seedExtensionCache(t, cacheDir, "https://example.com/extensions/go-diagnostics/", extFiles)

	path := writeTestHarness(t, dir, "child.yaml", `
agent: agents/child.md
role: test
base: https://example.com/harness/triage.yaml#sha256=`+computeHash(baseContent)+`
extensions:
  - extensions/local-child
`)
	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
		OrgAllowlist:  []string{"https://example.com/"},
	})
	require.NoError(t, err)
	require.Len(t, h.Extensions, 2)
	assert.True(t, filepath.IsAbs(h.Extensions[0].Path), "base extension resolved to a cache path: %s", h.Extensions[0].Path)
	assert.Equal(t, "go-diagnostics", filepath.Base(h.Extensions[0].Path))
	assert.Equal(t, []string{"--strict"}, h.Extensions[0].Args, "args survive the cache rewrite")
	assert.Equal(t, "extensions/local-child", h.Extensions[1].Path, "child's local entry is left for ResolveRelativeTo")
	content, err := os.ReadFile(filepath.Join(h.Extensions[0].Path, "index.js"))
	require.NoError(t, err)
	assert.Equal(t, extFiles["index.js"], content)

	var extDep *Dependency
	for i := range deps {
		if deps[i].Field == "extensions[0]" {
			extDep = &deps[i]
		}
	}
	require.NotNil(t, extDep, "extension recorded as a dependency: %+v", deps)
	assert.True(t, extDep.CacheHit)
	assert.Equal(t, "directory", extDep.Type)
}

func TestLoadWithBase_SourceURL_Extensions(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	fullsendDir := filepath.Join(dir, "fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))

	agentRes := []byte("# triage agent")
	require.NoError(t, fetch.CachePut(cacheDir, "https://example.com/agents/triage.md", agentRes))
	require.NoError(t, urlIndexPut(cacheDir, "https://example.com/agents/triage.md", fetch.ComputeSHA256(agentRes)))
	seedExtensionCache(t, cacheDir, "https://example.com/extensions/go-diagnostics/", map[string][]byte{"index.ts": []byte("//")})

	path := writeTestHarness(t, dir, "triage.yaml", `
role: test
slug: test
agent: agents/triage.md
extensions:
  - extensions/go-diagnostics
`)
	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: cacheDir,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
		OrgAllowlist:  []string{"https://example.com/"},
		SourceURL:     "https://example.com/harness/triage.yaml",
	})
	require.NoError(t, err)
	require.Len(t, h.Extensions, 1)
	assert.True(t, filepath.IsAbs(h.Extensions[0].Path))

	// Same flow as run.go: the cache path must survive ResolveRelativeTo and
	// pass ValidateFilesExist, rather than being re-rooted under fullsendDir.
	require.NoError(t, h.ResolveRelativeTo(fullsendDir))
	require.NoError(t, h.ValidateFilesExist())
}
