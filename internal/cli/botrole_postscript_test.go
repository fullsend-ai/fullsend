package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/mintclient"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunAgent_BotRoleHelperEnvReachedPostScript is the post-script half of
// #9602's first criterion: the runner-side bot-role lookup's address and
// bearer token (FULLSEND_BOT_ROLE_URL / FULLSEND_BOT_ROLE_TOKEN) reach every
// post-script, not only pre-scripts. The append in runAgent's post-script
// defer is inline wiring no narrower test (postScriptEnv alone) can see.
func TestRunAgent_BotRoleHelperEnvReachedPostScript(t *testing.T) {
	for _, k := range []string{"OTEL_SDK_DISABLED", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"} {
		t.Setenv(k, "")
	}
	repoDir := newShallowTestRepo(t)
	useFakeOpenshellRepoDownload(t, "cp -R "+shellQuoteForTest(repoDir)+"/. \"$dest\"/", "")
	// The helper starts only when a lookup could ever be answered: a mint
	// URL and the Actions OIDC request environment (set after the download
	// stub, which neutralizes ambient mint overrides). No issue or pull
	// request number is set, so no entity call is made and no lookup is
	// asked - and the agent token mint is stubbed so the run needs no
	// network beyond loopback.
	t.Setenv("REPO_FULL_NAME", "fullsend-ai/agents")
	t.Setenv("FULLSEND_MINT_URL", "http://127.0.0.1:1/mint")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://oidc.example.invalid/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "oidc-request-token-9602")
	origMint := statusMintToken
	statusMintToken = func(_ context.Context, req mintclient.MintRequest) (*mintclient.MintResult, error) {
		return &mintclient.MintResult{Token: "ghs_test_token_9602", ExpiresAt: "2026-06-15T12:00:00Z"}, nil
	}
	t.Cleanup(func() { statusMintToken = origMint })

	marker := filepath.Join(t.TempDir(), "postscript-env")
	postScript := "#!/bin/sh\nenv > " + shellQuoteForTest(marker) + "\n"
	dir := writeValidationLoopFullsendDir(t, passingValidationScript, postScript, 1, "")
	outputBase := t.TempDir()

	_, err := runValidationLoopAgent(t, dir, outputBase, repoDir)
	require.NoError(t, err)

	dump, err := os.ReadFile(marker)
	require.NoError(t, err, "the post-script must run; its environment dump is the evidence")
	env := string(dump)

	helperURL, ok := scriptEnvValue(t, env, "FULLSEND_BOT_ROLE_URL")
	require.True(t, ok, "the post-script environment must carry FULLSEND_BOT_ROLE_URL so a script can ask for a comment's bot role (#9602)")
	require.NotEmpty(t, helperURL)
	assert.True(t, strings.HasPrefix(helperURL, "http"), "FULLSEND_BOT_ROLE_URL must be a URL, got %q", helperURL)

	helperToken, ok := scriptEnvValue(t, env, "FULLSEND_BOT_ROLE_TOKEN")
	require.True(t, ok, "the post-script environment must carry FULLSEND_BOT_ROLE_TOKEN")
	require.NotEmpty(t, helperToken)
}
