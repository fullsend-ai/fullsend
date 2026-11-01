package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootCommand_HasLintSubcommand(t *testing.T) {
	cmd := newRootCmd()
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Name() == "lint" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected lint subcommand")
}

// stubLintGitHubToken pins token resolution so output assertions do not
// depend on whether the host has GH_TOKEN, GITHUB_TOKEN or a gh login.
// An empty token makes resolution fail, as on a CI runner without one.
func stubLintGitHubToken(t *testing.T, token string) {
	t.Helper()
	t.Setenv("GH_TOKEN", token)
	t.Setenv("GITHUB_TOKEN", "")
	old := ghAuthTokenFn
	t.Cleanup(func() { ghAuthTokenFn = old })
	ghAuthTokenFn = func() ([]byte, error) { return nil, errGitHubTokenMissing }
}

func TestLintCommand_ExecuteWithFullsendDir(t *testing.T) {
	stubLintGitHubToken(t, "test-token")
	dir := t.TempDir()
	content := "version: \"1\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o644))

	cmd := newLintCmd()
	out, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestLintCommand_ExecuteWithDefaultFullsendDir(t *testing.T) {
	stubLintGitHubToken(t, "test-token")
	dir := t.TempDir()
	fullsendPath := filepath.Join(dir, defaultFullsendDir)
	require.NoError(t, os.Mkdir(fullsendPath, 0o755))
	content := "version: \"1\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(fullsendPath, "config.yaml"), []byte(content), 0o644))
	t.Chdir(dir)

	cmd := newLintCmd()
	out, err := executeCmd(cmd, nil)
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestLintCommand_WarnsWithoutGitToken(t *testing.T) {
	stubLintGitHubToken(t, "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("version: \"1\"\n"), 0o644))

	cmd := newLintCmd()
	out, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.NoError(t, err, "a missing token is a warning, not a lint failure")
	assert.Contains(t, out, "Git token not available")
}

func TestLintCommand_MissingConfigYAML(t *testing.T) {
	dir := t.TempDir()
	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.Error(t, err)
}

func TestLintCommand_InvalidConfigYAML(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("invalid:\n  - yaml: ["), 0o644))

	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.Error(t, err)
}

func TestLintCommand_InvalidConfigSemantics(t *testing.T) {
	cases := []struct {
		name string
		cfg  string
		want string
	}{
		{name: "unsupported version", cfg: "version: \"999\"\n", want: `unsupported version "999"`},
		{name: "unknown runtime", cfg: "runtime: nonexistent\n", want: `invalid runtime "nonexistent"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// No agents: the config itself is the only thing to lint, and
			// loadPerRepoLayers unmarshals without validating, so the
			// semantic check is what catches this.
			require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(tc.cfg), 0o644))

			cmd := newLintCmd()
			out, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "lint found 1 error-level diagnostic(s)")
			assert.Contains(t, out, "config: error: config.yaml")
			assert.Contains(t, out, tc.want)
		})
	}
}

// writeLintAgentFixture commits the files a harness needs to pass the
// same path and file validation fullsend run performs: the agent markdown
// and a one-field policy at policies/base.yaml.
func writeLintAgentFixture(t *testing.T, dir, name string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", name+".md"), []byte("You are an agent."), 0o644))
	writePolicyFixture(t, dir)
}

func TestLintCommand_LoadsHarness_Valid(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	writeLintAgentFixture(t, dir, "my-agent")
	harnessYAML := `role: triage
agent: agents/my-agent.md
policy: policies/base.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	cmd := newLintCmd()
	out, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.NoError(t, err)
	assert.Contains(t, out, "my-agent resolved from config (local path)")
	assert.NotContains(t, out, "warning:")
	assert.NotContains(t, out, "error:")
}

func TestLintCommand_LintsBaseComposedHarness(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	// The deprecated env var lives only in the base harness; lint must
	// see it after base composition.
	writeLintAgentFixture(t, dir, "my-agent")
	baseYAML := `role: triage
agent: agents/my-agent.md
policy: policies/base.yaml
env:
  runner:
    GITHUB_ISSUE_URL: ${GITHUB_ISSUE_URL}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "base.yaml"), []byte(baseYAML), 0o644))
	childYAML := `base: base.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(childYAML), 0o644))

	cmd := newLintCmd()
	out, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.NoError(t, err)
	assert.Contains(t, out, "my-agent")
	assert.Contains(t, out, "GITHUB_ISSUE_URL is deprecated")
}

