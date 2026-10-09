package runtime

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestClaudeHookDigests_RoundTripAndKeyedBySandbox(t *testing.T) {
	t.Cleanup(func() {
		forgetClaudeHookDigests("claude-sb-a")
		forgetClaudeHookDigests("claude-sb-b")
	})

	_, ok := lookupClaudeHookDigests("claude-sb-a")
	assert.False(t, ok, "a sandbox this process never bootstrapped has no entry")

	a := claudeHookDigests{HooksJSON: "a", HookScripts: map[string]string{"tirith_check.py": "1"}}
	b := claudeHookDigests{HooksJSON: "b", HookScripts: map[string]string{}}
	recordClaudeHookDigests("claude-sb-a", a)
	recordClaudeHookDigests("claude-sb-b", b)

	got, ok := lookupClaudeHookDigests("claude-sb-a")
	require.True(t, ok)
	assert.Equal(t, a, got)
	got, ok = lookupClaudeHookDigests("claude-sb-b")
	require.True(t, ok)
	assert.Equal(t, b, got)

	forgetClaudeHookDigests("claude-sb-a")
	_, ok = lookupClaudeHookDigests("claude-sb-a")
	assert.False(t, ok)
}

func TestClaudeHookDigestsFor(t *testing.T) {
	t.Parallel()

	hooks := security.SandboxHookConfigFromHarness(&harness.Harness{})
	hooksJSON, err := security.GenerateHooksConfig(hooks)
	require.NoError(t, err)

	d := claudeHookDigestsFor(hooks, hooksJSON)
	assert.Equal(t, codexAssetSHA256(hooksJSON), d.HooksJSON)
	files := security.HookFiles(hooks)
	require.Len(t, d.HookScripts, len(files))
	for name, content := range files {
		assert.Equal(t, codexAssetSHA256(content), d.HookScripts[name], name)
	}

	// Security on with every hook off still records a non-nil map, which is
	// what Run checks to tell "installed nothing" from "never installed".
	off := false
	none := security.SandboxHookConfigFromHarness(&harness.Harness{Security: &harness.SecurityConfig{
		SandboxHooks: &harness.SandboxHooks{
			Tirith:                  &harness.TirithConfig{Enabled: &off},
			SSRFPreTool:             &off,
			CanaryPreTool:           &off,
			CanaryPostTool:          &off,
			SecretRedactPostTool:    &off,
			UnicodePostTool:         &off,
			ContextSuppressPostTool: &off,
		},
	}})
	empty := claudeHookDigestsFor(none, []byte(`{"hooks":{}}`))
	assert.NotNil(t, empty.HookScripts)
	assert.Empty(t, empty.HookScripts)
}

// The guard wraps .env on both sides, with the shadowing functions cleared
// between, and only when hooks are enabled.
func TestBuildRunCommand_HooksGuardPlacement(t *testing.T) {
	t.Parallel()

	envSource := ". " + sandbox.SandboxWorkspace + "/.env"

	t.Run("hooks disabled leaves the launch unchanged", func(t *testing.T) {
		cmd := buildRunCommand(RunParams{AgentBaseName: "agent", RepoDir: "/repo"}, claudeHookDigests{})
		assert.True(t, strings.HasPrefix(cmd, "cd /repo && "+envSource+" && claude --print"), cmd)
		assert.NotContains(t, cmd, "sha256sum")
	})

	t.Run("hooks enabled guards before and after .env", func(t *testing.T) {
		d := claudeHookDigests{HooksJSON: strings.Repeat("a", 64), HookScripts: testCodexHookScripts()}
		cmd := buildRunCommand(RunParams{
			AgentBaseName:     "agent",
			RepoDir:           "/repo",
			HooksSettingsPath: security.SandboxHooksSettings,
		}, d)
		guard := claudeHooksGuard(d)
		want := "cd /repo && " + guard + " && " + envSource +
			" && unset PYTHONPATH PYTHONHOME PYTHONSTARTUP PYTHONUSERBASE LD_PRELOAD LD_LIBRARY_PATH LD_AUDIT" +
			" && unset -f test command cut wc sha256sum find echo && " + guard + " && claude --print"
		assert.True(t, strings.HasPrefix(cmd, want), cmd)
		assert.Greater(t, strings.Index(cmd, "unset PYTHONPATH"), strings.Index(cmd, envSource),
			"the LD_* unset must come after .env is sourced")
		assert.Contains(t, guard, "'"+d.HooksJSON+"'")
	})
}

