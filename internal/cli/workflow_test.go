package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
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
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/pluginformat"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	cliWorkflowSHA  = "0123456789abcdef0123456789abcdef01234567"
	cliWorkflowSHA2 = "89abcdef0123456789abcdef0123456789abcdef"
)

func cliWorkflowTree() map[string][]byte {
	return map[string][]byte{
		".claude-plugin/plugin.json": []byte(`{"name":"wfplug"}`),
		"workflows/probe.js":         []byte("export const meta = { name: 'probe' };\n"),
	}
}

func cliPiTree() map[string][]byte {
	return map[string][]byte{"index.js": []byte("export default function (pi) {}\n")}
}

func TestCheckWorkflowRuntime(t *testing.T) {
	wf := &harness.Harness{Workflow: &harness.WorkflowSpec{Source: ".", Name: "n"}}
	tests := []struct {
		name    string
		h       *harness.Harness
		runtime string
		wantErr string
	}{
		{name: "no workflow under codex", h: &harness.Harness{}, runtime: "codex"},
		{name: "claude", h: wf, runtime: "claude"},
		{name: "pi", h: wf, runtime: "pi"},
		{name: "dummy", h: wf, runtime: "dummy"},
		{name: "dummy-playback", h: wf, runtime: "dummy-playback"},
		{name: "codex", h: wf, runtime: "codex", wantErr: `workflow: is not supported by the codex runtime; agent "code" resolves to "codex", but a workflow definition runs on claude (a Claude Code plugin) or pi (a pi extension); set runtime: claude or runtime: pi for this agent, or remove workflow: from its harness`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkWorkflowRuntime(tt.h, "code", tt.runtime)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.wantErr, err.Error())
		})
	}
}

func TestCheckWorkflowKind(t *testing.T) {
	claude := &resolve.ResolvedWorkflow{Kind: pluginformat.KindClaude, Local: true, Source: "wfdef"}
	pi := &resolve.ResolvedWorkflow{Kind: pluginformat.KindPi, Owner: "example-org", Repo: "sample-pipeline", Commit: cliWorkflowSHA}
	tests := []struct {
		name    string
		rw      *resolve.ResolvedWorkflow
		runtime string
		wantErr string
	}{
		{name: "no workflow", runtime: "pi"},
		{name: "claude plugin on claude", rw: claude, runtime: "claude"},
		{name: "pi extension on pi", rw: pi, runtime: "pi"},
		{name: "either on dummy", rw: pi, runtime: "dummy"},
		{name: "claude plugin on pi", rw: claude, runtime: "pi", wantErr: `workflow.source wfdef is a Claude Code plugin, but agent "code" resolves to the pi runtime, which runs a pi extension; set runtime: claude for this agent, or point workflow.source at a pi extension`},
		{name: "pi extension on claude", rw: pi, runtime: "claude", wantErr: `workflow.source example-org/sample-pipeline@0123456789ab is a pi extension, but agent "code" resolves to the claude runtime, which runs a Claude Code plugin; set runtime: pi for this agent, or point workflow.source at a Claude Code plugin`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkWorkflowKind(tt.rw, "code", tt.runtime)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.wantErr, err.Error())
		})
	}
}

func TestDescribeWorkflow(t *testing.T) {
	hash := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	tests := []struct {
		name string
		rw   resolve.ResolvedWorkflow
		want string
	}{
		{
			name: "remote sub-directory, claude",
			rw:   resolve.ResolvedWorkflow{Kind: pluginformat.KindClaude, Name: "probe", Args: "--issue 7", Owner: "example-org", Repo: "sample-pipeline", Commit: cliWorkflowSHA, Path: "pipelines/sample", TreeHash: hash, PluginName: "wfplug"},
			want: "example-org/sample-pipeline@0123456789ab/pipelines/sample (sha256:abcdef012345) delivered as claude plugin wfplug; workflow probe",
		},
		{
			name: "local root, pi",
			rw:   resolve.ResolvedWorkflow{Kind: pluginformat.KindPi, Local: true, Source: ".", TreeHash: hash},
			want: ". (sha256:abcdef012345) delivered as pi extension",
		},
		{
			name: "short values are kept",
			rw:   resolve.ResolvedWorkflow{Kind: pluginformat.KindClaude, Name: "n", Owner: "o", Repo: "r", Commit: "abc", TreeHash: "def", PluginName: "p"},
			want: "o/r@abc (sha256:def) delivered as claude plugin p; workflow n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, describeWorkflow(&tt.rw))
		})
	}
}

func TestWorkflowLocation(t *testing.T) {
	loc := workflowLocation("/repo/.fullsend/harness/code.yaml", "/repo/.fullsend", true)
	assert.Equal(t, resolve.WorkflowLocation{HarnessDir: "/repo/.fullsend/harness", FullsendDir: "/repo/.fullsend", AddedByURL: true}, loc)
}

