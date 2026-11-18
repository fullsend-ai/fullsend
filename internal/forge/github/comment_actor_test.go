package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// graphqlServer answers every GraphQL POST with body and records the
// path it was sent to.
func graphqlServer(t *testing.T, status int, body string) (*LiveClient, *string) {
	t.Helper()
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		var req map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New("tok").WithBaseURL(srv.URL), &path
}

func TestIssueOrPullRequestNodeID(t *testing.T) {
	c, path := graphqlServer(t, http.StatusOK, `{"data":{"repository":{"issueOrPullRequest":{"__typename":"PullRequest","id":"PR_1"}}}}`)
	typ, id, err := c.IssueOrPullRequestNodeID(context.Background(), "o", "r", 1)
	require.NoError(t, err)
	assert.Equal(t, "PullRequest", typ)
	assert.Equal(t, "PR_1", id)
	assert.Equal(t, "/graphql", *path)

	for name, body := range map[string]string{
		"not found":       `{"data":{"repository":{"issueOrPullRequest":null}}}`,
		"no repository":   `{"data":{"repository":null}}`,
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
			`{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"fs-triage"},"editor":null,"lastEditedAt":null,"issue":{"id":"I_1"},"pullRequest":null}}}`,
			CommentActor{EntityNodeID: "I_1", ActorType: "Bot", ActorLogin: "fs-triage"},
		},
		"human editor wins": {
			`{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"fs-triage"},"editor":{"__typename":"User","login":"octocat"},"lastEditedAt":"2026-11-18T00:00:00Z","issue":{"id":"I_1"},"pullRequest":null}}}`,
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
		"deleted author": {
			`{"data":{"node":{"__typename":"IssueComment","author":null,"lastEditedAt":null,"issue":{"id":"I_1"}}}}`,
			CommentActor{EntityNodeID: "I_1"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := graphqlServer(t, http.StatusOK, tc.body)
			got, err := c.CommentActor(context.Background(), "C_1")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"not found":           {http.StatusOK, `{"data":{"node":null}}`},
		"a review body":       {http.StatusOK, `{"data":{"node":{"__typename":"PullRequestReview"}}}`},
		"no parent":           {http.StatusOK, `{"data":{"node":{"__typename":"IssueComment","author":null}}}`},
		"edited, editor gone": {http.StatusOK, `{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"x"},"editor":null,"lastEditedAt":"2026-11-18T00:00:00Z","issue":{"id":"I_1"}}}}`},
		"graphql error":       {http.StatusOK, `{"errors":[{"message":"Could not resolve to a node"}]}`},
		"empty data":          {http.StatusOK, `{"data":null}`},
		"not json":            {http.StatusOK, `<html>`},
		"http error":          {http.StatusBadGateway, `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := graphqlServer(t, tc.status, tc.body)
			_, err := c.CommentActor(context.Background(), "C_1")
			assert.Error(t, err)
		})
	}
}
