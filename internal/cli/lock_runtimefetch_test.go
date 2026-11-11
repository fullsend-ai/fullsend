package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// A fetch-only harness (allow_runtime_fetch with no URL references) is held
// to the config-level subset check like a URL-referencing one (#9291).
func TestRunLock_FetchOnlyHarnessNoOrgConfigErrors(t *testing.T) {
	// Without config.yaml there is no config-level allowlist to validate
	// against, so lock fails closed exactly as it does for a harness with
	// URL references (config.DefaultAllowedRemoteResources is a resolution
	// fallback, not a validation ceiling), and the error names the
	// fetch-capable path, not URL references.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessContent := `agent: agents/code.md
role: test
allow_runtime_fetch: true
allowed_remote_resources:
  - "https://example.com/"
`
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "local.yaml"),
		[]byte(harnessContent),
		0o644,
	))

	// Deliberately do NOT create config.yaml.

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "local", dir, "", false, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runtime fetch requires a config.yaml with allowed_remote_resources")
	assert.Contains(t, err.Error(), "config.yaml")
}

func TestRunLock_BareDeclarationRemoteResourcesNotSubset(t *testing.T) {
	// A bare allowed_remote_resources declaration (no allow_runtime_fetch,
	// no URL references) still starts the runtime fetch service for
	// backward compatibility, so the deprecated shape is held to the same
	// config-level subset check too (#9291): the gate is
	// shouldStartFetchService, not just the explicit opt-in.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessContent := `agent: agents/code.md
role: test
allowed_remote_resources:
  - "https://disallowed.example.com/"
`
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "local.yaml"),
		[]byte(harnessContent),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("version: \"1\"\nallowed_remote_resources:\n  - \"https://allowed.example.com/\"\n"),
		0o644,
	))

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "local", dir, "", false, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validating allowed remote resources")
	assert.Contains(t, err.Error(), `"https://disallowed.example.com/" is not covered by the org allowlist`)
}

func TestRunLock_FetchOnlyHarnessCoveredListPasses(t *testing.T) {
	// A fetch-only harness whose list the config-level allowlist covers
	// still locks cleanly: no remote dependencies, so no lock file.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessContent := `agent: agents/code.md
role: test
allow_runtime_fetch: true
allowed_remote_resources:
  - "https://allowed.example.com/"
`
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "local.yaml"),
		[]byte(harnessContent),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("version: \"1\"\nallowed_remote_resources:\n  - \"https://allowed.example.com/\"\n"),
		0o644,
	))

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "local", dir, "", false, resolveFlags{}, printer)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(dir, "lock.yaml"))
	assert.True(t, os.IsNotExist(err), "lock file should not be created for a fetch-only local harness")
}

func TestRunLock_UpToDateEntryStillValidatesRemoteResources(t *testing.T) {
	// A fetch-capable harness whose lock entry is current returns before
	// the compose loop, so only the raw-declaration gate validates its
	// allowed_remote_resources. Lock with a config that covers the list,
	// then narrow config.yaml (harness and entry unchanged): the second
	// lock without --update must fail naming the uncovered entry instead
	// of reporting the entry up to date (#9291).
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md": agentContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	fetchOnlyPrefix := "https://fetch-only.example.com/"
	harnessContent := fmt.Sprintf(`agent: "%s/agents/code.md#sha256=%s"
role: test
allow_runtime_fetch: true
allowed_remote_resources:
  - "%s/"
  - "%s"
`, srv.URL, agentHash, srv.URL, fetchOnlyPrefix)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "local.yaml"),
		[]byte(harnessContent),
		0o644,
	))

	writeConfig := func(prefixes ...string) {
		content := "allowed_remote_resources:\n"
		for _, p := range prefixes {
			content += fmt.Sprintf("  - %q\n", p)
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o644))
	}
	writeConfig(srv.URL+"/", fetchOnlyPrefix)

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)

	// First lock creates the entry and records the harness hash.
	require.NoError(t, runLock(context.Background(), "local", dir, "", false, resolveFlags{}, printer))
	_, err := os.Stat(filepath.Join(dir, "lock.yaml"))
	require.NoError(t, err)

	// Narrow the config-level allowlist; the harness is unchanged, so the
	// entry is still current.
	writeConfig(srv.URL + "/")

	err = runLock(context.Background(), "local", dir, "", false, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validating allowed remote resources")
	assert.Contains(t, err.Error(), fmt.Sprintf("%q is not covered by the org allowlist", fetchOnlyPrefix))
	assert.NotContains(t, err.Error(), "up to date")
}

func TestRunLock_FetchOnlyHarnessMalformedOrgConfigErrors(t *testing.T) {
	// A config.yaml that exists but cannot be parsed is reported with its
	// load error, never as the fetch gate's missing-file message: only an
	// absent config.yaml is "runtime fetch requires"
	// (TestRunLock_FetchOnlyHarnessNoOrgConfigErrors).
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessContent := `agent: agents/code.md
role: test
allow_runtime_fetch: true
allowed_remote_resources:
  - "https://example.com/"
`
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "local.yaml"),
		[]byte(harnessContent),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("{{invalid yaml"),
		0o644,
	))

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "local", dir, "", false, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading fullsend config")
	assert.NotContains(t, err.Error(), "runtime fetch requires")
}

func TestRunLock_URLDirFetchCapableNoOrgConfigError(t *testing.T) {
	// The raw-declaration gate's URL branch: a harness that is both
	// fetch-capable and URL-referencing (a URL-sourced skill directory)
	// with no config.yaml fails with the URL-referenced message, not the
	// fetch-only one — each branch of the gate names its own requirement
	// (#9291).
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessContent := `agent: agents/code.md
role: test
skills:
  - "https://example.com/skills/helper/"
`
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "urldir.yaml"),
		[]byte(harnessContent),
		0o644,
	))

	// Deliberately do NOT create config.yaml.

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "urldir", dir, "", false, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "URL-referenced resources require a config.yaml")
	assert.NotContains(t, err.Error(), "runtime fetch requires")
}
