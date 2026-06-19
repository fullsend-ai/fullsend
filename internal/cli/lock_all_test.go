package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestLockAll_MutuallyExclusiveWithPositionalArg(t *testing.T) {
	cmd := newLockCmd()
	cmd.SetArgs([]string{"--fullsend-dir", t.TempDir(), "--all", "code"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--all and a positional agent name are mutually exclusive")
}

func TestLockAll_RequiresAllOrPositionalArg(t *testing.T) {
	cmd := newLockCmd()
	cmd.SetArgs([]string{"--fullsend-dir", t.TempDir()})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must specify an agent name or use --all flag")
}

func TestLockAll_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	printer := ui.New(os.Stdout)
	err := runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(dir, "lock.yaml"))
	assert.True(t, os.IsNotExist(err), "lock file should not be created for empty harness directory")
}

func TestLockAll_MultipleHarnesses(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)
	policyContent := []byte("sandbox: strict")
	policyHash := fetch.ComputeSHA256(policyContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md":        agentContent,
		"/agents/triage.md":      agentContent,
		"/policies/sandbox.yaml": policyContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	codeHarness := fmt.Sprintf(`agent: "%s/agents/code.md#sha256=%s"
policy: "%s/policies/sandbox.yaml#sha256=%s"
allowed_remote_resources:
  - "%s/"
`, srv.URL, agentHash, srv.URL, policyHash, srv.URL)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte(codeHarness),
		0o644,
	))

	triageHarness := fmt.Sprintf(`agent: "%s/agents/triage.md#sha256=%s"
allowed_remote_resources:
  - "%s/"
`, srv.URL, agentHash, srv.URL)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "triage.yaml"),
		[]byte(triageHarness),
		0o644,
	))

	orgConfig := fmt.Sprintf("allowed_remote_resources:\n  - \"%s/\"\n", srv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	err := runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer)
	require.NoError(t, err)

	lockPath := filepath.Join(dir, "lock.yaml")
	lf, err := lock.Load(lockPath)
	require.NoError(t, err)
	require.NotNil(t, lf)

	codeEntry := lf.Lookup("code")
	require.NotNil(t, codeEntry, "code harness should be locked")
	assert.Equal(t, "harness/code.yaml", codeEntry.Source)
	assert.Len(t, codeEntry.Dependencies, 2)

	triageEntry := lf.Lookup("triage")
	require.NotNil(t, triageEntry, "triage harness should be locked")
	assert.Equal(t, "harness/triage.yaml", triageEntry.Source)
	assert.Len(t, triageEntry.Dependencies, 1)
}

