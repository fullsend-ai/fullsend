package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestPreflightCredentialRefused(t *testing.T) {
	refused := []string{
		"GH_TOKEN",
		"GITHUB_TOKEN",
		"REVIEW_TOKEN",
		"GITLAB_TOKEN",
		"PUSH_TOKEN",
		"FULLSEND_FORGE_TOKEN",
		"FULLSEND_FETCH_TOKEN",
		"FULLSEND_GITLAB_POLLER_TOKEN",
		"FULLSEND_INFERENCE_GATEWAY_API_KEY",
		"OPENAI_API_KEY",
		"GH_WORKFLOW_TOKEN",
		"FULLSEND_TRIGGER_TOKEN",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN",
	}
	for _, key := range refused {
		assert.True(t, preflightCredentialRefused(key), "expected %s to be refused", key)
	}
	allowed := []string{"SENTINEL", "PATH", "HOME", "TMPDIR", "MY_TOOL_HOME", "JSONSCHEMA_PATH"}
	for _, key := range allowed {
		assert.False(t, preflightCredentialRefused(key), "expected %s to be allowed", key)
	}
}

func TestPreflightEnv_Allowlist(t *testing.T) {
	t.Setenv("PATH", "/host/path")
	t.Setenv("HOME", "/host/home")
	t.Setenv("TMPDIR", "/host/tmp")
	t.Setenv("GH_TOKEN", "ghs_host_secret_value")

	env := preflightEnv(map[string]string{"SENTINEL": "declared"}, "/fs/dir")

	assert.Equal(t, "/host/path", env["PATH"])
	assert.Equal(t, "/host/home", env["HOME"])
	assert.Equal(t, "/host/tmp", env["TMPDIR"])
	assert.Equal(t, "/fs/dir", env["FULLSEND_DIR"])
	assert.Equal(t, "1", env["PYTHONDONTWRITEBYTECODE"])
	assert.Equal(t, "declared", env["SENTINEL"])
	_, ok := env["GH_TOKEN"]
	assert.False(t, ok, "a host-set credential token must not reach the preflight environment")
}

// unsetEnvVar removes name from the process environment for the duration of
// the test and restores the prior value afterwards.
func unsetEnvVar(t *testing.T, name string) {
	t.Helper()
	old, had := os.LookupEnv(name)
	require.NoError(t, os.Unsetenv(name))
	t.Cleanup(func() {
		if had {
			require.NoError(t, os.Setenv(name, old))
			return
		}
		require.NoError(t, os.Unsetenv(name))
	})
}

func TestPreflightEnv_AllowlistedNameUnsetIsAbsent(t *testing.T) {
	unsetEnvVar(t, "TMPDIR")
	env := preflightEnv(nil, "/fs/dir")
	_, ok := env["TMPDIR"]
	assert.False(t, ok, "an unset allowlisted name must be absent, not set to empty")
}

func TestPreflightEnv_CollisionPrecedence(t *testing.T) {
	t.Setenv("PATH", "/host/path")
	env := preflightEnv(map[string]string{
		"PATH":                    "/declared/path",
		"FULLSEND_DIR":            "/declared/dir",
		"PYTHONDONTWRITEBYTECODE": "0",
	}, "/fs/dir")
	assert.Equal(t, "/declared/path", env["PATH"], "a declared PATH overrides the host allowlist value (design D2)")
	assert.Equal(t, "/fs/dir", env["FULLSEND_DIR"], "FULLSEND_DIR is runner-forced; a declaration is dropped")
	assert.Equal(t, "1", env["PYTHONDONTWRITEBYTECODE"], "PYTHONDONTWRITEBYTECODE is runner-forced; a declaration is dropped")
}

func TestPreflightEnv_DeclaredCredentialsRefused(t *testing.T) {
	env := preflightEnv(map[string]string{
		"GH_TOKEN":       "declared-secret",
		"REVIEW_TOKEN":   "declared-review-secret",
		"OPENAI_API_KEY": "sk-declared",
		"SENTINEL":       "declared",
	}, "/fs/dir")
	for _, key := range []string{"GH_TOKEN", "REVIEW_TOKEN", "OPENAI_API_KEY"} {
		_, ok := env[key]
		assert.False(t, ok, "%s must be refused even when declared under its own name", key)
	}
	assert.Equal(t, "declared", env["SENTINEL"], "non-credential declarations reach the preflight environment")
}

