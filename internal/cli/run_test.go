package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/fetchsvc"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/mintclient"
	"github.com/fullsend-ai/fullsend/internal/resolve"
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

// useFakeOpenshell prepends testdata/ to PATH so the stub openshell binary
// is found instead of a real installation, causing tests to fail fast at
// sandbox.CheckGateway instead of actually running agents.
func useFakeOpenshell(t *testing.T) {
	t.Helper()
	testdataDir, err := filepath.Abs("testdata")
	require.NoError(t, err)
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", testdataDir+string(filepath.ListSeparator)+origPath)
}

// useFakeOpenshellProviders uses a stub that passes CheckGateway and handles
// provider/profile/sandbox commands, allowing tests to exercise the full
// provider/profile orchestration block in runAgent.
func useFakeOpenshellProviders(t *testing.T) {
	t.Helper()
	stubDir, err := filepath.Abs(filepath.Join("testdata", "providers-stub"))
	require.NoError(t, err)
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", stubDir+string(filepath.ListSeparator)+origPath)
}

func TestRunAgent_HarnessLoadPipeline(t *testing.T) {
	// Exercises the early runAgent pipeline: absFullsendDir, policy,
	// org config loading, LoadWithBase, baseDeps, ResolveRelativeTo.
	// The function fails later at sandbox.CheckGateway (stub exits 1),
	// but by then all harness-loading code paths are covered.
	useFakeOpenshell(t)
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
	useFakeOpenshell(t)
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
	useFakeOpenshell(t)
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
	useFakeOpenshell(t)
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

func TestRunAgent_PerRepoConfig(t *testing.T) {
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
		[]byte("version: \"1\"\nroles:\n  - triage\n  - coder\nallowed_remote_resources:\n  - \"https://example.com/\"\n"),
		0o644,
	))

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openshell")
}

func TestTryLoadFullsendConfig_PerRepoFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"version: \"1\"\nroles:\n  - triage\nallowed_remote_resources:\n  - \"https://example.com/\"\nagents:\n  - name: lint\n    source: harness/lint.yaml\n",
	), 0o644))

	printer := ui.New(io.Discard)
	cfg := tryLoadFullsendConfig(path, printer)
	require.NotNil(t, cfg)
	expected := []string{
		"https://example.com/",
		"https://raw.githubusercontent.com/fullsend-ai/fullsend/",
		"https://raw.githubusercontent.com/fullsend-ai/agents/",
	}
	assert.Equal(t, expected, cfg.AllowedResources())
	require.Len(t, cfg.AgentEntries(), 1)
	assert.Equal(t, "lint", cfg.AgentEntries()[0].Name)
	if prc, ok := cfg.(config.PerRepoConfigReader); ok {
		assert.Equal(t, []string{"triage"}, prc.ConfigRoles())
	} else {
		assert.Equal(t, []string{"triage"}, cfg.(config.OrgConfigReader).OrgRepoDefaults().Roles)
	}
}

func TestTryLoadFullsendConfig_MissingFile(t *testing.T) {
	printer := ui.New(io.Discard)
	cfg := tryLoadFullsendConfig("/nonexistent/config.yaml", printer)
	assert.Nil(t, cfg)
}

func TestTryLoadFullsendConfig_UnreadableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 1"), 0o644))
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { os.Chmod(path, 0o644) })

	printer := ui.New(io.Discard)
	cfg := tryLoadFullsendConfig(path, printer)
	assert.Nil(t, cfg)
}

func TestTryLoadFullsendConfig_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("[[[not yaml"), 0o644))

	printer := ui.New(io.Discard)
	cfg := tryLoadFullsendConfig(path, printer)
	assert.Nil(t, cfg)
}

func TestRequireFullsendConfig_MissingFile(t *testing.T) {
	printer := ui.New(io.Discard)
	cfg, err := requireFullsendConfig("/nonexistent/config.yaml", printer)
	assert.Nil(t, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "URL-referenced resources require a config.yaml")
}

func TestRequireFullsendConfig_UnreadableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 1"), 0o644))
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { os.Chmod(path, 0o644) })

	printer := ui.New(io.Discard)
	cfg, err := requireFullsendConfig(path, printer)
	assert.Nil(t, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading fullsend config for remote resource validation")
}

func TestRequireFullsendConfig_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("[[[not yaml"), 0o644))

	printer := ui.New(io.Discard)
	cfg, err := requireFullsendConfig(path, printer)
	assert.Nil(t, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing org config")
}

func TestRequireFullsendConfig_PerRepoFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"version: \"1\"\nroles:\n  - triage\nallowed_remote_resources:\n  - \"https://example.com/\"\n",
	), 0o644))

	printer := ui.New(io.Discard)
	cfg, err := requireFullsendConfig(path, printer)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	expected := []string{
		"https://example.com/",
		"https://raw.githubusercontent.com/fullsend-ai/fullsend/",
		"https://raw.githubusercontent.com/fullsend-ai/agents/",
	}
	assert.Equal(t, expected, cfg.AllowedResources())
	if prc, ok := cfg.(config.PerRepoConfigReader); ok {
		assert.Equal(t, []string{"triage"}, prc.ConfigRoles())
	} else {
		assert.Equal(t, []string{"triage"}, cfg.(config.OrgConfigReader).OrgRepoDefaults().Roles)
	}
}

func TestIsPerRepoYAML(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{"per-repo with roles", "version: '1'\nroles:\n  - triage\n", true},
		{"org with dispatch", "version: '1'\ndispatch:\n  platform: github\n", false},
		{"org with repos", "version: '1'\nrepos:\n  acme/widget:\n    enabled: true\n", false},
		{"org with dispatch and roles", "version: '1'\ndispatch:\n  platform: github\nroles:\n  - triage\n", false},
		{"org with inference", "version: '1'\ninference:\n  provider: vertex\n", false},
		{"org with defaults", "version: '1'\ndefaults:\n  roles:\n    - triage\n", false},
		{"per-repo without roles", "version: '1'\nkill_switch: false\n", true},
		{"minimal (no discriminator)", "version: '1'\n", true},
		{"invalid yaml", "[[[bad", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, config.IsPerRepoYAML([]byte(tt.yaml)))
		})
	}
}

func TestTryLoadFullsendConfig_PerRepoMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("roles:\n  triage: true\n"), 0o644))

	printer := ui.New(io.Discard)
	cfg := tryLoadFullsendConfig(path, printer)
	assert.Nil(t, cfg)
}

func TestTryLoadFullsendConfig_OrgConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"version: \"1\"\ndispatch:\n  platform: github\nallowed_remote_resources:\n  - \"https://example.com/\"\n",
	), 0o644))

	printer := ui.New(io.Discard)
	cfg := tryLoadFullsendConfig(path, printer)
	require.NotNil(t, cfg)
	assert.Equal(t, "github", cfg.(config.OrgConfigReader).DispatchSettings().Platform)
	expected := []string{
		"https://example.com/",
		"https://raw.githubusercontent.com/fullsend-ai/fullsend/",
		"https://raw.githubusercontent.com/fullsend-ai/agents/",
	}
	assert.Equal(t, expected, cfg.AllowedResources())
}

func TestTryLoadFullsendConfig_ExplicitEmptyAllowlist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"version: \"1\"\ndispatch:\n  platform: github\nallowed_remote_resources: []\n",
	), 0o644))

	printer := ui.New(io.Discard)
	cfg := tryLoadFullsendConfig(path, printer)
	require.NotNil(t, cfg)
	assert.Empty(t, cfg.AllowedResources(), "explicit empty [] must preserve deny-all")
}

func TestTryLoadFullsendConfig_OmittedAllowlist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"version: \"1\"\ndispatch:\n  platform: github\n",
	), 0o644))

	printer := ui.New(io.Discard)
	cfg := tryLoadFullsendConfig(path, printer)
	require.NotNil(t, cfg)
	assert.Equal(t, config.DefaultAllowedRemoteResources(), cfg.AllowedResources(),
		"omitted field must get defaults")
}

func TestRequireFullsendConfig_OrgGetsDefaultAllowlist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"version: \"1\"\norg: example\n",
	), 0o644))

	printer := ui.New(io.Discard)
	cfg, err := requireFullsendConfig(path, printer)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, config.DefaultAllowedRemoteResources(), cfg.AllowedResources())
}

func TestRequireFullsendConfig_ExplicitEmptyAllowlist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"version: \"1\"\ndispatch:\n  platform: github\nallowed_remote_resources: []\n",
	), 0o644))

	printer := ui.New(io.Discard)
	cfg, err := requireFullsendConfig(path, printer)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Empty(t, cfg.AllowedResources(), "explicit empty [] must preserve deny-all")
}

func TestRequireFullsendConfig_PerRepoMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("roles:\n  triage: true\n"), 0o644))

	printer := ui.New(io.Discard)
	cfg, err := requireFullsendConfig(path, printer)
	assert.Nil(t, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing per-repo config")
}

func TestRunAgent_MalformedOrgConfig(t *testing.T) {
	// A malformed config.yaml should produce a warning but not prevent
	// local-only harnesses from proceeding through the pipeline.
	useFakeOpenshell(t)
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
	useFakeOpenshell(t)
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
	useFakeOpenshell(t)
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
	assert.Contains(t, err.Error(), "URL-referenced resources require a config.yaml")
}

func TestRunAgent_WithURLBase(t *testing.T) {
	// Harness with a URL base — exercises the baseDeps logging loop.
	useFakeOpenshell(t)
	baseContent := []byte("agent: agents/shared.md\nrole: test\n")
	baseHash := fetch.ComputeSHA256(baseContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/base.yaml":        baseContent,
		"/agents/shared.md": []byte("# shared agent"),
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

func TestRunAgent_ProviderProfileOrchestration(t *testing.T) {
	// Exercises the provider/profile orchestration block in runAgent
	// (steps 2a-2c): CheckGateway, checkProviderProfileIntegrity,
	// EnableProvidersV2, ImportProfile, EnsureProvider, CreateWithRetry.
	// Uses the providers-stub that passes all openshell commands.
	useFakeOpenshellProviders(t)

	profileContent := []byte("id: anthropic\nname: Anthropic\n")
	profileHash := fetch.ComputeSHA256(profileContent)

	providerContent := []byte("name: my-claude\ntype: anthropic\ncredentials:\n  API_KEY: ${MY_API_KEY}\n")
	providerHash := fetch.ComputeSHA256(providerContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/profiles/anthropic.yaml":  profileContent,
		"/providers/my-claude.yaml": providerContent,
	})

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
		[]byte(fmt.Sprintf(`agent: agents/code.md
role: test
providers:
  - "%s/providers/my-claude.yaml#sha256=%s"
openshell:
  profiles:
    - "%s/profiles/anthropic.yaml#sha256=%s"
`, srv.URL, providerHash, srv.URL, profileHash)),
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
	// The test will fail after the orchestration block (e.g. during
	// bootstrapCommon or pre-script setup), but it must NOT fail at
	// the gateway check or provider/profile steps.
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "gateway check failed")
	assert.NotContains(t, err.Error(), "enabling providers v2")
	assert.NotContains(t, err.Error(), "importing profile")
	assert.NotContains(t, err.Error(), "ensuring provider")
	assert.NotContains(t, err.Error(), "creating sandbox")
}

