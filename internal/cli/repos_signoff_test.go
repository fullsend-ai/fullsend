package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const signoffTestTrailer = "Signed-off-by: Install User <install@example.com>"

// signoffManifest renders a GitHub manifest for acme/api with optional
// signoff settings at the defaults, forge-section, and entry levels. An
// empty value omits the key at that level.
func signoffManifest(defaults, platform, entry string) string {
	setting := func(indent, value string) string {
		if value == "" {
			return ""
		}
		return fmt.Sprintf("%ssignoff: %s\n", indent, value)
	}
	return "version: 1\ndefaults:\n  inference:\n    auth: vertex-wif\n" +
		setting("  ", defaults) +
		"github:\n  mint_url: https://mint.example.com\n  fullsend_ref: v1.0.0\n" +
		setting("  ", platform) +
		"  repos:\n    - name: acme/api\n" +
		setting("      ", entry)
}

func newSignoffInstallClient(repoNames ...string) *forge.FakeClient {
	fc := newInstallFakeClient(repoNames...)
	fc.AuthenticatedUserIdentity = &forge.UserIdentity{Name: "Install User", Email: "install@example.com"}
	return fc
}

func signoffInstallOpts(manifestPath string, fc *forge.FakeClient, direct bool) *reposInstallConfig {
	return &reposInstallConfig{
		manifest:               manifestPath,
		concurrency:            4,
		roles:                  []string{"triage"},
		direct:                 direct,
		inferenceProject:       "inf-proj",
		inferenceProjectNumber: "123456789",
		inferenceRegion:        "us-central1",
		testClient:             fc,
	}
}

// scaffoldCommitMessages returns the messages of every commit recorded on
// the fake client, both direct and branch commits.
func scaffoldCommitMessages(fc *forge.FakeClient) []string {
	var msgs []string
	for _, rec := range fc.CommittedFiles {
		msgs = append(msgs, rec.Message)
	}
	for _, rec := range fc.CommittedFilesToBranch {
		msgs = append(msgs, rec.Message)
	}
	return msgs
}

func requireAllSignedOff(t *testing.T, fc *forge.FakeClient) {
	t.Helper()
	msgs := scaffoldCommitMessages(fc)
	require.NotEmpty(t, msgs, "expected scaffold commits")
	for _, msg := range msgs {
		assert.True(t, strings.HasSuffix(msg, "\n\n"+signoffTestTrailer),
			"commit message must end with the sign-off trailer, got %q", msg)
	}
}

func requireNoneSignedOff(t *testing.T, fc *forge.FakeClient) {
	t.Helper()
	msgs := scaffoldCommitMessages(fc)
	require.NotEmpty(t, msgs, "expected scaffold commits")
	for _, msg := range msgs {
		assert.NotContains(t, msg, "Signed-off-by", "commit message must not carry a sign-off trailer")
	}
}

func TestReposInstallCmd_SignoffFlag(t *testing.T) {
	cmd := newReposInstallCmd()
	f := cmd.Flags().Lookup("signoff")
	require.NotNil(t, f, "expected --signoff flag")
	assert.Equal(t, "false", f.DefValue)
}

func TestRunReposInstall_Signoff_ManifestCascade(t *testing.T) {
	tests := []struct {
		name                      string
		defaults, platform, entry string
		direct                    bool
		want                      bool
	}{
		{name: "unset", want: false},
		{name: "defaults true direct", defaults: "true", direct: true, want: true},
		{name: "defaults true PR", defaults: "true", want: true},
		{name: "forge section true", platform: "true", want: true},
		{name: "repo entry true", entry: "true", direct: true, want: true},
		{name: "repo true beats defaults false", defaults: "false", entry: "true", want: true},
		{name: "repo false beats defaults true", defaults: "true", entry: "false", want: false},
		{name: "repo false beats forge true", platform: "true", entry: "false", direct: true, want: false},
		{name: "forge false beats defaults true", defaults: "true", platform: "false", want: false},
		{name: "forge true beats defaults false", defaults: "false", platform: "true", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifestPath := writeTestManifest(t, signoffManifest(tt.defaults, tt.platform, tt.entry))
			fc := newSignoffInstallClient("acme/api")

			err := runReposInstall(context.Background(), signoffInstallOpts(manifestPath, fc, tt.direct))
			require.NoError(t, err)
			if tt.want {
				requireAllSignedOff(t, fc)
			} else {
				requireNoneSignedOff(t, fc)
			}
		})
	}
}

