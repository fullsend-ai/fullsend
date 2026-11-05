package repos

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const setTestManifest = `# Fleet manifest — keep this header.
version: 1

defaults:
  # Fleet-wide inference selection.
  inference:
    auth: vertex-wif # chosen by platform team
  config:
    runtime: claude

github:
  mint_url: https://mint.example.com
  repos:
    # The API service.
    - name: acme/api
      fullsend_ref: v1.0.0
    - name: "acme/web.v2"
    - name: "acme/*"

gitlab:
  url: https://gitlab.example.com
  repos:
    - name: group/project
`

func writeSetManifest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repos.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func loadSetManifest(t *testing.T, path string) *Manifest {
	t.Helper()
	m, err := LoadManifest(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, m.Validate())
	return m
}

func TestParseManifestPath(t *testing.T) {
	segs, err := parseManifestPath(`github.repos["acme/a.b*[x]"].config.runtime`)
	require.NoError(t, err)
	assert.Equal(t, []pathSeg{
		{name: "github"}, {name: "repos"}, {name: "acme/a.b*[x]", selector: true},
		{name: "config"}, {name: "runtime"},
	}, segs)

	segs, err = parseManifestPath(`github.repos["say \"hi\"/x"]`)
	require.NoError(t, err)
	assert.Equal(t, `say "hi"/x`, segs[2].name)

	for _, bad := range []string{
		"", ".github", "github.", "github..url", `["x"]`, `github.repos[acme/api]`,
		`github.repos["acme/api"`, `github.repos[""]`, `github.repos['acme/api']`,
		`github.repos["a"]x`, "github.u rl", "github/url",
	} {
		_, err := parseManifestPath(bad)
		assert.Error(t, err, "path %q", bad)
	}
}

func TestSetManifestValue_AllScopesPreserveContent(t *testing.T) {
	path := writeSetManifest(t, setTestManifest)

	edits := []struct{ path, value string }{
		{"defaults.config.runtime", "pi"},
		{"defaults.vendor", "true"},
		{"github.config.kill_switch", "true"},
		{"github.fullsend_ref", "v2.0.0"},
		{"gitlab.agent_runner_tags", "[fullsend-agent, gpu]"},
		{"gitlab.control_runner_tags", `["fullsend-api"]`},
		{`github.repos["acme/api"].config.runtime`, "claude"},
		{`github.repos["acme/web.v2"].app_set`, "acme-set"},
		{`github.repos["acme/*"].inference.auth`, "openai-api-key"},
		{`gitlab.repos["group/project"].config.roles`, "[triage, coder]"},
	}
	for _, e := range edits {
		changed, err := SetManifestValue(path, e.path, e.value)
		require.NoError(t, err, e.path)
		assert.True(t, changed, e.path)
	}

	m := loadSetManifest(t, path)
	assert.Equal(t, "pi", m.Defaults.Config.ExplicitRuntime())
	require.NotNil(t, m.Defaults.Vendor)
	assert.True(t, *m.Defaults.Vendor)
	assert.Equal(t, "v2.0.0", m.GitHub.FullsendRef)
	assert.Equal(t, []string{"fullsend-agent", "gpu"}, m.GitLab.AgentRunnerTags)
	assert.Equal(t, []string{"fullsend-api"}, m.GitLab.ControlRunnerTags)
	assert.Equal(t, "claude", m.GitHub.Repos[0].Config.ExplicitRuntime())
	assert.Equal(t, "v1.0.0", m.GitHub.Repos[0].FullsendRef)
	assert.Equal(t, "acme-set", m.GitHub.Repos[1].AppSet)
	assert.Equal(t, InferenceAuthOpenAIAPIKey, m.GitHub.Repos[2].Inference.Auth)
	roles, ok := m.GitLab.Repos[0].Config.ExplicitRoles()
	assert.True(t, ok)
	assert.Equal(t, []string{"triage", "coder"}, roles)

	got := readFile(t, path)
	for _, keep := range []string{
		"# Fleet manifest — keep this header.\nversion: 1\n\ndefaults:\n",
		"  # Fleet-wide inference selection.\n",
		"auth: vertex-wif # chosen by platform team\n",
		"\ngithub:\n",
		"    # The API service.\n    - name: acme/api\n      fullsend_ref: v1.0.0\n",
		`    - name: "acme/web.v2"` + "\n",
		"\ngitlab:\n  url: https://gitlab.example.com\n",
	} {
		assert.Contains(t, got, keep)
	}
	// Ordering: defaults, then github, then gitlab, as written.
	assert.Less(t, strings.Index(got, "defaults:"), strings.Index(got, "github:"))
	assert.Less(t, strings.Index(got, "github:"), strings.Index(got, "gitlab:"))
}

func TestSetManifestValue_ReplaceKeepsLineComment(t *testing.T) {
	path := writeSetManifest(t, setTestManifest)
	_, err := SetManifestValue(path, "defaults.inference.auth", "openai-api-key")
	require.NoError(t, err)
	want := strings.Replace(setTestManifest, "auth: vertex-wif #", "auth: openai-api-key #", 1)
	assert.Equal(t, want, readFile(t, path), "only the edited line changes")
}

func TestSetManifestValue_InsertIsMinimal(t *testing.T) {
	path := writeSetManifest(t, setTestManifest)
	_, err := SetManifestValue(path, "github.mint_mode", "private")
	require.NoError(t, err)
	want := strings.Replace(setTestManifest,
		"  repos:\n    # The API service.",
		"  repos:\n    # The API service.", 1)
	want = strings.Replace(want,
		"    - name: \"acme/*\"\n",
		"    - name: \"acme/*\"\n  mint_mode: private\n", 1)
	assert.Equal(t, want, readFile(t, path))
}