func TestBuildPreflightEnv_SortedAndMinimal(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghs_host_secret_value")
	t.Setenv("HOME", "/host/home")

	got := buildPreflightEnv(map[string]string{"SENTINEL": "x"}, "/fs/dir")
	require.NotEmpty(t, got)
	assert.True(t, slices.IsSorted(got), "entries must be sorted KEY=VALUE")
	joined := strings.Join(got, "\n")
	assert.Contains(t, joined, "FULLSEND_DIR=/fs/dir")
	assert.Contains(t, joined, "PYTHONDONTWRITEBYTECODE=1")
	assert.Contains(t, joined, "SENTINEL=x")
	assert.NotContains(t, joined, "GH_TOKEN=")
	assert.NotContains(t, joined, "ghs_host_secret_value")
	for _, e := range got {
		key := strings.SplitN(e, "=", 2)[0]
		switch key {
		case "PATH", "HOME", "TMPDIR", "FULLSEND_DIR", "PYTHONDONTWRITEBYTECODE", "SENTINEL":
		default:
			t.Fatalf("unexpected preflight env key %q: the environment must never start from os.Environ", key)
		}
	}
}

func TestExpandPreflightCommand_BraceFreePassthrough(t *testing.T) {
	got, err := expandPreflightCommand("printenv X; exit 1", map[string]string{})
	require.NoError(t, err)
	assert.Equal(t, "printenv X; exit 1", got)
}

func TestExpandPreflightCommand_AllowlistedAndDeclaredRefsExpand(t *testing.T) {
	t.Setenv("PATH", "/host/path")
	env := preflightEnv(map[string]string{"SENTINEL": "/tmp/sentinel-42"}, "/fs/dir")
	got, err := expandPreflightCommand("test -d ${FULLSEND_DIR} && test -n \"${PATH}\" && test -e ${SENTINEL}", env)
	require.NoError(t, err)
	assert.Contains(t, got, "/fs/dir")
	assert.Contains(t, got, "/host/path")
	assert.Contains(t, got, "/tmp/sentinel-42")
	assert.NotContains(t, got, "${")
}

func TestExpandPreflightCommand_RejectsHostSetCredential(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghs_host_secret_value")
	env := preflightEnv(nil, "/fs/dir")
	_, err := expandPreflightCommand("echo ${GH_TOKEN}", env)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "${GH_TOKEN}")
	assert.Contains(t, err.Error(), "env.runner")
	assert.NotContains(t, err.Error(), "ghs_host_secret_value", "the rejection must name the variable, never a value")
}

func TestExpandPreflightCommand_RejectsDeclaredCredential(t *testing.T) {
	env := preflightEnv(map[string]string{"GH_TOKEN": "declared-secret-9"}, "/fs/dir")
	_, err := expandPreflightCommand("echo ${GH_TOKEN}", env)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "${GH_TOKEN}")
	assert.NotContains(t, err.Error(), "declared-secret-9")
}

func TestExpandPreflightCommand_RejectionOmitsRawCommand(t *testing.T) {
	env := preflightEnv(map[string]string{"SENTINEL": "/tmp/sentinel-42"}, "/fs/dir")
	raw := "touch ${SENTINEL} && echo ${GH_TOKEN}"
	_, err := expandPreflightCommand(raw, env)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "${GH_TOKEN}")
	assert.NotContains(t, err.Error(), raw, "the rejection must not echo the raw command, which can carry literal secrets")
	assert.NotContains(t, err.Error(), "/tmp/sentinel-42")
}

func TestExpandPreflightCommand_RejectionOmitsLiteralSecret(t *testing.T) {
	env := preflightEnv(map[string]string{"SENTINEL_SECRET": "literal-secret-beside-ref"}, "/fs/dir")
	_, err := expandPreflightCommand("echo literal-secret-beside-ref && echo ${GH_TOKEN}", env)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "${GH_TOKEN}")
	assert.NotContains(t, err.Error(), "literal-secret-beside-ref", "a literal secret beside a rejected reference must not reach the rejection diagnostic")
}

