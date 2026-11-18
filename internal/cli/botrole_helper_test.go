package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/botidentity"
)

func TestBotRoleHelper_Recognized(t *testing.T) {
	resolver := &fakeBotRoleResolver{role: "triage"}
	h := &botRoleHelper{resolver: resolver, target: "fullsend-ai/agents"}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bot-role", strings.NewReader(`{"login":"fullsend-ai-triage[bot]"}`))
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp botidentity.MintBotRoleResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, botidentity.MintOutcomeRecognized, resp.Outcome)
	assert.Equal(t, "triage", resp.Role)

	require.Len(t, resolver.queries, 1)
	q := resolver.queries[0]
	assert.Equal(t, "fullsend-ai-triage[bot]", q.Login, "the login the script asked about must reach the resolver")
	assert.Equal(t, "fullsend-ai/agents", q.Target, "the target must be the run's, bound by the helper")
	assert.Equal(t, "github", q.Forge)
}

func TestBotRoleHelper_NoMatch(t *testing.T) {
	resolver := &fakeBotRoleResolver{}
	h := &botRoleHelper{resolver: resolver, target: "fullsend-ai/agents"}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bot-role", strings.NewReader(`{"login":"someone-else[bot]"}`))
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp botidentity.MintBotRoleResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, botidentity.MintOutcomeNoMatch, resp.Outcome)
	assert.Empty(t, resp.Role)
}

// A login that cannot be a GitHub App bot login (no "[bot]" suffix, the
// mint's own botLoginPattern requirement) is a completed no-match, not a
// lookup failure: a human author is the one case the command exists to
// answer, and the mint would reject the login with a 400 anyway (#9602).
func TestBotRoleHelper_HumanLoginIsNoMatchNotFailure(t *testing.T) {
	resolver := &fakeBotRoleResolver{}
	h := &botRoleHelper{resolver: resolver, target: "fullsend-ai/agents"}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bot-role", strings.NewReader(`{"login":"octocat"}`))
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "a human login must answer a completed lookup, not a failure")
	var resp botidentity.MintBotRoleResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, botidentity.MintOutcomeNoMatch, resp.Outcome)
	assert.Empty(t, resp.Role)
	assert.Empty(t, resolver.queries, "a login that cannot be an App bot must not reach the mint")
}

// A lookup that cannot be completed must answer an error status, so the
// script's `fullsend bot-role` fails instead of printing no_match (#9602).
func TestBotRoleHelper_LookupFailureDistinctFromNoMatch(t *testing.T) {
	h := &botRoleHelper{
		resolver: &fakeBotRoleResolver{err: errors.Join(botidentity.ErrLookup, errors.New("mint unreachable"))},
		target:   "fullsend-ai/agents",
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bot-role", strings.NewReader(`{"login":"fullsend-ai-triage[bot]"}`))
	h.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusOK, rec.Code, "a failed lookup must not answer 200")
	var resp botidentity.MintBotRoleResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.NotEqual(t, botidentity.MintOutcomeNoMatch, resp.Outcome)
	assert.NotEmpty(t, resp.Error)
	// The body a script sees carries a fixed message only: the resolver's
	// raw error text (mint URL, OIDC fetch detail) stays runner-side.
	assert.NotContains(t, resp.Error, "mint unreachable")
}

func TestBotRoleHelper_BadRequests(t *testing.T) {
	h := &botRoleHelper{resolver: &fakeBotRoleResolver{}, target: "fullsend-ai/agents"}

	for name, body := range map[string]string{
		"empty login":   `{"login":""}`,
		"missing login": `{}`,
		"invalid json":  `{"login":`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/bot-role", strings.NewReader(body))
			h.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func TestBotRoleHelper_MethodNotAllowed(t *testing.T) {
	h := &botRoleHelper{resolver: &fakeBotRoleResolver{}, target: "fullsend-ai/agents"}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/bot-role", nil)
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestBotRoleHelperEnv_Exports(t *testing.T) {
	assert.Nil(t, botRoleHelperEnv{}.exports(), "no helper running means no exports")

	env := botRoleHelperEnv{url: "http://127.0.0.1:1/bot-role", token: "tok"}
	exports := env.exports()
	require.Len(t, exports, 2)
	assert.Equal(t, "FULLSEND_BOT_ROLE_URL=http://127.0.0.1:1/bot-role", exports[0])
	assert.Equal(t, "FULLSEND_BOT_ROLE_TOKEN=tok", exports[1])
}

// The started service must be loopback, bearer-authenticated, and answer
// end to end through the same wire the `fullsend bot-role` subcommand
// reads.
func TestStartBotRoleHelperService(t *testing.T) {
	resolver := &fakeBotRoleResolver{role: "review"}
	env, shutdown, err := startBotRoleHelperService(resolver, "fullsend-ai/agents")
	require.NoError(t, err)
	t.Cleanup(shutdown)

	require.True(t, strings.HasPrefix(env.url, "http://127.0.0.1:"), "the helper must listen on loopback, got %s", env.url)
	require.NotEmpty(t, env.token)

	// healthz answers without a token.
	healthResp, err := http.Get(strings.TrimSuffix(env.url, "/bot-role") + "/healthz")
	require.NoError(t, err)
	defer healthResp.Body.Close()
	assert.Equal(t, http.StatusOK, healthResp.StatusCode)

	// A lookup without the bearer token is refused.
	unauthed, err := http.Post(env.url, "application/json", strings.NewReader(`{"login":"x[bot]"}`))
	require.NoError(t, err)
	unauthed.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, unauthed.StatusCode)

	// With the token, the answer is the mint's response shape.
	req, err := http.NewRequest(http.MethodPost, env.url, strings.NewReader(`{"login":"fullsend-ai-review[bot]"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+env.token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body botidentity.MintBotRoleResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, botidentity.MintOutcomeRecognized, body.Outcome)
	assert.Equal(t, "review", body.Role)
	require.Len(t, resolver.queries, 1)
	assert.Equal(t, "fullsend-ai/agents", resolver.queries[0].Target)
}