func TestSetManifestValue_SelectorMatchesExactEntry(t *testing.T) {
	const name = `acme/a.b*[x]`
	path := writeSetManifest(t, "version: 1\ngithub:\n  repos:\n    - name: \"acme/a.b*[x]\"\n    - name: acme/a\n")
	// Whole-entry form: the glob entry already exists and is replaced.
	_, err := SetManifestValue(path, `github.repos["acme/a.b*[x]"]`, `{"fullsend_ref": "v3"}`)
	require.NoError(t, err)
	_, err = SetManifestValue(path, `github.repos["acme/a.b*[x]"].app_set`, "globset")
	require.NoError(t, err)
	m := loadSetManifest(t, path)
	require.Len(t, m.GitHub.Repos, 2)
	assert.Equal(t, name, m.GitHub.Repos[0].Name)
	assert.Equal(t, "v3", m.GitHub.Repos[0].FullsendRef)
	assert.Equal(t, "globset", m.GitHub.Repos[0].AppSet)
	assert.Empty(t, m.GitHub.Repos[1].AppSet, "glob-looking selector must not match other entries")
}

func TestSetManifestValue_WholeEntryCreateAndReplace(t *testing.T) {
	path := writeSetManifest(t, setTestManifest)

	changed, err := SetManifestValue(path, `github.repos["acme/new"]`, "app_set: acme\nconfig:\n  runtime: claude\n")
	require.NoError(t, err)
	assert.True(t, changed)
	m := loadSetManifest(t, path)
	require.Len(t, m.GitHub.Repos, 4)
	assert.Equal(t, "acme/new", m.GitHub.Repos[3].Name)
	assert.Equal(t, "acme", m.GitHub.Repos[3].AppSet)
	assert.Equal(t, "claude", m.GitHub.Repos[3].Config.ExplicitRuntime())

	// Replacing drops fields the new value omits; a matching name is fine.
	_, err = SetManifestValue(path, `github.repos["acme/api"]`, `{"name": "acme/api"}`)
	require.NoError(t, err)
	m = loadSetManifest(t, path)
	assert.Equal(t, "acme/api", m.GitHub.Repos[0].Name)
	assert.Empty(t, m.GitHub.Repos[0].FullsendRef)

	// An empty object creates a bare entry in a new forge section list.
	path2 := writeSetManifest(t, "version: 1\n")
	_, err = SetManifestValue(path2, `github.repos["acme/x"]`, "{}")
	require.NoError(t, err)
	m = loadSetManifest(t, path2)
	require.Len(t, m.GitHub.Repos, 1)
	assert.Equal(t, "acme/x", m.GitHub.Repos[0].Name)
}

func TestSetManifestValue_WholeReposList(t *testing.T) {
	path := writeSetManifest(t, setTestManifest)
	_, err := SetManifestValue(path, "gitlab.repos", `[{"name": "g/a"}, {"name": "g/b", "fullsend_ref": "v1"}]`)
	require.NoError(t, err)
	m := loadSetManifest(t, path)
	require.Len(t, m.GitLab.Repos, 2)
	assert.Equal(t, "v1", m.GitLab.Repos[1].FullsendRef)

	changed, err := UnsetManifestValue(path, "gitlab.repos")
	require.NoError(t, err)
	assert.True(t, changed)
	m = loadSetManifest(t, path)
	assert.Empty(t, m.GitLab.Repos)
	assert.NotContains(t, readFile(t, path), "g/a")
}

func TestSetManifestValue_UnsetForms(t *testing.T) {
	path := writeSetManifest(t, setTestManifest)

	changed, err := UnsetManifestValue(path, `github.repos["acme/api"].fullsend_ref`)
	require.NoError(t, err)
	assert.True(t, changed)

	changed, err = UnsetManifestValue(path, `github.repos["acme/web.v2"]`)
	require.NoError(t, err)
	assert.True(t, changed)

	changed, err = UnsetManifestValue(path, "github.mint_url")
	require.NoError(t, err)
	assert.True(t, changed)

	// Removing the only key of a mapping leaves an explicit empty mapping.
	changed, err = UnsetManifestValue(path, "defaults.config.runtime")
	require.NoError(t, err)
	assert.True(t, changed)

	before := readFile(t, path)
	for _, absent := range []string{"github.app_set", "defaults.signoff", "gitlab.config.runtime", `github.repos["acme/zzz"]`} {
		changed, err = UnsetManifestValue(path, absent)
		require.NoError(t, err, absent)
		assert.False(t, changed, absent)
	}
	assert.Equal(t, before, readFile(t, path))

	m := loadSetManifest(t, path)
	assert.Empty(t, m.GitHub.MintURL)
	require.Len(t, m.GitHub.Repos, 2)
	assert.Empty(t, m.GitHub.Repos[0].FullsendRef)
	assert.Equal(t, "acme/*", m.GitHub.Repos[1].Name)
	assert.True(t, m.Defaults.Config.IsSet())
	assert.Empty(t, m.Defaults.Config.ExplicitRuntime())
	got := readFile(t, path)
	assert.NotContains(t, got, "mint_url")
	assert.NotContains(t, got, "web.v2")
	assert.Contains(t, got, "config: {}")
	assert.Contains(t, got, "# The API service.")

	// Unsetting a missing manifest does not create it.
	missing := filepath.Join(t.TempDir(), "repos.yaml")
	changed, err = UnsetManifestValue(missing, "github.url")
	require.NoError(t, err)
	assert.False(t, changed)
	_, statErr := os.Stat(missing)
	assert.True(t, os.IsNotExist(statErr))
}

func TestSetManifestValue_DistinctValues(t *testing.T) {
	path := writeSetManifest(t, "version: 1\ngithub:\n  repos:\n    - name: acme/api\n")
	for _, e := range []struct{ path, value, want string }{
		{"defaults.vendor", "false", "vendor: false"},
		{`github.repos["acme/api"].fullsend_ref`, "none", "fullsend_ref: none"},
		{`github.repos["acme/api"].app_set`, "", `app_set: ""`},
		{`github.repos["acme/api"].config`, "{}", "config: {}"},
		{"github.config.allowed_remote_resources", "[]", "allowed_remote_resources: []"},
		{`github.repos["acme/api"].config_base.source`, "true", `source: "true"`},
	} {
		_, err := SetManifestValue(path, e.path, e.value)
		require.NoError(t, err, e.path)
		assert.Contains(t, readFile(t, path), e.want, e.path)
	}
	m := loadSetManifest(t, path)
	require.NotNil(t, m.Defaults.Vendor)
	assert.False(t, *m.Defaults.Vendor)
	entry := m.GitHub.Repos[0]
	assert.Equal(t, NoneSentinel, entry.FullsendRef)
	assert.True(t, entry.Config.IsSet())
	list, declared := m.GitHub.Config.ExplicitAllowedRemoteResources()
	assert.True(t, declared)
	assert.Empty(t, list)

	_, err := UnsetManifestValue(path, "defaults.vendor")
	require.NoError(t, err)
	assert.Nil(t, loadSetManifest(t, path).Defaults.Vendor, "unset is distinct from false")

	// Setting the same value again reports no change and keeps the bytes.
	before := readFile(t, path)
	changed, err := SetManifestValue(path, `github.repos["acme/api"].fullsend_ref`, "none")
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, before, readFile(t, path))
}