func TestResolveHarnessWorkflow(t *testing.T) {
	t.Run("no workflow", func(t *testing.T) {
		rw, dep, err := resolveHarnessWorkflow(context.Background(), &harness.Harness{}, resolve.WorkflowLocation{}, resolve.ResolveOpts{})
		require.NoError(t, err)
		assert.Nil(t, rw)
		assert.Nil(t, dep)
	})
	t.Run("path source resolves in the checkout that holds the harness", func(t *testing.T) {
		repo := t.TempDir()
		fullsendDir := filepath.Join(repo, ".fullsend")
		require.NoError(t, os.MkdirAll(filepath.Join(fullsendDir, "harness"), 0o755))
		writeTree(t, filepath.Join(repo, "def"), cliWorkflowTree())
		gitInit(t, repo)
		h := &harness.Harness{Workflow: &harness.WorkflowSpec{Source: "def", Name: "probe"}}
		loc := workflowLocation(filepath.Join(fullsendDir, "harness", "code.yaml"), fullsendDir, false)
		rw, dep, err := resolveHarnessWorkflow(context.Background(), h, loc, resolve.ResolveOpts{WorkspaceRoot: fullsendDir})
		require.NoError(t, err)
		assert.Nil(t, dep)
		assert.Equal(t, "def (sha256:"+rw.TreeHash[:12]+") delivered as claude plugin wfplug; workflow probe", describeWorkflow(rw))
	})
	// A pi extension definition joins h.Plugins under the fixed sandbox
	// directory, and the bootstrap input hands it to the pi runtime as a
	// pi plugin (TestPiRuntimeBootstrap_WorkflowDefinition covers the
	// upload and the -e argument from there).
	t.Run("pi extension reaches the bootstrap plugin list", func(t *testing.T) {
		repo := t.TempDir()
		fullsendDir := filepath.Join(repo, ".fullsend")
		require.NoError(t, os.MkdirAll(filepath.Join(fullsendDir, "harness"), 0o755))
		writeTree(t, filepath.Join(repo, "def"), cliPiTree())
		gitInit(t, repo)
		h := &harness.Harness{Workflow: &harness.WorkflowSpec{Source: "def"}}
		loc := workflowLocation(filepath.Join(fullsendDir, "harness", "code.yaml"), fullsendDir, false)
		rw, _, err := resolveHarnessWorkflow(context.Background(), h, loc, resolve.ResolveOpts{WorkspaceRoot: fullsendDir})
		require.NoError(t, err)
		require.Len(t, h.Plugins, 1)
		assert.Equal(t, rw.LocalPath, h.Plugins[0].Path)
		inputs, err := pluginInputs(h.Plugins)
		require.NoError(t, err)
		require.Len(t, inputs, 1)
		assert.Equal(t, harness.WorkflowSandboxDir, inputs[0].Name)
		assert.Equal(t, pluginformat.KindPi, inputs[0].Kind)
		assert.Equal(t, rw.LocalPath, inputs[0].Path)
	})
}

func writeTree(t *testing.T, root string, files map[string][]byte) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, c, 0o644))
	}
}

// workflowHarness is a code harness whose workflow: pins source.
func workflowHarness(source string) string {
	return "agent: agents/code.md\nrole: test\nworkflow:\n  source: " + source + "\n  name: probe\n"
}

func pinnedWorkflowSource(repo, sha string, files map[string][]byte) string {
	return "https://github.com/example-org/" + repo + "/tree/" + sha + "#sha256=" + fetch.ComputeTreeHash(files)
}

// workflowLockDir writes a code harness (harnessYAML) and a config.yaml
// allowlisting example-org on github.com and raw.githubusercontent.com.
func workflowLockDir(t *testing.T, harnessYAML string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"), []byte(harnessYAML), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(workflowAllowlist), 0o644))
	return dir
}

const workflowAllowlist = "allowed_remote_resources:\n  - https://github.com/example-org/\n  - https://raw.githubusercontent.com/example-org/\n"

func TestRunLock_WorkflowDefinition(t *testing.T) {
	files := cliWorkflowTree()
	treeHash := fetch.ComputeTreeHash(files)
	url := "https://github.com/example-org/sample-pipeline/tree/" + cliWorkflowSHA
	dir := workflowLockDir(t, workflowHarness(url+"#sha256="+treeHash))

	fetcher := func(context.Context, string, string, string, string) (map[string][]byte, error) {
		return files, nil
	}
	printer := ui.New(io.Discard)
	require.NoError(t, runLock(context.Background(), "code", dir, "", false, resolveFlags{treeFetcher: gitfetch.TreeFetchFunc(fetcher)}, printer))

	lf, err := lock.Load(filepath.Join(dir, "lock.yaml"))
	require.NoError(t, err)
	assert.Equal(t, 2, lf.Version, "a lock file that records a workflow definition is version 2, which older releases refuse")
	entry := lf.Lookup("code")
	require.NotNil(t, entry)
	require.Len(t, entry.Dependencies, 1)
	dep := entry.Dependencies[0]
	assert.Equal(t, "workflow", dep.Field)
	assert.Equal(t, url, dep.URL)
	assert.Equal(t, "directory", dep.Type)
	assert.Equal(t, treeHash, dep.SHA256)
	assert.Len(t, dep.Files, 2)

	// Replaying the lock verifies the cache and leaves skills and
	// plugins alone: the definition is delivered by its own resolver.
	h, err := harness.Load(filepath.Join(dir, "harness", "code.yaml"))
	require.NoError(t, err)
	require.NoError(t, h.ResolveRelativeTo(dir))
	result, err := resolveFromLock(h, entry, dir, []string{"https://github.com/example-org/"}, printer)
	require.NoError(t, err)
	assert.Empty(t, result.Deps, "the definition is resolved by its own step, from the entry's hash")
	assert.Empty(t, h.Skills)
	assert.Empty(t, h.Plugins)
}

