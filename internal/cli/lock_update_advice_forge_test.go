package cli

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// runLockForge locks (or, with update, re-locks) the setup's "code" harness
// for one forge platform.
func (s *lockAdviceSetup) runLockForge(t *testing.T, forgeFlag string, update bool) error {
	t.Helper()
	return runLock(context.Background(), "code", s.dir, forgeFlag, update, resolveFlags{}, ui.New(os.Stdout))
}

// runAgentForge runs the setup's "code" harness to completion under a forge
// platform.
func (s *lockAdviceSetup) runAgentForge(t *testing.T, forgeFlag string) error {
	t.Helper()
	var out bytes.Buffer
	return runAgent(context.Background(), "code", s.dir, "", t.TempDir(), "", nil, false, "", forgeFlag, "",
		resolveFlags{maxDepth: 10, maxResources: 50}, statusOpts{}, ui.New(&out), false, runOverrideFlags{})
}

// TestRunAgent_PinMismatchAdviceNamesForge checks that a pin-mismatch error
// under a forge platform advises a re-lock command that names the platform
// for a child harness with no forge: block — without --forge, lock composes
// with platform "" and drops the forge-specific base files (#8988) — and
// that the advised command, run as written, re-pins and the run composes.
func TestRunAgent_PinMismatchAdviceNamesForge(t *testing.T) {
	usePreScriptStub(t)
	agentV1 := []byte("agent v1")
	s := newLockAdviceSetup(t, agentV1)
	require.NoError(t, s.runLockForge(t, "github", false))

	// The file changes upstream and the pinned content leaves the cache, so
	// composing online fetches content that does not match the pin.
	s.files["/agents/triage.md"] = []byte("agent v2, changed upstream")
	dropCachedContent(t, s.dir, fetch.ComputeSHA256(agentV1))

	err := s.runAgentForge(t, "github")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fullsend lock code --update")
	assert.Contains(t, err.Error(), "--fullsend-dir")
	assert.Contains(t, err.Error(), "--forge github")

	// The advised command, run as written, re-pins and the run composes.
	require.NoError(t, s.runLockForge(t, "github", true))
	require.NoError(t, s.runAgentForge(t, "github"))
}
