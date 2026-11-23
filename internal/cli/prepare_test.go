package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const prepareTestImage = "quay.io/example/fullsend-sandbox:1"

// fakePrepareSandbox records the sandbox operations fullsend prepare
// performs and answers them with the configured errors.
type fakePrepareSandbox struct {
	ensureErr  error
	gatewayErr error
	createErr  error
	deleteErr  error

	created  []string // sandbox names
	images   []string // image per create
	policies []string
	attempts []int
	timeouts []time.Duration
	deleted  []string
}

// stubPrepareSandbox replaces fullsend prepare's sandbox operations with a
// fake, and makes composition hermetic.
func stubPrepareSandbox(t *testing.T) *fakePrepareSandbox {
	t.Helper()
	neutralizeAgentsRepoFallback(t)
	t.Setenv("FULLSEND_SANDBOX_IMAGE", "")
	t.Setenv("FULLSEND_SANDBOX_ARCH", "")
	t.Setenv("OPENSHELL_GATEWAY_ENDPOINT", "")

	f := &fakePrepareSandbox{}
	origEnsure, origGateway, origCreate, origDelete := prepareEnsureOpenshellFn, prepareCheckGatewayFn, prepareCreateSandboxFn, deleteSandboxFn
	prepareEnsureOpenshellFn = func() error { return f.ensureErr }
	prepareCheckGatewayFn = func() error { return f.gatewayErr }
	prepareCreateSandboxFn = func(name string, providers []string, image, policy string, maxAttempts int, ready time.Duration, keep bool) error {
		assert.Empty(t, providers, "the preflight sandbox has no providers")
		assert.False(t, keep, "the preflight sandbox is never kept")
		f.created = append(f.created, name)
		f.images = append(f.images, image)
		f.policies = append(f.policies, policy)
		f.attempts = append(f.attempts, maxAttempts)
		f.timeouts = append(f.timeouts, ready)
		return f.createErr
	}
	deleteSandboxFn = func(name string) error {
		f.deleted = append(f.deleted, name)
		return f.deleteErr
	}
	t.Cleanup(func() {
		prepareEnsureOpenshellFn, prepareCheckGatewayFn, prepareCreateSandboxFn, deleteSandboxFn = origEnsure, origGateway, origCreate, origDelete
	})
	return f
}

// writePrepareFixture writes a .fullsend directory with a code agent whose
// harness inherits imageLine (e.g. "image: x") from a local base.
func writePrepareFixture(t *testing.T, imageLine string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	writePolicyFixture(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "code.md"), []byte("You are a coding agent."), 0o644))
	base := "agent: ../agents/code.md\nrole: test\npolicy: ../policies/base.yaml\nsandbox_timeout_seconds: 42\n"
	if imageLine != "" {
		base += imageLine + "\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "base.yaml"), []byte(base), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"), []byte("base: ./base.yaml\nrole: test\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("agents:\n  - harness/code.yaml\n"), 0o644))
	return dir
}

func prepareOpts(dir string) prepareOptions {
	return prepareOptions{
		agentName:   "code",
		fullsendDir: dir,
		jsonOut:     true,
		rFlags:      resolveFlags{maxDepth: 10, maxResources: 50},
	}
}

// runPrepareJSON runs fullsend prepare with --json and decodes its result.
func runPrepareJSON(t *testing.T, opts prepareOptions) (prepareResult, string, error) {
	t.Helper()
	var progress, out bytes.Buffer
	err := runPrepare(context.Background(), opts, ui.New(&progress), &out)
	var res prepareResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &res), "stdout must hold only the JSON result: %s", out.String())
	return res, progress.String(), err
}