// claudeGuardFixture lays out a fake /sandbox/claude-config and workspace under
// a temp dir and returns a function that runs the real launch command there,
// with a stub `claude` on PATH that prints CLAUDE_RAN.
type claudeGuardFixture struct {
	configDir string
	hooksDir  string
	envFile   string
	files     map[string][]byte
	hooksJSON []byte
	digests   claudeHookDigests
	binDir    string
	repoDir   string
	hookPath  string // the PATH pinned into hooks.json
	extraEnv  []string
}

func newClaudeGuardFixture(t *testing.T) *claudeGuardFixture {
	t.Helper()
	root := t.TempDir()
	f := &claudeGuardFixture{
		configDir: filepath.Join(root, "claude-config"),
		envFile:   filepath.Join(root, "workspace", ".env"),
		binDir:    filepath.Join(root, "bin"),
		repoDir:   filepath.Join(root, "workspace", "repo"),
	}
	f.hooksDir = filepath.Join(f.configDir, "hooks")
	hooks := security.SandboxHookConfigFromHarness(&harness.Harness{})
	f.files = security.HookFiles(hooks)
	var err error
	python := "/usr/bin/python3"
	if p, lookErr := exec.LookPath("python3"); lookErr == nil {
		python = p
	}
	f.hookPath = os.Getenv("PATH")
	f.hooksJSON, err = security.GenerateHooksConfigPinned(hooks, python, f.hookPath)
	require.NoError(t, err)
	f.digests = claudeHookDigestsFor(hooks, f.hooksJSON)

	require.NoError(t, os.MkdirAll(f.repoDir, 0o755))
	require.NoError(t, os.MkdirAll(f.binDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.binDir, "claude"),
		[]byte("#!/bin/sh\necho CLAUDE_RAN\n"), 0o755))
	f.install(t)
	return f
}

