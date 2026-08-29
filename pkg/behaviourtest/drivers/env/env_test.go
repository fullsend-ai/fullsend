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

func TestValidate_AcceptsDevAndStage(t *testing.T) {
	for _, envName := range []string{"dev", "stage"} {
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