func TestPrepare_CachedImageSkipsPullButRunsPreflight(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	fake.respond("image", "", nil) // already present
	sb := stubPrepareSandbox(t)
	dir := writePrepareFixture(t, "image: "+prepareTestImage)

	res, _, err := runPrepareJSON(t, prepareOpts(dir))

	require.NoError(t, err)
	assert.True(t, res.OK)
	assert.Equal(t, "code", res.Agent)
	assert.Equal(t, sandboxArch(), res.Arch)
	assert.Equal(t, prepareGatewayLocal, res.Gateway)
	assert.Equal(t, filepath.Join(dir, "harness", "code.yaml"), res.Harness)
	require.Len(t, res.Images, 1)
	assert.Equal(t, prepareImage{Role: "sandbox", Ref: prepareTestImage, Source: prepareSourceHarness, Status: prepareImageCached}, res.Images[0])
	assert.Equal(t, []string{"image"}, fake.subcommands(), "a cached image is not pulled")

	require.Len(t, sb.created, 1, "the preflight still runs for a cached image")
	assert.Equal(t, []string{prepareTestImage}, sb.images)
	assert.Equal(t, []string{""}, sb.policies)
	assert.Equal(t, []int{1}, sb.attempts)
	assert.Equal(t, []time.Duration{42 * time.Second}, sb.timeouts, "the harness sandbox timeout applies")
	assert.Equal(t, sb.created, sb.deleted, "the preflight sandbox is deleted")
	require.NotNil(t, res.Preflight)
	assert.Equal(t, preparePreflight{Sandbox: sb.created[0], Status: preparePreflightPassed}, *res.Preflight)
	assert.Nil(t, res.Error)
}

func TestPrepare_PullsMissingImageAnonymously(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	stubPrepareSandbox(t)
	dir := writePrepareFixture(t, "image: "+prepareTestImage)

	res, progress, err := runPrepareJSON(t, prepareOpts(dir))

	require.NoError(t, err)
	assert.Equal(t, prepareImagePulled, res.Images[0].Status)
	require.Equal(t, []string{"image", "pull"}, fake.subcommands())
	assert.Equal(t, []string{"pull", "--quiet", prepareTestImage}, fake.calls[1])
	assert.Contains(t, progress, "anonymously")
}

func TestPrepare_PullsGHCRImageWithWorkflowToken(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	stubPrepareSandbox(t)
	dir := writePrepareFixture(t, "image: "+privateImage)

	res, progress, err := runPrepareJSON(t, prepareOpts(dir))

	require.NoError(t, err)
	assert.Equal(t, prepareImagePulled, res.Images[0].Status)
	require.Len(t, fake.authFiles, 1, "the run's registry credential is used")
	assert.Contains(t, progress, "the workflow token")
	assert.NotContains(t, progress, testWorkflowToken)
}

func TestPrepare_SandboxImageOverride(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	fake.respond("image", "", nil)
	sb := stubPrepareSandbox(t)
	t.Setenv("FULLSEND_SANDBOX_IMAGE", "quay.io/example/override:2")
	dir := writePrepareFixture(t, "image: "+prepareTestImage)

	res, _, err := runPrepareJSON(t, prepareOpts(dir))

	require.NoError(t, err)
	assert.Equal(t, "quay.io/example/override:2", res.Images[0].Ref)
	assert.Equal(t, prepareSourceOverride, res.Images[0].Source)
	assert.Equal(t, []string{"quay.io/example/override:2"}, sb.images)
}

func TestPrepare_SandboxArchOverride(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	fake.respond("image", "", nil)
	stubPrepareSandbox(t)
	t.Setenv("FULLSEND_SANDBOX_ARCH", "arm64")

	res, _, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage)))

	require.NoError(t, err)
	assert.Equal(t, "arm64", res.Arch)
}

func TestPrepare_SandboxArchFromEnvFile(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	fake.respond("image", "", nil)
	stubPrepareSandbox(t) // restores FULLSEND_SANDBOX_ARCH after the test
	envFile := filepath.Join(t.TempDir(), "arch.env")
	require.NoError(t, os.WriteFile(envFile, []byte("FULLSEND_SANDBOX_ARCH=arm64\n"), 0o600))
	opts := prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage))
	opts.envFiles = []string{envFile}

	res, _, err := runPrepareJSON(t, opts)

	require.NoError(t, err)
	assert.Equal(t, "arm64", res.Arch)
}

func TestPrepare_NoImageUsesGatewayDefault(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	sb := stubPrepareSandbox(t)

	res, _, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "")))

	require.NoError(t, err)
	assert.Equal(t, "", res.Images[0].Ref)
	assert.Equal(t, prepareSourceDefault, res.Images[0].Source)
	assert.Equal(t, prepareImageSkipped, res.Images[0].Status)
	assert.Empty(t, fake.calls)
	assert.Equal(t, []string{""}, sb.images)
}

