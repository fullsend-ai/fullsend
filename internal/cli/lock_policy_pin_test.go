package cli

import (
	"context"
	"fmt"
	"io"
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

// Tests for AISDLC-551: lock replay must refuse to run the content locked
// under another pin for a policy URL.
//
// lockFieldHolds compares a field's URL with the lock entry's URL after
// dropping the "#sha256=" fragment, so when an overlay pins `policy` to one
// revision and the lock records the same URL under another pin, replay used
// to run the locked revision's content and report success. `policy` is the
// only single field other than `agent` an overlay can set to a URL:
// validation refuses a URL pre_script, post_script and
// validation_loop.script everywhere.
//
// The harness under test carries two overlays over one policy URL: the
// lock-time overlay matches only without an event (`fullsend lock` runs with
// none) and pins the revision the server serves, so the lock records it; the
// run-time overlay matches only with an event and pins whatever the test
// asks, so the two pins can differ while the harness file — and so the lock
// entry — stays current.

// lockTestOverlayPolicyContent is the policy revision the lock-time overlay
// pins and the test server serves.
var lockTestOverlayPolicyContent = []byte("# locked policy revision\n")

// lockTestOverlayPolicyFixture is a locked harness whose policy comes from an
// overlay, plus the lock entry replay works from.
type lockTestOverlayPolicyFixture struct {
	dir           string
	entry         *lock.HarnessLock
	policyURL     string
	lockedPin     string
	lockedContent []byte
	allowlist     []string
}

// setupOverlayPolicyLock locks a harness whose policy URL is pinned by the
// lock-time overlay to the served revision, and returns the fixture replay
// runs against. runTimePin is the #sha256= pin the run-time overlay sets the
// same URL to: pass the locked pin for the matching case, another pin for
// the mismatch.
func setupOverlayPolicyLock(t *testing.T, runTimePin string) lockTestOverlayPolicyFixture {
	t.Helper()
	srv, policy := newLockTestServer(t, map[string][]byte{
		"/policies/p.yaml": lockTestOverlayPolicyContent,
	})
	lockedPin := fetch.ComputeSHA256(lockTestOverlayPolicyContent)
	policyURL := srv.URL + "/policies/p.yaml"

	dir := t.TempDir()
	writeTestFiles(t, dir, map[string]string{
		"agents/code.md": "# code agent\n",
		"harness/code.yaml": fmt.Sprintf(`agent: agents/code.md
role: test
allowed_remote_resources:
  - "%s/"
overlays:
  # The run-time overlay: matches only with an event, and pins runTimePin.
  - when: '"source" in event && event.source.system == "github"'
    policy: "%s#sha256=%s"
  # The lock-time overlay: fullsend lock runs without an event, so only this
  # one matches at lock time and the lock records the pin it pins.
  - when: '!("source" in event)'
    policy: "%s#sha256=%s"
`, srv.URL+"/", policyURL, runTimePin, policyURL, lockedPin),
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
	require.NotNil(t, dep, "the lock must record the overlay-set policy URL")
	require.Equal(t, lockedPin, dep.SHA256, "the lock records the revision the lock-time overlay pinned")

	return lockTestOverlayPolicyFixture{
		dir:           dir,
		entry:         entry,
		policyURL:     policyURL,
		lockedPin:     lockedPin,
		lockedContent: lockTestOverlayPolicyContent,
		allowlist:     []string{srv.URL + "/"},
	}
}

// loadLockedOverlayPolicyHarness loads the locked harness offline the way
// `fullsend run` does. A nil event composes the lock-time overlay; an event
// with a source composes the run-time one.
func loadLockedOverlayPolicyHarness(t *testing.T, fx lockTestOverlayPolicyFixture, event map[string]any) *harness.Harness {
	t.Helper()
	h, _, err := harness.LoadWithBase(context.Background(), filepath.Join(fx.dir, "harness", "code.yaml"), harness.ComposeOpts{
		WorkspaceRoot: fx.dir,
		FetchPolicy:   fetch.FetchPolicy{Offline: true},
		OrgAllowlist:  fx.allowlist,
		Event:         event,
	})
	require.NoError(t, err)
	require.NoError(t, h.ResolveRelativeTo(fx.dir))
	require.True(t, h.HasURLReferences(), "the composed harness must hold the overlay policy URL")
	return h
}

// TestResolveFromLock_PolicyPinMismatchFailsReplay covers ac_1: given an
// overlay-set policy URL whose #sha256= pin differs from the pin the lock
// records for that URL, replay fails with the "pinned to another revision
// than the lock file records" error, and the run resolves without the lock —
// the error leaves the harness holding the overlay's URL, so runAgent falls
// back to normal resolution with it. Without the check, lockFieldHolds drops
// the fragment, the URLs compare equal and replay silently runs the locked
// revision's content (the red run of this test on the base).
func TestResolveFromLock_PolicyPinMismatchFailsReplay(t *testing.T) {
	otherPin := fetch.ComputeSHA256([]byte("# another revision of the same policy\n"))
	fx := setupOverlayPolicyLock(t, otherPin)

	h := loadLockedOverlayPolicyHarness(t, fx, map[string]any{"source": map[string]any{"system": "github"}})
	require.Equal(t, fx.policyURL+"#sha256="+otherPin, h.Policy, "the run-time overlay must pin the other revision")

	_, err := resolveFromLock(h, fx.entry, "fullsend lock --update", fx.dir, fx.allowlist, ui.New(io.Discard))
	require.Error(t, err, "replay must refuse a policy URL pinned to another revision than the lock file records")
	assert.Contains(t, err.Error(), "pinned to another revision than the lock file records")
	assert.Equal(t, fx.policyURL+"#sha256="+otherPin, h.Policy,
		"the harness must be left holding the overlay's URL so the run resolves without the lock")
}

// TestResolveFromLock_PolicyPinMatchReplaysLockedContent covers ac_2: given
// the same URL and pin, replay is unchanged — the lock entry's cached
// content still replaces the URL, exactly as it did before the pin check.
func TestResolveFromLock_PolicyPinMatchReplaysLockedContent(t *testing.T) {
	fx := setupOverlayPolicyLock(t, fetch.ComputeSHA256(lockTestOverlayPolicyContent))

	h := loadLockedOverlayPolicyHarness(t, fx, map[string]any{"source": map[string]any{"system": "github"}})
	require.Equal(t, fx.policyURL+"#sha256="+fx.lockedPin, h.Policy, "the run-time overlay must pin the locked revision")

	result, err := resolveFromLock(h, fx.entry, "fullsend lock --update", fx.dir, fx.allowlist, ui.New(io.Discard))
	require.NoError(t, err, "the same URL and pin must replay unchanged")
	assert.False(t, harness.IsURL(h.Policy), "the matching pin must still resolve to the cached path")
	got, err := os.ReadFile(h.Policy)
	require.NoError(t, err)
	assert.Equal(t, fx.lockedContent, got)
	assert.NotEmpty(t, result.Deps, "the replay still reports the resolved dependency")
}
