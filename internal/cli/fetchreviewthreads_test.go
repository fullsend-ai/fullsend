package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchReviewThreadsCommand(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/graphql", r.URL.Path)
		_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"id":"thread-1","isResolved":true,"path":"main.go","line":12,"originalLine":11,"resolvedBy":{"login":"reviewer"},"comments":{"pageInfo":{"hasNextPage":false},"nodes":[]}}]}}}}}`))
	}))
	defer srv.Close()

	var output bytes.Buffer
	cmd := newFetchReviewThreadsCmd()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"--repo", "owner/repo", "--pr", "42", "--token", "test-token", "--base-url", srv.URL})
	cmd.SetContext(context.Background())

	// The forge client owns its HTTP transport, so point the GitHub client at
	// the TLS test server through the environment used by the command factory.
	// The command's client uses the default transport; install the test server's
	// transport for the duration of this test.
	oldTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })

	require.NoError(t, cmd.Execute())
	var got forge.ReviewThreadPage
	require.NoError(t, json.Unmarshal(output.Bytes(), &got))
	require.Len(t, got.Threads, 1)
	assert.Equal(t, "thread-1", got.Threads[0].ID)
	assert.True(t, got.Threads[0].IsResolved)
}

func TestFetchReviewThreadsCommand_DefaultsToGitHubForge(t *testing.T) {
	cmd := newFetchReviewThreadsCmd()

	flag := cmd.Flags().Lookup("forge")
	require.NotNil(t, flag)
	assert.Equal(t, repos.ForgeGitHub, flag.DefValue)
}

func TestFetchReviewThreadsCommand_ReturnsForgeError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"denied"}`))
	}))
	defer srv.Close()
	oldTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })

	cmd := newFetchReviewThreadsCmd()
	cmd.SetArgs([]string{"--repo", "owner/repo", "--pr", "42", "--token", "test-token", "--base-url", srv.URL})
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list pull request review threads page 1")
}

func TestFetchReviewThreadsCommand_ValidatesFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "non-positive pull request", args: []string{"--repo", "owner/repo", "--pr", "0"}, want: "positive integer"},
		{name: "invalid repository", args: []string{"--repo", "owner", "--pr", "1"}, want: "owner/repo format"},
		{name: "unsupported forge", args: []string{"--repo", "owner/repo", "--pr", "1", "--forge", "bitbucket", "--token", "token"}, want: "unsupported forge"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newFetchReviewThreadsCmd()
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}
