package cli

import (
	"bufio"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestMintCommand_HasSubcommands(t *testing.T) {
	cmd := newMintCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Use] = true
	}
	assert.True(t, names["deploy"], "expected deploy subcommand")
	assert.True(t, names["enroll <org|owner/repo>"], "expected enroll subcommand")
	assert.True(t, names["unenroll <org|owner/repo>"], "expected unenroll subcommand")
	assert.True(t, names["status [org]"], "expected status subcommand")
}

func TestMintCommand_RegisteredInRoot(t *testing.T) {
	cmd := newRootCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	assert.True(t, names["mint"], "expected mint command registered in root")
}

// --- deploy command tests ---

func TestMintDeployCmd_Flags(t *testing.T) {
	cmd := newMintDeployCmd()

	projectFlag := cmd.Flags().Lookup("project")
	require.NotNil(t, projectFlag, "expected --project flag")
	assert.Equal(t, "", projectFlag.DefValue)

	regionFlag := cmd.Flags().Lookup("region")
	require.NotNil(t, regionFlag, "expected --region flag")
	assert.Equal(t, "us-central1", regionFlag.DefValue)

	sourceDirFlag := cmd.Flags().Lookup("source-dir")
	require.NotNil(t, sourceDirFlag, "expected --source-dir flag")

	skipDeployFlag := cmd.Flags().Lookup("skip-deploy")
	require.NotNil(t, skipDeployFlag, "expected --skip-deploy flag")

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag, "expected --dry-run flag")
}

func TestMintDeployCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestMintDeployCmd_InvalidProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=BAD"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP project ID")
}

func TestMintDeployCmd_InvalidRegion(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--region=invalid"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP region")
}

func TestMintDeployCmd_DryRun(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintDeployCmd_NoArgs(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--dry-run", "extra"})
	err := cmd.Execute()
	require.Error(t, err)
}

// --- enroll command tests ---

func TestMintEnrollCmd_Flags(t *testing.T) {
	cmd := newMintEnrollCmd()

	projectFlag := cmd.Flags().Lookup("project")
	require.NotNil(t, projectFlag, "expected --project flag")

	regionFlag := cmd.Flags().Lookup("region")
	require.NotNil(t, regionFlag, "expected --region flag")
	assert.Equal(t, "us-central1", regionFlag.DefValue)

	appSetFlag := cmd.Flags().Lookup("app-set")
	require.NotNil(t, appSetFlag, "expected --app-set flag")
	assert.Equal(t, "fullsend-ai", appSetFlag.DefValue)

	sourceOrgFlag := cmd.Flags().Lookup("source-org")
	require.NotNil(t, sourceOrgFlag, "expected deprecated --source-org alias")
	assert.Equal(t, "fullsend-ai", sourceOrgFlag.DefValue)
	assert.True(t, sourceOrgFlag.Hidden, "--source-org should be hidden")
	assert.NotEmpty(t, sourceOrgFlag.Deprecated, "--source-org should have a deprecation message")

	roleAppIDsFlag := cmd.Flags().Lookup("role-app-ids")
	require.NotNil(t, roleAppIDsFlag, "expected --role-app-ids flag")

	rolesFlag := cmd.Flags().Lookup("roles")
	require.NotNil(t, rolesFlag, "expected --roles flag")
	assert.Equal(t, strings.Join(config.DefaultAgentRoles(), ","), rolesFlag.DefValue)

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag, "expected --dry-run flag")
}

func TestMintEnrollCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "enroll"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestMintEnrollCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "enroll", "acme"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestMintEnrollCmd_InvalidProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "enroll", "acme", "--project=BAD"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP project ID")
}

// --- unenroll command tests ---

func TestMintUnenrollCmd_Flags(t *testing.T) {
	cmd := newMintUnenrollCmd()

	projectFlag := cmd.Flags().Lookup("project")
	require.NotNil(t, projectFlag, "expected --project flag")

	regionFlag := cmd.Flags().Lookup("region")
	require.NotNil(t, regionFlag, "expected --region flag")

	deleteSecretsFlag := cmd.Flags().Lookup("delete-secrets")
	require.NotNil(t, deleteSecretsFlag, "expected --delete-secrets flag")
	assert.Equal(t, "false", deleteSecretsFlag.DefValue)

	deleteProviderFlag := cmd.Flags().Lookup("delete-provider")
	require.NotNil(t, deleteProviderFlag, "expected --delete-provider flag")
	assert.Equal(t, "false", deleteProviderFlag.DefValue)

	yoloFlag := cmd.Flags().Lookup("yolo")
	require.NotNil(t, yoloFlag, "expected --yolo flag")
}

func TestMintUnenrollCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "unenroll"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestMintUnenrollCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "unenroll", "acme"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

// --- status command tests ---

func TestMintStatusCmd_Flags(t *testing.T) {
	cmd := newMintStatusCmd()

	projectFlag := cmd.Flags().Lookup("project")
	require.NotNil(t, projectFlag, "expected --project flag")

	regionFlag := cmd.Flags().Lookup("region")
	require.NotNil(t, regionFlag, "expected --region flag")
}

func TestMintStatusCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "status"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestMintStatusCmd_InvalidOrg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "status", "-org", "--project=my-project-id"})
	err := cmd.Execute()
	require.Error(t, err)
}

func TestMintStatusCmd_TooManyArgs(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "status", "org1", "org2", "--project=my-project-id"})
	err := cmd.Execute()
	require.Error(t, err)
}