func TestLockAll_MixedURLAndLocalHarnesses(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md": agentContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	urlHarness := fmt.Sprintf(`agent: "%s/agents/code.md#sha256=%s"
allowed_remote_resources:
  - "%s/"
`, srv.URL, agentHash, srv.URL)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte(urlHarness),
		0o644,
	))

	localHarness := "agent: agents/local.md\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "local.yaml"),
		[]byte(localHarness),
		0o644,
	))

	orgConfig := fmt.Sprintf("allowed_remote_resources:\n  - \"%s/\"\n", srv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	err := runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer)
	require.NoError(t, err)

	lockPath := filepath.Join(dir, "lock.yaml")
	lf, err := lock.Load(lockPath)
	require.NoError(t, err)
	require.NotNil(t, lf)

	codeEntry := lf.Lookup("code")
	require.NotNil(t, codeEntry, "URL-bearing harness should be locked")
	assert.Len(t, codeEntry.Dependencies, 1)

	localEntry := lf.Lookup("local")
	assert.Nil(t, localEntry, "local-only harness should not have a lock entry")
}

func TestLockAll_ParseFailure(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "good.yaml"),
		[]byte("agent: agents/code.md\n"),
		0o644,
	))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "bad.yaml"),
		[]byte("{{invalid yaml"),
		0o644,
	))

	printer := ui.New(os.Stdout)
	err := runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad")
}

func TestLockAll_YMLExtension(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	localHarness := "agent: agents/code.md\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "review.yml"),
		[]byte(localHarness),
		0o644,
	))

	printer := ui.New(os.Stdout)
	err := runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer)
	require.NoError(t, err)

	// Verify the .yml file was discovered (no error means it was processed).
	// Since it's local-only, no lock file should be created.
	_, err = os.Stat(filepath.Join(dir, "lock.yaml"))
	assert.True(t, os.IsNotExist(err), "lock file should not be created for local-only .yml harness")
}

func TestDiscoverHarnessNames(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "code.yaml"), []byte(""), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "triage.yaml"), []byte(""), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "review.yml"), []byte(""), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "readme.txt"), []byte(""), 0o644))

	names, err := discoverHarnessNames(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"code", "review", "triage"}, names)
}

func TestDiscoverHarnessNames_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	names, err := discoverHarnessNames(dir)
	require.NoError(t, err)
	assert.Empty(t, names)
}

func TestDiscoverHarnessNames_DeduplicatesExtensions(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "code.yaml"), []byte(""), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "code.yml"), []byte(""), 0o644))

	names, err := discoverHarnessNames(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"code"}, names)
}

func TestResolveHarnessPath_PrefersYaml(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"), []byte("a"), 0o644))

	printer := ui.New(os.Stdout)
	path, err := resolveHarnessPath(dir, "code", printer)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "harness", "code.yaml"), path)
}

func TestResolveHarnessPath_FallsBackToYml(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yml"), []byte("a"), 0o644))

	printer := ui.New(os.Stdout)
	path, err := resolveHarnessPath(dir, "code", printer)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "harness", "code.yml"), path)
}

func TestResolveHarnessPath_WarnsDualExtension(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"), []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yml"), []byte("a"), 0o644))

	var buf strings.Builder
	printer := ui.New(&buf)
	path, err := resolveHarnessPath(dir, "code", printer)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "harness", "code.yaml"), path)
	assert.Contains(t, buf.String(), "Both code.yaml and code.yml exist")
}

func TestResolveHarnessPath_NeitherExtensionExists(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	printer := ui.New(os.Stdout)
	_, err := resolveHarnessPath(dir, "missing", printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harness file not found: tried missing.yaml and missing.yml")
}

func TestResolveHarnessPath_StatError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test requires non-root to trigger permission errors")
	}

	dir := t.TempDir()
	harnessDir := filepath.Join(dir, "harness")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	require.NoError(t, os.Chmod(harnessDir, 0o000))
	t.Cleanup(func() { os.Chmod(harnessDir, 0o755) })

	printer := ui.New(os.Stdout)
	_, err := resolveHarnessPath(dir, "code", printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking harness file")
}

func TestLockAll_PartialProgressOnFailure(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md": agentContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	// First harness resolves successfully.
	goodHarness := fmt.Sprintf(`agent: "%s/agents/code.md#sha256=%s"
allowed_remote_resources:
  - "%s/"
`, srv.URL, agentHash, srv.URL)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "aaa-good.yaml"),
		[]byte(goodHarness),
		0o644,
	))

	// Second harness is malformed and will fail.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "zzz-bad.yaml"),
		[]byte("{{invalid yaml"),
		0o644,
	))

	orgConfig := fmt.Sprintf("allowed_remote_resources:\n  - \"%s/\"\n", srv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	err := runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zzz-bad")

	// The good harness should have been saved despite the failure.
	lockPath := filepath.Join(dir, "lock.yaml")
	lf, loadErr := lock.Load(lockPath)
	require.NoError(t, loadErr)
	require.NotNil(t, lf, "partial lock file should have been saved")

	goodEntry := lf.Lookup("aaa-good")
	require.NotNil(t, goodEntry, "successfully resolved harness should be in partial lock file")
	assert.Len(t, goodEntry.Dependencies, 1)
}

func TestLockAll_InvalidForgeFlag(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\n"),
		0o644,
	))

	printer := ui.New(os.Stdout)
	err := runLockAll(context.Background(), dir, "invalid-forge", false, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid forge platform")
}

func TestRunLock_InvalidForgeFlag(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\n"),
		0o644,
	))

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "code", dir, "invalid-forge", false, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid forge platform")
}

func TestLockOneAgent_YMLFallback(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	// Only .yml extension, no .yaml.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "review.yml"),
		[]byte("agent: agents/review.md\n"),
		0o644,
	))

	printer := ui.New(os.Stdout)
	result, err := lockOneAgent(context.Background(), "review", dir, "", false, nil, resolveFlags{}, printer)
	require.NoError(t, err)
	// Local-only harness returns nil (no deps to lock).
	assert.Nil(t, result)
}

func TestLockOneAgent_StalenessCheck(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessContent := []byte("agent: agents/code.md\n")
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		harnessContent,
		0o644,
	))

	// Pre-populate lock file with a current entry.
	harnessHash := fetch.ComputeSHA256(harnessContent)
	lf := &lock.LockFile{
		Version: 1,
		Harnesses: map[string]lock.HarnessLock{
			"code": {
				Source: "harness/code.yaml",
				SHA256: harnessHash,
				Dependencies: []lock.DependencyEntry{
					{Field: "agent", URL: "https://example.com/agent.md", SHA256: "abc"},
				},
			},
		},
	}

	printer := ui.New(os.Stdout)
	result, err := lockOneAgent(context.Background(), "code", dir, "", false, lf, resolveFlags{}, printer)
	require.NoError(t, err)
	assert.Nil(t, result, "should return nil when lock entry is up to date")
}

func TestLockOneAgent_DualExtensionWarning(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	// Create both .yaml and .yml for the same stem.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yml"),
		[]byte("agent: agents/code.md\n"),
		0o644,
	))

	var buf strings.Builder
	printer := ui.New(&buf)
	result, err := lockOneAgent(context.Background(), "code", dir, "", false, nil, resolveFlags{}, printer)
	require.NoError(t, err)
	assert.Nil(t, result, "local-only harness should return nil")
	assert.Contains(t, buf.String(), "Both code.yaml and code.yml exist")
}

func TestLockAll_CorruptLockFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\n"),
		0o644,
	))

	// Write a corrupt lock file.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "lock.yaml"),
		[]byte("{{corrupt yaml"),
		0o644,
	))

	printer := ui.New(os.Stdout)
	err := runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer)
	// Should not error — corrupt lock file is handled gracefully (reset to nil).
	require.NoError(t, err)
}

func TestLockAll_CobraDispatch(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\n"),
		0o644,
	))

	cmd := newLockCmd()
	cmd.SetArgs([]string{"--fullsend-dir", dir, "--all"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestLockCmd_SingleAgentCobraDispatch(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\n"),
		0o644,
	))

	cmd := newLockCmd()
	cmd.SetArgs([]string{"--fullsend-dir", dir, "code"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestLockOneAgent_AllowlistViolation(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md": agentContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessYAML := fmt.Sprintf(`agent: "%s/agents/code.md#sha256=%s"
allowed_remote_resources:
  - "%s/"
`, srv.URL, agentHash, srv.URL)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte(harnessYAML),
		0o644,
	))

	// Org config with a DIFFERENT allowlist that does NOT include the test server.
	orgConfig := "allowed_remote_resources:\n  - \"https://trusted.example.com/\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	_, err := lockOneAgent(context.Background(), "code", dir, "", false, nil, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowed remote resources")
}

func TestLockOneAgent_NonexistentHarness(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	printer := ui.New(os.Stdout)
	_, err := lockOneAgent(context.Background(), "nonexistent", dir, "", false, nil, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harness file not found: tried nonexistent.yaml and nonexistent.yml")
}

func TestLockOneAgent_StatError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test requires non-root to trigger permission errors")
	}

	dir := t.TempDir()
	harnessDir := filepath.Join(dir, "harness")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))

	// Remove execute permission so stat on any child fails with EPERM.
	require.NoError(t, os.Chmod(harnessDir, 0o000))
	t.Cleanup(func() { os.Chmod(harnessDir, 0o755) })

	printer := ui.New(os.Stdout)
	_, err := lockOneAgent(context.Background(), "code", dir, "", false, nil, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking harness file")
}

func TestRunLock_SaveError(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md": agentContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessYAML := fmt.Sprintf(`agent: "%s/agents/code.md#sha256=%s"
allowed_remote_resources:
  - "%s/"
`, srv.URL, agentHash, srv.URL)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte(harnessYAML),
		0o644,
	))

	orgConfig := fmt.Sprintf("allowed_remote_resources:\n  - \"%s/\"\n", srv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	// Create lock.yaml as a directory so Save fails.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "lock.yaml"), 0o755))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "code", dir, "", false, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "saving lock file")
}

func TestLockAll_SaveError(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md": agentContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessYAML := fmt.Sprintf(`agent: "%s/agents/code.md#sha256=%s"
allowed_remote_resources:
  - "%s/"
`, srv.URL, agentHash, srv.URL)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte(harnessYAML),
		0o644,
	))

	orgConfig := fmt.Sprintf("allowed_remote_resources:\n  - \"%s/\"\n", srv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	// Create lock.yaml as a directory so Save fails.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "lock.yaml"), 0o755))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	err := runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "saving lock file")
}

func TestLockAll_WithUpdateFlag(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md": agentContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessYAML := fmt.Sprintf(`agent: "%s/agents/code.md#sha256=%s"
allowed_remote_resources:
  - "%s/"
`, srv.URL, agentHash, srv.URL)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte(harnessYAML),
		0o644,
	))

	orgConfig := fmt.Sprintf("allowed_remote_resources:\n  - \"%s/\"\n", srv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)

	// First lock.
	require.NoError(t, runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer))

	lf1, _ := lock.Load(filepath.Join(dir, "lock.yaml"))
	entry1 := lf1.Lookup("code")
	require.NotNil(t, entry1)
	resolvedAt1 := entry1.ResolvedAt

	// Second lock with update=true should re-resolve.
	require.NoError(t, runLockAll(context.Background(), dir, "", true, resolveFlags{}, printer))

	lf2, _ := lock.Load(filepath.Join(dir, "lock.yaml"))
	entry2 := lf2.Lookup("code")
	require.NotNil(t, entry2)
	assert.True(t, entry2.ResolvedAt.After(resolvedAt1) || entry2.ResolvedAt.Equal(resolvedAt1))
}

func TestLockAll_AllUpToDateMessage(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md": agentContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessYAML := fmt.Sprintf(`agent: "%s/agents/code.md#sha256=%s"
allowed_remote_resources:
  - "%s/"
`, srv.URL, agentHash, srv.URL)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte(harnessYAML),
		0o644,
	))

	orgConfig := fmt.Sprintf("allowed_remote_resources:\n  - \"%s/\"\n", srv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	// First lock populates the lock file.
	printer := ui.New(os.Stdout)
	require.NoError(t, runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer))

	// Second lock — everything is up to date.
	var buf strings.Builder
	printer2 := ui.New(&buf)
	require.NoError(t, runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer2))
	assert.Contains(t, buf.String(), "already up to date")
}

func TestLockAll_PrunesStaleEntry(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md": agentContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessYAML := fmt.Sprintf(`agent: "%s/agents/code.md#sha256=%s"
allowed_remote_resources:
  - "%s/"
`, srv.URL, agentHash, srv.URL)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte(harnessYAML),
		0o644,
	))

	orgConfig := fmt.Sprintf("allowed_remote_resources:\n  - \"%s/\"\n", srv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)

	// First lock — creates entry for "code".
	require.NoError(t, runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer))
	lf1, err := lock.Load(filepath.Join(dir, "lock.yaml"))
	require.NoError(t, err)
	require.NotNil(t, lf1.Lookup("code"))

	// Replace the harness with a local-only version (no remote deps).
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/local.md\n"),
		0o644,
	))

	// Second lock — should prune the stale "code" entry.
	var buf strings.Builder
	printer2 := ui.New(&buf)
	require.NoError(t, runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer2))
	assert.Contains(t, buf.String(), "Pruned stale lock entry")

	lf2, err := lock.Load(filepath.Join(dir, "lock.yaml"))
	require.NoError(t, err)
	assert.Nil(t, lf2.Lookup("code"), "stale lock entry should have been pruned")
}

func TestLockAll_PrunesRemovedHarness(t *testing.T) {
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md": agentContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	harnessYAML := fmt.Sprintf(`agent: "%s/agents/code.md#sha256=%s"
allowed_remote_resources:
  - "%s/"
`, srv.URL, agentHash, srv.URL)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte(harnessYAML),
		0o644,
	))

	orgConfig := fmt.Sprintf("allowed_remote_resources:\n  - \"%s/\"\n", srv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)

	// First lock — creates entry for "code".
	require.NoError(t, runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer))
	lf1, err := lock.Load(filepath.Join(dir, "lock.yaml"))
	require.NoError(t, err)
	require.NotNil(t, lf1.Lookup("code"))

	// Delete the harness file.
	require.NoError(t, os.Remove(filepath.Join(dir, "harness", "code.yaml")))

	// Add a different local-only harness so --all has something to iterate.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "local.yaml"),
		[]byte("agent: agents/local.md\n"),
		0o644,
	))

	// Second lock — should prune the removed "code" entry.
	var buf strings.Builder
	printer2 := ui.New(&buf)
	require.NoError(t, runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer2))
	assert.Contains(t, buf.String(), "Pruned lock entry for removed harness")

	lf2, err := lock.Load(filepath.Join(dir, "lock.yaml"))
	require.NoError(t, err)
	assert.Nil(t, lf2.Lookup("code"), "removed harness should have been pruned from lock file")
}
