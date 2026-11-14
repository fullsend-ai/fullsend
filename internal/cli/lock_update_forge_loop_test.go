package cli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// forgeLoopSetup is the lock-advice fixture over a hit-counting test
// server, with a child whose forge: block declares two platforms: lock
// composes it once per platform (github, then gitlab), so a test can
// count how many times --update fetched each shared URL for the whole
// invocation.
type forgeLoopSetup struct {
	dir         string
	harnessPath string
	srv         *httptest.Server
	files       map[string][]byte
	hits        map[string]int
	agentURL    string
}

func newForgeLoopSetup(t *testing.T, agentContent []byte) *forgeLoopSetup {
	t.Helper()
	skip := []byte("#!/bin/bash\necho \"skipped=true\" >> \"${FULLSEND_PRESCRIPT_OUTPUT}\"\n")
	baseContent := []byte("agent: agents/triage.md\npolicy: policies/sandbox.yaml\nrole: test\npre_script: scripts/pre-code.sh\n")
	files := map[string][]byte{
		"/harness/base.yaml":     baseContent,
		"/agents/triage.md":      agentContent,
		"/policies/sandbox.yaml": []byte("version: 1\n"),
		"/scripts/pre-code.sh":   skip,
	}
	hits := make(map[string]int)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		if data, ok := files[r.URL.Path]; ok {
			w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	hostPort := strings.TrimPrefix(srv.URL, "https://")
	hostname, port, _ := net.SplitHostPort(hostPort)
	tlsCfg := srv.TLS.Clone()
	tlsCfg.InsecureSkipVerify = true
	policy := fetch.NewTestPolicy(tlsCfg, []string{hostname}, []string{port})
	fetch.DefaultPolicy = policy
	t.Cleanup(func() { fetch.DefaultPolicy = fetch.FetchPolicy{} })

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	harnessPath := filepath.Join(dir, "harness", "code.yaml")
	require.NoError(t, os.WriteFile(harnessPath, []byte(fmt.Sprintf(
		"role: test\nbase: %s/harness/base.yaml#sha256=%s\nforge:\n  github:\n    runner_env:\n      FORGE: github\n  gitlab:\n    runner_env:\n      FORGE: gitlab\n",
		srv.URL, fetch.ComputeSHA256(baseContent))), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(fmt.Sprintf(
		"agents:\n  - harness/code.yaml\nallowed_remote_resources:\n  - \"%s/\"\n", srv.URL)), 0o644))

	return &forgeLoopSetup{
		dir:         dir,
		harnessPath: harnessPath,
		srv:         srv,
		files:       files,
		hits:        hits,
		agentURL:    srv.URL + "/agents/triage.md",
	}
}

// runLock locks (or, with update, re-locks) the setup's two-forge "code"
// harness for every platform its forge: block declares.
func (s *forgeLoopSetup) runLock(t *testing.T, update bool) error {
	t.Helper()
	return runLock(context.Background(), "code", s.dir, "", update, resolveFlags{}, ui.New(os.Stdout))
}

// TestRunLock_UpdateTwoForgesFetchEachURLOnce checks that `lock --update`
// on a child whose forge: block declares two platforms re-resolves each
// shared URL once for the whole invocation, not once per forge platform:
// the per-invocation set of re-resolved index keys is shared through
// ComposeOpts across the per-forge compose loop, so the second platform's
// pass takes the index entries the first pass wrote — and the lock still
// records the changed upstream content.
func TestRunLock_UpdateTwoForgesFetchEachURLOnce(t *testing.T) {
	agentV1 := []byte("agent v1")
	agentV2 := []byte("agent v2, changed upstream")
	s := newForgeLoopSetup(t, agentV1)
	require.NoError(t, s.runLock(t, false))

	// The agent file changes upstream; the update must re-resolve it (not
	// serve the index's old copy) exactly once for both forge platforms.
	s.files["/agents/triage.md"] = agentV2
	for k := range s.hits {
		delete(s.hits, k)
	}

	require.NoError(t, s.runLock(t, true))

	assert.EqualValues(t, 1, s.hits["/agents/triage.md"],
		"--update must fetch the changed agent file once per invocation, not once per forge platform")
	assert.EqualValues(t, 1, s.hits["/policies/sandbox.yaml"],
		"--update must fetch each shared base file once per invocation, not once per forge platform")

	// The warm-cache update records the upstream change.
	lf, err := lock.Load(filepath.Join(s.dir, "lock.yaml"))
	require.NoError(t, err)
	entry := lf.Lookup("code")
	require.NotNil(t, entry)
	dep := entry.LookupDep(s.agentURL)
	require.NotNil(t, dep)
	assert.Equal(t, fetch.ComputeSHA256(agentV2), dep.SHA256,
		"warm-cache --update must record the upstream change, not the URL index's old copy")
}
