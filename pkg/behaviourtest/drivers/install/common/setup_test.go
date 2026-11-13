package common

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

// installCall records one repos install invocation: its argv and the
// manifest it pointed at, read while the fake CLI runs (the manifest
// directory is removed when the install returns).
type installCall struct {
	args         []string
	manifestPath string
	manifest     string
	preset       []byte
}

// recordingRunner returns a fake CLI that answers "inference status" with
// a healthy provider and records every repos install call.
func recordingRunner(t *testing.T, calls *[]installCall) CLIRunnerFunc {
	t.Helper()
	return func(_, _ string, args ...string) (string, error) {
		if isInference(args, "status") {
			return healthyStatusJSON, nil
		}
		if len(args) < 2 || args[0] != "repos" || args[1] != "install" {
			return "", nil
		}
		call := installCall{args: append([]string(nil), args...)}
		for i, a := range args {
			if a == "--manifest" && i+1 < len(args) {
				call.manifestPath = args[i+1]
			}
		}
		require.NotEmpty(t, call.manifestPath, "repos install must be given --manifest")
		data, err := os.ReadFile(call.manifestPath)
		require.NoError(t, err, "the manifest must exist when repos install runs")
		call.manifest = string(data)
		if preset, err := os.ReadFile(filepath.Join(filepath.Dir(call.manifestPath), presetFileName)); err == nil {
			call.preset = preset
		}
		*calls = append(*calls, call)
		return "", nil
	}
}

// resolveEntry loads a generated manifest with the real repos package,
// validates it, and resolves the settings for org/repo exactly as repos
// install would.
func resolveEntry(t *testing.T, dir, manifest string) repos.ResolvedConfig {
	t.Helper()
	path := filepath.Join(dir, manifestFileName)
	require.NoError(t, os.WriteFile(path, []byte(manifest), 0o600))
	m, err := repos.LoadManifest(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, m.Validate(), "generated manifest must be valid:\n%s", manifest)
	rc, ok := m.ResolveConfigWithGlobs("org", "repo")
	require.True(t, ok, "generated manifest must declare org/repo:\n%s", manifest)
	return rc
}

// stagedPresetDir writes the staged preset (if any) next to a manifest
// copy so resolveEntry can validate a relative config_base.source.
func stagedPresetDir(t *testing.T, call installCall) string {
	t.Helper()
	dir := t.TempDir()
	if call.preset != nil {
		require.NoError(t, os.WriteFile(filepath.Join(dir, presetFileName), call.preset, 0o600))
	}
	return dir
}

func declaredMintURL(t *testing.T, rc repos.ResolvedConfig) string {
	t.Helper()
	declared, err := config.DeclaredInfrastructureValues(rc.Managed, nil)
	require.NoError(t, err)
	return declared.MintURL
}

func runInstall(t *testing.T, mintURL, project string, opts InstallOpts) (installCall, []string) {
	t.Helper()
	var calls []installCall
	err := RunReposInstallWithOpts("/bin/fullsend", "tok", "org/repo", mintURL, project, opts, recordingRunner(t, &calls), t.Logf)
	require.NoError(t, err)
	require.Len(t, calls, 1, "expected exactly one repos install call")
	return calls[0], calls[0].args
}

// removedInstallFlags are the persistent-setting flags repos install
// rejects (#8219), plus github setup flags with no repos install
// equivalent. None may be passed.
var removedInstallFlags = []string{
	"--roles", "--fullsend-ref", "--mint-url", "--app-set", "--inference-auth",
	"--vendor", "--vendor-fullsend-binary", "--signoff", "--gitlab-url",
	"--vertex-region", "--forge", "--runtime", "--config", "--config-hash",
	"--inference-project", "--inference-wif-provider",
}

func assertNoRemovedFlags(t *testing.T, args []string) {
	t.Helper()
	for _, flag := range removedInstallFlags {
		assert.NotContains(t, args, flag, "repos install must not receive %s", flag)
	}
}

