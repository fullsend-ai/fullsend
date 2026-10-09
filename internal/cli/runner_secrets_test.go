package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/prescript"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"gopkg.in/yaml.v3"
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

// writeRunnerSecretsFile writes body to a mode 0600 bundle file and points
// FULLSEND_RUNNER_SECRETS_FILE at it, as the composite action does.
func writeRunnerSecretsFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runner-secrets.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	t.Setenv(runnerSecretsFileEnv, path)
	t.Setenv(runnerSecretsEnv, "")
	require.NoError(t, os.Unsetenv(runnerSecretsEnv))
	return path
}

func TestLoadRunnerSecrets_FromFileDeletesFileAndUnsets(t *testing.T) {
	path := writeRunnerSecretsFile(t, `{"JIRA_API_TOKEN":"jira-token-value"}`)

	secrets, err := loadRunnerSecrets()
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"JIRA_API_TOKEN": "jira-token-value"}, secrets)
	assert.NoFileExists(t, path, "the bundle file must be deleted right after it is read")
	_, present := os.LookupEnv(runnerSecretsFileEnv)
	assert.False(t, present, "%s must not reach any child process", runnerSecretsFileEnv)
}

func TestLoadRunnerSecrets_MalformedFileIsStillDeleted(t *testing.T) {
	path := writeRunnerSecretsFile(t, `not-json-secret-value`)

	secrets, err := loadRunnerSecrets()
	require.Error(t, err)
	assert.Nil(t, secrets)
	assert.Contains(t, err.Error(), runnerSecretsFileEnv+" must hold a JSON object")
	assert.NotContains(t, err.Error(), "not-json-secret-value")
	assert.NoFileExists(t, path)
	_, present := os.LookupEnv(runnerSecretsFileEnv)
	assert.False(t, present)
}

func TestLoadRunnerSecrets_MissingFileFails(t *testing.T) {
	writeRunnerSecretsFile(t, `{}`)
	t.Setenv(runnerSecretsFileEnv, filepath.Join(t.TempDir(), "absent.json"))

	_, err := loadRunnerSecrets()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the "+runnerSecretsFileEnv+" file")
	_, present := os.LookupEnv(runnerSecretsFileEnv)
	assert.False(t, present)
}

// The composite action always sets the path; it is empty when the
// FULLSEND_RUNNER_SECRETS secret is unset.
func TestLoadRunnerSecrets_EmptyFilePathIsNoOp(t *testing.T) {
	t.Setenv(runnerSecretsFileEnv, "")
	t.Setenv(runnerSecretsEnv, "")
	require.NoError(t, os.Unsetenv(runnerSecretsEnv))

	secrets, err := loadRunnerSecrets()
	require.NoError(t, err)
	assert.Nil(t, secrets)
	_, present := os.LookupEnv(runnerSecretsFileEnv)
	assert.False(t, present)
}

func TestLoadRunnerSecrets_FileAndInlineConflict(t *testing.T) {
	path := writeRunnerSecretsFile(t, `{"X":"x-secret-value-1"}`)
	t.Setenv(runnerSecretsEnv, `{"X":"x-secret-value-2"}`)

	_, err := loadRunnerSecrets()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not both")
	assert.NoFileExists(t, path)
	for _, name := range []string{runnerSecretsFileEnv, runnerSecretsEnv} {
		_, present := os.LookupEnv(name)
		assert.False(t, present, name)
	}
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
	assert.Contains(t, stderr, "::add-mask::first-line-value%0Asecond-line-value\n",
		"the whole value is masked, escaped for the workflow command")
	assert.Contains(t, stderr, "::add-mask::first-line-value\n")
	assert.Contains(t, stderr, "::add-mask::second-line-value\n")
}

// The bundle as a whole is never sent to add-mask: masking the JSON would
// echo it into the command stream, and only the values are secret.
func TestLoadRunnerSecrets_DoesNotMaskWholeBundle(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv(runnerSecretsEnv, `{"ONE_VALUE":"single-line-value"}`)

	stderr := captureStderr(t, func() {
		_, err := loadRunnerSecrets()
		require.NoError(t, err)
	})
	assert.Equal(t, "::add-mask::single-line-value\n", stderr)
}

