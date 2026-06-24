package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLabelsEnsureCmd_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "invalid action",
			args:    []string{"--repo", "o/r", "--number", "1", "--action", "nope", "--label", "bug", "--token", "tok"},
			wantErr: `--action must be add or remove`,
		},
		{
			name:    "invalid label",
			args:    []string{"--repo", "o/r", "--number", "1", "--action", "add", "--label", "bad label!", "--token", "tok"},
			wantErr: `invalid label name`,
		},
		{
			name:    "invalid repo",
			args:    []string{"--repo", "nope", "--number", "1", "--action", "add", "--label", "bug", "--token", "tok"},
			wantErr: `--repo must be in owner/repo format`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newLabelsEnsureCmd()
			cmd.SetArgs(tc.args)
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestLabelsEnsureCmd_AddLabel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/repos/o/r/issues/3/labels", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL)

	cmd := newLabelsEnsureCmd()
	cmd.SetArgs([]string{
		"--repo", "o/r",
		"--number", "3",
		"--action", "add",
		"--label", "workflow-change-needed",
		"--token", "test",
	})
	require.NoError(t, cmd.Execute())
}

func TestLabelsEnsureCmd_RemoveLabel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/repos/o/r/issues/4/labels/workflow-change-needed", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL)

	cmd := newLabelsEnsureCmd()
	cmd.SetArgs([]string{
		"--repo", "o/r",
		"--number", "4",
		"--action", "remove",
		"--label", "workflow-change-needed",
		"--token", "test",
	})
	require.NoError(t, cmd.Execute())
}

func TestLabelsCopyCmd_NoMatchingLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.Contains(r.URL.Path, "/issues/1") {
			json.NewEncoder(w).Encode(map[string]any{
				"number":   1,
				"html_url": "https://github.com/o/r/issues/1",
				"labels":   []map[string]any{{"name": "bug"}},
			})
		}
	}))
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL)

	cmd := newLabelsCopyCmd()
	cmd.SetArgs([]string{
		"--repo", "o/r",
		"--from", "1",
		"--to", "2",
		"--token", "test",
	})
	require.NoError(t, cmd.Execute())
}

func TestLabelsCopyCmd_CopiesMatchingLabels(t *testing.T) {
	var posted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/issues/1"):
			json.NewEncoder(w).Encode(map[string]any{
				"number":   1,
				"html_url": "https://github.com/o/r/issues/1",
				"labels": []map[string]any{
					{"name": "workflow-change-allowed"},
					{"name": "bug"},
				},
			})
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/issues/2/labels"):
			posted = true
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL)

	cmd := newLabelsCopyCmd()
	cmd.SetArgs([]string{
		"--repo", "o/r",
		"--from", "1",
		"--to", "2",
		"--token", "test",
	})
	require.NoError(t, cmd.Execute())
	assert.True(t, posted)
}

func TestLabelsCopyCmd_ValidationErrors(t *testing.T) {
	cmd := newLabelsCopyCmd()
	cmd.SetArgs([]string{"--repo", "o/r", "--from", "0", "--to", "2", "--token", "tok"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "positive integers")
}
