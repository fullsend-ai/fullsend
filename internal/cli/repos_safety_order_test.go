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
// persisted or the forge is written to. The only manifest edit install still
// plans (the install defaults recorded for a pending install) is applied in
// memory and compared with the installed layers first, for an overlay
// relaxation and for a preset replacement alike; selecting a glob-covered
// repository plans no edit at all (#8219).
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
			name:    "glob-covered selection",
			entries: "    - name: acme/*\n",
			target:  "acme/api",
			edit: func(o *reposInstallConfig) {
				o.repoFilter = []string{"acme/api"}
			},
		},
		{
			name:    "selection beside other entries",
			entries: "    - name: acme/other\n    - name: acme/api\n",
			target:  "acme/api",
			others:  []string{"acme/other"},
			edit: func(o *reposInstallConfig) {
				o.repoFilter = []string{"acme/api"}
			},
		},
		{
			name:    "managed defaults",
			entries: "    - name: acme/api\n",
			target:  "acme/api",
			pending: true,
			edit:    func(*reposInstallConfig) {},
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

// Effective-infrastructure conflicts (a mint URL that the repos.yaml
// mint_url and the declared configuration disagree on, or a Vertex flag
// that contradicts the declared value) are rejected before the planned
// create_issues writeback is persisted, so repos.yaml stays byte-identical
// — including for a pristine glob-covered repository.
func TestRunReposInstall_InfrastructureConflictLeavesManifestByteIdentical(t *testing.T) {
	const manifestHeader = `version: 1
defaults:
  config:
    %CONFIG%github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  inference:
    auth: vertex-wif
  repos:
`
	scenarios := []struct {
		name    string
		config  string
		entries string
		filter  []string
		edit    func(*reposInstallConfig)
		want    string
	}{
		{
			name:    "mint URL conflict",
			config:  "mint_url: https://other-mint.example.com\n",
			entries: "    - name: acme/api\n",
			want:    "mint URL conflict",
		},
		{
			name:    "mint URL conflict on a glob-covered selection",
			config:  "mint_url: https://other-mint.example.com\n",
			entries: "    - name: acme/*\n",
			filter:  []string{"acme/api"},
			want:    "mint URL conflict",
		},
		{
			name:    "vertex project flag contradicts declared project",
			config:  "inference:\n      project: declared-project\n",
			entries: "    - name: acme/api\n",
			want:    "disagrees with inference.project",
		},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			manifestYAML := strings.Replace(manifestHeader, "%CONFIG%", sc.config, 1) + sc.entries
			manifestPath := writeTestManifest(t, manifestYAML)
			fc := newInstallFakeClient("acme/api")
			fc.Secrets["acme/api/FULLSEND_GCP_PROJECT_ID"] = true

			opts := githubManagedInstallOpts(manifestPath, fc)
			opts.repoFilter = sc.filter
			err := runReposInstall(context.Background(), opts)

			require.Error(t, err)
			assert.Contains(t, err.Error(), sc.want)
			data, readErr := os.ReadFile(manifestPath)
			require.NoError(t, readErr)
			assert.Equal(t, manifestYAML, string(data), "repos.yaml must be byte-identical after a rejection")
			assertNoInstallWrites(t, fc)
		})
	}
}