func TestRunAgent_URLBaseNoOrgConfig(t *testing.T) {
	useFakeOpenshell(t)
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
	assert.Contains(t, err.Error(), "URL-referenced resources require a config.yaml")
}

func TestRunAgent_URLBaseMalformedOrgConfig(t *testing.T) {
	useFakeOpenshell(t)
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

func TestBuildScanContextCommand_AcceptsAdoptedTraceID(t *testing.T) {
	// A trace id adopted from an inbound W3C traceparent (issue #2779) is
	// dashed hex but not UUID v4; it must survive validation, not be replaced
	// with the "invalid-trace-id" sentinel.
	traceID := "4f3a9c1b-2d8e-0a7c-1f0b-1e2d3c4a5b6d"
	cmd := buildScanContextCommand("/sandbox/workspace/repo", traceID)
	assert.Contains(t, cmd, "FULLSEND_TRACE_ID='"+traceID+"'")
	assert.NotContains(t, cmd, "invalid-trace-id")
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

func TestRunAgent_ConfigAgentLocalPath(t *testing.T) {
	useFakeOpenshell(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "agents", "custom.md"),
		[]byte("You are a custom agent."),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "custom.yaml"),
		[]byte("agent: agents/custom.md\nrole: test\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("agents:\n  - harness/custom.yaml\nallowed_remote_resources:\n  - \"https://example.com/\"\n"),
		0o644,
	))

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "custom", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openshell")
}

func TestRunAgent_ConfigAgentURL(t *testing.T) {
	useFakeOpenshell(t)
	harnessContent := []byte("agent: agents/remote.md\nrole: test\n")
	harnessHash := fetch.ComputeSHA256(harnessContent)

	srv, policy := newLockTestServer(t, map[string][]byte{
		"/harness/triage.yaml": harnessContent,
		"/agents/remote.md":    []byte("You are a remote agent."),
	})

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "agents", "remote.md"),
		[]byte("You are a remote agent."),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte(fmt.Sprintf("agents:\n  - \"%s/harness/triage.yaml#sha256=%s\"\nallowed_remote_resources:\n  - \"%s/\"\n", srv.URL, harnessHash, srv.URL)),
		0o644,
	))

	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "triage", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openshell")
}

func TestRunAgent_ConfigAgentOverridesScaffold(t *testing.T) {
	useFakeOpenshell(t)
	// When config has an agent with the same name as a scaffold agent,
	// the config source is used instead of the scaffold wrapper.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "agents", "code.md"),
		[]byte("You are a custom code agent."),
		0o644,
	))
	// Config-driven local path agent named "code" — should take precedence
	// over any scaffold "code" harness wrapper.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: test\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("agents:\n  - harness/code.yaml\nallowed_remote_resources:\n  - \"https://example.com/\"\n"),
		0o644,
	))

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openshell")
}

func TestRunAgent_ScaffoldFallback(t *testing.T) {
	useFakeOpenshell(t)
	// When config has agents but the requested agent is not in config,
	// fall back to disk-based resolution.
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
	// Config has agents but "code" is not among them — should fall back to disk.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("agents:\n  - harness/other.yaml\nallowed_remote_resources:\n  - \"https://example.com/\"\n"),
		0o644,
	))

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openshell")
}

func TestRunAgent_UnknownAgentName(t *testing.T) {
	useFakeOpenshell(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	// Config has agents but "nonexistent" is not among them, and no file on disk.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("agents:\n  - harness/other.yaml\nallowed_remote_resources:\n  - \"https://example.com/\"\n"),
		0o644,
	))

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(io.Discard)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "nonexistent", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harness file not found")
}

func TestResolveAgentSource_NoConfig(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: test\n"),
		0o644,
	))

	printer := ui.New(io.Discard)
	path, deps, err := resolveAgentSource(context.Background(), dir, "code", nil, nil, harness.ComposeOpts{}, printer)
	require.NoError(t, err)
	assert.Contains(t, path, "code.yaml")
	assert.Empty(t, deps)
}

// canonTempDir returns t.TempDir() with symlinks resolved, so equality
// assertions against containedLocalPath's symlink-resolved output hold on
// hosts where the temp dir sits behind a symlink (e.g. macOS, where /var
// is a symlink to /private/var).
func canonTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return dir
}

func TestResolveAgentSource_ConfigLocalPath(t *testing.T) {
	dir := canonTempDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "custom.yaml"),
		[]byte("agent: agents/custom.md\nrole: test\n"),
		0o644,
	))

	orgCfg := config.NewOrgConfig(nil, nil, nil, "", "")
	orgCfg.SetAgents([]config.AgentEntry{
		{Source: "harness/custom.yaml"},
	})
	orgCfg.SetAllowedRemoteResources([]string{"https://example.com/"})

	printer := ui.New(io.Discard)
	path, deps, err := resolveAgentSource(context.Background(), dir, "custom", nil, orgCfg, harness.ComposeOpts{}, printer)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "harness", "custom.yaml"), path)
	assert.Empty(t, deps)
}

func TestResolveAgentSource_ConfigLocalPathNotFound(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	orgCfg := config.NewOrgConfig(nil, nil, nil, "", "")
	orgCfg.SetAgents([]config.AgentEntry{
		{Source: "harness/missing.yaml"},
	})
	orgCfg.SetAllowedRemoteResources([]string{"https://example.com/"})

	printer := ui.New(io.Discard)
	_, _, err := resolveAgentSource(context.Background(), dir, "missing", nil, orgCfg, harness.ComposeOpts{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config agent missing")
}

func TestResolveAgentSource_ConfigLocalPathAbsoluteRejected(t *testing.T) {
	dir := t.TempDir()

	orgCfg := config.NewOrgConfig(nil, nil, nil, "", "")
	orgCfg.SetAgents([]config.AgentEntry{
		{Source: "/etc/evil.yaml"},
	})
	orgCfg.SetAllowedRemoteResources([]string{"https://example.com/"})

	printer := ui.New(io.Discard)
	_, _, err := resolveAgentSource(context.Background(), dir, "evil", nil, orgCfg, harness.ComposeOpts{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute paths")
}

func TestResolveAgentSource_ConfigLocalPathTraversalRejected(t *testing.T) {
	dir := t.TempDir()

	orgCfg := config.NewOrgConfig(nil, nil, nil, "", "")
	orgCfg.SetAgents([]config.AgentEntry{
		{Source: "harness/../../etc/passwd"},
	})
	orgCfg.SetAllowedRemoteResources([]string{"https://example.com/"})

	printer := ui.New(io.Discard)
	_, _, err := resolveAgentSource(context.Background(), dir, "passwd", nil, orgCfg, harness.ComposeOpts{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path traversal")
}

func TestResolveAgentSource_AgentsRepoFallback_UnknownAgent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	fakeClient := forge.NewFakeClient()
	printer := ui.New(io.Discard)
	_, _, err := resolveAgentSource(context.Background(), dir, "nonexistent", fakeClient, nil, harness.ComposeOpts{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harness file not found")
}

func TestResolveAgentSource_AgentsRepoFallback_Offline(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	fakeClient := forge.NewFakeClient()
	printer := ui.New(io.Discard)
	opts := harness.ComposeOpts{
		FetchPolicy: fetch.FetchPolicy{Offline: true},
	}
	_, _, err := resolveAgentSource(context.Background(), dir, "triage", fakeClient, nil, opts, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harness file not found")
}

func TestResolveAgentSource_AgentsRepoFallback_NoClient(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))

	printer := ui.New(io.Discard)
	_, _, err := resolveAgentSource(context.Background(), dir, "triage", nil, nil, harness.ComposeOpts{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harness file not found")
}

func TestResolveAgentSource_AgentsRepoFallback_DiskFallback(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "triage.yaml"),
		[]byte("agent: agents/triage.md\nrole: test\n"),
		0o644,
	))

	printer := ui.New(io.Discard)
	path, deps, err := resolveAgentSource(context.Background(), dir, "triage", nil, nil, harness.ComposeOpts{}, printer)
	require.NoError(t, err)
	assert.Contains(t, path, "triage.yaml")
	assert.Empty(t, deps)
}

func TestResolveAgentSource_DisabledAgentBlocksFallback(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	// Place a triage harness on disk to prove fallback is NOT used.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "triage.yaml"),
		[]byte("agent: agents/triage.md\nrole: test\n"),
		0o644,
	))

	f := false
	orgCfg := config.NewOrgConfig(nil, nil, nil, "", "")
	orgCfg.SetAgents([]config.AgentEntry{
		{Name: "triage", Enabled: &f},
	})

	printer := ui.New(io.Discard)
	_, _, err := resolveAgentSource(context.Background(), dir, "triage", nil, orgCfg, harness.ComposeOpts{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "explicitly disabled")
}

func TestResolveAgentSource_DisabledFirstPartyAgentBlocksFallback(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	// Place a retro harness on disk so it would resolve if fallback were allowed.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "retro.yaml"),
		[]byte("agent: agents/retro.md\nrole: test\n"),
		0o644,
	))

	f := false
	orgCfg := config.NewOrgConfig(nil, nil, nil, "", "")
	orgCfg.SetAgents([]config.AgentEntry{
		{Name: "retro", Enabled: &f},
	})

	fakeClient := forge.NewFakeClient()
	printer := ui.New(io.Discard)
	_, _, err := resolveAgentSource(context.Background(), dir, "retro", fakeClient, orgCfg, harness.ComposeOpts{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "explicitly disabled")
}

func TestResolveAgentSource_SuppressionOnlyEntryBlocksFallback(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "retro.yaml"),
		[]byte("agent: agents/retro.md\nrole: test\n"),
		0o644,
	))

	// Suppression-only entry: enabled=false, no source.
	f := false
	orgCfg := config.NewOrgConfig(nil, nil, nil, "", "")
	orgCfg.SetAgents([]config.AgentEntry{
		{Name: "retro", Enabled: &f},
	})

	printer := ui.New(io.Discard)
	_, _, err := resolveAgentSource(context.Background(), dir, "retro", nil, orgCfg, harness.ComposeOpts{}, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "explicitly disabled")
}

