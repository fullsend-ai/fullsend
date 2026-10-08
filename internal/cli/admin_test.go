package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/dispatch/gcf"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestRoleAppPrivateKeySecret(t *testing.T) {
	assert.Equal(t, "FULLSEND_TRIAGE_APP_PRIVATE_KEY", roleAppPrivateKeySecret("triage"))
	assert.Equal(t, "FULLSEND_CI_CHECK_APP_PRIVATE_KEY", roleAppPrivateKeySecret("ci-check"))
}

func TestAdminCommand_HasSubcommands(t *testing.T) {
	cmd := newAdminCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Use] = true
	}
	assert.True(t, names["install <owner/repo>"], "expected install subcommand")
	assert.False(t, names["init <owner/repo>"], "init subcommand should not exist — merged into install")

	subNames := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subNames[sub.Name()] = true
	}
	assert.True(t, subNames["install"], "expected install subcommand")
	assert.True(t, subNames["foreign"], "expected foreign subcommand")
	for _, removed := range []string{"uninstall", "analyze", "enable", "disable"} {
		assert.False(t, subNames[removed], "%s subcommand should have been removed (per-org)", removed)
	}
}

func TestInstallCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestInstallCmd_Flags(t *testing.T) {
	cmd := newInstallCmd()

	agentsFlag := cmd.Flags().Lookup("agents")
	require.NotNil(t, agentsFlag, "expected --agents flag")
	assert.Equal(t, strings.Join(config.PerRepoDefaultRoles(), ","), agentsFlag.DefValue)

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag, "expected --dry-run flag")

	skipAppSetupFlag := cmd.Flags().Lookup("skip-app-setup")
	require.NotNil(t, skipAppSetupFlag, "expected --skip-app-setup flag")

	vendorFlag := cmd.Flags().Lookup("vendor")
	require.NotNil(t, vendorFlag, "expected --vendor flag")
	assert.Equal(t, "false", vendorFlag.DefValue)

	directFlag := cmd.Flags().Lookup("direct")
	require.NotNil(t, directFlag, "expected --direct flag")
	assert.Equal(t, "false", directFlag.DefValue)

	inferenceProjectFlag := cmd.Flags().Lookup("inference-project")
	require.NotNil(t, inferenceProjectFlag, "expected --inference-project flag")

	inferenceRegionFlag := cmd.Flags().Lookup("inference-region")
	require.NotNil(t, inferenceRegionFlag, "expected --inference-region flag")
	assert.Equal(t, "global", inferenceRegionFlag.DefValue)

	inferenceWIFProviderFlag := cmd.Flags().Lookup("inference-wif-provider")
	require.NotNil(t, inferenceWIFProviderFlag, "expected --inference-wif-provider flag")

	// Old GCP flags should have been removed.
	assert.Nil(t, cmd.Flags().Lookup("gcp-project"), "--gcp-project flag should have been renamed to --inference-project")
	assert.Nil(t, cmd.Flags().Lookup("gcp-region"), "--gcp-region flag should have been renamed to --inference-region")
	assert.Nil(t, cmd.Flags().Lookup("gcp-wif-provider"), "--gcp-wif-provider flag should have been renamed to --inference-wif-provider")

	// --gcp-wif-sa-email removed (direct WIF, no intermediate SA)
	wifSAEmailFlag := cmd.Flags().Lookup("gcp-wif-sa-email")
	assert.Nil(t, wifSAEmailFlag, "--gcp-wif-sa-email flag should have been removed")

	// --repo flag should not exist (issue #495)
	repoFlag := cmd.Flags().Lookup("repo")
	assert.Nil(t, repoFlag, "--repo flag should have been removed")

	mintProviderFlag := cmd.Flags().Lookup("mint-provider")
	require.NotNil(t, mintProviderFlag, "expected --mint-provider flag")
	assert.Equal(t, "gcf", mintProviderFlag.DefValue)

	mintProjectFlag := cmd.Flags().Lookup("mint-project")
	require.NotNil(t, mintProjectFlag, "expected --mint-project flag")

	mintRegionFlag := cmd.Flags().Lookup("mint-region")
	require.NotNil(t, mintRegionFlag, "expected --mint-region flag")
	assert.Equal(t, "us-central1", mintRegionFlag.DefValue)

	mintSourceDirFlag := cmd.Flags().Lookup("mint-source-dir")
	require.NotNil(t, mintSourceDirFlag, "expected --mint-source-dir flag")

	mintURLFlag := cmd.Flags().Lookup("mint-url")
	require.NotNil(t, mintURLFlag, "expected --mint-url flag")
	assert.Equal(t, DefaultMintURL, mintURLFlag.DefValue)

	// --gcp-auth-mode removed (WIF is the only mode)
	gcpAuthModeFlag := cmd.Flags().Lookup("gcp-auth-mode")
	assert.Nil(t, gcpAuthModeFlag, "--gcp-auth-mode flag should have been removed")

	// --scaffold-customized removed (customized dirs always included)
	scaffoldCustomizedFlag := cmd.Flags().Lookup("scaffold-customized")
	assert.Nil(t, scaffoldCustomizedFlag, "--scaffold-customized flag should have been removed")

	skipMintCheckFlag := cmd.Flags().Lookup("skip-mint-check")
	require.NotNil(t, skipMintCheckFlag, "expected --skip-mint-check flag")
	assert.Equal(t, "false", skipMintCheckFlag.DefValue)

	skipMintDeployFlag := cmd.Flags().Lookup("skip-mint-deploy")
	require.NotNil(t, skipMintDeployFlag, "expected --skip-mint-deploy flag")

	appSetFlag := cmd.Flags().Lookup("app-set")
	require.NotNil(t, appSetFlag, "expected --app-set flag")
	assert.Equal(t, "fullsend-ai", appSetFlag.DefValue)
}

func TestInstallCmd_InvalidAppSet(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "myorg/widget",
		"--inference-project", "proj", "--mint-project", "proj",
		"--app-set", "INVALID"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --app-set")
}

func TestInstallCmd_PerRepoRequiresMintProjectWithoutDefaultURL(t *testing.T) {
	cmd := newRootCmd()
	// When --mint-url is explicitly cleared, --mint-project is required.
	cmd.SetArgs([]string{"admin", "install", "acme/widget", "--mint-url", ""})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--mint-project")
}