func TestSetManifestValue_CreatesManifestAndParents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.yaml")
	changed, err := SetManifestValue(path, "github.mint_url", "https://mint.example.com")
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "version: 1\ngithub:\n  mint_url: https://mint.example.com\n", readFile(t, path))

	_, err = SetManifestValue(path, "defaults.config.inference.provider", "vertex")
	require.NoError(t, err)
	_, err = SetManifestValue(path, "defaults.config.models.aliases.opus", "claude-opus-x")
	require.NoError(t, err)
	_, err = SetManifestValue(path, `defaults.config.models.aliases["sonnet"]`, "claude-sonnet-x")
	require.NoError(t, err)
	got := readFile(t, path)
	assert.Contains(t, got, "provider: vertex")
	assert.Contains(t, got, "opus: claude-opus-x")
	assert.Contains(t, got, "sonnet: claude-sonnet-x")
	loadSetManifest(t, path)

	// An empty file is treated like a missing one.
	empty := writeSetManifest(t, "")
	_, err = SetManifestValue(empty, "defaults.inference.auth", "vertex-wif")
	require.NoError(t, err)
	assert.Equal(t, InferenceAuthVertexWIF, loadSetManifest(t, empty).Defaults.Inference.Auth)

	// A null parent value is replaced by a mapping.
	nullParent := writeSetManifest(t, "version: 1\ndefaults:\n  config:\n")
	_, err = SetManifestValue(nullParent, "defaults.config.runtime", "claude")
	require.NoError(t, err)
	assert.Equal(t, "claude", loadSetManifest(t, nullParent).Defaults.Config.ExplicitRuntime())

	// A flow-style parent is still edited correctly.
	flow := writeSetManifest(t, "version: 1\ndefaults: {inference: {auth: vertex-wif}}\n")
	_, err = SetManifestValue(flow, "defaults.inference.auth", "openai-api-key")
	require.NoError(t, err)
	_, err = SetManifestValue(flow, "defaults.signoff", "true")
	require.NoError(t, err)
	m := loadSetManifest(t, flow)
	assert.Equal(t, InferenceAuthOpenAIAPIKey, m.Defaults.Inference.Auth)
	require.NotNil(t, m.Defaults.Signoff)
	assert.True(t, *m.Defaults.Signoff)
}

func TestSetManifestValue_FailuresLeaveFileUnchanged(t *testing.T) {
	tests := []struct {
		name, path, value string
		unset             bool
		want              []string
	}{
		{name: "unknown config field", path: "defaults.config.nope", value: "x",
			want: []string{"defaults.config.nope", `unknown field "nope" at defaults.config`, "runtime"}},
		{name: "unknown top-level field", path: "nope.url", value: "x",
			want: []string{`unknown field "nope" at the top level`}},
		{name: "wrong-scope field", path: "defaults.mint_url", value: "https://m.example.com",
			want: []string{`unknown field "mint_url" at defaults`, "github.mint_url", "defaults.config.mint_url"}},
		{name: "config field at scope", path: "github.kill_switch", value: "true",
			want: []string{"github.config.kill_switch"}},
		{name: "removed sibling", path: "defaults.runtime", value: "claude",
			want: []string{"no longer supported", "defaults.config.runtime"}},
		{name: "removed entry sibling", path: `github.repos["acme/api"].allowed_remote_resources`, value: "[]",
			want: []string{`github.repos["acme/api"].config.allowed_remote_resources`}},
		{name: "deprecated alias", path: "gitlab.runner_tags", value: "[a]",
			want: []string{"deprecated alias", "gitlab.agent_runner_tags"}},
		{name: "version", path: "version", value: "2", want: []string{"version is fixed"}},
		{name: "entry name", path: `github.repos["acme/api"].name`, value: "acme/b",
			want: []string{"entry name comes from the selector"}},
		{name: "remove required name", path: `github.repos["acme/api"].name`, unset: true,
			want: []string{"name is required"}},
		{name: "wrong-platform field", path: "gitlab.mint_url", value: "https://m.example.com",
			want: []string{"gitlab.mint_url", "GitHub-only"}},
		{name: "wrong-platform entry field", path: `gitlab.repos["group/project"].app_set`, value: "x",
			want: []string{"app_set is only supported for GitHub repos"}},
		{name: "github runner tags", path: "github.agent_runner_tags", value: "[a]",
			want: []string{"agent_runner_tags"}},
		{name: "invalid enum", path: "github.mint_mode", value: "hybrid",
			want: []string{"github.mint_mode", "hybrid"}},
		{name: "invalid inference auth", path: "gitlab.inference.auth", value: "openai-wif",
			want: []string{"gitlab.inference.auth"}},
		{name: "invalid config enum", path: "defaults.config.runtime", value: "bogus",
			want: []string{"defaults.config.runtime", "resulting manifest is invalid"}},
		{name: "bool type", path: "defaults.vendor", value: "yes",
			want: []string{"defaults.vendor must be true or false"}},
		{name: "list type", path: "gitlab.agent_runner_tags", value: "a,b",
			want: []string{"must be a YAML or JSON list"}},
		{name: "empty list arg", path: "gitlab.agent_runner_tags", value: "",
			want: []string{"--unset"}},
		{name: "object type", path: `github.repos["acme/api"].config`, value: "[]",
			want: []string{"mapping (object)"}},
		{name: "bad yaml", path: "defaults.config", value: "{a: [",
			want: []string{"not valid YAML or JSON"}},
		{name: "unknown field in object", path: `github.repos["acme/new"]`, value: `{"bogus": 1}`,
			want: []string{"bogus"}},
		{name: "unknown field in config object", path: "defaults.config", value: `{"bogus": 1}`,
			want: []string{"bogus"}},
		{name: "mismatched entry name", path: `github.repos["acme/new"]`, value: `{"name": "acme/other"}`,
			want: []string{"does not match the selector"}},
		{name: "duplicate entry across forges", path: `gitlab.repos["acme/api"]`, value: "{}",
			want: []string{"duplicate repo"}},
		{name: "duplicate in list", path: "github.repos", value: `[{"name": "a/b"}, {"name": "a/b"}]`,
			want: []string{"duplicate repo"}},
		{name: "case variant selector", path: `github.repos["ACME/API"].app_set`, value: "x",
			want: []string{"no entry is named exactly", "acme/api"}},
		{name: "nested missing entry", path: `github.repos["acme/typo"].config.runtime`, value: "claude",
			want: []string{"acme/typo", "does not exist"}},
		{name: "nested unset missing entry", path: `github.repos["acme/typo"].app_set`, unset: true,
			want: []string{"does not exist"}},
		{name: "list element", path: "gitlab.agent_runner_tags.x", value: "a",
			want: []string{"is a list"}},
		{name: "repos without selector", path: "github.repos.acme", value: "a",
			want: []string{`github.repos["owner/repo"]`}},
		{name: "selector on struct", path: `github["x"]`, value: "a",
			want: []string{"does not accept"}},
		{name: "scalar has no fields", path: "github.url.host", value: "a",
			want: []string{"has no field"}},
		{name: "invalid resulting manifest", path: "gitlab.url", unset: true,
			want: []string{"gitlab.url is required"}},
		{name: "bad path", path: "github..url", value: "x", want: []string{"invalid path"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeSetManifest(t, setTestManifest)
			var err error
			if tt.unset {
				_, err = UnsetManifestValue(path, tt.path)
			} else {
				_, err = SetManifestValue(path, tt.path, tt.value)
			}
			require.Error(t, err)
			for _, w := range tt.want {
				assert.Contains(t, err.Error(), w)
			}
			assert.Equal(t, setTestManifest, readFile(t, path), "file must be byte-identical after a failed edit")
		})
	}
}