func TestDefaultInstallOpts_HasVendor(t *testing.T) {
	opts := DefaultInstallOpts()
	assert.True(t, opts.Vendor, "default should enable vendoring")
	assert.Empty(t, opts.FullsendRef, "default should not set a fullsend ref")
	assert.Empty(t, opts.ConfigPreset, "default should not set a config preset")
}

func TestRunReposInstallWithOpts_VendoredMode(t *testing.T) {
	call, args := runInstall(t, "https://mint.test", "", InstallOpts{Vendor: true})

	assert.Equal(t, []string{"repos", "install", "org/repo", "--manifest", call.manifestPath, "--direct"}, args,
		"only the manifest and one-run controls are passed")
	assertNoRemovedFlags(t, args)

	rc := resolveEntry(t, t.TempDir(), call.manifest)
	assert.Equal(t, repos.ForgeGitHub, rc.Forge)
	assert.True(t, rc.Vendor, "vendored tests vendor the current checkout's binary")
	assert.Empty(t, rc.FullsendRef)
	assert.Equal(t, "dummy", rc.Runtime, "empty Runtime defaults to dummy")
	assert.Equal(t, "vertex-wif", rc.InferenceAuth)
	assert.Empty(t, rc.Config, "no preset declared")
	assert.False(t, rc.AppSetExplicit, "no app set declared")
	assert.Equal(t, "https://mint.test", declaredMintURL(t, rc))
	assert.False(t, rc.MintURLExplicit, "the mint URL is a managed config value, so a preset cannot conflict with it")
}

func TestRunReposInstallWithOpts_CustomRuntimeAndAppSet(t *testing.T) {
	call, args := runInstall(t, "https://mint.test", "", InstallOpts{Vendor: true, Runtime: "dummy-playback", AppSet: "fullsend-test"})
	assertNoRemovedFlags(t, args)

	rc := resolveEntry(t, t.TempDir(), call.manifest)
	assert.Equal(t, "dummy-playback", rc.Runtime)
	assert.Equal(t, "fullsend-test", rc.AppSet)
	assert.True(t, rc.AppSetExplicit)
}

func TestRunReposInstallWithOpts_NonVendoredMode(t *testing.T) {
	call, args := runInstall(t, "https://mint.test", "", InstallOpts{Vendor: false, FullsendRef: "main"})
	assertNoRemovedFlags(t, args)
	assert.NotContains(t, args, "--fullsend-binary")
	assert.NotContains(t, args, "--fullsend-source")

	rc := resolveEntry(t, t.TempDir(), call.manifest)
	assert.False(t, rc.Vendor)
	assert.Equal(t, "main", rc.FullsendRef, "non-vendored tests pin fullsend_ref")
	assert.Equal(t, "dummy", rc.Runtime)
}

func TestRunReposInstallWithOpts_WithGCPProject(t *testing.T) {
	var calls [][]string
	var install []installCall
	record := recordingRunner(t, &install)
	runner := func(binary, token string, args ...string) (string, error) {
		calls = append(calls, args)
		return record(binary, token, args...)
	}

	opts := InstallOpts{Vendor: false, FullsendRef: "main"}
	err := RunReposInstallWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "test-project", opts, runner, t.Logf)
	require.NoError(t, err)

	// A healthy provider skips provision: inference status, repos install.
	require.Len(t, calls, 2)
	assert.True(t, isInference(calls[0], "status"))
	require.Len(t, install, 1)
	assert.Equal(t, []string{
		"repos", "install", "org/repo",
		"--manifest", install[0].manifestPath,
		"--direct",
		"--vertex-project", "test-project",
		"--vertex-wif-provider", "projects/1/locations/global/providers/wif",
	}, install[0].args, "the project and WIF provider are credential inputs")
	assertNoRemovedFlags(t, install[0].args)
	assert.NotContains(t, install[0].manifest, "test-project", "the project is never written to repos.yaml")
}

