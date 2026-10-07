package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestLoadRunnerSecrets_UnsetIsNoOp(t *testing.T) {
	t.Setenv(runnerSecretsEnv, "")
	require.NoError(t, os.Unsetenv(runnerSecretsEnv))

	secrets, err := loadRunnerSecrets()
	require.NoError(t, err)
	assert.Nil(t, secrets)
}

func TestLoadRunnerSecrets_BlankIsNoOpAndUnset(t *testing.T) {
	t.Setenv(runnerSecretsEnv, "  ")

	secrets, err := loadRunnerSecrets()
	require.NoError(t, err)
	assert.Nil(t, secrets)
	_, present := os.LookupEnv(runnerSecretsEnv)
	assert.False(t, present, "blank bundle must still be removed from the environment")
}

func TestLoadRunnerSecrets_ParsesAndUnsets(t *testing.T) {
	t.Setenv(runnerSecretsEnv, `{"JIRA_API_TOKEN":"jira-value-123456","CODERABBIT_API_KEY":"cr-value-123456"}`)

	secrets, err := loadRunnerSecrets()
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"JIRA_API_TOKEN":     "jira-value-123456",
		"CODERABBIT_API_KEY": "cr-value-123456",
	}, secrets)
	_, present := os.LookupEnv(runnerSecretsEnv)
	assert.False(t, present, "%s must not reach any child process", runnerSecretsEnv)
}

func TestLoadRunnerSecrets_RegistersValuesForRedaction(t *testing.T) {
	const value = "opaque-runner-secret-7689"
	t.Setenv(runnerSecretsEnv, `{"OPAQUE_VALUE":"`+value+`"}`)

	_, err := loadRunnerSecrets()
	require.NoError(t, err)

	// redactFeedback masks it even though OPAQUE_VALUE has no sensitive
	// suffix and is absent from the runner env passed in.
	out := redactFeedback("pre-script failed: auth "+value+" rejected", nil)
	assert.NotContains(t, out, value)
	res := security.NewSecretRedactor().Scan("x " + value)
	assert.NotContains(t, res.Sanitized, value)
}

func TestLoadRunnerSecrets_MasksValuesOnActions(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv(runnerSecretsEnv, `{"MULTI_LINE":"first-line-value\nsecond-line-value"}`)

	stderr := captureStderr(t, func() {
		_, err := loadRunnerSecrets()
		require.NoError(t, err)
	})
	assert.Contains(t, stderr, "::add-mask::first-line-value\n")
	assert.Contains(t, stderr, "::add-mask::second-line-value\n")
}

func TestLoadRunnerSecrets_Malformed(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":     `not-json-secret-value`,
		"array":        `["a"]`,
		"null":         `null`,
		"number value": `{"X":42}`,
		"object value": `{"X":{"a":"b"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(runnerSecretsEnv, raw)

			secrets, err := loadRunnerSecrets()
			require.Error(t, err)
			assert.Nil(t, secrets)
			assert.NotContains(t, err.Error(), "not-json-secret-value")
			_, present := os.LookupEnv(runnerSecretsEnv)
			assert.False(t, present, "a malformed bundle must still be removed")
		})
	}
}

func TestLoadRunnerSecrets_RefusedNames(t *testing.T) {
	refused := []string{
		"GH_WORKFLOW_TOKEN",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN",
		"OPENAI_API_KEY",
		"FULLSEND_ANYTHING",
		"GITHUB_TOKEN",
		"ACTIONS_RUNTIME_TOKEN",
		"RUNNER_TEMP",
		"CI_JOB_TOKEN",
		"LD_PRELOAD",
		"GH_TOKEN",
		"PUSH_TOKEN",
		"REVIEW_TOKEN",
		"GITLAB_TOKEN",
		"PATH",
		"HOME",
		"TRACEPARENT",
	}
	for _, name := range refused {
		t.Run(name, func(t *testing.T) {
			t.Setenv(runnerSecretsEnv, `{"`+name+`":"refused-value-123456"}`)

			_, err := loadRunnerSecrets()
			require.Error(t, err)
			assert.Contains(t, err.Error(), name+": name is reserved for the runner")
			assert.NotContains(t, err.Error(), "refused-value-123456")
		})
	}
}

func TestLoadRunnerSecrets_InvalidName(t *testing.T) {
	t.Setenv(runnerSecretsEnv, `{"1BAD":"value-123456","has-dash":"value-123456"}`)

	_, err := loadRunnerSecrets()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"1BAD": not a valid environment variable name`)
	assert.Contains(t, err.Error(), `"has-dash": not a valid environment variable name`)
}

