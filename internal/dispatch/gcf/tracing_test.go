package gcf

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testTraceEndpoint = "https://collector.example/v1/traces"
	// testHeaderValue is a fake collector credential that must never
	// appear in generated function source.
	testHeaderValue = "Bearer header-value-never-in-source"
)

func testTraceHeaders() []byte {
	return []byte("authorization=" + testHeaderValue)
}

// zipFiles returns the files in a source zip by name.
func zipFiles(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	out := make(map[string]string, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		content, err := io.ReadAll(rc)
		rc.Close()
		require.NoError(t, err)
		out[f.Name] = string(content)
	}
	return out
}

func activeFunction(hash string) *FunctionInfo {
	return &FunctionInfo{
		Name:  "projects/my-project/locations/us-central1/functions/fullsend-mint",
		State: "ACTIVE",
		URI:   "https://fullsend-mint-abc123.run.app",
		EnvVars: map[string]string{
			"GCP_PROJECT_NUMBER":     "123456789",
			"WIF_POOL_NAME":          "fullsend-pool",
			"WIF_PROVIDER_NAME":      "github-oidc",
			"ALLOWED_ROLES":          "coder",
			"ROLE_APP_IDS":           `{"coder":"12345"}`,
			"FULLSEND_SOURCE_HASH":   hash,
			"ALLOWED_WORKFLOW_FILES": "*",
		},
	}
}

func TestBundleEmbeddedMintSource_StampsTracing(t *testing.T) {
	stamp := TracingStamp{Endpoint: testTraceEndpoint, Deployment: "fullsend-mint/us-central1", HeadersSecret: TraceHeadersSecretPrefix + "0123456789abcdef"}
	data, err := bundleEmbeddedMintSource("1.0.0", "abc", StatusGitHubAuth{}, stamp)
	require.NoError(t, err)

	src, ok := zipFiles(t, data)["trace_consts.go"]
	require.True(t, ok, "trace_consts.go missing from bundle")
	assert.Contains(t, src, "package function")
	assert.Contains(t, src, `otlpTracesEndpoint = "https://collector.example/v1/traces"`)
	assert.Contains(t, src, `traceDeployment    = "fullsend-mint/us-central1"`)
	assert.Contains(t, src, `otlpHeadersSecret  = "fullsend-mint-otlp-headers-0123456789abcdef"`)
}

func TestNewTraceHeadersSecretID(t *testing.T) {
	a, err := NewTraceHeadersSecretID()
	require.NoError(t, err)
	b, err := NewTraceHeadersSecretID()
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "each deploy needs its own secret ID")
	assert.Regexp(t, `^fullsend-mint-otlp-headers-[0-9a-f]{16}$`, a)
}

// stampedHeadersSecret returns the headers secret ID stamped into the
// uploaded function source.
func stampedHeadersSecret(t *testing.T, fake *fakeGCFClient) string {
	t.Helper()
	src := zipFiles(t, fake.lastUploadedSource)["trace_consts.go"]
	m := regexp.MustCompile(`otlpHeadersSecret\s+= "([^"]*)"`).FindStringSubmatch(src)
	require.NotNil(t, m, "otlpHeadersSecret not stamped")
	return m[1]
}

func TestBundleEmbeddedMintSource_TracingDisabledByDefault(t *testing.T) {
	data, err := bundleEmbeddedMintSource("", "", StatusGitHubAuth{}, TracingStamp{})
	require.NoError(t, err)
	src := zipFiles(t, data)["trace_consts.go"]
	assert.Contains(t, src, `otlpTracesEndpoint = ""`)
}

func TestBundleFunctionSource_StampsTracingOverDiskCopy(t *testing.T) {
	srcDir := fakeFunctionSourceDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "trace_consts.go"), []byte("package function\n\nvar otlpTracesEndpoint = \"https://stale.example/v1/traces\"\n"), 0o644))

	data, err := bundleFunctionSource(srcDir, "", "", StatusGitHubAuth{}, TracingStamp{Endpoint: testTraceEndpoint, Deployment: "d"})
	require.NoError(t, err)

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	count := 0
	for _, f := range zr.File {
		if f.Name == "trace_consts.go" {
			count++
		}
	}
	assert.Equal(t, 1, count, "trace_consts.go must appear exactly once")
	src := zipFiles(t, data)["trace_consts.go"]
	assert.Contains(t, src, testTraceEndpoint)
	assert.NotContains(t, src, "stale.example")
}

