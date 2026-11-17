package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/harness"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// progressFixture writes config.yaml (when set) into a fresh fullsend dir
// and loads it the way fullsend run does.
func progressFixture(t *testing.T, configYAML string) (runConfig, string) {
	t.Helper()
	t.Setenv("GITHUB_EVENT_PATH", "")
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GITHUB_EVENT_NAME", "")
	dir := t.TempDir()
	if configYAML != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(configYAML), 0o644))
	}
	rc, err := loadRunConfig(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	return rc, dir
}

func writeProgressEvent(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "event.json")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return p
}

func TestResolveRunProgressVerbosity(t *testing.T) {
	const jiraQuiet = "version: \"1\"\nprogress:\n  verbosity: full\n  by_source:\n    jira: quiet\n"
	tests := []struct {
		name      string
		config    string
		event     string
		wantLevel string
		wantRule  string
	}{
		{name: "key unset logs full with no line", config: "version: \"1\"\n", event: prepareJiraEvent, wantLevel: "full"},
		{name: "no config logs full", event: prepareJiraEvent, wantLevel: "full"},
		{name: "jira run is quiet", config: jiraQuiet, event: prepareJiraEvent, wantLevel: "quiet", wantRule: `Progress log: quiet (run source "jira" from the normalized event)`},
		{name: "github run in the same repo is full", config: jiraQuiet, event: prepareGitHubEvent, wantLevel: "full"},
		{name: "repo-wide tools", config: "version: \"1\"\nprogress:\n  verbosity: tools\n", event: prepareGitHubEvent, wantLevel: "tools", wantRule: "Progress log: tools"},
		{name: "unknown source gets the strictest entry", config: jiraQuiet, wantLevel: "quiet", wantRule: "run source unknown: no valid normalized event"},
		{name: "unresolvable level logs quiet", config: "version: \"1\"\nprogress:\n  verbosity: loud\n", event: prepareGitHubEvent, wantLevel: "quiet", wantRule: "the progress setting could not be resolved"},
		{name: "unresolvable source key logs quiet", config: "version: \"1\"\nprogress:\n  by_source:\n    Jira: tools\n", event: prepareGitHubEvent, wantLevel: "quiet", wantRule: "could not be resolved"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rc, dir := progressFixture(t, tc.config)
			eventFile := ""
			if tc.event != "" {
				eventFile = writeProgressEvent(t, tc.event)
			}
			level, rule := resolveRunProgressVerbosity(rc, dir, eventFile)
			assert.Equal(t, tc.wantLevel, level)
			if tc.wantRule == "" {
				assert.Empty(t, rule)
				return
			}
			assert.Contains(t, rule, tc.wantRule)
		})
	}
}

// An event file that cannot be read leaves the run source unresolvable.
// With any progress setting the run logs at quiet, as ADR 0163 says for an
// unresolvable level; with none it logs as today. runAgent reads the event
// file first and fails on an unreadable one (see
// TestRunAgent_UnreadableEventFileFailsBeforeProgressLevel), so this rule
// does not depend on that ordering.
func TestResolveRunProgressVerbosity_UnreadableEventFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")
	const wantRule = "Progress log: quiet (the run source could not be resolved: the event file could not be read)"

	for name, cfg := range map[string]string{
		"no config file":    "",
		"no progress block": "version: \"1\"\n",
		"explicit full":     "version: \"1\"\nprogress:\n  verbosity: full\n",
	} {
		t.Run(name, func(t *testing.T) {
			rc, dir := progressFixture(t, cfg)
			level, rule := resolveRunProgressVerbosity(rc, dir, missing)
			assert.Equal(t, config.ProgressVerbosityFull, level, "unset must log as today")
			assert.Empty(t, rule)
		})
	}
	for name, cfg := range map[string]string{
		"by_source only":  "version: \"1\"\nprogress:\n  by_source:\n    jira: tools\n",
		"by_source full":  "version: \"1\"\nprogress:\n  by_source:\n    github: full\n",
		"repo-wide tools": "version: \"1\"\nprogress:\n  verbosity: tools\n",
	} {
		t.Run(name, func(t *testing.T) {
			rc, dir := progressFixture(t, cfg)
			level, rule := resolveRunProgressVerbosity(rc, dir, missing)
			assert.Equal(t, config.ProgressVerbosityQuiet, level, "an unresolvable source fails closed")
			assert.Equal(t, wantRule, rule)
		})
	}
}

