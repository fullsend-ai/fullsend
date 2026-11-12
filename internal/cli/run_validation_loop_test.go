package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/telemetry"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// useFakeOpenshellRepoDownload installs the providers stub behind a wrapper
// that answers `openshell sandbox download`, so a runAgent test reaches the
// validation loop through the real SafeDownload. policy is the shell body run
// for each repository download with $dest set to the local destination and $n
// set to the 1-based repository download count; it exits non-zero to fail the
// download. When outputFile is set, the first output listing reports it and
// its download returns fixed content. The returned function reports how many
// repository downloads happened.
func useFakeOpenshellRepoDownload(t *testing.T, policy, outputFile string) func() int {
	t.Helper()
	t.Setenv("GITHUB_ACTIONS", "false")
	// Keep ambient run overrides from changing the fixture's runtime or
	// starting a mint or OIDC exchange.
	for _, k := range []string{"FULLSEND_RUNTIME", "FULLSEND_MINT_URL", "FULLSEND_GCP_OIDC_URL"} {
		t.Setenv(k, "")
	}
	neutralizeAgentsRepoFallback(t)
	// ImportProfileVerified keeps its per-id content cache under os.TempDir().
	t.Setenv("TMPDIR", t.TempDir())

	base, err := filepath.Abs(filepath.Join("testdata", "providers-stub", "openshell"))
	require.NoError(t, err)
	baseScript, err := os.ReadFile(base)
	require.NoError(t, err)

	stubDir := t.TempDir()
	basePath := filepath.Join(stubDir, "openshell-base")
	require.NoError(t, os.WriteFile(basePath, baseScript, 0o755))
	countPath := filepath.Join(stubDir, "downloads")

	var outputArm, execArm string
	if outputFile != "" {
		quotedOutput := shellQuoteForTest(outputFile)
		listCountPath := filepath.Join(stubDir, "output-listings")
		outputArm = "    case \"$4\" in\n" +
			"      " + quotedOutput + ") printf 'iteration output\\n' > \"$dest/$(basename \"$4\")\"; exit 0 ;;\n" +
			"    esac\n"
		execArm = "  'sandbox exec')\n" +
			"    case \"$*\" in\n" +
			"      *\"/output -type f\"*)\n" +
			"        f=$(( $(cat " + shellQuoteForTest(listCountPath) + " 2>/dev/null || echo 0) + 1 ))\n" +
			"        echo \"$f\" > " + shellQuoteForTest(listCountPath) + "\n" +
			"        if [ \"$f\" -eq 1 ]; then printf '%s\\n' " + quotedOutput + "; fi\n" +
			"        exit 0 ;;\n" +
			"    esac\n" +
			"    exec " + shellQuoteForTest(basePath) + " \"$@\" ;;\n"
	}

	script := "#!/bin/sh\n" +
		"case \"$1 $2\" in\n" +
		"  'sandbox download')\n" +
		"    dest=\"$5\"\n" +
		"    mkdir -p \"$dest\"\n" +
		outputArm +
		"    n=$(( $(cat " + shellQuoteForTest(countPath) + " 2>/dev/null || echo 0) + 1 ))\n" +
		"    echo \"$n\" > " + shellQuoteForTest(countPath) + "\n" +
		policy + "\n" +
		"    exit 0 ;;\n" +
		execArm +
		"esac\n" +
		"exec " + shellQuoteForTest(basePath) + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(stubDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return func() int {
		data, err := os.ReadFile(countPath)
		if err != nil {
			return 0
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			return 0
		}
		return n
	}
}

// writeValidationLoopFullsendDir writes a minimal runnable harness whose
// validation loop runs script and whose post_script runs postScript when set.
func writeValidationLoopFullsendDir(t *testing.T, script, postScript string, maxIterations int, feedbackMode string) string {
	t.Helper()
	dir := t.TempDir()
	writePolicyFixture(t, dir)

	validation := "validation_loop:\n  script: scripts/validate.sh\n  max_iterations: " +
		strconv.Itoa(maxIterations) + "\n"
	if feedbackMode != "" {
		validation += "  feedback_mode: " + feedbackMode + "\n"
	}
	harness := "agent: agents/code.md\nrole: test\npolicy: policies/base.yaml\n" +
		"security:\n  enabled: false\n"
	if postScript != "" {
		harness += "post_script: scripts/post.sh\n"
	}
	files := map[string]string{
		"agents/code.md":                  "You are a coding agent.",
		"behaviour/current-scenario.yaml": "ops:\n  - op: wait\n    args: '1'\n",
		"config.yaml":                     "runtime: dummy\nagents:\n  - harness/code.yaml\n",
		"harness/code.yaml":               harness + validation,
	}
	if postScript != "" {
		files["scripts/post.sh"] = postScript
	}
	for path, content := range files {
		file := filepath.Join(dir, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
		require.NoError(t, os.WriteFile(file, []byte(content), 0o644))
	}
	scriptPath := filepath.Join(dir, "scripts", "validate.sh")
	require.NoError(t, os.MkdirAll(filepath.Dir(scriptPath), 0o755))
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))
	if postScript != "" {
		require.NoError(t, os.Chmod(filepath.Join(dir, "scripts", "post.sh"), 0o755))
	}
	return dir
}

