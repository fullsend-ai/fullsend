package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/binary"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/fetchsvc"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/mintclient"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestRunCommand_RequiresAgentName(t *testing.T) {
	cmd := newRunCmd()
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestRunCommand_HasFullsendDirFlag(t *testing.T) {
	cmd := newRunCmd()
	flag := cmd.Flags().Lookup("fullsend-dir")
	require.NotNil(t, flag)
	assert.Equal(t, "", flag.DefValue)

	annotations := flag.Annotations
	require.Contains(t, annotations, "cobra_annotation_bash_completion_one_required_flag")
}

func TestRunCommand_RegisteredOnRoot(t *testing.T) {
	root := newRootCmd()
	found := false
	for _, sub := range root.Commands() {
		if sub.Name() == "run" {
			found = true
			break
		}
	}
	assert.True(t, found, "run command should be registered on root")
}

func TestRunCommand_HasNoPostScriptFlag(t *testing.T) {
	cmd := newRunCmd()
	flag := cmd.Flags().Lookup("no-post-script")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
}

func TestRunCommand_HasOutputDirFlag(t *testing.T) {
	cmd := newRunCmd()
	flag := cmd.Flags().Lookup("output-dir")
	require.NotNil(t, flag)
	assert.Equal(t, "", flag.DefValue)
}

func TestRunCommand_HasTargetRepoFlag(t *testing.T) {
	cmd := newRunCmd()
	flag := cmd.Flags().Lookup("target-repo")
	require.NotNil(t, flag)
	assert.Equal(t, "", flag.DefValue)

	annotations := flag.Annotations
	require.Contains(t, annotations, "cobra_annotation_bash_completion_one_required_flag")
}

func TestRunCommand_HasOfflineFlag(t *testing.T) {
	cmd := newRunCmd()
	flag := cmd.Flags().Lookup("offline")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
}

func TestRunCommand_HasMaxDepthFlag(t *testing.T) {
	cmd := newRunCmd()
	flag := cmd.Flags().Lookup("max-depth")
	require.NotNil(t, flag)
	assert.Equal(t, "10", flag.DefValue)
}

func TestRunCommand_HasMaxResourcesFlag(t *testing.T) {
	cmd := newRunCmd()
	flag := cmd.Flags().Lookup("max-resources")
	require.NotNil(t, flag)
	assert.Equal(t, "50", flag.DefValue)
}

func TestRunCommand_AcceptsZeroMaxDepth(t *testing.T) {
	cmd := newRunCmd()
	cmd.SetArgs([]string{"test-agent", "--fullsend-dir", "/tmp", "--target-repo", "/tmp", "--max-depth", "0"})
	err := cmd.Execute()
	// --max-depth 0 is valid (disables transitive resolution); the error
	// should come from the run flow, not flag validation.
	if err != nil {
		assert.NotContains(t, err.Error(), "--max-depth must be >= 0")
	}
}

func TestRunCommand_RejectsNegativeMaxDepth(t *testing.T) {
	cmd := newRunCmd()
	cmd.SetArgs([]string{"test-agent", "--fullsend-dir", "/tmp", "--target-repo", "/tmp", "--max-depth", "-1"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--max-depth must be >= 0")
}

func TestRunCommand_RejectsZeroMaxResources(t *testing.T) {
	cmd := newRunCmd()
	cmd.SetArgs([]string{"test-agent", "--fullsend-dir", "/tmp", "--target-repo", "/tmp", "--max-resources", "0"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--max-resources must be >= 1")
}

func TestRunCommand_RejectsNegativeMaxResources(t *testing.T) {
	cmd := newRunCmd()
	cmd.SetArgs([]string{"test-agent", "--fullsend-dir", "/tmp", "--target-repo", "/tmp", "--max-resources", "-1"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--max-resources must be >= 1")
}

func TestRunAgent_HarnessLoadPipeline(t *testing.T) {
	// Exercises the early runAgent pipeline: absFullsendDir, policy,
	// org config loading, LoadWithBase, baseDeps, ResolveRelativeTo.
	// The function fails later at sandbox.EnsureAvailable (no openshell
	// in test env), but by then all harness-loading code paths are covered.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "agents", "code.md"),
		[]byte("You are a coding agent."),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: test\n"),
		0o644,
	))

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openshell")
}

func TestRunAgent_YMLFallback(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "agents", "code.md"),
		[]byte("You are a coding agent."),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yml"),
		[]byte("agent: agents/code.md\nrole: test\n"),
		0o644,
	))

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openshell")
}

func TestRunAgent_HarnessNotFound(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "nonexistent", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harness file not found: tried nonexistent.yaml and nonexistent.yml")
}

func TestRunAgent_HarnessLoadWithOrgConfig(t *testing.T) {
	// Same as above but with a config.yaml present, covering the
	// orgCfg != nil → orgAllowlist path.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "agents", "code.md"),
		[]byte("You are a coding agent."),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: test\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("allowed_remote_resources:\n  - \"https://example.com/\"\n"),
		0o644,
	))

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openshell")
}

func TestRunAgent_MalformedOrgConfig(t *testing.T) {
	// A malformed config.yaml should produce a warning but not prevent
	// local-only harnesses from proceeding through the pipeline.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "agents", "code.md"),
		[]byte("You are a coding agent."),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: test\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("{{invalid yaml"),
		0o644,
	))

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openshell")
}

func TestRunAgent_MalformedOrgConfigWithURLRefs(t *testing.T) {
	// A malformed config.yaml with URL-referenced resources should fail
	// with a parse error on the re-attempt inside HasURLReferences.
	agentHash := fetch.ComputeSHA256([]byte("agent content"))
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte(fmt.Sprintf("agent: \"https://example.com/agents/code.md#sha256=%s\"\nrole: test\n", agentHash)),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("{{invalid yaml"),
		0o644,
	))

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing org config")
}

func TestRunAgent_URLRefsNoOrgConfig(t *testing.T) {
	// Harness with URL agent but no config.yaml → exercises the
	// orgCfg == nil path inside HasURLReferences.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	agentHash := fetch.ComputeSHA256([]byte("agent content"))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte(fmt.Sprintf("agent: \"https://example.com/agents/code.md#sha256=%s\"\nrole: test\n", agentHash)),
		0o644,
	))

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "URL-referenced resources require an org-level config.yaml")
}

func TestRunAgent_WithURLBase(t *testing.T) {
	// Harness with a URL base — exercises the baseDeps logging loop.
	baseContent := []byte("agent: agents/shared.md\nrole: test\n")
	baseHash := fetch.ComputeSHA256(baseContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/base.yaml": baseContent,
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "agents", "shared.md"),
		[]byte("You are a shared agent."),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte(fmt.Sprintf("base: \"%s/base.yaml#sha256=%s\"\nrole: test\n", srv.URL, baseHash)),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte(fmt.Sprintf("allowed_remote_resources:\n  - \"%s/\"\n", srv.URL)),
		0o644,
	))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openshell")
}

