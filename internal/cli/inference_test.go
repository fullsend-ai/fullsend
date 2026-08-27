package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/dispatch/gcf"
)

func TestInferenceCommand_HasSubcommands(t *testing.T) {
	cmd := newInferenceCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	assert.True(t, names["provision"], "expected provision subcommand")
	assert.True(t, names["status"], "expected status subcommand")
	assert.True(t, names["deprovision"], "expected deprovision subcommand")
	assert.False(t, names["enroll"], "enroll is handled by provision, not a separate subcommand")
	assert.False(t, names["unenroll"], "unenroll replaced by deprovision")
}

func TestInferenceCommand_RegisteredInRoot(t *testing.T) {
	cmd := newRootCmd()
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Use == "inference" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected inference subcommand registered in root")
}

// --- provision tests ---

func TestInferenceProvisionCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "provision"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestInferenceProvisionCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "provision", "acme"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestInferenceProvisionCmd_RejectsInvalidProjectID(t *testing.T) {
	tests := []struct {
		name    string
		project string
	}{
		{"uppercase", "MY-PROJECT"},
		{"too short", "ab"},
		{"starts with digit", "1project"},
		{"starts with hyphen", "-project"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newRootCmd()
			cmd.SetArgs([]string{"inference", "provision", "acme",
				"--project", tc.project, "--dry-run"})
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid GCP project ID")
		})
	}
}

func TestInferenceProvisionCmd_Flags(t *testing.T) {
	cmd := newInferenceProvisionCmd()

	projectFlag := cmd.Flags().Lookup("project")
	require.NotNil(t, projectFlag, "expected --project flag")

	poolFlag := cmd.Flags().Lookup("pool")
	require.NotNil(t, poolFlag, "expected --pool flag")
	assert.Equal(t, "fullsend-inference", poolFlag.DefValue)

	providerFlag := cmd.Flags().Lookup("provider")
	require.NotNil(t, providerFlag, "expected --provider flag")
	assert.Equal(t, "github-oidc", providerFlag.DefValue)

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag, "expected --dry-run flag")

	assert.Nil(t, cmd.Flags().Lookup("region"), "should not have --region flag")
}

func TestInferenceProvisionCmd_DetectsOrgMode(t *testing.T) {
	// Org-scoped: arg without "/"
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "provision", "acme",
		"--project", "my-project",
		"--dry-run"})
	err := cmd.Execute()
	// Should succeed (dry-run prints what would happen)
	require.NoError(t, err)
}

func TestInferenceProvisionCmd_DetectsRepoMode(t *testing.T) {
	// Repo-scoped: arg with "/"
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "provision", "acme/widget",
		"--project", "my-project",
		"--dry-run"})
	err := cmd.Execute()
	// Should succeed (dry-run prints what would happen)
	require.NoError(t, err)
}

func TestInferenceProvisionCmd_DryRunOrgSucceeds(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "provision", "acme",
		"--project", "my-project",
		"--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestInferenceProvisionCmd_DryRunRepoSucceeds(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "provision", "acme/widget",
		"--project", "my-project",
		"--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestInferenceProvisionCmd_DryRunCustomPool(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "provision", "acme",
		"--project", "my-project",
		"--pool", "custom-pool",
		"--provider", "custom-provider",
		"--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestInferenceProvisionCmd_RejectsInvalidOrgName(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "provision", "-invalid",
		"--project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid")
}

func TestInferenceProvisionCmd_RejectsPlaceholderOrg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "provision", "x0fullsend0placeholder",
		"--project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot provision reserved placeholder org")
}

func TestInferenceProvisionCmd_RejectsPlaceholderOrgInRepoMode(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "provision", "x0fullsend0placeholder/somerepo",
		"--project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot provision reserved placeholder org")
}

func TestInferenceProvisionCmd_RejectsInvalidRepoFormat(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "provision", "acme/",
		"--project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid")
}

func TestInferenceProvisionCmd_DoesNotRequireGitHubToken(t *testing.T) {
	// Unset all GitHub tokens to prove they're not needed.
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "provision", "acme",
		"--project", "my-project",
		"--dry-run"})
	err := cmd.Execute()
	// Should not fail with "no GitHub token found"
	require.NoError(t, err)
}

// --- status tests ---

func TestInferenceStatusCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "status"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestInferenceStatusCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "status", "acme"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestInferenceStatusCmd_RejectsInvalidProjectID(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "status", "acme",
		"--project", "UPPER-CASE"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP project ID")
}

func TestInferenceStatusCmd_Flags(t *testing.T) {
	cmd := newInferenceStatusCmd()

	projectFlag := cmd.Flags().Lookup("project")
	require.NotNil(t, projectFlag, "expected --project flag")

	poolFlag := cmd.Flags().Lookup("pool")
	require.NotNil(t, poolFlag, "expected --pool flag")
	assert.Equal(t, "fullsend-inference", poolFlag.DefValue)

	providerFlag := cmd.Flags().Lookup("provider")
	require.NotNil(t, providerFlag, "expected --provider flag")
	assert.Equal(t, "github-oidc", providerFlag.DefValue)

	formatFlag := cmd.Flags().Lookup("format")
	require.NotNil(t, formatFlag, "expected --format flag")
	assert.Equal(t, "text", formatFlag.DefValue)

	assert.Nil(t, cmd.Flags().Lookup("region"), "should not have --region flag")
}

func TestInferenceStatusCmd_RejectsInvalidFormat(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "status", "acme",
		"--project", "my-project",
		"--format", "yaml"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--format must be one of: text, json, env")
}

func TestInferenceStatusCmd_DoesNotRequireGitHubToken(t *testing.T) {
	// Unset all GitHub tokens to prove they're not needed.
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	// Status without dry-run will try to reach GCP, which will fail,
	// but it should NOT fail with "no GitHub token found".
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "status", "acme",
		"--project", "my-project"})
	err := cmd.Execute()
	if err != nil {
		assert.NotContains(t, err.Error(), "no GitHub token found")
	}
}

// --- parseOrgOrRepo tests ---

func TestParseOrgOrRepo_OrgMode(t *testing.T) {
	org, repo, err := parseOrgOrRepo("acme")
	require.NoError(t, err)
	assert.Equal(t, "acme", org)
	assert.Equal(t, "", repo)
}

func TestParseOrgOrRepo_RepoMode(t *testing.T) {
	org, repo, err := parseOrgOrRepo("acme/widget")
	require.NoError(t, err)
	assert.Equal(t, "acme", org)
	assert.Equal(t, "acme/widget", repo)
}

func TestParseOrgOrRepo_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty owner in repo", "/widget", "invalid"},
		{"empty repo in repo", "acme/", "invalid"},
		{"leading hyphen", "-acme", "hyphen"},
		{"trailing hyphen", "acme-", "hyphen"},
		{"invalid chars", "ac me", "invalid"},
		{"dots in owner", "ac.me/widget", "invalid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseOrgOrRepo(tc.input)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// --- formatStatusJSON tests ---

func TestFormatStatusJSON(t *testing.T) {
	result := &inferenceStatusResult{
		Status:      "healthy",
		ProjectID:   "my-project",
		WIFProvider: "projects/123/locations/global/workloadIdentityPools/fullsend-inference/providers/github-oidc",
		Details:     []string{"Project number: 123", "WIF provider: found"},
	}

	output, err := formatStatusJSON(result)
	require.NoError(t, err)

	var parsed map[string]interface{}
	err = json.Unmarshal([]byte(output), &parsed)
	require.NoError(t, err)

	assert.Equal(t, "healthy", parsed["status"])
	assert.Equal(t, "my-project", parsed["FULLSEND_GCP_PROJECT_ID"])
	assert.Equal(t, "projects/123/locations/global/workloadIdentityPools/fullsend-inference/providers/github-oidc", parsed["FULLSEND_GCP_WIF_PROVIDER"])
	details, ok := parsed["details"].([]interface{})
	require.True(t, ok, "expected details to be an array")
	assert.Len(t, details, 2)
}

func TestFormatStatusJSON_Unhealthy(t *testing.T) {
	result := &inferenceStatusResult{
		Status:    "error",
		ProjectID: "my-project",
		Details:   []string{"Failed to get project number"},
	}

	output, err := formatStatusJSON(result)
	require.NoError(t, err)

	var parsed map[string]interface{}
	err = json.Unmarshal([]byte(output), &parsed)
	require.NoError(t, err)

	assert.Equal(t, "error", parsed["status"])
	assert.Nil(t, parsed["FULLSEND_GCP_PROJECT_ID"], "should not include config keys when unhealthy")
	assert.Nil(t, parsed["FULLSEND_GCP_WIF_PROVIDER"], "should not include config keys when unhealthy")
}

// --- formatStatusEnv tests ---

func TestFormatStatusEnv(t *testing.T) {
	result := &inferenceStatusResult{
		Status:      "healthy",
		ProjectID:   "my-project",
		WIFProvider: "projects/123/locations/global/workloadIdentityPools/fullsend-inference/providers/github-oidc",
	}

	output := formatStatusEnv(result)
	assert.Contains(t, output, "FULLSEND_INFERENCE_STATUS=healthy")
	assert.Contains(t, output, "FULLSEND_GCP_PROJECT_ID=my-project")
	assert.Contains(t, output, "FULLSEND_GCP_WIF_PROVIDER=projects/123/locations/global/workloadIdentityPools/fullsend-inference/providers/github-oidc")
	assert.NotContains(t, output, "FULLSEND_GCP_REGION")
	assert.NotContains(t, output, "Status:")
}

func TestFormatStatusEnv_Unhealthy(t *testing.T) {
	result := &inferenceStatusResult{
		Status:    "unhealthy",
		ProjectID: "my-project",
	}

	output := formatStatusEnv(result)
	assert.Contains(t, output, "FULLSEND_INFERENCE_STATUS=unhealthy")
	assert.NotContains(t, output, "FULLSEND_GCP_PROJECT_ID")
	assert.NotContains(t, output, "FULLSEND_GCP_WIF_PROVIDER")
}

func TestInferenceStatusCmd_RejectsProviderInRepoMode(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "status", "acme/widget",
		"--project", "my-project",
		"--provider", "custom-provider"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--provider is not supported in repo-scoped mode")
}

func TestInferenceStatusCmd_RejectsPlaceholderOrg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "status", "x0fullsend0placeholder",
		"--project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot check status of reserved placeholder org")
}

