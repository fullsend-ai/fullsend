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

// fakeGitHubComment is one comment node the fake GitHub GraphQL API
// answers: its author, its last editor (empty when never edited), and the
// issue or pull request it is on.
type fakeGitHubComment struct {
	authorType, authorLogin string
	editorType, editorLogin string
	issueID, pullRequestID  string
	// reviewThread makes the node a PullRequestReviewComment (on
	// pullRequestID) instead of an IssueComment.
	reviewThread bool
}

// useFakeGitHubGraphQL serves the two GitHub GraphQL questions the
// bot-role helper asks — the node ID and kind of the run's issue or pull
// request, and a comment node's author, edit history and parent — and points
// GITHUB_API_URL at it. entityNodeID is an Issue node ID ("I_…") or a
// PullRequest node ID ("PR_…").
func useFakeGitHubGraphQL(t *testing.T, entityNodeID string, comments map[string]fakeGitHubComment) {
	t.Helper()
	actor := func(typ, login string) any {
		if login == "" {
			return nil
		}
		return map[string]any{"__typename": typ, "login": login}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The run makes other GitHub REST calls through the same API base;
		// only GraphQL is answered here.
		if r.URL.Path != "/graphql" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		w.Header().Set("Content-Type", "application/json")
		if _, ok := req.Variables["number"]; ok {
			typ := "Issue"
			if strings.HasPrefix(entityNodeID, "PR_") {
				typ = "PullRequest"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{
				"issueOrPullRequest": map[string]any{"__typename": typ, "id": entityNodeID}}}})
			return
		}
		c, ok := comments[req.Variables["id"].(string)]
		if !ok {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"node": nil}})
			return
		}
		node := map[string]any{
			"__typename":       "IssueComment",
			"author":           actor(c.authorType, c.authorLogin),
			"lastEditedAt":     nil,
			"userContentEdits": nil,
			"issue":            map[string]any{"id": c.issueID},
			"pullRequest":      nil,
		}
		if c.reviewThread {
			node["__typename"] = "PullRequestReviewComment"
			delete(node, "issue")
		}
		if c.editorLogin != "" {
			// The newest edit-history entry is the revision
			// lastEditedAt names, as GitHub reports it.
			node["lastEditedAt"] = "2026-11-18T12:00:00Z"
			node["userContentEdits"] = map[string]any{"nodes": []any{map[string]any{
				"editor":   actor(c.editorType, c.editorLogin),
				"editedAt": "2026-11-18T12:00:00Z",
			}}}
		}
		if c.pullRequestID != "" {
			node["pullRequest"] = map[string]any{"id": c.pullRequestID}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"node": node}})
	}))
	t.Cleanup(srv.Close)
	// The forge client posts GraphQL to <GITHUB_API_URL>/graphql.
	t.Setenv("GITHUB_API_URL", srv.URL)
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GH_TOKEN", "ghs_fake_graphql_token")
}