func TestPrepare_RemoteGatewayIsNotWarmedLocally(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	sb := stubPrepareSandbox(t)
	t.Setenv("OPENSHELL_GATEWAY_ENDPOINT", "https://gateway.example.com:17670")

	res, progress, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage)))

	require.NoError(t, err)
	assert.Equal(t, prepareGatewayRemote, res.Gateway)
	assert.Empty(t, fake.calls, "the local Podman store is not the remote gateway's")
	assert.Equal(t, prepareImageSkipped, res.Images[0].Status)
	assert.Contains(t, res.Images[0].Detail, "OPENSHELL_GATEWAY_ENDPOINT")
	assert.Contains(t, res.Images[0].Detail, "the sandbox preflight pulled it through the gateway")
	assert.Contains(t, progress, "Not pulling "+prepareTestImage)
	assert.Equal(t, []string{prepareTestImage}, sb.images)
}

func TestPrepare_RemoteGatewayPreflightPullFailure(t *testing.T) {
	setupImagePull(t)
	sb := stubPrepareSandbox(t)
	sb.createErr = errors.New("sandbox create failed: Error: initializing source: unauthorized: authentication required")
	t.Setenv("OPENSHELL_GATEWAY_ENDPOINT", "https://gateway.example.com:17670")

	res, _, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage)))

	require.Error(t, err)
	assert.False(t, res.OK)
	assert.Equal(t, prepareImageFailed, res.Images[0].Status)
	require.NotNil(t, res.Error)
	assert.Equal(t, prepareErrAuth, res.Error.Kind)
	assert.Contains(t, res.Error.Hint, "CI_REGISTRY_PASSWORD")
	require.NotNil(t, res.Preflight)
	assert.Equal(t, preparePreflightFailed, res.Preflight.Status)
	assert.Empty(t, sb.deleted, "CreateWithRetry deletes a failed sandbox itself")
}

func TestPrepare_PodmanMissingLeavesWarmingToPreflight(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	imagePullLookPathFn = func(string) (string, error) { return "", errors.New("not found") }
	sb := stubPrepareSandbox(t)

	res, _, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage)))

	require.NoError(t, err)
	assert.Empty(t, fake.calls)
	assert.Equal(t, prepareImageSkipped, res.Images[0].Status)
	assert.Contains(t, res.Images[0].Detail, "podman not found")
	assert.Len(t, sb.created, 1)
}

func TestPrepare_PullFailureDiskSpace(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	fake.respond("pull", "Error: writing blob: storing blob to file: write /var/tmp/x: no space left on device", errors.New("exit status 125"))
	sb := stubPrepareSandbox(t)

	res, _, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage)))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no space left on device")
	assert.Contains(t, err.Error(), "hint: ")
	assert.False(t, res.OK)
	require.NotNil(t, res.Error)
	assert.Equal(t, prepareErrDiskSpace, res.Error.Kind)
	assert.NotContains(t, res.Error.Message, "hint:", "the hint has its own field")
	assert.NotEmpty(t, res.Error.Hint)
	assert.Equal(t, prepareImageFailed, res.Images[0].Status)
	assert.Nil(t, res.Preflight, "no preflight after a failed pull")
	assert.Empty(t, sb.created)
}

func TestPrepare_PullFailureRedactsCredential(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	fake.respond("pull", "Error: unauthorized "+testWorkflowToken, errors.New("exit status 125"))
	stubPrepareSandbox(t)

	res, progress, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+privateImage)))

	require.Error(t, err)
	assert.NotContains(t, err.Error(), testWorkflowToken)
	assert.NotContains(t, progress, testWorkflowToken)
	require.NotNil(t, res.Error)
	assert.Equal(t, prepareErrAuth, res.Error.Kind)
	assert.NotContains(t, res.Error.Message, testWorkflowToken)
}

func TestPrepare_PreflightFailureUnclassified(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	fake.respond("image", "", nil)
	sb := stubPrepareSandbox(t)
	sb.createErr = errors.New("sandbox \"x\" not ready after 42s")

	res, _, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage)))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating preflight sandbox")
	require.NotNil(t, res.Error)
	assert.Equal(t, prepareErrPreflight, res.Error.Kind)
	assert.Empty(t, res.Error.Hint)
	assert.Equal(t, prepareImageCached, res.Images[0].Status, "a cached image is not blamed for the preflight")
}

func TestPrepare_PreflightDeleteFailureWarns(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	fake.respond("image", "", nil)
	sb := stubPrepareSandbox(t)
	sb.deleteErr = errors.New("sandbox delete failed")

	res, progress, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage)))

	require.NoError(t, err)
	assert.True(t, res.OK)
	assert.Contains(t, progress, "Could not delete preflight sandbox")
}