// runValidationLoopAgent runs the agent in a fake OpenShell sandbox and returns
// its console output.
func runValidationLoopAgent(t *testing.T, dir, outputBase, repoDir string) (string, error) {
	t.Helper()
	var out strings.Builder
	err := runAgent(context.Background(), "code", dir, outputBase, repoDir, fakeSandboxBinary(t),
		nil, false, "", "github", "", resolveFlags{maxDepth: 10, maxResources: 50},
		statusOpts{}, ui.New(&out), false, runOverrideFlags{})
	return out.String(), err
}

// onlyTelemetryFile returns the single run-telemetry.jsonl under outputBase.
func onlyTelemetryFile(t *testing.T, outputBase string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(outputBase, "*", telemetry.TelemetryFile))
	require.NoError(t, err)
	require.Len(t, matches, 1)
	return matches[0]
}

const passingValidationScript = "#!/bin/sh\nexit 0\n"

// TestRunAgentValidationLoop_RejectedRepositoryFeedsNextIteration drives the
// real validation loop: SafeDownload rejects the first download, the next
// iteration gets fixed trusted feedback, returns a valid checkout, and
// validation passes.
func TestRunAgentValidationLoop_RejectedRepositoryFeedsNextIteration(t *testing.T) {
	for _, k := range []string{"OTEL_SDK_DISABLED", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"} {
		t.Setenv(k, "")
	}
	t.Setenv(telemetry.ContentCaptureEnvVar, "true")

	repoDir := newShallowTestRepo(t)
	downloads := useFakeOpenshellRepoDownload(t,
		"if [ \"$n\" -eq 1 ]; then : > \"$dest/.git\"; else cp -R "+shellQuoteForTest(repoDir)+"/. \"$dest\"/; fi", "")
	dir := writeValidationLoopFullsendDir(t, passingValidationScript, "", 2, "append")
	outputBase := t.TempDir()

	out, err := runValidationLoopAgent(t, dir, outputBase, repoDir)
	require.NoError(t, err)
	assert.Equal(t, 2, downloads(), "one rejected download, then one valid checkout")
	assert.Contains(t, out, "Iteration 2 of 2")
	assert.Contains(t, out, "Repository validation failed; retrying with validation feedback")
	assert.Contains(t, out, "Injecting validation feedback into agent prompt")
	assert.Contains(t, out, "Validation passed")

	feedback, readErr := os.ReadFile(filepath.Join(outputBase, mustRunDir(t, outputBase), "iteration-1", validationFeedbackFile))
	require.NoError(t, readErr)
	assert.Contains(t, string(feedback), "real root .git directory",
		"the audit artifact carries the rejection feedback")
	assert.Contains(t, string(feedback), "preserving existing work")

	raw, readErr := os.ReadFile(onlyTelemetryFile(t, outputBase))
	require.NoError(t, readErr)
	assert.Contains(t, string(raw), "one or more repository requirements were not met",
		"the rejection feedback reaches the next agent prompt")
	assert.Contains(t, string(raw), "plain directory is supported")
	assert.Contains(t, string(raw), "commondir or alternate object stores")
	assert.Contains(t, string(raw), "preserving existing work")
	assert.NotContains(t, string(raw), "is not a real directory",
		"the sandbox-provided error must not be injected into the prompt")
}

