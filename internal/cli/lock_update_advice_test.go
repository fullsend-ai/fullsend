package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// lockAdviceSetup is a child harness over a URL base whose base names a
// relative agent file, policy and skip pre-script, all served by one test
// server the test can still mutate (upstream changes become visible on the
// next fetch). The child is locked once by the test that builds it.
type lockAdviceSetup struct {
	dir         string
	harnessPath string
	srv         *httptest.Server
	files       map[string][]byte
	agentURL    string
}

func newLockAdviceSetup(t *testing.T, agentContent []byte) *lockAdviceSetup {
	t.Helper()
	skip := []byte("#!/bin/bash\necho \"skipped=true\" >> \"${FULLSEND_PRESCRIPT_OUTPUT}\"\n")
	baseContent := []byte("agent: agents/triage.md\npolicy: policies/sandbox.yaml\nrole: test\npre_script: scripts/pre-code.sh\n")
	files := map[string][]byte{
		"/harness/base.yaml":     baseContent,
		"/agents/triage.md":      agentContent,
		"/policies/sandbox.yaml": []byte("version: 1\n"),
		"/scripts/pre-code.sh":   skip,
	}
	srv, policy := newLockTestServer(t, files)

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	harnessPath := filepath.Join(dir, "harness", "code.yaml")
	require.NoError(t, os.WriteFile(harnessPath, []byte(fmt.Sprintf(
		"role: test\nbase: %s/harness/base.yaml#sha256=%s\n", srv.URL, fetch.ComputeSHA256(baseContent))), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(fmt.Sprintf(
		"agents:\n  - harness/code.yaml\nallowed_remote_resources:\n  - \"%s/\"\n", srv.URL)), 0o644))

	fetch.DefaultPolicy = policy
	t.Cleanup(func() { fetch.DefaultPolicy = fetch.FetchPolicy{} })

	return &lockAdviceSetup{
		dir:         dir,
		harnessPath: harnessPath,
		srv:         srv,
		files:       files,
		agentURL:    srv.URL + "/agents/triage.md",
	}
}

// runLock locks (or, with update, re-locks) the setup's "code" harness.
func (s *lockAdviceSetup) runLock(t *testing.T, update bool) error {
	t.Helper()
	return runLock(context.Background(), "code", s.dir, "", update, resolveFlags{}, ui.New(os.Stdout))
}

// runAgent runs the setup's "code" harness to completion.
func (s *lockAdviceSetup) runAgent(t *testing.T, offline bool) error {
	t.Helper()
	var out bytes.Buffer
	return runAgent(context.Background(), "code", s.dir, "", t.TempDir(), "", nil, false, "", "", "",
		resolveFlags{maxDepth: 10, maxResources: 50, offline: offline}, statusOpts{}, ui.New(&out), false, runOverrideFlags{})
}

// dropCachedContent removes the cached copy of hash, as a pruned cache or a
// fresh machine leaves a lock's pin without its content.
func dropCachedContent(t *testing.T, workspaceRoot, hash string) {
	t.Helper()
	cachePath, err := fetch.CachePath(workspaceRoot, hash)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(cachePath))
}

// TestRunAgent_PinMismatchAdviceNamesHarness checks that a pin-mismatch
// error advises a command that names the harness (`fullsend lock <name>
// --update`, with `--fullsend-dir` when the dir is not the default) and that
// the advised command succeeds as written (AISDLC-550 ac_1).
func TestRunAgent_PinMismatchAdviceNamesHarness(t *testing.T) {
	usePreScriptStub(t)
	agentV1 := []byte("agent v1")
	s := newLockAdviceSetup(t, agentV1)
	require.NoError(t, s.runLock(t, false))

	// The file changes upstream and the pinned content leaves the cache, so
	// composing online fetches content that does not match the pin.
	s.files["/agents/triage.md"] = []byte("agent v2, changed upstream")
	dropCachedContent(t, s.dir, fetch.ComputeSHA256(agentV1))

	err := s.runAgent(t, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fullsend lock code --update")
	assert.Contains(t, err.Error(), "--fullsend-dir")

	// The advised command, run as written, re-pins and the run composes.
	require.NoError(t, s.runLock(t, true))
	require.NoError(t, s.runAgent(t, false))
}

// TestRunAgent_OfflineMissAdviceNamesHarness checks that an offline-miss
// error (pinned content not in cache, offline mode) advises the same named
// command (AISDLC-550 ac_1).
func TestRunAgent_OfflineMissAdviceNamesHarness(t *testing.T) {
	usePreScriptStub(t)
	agentV1 := []byte("agent v1")
	s := newLockAdviceSetup(t, agentV1)
	require.NoError(t, s.runLock(t, false))

	dropCachedContent(t, s.dir, fetch.ComputeSHA256(agentV1))

	err := s.runAgent(t, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fullsend lock code --update")
	assert.Contains(t, err.Error(), "--fullsend-dir")
}

// TestRunLock_UpdateWarmCacheRecordsUpstreamChange checks that on the cache
// that wrote the lock, `fullsend lock <name> --update` records a URL base's
// relative file that changed upstream (the URL index does not serve the old
// copy for --update), or the message says to delete the index first
// (AISDLC-550 ac_2).
func TestRunLock_UpdateWarmCacheRecordsUpstreamChange(t *testing.T) {
	agentV1 := []byte("agent v1")
	agentV2 := []byte("agent v2, changed upstream")
	s := newLockAdviceSetup(t, agentV1)
	require.NoError(t, s.runLock(t, false))

	s.files["/agents/triage.md"] = agentV2

	err := s.runLock(t, true)
	if err == nil {
		lf, loadErr := lock.Load(filepath.Join(s.dir, "lock.yaml"))
		require.NoError(t, loadErr)
		entry := lf.Lookup("code")
		require.NotNil(t, entry)
		dep := entry.LookupDep(s.agentURL)
		require.NotNil(t, dep)
		assert.Equal(t, fetch.ComputeSHA256(agentV2), dep.SHA256,
			"warm-cache --update must record the upstream change, not the URL index's old copy")

		// The re-pinned lock composes offline with the new content.
		pins := runLockPins(t, s.dir, "code", s.harnessPath)
		h, _, composeErr := harness.LoadWithBase(context.Background(), s.harnessPath, harness.ComposeOpts{
			WorkspaceRoot: s.dir,
			FetchPolicy:   fetch.FetchPolicy{Offline: true},
			OrgAllowlist:  []string{s.srv.URL + "/"},
			LockedPins:    pins,
		})
		require.NoError(t, composeErr)
		agentData, readErr := os.ReadFile(h.Agent)
		require.NoError(t, readErr)
		assert.Equal(t, string(agentV2), string(agentData))
		return
	}
	// The criterion's other arm: the message says to delete the index first.
	assert.Contains(t, err.Error(), "index")
}