func TestRunReposInstall_Signoff_CLIOverride(t *testing.T) {
	tests := []struct {
		name     string
		defaults string
		entry    string
		signoff  bool
		want     bool
	}{
		{name: "flag true without manifest setting", signoff: true, want: true},
		{name: "flag true beats repo false", defaults: "true", entry: "false", signoff: true, want: true},
		{name: "flag false beats defaults true", defaults: "true", signoff: false, want: false},
		{name: "flag false beats repo true", entry: "true", signoff: false, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifestPath := writeTestManifest(t, signoffManifest(tt.defaults, "", tt.entry))
			fc := newSignoffInstallClient("acme/api")
			if !tt.want {
				// An explicit --signoff=false must not even resolve identity.
				fc.Errors["GetAuthenticatedUserIdentity"] = errors.New("identity must not be requested")
			}

			opts := signoffInstallOpts(manifestPath, fc, false)
			opts.signoff = tt.signoff
			opts.signoffChanged = true
			err := runReposInstall(context.Background(), opts)
			require.NoError(t, err)
			if tt.want {
				requireAllSignedOff(t, fc)
			} else {
				requireNoneSignedOff(t, fc)
			}
		})
	}
}

func TestRunReposInstall_Signoff_FlagParsing(t *testing.T) {
	cmd := newReposInstallCmd()
	require.NoError(t, cmd.Flags().Parse([]string{"--signoff=false"}))
	assert.True(t, cmd.Flags().Changed("signoff"))
	v, err := cmd.Flags().GetBool("signoff")
	require.NoError(t, err)
	assert.False(t, v)
}

func TestRunReposInstall_Signoff_MissingIdentity(t *testing.T) {
	tests := []struct {
		name     string
		identity *forge.UserIdentity
		dryRun   bool
		wantErr  string
	}{
		{name: "no identity", wantErr: "signoff requires a GitHub user identity (name and email)"},
		{name: "no identity dry run", dryRun: true, wantErr: "signoff requires a GitHub user identity (name and email)"},
		{name: "empty name", identity: &forge.UserIdentity{Email: "install@example.com"}, wantErr: "with both name and email set"},
		{name: "empty email", identity: &forge.UserIdentity{Name: "Install User"}, wantErr: "with both name and email set"},
		{name: "blank after sanitization", identity: &forge.UserIdentity{Name: "<>", Email: "install@example.com"}, wantErr: "sign-off identity must have non-empty name and email"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifestPath := writeTestManifest(t, signoffManifest("true", "", ""))
			fc := newInstallFakeClient("acme/api")
			fc.AuthenticatedUserIdentity = tt.identity

			opts := signoffInstallOpts(manifestPath, fc, false)
			opts.dryRun = tt.dryRun
			err := runReposInstall(context.Background(), opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Empty(t, scaffoldCommitMessages(fc), "no scaffold commit may be created without a valid identity")
			assert.Empty(t, fc.CreatedProposals)
		})
	}
}

func TestRunReposInstall_Signoff_DryRunReportsTrailer(t *testing.T) {
	manifestPath := writeTestManifest(t, signoffManifest("", "", "true"))
	fc := newSignoffInstallClient("acme/api")

	opts := signoffInstallOpts(manifestPath, fc, false)
	opts.dryRun = true
	var err error
	out := captureStdout(t, func() {
		err = runReposInstall(context.Background(), opts)
	})
	require.NoError(t, err)
	assert.Contains(t, out, "Would add trailer to GitHub scaffold commits: "+signoffTestTrailer)
	assert.NotContains(t, out, "inf-proj", "secret values must not appear in output")
	assert.Empty(t, scaffoldCommitMessages(fc), "dry run must not commit")
}