func TestLintCommand_LintsForgeConfigForPlatform(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	writeLintAgentFixture(t, dir, "my-agent")
	harnessYAML := `role: triage
agent: agents/my-agent.md
policy: policies/base.yaml
forge:
  github:
    env:
      runner:
        GITHUB_ISSUE_URL: ${GITHUB_ISSUE_URL}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	t.Run("forge applied for platform", func(t *testing.T) {
		cmd := newLintCmd()
		out, err := executeCmd(cmd, []string{"--fullsend-dir", dir, "--forge", "github"})
		require.NoError(t, err)
		// With a platform, the forge section is merged into the top-level
		// env before linting, so the diagnostic reports the merged field.
		assert.Contains(t, out, "env.runner: GITHUB_ISSUE_URL is deprecated")
	})

	t.Run("forge not applied without platform", func(t *testing.T) {
		// detectForgePlatform falls back to CI env vars; clear them so
		// omitting --forge really means no platform, even when the tests
		// themselves run in CI.
		t.Setenv("GITHUB_ACTIONS", "")
		t.Setenv("GITLAB_CI", "")
		cmd := newLintCmd()
		out, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
		require.NoError(t, err)
		// Without a platform the forge section is left unconsumed, so the
		// diagnostic reports the forge-positioned field instead.
		assert.Contains(t, out, "forge[github].env.runner: GITHUB_ISSUE_URL is deprecated")
	})

	t.Run("forge applied via CI env detection", func(t *testing.T) {
		t.Setenv("GITHUB_ACTIONS", "true")
		cmd := newLintCmd()
		out, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
		require.NoError(t, err)
		// GITHUB_ACTIONS=true selects the github platform without --forge,
		// so the forge section merges like the explicit-flag case.
		assert.Contains(t, out, "env.runner: GITHUB_ISSUE_URL is deprecated")
	})
}

func TestLintCommand_LintsOverlayForPlatform(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	writeLintAgentFixture(t, dir, "my-agent")
	harnessYAML := `role: triage
agent: agents/my-agent.md
policy: policies/base.yaml
overlays:
  - when: runtime.forge == "github"
    env:
      runner:
        GITHUB_ISSUE_URL: ${GITHUB_ISSUE_URL}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	cmd := newLintCmd()
	out, err := executeCmd(cmd, []string{"--fullsend-dir", dir, "--forge", "github"})
	require.NoError(t, err)
	assert.Contains(t, out, "GITHUB_ISSUE_URL is deprecated")
}

func TestLintCommand_LoadsHarness_WithDiagnostic(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	writeLintAgentFixture(t, dir, "my-agent")
	harnessYAML := `role: triage
agent: agents/my-agent.md
policy: policies/base.yaml
env:
  runner:
    GITHUB_ISSUE_URL: ${GITHUB_ISSUE_URL}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	cmd := newLintCmd()
	out, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.NoError(t, err)
	assert.Contains(t, out, "my-agent")
	assert.Contains(t, out, "GITHUB_ISSUE_URL is deprecated")
}

func TestLintCommand_LoadsHarness_ResolveError(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/missing.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))

	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving harness for agent \"my-agent\"")
}

func TestLintCommand_AggregatesErrorsAcrossAgents(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: first-agent
    source: harness/missing-first.yaml
  - name: second-agent
    source: harness/missing-second.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))

	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving harness for agent \"first-agent\"")
	assert.Contains(t, err.Error(), "resolving harness for agent \"second-agent\"")
}

func TestLintCommand_ContinuesLintingAfterAgentError(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: broken-agent
    source: harness/missing.yaml
  - name: ok-agent
    source: harness/ok.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	writeLintAgentFixture(t, dir, "ok")
	harnessYAML := `role: triage
agent: agents/ok.md
policy: policies/base.yaml
env:
  runner:
    GITHUB_ISSUE_URL: ${GITHUB_ISSUE_URL}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "ok.yaml"), []byte(harnessYAML), 0o644))

	cmd := newLintCmd()
	out, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving harness for agent \"broken-agent\"")
	assert.Contains(t, out, "ok-agent")
	assert.Contains(t, out, "GITHUB_ISSUE_URL is deprecated")
}

