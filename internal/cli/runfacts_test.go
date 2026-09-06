package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

func TestBuildRunFactsEnvLines(t *testing.T) {
	at := time.Date(2026, 9, 4, 8, 30, 0, 0, time.UTC)

	t.Run("exports both facts", func(t *testing.T) {
		lines := buildRunFactsEnvLines(runFacts{headSHA: "abc123", startedAt: at})
		assert.Equal(t, []string{
			"export FULLSEND_RUN_STARTED_AT='2026-09-04T08:30:00Z'",
			"export FULLSEND_RUN_HEAD_SHA='abc123'",
		}, lines)
	})

	t.Run("an issue run exports an empty head rather than omitting it", func(t *testing.T) {
		// The agent skips its re-check on an empty value, so the variable
		// must exist and be empty rather than be absent.
		lines := buildRunFactsEnvLines(runFacts{startedAt: at})
		assert.Contains(t, lines, "export FULLSEND_RUN_HEAD_SHA=''")
	})

	t.Run("the start is normalised to UTC", func(t *testing.T) {
		east := time.FixedZone("UTC+5", 5*60*60)
		lines := buildRunFactsEnvLines(runFacts{startedAt: at.In(east)})
		assert.Contains(t, lines, "export FULLSEND_RUN_STARTED_AT='2026-09-04T08:30:00Z'")
	})

	t.Run("a quote in the SHA cannot break out of the export", func(t *testing.T) {
		lines := buildRunFactsEnvLines(runFacts{headSHA: "a'; rm -rf /; '", startedAt: at})
		assert.Contains(t, lines, `export FULLSEND_RUN_HEAD_SHA='a'\''; rm -rf /; '\'''`)
	})
}

func TestRunHeadSHA(t *testing.T) {
	// The source-branch SHA is set only in merged results pipelines, so all
	// three shapes matter: it wins when present, CI_COMMIT_SHA covers the
	// ordinary merge request pipeline where it is empty, and a pipeline that
	// is not a merge request has no head to report.
	t.Run("gitlab prefers the merge request source sha", func(t *testing.T) {
		t.Setenv("CI_MERGE_REQUEST_SOURCE_BRANCH_SHA", "gl123")
		// Present too, and must lose: in a merged results pipeline this is
		// the merge-result commit, not the source head.
		t.Setenv("CI_COMMIT_SHA", "merge-result-sha")
		t.Setenv("CI_PIPELINE_SOURCE", "merge_request_event")
		assert.Equal(t, "gl123", runHeadSHA("gitlab"))
	})

	t.Run("gitlab falls back on an ordinary merge request pipeline", func(t *testing.T) {
		t.Setenv("CI_MERGE_REQUEST_SOURCE_BRANCH_SHA", "")
		t.Setenv("CI_PIPELINE_SOURCE", "merge_request_event")
		t.Setenv("CI_COMMIT_SHA", "gl456")
		assert.Equal(t, "gl456", runHeadSHA("gitlab"),
			"an empty source-branch SHA must not be reported as no head at all")
	})

	t.Run("gitlab reports no head off a merge request", func(t *testing.T) {
		t.Setenv("CI_MERGE_REQUEST_SOURCE_BRANCH_SHA", "")
		t.Setenv("CI_PIPELINE_SOURCE", "push")
		t.Setenv("CI_COMMIT_SHA", "branch-sha")
		assert.Empty(t, runHeadSHA("gitlab"),
			"a branch pipeline has no merge request head, and its commit sha is not one")
	})

	t.Run("github prefers the explicit PR_HEAD_SHA", func(t *testing.T) {
		t.Setenv("PR_HEAD_SHA", "gh123")
		assert.Equal(t, "gh123", runHeadSHA("github"))
	})

	t.Run("github falls back to the dispatched event payload", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "event.json")
		require.NoError(t, os.WriteFile(path,
			[]byte(`{"inputs":{"event_payload":"{\"pull_request\":{\"head\":{\"sha\":\"ev123\"}}}"}}`), 0o600))
		t.Setenv("PR_HEAD_SHA", "")
		t.Setenv("GITHUB_EVENT_PATH", path)
		assert.Equal(t, "ev123", runHeadSHA("github"))
	})

	t.Run("an issue run has no head", func(t *testing.T) {
		t.Setenv("PR_HEAD_SHA", "")
		t.Setenv("GITHUB_EVENT_PATH", "")
		assert.Empty(t, runHeadSHA("github"))
	})
}

