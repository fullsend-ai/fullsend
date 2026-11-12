package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gl "github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

// The verbose state is process-wide, so these tests never call t.Parallel
// and always restore it before returning.

func reposSubcommand(t *testing.T, name string, args ...string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	sub, _, err := newReposCmd().Find([]string{name})
	require.NoError(t, err)
	require.NoError(t, sub.ParseFlags(args))
	var stderr bytes.Buffer
	sub.SetErr(&stderr)
	sub.SetContext(context.Background())
	return sub, &stderr
}

func TestReposCmd_VerboseFlag(t *testing.T) {
	flag := newReposCmd().PersistentFlags().Lookup(reposVerboseFlag)
	require.NotNil(t, flag, "expected --verbose persistent flag")
	assert.Equal(t, "false", flag.DefValue)
}

func TestReposVerboseContext_DefaultWithholdsAndDoesNotLog(t *testing.T) {
	cmd, stderr := reposSubcommand(t, "status")
	ctx, restore := reposVerboseContext(cmd)
	defer restore()

	err := repos.SafeAPIError("listing tokens", errors.New("gitlab api: 403 not allowed"))
	assert.Equal(t, "listing tokens: request failed (server error text withheld)", err.Error())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	client, err := gl.New("test-token", gl.WithBaseURL(srv.URL))
	require.NoError(t, err)
	_, err = client.GetAuthenticatedUserID(ctx)
	require.Error(t, err)
	assert.Empty(t, stderr.String())
}

func TestReposVerboseContext_ShowsScrubbedErrorsAndLogsRequests(t *testing.T) {
	const envToken = "env-gitlab-token-value"
	const flagSecret = "flag-supplied-secret"
	t.Setenv("GITLAB_TOKEN", envToken)
	ghToken := "ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789AB"

	cmd, stderr := reposSubcommand(t, "install", "--verbose")
	ctx, restore := reposVerboseContext(cmd, flagSecret, "")

	err := repos.SafeAPIError("granting access", fmt.Errorf("gitlab api: 403 protected branch: %s %s %s", envToken, flagSecret, ghToken))
	msg := err.Error()
	assert.Contains(t, msg, "granting access: request failed: gitlab api: 403 protected branch")
	assert.NotContains(t, msg, envToken)
	assert.NotContains(t, msg, flagSecret)
	assert.NotContains(t, msg, ghToken)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"403 Forbidden"}`)
	}))
	defer srv.Close()
	client, err := gl.New(envToken, gl.WithBaseURL(srv.URL))
	require.NoError(t, err)
	_, err = client.GetAuthenticatedUserID(ctx)
	require.Error(t, err)
	assert.Equal(t, "gitlab api: GET /api/v4/user -> 403\n", stderr.String())
	assert.NotContains(t, stderr.String(), envToken)

	restore()
	err = repos.SafeAPIError("granting access", errors.New("gitlab api: 403 protected branch"))
	assert.Equal(t, "granting access: request failed (server error text withheld)", err.Error())
}

func TestNewRequestLogWriter_NoResponse(t *testing.T) {
	var buf bytes.Buffer
	newRequestLogWriter(&buf)("POST", "/api/v4/projects/1/access_tokens", 0)
	assert.Equal(t, "gitlab api: POST /api/v4/projects/1/access_tokens -> no response\n", buf.String())
}

func TestRoleTokenValues(t *testing.T) {
	assert.Equal(t, []string{"tok-a", "b=c"}, roleTokenValues([]string{"poller=tok-a", "missing-separator", "coder=b=c"}))
	assert.Empty(t, roleTokenValues(nil))
}

func TestReposSubcommands_AcceptVerbose(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp-test-token")
	manifestPath := writeTestManifest(t, `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  mint_url: https://mint.example.com
  repos: []
`)
	missing := filepath.Join(t.TempDir(), "missing.yaml")

	for name, args := range map[string][]string{
		"status":    {"repos", "status", "--verbose", "--manifest", manifestPath, "--json"},
		"install":   {"repos", "install", "--verbose", "--manifest", missing},
		"uninstall": {"repos", "--verbose", "uninstall", "acme/api", "--manifest", missing},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := newRootCmd()
			cmd.SetArgs(args)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			err := cmd.Execute()
			if name == "status" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			// Every run restores the default withheld behavior.
			assert.Contains(t, repos.SafeAPIError("op", errors.New("server text")).Error(), "server error text withheld")
		})
	}
}