func TestPrepare_GatewayAndOpenshellFailures(t *testing.T) {
	t.Run("openshell", func(t *testing.T) {
		setupImagePull(t)
		sb := stubPrepareSandbox(t)
		sb.ensureErr = errors.New("openshell not found in PATH")
		res, _, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage)))
		require.Error(t, err)
		require.NotNil(t, res.Error)
		assert.Equal(t, prepareErrOpenshell, res.Error.Kind)
		assert.Empty(t, sb.created)
	})
	t.Run("gateway", func(t *testing.T) {
		fake, _, _ := setupImagePull(t)
		sb := stubPrepareSandbox(t)
		sb.gatewayErr = errors.New("no openshell gateway running")
		res, _, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage)))
		require.Error(t, err)
		require.NotNil(t, res.Error)
		assert.Equal(t, prepareErrGateway, res.Error.Kind)
		assert.Empty(t, fake.calls, "nothing is pulled without a gateway")
		assert.Empty(t, sb.created)
	})
}

func TestPrepare_HarnessFailures(t *testing.T) {
	t.Run("unknown agent", func(t *testing.T) {
		setupImagePull(t)
		sb := stubPrepareSandbox(t)
		opts := prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage))
		opts.agentName = "nonexistent"
		res, _, err := runPrepareJSON(t, opts)
		require.Error(t, err)
		require.NotNil(t, res.Error)
		assert.Equal(t, prepareErrConfig, res.Error.Kind)
		assert.Empty(t, res.Images)
		assert.Empty(t, sb.created)
	})
	t.Run("malformed config", func(t *testing.T) {
		setupImagePull(t)
		stubPrepareSandbox(t)
		dir := writePrepareFixture(t, "image: "+prepareTestImage)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("agents: [\n"), 0o644))
		res, _, err := runPrepareJSON(t, prepareOpts(dir))
		require.Error(t, err)
		assert.Equal(t, prepareErrConfig, res.Error.Kind)
	})
	t.Run("invalid forge", func(t *testing.T) {
		setupImagePull(t)
		stubPrepareSandbox(t)
		opts := prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage))
		opts.forgeFlag = "nope"
		res, _, err := runPrepareJSON(t, opts)
		require.Error(t, err)
		assert.Equal(t, prepareErrConfig, res.Error.Kind)
	})
	t.Run("missing event file", func(t *testing.T) {
		setupImagePull(t)
		stubPrepareSandbox(t)
		opts := prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage))
		opts.eventFile = filepath.Join(t.TempDir(), "missing.json")
		res, _, err := runPrepareJSON(t, opts)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading event file")
		assert.Equal(t, prepareErrConfig, res.Error.Kind)
	})
	t.Run("missing env file", func(t *testing.T) {
		setupImagePull(t)
		stubPrepareSandbox(t)
		opts := prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage))
		opts.envFiles = []string{filepath.Join(t.TempDir(), "missing.env")}
		_, _, err := runPrepareJSON(t, opts)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "loading env file")
	})
	t.Run("bad resolve flags", func(t *testing.T) {
		setupImagePull(t)
		stubPrepareSandbox(t)
		opts := prepareOpts(writePrepareFixture(t, ""))
		opts.rFlags.maxDepth = -1
		_, _, err := runPrepareJSON(t, opts)
		require.ErrorContains(t, err, "--max-depth")
		opts.rFlags.maxDepth = 10
		opts.rFlags.maxResources = 0
		_, _, err = runPrepareJSON(t, opts)
		require.ErrorContains(t, err, "--max-resources")
	})
}

