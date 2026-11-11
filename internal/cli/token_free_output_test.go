package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	tokenFreeInitialToken  = "ghs_synthetic_initial_token_value_0001"
	tokenFreeRefreshToken  = "ghs_synthetic_refresh_token_value_0002"
	tokenFreeWorkflowToken = "ghs_synthetic_workflow_token_value_0003"
)

// resetTokenFreeNotice gives the test its own once-per-process notice.
func resetTokenFreeNotice(t *testing.T) {
	t.Helper()
	orig := tokenFreeNoticeOnce
	tokenFreeNoticeOnce = &sync.Once{}
	t.Cleanup(func() { tokenFreeNoticeOnce = orig })
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

func TestTokenFreeOutputEnabled(t *testing.T) {
	for value, want := range map[string]bool{
		"":      false,
		"  ":    false,
		"0":     false,
		"false": false,
		"FALSE": false,
		"1":     true,
		"true":  true,
		" on ":  true, // not a ParseBool value: fails toward token-free
		"yes":   true,
	} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			t.Setenv(tokenFreeOutputEnv, value)
			assert.Equal(t, want, tokenFreeOutputEnabled())
		})
	}
}

func TestMaskActionsValue_DefaultEmitsAddMask(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv(tokenFreeOutputEnv, "")
	stderr := captureStderr(t, func() { maskActionsValue(tokenFreeInitialToken) })
	assert.Equal(t, "::add-mask::"+tokenFreeInitialToken+"\n", stderr)
}

func TestMaskActionsValue_OutsideActionsPrintsNothing(t *testing.T) {
	for _, mode := range []string{"", "1"} {
		t.Run("token-free="+mode, func(t *testing.T) {
			resetTokenFreeNotice(t)
			t.Setenv("GITHUB_ACTIONS", "")
			t.Setenv(tokenFreeOutputEnv, mode)
			assert.Empty(t, captureStderr(t, func() { maskActionsValue(tokenFreeInitialToken) }))
		})
	}
}

// Under token-free output the value is registered with the in-process
// redactor even outside Actions; without the mode it is not registered.
func TestMaskActionsValue_OutsideActionsRegistersOnlyWhenTokenFree(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")

	const offValue = "synthetic-outside-actions-off-value"
	t.Setenv(tokenFreeOutputEnv, "")
	maskActionsValue(offValue)
	scan := security.NewSecretRedactor().Scan("log " + offValue + " here")
	assert.True(t, scan.Safe, "default mode registers nothing outside Actions")

	const onValue = "synthetic-outside-actions-on-value"
	t.Setenv(tokenFreeOutputEnv, "1")
	maskActionsValue(onValue)
	assertRedacted(t, onValue)
}

func TestMaskActionsValue_TokenFreeSuppressesValue(t *testing.T) {
	resetTokenFreeNotice(t)
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv(tokenFreeOutputEnv, "1")
	const multiLine = "synthetic-line-one-value\nsynthetic-line-two-value"
	stderr := captureStderr(t, func() {
		maskActionsValue(tokenFreeInitialToken)
		maskActionsValue(tokenFreeRefreshToken)
		maskActionsValue(multiLine)
	})
	assert.NotContains(t, stderr, "::add-mask::")
	for _, v := range []string{tokenFreeInitialToken, tokenFreeRefreshToken, "synthetic-line-one-value", "synthetic-line-two-value"} {
		assert.NotContains(t, stderr, v)
	}
	assert.Equal(t, 1, bytes.Count([]byte(stderr), []byte("token-free output is on")), "one value-free notice per process")
	assertRedacted(t, tokenFreeInitialToken)
	assertRedacted(t, tokenFreeRefreshToken)
}

