package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/dispatch/cf"
	"github.com/fullsend-ai/fullsend/internal/dispatch/gcf"
)

const testCollectorHeaderValue = "collector-header-value-for-tests"

func writeHeadersFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "otlp-headers")
	require.NoError(t, os.WriteFile(path, []byte(content), mode))
	require.NoError(t, os.Chmod(path, mode))
	return path
}

func TestResolveMintTracing(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		cfg, err := resolveMintTracing("", "", "", false)
		require.NoError(t, err)
		assert.False(t, cfg.enabled())
		assert.Nil(t, cfg.headers)
	})
	t.Run("headers file requires an endpoint", func(t *testing.T) {
		_, err := resolveMintTracing("", "", "/nonexistent", false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires --otlp-traces-endpoint")
	})
	t.Run("no-headers requires an endpoint", func(t *testing.T) {
		_, err := resolveMintTracing("", "", "", true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--otlp-no-headers requires")
	})
	t.Run("no-headers excludes headers file", func(t *testing.T) {
		_, err := resolveMintTracing("https://collector.example/v1/traces", "", "/nonexistent", true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutually exclusive")
	})
	t.Run("no-headers is recorded", func(t *testing.T) {
		cfg, err := resolveMintTracing("https://collector.example/v1/traces", "", "", true)
		require.NoError(t, err)
		assert.True(t, cfg.noHeaders)
	})
	t.Run("base endpoint gets the traces path", func(t *testing.T) {
		cfg, err := resolveMintTracing("", "https://collector.example/", "", false)
		require.NoError(t, err)
		assert.Equal(t, "https://collector.example/v1/traces", cfg.endpoint)
		assert.Equal(t, "https://collector.example", cfg.endpointOrigin())
	})
	t.Run("traces endpoint takes precedence", func(t *testing.T) {
		cfg, err := resolveMintTracing("https://a.example/custom", "https://b.example", "", false)
		require.NoError(t, err)
		assert.Equal(t, "https://a.example/custom", cfg.endpoint)
	})
	t.Run("invalid endpoint", func(t *testing.T) {
		_, err := resolveMintTracing("https://user:pass@collector.example/v1/traces", "", "", false)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "pass")
	})
	t.Run("valid headers file", func(t *testing.T) {
		path := writeHeadersFile(t, "Authorization=Bearer%20"+testCollectorHeaderValue+"\nX-Team=blue\n", 0o600)
		cfg, err := resolveMintTracing("https://collector.example/v1/traces", "", path, false)
		require.NoError(t, err)
		assert.Equal(t, 2, cfg.headerCount)
		assert.Equal(t, "Authorization=Bearer%20"+testCollectorHeaderValue+"\nX-Team=blue", string(cfg.headers))
	})
}

func TestReadOTLPHeadersFile_Rejects(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Run("group or world readable", func(t *testing.T) {
			path := writeHeadersFile(t, "Authorization=x", 0o644)
			_, _, err := readOTLPHeadersFile(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "chmod 600")
		})
	}
	t.Run("missing file", func(t *testing.T) {
		_, _, err := readOTLPHeadersFile(filepath.Join(t.TempDir(), "missing"))
		require.Error(t, err)
	})
	t.Run("directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "d")
		require.NoError(t, os.Mkdir(dir, 0o700))
		_, _, err := readOTLPHeadersFile(dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "regular file")
	})
	t.Run("empty", func(t *testing.T) {
		_, _, err := readOTLPHeadersFile(writeHeadersFile(t, "\n# only a comment\n", 0o600))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no headers")
	})
	t.Run("malformed without leaking values", func(t *testing.T) {
		_, _, err := readOTLPHeadersFile(writeHeadersFile(t, "no-separator-"+testCollectorHeaderValue, 0o600))
		require.Error(t, err)
		assert.NotContains(t, err.Error(), testCollectorHeaderValue)
	})
	t.Run("encoded line break", func(t *testing.T) {
		_, _, err := readOTLPHeadersFile(writeHeadersFile(t, "X-Auth=a%0D%0AX-Evil:%201", 0o600))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid value")
	})
	t.Run("too large", func(t *testing.T) {
		_, _, err := readOTLPHeadersFile(writeHeadersFile(t, "X-Auth="+strings.Repeat("a", maxOTLPHeadersFileSize), 0o600))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "larger than")
	})
}

