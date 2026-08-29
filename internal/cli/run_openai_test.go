package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/inference/openaiwif"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func openAITestEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func stubOpenAIExchange(t *testing.T, fn func(ctx context.Context, cfg openaiwif.Config) (*openaiwif.Token, error)) {
	t.Helper()
	orig := openAIExchange
	openAIExchange = fn
	t.Cleanup(func() { openAIExchange = orig })
}

func TestResolveOpenAICredential_WIF(t *testing.T) {
	var got openaiwif.Config
	expires := time.Now().Add(59 * time.Minute)
	stubOpenAIExchange(t, func(_ context.Context, cfg openaiwif.Config) (*openaiwif.Token, error) {
		got = cfg
		return &openaiwif.Token{Value: "opaque-token-$with$dollars", ExpiresAt: expires}, nil
	})

	cred, err := resolveOpenAICredential(context.Background(), openAITestEnv(map[string]string{
		"FULLSEND_OPENAI_AUDIENCE":             " fullsend://acme ",
		"FULLSEND_OPENAI_IDENTITY_PROVIDER_ID": "idp-1",
		"FULLSEND_OPENAI_SERVICE_ACCOUNT_ID":   "sa-1",
		"ACTIONS_ID_TOKEN_REQUEST_URL":         "https://oidc.example/token?api-version=2",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN":       "runner-token",
		// A static key alongside a full WIF trio must not win.
		"OPENAI_API_KEY": "sk-static",
	}), config.OpenAIWIFConfig{})
	require.NoError(t, err)
	assert.Equal(t, "opaque-token-$with$dollars", cred.value)
	assert.Equal(t, expires, cred.expiresAt)
	assert.Equal(t, "wif", cred.source)
	assert.Contains(t, cred.detail, "idp-1")
	assert.Contains(t, cred.detail, "sa-1")
	assert.NotContains(t, cred.detail, "opaque-token", "detail must never carry the token")

	assert.Equal(t, "fullsend://acme", got.Audience, "audience is trimmed")
	assert.Equal(t, "idp-1", got.IdentityProviderID)
	assert.Equal(t, "sa-1", got.ServiceAccountID)
	assert.Equal(t, "https://oidc.example/token?api-version=2", got.OIDCRequestURL)
	assert.Equal(t, "runner-token", got.OIDCRequestToken)
}