// TestRunAgent_BotRoleHelperEnvReachedPreScriptWithoutOIDC is the wiring
// half of #9602's first criterion: inside a `fullsend run` job on GitHub,
// a harness pre-script receives the runner-side bot-role lookup's address
// and bearer token (FULLSEND_BOT_ROLE_URL / FULLSEND_BOT_ROLE_TOKEN, the
// same shape as the runtime fetch service's FULLSEND_FETCH_URL/TOKEN) and
// the canonical ID of the entity the job handles (FULLSEND_ENTITY_ID),
// while the Actions OIDC credentials the lookup authenticates with stay
// runner-side: ACTIONS_ID_TOKEN_* never reaches the script.
func TestRunAgent_BotRoleHelperEnvReachedPreScriptWithoutOIDC(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv("FULLSEND_RUNTIME", "codex")
	out := filepath.Join(t.TempDir(), "github-output")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_OUTPUT", out)

	// Look like a real job for an issue on a target repository: a mint,
	// the Actions OIDC environment, the repository and issue the run
	// handles, and the GitHub API that names the issue's node. The agent
	// token mint is stubbed so the run needs no network beyond loopback.
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "unused", "expires_at": "2026-06-15T12:00:00Z"})
	}))
	t.Cleanup(mint.Close)
	t.Setenv("FULLSEND_MINT_URL", mint.URL)
	t.Setenv("GITHUB_REPOSITORY", "fullsend-ai/agents")
	t.Setenv("REPO_FULL_NAME", "fullsend-ai/agents")
	t.Setenv("ISSUE_NUMBER", "9602")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://oidc.example.invalid/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "oidc-request-token-9602")
	useFakeGitHubGraphQL(t, "I_kwDOAgents9602", nil)
	// statusMintToken is package-level shared state. Mutating it here is
	// safe only because this package's tests are serialized by
	// construction: they call t.Setenv, which forbids t.Parallel, and the
	// var is restored through t.Cleanup even on a failing assertion. A
	// future t.Parallel adoption must lift this constraint first (a
	// per-test stub instead of the shared var).
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
	require.True(t, ok, "the pre-script environment must carry FULLSEND_BOT_ROLE_URL so a script can ask for a comment's bot role (#9602)")
	require.NotEmpty(t, helperURL)
	assert.True(t, strings.HasPrefix(helperURL, "http"), "FULLSEND_BOT_ROLE_URL must be a URL, got %q", helperURL)

	helperToken, ok := scriptEnvValue(t, env, "FULLSEND_BOT_ROLE_TOKEN")
	require.True(t, ok, "the pre-script environment must carry FULLSEND_BOT_ROLE_TOKEN")
	require.NotEmpty(t, helperToken)
	assert.NotEqual(t, "oidc-request-token-9602", helperToken, "the helper token must be its own credential, not the Actions OIDC request token")

	entityID, ok := scriptEnvValue(t, env, "FULLSEND_ENTITY_ID")
	require.True(t, ok, "the pre-script environment must carry FULLSEND_ENTITY_ID, the entity its comments are asked about")
	assert.Equal(t, "fullsend:entity:v1:github:github.com:work_item:I_kwDOAgents9602", entityID,
		"FULLSEND_ENTITY_ID must be the normalized-entity v1 canonical ID of the issue the run handles")

	_, present := scriptEnvValue(t, env, "ACTIONS_ID_TOKEN_REQUEST_URL")
	assert.False(t, present, "ACTIONS_ID_TOKEN_REQUEST_URL must never reach a harness script")
	_, present = scriptEnvValue(t, env, "ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	assert.False(t, present, "ACTIONS_ID_TOKEN_REQUEST_TOKEN must never reach a harness script")
	assert.NotContains(t, env, "oidc-request-token-9602", "no OIDC credential value may leak into the script environment")
}