func TestResolveAgentSource_EnabledAgentStillResolves(t *testing.T) {
	dir := canonTempDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "custom.yaml"),
		[]byte("agent: agents/custom.md\nrole: test\n"),
		0o644,
	))

	tr := true
	orgCfg := config.NewOrgConfig(nil, nil, nil, "", "")
	orgCfg.SetAgents([]config.AgentEntry{
		{Name: "custom", Source: "harness/custom.yaml", Enabled: &tr},
	})

	printer := ui.New(io.Discard)
	path, _, err := resolveAgentSource(context.Background(), dir, "custom", nil, orgCfg, harness.ComposeOpts{}, printer)
	require.NoError(t, err)
	assert.Contains(t, path, "custom.yaml")
}

func TestTryAgentsRepoFallback_UnknownAgent(t *testing.T) {
	fakeClient := forge.NewFakeClient()
	printer := ui.New(io.Discard)
	_, _, ok := tryAgentsRepoFallback(context.Background(), "custom-agent", fakeClient, harness.ComposeOpts{}, printer)
	assert.False(t, ok)
}

func TestTryAgentsRepoFallback_Offline(t *testing.T) {
	fakeClient := forge.NewFakeClient()
	printer := ui.New(io.Discard)
	opts := harness.ComposeOpts{FetchPolicy: fetch.FetchPolicy{Offline: true}}
	_, _, ok := tryAgentsRepoFallback(context.Background(), "triage", fakeClient, opts, printer)
	assert.False(t, ok)
}

func TestTryAgentsRepoFallback_NilClient(t *testing.T) {
	printer := ui.New(io.Discard)
	_, _, ok := tryAgentsRepoFallback(context.Background(), "triage", nil, harness.ComposeOpts{}, printer)
	assert.False(t, ok)
}

func TestTryAgentsRepoFallback_GetRefError(t *testing.T) {
	fakeClient := forge.NewFakeClient()
	fakeClient.Errors["GetRef"] = fmt.Errorf("rate limited")
	printer := ui.New(io.Discard)
	_, _, ok := tryAgentsRepoFallback(context.Background(), "triage", fakeClient, harness.ComposeOpts{}, printer)
	assert.False(t, ok)
}

func TestTryAgentsRepoFallback_NotAllowlisted(t *testing.T) {
	fakeClient := forge.NewFakeClient()
	fakeClient.Refs["fullsend-ai/agents/tags/v0"] = "abc123def456789012345678901234567890abcd"
	printer := ui.New(io.Discard)
	opts := harness.ComposeOpts{
		OrgAllowlist: []string{"https://example.com/"},
	}
	_, _, ok := tryAgentsRepoFallback(context.Background(), "triage", fakeClient, opts, printer)
	assert.False(t, ok)
}

func TestTryAgentsRepoFallback_ExplicitlyEmptyAllowlist(t *testing.T) {
	fakeClient := forge.NewFakeClient()
	fakeClient.Refs["fullsend-ai/agents/tags/v0"] = "abc123def456789012345678901234567890abcd"
	printer := ui.New(io.Discard)
	opts := harness.ComposeOpts{
		OrgAllowlist: []string{},
	}
	_, _, ok := tryAgentsRepoFallback(context.Background(), "triage", fakeClient, opts, printer)
	assert.False(t, ok)
}

func TestTryAgentsRepoFallback_CaseNormalization(t *testing.T) {
	fakeClient := forge.NewFakeClient()
	fakeClient.Refs["fullsend-ai/agents/tags/v0"] = "abc123def456789012345678901234567890abcd"
	printer := ui.New(io.Discard)

	// "Triage" should pass the known-agent check but would have caused a 404
	// before the case-normalization fix. Now it uses "triage" in the URL.
	_, _, ok := tryAgentsRepoFallback(context.Background(), "Triage", fakeClient, harness.ComposeOpts{}, printer)
	// Fallback skips because fetch fails (no HTTP server), but it shouldn't
	// panic and should get past the known-agent gate.
	assert.False(t, ok)
}

func TestTryAgentsRepoFallback_ShortSHA(t *testing.T) {
	fakeClient := forge.NewFakeClient()
	fakeClient.Refs["fullsend-ai/agents/tags/v0"] = "abc"
	printer := ui.New(io.Discard)

	// Short SHA fails hex validation — exercises both validation and bounds guard.
	_, _, ok := tryAgentsRepoFallback(context.Background(), "triage", fakeClient, harness.ComposeOpts{}, printer)
	assert.False(t, ok)
}

func TestTryAgentsRepoFallback_InvalidSHA(t *testing.T) {
	fakeClient := forge.NewFakeClient()
	fakeClient.Refs["fullsend-ai/agents/tags/v0"] = "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"
	printer := ui.New(io.Discard)

	// Non-hex characters should be rejected by SHA validation.
	_, _, ok := tryAgentsRepoFallback(context.Background(), "triage", fakeClient, harness.ComposeOpts{}, printer)
	assert.False(t, ok)
}

func TestTryAgentsRepoFallback_AllKnownAgents(t *testing.T) {
	for _, name := range []string{"triage", "code", "fix", "review", "retro", "prioritize"} {
		t.Run(name, func(t *testing.T) {
			fakeClient := forge.NewFakeClient()
			printer := ui.New(io.Discard)
			// Should pass the known-agent gate (not return false immediately).
			// GetBranchRef will fail since no ref is set, confirming we got past the gate.
			_, _, ok := tryAgentsRepoFallback(context.Background(), name, fakeClient, harness.ComposeOpts{}, printer)
			assert.False(t, ok)
		})
	}
}