func TestRunAgent_URLBaseNoOrgConfig(t *testing.T) {
	// Harness with a URL base but no config.yaml — exercises the
	// pre-check that loads config strictly when a URL base is detected.
	baseContent := []byte("agent: agents/shared.md\n")
	baseHash := fetch.ComputeSHA256(baseContent)

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte(fmt.Sprintf("base: \"https://example.com/base.yaml#sha256=%s\"\n", baseHash)),
		0o644,
	))

	// No config.yaml.

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "URL-referenced resources require an org-level config.yaml")
}

func TestRunAgent_URLBaseMalformedOrgConfig(t *testing.T) {
	// Harness with a URL base and malformed config.yaml — exercises the
	// pre-check parse error path.
	baseContent := []byte("agent: agents/shared.md\n")
	baseHash := fetch.ComputeSHA256(baseContent)

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte(fmt.Sprintf("base: \"https://example.com/base.yaml#sha256=%s\"\n", baseHash)),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("{{invalid yaml"),
		0o644,
	))

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing org config")
}

func TestBuildScanContextCommand_SourcesEnv(t *testing.T) {
	traceID := "aabbccdd-1122-4334-8556-aabbccddeeff"
	cmd := buildScanContextCommand("/sandbox/workspace/repo", traceID)
	assert.Contains(t, cmd, ". /sandbox/workspace/.env &&")
	assert.Contains(t, cmd, "FULLSEND_TRACE_ID='"+traceID+"'")
	assert.Contains(t, cmd, "-exec fullsend scan context")
}

func TestCopyFile(t *testing.T) {
	t.Run("copies content and preserves permissions", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "source")
		dst := filepath.Join(t.TempDir(), "dest")

		content := []byte("hello world")
		require.NoError(t, os.WriteFile(src, content, 0o755))

		require.NoError(t, copyFile(src, dst))

		got, err := os.ReadFile(dst)
		require.NoError(t, err)
		assert.Equal(t, content, got)

		info, err := os.Stat(dst)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
	})

	t.Run("fails on missing source", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "dest")
		err := copyFile("/no/such/file", dst)
		assert.Error(t, err)
	})
}

func TestCollectOpenshellLogs_EmptyRunDir(t *testing.T) {
	// Should be a no-op when runDir is empty — no panic, no error.
	printer := ui.New(io.Discard)
	collectOpenshellLogs("test-sandbox", "", printer)
}

func TestCollectOpenshellLogs_CreatesLogsDir(t *testing.T) {
	// collectOpenshellLogs should create the logs/ directory and attempt
	// log collection. openshell is not available in test, so we expect
	// warnings but no panic.
	tmpDir := t.TempDir()
	runDir := filepath.Join(tmpDir, "run")
	require.NoError(t, os.MkdirAll(runDir, 0o755))

	printer := ui.New(io.Discard)
	collectOpenshellLogs("nonexistent-sandbox", runDir, printer)

	// The logs directory should be created even if collection fails.
	logsDir := filepath.Join(runDir, "logs")
	_, err := os.Stat(logsDir)
	assert.NoError(t, err, "logs directory should exist")
}

func TestRunCommand_HasEnvFileFlag(t *testing.T) {
	cmd := newRunCmd()
	flag := cmd.Flags().Lookup("env-file")
	require.NotNil(t, flag)
	assert.Equal(t, "[]", flag.DefValue)

	// Repeatable: set twice and verify both values are captured.
	require.NoError(t, cmd.Flags().Set("env-file", "/tmp/a.env"))
	require.NoError(t, cmd.Flags().Set("env-file", "/tmp/b.env"))

	val, err := cmd.Flags().GetStringArray("env-file")
	require.NoError(t, err)
	assert.Equal(t, []string{"/tmp/a.env", "/tmp/b.env"}, val)
}

func TestApplySandboxImageOverride_Applied(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_IMAGE", "ghcr.io/fullsend-ai/fullsend-sandbox:dev")

	resolved, overridden := applySandboxImageOverride("ghcr.io/fullsend-ai/fullsend-sandbox:latest")
	assert.True(t, overridden)
	assert.Equal(t, "ghcr.io/fullsend-ai/fullsend-sandbox:dev", resolved)
}

func TestApplySandboxImageOverride_NotSet(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_IMAGE", "")

	resolved, overridden := applySandboxImageOverride("ghcr.io/fullsend-ai/fullsend-sandbox:latest")
	assert.False(t, overridden)
	assert.Equal(t, "ghcr.io/fullsend-ai/fullsend-sandbox:latest", resolved)
}

func TestHasAgentsMD_UpperCase(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# agents"), 0o644))
	assert.True(t, hasAgentsMD(dir))
}

func TestHasAgentsMD_LowerCase(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents.md"), []byte("# agents"), 0o644))
	assert.True(t, hasAgentsMD(dir))
}

func TestHasAgentsMD_TitleCase(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Agents.md"), []byte("# agents"), 0o644))
	assert.True(t, hasAgentsMD(dir))
}

func TestHasAgentsMD_Missing(t *testing.T) {
	dir := t.TempDir()
	assert.False(t, hasAgentsMD(dir))
}

func TestHasAgentsMD_OtherFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# claude"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# readme"), 0o644))
	assert.False(t, hasAgentsMD(dir))
}

func TestHasClaudeMD_UpperCase(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# claude"), 0o644))
	assert.True(t, hasClaudeMD(dir))
}

func TestHasClaudeMD_LowerCase(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "claude.md"), []byte("# claude"), 0o644))
	assert.True(t, hasClaudeMD(dir))
}

func TestHasClaudeMD_TitleCase(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Claude.md"), []byte("# claude"), 0o644))
	assert.True(t, hasClaudeMD(dir))
}

func TestHasClaudeMD_Missing(t *testing.T) {
	dir := t.TempDir()
	assert.False(t, hasClaudeMD(dir))
}

func TestHasClaudeMD_OtherFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# agents"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# readme"), 0o644))
	assert.False(t, hasClaudeMD(dir))
}

func TestHasClaudeMD_DotPrefixed(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude.md"), []byte("# claude"), 0o644))
	assert.True(t, hasClaudeMD(dir))
}

func TestClaudeMDPointerContent(t *testing.T) {
	// Verify the injected CLAUDE.md content references AGENTS.md and
	// ends with a newline (so the file is well-formed).
	assert.Contains(t, claudeMDPointerContent, "AGENTS.md")
	assert.True(t, strings.HasSuffix(claudeMDPointerContent, "\n"), "content should end with newline")
}