func TestRunLock_WorkflowDefinitionError(t *testing.T) {
	files := cliWorkflowTree()
	dir := workflowLockDir(t, workflowHarness(pinnedWorkflowSource("sample-pipeline", cliWorkflowSHA, files)))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("allowed_remote_resources:\n  - https://github.com/other-org/\n"), 0o644))
	err := runLock(context.Background(), "code", dir, "", false, resolveFlags{}, ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `workflow.source URL "https://github.com/example-org/sample-pipeline/tree/`+cliWorkflowSHA+`" is not covered by allowed_remote_resources`)
}

// A harness registered by URL resolves relative paths in the consumer's
// repository, so its relative workflow.source is refused (ADR 0130 rule
// 1), here for a config.yaml agents: entry whose source is a URL.
func TestRunLock_WorkflowRelativeSourceInHarnessAddedByURL(t *testing.T) {
	dir := t.TempDir()
	harnessURL := "https://raw.githubusercontent.com/example-org/agents/" + cliWorkflowSHA + "/harness/code.yaml"
	content := []byte("agent: agents/code.md\nrole: test\nworkflow:\n  source: pipelines/sample\n  name: probe\n")
	require.NoError(t, fetch.CachePut(dir, harnessURL, content))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(workflowAllowlist+
		"agents:\n  - name: code\n    source: "+harnessURL+"#sha256="+fetch.ComputeSHA256(content)+"\n"), 0o644))
	gitInit(t, dir)

	err := runLock(context.Background(), "code", dir, "", false, resolveFlags{offline: true}, ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `workflow.source "pipelines/sample" is relative, but this harness was added by URL, so relative paths resolve in your repository, not the harness's; pin source as a tree URL with #sha256=, or install the harness through a one-line base: harness`)
}

// newWorkflowRunDir is newSkipHarnessDir with a workflow: harness whose
// source is wfdef, a definition with the given files. The fullsend dir is
// the git checkout itself, as for an org configuration repository, so
// the source is wfdef.
func newWorkflowRunDir(t *testing.T, runtimeLine string, files map[string][]byte, spec string) string {
	t.Helper()
	dir := newSkipHarnessDir(t, "")
	writeTree(t, filepath.Join(dir, "wfdef"), files)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: test\nworkflow:\n  source: wfdef\n"+spec), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(runtimeLine+"agents:\n  - harness/code.yaml\n"), 0o644))
	gitInit(t, dir)
	return dir
}

func runWorkflowAgent(t *testing.T, dir string, flags resolveFlags) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	if flags.maxDepth == 0 {
		flags.maxDepth, flags.maxResources = 10, 50
	}
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", flags,
		statusOpts{}, ui.New(&buf), false, runOverrideFlags{})
	return buf.String(), err
}

const claudeWorkflowSpec = "  name: probe\n  args: --issue 7\n"

func TestRunAgent_WorkflowPlanLine(t *testing.T) {
	usePreScriptStub(t)
	dir := newWorkflowRunDir(t, "", cliWorkflowTree(), claudeWorkflowSpec)

	out, err := runWorkflowAgent(t, dir, resolveFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating sandbox", "the workflow resolved and the run reached sandbox creation")
	assert.Contains(t, out, "wfdef (sha256:")
	assert.Contains(t, out, ") delivered as claude plugin wfplug; workflow probe")
	assert.NotContains(t, out, "/wfplug:probe", "the plan does not promise a command the runner does not start")
	assert.Contains(t, out, "workflow-definition (claude)", "the definition is delivered as a Claude plugin under the fixed sandbox name")
}

func TestRunAgent_WorkflowPiExtension(t *testing.T) {
	usePreScriptStub(t)
	dir := newWorkflowRunDir(t, "runtime: pi\n", cliPiTree(), "")

	out, err := runWorkflowAgent(t, dir, resolveFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating sandbox", "the workflow resolved and the run reached sandbox creation")
	assert.Contains(t, out, ") delivered as pi extension")
	assert.Contains(t, out, "workflow-definition (pi)", "the definition is delivered as a pi extension")
}

func TestRunAgent_WorkflowKindMismatch(t *testing.T) {
	usePreScriptStub(t)
	dir := newWorkflowRunDir(t, "runtime: pi\n", cliWorkflowTree(), claudeWorkflowSpec)

	_, err := runWorkflowAgent(t, dir, resolveFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `workflow.source wfdef is a Claude Code plugin, but agent "code" resolves to the pi runtime, which runs a pi extension`)
	assert.NotContains(t, err.Error(), "creating sandbox")
}

func TestRunAgent_WorkflowRefusedUnderCodexBeforeFetch(t *testing.T) {
	usePreScriptStub(t)
	dir := newWorkflowRunDir(t, "runtime: codex\n", cliWorkflowTree(), claudeWorkflowSpec)
	// A source that would fail resolution: the runtime is refused first.
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "wfdef")))

	_, err := runWorkflowAgent(t, dir, resolveFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `workflow: is not supported by the codex runtime; agent "code" resolves to "codex"`)
}

