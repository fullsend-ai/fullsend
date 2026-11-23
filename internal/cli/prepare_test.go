package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/agentnew"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
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

	// onCreate runs inside the fake create, with the create's context.
	onCreate func(ctx context.Context)

	created    []string // sandbox names
	images     []string // image per create
	policies   []string // policy path per create
	policyBody []string // policy file content at create time
	attempts   []int
	timeouts   []time.Duration
	deleted    []string
	// runner secrets variables visible to the create, as "NAME=value".
	createEnv []string
}

// stubPrepareSandbox replaces fullsend prepare's sandbox operations with a
// fake, and makes composition hermetic.
func stubPrepareSandbox(t *testing.T) *fakePrepareSandbox {
	t.Helper()
	neutralizeAgentsRepoFallback(t)
	t.Setenv("FULLSEND_SANDBOX_IMAGE", "")
	t.Setenv("FULLSEND_SANDBOX_ARCH", "")

	f := &fakePrepareSandbox{}
	origEnsure, origGateway, origCreate, origDelete := prepareEnsureOpenshellFn, prepareCheckGatewayFn, prepareCreateSandboxFn, deleteSandboxFn
	prepareEnsureOpenshellFn = func() error { return f.ensureErr }
	prepareCheckGatewayFn = func() error { return f.gatewayErr }
	prepareCreateSandboxFn = func(ctx context.Context, name string, providers []string, image, policy string, maxAttempts int, ready time.Duration, keep bool) error {
		assert.Empty(t, providers, "the preflight sandbox has no providers")
		assert.False(t, keep, "the preflight sandbox is never kept")
		f.created = append(f.created, name)
		f.images = append(f.images, image)
		f.policies = append(f.policies, policy)
		body, readErr := os.ReadFile(policy)
		assert.NoError(t, readErr, "the preflight policy file exists while the sandbox is created")
		f.policyBody = append(f.policyBody, string(body))
		for _, name := range []string{runnerSecretsFileEnv, runnerSecretsEnv} {
			if v, ok := os.LookupEnv(name); ok {
				f.createEnv = append(f.createEnv, name+"="+v)
			}
		}
		if f.onCreate != nil {
			f.onCreate(ctx)
		}
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

func TestPrepare_LocallyPresentImageSkipsPullButRunsPreflight(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	fake.respond("image", "", nil) // already present
	sb := stubPrepareSandbox(t)
	dir := writePrepareFixture(t, "image: "+privateImage)

	res, _, err := runPrepareJSON(t, prepareOpts(dir))

	require.NoError(t, err)
	assert.True(t, res.OK)
	assert.Equal(t, "code", res.Agent)
	assert.Equal(t, sandboxArch(), res.Arch)
	assert.Equal(t, filepath.Join(dir, "harness", "code.yaml"), res.Harness)
	require.Len(t, res.Images, 1)
	assert.Equal(t, prepareImage{Role: "sandbox", Ref: privateImage, Source: prepareSourceHarness, Status: prepareImageReady,
		Detail: "already in the local Podman store; the preflight sandbox started from it, so the gateway has pulled it"}, res.Images[0])
	assert.Equal(t, []string{"image"}, fake.subcommands(), "an image in the local store is not pulled again")

	require.Len(t, sb.created, 1, "the preflight still runs for a locally present image")
	assert.Equal(t, []string{privateImage}, sb.images)
	require.Len(t, sb.policies, 1)
	assert.NotEmpty(t, sb.policies[0], "the preflight sandbox is created with a policy")
	assert.Equal(t, []int{1}, sb.attempts)
	assert.Equal(t, []time.Duration{42 * time.Second}, sb.timeouts, "the harness sandbox timeout applies")
	assert.Equal(t, sb.created, sb.deleted, "the preflight sandbox is deleted")
	require.NotNil(t, res.Preflight)
	assert.Equal(t, preparePreflight{Sandbox: sb.created[0], Status: preparePreflightPassed, Cleanup: prepareCleanupDeleted}, *res.Preflight)
	assert.Nil(t, res.Error)
}

// The preflight sandbox uses the restrictive base policy: filesystem and
// process rules, and no network section. The file is gone afterwards.
func TestPrepare_PreflightUsesRestrictivePolicy(t *testing.T) {
	setupImagePull(t)
	sb := stubPrepareSandbox(t)

	_, _, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage)))

	require.NoError(t, err)
	require.Len(t, sb.policyBody, 1)
	assert.Equal(t, string(agentnew.BasePolicy()), sb.policyBody[0])
	assert.Contains(t, sb.policyBody[0], "filesystem_policy")
	assert.NotContains(t, sb.policyBody[0], "network_policies")
	_, statErr := os.Stat(sb.policies[0])
	assert.True(t, os.IsNotExist(statErr), "the policy file is removed after the preflight")
}

