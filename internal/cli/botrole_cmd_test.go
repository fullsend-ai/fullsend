package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Acceptance tests for #9602 (ADR 0107): `fullsend bot-role` is the
// subcommand `fullsend run` makes available to harness pre- and
// post-scripts, so a script can learn whether a given login is a
// registered Fullsend bot — and with which role — for the run's target
// repository, without ever seeing the Actions OIDC credentials
// (`fullsend run` withholds ACTIONS_ID_TOKEN_* from child scripts).
//
// The script-facing contract mirrors `fullsend fetch-skill`:
//
//   - `fullsend bot-role <login>` POSTs the login to the runner-side
//     lookup service discovered through FULLSEND_BOT_ROLE_URL,
//     authenticated with FULLSEND_BOT_ROLE_TOKEN, and prints the
//     registered canonical role or `no_match` to stdout, exiting 0.
//   - A lookup failure (the mint is unreachable or errors) fails the
//     command with empty stdout, never `no_match`, so a caller can fail
//     closed and keep a failed lookup apart from a no-match.
//   - The script cannot choose the target repository: the runner side
//     binds the lookup to the run's target.
const (
	botRoleScriptEnvURL   = "FULLSEND_BOT_ROLE_URL"
	botRoleScriptEnvToken = "FULLSEND_BOT_ROLE_TOKEN"
)

// botRoleCmd returns the registered `bot-role` subcommand, failing the
// test when harness scripts have no such command to call.
func botRoleCmd(t *testing.T) *cobra.Command {
	t.Helper()
	for _, c := range newRootCmd().Commands() {
		if c.Name() == "bot-role" {
			return c
		}
	}
	t.Fatal("`fullsend bot-role` is not registered: harness scripts have no way to resolve a bot's registered role (#9602)")
	return nil
}

// runBotRoleForScripts executes `fullsend bot-role` the way a harness
// pre- or post-script would, and returns its stdout, stderr and error.
func runBotRoleForScripts(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	botRoleCmd(t) // fail fast when the subcommand is missing
	var stdout, stderr bytes.Buffer
	root := newRootCmd()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"bot-role"}, args...))
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func TestBotRoleCmd_RegisteredOnRoot(t *testing.T) {
	cmd := botRoleCmd(t)
	assert.Equal(t, "bot-role", cmd.Name())
}

func TestBotRoleCmd_PrintsRegisteredRole(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method, "the login must travel in a request body, never a query string")
		assert.Equal(t, "Bearer helper-bearer-token", r.Header.Get("Authorization"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "fullsend-ai-triage[bot]", body["login"], "the request must carry the login the script asked about")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"outcome": "recognized", "role": "triage"})
	}))
	t.Cleanup(srv.Close)

	t.Setenv(botRoleScriptEnvURL, srv.URL)
	t.Setenv(botRoleScriptEnvToken, "helper-bearer-token")

	stdout, _, err := runBotRoleForScripts(t, "fullsend-ai-triage[bot]")
	require.NoError(t, err)
	assert.Equal(t, "triage\n", stdout)
}

func TestBotRoleCmd_PrintsNoMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"outcome": "no_match"})
	}))
	t.Cleanup(srv.Close)

	t.Setenv(botRoleScriptEnvURL, srv.URL)
	t.Setenv(botRoleScriptEnvToken, "helper-bearer-token")

	stdout, _, err := runBotRoleForScripts(t, "someone-else[bot]")
	require.NoError(t, err)
	assert.Equal(t, "no_match\n", stdout)
}

// A lookup that cannot be completed must be distinguishable from a
// successful no-match, so a caller can fail closed (#9602 ac_2).
func TestBotRoleCmd_LookupFailureDistinctFromNoMatch(t *testing.T) {
	t.Run("service reports an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "mint unreachable"})
		}))
		t.Cleanup(srv.Close)

		t.Setenv(botRoleScriptEnvURL, srv.URL)
		t.Setenv(botRoleScriptEnvToken, "helper-bearer-token")

		stdout, _, err := runBotRoleForScripts(t, "fullsend-ai-triage[bot]")
		require.Error(t, err, "a lookup failure must fail the command so the caller can fail closed")
		assert.Empty(t, stdout, "a failed lookup must print nothing a caller could read as a result")
		assert.NotContains(t, stdout, "no_match")
	})

	t.Run("helper unreachable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		srv.Close() // a listener that no longer answers, like a mint that is down

		t.Setenv(botRoleScriptEnvURL, srv.URL)
		t.Setenv(botRoleScriptEnvToken, "helper-bearer-token")

		stdout, _, err := runBotRoleForScripts(t, "fullsend-ai-triage[bot]")
		require.Error(t, err)
		assert.Empty(t, stdout)
		assert.NotContains(t, stdout, "no_match")
	})
}

// Outside a `fullsend run` job the helper environment is absent; the
// command must fail rather than answer, so a caller fails closed.
func TestBotRoleCmd_MissingHelperEnvFailsClosed(t *testing.T) {
	t.Setenv(botRoleScriptEnvURL, "")
	t.Setenv(botRoleScriptEnvToken, "")

	stdout, _, err := runBotRoleForScripts(t, "fullsend-ai-triage[bot]")
	require.Error(t, err, "without the runner-side lookup a script must not get an answer")
	assert.NotContains(t, stdout, "no_match")
}

// The lookup target is the run's target repository, bound by the runner
// side; a script must not be able to ask about another repository.
func TestBotRoleCmd_TargetBoundToRunNotScript(t *testing.T) {
	t.Run("no repository flag", func(t *testing.T) {
		for _, flag := range []string{"--repo", "--target"} {
			_, _, err := runBotRoleForScripts(t, flag, "evil/repo", "fullsend-ai-triage[bot]")
			require.Error(t, err, "the command must not accept %s: the target is the run's, not the script's", flag)
			assert.Contains(t, err.Error(), "unknown flag")
		}
	})

	t.Run("exactly one login", func(t *testing.T) {
		_, _, err := runBotRoleForScripts(t, "a[bot]", "b[bot]")
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "unknown command", "the registered command itself must reject a second positional argument")
	})
}
