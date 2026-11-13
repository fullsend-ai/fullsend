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
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/inference/actionsoidc"
	"github.com/fullsend-ai/fullsend/internal/mintclient"
	"github.com/fullsend-ai/fullsend/internal/prescript"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/fullsend-ai/fullsend/internal/workflowcmd"
)

// Synthetic values only: none of these is a real credential.
const (
	maskTestInitialToken  = "synthetic-initial-token-aaaa1111"
	maskTestRefreshToken  = "synthetic-refresh-token-bbbb2222"
	maskTestWorkflowToken = "synthetic-workflow-token-cccc3333"
)

// suppressWorkflowCommands turns suppression on for the test and restores it.
func suppressWorkflowCommands(t *testing.T) {
	t.Helper()
	workflowcmd.SetSuppressed(true)
	t.Cleanup(func() { workflowcmd.SetSuppressed(false) })
	orig := noGitHubWorkflowCommandsNoticeOnce
	noGitHubWorkflowCommandsNoticeOnce = &sync.Once{}
	t.Cleanup(func() { noGitHubWorkflowCommandsNoticeOnce = orig })
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

func TestNoGitHubWorkflowCommandsEnvEnabled(t *testing.T) {
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
			t.Setenv(noGitHubWorkflowCommandsEnv, value)
			assert.Equal(t, want, noGitHubWorkflowCommandsEnvEnabled())
		})
	}
}

func TestMaskActionsValue_DefaultEmitsAddMask(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	// The environment variable alone does not affect maskActionsValue; only
	// `fullsend run` reads it.
	t.Setenv(noGitHubWorkflowCommandsEnv, "1")
	stderr := captureStderr(t, func() { maskActionsValue(maskTestInitialToken) })
	assert.Equal(t, "::add-mask::"+maskTestInitialToken+"\n", stderr)
}

func TestMaskActionsValue_OutsideActionsPrintsNothing(t *testing.T) {
	for _, suppressed := range []bool{false, true} {
		t.Run(fmt.Sprintf("suppressed=%v", suppressed), func(t *testing.T) {
			if suppressed {
				suppressWorkflowCommands(t)
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

	suppressWorkflowCommands(t)
	const onValue = "synthetic-outside-actions-on-value"
	maskActionsValue(onValue)
	assertRedacted(t, onValue)
}

func TestMaskActionsValue_SuppressedWritesNoMaskCommand(t *testing.T) {
	suppressWorkflowCommands(t)
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
	assert.Equal(t, 1, bytes.Count([]byte(stderr), []byte("workflow commands are suppressed")), "one value-free notice per process")
	assert.Contains(t, stderr, "bypass that redactor")
	assertRedacted(t, maskTestInitialToken)
	assertRedacted(t, maskTestRefreshToken)
}

// Initial and refreshed (post-script remint) agent tokens: no value
// reaches stderr, the tokens still land in the environment, and the
// "Agent token minted" diagnostic is still printed.
func TestMintAgentToken_SuppressedInitialAndRemint(t *testing.T) {
	suppressWorkflowCommands(t)
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
	assert.Contains(t, stderr, "workflow commands are suppressed")
	assert.Contains(t, out.String(), "Agent token minted")
}

// A refreshed OIDC token is not printed while workflow commands are suppressed.
func TestRefreshOIDCToken_Suppressed(t *testing.T) {
	suppressWorkflowCommands(t)
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

// A rotated inference gateway token is not printed while workflow commands are
// suppressed, and the rotation still updates the provider.
func TestRotateGatewayTokenOnce_Suppressed(t *testing.T) {
	suppressWorkflowCommands(t)
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
	t.Setenv(noGitHubWorkflowCommandsEnv, "1")

	var stdout bytes.Buffer
	cmd := newMintTokenCmd()
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--role", "triage", "--repos", "my-repo", "--mint-url", mintServer.URL})
	stderr := captureStderr(t, func() { require.NoError(t, cmd.Execute()) })

	assert.Contains(t, stderr, "::add-mask::"+maskTestInitialToken)
	assert.Equal(t, maskTestInitialToken, stdout.String())
	assert.Nil(t, cmd.Flags().Lookup(noGitHubWorkflowCommandsFlag))
	assert.Nil(t, newRootCmd().PersistentFlags().Lookup(noGitHubWorkflowCommandsFlag))
}

func TestRunCmd_HasNoGitHubWorkflowCommandsFlag(t *testing.T) {
	f := newRunCmd().Flags().Lookup(noGitHubWorkflowCommandsFlag)
	require.NotNil(t, f)
	assert.Equal(t, "false", f.DefValue)
	assert.Contains(t, f.Usage, "::add-mask::")
	assert.Contains(t, f.Usage, noGitHubWorkflowCommandsEnv)
	assert.Contains(t, f.Usage, "::error::")
	assert.Contains(t, f.Usage, "bypass")
}

func TestResolveNoGitHubWorkflowCommands(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  string
		want bool
	}{
		{"default", nil, "", false},
		{"flag", []string{"--" + noGitHubWorkflowCommandsFlag}, "", true},
		{"env", nil, "1", true},
		{"env false", nil, "false", false},
		{"explicit false flag beats env", []string{"--" + noGitHubWorkflowCommandsFlag + "=false"}, "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(noGitHubWorkflowCommandsEnv, tc.env)
			cmd := &cobra.Command{Use: "probe"}
			addNoGitHubWorkflowCommandsFlag(cmd)
			require.NoError(t, cmd.ParseFlags(tc.args))
			assert.Equal(t, tc.want, resolveNoGitHubWorkflowCommands(cmd))
		})
	}
}

// A pre-script's workflow commands do not reach stdout or stderr while
// suppressed; its other lines do, and an add-mask value is registered with
// the redactor.
func TestRunPreScript_SuppressedDropsWorkflowCommands(t *testing.T) {
	suppressWorkflowCommands(t)
	const scriptValue = "synthetic-prescript-mask-value"
	h := &harness.Harness{PreScript: writePreScript(t,
		"echo '::notice::synthetic notice'\n"+
			"echo 'plain stdout line'\n"+
			"echo '::add-mask::"+scriptValue+"' >&2\n"+
			"echo '::error title=x::synthetic error' >&2\n"+
			"echo 'plain stderr line' >&2\n")}

	var stderr string
	stdout := captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			_, err := runPreScript(h, t.TempDir(), "", ui.New(io.Discard))
			require.NoError(t, err)
		})
	})
	assert.Equal(t, "plain stdout line\n", stdout)
	assert.Equal(t, "plain stderr line\n", stderr)
	assertRedacted(t, scriptValue)
}

