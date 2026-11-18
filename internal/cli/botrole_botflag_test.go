package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for the `fullsend bot-role --bot` flag (#9602): raw GraphQL
// author.login and gh --json comment/review authors return App bot authors
// without the REST form's [bot] suffix, so a script that knows the author
// is a Bot (GraphQL __typename == "Bot") states it with --bot and the
// command appends the suffix before the lookup — otherwise the helper
// would answer its certain no_match for the suffixless form and a genuine
// registered bot would read as not-a-bot. gh's top-level --json author
// field reports Bot authors as app/<slug>: the script strips that prefix
// before passing --bot.

// botRoleFlagTestServer answers one lookup, asserting the login the request
// carried, and replies with the given outcome.
func botRoleFlagTestServer(t *testing.T, wantLogin, outcome string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, wantLogin, body["login"], "the request must carry the login in REST form")
		resp := map[string]any{"outcome": outcome}
		if outcome == "recognized" {
			resp["role"] = "triage"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBotRoleCmd_BotFlagAppendsSuffixToBareLogin(t *testing.T) {
	srv := botRoleFlagTestServer(t, "fullsend-ai-triage[bot]", "recognized")

	t.Setenv(botRoleScriptEnvURL, srv.URL)
	t.Setenv(botRoleScriptEnvToken, "helper-bearer-token")

	stdout, _, err := runBotRoleForScripts(t, "--bot", "fullsend-ai-triage")
	require.NoError(t, err, "a GraphQL/gh --json Bot author must resolve, not fail")
	assert.Equal(t, "triage\n", stdout)
}

func TestBotRoleCmd_BotFlagKeepsRestFormLogin(t *testing.T) {
	srv := botRoleFlagTestServer(t, "fullsend-ai-triage[bot]", "recognized")

	t.Setenv(botRoleScriptEnvURL, srv.URL)
	t.Setenv(botRoleScriptEnvToken, "helper-bearer-token")

	// A login that already carries the suffix is sent unchanged.
	stdout, _, err := runBotRoleForScripts(t, "--bot", "fullsend-ai-triage[bot]")
	require.NoError(t, err)
	assert.Equal(t, "triage\n", stdout)
}

func TestBotRoleCmd_BareLoginWithoutBotFlagSentUnchanged(t *testing.T) {
	// Without --bot the command must not rewrite the login: the helper
	// answers its own no_match for a suffixless login, and a script that
	// did not state the author is a Bot gets that answer.
	srv := botRoleFlagTestServer(t, "someone", "no_match")

	t.Setenv(botRoleScriptEnvURL, srv.URL)
	t.Setenv(botRoleScriptEnvToken, "helper-bearer-token")

	stdout, _, err := runBotRoleForScripts(t, "someone")
	require.NoError(t, err)
	assert.Equal(t, "no_match\n", stdout)
}