func TestInferenceStatusCmd_RejectsPlaceholderOrgInRepoMode(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "status", "x0fullsend0placeholder/somerepo",
		"--project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot check status of reserved placeholder org")
}

func TestInferenceProvisionCmd_RejectsProviderInRepoMode(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "provision", "acme/widget",
		"--project", "my-project",
		"--provider", "custom-provider"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--provider is not supported in repo-scoped mode")
}

// --- deprovision tests ---

func TestInferenceDeprovisionCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "deprovision"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestInferenceDeprovisionCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "deprovision", "acme"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestInferenceDeprovisionCmd_RejectsInvalidProjectID(t *testing.T) {
	tests := []struct {
		name    string
		project string
	}{
		{"uppercase", "MY-PROJECT"},
		{"too short", "ab"},
		{"starts with digit", "1project"},
		{"starts with hyphen", "-project"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newRootCmd()
			cmd.SetArgs([]string{"inference", "deprovision", "acme",
				"--project", tc.project, "--dry-run"})
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid GCP project ID")
		})
	}
}

func TestInferenceDeprovisionCmd_DryRunOrgSucceeds(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "deprovision", "acme",
		"--project", "my-project",
		"--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestInferenceDeprovisionCmd_DryRunRepoSucceeds(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "deprovision", "acme/widget",
		"--project", "my-project",
		"--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestInferenceDeprovisionCmd_RejectsPlaceholderOrg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "deprovision", "x0fullsend0placeholder",
		"--project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot deprovision reserved placeholder org")
}

func TestInferenceDeprovisionCmd_RejectsPlaceholderOrgInRepoMode(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "deprovision", "x0fullsend0placeholder/somerepo",
		"--project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot deprovision reserved placeholder org")
}

func TestInferenceDeprovisionCmd_RejectsInvalidOrgName(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "deprovision", "-invalid",
		"--project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid")
}

func TestInferenceDeprovisionCmd_RejectsInvalidRepoFormat(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "deprovision", "acme/",
		"--project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid")
}

func TestInferenceDeprovisionCmd_RejectsProviderInRepoMode(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "deprovision", "acme/widget",
		"--project", "my-project",
		"--provider", "custom-provider"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--provider is not supported in repo-scoped mode")
}