// captureWASMBuildStamp stubs the WASM build (after
// createMinimalWorkerSourceDir) and records the tracing stamp it receives.
func captureWASMBuildStamp(t *testing.T) *cf.TracingStamp {
	t.Helper()
	var stamp cf.TracingStamp
	orig := cf.BuildWASMFn
	cf.BuildWASMFn = func(outPath, _, _ string, _ cf.StatusGitHubAuth, tracing cf.TracingStamp) error {
		stamp = tracing
		return os.WriteFile(outPath, []byte("fake-wasm"), 0o644)
	}
	t.Cleanup(func() { cf.BuildWASMFn = orig })
	return &stamp
}

func TestMintDeployCmd_CloudflarePreviewWithTracing(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	stamp := captureWASMBuildStamp(t)
	fake := &fakeCFWranglerRunner{deployURL: "https://bt-1-bt-mint.workers.dev"}
	withMintCFWrangler(t, fake)
	headersFile := writeHeadersFile(t, "Authorization=Bearer%20"+testCollectorHeaderValue, 0o600)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--preview=bt-1",
		"--worker-name=bt-mint",
		"--source-dir=" + sourceDir,
		"--otlp-traces-endpoint=https://collector.example/v1/traces",
		"--otlp-headers-file=" + headersFile,
	})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, cf.TracingStamp{Endpoint: "https://collector.example/v1/traces", Deployment: "bt-mint@bt-1"}, *stamp)
	assert.NotContains(t, fmt.Sprint(*stamp), testCollectorHeaderValue, "header values must not be stamped into the WASM build")
	require.Len(t, fake.deployCalls, 1)
	assert.Equal(t, "Authorization=Bearer%20"+testCollectorHeaderValue,
		string(fake.deployCalls[0].secrets[cf.TraceHeadersSecretName]),
		"preview collector headers must be a version-scoped secret")
	assert.Empty(t, fake.secretCalls, "previews must not use wrangler secret put")
	for _, v := range fake.deployCalls[0].envVars {
		assert.NotContains(t, v, testCollectorHeaderValue, "header values must not be plain Worker vars")
	}
}

func TestMintDeployCmd_CloudflareDurableWithTracing(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	stamp := captureWASMBuildStamp(t)
	fake := &fakeCFWranglerRunner{}
	withMintCFWrangler(t, fake)
	headersFile := writeHeadersFile(t, "Authorization=Bearer%20"+testCollectorHeaderValue, 0o600)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--worker-name=stage-mint",
		"--source-dir=" + sourceDir,
		"--otlp-endpoint=https://collector.example",
		"--otlp-headers-file=" + headersFile,
	})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, "https://collector.example/v1/traces", stamp.Endpoint)
	assert.Equal(t, "stage-mint", stamp.Deployment)
	assert.True(t, strings.HasPrefix(stamp.HeadersSecret, cf.TraceHeadersSecretName+"_"),
		"a durable build must read headers from a per-deploy secret name, got %q", stamp.HeadersSecret)
	require.Len(t, fake.deployCalls, 1)
	assert.Empty(t, fake.deployCalls[0].secrets)
	require.Len(t, fake.secretCalls, 1)
	assert.Equal(t, stamp.HeadersSecret, fake.secretCalls[0].secretName,
		"headers must be stored under the secret name stamped into the build")
	assert.Equal(t, "stage-mint", fake.secretCalls[0].workerName)
}