// A public image is pulled by the gateway into its own store during the
// preflight, so prepare pulls nothing locally.
func TestPrepare_PublicImageIsLeftToGateway(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	sb := stubPrepareSandbox(t)
	dir := writePrepareFixture(t, "image: "+prepareTestImage)

	res, progress, err := runPrepareJSON(t, prepareOpts(dir))

	require.NoError(t, err)
	assert.Empty(t, fake.calls, "no local pull for an image the gateway can pull itself")
	assert.Equal(t, prepareImageReady, res.Images[0].Status)
	assert.Contains(t, res.Images[0].Detail, "no registry credential applies")
	assert.Contains(t, progress, "gateway pulls the image into its own image store")
	assert.Equal(t, []string{prepareTestImage}, sb.images)
}

func TestPrepare_PullsGHCRImageWithWorkflowToken(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	stubPrepareSandbox(t)
	dir := writePrepareFixture(t, "image: "+privateImage)

	res, progress, err := runPrepareJSON(t, prepareOpts(dir))

	require.NoError(t, err)
	assert.Equal(t, prepareImageReady, res.Images[0].Status)
	assert.Contains(t, res.Images[0].Detail, "pulled into the local Podman store")
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
	assert.Equal(t, prepareImageReady, res.Images[0].Status)
	assert.Contains(t, res.Images[0].Detail, "gateway uses its default sandbox image")
	assert.Empty(t, fake.calls)
	assert.Equal(t, []string{""}, sb.images)
}

// prepare mints nothing, so it uses the caller's GH_TOKEN or GITHUB_TOKEN as
// the workflow token when GH_WORKFLOW_TOKEN is not exported, as run would
// after preserving it.
func TestPrepare_PullsGHCRImageWithoutWorkflowTokenVariable(t *testing.T) {
	const actionsToken = "ghs_exampleActionsToken123"
	tests := []struct {
		name        string
		ghToken     string
		githubToken string
	}{
		{"GH_TOKEN", actionsToken, ""},
		{"GITHUB_TOKEN", "", actionsToken},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake, _, _ := setupImagePull(t)
			stubPrepareSandbox(t)
			t.Setenv(workflowTokenEnv, "")
			t.Setenv("GH_TOKEN", tc.ghToken)
			t.Setenv("GITHUB_TOKEN", tc.githubToken)

			res, progress, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+privateImage)))

			require.NoError(t, err)
			assert.Equal(t, prepareImageReady, res.Images[0].Status)
			require.Len(t, fake.authFiles, 1, "the actions token authenticates the pull")
			var authFile struct {
				Auths map[string]struct{ Auth string } `json:"auths"`
			}
			require.NoError(t, json.Unmarshal(fake.authJSON, &authFile))
			wantAuth := base64.StdEncoding.EncodeToString([]byte(ghcrAuthUsername + ":" + actionsToken))
			assert.Equal(t, wantAuth, authFile.Auths[ghcrRegistry].Auth)
			assert.NotContains(t, progress, actionsToken)
			assert.Empty(t, os.Getenv(workflowTokenEnv), "prepare does not export a workflow token")
		})
	}
}