func TestExpandPreflightCommand_RejectsDeclaredRoleToken(t *testing.T) {
	env := preflightEnv(map[string]string{"REVIEW_TOKEN": "declared-review-secret"}, "/fs/dir")
	_, err := expandPreflightCommand("echo ${REVIEW_TOKEN}", env)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "${REVIEW_TOKEN}")
	assert.NotContains(t, err.Error(), "declared-review-secret")
}

func TestExpandPreflightCommand_NonPlainBraceFormSilentlyEmpties(t *testing.T) {
	env := preflightEnv(map[string]string{"SENTINEL": "/tmp/sentinel-42"}, "/fs/dir")
	// ${SENTINEL:-fallback} is not a plain ${NAME} reference: the rejection
	// scan does not match it, and os.Expand treats the whole brace content as
	// an unset name, so it expands to empty rather than falling back.
	got, err := expandPreflightCommand("echo ${SENTINEL:-fallback} ${SENTINEL}", env)
	require.NoError(t, err)
	assert.NotContains(t, got, "fallback", "a non-plain brace form expands empty, not to its default")
	assert.NotContains(t, got, "${")
	assert.Contains(t, got, "/tmp/sentinel-42")
}

func TestExpandPreflightCommand_RejectsAllowlistedButUnset(t *testing.T) {
	unsetEnvVar(t, "TMPDIR")
	env := preflightEnv(nil, "/fs/dir")
	_, err := expandPreflightCommand("test -d ${TMPDIR}", env)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "${TMPDIR}")
}

func TestExpandPreflightCommand_BareRefsLeftToShell(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghs_host_secret_value")
	env := preflightEnv(nil, "/fs/dir")

	// A command with no ${...} refs passes through untouched; the child shell
	// expands bare refs itself, where GH_TOKEN is absent and expands empty.
	raw := `test -z "$GH_TOKEN"`
	got, err := expandPreflightCommand(raw, env)
	require.NoError(t, err)
	assert.Equal(t, raw, got)
	assert.NotContains(t, got, "ghs_host_secret_value")

	// Once a ${...} ref is present, os.Expand also expands bare refs, which
	// matches the shell's unset-to-empty semantics for the child env.
	got, err = expandPreflightCommand("echo ${FULLSEND_DIR} $GH_TOKEN", env)
	require.NoError(t, err)
	assert.Contains(t, got, "/fs/dir")
	assert.NotContains(t, got, "GH_TOKEN", "a bare ref to a name outside the preflight env expands empty, like an unset shell variable")
	assert.NotContains(t, got, "ghs_host_secret_value")
}

func TestRunPreflightCheck_Passes(t *testing.T) {
	printer := ui.New(io.Discard)
	err := runPreflightCheck(context.Background(), printer, "validation_loop.preflight_check", "true", nil, "/fs/dir")
	require.NoError(t, err)
}

func TestRunPreflightCheck_DeclaredValueReachesChild(t *testing.T) {
	printer := ui.New(io.Discard)
	err := runPreflightCheck(context.Background(), printer, "validation_loop.preflight_check", "test \"$SENTINEL\" = \"reaches-child\"", map[string]string{"SENTINEL": "reaches-child"}, "/fs/dir")
	require.NoError(t, err, "an env.runner-declared, host-unset variable must reach the preflight child")
}

func TestRunPreflightCheck_DeclaredRoleTokenExcludedFromChild(t *testing.T) {
	printer := ui.New(io.Discard)
	err := runPreflightCheck(context.Background(), printer, "validation_loop.preflight_check", "test -z \"$REVIEW_TOKEN\"", map[string]string{"REVIEW_TOKEN": "review-secret-9"}, "/fs/dir")
	require.NoError(t, err, "a declared role token must not reach the preflight child environment")
}

