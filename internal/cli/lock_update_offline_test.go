package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// TestRunLock_UpdateWarmCacheOfflineKeepsCachedHash checks that
// `fullsend lock <name> --update --offline` keeps working on the cache that
// wrote the lock: offline there is nothing to fetch, so --update re-pins
// from the URL index cache and keeps the cached hash instead of failing
// (the index bypass applies only when fetching is allowed).
func TestRunLock_UpdateWarmCacheOfflineKeepsCachedHash(t *testing.T) {
	agentV1 := []byte("agent v1")
	agentV2 := []byte("agent v2, changed upstream")
	s := newLockAdviceSetup(t, agentV1)
	require.NoError(t, s.runLock(t, false))

	// The file changes upstream, but offline --update cannot fetch it: the
	// warm cache's URL index entry re-pins the cached hash.
	s.files["/agents/triage.md"] = agentV2
	require.NoError(t, runLock(context.Background(), "code", s.dir, "", true, resolveFlags{offline: true}, ui.New(os.Stdout)))
	lf, err := lock.Load(filepath.Join(s.dir, "lock.yaml"))
	require.NoError(t, err)
	entry := lf.Lookup("code")
	require.NotNil(t, entry)
	dep := entry.LookupDep(s.agentURL)
	require.NotNil(t, dep)
	assert.Equal(t, fetch.ComputeSHA256(agentV1), dep.SHA256,
		"warm-cache --update --offline must re-pin from the cache, not fail")
}

// TestRunLock_UpdateColdCacheOfflineAdvisesNetwork pins the cold-cache arm
// of the offline miss advice under BypassURLIndex: with nothing cached for
// the harness's URL base but the base harness itself, `lock --update
// --offline` can neither re-resolve from the source (offline) nor re-pin
// from an index entry it never wrote, so the error says re-resolving
// requires network access and to rerun without --offline — never the lock
// command the user just ran. The warm-cache neighbour is pinned by
// TestRunLock_UpdateWarmCacheOfflineKeepsCachedHash above; the non-bypass
// miss (a plain offline lock) by the lock advice family.
func TestRunLock_UpdateColdCacheOfflineAdvisesNetwork(t *testing.T) {
	agentV1 := []byte("agent v1")
	s := newLockAdviceSetup(t, agentV1)

	// Seed only the base harness itself, so the offline update gets past
	// the base fetch and misses on the base's relative files: no cached
	// copy and no URL index entry for them.
	require.NoError(t, fetch.CachePut(s.dir, s.srv.URL+"/harness/base.yaml", s.files["/harness/base.yaml"]))

	err := runLock(context.Background(), "code", s.dir, "", true, resolveFlags{offline: true}, ui.New(os.Stdout))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in cache and offline mode is enabled")
	assert.Contains(t, err.Error(), "re-resolving from the source requires network access; rerun without --offline")
	// The advice does not name the command the user just ran.
	assert.NotContains(t, err.Error(), "fullsend lock")
}