func TestDoInjectClaudeMDPointer_Success(t *testing.T) {
	var cmds []string
	mockExec := func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		cmds = append(cmds, cmd)
		return "", "", 0, nil
	}

	printer := ui.New(io.Discard)
	doInjectClaudeMDPointer("test-sandbox", "/workspace/repo", printer, mockExec)

	require.Len(t, cmds, 2)
	assert.Contains(t, cmds[0], "CLAUDE.md")
	assert.Contains(t, cmds[0], "/workspace/repo/CLAUDE.md")
	assert.Contains(t, cmds[0], "AGENTS.md") // content references AGENTS.md
	assert.Contains(t, cmds[1], ".git/info/exclude")
	assert.Contains(t, cmds[1], "CLAUDE.md")
}

func TestDoInjectClaudeMDPointer_WriteFails(t *testing.T) {
	var cmds []string
	mockExec := func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		cmds = append(cmds, cmd)
		return "", "write error", 1, fmt.Errorf("write failed")
	}

	printer := ui.New(io.Discard)
	doInjectClaudeMDPointer("test-sandbox", "/workspace/repo", printer, mockExec)

	// Should have attempted only the write, not the exclude.
	require.Len(t, cmds, 1)
}

func TestDoInjectClaudeMDPointer_ExcludeFails(t *testing.T) {
	callCount := 0
	mockExec := func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		callCount++
		if callCount == 2 {
			return "", "exclude error", 1, fmt.Errorf("exclude failed")
		}
		return "", "", 0, nil
	}

	printer := ui.New(io.Discard)
	doInjectClaudeMDPointer("test-sandbox", "/workspace/repo", printer, mockExec)

	// Both commands should have been attempted (write succeeds, exclude fails
	// but function continues).
	assert.Equal(t, 2, callCount)
}

func TestEnvToList_Sorted(t *testing.T) {
	env := map[string]string{
		"Z_VAR": "z",
		"A_VAR": "a",
		"M_VAR": "m",
	}
	list := envToList(env)
	require.Len(t, list, 3)
	assert.Equal(t, "A_VAR=a", list[0])
	assert.Equal(t, "M_VAR=m", list[1])
	assert.Equal(t, "Z_VAR=z", list[2])
}

func TestShellSafeExpandEnv(t *testing.T) {
	tests := []struct {
		name     string
		template string
		env      map[string]string
		want     string
	}{
		{
			name:     "simple value",
			template: `export FOO="${FOO}"`,
			env:      map[string]string{"FOO": "bar"},
			want:     `export FOO="bar"`,
		},
		{
			name:     "value with double quotes",
			template: `export MSG="${MSG}"`,
			env:      map[string]string{"MSG": `say "hello"`},
			want:     `export MSG="say \"hello\""`,
		},
		{
			name:     "value with parentheses",
			template: `export MSG="${MSG}"`,
			env:      map[string]string{"MSG": "fix (example) thing"},
			want:     `export MSG="fix (example) thing"`,
		},
		{
			name:     "value with single quotes",
			template: `export MSG="${MSG}"`,
			env:      map[string]string{"MSG": "it's broken"},
			want:     `export MSG="it's broken"`,
		},
		{
			name:     "value with dollar sign",
			template: `export V="${V}"`,
			env:      map[string]string{"V": "$HOME"},
			want:     `export V="\$HOME"`,
		},
		{
			name:     "value with backticks",
			template: `export CMD="${CMD}"`,
			env:      map[string]string{"CMD": "use `grep` here"},
			want:     "export CMD=\"use \\`grep\\` here\"",
		},
		{
			name:     "value with backslashes",
			template: `export P="${P}"`,
			env:      map[string]string{"P": `C:\Users\test`},
			want:     `export P="C:\\Users\\test"`,
		},
		{
			name:     "value with all four special chars",
			template: `export V="${V}"`,
			env:      map[string]string{"V": "a]\" $x `y` \\z"},
			want:     `export V="a]\" \$x ` + "\\`y\\`" + ` \\z"`,
		},
		{
			name:     "value with shell metacharacters safe inside double quotes",
			template: `export CMD="${CMD}"`,
			env:      map[string]string{"CMD": "foo || true && bar; baz > /dev/null"},
			want:     `export CMD="foo || true && bar; baz > /dev/null"`,
		},
		{
			name:     "empty value",
			template: `export FOO="${FOO}"`,
			env:      map[string]string{"FOO": ""},
			want:     `export FOO=""`,
		},
		{
			name:     "undefined variable",
			template: `export FOO="${UNDEFINED}"`,
			env:      map[string]string{},
			want:     `export FOO=""`,
		},
		{
			name:     "static lines unchanged",
			template: "export STATIC='hello world'",
			env:      map[string]string{},
			want:     "export STATIC='hello world'",
		},
		{
			name:     "multiple variables",
			template: "export A=\"${A}\"\nexport B=\"${B}\"",
			env:      map[string]string{"A": "1", "B": "two (2)"},
			want:     "export A=\"1\"\nexport B=\"two (2)\"",
		},
		{
			name:     "unquoted template with simple value",
			template: "export FOO=${FOO}",
			env:      map[string]string{"FOO": "bar"},
			want:     "export FOO=bar",
		},
		{
			name:     "braceless $VAR expansion",
			template: `export FOO="$FOO"`,
			env:      map[string]string{"FOO": `say "hello"`},
			want:     `export FOO="say \"hello\""`,
		},
		{
			name:     "real-world HUMAN_INSTRUCTION from issue 615",
			template: `export HUMAN_INSTRUCTION="${HUMAN_INSTRUCTION}"`,
			env:      map[string]string{"HUMAN_INSTRUCTION": `replacing --search "$ISSUE_NUMBER in:body,title" with timeline API || true`},
			want:     `export HUMAN_INSTRUCTION="replacing --search \"\$ISSUE_NUMBER in:body,title\" with timeline API || true"`,
		},
		{
			name:     "real-world instruction with parentheses from failing run",
			template: `export HUMAN_INSTRUCTION="${HUMAN_INSTRUCTION}"`,
			env:      map[string]string{"HUMAN_INSTRUCTION": `An administrator with elevated access to the GCP project (for example, with the ability to set IAM policy) can grant all required roles`},
			want:     `export HUMAN_INSTRUCTION="An administrator with elevated access to the GCP project (for example, with the ability to set IAM policy) can grant all required roles"`,
		},
		{
			name:     "injection attempt: break out of double quotes",
			template: `export V="${V}"`,
			env:      map[string]string{"V": `"; rm -rf /; echo "`},
			want:     `export V="\"; rm -rf /; echo \""`,
		},
		{
			name:     "injection attempt: command substitution",
			template: `export V="${V}"`,
			env:      map[string]string{"V": `$(cat /etc/passwd)`},
			want:     `export V="\$(cat /etc/passwd)"`,
		},
		{
			name:     "injection attempt: backtick substitution",
			template: `export V="${V}"`,
			env:      map[string]string{"V": "`cat /etc/passwd`"},
			want:     "export V=\"\\`cat /etc/passwd\\`\"",
		},
		{
			name:     "newlines in value",
			template: `export V="${V}"`,
			env:      map[string]string{"V": "line1\nline2\nline3"},
			want:     "export V=\"line1\nline2\nline3\"",
		},
		{
			name:     "tabs and special whitespace",
			template: `export V="${V}"`,
			env:      map[string]string{"V": "col1\tcol2"},
			want:     "export V=\"col1\tcol2\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			got := shellSafeExpandEnv(tt.template)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestShellSafeExpandEnv_ShellRoundtrip verifies that expanded env files
// produce the original value when sourced by a real shell. This is the
// definitive safety test: if the value survives a roundtrip through
// sh -c '. file && printf "%s" "$VAR"', the escaping is correct.
func TestShellSafeExpandEnv_ShellRoundtrip(t *testing.T) {
	values := []struct {
		name  string
		value string
	}{
		{"simple", "hello world"},
		{"double quotes", `say "hello" to "world"`},
		{"single quotes", "it's a test"},
		{"parentheses", "fix (example) thing"},
		{"pipes and logic", "foo || true && bar"},
		{"dollar sign", "cost is $100 or $HOME"},
		{"command substitution", "$(rm -rf /)"},
		{"backtick substitution", "`rm -rf /`"},
		{"backslashes", `path\to\file`},
		{"semicolons", "cmd1; cmd2; cmd3"},
		{"redirects", "echo foo > /tmp/evil"},
		{"glob chars", "match *.go and file?.txt"},
		{"mixed injection", `"; $(evil) ` + "`more`" + ` && rm -rf / #`},
		{"all four special chars", `quote" dollar$ tick` + "`" + ` slash\`},
		{"newlines", "line1\nline2\nline3"},
		{"tabs", "col1\tcol2"},
		{"empty", ""},
		{"unicode", "こんにちは 🎉"},
		{"real issue 615", `replacing --search "$ISSUE_NUMBER in:body,title" with timeline API || true`},
		{"real failing run", `An administrator with elevated access to the GCP project (for example, with the ability to set IAM policy) can grant all required roles with a single script:`},
		{"already escaped backslash", `already \" escaped`},
		{"nested quotes", `He said "she said 'hello'" today`},
		{"hash comment char", "value # not a comment"},
		{"exclamation mark", "hello! world!"},
		{"curly braces", "use ${VAR} syntax"},
		{"square brackets", "array[0] = value"},
		{"tilde", "~user/path"},
		{"ampersand", "Tom & Jerry"},
	}

	for _, tt := range values {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TEST_VAL", tt.value)
			expanded := shellSafeExpandEnv(`export TEST_VAL="${TEST_VAL}"`)

			// Write expanded content to a temp file and source it in sh.
			envFile := filepath.Join(t.TempDir(), "test.env")
			require.NoError(t, os.WriteFile(envFile, []byte(expanded+"\n"), 0o644))

			// Use printf "%s" (not echo) to avoid interpretation of \n etc.
			cmd := exec.Command("sh", "-c", fmt.Sprintf(`. %s && printf '%%s' "$TEST_VAL"`, envFile))
			out, err := cmd.Output()
			require.NoError(t, err, "shell failed to source expanded env file; expanded content:\n%s", expanded)
			assert.Equal(t, tt.value, string(out), "value did not survive shell roundtrip")
		})
	}
}

func TestNeedsCrossCompilation(t *testing.T) {
	result := needsCrossCompilation()
	if runtime.GOOS == "linux" {
		assert.False(t, result, "should not need cross-compilation on Linux")
	} else {
		assert.True(t, result, "should need cross-compilation on %s", runtime.GOOS)
	}
}

func TestSandboxArch_Default(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_ARCH", "")
	assert.Equal(t, runtime.GOARCH, sandboxArch())
}

func TestSandboxArch_Override(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_ARCH", "amd64")
	assert.Equal(t, "amd64", sandboxArch())
}

func TestSandboxArch_InvalidFallsBack(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_ARCH", "../../etc/passwd")
	assert.Equal(t, runtime.GOARCH, sandboxArch())
}

func TestValidateLinuxBinary_RejectsNonELF(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "not-elf")
	require.NoError(t, os.WriteFile(tmp, []byte("#!/bin/sh\necho hello"), 0o755))
	err := binary.ValidateLinuxBinary(tmp, "amd64")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid ELF binary")
}

func TestValidateLinuxBinary_RejectsMissing(t *testing.T) {
	err := binary.ValidateLinuxBinary("/tmp/nonexistent-fullsend-binary-12345", "amd64")
	require.Error(t, err)
}

func TestValidateLinuxBinary_AcceptsHostBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("host binary is only ELF on Linux")
	}
	exe, err := os.Executable()
	require.NoError(t, err)
	assert.NoError(t, binary.ValidateLinuxBinary(exe, runtime.GOARCH))
}

