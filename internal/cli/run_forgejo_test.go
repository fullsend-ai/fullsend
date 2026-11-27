package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/mintclient"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// testForgejoRunToken stands in for the Forgejo job's automatic run token.
const testForgejoRunToken = "forgejo-run-token-for-tests-0123456789"

// forgejoAPIProfile is a provider profile that issues FORGEJO_TOKEN on the
// test Forgejo instance only.
const forgejoAPIProfile = `id: fullsend-forgejo-api
display_name: Forgejo API
category: source_control
credentials:
  - name: api_token
    env_vars: [FORGEJO_TOKEN]
    required: true
endpoints:
  - host: forgejo.example.test
    port: 3443
    protocol: rest
    access: read-write
    enforcement: enforce
binaries:
  - "**/curl"
  - "**/git"
`

// clearForgejoRunEnv blanks the Forgejo, runner-detection and mint
// variables a Forgejo run reads, so the ambient environment cannot change
// what a test sees.
func clearForgejoRunEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		forgejoTokenEnv, forgejoURLEnv, forgejoAgentsRepoEnv, forgejoAgentsRefEnv,
		"FORGEJO_ACTIONS", "GITEA_ACTIONS", "GITLAB_CI", "GITHUB_OUTPUT",
		"GITHUB_SERVER_URL", "FULLSEND_RUNTIME", "FULLSEND_GCP_OIDC_URL",
		"FULLSEND_GCP_PROJECT_ID", "FULLSEND_GCP_WIF_PROVIDER",
	} {
		t.Setenv(k, "")
	}
	// A run with GITHUB_ACTIONS set validates Vertex credentials, which the
	// default runtime needs. Point at a fixture file so the result does not
	// depend on whether the host (or CI job) already provides one.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", writeHostFileFixture(t, "external_account"))
}

// writeForgejoRunFixture writes a config directory with one local agent,
// "hello", whose harness attaches a provider that hands FORGEJO_TOKEN to the
// sandbox under profile. It returns the directory.
func writeForgejoRunFixture(t *testing.T, profile string) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"harness", "agents", "providers", "profiles"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, d), 0o755))
	}
	writePolicyFixture(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "hello.md"), []byte("You are an agent."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "providers", "forgejo.yaml"),
		[]byte("name: forgejo\ntype: fullsend-forgejo-api\ncredentials:\n  FORGEJO_TOKEN: \"${FORGEJO_TOKEN}\"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profiles", "fullsend-forgejo-api.yaml"), []byte(profile), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "hello.yaml"), []byte(
		"agent: agents/hello.md\nrole: coder\npolicy: policies/base.yaml\n"+
			"providers:\n  - providers/forgejo.yaml\n"+
			"openshell:\n  profiles:\n    - profiles/fullsend-forgejo-api.yaml\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("version: \"1\"\nagents:\n  - harness/hello.yaml\n"), 0o644))
	return dir
}

// forbidGitHubCredentials fails the test if the run mints a token or asks
// gh for one.
func forbidGitHubCredentials(t *testing.T) {
	t.Helper()
	origMint := statusMintToken
	t.Cleanup(func() { statusMintToken = origMint })
	statusMintToken = func(_ context.Context, _ mintclient.MintRequest) (*mintclient.MintResult, error) {
		t.Error("a Forgejo run must not mint a token")
		return nil, errors.New("mint called")
	}
	origGH := ghAuthTokenFn
	t.Cleanup(func() { ghAuthTokenFn = origGH })
	ghAuthTokenFn = func() ([]byte, error) {
		t.Error("a Forgejo run must not read a GitHub token from gh")
		return nil, errors.New("gh auth token called")
	}
}

