package common

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultGitHubSetupOpts_HasVendor(t *testing.T) {
	opts := DefaultGitHubSetupOpts()
	assert.True(t, opts.Vendor, "default should enable vendoring")
	assert.Empty(t, opts.FullsendRef, "default should not set a fullsend ref")
	assert.Empty(t, opts.ConfigPreset, "default should not set a config preset")
}

func TestRunGitHubSetupWithOpts_VendoredMode(t *testing.T) {
	var capturedArgs []string
	runner := func(_, _ string, args ...string) (string, error) {
		capturedArgs = args
		return "", nil
	}

	opts := GitHubSetupOpts{Vendor: true}
	err := RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", opts, runner, t.Logf)
	require.NoError(t, err)

	joined := strings.Join(capturedArgs, " ")
	assert.Contains(t, joined, "--vendor")
	assert.NotContains(t, joined, "--fullsend-ref")
	assert.Contains(t, joined, "--mint-url")
	assert.Contains(t, joined, "--direct")
	assert.Contains(t, joined, "--runtime")
	assert.Contains(t, joined, "--runtime dummy", "empty Runtime should default to dummy")
}

func TestRunGitHubSetupWithOpts_CustomRuntime(t *testing.T) {
	var capturedArgs []string
	runner := func(_, _ string, args ...string) (string, error) {
		capturedArgs = args
		return "", nil
	}

	opts := GitHubSetupOpts{Vendor: true, Runtime: "dummy-playback", AppSet: "fullsend-test"}
	err := RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", opts, runner, t.Logf)
	require.NoError(t, err)

	assert.Contains(t, capturedArgs, "--runtime")
	assert.Contains(t, strings.Join(capturedArgs, " "), "--app-set fullsend-test")
	assert.Contains(t, capturedArgs, "dummy-playback")
	assert.NotContains(t, capturedArgs, "dummy")
}

func TestRunGitHubSetupWithOpts_NonVendoredMode(t *testing.T) {
	var capturedArgs []string
	runner := func(_, _ string, args ...string) (string, error) {
		capturedArgs = args
		return "", nil
	}

	opts := GitHubSetupOpts{Vendor: false, FullsendRef: "main"}
	err := RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", opts, runner, t.Logf)
	require.NoError(t, err)

	joined := strings.Join(capturedArgs, " ")
	assert.NotContains(t, joined, "--vendor")
	assert.Contains(t, joined, "--fullsend-ref")
	assert.Contains(t, joined, "main")
}

func TestRunGitHubSetupWithOpts_WithGCPProject(t *testing.T) {
	var capturedCalls [][]string
	runner := func(_, _ string, args ...string) (string, error) {
		capturedCalls = append(capturedCalls, args)
		if len(args) >= 2 && args[0] == "inference" && args[1] == "status" {
			return `{"status":"healthy","FULLSEND_GCP_WIF_PROVIDER":"projects/1/locations/global/providers/wif"}`, nil
		}
		return "", nil
	}

	opts := GitHubSetupOpts{Vendor: false, FullsendRef: "main"}
	err := RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "test-project", opts, runner, t.Logf)
	require.NoError(t, err)

	// A healthy provider skips provision: inference status, github setup.
	require.Len(t, capturedCalls, 2)
	assert.Equal(t, "inference", capturedCalls[0][0])
	assert.Equal(t, "status", capturedCalls[0][1])
	assert.Equal(t, "github", capturedCalls[1][0])
	assert.Equal(t, "setup", capturedCalls[1][1])

	setupArgs := strings.Join(capturedCalls[1], " ")
	assert.Contains(t, setupArgs, "--inference-project")
	assert.Contains(t, setupArgs, "--fullsend-ref")
	assert.NotContains(t, setupArgs, "--vendor")
}

func TestRunGitHubSetupWithOpts_NonVendoredNoRef_ReturnsError(t *testing.T) {
	runner := func(_, _ string, _ ...string) (string, error) {
		t.Fatal("CLI should not be called when validation fails")
		return "", nil
	}

	opts := GitHubSetupOpts{Vendor: false, FullsendRef: ""}
	err := RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", opts, runner, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-vendored mode requires FullsendRef")
}

func TestRunGitHubSetupWithOpts_VendoredWithRef_ReturnsError(t *testing.T) {
	runner := func(_, _ string, _ ...string) (string, error) {
		t.Fatal("CLI should not be called when validation fails")
		return "", nil
	}

	opts := GitHubSetupOpts{Vendor: true, FullsendRef: "main"}
	err := RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", opts, runner, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vendored mode conflicts with FullsendRef")
}

