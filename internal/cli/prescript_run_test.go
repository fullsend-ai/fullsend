package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/inference/vertexauth"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// writePreScript creates an executable script for runPreScript tests.
func writePreScript(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("pre-script tests require a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "pre-test.sh")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/bash\nset -euo pipefail\n"+body), 0o755))
	return path
}

func TestRunPreScript_NoOutput_Proceeds(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t, "true\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.False(t, res.Skipped)
}

func TestRunPreScript_SkipRequested(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t,
		`echo "skipped=true" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"+
			`echo "reason=open PR exists" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Equal(t, "open PR exists", res.Reason)
}

func TestRunPreScript_RunnerEnvVisible(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{
		PreScript: writePreScript(t,
			`[ "${MY_RUNNER_VAR}" = "on" ] || exit 7`+"\n"+
				`echo "skipped=true" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"),
		RunnerEnv: map[string]string{"MY_RUNNER_VAR": "on"},
	}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
}

func TestRunPreScript_ScriptFailureIsHardError(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t, "exit 3\n")}

	_, err := runPreScript(h, t.TempDir(), "", printer)
	require.ErrorContains(t, err, "running pre-script")
	// No captured output: the error stays the opaque exec.ExitError text.
	require.ErrorContains(t, err, "exit status 3")
}

func TestRunPreScript_MalformedOutputIsHardError(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t,
		`echo "skipped true" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n")}

	_, err := runPreScript(h, t.TempDir(), "", printer)
	require.ErrorContains(t, err, "parsing pre-script output")
}

// The headline claim of issue #4718: a skip exits before the sandbox is
// ever created. usePreScriptStub makes sandbox creation fail loudly, so a
// nil error here proves runAgent returned first. If the pre-script block
// is ever moved below sandbox creation, this fails with "creating
// sandbox" — the error its paired no-skip test asserts on.
func TestRunAgent_PreScriptSkip_ReturnsBeforeSandboxCreation(t *testing.T) {
	usePreScriptStub(t)
	dir := newSkipHarnessDir(t, `echo "skipped=true" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"+
		`echo "reason=open PR exists" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n")

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
	require.NoError(t, err)
}

// A Vertex run must reject the optional GCP mount before the pre-script can
// mutate repository state. The marker proves the script never ran.
func TestRunAgent_VertexMissingGCPCredentialsFailsBeforePreScript(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	marker := filepath.Join(t.TempDir(), "pre-script-ran")
	dir := newSkipHarnessDir(t, "touch "+marker+"\n")
	harnessPath := filepath.Join(dir, "harness", "code.yaml")
	f, err := os.OpenFile(harnessPath, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("host_files:\n  - src: ${GOOGLE_APPLICATION_CREDENTIALS}\n    dest: /tmp/.gcp-credentials.json\n    optional: true\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err = runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
	require.ErrorContains(t, err, "GOOGLE_APPLICATION_CREDENTIALS")
	assert.NoFileExists(t, marker)
}

// An OpenAI parent that declares the vertex-ai provider (enabling a
// Vertex-capable sub-agent dispatch, e.g. `Agent` with model: "sonnet") must
// reject the optional GCP mount before the pre-script runs, the same as a
// Vertex parent would. Before the fix this fell through to
// validateRequiredGCPHostFile, which skips optional mounts, so the missing
// credential surfaced only mid-run from the sub-agent's sandbox (#7980).
func TestRunAgent_OpenAIParentVertexProviderMissingGCPCredentialsFailsBeforePreScript(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv("FULLSEND_RUNTIME", "codex")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	marker := filepath.Join(t.TempDir(), "pre-script-ran")
	dir := newSkipHarnessDir(t, "touch "+marker+"\n")
	harnessPath := filepath.Join(dir, "harness", "code.yaml")
	f, err := os.OpenFile(harnessPath, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("providers:\n  - vertex-ai\n" +
		"host_files:\n  - src: ${GOOGLE_APPLICATION_CREDENTIALS}\n    dest: /tmp/.gcp-credentials.json\n    optional: true\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err = runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
	require.ErrorContains(t, err, "GOOGLE_APPLICATION_CREDENTIALS")
	assert.NoFileExists(t, marker)
}

func TestRunAgent_OpenAISkipsVertexCredentialSetup(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv("FULLSEND_RUNTIME", "codex")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("FULLSEND_GCP_PROJECT_ID", "")
	t.Setenv("FULLSEND_GCP_WIF_PROVIDER", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")
	marker := filepath.Join(t.TempDir(), "pre-script-ran")
	dir := newSkipHarnessDir(t, "touch "+marker+"\n")

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
	require.ErrorContains(t, err, "creating sandbox")
	assert.FileExists(t, marker)
}

func TestRunAgent_VertexMissingGCPInputsFailsBeforePreScript(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv("FULLSEND_RUNTIME", "claude")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("FULLSEND_GCP_PROJECT_ID", "")
	t.Setenv("FULLSEND_GCP_WIF_PROVIDER", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")
	marker := filepath.Join(t.TempDir(), "pre-script-ran")
	dir := newSkipHarnessDir(t, "touch "+marker+"\n")

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
	require.ErrorContains(t, err, "FULLSEND_GCP_PROJECT_ID")
	assert.NoFileExists(t, marker)
}

const testGCPWIFProvider = "projects/123456789/locations/global/workloadIdentityPools/fullsend/providers/github"

// wifStub replaces prepareGitHubWIF for one test and records its use.
type wifStub struct {
	calls   int
	cleaned bool
	cfg     vertexauth.Config
	env     map[string]string
}

// stubPrepareGitHubWIF installs a prepareGitHubWIF that returns real
// temporary credential files, or prepErr when it is non-nil.
func stubPrepareGitHubWIF(t *testing.T, prepErr error) *wifStub {
	t.Helper()
	dir := t.TempDir()
	creds := filepath.Join(dir, "sandbox-gcp-credentials.json")
	require.NoError(t, os.WriteFile(creds, []byte(`{"type":"external_account","credential_source":{"file":"/sandbox/workspace/.gcp-oidc-token"}}`), 0o600))
	token := filepath.Join(dir, "gcp-oidc-token.json")
	require.NoError(t, os.WriteFile(token, []byte(`{"value":"stub-oidc-token"}`), 0o600))
	s := &wifStub{env: map[string]string{
		"GOOGLE_APPLICATION_CREDENTIALS": creds,
		"GCP_OIDC_TOKEN_FILE":            token,
		"FULLSEND_GCP_OIDC_URL":          "https://token.actions.githubusercontent.com/token?audience=https%3A%2F%2Fiam.googleapis.com%2F" + testGCPWIFProvider,
	}}
	orig := prepareGitHubWIF
	prepareGitHubWIF = func(_ context.Context, cfg vertexauth.Config) (map[string]string, func(), error) {
		s.calls++
		s.cfg = cfg
		if prepErr != nil {
			return nil, nil, prepErr
		}
		if cfg.OnSubjectToken != nil {
			cfg.OnSubjectToken("stub-subject-jwt-value")
		}
		return s.env, func() { s.cleaned = true }, nil
	}
	t.Cleanup(func() { prepareGitHubWIF = orig })
	return s
}

// setActionsGCPEnv simulates a GitHub Actions job with the given GCP inputs.
// usePreScriptStub resets GITHUB_ACTIONS, so call this after it.
func setActionsGCPEnv(t *testing.T, runtimeName, projectID, wifProvider string) {
	t.Helper()
	t.Setenv("FULLSEND_RUNTIME", runtimeName)
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("FULLSEND_GCP_PROJECT_ID", projectID)
	t.Setenv("FULLSEND_GCP_WIF_PROVIDER", wifProvider)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
}

func runSkipHarnessAgent(t *testing.T, dir string, printer *ui.Printer) error {
	t.Helper()
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	return runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, printer, false, runOverrideFlags{})
}

func TestRunAgent_VertexPreservesExistingGCPCredentials(t *testing.T) {
	for _, tc := range []struct {
		name         string
		projectID    string
		provider     string
		wantPrepared bool
	}{
		{name: "legacy workflow without GCP inputs"},
		{name: "GCP inputs override an existing credential file", projectID: "test-project", provider: testGCPWIFProvider, wantPrepared: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usePreScriptStub(t)
			setActionsGCPEnv(t, "claude", tc.projectID, tc.provider)
			stub := stubPrepareGitHubWIF(t, nil)
			tokenFile := filepath.Join(t.TempDir(), "oidc-token.json")
			credentials := filepath.Join(t.TempDir(), "credentials.json")
			require.NoError(t, os.WriteFile(credentials, []byte(`{"type":"external_account","service_account_impersonation_url":"https://example.invalid/impersonate","credential_source":{"file":"`+tokenFile+`"}}`), 0o600))
			t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credentials)
			record := filepath.Join(t.TempDir(), "pre-script-env")
			dir := newSkipHarnessDir(t, `printf '%s' "${GOOGLE_APPLICATION_CREDENTIALS:-}" > `+record+"\n")
			var out strings.Builder

			err := runSkipHarnessAgent(t, dir, ui.New(&out))
			require.ErrorContains(t, err, "creating sandbox")
			seen, readErr := os.ReadFile(record)
			require.NoError(t, readErr, "pre-script must run")
			if tc.wantPrepared {
				assert.Equal(t, 1, stub.calls)
				assert.Equal(t, stub.env["GOOGLE_APPLICATION_CREDENTIALS"], string(seen))
				assert.True(t, stub.cleaned)
				assert.Contains(t, out.String(), "Vertex credentials: prepared GitHub WIF")
			} else {
				assert.Zero(t, stub.calls)
				assert.Equal(t, credentials, string(seen))
				assert.Contains(t, out.String(), "Vertex credentials: existing GOOGLE_APPLICATION_CREDENTIALS file")
			}
			assert.Equal(t, credentials, os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"))
		})
	}
}

// An existing credential file is only kept when the sandbox can use it
// without the runner's OIDC request token.
func TestRunAgent_VertexRejectsUnusableExistingCredentials(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		wantErr string
	}{
		{name: "URL source", content: `{"type":"external_account","credential_source":{"url":"https://token.actions.githubusercontent.com/token","headers":{"Authorization":"bearer request-token"}}}`, wantErr: "credential_source.url"},
		{name: "headers alongside a file", content: `{"type":"external_account","credential_source":{"file":"/tmp/token","headers":{"Authorization":"bearer request-token"}}}`, wantErr: "credential_source.url or headers"},
		{name: "no source", content: `{"type":"external_account"}`, wantErr: "credential_source.file"},
		{name: "no type", content: `{"client_email":"x"}`, wantErr: "no credential type"},
		{name: "invalid JSON", content: `not json`, wantErr: "not valid credential JSON"},
		{name: "empty file", content: ``, wantErr: "non-empty credential file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usePreScriptStub(t)
			setActionsGCPEnv(t, "claude", "", "")
			credentials := filepath.Join(t.TempDir(), "credentials.json")
			require.NoError(t, os.WriteFile(credentials, []byte(tc.content), 0o600))
			t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credentials)
			marker := filepath.Join(t.TempDir(), "pre-script-ran")
			dir := newSkipHarnessDir(t, "touch "+marker+"\n")

			err := runSkipHarnessAgent(t, dir, ui.New(io.Discard))
			require.ErrorContains(t, err, tc.wantErr)
			assert.NotContains(t, err.Error(), "request-token")
			assert.NoFileExists(t, marker)
		})
	}
}

func TestValidateExistingGCPCredentialFile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		wantErr string
	}{
		{name: "service account key", content: `{"type":"service_account"}`},
		{name: "file source", content: `{"type":"external_account","credential_source":{"file":"/tmp/token"}}`},
		{name: "null headers", content: `{"type":"external_account","credential_source":{"file":"/tmp/token","headers":null}}`},
		{name: "empty file name", content: `{"type":"external_account","credential_source":{"file":""}}`, wantErr: "credential_source.file"},
		{name: "impersonation over file source", content: `{"type":"impersonated_service_account","source_credentials":{"type":"external_account","credential_source":{"file":"/tmp/token"}}}`},
		{name: "impersonation over url source", content: `{"type":"impersonated_service_account","source_credentials":{"type":"external_account","credential_source":{"url":"https://example.invalid","headers":{"Authorization":"Bearer x"}}}}`, wantErr: "credential_source.url or headers"},
		{name: "url source beside source_credentials", content: `{"type":"external_account","credential_source":{"url":"https://example.invalid"},"source_credentials":{"type":"service_account"}}`, wantErr: "credential_source.url or headers"},
		{name: "oversized file", content: `{"type":"service_account","pad":"` + strings.Repeat("x", maxGCPCredentialFileBytes) + `"}`, wantErr: "exceeds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credentials.json")
			require.NoError(t, os.WriteFile(path, []byte(tc.content), 0o600))
			err := validateExistingGCPCredentialFile(path)
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantErr)
			}
		})
	}

	t.Run("missing file", func(t *testing.T) {
		require.ErrorContains(t, validateExistingGCPCredentialFile(filepath.Join(t.TempDir(), "missing.json")), "non-empty credential file")
	})
	t.Run("directory", func(t *testing.T) {
		require.ErrorContains(t, validateExistingGCPCredentialFile(t.TempDir()), "non-empty credential file")
	})
}