// The raw capture buffers still hold dropped command lines; the fallback
// failure detail and the exit-78 reason must not surface them.
func TestRunPreScript_SuppressedFallbacksSkipCommandLines(t *testing.T) {
	suppressWorkflowCommands(t)

	t.Run("hard failure", func(t *testing.T) {
		h := &harness.Harness{PreScript: writePreScript(t,
			"echo 'plain failure reason' >&2\n"+
				"echo '::notice::synthetic notice' >&2\n"+
				"exit 1\n")}
		var err error
		captureStdout(t, func() {
			captureStderr(t, func() { _, err = runPreScript(h, t.TempDir(), "", ui.New(io.Discard)) })
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "plain failure reason")
		assert.NotContains(t, err.Error(), "::notice::")
	})

	t.Run("hard failure keeps error annotation message", func(t *testing.T) {
		h := &harness.Harness{PreScript: writePreScript(t,
			"echo '::error::synthetic annotated failure' >&2\n"+
				"exit 1\n")}
		var err error
		captureStdout(t, func() {
			captureStderr(t, func() { _, err = runPreScript(h, t.TempDir(), "", ui.New(io.Discard)) })
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "synthetic annotated failure")
	})

	t.Run("neutral exit", func(t *testing.T) {
		h := &harness.Harness{PreScript: writePreScript(t,
			"echo 'No work to do'\n"+
				"echo '::notice::synthetic notice'\n"+
				"exit 78\n")}
		var res prescript.Result
		var err error
		captureStdout(t, func() {
			captureStderr(t, func() { res, err = runPreScript(h, t.TempDir(), "", ui.New(io.Discard)) })
		})
		require.NoError(t, err)
		assert.True(t, res.Skipped)
		assert.Equal(t, "No work to do", res.Reason)
	})
}

func TestRunPreScript_DefaultKeepsWorkflowCommands(t *testing.T) {
	h := &harness.Harness{PreScript: writePreScript(t, "echo '::notice::synthetic notice' >&2\n")}
	stderr := captureStderr(t, func() {
		_, err := runPreScript(h, t.TempDir(), "", ui.New(io.Discard))
		require.NoError(t, err)
	})
	assert.Equal(t, "::notice::synthetic notice\n", stderr)
}

// The agent-completed heartbeat annotation and the run-info annotation are
// not written while suppressed.
func TestRunAnnotations_Suppressed(t *testing.T) {
	suppressWorkflowCommands(t)
	t.Setenv("GITHUB_ACTIONS", "true")

	var out bytes.Buffer
	emitRunInfoNotice(&out, true, runInfoFor(aggregateMetrics{Runtime: "pi", Model: "synthetic-model"}, "medium"))
	assert.Empty(t, out.String())

	done := make(chan struct{})
	close(done)
	runHeartbeatTo(&out, ui.New(io.Discard), time.Now(), time.Minute, done)
	assert.Empty(t, out.String())
}