func TestInferenceDeprovisionCmd_Flags(t *testing.T) {
	cmd := newInferenceDeprovisionCmd()

	projectFlag := cmd.Flags().Lookup("project")
	require.NotNil(t, projectFlag, "expected --project flag")

	poolFlag := cmd.Flags().Lookup("pool")
	require.NotNil(t, poolFlag, "expected --pool flag")
	assert.Equal(t, "fullsend-inference", poolFlag.DefValue)

	providerFlag := cmd.Flags().Lookup("provider")
	require.NotNil(t, providerFlag, "expected --provider flag")
	assert.Equal(t, "github-oidc", providerFlag.DefValue)

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag, "expected --dry-run flag")
}

func TestInferenceDeprovisionCmd_DoesNotRequireGitHubToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "deprovision", "acme",
		"--project", "my-project",
		"--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

// --- runInferenceStatus integration tests ---

func newStatusCmd(client gcf.GCFClient) (*cobra.Command, *bytes.Buffer) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	return cmd, &buf
}

func TestRunInferenceStatus_RepoConditionMatch(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeWIFProvider(&gcf.WIFProviderInfo{
			AttributeCondition: "assertion.repository == 'acme/widget'",
		}),
	)
	cmd, buf := newStatusCmd(client)
	err := runInferenceStatus(cmd, "acme", "acme/widget", "my-project", "fullsend-inference", "github-oidc", "json", client)
	require.NoError(t, err)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
	assert.Equal(t, "healthy", parsed["status"])
}

func TestRunInferenceStatus_RepoConditionCaseInsensitiveMatch(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeWIFProvider(&gcf.WIFProviderInfo{
			AttributeCondition: "assertion.repository == 'RedHatProductSecurity/osidb-bindings'",
		}),
	)
	cmd, buf := newStatusCmd(client)
	err := runInferenceStatus(cmd, "redhatproductsecurity", "redhatproductsecurity/osidb-bindings", "my-project", "fullsend-inference", "github-oidc", "json", client)
	require.NoError(t, err)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
	assert.Equal(t, "healthy", parsed["status"])
}

func TestRunInferenceStatus_RepoConditionMismatch(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeWIFProvider(&gcf.WIFProviderInfo{
			AttributeCondition: "assertion.repository == 'acme/widget'",
		}),
	)
	cmd, _ := newStatusCmd(client)
	err := runInferenceStatus(cmd, "acme", "acme/other-repo", "my-project", "fullsend-inference", "github-oidc", "json", client)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unhealthy")
}

func TestRunInferenceStatus_OrgConditionMatch(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeWIFProvider(&gcf.WIFProviderInfo{
			AttributeCondition: "assertion.repository_owner == 'acme'",
		}),
	)
	cmd, buf := newStatusCmd(client)
	err := runInferenceStatus(cmd, "acme", "", "my-project", "fullsend-inference", "github-oidc", "json", client)
	require.NoError(t, err)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
	assert.Equal(t, "healthy", parsed["status"])
}

func TestRunInferenceStatus_OrgConditionCaseInsensitiveMatch(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeWIFProvider(&gcf.WIFProviderInfo{
			AttributeCondition: "assertion.repository_owner == 'GoogleCloudPlatform'",
		}),
	)
	cmd, buf := newStatusCmd(client)
	err := runInferenceStatus(cmd, "googlecloudplatform", "", "my-project", "fullsend-inference", "github-oidc", "json", client)
	require.NoError(t, err)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
	assert.Equal(t, "healthy", parsed["status"])
}

func TestRunInferenceStatus_OrgMultiOrgPoolMatch(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeWIFProvider(&gcf.WIFProviderInfo{
			AttributeCondition: "assertion.repository_owner in ['acme', 'BigCorp']",
		}),
	)
	cmd, buf := newStatusCmd(client)
	err := runInferenceStatus(cmd, "bigcorp", "", "my-project", "fullsend-inference", "github-oidc", "json", client)
	require.NoError(t, err)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
	assert.Equal(t, "healthy", parsed["status"])
}

func TestRunInferenceStatus_OrgConditionMismatch(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeWIFProvider(&gcf.WIFProviderInfo{
			AttributeCondition: "assertion.repository_owner == 'acme'",
		}),
	)
	cmd, _ := newStatusCmd(client)
	err := runInferenceStatus(cmd, "other-org", "", "my-project", "fullsend-inference", "github-oidc", "json", client)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unhealthy")
}

