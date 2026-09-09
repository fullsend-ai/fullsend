package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

// writeModelsAliasesConfig replaces the fixture's config.yaml — the file
// loadRunConfig reads — with one that carries a models.aliases block.
func writeModelsAliasesConfig(t *testing.T, dir, aliasesYAML string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("agents:\n  - harness/code.yaml\nmodels:\n  aliases:\n"+aliasesYAML), 0o644))
}

// The load path does not run Validate, so runAgent checks the effective
// alias map itself: an unknown key must fail before the sandbox is
// created, not become a working alias (#6882).
func TestRunAgent_ModelsAliases_UnknownKeyFailsBeforeSandbox(t *testing.T) {
	usePreScriptStub(t)
	dir := newSkipHarnessDir(t, "")
	writeModelsAliasesConfig(t, dir, "    grok: grok-4.6\n")

	var buf bytes.Buffer
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(&buf), false, runOverrideFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown alias key")
	assert.Contains(t, err.Error(), `"grok"`)
	assert.NotContains(t, err.Error(), "creating sandbox", "must fail before the sandbox is created")
	assert.NotContains(t, buf.String(), "creating sandbox")
}

// A remapped alias is echoed with both its own source and the config
// remap, and the run reaches sandbox creation (the runtime applies the
// resolved id at Run; that is covered in claude_test.go / pi_run_test.go).
func TestRunAgent_ModelsAliases_RemapIsEchoedAndReachesSandbox(t *testing.T) {
	usePreScriptStub(t)
	dir := newSkipHarnessDir(t, "")
	writeModelsAliasesConfig(t, dir, "    sonnet: claude-sonnet-5\n")

	var buf bytes.Buffer
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(&buf), false, runOverrideFlags{model: "sonnet"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating sandbox", "validation passed; the run reached sandbox creation")

	out := buf.String()
	// KeyValue styles only the key, so the value is a stable substring:
	// the alias name and its own source come first, then the remap.
	assert.Contains(t, out, "sonnet (from --model flag) → claude-sonnet-5 (from "+filepath.Join(dir, "config.yaml")+" models.aliases)",
		"plan block keeps the alias and its source, then shows the remap")
}

// The plan block is a human echo of the fallback chain: an aliased entry
// is shown as "alias → id" (buildRunCommand passes only the id on
// --fallback-model; see claude_test.go); a literal id stays as written.
func TestRunAgent_ModelsAliases_FallbackChainEchoedRemapped(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv("FULLSEND_FALLBACK_MODELS", "sonnet,claude-opus-4-6")
	dir := newSkipHarnessDir(t, "")
	writeModelsAliasesConfig(t, dir, "    sonnet: claude-sonnet-5\n")

	var buf bytes.Buffer
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(&buf), false, runOverrideFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating sandbox")
	assert.Contains(t, buf.String(), "sonnet → claude-sonnet-5, claude-opus-4-6 (from FULLSEND_FALLBACK_MODELS)",
		"aliased fallback entry is shown remapped, literal id as written")
}

// An alias that is not remapped keeps the pre-#6882 plan line exactly.
func TestRunAgent_ModelsAliases_UnrelatedAliasUnchanged(t *testing.T) {
	usePreScriptStub(t)
	dir := newSkipHarnessDir(t, "")
	writeModelsAliasesConfig(t, dir, "    sonnet: claude-sonnet-5\n")

	var buf bytes.Buffer
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(&buf), false, runOverrideFlags{model: "opus"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating sandbox")
	assert.NotContains(t, buf.String(), "opus →", "no remap for an alias without an entry")
	assert.NotContains(t, buf.String(), "models.aliases")
}