// runAgent reads the event file before it resolves the progress level, so
// an unreadable one fails the run before any progress line prints.
func TestRunAgent_UnreadableEventFileFailsBeforeProgressLevel(t *testing.T) {
	repoDir := newShallowTestRepo(t)
	useFakeOpenshellRepoDownload(t, "cp -R "+shellQuoteForTest(repoDir)+"/. \"$dest\"/", "")
	useProgressProbeRuntime(t)
	dir := writeValidationLoopFullsendDir(t, passingValidationScript, "", 1, "")
	writeProgressConfig(t, dir, config.ProgressVerbosityQuiet)

	var out strings.Builder
	err := runAgent(context.Background(), "code", dir, t.TempDir(), repoDir, fakeSandboxBinary(t),
		nil, false, "", "github", filepath.Join(t.TempDir(), "missing.json"), resolveFlags{maxDepth: 10, maxResources: 50},
		statusOpts{}, ui.New(&out), false, runOverrideFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading event file")
	assert.NotContains(t, out.String(), "Progress log:")
}

// The standard GitHub stage jobs carry no normalized event; a native
// GitHub event still counts as github, so by_source.jira does not quiet it.
func TestResolveRunProgressVerbosity_NativeGitHubEvent(t *testing.T) {
	rc, dir := progressFixture(t, "version: \"1\"\nprogress:\n  by_source:\n    jira: quiet\n")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_EVENT_NAME", "issue_comment")
	t.Setenv("GITHUB_EVENT_PATH", writeProgressEvent(t, `{"action":"created","issue":{"number":1}}`))
	level, rule := resolveRunProgressVerbosity(rc, dir, "")
	assert.Equal(t, config.ProgressVerbosityFull, level)
	assert.Empty(t, rule)
}

func TestValidationLines(t *testing.T) {
	out := []byte("  checked output SECRET-VALIDATION  \n")
	for _, level := range []string{"", config.ProgressVerbosityFull, config.ProgressVerbosityTools} {
		assert.Equal(t, "Validation passed: checked output SECRET-VALIDATION (1.5s)",
			validationPassedLine("Validation passed", out, nil, 1500*time.Millisecond, level), level)
		assert.Equal(t, "Validation failed: SECRET-FEEDBACK",
			validationFailedLine("Validation failed", "SECRET-FEEDBACK", level), level)
	}
	for _, level := range []string{config.ProgressVerbosityQuiet, "loud"} {
		assert.Equal(t, "Validation passed (1.5s)",
			validationPassedLine("Validation passed", out, nil, 1500*time.Millisecond, level), level)
		assert.Equal(t, "Validation failed", validationFailedLine("Validation failed", "SECRET-FEEDBACK", level), level)
	}
}

// At quiet the sweep prints passed or failed only, but the script still
// runs and decides the result.
func TestPostLoopValidationSweep_QuietHidesOutput(t *testing.T) {
	runDir := t.TempDir()
	for i := 1; i <= 2; i++ {
		require.NoError(t, os.MkdirAll(filepath.Join(runDir, fmt.Sprintf("iteration-%d", i)), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "iteration-1", "pass"), nil, 0o644))
	script := filepath.Join(t.TempDir(), "validate.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho SECRET-VALIDATION\n[ -f pass ]\n"), 0o755))
	h := &harness.Harness{RunnerEnv: map[string]string{}, ValidationLoop: &harness.ValidationLoop{Script: script}}

	var out bytes.Buffer
	result := postLoopValidationSweep(h, runDir, 2, true, config.ProgressVerbosityQuiet, ui.New(&out))
	assert.True(t, result.passed)
	assert.Equal(t, 1, result.validatedIter)
	assert.NotContains(t, out.String(), "SECRET-VALIDATION")
	assert.Contains(t, out.String(), "Post-loop validation failed (iteration 2)")
	assert.Contains(t, out.String(), "Validation passed (iteration 1)")

	out.Reset()
	postLoopValidationSweep(h, runDir, 2, true, "", ui.New(&out))
	assert.Contains(t, out.String(), "SECRET-VALIDATION", "full keeps today's output")
}

