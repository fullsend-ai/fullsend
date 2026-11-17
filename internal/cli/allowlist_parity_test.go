package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/harness"
)

// TestAllowlistResolutionParity_LayeredShapes reproduces AISDLC-512: the
// run/lint/lock family and the dispatch family resolve the config-level
// allowed_remote_resources through different loaders and disagree over a
// non-empty config.yaml list on top of a base layer's empty list
// (allowed_remote_resources: []). loadLockConfig (internal/cli/lock.go,
// also used by lint) applies config.EnsureDefaultAllowedRemoteResources
// over the layered chain, so fullsend's default prefixes are appended and
// `fullsend run`, `lint` and `lock` admit an agent whose source is covered
// only by a default prefix. Harness dispatch, `fullsend dispatch --route`
// and the Jira poll load the config with config.LoadConfig and register
// agents through harness.RegisteredAgents (internal/harness/registry.go),
// which validates against the raw layered AllowedResources() — defaults
// only when no list is set — so the same agent is rejected there.
//
// For every layered shape, one probe agent per prefix class
// (overlay-listed, base-listed, default-prefix-only, unlisted) must be
// admitted or denied identically by both command families.
func TestAllowlistResolutionParity_LayeredShapes(t *testing.T) {
	const (
		overlayPrefix = "https://overlay.example.com/"
		basePrefix    = "https://base.example.com/"
	)
	defaultPrefix := config.DefaultAllowedRemoteResources()[0]
	integrity := "#sha256=" + strings.Repeat("a", 64)

	probes := []struct {
		name   string
		source string
	}{
		{"overlay-listed prefix", overlayPrefix + "harness/probe.yaml" + integrity},
		{"base-listed prefix", basePrefix + "harness/probe.yaml" + integrity},
		{"default-prefix only", defaultPrefix + "harness/probe.yaml" + integrity},
		{"unlisted prefix", "https://unlisted.example.com/harness/probe.yaml" + integrity},
	}

	shapes := []struct {
		name    string
		base    string // config.base.yaml contents; empty means the file is absent
		overlay string // config.yaml allowlist section
	}{
		{"non-empty list over base empty list",
			"allowed_remote_resources: []\n",
			"allowed_remote_resources:\n  - " + overlayPrefix + "\n"},
		{"list alone",
			"",
			"allowed_remote_resources:\n  - " + overlayPrefix + "\n"},
		{"list over list",
			"allowed_remote_resources:\n  - " + basePrefix + "\n",
			"allowed_remote_resources:\n  - " + overlayPrefix + "\n"},
		{"omitted", "", "roles: [coder]\n"},
		{"top-level empty list", "", "allowed_remote_resources: []\n"},
		{"omitted over base empty list",
			"allowed_remote_resources: []\n",
			"roles: [coder]\n"},
	}

	for _, shape := range shapes {
		for _, probe := range probes {
			t.Run(shape.name+" / "+probe.name, func(t *testing.T) {
				dir := t.TempDir()
				overlay := shape.overlay +
					fmt.Sprintf("agents:\n  - name: probe\n    source: %q\n", probe.source)
				require.NoError(t, os.WriteFile(
					filepath.Join(dir, config.OverlayConfigFile), []byte(overlay), 0o644))
				if shape.base != "" {
					require.NoError(t, os.WriteFile(
						filepath.Join(dir, config.BaseConfigFile), []byte(shape.base), 0o644))
				}

				// The run/lint/lock family: loadLockConfig returns the
				// layered chain with EnsureDefaultAllowedRemoteResources
				// applied, and fullsend run/lint/lock validate agent
				// entries against that writer's list.
				runCfg, err := loadLockConfig(filepath.Join(dir, config.OverlayConfigFile))
				require.NoError(t, err)
				require.NotNil(t, runCfg)
				runErr := config.ValidateAgentEntries(runCfg.AgentEntries(), runCfg.AllowedResources())

				// The dispatch family: config.LoadConfig →
				// harness.RegisteredAgents, the path harness dispatch,
				// `fullsend dispatch --route` and the Jira poll take.
				cfg, err := config.LoadConfig(dir, config.LoadOpts{MissingOK: true})
				require.NoError(t, err)
				_, dispatchErr := harness.RegisteredAgents(cfg)

				assert.Equal(t, runErr == nil, dispatchErr == nil,
					"run/lint/lock and dispatch resolve the config-level allowlist differently for probe %q: run/lint/lock err: %v; dispatch err: %v",
					probe.source, runErr, dispatchErr)
			})
		}
	}
}