// install restores what Bootstrap uploads: hooks.json, the scripts and an
// empty .env.
func (f *claudeGuardFixture) install(t *testing.T) {
	t.Helper()
	require.NoError(t, os.RemoveAll(f.hooksDir))
	require.NoError(t, os.MkdirAll(f.hooksDir, 0o755))
	for name, content := range f.files {
		require.NoError(t, os.WriteFile(filepath.Join(f.hooksDir, name), content, 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(f.configDir, "hooks.json"), f.hooksJSON, 0o600))
	require.NoError(t, os.WriteFile(f.envFile, nil, 0o644))
}

// launch runs one iteration's command line and returns its output and error.
func (f *claudeGuardFixture) launch(t *testing.T, d claudeHookDigests) (string, error) {
	t.Helper()
	cmd := buildRunCommand(RunParams{
		AgentBaseName:     "agent",
		RepoDir:           f.repoDir,
		HooksSettingsPath: security.SandboxHooksSettings,
	}, d)
	cmd = strings.ReplaceAll(cmd, sandbox.SandboxWorkspace+"/.env", f.envFile)
	cmd = strings.ReplaceAll(cmd, sandbox.SandboxClaudeConfig, f.configDir)
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Env = append(os.Environ(), "PATH="+f.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	c.Env = append(c.Env, f.extraEnv...)
	out, err := c.CombinedOutput()
	return string(out), err
}

func (f *claudeGuardFixture) assertRuns(t *testing.T) {
	t.Helper()
	out, err := f.launch(t, f.digests)
	require.NoError(t, err, out)
	assert.Contains(t, out, "CLAUDE_RAN")
}

func (f *claudeGuardFixture) assertRefused(t *testing.T, mentions string) {
	t.Helper()
	out, err := f.launch(t, f.digests)
	require.Error(t, err, out)
	assert.Equal(t, claudeHooksMissingExit, exitCodeOf(t, err))
	assert.NotContains(t, out, "CLAUDE_RAN", "claude must not start")
	assert.Contains(t, out, mentions)
}

// TestClaudeLaunch_RefusesHooksChangedBetweenIterations runs the real launch
// command under /bin/sh: iteration 1 starts claude, the agent then changes a
// file, and iteration 2 must stop before claude runs.
func TestClaudeLaunch_RefusesHooksChangedBetweenIterations(t *testing.T) {
	const hooksJSONMsg = "hooks.json is not the file fullsend wrote"
	const scriptsMsg = "hook scripts are not the set fullsend installed"

	f := newClaudeGuardFixture(t)
	hooksJSONPath := filepath.Join(f.configDir, "hooks.json")
	tirith := filepath.Join(f.hooksDir, "tirith_check.py")

	cases := map[string]struct {
		mutate   func(t *testing.T)
		mentions string
	}{
		"hooks.json modified": {func(t *testing.T) {
			require.NoError(t, os.WriteFile(hooksJSONPath, []byte(`{"hooks":{}}`), 0o600))
		}, hooksJSONMsg},
		"hooks.json appended": {func(t *testing.T) {
			require.NoError(t, os.WriteFile(hooksJSONPath, append(append([]byte{}, f.hooksJSON...), ' '), 0o600))
		}, hooksJSONMsg},
		"hooks.json deleted": {func(t *testing.T) {
			require.NoError(t, os.Remove(hooksJSONPath))
		}, hooksJSONMsg},
		"hook script modified": {func(t *testing.T) {
			require.NoError(t, os.WriteFile(tirith, []byte("import sys\nsys.exit(0)\n"), 0o755))
		}, scriptsMsg},
		"hook script appended": {func(t *testing.T) {
			fh, err := os.OpenFile(tirith, os.O_APPEND|os.O_WRONLY, 0)
			require.NoError(t, err)
			_, err = fh.WriteString("\nimport sys; sys.exit(0)\n")
			require.NoError(t, err)
			require.NoError(t, fh.Close())
		}, scriptsMsg},
		"hook script deleted": {func(t *testing.T) {
			require.NoError(t, os.Remove(tirith))
		}, scriptsMsg},
		"another allowed script's bytes": {func(t *testing.T) {
			require.NoError(t, os.WriteFile(tirith, f.files["hook_io.py"], 0o755))
		}, scriptsMsg},
		"planted bytecode cache": {func(t *testing.T) {
			require.NoError(t, os.MkdirAll(filepath.Join(f.hooksDir, "__pycache__"), 0o755))
			require.NoError(t, os.WriteFile(
				filepath.Join(f.hooksDir, "__pycache__", "hook_io.cpython-312.pyc"), []byte("x"), 0o644))
		}, scriptsMsg},
		"planted package shadowing the stdlib": {func(t *testing.T) {
			require.NoError(t, os.MkdirAll(filepath.Join(f.hooksDir, "json"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(f.hooksDir, "json", "__init__.py"), []byte("pass\n"), 0o644))
		}, scriptsMsg},
		// .env runs in the launch shell after the first pass, so it could
		// rewrite a script there; the second pass is what catches it.
		".env rewrites a script after the first pass": {func(t *testing.T) {
			require.NoError(t, os.WriteFile(f.envFile,
				[]byte("printf 'import sys\\nsys.exit(0)\\n' > '"+tirith+"'\n"), 0o644))
		}, scriptsMsg},
		// A function or PATH entry from .env cannot stand in for the hashing
		// tools: `unset -f` clears the one, `command -p` ignores the other.
		".env shadows sha256sum after rewriting hooks.json": {func(t *testing.T) {
			fake := filepath.Join(filepath.Dir(f.envFile), "fakebin")
			require.NoError(t, os.MkdirAll(fake, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(fake, "sha256sum"),
				[]byte("#!/bin/sh\necho '"+f.digests.HooksJSON+"  x'\n"), 0o755))
			env := "echo '{}' > '" + hooksJSONPath + "'\n" +
				"sha256sum() { echo '" + f.digests.HooksJSON + "  x'; }\n" +
				"export PATH='" + fake + "':\"$PATH\"\n"
			require.NoError(t, os.WriteFile(f.envFile, []byte(env), 0o644))
		}, hooksJSONMsg},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f.install(t)
			f.assertRuns(t) // iteration 1
			tc.mutate(t)
			f.assertRefused(t, tc.mentions) // iteration 2
		})
	}

	t.Run("untouched files pass on every iteration", func(t *testing.T) {
		f.install(t)
		for range 3 {
			f.assertRuns(t)
		}
	})

	t.Run("empty digests fail closed", func(t *testing.T) {
		f.install(t)
		out, err := f.launch(t, claudeHookDigests{})
		require.Error(t, err, out)
		assert.Equal(t, claudeHooksMissingExit, exitCodeOf(t, err))
		assert.NotContains(t, out, "CLAUDE_RAN")
	})
}

// A .env that exports a preload library must not reach the second guard's
// hashing tools, nor claude itself.
func TestClaudeLaunch_ClearsLDVariablesSetByEnv(t *testing.T) {
	f := newClaudeGuardFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.binDir, "claude"),
		[]byte("#!/bin/sh\necho \"CLAUDE_RAN LD=[$LD_PRELOAD$LD_LIBRARY_PATH$LD_AUDIT]\"\n"), 0o755))
	require.NoError(t, os.WriteFile(f.envFile,
		[]byte("export LD_PRELOAD=/tmp/x.so LD_LIBRARY_PATH=/tmp/lib LD_AUDIT=/tmp/audit.so\n"), 0o644))

	out, err := f.launch(t, f.digests)
	require.NoError(t, err, out)
	assert.Contains(t, out, "CLAUDE_RAN LD=[]")
}