func TestRunReposInstallWithOpts_NonVendoredNoRef_ReturnsError(t *testing.T) {
	runner := func(_, _ string, _ ...string) (string, error) {
		t.Fatal("CLI should not be called when validation fails")
		return "", nil
	}

	opts := InstallOpts{Vendor: false, FullsendRef: ""}
	err := RunReposInstallWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", opts, runner, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-vendored mode requires FullsendRef")
}

func TestRunReposInstallWithOpts_VendoredWithRef_ReturnsError(t *testing.T) {
	runner := func(_, _ string, _ ...string) (string, error) {
		t.Fatal("CLI should not be called when validation fails")
		return "", nil
	}

	opts := InstallOpts{Vendor: true, FullsendRef: "main"}
	err := RunReposInstallWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", opts, runner, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vendored mode conflicts with FullsendRef")
}

func TestRunReposInstallWithOpts_RemoteConfigPreset_OmitsRuntime(t *testing.T) {
	call, args := runInstall(t, "https://mint.test", "", InstallOpts{Vendor: true, ConfigPreset: "https://example.com/preset.yaml"})
	assertNoRemovedFlags(t, args)

	rc := resolveEntry(t, t.TempDir(), call.manifest)
	assert.Equal(t, "https://example.com/preset.yaml", rc.Config)
	assert.Empty(t, rc.Runtime, "the preset's runtime is inherited rather than pinned in the managed config")
	assert.True(t, rc.Vendor)
	assert.Equal(t, "https://mint.test", declaredMintURL(t, rc), "the managed mint_url overrides the preset's")
	assert.Nil(t, call.preset, "a remote preset is not staged locally")
}

func TestRunReposInstallWithOpts_ConfigPresetWithExplicitRuntime(t *testing.T) {
	call, _ := runInstall(t, "https://mint.test", "", InstallOpts{
		Vendor: true, ConfigPreset: "https://example.com/preset.yaml", Runtime: "dummy-playback",
	})

	rc := resolveEntry(t, t.TempDir(), call.manifest)
	assert.Equal(t, "https://example.com/preset.yaml", rc.Config)
	assert.Equal(t, "dummy-playback", rc.Runtime,
		"an explicit Runtime must win over a preset, so post-install validation (which checks opts.Runtime) matches what was actually installed")
}

func TestRunReposInstallWithOpts_ConfigPresetWithFullsendRef(t *testing.T) {
	call, _ := runInstall(t, "https://mint.test", "", InstallOpts{
		Vendor: false, FullsendRef: "main", ConfigPreset: "https://example.com/preset.yaml",
	})

	rc := resolveEntry(t, t.TempDir(), call.manifest)
	assert.Equal(t, "https://example.com/preset.yaml", rc.Config)
	assert.Equal(t, "main", rc.FullsendRef)
	assert.False(t, rc.Vendor)
	assert.Empty(t, rc.Runtime)
}

func TestRunReposInstallWithOpts_WhitespaceConfigPreset_KeepsRuntime(t *testing.T) {
	call, _ := runInstall(t, "https://mint.test", "", InstallOpts{Vendor: true, ConfigPreset: "   "})

	rc := resolveEntry(t, t.TempDir(), call.manifest)
	assert.Empty(t, rc.Config)
	assert.Equal(t, "dummy", rc.Runtime)
}

func TestRunReposInstallWithOpts_LocalConfigPreset_StagedNextToManifest(t *testing.T) {
	presetContent := []byte("version: \"1\"\nruntime: dummy\n")
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, "presets"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(base, "presets", "bt.yaml"), presetContent, 0o600))

	call, _ := runInstall(t, "https://mint.test", "", InstallOpts{
		Vendor: true, ConfigPreset: "./presets/bt.yaml", PresetBaseDir: base,
	})

	assert.Equal(t, presetContent, call.preset, "the local preset is copied into the manifest directory")
	assert.Contains(t, call.manifest, "source: "+presetFileName)
	dir := stagedPresetDir(t, call)
	rc := resolveEntry(t, dir, call.manifest)
	assert.Equal(t, filepath.Join(dir, presetFileName), rc.Config, "repos install resolves the staged preset inside the manifest directory")
	assert.Empty(t, rc.Runtime)
}