func TestRunPreflightCheck_RejectsBeforeExecutionNoSideEffect(t *testing.T) {
	sentinelPath := filepath.Join(t.TempDir(), "side-effect")
	printer := ui.New(io.Discard)
	raw := "touch ${SENTINEL} && echo ${GH_TOKEN}"
	err := runPreflightCheck(context.Background(), printer, "validation_loop.preflight_check", raw, map[string]string{"SENTINEL": sentinelPath}, "/fs/dir")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validation_loop.preflight_check")
	assert.Contains(t, err.Error(), "${GH_TOKEN}")
	assert.Contains(t, err.Error(), "env.runner")
	assert.NotContains(t, err.Error(), raw, "the rejection must not echo the raw command")
	assert.NotContains(t, err.Error(), sentinelPath)
	_, statErr := os.Stat(sentinelPath)
	assert.True(t, os.IsNotExist(statErr), "no part of a rejected command may execute")
}

func TestRunPreflightCheck_FailureRedactsDeclaredValueInCommand(t *testing.T) {
	printer := ui.New(io.Discard)
	err := runPreflightCheck(context.Background(), printer, "validation_loop.preflight_check", "echo ${SENTINEL_SECRET}; exit 1", map[string]string{"SENTINEL_SECRET": "sentinel-secret-value-77"}, "/fs/dir")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validation_loop.preflight_check failed")
	assert.Contains(t, err.Error(), "Install the missing dependency")
	assert.NotContains(t, err.Error(), "sentinel-secret-value-77", "the command text must be redacted on failure")
	assert.Contains(t, err.Error(), "[REDACTED:SENTINEL_SECRET]")
}

func TestRunPreflightCheck_FailureRedactsOutput(t *testing.T) {
	printer := ui.New(io.Discard)
	err := runPreflightCheck(context.Background(), printer, "validation_loop.preflight_check", "printenv SENTINEL_SECRET; exit 1", map[string]string{"SENTINEL_SECRET": "sentinel-secret-value-77"}, "/fs/dir")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "sentinel-secret-value-77", "captured output must be redacted on failure")
	assert.Contains(t, err.Error(), "[REDACTED:SENTINEL_SECRET]")
}

func TestRunPreflightCheck_TimeoutRedactsCommand(t *testing.T) {
	old := preflightCheckTimeout
	preflightCheckTimeout = 50 * time.Millisecond
	t.Cleanup(func() { preflightCheckTimeout = old })

	printer := ui.New(io.Discard)
	err := runPreflightCheck(context.Background(), printer, "validation_loop.preflight_check", "echo ${SENTINEL_SECRET}; exec sleep 5", map[string]string{"SENTINEL_SECRET": "sentinel-secret-value-77"}, "/fs/dir")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
	assert.NotContains(t, err.Error(), "sentinel-secret-value-77", "the command text must be redacted on timeout")
	assert.Contains(t, err.Error(), "[REDACTED:SENTINEL_SECRET]")
}

// TestRunPreflightCheck_TimeoutBoundedWithNonExecChild covers #8861: a
// non-exec shell child (sleep is forked, not exec'd, because a command
// follows it) inherits the output pipe and keeps it open after the shell is
// killed on deadline. Without cmd.WaitDelay, CombinedOutput would block until
// the grandchild exits (~300s) instead of returning near the timeout.
func TestRunPreflightCheck_TimeoutBoundedWithNonExecChild(t *testing.T) {
	oldTimeout := preflightCheckTimeout
	oldWaitDelay := preflightWaitDelay
	preflightCheckTimeout = 50 * time.Millisecond
	preflightWaitDelay = 100 * time.Millisecond
	t.Cleanup(func() {
		preflightCheckTimeout = oldTimeout
		preflightWaitDelay = oldWaitDelay
	})

	printer := ui.New(io.Discard)
	start := time.Now()
	err := runPreflightCheck(context.Background(), printer, "preflight_check", "sleep 300; true", nil, "/fs/dir")
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preflight_check timed out")
	assert.Less(t, elapsed, 10*time.Second, "teardown must be bounded by timeout + WaitDelay, not by the grandchild's lifetime")
}