func TestSetManifestValue_UnsetDeprecatedRunnerTags(t *testing.T) {
	path := writeSetManifest(t, "version: 1\ngitlab:\n  url: https://gitlab.example.com\n  runner_tags: [a]\n  repos: []\n")
	changed, err := UnsetManifestValue(path, "gitlab.runner_tags")
	require.NoError(t, err)
	assert.True(t, changed)
	assert.NotContains(t, readFile(t, path), "runner_tags")
}

func TestSetManifestValue_DuplicateEntriesInFile(t *testing.T) {
	content := "version: 1\ngithub:\n  repos:\n    - name: acme/a\n    - name: acme/a\n"
	path := writeSetManifest(t, content)
	_, err := SetManifestValue(path, `github.repos["acme/a"].app_set`, "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate entries")
	assert.Equal(t, content, readFile(t, path))
}

func TestSetManifestValue_NonMappingParent(t *testing.T) {
	content := "version: 1\ndefaults: [1]\n"
	path := writeSetManifest(t, content)
	_, err := SetManifestValue(path, "defaults.vendor", "true")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "defaults is not a mapping")
	assert.Equal(t, content, readFile(t, path))

	_, err = SetManifestValue(writeSetManifest(t, "- a\n"), "defaults.vendor", "true")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "root must be a YAML mapping")
}

func TestSetManifestValue_ReadOnly(t *testing.T) {
	_, err := SetManifestValue("https://example.com/repos.yaml", "github.url", "https://ghes.example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is read-only; apply the equivalent edit to its source")

	path := writeSetManifest(t, setTestManifest)
	require.NoError(t, os.Chmod(path, 0o444))
	_, err = SetManifestValue(path, "github.fullsend_ref", "v9")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is read-only")
	assert.Equal(t, setTestManifest, readFile(t, path))
}

func TestSetManifestValue_TooLarge(t *testing.T) {
	path := writeSetManifest(t, "version: 1\n# "+strings.Repeat("x", maxManifestBytes)+"\n")
	_, err := SetManifestValue(path, "github.url", "https://ghes.example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum size")
}

func TestSetManifestValue_MigratedSetDefaultKeys(t *testing.T) {
	path := writeSetManifest(t, "version: 1\ngithub:\n  repos:\n    - name: acme/a\n")
	hash := strings.Repeat("ab", 32)
	for _, e := range []struct{ path, value string }{
		{"defaults.inference.auth", "vertex-wif"},
		{"defaults.vendor", "true"},
		{"defaults.config_base.source", "https://example.com/preset.yaml"},
		{"defaults.config_base.sha256", hash},
		{"github.url", "https://ghes.example.com"},
		{"github.mint_url", "https://mint.example.com"},
		{"github.mint_mode", "private"},
		{"github.fullsend_ref", "v1.2.3"},
		{"github.inference.auth", "openai-wif"},
		{"gitlab.url", "https://gitlab.example.com"},
		{"gitlab.fullsend_ref", "v1.2.3"},
		{"gitlab.inference.auth", "vertex-wif"},
	} {
		_, err := SetManifestValue(path, e.path, e.value)
		require.NoError(t, err, e.path)
	}
	m := loadSetManifest(t, path)
	assert.Equal(t, "https://example.com/preset.yaml", m.Defaults.ConfigBase.Source)
	assert.Equal(t, hash, m.Defaults.ConfigBase.SHA256)
	assert.Equal(t, MintModePrivate, m.GitHub.MintMode)
	assert.Equal(t, InferenceAuthOpenAIWIF, m.GitHub.Inference.Auth)
	assert.Equal(t, "https://gitlab.example.com", m.GitLab.URL)

	for _, bad := range []struct{ path, value string }{
		{"github.url", "https://ghes.example.com/path"},
		{"github.fullsend_ref", "v1; rm -rf /"},
		{"defaults.config_base.source", "http://insecure.example.com/x.yaml"},
		{"defaults.config_base.sha256", "short"},
	} {
		before := readFile(t, path)
		_, err := SetManifestValue(path, bad.path, bad.value)
		require.Error(t, err, bad.path)
		assert.Equal(t, before, readFile(t, path))
	}
}

