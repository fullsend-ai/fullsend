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

// TestAllowlistExpectedOutcomes_LayeredShapes pins what the two command
// families must agree ON, not just that they agree
// (TestAllowlistResolutionParity_LayeredShapes asserts the run/lint/lock
// family and the dispatch family admit and deny the same probes; this
// companion asserts the expected admit/deny result per (shape, probe)):
// an unlisted prefix is always denied, an explicit [] as the resolved
// effective value denies every probe, and the non-empty shapes admit the
// overlay-listed, base-listed and default-prefix probes they should. A
// later change that made both families reject the overlay-listed or
// default-prefix probe, or admit the unlisted one, fails here instead of
// passing as mere agreement.
func TestAllowlistExpectedOutcomes_LayeredShapes(t *testing.T) {
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
		want    map[string]bool
	}{
		{"non-empty list over base empty list",
			"allowed_remote_resources: []\n",
			"allowed_remote_resources:\n  - " + overlayPrefix + "\n",
			map[string]bool{"overlay-listed prefix": true, "base-listed prefix": false, "default-prefix only": true, "unlisted prefix": false}},
		{"list alone",
			"",
			"allowed_remote_resources:\n  - " + overlayPrefix + "\n",
			map[string]bool{"overlay-listed prefix": true, "base-listed prefix": false, "default-prefix only": true, "unlisted prefix": false}},
		{"list over list",
			"allowed_remote_resources:\n  - " + basePrefix + "\n",
			"allowed_remote_resources:\n  - " + overlayPrefix + "\n",
			map[string]bool{"overlay-listed prefix": true, "base-listed prefix": true, "default-prefix only": true, "unlisted prefix": false}},
		{"omitted", "", "roles: [coder]\n",
			map[string]bool{"overlay-listed prefix": false, "base-listed prefix": false, "default-prefix only": true, "unlisted prefix": false}},
		{"top-level empty list", "", "allowed_remote_resources: []\n",
			map[string]bool{"overlay-listed prefix": false, "base-listed prefix": false, "default-prefix only": false, "unlisted prefix": false}},
		{"omitted over base empty list",
			"allowed_remote_resources: []\n",
			"roles: [coder]\n",
			map[string]bool{"overlay-listed prefix": false, "base-listed prefix": false, "default-prefix only": false, "unlisted prefix": false}},
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

				want := shape.want[probe.name]
				assert.Equal(t, want, runErr == nil,
					"run/lint/lock must %s this probe, err: %v", wantVerb(want), runErr)
				assert.Equal(t, want, dispatchErr == nil,
					"dispatch must %s this probe, err: %v", wantVerb(want), dispatchErr)
			})
		}
	}
}

// wantVerb names the expected outcome for failure messages.
func wantVerb(admit bool) string {
	if admit {
		return "admit"
	}
	return "deny"
}