// TestRunAgentValidationLoop_CheckoutValidationFailureFeedsGenericFeedback
// covers a validation failure that is not a classified rejection: the first
// download loses .git/HEAD, the next iteration gets the generic feedback and a
// valid checkout, and validation passes.
func TestRunAgentValidationLoop_CheckoutValidationFailureFeedsGenericFeedback(t *testing.T) {
	for _, k := range []string{"OTEL_SDK_DISABLED", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"} {
		t.Setenv(k, "")
	}
	t.Setenv(telemetry.ContentCaptureEnvVar, "true")

	repoDir := newShallowTestRepo(t)
	downloads := useFakeOpenshellRepoDownload(t,
		"if [ \"$n\" -eq 1 ]; then cp -R "+shellQuoteForTest(repoDir)+"/. \"$dest\"/ && /bin/rm \"$dest/.git/HEAD\"; else cp -R "+shellQuoteForTest(repoDir)+"/. \"$dest\"/; fi", "")
	dir := writeValidationLoopFullsendDir(t, passingValidationScript, "", 2, "append")
	outputBase := t.TempDir()

	out, err := runValidationLoopAgent(t, dir, outputBase, repoDir)
	require.NoError(t, err)
	assert.Equal(t, 2, downloads(), "one invalid checkout, then one valid checkout")
	assert.Contains(t, out, "Iteration 2 of 2")
	assert.Contains(t, out, "Repository validation failed; retrying with validation feedback")
	assert.Contains(t, out, "Injecting validation feedback into agent prompt")
	assert.Contains(t, out, "Validation passed")

	raw, readErr := os.ReadFile(onlyTelemetryFile(t, outputBase))
	require.NoError(t, readErr)
	assert.Contains(t, string(raw), "The checkout may be invalid",
		"the ambiguous-failure feedback reaches the next agent prompt")
	assert.Contains(t, string(raw), "Git, the runner, or the filesystem may have failed")
	assert.NotContains(t, string(raw), "not a gitdir",
		"the sandbox-provided error must not be injected into the prompt")
}

// TestRunAgentValidationLoop_CheckoutValidationFailureRetriesWithoutFeedback
// proves an ambiguous Git validation failure keeps retrying without feedback.
func TestRunAgentValidationLoop_CheckoutValidationFailureRetriesWithoutFeedback(t *testing.T) {
	repoDir := newShallowTestRepo(t)
	downloads := useFakeOpenshellRepoDownload(t,
		"if [ \"$n\" -eq 1 ]; then cp -R "+shellQuoteForTest(repoDir)+"/. \"$dest\"/ && /bin/rm \"$dest/.git/HEAD\"; else cp -R "+shellQuoteForTest(repoDir)+"/. \"$dest\"/; fi", "")
	dir := writeValidationLoopFullsendDir(t, passingValidationScript, "", 2, "")
	outputBase := t.TempDir()

	out, err := runValidationLoopAgent(t, dir, outputBase, repoDir)
	require.NoError(t, err)
	assert.Equal(t, 2, downloads(), "the ambiguous failure retries the next iteration")
	assert.Contains(t, out, "Iteration 2 of 2")
	assert.NotContains(t, out, "Injecting validation feedback into agent prompt")
	_, statErr := os.Stat(filepath.Join(outputBase, mustRunDir(t, outputBase), "iteration-1", validationFeedbackFile))
	assert.True(t, os.IsNotExist(statErr), "no feedback artifact is written without append")
	assert.Contains(t, out, "Validation passed")
}

// TestRunAgentValidationLoop_GitTimeoutRetriesWithoutFeedback covers a native
// Git query that exceeds its deadline: the first checkout is not validated, the
// next iteration retries and validates.
func TestRunAgentValidationLoop_GitTimeoutRetriesWithoutFeedback(t *testing.T) {
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	delayed := filepath.Join(t.TempDir(), "delayed")
	stubDir := t.TempDir()
	stub := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *rev-parse*--resolve-git-dir*.fullsend-safe-download-*)\n" +
		"    if [ ! -f " + shellQuoteForTest(delayed) + " ]; then\n" +
		"      : > " + shellQuoteForTest(delayed) + "\n" +
		"      exec /bin/sleep 10\n" +
		"    fi ;;\n" +
		"esac\n" +
		"exec " + shellQuoteForTest(realGit) + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(stubDir, "git"), []byte(stub), 0o755))

	repoDir := newShallowTestRepo(t)
	downloads := useFakeOpenshellRepoDownload(t,
		"cp -R "+shellQuoteForTest(repoDir)+"/. \"$dest\"/", "")
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir := writeValidationLoopFullsendDir(t, passingValidationScript, "", 2, "")

	out, err := runValidationLoopAgent(t, dir, t.TempDir(), repoDir)
	require.NoError(t, err)
	require.FileExists(t, delayed, "the first staging Git query must have been delayed")
	assert.Equal(t, 2, downloads(), "a published first checkout would have validated and ended the loop")
	assert.Contains(t, out, "context deadline exceeded")
	assert.Contains(t, out, "Iteration 2 of 2")
	assert.NotContains(t, out, "Injecting validation feedback into agent prompt")
	assert.Contains(t, out, "Validation passed")
}