func TestTryAgentsRepoFallback_SuccessPath(t *testing.T) {
	harnessContent := []byte("agent: agents/triage.md\nrole: test\n")
	fakeSHA := "abcdef1234567890abcdef1234567890abcdef12"

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/" + fakeSHA + "/harness/triage.yaml"
		if r.URL.Path == expectedPath {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(harnessContent)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	hostPort := strings.TrimPrefix(srv.URL, "https://")
	hostname, port, _ := net.SplitHostPort(hostPort)

	tlsCfg := srv.TLS.Clone()
	tlsCfg.InsecureSkipVerify = true
	policy := fetch.NewTestPolicy(tlsCfg, []string{hostname}, []string{port})

	orig := defaultAgentsRepoURLPrefix
	defaultAgentsRepoURLPrefix = srv.URL + "/"
	t.Cleanup(func() { defaultAgentsRepoURLPrefix = orig })

	workDir := t.TempDir()

	fakeClient := forge.NewFakeClient()
	fakeClient.Refs["fullsend-ai/agents/tags/v0"] = fakeSHA

	printer := ui.New(io.Discard)
	opts := harness.ComposeOpts{
		WorkspaceRoot: workDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{srv.URL + "/"},
	}

	path, deps, ok := tryAgentsRepoFallback(context.Background(), "triage", fakeClient, opts, printer)
	require.True(t, ok, "expected fallback to succeed")
	assert.NotEmpty(t, path)
	assert.Len(t, deps, 1)
	assert.Contains(t, deps[0].URL, fakeSHA)
	assert.Equal(t, "file", deps[0].Type)
	assert.NotEmpty(t, deps[0].SHA256)
}

func TestTryAgentsRepoFallback_AuditLog(t *testing.T) {
	harnessContent := []byte("agent: agents/triage.md\nrole: test\n")
	fakeSHA := "abcdef1234567890abcdef1234567890abcdef12"

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/" + fakeSHA + "/harness/triage.yaml"
		if r.URL.Path == expectedPath {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(harnessContent)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	hostPort := strings.TrimPrefix(srv.URL, "https://")
	hostname, port, _ := net.SplitHostPort(hostPort)

	tlsCfg := srv.TLS.Clone()
	tlsCfg.InsecureSkipVerify = true
	policy := fetch.NewTestPolicy(tlsCfg, []string{hostname}, []string{port})

	orig := defaultAgentsRepoURLPrefix
	defaultAgentsRepoURLPrefix = srv.URL + "/"
	t.Cleanup(func() { defaultAgentsRepoURLPrefix = orig })

	workDir := t.TempDir()
	auditLog := filepath.Join(workDir, "audit.jsonl")

	fakeClient := forge.NewFakeClient()
	fakeClient.Refs["fullsend-ai/agents/tags/v0"] = fakeSHA

	printer := ui.New(io.Discard)
	opts := harness.ComposeOpts{
		WorkspaceRoot: workDir,
		FetchPolicy:   policy,
		OrgAllowlist:  []string{srv.URL + "/"},
		AuditLogPath:  auditLog,
		TraceID:       "test-trace-123",
	}

	_, _, ok := tryAgentsRepoFallback(context.Background(), "triage", fakeClient, opts, printer)
	require.True(t, ok, "expected fallback to succeed")

	auditContent, err := os.ReadFile(auditLog)
	require.NoError(t, err)
	assert.Contains(t, string(auditContent), fakeSHA)
	assert.Contains(t, string(auditContent), "test-trace-123")
	assert.Contains(t, string(auditContent), `"fetch_type":"static"`)
}

func TestTryAgentsRepoFallback_CachePutFailure(t *testing.T) {
	harnessContent := []byte("agent: agents/triage.md\nrole: test\n")
	fakeSHA := "abcdef1234567890abcdef1234567890abcdef12"

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/" + fakeSHA + "/harness/triage.yaml"
		if r.URL.Path == expectedPath {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(harnessContent)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	hostPort := strings.TrimPrefix(srv.URL, "https://")
	hostname, port, _ := net.SplitHostPort(hostPort)

	tlsCfg := srv.TLS.Clone()
	tlsCfg.InsecureSkipVerify = true
	policy := fetch.NewTestPolicy(tlsCfg, []string{hostname}, []string{port})

	orig := defaultAgentsRepoURLPrefix
	defaultAgentsRepoURLPrefix = srv.URL + "/"
	t.Cleanup(func() { defaultAgentsRepoURLPrefix = orig })

	fakeClient := forge.NewFakeClient()
	fakeClient.Refs["fullsend-ai/agents/tags/v0"] = fakeSHA

	printer := ui.New(io.Discard)
	opts := harness.ComposeOpts{
		WorkspaceRoot: "/nonexistent/path/that/will/fail",
		FetchPolicy:   policy,
		OrgAllowlist:  []string{srv.URL + "/"},
	}

	_, _, ok := tryAgentsRepoFallback(context.Background(), "triage", fakeClient, opts, printer)
	assert.False(t, ok, "expected fallback to fail when cache write fails")
}

func TestTryAgentsRepoFallback_FetchURLError(t *testing.T) {
	fakeSHA := "abcdef1234567890abcdef1234567890abcdef12"

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	hostPort := strings.TrimPrefix(srv.URL, "https://")
	hostname, port, _ := net.SplitHostPort(hostPort)

	tlsCfg := srv.TLS.Clone()
	tlsCfg.InsecureSkipVerify = true
	policy := fetch.NewTestPolicy(tlsCfg, []string{hostname}, []string{port})

	orig := defaultAgentsRepoURLPrefix
	defaultAgentsRepoURLPrefix = srv.URL + "/"
	t.Cleanup(func() { defaultAgentsRepoURLPrefix = orig })

	fakeClient := forge.NewFakeClient()
	fakeClient.Refs["fullsend-ai/agents/tags/v0"] = fakeSHA

	printer := ui.New(io.Discard)
	opts := harness.ComposeOpts{
		WorkspaceRoot: t.TempDir(),
		FetchPolicy:   policy,
		OrgAllowlist:  []string{srv.URL + "/"},
	}

	_, _, ok := tryAgentsRepoFallback(context.Background(), "triage", fakeClient, opts, printer)
	assert.False(t, ok)
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

func TestValidationEnv_IncludesSchemaWhenSet(t *testing.T) {
	h := &harness.Harness{
		RunnerEnv: map[string]string{"FOO": "bar"},
		ValidationLoop: &harness.ValidationLoop{
			Script: "scripts/validate.sh",
			Schema: "/tmp/test-schema.json",
		},
	}
	env := validationEnv(h, "/repo", "/run")
	assert.Contains(t, env, "FULLSEND_OUTPUT_SCHEMA=/tmp/test-schema.json")
	assert.Contains(t, env, "TARGET_REPO_DIR=/repo")
	assert.Contains(t, env, "FULLSEND_RUN_DIR=/run")
	assert.Contains(t, env, "FOO=bar")
}

func TestValidationEnv_OmitsSchemaWhenEmpty(t *testing.T) {
	h := &harness.Harness{
		RunnerEnv: map[string]string{"FOO": "bar"},
		ValidationLoop: &harness.ValidationLoop{
			Script: "scripts/validate.sh",
		},
	}
	env := validationEnv(h, "/repo", "/run")
	for _, e := range env {
		assert.False(t, strings.HasPrefix(e, "FULLSEND_OUTPUT_SCHEMA="),
			"FULLSEND_OUTPUT_SCHEMA should not be set when Schema is empty")
	}
}

func TestValidationEnv_OmitsSchemaWhenNoValidationLoop(t *testing.T) {
	h := &harness.Harness{
		RunnerEnv: map[string]string{"FOO": "bar"},
	}
	env := validationEnv(h, "/repo", "/run")
	for _, e := range env {
		assert.False(t, strings.HasPrefix(e, "FULLSEND_OUTPUT_SCHEMA="),
			"FULLSEND_OUTPUT_SCHEMA should not be set when ValidationLoop is nil")
	}
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

func TestPRHeadSHAFromEventPath_NullPullRequest(t *testing.T) {
	// issue_comment events dispatch with "pull_request": null when the
	// dispatch layer hasn't resolved the PR head. The function should
	// return empty (not panic) so the caller can fall back.
	eventJSON := `{
		"inputs": {
			"event_payload": "{\"issue\":{\"number\":4100},\"pull_request\":null,\"comment\":{\"body\":\"/fs-review\"}}"
		}
	}`
	f := filepath.Join(t.TempDir(), "event.json")
	require.NoError(t, os.WriteFile(f, []byte(eventJSON), 0o644))

	got := prHeadSHAFromEventPath(f)
	assert.Empty(t, got)
}

func TestPRHeadSHAFromEventPath_IssueCommentWithResolvedPR(t *testing.T) {
	// After the dispatch fix, issue_comment events include a resolved
	// pull_request object with the head SHA from the API.
	eventJSON := `{
		"inputs": {
			"event_payload": "{\"issue\":{\"number\":4100},\"pull_request\":{\"number\":4100,\"head\":{\"ref\":\"pr-4099\",\"sha\":\"03e61fc492fa89592dc4cd0f429ee926154ee8e5\",\"repo\":{\"full_name\":\"org/repo\"}}},\"comment\":{\"body\":\"/fs-review\"}}"
		}
	}`
	f := filepath.Join(t.TempDir(), "event.json")
	require.NoError(t, os.WriteFile(f, []byte(eventJSON), 0o644))

	got := prHeadSHAFromEventPath(f)
	assert.Equal(t, "03e61fc492fa89592dc4cd0f429ee926154ee8e5", got)
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

func TestBootstrapEnv_ValidationLoopSchemaPrecedence(t *testing.T) {
	dir := t.TempDir()
	schemaFile := filepath.Join(dir, "schema.json")
	require.NoError(t, os.WriteFile(schemaFile, []byte(`{"type":"object"}`), 0o644))

	h := &harness.Harness{
		Agent: "agents/test.md",
		ValidationLoop: &harness.ValidationLoop{
			Script: "scripts/validate.sh",
			Schema: schemaFile,
		},
		RunnerEnv: map[string]string{
			"FULLSEND_OUTPUT_SCHEMA": "/should/not/be/used",
		},
	}

	err := bootstrapEnv("nonexistent-sandbox", "/workspace/repo", h, nil)

	// Expected to fail at sandbox operations — the schema code path is
	// exercised before the failure.
	require.Error(t, err)
}

func TestBootstrapEnv_ValidationLoopSchemaFallback(t *testing.T) {
	h := &harness.Harness{
		Agent: "agents/test.md",
		RunnerEnv: map[string]string{
			"FULLSEND_OUTPUT_SCHEMA": "/nonexistent/schema.json",
		},
	}

	err := bootstrapEnv("nonexistent-sandbox", "/workspace/repo", h, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "copying .env file to sandbox")
}

func TestExpandValidationLoopSchema(t *testing.T) {
	dir := t.TempDir()
	schemaDir := filepath.Join(dir, "schemas")
	require.NoError(t, os.MkdirAll(schemaDir, 0o755))
	schemaPath := filepath.Join(schemaDir, "result.json")
	require.NoError(t, os.WriteFile(schemaPath, []byte(`{"type":"object"}`), 0o644))

	expander := func(key string) string {
		if key == "FULLSEND_DIR" {
			return dir
		}
		return ""
	}

	h := &harness.Harness{
		ValidationLoop: &harness.ValidationLoop{
			Schema: "${FULLSEND_DIR}/schemas/result.json",
		},
	}

	if h.ValidationLoop != nil && strings.Contains(h.ValidationLoop.Schema, "${") {
		h.ValidationLoop.Schema = os.Expand(h.ValidationLoop.Schema, expander)
	}

	assert.Equal(t, schemaPath, h.ValidationLoop.Schema)
	_, err := os.Stat(h.ValidationLoop.Schema)
	require.NoError(t, err, "expanded schema path should exist")
}

func TestBuildSandboxEnvLines_FromEnvSandbox(t *testing.T) {
	h := &harness.Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env: &harness.EnvConfig{
			Sandbox: map[string]string{
				"GITHUB_PR_URL": "https://github.com/org/repo/pull/1",
				"GH_TOKEN":      "tok123",
			},
		},
	}

	lines := buildSandboxEnvLines(h)
	assert.Contains(t, lines, "export GH_TOKEN='tok123'")
	assert.Contains(t, lines, "export GITHUB_PR_URL='https://github.com/org/repo/pull/1'")
}

func TestBuildSandboxEnvLines_NilEnv(t *testing.T) {
	h := &harness.Harness{Agent: "agents/test.md", Role: "test"}
	lines := buildSandboxEnvLines(h)
	assert.Nil(t, lines)
}

func TestBuildSandboxEnvLines_EmptySandbox(t *testing.T) {
	h := &harness.Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env:   &harness.EnvConfig{Runner: map[string]string{"FOO": "bar"}},
	}
	lines := buildSandboxEnvLines(h)
	assert.Nil(t, lines)
}

func TestBuildSandboxEnvLines_EscapesSingleQuotes(t *testing.T) {
	h := &harness.Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env: &harness.EnvConfig{
			Sandbox: map[string]string{"MSG": "it's a test"},
		},
	}
	lines := buildSandboxEnvLines(h)
	require.Len(t, lines, 1)
	assert.Equal(t, "export MSG='it'\\''s a test'", lines[0])
}

func TestBuildSandboxEnvLines_SortedKeys(t *testing.T) {
	h := &harness.Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env: &harness.EnvConfig{
			Sandbox: map[string]string{
				"ZZZ": "last",
				"AAA": "first",
			},
		},
	}
	lines := buildSandboxEnvLines(h)
	require.Len(t, lines, 2)
	assert.Equal(t, "export AAA='first'", lines[0])
	assert.Equal(t, "export ZZZ='last'", lines[1])
}

func TestBuildSandboxEnvLines_SkipsInvalidKeys(t *testing.T) {
	h := &harness.Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env: &harness.EnvConfig{
			Sandbox: map[string]string{
				"VALID_KEY":  "ok",
				"bad key":    "spaces",
				"'; rm -rf ": "inject",
			},
		},
	}
	lines := buildSandboxEnvLines(h)
	require.Len(t, lines, 1)
	assert.Equal(t, "export VALID_KEY='ok'", lines[0])
}

func TestBuildSandboxEnvLines_EmptyValue(t *testing.T) {
	h := &harness.Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env: &harness.EnvConfig{
			Sandbox: map[string]string{"EMPTY": ""},
		},
	}
	lines := buildSandboxEnvLines(h)
	require.Len(t, lines, 1)
	assert.Equal(t, "export EMPTY=''", lines[0])
}

func TestBuildSandboxEnvLines_SkipsReservedKeys(t *testing.T) {
	h := &harness.Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env: &harness.EnvConfig{
			Sandbox: map[string]string{
				"CUSTOM_VAR":           "allowed",
				"PATH":                 "/evil",
				"FULLSEND_FETCH_TOKEN": "stolen",
				"FULLSEND_OUTPUT_DIR":  "/tmp/bad",
			},
		},
	}
	lines := buildSandboxEnvLines(h)
	require.Len(t, lines, 1)
	assert.Equal(t, "export CUSTOM_VAR='allowed'", lines[0])
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