func TestRunReposInstallWithOpts_LocalConfigPreset_AbsolutePath(t *testing.T) {
	presetPath := filepath.Join(t.TempDir(), "preset.yaml")
	require.NoError(t, os.WriteFile(presetPath, []byte("version: \"1\"\n"), 0o600))

	call, _ := runInstall(t, "https://mint.test", "", InstallOpts{Vendor: true, ConfigPreset: presetPath})
	assert.Equal(t, []byte("version: \"1\"\n"), call.preset)
}

func TestRunReposInstallWithOpts_LocalConfigPreset_DefaultsToModuleRoot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "bt.yaml"), []byte("version: \"1\"\n"), 0o600))
	orig := moduleRootFn
	moduleRootFn = func() (string, error) { return root, nil }
	t.Cleanup(func() { moduleRootFn = orig })

	call, _ := runInstall(t, "https://mint.test", "", InstallOpts{Vendor: true, ConfigPreset: "bt.yaml"})
	assert.Equal(t, []byte("version: \"1\"\n"), call.preset)
}

func TestRunReposInstallWithOpts_LocalConfigPresetErrors(t *testing.T) {
	failRoot := func() (string, error) { return "", errors.New("no module") }
	for name, tc := range map[string]struct {
		preset  string
		root    func() (string, error)
		wantErr string
	}{
		"missing file":       {preset: filepath.Join(t.TempDir(), "absent.yaml"), wantErr: "reading config preset"},
		"unsupported scheme": {preset: "http://example.com/preset.yaml", wantErr: "only local paths and https:// URLs"},
		"module root error":  {preset: "bt.yaml", root: failRoot, wantErr: "no module"},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.root != nil {
				orig := moduleRootFn
				moduleRootFn = tc.root
				t.Cleanup(func() { moduleRootFn = orig })
			}
			runner := func(_, _ string, _ ...string) (string, error) {
				t.Fatal("repos install must not run when the manifest cannot be written")
				return "", nil
			}
			err := RunReposInstallWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "",
				InstallOpts{Vendor: true, ConfigPreset: tc.preset}, runner, t.Logf)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "repos install org/repo")
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestRunReposInstallWithOpts_ManifestNotCreated covers the missing
// manifest contract: repos install never creates one, so when the driver
// cannot write it the error is reported and the CLI is not run.
func TestRunReposInstallWithOpts_ManifestNotCreated(t *testing.T) {
	runner := func(_, _ string, _ ...string) (string, error) {
		t.Fatal("repos install must not run without a manifest")
		return "", nil
	}
	opts := DefaultInstallOpts()
	opts.ManifestDir = filepath.Join(t.TempDir(), "does-not-exist")

	err := RunReposInstallWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", opts, runner, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating manifest directory")
}

func TestWriteInstallManifest_WriteError(t *testing.T) {
	_, err := writeInstallManifest(filepath.Join(t.TempDir(), "does-not-exist"), "org/repo", "https://mint.test", DefaultInstallOpts())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "writing manifest")
}

// TestRunReposInstallWithOpts_ManifestRemoved checks that the generated
// manifest is removed after install, so each call (including a retry)
// starts from a fresh manifest.
func TestRunReposInstallWithOpts_ManifestRemoved(t *testing.T) {
	parent := t.TempDir()
	opts := DefaultInstallOpts()
	opts.ManifestDir = parent
	call, _ := runInstall(t, "https://mint.test", "", opts)

	assert.Equal(t, parent, filepath.Dir(filepath.Dir(call.manifestPath)))
	_, err := os.Stat(call.manifestPath)
	assert.True(t, errors.Is(err, os.ErrNotExist), "manifest must be removed after install")
}

func TestRunReposInstallWithOpts_CLIError(t *testing.T) {
	runner := func(_, _ string, _ ...string) (string, error) {
		return "", errors.New("loading manifest: repos install does not create a manifest")
	}
	err := RunReposInstallWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", DefaultInstallOpts(), runner, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repos install org/repo")
	assert.Contains(t, err.Error(), "does not create a manifest")
}

