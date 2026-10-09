package harness

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/fetch"
)

const (
	testWorkflowSHA  = "0123456789abcdef0123456789abcdef01234567"
	testWorkflowHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestWorkflowSpec_ParseAndValidate(t *testing.T) {
	remote := "https://github.com/example-org/sample-pipeline/tree/" + testWorkflowSHA
	pin := "#sha256=" + testWorkflowHash
	tests := []struct {
		name    string
		yaml    string
		want    *WorkflowSpec
		wantErr string
	}{
		{name: "absent", yaml: ""},
		{
			name: "remote root with name and args",
			yaml: "workflow:\n  source: " + remote + pin + "\n  name: probe\n  args: \"triage issue 7, then \\\"a b\\\"\"\n",
			want: &WorkflowSpec{Source: remote + pin, Name: "probe", Args: `triage issue 7, then "a b"`},
		},
		{
			name: "remote sub-directory",
			yaml: "workflow:\n  source: " + remote + "/pipelines/sample" + pin + "\n  name: run_all\n",
			want: &WorkflowSpec{Source: remote + "/pipelines/sample" + pin, Name: "run_all"},
		},
		{
			name: "local root without name (a pi extension)",
			yaml: "workflow:\n  source: .\n",
			want: &WorkflowSpec{Source: "."},
		},
		{
			name: "local sub-directory",
			yaml: "workflow:\n  source: pipelines/sample\n  name: probe\n",
			want: &WorkflowSpec{Source: "pipelines/sample", Name: "probe"},
		},
		{name: "missing source", yaml: "workflow:\n  name: probe\n", wantErr: "workflow.source is required"},
		{name: "newline in source", yaml: "workflow:\n  source: \"a\\nb\"\n", wantErr: "must not contain NUL, carriage return or newline"},
		{name: "http", yaml: "workflow:\n  source: http://github.com/o/r/tree/" + testWorkflowSHA + "\n", wantErr: "must use https"},
		{name: "other scheme", yaml: "workflow:\n  source: ssh://github.com/o/r\n", wantErr: "scheme is not supported"},
		{name: "absolute path", yaml: "workflow:\n  source: /srv/pipeline\n", wantErr: "is an absolute path"},
		{name: "backslash", yaml: "workflow:\n  source: 'a\\b'\n", wantErr: "contains a backslash"},
		{name: "dot dot", yaml: "workflow:\n  source: ../other\n", wantErr: `contains ".."`},
		{name: "git dir", yaml: "workflow:\n  source: x/.git/y\n", wantErr: "names .git/"},
		{name: "cache dir", yaml: "workflow:\n  source: .fullsend-cache\n", wantErr: "names .fullsend-cache/"},
		{name: "fragment on a path", yaml: "workflow:\n  source: \"pipe#x\"\n", wantErr: "contains ? or #"},
		{name: "remote without pin", yaml: "workflow:\n  source: " + remote + "\n", wantErr: "must end in #sha256="},
		{name: "remote branch ref", yaml: "workflow:\n  source: https://github.com/o/r/tree/main" + pin + "\n", wantErr: `ref "main" is not a commit sha`},
		{name: "remote blob", yaml: "workflow:\n  source: https://github.com/o/r/blob/" + testWorkflowSHA + "/x.js" + pin + "\n", wantErr: "must use /tree/"},
		{name: "remote not a forge", yaml: "workflow:\n  source: https://example.com/x" + pin + "\n", wantErr: "must be a GitHub tree URL"},
		{name: "remote on gitlab", yaml: "workflow:\n  source: https://gitlab.com/o/r/-/tree/" + testWorkflowSHA + pin + "\n", wantErr: "must be a github.com tree URL today"},
		{name: "bad name", yaml: "workflow:\n  source: .\n  name: probe.js\n", wantErr: `workflow.name "probe.js" may contain only`},
		{name: "newline in args", yaml: "workflow:\n  source: .\n  name: probe\n  args: \"a\\nb\"\n", wantErr: "workflow.args must be a single line"},
		{name: "carriage return in args", yaml: "workflow:\n  source: .\n  name: probe\n  args: \"a\\rb\"\n", wantErr: "workflow.args must be a single line"},
		{name: "args without name", yaml: "workflow:\n  source: .\n  args: x\n", wantErr: "workflow.args is set without workflow.name"},
		{
			name: "work-item variable in args",
			yaml: "workflow:\n  source: .\n  name: probe\n  args: issue ${ISSUE_NUMBER}\n",
			want: &WorkflowSpec{Source: ".", Name: "probe", Args: "issue ${ISSUE_NUMBER}"},
		},
		{
			name:    "credential variable in args",
			yaml:    "workflow:\n  source: .\n  name: probe\n  args: issue ${ISSUE_NUMBER} ${GH_TOKEN}\n",
			wantErr: "workflow.args references ${GH_TOKEN}, which names a credential (the *_TOKEN family (credential-shaped names)); pass work-item identifiers such as ${ISSUE_NUMBER} instead",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h Harness
			require.NoError(t, yaml.Unmarshal([]byte("agent: agents/a.md\nrole: test\n"+tt.yaml), &h))
			err := h.Validate()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, h.Workflow)
		})
	}
}