func TestInstallCmd_PerRepoDefaultMintURLSkipsMintProject(t *testing.T) {
	cmd := newRootCmd()
	// With the default mint URL, --mint-project is not required.
	// The error should be about --inference-project, not --mint-project.
	cmd.SetArgs([]string{"admin", "install", "acme/widget"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--inference-project is required for per-repo installation")
}

func TestInstallCmd_PerRepoRequiresInferenceProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget", "--mint-url", "https://mint-test-abc123.run.app"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--inference-project is required for per-repo installation")
}

func TestInstallCmd_PerRepoRejectsInvalidFormat(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/", "--mint-url", "https://mint-test-abc123.run.app", "--inference-project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo must be in owner/repo format")
}

func TestInstallCmd_PerRepoRejectsMultiSlash(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/team/repo", "--mint-url", "https://mint-test-abc123.run.app", "--inference-project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid repo name")
}

func TestInstallCmd_PerRepoRejectsNonHTTPSMintURL(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget", "--mint-url", "http://mint.example.com", "--inference-project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--mint-url must be a valid HTTPS URL")
}

func TestInstallCmd_PerRepoRejectsNonCloudRunMintURL(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget", "--mint-url", "https://evil.example.com", "--inference-project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--mint-url must be mint.fullsend.sh or a Cloud Run URL")
}

func TestInstallCmd_PerOrgEnrollmentFlagsRemoved(t *testing.T) {
	cmd := newInstallCmd()
	for _, flag := range []string{"enroll-all", "enroll-none"} {
		assert.Nil(t, cmd.Flags().Lookup(flag), "--%s flag should have been removed with per-org installation", flag)
	}
}

func TestInstallCmd_Use(t *testing.T) {
	cmd := newInstallCmd()
	assert.Equal(t, "install <owner/repo>", cmd.Use)
	assert.NotContains(t, cmd.Use, "<org")
}

func TestErrOrgTargetRemoved(t *testing.T) {
	err := errOrgTargetRemoved("fullsend admin install", "acme")
	require.Error(t, err)
	assert.Equal(t,
		`fullsend admin install requires an owner/repo target, got "acme": per-org installation has been removed; install each repository with 'fullsend admin install <owner/repo>'`,
		err.Error())
}

func TestInstallCmd_OrgOnlyTargetRejected(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project",
		"--dry-run"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
	assert.Contains(t, err.Error(), "per-org installation has been removed")
}

func TestInstallCmd_OrgTargetCheckedBeforeFlagValidation(t *testing.T) {
	// The org-target guard is the first check in RunE, so it wins over
	// flag validation errors that would otherwise be reported.
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme", "--app-set", "INVALID"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "per-org installation has been removed")
	assert.NotContains(t, err.Error(), "--app-set")
}

func TestInstallCmd_PerRepoAcceptsSharedFlags(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	sharedFlags := []struct {
		flag  string
		value string
	}{
		{"public", ""},
		{"skip-app-setup", ""},
		{"mint-provider", "gcf"},
		{"mint-source-dir", "/tmp/src"},
		{"skip-mint-deploy", ""},
		{"app-set", "custom-prefix"},
		{"vendor", ""},
	}
	for _, tc := range sharedFlags {
		t.Run(tc.flag, func(t *testing.T) {
			cmd := newRootCmd()
			args := []string{"admin", "install", "acme/widget",
				"--mint-url", "https://mint-test-abc123.run.app",
				"--inference-project", "my-project",
				"--dry-run"}
			if tc.value != "" {
				args = append(args, "--"+tc.flag, tc.value)
			} else {
				args = append(args, "--"+tc.flag)
			}
			cmd.SetArgs(args)
			err := cmd.Execute()
			require.NoError(t, err, "--%s should be accepted in per-repo mode", tc.flag)
		})
	}
}

func TestInstallCmd_ForceMintDeployFlagRemoved(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--force-mint-deploy",
		"--inference-project", "my-project",
		"--mint-project", "my-project",
		"--dry-run"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "force-mint-deploy")
}

func TestInstallCmd_PerRepoAcceptsMintRegion(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-region", "us-central1",
		"--inference-project", "my-project",
		"--mint-region", "europe-west1",
		"--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestParseAgentRoles(t *testing.T) {
	tests := []struct {
		input   string
		want    []string
		wantErr bool
	}{
		{"triage,review,coder", []string{"triage", "review", "coder"}, false},
		{" triage , review ", []string{"triage", "review"}, false},
		{"", nil, false},
		{"single", []string{"single"}, false},
		{"Invalid", nil, true},
		{"ok,BAD-role", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseAgentRoles(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "invalid role name")
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestValidateOrgName_Valid(t *testing.T) {
	valid := []string{"my-org", "org123", "A", "abc-def-ghi", "ORG"}
	for _, name := range valid {
		t.Run(name, func(t *testing.T) {
			assert.NoError(t, validateOrgName(name))
		})
	}
}

func TestValidateOrgName_Invalid(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"", "cannot be empty"},
		{"-leading", "cannot start or end with a hyphen"},
		{"trailing-", "cannot start or end with a hyphen"},
		{"invalid@char", "invalid character"},
		{"has space", "invalid character"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOrgName(tc.name)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestCheckInstallScopes_AllPresent(t *testing.T) {
	client := &forge.FakeClient{
		TokenScopes: []string{"repo", "workflow", "admin:org", "read:org"},
	}
	printer := ui.New(&discardWriter{})

	err := checkInstallScopes(context.Background(), client, printer)
	require.NoError(t, err)
}

func TestCheckInstallScopes_Missing(t *testing.T) {
	client := &forge.FakeClient{
		TokenScopes: []string{"repo"},
	}
	printer := ui.New(&discardWriter{})

	err := checkInstallScopes(context.Background(), client, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow")
	assert.Contains(t, err.Error(), "admin:org")
}

func TestCheckInstallScopes_FineGrainedToken(t *testing.T) {
	client := &forge.FakeClient{
		TokenScopes: nil,
	}
	var buf bytes.Buffer
	printer := ui.New(&buf)

	err := checkInstallScopes(context.Background(), client, printer)
	require.NoError(t, err)
	output := buf.String()
	assert.Contains(t, output, "fine-grained token detected")
	assert.Contains(t, output, "repo")
	assert.Contains(t, output, "workflow")
	assert.Contains(t, output, "admin:org")
}

func TestCheckInstallScopes_InstallationToken(t *testing.T) {
	client := &forge.FakeClient{
		InstallationToken: true,
	}
	printer := ui.New(&discardWriter{})

	err := checkInstallScopes(context.Background(), client, printer)
	require.NoError(t, err)
}

func TestCheckInstallScopes_GetTokenScopesError(t *testing.T) {
	client := &forge.FakeClient{
		Errors: map[string]error{"GetTokenScopes": errors.New("network error")},
	}
	printer := ui.New(&discardWriter{})

	err := checkInstallScopes(context.Background(), client, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking token scopes")
	assert.Contains(t, err.Error(), "network error")
}

func TestCheckInstallScopes_SyncWithPerRepoScopes(t *testing.T) {
	// App creation needs admin:org on top of the per-repo scopes.
	want := append(append([]string(nil), perRepoRequiredScopes...), "admin:org")

	assert.ElementsMatch(t, installRequiredScopes, want,
		"installRequiredScopes must match perRepoRequiredScopes plus admin:org; update the variable if the per-repo scopes change")
}

func TestCheckPerRepoScopes_AllPresent(t *testing.T) {
	client := &forge.FakeClient{
		TokenScopes: []string{"repo", "workflow", "read:org"},
	}
	printer := ui.New(&discardWriter{})

	err := checkPerRepoScopes(context.Background(), client, printer)
	require.NoError(t, err)
}

func TestCheckPerRepoScopes_Missing(t *testing.T) {
	client := &forge.FakeClient{
		TokenScopes: []string{"repo"},
	}
	printer := ui.New(&discardWriter{})

	err := checkPerRepoScopes(context.Background(), client, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow")
	assert.NotContains(t, err.Error(), "admin:org")
}

func TestCheckPerRepoScopes_FineGrainedToken(t *testing.T) {
	client := &forge.FakeClient{
		TokenScopes: nil,
	}
	var buf bytes.Buffer
	printer := ui.New(&buf)

	err := checkPerRepoScopes(context.Background(), client, printer)
	require.NoError(t, err)
	output := buf.String()
	assert.Contains(t, output, "fine-grained token detected")
	assert.Contains(t, output, "repo")
	assert.Contains(t, output, "workflow")
	assert.NotContains(t, output, "admin:org")
}

func TestCheckPerRepoScopes_InstallationToken(t *testing.T) {
	client := &forge.FakeClient{
		InstallationToken: true,
	}
	printer := ui.New(&discardWriter{})

	err := checkPerRepoScopes(context.Background(), client, printer)
	require.NoError(t, err)
}

func TestCheckPerRepoScopes_GetTokenScopesError(t *testing.T) {
	client := &forge.FakeClient{
		Errors: map[string]error{"GetTokenScopes": errors.New("network error")},
	}
	printer := ui.New(&discardWriter{})

	err := checkPerRepoScopes(context.Background(), client, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking token scopes")
	assert.Contains(t, err.Error(), "network error")
}

func TestCheckPerRepoScopes_DoesNotRequireAdminOrg(t *testing.T) {
	client := &forge.FakeClient{
		TokenScopes: []string{"repo", "workflow"},
	}
	printer := ui.New(&discardWriter{})

	err := checkPerRepoScopes(context.Background(), client, printer)
	require.NoError(t, err, "per-repo should not require admin:org scope")
}

func TestPatForbiddenGuidance(t *testing.T) {
	guidance := patForbiddenGuidance("test-org", "test-repo")
	assert.Contains(t, guidance, `"test-org"`)
	assert.Contains(t, guidance, "test-org/test-repo")
	assert.Contains(t, guidance, "GH_TOKEN")
	assert.Contains(t, guidance, "GITHUB_TOKEN")
	assert.Contains(t, guidance, "gh auth token")
	assert.Contains(t, guidance, "Contents:")
	assert.Contains(t, guidance, "Workflows:")
	assert.Contains(t, guidance, "Secrets:")
	assert.Contains(t, guidance, "Variables:")
	assert.Contains(t, guidance, "Pull requests:")
	assert.Contains(t, guidance, "Metadata:")
	assert.Contains(t, guidance, "https://github.com/settings/personal-access-tokens/new")
	assert.Contains(t, guidance, "export GH_TOKEN=github_pat_")
	assert.Contains(t, guidance, "fullsend github setup test-org/test-repo")
}

func TestPerRepoRequiredScopes_SubsetOfInstallScopes(t *testing.T) {
	installSet := make(map[string]bool)
	for _, s := range installRequiredScopes {
		installSet[s] = true
	}
	for _, s := range perRepoRequiredScopes {
		assert.True(t, installSet[s],
			"perRepoRequiredScopes contains %q which is not in installRequiredScopes", s)
	}
}

func TestInstallCmd_PerRepoRejectsInvalidRole(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--agents", "triage,INVALID",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role name")
}

func TestInstallCmd_RejectsInvalidRuntime(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget", "--runtime", "bogus", "--dry-run"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --runtime")
}

func TestInstallCmd_PerRepoRejectsOwnerWithDots(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "my.org/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid owner name")
}

func TestInstallCmd_PerRepoRejectsURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"https URL", "https://github.com/acme/widget"},
		{"http URL", "http://github.com/acme/widget"},
		{"www prefix", "www.github.com/acme/widget"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newRootCmd()
			cmd.SetArgs([]string{"admin", "install", tc.input,
				"--mint-url", "https://mint-test-abc123.run.app",
				"--inference-project", "my-project"})
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "expected owner/repo format, got a URL")
		})
	}
}

