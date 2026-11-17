package tracker_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/tracker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exercise the real decoder and tracker together: GitHub's issue endpoint
// returns PRs too, but this command must never close or comment on one.
func TestGitHubTransitionRejectsPRWithoutWrites(t *testing.T) {
	for _, state := range []string{"open", "closed"} {
		for _, comment := range []tracker.Body{"", "Completed"} {
			t.Run(state+"/"+string(comment), func(t *testing.T) {
				var methods []string
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					methods = append(methods, r.Method)
					if r.Method == http.MethodGet {
						assert.Equal(t, "/repos/owner/repo/issues/42", r.URL.Path)
						_, _ = fmt.Fprintf(w, `{"number":42,"state":%q,"pull_request":{"url":"https://api.github.com/repos/owner/repo/pulls/42"}}`, state)
						return
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{}`))
				}))
				defer srv.Close()
				client := tracker.NewForgeClient(github.New("test-token").WithBaseURL(srv.URL))
				result, err := client.TransitionIssue(context.Background(), "owner/repo", 42, tracker.TransitionOptions{State: "closed", Comment: comment})
				require.ErrorContains(t, err, "pull request, not an issue")
				assert.Nil(t, result)
				assert.Equal(t, []string{http.MethodGet}, methods)
			})
		}
	}
}
