package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/authorization"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestAuthCheck_BlockedExitCode(t *testing.T) {
	client := forge.NewFakeClient()
	client.Issues = map[string]forge.Issue{
		"o/r/1": {Number: 1, Labels: []string{"workflow-change-needed"}},
	}
	cmd := newAuthCheckTestCmd(client)
	cmd.SetArgs([]string{
		"--gate", "workflow-change",
		"--repo", "o/r",
		"--number", "1",
		"--phase", "pre-run",
	})
	err := cmd.Execute()
	require.Error(t, err)
	var ec *exitError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, AuthExitBlocked, ec.ExitCode())
}

func TestAuthCheck_MintJSONElevations(t *testing.T) {
	client := forge.NewFakeClient()
	client.Issues = map[string]forge.Issue{
		"o/r/1": {Number: 1, Labels: []string{"workflow-change-allowed"}},
	}
	client.LabelAppliedAt = map[string]time.Time{
		"o/r/1/workflow-change-allowed": time.Now().Add(-time.Hour),
	}

	var buf bytes.Buffer
	cmd := newAuthCheckTestCmd(client)
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{
		"--gate", "workflow-change",
		"--repo", "o/r",
		"--number", "1",
		"--phase", "mint",
		"--json",
	})
	require.NoError(t, cmd.Execute())

	var payload struct {
		Status     authorization.Status `json:"status"`
		Elevations []string             `json:"elevations"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &payload))
	assert.Equal(t, authorization.StatusOK, payload.Status)
	assert.Equal(t, []string{"workflow-change"}, payload.Elevations)
}

func newAuthCheckTestCmd(client forge.Client) *cobra.Command {
	var gateName, repo, phase string
	var number int
	var jsonOut bool
	cmd := &cobra.Command{
		Use: "check",
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := authorization.GateByName(gateName)
			owner, repoName, _ := splitOwnerRepo(repo)
			result, err := authorization.Evaluate(context.Background(), client, *g, authorization.Target{
				Owner: owner, Repo: repoName, Number: number,
			}, authorization.Phase(phase), authorization.Options{})
			if err != nil {
				return err
			}
			if result.Status != authorization.StatusOK {
				return newExitError(authExitCode(result.Status), "%s", result.Status)
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"status":     result.Status,
					"elevations": result.Elevations,
				})
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&gateName, "gate", "", "")
	cmd.Flags().StringVar(&repo, "repo", "", "")
	cmd.Flags().IntVar(&number, "number", 0, "")
	cmd.Flags().StringVar(&phase, "phase", "", "")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "")
	return cmd
}

func splitOwnerRepo(repo string) (owner, name string, ok bool) {
	for i := 0; i < len(repo); i++ {
		if repo[i] == '/' {
			return repo[:i], repo[i+1:], true
		}
	}
	return "", "", false
}

func TestAuthExitCode(t *testing.T) {
	assert.Equal(t, AuthExitBlocked, authExitCode(authorization.StatusBlocked))
	assert.Equal(t, AuthExitStaleOrUnauth, authExitCode(authorization.StatusStale))
	assert.Equal(t, AuthExitStaleOrUnauth, authExitCode(authorization.StatusUnauthorizedPush))
	assert.Equal(t, 1, authExitCode(authorization.Status("unknown")))
}

func TestNewAuthCmd_RegisteredInRoot(t *testing.T) {
	cmd := newRootCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	assert.True(t, names["auth"])
	assert.True(t, names["labels"])
}

func TestAuthCheckCmd_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "unknown gate",
			args:    []string{"--gate", "nope", "--repo", "o/r", "--number", "1", "--phase", "pre-run", "--token", "tok"},
			wantErr: `unknown gate "nope"`,
		},
		{
			name:    "invalid phase",
			args:    []string{"--gate", "workflow-change", "--repo", "o/r", "--number", "1", "--phase", "bad", "--token", "tok"},
			wantErr: `--phase must be pre-run, mint, or pre-push`,
		},
		{
			name:    "invalid repo",
			args:    []string{"--gate", "workflow-change", "--repo", "bad", "--number", "1", "--phase", "pre-run", "--token", "tok"},
			wantErr: `--repo must be in owner/repo format`,
		},
		{
			name:    "invalid number",
			args:    []string{"--gate", "workflow-change", "--repo", "o/r", "--number", "0", "--phase", "pre-run", "--token", "tok"},
			wantErr: `--number must be a positive integer`,
		},
		{
			name:    "missing changed files path",
			args:    []string{"--gate", "workflow-change", "--repo", "o/r", "--number", "1", "--phase", "pre-push", "--changed-files", "/no/such/file", "--token", "tok"},
			wantErr: "reading changed files",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newAuthCheckCmd()
			cmd.SetArgs(tc.args)
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestAuthCheckCmd_BlockedViaAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"number":   1,
			"html_url": "https://github.com/o/r/issues/1",
			"labels":   []map[string]any{{"name": "workflow-change-needed"}},
		})
	}))
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL)

	cmd := newAuthCheckCmd()
	cmd.SetArgs([]string{
		"--gate", "workflow-change",
		"--repo", "o/r",
		"--number", "1",
		"--phase", "pre-run",
		"--token", "test",
	})
	err := cmd.Execute()
	require.Error(t, err)
	var ec *exitError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, AuthExitBlocked, ec.ExitCode())
}

func TestAuthCheckCmd_MintJSONViaAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/timeline"):
			json.NewEncoder(w).Encode([]map[string]any{
				{
					"event":      "labeled",
					"created_at": time.Now().Add(-time.Hour).Format(time.RFC3339),
					"label":      map[string]string{"name": "workflow-change-allowed"},
				},
			})
		case strings.Contains(r.URL.Path, "/comments"):
			json.NewEncoder(w).Encode([]any{})
		default:
			json.NewEncoder(w).Encode(map[string]any{
				"number":   1,
				"html_url": "https://github.com/o/r/issues/1",
				"labels":   []map[string]any{{"name": "workflow-change-allowed"}},
			})
		}
	}))
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL)

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	cmd := newAuthCheckCmd()
	cmd.SetArgs([]string{
		"--gate", "workflow-change",
		"--repo", "o/r",
		"--number", "1",
		"--phase", "mint",
		"--json",
		"--token", "test",
	})
	require.NoError(t, cmd.Execute())
	require.NoError(t, w.Close())
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)

	var payload struct {
		Status     authorization.Status `json:"status"`
		Elevations []string             `json:"elevations"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &payload))
	assert.Equal(t, authorization.StatusOK, payload.Status)
	assert.Equal(t, []string{"workflow-change"}, payload.Elevations)
}

