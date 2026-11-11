package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/inference/actionsoidc"
	"github.com/fullsend-ai/fullsend/internal/mintclient"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Synthetic values only: none of these is a real credential.
const (
	maskTestInitialToken  = "synthetic-initial-token-aaaa1111"
	maskTestRefreshToken  = "synthetic-refresh-token-bbbb2222"
	maskTestWorkflowToken = "synthetic-workflow-token-cccc3333"
)

// suppressMaskCommands turns suppression on for the test and restores it.
func suppressMaskCommands(t *testing.T) {
	t.Helper()
	githubMaskCommandsSuppressed.Store(true)
	t.Cleanup(func() { githubMaskCommandsSuppressed.Store(false) })
	orig := noGitHubMaskCommandsNoticeOnce
	noGitHubMaskCommandsNoticeOnce = &sync.Once{}
	t.Cleanup(func() { noGitHubMaskCommandsNoticeOnce = orig })
}

// assertRedacted checks that value is registered with the process-wide
// redactor, so fullsend's in-process redaction covers it.
func assertRedacted(t *testing.T, value string) {
	t.Helper()
	scan := security.NewSecretRedactor().Scan("log " + value + " here")
	// Sanitized is empty when nothing was redacted, so check Safe too.
	assert.False(t, scan.Safe, "value must be registered for in-process redaction")
	assert.NotContains(t, scan.Sanitized, value, "value must be registered for in-process redaction")
}

func TestNoGitHubMaskCommandsEnvEnabled(t *testing.T) {
	for value, want := range map[string]bool{
		"":      false,
		"  ":    false,
		"0":     false,
		"false": false,
		"FALSE": false,
		"1":     true,
		"true":  true,
		" on ":  true, // not a ParseBool value: fails toward suppressing
		"yes":   true,
	} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			t.Setenv(noGitHubMaskCommandsEnv, value)
			assert.Equal(t, want, noGitHubMaskCommandsEnvEnabled())
		})
	}
}

func TestMaskActionsValue_DefaultEmitsAddMask(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	// The environment variable alone does not affect maskActionsValue; only
	// `fullsend run` reads it.
	t.Setenv(noGitHubMaskCommandsEnv, "1")
	stderr := captureStderr(t, func() { maskActionsValue(maskTestInitialToken) })
	assert.Equal(t, "::add-mask::"+maskTestInitialToken+"\n", stderr)
}

func TestMaskActionsValue_OutsideActionsPrintsNothing(t *testing.T) {
	for _, suppressed := range []bool{false, true} {
		t.Run(fmt.Sprintf("suppressed=%v", suppressed), func(t *testing.T) {
			if suppressed {
				suppressMaskCommands(t)
			}
			t.Setenv("GITHUB_ACTIONS", "")
			assert.Empty(t, captureStderr(t, func() { maskActionsValue(maskTestInitialToken) }))
		})
	}
}

// While suppressed, the value is registered with the in-process redactor
// even outside Actions; without suppression it is not registered.
func TestMaskActionsValue_OutsideActionsRegistersOnlyWhenSuppressed(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")

	const offValue = "synthetic-outside-actions-off-value"
	maskActionsValue(offValue)
	scan := security.NewSecretRedactor().Scan("log " + offValue + " here")
	assert.True(t, scan.Safe, "default mode registers nothing outside Actions")

	suppressMaskCommands(t)
	const onValue = "synthetic-outside-actions-on-value"
	maskActionsValue(onValue)
	assertRedacted(t, onValue)
}

func TestMaskActionsValue_SuppressedWritesNoMaskCommand(t *testing.T) {
	suppressMaskCommands(t)
	t.Setenv("GITHUB_ACTIONS", "true")
	const multiLine = "synthetic-line-one-value\nsynthetic-line-two-value"
	stderr := captureStderr(t, func() {
		maskActionsValue(maskTestInitialToken)
		maskActionsValue(maskTestRefreshToken)
		maskActionsValue(multiLine)
	})
	assert.NotContains(t, stderr, "::add-mask::")
	for _, v := range []string{maskTestInitialToken, maskTestRefreshToken, "synthetic-line-one-value", "synthetic-line-two-value"} {
		assert.NotContains(t, stderr, v)
	}
	assert.Equal(t, 1, bytes.Count([]byte(stderr), []byte("mask commands are suppressed")), "one value-free notice per process")
	assert.Contains(t, stderr, "may bypass that redactor")
	assertRedacted(t, maskTestInitialToken)
	assertRedacted(t, maskTestRefreshToken)
}

// Initial and refreshed (post-script remint) agent tokens: no value
// reaches stderr, the tokens still land in the environment, and the
// "Agent token minted" diagnostic is still printed.
func TestMintAgentToken_SuppressedInitialAndRemint(t *testing.T) {
	suppressMaskCommands(t)
	origMint := statusMintToken
	t.Cleanup(func() { statusMintToken = origMint })
	tokens := []string{maskTestInitialToken, maskTestRefreshToken}
	var calls atomic.Int32
	statusMintToken = func(_ context.Context, _ mintclient.MintRequest) (*mintclient.MintResult, error) {
		i := calls.Add(1) - 1
		return &mintclient.MintResult{Token: tokens[i], ExpiresAt: "2026-06-15T12:00:00Z"}, nil
	}

	t.Setenv("REPO_FULL_NAME", "org/my-repo")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GH_TOKEN", maskTestWorkflowToken)
	t.Setenv("PUSH_TOKEN", "")
	t.Setenv("PUSH_TOKEN_SOURCE", "")
	t.Setenv(workflowTokenEnv, "")

	var out bytes.Buffer
	printer := ui.New(&out)
	var cleanups []func()
	stderr := captureStderr(t, func() {
		for range tokens {
			minted, cleanup, err := mintAgentToken(context.Background(), "coder", "https://mint.example.com", "", printer)
			require.NoError(t, err)
			require.True(t, minted)
			cleanups = append(cleanups, cleanup)
			assert.Equal(t, tokens[len(cleanups)-1], os.Getenv("GH_TOKEN"), "authentication still uses the minted token")
		}
	})
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}

	assert.NotContains(t, stderr, "::add-mask::")
	for _, v := range append(tokens, maskTestWorkflowToken) {
		assert.NotContains(t, stderr, v)
		assert.NotContains(t, out.String(), v)
		assertRedacted(t, v)
	}
	assert.Contains(t, stderr, "mask commands are suppressed")
	assert.Contains(t, out.String(), "Agent token minted")
}