func TestSetManifestValue_ConfigBaseLocalPathContained(t *testing.T) {
	path := writeSetManifest(t, "version: 1\n")
	_, err := SetManifestValue(path, "defaults.config_base.source", "../outside.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes manifest directory")
	_, err = SetManifestValue(path, "defaults.config_base.source", "presets/base.yaml")
	require.NoError(t, err)
}

func TestSetManifestValue_NumberTypes(t *testing.T) {
	type numbers struct {
		I int
		U uint
		F float64
	}
	_, err := valueNode(reflect.TypeFor[numbers]().Field(0).Type, "12", "p")
	require.NoError(t, err)
	_, err = valueNode(reflect.TypeFor[numbers]().Field(0).Type, "x", "p")
	assert.ErrorContains(t, err, "must be an integer")
	_, err = valueNode(reflect.TypeFor[numbers]().Field(1).Type, "-1", "p")
	assert.ErrorContains(t, err, "non-negative integer")
	_, err = valueNode(reflect.TypeFor[numbers]().Field(1).Type, "3", "p")
	require.NoError(t, err)
	_, err = valueNode(reflect.TypeFor[numbers]().Field(2).Type, "1.5", "p")
	require.NoError(t, err)
	_, err = valueNode(reflect.TypeFor[numbers]().Field(2).Type, "x", "p")
	assert.ErrorContains(t, err, "must be a number")
}

func TestSetManifestValue_PreservesIndentWidth(t *testing.T) {
	content := "version: 1\ngithub:\n    mint_url: https://mint.example.com\n    repos:\n        - name: acme/a\n"
	path := writeSetManifest(t, content)
	_, err := SetManifestValue(path, `github.repos["acme/a"].config`, `{"runtime": "claude", "roles": ["coder"]}`)
	require.NoError(t, err)
	got := readFile(t, path)
	assert.True(t, strings.HasPrefix(got, "version: 1\ngithub:\n    mint_url: https://mint.example.com\n"), got)
	assert.Contains(t, got, "          config:\n              runtime: claude\n")
	loadSetManifest(t, path)
}

func TestSetManifestValue_InterfaceMapValues(t *testing.T) {
	const gw = "version: 1\ndefaults:\n  config:\n    inference:\n      gateway:\n        url: https://gw.example.com\n        audience: gw-aud\n        models:\n          m:\n            api: anthropic-messages\n"
	base := `defaults.config.inference.gateway.models["m"].compat.`

	for _, tc := range []struct {
		key, value, want string
	}{
		{"supportsStrictTools", "false", "supportsStrictTools: false"},
		{"supportsTemperature", "true", "supportsTemperature: true"},
		{"sessionAffinityFormat", "openrouter", "sessionAffinityFormat: openrouter"},
	} {
		path := writeSetManifest(t, gw)
		changed, err := SetManifestValue(path, base+tc.key, tc.value)
		require.NoError(t, err, tc.key)
		assert.True(t, changed)
		loadSetManifest(t, path)
		assert.Contains(t, readFile(t, path), tc.want, tc.key)
	}

	// A number parses for an interface target; whole-manifest validation
	// rejects it for a boolean flag.
	path := writeSetManifest(t, gw)
	_, err := SetManifestValue(path, base+"supportsStrictTools", "1.5")
	require.Error(t, err, "a number is not a valid boolean flag")
	assert.Contains(t, err.Error(), "resulting manifest is invalid")

	// A whole map still works.
	_, err = SetManifestValue(path, `defaults.config.inference.gateway.models["m"].compat`, `{"supportsStrictTools": true}`)
	require.NoError(t, err)

	// Empty values and aliases are rejected.
	_, err = valueNode(reflect.TypeFor[any](), "", "p")
	assert.ErrorContains(t, err, "must be a YAML or JSON value")
	_, err = valueNode(reflect.TypeFor[any](), "*a", "p")
	assert.Error(t, err)
}

func TestSetManifestValue_RejectsEditOverSizeLimit(t *testing.T) {
	pad := strings.Repeat("x", maxManifestBytes-len("version: 1\n# \n")-10)
	content := "version: 1\n# " + pad + "\n"
	require.Less(t, len(content), maxManifestBytes)
	path := writeSetManifest(t, content)

	_, err := SetManifestValue(path, "github.mint_url", "https://mint.example.com/"+strings.Repeat("a", 64))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "would exceed the maximum size")
	assert.Equal(t, content, readFile(t, path))
}

func TestSetManifestValue_UnsetMissingEntryIsConsistent(t *testing.T) {
	const entryPath = `github.repos["acme/typo"].config.runtime`
	for name, content := range map[string]string{
		"no github":     "version: 1\n",
		"null github":   "version: 1\ngithub:\n",
		"null repos":    "version: 1\ngithub:\n  repos:\n",
		"existing list": "version: 1\ngithub:\n  repos:\n    - name: acme/api\n",
	} {
		path := writeSetManifest(t, content)
		changed, err := UnsetManifestValue(path, entryPath)
		require.Error(t, err, name)
		assert.False(t, changed, name)
		assert.Contains(t, err.Error(), "does not exist", name)
		assert.Equal(t, content, readFile(t, path), name)
	}

	// Removing a whole missing entry stays a no-op everywhere.
	for _, content := range []string{"version: 1\n", "version: 1\ngithub:\n  repos:\n    - name: acme/api\n"} {
		path := writeSetManifest(t, content)
		changed, err := UnsetManifestValue(path, `github.repos["acme/typo"]`)
		require.NoError(t, err)
		assert.False(t, changed)
	}
}