// markProviderCreateToken wraps the stub openshell so that each
// `provider create` also records, in the returned file, whether the
// subprocess's FORGEJO_TOKEN environment variable equals want. Only a
// "match" or "mismatch" marker is written, never the token itself.
func markProviderCreateToken(t *testing.T, logPath, want string) string {
	t.Helper()
	binDir := filepath.Dir(logPath)
	markPath := filepath.Join(binDir, "provider-token.marker")
	basePath := filepath.Join(binDir, "openshell-marked")
	require.NoError(t, os.Rename(filepath.Join(binDir, "openshell"), basePath))
	script := "#!/bin/sh\n" +
		"if [ \"$1 $2\" = 'provider create' ]; then\n" +
		"  if [ \"$FORGEJO_TOKEN\" = " + shellQuoteForTest(want) + " ]; then\n" +
		"    echo match >> " + shellQuoteForTest(markPath) + "\n" +
		"  else\n" +
		"    echo mismatch >> " + shellQuoteForTest(markPath) + "\n" +
		"  fi\n" +
		"fi\n" +
		"exec " + shellQuoteForTest(basePath) + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	return markPath
}

// TestRunAgent_ForgejoTriggeredRun runs a local agent fixture as a Forgejo
// Actions job would: the runner sets FORGEJO_ACTIONS (and GITHUB_ACTIONS,
// which Forgejo runners also set), the job passes its run token as
// FORGEJO_TOKEN, and a mint URL is configured. The run must detect the
// Forgejo forge, mint nothing, read no GitHub token, hand FORGEJO_TOKEN to
// the sandbox only through the harness provider, and import a profile for
// the configured Forgejo instance.
func TestRunAgent_ForgejoTriggeredRun(t *testing.T) {
	logPath, captureDir := capturingProfileImportStub(t)
	markPath := markProviderCreateToken(t, logPath, testForgejoRunToken)
	clearForgejoRunEnv(t)
	forbidGitHubCredentials(t)
	t.Setenv("FORGEJO_ACTIONS", "true")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("FULLSEND_MINT_URL", "https://mint.example.com")
	t.Setenv("REPO_FULL_NAME", "org/my-repo")
	t.Setenv(forgejoTokenEnv, testForgejoRunToken)
	t.Setenv(forgejoURLEnv, "https://forgejo.example.test:3443/forgejo")
	dir := writeForgejoRunFixture(t, forgejoAPIProfile)

	var out bytes.Buffer
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	sOpts := statusOpts{statusRepo: "org/my-repo", statusNum: 7}
	err := runAgent(context.Background(), "hello", dir, "", t.TempDir(), fakeSandboxBinary(t), nil, false, "", "", "", rFlags, sOpts, ui.New(&out), false, runOverrideFlags{})
	// The stub cannot bootstrap an agent; credentials, profiles and sandbox
	// creation, which this test checks, come first. The run must stop at the
	// first bootstrap step (uploading the fullsend binary), so an unrelated
	// earlier or later failure does not pass.
	require.Error(t, err)
	assert.ErrorContains(t, err, "copying fullsend binary to sandbox")
	assert.NotContains(t, err.Error(), forgejoTokenEnv)
	assert.NotContains(t, err.Error(), forgejoURLEnv)

	output := out.String()
	assert.Contains(t, output, "Forgejo run: using FORGEJO_TOKEN for forgejo.example.test:3443; no token is minted")
	assert.Contains(t, output, "Status notifications disabled: status comments are not supported on the forgejo forge yet")
	assert.NotContains(t, output, "skipping token minting")
	assert.NotContains(t, output, testForgejoRunToken)

	data, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr)
	log := string(data)
	assert.NotContains(t, log, testForgejoRunToken, "the run token never reaches a command line")
	assert.Regexp(t, `(?m)^provider create --name forgejo --type fullsend-forgejo-api --credential FORGEJO_TOKEN$`, log)
	marker, markErr := os.ReadFile(markPath)
	require.NoError(t, markErr, "provider create ran")
	assert.Equal(t, "match\n", string(marker), "the provider subprocess receives the run token in FORGEJO_TOKEN")
	var sandboxLine string
	for _, l := range strings.Split(strings.TrimSpace(log), "\n") {
		if strings.HasPrefix(l, "sandbox create ") {
			sandboxLine = l
		}
	}
	require.NotEmpty(t, sandboxLine, "sandbox created: %s", log)
	assert.Regexp(t, `--provider forgejo( |$)`, sandboxLine, "the Forgejo credential provider is attached")

	generated, readErr := os.ReadFile(filepath.Join(captureDir, forgejoForgeProfileID+".yaml"))
	require.NoError(t, readErr, "the Forgejo forge profile was imported")
	p := parseBoundTestProfile(t, generated)
	assert.Equal(t, forgejoForgeProfileID, p.ID)
	assert.Empty(t, p.Credentials, "the generated profile carries no credential")
	require.Len(t, p.Endpoints, 1)
	assert.Equal(t, "forgejo.example.test", p.Endpoints[0].Host)
	assert.Equal(t, 3443, p.Endpoints[0].Port)
}

