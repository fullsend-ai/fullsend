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