func TestBundle_TracingChangesSourceHash(t *testing.T) {
	plain, err := bundleEmbeddedMintSource("", "", StatusGitHubAuth{}, TracingStamp{})
	require.NoError(t, err)
	traced, err := bundleEmbeddedMintSource("", "", StatusGitHubAuth{}, TracingStamp{Endpoint: testTraceEndpoint, Deployment: "d"})
	require.NoError(t, err)
	assert.NotEqual(t, sha256Hex(plain), sha256Hex(traced))
}

func TestTraceDeploymentName(t *testing.T) {
	assert.Equal(t, "fullsend-mint/europe-west4", TraceDeploymentName("europe-west4"))
	assert.Equal(t, "fullsend-mint/us-central1", TraceDeploymentName(""))
}

func TestProvisioner_Provision_TracingNewMint(t *testing.T) {
	fake := newFakeGCFClient()
	fake.secrets = map[string]bool{}
	fake.secretData = map[string][]byte{}
	fake.functionInfoAfterCreate = &FunctionInfo{URI: "https://mint.run.app"}
	headers := testTraceHeaders()

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		Region:            "europe-west4",
		GitHubOrgs:        []string{PlaceholderOrg},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
		Tracing:           TracingConfig{Endpoint: testTraceEndpoint, Headers: headers},
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)

	sid := stampedHeadersSecret(t, fake)
	assert.True(t, strings.HasPrefix(sid, TraceHeadersSecretPrefix), "unexpected secret ID %q", sid)
	assert.True(t, fake.secrets[sid], "headers secret not created")
	assert.Equal(t, string(testTraceHeaders()), string(fake.secretData[sid]))
	assert.Contains(t, fake.calls, "CreateFunction")

	files := zipFiles(t, fake.lastUploadedSource)
	assert.Contains(t, files["trace_consts.go"], testTraceEndpoint)
	assert.Contains(t, files["trace_consts.go"], `"fullsend-mint/europe-west4"`)
	for name, content := range files {
		assert.NotContains(t, content, "header-value-never-in-source", "collector header value leaked into %s", name)
	}
	assert.Equal(t, make([]byte, len(headers)), headers, "headers not zeroed after Provision")
}

func TestProvisioner_Provision_TracingNoHeaders(t *testing.T) {
	fake := newFakeGCFClient()
	fake.secrets = map[string]bool{}
	fake.functionInfoAfterCreate = &FunctionInfo{URI: "https://mint.run.app"}

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{PlaceholderOrg},
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
		Tracing:           TracingConfig{Endpoint: testTraceEndpoint},
	}, fake)
	fake.secrets["fullsend-coder-app-pem"] = true

	_, err := p.Provision(context.Background())
	require.NoError(t, err)

	for sid := range fake.secrets {
		assert.False(t, strings.HasPrefix(sid, TraceHeadersSecretPrefix), "no headers secret expected, got %s", sid)
	}
	src := zipFiles(t, fake.lastUploadedSource)["trace_consts.go"]
	assert.Contains(t, src, testTraceEndpoint)
	assert.Contains(t, src, `otlpHeadersSecret  = ""`)
}

// Rotating headers stores them in a new secret, leaves the previous
// deploy's secret untouched, and redeploys with the new secret ID stamped,
// so a still-running old revision keeps reading its own headers.
func TestProvisioner_Provision_TracingHeaderRotationRedeploys(t *testing.T) {
	srcDir := fakeFunctionSourceDir(t)
	oldSecret := TraceHeadersSecretPrefix + "0000000000000000"
	stamp := TracingStamp{Endpoint: testTraceEndpoint, Deployment: TraceDeploymentName(""), HeadersSecret: oldSecret}
	sourceZip, err := bundleFunctionSource(srcDir, "", "", StatusGitHubAuth{}, stamp)
	require.NoError(t, err)

	fake := newFakeGCFClient()
	fake.functionInfo = activeFunction(sha256Hex(sourceZip))
	fake.secrets = map[string]bool{oldSecret: true, "fullsend-coder-app-pem": true}
	fake.secretData = map[string][]byte{oldSecret: []byte("authorization=old")}

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{PlaceholderOrg},
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: srcDir,
		Tracing:           TracingConfig{Endpoint: testTraceEndpoint, Headers: testTraceHeaders()},
	}, fake)

	_, err = p.Provision(context.Background())
	require.NoError(t, err)

	assert.Contains(t, fake.calls, "UpdateFunction")
	newSecret := stampedHeadersSecret(t, fake)
	assert.NotEqual(t, oldSecret, newSecret)
	assert.Equal(t, string(testTraceHeaders()), string(fake.secretData[newSecret]))
	assert.Equal(t, "authorization=old", string(fake.secretData[oldSecret]), "previous deploy's headers must not be modified")
}