func TestRunReposInstall_Signoff_DryRunDisabledReportsNothing(t *testing.T) {
	manifestPath := writeTestManifest(t, signoffManifest("", "", ""))
	fc := newSignoffInstallClient("acme/api")

	opts := signoffInstallOpts(manifestPath, fc, false)
	opts.dryRun = true
	var err error
	out := captureStdout(t, func() {
		err = runReposInstall(context.Background(), opts)
	})
	require.NoError(t, err)
	assert.NotContains(t, out, "Would add trailer")
}

func TestRunReposInstall_Signoff_Upgrade(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			manifestPath := writeTestManifest(t, signoffManifest("", map[bool]string{true: "true", false: "false"}[enabled], ""))
			fc := newInstalledFakeClientCLI("acme/api")
			fc.AuthenticatedUserIdentity = &forge.UserIdentity{Name: "Install User", Email: "install@example.com"}
			// A stale installed ref makes convergence upgrade the scaffold.
			fc.FileContents["acme/api/.github/workflows/fullsend.yml"] = []byte("uses: fullsend-ai/fullsend/.github/workflows/dispatch.yml@v0.9.0")

			opts := signoffInstallOpts(manifestPath, fc, true)
			opts.roles = nil
			require.NoError(t, runReposInstall(context.Background(), opts))
			if enabled {
				requireAllSignedOff(t, fc)
			} else {
				requireNoneSignedOff(t, fc)
			}
		})
	}
}

func TestRunReposInstall_Signoff_GitLab(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		want     bool
	}{
		{
			name: "gitlab section true",
			manifest: `version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v1.0.0
  signoff: true
  repos:
    - name: group/project
`,
			want: true,
		},
		{
			name: "repo false beats defaults true",
			manifest: `version: 1
defaults:
  signoff: true
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: group/project
      signoff: false
`,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifestPath := writeTestManifest(t, tt.manifest)
			fc := forge.NewFakeClient()
			seedGitLabInputPrerequisites(fc, "group/project", "v1.0.0")
			fc.InstallationToken = true
			fc.AuthenticatedUser = "fullsend-app[bot]"
			fc.AuthenticatedUserIdentity = &forge.UserIdentity{Name: "Install User", Email: "install@example.com"}
			fc.CollaboratorPermissions = map[string]string{"group/project/fullsend-app[bot]": "write"}
			fc.Repos = []forge.Repository{{FullName: "group/project", Name: "project", DefaultBranch: "main"}}

			// Post-install steps need a live GitLab client and may fail
			// after the scaffold MR is created; only the commit matters.
			_ = runReposInstall(context.Background(), gitlabInstallOpts(manifestPath, fc))

			msgs := scaffoldCommitMessages(fc)
			require.NotEmpty(t, msgs, "expected scaffold commits")
			for _, msg := range msgs {
				assert.Contains(t, msg, "[skip ci]")
				if tt.want {
					assert.True(t, strings.HasSuffix(msg, " [skip ci]\n\n"+signoffTestTrailer),
						"trailer must follow [skip ci] as the last paragraph, got %q", msg)
				} else {
					assert.NotContains(t, msg, "Signed-off-by")
				}
			}
		})
	}
}

func TestRunReposInstall_Signoff_GitLabMissingIdentity(t *testing.T) {
	manifestPath := writeTestManifest(t, `version: 1
defaults:
  signoff: true
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: group/project
`)
	fc := forge.NewFakeClient()
	seedGitLabInputPrerequisites(fc, "group/project", "v1.0.0")
	fc.Repos = []forge.Repository{{FullName: "group/project", Name: "project", DefaultBranch: "main"}}

	err := runReposInstall(context.Background(), gitlabInstallOpts(manifestPath, fc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signoff requires a GitLab user identity")
	assert.NotContains(t, err.Error(), "GitHub App tokens")
	assert.Empty(t, scaffoldCommitMessages(fc))
}