// TestRunPreflightCheck_TopLevelRejectsOutOfAllowlistBeforeExecution
// characterizes the pre-execution reference gate for the top-level call site
// (FR-005, spec US2-1): runAgent invokes this shared executor with source
// label "preflight_check" (research.md D2), so a ${VAR} brace reference to a
// name outside the preflight environment — here a host-set credential, which
// the allowlist excludes even when set — fails the run before any shell
// executes. The error names the variable, never a value, and points to the
// env.runner declaration channel.
func TestRunPreflightCheck_TopLevelRejectsOutOfAllowlistBeforeExecution(t *testing.T) {
	t.Setenv("GH_TOKEN", "gh-host-secret-value-1")
	sentinelPath := filepath.Join(t.TempDir(), "side-effect")
	printer := ui.New(io.Discard)
	raw := "touch ${SENTINEL} && echo ${GH_TOKEN}"
	err := runPreflightCheck(context.Background(), printer, "preflight_check", raw, map[string]string{"SENTINEL": sentinelPath}, "/fs/dir")
	require.Error(t, err)
	// Field-qualified rejection (data-model.md error identity): the source
	// label names the top-level field, never the nested one, and the error
	// names the variable.
	assert.Contains(t, err.Error(), "preflight_check references ${GH_TOKEN}")
	assert.NotContains(t, err.Error(), "validation_loop.preflight_check")
	// The rejection points to the runner-env declaration channel.
	assert.Contains(t, err.Error(), "env.runner")
	// Never a value: the host secret, the declared sentinel value, and the
	// raw command (which can carry literal secrets) are all absent.
	assert.NotContains(t, err.Error(), "gh-host-secret-value-1")
	assert.NotContains(t, err.Error(), raw, "the rejection must not echo the raw command")
	assert.NotContains(t, err.Error(), sentinelPath)
	// The run fails before any shell executes: no part of the command ran.
	_, statErr := os.Stat(sentinelPath)
	assert.True(t, os.IsNotExist(statErr), "no part of a rejected command may execute")
}

// TestRunPreflightCheck_TopLevelBareRefsLeftToShell characterizes the
// bare-$VAR edge case for the top-level call site (FR-005, spec US2-1 edge
// case): bare references are deliberately outside the pre-execution rejection
// scan, so the command is not validated before execution — the child shell
// expands them, where unset means empty. A host-set credential never reaches
// the child environment, so probes like `test -z "$GH_TOKEN"` keep working,
// and the declared sentinel proves the command actually ran.
func TestRunPreflightCheck_TopLevelBareRefsLeftToShell(t *testing.T) {
	t.Setenv("GH_TOKEN", "gh-host-secret-value-1")
	sentinelPath := filepath.Join(t.TempDir(), "bare-ref-executed")
	printer := ui.New(io.Discard)
	raw := `test -z "$GH_TOKEN" && touch "$SENTINEL"`
	err := runPreflightCheck(context.Background(), printer, "preflight_check", raw, map[string]string{"SENTINEL": sentinelPath}, "/fs/dir")
	require.NoError(t, err, "a bare $VAR reference must not be validated pre-execution")
	_, statErr := os.Stat(sentinelPath)
	assert.NoError(t, statErr, "the command must have run in the child shell rather than being rejected before it")
}

// TestRunPreflightCheck_TopLevelNonPlainBraceFormNotValidated characterizes
// the non-plain brace edge case for the top-level call site (FR-005, spec
// US2-1 edge case): ${VAR:-default} is not a plain ${NAME} reference, so the
// pre-execution rejection scan does not match it even though VAR is outside
// the preflight environment. The command runs, and os.Expand treats the whole
// brace content as an unset name — the form expands to empty, never to its
// default — so test -z holds and the sentinel is created.
func TestRunPreflightCheck_TopLevelNonPlainBraceFormNotValidated(t *testing.T) {
	sentinelPath := filepath.Join(t.TempDir(), "non-plain-executed")
	printer := ui.New(io.Discard)
	raw := `touch ${SENTINEL} && test -z "${MYSTERY_VAR:-fallback}"`
	err := runPreflightCheck(context.Background(), printer, "preflight_check", raw, map[string]string{"SENTINEL": sentinelPath}, "/fs/dir")
	require.NoError(t, err, "a non-plain brace form must not be validated pre-execution")
	_, statErr := os.Stat(sentinelPath)
	assert.NoError(t, statErr, "the command must have run in the child shell rather than being rejected before it")
}