// Without any workflow credential the private image is left to the gateway.
func TestPrepare_NoWorkflowCredentialLeavesPrivateImageToGateway(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	stubPrepareSandbox(t)
	t.Setenv(workflowTokenEnv, "")

	res, _, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+privateImage)))

	require.NoError(t, err)
	assert.Equal(t, prepareImageReady, res.Images[0].Status)
	assert.Empty(t, fake.calls)
}

func TestPrepare_MalformedActionsTokenIsWarnedAndNotUsed(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	stubPrepareSandbox(t)
	t.Setenv(workflowTokenEnv, "")
	t.Setenv("GH_TOKEN", "not a token;")

	res, progress, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+privateImage)))

	require.NoError(t, err)
	assert.Equal(t, prepareImageReady, res.Images[0].Status)
	assert.Empty(t, fake.calls)
	assert.Contains(t, progress, "GH_TOKEN has an unexpected format")
	assert.NotContains(t, progress, "not a token;")
}

// Cancellation that arrives while the preflight sandbox becomes ready is
// reported after the sandbox is deleted, not as success.
func TestPrepare_CanceledAfterPreflightReadyIsReportedAfterCleanup(t *testing.T) {
	setupImagePull(t)
	sb := stubPrepareSandbox(t)
	ctx, cancel := context.WithCancel(context.Background())
	sb.onCreate = func(context.Context) { cancel() } // create itself succeeds
	var progress, out bytes.Buffer

	err := runPrepare(ctx, prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage)), ui.New(&progress), &out)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	var res prepareResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &res))
	assert.False(t, res.OK)
	require.NotNil(t, res.Error)
	assert.Equal(t, prepareErrCanceled, res.Error.Kind)
	assert.Equal(t, []string{sb.created[0]}, sb.deleted, "the sandbox that was created is deleted")
	assert.Equal(t, prepareCleanupDeleted, res.Preflight.Cleanup)
}

// A canceled run whose sandbox cannot be deleted still names it for removal.
func TestPrepare_CanceledAfterPreflightReadyWithDeleteFailureNamesSandbox(t *testing.T) {
	setupImagePull(t)
	sb := stubPrepareSandbox(t)
	ctx, cancel := context.WithCancel(context.Background())
	sb.onCreate = func(context.Context) { cancel() }
	sb.deleteErr = errors.New("delete refused")
	var progress, out bytes.Buffer

	err := runPrepare(ctx, prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage)), ui.New(&progress), &out)

	require.Error(t, err)
	var res prepareResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &res))
	require.NotNil(t, res.Error)
	assert.Equal(t, prepareErrCleanup, res.Error.Kind)
	assert.Contains(t, res.Error.Hint, "openshell sandbox delete "+sb.created[0])
}

func TestPrepare_PodmanMissingLeavesWarmingToPreflight(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	imagePullLookPathFn = func(string) (string, error) { return "", errors.New("not found") }
	sb := stubPrepareSandbox(t)

	res, _, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+privateImage)))

	require.NoError(t, err)
	assert.Empty(t, fake.calls)
	assert.Contains(t, res.Images[0].Detail, "the gateway pulls the image without registry credentials")
	assert.Len(t, sb.created, 1)
}

// A full disk during the local pull is not decided here: the pull is a
// warning, as in fullsend run, and the preflight, whose gateway pull would
// hit the same disk, reports the failure.
func TestPrepare_LocalPullDiskSpaceFailureIsLeftToPreflight(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	fake.respond("pull", "Error: writing blob: storing blob to file: write /var/tmp/x: no space left on device", errors.New("exit status 125"))
	sb := stubPrepareSandbox(t)
	sb.createErr = errors.New("sandbox create failed: no space left on device")

	res, progress, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+privateImage)))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no space left on device")
	assert.Contains(t, err.Error(), "hint: ")
	assert.False(t, res.OK)
	require.NotNil(t, res.Error)
	assert.Equal(t, prepareErrDiskSpace, res.Error.Kind)
	assert.NotContains(t, res.Error.Message, "hint:", "the hint has its own field")
	assert.NotEmpty(t, res.Error.Hint)
	assert.Equal(t, prepareImageFailed, res.Images[0].Status)
	assert.Contains(t, progress, "no space left on device", "the local pull failure is warned about")
	require.NotNil(t, res.Preflight, "the preflight decides")
	assert.Equal(t, preparePreflightFailed, res.Preflight.Status)
	assert.Len(t, sb.created, 1)
}