func TestValidateRemoteWorkflowSource_DotSegment(t *testing.T) {
	err := validateRemoteWorkflowSource("https://github.com/o/r/tree/"+testWorkflowSHA+"/a/./b#sha256="+testWorkflowHash, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `must not contain "." or ".." segments`)
}

func TestWorkflowSpec_IsRemote(t *testing.T) {
	assert.True(t, (&WorkflowSpec{Source: "https://github.com/o/r/tree/" + testWorkflowSHA}).IsRemote())
	assert.False(t, (&WorkflowSpec{Source: "."}).IsRemote())
	assert.True(t, ValidWorkflowName("run_all-2"))
	assert.False(t, ValidWorkflowName("a.b"))
}

func TestValidatePlugins_ReservedWorkflowDir(t *testing.T) {
	for name, entry := range map[string]string{
		"local":          "plugins/workflow-definition",
		"local any case": "plugins/Workflow-Definition",
		"url":            "https://github.com/o/r/tree/" + testWorkflowSHA + "/workflow-definition#sha256=" + testWorkflowHash,
	} {
		t.Run(name, func(t *testing.T) {
			h := &Harness{Agent: "agents/a.md", Role: "test", Plugins: []PluginSpec{{Path: entry}},
				Workflow: &WorkflowSpec{Source: "pipelines/sample", Name: "probe"}}
			err := h.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "reserved for the workflow: definition this harness declares")
		})
		// Without workflow: nothing takes the directory, so the name is an
		// ordinary plugin name.
		t.Run(name+" without workflow", func(t *testing.T) {
			h := &Harness{Agent: "agents/a.md", Role: "test", Plugins: []PluginSpec{{Path: entry}}}
			require.NoError(t, h.Validate())
		})
	}
}

// The reservation applies to the composed harness: a workflow: from the
// base and a plugin from the child (or the other way round) collide.
func TestPluginEntryDirName(t *testing.T) {
	assert.Equal(t, "x", PluginEntryDirName(PluginSpec{Path: "/a/plugins/x"}))
	assert.Equal(t, "y", PluginEntryDirName(PluginSpec{Path: "https://github.com/o/r/tree/" + testWorkflowSHA + "/p/y#sha256=abc"}))
	// A URL that names no forge tree path has no directory name yet.
	assert.Equal(t, "", PluginEntryDirName(PluginSpec{Path: "https://github.com/o/r/tree/" + testWorkflowSHA}))
	assert.Equal(t, "", PluginEntryDirName(PluginSpec{Path: "https://example.com/z"}))
}

func TestLoadWithBase_ReservedWorkflowDirAcrossBase(t *testing.T) {
	const wf = "workflow:\n  source: pipelines/sample\n  name: probe\n"
	const plugin = "plugins:\n  - path: plugins/workflow-definition\n"
	for name, tt := range map[string]struct{ base, child string }{
		"workflow in base, plugin in child": {base: wf, child: plugin},
		"plugin in base, workflow in child": {base: plugin, child: wf},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeTestHarness(t, dir, "base.yaml", "agent: agents/base.md\nrole: test\n"+tt.base)
			path := writeTestHarness(t, dir, "child.yaml", "base: base.yaml\n"+tt.child)
			_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "reserved for the workflow: definition this harness declares")
		})
	}
}