// The level gates printed lines only: at quiet the Level 3 content
// collector still receives reasoning, replies and tool calls for the
// agent span.
func TestIterationEventHandler_QuietStillFeedsCollector(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	var console bytes.Buffer
	renderer := agentruntime.NewEventRenderer(ui.New(&console))
	renderer.Verbosity = config.ProgressVerbosityQuiet
	collector := newContentCollector(4096)
	handler := iterationEventHandler(renderer.Handle, collector, nil)

	handler(agentruntime.ThinkingEvent{Text: "SECRET-THINK"})
	handler(agentruntime.TextEvent{Text: "SECRET-REPLY"})
	handler(agentruntime.ToolUseEvent{ID: "toolu_1", Name: "Read", Summary: "SECRET-TOOL"})
	handler(agentruntime.ToolResultEvent{ID: "toolu_1", Result: "SECRET-RESULT"})

	assert.NotContains(t, console.String(), "SECRET-")
	content := collector.Result("stop").OutputMessages
	for _, want := range []string{"SECRET-THINK", "SECRET-REPLY", "SECRET-TOOL", "SECRET-RESULT"} {
		assert.Contains(t, content, want)
	}
}

func TestEmitTranscriptErrorsAt(t *testing.T) {
	summaries := []agentruntime.TranscriptError{{
		Source: "session.jsonl", IsError: true, ErrorMessage: "SECRET-TRANSCRIPT", Subtype: "error_during_execution",
	}}
	var tx agentruntime.TranscriptHandler = agentruntime.DummyRuntime{}

	var full bytes.Buffer
	emitTranscriptErrorsAt(tx, &full, summaries, "")
	assert.Equal(t, "::error title=Agent Error (session.jsonl)::SECRET-TRANSCRIPT\n", full.String())

	var tools bytes.Buffer
	emitTranscriptErrorsAt(tx, &tools, summaries, config.ProgressVerbosityTools)
	assert.Equal(t, full.String(), tools.String())

	var quiet bytes.Buffer
	emitTranscriptErrorsAt(tx, &quiet, summaries, config.ProgressVerbosityQuiet)
	assert.Equal(t, "::error title=Agent Error::agent terminated with error (subtype: error_during_execution)\n", quiet.String())
	assert.NotContains(t, quiet.String(), "SECRET-TRANSCRIPT")
}

// At quiet the extracted output files print as a count only: their names
// are chosen by the agent. Other levels keep one line per file.
func TestPrintExtractedOutputFiles(t *testing.T) {
	files := []string{"SECRET-NAME.md", "agent-result.json"}
	for _, level := range []string{"", config.ProgressVerbosityFull, config.ProgressVerbosityTools} {
		var out bytes.Buffer
		printExtractedOutputFiles(ui.New(&out), files, 1500*time.Millisecond, level)
		assert.Contains(t, out.String(), "SECRET-NAME.md", level)
		assert.Contains(t, out.String(), "Extracted 2 output file(s) (1.5s)", level)
	}
	for _, level := range []string{config.ProgressVerbosityQuiet, "loud"} {
		var out bytes.Buffer
		printExtractedOutputFiles(ui.New(&out), files, 1500*time.Millisecond, level)
		assert.NotContains(t, out.String(), "SECRET-NAME", level)
		assert.NotContains(t, out.String(), "agent-result.json", level)
		assert.Contains(t, out.String(), "Extracted 2 output file(s) (1.5s)", level)
	}
}