// A deploy that fails after the headers were stored must not make the
// retry look unchanged: the retry stamps a new secret ID, so it redeploys.
func TestProvisioner_Provision_TracingRetryAfterFailedDeployRedeploys(t *testing.T) {
	srcDir := fakeFunctionSourceDir(t)
	oldSecret := TraceHeadersSecretPrefix + "0000000000000000"
	stamp := TracingStamp{Endpoint: testTraceEndpoint, Deployment: TraceDeploymentName(""), HeadersSecret: oldSecret}
	sourceZip, err := bundleFunctionSource(srcDir, "", "", StatusGitHubAuth{}, stamp)
	require.NoError(t, err)

	fake := newFakeGCFClient()
	fake.functionInfo = activeFunction(sha256Hex(sourceZip))
	fake.secrets = map[string]bool{oldSecret: true, "fullsend-coder-app-pem": true}
	fake.secretData = map[string][]byte{oldSecret: []byte("authorization=old")}
	fake.errs["UpdateFunction"] = errors.New("boom")

	cfg := func() Config {
		return Config{
			ProjectID:         "my-project",
			GitHubOrgs:        []string{PlaceholderOrg},
			AgentAppIDs:       singleRoleAppIDs(),
			FunctionSourceDir: srcDir,
			Tracing:           TracingConfig{Endpoint: testTraceEndpoint, Headers: testTraceHeaders()},
		}
	}

	_, err = newTestProvisioner(cfg(), fake).Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, fake.calls, "AddSecretVersion", "headers stored before the failed deploy")

	delete(fake.errs, "UpdateFunction")
	fake.calls = nil
	_, err = newTestProvisioner(cfg(), fake).Provision(context.Background())
	require.NoError(t, err)
	assert.Contains(t, fake.calls, "UpdateFunction", "retry must redeploy")
	assert.Equal(t, "authorization=old", string(fake.secretData[oldSecret]))
}

// A deploy to another region with other collector headers must not change
// the headers an earlier deploy's revision reads at cold start.
func TestProvisioner_Provision_TracingMultiRegionSecretsIsolated(t *testing.T) {
	fake := newFakeGCFClient()
	fake.secrets = map[string]bool{"fullsend-coder-app-pem": true}
	fake.secretData = map[string][]byte{}
	fake.functionInfoAfterCreate = &FunctionInfo{URI: "https://mint.run.app"}
	srcDir := fakeFunctionSourceDir(t)

	deploy := func(region, endpoint, headers string) string {
		t.Helper()
		p := newTestProvisioner(Config{
			ProjectID:         "my-project",
			Region:            region,
			GitHubOrgs:        []string{PlaceholderOrg},
			AgentAppIDs:       singleRoleAppIDs(),
			FunctionSourceDir: srcDir,
			Tracing:           TracingConfig{Endpoint: endpoint, Headers: []byte(headers)},
		}, fake)
		_, err := p.Provision(context.Background())
		require.NoError(t, err)
		return stampedHeadersSecret(t, fake)
	}

	secretA := deploy("us-central1", "https://collector-a.example/v1/traces", "authorization=a")
	secretB := deploy("europe-west4", "https://collector-b.example/v1/traces", "authorization=b")

	assert.NotEqual(t, secretA, secretB)
	assert.Equal(t, "authorization=a", string(fake.secretData[secretA]))
	assert.Equal(t, "authorization=b", string(fake.secretData[secretB]))
}

// With no function source directory, an existing active function is only
// left alone when tracing is not configured: a tracing deploy compares and
// deploys the embedded source stamped with the tracing settings.
func TestProvisioner_Provision_TracingEmptySourceDirRedeploys(t *testing.T) {
	newFake := func() *fakeGCFClient {
		fake := newFakeGCFClient()
		fake.functionInfo = activeFunction("stale-hash")
		fake.secrets = map[string]bool{"fullsend-coder-app-pem": true}
		return fake
	}
	cfg := Config{
		ProjectID:   "my-project",
		GitHubOrgs:  []string{PlaceholderOrg},
		AgentAppIDs: singleRoleAppIDs(),
	}

	fake := newFake()
	_, err := newTestProvisioner(cfg, fake).Provision(context.Background())
	require.NoError(t, err)
	assert.NotContains(t, fake.calls, "UpdateFunction", "untraced deploy keeps the no-source-dir shortcut")

	cfg.Tracing = TracingConfig{Endpoint: testTraceEndpoint}
	fake = newFake()
	_, err = newTestProvisioner(cfg, fake).Provision(context.Background())
	require.NoError(t, err)
	assert.Contains(t, fake.calls, "UpdateFunction")
	assert.Contains(t, zipFiles(t, fake.lastUploadedSource)["trace_consts.go"], testTraceEndpoint)
}