func TestRunHeadSHA_NeverFallsBackToTheBaseSHA(t *testing.T) {
	// On pull_request_target GITHUB_SHA is the base branch. Presenting it as
	// the head would make an agent report a head move on every run.
	t.Setenv("PR_HEAD_SHA", "")
	t.Setenv("GITHUB_EVENT_PATH", "")
	t.Setenv("GITHUB_SHA", "base000")
	assert.Empty(t, runHeadSHA("github"))
}

func TestRunFactsKeysAreReserved(t *testing.T) {
	// Reserved so a harness env.sandbox entry cannot shadow the baseline the
	// agent's re-check depends on.
	assert.True(t, reservedSandboxKeys["FULLSEND_RUN_HEAD_SHA"])
	assert.True(t, reservedSandboxKeys["FULLSEND_RUN_STARTED_AT"])
}

// The order of .env is behaviour, not formatting: later entries win, so what
// a value outranks is decided by where it sits.
//
// Two runner-owned groups depend on that. The run facts must follow .env.d
// and env.sandbox, because reservedSandboxKeys stops an env.sandbox entry
// shadowing them but says nothing about a harness host_files file landing in
// .env.d. The iteration source line must be genuinely last, because the file
// it reads is rewritten before every iteration and carries the budget and
// deadline; anything emitted after it would be stale by an iteration.
//
// The whole order is asserted rather than the individual pairs: a change
// that moved iteration sourcing above .env.d would satisfy every pairwise
// claim about the run facts while silently letting a harness env file
// overwrite the budget.
func TestEnvScriptOrderIsPinned(t *testing.T) {
	h := &harness.Harness{
		Agent: "agents/test.md",
		Env:   &harness.EnvConfig{Sandbox: map[string]string{"SOME_VAR": "x"}},
	}
	lines := buildEnvScriptLines("", "/workspace/repo", h, nil,
		runFacts{headSHA: "abc123", startedAt: time.Now()})

	idx := func(substr string) int {
		t.Helper()
		for i, l := range lines {
			if strings.Contains(l, substr) {
				return i
			}
		}
		t.Fatalf("no line containing %q in:\n%s", substr, strings.Join(lines, "\n"))
		return -1
	}

	envD := idx("/.env.d/*.env")
	sandboxVar := idx("export SOME_VAR=")
	headSHA := idx("export FULLSEND_RUN_HEAD_SHA=")
	startedAt := idx("export FULLSEND_RUN_STARTED_AT=")
	iterSource := idx(iterationEnvFile)

	// .env.d → env.sandbox → both run facts → iteration source.
	assert.Greater(t, sandboxVar, envD, "env.sandbox must override a shared host_files .env")
	assert.Greater(t, headSHA, sandboxVar, "FULLSEND_RUN_HEAD_SHA must outrank env.sandbox and .env.d")
	assert.Greater(t, startedAt, sandboxVar, "FULLSEND_RUN_STARTED_AT must outrank env.sandbox and .env.d")
	assert.Greater(t, iterSource, headSHA, "the iteration file is sourced after the run facts")
	assert.Greater(t, iterSource, startedAt, "the iteration file is sourced after the run facts")

	// Last outright, not merely after the entries checked above: the file is
	// rewritten per iteration, so anything emitted later would be stale.
	require.NotEmpty(t, lines)
	assert.Equal(t, iterationEnvSourceLine(), lines[len(lines)-1],
		"the iteration source line must be the final entry in .env")
}