// A durable collector rotation deploys the stamped build first, then
// stores the headers under that build's own secret name. Nothing is
// deleted: the previous collector's secret keeps its own name, which the
// new build never reads.
func TestMintDeployCmd_CloudflareDurableTracingRotationUsesFreshSecretName(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	stamp := captureWASMBuildStamp(t)
	fake := &fakeCFWranglerRunner{}
	withMintCFWrangler(t, fake)
	headersFile := writeHeadersFile(t, "Authorization=Bearer%20"+testCollectorHeaderValue, 0o600)

	var names []string
	for _, endpoint := range []string{"https://collector-a.example/v1/traces", "https://collector-b.example/v1/traces"} {
		cmd := newRootCmd()
		cmd.SetArgs([]string{
			"mint", "deploy",
			"--platform=cloudflare",
			"--worker-name=stage-mint",
			"--source-dir=" + sourceDir,
			"--otlp-traces-endpoint=" + endpoint,
			"--otlp-headers-file=" + headersFile,
		})
		require.NoError(t, cmd.Execute())
		names = append(names, stamp.HeadersSecret)
	}

	require.Len(t, names, 2)
	assert.NotEqual(t, names[0], names[1], "each deploy must stamp its own secret name")
	assert.Equal(t, []string{
		"deploy", "put:" + names[0],
		"deploy", "put:" + names[1],
	}, fake.events)
}

// Two overlapping durable deploys must never leave one collector's
// credentials readable by the build for the other's endpoint. Deploy A
// finishes its Worker deploy, deploy B then runs completely (so B's build
// is live), and only then does A store its headers. The live build (B)
// must read only the secret B stored.
func TestMintDeployCmd_CloudflareDurableTracingInterleavedDeploysKeepHeadersPaired(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	var builds []cf.TracingStamp
	orig := cf.BuildWASMFn
	cf.BuildWASMFn = func(outPath, _, _ string, _ cf.StatusGitHubAuth, tracing cf.TracingStamp) error {
		builds = append(builds, tracing)
		return os.WriteFile(outPath, []byte("fake-wasm"), 0o644)
	}
	t.Cleanup(func() { cf.BuildWASMFn = orig })
	fake := &fakeCFWranglerRunner{}
	withMintCFWrangler(t, fake)
	headersA := writeHeadersFile(t, "Authorization=Bearer%20credential-for-collector-a", 0o600)
	headersB := writeHeadersFile(t, "Authorization=Bearer%20credential-for-collector-b", 0o600)

	deploy := func(endpoint, headersFile string) error {
		cmd := newRootCmd()
		cmd.SetArgs([]string{
			"mint", "deploy",
			"--platform=cloudflare",
			"--worker-name=stage-mint",
			"--source-dir=" + sourceDir,
			"--otlp-traces-endpoint=" + endpoint,
			"--otlp-headers-file=" + headersFile,
		})
		return cmd.Execute()
	}

	// Deploy B runs to completion in the middle of deploy A, between A's
	// Worker deploy and A's secret put.
	var errB error
	fake.beforePut = func() { errB = deploy("https://collector-b.example/v1/traces", headersB) }
	require.NoError(t, deploy("https://collector-a.example/v1/traces", headersA))
	require.NoError(t, errB)

	require.Len(t, builds, 2)
	live := builds[1] // B's build was deployed last
	assert.Equal(t, "https://collector-b.example/v1/traces", live.Endpoint)

	// The Worker's secrets, as the fake saw them stored.
	secrets := map[string]string{}
	for _, c := range fake.secretCalls {
		secrets[c.secretName] = string(c.value)
	}
	assert.Equal(t, "Authorization=Bearer%20credential-for-collector-b", secrets[live.HeadersSecret],
		"the live build must read the headers stored by its own deploy")
	assert.Len(t, secrets, 2, "each deploy stores its headers under its own name")
}

func TestMintDeployCmd_CloudflareDurableTracingNoHeaders(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	fake := &fakeCFWranglerRunner{}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--otlp-traces-endpoint=https://collector.example/v1/traces",
		"--otlp-no-headers",
	})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, []string{"deploy"}, fake.events, "no headers are stored and no shared secret is touched")
}

func TestMintDeployCmd_CloudflareDurableTracingRequiresHeadersChoice(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	fake := &fakeCFWranglerRunner{}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--otlp-traces-endpoint=https://collector.example/v1/traces",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--otlp-headers-file or --otlp-no-headers")
	assert.Empty(t, fake.events, "nothing may be deployed or cleared")
}

