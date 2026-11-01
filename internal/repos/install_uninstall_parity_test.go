package repos

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Install writes repository variables and secrets from one set of builders;
// uninstall deletes them from separate, hand-maintained lists. Nothing ties
// the two together, so a name added to the install path can be left behind on
// every repository that later uninstalls. That has already happened once: four
// GitLab variables shipped without matching cleanup entries (#5877).
//
// The assertion is deliberately one-way. Everything install writes must be
// deleted on uninstall; the reverse is false by design and must not be
// asserted. The uninstall lists intentionally carry names install never
// writes, so that repositories installed by earlier versions are still cleaned
// up: the superseded poller variables in gitlabRetiredLegacyVars, the
// forge.VarLegacy* names, and the GitLab role-credential secrets. Two further
// omissions are deliberate and documented at their definitions —
// gitlabUninstallVars excludes the legacy shared forge token, and
// gitlabUninstallSecrets excludes the unprefixed OPENAI_API_KEY because other
// jobs in the same project may depend on it.
//
// One consequence worth stating: the secret check does not catch a name added
// to inferenceSecretsForAuth that installSecretsForForge never writes. Only
// uninstall derives from managedInferenceSecrets; install names its secrets
// directly, so the two sides must both be updated by hand.

// installSideExtras names repository values install provisions outside the
// variable builders, which the parity check therefore cannot discover on its
// own. GitLab's FULLSEND_DISPATCH_SECRET is written by seedGitLabPollState
// (install.go, GitLab branch only) through poll.EnsureDispatchSecret, and is
// cleaned up from gitlabUninstallVars.
//
// This list is itself hand-maintained, which is the very thing these tests
// exist to discourage. It is the narrow cost of keeping the check in the test
// package; if install grows another out-of-band value, add it here.
var installSideExtras = map[string][]string{
	ForgeGitLab: {forge.SecretDispatch},
}

// parityInstallConfig returns a config with every optional field populated, so
// the variable builders emit their maximal set. A conditional variable that is
// never emitted here would silently escape the parity check.
func parityInstallConfig(forgeName, auth string) InstallConfig {
	return InstallConfig{
		Forge:                   forgeName,
		InferenceAuth:           auth,
		InferenceProject:        "example-project",
		InferenceRegion:         "us-central1",
		InferenceServiceAccount: "fullsend@example.iam.gserviceaccount.com",
		ReviewAppClientID:       "review-client-id",
		OpenAIAPIKey:            "sk-test-not-a-real-key",
		AppSet:                  "default",
	}
}

func TestInstallUninstallVariableParity(t *testing.T) {
	const mintURL = "https://mint.example.com"

	for _, forgeName := range []string{ForgeGitHub, ForgeGitLab} {
		for _, auth := range ValidInferenceAuths() {
			t.Run(fmt.Sprintf("%s/%s", forgeName, auth), func(t *testing.T) {
				managed, err := managedVarsForForge(parityInstallConfig(forgeName, auth), mintURL)
				require.NoError(t, err)
				require.NotEmpty(t, managed,
					"no managed variables returned: the matrix no longer exercises %s", forgeName)

				cleanup := UninstallVarsForForge(forgeName)
				for _, v := range managed {
					assert.Contains(t, cleanup, v.Name,
						"install writes variable %q on %s but uninstall does not delete it; "+
							"add it to the uninstall list for that forge", v.Name, forgeName)
				}
				for _, name := range installSideExtras[forgeName] {
					assert.Contains(t, cleanup, name,
						"install provisions %q on %s outside managedVarsForForge but uninstall "+
							"does not delete it; add it to the uninstall list for that forge",
						name, forgeName)
				}
			})
		}
	}
}

func TestInstallUninstallSecretParity(t *testing.T) {
	const wifProvider = "projects/1/locations/global/workloadIdentityPools/pool/providers/provider"

	for _, forgeName := range []string{ForgeGitHub, ForgeGitLab} {
		for _, auth := range ValidInferenceAuths() {
			t.Run(fmt.Sprintf("%s/%s", forgeName, auth), func(t *testing.T) {
				written := installSecretsForForge(parityInstallConfig(forgeName, auth), wifProvider)
				require.NotEmpty(t, written,
					"no secrets returned: the matrix no longer exercises auth %s", auth)

				cleanup := UninstallSecretsForForge(forgeName)
				for name := range written {
					assert.Contains(t, cleanup, name,
						"install writes secret %q on %s but uninstall does not delete it; "+
							"add it to inferenceSecretsForAuth, which is what "+
							"managedInferenceSecrets and both uninstall lists are built from",
						name, forgeName)
				}
			})
		}
	}
}
