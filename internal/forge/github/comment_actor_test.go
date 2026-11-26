package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
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
		want forge.CommentActor
	}{
		"bot author on an issue": {
			`{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"fs-triage"},"lastEditedAt":null,"userContentEdits":null,"issue":{"id":"I_1"},"pullRequest":null}}}`,
			forge.CommentActor{EntityNodeID: "I_1", ActorType: "Bot", ActorLogin: "fs-triage"},
		},
		"human editor of the newest revision wins": {
			`{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"fs-triage"},"lastEditedAt":"2026-11-18T00:00:00Z","userContentEdits":{"nodes":[{"editor":{"__typename":"User","login":"octocat"},"editedAt":"2026-11-18T00:00:00Z"}]},"issue":{"id":"I_1"},"pullRequest":null}}}`,
			forge.CommentActor{EntityNodeID: "I_1", ActorType: "User", ActorLogin: "octocat"},
		},
		"edited twice, edit history newest-first": {
			`{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"fs-triage"},"lastEditedAt":"2026-11-18T00:00:00Z","userContentEdits":{"nodes":[{"editor":{"__typename":"User","login":"octocat"},"editedAt":"2026-11-18T00:00:00Z"},{"editor":{"__typename":"User","login":"alice"},"editedAt":"2026-11-17T00:00:00Z"}]},"issue":{"id":"I_1"},"pullRequest":null}}}`,
			forge.CommentActor{EntityNodeID: "I_1", ActorType: "User", ActorLogin: "octocat"},
		},
		"edited twice, edit history oldest-first": {
			`{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"fs-triage"},"lastEditedAt":"2026-11-18T00:00:00Z","userContentEdits":{"nodes":[{"editor":{"__typename":"User","login":"alice"},"editedAt":"2026-11-17T00:00:00Z"},{"editor":{"__typename":"User","login":"octocat"},"editedAt":"2026-11-18T00:00:00Z"}]},"issue":{"id":"I_1"},"pullRequest":null}}}`,
			forge.CommentActor{EntityNodeID: "I_1", ActorType: "User", ActorLogin: "octocat"},
		},
		"conversation comment on a pull request": {
			`{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"fs-review"},"lastEditedAt":null,"issue":{"id":"I_9"},"pullRequest":{"id":"PR_1"}}}}`,
			forge.CommentActor{OnPullRequest: true, EntityNodeID: "PR_1", ActorType: "Bot", ActorLogin: "fs-review"},
		},
		"review-thread comment": {
			`{"data":{"node":{"__typename":"PullRequestReviewComment","author":{"__typename":"Bot","login":"fs-review"},"lastEditedAt":null,"pullRequest":{"id":"PR_1"}}}}`,
			forge.CommentActor{OnPullRequest: true, EntityNodeID: "PR_1", ActorType: "Bot", ActorLogin: "fs-review"},
		},
		"legacy-format comment node id": {
			`{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"fs-triage"},"lastEditedAt":null,"issue":{"id":"I_1"}}}}`,
			forge.CommentActor{EntityNodeID: "I_1", ActorType: "Bot", ActorLogin: "fs-triage"},
		},
		"deleted author": {
			`{"data":{"node":{"__typename":"IssueComment","author":null,"lastEditedAt":null,"issue":{"id":"I_1"}}}}`,
			forge.CommentActor{EntityNodeID: "I_1"},
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
		status       int
		body         string
		wantNotFound bool
	}{
		"not found":                            {http.StatusOK, `{"data":{"node":null}}`, false},
		"a review body":                        {http.StatusOK, `{"data":{"node":{"__typename":"PullRequestReview"}}}`, false},
		"no parent":                            {http.StatusOK, `{"data":{"node":{"__typename":"IssueComment","author":null}}}`, false},
		"edited, editor gone":                  {http.StatusOK, `{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"x"},"lastEditedAt":"2026-11-18T00:00:00Z","userContentEdits":{"nodes":[{"editor":null,"editedAt":"2026-11-18T00:00:00Z"}]},"issue":{"id":"I_1"}}}}`, false},
		"edited, no history":                   {http.StatusOK, `{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"x"},"lastEditedAt":"2026-11-18T00:00:00Z","userContentEdits":{"nodes":[]},"issue":{"id":"I_1"}}}}`, false},
		"newest edit is not the last revision": {http.StatusOK, `{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"x"},"lastEditedAt":"2026-11-18T00:00:00Z","userContentEdits":{"nodes":[{"editor":{"__typename":"Bot","login":"x"},"editedAt":"2026-11-17T00:00:00Z"}]},"issue":{"id":"I_1"}}}}`, false},
		"two edits in the same second":         {http.StatusOK, `{"data":{"node":{"__typename":"IssueComment","author":{"__typename":"Bot","login":"x"},"lastEditedAt":"2026-11-18T00:00:00Z","userContentEdits":{"nodes":[{"editor":{"__typename":"User","login":"octocat"},"editedAt":"2026-11-18T00:00:00Z"},{"editor":{"__typename":"User","login":"alice"},"editedAt":"2026-11-18T00:00:00Z"}]},"issue":{"id":"I_1"}}}}`, false},
		"graphql error":                        {http.StatusOK, `{"errors":[{"message":"Could not resolve to a node"}]}`, false},
		"graphql not-found error":              {http.StatusOK, `{"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a node"}]}`, true},
		"empty data":                           {http.StatusOK, `{"data":null}`, false},
		"not json":                             {http.StatusOK, `<html>`, false},
		"http error":                           {http.StatusBadGateway, `{}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := graphqlServer(t, tc.status, tc.body)
			_, err := c.CommentActor(context.Background(), "C_1")
			if tc.wantNotFound {
				assert.ErrorIs(t, err, forge.ErrNotFound)
			} else {
				assert.Error(t, err)
			}
			if name == "not found" {
				// The comment ID is quoted, like the other commentID
				// errors in the lookup.
				assert.ErrorContains(t, err, `comment "C_1"`)
			}
		})
	}
}

// A NOT_FOUND GraphQL error maps to the forge.ErrNotFound sentinel from any
// position in the envelope, not only the first error, so a response
// carrying additional errors — or null data with NOT_FOUND in a secondary
// position — still reads as "absent" to callers that distinguish it from
// "failed".
func TestGraphQLQuery_NOT_FOUNDInSecondaryPosition(t *testing.T) {
	for name, body := range map[string]string{
		"secondary position": `{"data":null,"errors":[{"message":"first failure","type":"INTERNAL"},{"message":"gone","type":"NOT_FOUND"}]}`,
		"third of three":     `{"data":null,"errors":[{"message":"a","type":"X"},{"message":"b","type":"Y"},{"message":"gone","type":"NOT_FOUND"}]}`,
		"only error":         `{"data":null,"errors":[{"message":"gone","type":"NOT_FOUND"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := graphqlServer(t, http.StatusOK, body)
			_, _, err := c.IssueOrPullRequestNodeID(context.Background(), "o", "r", 1)
			require.Error(t, err)
			assert.ErrorIs(t, err, forge.ErrNotFound, "a NOT_FOUND in any position must keep the sentinel")
		})
	}

	t.Run("no NOT_FOUND keeps the generic error", func(t *testing.T) {
		c, _ := graphqlServer(t, http.StatusOK, `{"data":null,"errors":[{"message":"a","type":"X"},{"message":"b","type":"Y"}]}`)
		_, _, err := c.IssueOrPullRequestNodeID(context.Background(), "o", "r", 1)
		require.Error(t, err)
		assert.NotErrorIs(t, err, forge.ErrNotFound)
	})
}