// A refreshed OIDC token is not printed while mask commands are suppressed.
func TestRefreshOIDCToken_Suppressed(t *testing.T) {
	suppressMaskCommands(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"value": maskTestRefreshToken})
	}))
	defer srv.Close()
	t.Setenv("GITHUB_ACTIONS", "true")
	stderr := captureStderr(t, func() {
		_ = refreshOIDCToken(context.Background(), "nonexistent-sandbox", srv.URL, "bearer test-auth")
	})
	assert.NotContains(t, stderr, maskTestRefreshToken)
	assert.NotContains(t, stderr, "::add-mask::")
	assertRedacted(t, maskTestRefreshToken)
}

// A rotated inference gateway token is not printed while mask commands are
// suppressed, and the rotation still updates the provider.
func TestRotateGatewayTokenOnce_Suppressed(t *testing.T) {
	suppressMaskCommands(t)
	t.Setenv("GITHUB_ACTIONS", "true")
	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "req")
	iat := time.Now().Truncate(time.Second)
	stubGatewayAssertion(t, func(context.Context, actionsoidc.AssertionConfig) (*actionsoidc.Assertion, error) {
		return &actionsoidc.Assertion{Value: gatewayTestJWT, IssuedAt: iat, ExpiresAt: iat.Add(5 * time.Minute)}, nil
	})
	var updated map[string]string
	orig := updateGatewayProviderFn
	updateGatewayProviderFn = func(_ context.Context, _ string, creds map[string]string, _ time.Time) error {
		updated = creds
		return nil
	}
	t.Cleanup(func() { updateGatewayProviderFn = orig })

	h := gatewayProviderHandle{name: "inference-gateway-x", block: config.InferenceGatewayConfig{Audience: "aud"}, sandbox: "fs-x", sandboxUp: &atomic.Bool{}}
	stderr := captureStderr(t, func() {
		_, err := rotateGatewayTokenOnce(context.Background(), h)
		require.NoError(t, err)
	})
	assert.NotContains(t, stderr, gatewayTestJWT)
	assert.NotContains(t, stderr, "::add-mask::")
	assert.Equal(t, map[string]string{gatewayCredentialKey: gatewayTestJWT}, updated)
}

// The flag and the environment variable only exist for `fullsend run`:
// other commands keep emitting mask commands, and the root command has no
// such flag.
func TestMintTokenCmd_KeepsMaskCommandWithEnvSet(t *testing.T) {
	oidcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"value": "oidc-jwt"})
	}))
	defer oidcServer.Close()
	mintServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(mintclient.MintResult{Token: maskTestInitialToken, ExpiresAt: "2026-06-11T23:30:00Z"})
	}))
	defer mintServer.Close()

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", oidcServer.URL+"?d=1")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "tok")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv(noGitHubMaskCommandsEnv, "1")

	var stdout bytes.Buffer
	cmd := newMintTokenCmd()
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--role", "triage", "--repos", "my-repo", "--mint-url", mintServer.URL})
	stderr := captureStderr(t, func() { require.NoError(t, cmd.Execute()) })

	assert.Contains(t, stderr, "::add-mask::"+maskTestInitialToken)
	assert.Equal(t, maskTestInitialToken, stdout.String())
	assert.Nil(t, cmd.Flags().Lookup(noGitHubMaskCommandsFlag))
	assert.Nil(t, newRootCmd().PersistentFlags().Lookup(noGitHubMaskCommandsFlag))
}

func TestRunCmd_HasNoGitHubMaskCommandsFlag(t *testing.T) {
	f := newRunCmd().Flags().Lookup(noGitHubMaskCommandsFlag)
	require.NotNil(t, f)
	assert.Equal(t, "false", f.DefValue)
	assert.Contains(t, f.Usage, "::add-mask::")
	assert.Contains(t, f.Usage, noGitHubMaskCommandsEnv)
	assert.Contains(t, f.Usage, "bypass")
}

func TestResolveNoGitHubMaskCommands(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  string
		want bool
	}{
		{"default", nil, "", false},
		{"flag", []string{"--" + noGitHubMaskCommandsFlag}, "", true},
		{"env", nil, "1", true},
		{"env false", nil, "false", false},
		{"explicit false flag beats env", []string{"--" + noGitHubMaskCommandsFlag + "=false"}, "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(noGitHubMaskCommandsEnv, tc.env)
			cmd := &cobra.Command{Use: "probe"}
			addNoGitHubMaskCommandsFlag(cmd)
			require.NoError(t, cmd.ParseFlags(tc.args))
			assert.Equal(t, tc.want, resolveNoGitHubMaskCommands(cmd))
		})
	}
}