func TestLoadWithBase_Workflow(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		child    string
		want     *WorkflowSpec
		fromBase bool // the source is marked as declared in the base's directory
	}{
		{
			name:     "child inherits a local base's workflow, still relative",
			base:     "workflow:\n  source: pipelines/sample\n  name: probe\n  args: x\n",
			child:    "",
			want:     &WorkflowSpec{Source: "pipelines/sample", Name: "probe", Args: "x"},
			fromBase: true,
		},
		{
			name:  "child replaces base workflow wholesale",
			base:  "workflow:\n  source: pipelines/sample\n  name: probe\n  args: x\n",
			child: "workflow:\n  source: .\n",
			want:  &WorkflowSpec{Source: "."},
		},
		{
			name:  "child only",
			base:  "",
			child: "workflow:\n  source: pipelines/other\n  name: run\n",
			want:  &WorkflowSpec{Source: "pipelines/other", Name: "run"},
		},
		{name: "neither", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTestHarness(t, dir, "base.yaml", "agent: agents/base.md\nrole: test\n"+tt.base)
			path := writeTestHarness(t, dir, "child.yaml", "base: base.yaml\n"+tt.child)
			h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
			require.NoError(t, err)
			if tt.want != nil && tt.fromBase {
				realDir, err := filepath.EvalSymlinks(dir)
				require.NoError(t, err)
				tt.want.declaringDir = realDir
			}
			assert.Equal(t, tt.want, h.Workflow)
		})
	}
}

// A relative source in a local base resolves in the checkout that holds
// the base file that wrote it: through a chain of local bases, the
// deepest declaring base's directory is kept, and a URL source is not
// marked.
func TestLoadWithBase_LocalBaseWorkflowDeclaredIn(t *testing.T) {
	root := t.TempDir()
	realRoot, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	writeTestHarness(t, filepath.Join(root, "vendor", "gp"), "gp.yaml", "agent: agents/base.md\nrole: test\nworkflow:\n  source: pipelines/sample\n  name: probe\n")
	writeTestHarness(t, filepath.Join(root, "vendor", "mid"), "mid.yaml", "base: ../gp/gp.yaml\n")
	path := writeTestHarness(t, filepath.Join(root, "harness"), "child.yaml", "base: ../vendor/mid/mid.yaml\n")
	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{WorkspaceRoot: root})
	require.NoError(t, err)
	require.NotNil(t, h.Workflow)
	assert.Equal(t, filepath.Join(realRoot, "vendor", "gp"), h.Workflow.DeclaredIn())

	remote := &WorkflowSpec{Source: "https://github.com/o/r/tree/" + testWorkflowSHA + "#sha256=" + testWorkflowHash}
	markLocalBaseWorkflow(remote, root)
	assert.Empty(t, remote.DeclaredIn())
	markLocalBaseWorkflow(nil, root)
	assert.Empty(t, (&WorkflowSpec{Source: "x"}).DeclaredIn())
}

// seedURLBase caches base content as a raw.githubusercontent.com base at
// testWorkflowSHA and returns the child's base: value.
func seedURLBase(t *testing.T, cacheDir, ref, content string) string {
	t.Helper()
	baseURL := "https://raw.githubusercontent.com/example-org/sample-pipeline/" + ref + "/.fullsend/harness/base.yaml"
	require.NoError(t, fetch.CachePut(cacheDir, baseURL, []byte(content)))
	return baseURL + "#sha256=" + fetch.ComputeSHA256([]byte(content))
}

