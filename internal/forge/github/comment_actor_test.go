package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// graphqlRecorder records the path and headers of the GraphQL requests a
// graphqlServer received.
type graphqlRecorder struct {
	mu     sync.Mutex
	path   string
	header http.Header
}

func (r *graphqlRecorder) snapshot() (string, http.Header) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.path, r.header
}

// graphqlServer answers every GraphQL POST with body and records the
// path it was sent to and the headers it carried.
func graphqlServer(t *testing.T, status int, body string) (*LiveClient, *graphqlRecorder) {
	t.Helper()
	rec := &graphqlRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.path = r.URL.Path
		rec.header = r.Header.Clone()
		rec.mu.Unlock()
		var req map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New("tok").WithBaseURL(srv.URL), rec
}

func TestIssueOrPullRequestNodeID(t *testing.T) {
	c, rec := graphqlServer(t, http.StatusOK, `{"data":{"repository":{"issueOrPullRequest":{"__typename":"PullRequest","id":"PR_1"}}}}`)
	typ, id, err := c.IssueOrPullRequestNodeID(context.Background(), "o", "r", 1)
	require.NoError(t, err)
	assert.Equal(t, "PullRequest", typ)
	assert.Equal(t, "PR_1", id)
	path, sent := rec.snapshot()
	assert.Equal(t, "/graphql", path)
	assert.Equal(t, "1", sent.Get("X-Github-Next-Global-ID"), "entity node IDs must come back in the new global format")

	for name, body := range map[string]string{
		"not found":       `{"data":{"repository":{"issueOrPullRequest":null}}}`,
		"no repository":   `{"data":{"repository":null}}}`,
		"unexpected type": `{"data":{"repository":{"issueOrPullRequest":{"__typename":"Discussion","id":"D_1"}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := graphqlServer(t, http.StatusOK, body)
			_, _, err := c.IssueOrPullRequestNodeID(context.Background(), "o", "r", 1)
			assert.Error(t, err)
		})
	}
}

func TestCommentActor(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want CommentActor
	}{
		"bot author on an issue": {
			`{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"fs-triage"},"lastEditedAt":null,"userContentEdits":null,"issue":{"id":"I_1"},"pullRequest":null}}}`,
			CommentActor{EntityNodeID: "I_1", ActorType: "Bot", ActorLogin: "fs-triage"},
		},
		"human editor of the newest revision wins": {
			`{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"fs-triage"},"lastEditedAt":"2026-11-18T00:00:00Z","userContentEdits":{"nodes":[{"editor":{"__typename":"User","login":"octocat"},"editedAt":"2026-11-18T00:00:00Z"}]},"issue":{"id":"I_1"},"pullRequest":null}}}`,
			CommentActor{EntityNodeID: "I_1", ActorType: "User", ActorLogin: "octocat"},
		},
		"conversation comment on a pull request": {
			`{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"fs-review"},"lastEditedAt":null,"issue":{"id":"I_9"},"pullRequest":{"id":"PR_1"}}}}`,
			CommentActor{OnPullRequest: true, EntityNodeID: "PR_1", ActorType: "Bot", ActorLogin: "fs-review"},
		},
		"review-thread comment": {
			`{"data":{"node":{"__typename":"PullRequestReviewComment","author":{"__typename":"Bot","login":"fs-review"},"lastEditedAt":null,"pullRequest":{"id":"PR_1"}}}}`,
			CommentActor{OnPullRequest: true, EntityNodeID: "PR_1", ActorType: "Bot", ActorLogin: "fs-review"},
		},
		"legacy-format comment node id": {
			`{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"fs-triage"},"lastEditedAt":null,"issue":{"id":"I_1"}}}}`,
			CommentActor{EntityNodeID: "I_1", ActorType: "Bot", ActorLogin: "fs-triage"},
		},
		"deleted author": {
			`{"data":{"node":{"__typename":"IssueComment","author":null,"lastEditedAt":null,"issue":{"id":"I_1"}}}}`,
			CommentActor{EntityNodeID: "I_1"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, rec := graphqlServer(t, http.StatusOK, tc.body)
			commentID := "C_1"
			if name == "legacy-format comment node id" {
				commentID = "MDExOlB1bGxSZXF1ZXN0MzI0NDk1MDI3"
			}
			got, err := c.CommentActor(context.Background(), commentID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			_, sent := rec.snapshot()
			assert.Equal(t, "1", sent.Get("X-Github-Next-Global-ID"), "entity node IDs must come back in the new global format")
		})
	}

	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"not found":                            {http.StatusOK, `{"data":{"node":null}}`},
		"a review body":                        {http.StatusOK, `{"data":{"node":{"__typename":"PullRequestReview"}}}`},
		"no parent":                            {http.StatusOK, `{"data":{"node":{"__typename":"IssueComment","author":null}}}`},
		"edited, editor gone":                  {http.StatusOK, `{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"x"},"lastEditedAt":"2026-11-18T00:00:00Z","userContentEdits":{"nodes":[{"editor":null,"editedAt":"2026-11-18T00:00:00Z"}]},"issue":{"id":"I_1"}}}}`},
		"edited, no history":                   {http.StatusOK, `{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"x"},"lastEditedAt":"2026-11-18T00:00:00Z","userContentEdits":{"nodes":[]},"issue":{"id":"I_1"}}}}`},
		"newest edit is not the last revision": {http.StatusOK, `{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"x"},"lastEditedAt":"2026-11-18T00:00:00Z","userContentEdits":{"nodes":[{"editor":{"__typename":"Bot","login":"x"},"editedAt":"2026-11-17T00:00:00Z"}]},"issue":{"id":"I_1"}}}}`},
		"graphql error":                        {http.StatusOK, `{"errors":[{"message":"Could not resolve to a node"}]}`},
		"empty data":                           {http.StatusOK, `{"data":null}`},
		"not json":                             {http.StatusOK, `<html>`},
		"http error":                           {http.StatusBadGateway, `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := graphqlServer(t, tc.status, tc.body)
			_, err := c.CommentActor(context.Background(), "C_1")
			assert.Error(t, err)
		})
	}
}