const healthyStatusJSON = `{"status":"healthy","FULLSEND_GCP_WIF_PROVIDER":"projects/1/locations/global/providers/wif"}`

func isInference(args []string, sub string) bool {
	return len(args) >= 2 && args[0] == "inference" && args[1] == sub
}

func isReposInstall(args []string) bool {
	return len(args) >= 2 && args[0] == "repos" && args[1] == "install"
}

func TestRunReposInstallWithOpts_NotHealthy_Provisions(t *testing.T) {
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

			err := RunReposInstallWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "proj", DefaultInstallOpts(), runner, t.Logf)
			require.NoError(t, err)

			require.Len(t, calls, 4, "expected status, provision, status, install")
			assert.True(t, isInference(calls[0], "status"))
			assert.True(t, isInference(calls[1], "provision"))
			assert.True(t, isInference(calls[2], "status"))
			assert.True(t, isReposInstall(calls[3]))
			assert.Contains(t, calls[3], "projects/1/locations/global/providers/wif")
		})
	}
}

func TestRunReposInstallWithOpts_ProvisionError_IsHardFailure(t *testing.T) {
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

	err := RunReposInstallWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "proj", DefaultInstallOpts(), runner, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inference provision org/repo")
	assert.Contains(t, err.Error(), "provision boom")
	require.Len(t, calls, 2, "no status re-check or install after a failed provision")
}

// stubPostProvisionSleep replaces PostProvisionSleep with a recorder so
// tests skip the real backoff, and returns the recorded delays.
func stubPostProvisionSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	var slept []time.Duration
	orig := PostProvisionSleep
	PostProvisionSleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	t.Cleanup(func() { PostProvisionSleep = orig })
	return &slept
}

func TestRunReposInstallWithOpts_StillNotHealthyAfterProvision(t *testing.T) {
	slept := stubPostProvisionSleep(t)
	var calls [][]string
	runner := func(_, _ string, args ...string) (string, error) {
		calls = append(calls, args)
		if isInference(args, "status") {
			return `{"status":"unhealthy","FULLSEND_GCP_WIF_PROVIDER":"projects/1/locations/global/providers/wif"}`, nil
		}
		return "", nil
	}

	err := RunReposInstallWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "proj", DefaultInstallOpts(), runner, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `expected healthy status, got "unhealthy"`)
	require.Len(t, calls, 2+len(postProvisionStatusDelays)+1, "status, provision, every post-provision status read; no install")
	assert.Equal(t, postProvisionStatusDelays, *slept)
}

func TestProvisionInference_HealthyOnNthRead(t *testing.T) {
	slept := stubPostProvisionSleep(t)
	const healthyOn = 3
	var calls [][]string
	statusCalls := 0
	runner := func(_, _ string, args ...string) (string, error) {
		calls = append(calls, args)
		if isInference(args, "status") {
			statusCalls++
			if statusCalls < healthyOn {
				return `{"status":"not_provisioned"}`, nil
			}
			return healthyStatusJSON, nil
		}
		return "", nil
	}

	got, err := ProvisionInference("/bin/fullsend", "tok", "org/repo", "proj", runner, t.Logf)
	require.NoError(t, err)
	assert.Equal(t, "projects/1/locations/global/providers/wif", got)
	require.Len(t, calls, 1+healthyOn, "provision, then status until healthy")
	assert.True(t, isInference(calls[0], "provision"))
	assert.Equal(t, healthyOn, statusCalls)
	assert.Equal(t, postProvisionStatusDelays[:healthyOn-1], *slept)
}