func TestRunnerSecretNameRefused_AllowsOrdinaryNames(t *testing.T) {
	for _, name := range []string{"JIRA_API_TOKEN", "CODERABBIT_API_KEY", "JIRA_TOKEN", "MY_SECRET"} {
		assert.False(t, runnerSecretNameRefused(name), name)
	}
}

func TestValidateRunnerSecretRefs(t *testing.T) {
	secrets := map[string]string{"X": "value-123456"}

	t.Run("no secrets", func(t *testing.T) {
		h := &harness.Harness{Env: &harness.EnvConfig{Sandbox: map[string]string{"A": "${X}"}}}
		require.NoError(t, validateRunnerSecretRefs(h, nil))
	})

	t.Run("env.runner allowed", func(t *testing.T) {
		h := &harness.Harness{Env: &harness.EnvConfig{Runner: map[string]string{
			"X":      "${X}",
			"BEARER": "Bearer $X",
		}}}
		require.NoError(t, validateRunnerSecretRefs(h, secrets))
	})

	refused := map[string]*harness.Harness{
		"env.sandbox[A]":                  {Env: &harness.EnvConfig{Sandbox: map[string]string{"A": "${X}"}}},
		"runner_env[A]":                   {RunnerEnv: map[string]string{"A": "prefix-$X"}},
		"host_files[0].src":               {HostFiles: []harness.HostFile{{Src: "/tmp/${X}", Dest: "/d"}}},
		"validation_loop.schema":          {ValidationLoop: &harness.ValidationLoop{Script: "s", Schema: "${X}"}},
		"validation_loop.preflight_check": {ValidationLoop: &harness.ValidationLoop{Script: "s", PreflightCheck: "echo ${X}"}},
	}
	for source, h := range refused {
		t.Run(source, func(t *testing.T) {
			err := validateRunnerSecretRefs(h, secrets)
			require.Error(t, err)
			assert.Contains(t, err.Error(), source+" references runner secret X")
		})
	}

	t.Run("refused env.runner key", func(t *testing.T) {
		h := &harness.Harness{Env: &harness.EnvConfig{Runner: map[string]string{"PUSH_TOKEN": "${X}"}}}
		err := validateRunnerSecretRefs(h, secrets)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "env.runner[PUSH_TOKEN] receives runner secret X")
	})

	t.Run("refused env.runner key without secret is unchanged", func(t *testing.T) {
		h := &harness.Harness{Env: &harness.EnvConfig{Runner: map[string]string{"GH_TOKEN": "${GH_TOKEN}"}}}
		require.NoError(t, validateRunnerSecretRefs(h, secrets))
	})
}

func TestValidateRunnerSecretHostFiles(t *testing.T) {
	secrets := map[string]string{"X": "value-123456"}
	dir := t.TempDir()
	withRef := filepath.Join(dir, "with-ref.env")
	require.NoError(t, os.WriteFile(withRef, []byte(`export X="${X}"`+"\n"), 0o644))
	without := filepath.Join(dir, "without.env")
	require.NoError(t, os.WriteFile(without, []byte(`export Y="${HOME}"`+"\n"), 0o644))

	t.Run("expand true with reference", func(t *testing.T) {
		h := &harness.Harness{HostFiles: []harness.HostFile{{Src: withRef, Dest: "/d", Expand: true}}}
		err := validateRunnerSecretHostFiles(h, secrets)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "host_files[0] (expand: true) content references runner secret X")
	})
	t.Run("expand false is copied verbatim", func(t *testing.T) {
		h := &harness.Harness{HostFiles: []harness.HostFile{{Src: withRef, Dest: "/d"}}}
		require.NoError(t, validateRunnerSecretHostFiles(h, secrets))
	})
	t.Run("expand true without reference", func(t *testing.T) {
		h := &harness.Harness{HostFiles: []harness.HostFile{{Src: without, Dest: "/d", Expand: true}}}
		require.NoError(t, validateRunnerSecretHostFiles(h, secrets))
	})
	t.Run("missing optional file is skipped", func(t *testing.T) {
		h := &harness.Harness{HostFiles: []harness.HostFile{{Src: filepath.Join(dir, "absent"), Dest: "/d", Expand: true, Optional: true}}}
		require.NoError(t, validateRunnerSecretHostFiles(h, secrets))
	})
	t.Run("no secrets", func(t *testing.T) {
		h := &harness.Harness{HostFiles: []harness.HostFile{{Src: withRef, Dest: "/d", Expand: true}}}
		require.NoError(t, validateRunnerSecretHostFiles(h, nil))
	})
}

