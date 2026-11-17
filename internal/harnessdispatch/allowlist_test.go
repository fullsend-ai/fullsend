package harnessdispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
)

// TestListTriggeredHarnesses_AllowlistParityOverBaseEmptyList pins
// ListTriggeredHarnesses' own allowlist (the EffectiveAllowedResources call
// in enumerate.go): over a base layer's allowed_remote_resources: [] with a
// non-empty overlay list, the resolved allowlist must include fullsend's
// default prefixes, so an agent whose source is covered only by a default
// prefix is returned — the same list run/lint/lock resolve (AISDLC-512).
// Reverting that line to the raw cfg.AllowedResources() fails this test:
// the overlay-only list rejects the default-prefix source, the resolve
// error surfaces, and no harness is returned.
func TestListTriggeredHarnesses_AllowlistParityOverBaseEmptyList(t *testing.T) {
	const sha = "abcdef1234567890abcdef1234567890abcdef12"
	content := []byte(`agent: agents/review.md
role: review
slug: allowlist-parity
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
trigger: event.entity.kind == "work_item"
`)
	source := "https://raw.githubusercontent.com/fullsend-ai/fullsend/" + sha +
		"/harness/review.yaml#sha256=" + fetch.ComputeSHA256(content)

	// Production layout: the config dir sits inside the workspace root, and
	// ListTriggeredHarnesses resolves URL sources with WorkspaceRoot = the
	// config dir's parent.
	repo := t.TempDir()
	configDir := filepath.Join(repo, ".fullsend")
	require.NoError(t, os.MkdirAll(configDir, 0o755))

	// The overlay lists a prefix of its own; the base layer denies all, so
	// the agent source is covered only by a fullsend default prefix.
	overlay := "allowed_remote_resources:\n" +
		"  - https://overlay.example.com/\n" +
		"agents:\n" +
		"  - name: review\n" +
		fmt.Sprintf("    source: %q\n", source)
	require.NoError(t, os.WriteFile(filepath.Join(configDir, config.OverlayConfigFile), []byte(overlay), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(configDir, config.BaseConfigFile), []byte("allowed_remote_resources: []\n"), 0o644))

	// The harness content is pre-cached under the workspace root, so the
	// default-prefix URL resolves without the network. The harness's
	// agent: file resolves relative to the source URL, also under a
	// default prefix: cache it and record it in the URL index.
	require.NoError(t, fetch.CachePut(repo, source, content))
	agentURL := "https://raw.githubusercontent.com/fullsend-ai/fullsend/" + sha + "/agents/review.md"
	agentHash := fetch.ComputeSHA256([]byte("# review\n"))
	require.NoError(t, fetch.CachePut(repo, agentURL, []byte("# review\n")))
	indexBytes, err := json.MarshalIndent(map[string]string{agentURL: agentHash}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".fullsend-cache"), 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(repo, ".fullsend-cache", "url-index.json"), indexBytes, 0o600))

	cfg, err := config.LoadConfig(configDir, config.LoadOpts{MissingOK: false})
	require.NoError(t, err)

	out, err := ListTriggeredHarnesses(context.Background(), configDir, cfg, nil)
	require.NoError(t, err,
		"a default-prefix-only source over a base layer's [] must resolve like run/lint/lock")
	require.Len(t, out, 1)
	assert.Equal(t, "review", out[0].Name)
	assert.Equal(t, "allowlist-parity", out[0].Harness.Slug)
}
