package cli

import (
	"context"
	"fmt"
	"net/http"
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
role: coder
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
role: triage
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
role: test
allowed_remote_resources:
  - "%s/"
`, srv.URL, agentHash, srv.URL)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte(urlHarness),
		0o644,
	))

	localHarness := "agent: agents/local.md\nrole: test\n"
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
		[]byte("agent: agents/code.md\nrole: test\n"),
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

	localHarness := "agent: agents/code.md\nrole: test\n"
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
role: test
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
		[]byte("agent: agents/code.md\nrole: test\n"),
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
		[]byte("agent: agents/code.md\nrole: test\n"),
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
		[]byte("agent: agents/review.md\nrole: test\n"),
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

	harnessContent := []byte("agent: agents/code.md\nrole: test\n")
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
		[]byte("agent: agents/code.md\nrole: test\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yml"),
		[]byte("agent: agents/code.md\nrole: test\n"),
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
		[]byte("agent: agents/code.md\nrole: test\n"),
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
		[]byte("agent: agents/code.md\nrole: test\n"),
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
		[]byte("agent: agents/code.md\nrole: test\n"),
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
role: test
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
	assert.Contains(t, err.Error(), "harness file not found")
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
role: test
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
role: test
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
role: test
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
role: test
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
role: test
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
		[]byte("agent: agents/local.md\nrole: test\n"),
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
role: test
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
		[]byte("agent: agents/local.md\nrole: test\n"),
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

func TestResolveHarnessForLock_PrefersLocalPath(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: test\n"),
		0o644,
	))

	printer := ui.New(os.Stdout)
	path, deps, err := resolveHarnessForLock(
		context.Background(), dir, "code", nil, resolveFlags{}, fetch.FetchPolicy{}, printer)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "harness", "code.yaml"), path)
	assert.Nil(t, deps, "no agent source deps for local harness")
}

func TestResolveHarnessForLock_FallsBackToConfigLocalPath(t *testing.T) {
	dir := t.TempDir()
	// No harness/ directory — agent comes from config with a local path.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "custom"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "custom", "myagent.yaml"),
		[]byte("agent: agents/code.md\nrole: test\n"),
		0o644,
	))

	orgConfig := "agents:\n  - source: custom/myagent.yaml\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	orgCfg := tryLoadOrgConfig(filepath.Join(dir, "config.yaml"), ui.New(os.Stdout))
	require.NotNil(t, orgCfg)

	printer := ui.New(os.Stdout)
	path, deps, err := resolveHarnessForLock(
		context.Background(), dir, "myagent", orgCfg, resolveFlags{}, fetch.FetchPolicy{}, printer)
	require.NoError(t, err)
	assert.Contains(t, path, "myagent.yaml")
	assert.Nil(t, deps, "no agent source deps for local config path")
}

func TestResolveHarnessForLock_FallsBackToConfigURL(t *testing.T) {
	harnessContent := []byte("agent: agents/code.md\nrole: test\n")
	harnessHash := fetch.ComputeSHA256(harnessContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/harness/code.yaml": harnessContent,
	})

	dir := t.TempDir()
	// No local harness/ directory — agent comes from config URL.

	harnessURL := fmt.Sprintf("%s/harness/code.yaml#sha256=%s", srv.URL, harnessHash)
	orgConfig := fmt.Sprintf("agents:\n  - name: code\n    source: \"%s\"\nallowed_remote_resources:\n  - \"%s/\"\n", harnessURL, srv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	orgCfg := tryLoadOrgConfig(filepath.Join(dir, "config.yaml"), ui.New(os.Stdout))
	require.NotNil(t, orgCfg)

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	path, deps, err := resolveHarnessForLock(
		context.Background(), dir, "code", orgCfg, resolveFlags{}, policy, printer)
	require.NoError(t, err)
	assert.NotEmpty(t, path)
	require.Len(t, deps, 1, "URL-sourced agent should have one agent_source dep")
	assert.Equal(t, "agent_source", deps[0].Field)
}

func TestResolveHarnessForLock_NoConfigNoLocal(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	printer := ui.New(os.Stdout)
	_, _, err := resolveHarnessForLock(
		context.Background(), dir, "missing", nil, resolveFlags{}, fetch.FetchPolicy{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no config.yaml for fallback")
}

func TestResolveHarnessForLock_AgentNotInConfig(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	orgConfig := "agents:\n  - source: harness/other.yaml\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	orgCfg := tryLoadOrgConfig(filepath.Join(dir, "config.yaml"), ui.New(os.Stdout))
	require.NotNil(t, orgCfg)

	printer := ui.New(os.Stdout)
	_, _, err := resolveHarnessForLock(
		context.Background(), dir, "missing", orgCfg, resolveFlags{}, fetch.FetchPolicy{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in local harness directory or config")
}

func TestResolveHarnessForLock_DisabledAgent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	orgConfig := "agents:\n  - name: code\n    source: harness/code.yaml\n    enabled: false\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	orgCfg := tryLoadOrgConfig(filepath.Join(dir, "config.yaml"), ui.New(os.Stdout))
	require.NotNil(t, orgCfg)

	printer := ui.New(os.Stdout)
	_, _, err := resolveHarnessForLock(
		context.Background(), dir, "code", orgCfg, resolveFlags{}, fetch.FetchPolicy{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "explicitly disabled")
}

func TestLockOneAgent_ConfigFallback(t *testing.T) {
	// Agent only exists in config (no local harness file).
	// lockOneAgent should resolve it from config and lock its deps.
	agentContent := []byte("You are a coding agent.")
	agentHash := fetch.ComputeSHA256(agentContent)

	// Create the server first, then build harness content referencing its URL.
	srv, policy := newLockTestServer(t, nil)
	harnessYAML := fmt.Sprintf("agent: \"%s/agents/code.md#sha256=%s\"\nrole: test\nallowed_remote_resources:\n  - \"%s/\"\n", srv.URL, agentHash, srv.URL)
	harnessHash := fetch.ComputeSHA256([]byte(harnessYAML))

	// Replace the server handler to serve both the harness and agent.
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agents/code.md":
			w.Write(agentContent)
		case "/harness/code.yaml":
			w.Write([]byte(harnessYAML))
		default:
			http.NotFound(w, r)
		}
	})

	dir := t.TempDir()
	// No local harness/ directory — agent comes from config URL.
	harnessURL := fmt.Sprintf("%s/harness/code.yaml#sha256=%s", srv.URL, harnessHash)
	orgConfig := fmt.Sprintf("agents:\n  - name: code\n    source: \"%s\"\nallowed_remote_resources:\n  - \"%s/\"\n", harnessURL, srv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	printer := ui.New(os.Stdout)
	result, err := lockOneAgent(context.Background(), "code", dir, "", false, nil, resolveFlags{}, printer)
	require.NoError(t, err)
	require.NotNil(t, result, "config-resolved agent should have dependencies to lock")
	assert.NotEmpty(t, result.deps, "should have at least one dependency")

	// Source should use the agent name, not the cache-internal basename.
	assert.Equal(t, filepath.Join("harness", "code.yaml"), result.harnessLock.Source,
		"URL-resolved agent should have a readable Source, not a cache-internal path")

	// The agent_source dep should exist in the lock deps.
	var hasAgentSource bool
	for _, dep := range result.harnessLock.Dependencies {
		if dep.Field == "agent_source" {
			hasAgentSource = true
			break
		}
	}
	assert.True(t, hasAgentSource, "URL-resolved agent should have an agent_source dependency entry")
}

func TestLockAll_IncludesConfigAgents(t *testing.T) {
	// Local harness directory has "local" agent.
	// Config has "configonly" agent (local path).
	// --all should discover both.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "custom"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "local.yaml"),
		[]byte("agent: agents/local.md\nrole: test\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "custom", "configonly.yaml"),
		[]byte("agent: agents/configonly.md\nrole: test\n"),
		0o644,
	))

	orgConfig := "agents:\n  - source: custom/configonly.yaml\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	var buf strings.Builder
	printer := ui.New(&buf)
	err := runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "configonly", "should process config-only agent")
	assert.Contains(t, output, "local", "should process local agent")
}

func TestLockAll_NoLocalHarnessesButHasConfigAgents(t *testing.T) {
	// No harness/ directory at all, but config has agents.
	// --all should still discover and process config agents.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "custom"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "custom", "remote.yaml"),
		[]byte("agent: agents/remote.md\nrole: test\n"),
		0o644,
	))

	orgConfig := "agents:\n  - source: custom/remote.yaml\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(orgConfig), 0o644))

	var buf strings.Builder
	printer := ui.New(&buf)
	err := runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "remote", "should discover config-only agent when no local harness dir exists")
}

func TestLockAll_RejectedConfigDoesNotPruneConfigOnlyEntry(t *testing.T) {
	// An org-shaped (rejected) config.yaml must fail the run instead of being
	// treated as absent, which would prune the lock entry of an agent that is
	// registered only through that config.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "local.yaml"),
		[]byte("agent: agents/local.md\nrole: test\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("defaults:\n  roles:\n    - coder\nrepos: {}\n"),
		0o644,
	))

	lockPath := filepath.Join(dir, "lock.yaml")
	require.NoError(t, lock.Save(lockPath, &lock.LockFile{
		Harnesses: map[string]lock.HarnessLock{
			"configonly": {Source: "custom/configonly.yaml", SHA256: "abc123"},
		},
	}))
	before, err := os.ReadFile(lockPath)
	require.NoError(t, err)

	printer := ui.New(os.Stdout)
	err = runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "per-org configuration format")

	after, err := os.ReadFile(lockPath)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "lock file must be unchanged when config is rejected")
}

func TestRunLock_RejectedConfigFails(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "local.yaml"),
		[]byte("agent: agents/local.md\nrole: test\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("defaults:\n  roles:\n    - coder\nrepos: {}\n"),
		0o644,
	))

	printer := ui.New(os.Stdout)
	err := runLock(context.Background(), "local", dir, "", false, resolveFlags{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "per-org configuration format")
}

func TestLoadLockConfig_BaseLayer(t *testing.T) {
	t.Run("neither layer is absent", func(t *testing.T) {
		dir := t.TempDir()
		cfg, err := loadLockConfig(filepath.Join(dir, "config.yaml"))
		require.NoError(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("base-only deny-all allowlist is preserved", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, "config.base.yaml"),
			[]byte("allowed_remote_resources: []\n"),
			0o644,
		))
		cfg, err := loadLockConfig(filepath.Join(dir, "config.yaml"))
		require.NoError(t, err)
		require.NotNil(t, cfg)
		assert.Empty(t, cfg.AllowedResources())
	})

	t.Run("malformed base-only config fails", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, "config.base.yaml"),
			[]byte("{{invalid yaml"),
			0o644,
		))
		cfg, err := loadLockConfig(filepath.Join(dir, "config.yaml"))
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), "parsing base config")
	})

	t.Run("malformed base with valid overlay fails", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("roles: [coder]\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte("{{invalid yaml"), 0o644))
		_, err := loadLockConfig(filepath.Join(dir, "config.yaml"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing base config")
	})
}

func TestRunLock_BaseOnlyAllowlistGovernsRemoteAncestor(t *testing.T) {
	// A local base that inherits a remote ancestor is governed by the
	// allowlist of a base-only config.base.yaml: the ancestor is fetched
	// when the base layer allows it and rejected once the base layer is
	// deny-all.
	ancestor := []byte("role: test\n")
	ancestorHash := fetch.ComputeSHA256(ancestor)
	srv, policy := newLockTestServer(t, map[string][]byte{"/ancestor.yaml": ancestor})

	setup := func(t *testing.T, baseCfg string) string {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, "harness", "mid.yaml"),
			[]byte(fmt.Sprintf("base: \"%s/ancestor.yaml#sha256=%s\"\nrole: test\n", srv.URL, ancestorHash)),
			0o644,
		))
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, "harness", "leaf.yaml"),
			[]byte("base: mid.yaml\nrole: test\nagent: agents/leaf.md\n"),
			0o644,
		))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "leaf.md"), []byte("leaf agent"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(baseCfg), 0o644))
		return dir
	}

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	t.Run("base layer allows ancestor", func(t *testing.T) {
		dir := setup(t, fmt.Sprintf("allowed_remote_resources:\n  - \"%s/\"\n", srv.URL))
		err := runLock(context.Background(), "leaf", dir, "", false, resolveFlags{}, ui.New(os.Stdout))
		require.NoError(t, err)
	})

	t.Run("base layer deny-all rejects ancestor", func(t *testing.T) {
		dir := setup(t, "allowed_remote_resources: []\n")
		err := runLock(context.Background(), "leaf", dir, "", false, resolveFlags{}, ui.New(os.Stdout))
		require.Error(t, err)
	})
}

func TestRunLock_MalformedBaseOnlyConfigFails(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "local.yaml"),
		[]byte("agent: agents/local.md\nrole: test\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte("{{invalid yaml"), 0o644))

	err := runLock(context.Background(), "local", dir, "", false, resolveFlags{}, ui.New(os.Stdout))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing base config")
}