func TestMintDeployCmd_CloudflarePreviewTracingWithoutHeadersAllowed(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	fake := &fakeCFWranglerRunner{}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--preview=bt-4",
		"--source-dir=" + sourceDir,
		"--otlp-traces-endpoint=https://collector.example/v1/traces",
	})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, []string{"deploy"}, fake.events, "previews are version-scoped; no secret clearing")
	require.Len(t, fake.deployCalls, 1)
	got, ok := fake.deployCalls[0].secrets[cf.TraceHeadersSecretName]
	require.True(t, ok, "a traced preview must bind the headers secret explicitly so durable credentials are not inherited")
	assert.Empty(t, got)
}

func TestMintDeployCmd_CloudflarePreviewTracingNoHeadersBindsEmptySecret(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	fake := &fakeCFWranglerRunner{}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--preview=bt-5",
		"--source-dir=" + sourceDir,
		"--otlp-traces-endpoint=https://collector.example/v1/traces",
		"--otlp-no-headers",
	})
	require.NoError(t, cmd.Execute())
	require.Len(t, fake.deployCalls, 1)
	got, ok := fake.deployCalls[0].secrets[cf.TraceHeadersSecretName]
	require.True(t, ok)
	assert.Empty(t, got)
}

func TestMintDeployCmd_OTLPNoHeadersValidation(t *testing.T) {
	t.Run("requires endpoint", func(t *testing.T) {
		cmd := newRootCmd()
		cmd.SetArgs([]string{"mint", "deploy", "--platform=cloudflare", "--otlp-no-headers"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--otlp-no-headers requires")
	})
	t.Run("excludes headers file", func(t *testing.T) {
		headersFile := writeHeadersFile(t, "Authorization=x", 0o600)
		cmd := newRootCmd()
		cmd.SetArgs([]string{
			"mint", "deploy", "--platform=cloudflare",
			"--otlp-traces-endpoint=https://collector.example/v1/traces",
			"--otlp-headers-file=" + headersFile,
			"--otlp-no-headers",
		})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutually exclusive")
	})
}

func TestMintDeployCmd_CloudflareDurableTracingSecretError(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	withMintCFWrangler(t, &fakeCFWranglerRunner{secretPutErr: fmt.Errorf("api error")})
	headersFile := writeHeadersFile(t, "Authorization=x", 0o600)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--otlp-traces-endpoint=https://collector.example/v1/traces",
		"--otlp-headers-file=" + headersFile,
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storing collector headers")
}

func TestMintDeployCmd_CloudflareWithoutTracingStampsNothing(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	stamp := captureWASMBuildStamp(t)
	fake := &fakeCFWranglerRunner{}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--platform=cloudflare", "--preview=bt-2", "--source-dir=" + sourceDir})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, cf.TracingStamp{}, *stamp)
	require.Len(t, fake.deployCalls, 1)
	_, ok := fake.deployCalls[0].secrets[cf.TraceHeadersSecretName]
	assert.False(t, ok)
}

func TestMintDeployCmd_CloudflareDryRunWithTracing(t *testing.T) {
	withCFEnvVars(t)
	fake := &fakeCFWranglerRunner{}
	withMintCFWrangler(t, fake)
	headersFile := writeHeadersFile(t, "Authorization=x", 0o600)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy", "--platform=cloudflare", "--dry-run", "--preview=bt-3",
		"--otlp-traces-endpoint=https://collector.example/v1/traces",
		"--otlp-headers-file=" + headersFile,
	})
	require.NoError(t, cmd.Execute())
	assert.Empty(t, fake.deployCalls)
	assert.Empty(t, fake.secretCalls)
}