// TestRunPreflightCheck_TopLevelChildEnvExclusion characterizes the
// credential-safe child-environment contract for the top-level call site
// (FR-004, spec US2-3): runAgent invokes the shared executor with source
// label "preflight_check" (research.md D2), so the check's child process
// sees exactly the host PATH, HOME, and TMPDIR (only when set), the
// runner-forced FULLSEND_DIR and PYTHONDONTWRITEBYTECODE, and non-credential
// variables declared via env.runner (or the deprecated runner_env — both
// channels reach this call site as the same post-ADR-0055 merged runner
// env). Credential names — forge, mint, and provider tokens, runner
// secrets, and OIDC/trigger names — never reach the child even when
// declared, and nothing else from the host process is inherited: the child
// environment never starts from os.Environ (#8721).
func TestRunPreflightCheck_TopLevelChildEnvExclusion(t *testing.T) {
	// The distinctive host TMPDIR value must be a real directory: every
	// t.TempDir call (this one and the subtests' below) creates its
	// directory under the current TMPDIR, so capture the parent's real temp
	// dir before any subtest overrides TMPDIR.
	hostTmp := t.TempDir()

	// One declaration per refusal category, each under its own name, so the
	// exclusion is pinned name by name: the minted-token inventory (forge
	// token, agent and role tokens, GitLab and fetch tokens), the GitLab
	// role-routing prefix, the provider tokens, the runner-secrets bundle
	// names, and the OIDC/trigger names. Values are unique dummies so a
	// leak under any name is detectable.
	declaredCredentials := map[string]string{
		"FULLSEND_FORGE_TOKEN":                  "declared-forge-secret",
		"GH_TOKEN":                              "declared-gh-secret",
		"GITHUB_TOKEN":                          "declared-github-secret",
		"PUSH_TOKEN":                            "declared-push-secret",
		"REVIEW_TOKEN":                          "declared-review-secret",
		"GITLAB_TOKEN":                          "declared-gitlab-secret",
		"FULLSEND_FETCH_TOKEN":                  "declared-fetch-secret",
		"FULLSEND_GITLAB_POLLER_TOKEN":          "declared-poller-secret",
		"OPENAI_API_KEY":                        "sk-declared-openai-key",
		"FULLSEND_INFERENCE_GATEWAY_API_KEY":    "declared-gateway-secret",
		"GH_WORKFLOW_TOKEN":                     "declared-workflow-secret",
		"FULLSEND_GITHUB_RUNNER_SECRETS":        "declared-runner-secrets-bundle",
		"FULLSEND_GITHUB_RUNNER_SECRETS_FILE":   "/declared/runner-secrets.json",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN":        "declared-oidc-request-token",
		"ACTIONS_ID_TOKEN_REQUEST_URL":          "https://declared.example/oidc",
		"FULLSEND_TRIGGER_TOKEN":                "declared-trigger-secret",
		"FULLSEND_GITLAB_TRIGGER_ACCOUNT_TOKEN": "declared-trigger-account-secret",
	}
	runnerEnv := map[string]string{
		// A non-credential declaration: the legitimate channel that must
		// reach the child.
		"MY_TOOL_HOME": "/declared/tool/home",
		// Colliding declarations of the runner-forced keys: both are
		// dropped in favor of the forced values.
		"FULLSEND_DIR":            "/declared/dir",
		"PYTHONDONTWRITEBYTECODE": "0",
	}
	for key, value := range declaredCredentials {
		runnerEnv[key] = value
	}

	t.Run("allowlisted, forced, and declared names only", func(t *testing.T) {
		t.Setenv("HOME", "/host/home")
		t.Setenv("TMPDIR", hostTmp)
		// A host-set credential and unrelated host noise: neither may reach
		// the child, because the child environment never starts from
		// os.Environ.
		t.Setenv("GH_TOKEN", "gh-host-secret-value-1")
		t.Setenv("HOST_ONLY_NOISE", "host-noise-value-1")
		// PATH is left at its real host value: exec.LookPath resolves the
		// child's "sh" against the test process PATH, so it cannot be
		// faked — the child seeing the host PATH verbatim is the contract.
		pathEnv := os.Getenv("PATH")

		childEnv, raw, sentinelPath := captureTopLevelPreflightChildEnv(t, runnerEnv)

		// Exactly the contracted set. This one assertion also proves the
		// exclusions: any host name beyond the allowlist (GH_TOKEN,
		// HOST_ONLY_NOISE, ...) or any declared credential name would
		// surface as an extra key.
		assert.Equal(t, map[string]string{
			"PATH":                    pathEnv,
			"HOME":                    "/host/home",
			"TMPDIR":                  hostTmp,
			"FULLSEND_DIR":            "/fs/dir",
			"PYTHONDONTWRITEBYTECODE": "1",
			"SENTINEL":                sentinelPath,
			"MY_TOOL_HOME":            "/declared/tool/home",
		}, childEnv, "the child environment must be exactly the allowlisted host names, the runner-forced names, and non-credential env.runner declarations")
		// Explicit pins for the runner-forced keys: the colliding
		// declarations above must be dropped, not honored.
		assert.Equal(t, "/fs/dir", childEnv["FULLSEND_DIR"], "FULLSEND_DIR is runner-forced; a declaration is dropped")
		assert.Equal(t, "1", childEnv["PYTHONDONTWRITEBYTECODE"], "PYTHONDONTWRITEBYTECODE is runner-forced; a declaration is dropped")
		// Name-by-name exclusion, even when declared under the credential's
		// own name (the child-process counterpart of
		// TestPreflightEnv_DeclaredCredentialsRefused).
		for key := range declaredCredentials {
			_, ok := childEnv[key]
			assert.False(t, ok, "%s must not reach the preflight child even when declared under its own name", key)
		}
		// Exclusion by name and the exact set still leave one channel
		// unchecked: a value leaking under a different name. No credential
		// value may appear anywhere in the dump.
		for _, value := range declaredCredentials {
			assert.NotContains(t, raw, value, "no declared credential value may reach the child environment, under any name")
		}
		assert.NotContains(t, raw, "gh-host-secret-value-1", "a host-set credential value must not reach the child environment")
	})

	t.Run("unset allowlisted name is absent, not empty", func(t *testing.T) {
		unsetEnvVar(t, "TMPDIR")
		childEnv, _, sentinelPath := captureTopLevelPreflightChildEnv(t, map[string]string{
			"GH_TOKEN": "declared-gh-secret-unset-case",
		})
		_, ok := childEnv["TMPDIR"]
		assert.False(t, ok, "an unset allowlisted name must be absent from the child environment, not set to empty")
		// The rest of the contract holds with TMPDIR unset: forced names,
		// non-credential declarations, and credential exclusion.
		assert.Equal(t, "/fs/dir", childEnv["FULLSEND_DIR"])
		assert.Equal(t, "1", childEnv["PYTHONDONTWRITEBYTECODE"])
		assert.Equal(t, sentinelPath, childEnv["SENTINEL"], "non-credential declarations still reach the child")
		_, ok = childEnv["GH_TOKEN"]
		assert.False(t, ok, "declared credential names stay excluded from the child environment")
	})
}