func TestSetManifestValue_UnsetMissingFileRejectsMissingEntryPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "repos.yaml")

	changed, err := UnsetManifestValue(path, `github.repos["acme/typo"].config.runtime`)
	require.Error(t, err)
	assert.False(t, changed)
	assert.Contains(t, err.Error(), "does not exist")
	assert.NoFileExists(t, path)

	// Whole-entry and plain-key removal stay no-ops on a missing file.
	for _, p := range []string{`github.repos["acme/typo"]`, "defaults.inference.auth"} {
		changed, err := UnsetManifestValue(path, p)
		require.NoError(t, err, p)
		assert.False(t, changed, p)
		assert.NoFileExists(t, path)
	}
}

func TestSetManifestValue_RunnerTagsRejectEmptyElements(t *testing.T) {
	const base = "version: 1\ngitlab:\n  url: https://gitlab.example.com\n  repos: []\n"
	for _, key := range []string{"gitlab.agent_runner_tags", "gitlab.control_runner_tags"} {
		for name, value := range map[string]string{
			"empty string": `[""]`,
			"whitespace":   `["  "]`,
			"null":         `[null]`,
			"mixed":        `[gpu, ""]`,
		} {
			path := writeSetManifest(t, base)
			changed, err := SetManifestValue(path, key, value)
			require.Error(t, err, key+" "+name)
			assert.False(t, changed, key+" "+name)
			if name == "null" {
				assert.Contains(t, err.Error(), "not null", key+" "+name)
			} else {
				assert.Contains(t, err.Error(), "must not be empty", key+" "+name)
			}
			assert.Equal(t, base, readFile(t, path), key+" "+name)
		}

		// A genuinely empty list is still accepted.
		path := writeSetManifest(t, base)
		_, err := SetManifestValue(path, key, "[]")
		require.NoError(t, err, key)
	}
}

func TestSetManifestValue_DanglingSymlinkIsFollowed(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "fleet"), 0o755))
	target := filepath.Join(dir, "fleet", "repos.yaml")
	link := filepath.Join(dir, "repos.yaml")
	require.NoError(t, os.Symlink(filepath.Join("fleet", "repos.yaml"), link))

	changed, err := SetManifestValue(link, "defaults.inference.auth", "vertex-wif")
	require.NoError(t, err)
	assert.True(t, changed)

	info, err := os.Lstat(link)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "symlink must be preserved")
	assert.Contains(t, readFile(t, target), "vertex-wif")
}

func TestSetManifestValue_IntegerIsWrittenInBase10(t *testing.T) {
	type numbers struct {
		I int
		U uint8
	}
	for _, tc := range []struct {
		idx       int
		raw, want string
	}{
		{0, "010", "10"},
		{0, "+7", "7"},
		{0, "-0", "0"},
		{1, "010", "10"},
		{1, "007", "7"},
	} {
		n, err := valueNode(reflect.TypeFor[numbers]().Field(tc.idx).Type, tc.raw, "p")
		require.NoError(t, err, tc.raw)
		assert.Equal(t, tc.want, n.Value, tc.raw)
		var got int
		require.NoError(t, n.Decode(&got))
		assert.Equal(t, tc.want, strconv.Itoa(got), "node must reload as the validated number: %s", tc.raw)
	}
}

func TestSetManifestValue_UnsetAgentRunnerTagsClearsLegacyAlias(t *testing.T) {
	const legacy = "version: 1\ngitlab:\n  url: https://gitlab.example.com\n  runner_tags: [legacy]\n  repos: []\n"

	for name, run := range map[string]func(path string) error{
		"set empty": func(path string) error {
			_, err := SetManifestValue(path, "gitlab.agent_runner_tags", "[]")
			return err
		},
		"unset": func(path string) error {
			changed, err := UnsetManifestValue(path, "gitlab.agent_runner_tags")
			assert.True(t, changed)
			return err
		},
		"set new tags": func(path string) error {
			_, err := SetManifestValue(path, "gitlab.agent_runner_tags", "[fresh]")
			return err
		},
	} {
		path := writeSetManifest(t, legacy)
		require.NoError(t, run(path), name)
		assert.NotContains(t, readFile(t, path), "legacy", name)
		m := loadSetManifest(t, path)
		if name == "set new tags" {
			assert.Equal(t, []string{"fresh"}, gitlabAgentRunnerTags(m), name)
		} else {
			assert.Empty(t, gitlabAgentRunnerTags(m), name)
		}
	}
}