// A local pull never marks the image ready: if the preflight fails for a
// reason that is not the image's, the image stays unverified.
func TestPrepare_LocalPullDoesNotMakeImageReady(t *testing.T) {
	setupImagePull(t)
	sb := stubPrepareSandbox(t)
	sb.createErr = errors.New("sandbox \"x\" not ready after 42s")

	res, _, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+privateImage)))

	require.Error(t, err)
	assert.Equal(t, prepareImageUnverified, res.Images[0].Status)
	assert.Contains(t, res.Images[0].Detail, "pulled into the local Podman store")
	assert.NotContains(t, res.Images[0].Detail, "the gateway has pulled it")
}

// A failed credentialed pull falls back to the gateway's own pull, as
// fullsend run does, so a public image keeps working. The credential is
// redacted from every message.
func TestPrepare_PullFailureFallsBackToGatewayPull(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	fake.respond("pull", "Error: unauthorized "+testWorkflowToken, errors.New("exit status 125"))
	sb := stubPrepareSandbox(t)

	res, progress, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+privateImage)))

	require.NoError(t, err)
	assert.True(t, res.OK)
	assert.Equal(t, prepareImageReady, res.Images[0].Status)
	assert.Contains(t, res.Images[0].Detail, "the gateway pulls the image without registry credentials")
	assert.NotContains(t, res.Images[0].Detail, testWorkflowToken)
	assert.NotContains(t, progress, testWorkflowToken)
	assert.Len(t, sb.created, 1, "the preflight still runs, pulling through the gateway")
}

func TestPrepare_PullFailureThenPreflightFailureRedactsCredential(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	fake.respond("pull", "Error: unauthorized "+testWorkflowToken, errors.New("exit status 125"))
	sb := stubPrepareSandbox(t)
	sb.createErr = errors.New("unauthorized: authentication required")

	res, progress, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+privateImage)))

	require.Error(t, err)
	assert.NotContains(t, err.Error(), testWorkflowToken)
	assert.NotContains(t, progress, testWorkflowToken)
	require.NotNil(t, res.Error)
	assert.Equal(t, prepareErrAuth, res.Error.Kind)
	assert.Equal(t, prepareImageFailed, res.Images[0].Status)
	assert.NotContains(t, res.Images[0].Detail, testWorkflowToken)
}

// prepare must not consume the runner secrets bundle, but no child process
// may see it: not podman, not the sandbox tooling.
func TestPrepare_RunnerSecretsBundleHiddenFromChildrenAndRestored(t *testing.T) {
	setupImagePull(t)
	sb := stubPrepareSandbox(t)
	bundleFile := filepath.Join(t.TempDir(), "bundle.json")
	require.NoError(t, os.WriteFile(bundleFile, []byte(`{"TOKEN_A":"secret-value-aaaaaaaa"}`), 0o600))
	t.Setenv(runnerSecretsFileEnv, bundleFile)
	t.Setenv(runnerSecretsEnv, `{"TOKEN_B":"secret-value-bbbbbbbb"}`)

	var podmanEnv []string
	podmanCalls := 0
	podmanRun := runPodmanFn
	runPodmanFn = func(ctx context.Context, args ...string) ([]byte, error) {
		podmanCalls++
		for _, name := range []string{runnerSecretsFileEnv, runnerSecretsEnv} {
			if _, ok := os.LookupEnv(name); ok {
				podmanEnv = append(podmanEnv, name)
			}
		}
		return podmanRun(ctx, args...)
	}

	res, _, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+privateImage)))

	require.NoError(t, err)
	assert.True(t, res.OK)
	assert.Equal(t, 2, podmanCalls, "podman ran to look up and pull the image")
	assert.Empty(t, podmanEnv, "podman must not inherit the runner secrets bundle")
	assert.Empty(t, sb.createEnv, "the sandbox tooling must not inherit the runner secrets bundle")
	assert.Equal(t, bundleFile, os.Getenv(runnerSecretsFileEnv), "the bundle file variable is restored")
	assert.Equal(t, `{"TOKEN_B":"secret-value-bbbbbbbb"}`, os.Getenv(runnerSecretsEnv), "the inline bundle is restored")
	_, statErr := os.Stat(bundleFile)
	assert.NoError(t, statErr, "the bundle file is not deleted")
}

