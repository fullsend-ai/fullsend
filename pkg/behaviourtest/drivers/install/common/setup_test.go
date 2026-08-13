package common

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func noopLogf(string, ...any) {}

func TestRunGitHubSetup_NoGCPProject(t *testing.T) {
	var calls [][]string
	runCLI := func(binary, token string, args ...string) (string, error) {
		calls = append(calls, args)
		return "", nil
	}

	err := RunGitHubSetup("/usr/bin/fullsend", "tok", "org/repo", "https://mint.test", "", runCLI, noopLogf)
	require.NoError(t, err)

	require.Len(t, calls, 1, "expected a single github setup call")
	assert.Equal(t, "github", calls[0][0])
	assert.Equal(t, "setup", calls[0][1])
	assert.NotContains(t, calls[0], "--inference-project")
}

func TestRunGitHubSetup_SkipsProvisionWhenProviderExists(t *testing.T) {
	var calls [][]string
	runCLI := func(binary, token string, args ...string) (string, error) {
		calls = append(calls, args)
		if len(args) >= 2 && args[0] == "inference" && args[1] == "status" {
			return `{"status":"healthy","FULLSEND_GCP_WIF_PROVIDER":"projects/p/locations/l/providers/wif"}`, nil
		}
		return "", nil
	}

	err := RunGitHubSetup("/usr/bin/fullsend", "tok", "org/repo", "https://mint.test", "proj", runCLI, noopLogf)
	require.NoError(t, err)

	require.Len(t, calls, 2, "expected status + setup, no provision")
	assert.Equal(t, "status", calls[0][1])
	assert.Equal(t, "setup", calls[1][1])
	assert.Contains(t, calls[1], "--inference-wif-provider")
	assert.Contains(t, calls[1], "projects/p/locations/l/providers/wif")
}

func TestRunGitHubSetup_FallsBackToProvisionWhenProviderMissing(t *testing.T) {
	var calls [][]string
	statusCalls := 0
	runCLI := func(binary, token string, args ...string) (string, error) {
		calls = append(calls, args)
		if len(args) >= 2 && args[0] == "inference" && args[1] == "status" {
			statusCalls++
			if statusCalls == 1 {
				return "", fmt.Errorf("not found")
			}
			return `{"status":"healthy","FULLSEND_GCP_WIF_PROVIDER":"projects/p/locations/l/providers/wif"}`, nil
		}
		return "", nil
	}

	err := RunGitHubSetup("/usr/bin/fullsend", "tok", "org/repo", "https://mint.test", "proj", runCLI, noopLogf)
	require.NoError(t, err)

	require.Len(t, calls, 4, "expected status-fail, provision, status-ok, setup")
	assert.Equal(t, "status", calls[0][1])
	assert.Equal(t, "provision", calls[1][1])
	assert.Equal(t, "status", calls[2][1])
	assert.Equal(t, "setup", calls[3][1])
}

func TestRunGitHubSetup_ProvisionFails(t *testing.T) {
	runCLI := func(binary, token string, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "inference" && args[1] == "status" {
			return "", fmt.Errorf("not found")
		}
		if len(args) >= 2 && args[0] == "inference" && args[1] == "provision" {
			return "", fmt.Errorf("provision boom")
		}
		return "", nil
	}

	err := RunGitHubSetup("/usr/bin/fullsend", "tok", "org/repo", "https://mint.test", "proj", runCLI, noopLogf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provision boom")
}

func TestRunGitHubSetup_SetupCLIError(t *testing.T) {
	runCLI := func(binary, token string, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "github" && args[1] == "setup" {
			return "", fmt.Errorf("setup boom")
		}
		return "", nil
	}

	err := RunGitHubSetup("/usr/bin/fullsend", "tok", "org/repo", "https://mint.test", "", runCLI, noopLogf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setup boom")
}

func TestGetExistingInferenceWIFProvider_OK(t *testing.T) {
	runCLI := func(binary, token string, args ...string) (string, error) {
		return `{"status":"healthy","FULLSEND_GCP_WIF_PROVIDER":"projects/p/locations/l/providers/wif"}`, nil
	}

	got, err := GetExistingInferenceWIFProvider("/usr/bin/fullsend", "tok", "org/repo", "proj", runCLI, noopLogf)
	require.NoError(t, err)
	assert.Equal(t, "projects/p/locations/l/providers/wif", got)
}

func TestGetExistingInferenceWIFProvider_CLIError(t *testing.T) {
	runCLI := func(binary, token string, args ...string) (string, error) {
		return "", fmt.Errorf("boom")
	}

	_, err := GetExistingInferenceWIFProvider("/usr/bin/fullsend", "tok", "org/repo", "proj", runCLI, noopLogf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inference status")
}

func TestGetExistingInferenceWIFProvider_Unhealthy(t *testing.T) {
	runCLI := func(binary, token string, args ...string) (string, error) {
		return `{"status":"unhealthy","FULLSEND_GCP_WIF_PROVIDER":"projects/p/locations/l/providers/wif"}`, nil
	}

	_, err := GetExistingInferenceWIFProvider("/usr/bin/fullsend", "tok", "org/repo", "proj", runCLI, noopLogf)
	require.Error(t, err)
}