func TestAgentWorkingDirExcludes_ContainsKnownPatterns(t *testing.T) {
	// Verify the exclusion list contains the known agent working directories.
	expected := []string{".agentready/", ".fullsend-workspace/"}
	for _, pattern := range expected {
		found := false
		for _, exclude := range agentWorkingDirExcludes {
			if exclude == pattern {
				found = true
				break
			}
		}
		assert.True(t, found, "agentWorkingDirExcludes should contain %q", pattern)
	}
}

func TestAgentWorkingDirExcludes_NotEmpty(t *testing.T) {
	assert.NotEmpty(t, agentWorkingDirExcludes,
		"agentWorkingDirExcludes must not be empty — agents create working dirs that need exclusion")
}

func TestReadOIDCAuthFile_Success(t *testing.T) {
	f := filepath.Join(t.TempDir(), "auth")
	require.NoError(t, os.WriteFile(f, []byte("bearer test-token"), 0o600))
	val, err := readOIDCAuthFile(f)
	require.NoError(t, err)
	assert.Equal(t, "bearer test-token", val)
}

func TestReadOIDCAuthFile_EmptyPath(t *testing.T) {
	_, err := readOIDCAuthFile("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not set")
}

func TestReadOIDCAuthFile_EmptyFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "auth")
	require.NoError(t, os.WriteFile(f, []byte(""), 0o600))
	_, err := readOIDCAuthFile(f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestRefreshOIDCToken_FetchSucceedsSCPFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "bearer test-auth", r.Header.Get("Authorization"))
		fmt.Fprint(w, `{"value":"fresh-oidc-token-content"}`)
	}))
	defer srv.Close()

	err := refreshOIDCToken(context.Background(), "nonexistent-sandbox", srv.URL, "bearer test-auth")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "copying token to sandbox")
}

func TestRefreshOIDCToken_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	err := refreshOIDCToken(context.Background(), "nonexistent-sandbox", srv.URL, "bearer test-auth")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

func TestRefreshOIDCToken_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := refreshOIDCToken(context.Background(), "nonexistent-sandbox", srv.URL, "bearer test-auth")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty token")
}