func TestRunAgent_VertexPartialGCPInputsFails(t *testing.T) {
	for _, tc := range []struct{ name, projectID, provider string }{
		{name: "project only", projectID: "test-project"},
		{name: "provider only", provider: testGCPWIFProvider},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usePreScriptStub(t)
			setActionsGCPEnv(t, "claude", tc.projectID, tc.provider)
			stub := stubPrepareGitHubWIF(t, nil)
			marker := filepath.Join(t.TempDir(), "pre-script-ran")
			dir := newSkipHarnessDir(t, "touch "+marker+"\n")

			err := runSkipHarnessAgent(t, dir, ui.New(io.Discard))
			require.ErrorContains(t, err, "only one is set")
			assert.Zero(t, stub.calls)
			assert.NoFileExists(t, marker)
		})
	}
}

// A required ${GOOGLE_APPLICATION_CREDENTIALS} host file must resolve to the
// prepared credentials: preparation runs before env validation.
func TestRunAgent_VertexPreparedWIFSatisfiesRequiredHostFile(t *testing.T) {
	usePreScriptStub(t)
	setActionsGCPEnv(t, "claude", "test-project", testGCPWIFProvider)
	// Unset, not empty: env validation accepts a set-but-empty variable.
	require.NoError(t, os.Unsetenv("GOOGLE_APPLICATION_CREDENTIALS"))
	stub := stubPrepareGitHubWIF(t, nil)
	marker := filepath.Join(t.TempDir(), "pre-script-ran")
	dir := newSkipHarnessDir(t, "touch "+marker+"\n")
	f, err := os.OpenFile(filepath.Join(dir, "harness", "code.yaml"), os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("host_files:\n  - src: ${GOOGLE_APPLICATION_CREDENTIALS}\n    dest: /tmp/.gcp-credentials.json\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	err = runSkipHarnessAgent(t, dir, ui.New(io.Discard))
	require.ErrorContains(t, err, "creating sandbox")
	assert.NotContains(t, err.Error(), "validating env")
	assert.Equal(t, 1, stub.calls)
	assert.FileExists(t, marker)
}

// A Vertex parent fails closed when the WIF exchange fails.
func TestRunAgent_VertexPrepareErrorFailsBeforePreScript(t *testing.T) {
	usePreScriptStub(t)
	setActionsGCPEnv(t, "claude", "test-project", testGCPWIFProvider)
	stub := stubPrepareGitHubWIF(t, fmt.Errorf("validating Google STS exchange failed"))
	marker := filepath.Join(t.TempDir(), "pre-script-ran")
	dir := newSkipHarnessDir(t, "touch "+marker+"\n")

	err := runSkipHarnessAgent(t, dir, ui.New(io.Discard))
	require.ErrorContains(t, err, "validating Google STS exchange failed")
	assert.Equal(t, 1, stub.calls)
	assert.NoFileExists(t, marker)
}

// The prepared credentials reach the pre-script and the rest of the run,
// the OIDC URL stays runner-only, and everything is undone on return.
func TestRunAgent_VertexPreparedWIFReachesRun(t *testing.T) {
	usePreScriptStub(t)
	setActionsGCPEnv(t, "claude", "test-project", testGCPWIFProvider)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("GCP_OIDC_TOKEN_FILE", "original-token-file")
	stub := stubPrepareGitHubWIF(t, nil)

	// Wrap the openshell stub so a runner-side command records the process
	// environment the run exported.
	realStub, err := filepath.Abs(filepath.Join("testdata", "prescript-stub", "openshell"))
	require.NoError(t, err)
	wrapDir := t.TempDir()
	runnerRecord := filepath.Join(t.TempDir(), "runner-env")
	require.NoError(t, os.WriteFile(filepath.Join(wrapDir, "openshell"), []byte("#!/bin/sh\n"+
		`printf '%s\n' "${FULLSEND_GCP_OIDC_URL:-}" >> `+runnerRecord+"\n"+
		`exec `+realStub+` "$@"`+"\n"), 0o755))
	t.Setenv("PATH", wrapDir+string(filepath.ListSeparator)+os.Getenv("PATH"))

	record := filepath.Join(t.TempDir(), "pre-script-env")
	dir := newSkipHarnessDir(t, `printf '%s\n%s\n%s\n' "${GOOGLE_APPLICATION_CREDENTIALS:-}" "${GCP_OIDC_TOKEN_FILE:-}" "${FULLSEND_GCP_OIDC_URL:-unset}" > `+record+"\n")

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = runSkipHarnessAgent(t, dir, ui.New(io.Discard))
	})
	require.ErrorContains(t, runErr, "creating sandbox")

	assert.Equal(t, 1, stub.calls)
	assert.Equal(t, "test-project", stub.cfg.ProjectID)
	assert.Equal(t, testGCPWIFProvider, stub.cfg.WorkloadIdentityProvider)
	assert.Contains(t, stderr, "::add-mask::stub-subject-jwt-value")

	seen, err := os.ReadFile(record)
	require.NoError(t, err, "pre-script must run")
	assert.Equal(t, stub.env["GOOGLE_APPLICATION_CREDENTIALS"]+"\n"+stub.env["GCP_OIDC_TOKEN_FILE"]+"\nunset\n", string(seen),
		"the pre-script gets the credential files but not the runner-only OIDC URL")
	runnerSeen, err := os.ReadFile(runnerRecord)
	require.NoError(t, err)
	assert.Contains(t, strings.Split(string(runnerSeen), "\n"), stub.env["FULLSEND_GCP_OIDC_URL"])

	assert.True(t, stub.cleaned)
	assert.Equal(t, "", os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"))
	assert.Equal(t, "original-token-file", os.Getenv("GCP_OIDC_TOKEN_FILE"))
	_, urlSet := os.LookupEnv("FULLSEND_GCP_OIDC_URL")
	assert.False(t, urlSet)
}

// An OpenAI parent can dispatch Vertex sub-agents, so it gets Vertex
// credentials when the GCP inputs are set; that setup never fails the run.
func TestRunAgent_OpenAIParentVertexSubAgentCredentials(t *testing.T) {
	for _, tc := range []struct {
		name         string
		projectID    string
		provider     string
		prepErr      error
		wantCalls    int
		wantPrepared bool
		wantWarn     string
	}{
		{name: "GCP inputs set", projectID: "test-project", provider: testGCPWIFProvider, wantCalls: 1, wantPrepared: true},
		{name: "no GCP inputs", wantCalls: 0},
		{name: "prepare error", projectID: "test-project", provider: testGCPWIFProvider, prepErr: fmt.Errorf("validating Google STS exchange failed"), wantCalls: 1, wantWarn: "Vertex credentials for sub-agents unavailable: validating Google STS exchange failed"},
		{name: "partial inputs", projectID: "test-project", wantCalls: 0, wantWarn: "only one is set"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usePreScriptStub(t)
			setActionsGCPEnv(t, "codex", tc.projectID, tc.provider)
			stub := stubPrepareGitHubWIF(t, tc.prepErr)
			record := filepath.Join(t.TempDir(), "pre-script-env")
			dir := newSkipHarnessDir(t, `printf '%s' "${GOOGLE_APPLICATION_CREDENTIALS:-}" > `+record+"\n")
			var out strings.Builder

			err := runSkipHarnessAgent(t, dir, ui.New(&out))
			require.ErrorContains(t, err, "creating sandbox")
			assert.Equal(t, tc.wantCalls, stub.calls)
			assert.Equal(t, tc.wantPrepared, stub.cleaned)
			seen, readErr := os.ReadFile(record)
			require.NoError(t, readErr, "pre-script must run")
			if tc.wantPrepared {
				assert.Equal(t, stub.env["GOOGLE_APPLICATION_CREDENTIALS"], string(seen))
				assert.Contains(t, out.String(), "prepared GitHub WIF (for Vertex sub-agents)")
			} else {
				assert.Empty(t, string(seen))
			}
			if tc.wantWarn != "" {
				assert.Contains(t, out.String(), tc.wantWarn)
			}
		})
	}
}

