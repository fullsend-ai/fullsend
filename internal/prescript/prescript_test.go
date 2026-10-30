package prescript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepare_CreatesRemovableFileInDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path, cleanup, err := Prepare(dir)
	require.NoError(t, err)
	require.FileExists(t, path)
	// The file lives in the run directory, not the system temp dir, so a
	// tmp reaper cannot remove it during a long pre-script and it lands in
	// the uploaded run artifacts.
	assert.Equal(t, dir, filepath.Dir(path))

	cleanup()
	assert.NoFileExists(t, path)
}

func TestParseFile_MissingFileIsHardError(t *testing.T) {
	t.Parallel()

	// Prepare creates the file before the script runs, so absence
	// afterwards means the script removed it — treating that as "proceed"
	// is the silent-proceed failure this protocol prevents.
	_, err := ParseFile(filepath.Join(t.TempDir(), "does-not-exist"))
	require.ErrorContains(t, err, "is missing")
}

func TestParseFile_EmptyFile(t *testing.T) {
	t.Parallel()

	path := writeOutput(t, "")
	res, err := ParseFile(path)
	require.NoError(t, err)
	assert.False(t, res.Skipped)
}

func TestParseFile_Skip(t *testing.T) {
	t.Parallel()

	path := writeOutput(t, "skipped=true\nreason=an open PR already addresses this issue\n")
	res, err := ParseFile(path)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Equal(t, "an open PR already addresses this issue", res.Reason)
}

func TestParseFile_ExplicitFalse(t *testing.T) {
	t.Parallel()

	path := writeOutput(t, "skipped=false\n")
	res, err := ParseFile(path)
	require.NoError(t, err)
	assert.False(t, res.Skipped)
}

func TestParseFile_CommentsBlanksAndCRLF(t *testing.T) {
	t.Parallel()

	path := writeOutput(t, "# comment\r\n\r\nskipped=true\r\n")
	res, err := ParseFile(path)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
}

func TestParseFile_LastAssignmentWins(t *testing.T) {
	t.Parallel()

	path := writeOutput(t, "skipped=true\nskipped=false\n")
	res, err := ParseFile(path)
	require.NoError(t, err)
	assert.False(t, res.Skipped)
}

func TestParseFile_ExtraOutputs(t *testing.T) {
	t.Parallel()

	path := writeOutput(t, "skipped=true\nexisting_pr=123\n")
	res, err := ParseFile(path)
	require.NoError(t, err)
	assert.Equal(t, "123", res.Outputs["existing_pr"])
}

func TestParseFile_ValuePreservesEqualsAndSpaces(t *testing.T) {
	t.Parallel()

	path := writeOutput(t, "reason=a=b c\n")
	res, err := ParseFile(path)
	require.NoError(t, err)
	assert.Equal(t, "a=b c", res.Reason)
}

func TestParseFile_MalformedLine(t *testing.T) {
	t.Parallel()

	path := writeOutput(t, "skipped true\n")
	_, err := ParseFile(path)
	require.ErrorContains(t, err, "not key=value")
}

func TestParseFile_InvalidKey(t *testing.T) {
	t.Parallel()

	path := writeOutput(t, "bad key=1\n")
	_, err := ParseFile(path)
	require.ErrorContains(t, err, "invalid key")
}

func TestParseFile_InvalidSkippedValue(t *testing.T) {
	t.Parallel()

	path := writeOutput(t, "skipped=yes\n")
	_, err := ParseFile(path)
	require.ErrorContains(t, err, `skipped must be "true" or "false"`)
}

func TestParseFile_OversizeFile(t *testing.T) {
	t.Parallel()

	path := writeOutput(t, "# "+strings.Repeat("x", maxOutputSize)+"\n")
	_, err := ParseFile(path)
	require.ErrorContains(t, err, "exceeds maximum size")
}

func TestRelay_NoTarget(t *testing.T) {
	// t.Setenv forbids t.Parallel.
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_OUTPUT", "")

	relayed, err := Relay(Result{Skipped: true})
	require.NoError(t, err)
	assert.False(t, relayed)
}

// A stray exported GITHUB_OUTPUT in a local shell must not make fullsend
// append to an unrelated file — GITHUB_ACTIONS is the repo-wide signal.
func TestRelay_RequiresGitHubActions(t *testing.T) {
	out := filepath.Join(t.TempDir(), "github-output")
	t.Setenv("GITHUB_OUTPUT", out)
	t.Setenv("GITHUB_ACTIONS", "")

	relayed, err := Relay(Result{Skipped: true})
	require.NoError(t, err)
	assert.False(t, relayed)
	assert.NoFileExists(t, out)
}

func TestRelay_RejectsControlCharactersInValues(t *testing.T) {
	out := filepath.Join(t.TempDir(), "github-output")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_OUTPUT", out)

	// Relay is exported, so a hand-built Result must not be able to smuggle
	// entries past the parser's validation.
	relayed, err := Relay(Result{
		Skipped: true,
		Outputs: map[string]string{"reason": "dup\rskipped=false"},
	})
	require.ErrorContains(t, err, "control character")
	assert.False(t, relayed)
	assert.NoFileExists(t, out)
}

func TestRelay_WritesSkippedAndOutputs(t *testing.T) {
	out := filepath.Join(t.TempDir(), "github-output")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_OUTPUT", out)

	res := Result{
		Skipped: true,
		Reason:  "duplicate",
		Outputs: map[string]string{
			"skipped":  "true",
			"disabled": "true",
			"reason":   "duplicate",
			"pr":       "42",
		},
	}
	relayed, err := Relay(res)
	require.NoError(t, err)
	assert.True(t, relayed)

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "skipped=true\ndisabled=true\npr=42\nreason=duplicate\n", string(data))
}