func TestMaskActionsValue_EscapesCommandData(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	for name, tc := range map[string]struct{ value, want string }{
		"literal percent-25": {"abc%25def-secret", "::add-mask::abc%2525def-secret\n"},
		"multi-line":         {"line-one\r\nline-two", "::add-mask::line-one%0D%0Aline-two\n"},
		"plain":              {"plain-secret", "::add-mask::plain-secret\n"},
	} {
		t.Run(name, func(t *testing.T) {
			stderr := captureStderr(t, func() { maskActionsValue(tc.value) })
			assert.Equal(t, tc.want, stderr)
		})
	}
}

func TestLoadRunnerSecrets_MasksPercentEncodedValue(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv(runnerSecretsEnv, `{"PCT_VALUE":"pass%25word%0Avalue"}`)

	stderr := captureStderr(t, func() {
		_, err := loadRunnerSecrets()
		require.NoError(t, err)
	})
	// The runner unescapes %25 to %, so the value must be sent escaped or
	// a different string would be masked.
	assert.Equal(t, "::add-mask::pass%2525word%250Avalue\n", stderr)
}

func TestLoadRunnerSecrets_RefusesValueTooShortToRedact(t *testing.T) {
	t.Setenv(runnerSecretsEnv, `{"SHORT_VALUE":"abc1234","LONG_VALUE":"long-enough-value"}`)

	secrets, err := loadRunnerSecrets()
	require.Error(t, err)
	assert.Nil(t, secrets)
	assert.Contains(t, err.Error(), "SHORT_VALUE: value is too short to redact from logs")
	assert.NotContains(t, err.Error(), "LONG_VALUE")
	assert.NotContains(t, err.Error(), "abc1234")
}

func TestLoadRunnerSecrets_RegistersLinesAndJSONEscapedForm(t *testing.T) {
	t.Setenv(runnerSecretsEnv, `{"PEM_LIKE":"first-line-7689-a\nsecond-line-7689-b","QUOTED":"say \"hi\" to 7689-c"}`)

	_, err := loadRunnerSecrets()
	require.NoError(t, err)

	// A script that prints one line of a multi-line value is redacted.
	out := redactFeedback("pre-script failed: second-line-7689-b rejected", nil)
	assert.NotContains(t, out, "second-line-7689-b")
	// A script that embeds the value in JSON output is redacted too.
	res := security.NewSecretRedactor().Scan(`{"token":"say \"hi\" to 7689-c"}`)
	assert.NotContains(t, res.Sanitized, `say \"hi\" to 7689-c`)
}

func TestJSONEscapedString(t *testing.T) {
	assert.Equal(t, "plain", jsonEscapedString("plain"))
	assert.Equal(t, `a\"b\\c\nd`, jsonEscapedString("a\"b\\c\nd"))
	assert.Equal(t, "<&>", jsonEscapedString("<&>"), "HTML characters stay literal")
}

func TestLoadRunnerSecrets_Malformed(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":       `not-json-secret-value`,
		"array":          `["a"]`,
		"null":           `null`,
		"truncated":      `{"X":"not-json-secret-value"`,
		"trailing value": `{"X":"not-json-secret-value"} {}`,
		"bad value":      `{"X":not-json-secret-value}`,
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

func TestLoadRunnerSecrets_InvalidValues(t *testing.T) {
	for name, tc := range map[string]struct{ raw, want string }{
		"null value":    {`{"X":null}`, "X: value must be a string, not null"},
		"number value":  {`{"X":42}`, "X: value must be a string"},
		"object value":  {`{"X":{"a":"b"}}`, "X: value must be a string"},
		"duplicate key": {`{"X":"first-secret-value","X":"second-secret-value"}`, "X: duplicate key"},
		// A key that is not a variable name is quoted, so a newline in it
		// cannot start a workflow command line in the log.
		"injected key": {`{"BAD\n::warning::x":null}`, `"BAD\n::warning::x": value must be a string, not null`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(runnerSecretsEnv, tc.raw)

			secrets, err := loadRunnerSecrets()
			require.Error(t, err)
			assert.Nil(t, secrets)
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, err.Error(), "secret-value")
		})
	}
}