// Enabling tracing on an existing untraced mint changes the generated
// source and redeploys it.
func TestProvisioner_Provision_EnablingTracingRedeploys(t *testing.T) {
	srcDir := fakeFunctionSourceDir(t)
	sourceZip, err := bundleFunctionSource(srcDir, "", "", StatusGitHubAuth{}, TracingStamp{})
	require.NoError(t, err)

	fake := newFakeGCFClient()
	fake.functionInfo = activeFunction(sha256Hex(sourceZip))
	fake.secrets = map[string]bool{"fullsend-coder-app-pem": true}

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{PlaceholderOrg},
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: srcDir,
		Tracing:           TracingConfig{Endpoint: testTraceEndpoint},
	}, fake)

	_, err = p.Provision(context.Background())
	require.NoError(t, err)
	assert.Contains(t, fake.calls, "UpdateFunction")
	assert.Contains(t, zipFiles(t, fake.lastUploadedSource)["trace_consts.go"], testTraceEndpoint)
}

func TestProvisioner_Provision_TracingErrors(t *testing.T) {
	base := func() Config {
		return Config{
			ProjectID:         "my-project",
			GitHubOrgs:        []string{PlaceholderOrg},
			AgentAppIDs:       singleRoleAppIDs(),
			FunctionSourceDir: fakeFunctionSourceDir(t),
		}
	}
	tests := []struct {
		name    string
		mutate  func(*Config)
		fakeErr map[string]error
		wantErr string
	}{
		{"plain http endpoint", func(c *Config) { c.Tracing.Endpoint = "http://collector.example/v1/traces" }, nil, "must use https"},
		{"credentials in URL", func(c *Config) { c.Tracing.Endpoint = "https://u:p@collector.example/v1/traces" }, nil, "credentials"},
		{"headers without endpoint", func(c *Config) { c.Tracing.Headers = testTraceHeaders() }, nil, "require an OTLP traces endpoint"},
		{"malformed headers", func(c *Config) {
			c.Tracing = TracingConfig{Endpoint: testTraceEndpoint, Headers: []byte("not-a-pair")}
		}, nil, "invalid collector headers"},
		{"skip deploy", func(c *Config) {
			c.Tracing.Endpoint = testTraceEndpoint
			c.DeployMode = DeploySkip
		}, nil, "--skip-mint-deploy"},
		{"existing mint URL", func(c *Config) {
			c.Tracing.Endpoint = testTraceEndpoint
			c.MintURL = "https://mint.run.app"
		}, nil, "existing MintURL"},
		{"secret create fails", func(c *Config) {
			c.Tracing = TracingConfig{Endpoint: testTraceEndpoint, Headers: testTraceHeaders()}
		}, map[string]error{"CreateSecret": errors.New("boom")}, "creating secret fullsend-mint-otlp-headers-"},
		{"add version fails", func(c *Config) {
			c.Tracing = TracingConfig{Endpoint: testTraceEndpoint, Headers: testTraceHeaders()}
		}, map[string]error{"AddSecretVersion": errors.New("boom")}, "adding secret version for fullsend-mint-otlp-headers-"},
		{"IAM binding fails", func(c *Config) {
			c.Tracing = TracingConfig{Endpoint: testTraceEndpoint, Headers: testTraceHeaders()}
		}, map[string]error{"SetSecretIAMBinding": errors.New("boom")}, "granting secret access for fullsend-mint-otlp-headers-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeGCFClient()
			fake.secrets = map[string]bool{}
			for k, v := range tt.fakeErr {
				fake.errs[k] = v
			}
			cfg := base()
			tt.mutate(&cfg)
			_, err := newTestProvisioner(cfg, fake).Provision(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.NotContains(t, err.Error(), "header-value-never-in-source")
			assert.NotContains(t, fake.calls, "CreateFunction")
		})
	}
}