func TestLintCommand_LoadsHarness_LoadError(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte("invalid:\n  - yaml: ["), 0o644))

	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading harness for agent \"my-agent\"")
}

func TestLintCommand_InvalidTriggerFailsLoad(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	harnessYAML := `role: triage
agent: agents/my-agent.md
trigger: 1 + 1
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	cmd := newLintCmd()
	out, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading harness for agent \"my-agent\"")
	assert.Contains(t, err.Error(), "trigger expression must evaluate to bool")
	assert.Contains(t, out, "my-agent resolved from config")
}

func TestLintCommand_AllowedRemoteResourcesWithoutAllowRuntimeFetch(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	writeLintAgentFixture(t, dir, "my-agent")
	harnessYAML := `role: triage
agent: agents/my-agent.md
policy: policies/base.yaml
allowed_remote_resources:
  - "https://raw.githubusercontent.com/fullsend-ai/agents/"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	cmd := newLintCmd()
	out, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	// Warning-level diagnostic: reported but not fatal.
	require.NoError(t, err)
	assert.Contains(t, out, "allowed_remote_resources: allowed_remote_resources is declared without allow_runtime_fetch: true")
	assert.Contains(t, out, "deprecated")
}

func TestLintCommand_PreflightCheckPathLikeFails(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	// The harness still loads and validates cleanly — the path-like
	// preflight_check is an error-level lint diagnostic (ADR 0128
	// Decision 2), not a load error — so the plain, non-strict lint run
	// fails on the diagnostic itself: a bare path can never resolve in a
	// fresh consumer checkout.
	writeLintAgentFixture(t, dir, "my-agent")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scripts", "validate.sh"), []byte("#!/bin/sh\n"), 0o644))
	harnessYAML := `role: triage
agent: agents/my-agent.md
policy: policies/base.yaml
validation_loop:
  script: scripts/validate.sh
  preflight_check: scripts/common-preflight.sh
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	cmd := newLintCmd()
	out, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.Error(t, err)
	assert.Contains(t, out, "error: validation_loop.preflight_check")
	assert.Contains(t, out, "sh -c")
	assert.Contains(t, err.Error(), "lint found 1 error-level diagnostic(s)")
}

func TestLintCommand_StrictFailsOnWarningLevelDiagnostics(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	writeLintAgentFixture(t, dir, "my-agent")
	// allowed_remote_resources without allow_runtime_fetch yields exactly one
	// warning-level diagnostic and no error-level ones, so this fixture
	// exercises the --strict gate the preflight error-level rewrite no longer
	// covers.
	harnessYAML := `role: triage
agent: agents/my-agent.md
policy: policies/base.yaml
allowed_remote_resources:
  - "https://raw.githubusercontent.com/fullsend-ai/agents/"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	t.Run("warning without strict", func(t *testing.T) {
		cmd := newLintCmd()
		out, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
		require.NoError(t, err)
		assert.Contains(t, out, "warning: allowed_remote_resources")
		assert.Contains(t, out, "deprecated")
	})

	t.Run("fails with strict", func(t *testing.T) {
		cmd := newLintCmd()
		_, err := executeCmd(cmd, []string{"--fullsend-dir", dir, "--strict"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "lint found 1 warning-level diagnostic(s) (--strict)")
	})
}

func TestLintCommand_MissingPolicyFails(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "my-agent.md"), []byte("You are an agent."), 0o644))
	harnessYAML := `role: triage
agent: agents/my-agent.md
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `validating files for agent "my-agent"`)
	assert.Contains(t, err.Error(), "policy field is required")
}

func TestLintCommand_MissingAgentFileFails(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	writePolicyFixture(t, dir)
	harnessYAML := `role: triage