func TestRunGitHubSetupWithOpts_ConfigPreset(t *testing.T) {
	var capturedArgs []string
	runner := func(_, _ string, args ...string) (string, error) {
		capturedArgs = args
		return "", nil
	}

	opts := GitHubSetupOpts{Vendor: true, ConfigPreset: "https://example.com/preset.yaml"}
	err := RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", opts, runner, t.Logf)
	require.NoError(t, err)

	assert.Equal(t, []string{
		"github", "setup", "org/repo",
		"--direct",
		"--mint-url", "https://mint.test",
		"--config", "https://example.com/preset.yaml",
		"--vendor",
	}, capturedArgs)
}

func TestRunGitHubSetupWithOpts_ConfigPresetOmitsRuntime(t *testing.T) {
	var capturedArgs []string
	runner := func(_, _ string, args ...string) (string, error) {
		capturedArgs = args
		return "", nil
	}

	opts := GitHubSetupOpts{Vendor: true, ConfigPreset: "./presets/bt.yaml"}
	err := RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", opts, runner, t.Logf)
	require.NoError(t, err)

	joined := strings.Join(capturedArgs, " ")
	assert.Contains(t, joined, "--config")
	assert.NotContains(t, joined, "--runtime")
}

func TestRunGitHubSetupWithOpts_ConfigPresetWithExplicitRuntime(t *testing.T) {
	var capturedArgs []string
	runner := func(_, _ string, args ...string) (string, error) {
		capturedArgs = args
		return "", nil
	}

	opts := GitHubSetupOpts{Vendor: true, ConfigPreset: "./presets/bt.yaml", Runtime: "dummy-playback"}
	err := RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", opts, runner, t.Logf)
	require.NoError(t, err)

	joined := strings.Join(capturedArgs, " ")
	assert.Contains(t, joined, "--config ./presets/bt.yaml")
	assert.Contains(t, joined, "--runtime dummy-playback",
		"an explicit Runtime must be forwarded even with a preset, so post-install validation (which checks opts.Runtime) matches what was actually installed")
}

func TestRunGitHubSetupWithOpts_ConfigPresetWithFullsendRef(t *testing.T) {
	var capturedArgs []string
	runner := func(_, _ string, args ...string) (string, error) {
		capturedArgs = args
		return "", nil
	}

	opts := GitHubSetupOpts{Vendor: false, FullsendRef: "main", ConfigPreset: "https://example.com/preset.yaml"}
	err := RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", opts, runner, t.Logf)
	require.NoError(t, err)

	joined := strings.Join(capturedArgs, " ")
	assert.Contains(t, joined, "--config")
	assert.Contains(t, joined, "https://example.com/preset.yaml")
	assert.Contains(t, joined, "--fullsend-ref")
	assert.Contains(t, joined, "main")
	assert.NotContains(t, joined, "--vendor")
	assert.NotContains(t, joined, "--runtime")
}

func TestRunGitHubSetupWithOpts_WhitespaceConfigPreset_KeepsRuntime(t *testing.T) {
	var capturedArgs []string
	runner := func(_, _ string, args ...string) (string, error) {
		capturedArgs = args
		return "", nil
	}

	opts := GitHubSetupOpts{Vendor: true, ConfigPreset: "   "}
	err := RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", opts, runner, t.Logf)
	require.NoError(t, err)

	joined := strings.Join(capturedArgs, " ")
	assert.NotContains(t, joined, "--config")
	assert.Contains(t, joined, "--runtime")
}

func TestRunGitHubSetup_DelegatesToWithOpts(t *testing.T) {
	var capturedArgs []string
	runner := func(_, _ string, args ...string) (string, error) {
		capturedArgs = args
		return "", nil
	}

	err := RunGitHubSetup("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", runner, t.Logf)
	require.NoError(t, err)

	// RunGitHubSetup should use default opts (vendored).
	joined := strings.Join(capturedArgs, " ")
	assert.Contains(t, joined, "--vendor")
	assert.NotContains(t, joined, "--fullsend-ref")
}

const healthyStatusJSON = `{"status":"healthy","FULLSEND_GCP_WIF_PROVIDER":"projects/1/locations/global/providers/wif"}`

func isInference(args []string, sub string) bool {
	return len(args) >= 2 && args[0] == "inference" && args[1] == sub
}