func TestResolveOpenAICredential_WIFExchangeError(t *testing.T) {
	stubOpenAIExchange(t, func(context.Context, openaiwif.Config) (*openaiwif.Token, error) {
		return nil, errors.New("token endpoint returned 401")
	})
	_, err := resolveOpenAICredential(context.Background(), openAITestEnv(map[string]string{
		"FULLSEND_OPENAI_AUDIENCE":             "aud",
		"FULLSEND_OPENAI_IDENTITY_PROVIDER_ID": "idp",
		"FULLSEND_OPENAI_SERVICE_ACCOUNT_ID":   "sa",
		"ACTIONS_ID_TOKEN_REQUEST_URL":         "https://oidc.example/token",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN":       "runner-token",
	}), config.OpenAIWIFConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OpenAI WIF exchange failed")
	assert.Contains(t, err.Error(), "401")
}

func TestResolveOpenAICredential_PartialWIFIsAnError(t *testing.T) {
	stubOpenAIExchange(t, func(context.Context, openaiwif.Config) (*openaiwif.Token, error) {
		t.Fatal("exchange must not be attempted with a partial configuration")
		return nil, nil
	})
	_, err := resolveOpenAICredential(context.Background(), openAITestEnv(map[string]string{
		"FULLSEND_OPENAI_AUDIENCE": "aud",
		"OPENAI_API_KEY":           "sk-static",
	}), config.OpenAIWIFConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "partially configured")
	assert.Contains(t, err.Error(), "FULLSEND_OPENAI_IDENTITY_PROVIDER_ID")
	assert.Contains(t, err.Error(), "FULLSEND_OPENAI_SERVICE_ACCOUNT_ID")
	assert.NotContains(t, err.Error(), "FULLSEND_OPENAI_AUDIENCE, ", "only the missing variables are listed")
}

func TestResolveOpenAICredential_WIFWithoutOIDCEndpoint(t *testing.T) {
	stubOpenAIExchange(t, func(context.Context, openaiwif.Config) (*openaiwif.Token, error) {
		t.Fatal("exchange must not be attempted without the OIDC endpoint")
		return nil, nil
	})
	_, err := resolveOpenAICredential(context.Background(), openAITestEnv(map[string]string{
		"FULLSEND_OPENAI_AUDIENCE":             "aud",
		"FULLSEND_OPENAI_IDENTITY_PROVIDER_ID": "idp",
		"FULLSEND_OPENAI_SERVICE_ACCOUNT_ID":   "sa",
	}), config.OpenAIWIFConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ACTIONS_ID_TOKEN_REQUEST_URL")
	assert.Contains(t, err.Error(), "id-token: write")
}

func TestResolveOpenAICredential_Static(t *testing.T) {
	cred, err := resolveOpenAICredential(context.Background(), openAITestEnv(map[string]string{
		"OPENAI_API_KEY": "sk-local-dev",
	}), config.OpenAIWIFConfig{})
	require.NoError(t, err)
	assert.Equal(t, "sk-local-dev", cred.value)
	assert.True(t, cred.expiresAt.IsZero(), "a static key has no expiry")
	assert.Equal(t, "static", cred.source)
	assert.NotContains(t, cred.detail, "sk-local-dev")
}

func TestResolveOpenAICredential_NothingConfigured(t *testing.T) {
	_, err := resolveOpenAICredential(context.Background(), openAITestEnv(nil), config.OpenAIWIFConfig{})
	require.Error(t, err)
	for _, v := range []string{
		"FULLSEND_OPENAI_AUDIENCE",
		"FULLSEND_OPENAI_IDENTITY_PROVIDER_ID",
		"FULLSEND_OPENAI_SERVICE_ACCOUNT_ID",
		"OPENAI_API_KEY",
	} {
		assert.Contains(t, err.Error(), v)
	}
}

func TestRunScopedProviderName(t *testing.T) {
	assert.Equal(t, "openai-0123456789ab", runScopedProviderName("openai", "fs-tri-0123456789abcdef0123"))
	assert.Equal(t, "openai-abc", runScopedProviderName("openai", "abc"), "short names are used whole")
	a := runScopedProviderName("openai", generateSandboxName("triage"))
	b := runScopedProviderName("openai", generateSandboxName("triage"))
	assert.NotEqual(t, a, b, "two runs get distinct instances")
	assert.True(t, strings.HasPrefix(a, "openai-"))
}

func TestApplyRunScopedProviderNames(t *testing.T) {
	names := []string{"github", "openai", "vertex-ai"}
	assert.Equal(t, names, applyRunScopedProviderNames(names, nil), "no mapping is a no-op")
	got := applyRunScopedProviderNames(names, map[string]string{"openai": "openai-abc123"})
	assert.Equal(t, []string{"github", "openai-abc123", "vertex-ai"}, got, "order is preserved")
	assert.Equal(t, []string{"github", "openai", "vertex-ai"}, names, "input is not mutated")
}

// fakeOpenshellRecorder installs an openshell stub on PATH that appends every
// invocation's arguments to argsLog and the OPENAI_API_KEY it saw in its
// environment to envLog.
//
// failOn, when non-empty, makes any invocation whose arguments contain that
// substring exit 1 (after logging), so a single step can be made to fail.
func fakeOpenshellRecorder(t *testing.T, failOn ...string) (argsLog, envLog string) {
	t.Helper()
	binDir := t.TempDir()
	argsLog = filepath.Join(binDir, "args.log")
	envLog = filepath.Join(binDir, "env.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuoteForTest(argsLog) + "\n" +
		"printf '%s\\n' \"${OPENAI_API_KEY-<unset>}\" >> " + shellQuoteForTest(envLog) + "\n"
	for _, f := range failOn {
		script += "case \"$*\" in *" + shellQuoteForTest(f) + "*) echo 'stub: forced failure: provider.credentials must not be empty' >&2; exit 1 ;; esac\n"
	}
	script += "exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsLog, envLog
}

func shellQuoteForTest(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func TestEnsureOpenAIProvider_WIF(t *testing.T) {
	argsLog, envLog := fakeOpenshellRecorder(t)
	expires := time.Date(2026, 8, 27, 20, 0, 0, 0, time.UTC)
	stubOpenAIExchange(t, func(context.Context, openaiwif.Config) (*openaiwif.Token, error) {
		return &openaiwif.Token{Value: "tok-$literal$-value-9f8e7d", ExpiresAt: expires}, nil
	})
	t.Setenv("FULLSEND_OPENAI_AUDIENCE", "aud")
	t.Setenv("FULLSEND_OPENAI_IDENTITY_PROVIDER_ID", "idp")
	t.Setenv("FULLSEND_OPENAI_SERVICE_ACCOUNT_ID", "sa")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://oidc.example/token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "runner-token")
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("OPENAI_API_KEY", "ambient-key-must-not-be-used")

	pd := harness.ProviderDef{Name: "openai", Type: openAIProviderType, Credentials: map[string]string{"OPENAI_API_KEY": ""}}
	h, err := ensureOpenAIProvider(context.Background(), pd, "fs-tri-0123456789abcdef", config.OpenAIWIFConfig{}, ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, "openai-0123456789ab", h.name)
	assert.Equal(t, []string{"OPENAI_API_KEY"}, h.keys)
	assert.Equal(t, "wif", h.source)
	assert.Equal(t, expires, h.expiresAt)

	args, err := os.ReadFile(argsLog)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	require.Len(t, lines, 2, "empty create, then value and expiry together: %q", lines)
	assert.Equal(t, "provider create --name openai-0123456789ab --type fullsend-openai --credential OPENAI_API_KEY=", lines[0], "the instance is created without a credential")
	assert.Equal(t, "provider update openai-0123456789ab --credential OPENAI_API_KEY --credential-expires-at OPENAI_API_KEY=2026-08-27T20:00:00Z", lines[1], "bare-key form with the expiry in the same call")

	env, err := os.ReadFile(envLog)
	require.NoError(t, err)
	envLines := strings.Split(strings.TrimSpace(string(env)), "\n")
	assert.NotEqual(t, "tok-$literal$-value-9f8e7d", envLines[0], "the token is not handed to the create call")
	assert.Equal(t, "tok-$literal$-value-9f8e7d", envLines[1], "the value reaches the update child verbatim, `$` intact")

	res := security.NewSecretRedactor().Scan("log line with tok-$literal$-value-9f8e7d inside")
	assert.False(t, res.Safe)
	assert.NotContains(t, res.Sanitized, "9f8e7d", "the token is registered for exact-value redaction")
}

func TestEnsureOpenAIProvider_StaticGetsBoundedExpiry(t *testing.T) {
	argsLog, _ := fakeOpenshellRecorder(t)
	for _, k := range []string{"FULLSEND_OPENAI_AUDIENCE", "FULLSEND_OPENAI_IDENTITY_PROVIDER_ID", "FULLSEND_OPENAI_SERVICE_ACCOUNT_ID"} {
		t.Setenv(k, "")
	}
	t.Setenv("OPENAI_API_KEY", "sk-local-static-key")
	t.Setenv("GITHUB_ACTIONS", "")

	// No credential keys declared: the default key is used.
	pd := harness.ProviderDef{Name: "openai", Type: openAIProviderType}
	h, err := ensureOpenAIProvider(context.Background(), pd, "fs-cod-feedface", config.OpenAIWIFConfig{}, ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, "openai-feedface", h.name)
	assert.Equal(t, []string{"OPENAI_API_KEY"}, h.keys, "the default key when the definition declares none")
	assert.Equal(t, "static", h.source)
	assert.WithinDuration(t, time.Now().Add(openAIStaticKeyLifetime), h.expiresAt, 2*time.Minute)

	args, err := os.ReadFile(argsLog)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	require.Len(t, lines, 2, "empty create, then the key with a bounded expiry: %q", lines)
	assert.Equal(t, "provider create --name openai-feedface --type fullsend-openai --credential OPENAI_API_KEY=", lines[0])
	require.Regexp(t, `^provider update openai-feedface --credential OPENAI_API_KEY --credential-expires-at OPENAI_API_KEY=\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`, lines[1])
	stamp := strings.TrimPrefix(strings.Fields(lines[1])[6], "OPENAI_API_KEY=")
	at, err := time.Parse(time.RFC3339, stamp)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(openAIStaticKeyLifetime), at, 2*time.Minute, "static keys are bounded to the configured lifetime")
}

func TestEnsureOpenAIProvider_StoreFailureDeletesProvider(t *testing.T) {
	argsLog, _ := fakeOpenshellRecorder(t, "--credential-expires-at")
	for _, k := range []string{"FULLSEND_OPENAI_AUDIENCE", "FULLSEND_OPENAI_IDENTITY_PROVIDER_ID", "FULLSEND_OPENAI_SERVICE_ACCOUNT_ID"} {
		t.Setenv(k, "")
	}
	t.Setenv("OPENAI_API_KEY", "sk-local-static-key")
	t.Setenv("GITHUB_ACTIONS", "")

	pd := harness.ProviderDef{Name: "openai", Type: openAIProviderType}
	_, err := ensureOpenAIProvider(context.Background(), pd, "fs-cod-feedface", config.OpenAIWIFConfig{}, ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storing credential")

	args, readErr := os.ReadFile(argsLog)
	require.NoError(t, readErr)
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	require.Len(t, lines, 3, "empty create, failed store, delete: %q", lines)
	assert.True(t, strings.HasPrefix(lines[1], "provider update openai-feedface --credential OPENAI_API_KEY --credential-expires-at"))
	assert.Equal(t, "provider delete openai-feedface", lines[2], "a provider whose credential could not be stored with its expiry is not left behind")
}

func TestEnsureOpenAIProvider_StoreAndDeleteFailureBlanksCredential(t *testing.T) {
	argsLog, _ := fakeOpenshellRecorder(t, "--credential-expires-at", "provider delete")
	for _, k := range []string{"FULLSEND_OPENAI_AUDIENCE", "FULLSEND_OPENAI_IDENTITY_PROVIDER_ID", "FULLSEND_OPENAI_SERVICE_ACCOUNT_ID"} {
		t.Setenv(k, "")
	}
	t.Setenv("OPENAI_API_KEY", "sk-local-static-key")
	t.Setenv("GITHUB_ACTIONS", "")
	var buf strings.Builder
	_, err := ensureOpenAIProvider(context.Background(), harness.ProviderDef{Name: "openai", Type: openAIProviderType}, "fs-cod-feedface", config.OpenAIWIFConfig{}, ui.New(&buf))
	require.Error(t, err)
	lines := readArgLines(t, argsLog)
	require.Len(t, lines, 4, "empty create, failed store, failed delete, blank: %q", lines)
	assert.Equal(t, "provider update openai-feedface --credential OPENAI_API_KEY=", lines[3], "the credential is blanked when the delete fails too")
	assert.Contains(t, buf.String(), "blanked")
}

func TestEnsureOpenAIProvider_NoCredentialFailsBeforeOpenshell(t *testing.T) {
	argsLog, _ := fakeOpenshellRecorder(t)
	for _, k := range []string{"FULLSEND_OPENAI_AUDIENCE", "FULLSEND_OPENAI_IDENTITY_PROVIDER_ID", "FULLSEND_OPENAI_SERVICE_ACCOUNT_ID", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	pd := harness.ProviderDef{Name: "openai", Type: openAIProviderType}
	_, err := ensureOpenAIProvider(context.Background(), pd, "fs-cod-feedface", config.OpenAIWIFConfig{}, ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `provider "openai"`)
	assert.Contains(t, err.Error(), "no OpenAI credential")
	_, statErr := os.Stat(argsLog)
	assert.True(t, os.IsNotExist(statErr), "openshell must not be invoked without a credential")
}

// recordingProvidersStub installs an openshell stub that behaves like
// testdata/providers-stub (passes the gateway check and every
// provider/profile/sandbox command) and additionally appends each
// invocation's arguments to the returned log file.
func recordingProvidersStub(t *testing.T) string {
	t.Helper()
	neutralizeAgentsRepoFallback(t)
	// ImportProfile keeps a per-id content cache under os.TempDir(); give
	// each test its own so an earlier import cannot short-circuit this one.
	t.Setenv("TMPDIR", t.TempDir())
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuoteForTest(logPath) + "\n" +
		"case \"$1 $2\" in\n" +
		"  'gateway list') echo default-gateway; exit 0 ;;\n" +
		"  'settings '*) exit 0 ;;\n" +
		"  'provider list-profiles') echo '    fullsend-openai  Fullsend OpenAI  endpoints: 1'; exit 0 ;;\n" +
		"  'provider profile'|'provider create'|'provider update') exit 0 ;;\n" +
		// Like OpenShell 0.0.83: a provider cannot be deleted while a sandbox
		// still references it, so track the sandbox in a marker file.
		"  'provider delete') if [ -e " + shellQuoteForTest(logPath+".sandbox") + " ]; then echo \"error: provider '$3' is attached to sandbox(es): fs-x\" >&2; exit 1; fi; exit 0 ;;\n" +
		"  'sandbox create') : > " + shellQuoteForTest(logPath+".sandbox") + "; exit 0 ;;\n" +
		"  'sandbox delete') rm -f " + shellQuoteForTest(logPath+".sandbox") + "; exit 0 ;;\n" +
		"  'sandbox ready') exit 0 ;;\n" +
		"  'sandbox get') echo 'Status: Ready'; exit 0 ;;\n" +
		"  'policy get') printf 'Version: 1\\nStatus: Effective\\n---\\nversion: 1\\nnetwork_policies:\\n  _provider_openai:\\n    endpoints:\\n    - host: api.openai.com\\n      port: 443\\n      protocol: rest\\n'; exit 0 ;;\n" +
		// The first in-sandbox command fails so the run stops right after
		// sandbox creation and the deferred cleanup runs.
		"  'sandbox exec') echo 'stub: no sandbox' >&2; exit 1 ;;\n" +
		"esac\n" +
		"echo \"openshell stub: unhandled: $*\" >&2; exit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// writeOpenAIFullsendDir lays out a fullsend dir whose code harness declares
// the openai provider — by bare name, or by file path the way the
// behaviour scenarios do — with the scaffold's provider and profile files.
func writeOpenAIFullsendDir(t *testing.T, pathForm bool) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"harness", "agents", "providers", "profiles"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, d), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "code.md"), []byte("You are a coding agent."), 0o644))
	harnessYAML := "agent: agents/code.md\nrole: test\nproviders:\n  - openai\n"
	if pathForm {
		harnessYAML = "agent: agents/code.md\nrole: test\nprofiles:\n  - profiles/fullsend-openai.yaml\nproviders:\n  - providers/openai.yaml\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"), []byte(harnessYAML), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "providers", "openai.yaml"),
		[]byte("name: openai\ntype: fullsend-openai\ncredentials:\n  OPENAI_API_KEY: \"\"\n"), 0o644))
	_ = pathForm // the profile is never shipped by the workspace: the runner embeds it
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("agents:\n  - harness/code.yaml\n"), 0o644))
	return dir
}