agent: agents/my-agent.md
policy: policies/base.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `validating files for agent "my-agent"`)
	assert.Contains(t, err.Error(), "agent:")
	assert.Contains(t, err.Error(), "no such file or directory")
}

func TestLintCommand_MissingScriptFails(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	writeLintAgentFixture(t, dir, "my-agent")
	harnessYAML := `role: triage
agent: agents/my-agent.md
policy: policies/base.yaml
validation_loop:
  script: scripts/validate.sh
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `validating files for agent "my-agent"`)
	assert.Contains(t, err.Error(), "validation_loop.script")
	assert.Contains(t, err.Error(), "no such file or directory")
}

func TestLintCommand_RelativePathEscapeFails(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	writePolicyFixture(t, dir)
	harnessYAML := `role: triage
agent: ../outside.md
policy: policies/base.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `resolving paths for agent "my-agent"`)
	assert.Contains(t, err.Error(), "resolves outside fullsend directory")
}

func TestLintCommand_RuntimeExpandedPathsSkipped(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scripts", "validate.sh"), []byte("#!/bin/sh\n"), 0o644))
	writeLintAgentFixture(t, dir, "my-agent")
	// ${VAR} paths expand at run time (run expands them before the file
	// checks); lint has no run env, so they are skipped, not stat-checked.
	harnessYAML := `role: triage
agent: agents/my-agent.md
policy: policies/base.yaml
host_files:
  - src: ${LINT_TEST_VAR}/report.txt
    dest: /sandbox/report.txt
validation_loop:
  script: scripts/validate.sh
  schema: ${LINT_TEST_VAR}/schema.json
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	cmd := newLintCmd()
	out, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.NoError(t, err)
	assert.NotContains(t, out, "error:")
}

func TestLintCommand_URLPolicyNotFetched(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
allowed_remote_resources:
  - "https://example.com/"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "my-agent.md"), []byte("You are an agent."), 0o644))
	// URL references are skipped by the file checks — run resolves them
	// to cache paths first; lint validates what is on disk (--offline
	// proves no fetch is attempted).
	harnessYAML := `role: triage
agent: agents/my-agent.md
policy: https://example.com/policy.yaml#sha256=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
allowed_remote_resources:
  - "https://example.com/"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"--fullsend-dir", dir, "--offline"})
	require.NoError(t, err)
}

