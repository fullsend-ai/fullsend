package harnessdispatch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// ---------------------------------------------------------------------------
// ListTriggeredHarnesses with no event, guarded overlay not dropped
//
// Given ListTriggeredHarnesses with no event, When a guarded overlay would
// error, Then the harness is still enumerated (not dropped).
// ---------------------------------------------------------------------------

func TestListTriggeredHarnesses_NoEvent_GuardedOverlayNotDropped(t *testing.T) {
	dir := t.TempDir()

	harnessYAML := `agent: agents/triage.md
role: triage
slug: guarded-overlay
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
trigger: 'event.entity.kind == "pr"'
overlays:
  - when: 'event.source.system == "github"'
    image: ghcr.io/fullsend-ai/fullsend-sandbox-gh:latest
`
	writeHarnessConfig(t, dir, harnessYAML)
	dirCfg, err := config.LoadConfig(dir, config.LoadOpts{MissingOK: false})
	require.NoError(t, err)

	// ListTriggeredHarnesses is called without an event (dispatch enumeration).
	// The overlay's when: references event.source.system which will error
	// because there is no event. The harness must still appear in the list.
	out, err := ListTriggeredHarnesses(context.Background(), dir, dirCfg, nil)
	require.NoError(t, err, "enumeration must not fail because an overlay would error without event")
	require.Len(t, out, 1, "harness with guarded overlay must still be enumerated")
	assert.Equal(t, "issue-ping", out[0].Name)
}

func TestListTriggeredHarnesses_NoEvent_GuardedOverlayFieldNotApplied(t *testing.T) {
	dir := t.TempDir()

	harnessYAML := `agent: agents/triage.md
role: triage
slug: guarded-overlay-field
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
trigger: 'event.entity.kind == "pr"'
overlays:
  - when: 'event.source.system == "github"'
    model: gpt-4-overlay
`
	writeHarnessConfig(t, dir, harnessYAML)
	dirCfg, err := config.LoadConfig(dir, config.LoadOpts{MissingOK: false})
	require.NoError(t, err)

	out, err := ListTriggeredHarnesses(context.Background(), dir, dirCfg, nil)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "opus", out[0].Harness.Model,
		"overlay must not match without event (non-match); original model preserved")
}