func TestValidateRunnerSecretProviders(t *testing.T) {
	secrets := map[string]string{"X": "value-123456"}
	defs := []harness.ProviderDef{
		{Name: "ok", Type: "generic", Credentials: map[string]string{"TOKEN": "${GH_TOKEN}"}},
		{Name: "leaky", Type: "generic", Credentials: map[string]string{"TOKEN": "${X}"}, Config: map[string]string{"URL": "https://h/$X"}},
	}

	err := validateRunnerSecretProviders(defs, secrets)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider leaky credentials[TOKEN] references runner secret X")
	assert.Contains(t, err.Error(), "provider leaky config[URL] references runner secret X")
	assert.NotContains(t, err.Error(), "provider ok")

	require.NoError(t, validateRunnerSecretProviders(defs[:1], secrets))
	require.NoError(t, validateRunnerSecretProviders(defs, nil))
}

func TestWithRunnerSecretExpanderAndLookup(t *testing.T) {
	secrets := map[string]string{"X": "from-secret"}
	fallback := func(k string) string { return "fallback-" + k }
	lookupFallback := func(k string) (string, bool) { return "", false }

	expand := withRunnerSecretExpander(secrets, fallback)
	assert.Equal(t, "from-secret", expand("X"))
	assert.Equal(t, "fallback-Y", expand("Y"))

	lookup := withRunnerSecretLookup(secrets, lookupFallback)
	v, ok := lookup("X")
	assert.True(t, ok)
	assert.Equal(t, "from-secret", v)
	_, ok = lookup("Y")
	assert.False(t, ok)

	// No secrets: the fallbacks are returned unchanged.
	assert.Equal(t, "fallback-X", withRunnerSecretExpander(nil, fallback)("X"))
	_, ok = withRunnerSecretLookup(nil, lookupFallback)("X")
	assert.False(t, ok)
}

func TestRunnerSecretNameSetAndSortedNames(t *testing.T) {
	assert.Nil(t, runnerSecretNameSet(nil))
	secrets := map[string]string{"B": "1", "A": "2"}
	assert.Equal(t, map[string]bool{"A": true, "B": true}, runnerSecretNameSet(secrets))
	assert.Equal(t, []string{"A", "B"}, sortedRunnerSecretNames(secrets))
}

func TestChildScriptEnv_StripsRunnerSecretsBundle(t *testing.T) {
	t.Setenv(runnerSecretsEnv, `{"X":"v"}`)
	for _, e := range childScriptEnv(nil, "") {
		assert.False(t, strings.HasPrefix(e, runnerSecretsEnv+"="), "child env must not carry %s", runnerSecretsEnv)
	}
}

// newRunnerSecretHarnessDir builds a fullsend dir whose code harness
// declares harnessExtra and runs preScriptBody before skipping.
func newRunnerSecretHarnessDir(t *testing.T, harnessExtra, preScriptBody string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "code.md"), []byte("You are a coding agent."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("agents:\n  - harness/code.yaml\n"), 0o644))
	body := preScriptBody + `echo "skipped=true" >> "${FULLSEND_PRESCRIPT_OUTPUT}"` + "\n"
	harnessYAML := "agent: agents/code.md\nrole: test\npre_script: " + writePreScript(t, body) + "\n" + harnessExtra
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"), []byte(harnessYAML), 0o644))
	return dir
}

func runRunnerSecretAgent(t *testing.T, dir string) error {
	t.Helper()
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	return runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
}