// TestRunAgent_ForgejoFailsClearly checks each Forgejo precondition stops the
// run with its own error before any sandbox is created.
func TestRunAgent_ForgejoFailsClearly(t *testing.T) {
	otherHostProfile := strings.Replace(forgejoAPIProfile, "host: forgejo.example.test", "host: api.github.com", 1)
	for _, tc := range []struct {
		name    string
		token   string
		url     string
		profile string
		// provider, when set, replaces the fixture's provider definition.
		provider string
		// bareProvider makes the harness name the provider ("forgejo")
		// instead of listing its file, so it is not path-resolved.
		bareProvider bool
		wantIs       error
		wantErr      string
	}{
		{name: "missing FORGEJO_TOKEN", url: "https://forgejo.example.test:3443", profile: forgejoAPIProfile,
			wantIs: errForgejoTokenMissing},
		{name: "blank FORGEJO_TOKEN", token: "   ", url: "https://forgejo.example.test:3443", profile: forgejoAPIProfile,
			wantIs: errForgejoTokenMissing},
		{name: "no Forgejo instance for the sandbox", token: testForgejoRunToken, profile: forgejoAPIProfile,
			wantIs: errForgejoInstanceMissing},
		{name: "plain http to a remote instance", token: testForgejoRunToken, url: "http://forgejo.example.test", profile: forgejoAPIProfile,
			wantErr: "must use https"},
		{name: "profile sends FORGEJO_TOKEN to another host", token: testForgejoRunToken, url: "https://forgejo.example.test:3443", profile: otherHostProfile,
			wantErr: `binding FORGEJO_TOKEN to the Forgejo instance: profile "fullsend-forgejo-api": profile issues FORGEJO_TOKEN on api.github.com:3443; want only forgejo.example.test:3443`},
		{name: "provider hands FORGEJO_TOKEN to the sandbox under another key", token: testForgejoRunToken, url: "https://forgejo.example.test:3443", profile: strings.Replace(otherHostProfile, "FORGEJO_TOKEN", "API_TOKEN", 1),
			provider: "name: forgejo\ntype: fullsend-forgejo-api\ncredentials:\n  API_TOKEN: \"${FORGEJO_TOKEN}\"\n",
			wantIs:   errForgejoTokenUnderOtherKey},
		{name: "provider config value references FORGEJO_TOKEN", token: testForgejoRunToken, url: "https://forgejo.example.test:3443", profile: forgejoAPIProfile,
			provider: "name: forgejo\ntype: fullsend-forgejo-api\ncredentials:\n  FORGEJO_TOKEN: \"${FORGEJO_TOKEN}\"\nconfig:\n  api_key: \"${FORGEJO_TOKEN}\"\n",
			wantIs:   errForgejoTokenUnderOtherKey},
		{name: "provider hands the run token to the sandbox as GITHUB_TOKEN under another key", token: testForgejoRunToken, url: "https://forgejo.example.test:3443", profile: strings.Replace(otherHostProfile, "FORGEJO_TOKEN", "API_TOKEN", 1),
			provider: "name: forgejo\ntype: fullsend-forgejo-api\ncredentials:\n  API_TOKEN: \"${GITHUB_TOKEN}\"\n",
			wantIs:   errForgejoTokenUnderOtherKey},
		{name: "provider config value references GITHUB_TOKEN", token: testForgejoRunToken, url: "https://forgejo.example.test:3443", profile: forgejoAPIProfile,
			provider: "name: forgejo\ntype: fullsend-forgejo-api\ncredentials:\n  FORGEJO_TOKEN: \"${FORGEJO_TOKEN}\"\nconfig:\n  api_key: \"${GITHUB_TOKEN}\"\n",
			wantIs:   errForgejoTokenUnderOtherKey},
		{name: "provider profile is an unlisted gateway profile", token: testForgejoRunToken, url: "https://forgejo.example.test:3443", profile: forgejoAPIProfile,
			provider:     "name: forgejo\ntype: fullsend-preinstalled\ncredentials:\n  FORGEJO_TOKEN: \"${FORGEJO_TOKEN}\"\n",
			bareProvider: true,
			wantIs:       errForgejoTokenProfileNotLoaded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := recordingProvidersStub(t)
			clearForgejoRunEnv(t)
			forbidGitHubCredentials(t)
			t.Setenv("FULLSEND_MINT_URL", "https://mint.example.com")
			t.Setenv("REPO_FULL_NAME", "org/my-repo")
			t.Setenv(forgejoTokenEnv, tc.token)
			t.Setenv(forgejoURLEnv, tc.url)
			dir := writeForgejoRunFixture(t, tc.profile)
			if tc.provider != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "providers", "forgejo.yaml"), []byte(tc.provider), 0o644))
			}
			if tc.bareProvider {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "hello.yaml"), []byte(
					"agent: agents/hello.md\nrole: coder\npolicy: policies/base.yaml\n"+
						"providers:\n  - forgejo\n"+
						"openshell:\n  profiles:\n    - profiles/fullsend-forgejo-api.yaml\n"), 0o644))
			}

			var out bytes.Buffer
			rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
			err := runAgent(context.Background(), "hello", dir, "", t.TempDir(), "", nil, false, "", "forgejo", "", rFlags, statusOpts{}, ui.New(&out), false, runOverrideFlags{})
			require.Error(t, err)
			if tc.wantIs != nil {
				assert.ErrorIs(t, err, tc.wantIs)
			}
			if tc.wantErr != "" {
				assert.Contains(t, err.Error(), tc.wantErr)
			}
			assert.NotContains(t, out.String(), testForgejoRunToken)
			data, _ := os.ReadFile(logPath)
			assert.NotContains(t, string(data), "sandbox create", "no sandbox is created")
		})
	}
}