func TestHideRunnerSecretsEnv_NothingSetRestoresNothing(t *testing.T) {
	_, hadFile := os.LookupEnv(runnerSecretsFileEnv)
	_, hadInline := os.LookupEnv(runnerSecretsEnv)
	if hadFile || hadInline {
		t.Skip("runner secrets variables set in the test environment")
	}

	restore, err := hideRunnerSecretsEnv()
	require.NoError(t, err)
	restore()

	_, ok := os.LookupEnv(runnerSecretsFileEnv)
	assert.False(t, ok)
	_, ok = os.LookupEnv(runnerSecretsEnv)
	assert.False(t, ok)
}

func TestPrepare_PreflightFailureUnclassified(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	fake.respond("image", "", nil)
	sb := stubPrepareSandbox(t)
	sb.createErr = errors.New("sandbox \"x\" not ready after 42s")

	res, _, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+privateImage)))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating preflight sandbox")
	require.NotNil(t, res.Error)
	assert.Equal(t, prepareErrPreflight, res.Error.Kind)
	assert.Empty(t, res.Error.Hint)
	assert.Equal(t, prepareImageUnverified, res.Images[0].Status, "an unrelated preflight failure does not blame the image")
}

// A preflight sandbox that cannot be deleted is reported, not ignored: a
// leaked sandbox holds the disk the preflight exists to protect.
func TestPrepare_PreflightDeleteFailureFails(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	fake.respond("image", "", nil)
	sb := stubPrepareSandbox(t)
	sb.deleteErr = errors.New("sandbox delete failed")

	res, progress, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage)))

	require.Error(t, err)
	assert.False(t, res.OK)
	require.NotNil(t, res.Error)
	assert.Equal(t, prepareErrCleanup, res.Error.Kind)
	assert.Contains(t, res.Error.Hint, "openshell sandbox delete "+sb.created[0])
	require.NotNil(t, res.Preflight)
	assert.Equal(t, preparePreflightPassed, res.Preflight.Status, "the sandbox itself started")
	assert.Equal(t, prepareCleanupFailed, res.Preflight.Cleanup)
	assert.Equal(t, prepareImageReady, res.Images[0].Status, "a teardown failure is not the image's failure")
	assert.Contains(t, progress, "Could not delete preflight sandbox")
}

// A failed creation whose cleanup also failed names the sandbox to delete.
func TestPrepare_PreflightCreateCleanupFailureIsReported(t *testing.T) {
	setupImagePull(t)
	sb := stubPrepareSandbox(t)
	sb.createErr = fmt.Errorf("sandbox not ready (%w for sandbox x: delete refused)", sandbox.ErrCleanupFailed)

	res, _, err := runPrepareJSON(t, prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage)))

	require.Error(t, err)
	require.NotNil(t, res.Error)
	assert.Equal(t, prepareErrPreflight, res.Error.Kind)
	assert.Contains(t, res.Error.Hint, "openshell sandbox delete "+sb.created[0])
	assert.Equal(t, prepareCleanupFailed, res.Preflight.Cleanup)
}

func TestPrepare_CanceledBeforePreflightCreatesNothing(t *testing.T) {
	setupImagePull(t)
	sb := stubPrepareSandbox(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var progress, out bytes.Buffer

	err := runPrepare(ctx, prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage)), ui.New(&progress), &out)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	var res prepareResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &res))
	require.NotNil(t, res.Error)
	assert.Equal(t, prepareErrCanceled, res.Error.Kind)
	assert.Empty(t, sb.created, "no sandbox is created after cancellation")
}