func TestProvisionInference_NeverHealthy_FailsAfterBudget(t *testing.T) {
	slept := stubPostProvisionSleep(t)
	statusCalls := 0
	runner := func(_, _ string, args ...string) (string, error) {
		if isInference(args, "status") {
			statusCalls++
			return `{"status":"not_provisioned"}`, nil
		}
		return "", nil
	}

	_, err := ProvisionInference("/bin/fullsend", "tok", "org/repo", "proj", runner, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not healthy after provision")
	assert.Contains(t, err.Error(), `status="not_provisioned"`, "error names the last status")
	assert.Equal(t, len(postProvisionStatusDelays)+1, statusCalls)

	var total time.Duration
	for _, d := range *slept {
		total += d
	}
	assert.Equal(t, 60*time.Second, total, "the backoff budget is ~60s")
}

func TestProvisionInference_ProvisionFails_NoPolling(t *testing.T) {
	slept := stubPostProvisionSleep(t)
	statusCalls := 0
	runner := func(_, _ string, args ...string) (string, error) {
		switch {
		case isInference(args, "provision"):
			return "", errors.New("provision boom")
		case isInference(args, "status"):
			statusCalls++
		}
		return healthyStatusJSON, nil
	}

	_, err := ProvisionInference("/bin/fullsend", "tok", "org/repo", "proj", runner, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provision boom")
	assert.Zero(t, statusCalls, "no status reads after a failed provision")
	assert.Empty(t, *slept)
}

func TestResolveInferenceWIFProvider_AlreadyProvisioned_SingleRead(t *testing.T) {
	slept := stubPostProvisionSleep(t)
	var calls [][]string
	runner := func(_, _ string, args ...string) (string, error) {
		calls = append(calls, args)
		return healthyStatusJSON, nil
	}

	got, err := ResolveInferenceWIFProvider("/bin/fullsend", "tok", "org/repo", "proj", runner, t.Logf)
	require.NoError(t, err)
	assert.Equal(t, "projects/1/locations/global/providers/wif", got)
	require.Len(t, calls, 1, "exactly one status read, no provision")
	assert.True(t, isInference(calls[0], "status"))
	assert.Empty(t, *slept)
}

func TestRunReposInstallWithOpts_UsesResolver(t *testing.T) {
	var calls [][]string
	runner := func(_, _ string, args ...string) (string, error) {
		calls = append(calls, args)
		return "", nil
	}
	var resolved []string
	opts := DefaultInstallOpts()
	opts.ResolveWIFProvider = func(target, project string) (string, error) {
		resolved = append(resolved, target, project)
		return "projects/9/locations/global/providers/cached", nil
	}

	err := RunReposInstallWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", " proj ", opts, runner, t.Logf)
	require.NoError(t, err)

	assert.Equal(t, []string{"org/repo", "proj"}, resolved)
	require.Len(t, calls, 1, "the resolver replaces the inference CLI calls")
	joined := strings.Join(calls[0], " ")
	assert.Contains(t, joined, "--vertex-project proj")
	assert.Contains(t, joined, "--vertex-wif-provider projects/9/locations/global/providers/cached")
}

func TestRunReposInstallWithOpts_ResolverError(t *testing.T) {
	runner := func(_, _ string, _ ...string) (string, error) {
		t.Fatal("repos install must not run when the resolver fails")
		return "", nil
	}
	opts := DefaultInstallOpts()
	opts.ResolveWIFProvider = func(string, string) (string, error) {
		return "", errors.New("resolve boom")
	}

	err := RunReposInstallWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "proj", opts, runner, t.Logf)
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

func TestWaitInferenceHealthy_ContextCancelledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	statusCalls := 0
	runner := func(_, _ string, args ...string) (string, error) {
		statusCalls++
		cancel()
		return `{"status":"not_provisioned"}`, nil
	}

	start := time.Now()
	_, err := WaitInferenceHealthy(ctx, "/bin/fullsend", "tok", "org/repo", "proj", runner, t.Logf)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, err.Error(), `status="not_provisioned"`, "error keeps the last status")
	assert.Equal(t, 1, statusCalls, "no status read after cancellation")
	assert.Less(t, time.Since(start), postProvisionStatusDelays[0], "the real backoff is cut short")
}