// TestRunAgent_ForgejoTokenRegisteredForRedaction checks the accepted
// FORGEJO_TOKEN, which reaches the run only through the process environment
// and a provider, is registered with the runtime redactor, so feedback that
// echoes it is scrubbed.
func TestRunAgent_ForgejoTokenRegisteredForRedaction(t *testing.T) {
	const opaque = "zzopaqueforgejo0123456789run"
	clearForgejoRunEnv(t)
	forbidGitHubCredentials(t)
	capturingProfileImportStub(t)
	t.Setenv("FORGEJO_ACTIONS", "true")
	t.Setenv("FULLSEND_MINT_URL", "https://mint.example.com")
	t.Setenv("REPO_FULL_NAME", "org/my-repo")
	t.Setenv(forgejoTokenEnv, opaque)
	t.Setenv(forgejoURLEnv, "https://forgejo.example.test:3443")
	dir := writeForgejoRunFixture(t, forgejoAPIProfile)

	var out bytes.Buffer
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	_ = runAgent(context.Background(), "hello", dir, "", t.TempDir(), fakeSandboxBinary(t), nil, false, "", "", "", rFlags, statusOpts{}, ui.New(&out), false, runOverrideFlags{})

	got := redactFeedback("script failed: token "+opaque+" rejected", nil)
	assert.NotContains(t, got, opaque)
	assert.NotContains(t, out.String(), opaque)
}

func TestRegisterForgejoRunToken(t *testing.T) {
	getenv := func(v string) func(string) string {
		return func(string) string { return v }
	}
	require.NoError(t, registerForgejoRunToken(getenv("  zzopaqueforgejo-register-0123456789  ")))
	assert.NotContains(t, redactFeedback("x zzopaqueforgejo-register-0123456789 y", nil), "zzopaqueforgejo-register-0123456789")
	assert.ErrorIs(t, registerForgejoRunToken(getenv("short")), errForgejoTokenUnredactable)
}