func TestRefreshOIDCToken_NonJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>Service Unavailable</html>")
	}))
	defer srv.Close()

	err := refreshOIDCToken(context.Background(), "nonexistent-sandbox", srv.URL, "bearer test-auth")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-JSON response")
}

func TestRefreshOIDCToken_CancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"value":"fresh-oidc-token-content"}`)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := refreshOIDCToken(ctx, "nonexistent-sandbox", srv.URL, "bearer test-auth")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching OIDC token")
}

func TestRunOIDCRefresh_TicksAndStops(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"value":"fresh-oidc-token-content"}`)
	}))
	defer srv.Close()

	origInterval := oidcRefreshInterval
	oidcRefreshInterval = 50 * time.Millisecond
	defer func() { oidcRefreshInterval = origInterval }()

	ctx, cancel := context.WithCancel(context.Background())
	printer := ui.New(io.Discard)

	finished := make(chan struct{})
	go func() {
		runOIDCRefresh(ctx, "nonexistent-sandbox", srv.URL, "bearer test-auth", printer)
		close(finished)
	}()

	require.Eventually(t, func() bool { return calls.Load() >= 2 }, 2*time.Second, 10*time.Millisecond,
		"expected at least 2 refresh calls")

	cancel()

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("runOIDCRefresh did not exit after context was cancelled")
	}

	assert.GreaterOrEqual(t, calls.Load(), int32(2))
}

func TestRunHeartbeat_SingleNoticeOnCompletion(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")

	// Use a very short heartbeat interval so ticks happen quickly.
	origInterval := heartbeatInterval
	heartbeatInterval = 50 * time.Millisecond
	defer func() { heartbeatInterval = origInterval }()

	var buf bytes.Buffer
	printer := ui.New(io.Discard)
	done := make(chan struct{})
	start := time.Now()

	finished := make(chan struct{})
	go func() {
		runHeartbeatTo(&buf, printer, start, 10*time.Minute, done)
		close(finished)
	}()

	// Let it tick several times.
	time.Sleep(200 * time.Millisecond)
	close(done)

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("runHeartbeat did not exit after done was closed")
	}

	output := buf.String()

	// Should contain exactly one ::notice:: line — the completion notice.
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var noticeLines []string
	for _, line := range lines {
		if strings.Contains(line, "::notice::") {
			noticeLines = append(noticeLines, line)
		}
	}
	assert.Len(t, noticeLines, 1, "expected exactly one ::notice:: annotation, got: %v", noticeLines)
	assert.Contains(t, noticeLines[0], "Agent completed (")
}

func TestRunHeartbeat_NoNoticeWhenNotCI(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "false")

	origInterval := heartbeatInterval
	heartbeatInterval = 50 * time.Millisecond
	defer func() { heartbeatInterval = origInterval }()

	var buf bytes.Buffer
	printer := ui.New(io.Discard)
	done := make(chan struct{})
	start := time.Now()

	finished := make(chan struct{})
	go func() {
		runHeartbeatTo(&buf, printer, start, 10*time.Minute, done)
		close(finished)
	}()

	time.Sleep(200 * time.Millisecond)
	close(done)

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("runHeartbeat did not exit after done was closed")
	}

	assert.Empty(t, buf.String(), "should not emit any ::notice:: when not in CI")
}

func TestValidationFailMessage_UsesOutputWhenPresent(t *testing.T) {
	msg := validationFailMessage([]byte("check failed: lint errors"), fmt.Errorf("exit status 1"))
	assert.Equal(t, "check failed: lint errors", msg)
}

func TestValidationFailMessage_FallsBackToError(t *testing.T) {
	msg := validationFailMessage([]byte(""), fmt.Errorf("exec: \"missing-script\": executable file not found in $PATH"))
	assert.Equal(t, "exec: \"missing-script\": executable file not found in $PATH", msg)
}

func TestValidationFailMessage_FallsBackWhenWhitespaceOnly(t *testing.T) {
	msg := validationFailMessage([]byte("  \n\t  "), fmt.Errorf("exit status 127"))
	assert.Equal(t, "exit status 127", msg)
}

func TestValidationFailMessage_TrimsOutput(t *testing.T) {
	msg := validationFailMessage([]byte("  some output\n"), fmt.Errorf("exit status 1"))
	assert.Equal(t, "some output", msg)
}

func TestOpenTeeReader_EmptyPath(t *testing.T) {
	src := strings.NewReader("hello")
	printer := ui.New(io.Discard)

	r, close := openTeeReader(src, "", printer)
	defer close()

	// r should be the original reader — no file created
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
}

func TestOpenTeeReader_WritesToFile(t *testing.T) {
	content := "line1\nline2\n"
	src := strings.NewReader(content)
	printer := ui.New(io.Discard)

	outPath := filepath.Join(t.TempDir(), "out.jsonl")
	r, close := openTeeReader(src, outPath, printer)
	defer close()

	got, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, content, string(got))

	close() // flush before reading file
	fileData, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Equal(t, content, string(fileData))
}

func TestOpenTeeReader_CreateFailFallsBackToSource(t *testing.T) {
	content := "data"
	src := strings.NewReader(content)

	var warnBuf bytes.Buffer
	printer := ui.New(&warnBuf)

	// Unwritable path — directory that doesn't exist
	r, close := openTeeReader(src, "/nonexistent-dir/out.jsonl", printer)
	defer close()

	got, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, content, string(got), "source stream still readable after create failure")
	assert.Contains(t, warnBuf.String(), "Failed to create claude-output.jsonl")
}

func TestOpenTeeReader_FileCompleteOnParserError(t *testing.T) {
	// Simulate: progressParser reads part of stream, then errors; caller drains
	// remainder via io.Copy(io.Discard, r). File should contain all bytes.
	content := "part1\npart2\n"
	src := strings.NewReader(content)
	printer := ui.New(io.Discard)

	outPath := filepath.Join(t.TempDir(), "out.jsonl")
	r, closeFile := openTeeReader(src, outPath, printer)

	// Simulate parser reading only first 6 bytes then returning an error
	firstPart := make([]byte, 6)
	_, err := io.ReadFull(r, firstPart)
	require.NoError(t, err)

	// Simulate drain of remaining bytes (as runAgentWithProgress does on parse error)
	_, err = io.Copy(io.Discard, r)
	require.NoError(t, err)

	closeFile()

	fileData, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Equal(t, content, string(fileData), "file should contain all bytes including post-error drain")
}

func TestPRHeadSHAFromEventPath_WithSHA(t *testing.T) {
	// Simulate a workflow_dispatch event file where the nested event_payload
	// contains pull_request.head.sha.
	eventJSON := `{
		"inputs": {
			"event_payload": "{\"pull_request\":{\"number\":42,\"head\":{\"ref\":\"feature\",\"sha\":\"abc123def\",\"repo\":{\"full_name\":\"org/repo\"}}}}"
		}
	}`
	f := filepath.Join(t.TempDir(), "event.json")
	require.NoError(t, os.WriteFile(f, []byte(eventJSON), 0o644))

	got := prHeadSHAFromEventPath(f)
	assert.Equal(t, "abc123def", got)
}

