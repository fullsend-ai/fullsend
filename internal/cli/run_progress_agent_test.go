package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// progressProbeRuntime wraps the dummy runtime: before each iteration it
// records the RunParams runAgent passed and sends reasoning, reply and
// tool events through OnEvent, the way a real runtime streams them.
type progressProbeRuntime struct {
	agentruntime.Runtime
	calls *[]agentruntime.RunParams
}

func (p progressProbeRuntime) Run(ctx context.Context, params agentruntime.RunParams, printer *ui.Printer, start time.Time, m *agentruntime.RunMetrics) (int, error) {
	*p.calls = append(*p.calls, params)
	params.OnEvent(agentruntime.ThinkingEvent{Text: "SECRET-THINK"})
	params.OnEvent(agentruntime.TextEvent{Text: "SECRET-REPLY"})
	params.OnEvent(agentruntime.ToolUseEvent{ID: "toolu_1", Name: "Bash", Summary: "SECRET-TOOL"})
	return p.Runtime.Run(ctx, params, printer, start, m)
}

// useProgressProbeRuntime makes runAgent run the dummy runtime from the
// fixture's config wrapped in progressProbeRuntime, and returns the
// recorded RunParams.
func useProgressProbeRuntime(t *testing.T) *[]agentruntime.RunParams {
	t.Helper()
	calls := &[]agentruntime.RunParams{}
	prev := resolveRunBackendFn
	resolveRunBackendFn = func(o runOverrides, rc runConfig, agentName string) (agentruntime.Backend, string, error) {
		b, src, err := prev(o, rc, agentName)
		if err != nil {
			return b, src, err
		}
		b.Runtime = progressProbeRuntime{Runtime: b.Runtime, calls: calls}
		return b, src, nil
	}
	t.Cleanup(func() { resolveRunBackendFn = prev })
	return calls
}

// writeProgressConfig adds a progress block to the fixture's config.yaml.
func writeProgressConfig(t *testing.T, dir, verbosity string) {
	t.Helper()
	cfg := "runtime: dummy\nagents:\n  - harness/code.yaml\n"
	if verbosity != "" {
		cfg += "progress:\n  verbosity: " + verbosity + "\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
}

// runAgent applies the resolved level to the renderer it hands the
// runtime: at quiet the job log carries no reasoning, reply or tool text;
// with the key unset it prints them as before. It also passes the level
// and a stderr file beside output.jsonl to the runtime.
func TestRunAgent_AppliesProgressLevelToRenderer(t *testing.T) {
	for _, verbosity := range []string{config.ProgressVerbosityQuiet, ""} {
		t.Run("verbosity="+verbosity, func(t *testing.T) {
			repoDir := newShallowTestRepo(t)
			useFakeOpenshellRepoDownload(t, "cp -R "+shellQuoteForTest(repoDir)+"/. \"$dest\"/", "")
			calls := useProgressProbeRuntime(t)
			dir := writeValidationLoopFullsendDir(t, passingValidationScript, "", 1, "")
			writeProgressConfig(t, dir, verbosity)

			out, err := runValidationLoopAgent(t, dir, t.TempDir(), repoDir)
			require.NoError(t, err)
			require.Len(t, *calls, 1)
			params := (*calls)[0]
			assert.Equal(t, filepath.Join(filepath.Dir(params.OutputPath), agentruntime.AgentStderrFile), params.StderrPath,
				"the stderr file sits beside output.jsonl")

			if verbosity == config.ProgressVerbosityQuiet {
				assert.Equal(t, config.ProgressVerbosityQuiet, params.ProgressVerbosity)
				assert.Contains(t, out, "Progress log: quiet")
				assert.NotContains(t, out, "SECRET-", "quiet must reach the renderer runAgent builds")
				return
			}
			assert.Equal(t, config.ProgressVerbosityFull, params.ProgressVerbosity)
			assert.NotContains(t, out, "Progress log:")
			for _, want := range []string{"SECRET-THINK", "SECRET-REPLY", "SECRET-TOOL"} {
				assert.Contains(t, out, want, "unset prints what it always did")
			}
		})
	}
}

// At quiet the validation failure prints as "Validation failed" only, but
// the next iteration's prompt and the feedback file still carry the
// script's full output.
func TestRunAgent_QuietValidationKeepsFeedbackForNextIteration(t *testing.T) {
	repoDir := newShallowTestRepo(t)
	useFakeOpenshellRepoDownload(t, "cp -R "+shellQuoteForTest(repoDir)+"/. \"$dest\"/", "")
	calls := useProgressProbeRuntime(t)
	marker := filepath.Join(t.TempDir(), "failed-once")
	script := "#!/bin/sh\n" +
		"if [ -f " + shellQuoteForTest(marker) + " ]; then echo ok; exit 0; fi\n" +
		": > " + shellQuoteForTest(marker) + "\n" +
		"echo 'SECRET-VALIDATION-FEEDBACK: tests failed'\nexit 1\n"
	dir := writeValidationLoopFullsendDir(t, script, "", 2, "append")
	writeProgressConfig(t, dir, config.ProgressVerbosityQuiet)
	outputBase := t.TempDir()

	out, err := runValidationLoopAgent(t, dir, outputBase, repoDir)
	require.NoError(t, err)
	assert.Contains(t, out, "Validation failed")
	assert.Contains(t, out, "Validation passed")
	assert.NotContains(t, out, "SECRET-VALIDATION-FEEDBACK", "quiet prints passed or failed only")

	require.Len(t, *calls, 2)
	assert.Contains(t, (*calls)[1].Prompt, "SECRET-VALIDATION-FEEDBACK",
		"the next iteration still gets the full validation feedback")
	feedback, err := os.ReadFile(filepath.Join(outputBase, mustRunDir(t, outputBase), "iteration-1", validationFeedbackFile))
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(feedback), "SECRET-VALIDATION-FEEDBACK"), "the feedback file keeps the full text")
}

