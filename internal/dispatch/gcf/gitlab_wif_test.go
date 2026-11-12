package gcf

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newGitLabTestProvisioner(fake *fakeGCFClient) *Provisioner {
	return NewProvisioner(Config{ProjectID: "my-project", WIFPoolName: DefaultInferencePool}, fake)
}

func testGitLabProject() GitLabProject {
	return GitLabProject{Path: "acme/team/app", ID: "12345", IssuerURL: "https://gitlab.example.com/"}
}

func TestBuildGitLabProviderID(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"acme/app", "gl-acme-app"},
		{"acme/team/app", "gl-acme-team-app"},
		{"Acme/Team_X/my.app", "gl-acme-team-x-my-app"},
		{"acme/a-very-long-subgroup-name/project", "gl-acme-a-very-long-subgroup-nam"},
		// Truncation landing on a separator trims the trailing hyphen.
		{"acme/abcdefghijklmnopqrstuvwx/zzz", "gl-acme-abcdefghijklmnopqrstuvwx"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			got := BuildGitLabProviderID(tc.path)
			assert.Equal(t, tc.want, got)
			assert.LessOrEqual(t, len(got), 32)
		})
	}
}

func TestValidateGitLabProjectPath(t *testing.T) {
	for _, ok := range []string{"acme/app", "acme/team/app", "a_b/c.d-e", "1group/2proj"} {
		assert.NoError(t, ValidateGitLabProjectPath(ok), ok)
	}
	for _, bad := range []string{"", "app", "acme/", "/app", "acme//app", "acme/app'", "acme/-app", "acme/.app",
		"acme/app.git", "acme/app.atom", "acme/a..b", "acme/app ", "acme\\app"} {
		assert.Error(t, ValidateGitLabProjectPath(bad), bad)
	}
}

func TestValidateGitLabProjectID(t *testing.T) {
	assert.NoError(t, ValidateGitLabProjectID("12345"))
	for _, bad := range []string{"", "0", "012", "-1", "12a", "1 2", "123456789012345678901"} {
		assert.Error(t, ValidateGitLabProjectID(bad), bad)
	}
}

func TestNormalizeGitLabIssuerURL(t *testing.T) {
	got, err := NormalizeGitLabIssuerURL("")
	require.NoError(t, err)
	assert.Equal(t, DefaultGitLabURL, got)

	got, err = NormalizeGitLabIssuerURL("https://gitlab.example.com/gitlab/")
	require.NoError(t, err)
	assert.Equal(t, "https://gitlab.example.com/gitlab", got)

	for _, bad := range []string{"http://gitlab.example.com", "gitlab.example.com", "https://",
		"https://user:pw@gitlab.example.com", "https://gitlab.example.com?x=1", "https://gitlab.example.com#f",
		"https://gitlab.example.com?", "://bad"} {
		_, err := NormalizeGitLabIssuerURL(bad)
		assert.Error(t, err, bad)
	}
}