func TestRunAgent_WorkflowResolutionError(t *testing.T) {
	usePreScriptStub(t)
	dir := newWorkflowRunDir(t, "", cliWorkflowTree(), claudeWorkflowSpec)
	require.NoError(t, os.Remove(filepath.Join(dir, "wfdef", "workflows", "probe.js")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "wfdef", "workflows", "other.js"), []byte("x"), 0o644))
	gitIn(t, dir, "add", "-A")

	_, err := runWorkflowAgent(t, dir, resolveFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `workflow.source wfdef has no workflows/probe.js; the definition ships: other`)
}

// A relative source that a local base harness declares resolves in the
// git checkout that holds the base file, here a separate checkout nested
// in the workspace, not in the child's checkout, which has no such path.
func TestRunAgent_WorkflowFromLocalBaseInNestedCheckout(t *testing.T) {
	usePreScriptStub(t)
	dir := newSkipHarnessDir(t, "")
	nested := filepath.Join(dir, "vendor", "sample-pipeline")
	writeTree(t, filepath.Join(nested, "pipelines", "x"), cliWorkflowTree())
	require.NoError(t, os.WriteFile(filepath.Join(nested, "base.yaml"), []byte("workflow:\n  source: pipelines/x\n  name: probe\n"), 0o644))
	gitInit(t, nested)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"),
		[]byte("base: ../vendor/sample-pipeline/base.yaml\nagent: agents/code.md\nrole: test\n"), 0o644))
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "add", "agents", "harness", "config.yaml")

	out, err := runWorkflowAgent(t, dir, resolveFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating sandbox", "the workflow resolved and the run reached sandbox creation")
	assert.Contains(t, out, "pipelines/x (sha256:"+fetch.ComputeTreeHash(cliWorkflowTree())[:12]+") delivered as claude plugin wfplug; workflow probe")
}

// A remote workflow.source counts as a URL reference, so a harness whose
// only remote dependency is its definition gets the allowlist entry
// checks every URL harness gets, and an invalid entry is refused before
// anything is fetched.
func TestRunAgent_WorkflowOnlyHarnessValidatesAllowlist(t *testing.T) {
	usePreScriptStub(t)
	dir := newSkipHarnessDir(t, "")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"),
		[]byte(workflowHarness(pinnedWorkflowSource("sample-pipeline", cliWorkflowSHA, cliWorkflowTree()))), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("allowed_remote_resources:\n  - h\nagents:\n  - harness/code.yaml\n"), 0o644))
	fetched := false
	_, err := runWorkflowAgent(t, dir, resolveFlags{treeFetcher: func(context.Context, string, string, string, string) (map[string][]byte, error) {
		fetched = true
		return cliWorkflowTree(), nil
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `validating allowed remote resources: org allowlist[0]: "h" is not a valid HTTPS URL`)
	assert.False(t, fetched, "nothing is fetched under an invalid allowlist")
}

// gitInit makes dir a git checkout with everything in it staged: a path
// workflow.source reads the index.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "add", "-A")
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

// refTrees serves a different definition tree per commit.
func refTrees() (map[string]map[string][]byte, gitfetch.TreeFetchFunc) {
	trees := map[string]map[string][]byte{
		cliWorkflowSHA:  cliWorkflowTree(),
		cliWorkflowSHA2: {".claude-plugin/plugin.json": []byte(`{"name":"wfplug"}`), "workflows/probe.js": []byte("export const meta = { name: 'probe', description: 'v2' };\n")},
	}
	return trees, func(_ context.Context, _, _, ref, _ string) (map[string][]byte, error) {
		if files, ok := trees[ref]; ok {
			return files, nil
		}
		return nil, errors.New("no tree at " + ref)
	}
}

func lockedWorkflow(t *testing.T, dir string) lock.DependencyEntry {
	t.Helper()
	lf, err := lock.Load(filepath.Join(dir, "lock.yaml"))
	require.NoError(t, err)
	entry := lf.Lookup("code")
	require.NotNil(t, entry)
	for _, d := range entry.Dependencies {
		if d.Field == resolve.WorkflowDependencyField {
			return d
		}
	}
	t.Fatal("lock.yaml records no workflow definition")
	return lock.DependencyEntry{}
}

func relock(t *testing.T, dir string, flags resolveFlags) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, runLock(context.Background(), "code", dir, "", false, flags, ui.New(&buf)))
	return buf.String()
}

// The pin lives in the harness, so the harness hash the lock entry
// records covers it: changing the pin makes the entry stale.
func TestRunLock_WorkflowPinFreshness(t *testing.T) {
	trees, fetcher := refTrees()
	flags := resolveFlags{treeFetcher: fetcher}
	dir := workflowLockDir(t, workflowHarness(pinnedWorkflowSource("sample-pipeline", cliWorkflowSHA, trees[cliWorkflowSHA])))
	relock(t, dir, flags)
	assert.Contains(t, relock(t, dir, flags), "Lock entry for code is up to date")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"),
		[]byte(workflowHarness(pinnedWorkflowSource("sample-pipeline", cliWorkflowSHA2, trees[cliWorkflowSHA2]))), 0o644))
	assert.NotContains(t, relock(t, dir, flags), "is up to date")
	dep := lockedWorkflow(t, dir)
	assert.Equal(t, "https://github.com/example-org/sample-pipeline/tree/"+cliWorkflowSHA2, dep.URL)
	assert.Equal(t, fetch.ComputeTreeHash(trees[cliWorkflowSHA2]), dep.SHA256)
}