func TestPRHeadSHAFromEventPath_WithoutSHA(t *testing.T) {
	// Event payload has pull_request but no head.sha — should return empty.
	eventJSON := `{
		"inputs": {
			"event_payload": "{\"pull_request\":{\"number\":42,\"head\":{\"ref\":\"feature\",\"repo\":{\"full_name\":\"org/repo\"}}}}"
		}
	}`
	f := filepath.Join(t.TempDir(), "event.json")
	require.NoError(t, os.WriteFile(f, []byte(eventJSON), 0o644))

	got := prHeadSHAFromEventPath(f)
	assert.Empty(t, got)
}

func TestPRHeadSHAFromEventPath_NoPullRequest(t *testing.T) {
	// Issue-only event — no pull_request in the payload.
	eventJSON := `{
		"inputs": {
			"event_payload": "{\"issue\":{\"number\":99}}"
		}
	}`
	f := filepath.Join(t.TempDir(), "event.json")
	require.NoError(t, os.WriteFile(f, []byte(eventJSON), 0o644))

	got := prHeadSHAFromEventPath(f)
	assert.Empty(t, got)
}

func TestPRHeadSHAFromEventPath_EmptyPath(t *testing.T) {
	got := prHeadSHAFromEventPath("")
	assert.Empty(t, got)
}

func TestPRHeadSHAFromEventPath_MissingFile(t *testing.T) {
	got := prHeadSHAFromEventPath("/nonexistent/path/event.json")
	assert.Empty(t, got)
}

func TestPRHeadSHAFromEventPath_NoInputs(t *testing.T) {
	// Direct event (not workflow_dispatch) — no inputs field.
	eventJSON := `{"action": "opened", "pull_request": {"number": 1}}`
	f := filepath.Join(t.TempDir(), "event.json")
	require.NoError(t, os.WriteFile(f, []byte(eventJSON), 0o644))

	got := prHeadSHAFromEventPath(f)
	assert.Empty(t, got)
}

// --- detectForgePlatform tests ---

func TestDetectForgePlatform_ExplicitFlag(t *testing.T) {
	p, err := detectForgePlatform("github")
	require.NoError(t, err)
	assert.Equal(t, "github", p)

	p, err = detectForgePlatform("gitlab")
	require.NoError(t, err)
	assert.Equal(t, "gitlab", p)
}

func TestDetectForgePlatform_InvalidFlag(t *testing.T) {
	_, err := detectForgePlatform("bitbucket")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid forge platform")
}

func TestDetectForgePlatform_GitHubActions(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITLAB_CI", "")

	p, err := detectForgePlatform("")
	require.NoError(t, err)
	assert.Equal(t, "github", p)
}

func TestDetectForgePlatform_GitLabCI(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GITLAB_CI", "true")

	p, err := detectForgePlatform("")
	require.NoError(t, err)
	assert.Equal(t, "gitlab", p)
}

func TestDetectForgePlatform_NoEnv(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GITLAB_CI", "")

	p, err := detectForgePlatform("")
	require.NoError(t, err)
	assert.Equal(t, "", p)
}

func TestDetectForgePlatform_FlagOverridesEnv(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")

	p, err := detectForgePlatform("gitlab")
	require.NoError(t, err)
	assert.Equal(t, "gitlab", p)
}

func TestDetectForgePlatform_GitHubPrecedesGitLab(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITLAB_CI", "true")

	p, err := detectForgePlatform("")
	require.NoError(t, err)
	assert.Equal(t, "github", p)
}

func TestRunCommand_HasForgeFlag(t *testing.T) {
	cmd := newRunCmd()
	flag := cmd.Flags().Lookup("forge")
	require.NotNil(t, flag)
	assert.Equal(t, "", flag.DefValue)
}

func TestLockCommand_HasForgeFlag(t *testing.T) {
	cmd := newLockCmd()
	flag := cmd.Flags().Lookup("forge")
	require.NotNil(t, flag)
	assert.Equal(t, "", flag.DefValue)
}

func TestBootstrapEnv_IncludesFetchServiceVars(t *testing.T) {
	h := &harness.Harness{Agent: "agents/test.md"}
	fEnv := fetchServiceEnv{addr: "127.0.0.1:54321", token: "deadbeef"}

	err := bootstrapEnv("nonexistent-sandbox", "/workspace/repo", h, nil, fEnv)

	// Expected to fail at sandbox.UploadFile — we just verify the fetch
	// env var code path was reached (coverage) and the error is from upload.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "copying .env file to sandbox")
}

func TestBootstrapEnv_SkipsFetchVarsWhenEmpty(t *testing.T) {
	h := &harness.Harness{Agent: "agents/test.md"}

	err := bootstrapEnv("nonexistent-sandbox", "/workspace/repo", h, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "copying .env file to sandbox")
}

func TestShouldStartFetchService_AllowRuntimeFetch(t *testing.T) {
	h := &harness.Harness{
		Agent:                  "agents/test.md",
		AllowRuntimeFetch:      true,
		AllowedRemoteResources: []string{"https://github.com/org/"},
	}
	start, warning := shouldStartFetchService(h)
	assert.True(t, start)
	assert.Empty(t, warning)
}

