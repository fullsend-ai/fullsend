package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// A padded credential printed at the very start or end of a failing
// validation script's output is redacted in the feedback file and the
// retry prompt, although validationFailMessage trims the output (#8165).
func TestWriteValidationFeedback_RedactsPaddedSecretBeforeTrim(t *testing.T) {
	for name, tc := range map[string]struct{ value, output string }{
		"leading whitespace at the start": {" padded-lead-value-1", " padded-lead-value-1 rejected\n"},
		"trailing whitespace at the end":  {"padded-trail-value-2\n", "token padded-trail-value-2\n"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			env := map[string]string{"PUSH_TOKEN": tc.value}
			feedback := writeValidationFeedback(dir, []byte(tc.output), fmt.Errorf("exit status 1"), env, ui.New(io.Discard))

			assert.NotContains(t, feedback, "padded-")
			assert.Contains(t, feedback, "[REDACTED:PUSH_TOKEN]")
			data, err := os.ReadFile(filepath.Join(dir, validationFeedbackFile))
			require.NoError(t, err)
			assert.NotContains(t, string(data), "padded-")

			prompt, _ := buildFeedbackPrompt(feedback)
			assert.NotContains(t, prompt, "padded-")
			assert.Contains(t, prompt, "[REDACTED:PUSH_TOKEN]")
		})
	}
}

// The post-loop sweep's console line gets the same redact-before-trim
// ordering as the retry feedback.
func TestPostLoopValidationSweep_RedactsPaddedSecretBeforeTrim(t *testing.T) {
	runDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(runDir, "iteration-1"), 0o755))
	script := filepath.Join(t.TempDir(), "validate.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$PUSH_TOKEN\"\nexit 1\n"), 0o755))

	h := &harness.Harness{
		RunnerEnv:      map[string]string{"PUSH_TOKEN": "  sweep-padded-value-3  "},
		ValidationLoop: &harness.ValidationLoop{Script: script},
	}
	var out bytes.Buffer
	result := postLoopValidationSweep(h, runDir, 1, true, ui.New(&out))

	assert.False(t, result.passed)
	assert.NotContains(t, out.String(), "sweep-padded-value-3")
	assert.Contains(t, out.String(), "[REDACTED:PUSH_TOKEN]")
}

// A padded credential that is a failing preflight check's whole output is
// redacted in the run error.
func TestRunPreflightCheck_FailureRedactsPaddedOutput(t *testing.T) {
	printer := ui.New(io.Discard)
	env := map[string]string{"SENTINEL_SECRET": "  sentinel-padded-value-4  "}
	err := runPreflightCheck(context.Background(), printer, "validation_loop.preflight_check", `printf '%s' "$SENTINEL_SECRET"; exit 1`, env, "/fs/dir")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "sentinel-padded-value-4")
	assert.Contains(t, err.Error(), "[REDACTED:SENTINEL_SECRET]")
}

// The exec error a failing script with no output falls back to is still
// redacted.
func TestRedactedValidationFailMessage_RedactsExecErrorFallback(t *testing.T) {
	env := map[string]string{"PUSH_TOKEN": "exec-error-value-5"}
	got := redactedValidationFailMessage(nil, fmt.Errorf("fork /tmp/exec-error-value-5: denied"), env)
	assert.Equal(t, "fork /tmp/[REDACTED:PUSH_TOKEN]: denied", got)
}

// Two secrets that overlap in the text without either containing the
// other are both masked whole, in either order and whatever the map
// order (#8165).
func TestRedactFeedback_PartlyOverlappingLiterals(t *testing.T) {
	// Y_TOKEN is the longer value, so its mask covers the merged range.
	env := map[string]string{"X_TOKEN": "aaaabc12", "Y_TOKEN": "12345zzzzz"}
	reversed := map[string]string{"X_TOKEN": "345abcdef", "Y_TOKEN": "zzzzz12345"}
	for range 50 {
		assert.Equal(t, "v=[REDACTED:Y_TOKEN] end", redactFeedback("v=aaaabc12345zzzzz end", env), "shorter value first")
		assert.Equal(t, "v=[REDACTED:Y_TOKEN] end", redactFeedback("v=zzzzz12345abcdef end", reversed), "longer value first")
	}
	got, keys := replaceEnvSecrets("v=aaaabc12345zzzzz end", env)
	assert.Equal(t, "v=[REDACTED:Y_TOKEN] end", got)
	assert.Equal(t, []string{"Y_TOKEN", "X_TOKEN"}, keys, "both leaked values are reported")
}

// Overlap where the later value is the longer one, and three values
// chained through each other, still leave no fragment.
func TestMaskSecretLiterals_MergesOverlapChains(t *testing.T) {
	literals := []secretLiteral{
		{"ccccc-ddddd-eeee", "[C]"},
		{"aaaa-bbbbb", "[A]"},
		{"bbbbb-cc", "[B]"},
	}
	got, found := maskSecretLiterals("x aaaa-bbbbb-ccccc-ddddd-eeee y", literals)
	assert.Equal(t, "x [C] y", got)
	assert.Equal(t, []bool{true, true, true}, found)
}

// Values that only touch are not merged and keep their own masks; a value
// inside a longer one is masked as the longer one.
func TestMaskSecretLiterals_AdjacentAndContained(t *testing.T) {
	literals := []secretLiteral{
		{"12345zzzzz", "[Y]"},
		{"aaaabc12", "[X]"},
	}
	got, _ := maskSecretLiterals("aaaabc1212345zzzzz", literals)
	assert.Equal(t, "[X][Y]", got)

	literals = []secretLiteral{
		{"prefix-aaaabc12-suffix", "[L]"},
		{"aaaabc12", "[X]"},
	}
	got, found := maskSecretLiterals("a prefix-aaaabc12-suffix b aaaabc12", literals)
	assert.Equal(t, "a [L] b [X]", got)
	assert.Equal(t, []bool{true, true}, found)
}

// A value that overlaps itself in the text is covered whole, where a
// plain replace-all would leave its tail.
func TestMaskSecretLiterals_SelfOverlap(t *testing.T) {
	got, _ := maskSecretLiterals("x ababababab y", []secretLiteral{{"abababab", "[S]"}})
	assert.Equal(t, "x [S] y", got)
}

// No match, and an empty value, leave the text unchanged.
func TestMaskSecretLiterals_NoMatch(t *testing.T) {
	got, found := maskSecretLiterals("nothing here", []secretLiteral{{"", "[E]"}, {"absent-value", "[A]"}})
	assert.Equal(t, "nothing here", got)
	assert.Equal(t, []bool{false, false}, found)
}