// A relative source in a URL base resolves in the base's repository at
// the base's commit; moving the base: pin to another commit changes the
// child harness bytes, so the lock entry is stale and re-locks to the
// definition at the new commit.
func TestRunLock_WorkflowThroughURLBase(t *testing.T) {
	trees, treeFetcher := refTrees()
	var fetched []string
	flags := resolveFlags{treeFetcher: func(ctx context.Context, cloneURL, path, ref, token string) (map[string][]byte, error) {
		fetched = append(fetched, cloneURL+" "+path+" "+ref)
		return treeFetcher(ctx, cloneURL, path, ref, token)
	}}
	baseContent := []byte("workflow:\n  source: pipelines/sample\n  name: probe\n")
	baseAt := func(t *testing.T, dir, sha string) string {
		u := "https://raw.githubusercontent.com/example-org/sample-pipeline/" + sha + "/.fullsend/harness/base.yaml"
		require.NoError(t, fetch.CachePut(dir, u, baseContent))
		return u + "#sha256=" + fetch.ComputeSHA256(baseContent)
	}
	child := func(base string) string { return "base: " + base + "\nagent: agents/code.md\nrole: test\n" }

	dir := workflowLockDir(t, "")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"), []byte(child(baseAt(t, dir, cliWorkflowSHA))), 0o644))
	relock(t, dir, flags)
	dep := lockedWorkflow(t, dir)
	assert.Equal(t, "https://raw.githubusercontent.com/example-org/sample-pipeline/"+cliWorkflowSHA+"/pipelines/sample/", dep.URL)
	assert.Equal(t, fetch.ComputeTreeHash(trees[cliWorkflowSHA]), dep.SHA256, "the tree hash is recorded although the source has no #sha256=")
	assert.Equal(t, []string{"https://github.com/example-org/sample-pipeline.git pipelines/sample " + cliWorkflowSHA}, fetched)
	assert.Contains(t, relock(t, dir, flags), "Lock entry for code is up to date")

	// The lock replays under the raw-content prefix alone, the one a URL
	// base: needs anyway.
	h, _, err := harness.LoadWithBase(context.Background(), filepath.Join(dir, "harness", "code.yaml"), harness.ComposeOpts{
		WorkspaceRoot: dir, FetchPolicy: fetch.FetchPolicy{Offline: true}, OrgAllowlist: []string{"https://raw.githubusercontent.com/example-org/"},
	})
	require.NoError(t, err)
	lf, err := lock.Load(filepath.Join(dir, "lock.yaml"))
	require.NoError(t, err)
	_, err = resolveFromLock(h, lf.Lookup("code"), dir, []string{"https://raw.githubusercontent.com/example-org/"}, ui.New(io.Discard))
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"), []byte(child(baseAt(t, dir, cliWorkflowSHA2))), 0o644))
	assert.NotContains(t, relock(t, dir, flags), "is up to date")
	dep = lockedWorkflow(t, dir)
	assert.Equal(t, "https://raw.githubusercontent.com/example-org/sample-pipeline/"+cliWorkflowSHA2+"/pipelines/sample/", dep.URL)
	assert.Equal(t, fetch.ComputeTreeHash(trees[cliWorkflowSHA2]), dep.SHA256)
}