// TestRunAgentValidationLoop_RejectedRepositoryWithoutFeedbackStops proves a
// directly detected malformed repository stops the retry loop without feedback,
// and the post-loop sweep still validates the output.
func TestRunAgentValidationLoop_RejectedRepositoryWithoutFeedbackStops(t *testing.T) {
	repoDir := newShallowTestRepo(t)
	downloads := useFakeOpenshellRepoDownload(t, ": > \"$dest/.git\"", "")
	dir := writeValidationLoopFullsendDir(t, passingValidationScript, "", 2, "")

	out, err := runValidationLoopAgent(t, dir, t.TempDir(), repoDir)
	require.NoError(t, err, "the post-loop sweep still validates the iteration output")
	assert.Equal(t, 1, downloads(), "no second agent iteration runs")
	assert.NotContains(t, out, "Iteration 2 of 2")
	assert.Contains(t, out, "Repository output rejected; stopping agent retries")
	assert.Contains(t, out, "Post-loop validation (iteration 1)")
}

// TestRunAgentValidationLoop_ReftableHeadRejectionStopsWithoutFeedback: a
// directory .git/HEAD fails the sanitizer before Git runs.
func TestRunAgentValidationLoop_ReftableHeadRejectionStopsWithoutFeedback(t *testing.T) {
	repoDir := newShallowTestRepo(t)
	downloads := useFakeOpenshellRepoDownload(t,
		"mkdir -p \"$dest/.git/reftable\" \"$dest/.git/HEAD\"", "")
	dir := writeValidationLoopFullsendDir(t, passingValidationScript, "", 2, "")

	out, err := runValidationLoopAgent(t, dir, t.TempDir(), repoDir)
	require.NoError(t, err, "the post-loop sweep still validates the iteration output")
	assert.Equal(t, 1, downloads(), "no second agent iteration runs")
	assert.NotContains(t, out, "Iteration 2 of 2")
	assert.Contains(t, out, "Repository output rejected; stopping agent retries")
	assert.Contains(t, out, "Post-loop validation (iteration 1)")
}

// TestRunAgentValidationLoop_RejectedRepositoryOnFinalIterationKeepsSweep
// proves the last configured iteration reports the stop truthfully instead of
// promising a feedback retry, and the post-loop sweep still runs.
func TestRunAgentValidationLoop_RejectedRepositoryOnFinalIterationKeepsSweep(t *testing.T) {
	repoDir := newShallowTestRepo(t)
	downloads := useFakeOpenshellRepoDownload(t, ": > \"$dest/.git\"", "")
	dir := writeValidationLoopFullsendDir(t, passingValidationScript, "", 1, "append")

	out, err := runValidationLoopAgent(t, dir, t.TempDir(), repoDir)
	require.NoError(t, err, "the post-loop sweep still validates the iteration output")
	assert.Equal(t, 1, downloads())
	assert.NotContains(t, out, "retrying with validation feedback",
		"no iteration remains to receive the feedback")
	assert.Contains(t, out, "no iterations remain")
	assert.Contains(t, out, "Post-loop validation (iteration 1)")
}