// A duplicate key's value is masked like any other, so the entry that is
// refused never prints in clear.
func TestLoadRunnerSecrets_MasksDuplicateValue(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv(runnerSecretsEnv, `{"X":"first-secret-value","X":"second-secret-value"}`)

	stderr := captureStderr(t, func() {
		_, err := loadRunnerSecrets()
		require.Error(t, err)
	})
	assert.Contains(t, stderr, "::add-mask::first-secret-value\n")
	assert.Contains(t, stderr, "::add-mask::second-secret-value\n")
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
		"PUSH_TOKEN_SOURCE",
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

// Every name the reusable workflows and the composite action set on a
// fullsend run step is refused, so a runner secret never shadows a named
// workflow secret (JIRA_TOKEN on harness-run) or a workflow-provided value.
func TestRunnerSecretNameRefused_CoversWorkflowSetNames(t *testing.T) {
	type step struct {
		Name string            `yaml:"name"`
		Env  map[string]string `yaml:"env"`
		With map[string]any    `yaml:"with"`
	}
	stepEnvKeys := func(steps []step, keep func(step) bool) []string {
		var keys []string
		for _, st := range steps {
			if keep(st) {
				for k := range st.Env {
					keys = append(keys, k)
				}
			}
		}
		return keys
	}
	var names []string
	for _, wf := range []string{"reusable-dispatch.yml", "reusable-prioritize.yml"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", wf))
		require.NoError(t, err)
		var doc struct {
			Jobs map[string]struct {
				Steps []step `yaml:"steps"`
			} `yaml:"jobs"`
		}
		require.NoError(t, yaml.Unmarshal(raw, &doc), wf)
		for _, job := range doc.Jobs {
			names = append(names, stepEnvKeys(job.Steps, func(st step) bool { _, ok := st.With["agent"]; return ok })...)
		}
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "action.yml"))
	require.NoError(t, err)
	var action struct {
		Runs struct {
			Steps []step `yaml:"steps"`
		} `yaml:"runs"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &action))
	actionNames := stepEnvKeys(action.Runs.Steps, func(st step) bool { return st.Name == "Run fullsend" })
	require.Contains(t, actionNames, "STATUS_RUN_URL", "action.yml must have a Run fullsend step with its env")
	names = append(names, actionNames...)

	require.Contains(t, names, "JIRA_TOKEN", "harness-run must still set the named JIRA_TOKEN secret")
	for _, name := range names {
		assert.True(t, runnerSecretNameRefused(name), "%s is set on a fullsend run step but not refused as a runner secret name", name)
	}
}

func TestRunnerSecretNameRefused_AllowsOrdinaryNames(t *testing.T) {
	for _, name := range []string{"JIRA_API_TOKEN", "JIRA_API_EMAIL", "CODERABBIT_API_KEY", "MY_SECRET"} {
		assert.False(t, runnerSecretNameRefused(name), name)
	}
	// The named workflow secrets keep their single source.
	for _, name := range []string{"JIRA_TOKEN", "JIRA_USER_EMAIL", "JIRA_BASE_URL", "OTEL_EXPORTER_OTLP_HEADERS",
		"GIT_BOT_EMAIL", "OPENSHELL_VERSION", "TARGET_REPO_DIR", "GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_CORE_PROJECT"} {
		assert.True(t, runnerSecretNameRefused(name), name)
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

	// A destination key with '=' would let the child read a different
	// variable name than the one the reserved-name check saw.
	t.Run("env.runner key with equals", func(t *testing.T) {
		h := &harness.Harness{Env: &harness.EnvConfig{Runner: map[string]string{"GH_TOKEN=x": "${X}"}}}
		err := validateRunnerSecretRefs(h, secrets)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"GH_TOKEN=x" is not a valid environment variable name`)
	})

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

func TestRunnerSecretNameSetAndReferencedNames(t *testing.T) {
	assert.Nil(t, runnerSecretNameSet(nil))
	secrets := map[string]string{"B": "1", "A": "2", "UNUSED": "3"}
	assert.Equal(t, map[string]bool{"A": true, "B": true, "UNUSED": true}, runnerSecretNameSet(secrets))

	h := &harness.Harness{Env: &harness.EnvConfig{Runner: map[string]string{
		"FIRST":  "${B}",
		"SECOND": "Bearer $A and ${B}",
		"OTHER":  "${HOME}",
	}}}
	assert.Equal(t, []string{"A", "B"}, referencedRunnerSecretNames(h, secrets))
	assert.Nil(t, referencedRunnerSecretNames(h, nil))
	assert.Nil(t, referencedRunnerSecretNames(&harness.Harness{}, secrets))
}

func TestRunnerSecretRefusedNames_DerivedFromRoleTokens(t *testing.T) {
	for _, vars := range roleTokenVars {
		for _, tv := range vars {
			assert.True(t, runnerSecretRefusedNames[tv.Name], tv.Name)
		}
	}
	for _, name := range []string{"GH_TOKEN", "GITLAB_TOKEN", "PATH"} {
		assert.True(t, runnerSecretRefusedNames[name], name)
	}
}