// prepare composes through composeRunHarness, the function runAgent uses,
// so it reports the image the run creates its sandbox from.
func TestPrepare_ImageMatchesRunComposition(t *testing.T) {
	setupImagePull(t)
	stubPrepareSandbox(t)
	dir := writePrepareFixture(t, "image: "+prepareTestImage)

	image, source, _, _, err := resolvePrepareImage(context.Background(), prepareOpts(dir), ui.New(io.Discard))
	require.NoError(t, err)

	orgCfg, err := loadLockConfig(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	composed, err := composeRunHarness(context.Background(), runHarnessInput{
		agentName:      "code",
		fullsendDir:    dir,
		absFullsendDir: dir,
		orgConfigPath:  filepath.Join(dir, "config.yaml"),
		orgCfg:         orgCfg,
		composeOpts:    newRunComposeOpts(dir, fetch.DefaultPolicy, "github", orgCfg.AllowedResources(), nil, "", nil, orgCfg),
	}, ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, composed.h.Image, image)
	assert.Equal(t, prepareSourceHarness, source)
}

func TestPrepareCmd_TextOutput(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	fake.respond("image", "", nil)
	stubPrepareSandbox(t)
	dir := writePrepareFixture(t, "image: "+prepareTestImage)

	var out bytes.Buffer
	opts := prepareOpts(dir)
	opts.jsonOut = false
	require.NoError(t, runPrepare(context.Background(), opts, ui.New(&out), &out))
	assert.Contains(t, out.String(), "Agent code is prepared")
	assert.NotContains(t, out.String(), `"ok"`)
}

func TestPrepareCmd_ExecuteJSON(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	fake.respond("image", "", nil)
	stubPrepareSandbox(t)
	dir := writePrepareFixture(t, "image: "+prepareTestImage)

	root := newRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"prepare", "code", "--fullsend-dir", dir, "--json"})
	require.NoError(t, root.ExecuteContext(context.Background()))

	var res prepareResult
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &res), "stdout holds only JSON: %s", stdout.String())
	assert.True(t, res.OK)
	assert.Contains(t, stderr.String(), "Preparing agent: code", "progress goes to stderr")
}

func TestPrepareCmd_Registered(t *testing.T) {
	cmd, _, err := newRootCmd().Find([]string{"prepare"})
	require.NoError(t, err)
	assert.Equal(t, "prepare", cmd.Name())
	for _, flag := range []string{"fullsend-dir", "env-file", "forge", "event-file", "json", "offline", "max-depth", "max-resources"} {
		assert.NotNil(t, cmd.Flags().Lookup(flag), flag)
	}
}

func TestReadRunEventFile(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.json")
	require.NoError(t, os.WriteFile(valid, []byte(routePROpened), 0o644))
	ev, m, err := readRunEventFile(valid)
	require.NoError(t, err)
	require.NotNil(t, ev)
	assert.NotEmpty(t, m)

	invalid := filepath.Join(dir, "invalid.json")
	require.NoError(t, os.WriteFile(invalid, []byte("{"), 0o644))
	_, _, err = readRunEventFile(invalid)
	require.ErrorContains(t, err, "parsing event file")

	_, _, err = readRunEventFile(filepath.Join(dir, "missing.json"))
	require.ErrorContains(t, err, "reading event file")
}

func TestPrepare_EventFile(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	fake.respond("image", "", nil)
	stubPrepareSandbox(t)
	opts := prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage))
	opts.eventFile = filepath.Join(t.TempDir(), "event.json")
	require.NoError(t, os.WriteFile(opts.eventFile, []byte(routePROpened), 0o644))

	res, _, err := runPrepareJSON(t, opts)

	require.NoError(t, err)
	assert.Equal(t, prepareTestImage, res.Images[0].Ref)
}

func TestClassifyPrepareFailure(t *testing.T) {
	tests := []struct {
		msg  string
		kind string
	}{
		{"write /var/lib/containers: no space left on device", prepareErrDiskSpace},
		{"Disk quota exceeded", prepareErrDiskSpace},
		{"Error: unauthorized: authentication required", prepareErrAuth},
		{"requested access to the resource is denied", prepareErrAuth},
		{"received unexpected HTTP status: 403 Forbidden", prepareErrAuth},
		{"reading manifest 1 in quay.io/x/y: manifest unknown", prepareErrUnresolvable},
		{"invalid reference format", prepareErrUnresolvable},
		{"dial tcp: lookup registry.invalid: no such host", prepareErrUnresolvable},
		{"open /etc/x: permission denied", prepareErrPull},
		{"something else", prepareErrPull},
	}
	for _, tc := range tests {
		kind, hint := classifyPrepareFailure(tc.msg, prepareErrPull)
		assert.Equal(t, tc.kind, kind, tc.msg)
		if kind == prepareErrPull {
			assert.Empty(t, hint, tc.msg)
		} else {
			assert.NotEmpty(t, hint, tc.msg)
		}
	}
}

func TestPrepareError(t *testing.T) {
	base := errors.New("boom")
	assert.Equal(t, "boom", (&prepareError{err: base}).Error())
	err := &prepareError{kind: prepareErrPull, hint: "do this", err: base}
	assert.Equal(t, "boom\nhint: do this", err.Error())
	assert.ErrorIs(t, err, base)
}