// TestIsGCPWIFQuotaError pins the bounded signature list behind the GCP WIF
// quota mark (task 2.2 / design D3 of the behaviour-infra-retry change):
// every known quota/rate-limit text from the inference CLI calls matches,
// and the errors those call sites can also produce — parse failures and
// unrelated CLI failures — must not. The realistic messages below mimic the
// CLI runner's error shape (e2etest.tryRunCLIFromDir embeds the command's
// combined output, where cobra prints "Error: <message>", in the error
// string); classification through failclass.Classify is covered in
// setup_infraclass_ext_test.go because failclass cannot be imported here.
func TestIsGCPWIFQuotaError(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"iam write retry exhaustion": {
			err:  fmt.Errorf("inference provision org/repo: [cli] fullsend inference provision org/repo --project proj failed: exit status 1\nError: rate limited (HTTP 429) after 7 attempts"),
			want: true,
		},
		"gcp quota body": {
			err:  fmt.Errorf("inference provision org/repo: [cli] fullsend inference provision org/repo --project proj failed: exit status 1\nError: Quota exceeded for quota project '123456789' and limit 'CreateWorkloadIdentityPool' of service 'iam.googleapis.com'"),
			want: true,
		},
		"iam read returns 429": {
			err:  fmt.Errorf("inference status org/repo: [cli] fullsend inference status org/repo --project proj --format json failed: exit status 1\nError: getting WIF provider returned 429: too many requests"),
			want: true,
		},
		"unwrapped 429 status": {
			err:  fmt.Errorf("unexpected status 429 deleting WIF pool: backend error"),
			want: true,
		},
		"gcp rate limit body": {
			err:  fmt.Errorf("inference provision org/repo: [cli] fullsend inference provision org/repo --project proj failed: exit status 1\nError: Rate limit exceeded for consumer"),
			want: true,
		},
		"nil error": {
			err: nil,
		},
		"unhealthy status is not quota": {
			err: fmt.Errorf(`expected healthy status, got "unhealthy"`),
		},
		"missing provider is not quota": {
			err: fmt.Errorf(`missing FULLSEND_GCP_WIF_PROVIDER (status="not_provisioned")`),
		},
		"unrelated cli failure": {
			err: fmt.Errorf("inference provision org/repo: [cli] fullsend inference provision org/repo --project proj failed: exit status 1\nError: creating WIF pool returned 403: Permission 'iam.workloadIdentityPools.create' denied"),
		},
		"other status code is not quota": {
			err: fmt.Errorf("getting WIF provider returned 404: provider not found"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, isGCPWIFQuotaError(tc.err))
		})
	}
}

// TestMarkGCPWIFQuota covers the wrap rule the two inference call sites
// share: quota-signature errors gain the "setup: gcp wif quota" mark, and
// everything else — nil, unrelated errors, and already-marked errors —
// passes through unchanged.
func TestMarkGCPWIFQuota(t *testing.T) {
	quotaErr := errors.New("rate limited (HTTP 429) after 7 attempts")

	t.Run("quota error is marked with the setup reason", func(t *testing.T) {
		marked := markGCPWIFQuota(quotaErr)
		require.Error(t, marked)
		assert.True(t, behaviourfail.Is(marked))
		var infra *behaviourfail.InfraError
		require.ErrorAs(t, marked, &infra)
		assert.Equal(t, "setup: gcp wif quota", infra.Reason)
		assert.ErrorIs(t, marked, quotaErr, "the CLI failure stays reachable through the mark")
	})

	t.Run("nil passes through", func(t *testing.T) {
		assert.NoError(t, markGCPWIFQuota(nil))
	})

	t.Run("unrelated error passes through unchanged", func(t *testing.T) {
		plain := fmt.Errorf(`expected healthy status, got "unhealthy"`)
		got := markGCPWIFQuota(plain)
		assert.Same(t, plain, got)
		assert.False(t, behaviourfail.Is(got))
	})

	t.Run("already-marked error is not wrapped again", func(t *testing.T) {
		marked := behaviourfail.Mark(quotaErr, "setup: gcp wif quota")
		got := markGCPWIFQuota(marked)
		assert.Same(t, marked, got, "WaitInferenceHealthy's marked error flows through ResolveInferenceWIFProvider unwrapped")
	})
}
