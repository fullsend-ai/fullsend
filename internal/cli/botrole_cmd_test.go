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
// post-scripts, so a script can learn the registered Fullsend bot role of
// a comment's author on the entity the job handles, without ever seeing
// the Actions OIDC credentials (`fullsend run` withholds
// ACTIONS_ID_TOKEN_* from child scripts).
//
// The question is keyed by the comment, not a login (review on #9661): on
// GitLab and Jira one account can post for several roles, so the answer is
// the role of the actor that produced the comment. The comment is named by
// the canonical entity ID of the normalized-entity v1 spec
// (`fullsend:entity:v1:<system>:<host>:<kind>:<source_id>`, exported to the
// scripts as FULLSEND_ENTITY_ID) and the comment's action ID.
//
// The script-facing contract mirrors `fullsend fetch-skill`:
//
//   - `fullsend bot-role --entity <entity.id> --comment <action id>` POSTs
//     both to the runner-side lookup service discovered through
//     FULLSEND_BOT_ROLE_URL, authenticated with FULLSEND_BOT_ROLE_TOKEN,
//     and prints the registered canonical role or `no_match` to stdout,
//     exiting 0.
//   - A lookup that cannot be completed fails the command with empty
//     stdout, never `no_match`, so a caller can fail closed.
//   - The script cannot choose the target repository: the runner side
//     binds the lookup to the run's target and entity.
const (
	botRoleScriptEnvURL   = "FULLSEND_BOT_ROLE_URL"
	botRoleScriptEnvToken = "FULLSEND_BOT_ROLE_TOKEN"

	testEntityID  = "fullsend:entity:v1:github:github.com:work_item:I_kwDOAgents9602"
	testCommentID = "IC_kwDOComment1"
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

// commentArgs are the flags a script passes for testCommentID on
// testEntityID.
var commentArgs = []string{"--entity", testEntityID, "--comment", testCommentID}

func TestBotRoleCmd_RegisteredOnRoot(t *testing.T) {
	cmd := botRoleCmd(t)
	assert.Equal(t, "bot-role", cmd.Name())
	assert.Equal(t, "bot-role", cmd.Use, "Use carries the command name only; flags are declared with cmd.Flags(), so usage output does not list them twice")
}

func TestBotRoleCmd_PrintsRegisteredRole(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method, "the comment must travel in a request body, never a query string")
		assert.Equal(t, "Bearer helper-bearer-token", r.Header.Get("Authorization"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, testEntityID, body["entity"], "the request must carry the entity the script named")
		assert.Equal(t, testCommentID, body["comment"], "the request must carry the comment the script asked about")
		assert.NotContains(t, body, "login", "the question is the comment's, not a login's")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"outcome": "recognized", "role": "triage"})
	}))
	t.Cleanup(srv.Close)

	t.Setenv(botRoleScriptEnvURL, srv.URL)
	t.Setenv(botRoleScriptEnvToken, "helper-bearer-token")

	stdout, _, err := runBotRoleForScripts(t, commentArgs...)
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

	stdout, _, err := runBotRoleForScripts(t, commentArgs...)
	require.NoError(t, err)
	assert.Equal(t, "no_match\n", stdout)
}

// A lookup that cannot be completed, or a comment the helper refuses (on
// another entity, or on a forge without a trusted lookup), must be
// distinguishable from a successful no-match, so a caller can fail closed
// (#9602 ac_2, ac_3).
func TestBotRoleCmd_LookupFailureDistinctFromNoMatch(t *testing.T) {
	for name, status := range map[string]int{
		"service reports an error":     http.StatusBadGateway,
		"comment refused":              http.StatusBadRequest,
		"no trusted lookup this forge": http.StatusNotImplemented,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "bot-role lookup failed"})
			}))
			t.Cleanup(srv.Close)

			t.Setenv(botRoleScriptEnvURL, srv.URL)
			t.Setenv(botRoleScriptEnvToken, "helper-bearer-token")

			stdout, _, err := runBotRoleForScripts(t, commentArgs...)
			require.Error(t, err, "a lookup failure must fail the command so the caller can fail closed")
			assert.Empty(t, stdout, "a failed lookup must print nothing a caller could read as a result")
		})
	}

	t.Run("helper unreachable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		srv.Close() // a listener that no longer answers

		t.Setenv(botRoleScriptEnvURL, srv.URL)
		t.Setenv(botRoleScriptEnvToken, "helper-bearer-token")

		stdout, _, err := runBotRoleForScripts(t, commentArgs...)
		require.Error(t, err)
		assert.Empty(t, stdout)
	})
}

// Outside a `fullsend run` job the helper environment is absent; the
// command must fail rather than answer, so a caller fails closed.
func TestBotRoleCmd_MissingHelperEnvFailsClosed(t *testing.T) {
	t.Setenv(botRoleScriptEnvURL, "")
	t.Setenv(botRoleScriptEnvToken, "")

	stdout, _, err := runBotRoleForScripts(t, commentArgs...)
	require.Error(t, err, "without the runner-side lookup a script must not get an answer")
	assert.Empty(t, stdout)
}

// The command asks about one comment on one entity; it takes no login and
// no repository, and both flags are required.
func TestBotRoleCmd_ArgumentsAreEntityAndComment(t *testing.T) {
	t.Run("no repository flag", func(t *testing.T) {
		for _, flag := range []string{"--repo", "--target"} {
			_, _, err := runBotRoleForScripts(t, append([]string{flag, "evil/repo"}, commentArgs...)...)
			require.Error(t, err, "the command must not accept %s: the target is the run's, not the script's", flag)
			assert.Contains(t, err.Error(), "unknown flag")
		}
	})

	t.Run("no login argument", func(t *testing.T) {
		_, _, err := runBotRoleForScripts(t, append([]string{"fullsend-ai-triage[bot]"}, commentArgs...)...)
		require.Error(t, err, "a login is not a question the command answers")
	})

	t.Run("both flags required", func(t *testing.T) {
		for name, args := range map[string][]string{
			"no entity":  {"--comment", testCommentID},
			"no comment": {"--entity", testEntityID},
			"neither":    {},
		} {
			t.Run(name, func(t *testing.T) {
				stdout, _, err := runBotRoleForScripts(t, args...)
				require.Error(t, err)
				assert.Empty(t, stdout)
			})
		}
	})
}
