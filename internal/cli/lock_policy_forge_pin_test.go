package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// The forge variant of the AISDLC-551 policy pin check.
//
// runLock locks one harness per forge platform (sorted, so github before
// gitlab) and keys lock dependencies by URL alone, keeping the first entry
// it sees: forge.github.policy = URL#sha256=A and forge.gitlab.policy =
// URL#sha256=B leave the lock recording only A. A gitlab run composes
// h.Policy = URL#sha256=B, so replay must refuse it exactly like an overlay
// mismatch — and the error must name the pin the lock records, because
// re-locking records A again and never clears the fallback. The URL-only
// dedup gap itself (keying lock deps by URL plus pin, or per forge
// platform) is AISDLC-563's to fix.

// TestResolveFromLock_ForgePolicyPinMismatchFailsReplay covers the
// forge.<platform>.policy shape of the pin mismatch: given two forge
// variants pinning one policy URL to different revisions, the lock records
// only the first platform's pin, and a run on the other platform fails
// replay with the "pinned to another revision than the lock file records"
// error naming the lock's pin, leaving the harness holding the run's URL so
// it resolves without the lock.
func TestResolveFromLock_ForgePolicyPinMismatchFailsReplay(t *testing.T) {
	lockedContent := []byte("# locked policy revision (github pin)\n")
	otherContent := []byte("# another revision of the same policy (gitlab pin)\n")
	srv, policy := newLockTestServer(t, map[string][]byte{
		"/policies/p.yaml": lockedContent,
	})
	lockedPin := fetch.ComputeSHA256(lockedContent)
	otherPin := fetch.ComputeSHA256(otherContent)
	policyURL := srv.URL + "/policies/p.yaml"

	dir := t.TempDir()
	// The gitlab pin's content must already be in the cache before the
	// lock: the lock's gitlab pass resolves the same URL under the gitlab
	// pin, and fetching it would fail the integrity check against the
	// revision the server serves (the github one). A cache hit skips that
	// check — the shape of a URL whose content changed between locks.
	require.NoError(t, fetch.CachePut(dir, policyURL, otherContent))

	writeTestFiles(t, dir, map[string]string{
		"agents/code.md": "# code agent\n",
		"harness/code.yaml": fmt.Sprintf(`agent: agents/code.md
role: test
allowed_remote_resources:
  - "%s/"
forge:
  github:
    policy: "%s#sha256=%s"
  gitlab:
    policy: "%s#sha256=%s"
`, srv.URL+"/", policyURL, lockedPin, policyURL, otherPin),
		"config.yaml": fmt.Sprintf("allowed_remote_resources:\n  - %q\n", srv.URL+"/"),
	})

	printer := ui.New(io.Discard)
	fetch.DefaultPolicy = policy
	t.Cleanup(func() { fetch.DefaultPolicy = fetch.FetchPolicy{} })
	require.NoError(t, runLock(context.Background(), "code", dir, "", false, resolveFlags{}, printer))

	lf, err := lock.Load(filepath.Join(dir, "lock.yaml"))
	require.NoError(t, err)
	entry := lf.Lookup("code")
	require.NotNil(t, entry)
	dep := entry.LookupDep(policyURL)
	require.NotNil(t, dep, "the lock must record the forge-set policy URL")
	require.Equal(t, lockedPin, dep.SHA256,
		"the lock keeps the first platform's pin (github sorts before gitlab); the gitlab pin is collapsed by URL")

	// A gitlab run composes the gitlab variant of the policy offline.
	h, _, err := harness.LoadWithBase(context.Background(), filepath.Join(dir, "harness", "code.yaml"), harness.ComposeOpts{
		WorkspaceRoot: dir,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
		OrgAllowlist:  []string{srv.URL + "/"},
		ForgePlatform: "gitlab",
	})
	require.NoError(t, err)
	require.NoError(t, h.ResolveRelativeTo(dir))
	require.Equal(t, policyURL+"#sha256="+otherPin, h.Policy, "the gitlab forge policy must be composed with its own pin")
	require.True(t, h.HasURLReferences(), "the composed harness must hold the gitlab policy URL")

	_, err = resolveFromLock(h, entry, dir, []string{srv.URL + "/"}, ui.New(io.Discard))
	require.Error(t, err, "replay must refuse a forge policy URL pinned to another revision than the lock records")
	assert.Contains(t, err.Error(), "pinned to another revision than the lock file records")
	assert.Contains(t, err.Error(), "the lock records sha256="+lockedPin,
		"the error must name the pin the lock holds, so re-locking is visibly not the remedy")
	assert.Equal(t, policyURL+"#sha256="+otherPin, h.Policy,
		"the harness must be left holding the gitlab pin's URL so the run resolves without the lock")
}