func TestRunAgent_OpenAIProviderIsRunScopedAndDeleted(t *testing.T) {
	cases := []struct {
		name     string
		keep     bool
		pathForm bool
		noDef    bool
	}{
		{"keepSandbox=false", false, false, false},
		{"keepSandbox=true", true, false, false},
		{"path-form provider entry", false, true, false},
		{"bare name, no providers/openai.yaml on disk (embedded definition)", false, false, true},
	}
	t.Run("a workspace profile with the reserved id is refused", func(t *testing.T) {
		recordingProvidersStub(t)
		for _, k := range []string{"FULLSEND_OPENAI_AUDIENCE", "FULLSEND_OPENAI_IDENTITY_PROVIDER_ID", "FULLSEND_OPENAI_SERVICE_ACCOUNT_ID", "GITHUB_ACTIONS"} {
			t.Setenv(k, "")
		}
		t.Setenv("OPENAI_API_KEY", "sk-local-static-key-for-test")
		dir := writeOpenAIFullsendDir(t, false)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "profiles", "fullsend-openai.yaml"), []byte("id: fullsend-openai\ndisplay_name: Not the real one\n"), 0o644))
		err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", resolveFlags{maxDepth: 10, maxResources: 50}, statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reserved for the copy built into fullsend")
	})
	for _, tc := range cases {
		keep := tc.keep
		t.Run(tc.name, func(t *testing.T) {
			logPath := recordingProvidersStub(t)
			for _, k := range []string{"FULLSEND_OPENAI_AUDIENCE", "FULLSEND_OPENAI_IDENTITY_PROVIDER_ID", "FULLSEND_OPENAI_SERVICE_ACCOUNT_ID", "GITHUB_ACTIONS"} {
				t.Setenv(k, "")
			}
			t.Setenv("OPENAI_API_KEY", "sk-local-static-key-for-test")
			dir := writeOpenAIFullsendDir(t, tc.pathForm)
			if tc.noDef {
				require.NoError(t, os.Remove(filepath.Join(dir, "providers", "openai.yaml")))
			}

			rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
			err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags, statusOpts{}, ui.New(io.Discard), keep, runOverrideFlags{})
			// The run fails after sandbox creation (the stub cannot bootstrap
			// an agent), but the provider block must have completed.
			require.Error(t, err)
			t.Logf("runAgent returned (expected to fail after the provider block): %v", err)
			assert.NotContains(t, err.Error(), "ensuring provider")
			assert.NotContains(t, err.Error(), "no OpenAI credential")

			data, readErr := os.ReadFile(logPath)
			require.NoError(t, readErr)
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")

			var createLine, sandboxLine, deleteLine, sandboxDelete, expireLine string
			deleteIdx, sandboxDeleteIdx := -1, -1
			for i, l := range lines {
				switch {
				case strings.HasPrefix(l, "provider create "):
					createLine = l
				case strings.HasPrefix(l, "sandbox create "):
					sandboxLine = l
				case strings.HasPrefix(l, "provider delete "):
					deleteLine, deleteIdx = l, i
				case strings.HasPrefix(l, "sandbox delete "):
					sandboxDelete, sandboxDeleteIdx = l, i
				case strings.HasPrefix(l, "provider update ") && strings.Contains(l, " --credential-expires-at") && !strings.Contains(l, " --credential OPENAI_API_KEY "):
					expireLine = l
				}
			}
			var profileLine string
			for _, l := range lines {
				if strings.HasPrefix(l, "provider profile import --file ") && strings.Contains(l, "fullsend-openai-") {
					profileLine = l
				}
			}
			require.NotEmpty(t, profileLine, "the runner imports the fullsend-openai profile from its embedded scaffold: %q", lines)
			require.NotEmpty(t, createLine, "provider created: %q", lines)
			assert.Regexp(t, `^provider create --name openai-[0-9a-f]{12} --type fullsend-openai --credential OPENAI_API_KEY=$`, createLine,
				"run-scoped name, created without a credential")
			assert.NotContains(t, strings.Join(lines, "\n"), "sk-local-static-key-for-test", "the value never reaches a command line")
			scoped := strings.Fields(createLine)[3]

			require.NotEmpty(t, sandboxLine, "sandbox created: %q", lines)
			assert.Contains(t, sandboxLine, "--provider "+scoped, "the sandbox attaches the run-scoped instance")
			assert.NotRegexp(t, `--provider openai( |$)`, sandboxLine, "the bare harness name is not attached")

			assert.Equal(t, "provider delete "+scoped, deleteLine, "deletion is attempted at run end")
			if keep {
				assert.Empty(t, sandboxDelete, "--keep-sandbox keeps the sandbox")
				// The gateway refuses the delete while the kept sandbox
				// references the provider, so the credential is expired in place.
				require.NotEmpty(t, expireLine, "expired in place: %q", lines)
				assert.True(t, strings.HasPrefix(expireLine, "provider update "+scoped+" --credential-expires-at OPENAI_API_KEY="), expireLine)
			} else {
				assert.NotEmpty(t, sandboxDelete)
				assert.Less(t, sandboxDeleteIdx, deleteIdx, "the sandbox is deleted before the provider, so the delete succeeds")
			}
		})
	}
}