func TestSetupFetchService_WithTreeFetcher(t *testing.T) {
	tmpDir := t.TempDir()
	h := &harness.Harness{Agent: "agents/test.md"}
	mockFetcher := func(_ context.Context, _, _, _, _ string) (map[string][]byte, error) {
		return nil, nil
	}

	env, shutdown, err := setupFetchService(
		context.Background(),
		mockFetcher,
		"test-token",
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

func TestSetupFetchService_ResolvesTokenWhenNoGitToken(t *testing.T) {
	tmpDir := t.TempDir()
	h := &harness.Harness{
		Agent:                  "agents/test.md",
		AllowedRemoteResources: []string{"https://github.com/org/"},
	}

	tokenResolved := false
	env, shutdown, err := setupFetchService(
		context.Background(),
		nil,
		"",
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

func TestSetupFetchService_NoTokenNoRemoteResources(t *testing.T) {
	tmpDir := t.TempDir()
	h := &harness.Harness{Agent: "agents/test.md"}

	env, shutdown, err := setupFetchService(
		context.Background(),
		nil,
		"",
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
		"",
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
		"",
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

func TestSetupStatusNotifier_FactoryRejectsMalformedToken(t *testing.T) {
	tmpDir := t.TempDir()
	printer := ui.New(io.Discard)

	origMint := statusMintToken
	statusMintToken = func(_ context.Context, _ mintclient.MintRequest) (*mintclient.MintResult, error) {
		return &mintclient.MintResult{Token: "not-a-valid-token-format!"}, nil
	}
	defer func() { statusMintToken = origMint }()

	sOpts := statusOpts{
		statusRepo: "org/repo",
		statusNum:  7,
		mintURL:    "https://mint.example.com",
	}

	t.Setenv("GITHUB_RUN_ID", "run-42")

	n, err := setupStatusNotifier(tmpDir, "coder", sOpts, printer)
	require.NoError(t, err)

	client, err := n.InvokeClientFactory(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected characters")
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
	useFakeOpenshell(t)
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

// --- mintAgentToken tests ---

func TestMintAgentToken_SkipsWhenNoMintURL(t *testing.T) {
	printer := ui.New(io.Discard)
	minted, _, err := mintAgentToken(context.Background(), "coder", "", printer)
	require.NoError(t, err)
	assert.False(t, minted)
}

func TestMintAgentToken_SkipsWhenNoRole(t *testing.T) {
	printer := ui.New(io.Discard)
	minted, _, err := mintAgentToken(context.Background(), "", "https://mint.example.com", printer)
	require.NoError(t, err)
	assert.False(t, minted)
}

func TestMintAgentToken_CoderRole(t *testing.T) {
	origMint := statusMintToken
	defer func() { statusMintToken = origMint }()

	statusMintToken = func(_ context.Context, req mintclient.MintRequest) (*mintclient.MintResult, error) {
		assert.Equal(t, "https://mint.example.com", req.MintURL)
		assert.Equal(t, "coder", req.Role)
		assert.Equal(t, []string{"my-repo"}, req.Repos)
		return &mintclient.MintResult{Token: "ghs_coder_token", ExpiresAt: "2026-06-15T12:00:00Z"}, nil
	}

	t.Setenv("REPO_FULL_NAME", "org/my-repo")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("PUSH_TOKEN", "")
	t.Setenv("PUSH_TOKEN_SOURCE", "")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	minted, cleanup, err := mintAgentToken(context.Background(), "coder", "https://mint.example.com", printer)
	require.NoError(t, err)
	defer cleanup()
	assert.True(t, minted)
	require.NotNil(t, cleanup)

	assert.Equal(t, "ghs_coder_token", os.Getenv("GH_TOKEN"))
	assert.Equal(t, "ghs_coder_token", os.Getenv("PUSH_TOKEN"))
	assert.Equal(t, "github-app", os.Getenv("PUSH_TOKEN_SOURCE"))

	cleanup()
	assert.Equal(t, "", os.Getenv("GH_TOKEN"), "cleanup should restore GH_TOKEN to original empty value")
	assert.Equal(t, "", os.Getenv("PUSH_TOKEN"), "cleanup should restore PUSH_TOKEN to original empty value")
	assert.Equal(t, "", os.Getenv("PUSH_TOKEN_SOURCE"), "cleanup should restore PUSH_TOKEN_SOURCE to original empty value")

	output := buf.String()
	assert.Contains(t, output, "Minting agent token (role: coder)")
	assert.Contains(t, output, "Agent token minted")
}

func TestMintAgentToken_ReviewRole(t *testing.T) {
	origMint := statusMintToken
	defer func() { statusMintToken = origMint }()

	statusMintToken = func(_ context.Context, req mintclient.MintRequest) (*mintclient.MintResult, error) {
		assert.Equal(t, "review", req.Role)
		return &mintclient.MintResult{Token: "ghs_review_token", ExpiresAt: "2026-06-15T12:00:00Z"}, nil
	}

	t.Setenv("REPO_FULL_NAME", "org/my-repo")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("REVIEW_TOKEN", "")

	printer := ui.New(io.Discard)
	minted, cleanup, err := mintAgentToken(context.Background(), "review", "https://mint.example.com", printer)
	require.NoError(t, err)
	assert.True(t, minted)
	require.NotNil(t, cleanup)
	defer cleanup()

	assert.Equal(t, "ghs_review_token", os.Getenv("GH_TOKEN"))
	assert.Equal(t, "ghs_review_token", os.Getenv("REVIEW_TOKEN"))
}

func TestMintAgentToken_RetroRole_NoExtras(t *testing.T) {
	origMint := statusMintToken
	defer func() { statusMintToken = origMint }()

	statusMintToken = func(_ context.Context, req mintclient.MintRequest) (*mintclient.MintResult, error) {
		assert.Equal(t, "retro", req.Role)
		assert.Equal(t, []string{"my-repo", ".fullsend"}, req.Repos)
		return &mintclient.MintResult{Token: "ghs_retro_token", ExpiresAt: "2026-06-15T12:00:00Z"}, nil
	}

	t.Setenv("MINT_REPOS", "my-repo,.fullsend")
	t.Setenv("GH_TOKEN", "")

	printer := ui.New(io.Discard)
	minted, cleanup, err := mintAgentToken(context.Background(), "retro", "https://mint.example.com", printer)
	require.NoError(t, err)
	assert.True(t, minted)
	require.NotNil(t, cleanup)
	defer cleanup()

	assert.Equal(t, "ghs_retro_token", os.Getenv("GH_TOKEN"))
}

func TestMintAgentToken_ResolvesAliases(t *testing.T) {
	origMint := statusMintToken
	defer func() { statusMintToken = origMint }()

	statusMintToken = func(_ context.Context, req mintclient.MintRequest) (*mintclient.MintResult, error) {
		assert.Equal(t, "coder", req.Role, "code should resolve to coder")
		return &mintclient.MintResult{Token: "ghs_alias_token", ExpiresAt: "2026-06-15T12:00:00Z"}, nil
	}

	t.Setenv("REPO_FULL_NAME", "org/my-repo")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("PUSH_TOKEN", "")
	t.Setenv("PUSH_TOKEN_SOURCE", "")

	printer := ui.New(io.Discard)
	minted, cleanup, err := mintAgentToken(context.Background(), "code", "https://mint.example.com", printer)
	require.NoError(t, err)
	assert.True(t, minted)
	require.NotNil(t, cleanup)
	defer cleanup()

	assert.Equal(t, "ghs_alias_token", os.Getenv("PUSH_TOKEN"))
}

func TestMintAgentToken_TriageRole_NoExtras(t *testing.T) {
	origMint := statusMintToken
	defer func() { statusMintToken = origMint }()

	statusMintToken = func(_ context.Context, req mintclient.MintRequest) (*mintclient.MintResult, error) {
		assert.Equal(t, "triage", req.Role)
		return &mintclient.MintResult{Token: "ghs_triage_token", ExpiresAt: "2026-06-15T12:00:00Z"}, nil
	}

	t.Setenv("REPO_FULL_NAME", "org/my-repo")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("PUSH_TOKEN", "should-not-change")

	printer := ui.New(io.Discard)
	minted, cleanup, err := mintAgentToken(context.Background(), "triage", "https://mint.example.com", printer)
	require.NoError(t, err)
	assert.True(t, minted)
	require.NotNil(t, cleanup)
	defer cleanup()

	assert.Equal(t, "ghs_triage_token", os.Getenv("GH_TOKEN"))
	assert.Equal(t, "should-not-change", os.Getenv("PUSH_TOKEN"), "triage should not set PUSH_TOKEN")
}

func TestMintAgentToken_MintError(t *testing.T) {
	origMint := statusMintToken
	defer func() { statusMintToken = origMint }()

	statusMintToken = func(_ context.Context, _ mintclient.MintRequest) (*mintclient.MintResult, error) {
		return nil, fmt.Errorf("OIDC exchange failed")
	}

	t.Setenv("REPO_FULL_NAME", "org/my-repo")

	printer := ui.New(io.Discard)
	_, _, err := mintAgentToken(context.Background(), "coder", "https://mint.example.com", printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "minting agent token for role coder")
}

func TestMintAgentToken_RepoResolutionError(t *testing.T) {
	origMint := statusMintToken
	defer func() { statusMintToken = origMint }()

	// No REPO_FULL_NAME and no MINT_REPOS set
	printer := ui.New(io.Discard)
	_, _, err := mintAgentToken(context.Background(), "coder", "https://mint.example.com", printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving mint repos for role coder")
}

func TestMintAgentToken_RejectsMalformedToken(t *testing.T) {
	origMint := statusMintToken
	defer func() { statusMintToken = origMint }()

	statusMintToken = func(_ context.Context, _ mintclient.MintRequest) (*mintclient.MintResult, error) {
		return &mintclient.MintResult{Token: "bad token with spaces!", ExpiresAt: "2026-06-15T12:00:00Z"}, nil
	}

	t.Setenv("REPO_FULL_NAME", "org/my-repo")

	printer := ui.New(io.Discard)
	_, _, err := mintAgentToken(context.Background(), "coder", "https://mint.example.com", printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected characters")
}

func TestMintAgentToken_MasksTokenInGitHubActions(t *testing.T) {
	origMint := statusMintToken
	defer func() { statusMintToken = origMint }()

	statusMintToken = func(_ context.Context, _ mintclient.MintRequest) (*mintclient.MintResult, error) {
		return &mintclient.MintResult{Token: "ghs_maskable", ExpiresAt: "2026-06-15T12:00:00Z"}, nil
	}

	t.Setenv("REPO_FULL_NAME", "org/my-repo")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GH_TOKEN", "")

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	printer := ui.New(io.Discard)
	minted, cleanup, err := mintAgentToken(context.Background(), "triage", "https://mint.example.com", printer)

	w.Close()
	os.Stderr = oldStderr

	require.NoError(t, err)
	assert.True(t, minted)
	if cleanup != nil {
		defer cleanup()
	}

	var buf bytes.Buffer
	io.Copy(&buf, r)
	assert.Contains(t, buf.String(), "::add-mask::ghs_maskable")
}

// --- resolveMintRepos tests ---

func TestResolveMintRepos_FromMINT_REPOS(t *testing.T) {
	t.Setenv("MINT_REPOS", "repo-a,repo-b")
	repos, err := resolveMintRepos()
	require.NoError(t, err)
	assert.Equal(t, []string{"repo-a", "repo-b"}, repos)
}

func TestResolveMintRepos_TrimsWhitespace(t *testing.T) {
	t.Setenv("MINT_REPOS", " repo-a , repo-b ")
	repos, err := resolveMintRepos()
	require.NoError(t, err)
	assert.Equal(t, []string{"repo-a", "repo-b"}, repos)
}

func TestResolveMintRepos_FromREPO_FULL_NAME(t *testing.T) {
	t.Setenv("REPO_FULL_NAME", "org/my-repo")
	repos, err := resolveMintRepos()
	require.NoError(t, err)
	assert.Equal(t, []string{"my-repo"}, repos)
}

func TestResolveMintRepos_MINT_REPOS_TakesPrecedence(t *testing.T) {
	t.Setenv("MINT_REPOS", "override-repo")
	t.Setenv("REPO_FULL_NAME", "org/other-repo")
	repos, err := resolveMintRepos()
	require.NoError(t, err)
	assert.Equal(t, []string{"override-repo"}, repos)
}

func TestResolveMintRepos_NeitherSet(t *testing.T) {
	_, err := resolveMintRepos()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MINT_REPOS or REPO_FULL_NAME must be set")
}

func TestResolveMintRepos_InvalidREPO_FULL_NAME(t *testing.T) {
	t.Setenv("REPO_FULL_NAME", "no-slash")
	_, err := resolveMintRepos()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo format")
}

func TestResolveMintRepos_EmptyRepoInREPO_FULL_NAME(t *testing.T) {
	t.Setenv("REPO_FULL_NAME", "org/")
	_, err := resolveMintRepos()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo format")
}

func TestResolveMintRepos_EmptyMINT_REPOS_FallsBack(t *testing.T) {
	t.Setenv("MINT_REPOS", ",,,")
	t.Setenv("REPO_FULL_NAME", "org/fallback-repo")
	repos, err := resolveMintRepos()
	require.NoError(t, err)
	assert.Equal(t, []string{"fallback-repo"}, repos)
}

func TestMintAgentToken_SanitizesExpiresAt(t *testing.T) {
	origMint := statusMintToken
	defer func() { statusMintToken = origMint }()

	statusMintToken = func(_ context.Context, _ mintclient.MintRequest) (*mintclient.MintResult, error) {
		return &mintclient.MintResult{
			Token:     "ghs_safe_token",
			ExpiresAt: "2026-06-15T12:00:00Z::warning::injected",
		}, nil
	}

	t.Setenv("REPO_FULL_NAME", "org/my-repo")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("PUSH_TOKEN", "")
	t.Setenv("PUSH_TOKEN_SOURCE", "")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	minted, cleanup, err := mintAgentToken(context.Background(), "coder", "https://mint.example.com", printer)
	require.NoError(t, err)
	assert.True(t, minted)
	if cleanup != nil {
		defer cleanup()
	}

	output := buf.String()
	assert.NotContains(t, output, "::warning::")
	assert.Contains(t, output, "2026-06-15T12:00:00Z")
}

func TestMintAgentToken_SanitizesExpiresAt_FractionalSeconds(t *testing.T) {
	origMint := statusMintToken
	defer func() { statusMintToken = origMint }()

	statusMintToken = func(_ context.Context, _ mintclient.MintRequest) (*mintclient.MintResult, error) {
		return &mintclient.MintResult{
			Token:     "ghs_safe_token",
			ExpiresAt: "2026-06-15T12:00:00.123Z",
		}, nil
	}

	t.Setenv("REPO_FULL_NAME", "org/my-repo")
	t.Setenv("GH_TOKEN", "")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	minted, cleanup, err := mintAgentToken(context.Background(), "triage", "https://mint.example.com", printer)
	require.NoError(t, err)
	assert.True(t, minted)
	if cleanup != nil {
		defer cleanup()
	}

	output := buf.String()
	assert.Contains(t, output, "2026-06-15T12:00:00.123Z")
}

func TestMintAgentToken_RejectsInvalidRole(t *testing.T) {
	origMint := statusMintToken
	defer func() { statusMintToken = origMint }()

	statusMintToken = func(_ context.Context, _ mintclient.MintRequest) (*mintclient.MintResult, error) {
		t.Fatal("mint should not be called for invalid role")
		return nil, nil
	}

	t.Setenv("REPO_FULL_NAME", "org/my-repo")

	printer := ui.New(io.Discard)
	_, _, err := mintAgentToken(context.Background(), "INVALID--ROLE", "https://mint.example.com", printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role")
}

func TestResolveMintRepos_InvalidRepoInMINT_REPOS(t *testing.T) {
	t.Setenv("MINT_REPOS", "valid-repo,invalid repo!@#")
	_, err := resolveMintRepos()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid repo name")
	assert.Contains(t, err.Error(), "MINT_REPOS")
}

func TestResolveMintRepos_InvalidRepoInREPO_FULL_NAME(t *testing.T) {
	t.Setenv("REPO_FULL_NAME", "org/invalid repo!@#")
	_, err := resolveMintRepos()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid repo name")
	assert.Contains(t, err.Error(), "REPO_FULL_NAME")
}

func TestRoleTokenVars_Coverage(t *testing.T) {
	assert.Equal(t, []tokenVar{{Name: "PUSH_TOKEN"}, {Name: "PUSH_TOKEN_SOURCE", Value: "github-app"}}, roleTokenVars["coder"])
	assert.Equal(t, []tokenVar{{Name: "REVIEW_TOKEN"}}, roleTokenVars["review"])
	_, hasRetro := roleTokenVars["retro"]
	assert.False(t, hasRetro, "retro should not have extra token vars (RETRO_SANDBOX_TOKEN removed in #2412)")
	_, hasTriage := roleTokenVars["triage"]
	assert.False(t, hasTriage, "triage should not have extra token vars")
	_, hasPrioritize := roleTokenVars["prioritize"]
	assert.False(t, hasPrioritize, "prioritize should not have extra token vars")
}

func TestMintAgentToken_CleanupRestoresOriginals(t *testing.T) {
	origMint := statusMintToken
	defer func() { statusMintToken = origMint }()

	statusMintToken = func(_ context.Context, _ mintclient.MintRequest) (*mintclient.MintResult, error) {
		return &mintclient.MintResult{Token: "ghs_new_token", ExpiresAt: "2026-06-15T12:00:00Z"}, nil
	}

	t.Setenv("REPO_FULL_NAME", "org/my-repo")
	t.Setenv("GH_TOKEN", "ghp_original_pat")
	t.Setenv("PUSH_TOKEN", "ghp_original_push")
	t.Setenv("PUSH_TOKEN_SOURCE", "manual")

	printer := ui.New(io.Discard)
	minted, cleanup, err := mintAgentToken(context.Background(), "coder", "https://mint.example.com", printer)
	require.NoError(t, err)
	defer cleanup()
	assert.True(t, minted)
	require.NotNil(t, cleanup)

	assert.Equal(t, "ghs_new_token", os.Getenv("GH_TOKEN"))

	cleanup()
	assert.Equal(t, "ghp_original_pat", os.Getenv("GH_TOKEN"), "cleanup should restore original GH_TOKEN")
	assert.Equal(t, "ghp_original_push", os.Getenv("PUSH_TOKEN"), "cleanup should restore original PUSH_TOKEN")
	assert.Equal(t, "manual", os.Getenv("PUSH_TOKEN_SOURCE"), "cleanup should restore original PUSH_TOKEN_SOURCE")
}

func TestRunAgent_FallsBackToFULLSEND_MINT_URL(t *testing.T) {
	useFakeOpenshell(t)
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
		[]byte("agent: agents/code.md\nrole: coder\n"),
		0o644,
	))

	origMint := statusMintToken
	defer func() { statusMintToken = origMint }()

	var mintCalled bool
	statusMintToken = func(_ context.Context, req mintclient.MintRequest) (*mintclient.MintResult, error) {
		mintCalled = true
		assert.Equal(t, "https://mint-from-env.example.com", req.MintURL)
		return &mintclient.MintResult{Token: "ghs_env_token", ExpiresAt: "2026-06-15T12:00:00Z"}, nil
	}

	t.Setenv("FULLSEND_MINT_URL", "https://mint-from-env.example.com")
	t.Setenv("REPO_FULL_NAME", "org/my-repo")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("PUSH_TOKEN", "")
	t.Setenv("PUSH_TOKEN_SOURCE", "")

	var buf bytes.Buffer
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(&buf)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "openshell")
	assert.True(t, mintCalled, "should have used FULLSEND_MINT_URL env var fallback")
}

func TestRunAgent_WarnsWhenNoMintURL(t *testing.T) {
	useFakeOpenshell(t)
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
		[]byte("agent: agents/code.md\nrole: coder\n"),
		0o644,
	))

	origMint := statusMintToken
	defer func() { statusMintToken = origMint }()

	statusMintToken = func(_ context.Context, _ mintclient.MintRequest) (*mintclient.MintResult, error) {
		t.Fatal("mint should not be called when no mint URL is available")
		return nil, nil
	}

	t.Setenv("FULLSEND_MINT_URL", "")

	var buf bytes.Buffer
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(&buf)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)

	require.Error(t, err)
	assert.Contains(t, buf.String(), "skipping token minting")
}

func TestRunAgent_MintTokenError(t *testing.T) {
	useFakeOpenshell(t)
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
		[]byte("agent: agents/code.md\nrole: coder\n"),
		0o644,
	))

	origMint := statusMintToken
	defer func() { statusMintToken = origMint }()

	statusMintToken = func(_ context.Context, _ mintclient.MintRequest) (*mintclient.MintResult, error) {
		return nil, fmt.Errorf("OIDC token exchange failed")
	}

	t.Setenv("FULLSEND_MINT_URL", "https://mint.example.com")
	t.Setenv("REPO_FULL_NAME", "org/my-repo")

	var buf bytes.Buffer
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(&buf)
	repoDir := t.TempDir()
	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", rFlags, statusOpts{}, printer, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent token minting failed")
}

func TestRunAgent_StatusNotifierSetup(t *testing.T) {
	useFakeOpenshell(t)
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
		[]byte("agent: agents/code.md\nrole: coder\n"),
		0o644,
	))

	origMint := statusMintToken
	defer func() { statusMintToken = origMint }()

	statusMintToken = func(_ context.Context, _ mintclient.MintRequest) (*mintclient.MintResult, error) {
		return &mintclient.MintResult{Token: "ghs_test_token", ExpiresAt: "2026-06-15T12:00:00Z"}, nil
	}

	t.Setenv("FULLSEND_MINT_URL", "https://mint.example.com")
	t.Setenv("REPO_FULL_NAME", "org/my-repo")
	t.Setenv("GITHUB_RUN_ID", "run-42")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("PUSH_TOKEN", "")
	t.Setenv("PUSH_TOKEN_SOURCE", "")

	var buf bytes.Buffer
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	printer := ui.New(&buf)
	repoDir := t.TempDir()
	sOpts := statusOpts{
		statusRepo: "org/my-repo",
		statusNum:  42,
		mintURL:    "https://mint.example.com",
	}
	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", rFlags, sOpts, printer, false)

	// Will error downstream (openshell not available), but status notifier setup should succeed
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openshell")
}