// --- resolveSharedRoleAppIDs tests ---

func TestResolveSharedRoleAppIDs_MatchesInstalledApps(t *testing.T) {
	fake := forge.NewFakeClient()
	fake.Installations = []forge.Installation{
		{AppID: 100, AppSlug: "acme-coder"},
		{AppID: 200, AppSlug: "acme-reviewer"},
	}

	existingIDs := map[string]string{
		"coder":    "100",
		"reviewer": "200",
	}

	result, err := resolveSharedRoleAppIDs(context.Background(), fake, existingIDs, "new-org", []string{"coder", "reviewer"})
	require.NoError(t, err)
	assert.Equal(t, "100", result["coder"])
	assert.Equal(t, "200", result["reviewer"])
}

func TestResolveSharedRoleAppIDs_ErrorWhenAppNotInstalled(t *testing.T) {
	fake := forge.NewFakeClient()
	fake.Installations = []forge.Installation{
		{AppID: 100, AppSlug: "acme-coder"},
	}

	existingIDs := map[string]string{
		"coder":    "100",
		"reviewer": "999",
	}

	_, err := resolveSharedRoleAppIDs(context.Background(), fake, existingIDs, "new-org", []string{"coder", "reviewer"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no shared app for role \"reviewer\"")
}

func TestResolveSharedRoleAppIDs_ErrorWhenNoExistingIDs(t *testing.T) {
	fake := forge.NewFakeClient()

	_, err := resolveSharedRoleAppIDs(context.Background(), fake, nil, "new-org", []string{"coder"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no existing ROLE_APP_IDS")
}

func TestResolveSharedRoleAppIDs_ErrorWhenRoleNotConfigured(t *testing.T) {
	fake := forge.NewFakeClient()
	fake.Installations = []forge.Installation{{AppID: 100, AppSlug: "acme-coder"}}

	_, err := resolveSharedRoleAppIDs(context.Background(), fake, map[string]string{"coder": "100"}, "new-org", []string{"triage"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no app ID configured for role "triage"`)
}

func TestResolveSharedRoleAppIDs_UsesRoleOnlyIDs(t *testing.T) {
	fake := forge.NewFakeClient()
	fake.Installations = []forge.Installation{
		{AppID: 100, AppSlug: "acme-coder"},
	}

	existingIDs := map[string]string{
		"coder": "100",
	}

	result, err := resolveSharedRoleAppIDs(context.Background(), fake, existingIDs, "new-org", []string{"coder"})
	require.NoError(t, err)
	assert.Equal(t, "100", result["coder"])
}

func TestResolveSharedRoleAppIDs_IgnoresLegacyOrgScopedKeys(t *testing.T) {
	fake := forge.NewFakeClient()
	fake.Installations = []forge.Installation{
		{AppID: 100, AppSlug: "acme-coder"},
	}

	existingIDs := map[string]string{
		"acme-corp/coder": "100",
	}

	_, err := resolveSharedRoleAppIDs(context.Background(), fake, existingIDs, "acme-corp", []string{"coder"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no existing ROLE_APP_IDS")
}

func TestDetectSharedApps_MatchesRoleOnlyIDs(t *testing.T) {
	old := detectSharedAppsGCFClientFactory
	detectSharedAppsGCFClientFactory = func(string) gcf.GCFClient {
		return gcf.NewFakeGCFClient(gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI: "https://mint.example.com",
			EnvVars: map[string]string{
				"ROLE_APP_IDS": `{"coder":"100","triage":"200"}`,
			},
		}))
	}
	t.Cleanup(func() { detectSharedAppsGCFClientFactory = old })

	fake := forge.NewFakeClient()
	fake.Installations = []forge.Installation{
		{AppID: 100, AppSlug: "fullsend-ai-coder"},
		{AppID: 200, AppSlug: "fullsend-ai-triage"},
	}

	slugs, roleIDs, err := detectSharedApps(context.Background(), fake, ui.New(&strings.Builder{}), "acme", []string{"coder", "triage"}, "mint-project", "us-central1")
	require.NoError(t, err)
	assert.Equal(t, "fullsend-ai-coder", slugs["coder"])
	assert.Equal(t, "100", roleIDs["coder"])
	assert.Equal(t, "200", roleIDs["triage"])
}

func TestDetectSharedApps_NoRoleOnlyIDs(t *testing.T) {
	old := detectSharedAppsGCFClientFactory
	detectSharedAppsGCFClientFactory = func(string) gcf.GCFClient {
		return gcf.NewFakeGCFClient(gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"acme/coder":"100"}`},
		}))
	}
	t.Cleanup(func() { detectSharedAppsGCFClientFactory = old })

	slugs, roleIDs, err := detectSharedApps(context.Background(), forge.NewFakeClient(), ui.New(&strings.Builder{}), "acme", []string{"coder"}, "mint-project", "us-central1")
	require.NoError(t, err)
	assert.Empty(t, slugs)
	assert.Empty(t, roleIDs)
}

func TestDetectSharedApps_ReadRoleAppIDsError(t *testing.T) {
	old := detectSharedAppsGCFClientFactory
	detectSharedAppsGCFClientFactory = func(string) gcf.GCFClient {
		return gcf.NewFakeGCFClient(gcf.WithFakeErrors(map[string]error{
			"GetFunction": fmt.Errorf("permission denied"),
		}))
	}
	t.Cleanup(func() { detectSharedAppsGCFClientFactory = old })

	out := &strings.Builder{}
	slugs, roleIDs, err := detectSharedApps(context.Background(), forge.NewFakeClient(), ui.New(out), "acme", []string{"coder"}, "mint-project", "us-central1")
	require.NoError(t, err)
	assert.Nil(t, slugs)
	assert.Nil(t, roleIDs)
	assert.Contains(t, out.String(), "Could not read ROLE_APP_IDS")
}

func TestDetectSharedApps_ListInstallationsError(t *testing.T) {
	old := detectSharedAppsGCFClientFactory
	detectSharedAppsGCFClientFactory = func(string) gcf.GCFClient {
		return gcf.NewFakeGCFClient(
			gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
				URI:     "https://mint.example.com",
				EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
			}),
			gcf.WithFakeTrafficEnvVars(map[string]string{
				"ROLE_APP_IDS": `{"coder":"100"}`,
			}),
		)
	}
	t.Cleanup(func() { detectSharedAppsGCFClientFactory = old })

	fake := forge.NewFakeClient()
	fake.Errors["ListOrgInstallations"] = fmt.Errorf("forbidden")

	slugs, roleIDs, err := detectSharedApps(context.Background(), fake, ui.New(&strings.Builder{}), "acme", []string{"coder"}, "mint-project", "us-central1")
	require.NoError(t, err)
	assert.Nil(t, slugs)
	assert.Equal(t, map[string]string{"coder": "100"}, roleIDs)
}