// Initial and refreshed (post-script remint) agent tokens: no value
// reaches stderr, the tokens still land in the environment, and the
// "Agent token minted" diagnostic is still printed.
func TestMintAgentToken_TokenFreeInitialAndRemint(t *testing.T) {
	resetTokenFreeNotice(t)
	origMint := statusMintToken
	t.Cleanup(func() { statusMintToken = origMint })
	tokens := []string{tokenFreeInitialToken, tokenFreeRefreshToken}
	var calls atomic.Int32
	statusMintToken = func(_ context.Context, _ mintclient.MintRequest) (*mintclient.MintResult, error) {
		i := calls.Add(1) - 1
		return &mintclient.MintResult{Token: tokens[i], ExpiresAt: "2026-06-15T12:00:00Z"}, nil
	}

	t.Setenv("REPO_FULL_NAME", "org/my-repo")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv(tokenFreeOutputEnv, "true")
	t.Setenv("GH_TOKEN", tokenFreeWorkflowToken)
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
	for _, v := range append(tokens, tokenFreeWorkflowToken) {
		assert.NotContains(t, stderr, v)
		assert.NotContains(t, out.String(), v)
		assertRedacted(t, v)
	}
	assert.Contains(t, stderr, "token-free output is on")
	assert.Contains(t, out.String(), "Agent token minted")
}

// A refreshed OIDC token is not printed under token-free output.
func TestRefreshOIDCToken_TokenFree(t *testing.T) {
	resetTokenFreeNotice(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"value": tokenFreeRefreshToken})
	}))
	defer srv.Close()
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv(tokenFreeOutputEnv, "1")
	stderr := captureStderr(t, func() {
		_ = refreshOIDCToken(context.Background(), "nonexistent-sandbox", srv.URL, "bearer test-auth")
	})
	assert.NotContains(t, stderr, tokenFreeRefreshToken)
	assert.NotContains(t, stderr, "::add-mask::")
	assertRedacted(t, tokenFreeRefreshToken)
}

// A rotated inference gateway token is not printed under token-free
// output, and the rotation still updates the provider.
func TestRotateGatewayTokenOnce_TokenFree(t *testing.T) {
	resetTokenFreeNotice(t)
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv(tokenFreeOutputEnv, "1")
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

// fullsend mint token: the mask line is suppressed, but the token is still
// printed on stdout because it is the command's result.
func TestMintTokenCmd_TokenFree(t *testing.T) {
	resetTokenFreeNotice(t)
	oidcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"value": "oidc-jwt"})
	}))
	defer oidcServer.Close()
	mintServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(mintclient.MintResult{Token: tokenFreeInitialToken, ExpiresAt: "2026-06-11T23:30:00Z"})
	}))
	defer mintServer.Close()

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", oidcServer.URL+"?d=1")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "tok")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv(tokenFreeOutputEnv, "1")

	var stdout bytes.Buffer
	cmd := newMintTokenCmd()
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--role", "triage", "--repos", "my-repo", "--mint-url", mintServer.URL})
	stderr := captureStderr(t, func() { require.NoError(t, cmd.Execute()) })

	assert.NotContains(t, stderr, tokenFreeInitialToken)
	assert.NotContains(t, stderr, "::add-mask::")
	assert.Equal(t, tokenFreeInitialToken, stdout.String())
}

// The root --token-free-output flag turns the mode on for any subcommand
// and exports it for child processes; without it the default is kept.
func TestRootCmd_TokenFreeOutputFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"flag set", []string{"--token-free-output", "probe"}, true},
		{"flag after subcommand", []string{"probe", "--token-free-output"}, true},
		{"default", []string{"probe"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tokenFreeOutputEnv, "")
			var got bool
			root := newRootCmd()
			root.AddCommand(&cobra.Command{
				Use: "probe",
				RunE: func(*cobra.Command, []string) error {
					got = tokenFreeOutputEnabled()
					return nil
				},
			})
			root.SetOut(io.Discard)
			root.SetArgs(tc.args)
			require.NoError(t, root.Execute())
			assert.Equal(t, tc.want, got)
		})
	}
}