func TestSetManifestValue_RejectsAliasesAndDropsAnchors(t *testing.T) {
	_, err := valueNode(reflect.TypeFor[map[string]string](), "{a: &x v, b: *x}", "p")
	assert.ErrorContains(t, err, "alias")
	_, err = valueNode(reflect.TypeFor[[]string](), "[&x v, *x]", "p")
	assert.ErrorContains(t, err, "alias")

	n, err := valueNode(reflect.TypeFor[map[string]any](), "{gate: &gate on}", "p")
	require.NoError(t, err)
	for _, c := range n.Content {
		assert.Empty(t, c.Anchor)
	}

	// A new anchor named like one already in the file must not capture the
	// file's later alias: the edit either fails with the file untouched or
	// leaves the alias's value alone.
	content := "version: 1\ndefaults:\n  config:\n    inference:\n      gateway:\n        url: https://gw.example.com\n        audience: gw-aud\n        models:\n          m:\n            api: anthropic-messages\n            compat:\n              supportsTemperature: &gate true\n              supportsStrictTools: *gate\n"
	path := writeSetManifest(t, content)
	_, err = SetManifestValue(path, `defaults.config.inference.gateway.models["m"].compat.supportsTemperature`, "&gate false")
	if err != nil {
		assert.Equal(t, content, readFile(t, path))
		return
	}
	var doc struct {
		Defaults struct {
			Config struct {
				Inference struct {
					Gateway struct {
						Models map[string]struct {
							Compat map[string]any `yaml:"compat"`
						} `yaml:"models"`
					} `yaml:"gateway"`
				} `yaml:"inference"`
			} `yaml:"config"`
		} `yaml:"defaults"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(readFile(t, path)), &doc))
	compat := doc.Defaults.Config.Inference.Gateway.Models["m"].Compat
	assert.Equal(t, true, compat["supportsStrictTools"])
}

func TestWriteManifestAtomic(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.yaml")
	require.NoError(t, os.WriteFile(real, []byte("old\n"), 0o640))
	link := filepath.Join(dir, "repos.yaml")
	require.NoError(t, os.Symlink(real, link))

	require.NoError(t, writeManifestAtomic(link, []byte("new\n")))

	info, err := os.Lstat(link)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "symlink must be preserved")
	assert.Equal(t, "new\n", readFile(t, real))
	st, err := os.Stat(real)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), st.Mode().Perm())
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "no temp files left behind")

	// A failed write leaves the original in place.
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if os.Geteuid() != 0 {
		require.Error(t, writeManifestAtomic(link, []byte("newer\n")))
		assert.Equal(t, "new\n", readFile(t, real))
	}
}

func TestSetManifestValue_WrongForgeFieldsRejectedWhateverTheValue(t *testing.T) {
	const base = "version: 1\n" +
		"github:\n  repos: []\n" +
		"gitlab:\n  url: https://gitlab.example.com\n  repos:\n    - name: group/project\n"
	tests := []struct {
		name, path, value string
		want              string
	}{
		{"github empty runner tags", "github.agent_runner_tags", "[]", "GitLab-only"},
		{"github empty control tags", "github.control_runner_tags", "[]", "GitLab-only"},
		{"gitlab empty mint_url", "gitlab.mint_url", "", "GitHub-only"},
		{"gitlab empty mint_mode", "gitlab.mint_mode", "", "GitHub-only"},
		{"gitlab empty app_set", "gitlab.app_set", "", "GitHub-only"},
		{"gitlab entry empty app_set", `gitlab.repos["group/project"].app_set`, "", "only supported for GitHub repos"},
		{"gitlab entry empty mint_url", `gitlab.repos["group/project"].mint_url`, "", "only supported for GitHub repos"},
		{"gitlab object empty mint_url", "gitlab", `{"url": "https://gitlab.example.com", "mint_url": ""}`, "gitlab.mint_url"},
		{"github object empty runner tags", "github", `{"agent_runner_tags": []}`, "github.agent_runner_tags"},
		{"gitlab entry object empty app_set", `gitlab.repos["group/new"]`, `{"app_set": ""}`, "only supported for GitHub repos"},
		{"gitlab repos list empty mint_mode", "gitlab.repos", `[{"name": "group/new", "mint_mode": ""}]`, "mint_mode"},
		{"gitlab object nested entry empty app_set", "gitlab", `{"url": "https://gitlab.example.com", "repos": [{"name": "g/p", "app_set": ""}]}`, "app_set"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeSetManifest(t, base)
			changed, err := SetManifestValue(path, tc.path, tc.value)
			require.Error(t, err)
			assert.False(t, changed)
			assert.Contains(t, err.Error(), tc.want)
			assert.Equal(t, base, readFile(t, path))
		})
	}

	// --unset still removes a wrong-forge key that is already in the file.
	path := writeSetManifest(t, "version: 1\ngitlab:\n  url: https://gitlab.example.com\n  mint_url: \"\"\n  repos: []\n")
	changed, err := UnsetManifestValue(path, "gitlab.mint_url")
	require.NoError(t, err)
	assert.True(t, changed)
	assert.NotContains(t, readFile(t, path), "mint_url")
}

func TestSetManifestValue_WholeObjectRejectsDeprecatedRunnerTags(t *testing.T) {
	const base = "version: 1\ngitlab:\n  url: https://gitlab.example.com\n  repos: []\n"
	path := writeSetManifest(t, base)
	changed, err := SetManifestValue(path, "gitlab", `{"url": "https://gitlab.example.com", "runner_tags": ["legacy"]}`)
	require.Error(t, err)
	assert.False(t, changed)
	assert.Contains(t, err.Error(), "deprecated alias")
	assert.Equal(t, base, readFile(t, path))
}

func TestSetManifestValue_DotDotInManifestArgThroughDirSymlink(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "actual", "deep"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join("actual", "deep"), filepath.Join(dir, "sub")))
	// An unrelated file sits at the lexical destination <dir>/repos.yaml.
	const lexical = "version: 1\ngithub:\n  repos: []\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "repos.yaml"), []byte(lexical), 0o644))

	// sub/.. is actual/, so the manifest is actual/repos.yaml (missing).
	arg := dir + string(filepath.Separator) + "sub" + string(filepath.Separator) + ".." + string(filepath.Separator) + "repos.yaml"
	changed, err := SetManifestValue(arg, "defaults.inference.auth", "vertex-wif")
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Contains(t, readFile(t, filepath.Join(dir, "actual", "repos.yaml")), "vertex-wif")
	assert.Equal(t, lexical, readFile(t, filepath.Join(dir, "repos.yaml")), "the lexical destination must stay untouched")
}

func TestSetManifestValue_WholeObjectRejectsNullStringElements(t *testing.T) {
	const base = "version: 1\ngitlab:\n  url: https://gitlab.example.com\n  repos: []\n"
	for name, tc := range map[string]struct{ path, value string }{
		"agent tags in platform":   {"gitlab", `{"url": "https://gitlab.example.com", "agent_runner_tags": [null]}`},
		"control tags in platform": {"gitlab", `{"url": "https://gitlab.example.com", "control_runner_tags": ["a", null]}`},
		"nested list in config":    {"defaults.config", `{"allowed_remote_resources": [null]}`},
	} {
		path := writeSetManifest(t, base)
		changed, err := SetManifestValue(path, tc.path, tc.value)
		require.Error(t, err, name)
		assert.False(t, changed, name)
		assert.Contains(t, err.Error(), "not null", name)
		assert.Equal(t, base, readFile(t, path), name)
	}
}

func TestSetManifestValue_DanglingSymlinkWithDotDotThroughDirSymlink(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "actual", "deep"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join("actual", "deep"), filepath.Join(dir, "sub")))
	link := filepath.Join(dir, "link.yaml")
	// A literal string: filepath.Join would collapse the ".." lexically.
	require.NoError(t, os.Symlink("sub/../repos.yaml", link))

	changed, err := SetManifestValue(link, "defaults.inference.auth", "vertex-wif")
	require.NoError(t, err)
	assert.True(t, changed)

	info, err := os.Lstat(link)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "symlink must be preserved")
	assert.Contains(t, readFile(t, filepath.Join(dir, "actual", "repos.yaml")), "vertex-wif")
	assert.NoFileExists(t, filepath.Join(dir, "repos.yaml"), "the lexical path must not be created")
}

func TestSetManifestValue_MissingIntermediateWithDotDotIsRejected(t *testing.T) {
	dir := t.TempDir()
	// An existing manifest sits where a lexical ".." collapse would land.
	const lexical = "version: 1\ngithub:\n  repos:\n    - name: acme/api\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "repos.yaml"), []byte(lexical), 0o644))

	sep := string(filepath.Separator)
	arg := dir + sep + "missing" + sep + ".." + sep + "repos.yaml"
	changed, err := SetManifestValue(arg, "defaults.inference.auth", "vertex-wif")
	require.Error(t, err)
	assert.False(t, changed)
	assert.Contains(t, err.Error(), "does not exist")
	assert.Equal(t, lexical, readFile(t, filepath.Join(dir, "repos.yaml")), "the lexical destination must stay untouched")

	changed, err = UnsetManifestValue(arg, "defaults.inference.auth")
	require.Error(t, err)
	assert.False(t, changed)
	assert.Equal(t, lexical, readFile(t, filepath.Join(dir, "repos.yaml")))
}

func TestSetManifestValue_MissingIntermediateCannotBypassReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	dir := t.TempDir()
	const lexical = "version: 1\n"
	ro := filepath.Join(dir, "repos.yaml")
	require.NoError(t, os.WriteFile(ro, []byte(lexical), 0o444))

	sep := string(filepath.Separator)
	arg := dir + sep + "missing" + sep + ".." + sep + "repos.yaml"
	_, err := SetManifestValue(arg, "defaults.inference.auth", "vertex-wif")
	require.Error(t, err)
	assert.Equal(t, lexical, readFile(t, ro))
}

func TestSetManifestValue_DanglingSymlinkThroughMissingDirIsRejected(t *testing.T) {
	dir := t.TempDir()
	const lexical = "version: 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "repos.yaml"), []byte(lexical), 0o644))
	link := filepath.Join(dir, "link.yaml")
	require.NoError(t, os.Symlink("missing/../repos.yaml", link))

	changed, err := SetManifestValue(link, "defaults.inference.auth", "vertex-wif")
	require.Error(t, err)
	assert.False(t, changed)
	assert.Equal(t, lexical, readFile(t, filepath.Join(dir, "repos.yaml")))
	info, lerr := os.Lstat(link)
	require.NoError(t, lerr)
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "symlink must be preserved")
}

func TestSetManifestValue_NonDirectoryIntermediateIsRejected(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0o644))
	sep := string(filepath.Separator)
	arg := dir + sep + "file" + sep + ".." + sep + "repos.yaml"
	_, err := SetManifestValue(arg, "defaults.inference.auth", "vertex-wif")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
	assert.NoFileExists(t, filepath.Join(dir, "repos.yaml"))
}

func TestSetManifestValue_RejectsMergeKeyInValue(t *testing.T) {
	const base = "version: 1\ngitlab:\n  url: https://gitlab.example.com\n  repos: []\n"
	for name, tc := range map[string]struct{ path, value string }{
		"platform object": {"gitlab", `{url: "https://gitlab.example.com", <<: {runner_tags: [legacy]}}`},
		"nested object":   {"defaults.config", `{inference: {<<: {auth: x}}}`},
	} {
		path := writeSetManifest(t, base)
		changed, err := SetManifestValue(path, tc.path, tc.value)
		require.Error(t, err, name)
		assert.False(t, changed, name)
		assert.Contains(t, err.Error(), "merge key", name)
		assert.Equal(t, base, readFile(t, path), name)
	}
}

func TestSetManifestValue_LocalSourceValidatedAgainstRequestedPath(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "store")
	fleet := filepath.Join(dir, "fleet")
	require.NoError(t, os.Mkdir(store, 0o755))
	require.NoError(t, os.Mkdir(fleet, 0o755))
	const base = "version: 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(store, "repos.yaml"), []byte(base), 0o644))
	link := filepath.Join(fleet, "repos.yaml")
	require.NoError(t, os.Symlink(filepath.Join(store, "repos.yaml"), link))

	// A source inside the symlink target's directory but outside the
	// requested manifest's directory is rejected, since LoadManifest through
	// the same path would reject it.
	changed, err := SetManifestValue(link, "defaults.config_base.source", filepath.Join(store, "preset.yaml"))
	require.Error(t, err)
	assert.False(t, changed)
	assert.Contains(t, err.Error(), "escapes manifest directory")
	assert.Equal(t, base, readFile(t, filepath.Join(store, "repos.yaml")))

	// A source inside the requested manifest's directory is accepted and the
	// result loads and validates through the same path.
	changed, err = SetManifestValue(link, "defaults.config_base.source", filepath.Join(fleet, "preset.yaml"))
	require.NoError(t, err)
	assert.True(t, changed)
	loadSetManifest(t, link)
}

func TestSetManifestValue_RejectsEditThroughExistingMergeKey(t *testing.T) {
	const base = `version: 1
defaults:
  <<:
    config:
      kill_switch: true
      allowed_remote_resources: []
github:
  repos: []
`
	for name, tc := range map[string]struct{ path, value string }{
		"insert nested key": {"defaults.config.runtime", "pi"},
		"insert sibling":    {"defaults.inference.auth", "vertex-wif"},
	} {
		path := writeSetManifest(t, base)
		changed, err := SetManifestValue(path, tc.path, tc.value)
		require.Error(t, err, name)
		assert.False(t, changed, name)
		assert.Contains(t, err.Error(), "merge key", name)
		assert.Equal(t, base, readFile(t, path), name)
	}

	// Removing a literal key beside a merge key would surface the merged one.
	const withKey = "version: 1\ndefaults:\n  <<:\n    config: {kill_switch: true}\n  config:\n    runtime: pi\n"
	path := writeSetManifest(t, withKey)
	changed, err := UnsetManifestValue(path, "defaults.config")
	require.Error(t, err)
	assert.False(t, changed)
	assert.Contains(t, err.Error(), "merge key")
	assert.Equal(t, withKey, readFile(t, path))
}