func TestRunGitHubSetupWithOpts_NotHealthy_Provisions(t *testing.T) {
	// "inference status" exits 0 with a non-healthy status when the
	// provider is missing; the fallback provisions and re-checks.
	for name, first := range map[string]struct {
		out string
		err error
	}{
		"not_provisioned": {out: `{"status":"not_provisioned"}`},
		"unhealthy":       {out: `{"status":"unhealthy","FULLSEND_GCP_WIF_PROVIDER":"projects/1/locations/global/providers/wif"}`},
		"cli error":       {err: errors.New("status exploded")},
	} {
		t.Run(name, func(t *testing.T) {
			var calls [][]string
			statusCalls := 0
			runner := func(_, _ string, args ...string) (string, error) {
				calls = append(calls, args)
				if isInference(args, "status") {
					statusCalls++
					if statusCalls == 1 {
						return first.out, first.err
					}
					return healthyStatusJSON, nil
				}
				return "", nil
			}

			err := RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "proj", DefaultGitHubSetupOpts(), runner, t.Logf)
			require.NoError(t, err)

			require.Len(t, calls, 4, "expected status, provision, status, setup")
			assert.True(t, isInference(calls[0], "status"))
			assert.True(t, isInference(calls[1], "provision"))
			assert.True(t, isInference(calls[2], "status"))
			assert.Equal(t, []string{"github", "setup"}, calls[3][:2])
			assert.Contains(t, calls[3], "projects/1/locations/global/providers/wif")
		})
	}
}

func TestRunGitHubSetupWithOpts_ProvisionError_IsHardFailure(t *testing.T) {
	var calls [][]string
	runner := func(_, _ string, args ...string) (string, error) {
		calls = append(calls, args)
		switch {
		case isInference(args, "status"):
			return `{"status":"not_provisioned"}`, nil
		case isInference(args, "provision"):
			return "", errors.New("provision boom")
		}
		return "", nil
	}

	err := RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "proj", DefaultGitHubSetupOpts(), runner, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inference provision org/repo")
	assert.Contains(t, err.Error(), "provision boom")
	require.Len(t, calls, 2, "no status re-check or setup after a failed provision")
}

func TestRunGitHubSetupWithOpts_StillNotHealthyAfterProvision(t *testing.T) {
	var calls [][]string
	runner := func(_, _ string, args ...string) (string, error) {
		calls = append(calls, args)
		if isInference(args, "status") {
			return `{"status":"unhealthy","FULLSEND_GCP_WIF_PROVIDER":"projects/1/locations/global/providers/wif"}`, nil
		}
		return "", nil
	}

	err := RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "proj", DefaultGitHubSetupOpts(), runner, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `expected healthy status, got "unhealthy"`)
	require.Len(t, calls, 3, "status, provision, status; no setup")
}

func TestRunGitHubSetupWithOpts_UsesResolver(t *testing.T) {
	var calls [][]string
	runner := func(_, _ string, args ...string) (string, error) {
		calls = append(calls, args)
		return "", nil
	}
	var resolved []string
	opts := DefaultGitHubSetupOpts()
	opts.ResolveWIFProvider = func(target, project string) (string, error) {
		resolved = append(resolved, target, project)
		return "projects/9/locations/global/providers/cached", nil
	}

	err := RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", " proj ", opts, runner, t.Logf)
	require.NoError(t, err)

	assert.Equal(t, []string{"org/repo", "proj"}, resolved)
	require.Len(t, calls, 1, "the resolver replaces the inference CLI calls")
	assert.Contains(t, calls[0], "projects/9/locations/global/providers/cached")
}

func TestRunGitHubSetupWithOpts_ResolverError(t *testing.T) {
	runner := func(_, _ string, _ ...string) (string, error) {
		t.Fatal("github setup must not run when the resolver fails")
		return "", nil
	}
	opts := DefaultGitHubSetupOpts()
	opts.ResolveWIFProvider = func(string, string) (string, error) {
		return "", errors.New("resolve boom")
	}

	err := RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "proj", opts, runner, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve boom")
}

func TestInferenceStatusWIFProvider(t *testing.T) {
	for name, tc := range map[string]struct {
		out     string
		err     error
		want    string
		wantErr string
	}{
		"healthy":         {out: healthyStatusJSON, want: "projects/1/locations/global/providers/wif"},
		"not provisioned": {out: `{"status":"not_provisioned"}`, wantErr: "missing FULLSEND_GCP_WIF_PROVIDER"},
		"unhealthy":       {out: `{"status":"unhealthy","FULLSEND_GCP_WIF_PROVIDER":"p"}`, wantErr: "expected healthy status"},
		"cli error":       {err: errors.New("boom"), wantErr: "boom"},
	} {
		t.Run(name, func(t *testing.T) {
			var calls [][]string
			runner := func(_, _ string, args ...string) (string, error) {
				calls = append(calls, args)
				return tc.out, tc.err
			}
			got, err := InferenceStatusWIFProvider("/bin/fullsend", "tok", "org/repo", "proj", runner, t.Logf)
			assert.Equal(t, [][]string{{"inference", "status", "org/repo", "--project", "proj", "--format", "json"}}, calls)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "inference status org/repo")
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