func TestResolveBackendFromConfigData_OrgConfig(t *testing.T) {
	t.Parallel()

	data := []byte(`version: "1"
dispatch:
  platform: github-actions
defaults:
  roles: [triage]
  runtime: dummy
repos:
  widget:
    enabled: true
`)
	backend, err := resolveBackendFromConfigData(data)
	require.NoError(t, err)
	assert.Equal(t, "dummy", backend.Runtime.Name())
}

func TestResolveBackendFromConfigData_PerRepoConfig(t *testing.T) {
	t.Parallel()

	cfg := config.NewPerRepoConfig(config.PerRepoDefaultRoles(), "acme/test-repo")
	cfg.SetRuntime("dummy")
	data, err := cfg.Marshal()
	require.NoError(t, err)

	backend, err := resolveBackendFromConfigData(data)
	require.NoError(t, err)
	assert.Equal(t, "dummy", backend.Runtime.Name())
}

func TestResolveBackendFromConfigData_Invalid(t *testing.T) {
	t.Parallel()

	_, err := resolveBackendFromConfigData([]byte("not: [valid: yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing config for runtime selection")
}

func TestResolveBackendFromConfigData_UnknownRuntime(t *testing.T) {
	t.Parallel()

	data := []byte(`version: "1"
dispatch:
  platform: github-actions
defaults:
  roles: [triage]
  runtime: nonexistent
repos:
  widget:
    enabled: true
`)
	_, err := resolveBackendFromConfigData(data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving runtime")
}

func TestIsOrgConfigData(t *testing.T) {
	t.Parallel()

	perRepo := config.NewPerRepoConfig(config.PerRepoDefaultRoles(), "acme/test-repo")
	perRepoData, err := perRepo.Marshal()
	require.NoError(t, err)
	assert.False(t, isOrgConfigData(perRepoData))

	org := config.NewOrgConfig([]string{"widget"}, []string{"widget"}, config.DefaultAgentRoles(), "", "acme")
	orgData, err := org.Marshal()
	require.NoError(t, err)
	assert.True(t, isOrgConfigData(orgData))
}

func TestBackendFromConfigFile_MissingUsesDefault(t *testing.T) {
	t.Parallel()

	backend, source, err := backendFromConfigFile(filepath.Join(t.TempDir(), "missing.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "default (config not found)", source)
	assert.Equal(t, "claude", backend.Runtime.Name())
}

func TestBackendFromConfigFile_PerRepoConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := config.NewPerRepoConfig(config.PerRepoDefaultRoles(), "acme/test-repo")
	cfg.SetRuntime("dummy")
	data, err := cfg.Marshal()
	require.NoError(t, err)
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, data, 0o644))

	backend, _, err := backendFromConfigFile(path)
	require.NoError(t, err)
	assert.Equal(t, "dummy", backend.Runtime.Name())
}

func TestBackendFromConfigFile_PerRepoNestedConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := config.NewPerRepoConfig(config.PerRepoDefaultRoles(), "acme/test-repo")
	cfg.SetRuntime("dummy")
	data, err := cfg.Marshal()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".fullsend"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".fullsend", "config.yaml"), data, 0o644))

	backend, source, err := backendFromConfigFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, source, ".fullsend")
	assert.Equal(t, "dummy", backend.Runtime.Name())
}