func TestPlanGitLabWIF(t *testing.T) {
	plan, err := PlanGitLabWIF(testGitLabProject())
	require.NoError(t, err)
	assert.Equal(t, "gl-acme-team-app", plan.ProviderID)
	assert.Equal(t, "https://gitlab.example.com", plan.Config.IssuerURI)
	assert.Equal(t, []string{"fullsend"}, plan.Config.AllowedAudiences)
	assert.Equal(t, "assertion.project_id", plan.Config.AttributeMapping["google.subject"])
	assert.Equal(t, "assertion.project_path", plan.Config.AttributeMapping["attribute.project_path"])
	assert.Equal(t, "assertion.project_id == '12345' && assertion.project_path == 'acme/team/app'", plan.Config.AttributeCondition)
	assert.Equal(t, "attribute.project_path/acme/team/app", plan.PrincipalSetSuffix)

	_, err = PlanGitLabWIF(GitLabProject{Path: "acme/app"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GitLab project ID")

	_, err = PlanGitLabWIF(GitLabProject{Path: "app", ID: "1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GitLab project path")

	_, err = PlanGitLabWIF(GitLabProject{Path: "acme/app", ID: "1", IssuerURL: "http://x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GitLab URL")
}

func TestProvisionGitLabWIF_CreatesProviderAndBinding(t *testing.T) {
	fake := newFakeGCFClient()
	wif, err := newGitLabTestProvisioner(fake).ProvisionGitLabWIF(context.Background(), testGitLabProject())
	require.NoError(t, err)

	assert.Equal(t, "projects/123456789/locations/global/workloadIdentityPools/fullsend-inference/providers/gl-acme-team-app", wif)
	assert.Equal(t, []string{"GetProjectNumber", "GetWIFProvider", "CreateWIFPool", "CreateWIFProvider", "SetProjectIAMBinding"}, fake.calls)

	assert.Equal(t, "gl-acme-team-app", fake.lastWIFProviderID)
	cfg := fake.lastWIFProviderConfig
	assert.Equal(t, "https://gitlab.example.com", cfg.IssuerURI)
	assert.Equal(t, []string{"fullsend"}, cfg.AllowedAudiences)
	assert.Equal(t, "assertion.project_id", cfg.AttributeMapping["google.subject"])
	assert.NotContains(t, cfg.AttributeMapping, "attribute.repository")
	assert.Contains(t, cfg.AttributeCondition, "assertion.project_id == '12345'")
	assert.Contains(t, cfg.AttributeCondition, "assertion.project_path == 'acme/team/app'")

	require.Len(t, fake.projectIAMBindings, 1)
	assert.Equal(t, projectIAMBinding{
		ProjectID: "my-project",
		Member:    "principalSet://iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/fullsend-inference/attribute.project_path/acme/team/app",
		Role:      "roles/aiplatform.user",
	}, fake.projectIAMBindings[0])
}

func TestProvisionGitLabWIF_IdempotentWhenProviderMatches(t *testing.T) {
	plan, err := PlanGitLabWIF(testGitLabProject())
	require.NoError(t, err)
	fake := newFakeGCFClient()
	fake.wifProvider = &WIFProviderInfo{
		AttributeCondition: plan.Config.AttributeCondition,
		AllowedAudiences:   plan.Config.AllowedAudiences,
		IssuerURI:          plan.Config.IssuerURI,
		AttributeMapping:   gitlabAttributeMapping(),
		State:              WIFProviderStateActive,
	}

	_, err = newGitLabTestProvisioner(fake).ProvisionGitLabWIF(context.Background(), testGitLabProject())
	require.NoError(t, err)
	assert.Equal(t, []string{"GetProjectNumber", "GetWIFProvider", "SetProjectIAMBinding"}, fake.calls)
}

func TestProvisionGitLabWIF_UpdatesDriftedActiveProvider(t *testing.T) {
	plan, err := PlanGitLabWIF(testGitLabProject())
	require.NoError(t, err)
	fake := newFakeGCFClient()
	fake.wifProvider = &WIFProviderInfo{
		AttributeCondition: plan.Config.AttributeCondition,
		AllowedAudiences:   plan.Config.AllowedAudiences,
		IssuerURI:          plan.Config.IssuerURI,
		// Hand-created with the oversize sub mapping.
		AttributeMapping: map[string]string{"google.subject": "assertion.sub"},
		State:            WIFProviderStateActive,
	}

	_, err = newGitLabTestProvisioner(fake).ProvisionGitLabWIF(context.Background(), testGitLabProject())
	require.NoError(t, err)
	assert.Equal(t, []string{"GetProjectNumber", "GetWIFProvider", "CreateWIFPool", "UpdateWIFProvider", "SetProjectIAMBinding"}, fake.calls)
	assert.Equal(t, "assertion.project_id", fake.lastWIFProviderConfig.AttributeMapping["google.subject"])
}

func TestProvisionGitLabWIF_RecreatesDeletedProvider(t *testing.T) {
	plan, err := PlanGitLabWIF(testGitLabProject())
	require.NoError(t, err)
	fake := newFakeGCFClient()
	fake.wifProvider = &WIFProviderInfo{
		AttributeCondition: plan.Config.AttributeCondition,
		State:              "DELETED",
	}

	_, err = newGitLabTestProvisioner(fake).ProvisionGitLabWIF(context.Background(), testGitLabProject())
	require.NoError(t, err)
	assert.Equal(t, []string{"GetProjectNumber", "GetWIFProvider", "CreateWIFPool", "CreateWIFProvider", "SetProjectIAMBinding"}, fake.calls)
}

func TestProvisionGitLabWIF_RefusesProviderIDCollision(t *testing.T) {
	fake := newFakeGCFClient()
	fake.wifProvider = &WIFProviderInfo{
		AttributeCondition: "assertion.project_id == '999' && assertion.project_path == 'acme/team/other'",
		State:              WIFProviderStateActive,
	}

	_, err := newGitLabTestProvisioner(fake).ProvisionGitLabWIF(context.Background(), testGitLabProject())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "different project")
	assert.Equal(t, []string{"GetProjectNumber", "GetWIFProvider"}, fake.calls)
}

func TestProvisionGitLabWIF_Errors(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name    string
		cfg     Config
		gl      GitLabProject
		errs    map[string]error
		wantErr string
	}{
		{"missing project", Config{WIFPoolName: "p"}, testGitLabProject(), nil, "GCP project ID is required"},
		{"invalid project", Config{ProjectID: "BAD", WIFPoolName: "p"}, testGitLabProject(), nil, "invalid GCP project ID"},
		{"missing gitlab id", Config{ProjectID: "my-project", WIFPoolName: "p"}, GitLabProject{Path: "acme/app"}, nil, "invalid GitLab project ID"},
		{"project number", Config{ProjectID: "my-project", WIFPoolName: "p"}, testGitLabProject(), map[string]error{"GetProjectNumber": boom}, "getting project number"},
		{"get provider", Config{ProjectID: "my-project", WIFPoolName: "p"}, testGitLabProject(), map[string]error{"GetWIFProvider": boom}, "reading WIF provider"},
		{"create pool", Config{ProjectID: "my-project", WIFPoolName: "p"}, testGitLabProject(), map[string]error{"CreateWIFPool": boom}, "creating WIF pool"},
		{"create provider", Config{ProjectID: "my-project", WIFPoolName: "p"}, testGitLabProject(), map[string]error{"CreateWIFProvider": boom}, "creating WIF provider"},
		{"iam binding", Config{ProjectID: "my-project", WIFPoolName: "p"}, testGitLabProject(), map[string]error{"SetProjectIAMBinding": boom}, "granting Agent Platform access"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeGCFClient()
			maps.Copy(fake.errs, tc.errs)
			_, err := NewProvisioner(tc.cfg, fake).ProvisionGitLabWIF(context.Background(), tc.gl)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}

	t.Run("update provider", func(t *testing.T) {
		fake := newFakeGCFClient()
		fake.errs["UpdateWIFProvider"] = boom
		fake.wifProvider = &WIFProviderInfo{
			AttributeCondition: "assertion.project_path == 'acme/team/app'",
			State:              WIFProviderStateActive,
		}
		_, err := newGitLabTestProvisioner(fake).ProvisionGitLabWIF(context.Background(), testGitLabProject())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "updating WIF provider")
	})
}