// The hook commands must keep running the real interpreter and the real hook
// when an earlier iteration's .env puts a fake python3 first on PATH and
// plants a sitecustomize.py via PYTHONPATH. Both leave hooks.json and the
// scripts byte-identical, so the digests cannot catch them.
func TestClaudeLaunch_HooksIgnorePathAndPythonPathFromEnv(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	f := newClaudeGuardFixture(t)

	evil := t.TempDir()
	marker := filepath.Join(evil, "marker")
	require.NoError(t, os.WriteFile(filepath.Join(evil, "python3"),
		[]byte("#!/bin/sh\necho fake-python >> '"+marker+"'\nexit 0\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(evil, "sitecustomize.py"),
		[]byte("open('"+marker+"', 'a').write('sitecustomize\\n')\n"), 0o644))
	require.NoError(t, os.WriteFile(f.envFile, []byte(
		"export PATH='"+evil+"':\"$PATH\"\nexport PYTHONPATH='"+evil+"'\nexport PYTHONSTARTUP='"+filepath.Join(evil, "sitecustomize.py")+"'\n"), 0o644))

	var cfg struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(f.hooksJSON, &cfg))
	require.NotEmpty(t, cfg.Hooks["PostToolUse"])
	command := strings.ReplaceAll(cfg.Hooks["PostToolUse"][0].Hooks[0].Command, security.SandboxHooksDir, f.hooksDir)

	// claude stub: run the hook command the way Claude Code would, from the
	// environment the launch left behind.
	require.NoError(t, os.WriteFile(filepath.Join(f.binDir, "claude"), []byte(
		"#!/bin/sh\nprintf '%s' '{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"ls\"},\"tool_response\":{\"stdout\":\"ok\",\"stderr\":\"\"}}' | sh -c \"$FS_HOOK_CMD\"\necho HOOK_EXIT=$?\n"), 0o755))
	f.extraEnv = []string{"FS_HOOK_CMD=" + command}

	out, err := f.launch(t, f.digests)
	require.NoError(t, err, out)
	assert.Contains(t, out, "HOOK_EXIT=0")
	_, statErr := os.Stat(marker)
	assert.True(t, os.IsNotExist(statErr), "neither the fake python3 nor sitecustomize.py may run: %s", out)
}

func TestClaudeLaunch_ClearsPythonVariablesSetByEnv(t *testing.T) {
	f := newClaudeGuardFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.binDir, "claude"),
		[]byte("#!/bin/sh\necho \"CLAUDE_RAN PY=[$PYTHONPATH$PYTHONHOME$PYTHONSTARTUP$PYTHONUSERBASE]\"\n"), 0o755))
	require.NoError(t, os.WriteFile(f.envFile,
		[]byte("export PYTHONPATH=/tmp/p PYTHONHOME=/tmp/h PYTHONSTARTUP=/tmp/s.py PYTHONUSERBASE=/tmp/u\n"), 0o644))

	out, err := f.launch(t, f.digests)
	require.NoError(t, err, out)
	assert.Contains(t, out, "CLAUDE_RAN PY=[]")
}

