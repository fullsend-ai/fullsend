package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// commentedAgentConfig is a hand-maintained config: comments, 4-space
// nesting, quoted scalars, and top-level keys out of Marshal's order.
// The agent commands must leave every line they do not change intact.
const commentedAgentConfig = `# Per-repo fullsend config. Keep the rationale comments.
mint_url: https://mint.example.com
version: "1"
roles:
    - "triage"   # dispatched by the poller
    - "coder"

# review runs on opus directly, not through an alias.
agents:
    - name: review
      model: opus
      subagents:
          correctness: opus   # catches the most
          style-conventions: haiku

# Kept last on purpose.
runtime: claude
`

func writeAgentConfigFixture(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "lint.yaml"), []byte("role: coder\n"), 0o644))
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func TestAgentCommands_PreserveConfigLayout(t *testing.T) {
	dir := t.TempDir()
	path := writeAgentConfigFixture(t, dir, commentedAgentConfig)
	var out bytes.Buffer
	printer := ui.New(&out)

	// add appends just the new entry.
	require.NoError(t, runAgentAdd(context.Background(), "harness/lint.yaml", "lint", dir, nil, printer))
	added := strings.Replace(commentedAgentConfig, "          style-conventions: haiku\n",
		"          style-conventions: haiku\n    - name: lint\n      source: harness/lint.yaml\n", 1)
	assert.Equal(t, added, readFile(t, path))

	// set edits one existing entry field by field.
	require.NoError(t, runAgentSet(dir, "review", agentSetFlags{
		model: "sonnet", modelSet: true,
		subagentSet: true, subagentArgs: []string{"default=haiku"},
	}, printer))
	set := strings.Replace(added, "      model: opus\n", "      model: sonnet\n", 1)
	set = strings.Replace(set, "          style-conventions: haiku\n",
		"          style-conventions: haiku\n          default: haiku\n", 1)
	assert.Equal(t, set, readFile(t, path))

	// remove takes out only that entry, restoring the rest byte for byte.
	require.NoError(t, runAgentRemove(dir, "lint", printer))
	removed := strings.Replace(set, "    - name: lint\n      source: harness/lint.yaml\n", "", 1)
	assert.Equal(t, removed, readFile(t, path))
}

func TestRunAgentAdd_URLPreservesConfigLayout(t *testing.T) {
	harnessContent := []byte("role: triage\n")
	srv, policy := newAgentTestServer(t, map[string][]byte{
		"/my-org/my-agents/" + testCommitSHA + "/harness/triage.yaml": harnessContent,
	})
	origPolicy := fetch.DefaultPolicy
	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = origPolicy }()

	dir := t.TempDir()
	path := writeAgentConfigFixture(t, dir, commentedAgentConfig)

	source := srv.URL + "/my-org/my-agents/" + testCommitSHA + "/harness/triage.yaml"
	require.NoError(t, runAgentAdd(context.Background(), source, "", dir, forge.NewFakeClient(), ui.New(&bytes.Buffer{})))

	cfg, err := loadAgentConfig(path)
	require.NoError(t, err)
	entry, ok := config.AgentSettingsFor(cfg.AgentEntries(), "triage")
	require.True(t, ok)
	require.Empty(t, entry.Ref)

	// Only the entry and the one prefix it needs are added; the default
	// prefixes stay inherited rather than being copied into the file.
	want := strings.Replace(commentedAgentConfig, "          style-conventions: haiku\n",
		"          style-conventions: haiku\n    - source: "+entry.Source+"\n", 1)
	want = strings.Replace(want, "\n# Kept last on purpose.\n",
		"allowed_remote_resources:\n    - "+srv.URL+"/my-org/my-agents/\n\n# Kept last on purpose.\n", 1)
	assert.Equal(t, want, readFile(t, path))
	assert.Contains(t, cfg.AllowedResources(), srv.URL+"/my-org/my-agents/")
	for _, d := range config.DefaultAllowedRemoteResources() {
		assert.Contains(t, cfg.AllowedResources(), d, "inherited default prefixes still apply")
	}
}