func TestAuthCheckCmd_ApplyBlockedPostsComment(t *testing.T) {
	var posted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/user":
			json.NewEncoder(w).Encode(map[string]any{"login": "fullsend-bot[bot]"})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/1":
			json.NewEncoder(w).Encode(map[string]any{
				"number":   1,
				"html_url": "https://github.com/o/r/issues/1",
				"labels":   []map[string]any{{"name": "workflow-change-needed"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/1/comments":
			json.NewEncoder(w).Encode([]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/1/comments":
			posted = true
			json.NewEncoder(w).Encode(map[string]any{
				"id":       100,
				"html_url": "https://github.com/o/r/issues/1#issuecomment-100",
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL)

	cmd := newAuthCheckCmd()
	cmd.SetArgs([]string{
		"--gate", "workflow-change",
		"--repo", "o/r",
		"--number", "1",
		"--phase", "pre-run",
		"--apply",
		"--token", "test",
	})
	err := cmd.Execute()
	require.Error(t, err)
	var ec *exitError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, AuthExitBlocked, ec.ExitCode())
	assert.True(t, posted)
}

func TestAuthCheckCmd_MintPrintsElevations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/timeline"):
			json.NewEncoder(w).Encode([]map[string]any{
				{
					"event":      "labeled",
					"created_at": time.Now().Add(-time.Hour).Format(time.RFC3339),
					"label":      map[string]string{"name": "workflow-change-allowed"},
				},
			})
		case strings.Contains(r.URL.Path, "/comments"):
			json.NewEncoder(w).Encode([]any{})
		default:
			json.NewEncoder(w).Encode(map[string]any{
				"number":   1,
				"html_url": "https://github.com/o/r/issues/1",
				"labels":   []map[string]any{{"name": "workflow-change-allowed"}},
			})
		}
	}))
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL)

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	cmd := newAuthCheckCmd()
	cmd.SetArgs([]string{
		"--gate", "workflow-change",
		"--repo", "o/r",
		"--number", "1",
		"--phase", "mint",
		"--token", "test",
	})
	require.NoError(t, cmd.Execute())
	require.NoError(t, w.Close())
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "workflow-change")
}

func TestAuthCheckCmd_PrePushUnauthorizedViaAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"number":   1,
			"html_url": "https://github.com/o/r/issues/1",
			"labels":   []map[string]any{},
		})
	}))
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL)

	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = w.WriteString(".github/workflows/ci.yml\n")
	require.NoError(t, err)
	require.NoError(t, w.Close())
	oldStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	cmd := newAuthCheckCmd()
	cmd.SetArgs([]string{
		"--gate", "workflow-change",
		"--repo", "o/r",
		"--number", "1",
		"--phase", "pre-push",
		"--changed-files", "-",
		"--token", "test",
	})
	err = cmd.Execute()
	require.Error(t, err)
	var ec *exitError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, AuthExitStaleOrUnauth, ec.ExitCode())
}