func TestInstallCmd_SkipMintCheckUsesDefaultMintURL(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--skip-mint-check",
		"--inference-project", "my-project",
		"--dry-run"})
	err := cmd.Execute()
	// With the default mint URL, --skip-mint-check no longer errors
	// when --mint-url is not explicitly provided.
	require.NoError(t, err)
}

func TestInstallCmd_SkipMintCheckRejectsEmptyMintURL(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--skip-mint-check",
		"--mint-url", "",
		"--inference-project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--mint-url is required when using --skip-mint-check")
}

func TestInstallCmd_SkipMintCheckAcceptsNonCloudRunURL(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--skip-mint-check",
		"--mint-url", "https://mint.example.com/v1/token",
		"--inference-project", "my-project",
		"--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestInstallCmd_SkipMintCheckSkipsMintProject(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	// Without --skip-mint-check and without --mint-project/--mint-url, an error is returned.
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget"})
	err := cmd.Execute()
	require.Error(t, err)

	// With --skip-mint-check, --mint-project is not required.
	cmd2 := newRootCmd()
	cmd2.SetArgs([]string{"admin", "install", "acme/widget",
		"--skip-mint-check",
		"--mint-url", "https://mint.example.com/v1/token",
		"--inference-project", "my-project",
		"--dry-run"})
	err2 := cmd2.Execute()
	require.NoError(t, err2)
}

func TestInstallCmd_SkipMintCheckRejectsUserinfo(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--skip-mint-check",
		"--mint-url", "https://user:pass@mint.example.com/v1/token",
		"--inference-project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain embedded credentials")
}

func TestInstallCmd_SkipMintCheckRejectsHTTP(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--skip-mint-check",
		"--mint-url", "http://mint.example.com",
		"--inference-project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--mint-url must be a valid HTTPS URL")
}

func TestValidateMintURLHTTPS(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{"valid URL", "https://mint.example.com/v1/token", ""},
		{"valid with port", "https://mint.example.com:8443/v1", ""},
		{"http rejected", "http://mint.example.com", "HTTPS URL"},
		{"empty string", "", "HTTPS URL"},
		{"no host", "https://", "HTTPS URL"},
		{"userinfo", "https://user:pass@host.com", "embedded credentials"},
		{"username only", "https://user@host.com", "embedded credentials"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMintURLHTTPS(tc.input)
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			}
		})
	}
}

func TestValidateSkipMintCheck(t *testing.T) {
	require.Error(t, validateSkipMintCheck(""))
	require.Error(t, validateSkipMintCheck("http://example.com"))
	require.NoError(t, validateSkipMintCheck("https://mint.example.com/v1/token"))
}

func TestValidateMintURL_AcceptsHostedCommunityMint(t *testing.T) {
	require.NoError(t, validateMintURL("https://mint.fullsend.sh"))
}

func TestValidateMintURL_AcceptsCloudRunURL(t *testing.T) {
	require.NoError(t, validateMintURL("https://fullsend-mint-abc123.run.app"))
}

func TestValidateMintURL_AcceptsCloudFunctionsURL(t *testing.T) {
	require.NoError(t, validateMintURL("https://us-central1-my-project.cloudfunctions.net"))
}

func TestValidateMintURL_RejectsArbitraryHosts(t *testing.T) {
	err := validateMintURL("https://evil.example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "got host")
}

func TestValidateMintURL_RejectsOtherFullsendSubdomains(t *testing.T) {
	err := validateMintURL("https://evil.fullsend.sh")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "got host")
}

func TestDefaultMintURL_IsHostedCommunity(t *testing.T) {
	assert.Equal(t, "https://mint.fullsend.sh", DefaultMintURL)
}

func TestIsHostedMintURL(t *testing.T) {
	assert.True(t, IsHostedMintURL("https://mint.fullsend.sh"))
	assert.True(t, IsHostedMintURL("https://mint.fullsend.sh/v1/token"))
	assert.True(t, IsHostedMintURL("https://mint.fullsend.sh:443"))
	assert.True(t, IsHostedMintURL("https://Mint.Fullsend.SH"))
	assert.False(t, IsHostedMintURL("https://evil.example.com"))
	assert.False(t, IsHostedMintURL("https://fullsend-mint-abc123.run.app"))
	assert.False(t, IsHostedMintURL(""))
	assert.False(t, IsHostedMintURL("://"))
}

func TestValidateWIFProvider_Valid(t *testing.T) {
	valid := []string{
		"projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/gh-acme-widget",
		"projects/999999999999/locations/global/workloadIdentityPools/my-pool-123/providers/my-provider-456",
		"projects/1/locations/global/workloadIdentityPools/abcd/providers/efgh",
		"projects/1/locations/global/workloadIdentityPools/a-very-long-pool-name-32-chars1/providers/a-very-long-prov-name-32-chars1",
	}
	for _, v := range valid {
		t.Run(v, func(t *testing.T) {
			require.NoError(t, validateWIFProvider(v))
		})
	}
}

func TestValidateWIFProvider_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"bare name", "standalone-fullsend"},
		{"missing projects prefix", "123/locations/global/workloadIdentityPools/pool/providers/prov"},
		{"partial path", "projects/123/locations/global/workloadIdentityPools/pool"},
		{"wrong location", "projects/123/locations/us-east1/workloadIdentityPools/pool/providers/prov"},
		{"non-numeric project", "projects/my-project/locations/global/workloadIdentityPools/pool/providers/prov"},
		{"empty string", ""},
		{"trailing slash", "projects/123/locations/global/workloadIdentityPools/pool/providers/prov/"},
		{"uppercase pool", "projects/123/locations/global/workloadIdentityPools/Pool/providers/prov"},
		{"pool too short (1 char)", "projects/123/locations/global/workloadIdentityPools/a/providers/abcd"},
		{"pool too short (3 chars)", "projects/123/locations/global/workloadIdentityPools/abc/providers/abcd"},
		{"provider too short (1 char)", "projects/123/locations/global/workloadIdentityPools/abcd/providers/a"},
		{"pool trailing hyphen", "projects/123/locations/global/workloadIdentityPools/abcd-/providers/abcd"},
		{"provider trailing hyphen", "projects/123/locations/global/workloadIdentityPools/abcd/providers/abcd-"},
		{"pool too long (33 chars)", "projects/123/locations/global/workloadIdentityPools/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/providers/abcd"},
		{"provider too long (33 chars)", "projects/123/locations/global/workloadIdentityPools/abcd/providers/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWIFProvider(tc.input)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--inference-wif-provider must be a full WIF provider resource name")
		})
	}
}

func TestInstallCmd_PerRepoRejectsInvalidWIFProvider(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project",
		"--inference-wif-provider", "just-a-name"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--inference-wif-provider must be a full WIF provider resource name")
}