func TestDetectForgePlatform_ForgejoRunner(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		flag string
		want string
	}{
		{name: "FORGEJO_ACTIONS precedes GITHUB_ACTIONS", env: map[string]string{"FORGEJO_ACTIONS": "true", "GITHUB_ACTIONS": "true"}, want: "forgejo"},
		{name: "GITEA_ACTIONS", env: map[string]string{"GITEA_ACTIONS": "true", "GITHUB_ACTIONS": "true"}, want: "forgejo"},
		{name: "explicit flag", flag: "forgejo", want: "forgejo"},
		{name: "flag overrides a Forgejo runner", env: map[string]string{"FORGEJO_ACTIONS": "true"}, flag: "github", want: "github"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{"FORGEJO_ACTIONS", "GITEA_ACTIONS", "GITHUB_ACTIONS", "GITLAB_CI"} {
				t.Setenv(k, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := detectForgePlatform(tc.flag, nil)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestResolveForgejoRunInstance(t *testing.T) {
	for _, tc := range []struct {
		name     string
		url      string
		wantHost string
		wantPort int
		wantErr  string
	}{
		{name: "unset", wantErr: "FULLSEND_FORGEJO_URL is not set"},
		{name: "https default port", url: "https://forgejo.example.com", wantHost: "forgejo.example.com", wantPort: 443},
		{name: "path prefix and port", url: " https://forgejo.example.com:8443/forgejo/ ", wantHost: "forgejo.example.com", wantPort: 8443},
		{name: "loopback http default port", url: "http://localhost", wantHost: "localhost", wantPort: 80},
		{name: "loopback http with port", url: "http://127.0.0.1:3000", wantHost: "127.0.0.1", wantPort: 3000},
		{name: "remote http", url: "http://forgejo.example.com", wantErr: "must use https"},
		{name: "credentials", url: "https://user:pw@forgejo.example.com", wantErr: "must not include credentials"},
		{name: "not a URL", url: "forgejo.example.com", wantErr: "must be an absolute https URL"},
		{name: "wildcard host", url: "https://*.example.com", wantErr: "FULLSEND_FORGEJO_URL"},
		{name: "port out of range", url: "https://forgejo.example.com:70000", wantErr: "out of range"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(k string) string {
				if k == forgejoURLEnv {
					return tc.url
				}
				return ""
			}
			host, port, err := resolveForgejoRunInstance(getenv)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantHost, host)
			assert.Equal(t, tc.wantPort, port)
		})
	}
}

func TestRunGitHubTokenResolver_ForgejoReadsNoGitHubToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", testForgejoRunToken)
	token, err := runGitHubTokenResolver(repos.ForgeForgejo)()
	assert.Empty(t, token, "a Forgejo job's GITHUB_TOKEN is its Forgejo run token")
	assert.ErrorIs(t, err, errForgejoGitHubTokenUnused)

	token, err = runGitHubTokenResolver(repos.ForgeGitHub)()
	require.NoError(t, err)
	assert.Equal(t, testForgejoRunToken, token, "other forges keep the standard resolution")
}

func TestForgeUsesDirectToken(t *testing.T) {
	assert.True(t, forgeUsesDirectToken(repos.ForgeForgejo))
	assert.True(t, forgeUsesDirectToken(repos.ForgeGitLab))
	assert.False(t, forgeUsesDirectToken(repos.ForgeGitHub))
	assert.False(t, forgeUsesDirectToken(""))
}

func TestRemintSkippedOnForgejo(t *testing.T) {
	forbidGitHubCredentials(t)
	h := &harness.Harness{Role: "coder", PrivilegeLevels: map[string]string{
		harness.PrivilegeStagePreScript:  "read",
		harness.PrivilegeStagePostScript: "read",
	}}
	printer := ui.New(&bytes.Buffer{})

	cleanup, err := remintAgentTokenForPostScript(context.Background(), h, "https://mint.example.com", repos.ForgeForgejo, "write", printer)
	require.NoError(t, err)
	cleanup()

	cleanup, err = maybeRemintAgentTokenForStage(context.Background(), h, "https://mint.example.com", repos.ForgeForgejo, harness.PrivilegeStagePreScript, "write", printer)
	require.NoError(t, err)
	cleanup()
}

func TestSetupStatusNotifier_ForgejoUnsupported(t *testing.T) {
	_, err := setupStatusNotifier(t.TempDir(), "coder", repos.ForgeForgejo, statusOpts{statusRepo: "org/repo", statusNum: 1}, ui.New(&bytes.Buffer{}))
	assert.ErrorIs(t, err, errForgejoStatusUnsupported)
}

func TestResolvePlaybackForgeClient_ForgejoUnsupported(t *testing.T) {
	var out bytes.Buffer
	assert.Nil(t, resolvePlaybackForgeClient(repos.ForgeForgejo, nil, ui.New(&out)))
	assert.Contains(t, out.String(), "tracking comments are not supported on the forgejo forge yet")
}

// A config-only `forge: forgejo` (no --forge, no Forgejo Actions env) must
// reach the Forgejo guard rather than the GitHub client.
func TestNewReconcileStatusCmd_ForgejoFromConfig(t *testing.T) {
	clearForgejoRunEnv(t)
	t.Setenv("FULLSEND_MINT_URL", "https://mint.example.com")
	forbidGitHubCredentials(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("version: \"1\"\nforge: forgejo\n"), 0o644))
	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{"--repo", "org/repo", "--number", "7", "--run-id", "run-1", "--fullsend-dir", dir})
	err := cmd.Execute()
	assert.ErrorIs(t, err, errForgejoStatusUnsupported)
}