// TestRunAgentValidationLoop_TransientExtractionFailureRetries proves an
// ordinary extraction failure keeps the existing retry behavior: the next
// iteration runs the agent again and can pass.
func TestRunAgentValidationLoop_TransientExtractionFailureRetries(t *testing.T) {
	repoDir := newShallowTestRepo(t)
	// SafeDownload attempts the download twice per iteration; both must fail
	// before the iteration is skipped.
	downloads := useFakeOpenshellRepoDownload(t,
		"if [ \"$n\" -le 2 ]; then exit 1; fi; cp -R "+shellQuoteForTest(repoDir)+"/. \"$dest\"/", "")
	dir := writeValidationLoopFullsendDir(t, passingValidationScript, "", 2, "")

	out, err := runValidationLoopAgent(t, dir, t.TempDir(), repoDir)
	require.NoError(t, err)
	assert.Equal(t, 3, downloads(), "two failed attempts, then the retry succeeds")
	assert.Contains(t, out, "Failed to extract target repo (cleaning up)")
	assert.Contains(t, out, "Iteration 2 of 2")
	assert.Contains(t, out, "Validation passed")
}

// TestRunAgentValidationLoop_RejectedIterationKeepsEarlierOutputForSweep
// proves a classified rejection on a later iteration leaves an earlier
// iteration's extracted output intact: the output-only sweep validates it with
// an empty TARGET_REPO_DIR, and the post-script gets an empty REPO_DIR.
func TestRunAgentValidationLoop_RejectedIterationKeepsEarlierOutputForSweep(t *testing.T) {
	repoDir := newShallowTestRepo(t)
	remoteOutput := "/sandbox/workspace/output/marker.txt"
	// Iteration 1 returns a valid checkout; iteration 2 is rejected.
	downloads := useFakeOpenshellRepoDownload(t,
		"if [ \"$n\" -eq 2 ]; then : > \"$dest/.git\"; else cp -R "+shellQuoteForTest(repoDir)+"/. \"$dest\"/; fi",
		remoteOutput)

	outputBase := t.TempDir()
	records := filepath.Join(t.TempDir(), "validation-records")
	postRecords := filepath.Join(t.TempDir(), "post-records")
	validation := fmt.Sprintf(`#!/bin/sh
marker=no
[ -f output/marker.txt ] && marker=yes
printf '%%s|%%s|%%s\n' "$PWD" "$TARGET_REPO_DIR" "$marker" >> %s
if [ "$marker" = yes ] && [ -n "$TARGET_REPO_DIR" ]; then exit 1; fi
[ "$marker" = yes ]
`, shellQuoteForTest(records))
	postScript := fmt.Sprintf("#!/bin/sh\nprintf '%%s|%%s\\n' \"$REPO_DIR\" \"$FULLSEND_VALIDATED_ITERATION_DIR\" >> %s\n",
		shellQuoteForTest(postRecords))
	dir := writeValidationLoopFullsendDir(t, validation, postScript, 2, "")

	out, err := runValidationLoopAgent(t, dir, outputBase, repoDir)
	require.NoError(t, err)
	assert.Equal(t, 2, downloads(), "one repository download per iteration")

	runDir := filepath.Join(outputBase, mustRunDir(t, outputBase))
	marker := filepath.Join(runDir, "iteration-1", "output", "marker.txt")
	data, readErr := os.ReadFile(marker)
	require.NoError(t, readErr, "the earlier iteration's extracted output must survive the rejected iteration")
	assert.Equal(t, "iteration output\n", string(data))

	inlineSawOutput, sweepSawOutput := false, false
	for _, line := range readLines(t, records) {
		parts := strings.Split(line, "|")
		require.Len(t, parts, 3)
		if parts[2] != "yes" {
			assert.Empty(t, parts[1], "an output-less sweep run gets an empty TARGET_REPO_DIR")
			continue
		}
		if parts[1] == "" {
			sweepSawOutput = true
		} else {
			inlineSawOutput = true
		}
	}
	assert.True(t, inlineSawOutput, "the inline run sees the extracted output")
	assert.True(t, sweepSawOutput, "the output-only sweep sees it with an empty TARGET_REPO_DIR")

	post := readLines(t, postRecords)
	require.Len(t, post, 1)
	fields := strings.Split(post[0], "|")
	require.Len(t, fields, 2)
	assert.Empty(t, fields[0], "the post-script gets an empty REPO_DIR for a non-final validated iteration")
	assert.Equal(t, filepath.Join(runDir, "iteration-1", "output"), fields[1])
	assert.Contains(t, out, "Validation passed (iteration 1)")
}

// readLines returns the non-empty lines of a test record file.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// mustRunDir returns the single run directory under outputBase.
func mustRunDir(t *testing.T, outputBase string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(outputBase, "*"))
	require.NoError(t, err)
	require.Len(t, matches, 1)
	return filepath.Base(matches[0])
}
