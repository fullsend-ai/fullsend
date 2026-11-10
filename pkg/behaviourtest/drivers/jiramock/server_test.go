package jiramock

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
)

func TestSearchReturnsAddedIssues(t *testing.T) {
	t.Parallel()
	srv, state := NewServer()
	defer srv.Close()

	state.AddIssue("PROJ-1", []string{"bug"})
	state.AddIssue("PROJ-2", []string{"feature"})

	client, err := jira.New("test-token", jira.WithBaseURL(srv.URL))
	require.NoError(t, err)

	issues, err := client.SearchIssues(context.Background(), "project = PROJ", 0)
	require.NoError(t, err)
	assert.Len(t, issues, 2)
}

func TestCommentsReturned(t *testing.T) {
	t.Parallel()
	srv, state := NewServer()
	defer srv.Close()

	state.AddIssue("PROJ-1", nil)
	state.AddComment("PROJ-1", "/fs-triage check criteria")

	client, err := jira.New("test-token", jira.WithBaseURL(srv.URL))
	require.NoError(t, err)

	comments, err := client.ListComments(context.Background(), "PROJ-1")
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "/fs-triage check criteria", comments[0].Body)
	assert.Equal(t, "commenter-001", comments[0].Author.AccountID)
}

func TestChangelogReturned(t *testing.T) {
	t.Parallel()
	srv, state := NewServer()
	defer srv.Close()

	state.AddIssue("PROJ-1", []string{"backlog"})
	state.AddLabelChange("PROJ-1", "ready-to-code")

	client, err := jira.New("test-token", jira.WithBaseURL(srv.URL))
	require.NoError(t, err)

	entries, err := client.ListChangelog(context.Background(), "PROJ-1")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Len(t, entries[0].Items, 1)
	assert.Equal(t, "labels", entries[0].Items[0].Field)
	assert.Contains(t, entries[0].Items[0].ToString, "ready-to-code")
}

func TestTimestampedActivityHelpers(t *testing.T) {
	t.Parallel()
	srv, state := NewServer()
	defer srv.Close()

	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	state.AddIssue("PROJ-1", nil)
	state.AddCommentFromActorAt("PROJ-1", "/fs-code", "dev-001", "Dev", at)
	state.AddLabelChangeAt("PROJ-1", "ready-to-code", at.Add(time.Minute))

	client, err := jira.New("test-token", jira.WithBaseURL(srv.URL))
	require.NoError(t, err)
	ctx := context.Background()

	comments, err := client.ListComments(ctx, "PROJ-1")
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "dev-001", comments[0].Author.AccountID)
	created, err := time.Parse("2006-01-02T15:04:05.000-0700", comments[0].Created)
	require.NoError(t, err)
	assert.True(t, created.Equal(at), "comment created = %s, want %s", created, at)

	entries, err := client.ListChangelog(ctx, "PROJ-1")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	changed, err := time.Parse("2006-01-02T15:04:05.000-0700", entries[0].Created)
	require.NoError(t, err)
	assert.True(t, changed.Equal(at.Add(time.Minute)), "changelog created = %s", changed)
}

func TestEntityPropertyCRUD(t *testing.T) {
	t.Parallel()
	srv, state := NewServer()
	defer srv.Close()
	_ = state

	client, err := jira.New("test-token", jira.WithBaseURL(srv.URL))
	require.NoError(t, err)
	ctx := context.Background()

	// Set property.
	err = client.SetEntityProperty(ctx, "PROJ-1", "test.prop", map[string]string{"id": "abc"})
	require.NoError(t, err)

	// Get property.
	val, err := client.GetEntityProperty(ctx, "PROJ-1", "test.prop")
	require.NoError(t, err)

	var parsed map[string]string
	require.NoError(t, json.Unmarshal(val, &parsed))
	assert.Equal(t, "abc", parsed["id"])

	// Delete property.
	err = client.DeleteEntityProperty(ctx, "PROJ-1", "test.prop")
	require.NoError(t, err)

	// Get after delete should 404.
	_, err = client.GetEntityProperty(ctx, "PROJ-1", "test.prop")
	require.Error(t, err)
}

func TestProjectRoleActors(t *testing.T) {
	t.Parallel()
	srv, state := NewServer()
	defer srv.Close()
	_ = state

	client, err := jira.New("test-token", jira.WithBaseURL(srv.URL))
	require.NoError(t, err)

	actors, err := client.GetProjectRoleActors(context.Background(), "PROJ")
	require.NoError(t, err)
	require.Contains(t, actors, "Developers")
	assert.True(t, actors["Developers"].DirectUsers["commenter-001"])
	assert.True(t, actors["Developers"].DirectUsers["changer-001"])
}

// TestProjectRoleActorsGroupResolution verifies GetProjectRoleActors and
// GetUserGroups together resolve an actor's role via group membership,
// exercising the same client-call sequence internal/jirapoll's per-actor
// role resolution uses, end-to-end against the mock server.
func TestProjectRoleActorsGroupResolution(t *testing.T) {
	t.Parallel()
	srv, state := NewServer()
	defer srv.Close()

	state.SetRoleGroup("Administrators", "admin-group-001")
	state.SetUserGroups("admin-001", "admin-group-001")

	client, err := jira.New("test-token", jira.WithBaseURL(srv.URL))
	require.NoError(t, err)

	actors, err := client.GetProjectRoleActors(context.Background(), "PROJ")
	require.NoError(t, err)
	require.Contains(t, actors, "Administrators")
	assert.Contains(t, actors["Administrators"].GroupIDs, "admin-group-001")

	groups, err := client.GetUserGroups(context.Background(), "admin-001")
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Equal(t, "admin-group-001", groups[0].GroupID)

	// An actor with no registered groups gets an empty (not error) response.
	groups, err = client.GetUserGroups(context.Background(), "nobody")
	require.NoError(t, err)
	assert.Empty(t, groups)
}

func TestRejectsMissingOrMalformedAuthHeader(t *testing.T) {
	t.Parallel()
	srv, state := NewServer()
	defer srv.Close()

	state.AddIssue("PROJ-1", nil)

	cases := []struct {
		name string
		auth string
	}{
		{name: "missing", auth: ""},
		{name: "malformed", auth: "Token abc123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/rest/api/3/issue/PROJ-1", nil)
			require.NoError(t, err)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		})
	}
}

func TestConcurrentAccess(t *testing.T) {
	t.Parallel()
	srv, state := NewServer()
	defer srv.Close()

	client, err := jira.New("test-token", jira.WithBaseURL(srv.URL))
	require.NoError(t, err)
	ctx := context.Background()

	// Run 12 goroutines manipulating state concurrently.
	const n = 12
	done := make(chan error, n)
	for i := range n {
		go func(i int) {
			key := "PROJ-" + json.Number(json.Number(string(rune('A'+i)))).String()
			state.AddIssue(key, []string{"label"})
			state.AddComment(key, "comment")
			state.AddLabelChange(key, "new-label")

			_, err := client.SearchIssues(ctx, "project = PROJ", 0)
			if err != nil {
				done <- err
				return
			}
			_, err = client.ListComments(ctx, key)
			if err != nil {
				done <- err
				return
			}
			done <- nil
		}(i)
	}
	for range n {
		require.NoError(t, <-done)
	}
}