func TestNewReconcileStatusCmd_ForgejoUnsupported(t *testing.T) {
	t.Setenv("FULLSEND_MINT_URL", "https://mint.example.com")
	forbidGitHubCredentials(t)
	cmd := newReconcileStatusCmd()
	cmd.SetArgs([]string{"--repo", "org/repo", "--number", "7", "--run-id", "run-1", "--forge", "forgejo"})
	err := cmd.Execute()
	assert.ErrorIs(t, err, errForgejoStatusUnsupported)
}

func TestForgejoEgressEntry(t *testing.T) {
	assert.Equal(t, "forgejo.example.com:443", forgejoEgressEntry("forgejo.example.com", 443))
	assert.Equal(t, "", forgejoEgressEntry("", 0))
}

func TestCheckForgejoCredentialBinding(t *testing.T) {
	dir := t.TempDir()
	good := writeTestProfile(t, dir, "good.yaml", forgejoAPIProfile)
	other := writeTestProfile(t, dir, "other.yaml", "id: fullsend-vertex-ai\nendpoints:\n  - host: aiplatform.googleapis.com\n    port: 443\n")
	twoHosts := writeTestProfile(t, dir, "two.yaml", strings.Replace(forgejoAPIProfile, "binaries:\n",
		"  - host: forgejo.example.test\n    port: 22\nbinaries:\n", 1))

	require.NoError(t, checkForgejoCredentialBinding([]resolve.ResolvedProfile{
		{ID: "good", LocalPath: good}, {ID: "other", LocalPath: other},
	}, "forgejo.example.test", 3443))

	err := checkForgejoCredentialBinding([]resolve.ResolvedProfile{{ID: "two", LocalPath: twoHosts}}, "forgejo.example.test", 3443)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `profile "two": profile issues FORGEJO_TOKEN on 2 endpoints`)

	err = checkForgejoCredentialBinding([]resolve.ResolvedProfile{{ID: "good", LocalPath: good}}, "", 0)
	assert.ErrorIs(t, err, errNoForgejoForgeHost)

	err = checkForgejoCredentialBinding([]resolve.ResolvedProfile{{ID: "gone", LocalPath: filepath.Join(dir, "missing.yaml")}}, "forgejo.example.test", 3443)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `reading profile "gone"`)
}