func TestChildScriptEnv_StripsRunnerSecretsBundle(t *testing.T) {
	t.Setenv(runnerSecretsEnv, `{"X":"v"}`)
	t.Setenv(runnerSecretsFileEnv, "/tmp/runner-secrets.json")
	for _, e := range childScriptEnv(nil, "") {
		assert.False(t, strings.HasPrefix(e, runnerSecretsEnv+"="), "child env must not carry %s", runnerSecretsEnv)
		assert.False(t, strings.HasPrefix(e, runnerSecretsFileEnv+"="), "child env must not carry %s", runnerSecretsFileEnv)
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
	return runRunnerSecretAgentTo(t, dir, io.Discard)
}

func runRunnerSecretAgentTo(t *testing.T, dir string, out io.Writer) error {
	t.Helper()
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	return runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(out), false, runOverrideFlags{})
}

// The CI path: the bundle arrives as a file, which is gone and whose path
// variable is unset by the time the pre-script runs.
func TestRunAgent_RunnerSecretFileReachesPreScript(t *testing.T) {
	usePreScriptStub(t)
	path := writeRunnerSecretsFile(t, `{"X":"x-secret-value-1","Y":"y-secret-value-2"}`)
	marker := filepath.Join(t.TempDir(), "ran")
	dir := newRunnerSecretHarnessDir(t,
		"env:\n  runner:\n    X: \"${X}\"\n",
		`[ "${X:-}" = "x-secret-value-1" ] || exit 11`+"\n"+
			`[ -z "${Y+set}" ] || exit 13`+"\n"+
			`[ -z "${`+runnerSecretsFileEnv+`+set}" ] || exit 14`+"\n"+
			`[ ! -e "`+path+`" ] || exit 15`+"\n"+
			"touch "+marker+"\n")

	require.NoError(t, runRunnerSecretAgent(t, dir))
	assert.FileExists(t, marker)
}