func TestLintCommand_URLReferenceNotAllowlistedFails(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	writeLintAgentFixture(t, dir, "my-agent")
	// run rejects a URL that neither allowlist covers before fetching it;
	// lint must too.
	harnessYAML := `role: triage
agent: agents/my-agent.md
policy: https://example.com/policy.yaml#sha256=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"--fullsend-dir", dir, "--offline"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `policy: URL "https://example.com/policy.yaml" is not in allowed_remote_resources`)
}

func TestLintCommand_SkipsDisabledAgents(t *testing.T) {
	stubLintGitHubToken(t, "test-token")
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: disabled-agent
    source: harness/nonexistent.yaml
    enabled: false
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))

	cmd := newLintCmd()
	out, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestLintCommand_OverrideOnlyAgentOfflineErrors(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: code
    runtime: claude
    model: opus
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))

	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"--fullsend-dir", dir, "--offline"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving harness for agent \"code\"")
	assert.Contains(t, err.Error(), "agents-repo fallback unavailable")
}

func TestLintCommand_InvalidConfigTriggerReportedOnce(t *testing.T) {
	stubLintGitHubToken(t, "test-token")
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: review
    trigger: '"yes"'
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))

	cmd := newLintCmd()
	out, err := executeCmd(cmd, []string{"--fullsend-dir", dir, "--offline"})
	require.Error(t, err)
	assert.Contains(t, out, "config: error: config.yaml")
	// Reported once, by config validation: per-agent resolution would
	// repeat the same whole-list validation error.
	assert.Equal(t, 1, strings.Count(out+err.Error(), "must evaluate to bool"))
	assert.NotContains(t, err.Error(), "resolving harness")
}

func TestLintCommand_RemoteResourceNotInOrgAllowlistFails(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
allowed_remote_resources:
  - "https://raw.githubusercontent.com/fullsend-ai/agents/"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	writeLintAgentFixture(t, dir, "my-agent")
	harnessYAML := `role: triage
agent: agents/my-agent.md
policy: https://example.com/policy.yaml#sha256=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
allowed_remote_resources:
  - "https://example.com/"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"--fullsend-dir", dir, "--offline"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `validating allowed remote resources for agent "my-agent"`)
}

func TestLintCommand_MissingLocalProviderFails(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	writeLintAgentFixture(t, dir, "my-agent")
	harnessYAML := `role: triage
agent: agents/my-agent.md
policy: policies/base.yaml
providers:
  - providers/missing.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `resolving local profiles/providers for agent "my-agent"`)
}

func TestLintCommand_MissingLocalProviderWithURLReferenceFails(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
allowed_remote_resources:
  - "https://example.com/"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	writeLintAgentFixture(t, dir, "my-agent")
	// A URL reference must not stop the local provider check, and the
	// check must not fetch it (--offline with nothing cached).
	harnessYAML := `role: triage
agent: agents/my-agent.md
policy: https://example.com/policy.yaml#sha256=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
allowed_remote_resources:
  - "https://example.com/"
providers:
  - providers/missing.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"--fullsend-dir", dir, "--offline"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `resolving local profiles/providers for agent "my-agent"`)
}

func TestLintCommand_InvalidConfigDoesNotHideInvalidAgentEntry(t *testing.T) {
	stubLintGitHubToken(t, "test-token")
	dir := t.TempDir()
	cfg := `version: "2"
agents:
  - name: review
    trigger: '"yes"'
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))

	cmd := newLintCmd()
	out, err := executeCmd(cmd, []string{"--fullsend-dir", dir, "--offline"})
	require.Error(t, err)
	assert.Contains(t, out, `unsupported version "2"`)
	assert.Contains(t, err.Error(), "must evaluate to bool")
}

func TestLintCommand_DeniedEnvReferenceFails(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	writeLintAgentFixture(t, dir, "my-agent")
	// run refuses OIDC credential variables whatever the host holds.
	harnessYAML := `role: triage
agent: agents/my-agent.md
policy: policies/base.yaml
env:
  runner:
    T: ${ACTIONS_ID_TOKEN_REQUEST_TOKEN}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "set-on-host")

	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `validating env for agent "my-agent"`)
}

func TestLintCommand_RejectsExtraPositionalArgs(t *testing.T) {
	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"one", "two"})
	require.Error(t, err)
}

// writeScopedLintFixture writes a config registering the given agents[]
// YAML plus a valid local harness for each of the named agents: each
// harness carries one warning-level diagnostic (GITHUB_ISSUE_URL), so the
// output shows which agents were linted.
func writeScopedLintFixture(t *testing.T, agentsYAML string, harnessAgents ...string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := "version: \"1\"\nagents:\n" + agentsYAML
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	for _, name := range harnessAgents {
		writeLintAgentFixture(t, dir, name)
		harnessYAML := "role: triage\nagent: agents/" + name + ".md\npolicy: policies/base.yaml\n" +
			"env:\n  runner:\n    GITHUB_ISSUE_URL: ${GITHUB_ISSUE_URL}\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", name+".yaml"), []byte(harnessYAML), 0o644))
	}
	return dir
}

