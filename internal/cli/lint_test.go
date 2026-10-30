package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

func TestRootCommand_HasLintSubcommand(t *testing.T) {
	cmd := newRootCmd()
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Use == "lint" {
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

// TestLintCommand_ReportsReservedSandboxEnvKeys pins the wiring in the lint
// loop: run and lock already report the env.sandbox keys the gate drops, and
// lint must report the same warning so the loss is visible at commit time.
func TestLintCommand_ReportsReservedSandboxEnvKeys(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	writeLintAgentFixture(t, dir, "my-agent")
	// HTTPS_PROXY is on the credential deny-list; https_proxy checks the
	// case-insensitive match; MY_TOOL_SETTING survives the gate.
	harnessYAML := `role: triage
agent: agents/my-agent.md
policy: policies/base.yaml
env:
  sandbox:
    HTTPS_PROXY: http://proxy.example:8080
    https_proxy: http://proxy.example:8080
    MY_TOOL_SETTING: keep-me
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	want := reservedSandboxEnvDiagnostics(&harness.Harness{Env: &harness.EnvConfig{Sandbox: map[string]string{
		"HTTPS_PROXY":     "http://proxy.example:8080",
		"https_proxy":     "http://proxy.example:8080",
		"MY_TOOL_SETTING": "keep-me",
	}}})
	require.Len(t, want, 1, "precondition: the fixture declares reserved keys")

	t.Run("warning without strict", func(t *testing.T) {
		cmd := newLintCmd()
		out, err := executeCmd(cmd, []string{"--fullsend-dir", dir})
		require.NoError(t, err, "the reserved-key diagnostic is a warning")
		assert.Contains(t, out, "my-agent: warning: env.sandbox")
		assert.Contains(t, out, want[0].Message, "lint must report the same warning as run and lock")
		assert.Contains(t, out, "HTTPS_PROXY, https_proxy")
		assert.NotContains(t, out, "MY_TOOL_SETTING", "a key that survives must not be named")
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

func TestLintCommand_RejectsPositionalArgs(t *testing.T) {
	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"garbage"})
	require.Error(t, err)
}