// fullsend run with a current lock entry delivers the locked definition
// tree for a base-pinned source (no #sha256=): the lock entry's hash is
// the authority, not the URL index, so an offline replay works without
// an index entry and an index repointed at another cached tree is
// ignored. The hash is read before the replay, so it also binds the
// definition when the replay falls back to normal resolution, and a
// definition missing from the cache, or cached with stray entries, is
// refetched and must hash to it.
func TestRunAgent_WorkflowLockReplayIgnoresURLIndex(t *testing.T) {
	trees, workflowFetcher := refTrees()
	lockedHash := fetch.ComputeTreeHash(trees[cliWorkflowSHA])
	otherHash := fetch.ComputeTreeHash(trees[cliWorkflowSHA2])
	cleanURL := "https://github.com/example-org/sample-pipeline/tree/" + cliWorkflowSHA + "/pipelines/sample"
	skill := map[string][]byte{"SKILL.md": []byte("---\nname: helper\ndescription: helper skill\n---\n# helper\n")}
	skillHash := fetch.ComputeTreeHash(skill)
	skillURL := "https://github.com/example-org/sample-skills/tree/" + cliWorkflowSHA + "/skills/helper#sha256=" + skillHash
	// fetcherServing serves the unrelated skill and, for the definition,
	// the tree at commit workflowRef, whatever commit is asked for.
	fetcherServing := func(workflowRef string, fetched *[]string) gitfetch.TreeFetchFunc {
		return func(ctx context.Context, cloneURL, subpath, ref, token string) (map[string][]byte, error) {
			*fetched = append(*fetched, subpath)
			if subpath == "skills/helper" {
				return skill, nil
			}
			return workflowFetcher(ctx, cloneURL, subpath, workflowRef, token)
		}
	}
	setup := func(t *testing.T) string {
		t.Helper()
		usePreScriptStub(t)
		dir := newSkipHarnessDir(t, "")
		baseContent := []byte("workflow:\n  source: pipelines/sample\n  name: probe\n")
		baseURL := "https://raw.githubusercontent.com/example-org/sample-pipeline/" + cliWorkflowSHA + "/.fullsend/harness/base.yaml"
		require.NoError(t, fetch.CachePut(dir, baseURL, baseContent))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"),
			[]byte("base: "+baseURL+"#sha256="+fetch.ComputeSHA256(baseContent)+"\nagent: agents/code.md\nrole: test\nskills:\n  - "+skillURL+"\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(workflowAllowlist+"agents:\n  - harness/code.yaml\n"), 0o644))
		gitInit(t, dir)
		var fetched []string
		relock(t, dir, resolveFlags{treeFetcher: fetcherServing(cliWorkflowSHA, &fetched)})
		require.Equal(t, lockedHash, lockedWorkflow(t, dir).SHA256)
		return dir
	}
	repointIndex := func(t *testing.T, dir string) {
		t.Helper()
		_, err := fetch.CachePutMaterializedDir(context.Background(), dir, cleanURL, trees[cliWorkflowSHA2])
		require.NoError(t, err)
		require.NoError(t, harness.RecordURLIndex(dir, resolve.WorkflowDependencyField+":"+cleanURL, otherHash))
	}
	lockedTreePath := func(t *testing.T, dir string) string {
		t.Helper()
		p, err := fetch.MaterializedCachePath(dir, lockedHash)
		require.NoError(t, err)
		return p
	}
	assertLockedTreeDelivered := func(t *testing.T, flags resolveFlags, dir string) string {
		t.Helper()
		out, err := runWorkflowAgent(t, dir, flags)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "creating sandbox", "the workflow resolved and the run reached sandbox creation")
		assert.Contains(t, out, "(sha256:"+lockedHash[:12]+") delivered as claude plugin wfplug")
		assert.NotContains(t, out, otherHash[:12])
		return out
	}

	t.Run("missing index entry", func(t *testing.T) {
		dir := setup(t)
		require.NoError(t, os.Remove(filepath.Join(dir, ".fullsend-cache", "url-index.json")))
		out := assertLockedTreeDelivered(t, resolveFlags{offline: true}, dir)
		assert.Contains(t, out, "dependencies from lock file")
	})
	t.Run("index repointed at another cached tree", func(t *testing.T) {
		dir := setup(t)
		repointIndex(t, dir)
		out := assertLockedTreeDelivered(t, resolveFlags{offline: true}, dir)
		assert.Contains(t, out, "dependencies from lock file")
	})
	t.Run("replay falls back with the index repointed", func(t *testing.T) {
		dir := setup(t)
		repointIndex(t, dir)
		skillCache, err := fetch.CachePath(dir, skillHash)
		require.NoError(t, err)
		require.NoError(t, os.RemoveAll(skillCache))
		// A fetch of the definition would serve the other tree too: the
		// locked tree is still in the cache and is found by the lock hash.
		var fetched []string
		out := assertLockedTreeDelivered(t, resolveFlags{treeFetcher: fetcherServing(cliWorkflowSHA2, &fetched)}, dir)
		assert.Contains(t, out, "Falling back to normal resolution")
		assert.Equal(t, []string{"skills/helper"}, fetched, "only the unrelated skill is refetched")
	})
	t.Run("replay falls back and the locked tree is not cached", func(t *testing.T) {
		dir := setup(t)
		repointIndex(t, dir)
		skillCache, err := fetch.CachePath(dir, skillHash)
		require.NoError(t, err)
		require.NoError(t, os.RemoveAll(skillCache))
		require.NoError(t, os.RemoveAll(lockedTreePath(t, dir)))
		var fetched []string
		_, runErr := runWorkflowAgent(t, dir, resolveFlags{treeFetcher: fetcherServing(cliWorkflowSHA2, &fetched)})
		require.Error(t, runErr)
		assert.Contains(t, runErr.Error(), ".fullsend/lock.yaml records sha256="+lockedHash+" but the fetched tree hashes to sha256="+otherHash,
			"a refetched tree that is not the locked one fails closed")
	})
	t.Run("locked tree missing from the cache is refetched", func(t *testing.T) {
		dir := setup(t)
		repointIndex(t, dir)
		require.NoError(t, os.RemoveAll(lockedTreePath(t, dir)))
		var fetched []string
		out := assertLockedTreeDelivered(t, resolveFlags{treeFetcher: fetcherServing(cliWorkflowSHA, &fetched)}, dir)
		assert.Contains(t, out, "dependencies from lock file", "the other dependencies still replay")
		assert.Equal(t, []string{"pipelines/sample"}, fetched)
	})
	t.Run("cached locked tree with a stray entry is refetched", func(t *testing.T) {
		dir := setup(t)
		repointIndex(t, dir)
		require.NoError(t, os.MkdirAll(filepath.Join(lockedTreePath(t, dir), "tree", "stray"), 0o755))
		var fetched []string
		out := assertLockedTreeDelivered(t, resolveFlags{treeFetcher: fetcherServing(cliWorkflowSHA, &fetched)}, dir)
		assert.Contains(t, out, "dependencies from lock file")
		assert.Equal(t, []string{"pipelines/sample"}, fetched)
		_, statErr := os.Stat(filepath.Join(lockedTreePath(t, dir), "tree", "stray"))
		assert.True(t, os.IsNotExist(statErr), "the refetched tree replaces the one with the stray entry")
	})
	t.Run("malformed workflow entry fails closed", func(t *testing.T) {
		dir := setup(t)
		lockPath := filepath.Join(dir, "lock.yaml")
		lf, err := lock.Load(lockPath)
		require.NoError(t, err)
		entry := lf.Lookup("code")
		for i := range entry.Dependencies {
			if entry.Dependencies[i].Field == resolve.WorkflowDependencyField {
				entry.Dependencies[i].SHA256 = "not-a-hash"
			}
		}
		lf.SetHarness("code", *entry)
		require.NoError(t, lock.Save(lockPath, lf))
		_, runErr := runWorkflowAgent(t, dir, resolveFlags{offline: true})
		require.Error(t, runErr)
		assert.Contains(t, runErr.Error(), `lock.yaml entry for agent "code" is malformed`)
		assert.Contains(t, runErr.Error(), "fullsend lock --update code")
	})
}