func TestShouldStartFetchService_URLSkills(t *testing.T) {
	h := &harness.Harness{
		Agent:  "agents/test.md",
		Skills: []string{"https://github.com/org/skills/tree/abc/rust#sha256=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
	}
	start, warning := shouldStartFetchService(h)
	assert.True(t, start)
	assert.Empty(t, warning)
}

func TestShouldStartFetchService_AllowedRemoteResourcesOnly(t *testing.T) {
	h := &harness.Harness{
		Agent:                  "agents/test.md",
		AllowedRemoteResources: []string{"https://github.com/org/"},
	}
	start, warning := shouldStartFetchService(h)
	assert.True(t, start)
	assert.Contains(t, warning, "deprecated")
}

func TestShouldStartFetchService_NoRemoteResources(t *testing.T) {
	h := &harness.Harness{Agent: "agents/test.md"}
	start, warning := shouldStartFetchService(h)
	assert.False(t, start)
	assert.Empty(t, warning)
}

func TestSetupFetchService_WithForgeClient(t *testing.T) {
	tmpDir := t.TempDir()
	h := &harness.Harness{Agent: "agents/test.md"}
	mockClient := &mockForgeClient{}

	env, shutdown, err := setupFetchService(
		context.Background(),
		mockClient,
		h,
		func() (string, error) { return "", fmt.Errorf("should not be called") },
		fetchsvc.ServiceConfig{
			Harness:       h,
			WorkspaceRoot: tmpDir,
			MaxFetches:    10,
		},
		func(string) {},
	)
	require.NoError(t, err)
	defer shutdown()

	assert.NotEmpty(t, env.addr)
	assert.NotEmpty(t, env.token)
	assert.Len(t, env.token, 64)
}

func TestSetupFetchService_ResolvesTokenWhenNoForgeClient(t *testing.T) {
	tmpDir := t.TempDir()
	h := &harness.Harness{
		Agent:                  "agents/test.md",
		AllowedRemoteResources: []string{"https://github.com/org/"},
	}

	tokenResolved := false
	env, shutdown, err := setupFetchService(
		context.Background(),
		nil,
		h,
		func() (string, error) { tokenResolved = true; return "ghp_test", nil },
		fetchsvc.ServiceConfig{
			Harness:       h,
			WorkspaceRoot: tmpDir,
			MaxFetches:    10,
		},
		func(string) {},
	)
	require.NoError(t, err)
	defer shutdown()

	assert.True(t, tokenResolved)
	assert.NotEmpty(t, env.addr)
}

func TestSetupFetchService_NoForgeClientNoRemoteResources(t *testing.T) {
	tmpDir := t.TempDir()
	h := &harness.Harness{Agent: "agents/test.md"}

	env, shutdown, err := setupFetchService(
		context.Background(),
		nil,
		h,
		func() (string, error) { return "", fmt.Errorf("should not be called") },
		fetchsvc.ServiceConfig{
			Harness:       h,
			WorkspaceRoot: tmpDir,
			MaxFetches:    10,
		},
		func(string) {},
	)
	require.NoError(t, err)
	defer shutdown()

	assert.NotEmpty(t, env.addr)
}

func TestSetupFetchService_TokenResolutionFails(t *testing.T) {
	tmpDir := t.TempDir()
	h := &harness.Harness{
		Agent:                  "agents/test.md",
		AllowedRemoteResources: []string{"https://github.com/org/"},
	}

	var warned string
	env, shutdown, err := setupFetchService(
		context.Background(),
		nil,
		h,
		func() (string, error) { return "", fmt.Errorf("no token available") },
		fetchsvc.ServiceConfig{
			Harness:       h,
			WorkspaceRoot: tmpDir,
			MaxFetches:    10,
		},
		func(msg string) { warned = msg },
	)
	require.NoError(t, err)
	defer shutdown()

	assert.NotEmpty(t, env.addr)
	assert.Contains(t, warned, "no token available")
}

func TestSetupFetchService_CustomMaxFetches(t *testing.T) {
	tmpDir := t.TempDir()
	maxFetches := 50
	h := &harness.Harness{
		Agent:                  "agents/test.md",
		AllowRuntimeFetch:      true,
		AllowedRemoteResources: []string{"https://github.com/org/"},
		MaxRuntimeFetches:      &maxFetches,
	}

	cfg := fetchsvc.ServiceConfig{
		Harness:       h,
		WorkspaceRoot: tmpDir,
		MaxFetches:    h.EffectiveMaxRuntimeFetches(),
	}
	assert.Equal(t, 50, cfg.MaxFetches)

	env, shutdown, err := setupFetchService(
		context.Background(),
		nil,
		h,
		func() (string, error) { return "ghp_test", nil },
		cfg,
		func(string) {},
	)
	require.NoError(t, err)
	defer shutdown()

	assert.NotEmpty(t, env.addr)
}

func TestEffectiveMaxRuntimeFetches_MatchesFetchsvcDefault(t *testing.T) {
	h := &harness.Harness{}
	if h.EffectiveMaxRuntimeFetches() != fetchsvc.DefaultMaxFetches {
		t.Fatalf("harness default %d != fetchsvc.DefaultMaxFetches %d — update defaultMaxRuntimeFetches in harness.go",
			h.EffectiveMaxRuntimeFetches(), fetchsvc.DefaultMaxFetches)
	}
}

type mockForgeClient struct {
	forge.Client
}

func TestSetupStatusNotifier_MintURL(t *testing.T) {
	tmpDir := t.TempDir()
	printer := ui.New(io.Discard)

	sOpts := statusOpts{
		statusRepo: "org/repo",
		statusNum:  7,
		mintURL:    "https://mint.example.com",
	}

	t.Setenv("GITHUB_RUN_ID", "run-42")

	n, err := setupStatusNotifier(tmpDir, "review", sOpts, printer)
	require.NoError(t, err)
	assert.NotNil(t, n)
	assert.True(t, n.HasClientFactory(), "client factory should be set when mint URL provided")
}

func TestSetupStatusNotifier_MintURLFromEnv(t *testing.T) {
	tmpDir := t.TempDir()
	printer := ui.New(io.Discard)

	sOpts := statusOpts{
		statusRepo: "org/repo",
		statusNum:  7,
	}

	t.Setenv("FULLSEND_MINT_URL", "https://mint.example.com")
	t.Setenv("GITHUB_RUN_ID", "run-42")

	n, err := setupStatusNotifier(tmpDir, "code", sOpts, printer)
	require.NoError(t, err)
	assert.NotNil(t, n)
	assert.True(t, n.HasClientFactory(), "client factory should be set from FULLSEND_MINT_URL env var")
}

func TestSetupStatusNotifier_NoMintURL(t *testing.T) {
	tmpDir := t.TempDir()
	printer := ui.New(io.Discard)

	sOpts := statusOpts{
		statusRepo: "org/repo",
		statusNum:  7,
	}

	t.Setenv("GITHUB_RUN_ID", "run-42")
	t.Setenv("FULLSEND_MINT_URL", "")
	t.Setenv("GITHUB_TOKEN", "")

	_, err := setupStatusNotifier(tmpDir, "review", sOpts, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no mint URL available")
}

func TestSetupStatusNotifier_InvalidRepo(t *testing.T) {
	tmpDir := t.TempDir()
	printer := ui.New(io.Discard)

	sOpts := statusOpts{
		statusRepo: "noslash",
		statusNum:  7,
	}

	_, err := setupStatusNotifier(tmpDir, "review", sOpts, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--status-repo must be in owner/repo format")
}

func TestRunCommand_HasMintURLFlag(t *testing.T) {
	cmd := newRunCmd()

	f := cmd.Flags().Lookup("mint-url")
	require.NotNil(t, f, "run command should have --mint-url flag")
	assert.Equal(t, "", f.DefValue)
}

func TestSetupStatusNotifier_FactoryMintSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	printer := ui.New(io.Discard)

	origMint := statusMintToken
	statusMintToken = func(_ context.Context, req mintclient.MintRequest) (*mintclient.MintResult, error) {
		assert.Equal(t, "coder", req.Role)
		assert.Equal(t, []string{"repo"}, req.Repos)
		return &mintclient.MintResult{Token: "ghs_test_minted"}, nil
	}
	defer func() { statusMintToken = origMint }()

	sOpts := statusOpts{
		statusRepo: "org/repo",
		statusNum:  7,
		mintURL:    "https://mint.example.com",
	}

	t.Setenv("GITHUB_RUN_ID", "run-42")
	t.Setenv("GITHUB_ACTIONS", "true")

	n, err := setupStatusNotifier(tmpDir, "code", sOpts, printer)
	require.NoError(t, err)

	client, err := n.InvokeClientFactory(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, client)
}

func TestSetupStatusNotifier_FactoryMintError(t *testing.T) {
	tmpDir := t.TempDir()
	printer := ui.New(io.Discard)

	origMint := statusMintToken
	statusMintToken = func(_ context.Context, _ mintclient.MintRequest) (*mintclient.MintResult, error) {
		return nil, fmt.Errorf("OIDC unavailable")
	}
	defer func() { statusMintToken = origMint }()

	sOpts := statusOpts{
		statusRepo: "org/repo",
		statusNum:  7,
		mintURL:    "https://mint.example.com",
	}

	t.Setenv("GITHUB_RUN_ID", "run-42")

	n, err := setupStatusNotifier(tmpDir, "review", sOpts, printer)
	require.NoError(t, err)

	client, err := n.InvokeClientFactory(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OIDC unavailable")
	assert.Nil(t, client)
}

func TestRunCommand_StatusTokenFlagRemoved(t *testing.T) {
	cmd := newRunCmd()
	f := cmd.Flags().Lookup("status-token")
	assert.Nil(t, f, "--status-token flag should no longer exist")
}

func TestTitleCase(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"hello world", "Hello World"},
		{"code", "Code"},
		{"", ""},
		{"already Title", "Already Title"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, titleCase(tt.in))
	}
}

func TestSetupStatusNotifier_ConfigYAML(t *testing.T) {
	tmpDir := t.TempDir()
	printer := ui.New(io.Discard)

	configData := `defaults:
  status_notifications:
    comment:
      start: enabled
      completion: disabled
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "config.yaml"), []byte(configData), 0o644))

	sOpts := statusOpts{
		statusRepo: "org/repo",
		statusNum:  7,
		mintURL:    "https://mint.example.com",
	}

	t.Setenv("GITHUB_RUN_ID", "run-42")

	n, err := setupStatusNotifier(tmpDir, "review", sOpts, printer)
	require.NoError(t, err)
	assert.NotNil(t, n)
}

func TestSetupStatusNotifier_RunIDFallback(t *testing.T) {
	tmpDir := t.TempDir()
	printer := ui.New(io.Discard)

	sOpts := statusOpts{
		statusRepo: "org/repo",
		statusNum:  7,
		mintURL:    "https://mint.example.com",
	}

	t.Setenv("GITHUB_RUN_ID", "")

	n, err := setupStatusNotifier(tmpDir, "code", sOpts, printer)
	require.NoError(t, err)
	assert.NotNil(t, n)
}

func TestSetupStatusNotifier_PRHeadSHA(t *testing.T) {
	tmpDir := t.TempDir()
	printer := ui.New(io.Discard)

	eventPayload := `{"inputs":{"event_payload":"{\"pull_request\":{\"head\":{\"sha\":\"abc123def456\"}}}"}}`
	eventFile := filepath.Join(tmpDir, "event.json")
	require.NoError(t, os.WriteFile(eventFile, []byte(eventPayload), 0o644))

	sOpts := statusOpts{
		statusRepo: "org/repo",
		statusNum:  7,
		mintURL:    "https://mint.example.com",
	}

	t.Setenv("GITHUB_EVENT_PATH", eventFile)
	t.Setenv("GITHUB_RUN_ID", "run-42")

	n, err := setupStatusNotifier(tmpDir, "code", sOpts, printer)
	require.NoError(t, err)
	assert.NotNil(t, n)
}

func TestEmitDiagnostic_Warning(t *testing.T) {
	var buf bytes.Buffer
	printer := ui.New(&buf)

	diag := harness.Diagnostic{
		Severity: harness.SeverityWarning,
		Field:    "role",
		Message:  "test warning message",
	}
	emitDiagnostic(printer, diag)

	output := buf.String()
	assert.Contains(t, output, "warning")
	assert.Contains(t, output, "role")
	assert.Contains(t, output, "test warning message")
}

func TestEmitDiagnostic_Error(t *testing.T) {
	var buf bytes.Buffer
	printer := ui.New(&buf)

	diag := harness.Diagnostic{
		Severity: harness.SeverityError,
		Field:    "agent",
		Message:  "test error message",
	}
	emitDiagnostic(printer, diag)

	output := buf.String()
	assert.Contains(t, output, "error")
	assert.Contains(t, output, "agent")
	assert.Contains(t, output, "test error message")
}

func TestEmitDiagnosticWithContext(t *testing.T) {
	var buf bytes.Buffer
	printer := ui.New(&buf)

	diag := harness.Diagnostic{
		Severity: harness.SeverityWarning,
		Field:    "role",
		Message:  "role is not set",
	}
	emitDiagnosticWithContext(printer, "triage", diag)

	output := buf.String()
	assert.Contains(t, output, "triage")
	assert.Contains(t, output, "warning")
	assert.Contains(t, output, "role")
}

func TestRunAgent_ErrorOnMissingRole(t *testing.T) {
	// Verifies that runAgent fails with a hard error when harness has no role.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "agents", "code.md"),
		[]byte("You are a coding agent."),
		0o644,
	))
	// Harness without role field
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\n"),
		0o644,
	))

	var buf bytes.Buffer
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(&buf)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid harness: role field is required")
}

func TestWriteMetricsJSON(t *testing.T) {
	dir := t.TempDir()
	m := aggregateMetrics{
		NumTurns:     12,
		TotalCostUSD: 0.58,
		Iterations:   2,
		ToolCalls:    34,
	}
	m.TokenUsage.Input = 18000
	m.TokenUsage.Output = 5200

	if err := writeMetricsJSON(dir, m); err != nil {
		t.Fatalf("writeMetricsJSON failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "metrics.json"))
	if err != nil {
		t.Fatalf("reading metrics.json: %v", err)
	}

	var got aggregateMetrics
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshalling metrics.json: %v", err)
	}

	if got.NumTurns != 12 {
		t.Errorf("num_turns = %d, want 12", got.NumTurns)
	}
	if got.TotalCostUSD != 0.58 {
		t.Errorf("total_cost_usd = %f, want 0.58", got.TotalCostUSD)
	}
	if got.TokenUsage.Input != 18000 {
		t.Errorf("token_usage.input = %d, want 18000", got.TokenUsage.Input)
	}
	if got.TokenUsage.Output != 5200 {
		t.Errorf("token_usage.output = %d, want 5200", got.TokenUsage.Output)
	}
	if got.Iterations != 2 {
		t.Errorf("iterations = %d, want 2", got.Iterations)
	}
	if got.ToolCalls != 34 {
		t.Errorf("tool_calls = %d, want 34", got.ToolCalls)
	}
}
