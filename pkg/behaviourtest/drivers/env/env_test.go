package env

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadRunnerConfig_EnvironmentDefault(t *testing.T) {
	t.Setenv("ENVIRONMENT", "")

	cfg := LoadRunnerConfig()
	assert.Equal(t, "dev", cfg.Environment)
}

func TestLoadRunnerConfig_EnvironmentFromEnv(t *testing.T) {
	t.Setenv("ENVIRONMENT", "stage")

	cfg := LoadRunnerConfig()
	assert.Equal(t, "stage", cfg.Environment)
}

func TestLoadRunnerConfig_EnvironmentTrimsWhitespace(t *testing.T) {
	t.Setenv("ENVIRONMENT", "  stage  ")

	cfg := LoadRunnerConfig()
	assert.Equal(t, "stage", cfg.Environment)
}

func TestValidate_AcceptsDevStageAndGitHubEmulator(t *testing.T) {
	for _, envName := range []string{"dev", "stage", "github-emulator"} {
		t.Run(envName, func(t *testing.T) {
			cfg := RunnerConfig{
				SCM:         "github",
				CI:          "githubactions",
				InstallMode: "per-repo",
				Environment: envName,
			}
			require.NoError(t, cfg.Validate())
		})
	}
}

func TestValidate_AcceptsGitLabSCM(t *testing.T) {
	cfg := RunnerConfig{
		SCM:         "gitlab",
		CI:          "githubactions",
		InstallMode: "per-repo",
		Environment: "dev",
	}
	require.NoError(t, cfg.Validate())
}

func TestValidate_RejectsUnknownEnvironment(t *testing.T) {
	cfg := RunnerConfig{
		SCM:         "github",
		CI:          "githubactions",
		InstallMode: "per-repo",
		Environment: "prod",
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported ENVIRONMENT "prod"`)
}

func TestValidate_RejectsEmptyEnvironment(t *testing.T) {
	cfg := RunnerConfig{
		SCM:         "github",
		CI:          "githubactions",
		InstallMode: "per-repo",
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported ENVIRONMENT ""`)
}

func TestLoadRunnerConfig_DefaultsToGitHubDrivers(t *testing.T) {
	t.Setenv("ENVIRONMENT", "dev")
	t.Setenv("BEHAVIOUR_SCM", "")
	t.Setenv("BEHAVIOUR_CI", "")

	cfg := LoadRunnerConfig()
	assert.Equal(t, "github", cfg.SCM)
	assert.Equal(t, "githubactions", cfg.CI)
	require.NoError(t, cfg.Validate())
}

func TestLoadRunnerConfig_ForgejoDevDefaultsToForgejoDrivers(t *testing.T) {
	t.Setenv("ENVIRONMENT", "forgejo-dev")
	t.Setenv("BEHAVIOUR_SCM", "")
	t.Setenv("BEHAVIOUR_CI", "")

	cfg := LoadRunnerConfig()
	assert.Equal(t, "forgejo-dev", cfg.Environment)
	assert.Equal(t, "forgejo", cfg.SCM)
	assert.Equal(t, "forgejoactions", cfg.CI)
	require.NoError(t, cfg.Validate())
}

func TestLoadRunnerConfig_ForgejoDevKeepsExplicitSelections(t *testing.T) {
	t.Setenv("ENVIRONMENT", "forgejo-dev")
	t.Setenv("BEHAVIOUR_SCM", "github")
	t.Setenv("BEHAVIOUR_CI", "githubactions")

	cfg := LoadRunnerConfig()
	assert.Equal(t, "github", cfg.SCM, "an explicit selection is not overridden")
	assert.Equal(t, "githubactions", cfg.CI, "an explicit selection is not overridden")
	err := cfg.Validate()
	require.Error(t, err, "a GitHub selection must not run in forgejo-dev")
	assert.Contains(t, err.Error(), "ENVIRONMENT=forgejo-dev requires BEHAVIOUR_SCM=forgejo")
}

func TestValidate_AcceptsForgejoDev(t *testing.T) {
	cfg := RunnerConfig{
		SCM:         "forgejo",
		CI:          "forgejoactions",
		InstallMode: "per-repo",
		Environment: "forgejo-dev",
	}
	require.NoError(t, cfg.Validate())
}

func TestValidate_RejectsMixedForgejoSelections(t *testing.T) {
	tests := []struct {
		name        string
		scm         string
		ci          string
		environment string
		wantErr     string
	}{
		{
			name: "forgejo-dev with GitHub SCM", scm: "github", ci: "forgejoactions", environment: "forgejo-dev",
			wantErr: `ENVIRONMENT=forgejo-dev requires BEHAVIOUR_SCM=forgejo, got "github"`,
		},
		{
			name: "forgejo-dev with GitLab SCM", scm: "gitlab", ci: "forgejoactions", environment: "forgejo-dev",
			wantErr: `ENVIRONMENT=forgejo-dev requires BEHAVIOUR_SCM=forgejo, got "gitlab"`,
		},
		{
			name: "forgejo-dev with GitHub Actions CI", scm: "forgejo", ci: "githubactions", environment: "forgejo-dev",
			wantErr: `ENVIRONMENT=forgejo-dev requires BEHAVIOUR_CI=forgejoactions, got "githubactions"`,
		},
		{
			name: "Forgejo SCM in dev", scm: "forgejo", ci: "githubactions", environment: "dev",
			wantErr: `BEHAVIOUR_SCM=forgejo requires ENVIRONMENT=forgejo-dev, got "dev"`,
		},
		{
			name: "Forgejo Actions CI in stage", scm: "github", ci: "forgejoactions", environment: "stage",
			wantErr: `BEHAVIOUR_CI=forgejoactions requires ENVIRONMENT=forgejo-dev, got "stage"`,
		},
		{
			name: "Forgejo SCM and CI in dev", scm: "forgejo", ci: "forgejoactions", environment: "dev",
			wantErr: `BEHAVIOUR_SCM=forgejo requires ENVIRONMENT=forgejo-dev, got "dev"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := RunnerConfig{
				SCM:         tt.scm,
				CI:          tt.ci,
				InstallMode: "per-repo",
				Environment: tt.environment,
			}
			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidate_RejectsUnknownDriverValues(t *testing.T) {
	scmErr := RunnerConfig{SCM: "svn", CI: "forgejoactions", InstallMode: "per-repo", Environment: "forgejo-dev"}.Validate()
	require.Error(t, scmErr)
	assert.Contains(t, scmErr.Error(), `unsupported BEHAVIOUR_SCM "svn"`)

	ciErr := RunnerConfig{SCM: "forgejo", CI: "tekton", InstallMode: "per-repo", Environment: "forgejo-dev"}.Validate()
	require.Error(t, ciErr)
	assert.Contains(t, ciErr.Error(), `unsupported BEHAVIOUR_CI "tekton"`)

	envErr := RunnerConfig{SCM: "forgejo", CI: "forgejoactions", InstallMode: "per-repo", Environment: "forgejo-ext"}.Validate()
	require.Error(t, envErr)
	assert.Contains(t, envErr.Error(), `unsupported ENVIRONMENT "forgejo-ext"`)
}