// A parent that does not use Vertex never mounts a credential file that
// fails validation: a URL source would copy the runner's request token into
// the sandbox.
func TestRunAgent_NonVertexParentDropsUnusableCredentialFile(t *testing.T) {
	for _, tc := range []struct {
		name     string
		content  string
		wantKept bool
	}{
		{name: "url source", content: `{"type":"external_account","credential_source":{"url":"https://example.invalid","headers":{"Authorization":"Bearer x"}}}`},
		{name: "file source", content: `{"type":"external_account","credential_source":{"file":"/tmp/token"}}`, wantKept: true},
	} {
		for _, runtimeName := range []string{"codex", "dummy"} {
			t.Run(tc.name+"/"+runtimeName, func(t *testing.T) {
				usePreScriptStub(t)
				setActionsGCPEnv(t, runtimeName, "", "")
				creds := filepath.Join(t.TempDir(), "credentials.json")
				require.NoError(t, os.WriteFile(creds, []byte(tc.content), 0o600))
				t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", creds)
				record := filepath.Join(t.TempDir(), "pre-script-env")
				dir := newSkipHarnessDir(t, `printf '%s' "${GOOGLE_APPLICATION_CREDENTIALS:-}" > `+record+"\n")
				var out strings.Builder

				_ = runSkipHarnessAgent(t, dir, ui.New(&out))
				seen, err := os.ReadFile(record)
				require.NoError(t, err, "pre-script must run")
				if tc.wantKept {
					assert.Equal(t, creds, string(seen))
				} else {
					assert.Empty(t, string(seen))
					assert.Contains(t, out.String(), "Ignoring GOOGLE_APPLICATION_CREDENTIALS")
				}
				assert.Equal(t, creds, os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"), "the run restores the environment")
			})
		}
	}
}

// A required GCP mount on a non-Vertex run fails before the pre-script once
// its credential file has been dropped.
func TestRunAgent_NonVertexRequiredGCPHostFileFailsBeforePreScript(t *testing.T) {
	usePreScriptStub(t)
	setActionsGCPEnv(t, "codex", "", "")
	creds := filepath.Join(t.TempDir(), "credentials.json")
	require.NoError(t, os.WriteFile(creds, []byte(`{"type":"external_account","credential_source":{"url":"https://example.invalid"}}`), 0o600))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", creds)
	marker := filepath.Join(t.TempDir(), "pre-script-ran")
	dir := newSkipHarnessDir(t, "touch "+marker+"\n")
	f, err := os.OpenFile(filepath.Join(dir, "harness", "code.yaml"), os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("host_files:\n  - src: ${GOOGLE_APPLICATION_CREDENTIALS}\n    dest: /tmp/.gcp-credentials.json\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	err = runSkipHarnessAgent(t, dir, ui.New(io.Discard))
	require.ErrorContains(t, err, "GOOGLE_APPLICATION_CREDENTIALS is empty")
	assert.NoFileExists(t, marker)
}

// dummy runtimes do no inference, so a GitHub Actions run without GCP
// inputs must not fail on Vertex setup. With inputs set they still get WIF:
// behaviour tests run dummy agents on fleet harnesses that require the file.
func TestRunAgent_DummyRuntimeNeedsNoGCPInputs(t *testing.T) {
	for _, tc := range []struct {
		runtimeName, projectID, provider string
		requiredMount                    bool
	}{
		{runtimeName: "dummy"},
		{runtimeName: "dummy-playback"},
		{runtimeName: "dummy", projectID: "test-project", provider: testGCPWIFProvider, requiredMount: true},
	} {
		runtimeName := tc.runtimeName
		t.Run(runtimeName+"/"+tc.projectID, func(t *testing.T) {
			usePreScriptStub(t)
			setActionsGCPEnv(t, runtimeName, tc.projectID, tc.provider)
			stub := stubPrepareGitHubWIF(t, nil)
			marker := filepath.Join(t.TempDir(), "pre-script-ran")
			dir := newSkipHarnessDir(t, "touch "+marker+"\n")
			if tc.requiredMount {
				f, err := os.OpenFile(filepath.Join(dir, "harness", "code.yaml"), os.O_APPEND|os.O_WRONLY, 0)
				require.NoError(t, err)
				_, err = f.WriteString("host_files:\n  - src: ${GOOGLE_APPLICATION_CREDENTIALS}\n    dest: /tmp/.gcp-credentials.json\n")
				require.NoError(t, err)
				require.NoError(t, f.Close())
			}

			err := runSkipHarnessAgent(t, dir, ui.New(io.Discard))
			if err != nil {
				assert.NotContains(t, err.Error(), "FULLSEND_GCP_PROJECT_ID")
				assert.NotContains(t, err.Error(), "GOOGLE_APPLICATION_CREDENTIALS")
			}
			if tc.projectID != "" {
				assert.Equal(t, 1, stub.calls)
			} else {
				assert.Zero(t, stub.calls)
			}
			assert.FileExists(t, marker)
		})
	}
}

func TestRunInferenceProvider(t *testing.T) {
	assert.Equal(t, runProviderOpenAI, runInferenceProvider("codex", true))
	assert.Equal(t, runProviderNone, runInferenceProvider("dummy", false))
	assert.Equal(t, runProviderNone, runInferenceProvider("dummy-playback", false))
	assert.Equal(t, runProviderVertex, runInferenceProvider("opencode", false))
	assert.Equal(t, runProviderVertex, runInferenceProvider("claude", false))
}

func TestIsVertexProviderRef(t *testing.T) {
	assert.True(t, isVertexProviderRef("vertex-ai"))
	assert.True(t, isVertexProviderRef("providers/vertex-ai.yaml"))
	assert.True(t, isVertexProviderRef("providers/vertex-ai.yml"))
	assert.True(t, isVertexProviderRef("https://github.com/org/repo/tree/main/providers/vertex-ai.yaml#sha256="+strings.Repeat("a", 64)))
	assert.False(t, isVertexProviderRef("github"))
	assert.False(t, isVertexProviderRef("providers/github.yaml"))
	assert.False(t, isVertexProviderRef("providers/not-vertex-ai.yaml"))
}

func TestHarnessMayReachVertex(t *testing.T) {
	assert.False(t, harnessMayReachVertex(&harness.Harness{}))
	assert.False(t, harnessMayReachVertex(&harness.Harness{Providers: []string{"github"}}))
	assert.True(t, harnessMayReachVertex(&harness.Harness{Providers: []string{"github", "vertex-ai"}}))
	assert.True(t, harnessMayReachVertex(&harness.Harness{Providers: []string{"providers/vertex-ai.yaml"}}))
}

func TestValidateVertexGCPCredentials(t *testing.T) {
	h := &harness.Harness{HostFiles: []harness.HostFile{{
		Src:      "${GOOGLE_APPLICATION_CREDENTIALS}",
		Dest:     "/tmp/.gcp-credentials.json",
		Optional: true,
	}}}

	t.Run("Vertex accepts an existing GCP credential file", func(t *testing.T) {
		credentials := filepath.Join(t.TempDir(), "credentials.json")
		require.NoError(t, os.WriteFile(credentials, []byte("{}"), 0o600))
		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credentials)
		require.NoError(t, validateVertexGCPCredentials(h))
	})

	t.Run("Vertex rejects a missing GCP credential file", func(t *testing.T) {
		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing.json"))
		require.ErrorContains(t, validateVertexGCPCredentials(h), "existing file")
	})

	t.Run("Vertex rejects a directory", func(t *testing.T) {
		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", t.TempDir())
		require.ErrorContains(t, validateVertexGCPCredentials(h), "regular file")
	})

	t.Run("Vertex rejects an empty file", func(t *testing.T) {
		credentials := filepath.Join(t.TempDir(), "credentials.json")
		require.NoError(t, os.WriteFile(credentials, nil, 0o600))
		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credentials)
		require.ErrorContains(t, validateVertexGCPCredentials(h), "non-empty file")
	})
}

// Without a skip, the run must still reach sandbox creation — a guard
// against the skip path swallowing every run — and skipped=false must be
// relayed so an absent key means only "this CLI predates the protocol".
// The two assertions share one run.
func TestRunAgent_PreScriptNoSkip_ProceedsToSandboxAndRelaysFalse(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv("FULLSEND_RUNTIME", "codex")
	out := filepath.Join(t.TempDir(), "github-output")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_OUTPUT", out)
	dir := newSkipHarnessDir(t, "true\n")

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating sandbox")

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	// role=test is emitted right after the harness loads, before the
	// pre-script relay, and must reflect the harness role ("test") rather
	// than the agent name ("code") passed to runAgent above (#7000).
	assert.Equal(t, "role=test\nskipped=false\n", string(data))
}

// A harness with no pre_script must still relay skipped=false, otherwise
// an empty output would mean two different things and the documented
// three-state contract would not hold.
func TestRunAgent_NoPreScript_StillRelaysSkippedFalse(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv("FULLSEND_RUNTIME", "codex")
	out := filepath.Join(t.TempDir(), "github-output")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_OUTPUT", out)
	dir := newSkipHarnessDir(t, "")

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating sandbox")

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	// role=test is emitted right after the harness loads, before the
	// pre-script relay, and must reflect the harness role ("test") rather
	// than the agent name ("code") passed to runAgent above (#7000).
	assert.Equal(t, "role=test\nskipped=false\n", string(data))
}

// The skip path relays skipped=true. Fast: it returns before sandbox
// creation, so it does not pay the create-retry backoff.
func TestRunAgent_PreScriptSkip_RelaysSkippedTrue(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv("FULLSEND_RUNTIME", "codex")
	out := filepath.Join(t.TempDir(), "github-output")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_OUTPUT", out)
	dir := newSkipHarnessDir(t, `echo "skipped=true" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"+
		`echo "reason=open PR exists" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n")

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	require.NoError(t, runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "",
		rFlags, statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{}))

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	// role=test is emitted right after the harness loads, before the
	// pre-script relay, and must reflect the harness role ("test") rather
	// than the agent name ("code") passed to runAgent above (#7000).
	assert.Equal(t, "role=test\nskipped=true\nreason=open PR exists\n", string(data))
}

// A relay target that cannot be written must fail the run rather than
// exiting 0 with a decision the workflow gate never sees.
func TestRunAgent_PreScriptRelayFailureIsHardError(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv("FULLSEND_RUNTIME", "codex")
	t.Setenv("GITHUB_ACTIONS", "true")
	// A directory can be opened but not written to.
	t.Setenv("GITHUB_OUTPUT", t.TempDir())
	dir := newSkipHarnessDir(t, `echo "skipped=true" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n")

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
	require.ErrorContains(t, err, "relaying pre-script outputs")
}

func TestRunPreScript_OutputFileExistsAndIsWritable(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t,
		`[ -f "${FULLSEND_PRESCRIPT_OUTPUT}" ] || exit 8`+"\n"+
			`[ -w "${FULLSEND_PRESCRIPT_OUTPUT}" ] || exit 9`+"\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.False(t, res.Skipped)
}

// The output file is removed once parsed, so skips do not accumulate
// files in the run directory.
func TestRunPreScript_CleansUpOutputFile(t *testing.T) {
	printer := ui.New(io.Discard)
	runDir := t.TempDir()
	h := &harness.Harness{PreScript: writePreScript(t,
		`echo "skipped=true" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n")}

	_, err := runPreScript(h, runDir, "", printer)
	require.NoError(t, err)

	entries, err := os.ReadDir(runDir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// --- Exit code 78 (neutral skip) tests (issue #582) ---

func TestRunPreScript_Exit78_SkipsWithStdoutReason(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t,
		"echo \"No issues need scoring\"\nexit 78\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Equal(t, "No issues need scoring", res.Reason)
}

func TestRunPreScript_Exit78_SkipsWithOutputFileReason(t *testing.T) {
	printer := ui.New(io.Discard)
	// Script writes a reason to the output file, then exits 78. The file
	// reason should take precedence over stdout.
	h := &harness.Harness{PreScript: writePreScript(t,
		`echo "reason=all scores are fresh" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"+
			"echo \"stdout line\"\n"+
			"exit 78\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Equal(t, "all scores are fresh", res.Reason)
}

func TestRunPreScript_Exit78_OverridesSkippedFalseInFile(t *testing.T) {
	printer := ui.New(io.Discard)
	// Script explicitly writes skipped=false but exits 78. Exit code wins.
	h := &harness.Harness{PreScript: writePreScript(t,
		`echo "skipped=false" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"+
			"exit 78\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped, "exit 78 must override skipped=false in output file")
}

func TestRunPreScript_Exit78_NoReasonDefaultsEmpty(t *testing.T) {
	printer := ui.New(io.Discard)
	// Script exits 78 with no stdout and no output file content.
	h := &harness.Harness{PreScript: writePreScript(t, "exit 78\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Empty(t, res.Reason)
}

func TestRunPreScript_Exit78_DeletedOutputFileStillSkips(t *testing.T) {
	printer := ui.New(io.Discard)
	// Script deletes the output file then exits 78. Exit 0 treats a missing
	// file as a hard error; exit 78 must still skip — the exit code is
	// authoritative.
	h := &harness.Harness{PreScript: writePreScript(t,
		`rm -f "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"+
			"echo \"No work today\"\n"+
			"exit 78\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Equal(t, "No work today", res.Reason)
}

func TestRunPreScript_Exit78_MalformedOutputFileStillSkips(t *testing.T) {
	printer := ui.New(io.Discard)
	// Script writes malformed content to the output file but exits 78.
	// The exit code is authoritative — a parse error must not block the skip.
	h := &harness.Harness{PreScript: writePreScript(t,
		`echo "this is not key=value format but has no equals" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"+
			"echo \"Skipping: no work\"\n"+
			"exit 78\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Equal(t, "Skipping: no work", res.Reason)
}

func TestRunPreScript_Exit78_PreservesOtherOutputs(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t,
		`echo "reason=stale scores refreshed" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"+
			`echo "checked_count=42" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"+
			"exit 78\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Equal(t, "stale scores refreshed", res.Reason)
	assert.Equal(t, "42", res.Outputs["checked_count"])
}

func TestRunPreScript_Exit78_UsesLastNonEmptyStdoutLine(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t,
		"echo \"Checking issues...\"\n"+
			"echo \"Checked 5 issues\"\n"+
			"echo \"All scores are current\"\n"+
			"echo \"\"\n"+
			"exit 78\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Equal(t, "All scores are current", res.Reason)
}

func TestRunPreScript_Exit78_StdoutReasonSanitized(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t,
		"printf 'Has\\ttab and \\x01control'\nexit 78\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Equal(t, "Hastab and control", res.Reason)
}

// Exit code 78 from a pre-script must still relay as skipped=true so
// workflow-level gating works correctly.
func TestRunAgent_PreScriptExit78_RelaysSkippedTrue(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv("FULLSEND_RUNTIME", "codex")
	out := filepath.Join(t.TempDir(), "github-output")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_OUTPUT", out)
	dir := newSkipHarnessDir(t, "echo \"Nothing to do\"\nexit 78\n")

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	require.NoError(t, runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "",
		rFlags, statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{}))

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Contains(t, string(data), "skipped=true")
	assert.Contains(t, string(data), "reason=Nothing to do")
}

// Other non-zero exit codes must remain hard failures — only 78 is neutral.
func TestRunPreScript_OtherNonZeroExitIsStillHardError(t *testing.T) {
	printer := ui.New(io.Discard)
	for _, code := range []int{1, 2, 77, 79, 127} {
		t.Run(fmt.Sprintf("exit_%d", code), func(t *testing.T) {
			h := &harness.Harness{PreScript: writePreScript(t,
				fmt.Sprintf("exit %d\n", code))}
			_, err := runPreScript(h, t.TempDir(), "", printer)
			require.ErrorContains(t, err, "running pre-script")
		})
	}
}

// --- Hard-failure diagnostics (issue #7363) ---

func TestRunPreScript_HardFailureIncludesStderr(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t,
		"echo checking...\n"+
			"echo 'Fix iteration 6 exceeds bot cap of 5. Escalating to human.' >&2\n"+
			"exit 1\n")}

	_, err := runPreScript(h, t.TempDir(), "", printer)
	require.ErrorContains(t, err, "running pre-script")
	require.ErrorContains(t, err, "Fix iteration 6 exceeds bot cap of 5. Escalating to human.")
}

func TestRunPreScript_HardFailureIncludesGHAErrorOnStdout(t *testing.T) {
	printer := ui.New(io.Discard)
	// pre-fix.sh emits workflow-command annotations on stdout via gha_echo.
	h := &harness.Harness{PreScript: writePreScript(t,
		"echo '::error::Fix iteration 6 exceeds bot cap of 5. Escalating to human.'\n"+
			"echo '::error::A human can still direct the agent with /fs-fix (up to 10 total iterations).'\n"+
			"exit 1\n")}

	_, err := runPreScript(h, t.TempDir(), "", printer)
	require.ErrorContains(t, err, "running pre-script")
	require.ErrorContains(t, err, "Fix iteration 6 exceeds bot cap of 5. Escalating to human.")
	require.ErrorContains(t, err, "A human can still direct the agent with /fs-fix (up to 10 total iterations).")
}

// A pre-script's hard-failure detail is posted to the visible PR status
// comment, so a credential value from the runner env that a script echoes
// on its way to a hard failure must not reach that comment verbatim.
func TestRunPreScript_HardFailureRedactsRunnerEnvSecret(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{
		PreScript: writePreScript(t,
			"echo \"remote is https://x-access-token:${PUSH_TOKEN}@github.com/o/r.git\" >&2\n"+
				"exit 1\n"),
		RunnerEnv: map[string]string{"PUSH_TOKEN": "supersecretpushtokenvalue1234567890"},
	}

	_, err := runPreScript(h, t.TempDir(), "", printer)
	require.ErrorContains(t, err, "running pre-script")
	assert.NotContains(t, err.Error(), "supersecretpushtokenvalue1234567890")
	assert.Contains(t, err.Error(), "[REDACTED:PUSH_TOKEN]")
}

// The exit-78 stdout-derived reason has the same exposure as the
// hard-failure detail — it is incidental script output, not a value the
// script author deliberately chose to put in a reason= line — and gets the
// same redaction pass.
func TestRunPreScript_Exit78StdoutReasonRedactsRunnerEnvSecret(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{
		PreScript: writePreScript(t,
			"echo \"skip check used token ${PUSH_TOKEN}\"\n"+
				"exit 78\n"),
		RunnerEnv: map[string]string{"PUSH_TOKEN": "supersecretpushtokenvalue1234567890"},
	}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.NotContains(t, res.Reason, "supersecretpushtokenvalue1234567890")
	assert.Contains(t, res.Reason, "[REDACTED:PUSH_TOKEN]")
}

func TestPreScriptFailureDetail(t *testing.T) {
	tests := []struct {
		name           string
		stdout, stderr string
		want           string
	}{
		{
			name: "empty",
			want: "",
		},
		{
			name:   "stderr last line",
			stdout: "checking...\n",
			stderr: "boom\n",
			want:   "boom",
		},
		{
			name:   "stdout fallback when stderr empty",
			stdout: "boom\n",
			want:   "boom",
		},
		{
			name:   "stderr preferred over stdout without annotations",
			stdout: "progress\n",
			stderr: "real error\n",
			want:   "real error",
		},
		{
			name:   "gha workflow command on stdout",
			stdout: "::error::Fix iteration 6 exceeds bot cap of 5\n",
			stderr: "noise\n",
			want:   "Fix iteration 6 exceeds bot cap of 5",
		},
		{
			name:   "gha logging command on stderr",
			stderr: "##[error]Fix iteration 6 exceeds bot cap of 5\n",
			want:   "Fix iteration 6 exceeds bot cap of 5",
		},
		{
			name:   "gha error with parameters",
			stdout: "::error title=pre-fix,file=pre-fix.sh::input validation failed\n",
			want:   "input validation failed",
		},
		{
			name: "multiple gha errors joined in order",
			stdout: "::error::Fix iteration 6 exceeds bot cap of 5. Escalating to human.\n" +
				"::error::The review-fix loop has run 6 times without converging.\n" +
				"::error::A human can still direct the agent with /fs-fix (up to 10 total iterations).\n",
			want: "Fix iteration 6 exceeds bot cap of 5. Escalating to human. " +
				"The review-fix loop has run 6 times without converging. " +
				"A human can still direct the agent with /fs-fix (up to 10 total iterations).",
		},
		{
			name:   "blank and warning lines ignored",
			stdout: "::warning::not an error\n\n::error::the real problem\n",
			want:   "the real problem",
		},
		{
			name:   "empty annotation skipped",
			stdout: "::error::\n::error::kept\n",
			want:   "kept",
		},
		{
			name:   "control characters stripped",
			stderr: "Has\ttab and \x01control\n",
			want:   "Hastab and control",
		},
		{
			name:   "whitespace-only streams",
			stdout: "  \n\n",
			stderr: "\t\n",
			want:   "",
		},
		{
			// The implementation scans a stdout+stderr concatenation, so
			// stdout annotations always sort first even when the stderr
			// annotation was actually written first — stream order, not
			// true chronological order (see the doc comment).
			name:   "mixed-stream annotations: stdout group first regardless of write order",
			stdout: "::error::from stdout\n",
			stderr: "::error::from stderr\n",
			want:   "from stdout from stderr",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, preScriptFailureDetail(tc.stdout, tc.stderr))
		})
	}
}

func TestPreScriptFailureDetail_CapsAt1024(t *testing.T) {
	long := strings.Repeat("x", 2000)
	got := preScriptFailureDetail("", long)
	require.Len(t, got, 1024)
	assert.Equal(t, strings.Repeat("x", 1024), got)
}

func TestPreScriptFailureDetail_TruncatesInvalidUTF8(t *testing.T) {
	// 1023 ASCII bytes plus the first byte of a 2-byte rune, so the
	// 1024-byte cap lands mid-character and must be trimmed back.
	long := strings.Repeat("x", 1023) + "é"
	got := preScriptFailureDetail("", long)
	require.True(t, utf8.ValidString(got))
	assert.Equal(t, strings.Repeat("x", 1023), got)
}

func TestParseGHAErrorLine(t *testing.T) {
	tests := []struct {
		line   string
		want   string
		wantOK bool
	}{
		{"::error::hello", "hello", true},
		{"::error title=t::hello", "hello", true},
		{"##[error]hello", "hello", true},
		{"##[error]  hello  ", "hello", true},
		{"::warning::hello", "", false},
		{"::error", "", false},
		{"not an annotation", "", false},
		{"::errorfoo::hello", "", false},
		{"::error::", "", true},
		{"::error title=t", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			got, ok := parseGHAErrorLine(tc.line)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

// usePreScriptStub puts an openshell stub on PATH that passes the gateway
// check but refuses sandbox creation, so a run that gets that far fails
// recognizably. It also replaces sandbox.RetrySleepFn with a no-op so
// retry backoff does not add real delays (see #6060).
func usePreScriptStub(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_ACTIONS", "false")
	stubDir, err := filepath.Abs(filepath.Join("testdata", "prescript-stub"))
	require.NoError(t, err)
	t.Setenv("PATH", stubDir+string(filepath.ListSeparator)+os.Getenv("PATH"))

	orig := sandbox.RetrySleepFn
	sandbox.RetrySleepFn = func(time.Duration) {}
	t.Cleanup(func() { sandbox.RetrySleepFn = orig })
}

// newSkipHarnessDir builds a minimal fullsend dir whose code harness runs
// the given pre-script body.
func newSkipHarnessDir(t *testing.T, preScriptBody string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "code.md"),
		[]byte("You are a coding agent."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("agents:\n  - harness/code.yaml\n"), 0o644))

	harnessYAML := "agent: agents/code.md\nrole: test\n"
	if preScriptBody != "" {
		harnessYAML += "pre_script: " + writePreScript(t, preScriptBody) + "\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"),
		[]byte(harnessYAML), 0o644))
	return dir
}