func TestInstallCmd_PerRepoAcceptsValidWIFProvider(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project",
		"--inference-wif-provider", "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		"--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestInstallCmd_PerRepoDryRun_Vendor(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project",
		"--inference-wif-provider", "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		"--dry-run",
		"--vendor"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestRunPerRepoInstall_ValidationErrors(t *testing.T) {
	base := perRepoInstallConfig{
		RepoFullName:     "acme/widget",
		Agents:           strings.Join(config.PerRepoDefaultRoles(), ","),
		InferenceProject: "my-project",
		MintProject:      "my-project",
		MintURL:          "https://mint.example.com/v1/token",
		SkipMintCheck:    true,
	}
	tests := []struct {
		name string
		cfg  perRepoInstallConfig
		want string
	}{
		{
			name: "url not owner/repo",
			cfg: func() perRepoInstallConfig {
				c := base
				c.RepoFullName = "https://github.com/acme/widget"
				return c
			}(),
			want: "expected owner/repo format",
		},
		{
			name: "invalid owner",
			cfg: func() perRepoInstallConfig {
				c := base
				c.RepoFullName = "-bad/widget"
				return c
			}(),
			want: "invalid owner name",
		},
		{
			name: "missing inference project",
			cfg: func() perRepoInstallConfig {
				c := base
				c.InferenceProject = ""
				return c
			}(),
			want: "--inference-project is required",
		},
		{
			name: "missing mint project without skip",
			cfg: func() perRepoInstallConfig {
				c := base
				c.SkipMintCheck = false
				c.MintURL = ""
				c.MintProject = ""
				return c
			}(),
			want: "--mint-project",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runPerRepoInstall(context.Background(), tt.cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

type testWIFProvisioner struct {
	wifProvider    string
	wifErr         error
	discoverResult *adminMintDiscovery
	discoverErr    error
}

func (p *testWIFProvisioner) DiscoverMint(_ context.Context) (*adminMintDiscovery, error) {
	return p.discoverResult, p.discoverErr
}

func (p *testWIFProvisioner) ProvisionWIF(_ context.Context) (string, error) {
	return p.wifProvider, p.wifErr
}

func (p *testWIFProvisioner) RegisterPerRepoWIF(_ context.Context, _ string) error { return nil }
func (p *testWIFProvisioner) DeletePerRepoWIF(_ context.Context, _ string) error   { return nil }
func (p *testWIFProvisioner) DeleteWIFProvider(_ context.Context, _ string) error  { return nil }

func perRepoTestBase() perRepoInstallConfig {
	return perRepoInstallConfig{
		RepoFullName:         "acme/widget",
		Agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
		InferenceProject:     "test-project",
		InferenceRegion:      "us-central1",
		MintURL:              "https://mint.example.com/v1/token",
		SkipMintCheck:        true,
		SkipAppSetup:         true,
		InferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/fullsend-provider",
		testPrinter:          ui.New(&bytes.Buffer{}),
	}
}

func TestRunPerRepoInstall_SuccessfulNonVendor(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: "widget", FullName: "acme/widget", DefaultBranch: "main"},
	}

	cfg := perRepoTestBase()
	cfg.testClient = client
	cfg.Direct = true

	err := runPerRepoInstall(context.Background(), cfg)
	require.NoError(t, err)
}

func TestRunPerRepoInstall_WithWIFProvisioning(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: "widget", FullName: "acme/widget", DefaultBranch: "main"},
	}

	cfg := perRepoTestBase()
	cfg.testClient = client
	cfg.InferenceWIFProvider = ""
	cfg.Direct = true
	cfg.testWIFProvisioner = &testWIFProvisioner{
		wifProvider: "projects/123/locations/global/workloadIdentityPools/pool/providers/prov",
	}

	err := runPerRepoInstall(context.Background(), cfg)
	require.NoError(t, err)
}

func TestRunPerRepoInstall_WIFProvisioningError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: "widget", FullName: "acme/widget", DefaultBranch: "main"},
	}

	cfg := perRepoTestBase()
	cfg.testClient = client
	cfg.InferenceWIFProvider = ""
	cfg.Direct = true
	cfg.testWIFProvisioner = &testWIFProvisioner{
		wifErr: fmt.Errorf("IAM permission denied"),
	}

	err := runPerRepoInstall(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provisioning WIF")
}

func TestRunPerRepoInstall_AlreadyInstalledUpgrade(t *testing.T) {
	client := forge.NewFakeClient()
	client.VariableValues = map[string]string{
		"acme/widget/FULLSEND_MINT_URL": "https://mint.example.com/v1/token",
	}
	client.Repos = []forge.Repository{
		{Name: "widget", FullName: "acme/widget", DefaultBranch: "main"},
	}

	cfg := perRepoTestBase()
	cfg.testClient = client
	cfg.Direct = true

	err := runPerRepoInstall(context.Background(), cfg)
	require.NoError(t, err)
}

func TestRunPerRepoInstall_AppSet_PreservesExistingFallsBackToDefault(t *testing.T) {
	tests := []struct {
		name           string
		existingAppSet string // pre-existing FULLSEND_APP_SET on the repo, if any
		wantAppSet     string
	}{
		{"no existing app set falls back to flag default", "", appsetup.DefaultAppSet},
		{"existing custom app set is preserved on rerun without --app-set", "acme-custom", "acme-custom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := forge.NewFakeClient()
			client.Repos = []forge.Repository{
				{Name: "widget", FullName: "acme/widget", DefaultBranch: "main"},
			}
			client.VariableValues = map[string]string{
				"acme/widget/FULLSEND_MINT_URL": "https://mint.example.com/v1/token",
			}
			if tt.existingAppSet != "" {
				client.VariableValues["acme/widget/"+forge.VarAppSet] = tt.existingAppSet
			}

			cfg := perRepoTestBase()
			cfg.testClient = client
			cfg.Direct = true
			// --app-set was not passed explicitly: AppSet carries only the
			// flag's default value, so a rerun must not let it clobber a
			// custom value already on the repo.
			cfg.AppSet = appsetup.DefaultAppSet
			cfg.AppSetExplicit = false

			err := runPerRepoInstall(context.Background(), cfg)
			require.NoError(t, err)

			gotAppSet := client.VariableValues["acme/widget/"+forge.VarAppSet]
			assert.Equal(t, tt.wantAppSet, gotAppSet,
				"runPerRepoInstall should preserve an existing FULLSEND_APP_SET and only default when absent")
		})
	}
}

func TestRunPerRepoInstall_AppSet_ExplicitRepairsExisting(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: "widget", FullName: "acme/widget", DefaultBranch: "main"},
	}
	client.VariableValues = map[string]string{
		"acme/widget/FULLSEND_MINT_URL":  "https://mint.example.com/v1/token",
		"acme/widget/" + forge.VarAppSet: "acme-custom",
	}

	cfg := perRepoTestBase()
	cfg.testClient = client
	cfg.Direct = true
	cfg.AppSet = "acme-explicit"
	cfg.AppSetExplicit = true

	err := runPerRepoInstall(context.Background(), cfg)
	require.NoError(t, err)

	gotAppSet := client.VariableValues["acme/widget/"+forge.VarAppSet]
	assert.Equal(t, "acme-explicit", gotAppSet,
		"an explicit --app-set should repair drift even when a different value is already on the repo")
}

func TestRunPerRepoInstall_DryRun(t *testing.T) {
	client := forge.NewFakeClient()

	cfg := perRepoTestBase()
	cfg.testClient = client
	cfg.DryRun = true

	err := runPerRepoInstall(context.Background(), cfg)
	require.NoError(t, err)
}

func TestRunPerRepoInstall_ScaffoldCommitError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors = map[string]error{
		"GetRepo": fmt.Errorf("repo not accessible"),
	}

	cfg := perRepoTestBase()
	cfg.testClient = client
	cfg.Direct = true

	err := runPerRepoInstall(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting repo info")
}