func TestLoadWithBase_URLBase_RelativeWorkflowSourcePinnedToBaseCommit(t *testing.T) {
	tests := []struct {
		name, source, wantSource, wantDir, extra string
	}{
		{
			name:       "sub-directory",
			source:     "pipelines/sample/",
			wantSource: "https://github.com/example-org/sample-pipeline/tree/" + testWorkflowSHA + "/pipelines/sample",
			wantDir:    "https://raw.githubusercontent.com/example-org/sample-pipeline/" + testWorkflowSHA + "/pipelines/sample/",
			extra:      "forge:\n  github:\n    policy: policies/p.yaml\n",
		},
		{
			name:       "repository root",
			source:     ".",
			wantSource: "https://github.com/example-org/sample-pipeline/tree/" + testWorkflowSHA,
			wantDir:    "https://raw.githubusercontent.com/example-org/sample-pipeline/" + testWorkflowSHA + "/",
			extra:      "overlays:\n  - when: \"true\"\n    policy: policies/p.yaml\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			cacheDir := filepath.Join(dir, "cache")
			base := seedURLBase(t, cacheDir, testWorkflowSHA, "workflow:\n  source: "+tt.source+"\n  name: probe\n")
			// forge: and overlays: on the child resolve after the merge; the
			// marker composition set must survive them.
			path := writeTestHarness(t, dir, "child.yaml", "agent: agents/child.md\nrole: test\nbase: "+base+"\n"+tt.extra)
			h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
				WorkspaceRoot: cacheDir,
				FetchPolicy:   fetch.FetchPolicy{Offline: true},
				OrgAllowlist:  []string{"https://raw.githubusercontent.com/example-org/"},
				ForgePlatform: "github",
			})
			require.NoError(t, err)
			assert.Equal(t, "policies/p.yaml", h.Policy, "the forge: or overlays: block applied")
			require.NotNil(t, h.Workflow)
			assert.Equal(t, tt.wantSource, h.Workflow.Source)
			assert.Equal(t, tt.wantDir, h.Workflow.InheritedFromBase())
			assert.Equal(t, "probe", h.Workflow.Name)
		})
	}
}

func TestLoadWithBase_URLBase_RelativeWorkflowSourceErrors(t *testing.T) {
	tests := []struct {
		name, ref, source, allow, wantErr string
	}{
		{name: "branch ref", ref: "main", source: ".", allow: "https://raw.githubusercontent.com/example-org/", wantErr: `pins ref "main", not a commit sha`},
		{name: "not allowlisted", ref: testWorkflowSHA, source: "pipelines/sample", allow: "https://raw.githubusercontent.com/example-org/sample-pipeline/" + testWorkflowSHA + "/.fullsend/", wantErr: "is not in allowed_remote_resources"},
		{name: "traversal", ref: testWorkflowSHA, source: "../x", allow: "https://raw.githubusercontent.com/example-org/", wantErr: `contains ".."`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			cacheDir := filepath.Join(dir, "cache")
			base := seedURLBase(t, cacheDir, tt.ref, "workflow:\n  source: "+tt.source+"\n  name: probe\n")
			path := writeTestHarness(t, dir, "child.yaml", "agent: agents/child.md\nrole: test\nbase: "+base+"\n")
			_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
				WorkspaceRoot: cacheDir,
				FetchPolicy:   fetch.FetchPolicy{Offline: true},
				OrgAllowlist:  []string{tt.allow},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestResolveBaseWorkflowSource(t *testing.T) {
	allow := []string{"https://raw.githubusercontent.com/"}
	require.NoError(t, ResolveBaseWorkflowSource(nil, "", allow))

	remote := &WorkflowSpec{Source: "https://github.com/o/r/tree/" + testWorkflowSHA + "#sha256=" + testWorkflowHash}
	require.NoError(t, ResolveBaseWorkflowSource(remote, "https://raw.githubusercontent.com/o/r/main/h.yaml", allow))
	assert.Empty(t, remote.InheritedFromBase(), "a URL source is left alone")

	err := ResolveBaseWorkflowSource(&WorkflowSpec{Source: "."}, "https://example.com/h.yaml", allow)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not name one")
}

func TestValidate_InheritedWorkflowSourceNeedsNoPin(t *testing.T) {
	w := &WorkflowSpec{Source: "pipelines/sample", Name: "probe"}
	require.NoError(t, ResolveBaseWorkflowSource(w, "https://raw.githubusercontent.com/o/r/"+testWorkflowSHA+"/h.yaml", []string{"https://raw.githubusercontent.com/o/"}))
	h := &Harness{Agent: "agents/a.md", Role: "test", Workflow: w}
	require.NoError(t, h.Validate(), "a base-pinned source carries no #sha256= fragment")

	written := &Harness{Agent: "agents/a.md", Role: "test", Workflow: &WorkflowSpec{Source: w.Source}}
	err := written.Validate()
	require.Error(t, err, "the same URL written in a harness needs its pin")
	assert.True(t, strings.Contains(err.Error(), "#sha256="))
}

func TestMergeBaseIntoChild_WorkflowIsCopied(t *testing.T) {
	base := &Harness{Workflow: &WorkflowSpec{Source: ".", Name: "probe"}}
	child := &Harness{}
	mergeBaseIntoChild(base, child)
	require.NotNil(t, child.Workflow)
	child.Workflow.Args = "changed"
	assert.Empty(t, base.Workflow.Args, "the child must not share the base's WorkflowSpec")
}

func TestWorkflowUnderForgeOrOverlaysIsIgnored(t *testing.T) {
	for name, block := range map[string]string{
		"forge":    "forge:\n  github:\n    workflow:\n      source: .\n      name: probe\n",
		"overlays": "overlays:\n  - when: \"true\"\n    workflow:\n      source: .\n      name: probe\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeTestHarness(t, t.TempDir(), "code.yaml", "agent: agents/a.md\nrole: test\n"+block)
			h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{ForgePlatform: "github"})
			require.NoError(t, err)
			assert.Nil(t, h.Workflow, "workflow: is top-level only; under forge: or overlays: it is ignored, not an error")
		})
	}
}