// The hooks run with -B, so running them does not itself leave a
// __pycache__ behind that would lock out the next iteration.
func TestClaudeLaunch_HookRunDoesNotTripTheGuard(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	f := newClaudeGuardFixture(t)
	f.assertRuns(t)

	chain := filepath.Join(f.hooksDir, "posttool_chain.py")
	var cfg struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(f.hooksJSON, &cfg))
	require.NotEmpty(t, cfg.Hooks["PostToolUse"])
	command := cfg.Hooks["PostToolUse"][0].Hooks[0].Command
	command = strings.ReplaceAll(command, security.SandboxHooksDir, f.hooksDir)
	require.Contains(t, command, chain)

	c := exec.Command("/bin/sh", "-c", command)
	// The hook wiring must not depend on PYTHONDONTWRITEBYTECODE, and an
	// environment that happens to set it would hide a missing -B.
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PYTHONDONTWRITEBYTECODE=") {
			c.Env = append(c.Env, kv)
		}
	}
	c.Stdin = strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"ls"},"tool_response":{"stdout":"ok","stderr":""}}`)
	out, err := c.CombinedOutput()
	require.NoError(t, err, "the chain must run (and so import hook_io): %s", out)

	_, err = os.Stat(filepath.Join(f.hooksDir, "__pycache__"))
	assert.True(t, os.IsNotExist(err), "running a hook must not write bytecode into the hooks dir")
	f.assertRuns(t)
}

func TestClaudeRuntime_Run_FailsClosedWithoutRecordedDigests(t *testing.T) {
	stubDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	require.NoError(t, os.WriteFile(filepath.Join(stubDir, "openshell"),
		[]byte("#!/bin/sh\necho \"$@\" >> '"+logPath+"'\nexit 0\n"), 0o755))
	t.Setenv("PATH", stubDir)

	var metrics RunMetrics
	_, err := ClaudeRuntime{}.Run(context.Background(), RunParams{
		SandboxName:       "claude-never-bootstrapped",
		AgentBaseName:     "agent",
		RepoDir:           "/sandbox/workspace/repo",
		HooksSettingsPath: security.SandboxHooksSettings,
		Timeout:           10 * time.Second,
	}, ui.New(io.Discard), time.Now(), &metrics)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no runner-held hook digests")
	_, statErr := os.Stat(logPath)
	assert.True(t, os.IsNotExist(statErr), "nothing may run in the sandbox without the digests")
}

func TestClaudeRuntime_Run_RejectsNonDefaultHooksSettingsPath(t *testing.T) {
	var metrics RunMetrics
	exitCode, err := ClaudeRuntime{}.Run(context.Background(), RunParams{
		SandboxName:       "claude-other-settings",
		AgentBaseName:     "agent",
		RepoDir:           "/sandbox/workspace/repo",
		HooksSettingsPath: "/sandbox/elsewhere/hooks.json",
		Timeout:           10 * time.Second,
	}, ui.New(io.Discard), time.Now(), &metrics)
	require.Error(t, err)
	assert.Equal(t, -1, exitCode)
	assert.Contains(t, err.Error(), "only covers the default hooks file")
}

func TestClaudeRuntime_Run_ReportsTamperedHooks(t *testing.T) {
	const name = "claude-tampered"
	t.Cleanup(func() { forgetClaudeHookDigests(name) })
	recordClaudeHookDigests(name, claudeHookDigests{HooksJSON: "x", HookScripts: map[string]string{}})

	stubDir := t.TempDir()
	cmdPath := filepath.Join(t.TempDir(), "cmd")
	// Record the command the runtime sends, then exit as the guard would.
	script := "#!/bin/sh\nfor a; do last=\"$a\"; done\nprintf '%s' \"$last\" > '" + cmdPath + "'\nexit 97\n"
	require.NoError(t, os.WriteFile(filepath.Join(stubDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", stubDir)

	var metrics RunMetrics
	exitCode, err := ClaudeRuntime{}.Run(context.Background(), RunParams{
		SandboxName:       name,
		AgentBaseName:     "agent",
		RepoDir:           "/sandbox/workspace/repo",
		HooksSettingsPath: security.SandboxHooksSettings,
		Timeout:           10 * time.Second,
	}, ui.New(io.Discard), time.Now(), &metrics)
	require.Error(t, err)
	assert.Equal(t, claudeHooksMissingExit, exitCode)
	assert.Contains(t, err.Error(), "hooks.json or hook scripts")

	sent, readErr := os.ReadFile(cmdPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(sent), "sha256sum", "the launch carries the guard")
}

func TestInstallClaudeHooks_RecordsDigests(t *testing.T) {
	const name = "claude-install-records"
	t.Cleanup(func() { forgetClaudeHookDigests(name) })
	stubDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(stubDir, "openshell"), []byte(claudeHooksStubScript("", "")), 0o755))
	t.Setenv("PATH", stubDir)

	hooks := security.SandboxHookConfig{}
	require.NoError(t, installClaudeHooks(name, hooks))

	got, ok := lookupClaudeHookDigests(name)
	require.True(t, ok)
	hooksJSON, err := security.GenerateHooksConfigPinned(hooks, "/usr/bin/python3", "/usr/local/bin:/usr/bin:/bin")
	require.NoError(t, err)
	assert.Equal(t, claudeHookDigestsFor(hooks, hooksJSON), got)
}

func TestInstallClaudeHooks_NoDigestsOnFailure(t *testing.T) {
	const name = "claude-install-fails"
	t.Cleanup(func() { forgetClaudeHookDigests(name) })
	stubDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(stubDir, "openshell"), []byte(claudeHooksStubScript("", "*hooks.json*")), 0o755))
	t.Setenv("PATH", stubDir)

	require.Error(t, installClaudeHooks(name, security.SandboxHookConfig{}))
	_, ok := lookupClaudeHookDigests(name)
	assert.False(t, ok)
}