func TestFilterSlugsByAppSet(t *testing.T) {
	tests := []struct {
		name   string
		appSet string
		slugs  map[string]string
		want   map[string]string
	}{
		{
			name:   "matching app-set preserved",
			appSet: "fullsend-ai",
			slugs:  map[string]string{"coder": "fullsend-ai-coder", "review": "fullsend-ai-review"},
			want:   map[string]string{"coder": "fullsend-ai-coder", "review": "fullsend-ai-review"},
		},
		{
			name:   "different app-set filtered out",
			appSet: "fullsend-ai",
			slugs:  map[string]string{"coder": "konflux-ci-coder", "review": "konflux-ci-review"},
			want:   map[string]string{},
		},
		{
			name:   "mixed app-sets keeps only matching",
			appSet: "fullsend-ai",
			slugs:  map[string]string{"coder": "fullsend-ai-coder", "review": "konflux-ci-review"},
			want:   map[string]string{"coder": "fullsend-ai-coder"},
		},
		{
			name:   "nil input returns empty map",
			appSet: "fullsend-ai",
			slugs:  nil,
			want:   map[string]string{},
		},
		{
			name:   "shorter prefix does not match longer slug",
			appSet: "fullsend",
			slugs:  map[string]string{"coder": "fullsend-ai-coder"},
			want:   map[string]string{},
		},
		{
			name:   "default app-set matches own slugs",
			appSet: "fullsend",
			slugs:  map[string]string{"coder": "fullsend-coder", "review": "fullsend-review"},
			want:   map[string]string{"coder": "fullsend-coder", "review": "fullsend-review"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterSlugsByAppSet(tt.slugs, tt.appSet)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestInstallCmd_SkipMintCheckStillValidatesWIFProvider(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--dry-run",
		"--skip-mint-check",
		"--mint-url", "https://mint.example.com/v1/token",
		"--inference-project", "my-project",
		"--inference-wif-provider", "standalone-fullsend"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--inference-wif-provider must be a full WIF provider resource name")
}

func TestApplyPerRepoScaffold(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	printer := ui.New(&bytes.Buffer{})

	files := []forge.TreeFile{
		{Path: ".github/workflows/fullsend.yaml", Content: []byte("workflow"), Mode: "100644"},
		{Path: ".fullsend/config.yaml", Content: []byte("config"), Mode: "100644"},
	}
	repoVars := map[string]string{
		"FULLSEND_MINT_URL":   "https://mint.example.run.app",
		"FULLSEND_GCP_REGION": "global",
	}
	repoSecrets := map[string]string{
		"FULLSEND_GCP_PROJECT_ID":   "my-project",
		"FULLSEND_GCP_WIF_PROVIDER": "projects/123/locations/global/workloadIdentityPools/pool/providers/prov",
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, repoVars, repoSecrets, scaffoldOptions{})
	require.NoError(t, err)

	require.Len(t, client.CommittedFilesToBranch, 1)
	assert.Equal(t, "acme", client.CommittedFilesToBranch[0].Owner)
	assert.Equal(t, "widget", client.CommittedFilesToBranch[0].Repo)
	assert.Len(t, client.CommittedFilesToBranch[0].Files, 2)

	require.NotEmpty(t, client.CreatedProposals, "expected scaffold PR to be created")

	varNames := make(map[string]string)
	for _, v := range client.Variables {
		varNames[v.Name] = v.Value
	}
	assert.Equal(t, "https://mint.example.run.app", varNames["FULLSEND_MINT_URL"])
	assert.Equal(t, "global", varNames["FULLSEND_GCP_REGION"])

	secretNames := make(map[string]string)
	for _, s := range client.CreatedSecrets {
		secretNames[s.Name] = s.Value
	}
	assert.Equal(t, "my-project", secretNames["FULLSEND_GCP_PROJECT_ID"])
	assert.Contains(t, secretNames, "FULLSEND_GCP_WIF_PROVIDER")
}

func TestApplyPerRepoScaffold_WithSignOff(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	var buf bytes.Buffer
	printer := ui.New(&buf)

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("config"), Mode: "100644"},
	}
	trailer := "Signed-off-by: Test User <test@example.com>"

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil, scaffoldOptions{direct: true, signOffTrailer: trailer})
	require.NoError(t, err)

	require.NotEmpty(t, client.CommittedFiles)
	commitMsg := client.CommittedFiles[0].Message
	assert.Contains(t, commitMsg, "chore: initialize fullsend per-repo installation")
	assert.Contains(t, commitMsg, "Signed-off-by: Test User <test@example.com>")
}

func TestApplyPerRepoScaffold_WithoutSignOff(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	printer := ui.New(&bytes.Buffer{})

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("config"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil, scaffoldOptions{direct: true})
	require.NoError(t, err)

	require.NotEmpty(t, client.CommittedFiles)
	commitMsg := client.CommittedFiles[0].Message
	assert.Equal(t, "chore: initialize fullsend per-repo installation", commitMsg)
}

func TestApplyPerRepoScaffold_GetRepoError(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Errors["GetRepo"] = errors.New("not found")
	printer := ui.New(&bytes.Buffer{})

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", nil, nil, nil, scaffoldOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting repo info")
}

func TestApplyPerRepoScaffold_CommitFilesError(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = errors.New("permission denied")
	printer := ui.New(&bytes.Buffer{})

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil, scaffoldOptions{direct: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "committing scaffold files")
	assert.Empty(t, client.CreatedBranches, "should not attempt fallback for generic error")
	assert.Empty(t, client.CreatedProposals, "should not attempt fallback for generic error")
}

func TestApplyPerRepoScaffold_Idempotent(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	noChange := false
	client.CommitFilesChanged = &noChange
	client.Errors = map[string]error{
		"CreateChangeProposal": fmt.Errorf("PR: %w", forge.ErrAlreadyExists),
	}
	var buf bytes.Buffer
	printer := ui.New(&buf)

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, map[string]string{"K": "V"}, map[string]string{"S": "secret"}, scaffoldOptions{})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "up to date")
	assert.Len(t, client.Variables, 1, "variables should still be set even when files are unchanged")
	assert.Len(t, client.CreatedSecrets, 1, "secrets should still be set even when files are unchanged")
}

func TestApplyPerRepoScaffold_DefaultPR_NoChanges(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors = map[string]error{
		"CreateChangeProposal": fmt.Errorf("PR: %w", forge.ErrNoChanges),
	}
	var buf bytes.Buffer
	printer := ui.New(&buf)

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, map[string]string{"K": "V"}, nil, scaffoldOptions{})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "up to date")
}

func TestApplyPerRepoScaffold_NonMainBranch(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "develop"}}
	var buf bytes.Buffer
	printer := ui.New(&buf)

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil, scaffoldOptions{direct: true})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "acme/widget (develop branch)")
	assert.Contains(t, buf.String(), "Pushed 1 file to develop")
}

func TestApplyPerRepoScaffold_CreateVariableError(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors = map[string]error{
		"CreateOrUpdateRepoVariable": errors.New("rate limited"),
	}
	printer := ui.New(&bytes.Buffer{})

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", nil, map[string]string{"K": "V"}, nil, scaffoldOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setting repo variable")
}

func TestApplyPerRepoScaffold_CreateSecretError(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors = map[string]error{
		"CreateRepoSecret": errors.New("forbidden"),
	}
	printer := ui.New(&bytes.Buffer{})

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", nil, nil, map[string]string{"S": "V"}, scaffoldOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setting repo secret")
}

func TestApplyPerRepoScaffold_ProtectedBranchFallback(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	var buf bytes.Buffer
	printer := ui.New(&buf)

	files := []forge.TreeFile{
		{Path: ".github/workflows/fullsend.yaml", Content: []byte("workflow"), Mode: "100644"},
	}
	repoVars := map[string]string{"K": "V"}
	repoSecrets := map[string]string{"S": "secret"}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, repoVars, repoSecrets, scaffoldOptions{direct: true})
	require.NoError(t, err)

	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "acme/widget/fullsend/scaffold-install", client.CreatedBranches[0])

	require.Len(t, client.CommittedFilesToBranch, 1)
	assert.Equal(t, "fullsend/scaffold-install", client.CommittedFilesToBranch[0].Branch)
	assert.Len(t, client.CommittedFilesToBranch[0].Files, 1)

	require.Len(t, client.CreatedProposals, 1)
	assert.Contains(t, client.CreatedProposals[0].Title, "fullsend")

	output := buf.String()
	assert.Contains(t, output, "protected")
	assert.Contains(t, output, "PR #1")
	assert.Contains(t, output, "Merge the PR")
}

func TestApplyPerRepoScaffold_ProtectedBranch_ExistingBranch(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.ExistingBranches["acme/widget/fullsend/scaffold-install"] = true
	var buf bytes.Buffer
	printer := ui.New(&buf)

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil, scaffoldOptions{direct: true})
	require.NoError(t, err)

	require.Len(t, client.CommittedFilesToBranch, 1, "should proceed despite branch existing")
	require.Len(t, client.CreatedProposals, 1)
}

func TestApplyPerRepoScaffold_ProtectedBranch_StillSetsVarsAndSecrets(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	printer := ui.New(&bytes.Buffer{})

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}
	repoVars := map[string]string{"FULLSEND_MINT_URL": "https://mint.example.run.app"}
	repoSecrets := map[string]string{"FULLSEND_GCP_PROJECT_ID": "my-project"}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, repoVars, repoSecrets, scaffoldOptions{direct: true})
	require.NoError(t, err)

	assert.Len(t, client.Variables, 1, "variables should be set even with PR fallback")
	assert.Len(t, client.CreatedSecrets, 1, "secrets should be set even with PR fallback")
}