// profileListingStub installs an openshell stub whose `provider
// list-profiles` answer is the given text and which logs every call.
func profileListingStub(t *testing.T, listing string) string {
	t.Helper()
	binDir := t.TempDir()
	argsLog := filepath.Join(binDir, "args.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuoteForTest(argsLog) + "\n" +
		"case \"$1 $2\" in 'provider list-profiles') printf '%s\\n' " + shellQuoteForTest(listing) + "; exit 0 ;; esac\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsLog
}

func TestEnsureOpenAIProfile(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	argsLog := profileListingStub(t, "Available Provider Profiles:\n    fullsend-openai  Fullsend OpenAI  endpoints: 1\n    nvidia  NVIDIA  endpoints: 1  inference")
	require.NoError(t, ensureOpenAIProfile(context.Background(), "fullsend-openai", ui.New(io.Discard)))
	lines := readArgLines(t, argsLog)
	require.Len(t, lines, 3, "delete, import, then confirm the listing: %q", lines)
	assert.Equal(t, "provider profile delete fullsend-openai", lines[0])
	assert.Regexp(t, `^provider profile import --file \S+fullsend-openai-\S+\.yaml$`, lines[1], "imported from a temp copy of the embedded profile")
	assert.Equal(t, "provider list-profiles -o json", lines[2])

	// Second call: the content cache is deliberately not trusted, so the
	// embedded profile is sent again and the gateway asked again.
	require.NoError(t, ensureOpenAIProfile(context.Background(), "fullsend-openai", ui.New(io.Discard)))
	lines = readArgLines(t, argsLog)
	require.Len(t, lines, 6, "%q", lines)
	assert.Equal(t, "provider profile delete fullsend-openai", lines[3])
	assert.Equal(t, "provider list-profiles -o json", lines[5])

	err := ensureOpenAIProfile(context.Background(), "no-such-profile", ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not shipped by this fullsend build")
}

func TestEnsureOpenAIProfile_StaleCacheReimports(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	// A cache that says "imported" against a gateway that does not list it.
	argsLog := profileListingStub(t, "Available Provider Profiles:\n    nvidia  NVIDIA  endpoints: 1  inference")
	err := ensureOpenAIProfile(context.Background(), "fullsend-openai", ui.New(io.Discard))
	require.Error(t, err, "the stub never lists it, so the re-import cannot be confirmed either")
	assert.Contains(t, err.Error(), "not on the gateway after import")
	lines := readArgLines(t, argsLog)
	// import, list (missing), forget cache + delete + import again, list.
	require.Len(t, lines, 6, "%q", lines)
	assert.Equal(t, "provider list-profiles -o json", lines[2])
	assert.Equal(t, "provider profile delete fullsend-openai", lines[3], "the cache was dropped, so the import ran again")
	assert.Regexp(t, `^provider profile import --file `, lines[4])
	assert.Equal(t, "provider list-profiles -o json", lines[5])
}

func TestReservedSandboxKeys_IncludesOpenAIKey(t *testing.T) {
	assert.True(t, reservedSandboxKeys["OPENAI_API_KEY"], "env.sandbox must not be able to shadow the provider placeholder")
}

func TestCleanupRunScopedProvider_ExpiresInPlaceWhenDeleteFails(t *testing.T) {
	argsLog, _ := fakeOpenshellRecorder(t, "provider delete")
	cleanupRunScopedProvider("openai-feedface", []string{"OPENAI_API_KEY"}, true, ui.New(io.Discard))

	lines := readArgLines(t, argsLog)
	require.Len(t, lines, 2, "delete attempted, then expired in place: %q", lines)
	assert.Equal(t, "provider delete openai-feedface", lines[0])
	require.Regexp(t, `^provider update openai-feedface --credential-expires-at OPENAI_API_KEY=\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`, lines[1])
	stamp := strings.TrimPrefix(strings.Fields(lines[1])[4], "OPENAI_API_KEY=")
	at, err := time.Parse(time.RFC3339, stamp)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), at, time.Minute, "expired now, so a kept sandbox fails closed")
}

func TestCleanupRunScopedProvider_AlreadyGone(t *testing.T) {
	binDir := t.TempDir()
	argsLog := filepath.Join(binDir, "args.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuoteForTest(argsLog) + "\necho '! Provider openai-feedface not found'; exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var buf strings.Builder
	cleanupRunScopedProvider("openai-feedface", []string{"OPENAI_API_KEY"}, false, ui.New(&buf))
	assert.Equal(t, []string{"provider delete openai-feedface"}, readArgLines(t, argsLog), "no expiry call for a provider that is already gone")
	assert.Contains(t, buf.String(), "already gone")
}

func TestCleanupRunScopedProvider_Deleted(t *testing.T) {
	argsLog, _ := fakeOpenshellRecorder(t)
	cleanupRunScopedProvider("openai-feedface", []string{"OPENAI_API_KEY"}, false, ui.New(io.Discard))
	assert.Equal(t, []string{"provider delete openai-feedface"}, readArgLines(t, argsLog), "no expiry call when the delete succeeds")
}

func readArgLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestOpenAIRefreshDelay(t *testing.T) {
	now := time.Date(2026, 8, 27, 20, 0, 0, 0, time.UTC)
	assert.Equal(t, 54*time.Minute, openAIRefreshDelay(now.Add(time.Hour), now, time.Minute), "margin and jitter before expiry")
	assert.Equal(t, 150*time.Second, openAIRefreshDelay(now.Add(5*time.Minute), now, 0), "a token shorter than twice the margin refreshes at half its life")
	assert.Equal(t, 5*time.Minute, openAIRefreshDelay(now.Add(10*time.Minute), now, 0), "the margin is capped at half the remaining lifetime")
	assert.Equal(t, openAIRefreshMinDelay, openAIRefreshDelay(now.Add(time.Minute), now, 0), "never below the minimum")
	assert.Equal(t, openAIRefreshMinDelay, openAIRefreshDelay(now.Add(-time.Hour), now, 0), "already expired still waits the minimum")
}

func TestRefreshOpenAIProvider_WIF(t *testing.T) {
	argsLog, envLog := fakeOpenshellRecorder(t)
	expires := time.Date(2026, 8, 27, 21, 0, 0, 0, time.UTC)
	stubOpenAIExchange(t, func(context.Context, openaiwif.Config) (*openaiwif.Token, error) {
		return &openaiwif.Token{Value: "tok-refreshed-$2-a1b2c3d4", ExpiresAt: expires}, nil
	})
	t.Setenv("FULLSEND_OPENAI_AUDIENCE", "aud")
	t.Setenv("FULLSEND_OPENAI_IDENTITY_PROVIDER_ID", "idp")
	t.Setenv("FULLSEND_OPENAI_SERVICE_ACCOUNT_ID", "sa")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://oidc.example/token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "runner-token")
	t.Setenv("GITHUB_ACTIONS", "")

	h := openAIProviderHandle{name: "openai-abc", keys: []string{"OPENAI_API_KEY"}, source: "wif", expiresAt: time.Now()}
	next, _, err := refreshOpenAIProvider(context.Background(), h, "", time.Minute, ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, expires, next)

	lines := readArgLines(t, argsLog)
	require.Len(t, lines, 1, "one update carries the credential and its expiry: %q", lines)
	assert.Equal(t, "provider update openai-abc --credential OPENAI_API_KEY --credential-expires-at OPENAI_API_KEY=2026-08-27T21:00:00Z", lines[0], "bare-key update, value not on the command line")
	env := readArgLines(t, envLog)
	assert.Equal(t, "tok-refreshed-$2-a1b2c3d4", env[0], "the new value reaches the child verbatim")
	res := security.NewSecretRedactor().Scan("x tok-refreshed-$2-a1b2c3d4 y")
	assert.False(t, res.Safe, "the refreshed token is registered for redaction")
}

func TestRefreshOpenAIProvider_StaticHeartbeat(t *testing.T) {
	argsLog, _ := fakeOpenshellRecorder(t)
	stubOpenAIExchange(t, func(context.Context, openaiwif.Config) (*openaiwif.Token, error) {
		t.Fatal("a static key is never exchanged")
		return nil, nil
	})
	h := openAIProviderHandle{name: "openai-abc", keys: []string{"OPENAI_API_KEY"}, source: "static", expiresAt: time.Now()}
	next, _, err := refreshOpenAIProvider(context.Background(), h, "", time.Minute, ui.New(io.Discard))
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(openAIStaticKeyLifetime), next, time.Minute)
	lines := readArgLines(t, argsLog)
	require.Len(t, lines, 1, "only the expiry moves for a static key: %q", lines)
	assert.True(t, strings.HasPrefix(lines[0], "provider update openai-abc --credential-expires-at OPENAI_API_KEY="))
}