func TestPrepare_CanceledDuringPreflightReportsCancellation(t *testing.T) {
	setupImagePull(t)
	sb := stubPrepareSandbox(t)
	ctx, cancel := context.WithCancel(context.Background())
	sb.onCreate = func(context.Context) { cancel() }
	sb.createErr = fmt.Errorf("sandbox creation canceled: %w", context.Canceled)
	var progress, out bytes.Buffer

	err := runPrepare(ctx, prepareOpts(writePrepareFixture(t, "image: "+prepareTestImage)), ui.New(&progress), &out)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	var res prepareResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &res))
	require.NotNil(t, res.Error)
	assert.Equal(t, prepareErrCanceled, res.Error.Kind)
	assert.Equal(t, preparePreflightFailed, res.Preflight.Status)
}

func TestPrepare_CanceledDuringPullIsNotAFallback(t *testing.T) {
	setupImagePull(t)
	sb := stubPrepareSandbox(t)
	ctx, cancel := context.WithCancel(context.Background())
	runPodmanFn = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "pull" {
			cancel()
			return nil, context.Canceled
		}
		return nil, errors.New("exit status 1") // not cached
	}
	var progress, out bytes.Buffer

	err := runPrepare(ctx, prepareOpts(writePrepareFixture(t, "image: "+privateImage)), ui.New(&progress), &out)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, sb.created, "a canceled pull does not fall back to a gateway pull")
}

// Cancellation stops preparation between steps, before the next one starts.
func TestPrepare_CanceledBetweenStepsStopsEarly(t *testing.T) {
	for _, afterGateway := range []bool{false, true} {
		name := "after the openshell check"
		if afterGateway {
			name = "after the gateway check"
		}
		t.Run(name, func(t *testing.T) {
			fake, _, _ := setupImagePull(t)
			sb := stubPrepareSandbox(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			gatewayChecked := false
			prepareEnsureOpenshellFn = func() error {
				if !afterGateway {
					cancel()
				}
				return nil
			}
			prepareCheckGatewayFn = func() error {
				gatewayChecked = true
				cancel()
				return nil
			}
			var progress, out bytes.Buffer

			err := runPrepare(ctx, prepareOpts(writePrepareFixture(t, "image: "+privateImage)), ui.New(&progress), &out)

			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			var res prepareResult
			require.NoError(t, json.Unmarshal(out.Bytes(), &res))
			require.NotNil(t, res.Error)
			assert.Equal(t, prepareErrCanceled, res.Error.Kind)
			assert.Equal(t, afterGateway, gatewayChecked)
			assert.Empty(t, fake.calls, "nothing is pulled after cancellation")
			assert.Empty(t, sb.created)
		})
	}
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
	for _, flag := range []string{"fullsend-dir", "env-file", "forge", "event-file", "json", "offline"} {
		assert.NotNil(t, cmd.Flags().Lookup(flag), flag)
	}
	// Composing a harness never reads these resolve limits, so prepare must
	// not offer flags that change nothing.
	for _, flag := range []string{"max-depth", "max-resources"} {
		assert.Nil(t, cmd.Flags().Lookup(flag), flag)
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
		{"open /etc/x: permission denied", prepareErrPreflight},
		{"something else", prepareErrPreflight},
	}
	for _, tc := range tests {
		kind, hint := classifyPrepareFailure(tc.msg)
		assert.Equal(t, tc.kind, kind, tc.msg)
		if kind == prepareErrPreflight {
			assert.Empty(t, hint, tc.msg)
		} else {
			assert.NotEmpty(t, hint, tc.msg)
		}
	}
}

func TestPrepareError(t *testing.T) {
	base := errors.New("boom")
	assert.Equal(t, "boom", (&prepareError{err: base}).Error())
	err := &prepareError{kind: prepareErrPreflight, hint: "do this", err: base}
	assert.Equal(t, "boom\nhint: do this", err.Error())
	assert.ErrorIs(t, err, base)
}
