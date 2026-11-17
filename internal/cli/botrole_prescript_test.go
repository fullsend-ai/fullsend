package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/mintclient"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// scriptEnvValue returns the value of key in an `env` dump written by a
// script.
func scriptEnvValue(t *testing.T, dump, key string) (string, bool) {
	t.Helper()
	for _, line := range strings.Split(dump, "\n") {
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimPrefix(line, key+"="), true
		}
	}
	return "", false
}

// TestRunAgent_BotRoleHelperEnvReachedPreScriptWithoutOIDC is the wiring
// half of #9602's first criterion: inside a `fullsend run` job for a
// target repository, a harness pre-script receives the runner-side
// bot-role lookup's address and bearer token (FULLSEND_BOT_ROLE_URL /
// FULLSEND_BOT_ROLE_TOKEN, the same shape as the runtime fetch
// service's FULLSEND_FETCH_URL/TOKEN), while the Actions OIDC
// credentials the lookup authenticates with stay runner-side:
// ACTIONS_ID_TOKEN_* never reaches the script.
func TestRunAgent_BotRoleHelperEnvReachedPreScriptWithoutOIDC(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv("FULLSEND_RUNTIME", "codex")
	out := filepath.Join(t.TempDir(), "github-output")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_OUTPUT", out)

	// Look like a real job for a target repository: a mint, the Actions
	// OIDC environment, and the repository the run targets. The agent
	// token mint is stubbed so the run needs no network beyond the
	// loopback mint below.
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "unused", "expires_at": "2026-06-15T12:00:00Z"})
	}))
	t.Cleanup(mint.Close)
	t.Setenv("FULLSEND_MINT_URL", mint.URL)
	t.Setenv("GITHUB_REPOSITORY", "fullsend-ai/agents")
	t.Setenv("REPO_FULL_NAME", "fullsend-ai/agents")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://oidc.example.invalid/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "oidc-request-token-9602")
	origMint := statusMintToken
	statusMintToken = func(_ context.Context, req mintclient.MintRequest) (*mintclient.MintResult, error) {
		return &mintclient.MintResult{Token: "ghs_test_token_9602", ExpiresAt: "2026-06-15T12:00:00Z"}, nil
	}
	t.Cleanup(func() { statusMintToken = origMint })

	marker := filepath.Join(t.TempDir(), "prescript-env")
	dir := newSkipHarnessDir(t, "env > "+marker+"\n"+
		`echo "skipped=true" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n")

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	require.NoError(t, runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{}))

	dump, err := os.ReadFile(marker)
	require.NoError(t, err, "the pre-script must run; its environment dump is the evidence")
	env := string(dump)

	helperURL, ok := scriptEnvValue(t, env, "FULLSEND_BOT_ROLE_URL")
	require.True(t, ok, "the pre-script environment must carry FULLSEND_BOT_ROLE_URL so a script can ask for a bot's registered role (#9602)")
	require.NotEmpty(t, helperURL)
	assert.True(t, strings.HasPrefix(helperURL, "http"), "FULLSEND_BOT_ROLE_URL must be a URL, got %q", helperURL)

	helperToken, ok := scriptEnvValue(t, env, "FULLSEND_BOT_ROLE_TOKEN")
	require.True(t, ok, "the pre-script environment must carry FULLSEND_BOT_ROLE_TOKEN")
	require.NotEmpty(t, helperToken)
	assert.NotEqual(t, "oidc-request-token-9602", helperToken, "the helper token must be its own credential, not the Actions OIDC request token")

	_, present := scriptEnvValue(t, env, "ACTIONS_ID_TOKEN_REQUEST_URL")
	assert.False(t, present, "ACTIONS_ID_TOKEN_REQUEST_URL must never reach a harness script")
	_, present = scriptEnvValue(t, env, "ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	assert.False(t, present, "ACTIONS_ID_TOKEN_REQUEST_TOKEN must never reach a harness script")
	assert.NotContains(t, env, "oidc-request-token-9602", "no OIDC credential value may leak into the script environment")
}