// captureTopLevelPreflightChildEnv runs one top-level preflight check whose
// command dumps the child process's environment to a sentinel file, and
// returns the variables the child saw, the raw dump, and the sentinel path.
// SENTINEL is injected into the declarations as the capture target — a
// non-credential name, so it reaches the child and the successful redirect
// proves the dump was written by the child shell. Shell bookkeeping
// variables that POSIX sh implementations export on their own (PWD, SHLVL,
// _, OLDPWD) are removed from the parsed result: they are added by the
// shell rather than passed by fullsend and vary between implementations
// (/bin/sh is bash on macOS, dash on Linux CI).
func captureTopLevelPreflightChildEnv(t *testing.T, runnerEnv map[string]string) (childEnv map[string]string, raw string, sentinelPath string) {
	t.Helper()
	sentinelPath = filepath.Join(t.TempDir(), "child-env")
	env := make(map[string]string, len(runnerEnv)+1)
	for k, v := range runnerEnv {
		env[k] = v
	}
	env["SENTINEL"] = sentinelPath
	printer := ui.New(io.Discard)
	err := runPreflightCheck(context.Background(), printer, "preflight_check", `printenv > "$SENTINEL"`, env, "/fs/dir")
	require.NoError(t, err, "the environment dump command must run in the child shell and exit zero")
	dumped, readErr := os.ReadFile(sentinelPath)
	require.NoError(t, readErr, "the child must have written its environment to the sentinel file")
	raw = string(dumped)
	childEnv = map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("unexpected non KEY=VALUE line in child environment dump: %q", line)
		}
		childEnv[key] = value
	}
	for _, shellVar := range []string{"PWD", "SHLVL", "_", "OLDPWD"} {
		delete(childEnv, shellVar)
	}
	return childEnv, raw, sentinelPath
}

