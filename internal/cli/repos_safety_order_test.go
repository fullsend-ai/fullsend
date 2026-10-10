package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wideningInstallPreset supplies a cross-repo create_issues target the code
// defaults do not allow, so declaring it for an installed repository without
// a base file widens the effective configuration.
const wideningInstallPreset = "version: \"1\"\ncreate_issues:\n  allow_targets:\n    repos:\n      - acme/other\n"

// assertNoInstallWrites fails when the fake client recorded any forge write.
func assertNoInstallWrites(t *testing.T, fc *forge.FakeClient) {
	t.Helper()
	assert.Empty(t, fc.CreatedFiles, "no file writes")
	assert.Empty(t, fc.CommittedFiles, "no commits")
	assert.Empty(t, fc.CommittedFilesToBranch, "no branch commits")
	assert.Empty(t, fc.ForceCommittedFiles, "no forced commits")
	assert.Empty(t, fc.CreatedBranches, "no branches")
	assert.Empty(t, fc.CreatedProposals, "no PR/MR")
	assert.Empty(t, fc.CreatedSecrets, "no secret writes")
	assert.Empty(t, fc.DeletedSecrets, "no secret deletions")
	assert.Empty(t, fc.Variables, "no variable writes")
	assert.Empty(t, fc.UpdatedVariables, "no variable updates")
}

// A layered safety rejection stops `repos install` before repos.yaml is
// persisted or the forge is written to. Every manifest edit the install
// plans (glob carving, a new entry, --app-set, --inference-auth, and the
// install defaults and explicit --roles recorded for a pending install) is
// applied in memory and compared with the installed layers first, for an
// overlay relaxation and for a preset replacement alike.
func TestRunReposInstall_SafetyRejectionLeavesManifestByteIdentical(t *testing.T) {
	const header = `version: 1
%DEFAULTS%github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  inference:
    auth: vertex-wif
  repos:
`
	type scenario struct {
		name    string
		entries string // manifest repos list
		target  string // repository whose layers are rejected
		others  []string
		pending bool // credentials only, no shim workflow
		edit    func(*reposInstallConfig)
	}
	scenarios := []scenario{
		{
			name:    "glob carving",
			entries: "    - name: acme/*\n",
			target:  "acme/api",
			edit: func(o *reposInstallConfig) {
				o.repoFilter = []string{"acme/api"}
				o.fullsendRef = "v2.0.0"
			},
		},
		{
			name:    "added entry",
			entries: "    - name: acme/other\n",
			target:  "acme/new",
			others:  []string{"acme/other"},
			edit: func(o *reposInstallConfig) {
				o.repoFilter = []string{"acme/new"}
				o.forge = "github"
			},
		},
		{
			name:    "app-set",
			entries: "    - name: acme/api\n",
			target:  "acme/api",
			edit:    func(o *reposInstallConfig) { o.appSet = "alt" },
		},
		{
			name:    "inference-auth",
			entries: "    - name: acme/api\n",
			target:  "acme/api",
			edit:    func(o *reposInstallConfig) { o.inferenceAuth = "vertex-wif" },
		},
		{
			name:    "managed defaults and roles",
			entries: "    - name: acme/api\n",
			target:  "acme/api",
			pending: true,
			edit: func(o *reposInstallConfig) {
				o.roles = []string{"triage", "review"}
				o.rolesChanged = true
			},
		},
	}
	rejections := []struct {
		name         string
		defaults     string
		targetConfig string
		want         string
	}{
		{
			name:         "overlay relaxation",
			targetConfig: managedConfigMarker + "kill_switch: true\n",
			want:         "kill_switch",
		},
		{
			name:         "preset relaxation",
			defaults:     "defaults:\n  config_base:\n    source: preset.yaml\n",
			targetConfig: managedConfigMarker + "{}\n",
			want:         "create_issues.allow_targets",
		},
	}

	for _, sc := range scenarios {
		for _, rej := range rejections {
			t.Run(sc.name+"/"+rej.name, func(t *testing.T) {
				manifestYAML := strings.Replace(header, "%DEFAULTS%", rej.defaults, 1) + sc.entries
				manifestPath := writeTestManifest(t, manifestYAML)
				require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(manifestPath), "preset.yaml"), []byte(wideningInstallPreset), 0o644))

				names := append([]string{sc.target}, sc.others...)
				var fc *forge.FakeClient
				if sc.pending {
					fc = newInstallFakeClient(names...)
					fc.Secrets[sc.target+"/FULLSEND_GCP_PROJECT_ID"] = true
				} else {
					fc = newInstalledFakeClientCLI(names...)
				}
				fc.FileContents[overlayPath(sc.target)] = []byte(rej.targetConfig)
				for _, other := range sc.others {
					fc.FileContents[overlayPath(other)] = []byte(managedConfigMarker + "{}\n")
				}

				opts := githubManagedInstallOpts(manifestPath, fc)
				sc.edit(opts)
				err := runReposInstall(context.Background(), opts)

				require.Error(t, err)
				assert.Contains(t, err.Error(), rej.want)
				data, readErr := os.ReadFile(manifestPath)
				require.NoError(t, readErr)
				assert.Equal(t, manifestYAML, string(data), "repos.yaml must be byte-identical after a rejection")
				assertNoInstallWrites(t, fc)
			})
		}
	}
}