func TestBackendFromConfigFile_ReadError(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("directory-as-file read error differs on Windows")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.Mkdir(path, 0o755))

	_, _, err := backendFromConfigFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config.yaml for runtime selection")
}

func TestBackendFromConfigFile_ResolveError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := []byte(`version: "1"
dispatch:
  platform: github-actions
defaults:
  roles: [triage]
  runtime: nonexistent
repos:
  widget:
    enabled: true
`)
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, data, 0o644))

	_, _, err := backendFromConfigFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving runtime")
}

func TestIsOrgConfigData_InvalidYAML(t *testing.T) {
	t.Parallel()

	assert.False(t, isOrgConfigData([]byte("not: [valid")))
}

func TestIsOrgConfigData_HeaderlessPerRepoByStructure(t *testing.T) {
	t.Parallel()

	// Hand-edited per-repo config without the header comment.
	data := []byte("version: \"1\"\nroles:\n  - triage\n")
	assert.False(t, isOrgConfigData(data))
}

func TestIsOrgConfigData_HeaderlessOrgByStructure(t *testing.T) {
	t.Parallel()

	data := []byte("version: \"1\"\ndefaults:\n  roles:\n    - triage\nrepos:\n  widget:\n    enabled: true\n")
	assert.True(t, isOrgConfigData(data))
}

func TestDefaultAllowlistCoversAgentsRepoFallback(t *testing.T) {
	// Verify the default allowlist covers the agents repo fallback URL shape.
	// A future edit to DefaultAllowedRemoteResources() that drops the
	// agents repo prefix would regress #3396 silently without this.
	sampleURL := defaultAgentsRepoURLPrefix + strings.Repeat("a", 40) + "/scripts/pre-triage.sh"
	assert.NotEmpty(t, harness.MatchingAllowedPrefixInList(sampleURL, config.DefaultAllowedRemoteResources()),
		"DefaultAllowedRemoteResources must cover the agents repo fallback URL shape")
}