// syncBuffer is a strings.Builder safe to read while the refresher writes.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestRunOpenAIRefresh_RefreshesThenStopsOnCancel(t *testing.T) {
	shrinkOpenAIRefreshSchedule(t)
	argsLog, _ := fakeOpenshellRecorder(t)
	var buf syncBuffer
	h := openAIProviderHandle{name: "openai-abc", keys: []string{"OPENAI_API_KEY"}, source: "static", expiresAt: time.Now()}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runOpenAIRefresh(ctx, h, ui.New(&buf))
	}()
	require.Eventually(t, func() bool {
		return strings.Contains(buf.String(), "OpenAI credential refreshed for openai-abc")
	}, 5*time.Second, 20*time.Millisecond, "a refresh fires once the (shrunk) delay elapses")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("refresher did not stop on cancel")
	}
	assert.Contains(t, strings.Join(readArgLines(t, argsLog), "\n"), "--credential-expires-at")
}

func TestRunOpenAIRefresh_GivesUpAfterRetries(t *testing.T) {
	shrinkOpenAIRefreshSchedule(t)
	argsLog, _ := fakeOpenshellRecorder(t, "--credential-expires-at")
	var buf strings.Builder
	h := openAIProviderHandle{name: "openai-abc", keys: []string{"OPENAI_API_KEY"}, source: "static", expiresAt: time.Now()}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runOpenAIRefresh(context.Background(), h, ui.New(&buf))
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("refresher did not give up")
	}
	lines := readArgLines(t, argsLog)
	assert.Len(t, lines, openAIRefreshRetries, "one attempt per retry: %q", lines)
	assert.Contains(t, buf.String(), "gave up")
	assert.Contains(t, buf.String(), "gave up")
}

// shrinkOpenAIRefreshSchedule makes the refresh loop fire within
// milliseconds for tests and restores the production values afterwards.
func shrinkOpenAIRefreshSchedule(t *testing.T) {
	t.Helper()
	margin, jitter, minDelay, backoff := openAIRefreshMargin, openAIRefreshJitter, openAIRefreshMinDelay, openAIRefreshBackoff
	openAIRefreshMargin, openAIRefreshJitter, openAIRefreshMinDelay, openAIRefreshBackoff = 0, 0, 20*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() {
		openAIRefreshMargin, openAIRefreshJitter, openAIRefreshMinDelay, openAIRefreshBackoff = margin, jitter, minDelay, backoff
	})
}

func TestCheckOpenAIScope(t *testing.T) {
	w, err := checkOpenAIScope("api.model.request")
	require.NoError(t, err)
	assert.Empty(t, w)
	w, err = checkOpenAIScope("api.model.read api.model.request")
	require.NoError(t, err)
	assert.Empty(t, w)
	w, err = checkOpenAIScope("")
	require.NoError(t, err)
	assert.Contains(t, w, "does not narrow permissions")
	_, err = checkOpenAIScope("api.model.request api.vector_store.read")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api.vector_store.read")
}