func TestRejectForgejoTokenUnderOtherKey(t *testing.T) {
	ok := []harness.ProviderDef{
		{Name: "forgejo", Credentials: map[string]string{"FORGEJO_TOKEN": "${FORGEJO_TOKEN}"}},
		{Name: "other", Credentials: map[string]string{"API_TOKEN": "${OTHER_TOKEN}"}},
	}
	require.NoError(t, rejectForgejoTokenUnderOtherKey(ok))

	// GITHUB_TOKEN carries the same run token on a Forgejo job.
	for _, value := range []string{"${GITHUB_TOKEN}", "$GITHUB_TOKEN", "Bearer ${GITHUB_TOKEN}"} {
		err := rejectForgejoTokenUnderOtherKey([]harness.ProviderDef{
			{Name: "sneaky", Credentials: map[string]string{"API_TOKEN": value}},
		})
		require.ErrorIs(t, err, errForgejoTokenUnderOtherKey, value)
		assert.Contains(t, err.Error(), `provider "sneaky" credential "API_TOKEN"`)

		err = rejectForgejoTokenUnderOtherKey([]harness.ProviderDef{
			{Name: "sneaky", Credentials: map[string]string{"GITHUB_TOKEN": value}},
		})
		require.ErrorIs(t, err, errForgejoTokenUnderOtherKey, value)

		err = rejectForgejoTokenUnderOtherKey([]harness.ProviderDef{
			{Name: "sneaky", Credentials: map[string]string{"FORGEJO_TOKEN": "${FORGEJO_TOKEN}"}, Config: map[string]string{"api_key": value}},
		})
		require.ErrorIs(t, err, errForgejoTokenUnderOtherKey, value)
		assert.Contains(t, err.Error(), `provider "sneaky" config "api_key"`)
	}

	for _, value := range []string{"${FORGEJO_TOKEN}", "$FORGEJO_TOKEN", "Bearer ${FORGEJO_TOKEN}"} {
		err := rejectForgejoTokenUnderOtherKey([]harness.ProviderDef{
			{Name: "sneaky", Credentials: map[string]string{"API_TOKEN": value}},
		})
		require.ErrorIs(t, err, errForgejoTokenUnderOtherKey, value)
		assert.Contains(t, err.Error(), `provider "sneaky" credential "API_TOKEN"`)
	}

	// A config value is passed inline on the provider CLI's argv.
	for _, value := range []string{"${FORGEJO_TOKEN}", "$FORGEJO_TOKEN", "Bearer ${FORGEJO_TOKEN}"} {
		err := rejectForgejoTokenUnderOtherKey([]harness.ProviderDef{
			{Name: "sneaky", Credentials: map[string]string{"FORGEJO_TOKEN": "${FORGEJO_TOKEN}"}, Config: map[string]string{"api_key": value}},
		})
		require.ErrorIs(t, err, errForgejoTokenUnderOtherKey, value)
		assert.Contains(t, err.Error(), `provider "sneaky" config "api_key"`)
	}
	require.NoError(t, rejectForgejoTokenUnderOtherKey([]harness.ProviderDef{
		{Name: "plain", Credentials: map[string]string{"FORGEJO_TOKEN": "${FORGEJO_TOKEN}"}, Config: map[string]string{"base_url": "https://forgejo.example.test"}},
	}))
}

func TestRequireForgejoTokenProfileLoaded(t *testing.T) {
	loaded := []resolve.ResolvedProfile{{ID: "fullsend-forgejo-api"}}
	tokenCred := map[string]string{"FORGEJO_TOKEN": "${FORGEJO_TOKEN}"}

	require.NoError(t, requireForgejoTokenProfileLoaded([]harness.ProviderDef{
		{Name: "forgejo", Type: "fullsend-forgejo-api", Credentials: tokenCred},
		// No FORGEJO_TOKEN reference: its profile need not be loaded.
		{Name: "vertex", Type: "fullsend-vertex-ai", Credentials: map[string]string{"KEY": "${OTHER}"}},
	}, loaded))

	err := requireForgejoTokenProfileLoaded([]harness.ProviderDef{
		{Name: "bare", Type: "fullsend-preinstalled", Credentials: tokenCred},
	}, loaded)
	require.ErrorIs(t, err, errForgejoTokenProfileNotLoaded)
	assert.Contains(t, err.Error(), `provider "bare" type "fullsend-preinstalled"`)

	// The FORGEJO_TOKEN key fed from GITHUB_TOKEN (the same run token) needs
	// a loaded profile too.
	err = requireForgejoTokenProfileLoaded([]harness.ProviderDef{
		{Name: "bare", Type: "fullsend-preinstalled", Credentials: map[string]string{"FORGEJO_TOKEN": "${GITHUB_TOKEN}"}},
	}, loaded)
	require.ErrorIs(t, err, errForgejoTokenProfileNotLoaded)
}