func TestURLIndexWrappers(t *testing.T) {
	ws := t.TempDir()
	_, ok := LookupURLIndex(ws, "workflow:https://github.com/o/r/tree/"+testWorkflowSHA)
	assert.False(t, ok)
	require.NoError(t, RecordURLIndex(ws, "workflow:https://github.com/o/r/tree/"+testWorkflowSHA, testWorkflowHash))
	got, ok := LookupURLIndex(ws, "workflow:https://github.com/o/r/tree/"+testWorkflowSHA)
	assert.True(t, ok)
	assert.Equal(t, testWorkflowHash, got)
}

func TestHasURLReferences_WorkflowSource(t *testing.T) {
	assert.False(t, (&Harness{Workflow: &WorkflowSpec{Source: "pipelines/sample"}}).HasURLReferences())
	assert.True(t, (&Harness{Workflow: &WorkflowSpec{Source: "https://github.com/o/r/tree/" + testWorkflowSHA + "#sha256=" + testWorkflowHash}}).HasURLReferences())
}

func TestCredentialShapedEnvName(t *testing.T) {
	for _, name := range []string{
		"GH_TOKEN", "GITHUB_TOKEN", "OTEL_EXPORTER_OTLP_HEADERS", "otel_service_name", "MY_API_KEY", "SIGNING_PRIVATE_KEY", "AWS_ACCESS_KEY", "APP_SECRET_KEY",
		"DB_PASSWORD", "GOOGLE_APPLICATION_CREDENTIALS", "X_SECRET_Y", "WEBHOOK_SECRET", "HTTPS_PROXY", "https_proxy",
		"TOKEN", "PASSWORD", "SECRET",
	} {
		rule, ok := CredentialShapedEnvName(name)
		assert.True(t, ok, name)
		assert.NotEmpty(t, rule, name)
	}
	for _, name := range []string{"ISSUE_NUMBER", "ISSUE_KEY", "PR_NUMBER", "REPO_FULL_NAME", "KEYWORDS", "TOKENIZER", "FULLSEND_DIR"} {
		_, ok := CredentialShapedEnvName(name)
		assert.False(t, ok, name)
	}
}

func TestCheckWorkflowArgsVariables(t *testing.T) {
	for _, name := range []string{"GH_TOKEN", "OTEL_EXPORTER_OTLP_HEADERS", "MY_API_KEY", "DB_PASSWORD", "X_SECRET_Y"} {
		err := CheckWorkflowArgsVariables("issue ${"+name+"}", nil)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "workflow.args references ${"+name+"}, which names a credential (")
	}
	require.NoError(t, CheckWorkflowArgsVariables("issue ${ISSUE_NUMBER}", nil))
	require.NoError(t, CheckWorkflowArgsVariables("literal $GH_TOKEN text", nil), "args without ${ are not expanded")

	extra := func(name string) (string, bool) { return "runner-only", name == "RUNNER_ONLY" }
	err := CheckWorkflowArgsVariables("${ISSUE_NUMBER} ${RUNNER_ONLY}", extra)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "${RUNNER_ONLY}, which names a credential (runner-only)")
	require.NoError(t, CheckWorkflowArgsVariables("${ISSUE_NUMBER}", extra))
}