func TestApplyPerRepoScaffold_ProtectedBranch_CreateBranchFails(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CreateBranch"] = fmt.Errorf("forbidden")
	printer := ui.New(&bytes.Buffer{})

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil, scaffoldOptions{direct: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating scaffold branch")
}

func TestApplyPerRepoScaffold_ProtectedBranch_CommitToBranchFails(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CommitFilesToBranch"] = fmt.Errorf("server error")
	printer := ui.New(&bytes.Buffer{})

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil, scaffoldOptions{direct: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "committing scaffold files to branch")
}

func TestApplyPerRepoScaffold_ProtectedBranch_ScaffoldBranchAlsoProtected(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CommitFilesToBranch"] = fmt.Errorf("%w: scaffold branch also protected", forge.ErrBranchProtected)
	printer := ui.New(&bytes.Buffer{})

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil, scaffoldOptions{direct: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is protected")
	assert.Contains(t, err.Error(), "configure branch protection")
}

func TestApplyPerRepoScaffold_ProtectedBranch_CreatePRFails(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CreateChangeProposal"] = fmt.Errorf("forbidden")
	printer := ui.New(&bytes.Buffer{})

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil, scaffoldOptions{direct: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating scaffold PR")
}

func TestApplyPerRepoScaffold_ProtectedBranch_DuplicatePR(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CreateChangeProposal"] = fmt.Errorf("pr: %w", forge.ErrAlreadyExists)
	var buf bytes.Buffer
	printer := ui.New(&buf)

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil, scaffoldOptions{direct: true})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "already exists")
	assert.Contains(t, output, "Merge the PR")
}

func TestLoadKnownSlugs_HarnessFiles(t *testing.T) {
	client := forge.NewFakeClient()
	client.DirContents["myorg/.fullsend/harness@HEAD"] = []forge.DirectoryEntry{
		{Path: "harness/triage.yaml", Type: "file"},
		{Path: "harness/coder.yaml", Type: "file"},
	}
	client.FileContentsRef["myorg/.fullsend/harness/triage.yaml@HEAD"] = []byte("role: triage\nslug: fullsend-ai-triage\n")
	client.FileContentsRef["myorg/.fullsend/harness/coder.yaml@HEAD"] = []byte("role: coder\nslug: fullsend-ai-coder\n")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	slugs := loadKnownSlugs(context.Background(), client, "myorg", forge.ConfigRepoName, "HEAD", printer)

	assert.Equal(t, map[string]string{
		"triage": "fullsend-ai-triage",
		"coder":  "fullsend-ai-coder",
	}, slugs)
}

func TestLoadKnownSlugs_NoHarnessFiles_ReturnsNil(t *testing.T) {
	client := forge.NewFakeClient()

	var buf bytes.Buffer
	printer := ui.New(&buf)
	slugs := loadKnownSlugs(context.Background(), client, "myorg", forge.ConfigRepoName, "HEAD", printer)

	assert.Nil(t, slugs)
}

func TestLoadKnownSlugs_HarnessFilesWithoutRoleSlug_ReturnsNil(t *testing.T) {
	client := forge.NewFakeClient()
	client.DirContents["myorg/.fullsend/harness@HEAD"] = []forge.DirectoryEntry{
		{Path: "harness/triage.yaml", Type: "file"},
	}
	client.FileContentsRef["myorg/.fullsend/harness/triage.yaml@HEAD"] = []byte("agent: agents/triage.md\nmodel: opus\n")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	slugs := loadKnownSlugs(context.Background(), client, "myorg", forge.ConfigRepoName, "HEAD", printer)

	assert.Nil(t, slugs)
}

func TestLoadKnownSlugs_DuplicateRoles_FirstWins(t *testing.T) {
	client := forge.NewFakeClient()
	client.DirContents["myorg/.fullsend/harness@HEAD"] = []forge.DirectoryEntry{
		{Path: "harness/code.yaml", Type: "file"},
		{Path: "harness/fix.yaml", Type: "file"},
	}
	// Both files declare role: coder. DiscoverRemoteAgents sorts by Role then
	// Filename, so code.yaml comes first.
	client.FileContentsRef["myorg/.fullsend/harness/code.yaml@HEAD"] = []byte("role: coder\nslug: fullsend-ai-coder\n")
	client.FileContentsRef["myorg/.fullsend/harness/fix.yaml@HEAD"] = []byte("role: coder\nslug: fullsend-ai-fix\n")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	slugs := loadKnownSlugs(context.Background(), client, "myorg", forge.ConfigRepoName, "HEAD", printer)

	assert.Equal(t, map[string]string{
		"coder": "fullsend-ai-coder",
	}, slugs)
	assert.Contains(t, buf.String(), "duplicate role")
}

func TestLoadKnownSlugs_PartialError_LogsWarning(t *testing.T) {
	client := forge.NewFakeClient()
	client.DirContents["myorg/.fullsend/harness@HEAD"] = []forge.DirectoryEntry{
		{Path: "harness/triage.yaml", Type: "file"},
		{Path: "harness/bad.yaml", Type: "file"},
	}
	client.FileContentsRef["myorg/.fullsend/harness/triage.yaml@HEAD"] = []byte("role: triage\nslug: fullsend-ai-triage\n")
	// bad.yaml is not in FileContentsRef → GetFileContentAtRef returns ErrNotFound.

	var buf bytes.Buffer
	printer := ui.New(&buf)
	slugs := loadKnownSlugs(context.Background(), client, "myorg", forge.ConfigRepoName, "HEAD", printer)

	assert.Equal(t, map[string]string{
		"triage": "fullsend-ai-triage",
	}, slugs)
	assert.Contains(t, buf.String(), "harness discovery")
}

func TestLoadKnownSlugs_RoleWithoutSlug_WarnsAndSkips(t *testing.T) {
	client := forge.NewFakeClient()
	client.DirContents["myorg/.fullsend/harness@HEAD"] = []forge.DirectoryEntry{
		{Path: "harness/triage.yaml", Type: "file"},
	}
	client.FileContentsRef["myorg/.fullsend/harness/triage.yaml@HEAD"] = []byte("role: triage\n")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	slugs := loadKnownSlugs(context.Background(), client, "myorg", forge.ConfigRepoName, "HEAD", printer)

	assert.Nil(t, slugs)
	assert.Contains(t, buf.String(), "both must be set")
}

func TestCheckTokenScopes_InstallationTokenSkipped(t *testing.T) {
	client := forge.NewFakeClient()
	client.InstallationToken = true

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := checkTokenScopes(context.Background(), client, printer, []string{"repo", "delete_repo", "workflow"})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "installation token")
}

func TestCheckTokenScopes_MissingScopes(t *testing.T) {
	client := forge.NewFakeClient()
	client.TokenScopes = []string{"repo"}

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := checkTokenScopes(context.Background(), client, printer, []string{"repo", "delete_repo"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete_repo")
}

func TestCheckTokenScopes_InstallationTokenProbeFails(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors["IsInstallationToken"] = fmt.Errorf("network down")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := checkTokenScopes(context.Background(), client, printer, []string{"repo"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "detecting installation token")
}

func TestLoadKnownSlugs_HardError_ReturnsNil(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors["ListDirectoryContents"] = fmt.Errorf("network timeout")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	slugs := loadKnownSlugs(context.Background(), client, "myorg", forge.ConfigRepoName, "HEAD", printer)

	assert.Nil(t, slugs)
	assert.Contains(t, buf.String(), "harness discovery")
}

func TestApplyPerRepoScaffold_ProtectedBranch_BranchUpToDate(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CreateChangeProposal"] = fmt.Errorf("PR: %w", forge.ErrAlreadyExists)
	noChange := false
	client.CommitFilesChanged = &noChange
	var buf bytes.Buffer
	printer := ui.New(&buf)

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil, scaffoldOptions{direct: true})
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "up to date")
}

func TestGCFWIFAdapter_DiscoverMint_Success(t *testing.T) {
	fakeClient := gcf.NewFakeGCFClient(gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
		URI: "https://mint.example.run.app",
		EnvVars: map[string]string{
			"ROLE_APP_IDS":       `{"coder":"100","triage":"200"}`,
			"PER_REPO_WIF_REPOS": "acme/widget,acme/api",
		},
	}))
	prov := gcf.NewProvisioner(gcf.Config{
		ProjectID:  "test-project",
		GitHubOrgs: []string{"acme"},
		Repo:       "acme/widget",
	}, fakeClient)
	adapter := &gcfProvisionerAdapter{provisioner: prov}

	d, err := adapter.DiscoverMint(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://mint.example.run.app", d.URL)
	assert.Equal(t, "100", d.RoleAppIDs["coder"])
	assert.Equal(t, "200", d.RoleAppIDs["triage"])
	assert.Contains(t, d.PerRepoWIFRepos, "acme/widget")
	assert.Contains(t, d.PerRepoWIFRepos, "acme/api")
}