func TestMintDeployCmd_TracingFlagErrors(t *testing.T) {
	t.Run("gcp requires a headers choice", func(t *testing.T) {
		fake := gcf.NewFakeGCFClient()
		withMintGCFClient(t, fake)
		cmd := newRootCmd()
		cmd.SetArgs([]string{"mint", "deploy", "--platform=gcp", "--project=my-project-id", "--otlp-traces-endpoint=https://collector.example/v1/traces"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--otlp-headers-file or --otlp-no-headers")
		assert.Empty(t, gcf.RecordedCalls(fake))
	})
	t.Run("gcp rejects skip-deploy", func(t *testing.T) {
		fake := gcf.NewFakeGCFClient()
		withMintGCFClient(t, fake)
		cmd := newRootCmd()
		cmd.SetArgs([]string{"mint", "deploy", "--platform=gcp", "--project=my-project-id", "--skip-deploy", "--otlp-no-headers", "--otlp-traces-endpoint=https://collector.example/v1/traces"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--skip-deploy")
		assert.Empty(t, gcf.RecordedCalls(fake))
	})
	t.Run("gcp rejects plain http", func(t *testing.T) {
		cmd := newRootCmd()
		cmd.SetArgs([]string{"mint", "deploy", "--platform=gcp", "--project=my-project-id", "--otlp-no-headers", "--otlp-traces-endpoint=http://collector.example/v1/traces"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "https")
	})
	t.Run("invalid endpoint", func(t *testing.T) {
		cmd := newRootCmd()
		cmd.SetArgs([]string{"mint", "deploy", "--platform=cloudflare", "--otlp-traces-endpoint=collector"})
		require.Error(t, cmd.Execute())
	})
}

func TestMintDeployCmd_GCPDryRunWithTracing(t *testing.T) {
	fake := gcf.NewFakeGCFClient()
	withMintGCFClient(t, fake)
	headersFile := writeHeadersFile(t, "Authorization="+testCollectorHeaderValue, 0o600)

	out := captureStdout(t, func() {
		cmd := newRootCmd()
		cmd.SetArgs([]string{
			"mint", "deploy", "--platform=gcp", "--project=my-project-id", "--region=europe-west4", "--dry-run",
			"--otlp-endpoint=https://collector.example",
			"--otlp-headers-file=" + headersFile,
		})
		require.NoError(t, cmd.Execute())
	})
	assert.Contains(t, out, "https://collector.example into the function source (deployment fullsend-mint/europe-west4)")
	assert.Contains(t, out, "1 collector header(s) in a new Secret Manager secret "+gcf.TraceHeadersSecretPrefix)
	assert.NotContains(t, out, testCollectorHeaderValue)
	assert.Empty(t, gcf.RecordedCalls(fake))
}

// The GCP deploy stores the collector headers in Secret Manager before the
// function source is uploaded. The upload is failed here so the test stops
// before the readiness probe, which needs a real Cloud Run URL.
func TestRunMintDeployGCP_TracingStoresHeadersSecret(t *testing.T) {
	secretData := map[string][]byte{}
	fake := gcf.NewFakeGCFClient(
		gcf.WithFakeSecrets(map[string]bool{}),
		gcf.WithFakeSecretData(secretData),
		gcf.WithFakeErrors(map[string]error{"UploadFunctionSource": fmt.Errorf("upload stopped by test")}),
	)
	withMintGCFClient(t, fake)
	tracing, err := resolveMintTracing("https://collector.example/v1/traces", "", writeHeadersFile(t, "Authorization="+testCollectorHeaderValue, 0o600), false)
	require.NoError(t, err)

	var deployErr error
	out := captureStdout(t, func() {
		deployErr = runMintDeployGCP(context.Background(), "my-project-id", "us-central1", filepath.Join(t.TempDir(), "missing"), false, false, "", "", nil, false, gcf.StatusGitHubAuth{}, tracing)
	})
	require.Error(t, deployErr)
	assert.Contains(t, deployErr.Error(), "upload stopped by test")
	assert.NotContains(t, deployErr.Error(), testCollectorHeaderValue)
	assert.NotContains(t, out, testCollectorHeaderValue)
	require.Len(t, secretData, 1)
	for sid, data := range secretData {
		assert.True(t, strings.HasPrefix(sid, gcf.TraceHeadersSecretPrefix), "unexpected secret ID %q", sid)
		assert.Equal(t, "Authorization="+testCollectorHeaderValue, string(data))
	}
	assert.Contains(t, gcf.RecordedCalls(fake), "SetSecretIAMBinding")
}