// --- role aliasing tests ---

func TestResolveRole(t *testing.T) {
	assert.Equal(t, "coder", resolveRole("fix"))
	assert.Equal(t, "coder", resolveRole("coder"))
	assert.Equal(t, "triage", resolveRole("triage"))
	assert.Equal(t, "review", resolveRole("review"))
}

func TestParseAndResolveRoles_FixAlias(t *testing.T) {
	roles, err := parseAndResolveRoles("triage,fix,coder,review")
	require.NoError(t, err)

	// "fix" should be resolved to "coder" and deduplicated.
	assert.NotContains(t, roles, "fix")
	assert.Contains(t, roles, "coder")
	assert.Contains(t, roles, "triage")
	assert.Contains(t, roles, "review")

	// No duplicates.
	seen := make(map[string]bool)
	for _, r := range roles {
		assert.False(t, seen[r], "duplicate role: %s", r)
		seen[r] = true
	}
}

func TestParseAndResolveRoles_Sorted(t *testing.T) {
	roles, err := parseAndResolveRoles("review,triage,coder")
	require.NoError(t, err)

	sorted := make([]string, len(roles))
	copy(sorted, roles)
	sort.Strings(sorted)
	assert.Equal(t, sorted, roles, "roles should be sorted")
}

func TestParseAndResolveRoles_InvalidRole(t *testing.T) {
	_, err := parseAndResolveRoles("INVALID")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role name")
}

func TestDefaultMintRoles(t *testing.T) {
	roles := defaultMintRoles()
	assert.Equal(t, config.DefaultAgentRoles(), roles)
}

// --- resolveEnrollAppIDs tests ---

func TestResolveEnrollAppIDs_ExplicitJSON(t *testing.T) {
	result, err := resolveEnrollAppIDs(
		`{"coder":"111","triage":"222"}`,
		nil,
		"my-app-set",
		"target-org",
		[]string{"coder", "triage"},
	)
	require.NoError(t, err)
	assert.Equal(t, "111", result["target-org/coder"])
	assert.Equal(t, "222", result["target-org/triage"])
}

func TestResolveEnrollAppIDs_ExplicitJSON_InvalidJSON(t *testing.T) {
	_, err := resolveEnrollAppIDs(
		`{invalid`,
		nil,
		"my-app-set",
		"target-org",
		[]string{"coder"},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing --role-app-ids")
}

func TestResolveEnrollAppIDs_FromAppSet(t *testing.T) {
	existing := map[string]string{
		"my-app-set/coder":  "111",
		"my-app-set/triage": "222",
	}
	result, err := resolveEnrollAppIDs(
		"",
		existing,
		"my-app-set",
		"target-org",
		[]string{"coder", "triage"},
	)
	require.NoError(t, err)
	assert.Equal(t, "111", result["target-org/coder"])
	assert.Equal(t, "222", result["target-org/triage"])
}

func TestResolveEnrollAppIDs_TargetAlreadyRegistered(t *testing.T) {
	existing := map[string]string{
		"my-app-set/coder": "111",
		"target-org/coder": "999",
	}
	result, err := resolveEnrollAppIDs(
		"",
		existing,
		"my-app-set",
		"target-org",
		[]string{"coder"},
	)
	require.NoError(t, err)
	assert.Equal(t, "999", result["target-org/coder"], "should use target org's existing entry")
}

func TestResolveEnrollAppIDs_NoExistingIDs(t *testing.T) {
	_, err := resolveEnrollAppIDs(
		"",
		nil,
		"my-app-set",
		"target-org",
		[]string{"coder"},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no existing ROLE_APP_IDS")
}

func TestResolveEnrollAppIDs_RoleMissingFromAppSet(t *testing.T) {
	existing := map[string]string{
		"my-app-set/coder": "111",
	}
	_, err := resolveEnrollAppIDs(
		"",
		existing,
		"my-app-set",
		"target-org",
		[]string{"coder", "unknown-role"},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown-role")
	assert.Contains(t, err.Error(), "not found in app set")
}

// Covers per-repo enrollment where owner == appSet (e.g., fullsend-ai/repo --app-set=fullsend-ai).
// The org-level path blocks this case; repo-level allows it because the org owns the apps.
func TestResolveEnrollAppIDs_SelfEnroll(t *testing.T) {
	result, err := resolveEnrollAppIDs(
		"",
		map[string]string{"my-app-set/coder": "111"},
		"my-app-set",
		"my-app-set",
		[]string{"coder"},
	)
	require.NoError(t, err)
	assert.Equal(t, "111", result["my-app-set/coder"], "self-enroll should reuse existing entry")
}

// --- confirmUnenroll tests ---

func TestConfirmUnenroll_Match(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	reader := bufio.NewReader(strings.NewReader("acme-org\n"))
	err := confirmUnenroll(printer, "acme-org", reader, true)
	require.NoError(t, err)
}

func TestConfirmUnenroll_Mismatch(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	reader := bufio.NewReader(strings.NewReader("wrong-name\n"))
	err := confirmUnenroll(printer, "acme-org", reader, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirmation did not match")
}

func TestConfirmUnenroll_EOF(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	reader := bufio.NewReader(strings.NewReader(""))
	err := confirmUnenroll(printer, "acme-org", reader, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading confirmation")
}

func TestConfirmUnenroll_NonTerminal(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	reader := bufio.NewReader(strings.NewReader("acme-org\n"))
	err := confirmUnenroll(printer, "acme-org", reader, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdin is not a terminal")
}