func TestRelay_AppendsToExistingFile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "github-output")
	require.NoError(t, os.WriteFile(out, []byte("prior=1\n"), 0o600))
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_OUTPUT", out)

	relayed, err := Relay(Result{Skipped: false})
	require.NoError(t, err)
	assert.True(t, relayed)

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "prior=1\nskipped=false\n", string(data))
}

func writeOutput(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prescript-output")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// An embedded CR is a line terminator to the GitHub Actions runner, so a
// value carrying one could smuggle extra GITHUB_OUTPUT entries — including
// an override of skipped itself. Rejecting it at parse time is what makes
// the plain key=value relay safe.
func TestParseFile_RejectsEmbeddedControlCharacters(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"carriage return in reason": "skipped=true\nreason=dup\rskipped=false\n",
		"NUL in reason":             "reason=a\x00b\n",
		"escape in extra output":    "pr=\x1b[31mred\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseFile(writeOutput(t, content))
			require.ErrorContains(t, err, "control character")
		})
	}
}

// GitHub Actions output names conventionally use hyphens; rejecting them
// would turn an ordinary key into a hard run failure.
func TestParseFile_HyphenatedKeysAllowed(t *testing.T) {
	t.Parallel()

	res, err := ParseFile(writeOutput(t, "skipped=true\nexisting-pr=123\n"))
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Equal(t, "123", res.Outputs["existing-pr"])
}

// A mis-cased reserved key would otherwise be stored as an unrelated
// output and the run would proceed silently — the exact failure the
// hard-error policy exists to prevent.
func TestParseFile_MiscasedReservedKeyIsHardError(t *testing.T) {
	t.Parallel()

	for _, line := range []string{"SKIPPED=true\n", "Skipped=true\n", "REASON=dup\n", "Disabled=true\n"} {
		_, err := ParseFile(writeOutput(t, line))
		require.ErrorContains(t, err, "differs only in case", "input %q", line)
	}
}

func TestParseFile_HeredocSyntaxNamesTheLimitation(t *testing.T) {
	t.Parallel()

	_, err := ParseFile(writeOutput(t, "skipped=true\nreason<<EOF\nline one\nEOF\n"))
	require.ErrorContains(t, err, "heredoc syntax")
}

// The disabled key is reserved for CLI use (when an agent is disabled in
// config) and must not be emitted by pre-scripts.
func TestParseFile_DisabledKeyIsReservedForCLI(t *testing.T) {
	t.Parallel()

	_, err := ParseFile(writeOutput(t, "disabled=true\n"))
	require.ErrorContains(t, err, `key "disabled" is reserved for CLI use and must not be emitted by pre-scripts`)
}

func TestDisabledResult(t *testing.T) {
	t.Parallel()

	res := DisabledResult("agent disabled in config")
	assert.True(t, res.Skipped)
	assert.Equal(t, "agent disabled in config", res.Reason)
	assert.Equal(t, map[string]string{
		disabledKey: "true",
		"reason":    "agent disabled in config",
	}, res.Outputs)
}

// Whitespace around the value is insignificant, matching the key side —
// "skipped = true" must not fail the run.
func TestParseFile_ValueWhitespaceIsInsignificant(t *testing.T) {
	t.Parallel()

	res, err := ParseFile(writeOutput(t, "skipped = true\nreason=   padded   \n"))
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Equal(t, "padded", res.Reason)
}

func TestLogLine(t *testing.T) {
	t.Parallel()

	assert.Empty(t, LogLine(Result{}))
	assert.Equal(t, "pr=42 reason=dup skipped=true", LogLine(Result{
		Outputs: map[string]string{"skipped": "true", "reason": "dup", "pr": "42"},
	}))
}

// `skipped=${SKIP}` with SKIP unset must not degrade into a duplicate run.
// An *absent* skipped key still means proceed.
func TestParseFile_EmptySkippedValueIsHardError(t *testing.T) {
	t.Parallel()

	_, err := ParseFile(writeOutput(t, "skipped=\nreason=meant to skip\n"))
	require.ErrorContains(t, err, `skipped must be "true" or "false"`)

	res, err := ParseFile(writeOutput(t, "reason=no skip requested\n"))
	require.NoError(t, err)
	assert.False(t, res.Skipped)
}

// A newline in a key smuggles a GITHUB_OUTPUT line just as effectively as
// one in a value — including an override of the skipped Relay just wrote.
func TestRelay_RejectsInvalidKeys(t *testing.T) {
	out := filepath.Join(t.TempDir(), "github-output")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_OUTPUT", out)

	relayed, err := Relay(Result{
		Skipped: false,
		Outputs: map[string]string{"a\nskipped": "true"},
	})
	require.ErrorContains(t, err, "invalid key")
	assert.False(t, relayed)
	assert.NoFileExists(t, out)
}

func TestLogLine_DropsInvalidOutputs(t *testing.T) {
	t.Parallel()

	assert.Empty(t, LogLine(Result{Outputs: map[string]string{"a\nskipped": "true"}}))
	assert.Empty(t, LogLine(Result{Outputs: map[string]string{"ok": "a\rb"}}))
}

// The heredoc check is anchored at the key, so an ordinary value
// containing "<<" is not mistaken for one.
func TestParseFile_HeredocDetection(t *testing.T) {
	t.Parallel()

	// Delimiter containing "=" still names the limitation.
	_, err := ParseFile(writeOutput(t, "reason<<E=OF\nbody\nE=OF\n"))
	require.ErrorContains(t, err, "heredoc syntax")

	res, err := ParseFile(writeOutput(t, "reason=shift left << 2\n"))
	require.NoError(t, err)
	assert.Equal(t, "shift left << 2", res.Reason)
}