func TestResolveOpenAICredential_RefusesBroadScope(t *testing.T) {
	env := openAITestEnv(map[string]string{
		"FULLSEND_OPENAI_AUDIENCE":             "aud",
		"FULLSEND_OPENAI_IDENTITY_PROVIDER_ID": "idp",
		"FULLSEND_OPENAI_SERVICE_ACCOUNT_ID":   "sa",
		"ACTIONS_ID_TOKEN_REQUEST_URL":         "https://oidc.example/token",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN":       "runner-token",
	})
	stubOpenAIExchange(t, func(context.Context, openaiwif.Config) (*openaiwif.Token, error) {
		return &openaiwif.Token{Value: "tok", ExpiresAt: time.Now().Add(5 * time.Minute), Scope: "api.model.request api.vector_store.read"}, nil
	})
	_, err := resolveOpenAICredential(context.Background(), env, config.OpenAIWIFConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token refused")
	assert.Contains(t, err.Error(), "api.vector_store.read")

	stubOpenAIExchange(t, func(context.Context, openaiwif.Config) (*openaiwif.Token, error) {
		return &openaiwif.Token{Value: "tok", ExpiresAt: time.Now().Add(5 * time.Minute)}, nil
	})
	cred, err := resolveOpenAICredential(context.Background(), env, config.OpenAIWIFConfig{})
	require.NoError(t, err)
	require.Len(t, cred.warnings, 1, "an un-narrowed mapping is allowed with a warning")
	assert.Contains(t, cred.detail, "scope (not narrowed)")
}

func TestOpenAIKeyIsNotExpandable(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-local-static-key")
	assert.True(t, oidcDenyKeys["OPENAI_API_KEY"])
	assert.True(t, reservedSandboxKeys["OPENAI_API_KEY"], "merged by init()")
	assert.NotContains(t, safeExpandEnv("prefix-${OPENAI_API_KEY}-suffix"), "sk-local-static-key", "a harness ${OPENAI_API_KEY} expansion must not yield the key")
	assert.NotContains(t, shellSafeExpandEnv("prefix-${OPENAI_API_KEY}-suffix"), "sk-local-static-key")
}

func TestEnsureOpenAIProvider_FallsBackWhenEmptyCreateIsRefused(t *testing.T) {
	argsLog, envLog := fakeOpenshellRecorder(t, "--type fullsend-openai --credential OPENAI_API_KEY=")
	for _, k := range []string{"FULLSEND_OPENAI_AUDIENCE", "FULLSEND_OPENAI_IDENTITY_PROVIDER_ID", "FULLSEND_OPENAI_SERVICE_ACCOUNT_ID"} {
		t.Setenv(k, "")
	}
	t.Setenv("OPENAI_API_KEY", "sk-local-static-key")
	t.Setenv("GITHUB_ACTIONS", "")
	var buf strings.Builder
	h, err := ensureOpenAIProvider(context.Background(), harness.ProviderDef{Name: "openai", Type: openAIProviderType}, "fs-cod-feedface", config.OpenAIWIFConfig{}, ui.New(&buf))
	require.NoError(t, err)
	assert.Equal(t, "openai-feedface", h.name)
	lines := readArgLines(t, argsLog)
	// The refused empty create is not retried (non-transient); the fallback
	// creates with the value and the expiry follows immediately.
	require.GreaterOrEqual(t, len(lines), 3, "%q", lines)
	assert.Equal(t, "provider create --name openai-feedface --type fullsend-openai --credential OPENAI_API_KEY", lines[len(lines)-2], "fallback create carries the value by bare key")
	assert.True(t, strings.HasPrefix(lines[len(lines)-1], "provider update openai-feedface --credential OPENAI_API_KEY --credential-expires-at OPENAI_API_KEY="))
	env := readArgLines(t, envLog)
	assert.Equal(t, "sk-local-static-key", env[len(env)-2], "the value reaches the fallback create child")
	assert.Contains(t, buf.String(), "refused an empty credential on create")
}

func TestEnsureOpenAIProvider_IgnoresExtraCredentialKeys(t *testing.T) {
	argsLog, _ := fakeOpenshellRecorder(t)
	for _, k := range []string{"FULLSEND_OPENAI_AUDIENCE", "FULLSEND_OPENAI_IDENTITY_PROVIDER_ID", "FULLSEND_OPENAI_SERVICE_ACCOUNT_ID"} {
		t.Setenv(k, "")
	}
	t.Setenv("OPENAI_API_KEY", "sk-local-static-key")
	t.Setenv("GITHUB_ACTIONS", "")
	var buf strings.Builder
	pd := harness.ProviderDef{Name: "openai", Type: openAIProviderType, Credentials: map[string]string{"OPENAI_API_KEY": "", "LD_PRELOAD": ""}}
	h, err := ensureOpenAIProvider(context.Background(), pd, "fs-cod-feedface", config.OpenAIWIFConfig{}, ui.New(&buf))
	require.NoError(t, err)
	assert.Equal(t, []string{"OPENAI_API_KEY"}, h.keys, "the token is never copied under another key")
	assert.Contains(t, buf.String(), "LD_PRELOAD")
	for _, l := range readArgLines(t, argsLog) {
		assert.NotContains(t, l, "LD_PRELOAD")
	}
}

func TestCheckProviderProfileIntegrity_KnowsEmbeddedOpenAIProfile(t *testing.T) {
	providers := []resolve.ResolvedProvider{{Def: harness.ProviderDef{Name: "openai", Type: openAIProviderType}}}
	w, err := checkProviderProfileIntegrity(providers, nil, []string{"fullsend-github"})
	require.NoError(t, err, "the runner imports fullsend-openai itself, so a path-form provider needs no profiles: entry")
	assert.Empty(t, w)
	_, err = checkProviderProfileIntegrity([]resolve.ResolvedProvider{{Def: harness.ProviderDef{Name: "x", Type: "no-such-profile"}}}, nil, []string{"fullsend-github"})
	require.Error(t, err)
}

func TestAppendEmbeddedProviderDefs(t *testing.T) {
	defs := appendEmbeddedProviderDefs(nil, nil, []string{"openai", "vertex-ai", "no-such-provider", "https://x/p.yaml"}, ui.New(io.Discard))
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name+":"+d.Type)
	}
	assert.Equal(t, []string{"openai:fullsend-openai"}, names, "only the scaffold-shipped OpenAI definition is filled in; other bare names keep their warning")
	local := []harness.ProviderDef{{Name: "openai", Type: "custom-type"}}
	defs = appendEmbeddedProviderDefs(local, nil, []string{"openai"}, ui.New(io.Discard))
	require.Len(t, defs, 1)
	assert.Equal(t, "custom-type", defs[0].Type, "a local definition is never replaced")
}