// TestRunAgent_BotRoleHelperResolvesThroughRunsOwnMint is the answer half of
// #9602's first criterion: a lookup a pre-script makes through the run's own
// helper for a comment on the run's issue reaches the mint the run resolved
// (FULLSEND_MINT_URL) for the comment author's bot login and the run's target
// (REPO_FULL_NAME), authenticated with the job's Actions OIDC token. The
// mint, the OIDC endpoint and the GitHub API are stubbed on loopback, and
// GITHUB_REPOSITORY is deliberately different from REPO_FULL_NAME so a wrong
// target source would fail the assertion. This pins the resolver, target and
// entity wiring in runAgent, which no helper-level test (a directly
// constructed botRoleHelper) can see.
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
	t.Setenv("ISSUE_NUMBER", "9602")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", oidc.URL+"/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "oidc-request-token")
	useFakeGitHubGraphQL(t, "I_kwDOAgents9602", map[string]fakeGitHubComment{
		"IC_kwDOComment1": {authorType: "Bot", authorLogin: "fullsend-ai-triage", issueID: "I_kwDOAgents9602"},
	})
	// statusMintToken is package-level shared state. Mutating it here is
	// safe only because this package's tests are serialized by
	// construction: they call t.Setenv, which forbids t.Parallel, and the
	// var is restored through t.Cleanup even on a failing assertion. A
	// future t.Parallel adoption must lift this constraint first (a
	// per-test stub instead of the shared var).
	origMint := statusMintToken
	statusMintToken = func(_ context.Context, req mintclient.MintRequest) (*mintclient.MintResult, error) {
		return &mintclient.MintResult{Token: "ghs_test_token_9602", ExpiresAt: "2026-06-15T12:00:00Z"}, nil
	}
	t.Cleanup(func() { statusMintToken = origMint })

	marker := filepath.Join(t.TempDir(), "botrole-answer")
	dir := newSkipHarnessDir(t,
		"curl -fsS -X POST -H \"Authorization: Bearer $FULLSEND_BOT_ROLE_TOKEN\" "+
			"-H \"Content-Type: application/json\" "+
			"-d \"{\\\"entity\\\":\\\"$FULLSEND_ENTITY_ID\\\",\\\"comment\\\":\\\"IC_kwDOComment1\\\"}\" "+
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
		"the run's helper must resolve the comment author's bot login for REPO_FULL_NAME, not GITHUB_REPOSITORY")
	assert.Equal(t, "Bearer "+jwt, got.auth, "the lookup must authenticate with the job's OIDC token")
}

// TestRunAgent_BotRoleHelperSkippedWhenLookupImpossible pins the cost side
// of the helper's start condition: a run whose mint resolver could never
// answer a lookup — here because the Actions OIDC request environment is
// absent, so there is no credential to authenticate with — starts no
// helper and makes no run-start entity call. Its pre-script gets no
// FULLSEND_BOT_ROLE_* environment and fails closed at the first ask,
// instead of every such run paying a blocking GraphQL call on the critical
// path before its script.
func TestRunAgent_BotRoleHelperSkippedWhenLookupImpossible(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv("FULLSEND_RUNTIME", "codex")
	out := filepath.Join(t.TempDir(), "github-output")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_OUTPUT", out)

	// Look like a real job for an issue on a target repository: a mint,
	// the repository and issue the run handles, and the GitHub API that
	// would name the issue's node — but no Actions OIDC environment, so
	// no bot-role lookup could ever be answered.
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "unused", "expires_at": "2026-06-15T12:00:00Z"})
	}))
	t.Cleanup(mint.Close)
	t.Setenv("FULLSEND_MINT_URL", mint.URL)
	t.Setenv("GITHUB_REPOSITORY", "fullsend-ai/agents")
	t.Setenv("REPO_FULL_NAME", "fullsend-ai/agents")
	t.Setenv("ISSUE_NUMBER", "9602")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")

	// A GraphQL endpoint that records the entity question; the run must
	// not ask it.
	graphqlAsked := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			graphqlAsked <- struct{}{}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GITHUB_API_URL", srv.URL)
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GH_TOKEN", "ghs_fake_graphql_token")

	// statusMintToken is package-level shared state. Mutating it here is
	// safe only because this package's tests are serialized by
	// construction: they call t.Setenv, which forbids t.Parallel, and the
	// var is restored through t.Cleanup even on a failing assertion. A
	// future t.Parallel adoption must lift this constraint first (a
	// per-test stub instead of the shared var).
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

	for _, key := range []string{"FULLSEND_BOT_ROLE_URL", "FULLSEND_BOT_ROLE_TOKEN", "FULLSEND_ENTITY_ID"} {
		_, present := scriptEnvValue(t, env, key)
		assert.False(t, present, "%s must not reach a script of a run that cannot answer a bot-role lookup", key)
	}
	select {
	case <-graphqlAsked:
		t.Fatal("the run-start entity call must not be made when no bot-role lookup could ever be answered")
	default:
	}
}