// At quiet the output scan prints the finding count only: no file path and
// no finding detail. It still sanitizes the file.
func TestScanOutputFiles_QuietPrintsCountsOnly(t *testing.T) {
	secret := "Token: ghp_FAKEtesttoken" + strings.Repeat("0", 24) + "\n"
	for _, level := range []string{config.ProgressVerbosityQuiet, config.ProgressVerbosityFull} {
		t.Run(level, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "iteration-1", "output", "SECRET-NAME.md")
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.WriteFile(p, []byte(secret), 0o644))

			var out bytes.Buffer
			require.NoError(t, scanOutputFiles(dir, "trace-id", level, ui.New(&out)))
			got, err := os.ReadFile(p)
			require.NoError(t, err)
			assert.NotContains(t, string(got), "ghp_FAKEtest", "the file is sanitized at every level")
			assert.Contains(t, out.String(), "Sanitized 1 finding(s) in output files")
			if level == config.ProgressVerbosityQuiet {
				assert.NotContains(t, out.String(), "SECRET-NAME")
				assert.NotContains(t, out.String(), "Sanitized [")
				return
			}
			assert.Contains(t, out.String(), "Sanitized [")
			assert.Contains(t, out.String(), "SECRET-NAME.md")
		})
	}
}

func TestFailureLine(t *testing.T) {
	err := fmt.Errorf("copy SECRET-NAME.json: denied")
	for _, level := range []string{"", config.ProgressVerbosityFull, config.ProgressVerbosityTools} {
		assert.Equal(t, "Failed to extract output files: copy SECRET-NAME.json: denied",
			failureLine("Failed to extract output files", err, level), "level %q prints as before", level)
	}
	for _, level := range []string{config.ProgressVerbosityQuiet, "bogus"} {
		assert.Equal(t, "Failed to extract output files", failureLine("Failed to extract output files", err, level))
	}
}

func TestAuditChainFailure(t *testing.T) {
	const broken = "entry 2: hash mismatch: expected abc, got SECRET-HASH"
	for _, level := range []string{"", config.ProgressVerbosityFull, config.ProgressVerbosityTools} {
		line, err := auditChainFailure(broken, level)
		assert.Equal(t, "Audit log integrity check FAILED: "+broken, line, "level %q prints as before", level)
		assert.EqualError(t, err, "audit log integrity check failed: "+broken)
	}
	for _, level := range []string{config.ProgressVerbosityQuiet, "bogus"} {
		line, err := auditChainFailure(broken, level)
		assert.Equal(t, "Audit log integrity check FAILED", line)
		assert.EqualError(t, err, "audit log integrity check failed: "+broken,
			"the error keeps the message for telemetry; quietRunError replaces what main prints")
	}
}

// At quiet the run's final error prints as a fixed message, but only once
// telemetry has recorded it; the original stays reachable for errors.Is
// and the exit code. Other levels return the error unchanged.
func TestQuietRunError(t *testing.T) {
	orig := fmt.Errorf("extracting target repo (iteration 1): %w", errors.New("SECRET-PATH"))
	for _, level := range []string{"", config.ProgressVerbosityFull, config.ProgressVerbosityTools} {
		assert.Same(t, orig, quietRunError(orig, true, level), "level %q", level)
	}
	assert.Same(t, orig, quietRunError(orig, false, config.ProgressVerbosityQuiet),
		"an error telemetry never recorded prints as before")
	assert.NoError(t, quietRunError(nil, true, config.ProgressVerbosityQuiet))

	for _, level := range []string{config.ProgressVerbosityQuiet, "bogus"} {
		got := quietRunError(orig, true, level)
		assert.EqualError(t, got, quietRunErrorMessage)
		assert.ErrorIs(t, got, orig)
		var ec interface{ ExitCode() int }
		require.ErrorAs(t, got, &ec)
		assert.Equal(t, 1, ec.ExitCode(), "a plain error still exits 1")
	}
	stale := quietRunError(&staleHeadError{}, true, config.ProgressVerbosityQuiet)
	var ec interface{ ExitCode() int }
	require.ErrorAs(t, stale, &ec)
	assert.Equal(t, StaleHeadExitCode, ec.ExitCode(), "an error with its own exit code keeps it")
}
