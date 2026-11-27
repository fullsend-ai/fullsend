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

// allAgentsSetup is the `lock --all` fixture over a hit-counting test
// server: two harnesses share one remote base, so a test can count how
// many times `lock --all --update` fetched each shared URL for the whole
// invocation.
type allAgentsSetup struct {
	dir      string
	srv      *httptest.Server
	files    map[string][]byte
	hits     map[string]int
	agentURL string
}

func newAllAgentsSetup(t *testing.T, agentContent []byte) *allAgentsSetup {
	t.Helper()
	baseContent := []byte("agent: agents/triage.md\npolicy: policies/sandbox.yaml\nrole: test\n")
	files := map[string][]byte{
		"/harness/base.yaml":     baseContent,
		"/agents/triage.md":      agentContent,
		"/policies/sandbox.yaml": []byte("version: 1\n"),
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
	// Two harnesses share one remote base: --all composes each of them,
	// so a shared base URL must be re-resolved once for the invocation,
	// not once per agent.
	for _, name := range []string{"code", "triage"} {
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, "harness", name+".yaml"),
			[]byte(fmt.Sprintf("role: test\nbase: %s/harness/base.yaml#sha256=%s\n",
				srv.URL, fetch.ComputeSHA256(baseContent))),
			0o644,
		))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(fmt.Sprintf(
		"allowed_remote_resources:\n  - \"%s/\"\n", srv.URL)), 0o644))

	return &allAgentsSetup{
		dir:      dir,
		srv:      srv,
		files:    files,
		hits:     hits,
		agentURL: srv.URL + "/agents/triage.md",
	}
}

// TestLockAll_UpdateFetchEachURLOnceAcrossAgents checks that
// `lock --all --update` on harnesses that share one base re-resolves each
// shared URL once for the whole invocation, not once per agent: the set of
// re-resolved index keys is created once per invocation (in runLockAll)
// and passed into every agent's lockOneAgent, so the second agent's
// compose passes take the index entries the first agent wrote — and both
// lock entries still record the changed upstream content.
func TestLockAll_UpdateFetchEachURLOnceAcrossAgents(t *testing.T) {
	agentV1 := []byte("agent v1")
	agentV2 := []byte("agent v2, changed upstream")
	s := newAllAgentsSetup(t, agentV1)
	require.NoError(t, runLockAll(context.Background(), s.dir, "", false, resolveFlags{}, ui.New(os.Stdout)))

	// The agent file changes upstream; the update must re-resolve it (not
	// serve the index's old copy) exactly once for both agents.
	s.files["/agents/triage.md"] = agentV2
	for k := range s.hits {
		delete(s.hits, k)
	}

	require.NoError(t, runLockAll(context.Background(), s.dir, "", true, resolveFlags{}, ui.New(os.Stdout)))

	assert.EqualValues(t, 1, s.hits["/agents/triage.md"],
		"--all --update must fetch the changed agent file once per invocation, not once per agent")
	assert.EqualValues(t, 1, s.hits["/policies/sandbox.yaml"],
		"--all --update must fetch each shared base file once per invocation, not once per agent")

	// Both agents' warm-cache updates record the upstream change.
	lf, err := lock.Load(filepath.Join(s.dir, "lock.yaml"))
	require.NoError(t, err)
	for _, name := range []string{"code", "triage"} {
		entry := lf.Lookup(name)
		require.NotNil(t, entry, "%s harness should be locked", name)
		dep := entry.LookupDep(s.agentURL)
		require.NotNil(t, dep, "%s harness should pin the shared agent file", name)
		assert.Equal(t, fetch.ComputeSHA256(agentV2), dep.SHA256,
			"warm-cache --all --update must record the upstream change for %s, not the URL index's old copy", name)
	}
}