// TestRunPreflightCheck_TopLevelDiagnosticRedaction characterizes the
// diagnostic-redaction contract for the top-level call site (FR-004, spec
// US2-2, SC-003): runAgent invokes the shared executor with source label
// "preflight_check" (research.md D2), so a failing or timing-out top-level
// check whose declared runner-env values include a secret reports only
// redacted command text and output — the secret value never appears in the
// failure or timeout diagnostic. SENTINEL_SECRET is a non-credential name,
// so the declaration reaches the child environment (name exclusion is
// TestRunPreflightCheck_TopLevelChildEnvExclusion's contract, not this
// one): that is what splices the value into the expanded command text and
// the captured output, the two channels the diagnostics report. The source
// label field-qualifies both shapes (data-model.md Diagnostics):
// `preflight_check failed: <redacted cmd>` and `preflight_check timed out
// after <bound>: <redacted cmd>`.
func TestRunPreflightCheck_TopLevelDiagnosticRedaction(t *testing.T) {
	const secretValue = "sentinel-secret-value-77"
	runnerEnv := map[string]string{"SENTINEL_SECRET": secretValue}

	t.Run("failure redacts declared value in command text and output", func(t *testing.T) {
		printer := ui.New(io.Discard)
		// The expansion splices the secret into the command text, and echo
		// repeats it in the captured output: both channels must come back
		// masked, never the value.
		err := runPreflightCheck(context.Background(), printer, "preflight_check", "echo ${SENTINEL_SECRET}; exit 1", runnerEnv, "/fs/dir")
		require.Error(t, err)
		// Field-qualified failure (data-model.md error identity): the source
		// label names the top-level field, never the nested one.
		assert.Contains(t, err.Error(), "preflight_check failed")
		assert.NotContains(t, err.Error(), "validation_loop.preflight_check")
		assert.Contains(t, err.Error(), "Install the missing dependency")
		assert.Contains(t, err.Error(), "[REDACTED:SENTINEL_SECRET]")
		assert.NotContains(t, err.Error(), secretValue, "the command text and captured output must be redacted on failure")
	})

	t.Run("failure redacts captured output", func(t *testing.T) {
		printer := ui.New(io.Discard)
		// The command text carries only the name; the child's printenv puts
		// the declared value into the captured output.
		err := runPreflightCheck(context.Background(), printer, "preflight_check", "printenv SENTINEL_SECRET; exit 1", runnerEnv, "/fs/dir")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "preflight_check failed")
		assert.NotContains(t, err.Error(), "validation_loop.preflight_check")
		assert.Contains(t, err.Error(), "[REDACTED:SENTINEL_SECRET]")
		assert.NotContains(t, err.Error(), secretValue, "captured output must be redacted on failure")
	})

	t.Run("timeout redacts command text", func(t *testing.T) {
		old := preflightCheckTimeout
		preflightCheckTimeout = 50 * time.Millisecond
		t.Cleanup(func() { preflightCheckTimeout = old })

		printer := ui.New(io.Discard)
		// The echo runs before the deadline, but the timeout diagnostic
		// carries only the command text — which the expansion filled with
		// the secret — so that text must come back masked.
		err := runPreflightCheck(context.Background(), printer, "preflight_check", "echo ${SENTINEL_SECRET}; exec sleep 5", runnerEnv, "/fs/dir")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "preflight_check timed out")
		assert.NotContains(t, err.Error(), "validation_loop.preflight_check")
		assert.Contains(t, err.Error(), "[REDACTED:SENTINEL_SECRET]")
		assert.NotContains(t, err.Error(), secretValue, "the command text must be redacted on timeout")
	})
}
