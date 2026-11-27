package cli

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
)

// TestLintCommand_OfflineMissAdviceNamesHarness pins lint's wiring of the
// shared lock-update hint — the one lockUpdateHint call without a test
// naming its arguments (run's copy is guarded by
// TestRunAgent_LockReplayCacheMissAdviceNamesHarness, lock's by the lock
// advice family): lint builds agentOpts.LockUpdateCommand with
// lockUpdateHint(agentName, fullsendDir, harnessPath, forgePlatform, true)
// at lint.go, so its harness-failure advice names the full command. A
// stale fullsendDir or a swapped value in lint's wiring would ship
// silently everywhere else and fail here.
//
// Lint composes without the lock's pins (it replays no lock entry), so the
// advice arm it reaches is the offline miss: the child harness is locked
// under a non-default --fullsend-dir, the pinned base file's cached copy
// leaves the cache (a pruned cache or a fresh machine), and an offline
// lint reports the miss advising `fullsend lock code --update
// --fullsend-dir '<dir>'` exactly as the shared hint builds it.
func TestLintCommand_OfflineMissAdviceNamesHarness(t *testing.T) {
	// detectForgePlatform falls back to CI env vars; clear them so the
	// forge-less fixture child really composes without a platform (and so
	// lockForgeArg adds no --forge), even when the tests themselves run
	// in CI.
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GITLAB_CI", "")
	stubLintGitHubToken(t, "test-token")
	agentV1 := []byte("agent v1")
	s := newLockAdviceSetup(t, agentV1)
	require.NoError(t, s.runLock(t, false))

	// The pinned content leaves the cache; offline, lint cannot fetch it
	// and reports the miss with the named advice.
	dropCachedContent(t, s.dir, fetch.ComputeSHA256(agentV1))

	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"--fullsend-dir", s.dir, "--offline"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in cache and offline mode is enabled")
	assert.Contains(t, err.Error(), fmt.Sprintf("run fullsend lock code --update --fullsend-dir '%s' first", s.dir))
}
