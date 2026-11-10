package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAddIssueLabel(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		existing             bool
		getStatus, putStatus int
	}{
		{"add preserving existing", false, 200, 204},
		{"already present", true, 200, 204},
		{"read denied", false, 403, 204},
		{"write denied", false, 200, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/rest/api/3/issue/PROJ-7", r.URL.Path)
				if r.Method == http.MethodGet {
					w.WriteHeader(tc.getStatus)
					labels := []string{"existing"}
					if tc.existing {
						labels = append(labels, "ai-coding-agent")
					}
					body, _ := json.Marshal(labels)
					fmt.Fprintf(w, `{"fields":{"labels":%s}}`, body)
					return
				}
				require.Equal(t, http.MethodPut, r.Method)
				writes++
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.Equal(t, map[string]any{"update": map[string]any{"labels": []any{map[string]any{"add": "ai-coding-agent"}}}}, body)
				w.WriteHeader(tc.putStatus)
			}))
			defer server.Close()
			client, err := New("token", WithBaseURL(server.URL))
			require.NoError(t, err)
			err = client.AddIssueLabel(context.Background(), "PROJ-7", "ai-coding-agent")
			if tc.getStatus != 200 || tc.putStatus != 204 {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			want := 1
			if tc.existing || tc.getStatus != 200 {
				want = 0
			}
			require.Equal(t, want, writes)
		})
	}
}
