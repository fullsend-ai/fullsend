package cli

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

// A rotation whose webhook fast path is deferred leaves a ready replacement,
// a pending retirement, and no managed webhook. The cutover step must retire
// the previous generation instead of failing on the unprovisioned webhook.
func TestFinishGitLabTriggerAccountRetirement_WebhookDeferredWithoutManagedHook(t *testing.T) {
	e := newTAHEnv(t)
	old := e.installReady()
	oldToken := e.triggerToken(old.TriggerTokenID)

	// The project has no webhooks at all (provisioning is deferred).
	e.s.fallback = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/hooks") {
			tahJSON(w, http.StatusOK, []any{})
			return true
		}
		return false
	}

	e.s.takeEvents()
	require.NoError(t, maybeConvergeGitLabTriggerAccount(context.Background(), &reposInstallConfig{}, e.client, ui.New(e.out), "g", "p", true, true, nil))
	cur := e.ledger()
	require.Len(t, cur.Retired, 1, "the previous generation waits for the webhook cutover")
	assert.Contains(t, e.s.triggerIDs(), old.TriggerTokenID, "the old trigger stays usable until retirement")

	require.NoError(t, finishGitLabTriggerAccountRetirement(context.Background(), &reposInstallConfig{}, e.client, "g", "p"))
	cur = e.requireReplacement(old, oldToken, e.s.takeEvents())
	assert.Empty(t, cur.Retired)
	e.requireLeaseReleased()
}