// The acceptance case from #7689: the pre-script receives X, does not
// receive the undeclared Y, and never sees the bundle itself.
func TestRunAgent_RunnerSecretReachesPreScript(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv(runnerSecretsEnv, `{"X":"x-secret-value-1","Y":"y-secret-value-2"}`)
	marker := filepath.Join(t.TempDir(), "ran")
	dir := newRunnerSecretHarnessDir(t,
		"env:\n  runner:\n    X: \"${X}\"\n    AUTH: \"Bearer ${X}\"\n",
		`[ "${X:-}" = "x-secret-value-1" ] || exit 11`+"\n"+
			`[ "${AUTH:-}" = "Bearer x-secret-value-1" ] || exit 12`+"\n"+
			`[ -z "${Y+set}" ] || exit 13`+"\n"+
			`[ -z "${`+runnerSecretsEnv+`+set}" ] || exit 14`+"\n"+
			"touch "+marker+"\n")

	require.NoError(t, runRunnerSecretAgent(t, dir))
	assert.FileExists(t, marker)
}

// With the bundle unset, a harness that references an unset name still
// fails exactly as before.
func TestRunAgent_RunnerSecretUnsetBehavesAsBefore(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv(runnerSecretsEnv, "")
	require.NoError(t, os.Unsetenv(runnerSecretsEnv))
	t.Setenv("FULLSEND_TEST_UNSET_7689", "")
	require.NoError(t, os.Unsetenv("FULLSEND_TEST_UNSET_7689"))
	dir := newRunnerSecretHarnessDir(t, "env:\n  runner:\n    X: \"${FULLSEND_TEST_UNSET_7689}\"\n", "")

	err := runRunnerSecretAgent(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FULLSEND_TEST_UNSET_7689 is not set")
}

func TestRunAgent_RunnerSecretRefusedFromSandbox(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv(runnerSecretsEnv, `{"X":"x-secret-value-1"}`)
	marker := filepath.Join(t.TempDir(), "ran")
	dir := newRunnerSecretHarnessDir(t, "env:\n  sandbox:\n    X: \"${X}\"\n", "touch "+marker+"\n")

	err := runRunnerSecretAgent(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "env.sandbox[X] references runner secret X")
	assert.NoFileExists(t, marker)
}

func TestRunAgent_RunnerSecretRefusedFromExpandedHostFile(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv(runnerSecretsEnv, `{"X":"x-secret-value-1"}`)
	src := filepath.Join(t.TempDir(), "env.sh")
	require.NoError(t, os.WriteFile(src, []byte(`export X="${X}"`+"\n"), 0o644))
	marker := filepath.Join(t.TempDir(), "ran")
	dir := newRunnerSecretHarnessDir(t,
		"host_files:\n  - src: "+src+"\n    dest: /tmp/env.sh\n    expand: true\n", "touch "+marker+"\n")

	err := runRunnerSecretAgent(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host_files[0] (expand: true) content references runner secret X")
	assert.NoFileExists(t, marker)
}

func TestRunAgent_RunnerSecretRefusedName(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv(runnerSecretsEnv, `{"GH_WORKFLOW_TOKEN":"forged-value-123"}`)
	dir := newRunnerSecretHarnessDir(t, "", "")

	err := runRunnerSecretAgent(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GH_WORKFLOW_TOKEN: name is reserved for the runner")
}

func TestRunAgent_RunnerSecretRefusedInEventOverlay(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv(runnerSecretsEnv, `{"X":"x-secret-value-1"}`)
	dir := newRunnerSecretHarnessDir(t,
		"overlays:\n  - when: 'has(event.kind)'\n    env:\n      runner:\n        X: \"${X}\"\n", "")

	err := runRunnerSecretAgent(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overlays[0] references runner secret(s) X")
}

func TestRunAgent_RunnerSecretAllowedInForgeOverlay(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv(runnerSecretsEnv, `{"X":"x-secret-value-1"}`)
	marker := filepath.Join(t.TempDir(), "ran")
	dir := newRunnerSecretHarnessDir(t,
		"overlays:\n  - when: 'runtime.forge == \"\" || runtime.forge != \"\"'\n    env:\n      runner:\n        X: \"${X}\"\n",
		`[ "${X:-}" = "x-secret-value-1" ] || exit 11`+"\n"+"touch "+marker+"\n")

	require.NoError(t, runRunnerSecretAgent(t, dir))
	assert.FileExists(t, marker)
}