func TestRunAgentUpdate_PreservesConfigLayout(t *testing.T) {
	newSHA := "b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3"
	newContent := []byte("role: triage\nupdated: true\n")
	srv, policy := newAgentTestServer(t, map[string][]byte{
		"/org/repo/" + newSHA + "/harness/triage.yaml": newContent,
	})
	origPolicy := fetch.DefaultPolicy
	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = origPolicy }()

	oldSource := srv.URL + "/org/repo/" + testCommitSHA + "/harness/triage.yaml#sha256=" + strings.Repeat("1", 64)
	orig := strings.Replace(commentedAgentConfig, "          style-conventions: haiku\n",
		"          style-conventions: haiku\n    # triage pinned after the v2 harness change.\n    - \""+oldSource+"\"\n", 1) +
		"allowed_remote_resources:\n    - \"" + srv.URL + "/org/repo/\"   # our fork\n"

	dir := t.TempDir()
	path := writeAgentConfigFixture(t, dir, orig)
	require.NoError(t, runAgentUpdate(context.Background(), "triage", newSHA, dir, nil, ui.New(&bytes.Buffer{})))

	cfg, err := loadAgentConfig(path)
	require.NoError(t, err)
	entry, ok := config.AgentSettingsFor(cfg.AgentEntries(), "triage")
	require.True(t, ok)
	assert.Contains(t, entry.Source, newSHA)
	assert.Contains(t, entry.Source, "#sha256="+fetch.ComputeSHA256(newContent))

	// The entry keeps its plain-string form and its quotes.
	assert.Equal(t, strings.Replace(orig, `"`+oldSource+`"`, `"`+entry.Source+`"`, 1), readFile(t, path))
}

func TestRunAgentUpdate_AfterURLAddPreservesConfigLayout(t *testing.T) {
	newSHA := "b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3"
	newContent := []byte("role: triage\nupdated: true\n")
	srv, policy := newAgentTestServer(t, map[string][]byte{
		"/my-org/my-agents/" + testCommitSHA + "/harness/triage.yaml": []byte("role: triage\n"),
		"/my-org/my-agents/" + newSHA + "/harness/triage.yaml":        newContent,
	})
	origPolicy := fetch.DefaultPolicy
	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = origPolicy }()

	dir := t.TempDir()
	path := writeAgentConfigFixture(t, dir, commentedAgentConfig)
	printer := ui.New(&bytes.Buffer{})

	// add writes the unnamed entry as "- source: <url>".
	source := srv.URL + "/my-org/my-agents/" + testCommitSHA + "/harness/triage.yaml"
	require.NoError(t, runAgentAdd(context.Background(), source, "", dir, forge.NewFakeClient(), printer))
	added := readFile(t, path)
	require.Contains(t, added, "    - source: ")

	// update changes that first pair; the dash, comments and every other
	// line must survive rather than falling back to a full rewrite.
	require.NoError(t, runAgentUpdate(context.Background(), "triage", newSHA, dir, nil, printer))
	cfg, err := loadAgentConfig(path)
	require.NoError(t, err)
	entry, ok := config.AgentSettingsFor(cfg.AgentEntries(), "triage")
	require.True(t, ok)
	assert.Contains(t, entry.Source, newSHA)

	var oldLine string
	for _, l := range strings.Split(added, "\n") {
		if strings.HasPrefix(l, "    - source: ") {
			oldLine = l
		}
	}
	require.NotEmpty(t, oldLine)
	assert.Equal(t, strings.Replace(added, oldLine, "    - source: "+entry.Source, 1), readFile(t, path))
}

func TestRunAgentRemove_DropsEntryCommentAndPrefix(t *testing.T) {
	hash := strings.Repeat("4", 64)
	source := "https://raw.githubusercontent.com/org/repo/" + testCommitSHA + "/harness/triage.yaml#sha256=" + hash
	orig := strings.Replace(commentedAgentConfig, "          style-conventions: haiku\n",
		"          style-conventions: haiku\n    # triage from our fork.\n    - \""+source+"\"\n", 1) +
		"allowed_remote_resources:\n    - \"https://raw.githubusercontent.com/org/repo/\"\n"

	dir := t.TempDir()
	path := writeAgentConfigFixture(t, dir, orig)
	require.NoError(t, runAgentRemove(dir, "triage", ui.New(&bytes.Buffer{})))

	// The last local prefix goes with the key, so the defaults stay
	// inherited instead of being opted out of with an empty list.
	assert.Equal(t, commentedAgentConfig, readFile(t, path))
	cfg, err := loadAgentConfig(path)
	require.NoError(t, err)
	assert.Equal(t, config.DefaultAllowedRemoteResources(), cfg.AllowedResources())
}

func TestWriteAgentConfig_NoExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := config.NewPerRepoConfig(nil, "")
	require.NoError(t, writeAgentConfig(path, cfg))
	want, err := cfg.Marshal()
	require.NoError(t, err)
	assert.Equal(t, string(want), readFile(t, path))
}

func TestWriteAgentConfig_ReadError(t *testing.T) {
	dir := t.TempDir()
	// A directory where the file should be cannot be read as a file.
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.Mkdir(path, 0o755))
	err := writeAgentConfig(path, config.NewPerRepoConfig(nil, ""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}
