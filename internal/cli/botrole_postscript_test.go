package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/botidentity"
	"github.com/fullsend-ai/fullsend/internal/mintclient"
	"github.com/fullsend-ai/fullsend/internal/ui"
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
	require.True(t, ok, "the post-script environment must carry FULLSEND_BOT_ROLE_URL so a script can ask for a bot's registered role (#9602)")
	require.NotEmpty(t, helperURL)
	assert.True(t, strings.HasPrefix(helperURL, "http"), "FULLSEND_BOT_ROLE_URL must be a URL, got %q", helperURL)

	helperToken, ok := scriptEnvValue(t, env, "FULLSEND_BOT_ROLE_TOKEN")
	require.True(t, ok, "the post-script environment must carry FULLSEND_BOT_ROLE_TOKEN")
	require.NotEmpty(t, helperToken)
}

// TestRunAgent_BotRoleHelperResolvesThroughRunsOwnMint is the answer half of
// #9602's first criterion: a lookup a pre-script makes through the run's own
// helper reaches the mint the run resolved (FULLSEND_MINT_URL) for the run's
// target (REPO_FULL_NAME), authenticated with the job's Actions OIDC token.
// The mint and the OIDC endpoint are stubbed on loopback, and
// GITHUB_REPOSITORY is deliberately different from REPO_FULL_NAME so a wrong
// target source would fail the assertion. This pins the resolver and target
// wiring in runAgent, which no helper-level test (a directly constructed
// botRoleHelper) can see.
func TestRunAgent_BotRoleHelperResolvesThroughRunsOwnMint(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available")
	}
	usePreScriptStub(t)
	t.Setenv("FULLSEND_RUNTIME", "codex")
	out := filepath.Join(t.TempDir(), "github-output")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_OUTPUT", out)

	jwt := fakeJWT()
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "bearer oidc-request-token", r.Header.Get("Authorization"))
		assert.Equal(t, "fullsend-mint", r.URL.Query().Get("audience"))
		_ = json.NewEncoder(w).Encode(map[string]string{"value": jwt})
	}))
	t.Cleanup(oidc.Close)

	type mintLookup struct {
		auth string
		req  botidentity.MintBotRoleRequest
	}
	lookups := make(chan mintLookup, 4)
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == botidentity.MintBotRolePath {
			var req botidentity.MintBotRoleRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			lookups <- mintLookup{auth: r.Header.Get("Authorization"), req: req}
			_ = json.NewEncoder(w).Encode(botidentity.MintBotRoleResponse{Outcome: botidentity.MintOutcomeRecognized, Role: "triage"})
			return
		}
		// The agent-token mint the run itself performs.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "unused", "expires_at": "2026-06-15T12:00:00Z"})
	}))
	t.Cleanup(mint.Close)

	t.Setenv("FULLSEND_MINT_URL", mint.URL)
	t.Setenv("GITHUB_REPOSITORY", "fullsend-ai/demo")
	t.Setenv("REPO_FULL_NAME", "fullsend-ai/agents")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", oidc.URL+"/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "oidc-request-token")
	origMint := statusMintToken
	statusMintToken = func(_ context.Context, req mintclient.MintRequest) (*mintclient.MintResult, error) {
		return &mintclient.MintResult{Token: "ghs_test_token_9602", ExpiresAt: "2026-06-15T12:00:00Z"}, nil
	}
	t.Cleanup(func() { statusMintToken = origMint })

	marker := filepath.Join(t.TempDir(), "botrole-answer")
	dir := newSkipHarnessDir(t,
		"curl -fsS -X POST -H \"Authorization: Bearer $FULLSEND_BOT_ROLE_TOKEN\" "+
			"-H \"Content-Type: application/json\" "+
			"-d '{\"login\":\"fullsend-ai-triage[bot]\"}' "+
			"\"$FULLSEND_BOT_ROLE_URL\" > "+shellQuoteForTest(marker)+"\n"+
			"echo \"skipped=true\" >> \"${FULLSEND_PRESCRIPT_OUTPUT}\"\n")

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	require.NoError(t, runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{}))

	answer, err := os.ReadFile(marker)
	require.NoError(t, err, "the pre-script's lookup through the run's helper must produce output")
	assert.Contains(t, string(answer), "triage", "the helper must answer the stubbed mint's registered role")

	require.Len(t, lookups, 1, "exactly one bot-role lookup must reach the mint")
	got := <-lookups
	assert.Equal(t, botidentity.MintBotRoleRequest{ActorLogin: "fullsend-ai-triage[bot]", Repo: "fullsend-ai/agents"}, got.req,
		"the run's helper must resolve for REPO_FULL_NAME, not GITHUB_REPOSITORY")
	assert.Equal(t, "Bearer "+jwt, got.auth, "the lookup must authenticate with the job's OIDC token")
}
