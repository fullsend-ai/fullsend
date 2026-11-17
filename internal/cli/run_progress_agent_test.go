package cli

import (
	"context"
	"os"
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