// The run log names only the bundle keys the harness references.
func TestRunAgent_LogsOnlyReferencedRunnerSecretNames(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv(runnerSecretsEnv, `{"REFERENCED_NAME":"x-secret-value-1","UNREFERENCED_NAME":"y-secret-value-2"}`)
	dir := newRunnerSecretHarnessDir(t, "env:\n  runner:\n    TOKEN: \"${REFERENCED_NAME}\"\n", "")

	var out strings.Builder
	require.NoError(t, runRunnerSecretAgentTo(t, dir, &out))
	assert.Contains(t, out.String(), "Runner secrets")
	assert.Contains(t, out.String(), "REFERENCED_NAME")
	assert.NotContains(t, out.String(), "UNREFERENCED_NAME")
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

// Overlapping literals are redacted longest first, whatever the map order.
func TestRedactFeedback_OverlappingLiteralsLongestFirst(t *testing.T) {
	env := map[string]string{"X_TOKEN": "abcdefgh", "Y_TOKEN": "abcdefghijklmnop"}
	for range 50 {
		assert.Equal(t, "v=[REDACTED:Y_TOKEN] w=[REDACTED:X_TOKEN]", redactFeedback("v=abcdefghijklmnop w=abcdefgh", env))
	}
}

// A shorter env.runner literal must not break a longer registered runtime
// secret that it is a prefix of.
func TestRedactFeedback_RuntimeSecretOverlapsRunnerEnv(t *testing.T) {
	require.True(t, security.RegisterRuntimeSecret("zzopaque-runtime-0123456789"))
	out := redactFeedback("x zzopaque-runtime-0123456789", map[string]string{"A_TOKEN": "zzopaque"})
	assert.Equal(t, "x ***", out)
}

// A pre-script skip reason that echoes a runner secret is redacted before
// it is printed, posted or recorded.
func TestRunAgent_RunnerSecretSkipReasonRedacted(t *testing.T) {
	usePreScriptStub(t)
	writeRunnerSecretsFile(t, `{"X":"x-secret-value-skip"}`)
	dir := newRunnerSecretHarnessDir(t,
		"env:\n  runner:\n    X: \"${X}\"\n",
		`echo "reason=leak ${X}" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n")

	var out bytes.Buffer
	require.NoError(t, runRunnerSecretAgentTo(t, dir, &out))
	assert.Contains(t, out.String(), "Run skipped by pre-script: leak ***")
	assert.Contains(t, out.String(), "Pre-script outputs: reason=leak ***")
	assert.NotContains(t, out.String(), "x-secret-value-skip")
}

// The scrubbed result is what reaches GITHUB_OUTPUT: the reason and any
// output carrying a secret are redacted, and a non-reason output that only
// looks like a token is relayed unchanged.
func TestRedactPreScriptResult_RelaysScrubbedOutputs(t *testing.T) {
	require.True(t, security.RegisterRuntimeSecret("x-secret-value-relay"))
	ghOutput := filepath.Join(t.TempDir(), "github-output")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_OUTPUT", ghOutput)
	// Token-shaped but not a credential; built at runtime so secret
	// scanners do not flag the fixture.
	lookalike := "gh" + "p_" + strings.Repeat("A", 36)
	res := prescript.Result{
		Skipped: true,
		Reason:  "leak x-secret-value-relay " + lookalike,
		Outputs: map[string]string{
			"skipped": "true",
			"reason":  "leak x-secret-value-relay " + lookalike,
			"carried": "x-secret-value-relay",
			"note":    lookalike,
		},
	}
	redactPreScriptResult(&res, map[string]string{"X": "x-secret-value-relay"})
	relayed, err := prescript.Relay(res)
	require.NoError(t, err)
	require.True(t, relayed)

	raw, err := os.ReadFile(ghOutput)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "x-secret-value-relay")
	assert.Contains(t, string(raw), "carried=***")
	assert.NotContains(t, res.Reason, lookalike, "the reason gets the pattern scan too")
	assert.Contains(t, string(raw), "note="+lookalike, "outputs lose exact credential values only")

	// The log line hides the look-alike too, without touching the relayed map.
	line := prescript.LogLine(preScriptLogResult(res, nil))
	assert.NotContains(t, line, lookalike)
	assert.Equal(t, lookalike, res.Outputs["note"])
}

// A failing preflight check that prints a runner secret is redacted in the
// run error, which reaches the trace and the completion comment.
func TestRunAgent_RunnerSecretPreflightFailureRedacted(t *testing.T) {
	usePreScriptStub(t)
	writeRunnerSecretsFile(t, `{"X":"x-secret-value-pre"}`)
	validate := writePreScript(t, "exit 0\n")
	dir := newRunnerSecretHarnessDir(t,
		"env:\n  runner:\n    X: \"${X}\"\nvalidation_loop:\n  script: "+validate+"\n  preflight_check: \"printenv X; exit 1\"\n",
		"")

	err := runRunnerSecretAgent(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preflight_check failed")
	assert.NotContains(t, err.Error(), "x-secret-value-pre")
}

// A pre-script output line that fails to parse is quoted in the error;
// a runner secret in it must not reach the run error.
func TestRunAgent_RunnerSecretPreScriptParseErrorRedacted(t *testing.T) {
	usePreScriptStub(t)
	writeRunnerSecretsFile(t, `{"X":"x-secret-value-parse"}`)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "code.md"), []byte("You are a coding agent."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("agents:\n  - harness/code.yaml\n"), 0o644))
	script := writePreScript(t, `echo "skipped=${X}" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n")
	harnessYAML := "agent: agents/code.md\nrole: test\npre_script: " + script + "\nenv:\n  runner:\n    X: \"${X}\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"), []byte(harnessYAML), 0o644))

	err := runRunnerSecretAgent(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "skipped")
	assert.NotContains(t, err.Error(), "x-secret-value-parse")
}

// A runner secret that straddles the 1024-byte cap on the failure line is
// redacted before the line is cut, so no prefix of it survives.
func TestRunAgent_RunnerSecretAcrossFailureDetailCap(t *testing.T) {
	usePreScriptStub(t)
	writeRunnerSecretsFile(t, `{"X":"x-secret-value-straddle"}`)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "code.md"), []byte("You are a coding agent."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("agents:\n  - harness/code.yaml\n"), 0o644))
	filler := strings.Repeat("a", 1015)
	script := writePreScript(t, `echo "`+filler+`${X}" >&2`+"\nexit 1\n")
	harnessYAML := "agent: agents/code.md\nrole: test\npre_script: " + script + "\nenv:\n  runner:\n    X: \"${X}\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"), []byte(harnessYAML), 0o644))

	err := runRunnerSecretAgent(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "running pre-script")
	assert.NotContains(t, err.Error(), "x-secret")
}