func TestGCFWIFAdapter_DiscoverMint_NilProvisioner(t *testing.T) {
	adapter := &gcfProvisionerAdapter{provisioner: nil}
	_, err := adapter.DiscoverMint(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, errMintNotFound))
}

func TestGCFWIFAdapter_DiscoverMint_FunctionNotFound(t *testing.T) {
	fakeClient := gcf.NewFakeGCFClient(gcf.WithFakeErrors(map[string]error{
		"GetFunction": fmt.Errorf("checking mint function: %w", gcf.ErrFunctionNotFound),
	}))
	prov := gcf.NewProvisioner(gcf.Config{
		ProjectID:  "test-project",
		GitHubOrgs: []string{"acme"},
		Repo:       "acme/widget",
	}, fakeClient)
	adapter := &gcfProvisionerAdapter{provisioner: prov}

	_, err := adapter.DiscoverMint(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, errMintNotFound),
		"expected errMintNotFound, got: %v", err)
	assert.True(t, errors.Is(err, gcf.ErrFunctionNotFound),
		"original gcf error should be preserved in chain, got: %v", err)
}

func TestGCFWIFAdapter_DiscoverMint_OtherError(t *testing.T) {
	fakeClient := gcf.NewFakeGCFClient(gcf.WithFakeErrors(map[string]error{
		"GetFunction": fmt.Errorf("permission denied"),
	}))
	prov := gcf.NewProvisioner(gcf.Config{
		ProjectID:  "test-project",
		GitHubOrgs: []string{"acme"},
		Repo:       "acme/widget",
	}, fakeClient)
	adapter := &gcfProvisionerAdapter{provisioner: prov}

	_, err := adapter.DiscoverMint(context.Background())
	require.Error(t, err)
	assert.False(t, errors.Is(err, errMintNotFound),
		"non-function-not-found errors should not be translated")
}

func TestGCFWIFAdapter_ProvisionWIF_NilProvisioner(t *testing.T) {
	adapter := &gcfProvisionerAdapter{provisioner: nil}
	_, err := adapter.ProvisionWIF(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

func TestGCFWIFAdapter_RegisterPerRepoWIF_NilProvisioner(t *testing.T) {
	adapter := &gcfProvisionerAdapter{provisioner: nil}
	err := adapter.RegisterPerRepoWIF(context.Background(), "acme/widget")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

func TestGCFWIFAdapter_DeletePerRepoWIF_NilProvisioner(t *testing.T) {
	adapter := &gcfProvisionerAdapter{provisioner: nil}
	err := adapter.DeletePerRepoWIF(context.Background(), "acme/widget")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

func TestGCFWIFAdapter_DeleteWIFProvider_NilProvisioner(t *testing.T) {
	adapter := &gcfProvisionerAdapter{provisioner: nil}
	err := adapter.DeleteWIFProvider(context.Background(), "acme/widget")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

func TestGCFWIFAdapter_DeleteWIFProvider_Success(t *testing.T) {
	fakeClient := gcf.NewFakeGCFClient()
	prov := gcf.NewProvisioner(gcf.Config{
		ProjectID:   "test-project",
		GitHubOrgs:  []string{"acme"},
		Repo:        "acme/widget",
		WIFPoolName: "fullsend-pool",
	}, fakeClient)
	adapter := &gcfProvisionerAdapter{provisioner: prov}

	err := adapter.DeleteWIFProvider(context.Background(), "acme/widget")
	require.NoError(t, err)
}

func TestGCFWIFAdapter_ProvisionWIF_Success(t *testing.T) {
	fakeClient := gcf.NewFakeGCFClient()
	prov := gcf.NewProvisioner(gcf.Config{
		ProjectID:   "test-project",
		GitHubOrgs:  []string{"acme"},
		Repo:        "acme/widget",
		WIFPoolName: "fullsend-pool",
	}, fakeClient)
	adapter := &gcfProvisionerAdapter{provisioner: prov}

	provider, err := adapter.ProvisionWIF(context.Background())
	require.NoError(t, err)
	assert.Contains(t, provider, "fullsend-pool")
}

func TestGCFWIFAdapter_RegisterPerRepoWIF_Success(t *testing.T) {
	fakeClient := gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.run.app",
			EnvVars: map[string]string{},
		}),
	)
	prov := gcf.NewProvisioner(gcf.Config{
		ProjectID:  "test-project",
		GitHubOrgs: []string{"acme"},
	}, fakeClient)
	adapter := &gcfProvisionerAdapter{provisioner: prov}

	err := adapter.RegisterPerRepoWIF(context.Background(), "acme/widget")
	require.NoError(t, err)
}

func TestGCFWIFAdapter_DeletePerRepoWIF_Success(t *testing.T) {
	fakeClient := gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI: "https://mint.example.run.app",
			EnvVars: map[string]string{
				"PER_REPO_WIF_REPOS": "acme/widget,acme/api",
			},
		}),
	)
	prov := gcf.NewProvisioner(gcf.Config{
		ProjectID:  "test-project",
		GitHubOrgs: []string{"acme"},
	}, fakeClient)
	adapter := &gcfProvisionerAdapter{provisioner: prov}

	err := adapter.DeletePerRepoWIF(context.Background(), "acme/widget")
	require.NoError(t, err)
}

// nonGitHubClient wraps forge.Client without GitHubExtensions.
type nonGitHubClient struct {
	forge.Client
}

func TestResolveSharedRoleAppIDs_NonGitHub_ReturnsError(t *testing.T) {
	inner := forge.NewFakeClient()
	client := &nonGitHubClient{Client: inner}

	existingIDs := map[string]string{"coder": "100"}
	_, err := resolveSharedRoleAppIDs(context.Background(), client, existingIDs, "acme", []string{"coder"})
	require.Error(t, err)
	assert.True(t, forge.IsNotSupported(err))
}

func TestDetectSharedApps_NonGitHub_ReturnsRoleOnlyIDs(t *testing.T) {
	old := detectSharedAppsGCFClientFactory
	detectSharedAppsGCFClientFactory = func(string) gcf.GCFClient {
		return gcf.NewFakeGCFClient(gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}))
	}
	t.Cleanup(func() { detectSharedAppsGCFClientFactory = old })

	inner := forge.NewFakeClient()
	client := &nonGitHubClient{Client: inner}

	slugs, roleIDs, err := detectSharedApps(context.Background(), client, ui.New(&strings.Builder{}), "acme", []string{"coder"}, "mint-project", "us-central1")
	require.NoError(t, err)
	assert.Nil(t, slugs)
	assert.Equal(t, map[string]string{"coder": "100"}, roleIDs)
}

func TestToAgentCredentials(t *testing.T) {
	ac := &appsetup.AppCredentials{
		AppID:    42,
		Slug:     "test-slug",
		Name:     "test-name",
		PEM:      "pem-data",
		ClientID: "client-id",
	}

	cred := toAgentCredentials("triage", ac)

	assert.Equal(t, "triage", cred.Role)
	assert.Equal(t, "test-name", cred.Name)
	assert.Equal(t, "test-slug", cred.Slug)
	assert.Equal(t, "pem-data", cred.PEM)
	assert.Equal(t, "client-id", cred.ClientID)
	assert.Equal(t, 42, cred.AppID)
}
