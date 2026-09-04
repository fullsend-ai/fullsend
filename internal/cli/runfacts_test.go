package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	t.Run("gitlab reads the merge request source sha", func(t *testing.T) {
		t.Setenv("CI_MERGE_REQUEST_SOURCE_BRANCH_SHA", "gl123")
		assert.Equal(t, "gl123", runHeadSHA("gitlab"))
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