func TestLockedWorkflowSHA256(t *testing.T) {
	good := strings.Repeat("ab", 32)
	wf := func(url, typ, sha string) lock.DependencyEntry {
		return lock.DependencyEntry{Field: resolve.WorkflowDependencyField, URL: url, Type: typ, SHA256: sha}
	}
	other := lock.DependencyEntry{Field: "plugins[0]", URL: "https://example.com/p", Type: "directory", SHA256: "a"}
	for _, tc := range []struct {
		name    string
		deps    []lock.DependencyEntry
		want    string
		wantErr string
	}{
		{name: "none", deps: nil},
		{name: "other fields only", deps: []lock.DependencyEntry{other}},
		{name: "workflow", deps: []lock.DependencyEntry{other, wf("https://example.com/w", "directory", good)}, want: good},
		{name: "second workflow", deps: []lock.DependencyEntry{wf("u", "directory", good), wf("u", "directory", good)}, wantErr: "dependencies[1] (field workflow) records a second workflow dependency"},
		{name: "no url", deps: []lock.DependencyEntry{wf("", "directory", good)}, wantErr: "dependencies[0] (field workflow) has no url"},
		{name: "file type", deps: []lock.DependencyEntry{wf("u", "file", good)}, wantErr: `has type "file", not "directory"`},
		{name: "upper-case hash", deps: []lock.DependencyEntry{wf("u", "directory", strings.ToUpper(good))}, wantErr: "not 64 lowercase hex characters"},
		{name: "short hash", deps: []lock.DependencyEntry{wf("u", "directory", "ab")}, wantErr: `has sha256 "ab"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := lockedWorkflowSHA256(&lock.HarnessLock{Dependencies: tc.deps}, "code", "custom")
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Contains(t, err.Error(), "regenerate it with `fullsend lock --update code --fullsend-dir custom`")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRunLock_WorkflowSharesURLWithPluginOrSkill(t *testing.T) {
	files := map[string][]byte{
		"plugin.json":        []byte(`{}`),
		"workflows/probe.js": []byte("export const meta = { name: 'probe' };\n"),
		"SKILL.md":           []byte("---\nname: shared\ndescription: shared tree\n---\n# shared\n"),
	}
	treeURL := "https://github.com/example-org/sample-pipeline/tree/" + cliWorkflowSHA + "/pipelines/shared"
	pinned := treeURL + "#sha256=" + fetch.ComputeTreeHash(files)
	fetcher := func(context.Context, string, string, string, string) (map[string][]byte, error) { return files, nil }

	for _, tc := range []struct {
		name, harnessExtra, field string
		check                     func(t *testing.T, h *harness.Harness)
	}{
		{
			name:         "plugin",
			harnessExtra: "plugins:\n  - " + pinned + "\n",
			field:        "plugins[0]",
			check: func(t *testing.T, h *harness.Harness) {
				require.Len(t, h.Plugins, 1)
				assert.Equal(t, "shared", filepath.Base(h.Plugins[0].Path), "the plugin is bound to its cached tree")
			},
		},
		{
			name:         "skill",
			harnessExtra: "skills:\n  - " + pinned + "\n",
			field:        "skills[0]",
			check: func(t *testing.T, h *harness.Harness) {
				require.NotEmpty(t, h.Skills)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := workflowLockDir(t, workflowHarness(pinned)+tc.harnessExtra)
			require.NoError(t, runLock(context.Background(), "code", dir, "", false, resolveFlags{treeFetcher: gitfetch.TreeFetchFunc(fetcher)}, ui.New(io.Discard)))
			lf, err := lock.Load(filepath.Join(dir, "lock.yaml"))
			require.NoError(t, err)
			entry := lf.Lookup("code")
			require.NotNil(t, entry)
			fields := map[string]string{}
			for _, d := range entry.Dependencies {
				fields[d.Field] = d.URL
			}
			assert.Equal(t, map[string]string{resolve.WorkflowDependencyField: treeURL, tc.field: treeURL}, fields)

			h, err := harness.Load(filepath.Join(dir, "harness", "code.yaml"))
			require.NoError(t, err)
			require.NoError(t, h.ResolveRelativeTo(dir))
			result, err := resolveFromLock(h, entry, dir, []string{"https://github.com/example-org/"}, ui.New(io.Discard))
			require.NoError(t, err)
			require.Len(t, result.Deps, 1)
			assert.Equal(t, tc.field, result.Deps[0].Field, "the definition is resolved by its own step, from the entry's hash")
			tc.check(t, h)
		})
	}
}

// TestRunLock_SharedSkillAcrossForgeVariants checks that a skill URL the
// forge variants hold at different skills[N] indices is locked once (by
// URL; only the workflow field is keyed by field and URL), and that each
// variant's lock replay gets its own skills.
func TestRunLock_SharedSkillAcrossForgeVariants(t *testing.T) {
	shared := map[string][]byte{"SKILL.md": []byte("---\nname: shared\ndescription: shared skill\n---\n# shared\n")}
	pinned := "https://github.com/example-org/sample-pipeline/tree/" + cliWorkflowSHA + "/skills/shared#sha256=" + fetch.ComputeTreeHash(shared)
	sharedURL, _, _ := harness.ParseIntegrityHash(pinned)
	fetcher := gitfetch.TreeFetchFunc(func(context.Context, string, string, string, string) (map[string][]byte, error) {
		return shared, nil
	})
	// github holds the shared skill at skills[1], behind a local skill;
	// gitlab holds it at skills[0].
	harnessYAML := "agent: agents/code.md\nrole: test\nforge:\n  github:\n    skills:\n      - skills/local\n      - " + pinned +
		"\n  gitlab:\n    skills:\n      - " + pinned + "\n"
	dir := workflowLockDir(t, harnessYAML)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills", "local"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "local", "SKILL.md"), []byte("---\nname: local\ndescription: local skill\n---\n# local\n"), 0o644))
	require.NoError(t, runLock(context.Background(), "code", dir, "", false, resolveFlags{treeFetcher: fetcher}, ui.New(io.Discard)))

	lf, err := lock.Load(filepath.Join(dir, "lock.yaml"))
	require.NoError(t, err)
	entry := lf.Lookup("code")
	require.NotNil(t, entry)
	var fields []string
	for _, d := range entry.Dependencies {
		if d.URL == sharedURL {
			fields = append(fields, d.Field)
		}
	}
	assert.Equal(t, []string{"skills[1]"}, fields, "the shared skill is locked once")

	for platform, want := range map[string][]string{"github": {"local", "shared"}, "gitlab": {"shared"}} {
		t.Run(platform, func(t *testing.T) {
			policy := fetch.DefaultPolicy
			policy.Offline = true
			h, _, err := harness.LoadWithBase(context.Background(), filepath.Join(dir, "harness", "code.yaml"), harness.ComposeOpts{
				WorkspaceRoot: dir,
				FetchPolicy:   policy,
				ForgePlatform: platform,
				TreeFetcher:   fetcher,
			})
			require.NoError(t, err)
			require.NoError(t, h.ResolveRelativeTo(dir))
			_, err = resolveFromLock(h, entry, dir, []string{"https://github.com/example-org/"}, ui.New(io.Discard))
			require.NoError(t, err)
			var got []string
			for _, s := range h.Skills {
				got = append(got, filepath.Base(s.Source))
			}
			assert.Equal(t, want, got)
		})
	}
}

// TestRunLock_WorkflowNamespaceCollisionWithURLPlugin checks that lock
// refuses a URL plugin whose fetched manifest name is the definition's
// plugin name, with the error fullsend run gives for the same harness.
func TestRunLock_WorkflowNamespaceCollisionWithURLPlugin(t *testing.T) {
	usePreScriptStub(t)
	files := cliWorkflowTree()
	treeURL := "https://github.com/example-org/sample-pipeline/tree/" + cliWorkflowSHA + "/plugins/helpers"
	pinned := treeURL + "#sha256=" + fetch.ComputeTreeHash(files)
	flags := resolveFlags{maxDepth: 10, maxResources: 50, treeFetcher: gitfetch.TreeFetchFunc(func(context.Context, string, string, string, string) (map[string][]byte, error) {
		return files, nil
	})}
	dir := newSkipHarnessDir(t, "")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"), []byte(workflowHarness(pinnedWorkflowSource("sample-pipeline", cliWorkflowSHA, files))+"plugins:\n  - "+pinned+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(workflowAllowlist+"agents:\n  - harness/code.yaml\n"), 0o644))
	want := `resolving workflow definition: plugins[0] "`
	wantNS := `has the Claude Code plugin name "wfplug", the same as the workflow: definition's plugin name "wfplug"`

	lockErr := runLock(context.Background(), "code", dir, "", false, flags, ui.New(io.Discard))
	require.Error(t, lockErr)
	assert.Contains(t, lockErr.Error(), want)
	assert.Contains(t, lockErr.Error(), wantNS)
	_, statErr := os.Stat(filepath.Join(dir, "lock.yaml"))
	assert.True(t, os.IsNotExist(statErr), "nothing is locked")

	runErr := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", flags,
		statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
	require.Error(t, runErr)
	assert.Equal(t, runErr.Error(), lockErr.Error(), "lock refuses what run refuses, with the same error")
}