// stderrToTempFile points os.Stderr at a temp file while fn runs and
// returns what was written. A file, not a pipe: runAgent writes more than
// a pipe buffer holds.
func stderrToTempFile(t *testing.T, fn func()) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	require.NoError(t, err)
	prev := os.Stderr
	os.Stderr = f
	defer func() { os.Stderr = prev }()
	fn()
	require.NoError(t, f.Close())
	data, err := os.ReadFile(f.Name())
	require.NoError(t, err)
	return string(data)
}

// useClaudeTranscripts makes runAgent extract transcripts with the Claude
// Code handler, so the real per-file lines run against the fake openshell.
func useClaudeTranscripts(t *testing.T) {
	t.Helper()
	prev := resolveRunBackendFn
	resolveRunBackendFn = func(o runOverrides, rc runConfig, agentName string) (agentruntime.Backend, string, error) {
		b, src, err := prev(o, rc, agentName)
		b.Transcripts = agentruntime.ClaudeRuntime{}
		return b, src, err
	}
	t.Cleanup(func() { resolveRunBackendFn = prev })
}

// wrapOpenshellExtraction puts an openshell in front of the current one:
// its output listing runs listing, its transcript listing names
// SECRET-TRANSCRIPT.jsonl, and copying SECRET-OUT.json fails with
// SECRET-DL-ERR. Everything else goes to the openshell it wraps.
func wrapOpenshellExtraction(t *testing.T, listing string) {
	t.Helper()
	prevBin, err := exec.LookPath("openshell")
	require.NoError(t, err)
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"case \"$1 $2\" in\n" +
		"  'sandbox exec')\n" +
		"    case \"$*\" in\n" +
		"      *\"/output -type f\"*) " + listing + " ;;\n" +
		"      *\"-name '*.jsonl'\"*) echo /sandbox/claude/projects/p/SECRET-TRANSCRIPT.jsonl; exit 0 ;;\n" +
		"    esac ;;\n" +
		"  'sandbox download')\n" +
		"    case \"$4\" in\n" +
		"      *SECRET-OUT*) echo SECRET-DL-ERR >&2; exit 1 ;;\n" +
		"      *SECRET-TRANSCRIPT*) mkdir -p \"$5\"; printf '{}\\n' > \"$5/$(basename \"$4\")\"; exit 0 ;;\n" +
		"    esac ;;\n" +
		"esac\n" +
		"exec " + shellQuoteForTest(prevBin) + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// At quiet neither the printer nor os.Stderr (where the sandbox and the
// runtimes write) carries an output file name, a transcript name or a
// copy or listing error. Unset prints them as before, which shows the
// test reaches those lines.
func TestRunAgent_QuietKeepsExtractionNamesAndErrorsOutOfTheLog(t *testing.T) {
	cases := map[string]string{
		"listing fails": "echo SECRET-LIST-ERR >&2; exit 1",
		"copy fails":    "echo /sandbox/workspace/output/SECRET-OUT.json; exit 0",
	}
	for name, listing := range cases {
		for _, verbosity := range []string{config.ProgressVerbosityQuiet, ""} {
			t.Run(name+"/verbosity="+verbosity, func(t *testing.T) {
				repoDir := newShallowTestRepo(t)
				useFakeOpenshellRepoDownload(t, "cp -R "+shellQuoteForTest(repoDir)+"/. \"$dest\"/", "")
				wrapOpenshellExtraction(t, listing)
				useProgressProbeRuntime(t)
				useClaudeTranscripts(t)
				dir := writeValidationLoopFullsendDir(t, passingValidationScript, "", 1, "")
				writeProgressConfig(t, dir, verbosity)

				var out string
				var err error
				stderr := stderrToTempFile(t, func() { out, err = runValidationLoopAgent(t, dir, t.TempDir(), repoDir) })
				require.NoError(t, err)
				assert.Contains(t, out, "Failed to extract output files")
				if verbosity == config.ProgressVerbosityQuiet {
					assert.NotContains(t, out, "SECRET-", "quiet printer lines carry no names or error text")
					assert.NotContains(t, stderr, "SECRET-", "quiet stderr lines carry no names or error text")
					assert.Contains(t, stderr, "Transcripts: 1 file(s) saved, 0 skipped or failed to copy")
					return
				}
				all := out + stderr
				assert.Contains(t, all, "SECRET-TRANSCRIPT.jsonl", "unset names the transcript as before")
				if name == "listing fails" {
					assert.Contains(t, out, "SECRET-LIST-ERR", "unset prints the error text as before")
				} else {
					assert.Contains(t, stderr, "Failed to copy SECRET-OUT.json", "unset names the file as before")
				}
			})
		}
	}
}

// A run with no validation loop returns the target-repo extraction error
// to main, which prints it as "Error: %v". At quiet that error prints as a
// fixed message, and neither the job log nor stderr carries the sandbox
// text it wraps; telemetry still records the full error. Unset prints the
// error as before.
func TestRunAgent_QuietFinalErrorIsFixed(t *testing.T) {
	for _, k := range []string{"OTEL_SDK_DISABLED", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"} {
		t.Setenv(k, "")
	}
	const marker = "SECRET-EXTRACT-PATH"
	for _, verbosity := range []string{config.ProgressVerbosityQuiet, ""} {
		t.Run("verbosity="+verbosity, func(t *testing.T) {
			repoDir := newShallowTestRepo(t)
			useFakeOpenshellRepoDownload(t, "echo '"+marker+"' >&2; exit 1", "")
			dir := writeValidationLoopFullsendDir(t, passingValidationScript, "", 1, "")
			require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"),
				[]byte("agent: agents/code.md\nrole: test\npolicy: policies/base.yaml\nsecurity:\n  enabled: false\n"), 0o644))
			writeProgressConfig(t, dir, verbosity)
			outputBase := t.TempDir()

			var out string
			var err error
			stderr := stderrToTempFile(t, func() {
				out, err = runValidationLoopAgent(t, dir, outputBase, repoDir)
			})
			require.Error(t, err)
			raw, readErr := os.ReadFile(onlyTelemetryFile(t, outputBase))
			require.NoError(t, readErr)
			assert.Contains(t, string(raw), marker, "telemetry records the full error at every level")

			if verbosity == config.ProgressVerbosityQuiet {
				assert.EqualError(t, err, quietRunErrorMessage, "what main prints")
				assert.NotContains(t, out, marker)
				assert.NotContains(t, stderr, marker)
				return
			}
			assert.Contains(t, err.Error(), "extracting target repo (iteration 1)")
			assert.Contains(t, err.Error(), marker, "unset returns the error as before")
		})
	}
}