func TestRunInferenceStatus_NotProvisioned(t *testing.T) {
	client := gcf.NewFakeGCFClient()
	cmd, buf := newStatusCmd(client)
	err := runInferenceStatus(cmd, "acme", "", "my-project", "fullsend-inference", "github-oidc", "json", client)
	require.NoError(t, err)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
	assert.Equal(t, "not_provisioned", parsed["status"])
}

func TestRunInferenceStatus_EnvFormat(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeWIFProvider(&gcf.WIFProviderInfo{
			AttributeCondition: "assertion.repository_owner == 'acme'",
		}),
	)
	cmd, buf := newStatusCmd(client)
	err := runInferenceStatus(cmd, "acme", "", "my-project", "fullsend-inference", "github-oidc", "env", client)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "FULLSEND_INFERENCE_STATUS=healthy")
}

func TestRunInferenceStatus_TextFormat(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeWIFProvider(&gcf.WIFProviderInfo{
			AttributeCondition: "assertion.repository_owner == 'acme'",
		}),
	)
	cmd, buf := newStatusCmd(client)
	err := runInferenceStatus(cmd, "acme", "", "my-project", "fullsend-inference", "github-oidc", "text", client)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Status: healthy")
}

// --- conditionMatchesRepo tests ---

func TestConditionMatchesRepo_ExactCase(t *testing.T) {
	assert.True(t, conditionMatchesRepo(
		"assertion.repository == 'acme/widget'",
		"acme/widget",
	))
}

func TestConditionMatchesRepo_MixedCaseOrg(t *testing.T) {
	// GitHub OIDC tokens preserve canonical display case.
	// A provision with mixed-case input writes a mixed-case condition,
	// and status should report it as healthy.
	assert.True(t, conditionMatchesRepo(
		"assertion.repository == 'RedHatProductSecurity/osidb-bindings'",
		"RedHatProductSecurity/osidb-bindings",
	))
}

func TestConditionMatchesRepo_CaseInsensitiveMatch(t *testing.T) {
	// Condition was provisioned with mixed case; status queried with lowercase.
	assert.True(t, conditionMatchesRepo(
		"assertion.repository == 'RedHatProductSecurity/osidb-bindings'",
		"redhatproductsecurity/osidb-bindings",
	))
	// Condition was provisioned with lowercase; status queried with mixed case.
	assert.True(t, conditionMatchesRepo(
		"assertion.repository == 'redhatproductsecurity/osidb-bindings'",
		"RedHatProductSecurity/osidb-bindings",
	))
}

func TestConditionMatchesRepo_Mismatch(t *testing.T) {
	assert.False(t, conditionMatchesRepo(
		"assertion.repository == 'acme/widget'",
		"acme/other-repo",
	))
}

// --- conditionMatchesOrg tests ---

func TestConditionMatchesOrg_ExactCase(t *testing.T) {
	assert.True(t, conditionMatchesOrg(
		"assertion.repository_owner == 'acme'",
		"acme",
	))
}

func TestConditionMatchesOrg_MixedCaseOrg(t *testing.T) {
	assert.True(t, conditionMatchesOrg(
		"assertion.repository_owner == 'GoogleCloudPlatform'",
		"GoogleCloudPlatform",
	))
}

func TestConditionMatchesOrg_CaseInsensitiveMatch(t *testing.T) {
	// Condition was provisioned with mixed case; status queried with lowercase.
	assert.True(t, conditionMatchesOrg(
		"assertion.repository_owner == 'GoogleCloudPlatform'",
		"googlecloudplatform",
	))
	// Condition was provisioned with lowercase; status queried with mixed case.
	assert.True(t, conditionMatchesOrg(
		"assertion.repository_owner == 'googlecloudplatform'",
		"GoogleCloudPlatform",
	))
}

func TestConditionMatchesOrg_MultiOrgPool(t *testing.T) {
	condition := "assertion.repository_owner in ['acme', 'BigCorp']"
	assert.True(t, conditionMatchesOrg(condition, "acme"))
	assert.True(t, conditionMatchesOrg(condition, "BigCorp"))
	assert.True(t, conditionMatchesOrg(condition, "bigcorp"))
	assert.True(t, conditionMatchesOrg(condition, "ACME"))
}

func TestConditionMatchesOrg_Mismatch(t *testing.T) {
	assert.False(t, conditionMatchesOrg(
		"assertion.repository_owner == 'acme'",
		"other-org",
	))
}