func TestAgentsRepoFallback_LoadWithBase_DefaultAllowlist(t *testing.T) {
	// Integration test for #3396: a URL-sourced harness (from agents-repo
	// fallback) with pre_script/post_script must succeed when OrgAllowlist
	// carries the default allowlist (now always set via config loading or
	// the nil-config fallback in runAgent).
	preScript := []byte("#!/bin/bash\necho pre")
	postScript := []byte("#!/bin/bash\necho post")
	fakeSHA := "abcdef1234567890abcdef1234567890abcdef12"

	harnessContent := []byte(`role: triage
slug: triage
agent: agents/triage.md
pre_script: scripts/pre-triage.sh
post_script: scripts/post-triage.sh
`)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/"+fakeSHA+"/harness/triage.yaml":
			_, _ = w.Write(harnessContent)
		case r.URL.Path == "/"+fakeSHA+"/scripts/pre-triage.sh":
			_, _ = w.Write(preScript)
		case r.URL.Path == "/"+fakeSHA+"/scripts/post-triage.sh":
			_, _ = w.Write(postScript)
		case r.URL.Path == "/"+fakeSHA+"/agents/triage.md":
			_, _ = w.Write([]byte("# triage agent"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	hostPort := strings.TrimPrefix(srv.URL, "https://")
	hostname, port, _ := net.SplitHostPort(hostPort)
	tlsCfg := srv.TLS.Clone()
	tlsCfg.InsecureSkipVerify = true
	policy := fetch.NewTestPolicy(tlsCfg, []string{hostname}, []string{port})

	workDir := t.TempDir()
	sourceURL := srv.URL + "/" + fakeSHA + "/harness/triage.yaml"

	harnessDir := filepath.Join(workDir, "harness")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	harnessPath := filepath.Join(harnessDir, "triage.yaml")
	require.NoError(t, os.WriteFile(harnessPath, harnessContent, 0o644))

	// With the test server's URL in the allowlist, LoadWithBase succeeds.
	opts := harness.ComposeOpts{
		WorkspaceRoot: workDir,
		FetchPolicy:   policy,
		SourceURL:     sourceURL,
		OrgAllowlist:  []string{srv.URL + "/"},
	}

	h, _, err := harness.LoadWithBase(context.Background(), harnessPath, opts)
	require.NoError(t, err, "should succeed with allowlist set")
	assert.True(t, filepath.IsAbs(h.PreScript), "pre_script should be resolved to cache path")
	assert.True(t, filepath.IsAbs(h.PostScript), "post_script should be resolved to cache path")
}

func TestDedupResolvedProfiles(t *testing.T) {
	tests := []struct {
		name    string
		input   []resolve.ResolvedProfile
		wantIDs []string
	}{
		{
			name:    "empty",
			input:   nil,
			wantIDs: nil,
		},
		{
			name:    "single",
			input:   []resolve.ResolvedProfile{{ID: "a"}},
			wantIDs: []string{"a"},
		},
		{
			name:    "no duplicates",
			input:   []resolve.ResolvedProfile{{ID: "a"}, {ID: "b"}},
			wantIDs: []string{"a", "b"},
		},
		{
			name: "last wins",
			input: []resolve.ResolvedProfile{
				{ID: "a", LocalPath: "/base/a"},
				{ID: "b", LocalPath: "/base/b"},
				{ID: "a", LocalPath: "/child/a"},
			},
			wantIDs: []string{"b", "a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedupResolvedProfiles(tt.input)
			if tt.wantIDs == nil {
				assert.Len(t, got, len(tt.input))
				return
			}
			var ids []string
			for _, rp := range got {
				ids = append(ids, rp.ID)
			}
			assert.Equal(t, tt.wantIDs, ids)
			// Verify last-wins keeps child path
			if tt.name == "last wins" {
				for _, rp := range got {
					if rp.ID == "a" {
						assert.Equal(t, "/child/a", rp.LocalPath)
					}
				}
			}
		})
	}
}

func TestDedupResolvedProviders(t *testing.T) {
	tests := []struct {
		name      string
		input     []resolve.ResolvedProvider
		wantNames []string
	}{
		{
			name:      "empty",
			input:     nil,
			wantNames: nil,
		},
		{
			name:      "single",
			input:     []resolve.ResolvedProvider{{Def: harness.ProviderDef{Name: "a"}}},
			wantNames: []string{"a"},
		},
		{
			name: "no duplicates",
			input: []resolve.ResolvedProvider{
				{Def: harness.ProviderDef{Name: "a"}},
				{Def: harness.ProviderDef{Name: "b"}},
			},
			wantNames: []string{"a", "b"},
		},
		{
			name: "last wins",
			input: []resolve.ResolvedProvider{
				{Def: harness.ProviderDef{Name: "a"}, LocalPath: "/base/a"},
				{Def: harness.ProviderDef{Name: "b"}, LocalPath: "/base/b"},
				{Def: harness.ProviderDef{Name: "a"}, LocalPath: "/child/a"},
			},
			wantNames: []string{"b", "a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedupResolvedProviders(tt.input)
			if tt.wantNames == nil {
				assert.Len(t, got, len(tt.input))
				return
			}
			var names []string
			for _, rp := range got {
				names = append(names, rp.Def.Name)
			}
			assert.Equal(t, tt.wantNames, names)
			if tt.name == "last wins" {
				for _, rp := range got {
					if rp.Def.Name == "a" {
						assert.Equal(t, "/child/a", rp.LocalPath)
					}
				}
			}
		})
	}
}

func TestMergeProviderDefs(t *testing.T) {
	tests := []struct {
		name         string
		local        []harness.ProviderDef
		url          []resolve.ResolvedProvider
		wantNames    []string
		wantShadowed []string
	}{
		{
			name:      "local only",
			local:     []harness.ProviderDef{{Name: "local-a", Type: "t1"}, {Name: "local-b", Type: "t2"}},
			url:       nil,
			wantNames: []string{"local-a", "local-b"},
		},
		{
			name:  "URL only",
			local: nil,
			url: []resolve.ResolvedProvider{
				{Def: harness.ProviderDef{Name: "beta", Type: "t1"}},
				{Def: harness.ProviderDef{Name: "alpha", Type: "t2"}},
			},
			wantNames: []string{"alpha", "beta"},
		},
		{
			name:  "local shadows URL",
			local: []harness.ProviderDef{{Name: "shared", Type: "local-type"}},
			url: []resolve.ResolvedProvider{
				{Def: harness.ProviderDef{Name: "shared", Type: "url-type"}},
				{Def: harness.ProviderDef{Name: "url-only", Type: "t1"}},
			},
			wantNames:    []string{"shared", "url-only"},
			wantShadowed: []string{"shared"},
		},
		{
			name:  "duplicate URL names last wins",
			local: nil,
			url: []resolve.ResolvedProvider{
				{Def: harness.ProviderDef{Name: "p", Type: "base-type"}},
				{Def: harness.ProviderDef{Name: "p", Type: "child-type"}},
			},
			wantNames: []string{"p"},
		},
		{
			name:      "both empty",
			local:     nil,
			url:       nil,
			wantNames: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, shadowed := mergeProviderDefs(tt.local, tt.url)
			var names []string
			for _, d := range got {
				names = append(names, d.Name)
			}
			if tt.wantNames == nil {
				assert.Empty(t, got)
			} else {
				assert.Equal(t, tt.wantNames, names)
			}
			if tt.wantShadowed == nil {
				assert.Empty(t, shadowed)
			} else {
				assert.Equal(t, tt.wantShadowed, shadowed)
			}
			// Verify local shadows URL by checking type
			if tt.name == "local shadows URL" {
				assert.Equal(t, "local-type", got[0].Type)
			}
			// Verify last-wins dedup
			if tt.name == "duplicate URL names last wins" {
				assert.Equal(t, "child-type", got[0].Type)
			}
		})
	}
}

func TestSandboxProviderNames(t *testing.T) {
	tests := []struct {
		name             string
		harnessProviders []string
		resolved         []resolve.ResolvedProvider
		want             []string
	}{
		{
			name:             "harness-declared and URL-resolved names are both included",
			harnessProviders: []string{"local-prov"},
			resolved: []resolve.ResolvedProvider{
				{Def: harness.ProviderDef{Name: "url-prov", Type: "t2"}},
			},
			want: []string{"local-prov", "url-prov"},
		},
		{
			name:             "URL-only providers are included",
			harnessProviders: nil,
			resolved: []resolve.ResolvedProvider{
				{Def: harness.ProviderDef{Name: "remote-a", Type: "t1"}},
				{Def: harness.ProviderDef{Name: "remote-b", Type: "t2"}},
			},
			want: []string{"remote-a", "remote-b"},
		},
		{
			name:             "harness-only providers are included",
			harnessProviders: []string{"loc"},
			resolved:         nil,
			want:             []string{"loc"},
		},
		{
			name:             "empty when no providers",
			harnessProviders: nil,
			resolved:         nil,
			want:             []string{},
		},
		{
			name:             "directory providers not in harness are excluded",
			harnessProviders: []string{"declared-a"},
			resolved:         nil,
			want:             []string{"declared-a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sandboxProviderNames(tt.harnessProviders, tt.resolved)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSandboxProviderNames_ExcludesUndeclaredDirectoryProviders(t *testing.T) {
	// Regression test: the sandbox must only receive harness-declared +
	// URL-resolved provider names. Providers that exist in the providers/
	// directory but are not declared in the harness must NOT be attached
	// to the sandbox, even though they are created on the gateway.
	harnessProviders := []string{"declared-only"}
	urlResolved := []resolve.ResolvedProvider{
		{Def: harness.ProviderDef{Name: "url-resolved", Type: "t1"}},
	}

	got := sandboxProviderNames(harnessProviders, urlResolved)

	assert.Equal(t, []string{"declared-only", "url-resolved"}, got)
	assert.NotContains(t, got, "undeclared-directory-provider",
		"directory providers not in harness must not appear in sandbox provider names")
}

func TestCheckProviderProfileIntegrity(t *testing.T) {
	tests := []struct {
		name      string
		providers []resolve.ResolvedProvider
		profiles  []resolve.ResolvedProfile
		wantWarn  bool
		wantErr   bool
	}{
		{
			name:      "no providers",
			providers: nil,
			profiles:  nil,
		},
		{
			name: "providers without profiles warns",
			providers: []resolve.ResolvedProvider{
				{Def: harness.ProviderDef{Name: "p", Type: "anthropic"}},
			},
			profiles: nil,
			wantWarn: true,
		},
		{
			name: "all providers match profiles",
			providers: []resolve.ResolvedProvider{
				{Def: harness.ProviderDef{Name: "p1", Type: "anthropic"}},
				{Def: harness.ProviderDef{Name: "p2", Type: "openai"}},
			},
			profiles: []resolve.ResolvedProfile{
				{ID: "anthropic"},
				{ID: "openai"},
			},
		},
		{
			name: "provider references unknown profile",
			providers: []resolve.ResolvedProvider{
				{Def: harness.ProviderDef{Name: "p", Type: "unknown-type"}},
			},
			profiles: []resolve.ResolvedProfile{
				{ID: "anthropic"},
			},
			wantErr: true,
		},
		{
			name: "multiple mismatches reported together",
			providers: []resolve.ResolvedProvider{
				{Def: harness.ProviderDef{Name: "p1", Type: "missing-a"}},
				{Def: harness.ProviderDef{Name: "p2", Type: "anthropic"}},
				{Def: harness.ProviderDef{Name: "p3", Type: "missing-b"}},
			},
			profiles: []resolve.ResolvedProfile{
				{ID: "anthropic"},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warn, err := checkProviderProfileIntegrity(tt.providers, tt.profiles)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "gateway-resident")
				if tt.name == "multiple mismatches reported together" {
					assert.Contains(t, err.Error(), "missing-a")
					assert.Contains(t, err.Error(), "missing-b")
					assert.NotContains(t, err.Error(), "anthropic")
				}
			} else {
				require.NoError(t, err)
			}
			if tt.wantWarn {
				assert.NotEmpty(t, warn)
			} else if !tt.wantErr {
				assert.Empty(t, warn)
			}
		})
	}
}

func TestDedupResolvedProfiles_ComposeScenario(t *testing.T) {
	// Simulates base+child compose: base declares profile "anthropic" at one
	// URL, child redeclares it at another. After concatenation (base first,
	// child second) and dedup, child's version should win.
	baseProfile := resolve.ResolvedProfile{
		ID:        "anthropic",
		LocalPath: "/cache/base/anthropic.yaml",
	}
	childProfile := resolve.ResolvedProfile{
		ID:        "anthropic",
		LocalPath: "/cache/child/anthropic.yaml",
	}
	got := dedupResolvedProfiles([]resolve.ResolvedProfile{baseProfile, childProfile})
	require.Len(t, got, 1)
	assert.Equal(t, "anthropic", got[0].ID)
	assert.Equal(t, "/cache/child/anthropic.yaml", got[0].LocalPath)
}

func TestDedupResolvedProviders_ComposeScenario(t *testing.T) {
	// Simulates base+child compose: both declare provider "my-claude",
	// child's version should win after dedup.
	baseProvider := resolve.ResolvedProvider{
		Def:       harness.ProviderDef{Name: "my-claude", Type: "claude-code"},
		LocalPath: "/cache/base/my-claude.yaml",
	}
	childProvider := resolve.ResolvedProvider{
		Def:       harness.ProviderDef{Name: "my-claude", Type: "claude-code-v2"},
		LocalPath: "/cache/child/my-claude.yaml",
	}
	got := dedupResolvedProviders([]resolve.ResolvedProvider{baseProvider, childProvider})
	require.Len(t, got, 1)
	assert.Equal(t, "my-claude", got[0].Def.Name)
	assert.Equal(t, "claude-code-v2", got[0].Def.Type)
	assert.Equal(t, "/cache/child/my-claude.yaml", got[0].LocalPath)
}

func TestForceRemoveAll_ReadOnlyTree(t *testing.T) {
	// Simulate the readonly_repo enforcement: create a directory tree
	// with files and directories that have had write permission removed.
	// forceRemoveAll must restore permissions and successfully delete.
	root := t.TempDir()
	nested := filepath.Join(root, "target", ".claude", "commands")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nested, "enable-arm64-builds.md"), []byte("content"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "target", "README.md"), []byte("readme"), 0o644))

	target := filepath.Join(root, "target")

	// Remove write permissions recursively, mirroring the sandbox chmod.
	err := filepath.WalkDir(target, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.Chmod(p, info.Mode()&^0o222) // a-w
	})
	require.NoError(t, err)

	// Verify that plain os.RemoveAll fails.
	err = os.RemoveAll(target)
	require.Error(t, err, "os.RemoveAll should fail on read-only tree")

	// forceRemoveAll must succeed.
	require.NoError(t, forceRemoveAll(target))

	// Verify complete removal.
	_, err = os.Stat(target)
	assert.True(t, os.IsNotExist(err), "directory should be fully removed")
}

func TestForceRemoveAll_AlreadyWritable(t *testing.T) {
	// Normal (writable) directories should be removed without issues.
	root := t.TempDir()
	target := filepath.Join(root, "writable")
	require.NoError(t, os.MkdirAll(filepath.Join(target, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, "sub", "file.txt"), []byte("data"), 0o644))

	require.NoError(t, forceRemoveAll(target))

	_, err := os.Stat(target)
	assert.True(t, os.IsNotExist(err))
}

func TestForceRemoveAll_NonExistent(t *testing.T) {
	// Removing a path that does not exist should succeed (same as os.RemoveAll).
	require.NoError(t, forceRemoveAll(filepath.Join(t.TempDir(), "does-not-exist")))
}
