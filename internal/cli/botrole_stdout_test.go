package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for `fullsend bot-role`'s stdout contract (#9602): success means the
// outcome reached the caller, so a stdout write that fails must fail the
// command — never exit 0 with an incomplete answer, which a script would
// read as a trusted empty outcome.

// failingWriter fails every write, as a closed or full stdout would.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed: no space left on device")
}

// botRoleOutcomeServer answers one lookup with the given mint bot-role
// response body.
func botRoleOutcomeServer(t *testing.T, responseBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRunBotRole_StdoutWriteErrorFailsCommand(t *testing.T) {
	srv := botRoleOutcomeServer(t, `{"outcome":"recognized","role":"triage"}`)

	t.Setenv(botRoleHelperURLEnvKey, srv.URL)
	t.Setenv(botRoleHelperTokenEnvKey, "helper-bearer-token")

	err := runBotRole(context.Background(), testEntityID, testCommentID, failingWriter{})
	require.Error(t, err, "a failing stdout writer must fail the command instead of reporting success")
	assert.Contains(t, err.Error(), "failed to print the bot-role outcome")
}

func TestRunBotRole_StdoutWriteErrorOnNoMatchFailsCommand(t *testing.T) {
	// The no-match branch prints through the same writer, so it must fail
	// closed the same way rather than let a script read a missing answer
	// as no_match.
	srv := botRoleOutcomeServer(t, `{"outcome":"no_match"}`)

	t.Setenv(botRoleHelperURLEnvKey, srv.URL)
	t.Setenv(botRoleHelperTokenEnvKey, "helper-bearer-token")

	err := runBotRole(context.Background(), testEntityID, testCommentID, failingWriter{})
	require.Error(t, err, "a failing stdout writer must fail the no-match answer too")
	assert.Contains(t, err.Error(), "failed to print the bot-role outcome")
}