func TestRejectReservedProfileID(t *testing.T) {
	require.NoError(t, rejectReservedProfileID(openAIProviderType, nil, []string{"fullsend-github"}))
	err := rejectReservedProfileID(openAIProviderType, nil, []string{"fullsend-openai"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reserved")
	err = rejectReservedProfileID(openAIProviderType, []resolve.ResolvedProfile{{ID: "fullsend-openai"}}, nil)
	require.Error(t, err)
}

func TestEnsureOpenAIProvider_RefusesUnredactableCredential(t *testing.T) {
	argsLog, _ := fakeOpenshellRecorder(t)
	for _, k := range []string{"FULLSEND_OPENAI_AUDIENCE", "FULLSEND_OPENAI_IDENTITY_PROVIDER_ID", "FULLSEND_OPENAI_SERVICE_ACCOUNT_ID"} {
		t.Setenv(k, "")
	}
	t.Setenv("OPENAI_API_KEY", "short")
	t.Setenv("GITHUB_ACTIONS", "")
	_, err := ensureOpenAIProvider(context.Background(), harness.ProviderDef{Name: "openai", Type: openAIProviderType}, "fs-cod-feedface", config.OpenAIWIFConfig{}, ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too short to redact")
	_, statErr := os.Stat(argsLog)
	assert.True(t, os.IsNotExist(statErr), "nothing reaches openshell")
}

func TestRunOpenAIRefresh_WIFSourceReexchanges(t *testing.T) {
	shrinkOpenAIRefreshSchedule(t)
	argsLog, envLog := fakeOpenshellRecorder(t)
	t.Setenv("FULLSEND_OPENAI_AUDIENCE", "aud")
	t.Setenv("FULLSEND_OPENAI_IDENTITY_PROVIDER_ID", "idp")
	t.Setenv("FULLSEND_OPENAI_SERVICE_ACCOUNT_ID", "sa")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://oidc.example/token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "runner-token")
	t.Setenv("GITHUB_ACTIONS", "")
	var calls int32
	stubOpenAIExchange(t, func(context.Context, openaiwif.Config) (*openaiwif.Token, error) {
		n := atomic.AddInt32(&calls, 1)
		return &openaiwif.Token{Value: fmt.Sprintf("tok-refreshed-%d-abcdef", n), ExpiresAt: time.Now().Add(time.Hour), Scope: "api.model.request"}, nil
	})
	var buf syncBuffer
	h := openAIProviderHandle{name: "openai-abc", keys: []string{"OPENAI_API_KEY"}, source: "wif", expiresAt: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runOpenAIRefresh(ctx, h, ui.New(&buf))
	}()
	require.Eventually(t, func() bool { return strings.Contains(buf.String(), "refreshed for openai-abc (wif") }, 5*time.Second, 20*time.Millisecond)
	cancel()
	<-done
	assert.GreaterOrEqual(t, atomic.LoadInt32(&calls), int32(1), "a fresh exchange per refresh")
	lines := readArgLines(t, argsLog)
	assert.True(t, strings.HasPrefix(lines[0], "provider update openai-abc --credential OPENAI_API_KEY --credential-expires-at OPENAI_API_KEY="), lines[0])
	assert.Equal(t, "tok-refreshed-1-abcdef", readArgLines(t, envLog)[0], "the re-exchanged token reaches the update child")
}

func TestUninspectedEndpointRules(t *testing.T) {
	policy := []byte(`version: 1
network_policies:
  _provider_openai_abc:
    endpoints:
    - host: api.openai.com
      port: 443
      protocol: rest
      rules:
      - allow: {method: POST, path: /v1/responses}
  codex:
    endpoints:
    - host: api.openai.com
      port: 443
    - host: chatgpt.com
      port: 443
  wildcard_skip:
    endpoints:
    - host: "*.openai.com"
      ports: [443, 8443]
      protocol: rest
      tls: skip
  opted_in:
    endpoints:
    - host: api.openai.com
      port: 443
      allow_uninspected_credentials: true
  tcp_route:
    endpoints:
    - host: api.openai.com
      port: 443
      protocol: tcp
  no_port:
    endpoints:
    - host: api.openai.com
  other_port:
    endpoints:
    - host: api.openai.com
      port: 80
  unrelated:
    endpoints:
    - host: api.anthropic.com
      port: 443
`)
	rules, err := uninspectedEndpointRules(policy, "api.openai.com", 443)
	require.NoError(t, err)
	assert.Equal(t, []string{"codex", "no_port", "opted_in", "tcp_route", "wildcard_skip"}, rules,
		"L4 (no protocol or tcp), tls: skip, a host-only rule (every port) and an allow_uninspected_credentials opt-in all leave the credential uninjected")

	rules, err = uninspectedEndpointRules([]byte("version: 1\n"), "api.openai.com", 443)
	require.NoError(t, err)
	assert.Empty(t, rules)

	_, err = uninspectedEndpointRules([]byte("network_policies: [not a map"), "api.openai.com", 443)
	require.Error(t, err)
}

func TestPolicyHostMatches(t *testing.T) {
	for _, tc := range []struct {
		pattern, host string
		want          bool
	}{
		{"api.openai.com", "api.openai.com", true},
		{"API.openai.com", "api.openai.com", true},
		{"openai.com", "api.openai.com", false},
		{"*.openai.com", "api.openai.com", true},
		{"*.openai.com", "a.b.openai.com", false},
		{"**.openai.com", "a.b.openai.com", true},
		{"**.openai.com", "openai.com", false},
		{"*", "api.openai.com", false},
		{"**", "api.openai.com", true},
		{"api-*.openai.com", "api-eu.openai.com", true},
	} {
		assert.Equal(t, tc.want, policyHostMatches(tc.pattern, tc.host), "%s vs %s", tc.pattern, tc.host)
	}
}

func TestCheckOpenAIEgressInspected(t *testing.T) {
	t.Run("uninspected rule fails the run with the rule named", func(t *testing.T) {
		stubOpenshell(t, "case \"$1 $2\" in 'policy get') printf 'Version: 3\\n---\\nnetwork_policies:\\n  codex:\\n    endpoints:\\n    - host: api.openai.com\\n      port: 443\\n'; exit 0 ;; esac; exit 1")
		err := checkOpenAIEgressInspected(context.Background(), "fs-x")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rule codex allows api.openai.com:443 without L7 inspection")
		assert.Contains(t, err.Error(), "policies/base.yaml")
	})
	t.Run("inspected policy passes", func(t *testing.T) {
		stubOpenshell(t, "case \"$1 $2\" in 'policy get') printf -- '---\\nnetwork_policies:\\n  codex:\\n    endpoints:\\n    - host: api.openai.com\\n      port: 443\\n      protocol: rest\\n      access: read-write\\n'; exit 0 ;; esac; exit 1")
		require.NoError(t, checkOpenAIEgressInspected(context.Background(), "fs-x"))
	})
	t.Run("unreadable policy fails closed", func(t *testing.T) {
		stubOpenshell(t, "echo 'error: no such sandbox' >&2; exit 1")
		err := checkOpenAIEgressInspected(context.Background(), "fs-x")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no such sandbox")
	})
	t.Run("stderr noise does not reach the parser", func(t *testing.T) {
		stubOpenshell(t, "case \"$1 $2\" in 'policy get') echo 'warning: a newer openshell is available' >&2; printf -- '---\\nnetwork_policies:\\n  ok:\\n    endpoints:\\n    - host: api.openai.com\\n      port: 443\\n      protocol: rest\\n'; exit 0 ;; esac; exit 1")
		require.NoError(t, checkOpenAIEgressInspected(context.Background(), "fs-x"))
	})
}

// ph builds a gateway placeholder for tests; the prefix is assembled from
// parts because OpenShell 0.0.110+ resets model requests whose body carries
// it contiguously, and agents read this file.
func ph(suffix string) string { return "openshell:resolve:env" + ":" + suffix }

// stubOpenshell puts a throwaway `openshell` on PATH whose body is the
// given shell script (after the shebang).
func stubOpenshell(t *testing.T, body string) {
	t.Helper()
	binDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte("#!/bin/sh\n"+body+"\n"), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestReseedOpenAIAuth_WaitsForTheNewGenerationThenSeeds(t *testing.T) {
	binDir := t.TempDir()
	counter := filepath.Join(binDir, "count")
	log := filepath.Join(binDir, "log")
	// `sandbox exec ... printf %s "$OPENAI_API_KEY"` answers the old
	// placeholder twice, then the new one; the seed exec is recorded.
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *'printf %s'*) n=$(cat " + shellQuoteForTest(counter) + " 2>/dev/null || echo 0); n=$((n+1)); echo $n > " + shellQuoteForTest(counter) + "; if [ $n -le 2 ]; then printf '" + ph("v111_OPENAI_API_KEY") + "'; else printf '" + ph("v222_OPENAI_API_KEY") + "'; fi; exit 0 ;;\n" +
		"  *auth.json*) echo seeded >> " + shellQuoteForTest(log) + "; exit 0 ;;\n" +
		"esac\necho \"unhandled: $*\" >&2; exit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := openAIPlaceholderPoll
	openAIPlaceholderPoll = 10 * time.Millisecond
	t.Cleanup(func() { openAIPlaceholderPoll = old })

	h := openAIProviderHandle{sandbox: "fs-x", authSeed: `printf '{"openai":...}' > /sandbox/pi-config/auth.json`}
	got, err := reseedOpenAIAuth(context.Background(), h, ph("v111_OPENAI_API_KEY"), ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, ph("v222_OPENAI_API_KEY"), got)
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	assert.Equal(t, "seeded\n", string(data), "the seed ran exactly once, after the placeholder changed")
	n, _ := os.ReadFile(counter)
	assert.Equal(t, "3\n", string(n), "polled until the third answer")

	// Without a known baseline the wait could not tell old from new.
	_, err = reseedOpenAIAuth(context.Background(), h, "", ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "currently holds is unknown")
}

func TestReseedOpenAIAuth_SettleTimeoutIsAnError(t *testing.T) {
	binDir := t.TempDir()
	log := filepath.Join(binDir, "log")
	script := "#!/bin/sh\ncase \"$*\" in *'printf %s'*) printf '" + ph("v111_OPENAI_API_KEY") + "'; exit 0 ;; *auth.json*) echo seeded >> " + shellQuoteForTest(log) + "; exit 0 ;; esac; exit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldPoll, oldSettle := openAIPlaceholderPoll, openAIPlaceholderSettle
	openAIPlaceholderPoll, openAIPlaceholderSettle = 5*time.Millisecond, 30*time.Millisecond
	t.Cleanup(func() { openAIPlaceholderPoll, openAIPlaceholderSettle = oldPoll, oldSettle })

	h := openAIProviderHandle{sandbox: "fs-x", authSeed: "seed auth.json"}
	_, err := reseedOpenAIAuth(context.Background(), h, ph("v111_OPENAI_API_KEY"), ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still hands out the previous OpenAI placeholder")
	_, statErr := os.Stat(log)
	assert.True(t, os.IsNotExist(statErr), "the stale placeholder is never re-seeded")
}

func TestReseedOpenAIAuth_VerifiesAndRepeatsTheSeed(t *testing.T) {
	binDir := t.TempDir()
	log := filepath.Join(binDir, "log")
	checks := filepath.Join(binDir, "checks")
	// The first verification says the file still names the old
	// placeholder (an iteration seed raced the re-seed); the second passes.
	script := "#!/bin/sh\ncase \"$*\" in *'printf %s'*) printf '" + ph("v222_OPENAI_API_KEY") + "'; exit 0 ;; *'grep -qF'*) n=$(cat " + shellQuoteForTest(checks) + " 2>/dev/null || echo 0); n=$((n+1)); echo $n > " + shellQuoteForTest(checks) + "; [ $n -ge 2 ] ;; *'seed auth.json'*) echo seeded >> " + shellQuoteForTest(log) + "; exit 0 ;; esac; exit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	h := openAIProviderHandle{sandbox: "fs-x", authSeed: "seed auth.json", authFile: "/sandbox/pi-config/auth.json"}
	got, err := reseedOpenAIAuth(context.Background(), h, ph("v111_OPENAI_API_KEY"), ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, ph("v222_OPENAI_API_KEY"), got)
	data, _ := os.ReadFile(log)
	assert.Equal(t, "seeded\nseeded\n", string(data), "seeded again after the first verification failed")
}

func TestRefreshOpenAIProvider_ReseedsOnlyOnceTheSandboxIsUp(t *testing.T) {
	binDir := t.TempDir()
	argsLog := filepath.Join(binDir, "args")
	stage := filepath.Join(binDir, "stage")
	// Before `provider update` the exec env carries v111; afterwards v222.
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuoteForTest(argsLog) + "\n" +
		"case \"$1 $2\" in 'provider update') echo rotated > " + shellQuoteForTest(stage) + "; exit 0 ;; esac\n" +
		"case \"$*\" in *'printf %s'*) if [ -e " + shellQuoteForTest(stage) + " ]; then printf '" + ph("v222_OPENAI_API_KEY") + "'; else printf '" + ph("v111_OPENAI_API_KEY") + "'; fi; exit 0 ;; *'grep -qF'*) exit 0 ;; *'seed auth.json'*) exit 0 ;; esac; exit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := openAIPlaceholderPoll
	openAIPlaceholderPoll = 5 * time.Millisecond
	t.Cleanup(func() { openAIPlaceholderPoll = old })

	up := &atomic.Bool{}
	h := openAIProviderHandle{name: "openai-abc", keys: []string{"OPENAI_API_KEY"}, source: "static", sandbox: "fs-x", authSeed: "seed auth.json", authFile: "/sandbox/pi-config/auth.json", sandboxUp: up}

	// Sandbox not up yet: the expiry is pushed out, nothing is read or seeded.
	_, got, err := refreshOpenAIProvider(context.Background(), h, "", time.Minute, ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, "", got)
	lines := readArgLines(t, argsLog)
	require.Len(t, lines, 1, "%q", lines)
	assert.True(t, strings.HasPrefix(lines[0], "provider update openai-abc --credential-expires-at"), lines[0])

	// Sandbox up: baseline read, expiry pushed out (a new generation), wait for
	// the change, seed, verify — on the static path too.
	up.Store(true)
	os.Remove(stage)
	os.Remove(argsLog)
	_, got, err = refreshOpenAIProvider(context.Background(), h, "", time.Minute, ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, ph("v222_OPENAI_API_KEY"), got)
	joined := strings.Join(readArgLines(t, argsLog), "\n")
	first := strings.Index(joined, "printf %s")
	update := strings.Index(joined, "provider update")
	seed := strings.Index(joined, "seed auth.json")
	assert.Less(t, first, update, "baseline read before the update")
	assert.Less(t, update, seed, "seed after the update")
	assert.Contains(t, joined, "grep -qF", "the seed is verified")
}

func TestCleanupRunScopedProvider_WaitsOutTheDetachRace(t *testing.T) {
	binDir := t.TempDir()
	counter := filepath.Join(binDir, "count")
	script := "#!/bin/sh\n" +
		"case \"$1 $2\" in 'provider delete') n=$(cat " + shellQuoteForTest(counter) + " 2>/dev/null || echo 0); n=$((n+1)); echo $n > " + shellQuoteForTest(counter) + "; if [ $n -le 2 ]; then echo \"error: provider '$3' is attached to sandbox(es): fs-x\" >&2; exit 1; fi; echo \"Deleted provider $3\"; exit 0 ;; esac; exit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := openAIDeleteBackoff
	openAIDeleteBackoff = 5 * time.Millisecond
	t.Cleanup(func() { openAIDeleteBackoff = old })

	var buf syncBuffer
	cleanupRunScopedProvider("openai-feedface", []string{"OPENAI_API_KEY"}, false, ui.New(&buf))
	assert.Contains(t, buf.String(), "Run-scoped provider deleted")
	n, _ := os.ReadFile(counter)
	assert.Equal(t, "3\n", string(n), "two refusals, then success")

	// A kept sandbox holds the reference for good: no retries, expire in place.
	require.NoError(t, os.WriteFile(counter, []byte("-100\n"), 0o644))
	cleanupRunScopedProvider("openai-feedface", []string{"OPENAI_API_KEY"}, true, ui.New(io.Discard))
	n, _ = os.ReadFile(counter)
	assert.Equal(t, "-99\n", string(n), "a single attempt when the sandbox is kept")
}

func TestResolveOpenAICredential_ConfigFallback(t *testing.T) {
	ids := config.OpenAIWIFConfig{Audience: "fullsend://acme", IdentityProviderID: "idp_cfg", ServiceAccountID: "sa_cfg"}
	t.Run("a partial config block is an error where the block applies", func(t *testing.T) {
		_, err := resolveOpenAICredential(context.Background(), openAITestEnv(map[string]string{"ACTIONS_ID_TOKEN_REQUEST_URL": "https://oidc.example/token", "OPENAI_API_KEY": "sk-stray"}), config.OpenAIWIFConfig{Audience: "fullsend://acme"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "inference.openai in config.yaml is partially configured: missing identity_provider_id, service_account_id")
		_, err = resolveOpenAICredential(context.Background(), openAITestEnv(nil), config.OpenAIWIFConfig{Audience: "fullsend://acme"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "partially configured", "also with no static key and no OIDC endpoint")
	})
	t.Run("the complete block drives the exchange when the variables are unset", func(t *testing.T) {
		_, err := resolveOpenAICredential(context.Background(), openAITestEnv(nil), ids)
		require.Error(t, err, "no OIDC endpoint in this test process")
		assert.Contains(t, err.Error(), "no GitHub OIDC endpoint", "reached the WIF branch with the config ids")
	})
	t.Run("variables win over the block and are not merged with it", func(t *testing.T) {
		_, err := resolveOpenAICredential(context.Background(), openAITestEnv(map[string]string{"FULLSEND_OPENAI_AUDIENCE": "fullsend://other"}), ids)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "OpenAI WIF is partially configured: missing FULLSEND_OPENAI_IDENTITY_PROVIDER_ID, FULLSEND_OPENAI_SERVICE_ACCOUNT_ID", "a partial variable set is reported as such; the config block does not fill the gaps")
	})
	t.Run("a whitespace-only block counts as unset", func(t *testing.T) {
		_, err := resolveOpenAICredential(context.Background(), openAITestEnv(nil), config.OpenAIWIFConfig{Audience: "  ", IdentityProviderID: "\t", ServiceAccountID: " "})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no OpenAI credential", "not treated as a complete WIF configuration")
	})
	t.Run("a developer's OPENAI_API_KEY wins over the committed block when there is no OIDC endpoint", func(t *testing.T) {
		cred, err := resolveOpenAICredential(context.Background(), openAITestEnv(map[string]string{"OPENAI_API_KEY": "sk-local-static-key"}), ids)
		require.NoError(t, err)
		assert.Equal(t, "static", cred.source)
		assert.Contains(t, cred.detail, "inference.openai in config.yaml not used")
	})
	t.Run("with an OIDC endpoint the committed block wins over a stray OPENAI_API_KEY", func(t *testing.T) {
		_, err := resolveOpenAICredential(context.Background(), openAITestEnv(map[string]string{"OPENAI_API_KEY": "sk-stray", "ACTIONS_ID_TOKEN_REQUEST_URL": "https://oidc.example/token"}), ids)
		require.Error(t, err, "reaches the exchange (no token in this test process)")
		assert.NotContains(t, err.Error(), "no OpenAI credential")
	})
	t.Run("no variables and no block: the usual message names both", func(t *testing.T) {
		_, err := resolveOpenAICredential(context.Background(), openAITestEnv(nil), config.OpenAIWIFConfig{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "inference.openai in config.yaml")
	})
}
