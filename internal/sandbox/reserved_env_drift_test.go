package sandbox

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

// TestReservedCredentialKeys_ReservedForPluginEnv keeps two deny-lists
// that guard the same processes from drifting apart.
//
// reservedCredentialKeys refuses a name as a provider *credential* key,
// because openshell exports credentials into its child's environment.
// harness.PluginSpec.Env is a second door into that same environment:
// the pi runtime exports it right before pi starts and pi hands its whole
// environment to every hook script it spawns. A name dangerous enough to
// refuse on one path is dangerous on the other.
//
// The lists cannot be one variable — this package imports internal/harness,
// so the dependency only runs one way — hence this test. It asserts the
// direction that matters: everything the credential list refuses, the
// plugin-env list refuses too. The plugin list is deliberately the
// broader of the two (whole vendor families, every *_TOKEN), so the
// converse is not asserted.
func TestReservedCredentialKeys_ReservedForPluginEnv(t *testing.T) {
	t.Parallel()
	require.NotEmpty(t, reservedCredentialKeys)
	for key := range reservedCredentialKeys {
		t.Run(key, func(t *testing.T) {
			h := harness.Harness{
				Role:    "code",
				Agent:   "agents/code.md",
				Plugins: []harness.PluginSpec{{Path: "extensions/x", Env: map[string]string{key: "v"}}},
			}
			err := h.Validate()
			require.Errorf(t, err, "%q is a reserved credential key but is allowed as plugin env; add it to reservedPluginEnvNames or a prefix in internal/harness/plugin_spec.go", key)
			assert.Contains(t, err.Error(), `env key "`+key+`" is reserved`)
		})
	}
}