func TestLintCommand_ScopedLintsOnlySelectedAgent(t *testing.T) {
	stubLintGitHubToken(t, "test-token")
	dir := writeScopedLintFixture(t, `  - name: alpha
    source: harness/alpha.yaml
  - name: beta
    source: harness/beta.yaml
`, "alpha", "beta")

	t.Run("no name lints every agent", func(t *testing.T) {
		out, err := executeCmd(newLintCmd(), []string{"--fullsend-dir", dir})
		require.NoError(t, err)
		assert.Contains(t, out, "alpha: warning:")
		assert.Contains(t, out, "beta: warning:")
	})

	t.Run("name lints only that agent", func(t *testing.T) {
		out, err := executeCmd(newLintCmd(), []string{"--fullsend-dir", dir, "beta"})
		require.NoError(t, err)
		assert.Contains(t, out, "beta: warning:")
		assert.NotContains(t, out, "alpha")
	})

	t.Run("name matches case-insensitively", func(t *testing.T) {
		out, err := executeCmd(newLintCmd(), []string{"--fullsend-dir", dir, "BETA"})
		require.NoError(t, err)
		assert.Contains(t, out, "beta: warning:")
		assert.NotContains(t, out, "alpha")
	})
}

func TestLintCommand_ScopedSkipsBrokenSiblingHarness(t *testing.T) {
	stubLintGitHubToken(t, "test-token")
	// broken has a valid agents[] entry whose harness does not exist: only
	// per-agent resolution finds that, and a scoped lint does not resolve
	// other agents.
	dir := writeScopedLintFixture(t, `  - name: broken
    source: harness/missing.yaml
  - name: ok
    source: harness/ok.yaml
`, "ok")

	out, err := executeCmd(newLintCmd(), []string{"--fullsend-dir", dir, "ok"})
	require.NoError(t, err)
	assert.Contains(t, out, "ok: warning:")
	assert.NotContains(t, out, "broken")
	assert.NotContains(t, out, "other agent")

	_, err = executeCmd(newLintCmd(), []string{"--fullsend-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `resolving harness for agent "broken"`)
}

func TestLintCommand_ScopedReportsInvalidSiblingEntriesAsWarning(t *testing.T) {
	stubLintGitHubToken(t, "test-token")
	cases := []struct {
		name     string
		siblings string
		want     string
	}{
		{
			name: "one sibling",
			siblings: `  - name: review
    trigger: '"yes"'
`,
			want: "config: warning: agents: 1 other agent has lint errors; run `fullsend lint` to inspect them",
		},
		{
			name: "two siblings",
			siblings: `  - name: review
    trigger: '"yes"'
  - name: insecure
    source: http://example.com/insecure.yaml
`,
			want: "config: warning: agents: 2 other agents have lint errors; run `fullsend lint` to inspect them",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeScopedLintFixture(t, tc.siblings+`  - name: my-agent
    source: harness/my-agent.yaml
`, "my-agent")

			out, err := executeCmd(newLintCmd(), []string{"--fullsend-dir", dir, "my-agent"})
			require.NoError(t, err, "sibling entry errors are warnings in a scoped lint")
			assert.Contains(t, out, tc.want)
			assert.Contains(t, out, "my-agent: warning:", "the selected agent is still linted")

			_, err = executeCmd(newLintCmd(), []string{"--fullsend-dir", dir, "my-agent", "--strict"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "(--strict)")

			// The full lint still reports the invalid entry as an error.
			_, err = executeCmd(newLintCmd(), []string{"--fullsend-dir", dir, "--offline"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "lint found 1 error-level diagnostic(s)")
		})
	}
}

func TestLintCommand_ScopedUnknownAgentFails(t *testing.T) {
	stubLintGitHubToken(t, "test-token")

	t.Run("lists enabled agents", func(t *testing.T) {
		dir := writeScopedLintFixture(t, `  - name: review
    source: harness/review.yaml
  - name: alpha
    source: harness/alpha.yaml
  - name: off
    source: harness/off.yaml
    enabled: false
`, "review", "alpha")

		out, err := executeCmd(newLintCmd(), []string{"--fullsend-dir", dir, "revieww"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `agent "revieww" is not registered in config.yaml; registered agents that can be linted: alpha, review`)
		assert.NotContains(t, err.Error(), "off")
		assert.NotContains(t, out, "warning:", "no agent is linted")
	})

	t.Run("no enabled agents", func(t *testing.T) {
		dir := writeScopedLintFixture(t, `  - name: off
    source: harness/off.yaml
    enabled: false
`)
		_, err := executeCmd(newLintCmd(), []string{"--fullsend-dir", dir, "review"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `agent "review" is not registered in config.yaml, and no enabled agents are`)
	})

	t.Run("empty name", func(t *testing.T) {
		dir := writeScopedLintFixture(t, `  - name: review
    source: harness/review.yaml
`, "review")
		_, err := executeCmd(newLintCmd(), []string{"--fullsend-dir", dir, ""})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "agent name must not be empty")
	})
}

func TestLintCommand_ScopedDisabledAgentFails(t *testing.T) {
	stubLintGitHubToken(t, "test-token")
	cases := []struct {
		name   string
		agents string
	}{
		{name: "enabled false", agents: `  - name: off
    source: harness/off.yaml
    enabled: false
`},
		{name: "later disable wins", agents: `  - name: off
    source: harness/off.yaml
  - name: off
    enabled: false
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeScopedLintFixture(t, tc.agents, "off")
			out, err := executeCmd(newLintCmd(), []string{"--fullsend-dir", dir, "off"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), `agent "off" is disabled in config.yaml (enabled: false)`)
			assert.NotContains(t, out, "off: warning:", "a disabled agent is not linted")
		})
	}
}

func TestLintCommand_ScopedSelectedAgentErrorsFail(t *testing.T) {
	stubLintGitHubToken(t, "test-token")

	t.Run("invalid entry", func(t *testing.T) {
		dir := writeScopedLintFixture(t, `  - name: review
    trigger: '"yes"'
`)
		_, err := executeCmd(newLintCmd(), []string{"--fullsend-dir", dir, "review", "--offline"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid agent config")
		assert.Contains(t, err.Error(), "must evaluate to bool")
		assert.NotContains(t, err.Error(), "resolving harness")
	})

	t.Run("harness error", func(t *testing.T) {
		dir := writeScopedLintFixture(t, `  - name: ok
    source: harness/ok.yaml
  - name: broken
    source: harness/missing.yaml
`, "ok")
		_, err := executeCmd(newLintCmd(), []string{"--fullsend-dir", dir, "broken"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `resolving harness for agent "broken"`)
	})
}

func TestLintCommand_ScopedStillValidatesConfig(t *testing.T) {
	stubLintGitHubToken(t, "test-token")

	t.Run("invalid yaml", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("invalid:\n  - yaml: ["), 0o644))
		_, err := executeCmd(newLintCmd(), []string{"--fullsend-dir", dir, "review"})
		require.Error(t, err)
	})

	t.Run("unsupported version", func(t *testing.T) {
		dir := writeScopedLintFixture(t, `  - name: my-agent
    source: harness/my-agent.yaml
`, "my-agent")
		cfg := `version: "999"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))

		out, err := executeCmd(newLintCmd(), []string{"--fullsend-dir", dir, "my-agent"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "lint found 1 error-level diagnostic(s)")
		assert.Contains(t, out, `config: error: config.yaml: unsupported version "999"`)
		assert.Contains(t, out, "my-agent: warning:", "the selected agent is still linted")
	})
}
