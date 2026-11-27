package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/entityacquire"
	"github.com/fullsend-ai/fullsend/internal/forge"
	ne "github.com/fullsend-ai/fullsend/internal/normentity"
)

const (
	entityUpdated = "2026-11-24T10:00:00Z"
	entityHead    = "abc123"
)

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// fakeEntityGraphQL serves the entity adapter's GraphQL queries. Collection
// pages are served by connection field; cursors are "c<page index>".
type fakeEntityGraphQL struct {
	t        *testing.T
	typename string
	base     map[string]any
	conns    map[string][]map[string]any
	// extra is served as the second connection of a page read that
	// selects one.
	extra map[string]any
	// pageUpdatedAt overrides the updatedAt reported by page reads.
	pageUpdatedAt string
	// pageID overrides the entity ID reported by page reads.
	pageID string
	// respond, when set, replaces the response to a request.
	respond func(w http.ResponseWriter, req gqlRequest) bool

	mu       sync.Mutex
	requests []gqlRequest
}

func (f *fakeEntityGraphQL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	require.Equal(f.t, "/graphql", r.URL.Path)
	var req gqlRequest
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&req))
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	if f.respond != nil && f.respond(w, req) {
		return
	}
	if _, isBase := req.Variables["author"]; isBase {
		// The query aliases state per fragment (GitHub types the two enums
		// differently), so serve the alias that matches the entity.
		var base map[string]any
		if f.base != nil {
			base = map[string]any{}
			for k, v := range f.base {
				base[k] = v
			}
		}
		if state, ok := base["state"]; ok {
			delete(base, "state")
			alias := "issueState"
			if base["__typename"] == "PullRequest" {
				alias = "pullRequestState"
			}
			assert.Contains(f.t, req.Query, alias+": state")
			base[alias] = state
		}
		if events, ok := base["closedEvents"]; ok {
			delete(base, "closedEvents")
			alias := "issueClosedEvents"
			if base["__typename"] == "PullRequest" {
				alias = "pullRequestClosedEvents"
			}
			assert.Contains(f.t, req.Query, alias+": timelineItems(itemTypes: [CLOSED_EVENT], last: 1)")
			base[alias] = events
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{
			"id": "R_1", "nameWithOwner": "Owner/Repo", "issueOrPullRequest": base,
		}}})
		return
	}
	field := ""
	for name := range f.conns {
		if strings.Contains(req.Query, "conn: "+name+"(") {
			field = name
		}
	}
	require.NotEmpty(f.t, field, "unexpected query: %s", req.Query)
	assert.EqualValues(f.t, entityPageSize, req.Variables["first"])
	idx := 0
	if c, ok := req.Variables["cursor"].(string); ok {
		n, err := strconv.Atoi(strings.TrimPrefix(c, "c"))
		require.NoError(f.t, err)
		idx = n
	}
	pages := f.conns[field]
	require.Less(f.t, idx, len(pages))
	conn := pages[idx]
	next := idx < len(pages)-1
	// Pages are served in read order; a backward connection (newest first)
	// continues through hasPreviousPage and startCursor.
	cursor := map[bool]string{true: "c" + strconv.Itoa(idx+1)}[next]
	conn["pageInfo"] = map[string]any{"hasNextPage": next, "endCursor": cursor, "hasPreviousPage": next, "startCursor": cursor}
	updated := entityUpdated
	if f.pageUpdatedAt != "" {
		updated = f.pageUpdatedAt
	}
	id := f.base["id"]
	if f.pageID != "" {
		id = f.pageID
	}
	entity := map[string]any{
		"__typename": f.typename, "id": id, "updatedAt": updated, "headRefOid": entityHead, "conn": conn,
	}
	if strings.Contains(req.Query, "extra: ") && f.extra != nil {
		entity["extra"] = f.extra
	}
	json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{
		"issueOrPullRequest": entity,
	}}})
}

func (f *fakeEntityGraphQL) pageQueries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		if _, isBase := r.Variables["author"]; !isBase {
			out = append(out, r.Query)
		}
	}
	return out
}

func issueBase() map[string]any {
	return map[string]any{
		"__typename": "Issue", "id": "I_kwDOExample53", "number": 7,
		"url": "https://GitHub.com/Owner/Repo/issues/7", "state": "CLOSED",
		"createdAt": "2026-11-01T09:00:00Z", "updatedAt": entityUpdated,
		"stateReason": "NOT_PLANNED", "closedAt": "2026-11-20T12:30:00Z",
		"closedEvents": closedEvents("2026-11-20T12:30:00Z", map[string]any{"__typename": "User", "login": "carol", "databaseId": 44}),
		"author":       map[string]any{"__typename": "User", "login": "alice", "databaseId": 42},
	}
}

// closedEvents is a base read's most recent ClosedEvent connection.
func closedEvents(createdAt string, actor any) map[string]any {
	return map[string]any{"nodes": []any{map[string]any{"__typename": "ClosedEvent", "createdAt": createdAt, "actor": actor}}}
}

func pullBase() map[string]any {
	return map[string]any{
		"__typename": "PullRequest", "id": "PR_kwDOExample9", "number": 7,
		"url": "https://github.com/owner/repo/pull/7", "state": "MERGED",
		"createdAt": "2026-11-01T09:00:00Z", "updatedAt": entityUpdated, "headRefOid": entityHead,
		"closedAt": "2026-11-20T12:30:00Z", "mergedAt": "2026-11-20T12:30:00Z",
		"mergedBy":    map[string]any{"__typename": "Bot", "login": "merge-bot", "databaseId": 7},
		"author":      map[string]any{"__typename": "User", "login": "bob", "databaseId": 43},
		"baseRefName": "main", "headRefName": "feature", "isCrossRepository": true, "isDraft": false,
		"baseRepository": map[string]any{"nameWithOwner": "Owner/Repo"}, "headRepository": nil,
		"closedEvents": closedEvents("2026-11-20T12:30:00Z", map[string]any{"__typename": "Bot", "login": "merge-bot", "databaseId": 7}),
	}
}

func newEntityTest(t *testing.T, f *fakeEntityGraphQL) *EntityAdapter {
	t.Helper()
	f.t = t
	if f.typename == "" {
		f.typename, _ = f.base["__typename"].(string)
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	a, err := NewEntityAdapter(New("test-token").WithBaseURL(srv.URL), "owner", "repo", 7)
	require.NoError(t, err)
	return a
}

func labelPage(total int, names ...string) map[string]any {
	nodes := make([]any, len(names))
	for i, n := range names {
		nodes[i] = map[string]any{"name": n}
	}
	return map[string]any{"totalCount": total, "nodes": nodes}
}

func TestNewEntityAdapterValidates(t *testing.T) {
	c := New("t")
	_, err := NewEntityAdapter(nil, "o", "r", 1)
	require.Error(t, err)
	_, err = NewEntityAdapter(c, "", "r", 1)
	require.Error(t, err)
	_, err = NewEntityAdapter(c, "o", "r", 0)
	require.Error(t, err)
}

func TestEntityAdapterReadBaseIssue(t *testing.T) {
	f := &fakeEntityGraphQL{base: issueBase()}
	a := newEntityTest(t, f)
	facts := []entityacquire.Need{
		entityacquire.NeedState, entityacquire.NeedTitle, entityacquire.NeedAuthor,
		entityacquire.NeedCreatedAt, entityacquire.NeedUpdatedAt, entityacquire.NeedLifecycle,
		entityacquire.NeedBody, entityacquire.NeedExtensions, entityacquire.NeedWorkItem,
		entityacquire.NeedChangeProposal,
	}
	base, err := a.ReadBase(context.Background(), facts)
	require.NoError(t, err)
	assert.Equal(t, entityUpdated, base.Revision)

	vars := f.requests[0].Variables
	assert.Equal(t, true, vars["author"])
	assert.Equal(t, true, vars["lifecycle"])
	assert.Equal(t, true, vars["workItem"])
	assert.Equal(t, true, vars["changeProposal"])

	e := base.Entity
	assert.Equal(t, ne.KindWorkItem, e.Kind)
	assert.Equal(t, ne.Source{System: ne.SystemGitHub, Host: "github.com", Repository: "owner/repo", RepositoryID: "R_1"}, e.Source)
	assert.Equal(t, ne.ID("fullsend:entity:v1:github:github.com:work_item:I_kwDOExample53"), e.ID)
	assert.Equal(t, "I_kwDOExample53", e.SourceID)
	assert.Equal(t, "#7", e.Key)
	assert.Equal(t, 7, e.Number)

	assert.Equal(t, ne.Known(ne.StateClosed), e.State)
	r, ok := e.Title.UnknownReason()
	assert.True(t, ok)
	assert.Equal(t, ne.ReasonUnsupported, r)
	r, ok = e.Body.UnknownReason()
	assert.True(t, ok)
	assert.Equal(t, ne.ReasonUnsupported, r)
	assert.Nil(t, e.Extensions)
	assert.Equal(t, ne.Known(ne.Timestamp("2026-11-01T09:00:00.000000000Z")), e.CreatedAt)
	assert.Equal(t, ne.Known(ne.Timestamp("2026-11-24T10:00:00.000000000Z")), e.UpdatedAt)

	author, ok := e.Author.Value()
	require.True(t, ok)
	assert.Equal(t, ne.Known("42"), author.ID)
	assert.Equal(t, ne.Known("alice"), author.Login)
	assert.Equal(t, ne.Known(ne.ActorHuman), author.Kind)
	assert.Equal(t, ne.Unknown[ne.Role](ne.ReasonNotLoaded), author.Role)
	assert.Equal(t, ne.Unknown[bool](ne.ReasonNotLoaded), author.RoleVerified)
	assert.True(t, author.BotRole.IsZero())

	assert.Equal(t, ne.Known(ne.Timestamp("2026-11-20T12:30:00.000000000Z")), e.Lifecycle.ClosedAt)
	closer, ok := e.Lifecycle.ClosedBy.Value()
	require.True(t, ok, "the most recent close event at the close time names the closer")
	assert.Equal(t, ne.Known("44"), closer.ID)
	assert.Equal(t, ne.Known("carol"), closer.Login)
	assert.Equal(t, ne.Known(ne.ActorHuman), closer.Kind)
	assert.Equal(t, ne.Unknown[ne.Role](ne.ReasonNotLoaded), closer.Role)
	assert.True(t, e.Lifecycle.MergedAt.IsZero(), "merge facts do not apply to an issue")

	w, ok := e.WorkItem.Value()
	require.True(t, ok)
	assert.Equal(t, ne.Known("not_planned"), w.StateReason)
	assert.Equal(t, ne.Unknown[ne.IssueType](ne.ReasonUnsupported), w.IssueType)
	status, ok := w.SourceStatus.Value()
	require.True(t, ok)
	assert.Equal(t, ne.Known("CLOSED"), status.Name)
	assert.True(t, e.ChangeProposal.IsZero(), "change proposal does not apply to an issue")
}

func TestEntityAdapterReadBasePullRequest(t *testing.T) {
	f := &fakeEntityGraphQL{base: pullBase()}
	a := newEntityTest(t, f)
	base, err := a.ReadBase(context.Background(), []entityacquire.Need{
		entityacquire.NeedState, entityacquire.NeedLifecycle, entityacquire.NeedWorkItem, entityacquire.NeedChangeProposal,
	})
	require.NoError(t, err)
	assert.Equal(t, entityUpdated+"@"+entityHead, base.Revision)
	e := base.Entity
	assert.Equal(t, ne.KindChangeProposal, e.Kind)
	assert.Equal(t, ne.Known(ne.StateMerged), e.State)
	assert.True(t, e.WorkItem.IsZero())
	assert.True(t, e.Author.IsZero(), "an unrequested fact group is absent")

	merger, ok := e.Lifecycle.MergedBy.Value()
	require.True(t, ok)
	assert.Equal(t, ne.Known("7"), merger.ID)
	assert.Equal(t, ne.Known(ne.ActorBot), merger.Kind)
	assert.Equal(t, ne.Known(ne.RoleNone), merger.Role)
	assert.Equal(t, ne.Unknown[string](ne.ReasonNotLoaded), merger.BotRole)
	assert.Equal(t, ne.Known(ne.Timestamp("2026-11-20T12:30:00.000000000Z")), e.Lifecycle.MergedAt)
	assert.Equal(t, ne.Known(ne.Timestamp("2026-11-20T12:30:00.000000000Z")), e.Lifecycle.ClosedAt)
	closer, ok := e.Lifecycle.ClosedBy.Value()
	require.True(t, ok)
	assert.Equal(t, ne.Known("7"), closer.ID)
	assert.Equal(t, ne.Known(ne.ActorBot), closer.Kind)

	cp, ok := e.ChangeProposal.Value()
	require.True(t, ok)
	assert.Equal(t, ne.Known("owner/repo"), cp.BaseRepository)
	assert.True(t, cp.HeadRepository.IsNull(), "a deleted head repository is null")
	assert.Equal(t, ne.Known("main"), cp.BaseRef)
	assert.Equal(t, ne.Known("feature"), cp.HeadRef)
	assert.Equal(t, ne.Known(entityHead), cp.HeadSHA)
	assert.Equal(t, ne.Known(true), cp.IsFork)
	assert.Equal(t, ne.Known(false), cp.Draft)
	assert.Equal(t, ne.Unknown[ne.Approval](ne.ReasonUnsupported), cp.Approval)
}

func TestEntityAdapterReadBaseOnlyRequestedGroups(t *testing.T) {
	pr := pullBase()
	pr["state"] = "OPEN"
	pr["headRepository"] = map[string]any{"nameWithOwner": "Fork/Repo"}
	f := &fakeEntityGraphQL{base: pr}
	a := newEntityTest(t, f)
	base, err := a.ReadBase(context.Background(), nil)
	require.NoError(t, err)
	vars := f.requests[0].Variables
	for _, k := range []string{"author", "lifecycle", "workItem", "changeProposal"} {
		assert.Equal(t, false, vars[k], k)
	}
	assert.True(t, base.Entity.State.IsZero())
	assert.True(t, base.Entity.ChangeProposal.IsZero())

	base, err = a.ReadBase(context.Background(), []entityacquire.Need{entityacquire.NeedLifecycle, entityacquire.NeedChangeProposal})
	require.NoError(t, err)
	l := base.Entity.Lifecycle
	assert.True(t, l.ClosedAt.IsNull())
	assert.True(t, l.ClosedBy.IsNull())
	assert.True(t, l.MergedAt.IsNull())
	assert.True(t, l.MergedBy.IsNull())
	cp, ok := base.Entity.ChangeProposal.Value()
	require.True(t, ok)
	assert.Equal(t, ne.Known("fork/repo"), cp.HeadRepository)
}

func TestEntityAdapterReadBaseUnresolvedValues(t *testing.T) {
	b := pullBase()
	b["state"] = "UNKNOWN_STATE"
	b["createdAt"] = "not a time"
	b["author"] = nil
	b["baseRepository"] = nil
	f := &fakeEntityGraphQL{base: b}
	a := newEntityTest(t, f)
	base, err := a.ReadBase(context.Background(), []entityacquire.Need{
		entityacquire.NeedState, entityacquire.NeedAuthor, entityacquire.NeedCreatedAt,
		entityacquire.NeedLifecycle, entityacquire.NeedChangeProposal,
	})
	require.NoError(t, err)
	e := base.Entity
	assert.Equal(t, ne.Unknown[ne.State](ne.ReasonUnsupported), e.State)
	assert.Equal(t, ne.Unknown[ne.Timestamp](ne.ReasonFailed), e.CreatedAt)
	assert.Equal(t, ne.Unknown[ne.Actor](ne.ReasonUnsupported), e.Author)
	assert.Equal(t, ne.Unknown[ne.ChangeProposal](ne.ReasonFailed), e.ChangeProposal)
	for _, f := range []ne.Field[ne.Timestamp]{e.Lifecycle.ClosedAt, e.Lifecycle.MergedAt} {
		_, unknown := f.UnknownReason()
		assert.True(t, unknown)
	}
	for _, f := range []ne.Field[ne.Actor]{e.Lifecycle.ClosedBy, e.Lifecycle.MergedBy} {
		_, unknown := f.UnknownReason()
		assert.True(t, unknown)
	}
}

func TestEntityAdapterMissingCloseTimes(t *testing.T) {
	b := pullBase()
	b["closedAt"] = nil
	b["mergedAt"] = nil
	b["mergedBy"] = map[string]any{"__typename": "Mannequin", "login": "ghost"}
	f := &fakeEntityGraphQL{base: b}
	a := newEntityTest(t, f)
	base, err := a.ReadBase(context.Background(), []entityacquire.Need{entityacquire.NeedLifecycle})
	require.NoError(t, err)
	l := base.Entity.Lifecycle
	assert.Equal(t, ne.Unknown[ne.Timestamp](ne.ReasonFailed), l.ClosedAt)
	assert.Equal(t, ne.Unknown[ne.Timestamp](ne.ReasonFailed), l.MergedAt)
	merger, ok := l.MergedBy.Value()
	require.True(t, ok)
	assert.Equal(t, ne.Unknown[string](ne.ReasonUnsupported), merger.ID)
	assert.Equal(t, ne.Unknown[ne.ActorKind](ne.ReasonUnsupported), merger.Kind)
}

func TestEntityAdapterClosedByNeedsCurrentCloseEvent(t *testing.T) {
	user := map[string]any{"__typename": "User", "login": "carol", "databaseId": 44}
	for name, tc := range map[string]struct {
		closedAt any
		events   any
		want     ne.UnknownReason
	}{
		"no close event":            {closedAt: "2026-11-20T12:30:00Z", events: map[string]any{"nodes": []any{}}},
		"connection not returned":   {closedAt: "2026-11-20T12:30:00Z", events: nil},
		"event of an earlier close": {closedAt: "2026-11-20T12:30:00Z", events: closedEvents("2026-11-19T08:00:00Z", user)},
		"unparseable event time":    {closedAt: "2026-11-20T12:30:00Z", events: closedEvents("not a time", user)},
		"unresolved close time":     {closedAt: nil, events: closedEvents("2026-11-20T12:30:00Z", user)},
		"unexpected item type": {closedAt: "2026-11-20T12:30:00Z", events: map[string]any{"nodes": []any{
			map[string]any{"__typename": "ReopenedEvent"},
		}}},
		"deleted closer": {closedAt: "2026-11-20T12:30:00Z", events: closedEvents("2026-11-20T12:30:00Z", nil), want: ne.ReasonUnsupported},
	} {
		t.Run(name, func(t *testing.T) {
			for _, b := range []map[string]any{issueBase(), pullBase()} {
				b["closedAt"] = tc.closedAt
				b["closedEvents"] = tc.events
				f := &fakeEntityGraphQL{base: b}
				a := newEntityTest(t, f)
				base, err := a.ReadBase(context.Background(), []entityacquire.Need{entityacquire.NeedLifecycle})
				require.NoError(t, err, "an unresolved closer never fails the read")
				want := tc.want
				if want == "" {
					want = ne.ReasonFailed
				}
				assert.Equal(t, ne.Unknown[ne.Actor](want), base.Entity.Lifecycle.ClosedBy, b["__typename"])
			}
		})
	}
}

func TestEntityAdapterBaseQueryReadsCloseEventWithLifecycle(t *testing.T) {
	for _, alias := range []string{"issueClosedEvents", "pullRequestClosedEvents"} {
		assert.Contains(t, entityBaseQuery, alias+": timelineItems(itemTypes: [CLOSED_EVENT], last: 1) @include(if: $lifecycle)")
	}
	b := issueBase()
	b["state"] = "OPEN"
	f := &fakeEntityGraphQL{base: b}
	a := newEntityTest(t, f)
	base, err := a.ReadBase(context.Background(), []entityacquire.Need{entityacquire.NeedLifecycle})
	require.NoError(t, err)
	assert.True(t, base.Entity.Lifecycle.ClosedBy.IsNull(), "an open entity has no current closer, whatever its history")
}

func TestEntityAdapterAcquireClosedBy(t *testing.T) {
	f := &fakeEntityGraphQL{base: issueBase()}
	res := acquireGitHub(t, f, `entity.lifecycle.closed_by.login == "carol"`)
	assert.Equal(t, entityacquire.StatusSettled, res.Status)
	assert.Equal(t, "true", res.Value.String())
	require.Len(t, f.requests, 1, "the closer is read with the base read")
	assert.Equal(t, true, f.requests[0].Variables["lifecycle"])

	b := issueBase()
	b["closedEvents"] = closedEvents("2026-11-19T08:00:00Z", map[string]any{"__typename": "User", "login": "dave", "databaseId": 45})
	res = acquireGitHub(t, &fakeEntityGraphQL{base: b}, `entity.lifecycle.closed_by.login != "carol"`)
	assert.Equal(t, entityacquire.StatusIncomplete, res.Status, "an earlier close's actor is never the current closer")
	assert.True(t, res.Retryable())
}

func TestEntityAdapterWorkItemOpen(t *testing.T) {
	b := issueBase()
	b["state"] = "OPEN"
	b["stateReason"] = nil
	f := &fakeEntityGraphQL{base: b}
	a := newEntityTest(t, f)
	base, err := a.ReadBase(context.Background(), []entityacquire.Need{entityacquire.NeedWorkItem})
	require.NoError(t, err)
	w, ok := base.Entity.WorkItem.Value()
	require.True(t, ok)
	assert.True(t, w.StateReason.IsNull())
}

func TestEntityAdapterReadBaseErrors(t *testing.T) {
	cases := map[string]struct {
		mutate  func(map[string]any) map[string]any
		respond func(w http.ResponseWriter)
		is      error
	}{
		"not found": {mutate: func(map[string]any) map[string]any { return nil }, is: forge.ErrNotFound},
		"discussion type": {mutate: func(b map[string]any) map[string]any {
			b["__typename"] = "Discussion"
			return b
		}},
		"other number": {mutate: func(b map[string]any) map[string]any {
			b["number"] = 8
			return b
		}},
		"bad url": {mutate: func(b map[string]any) map[string]any {
			b["url"] = "::"
			return b
		}},
		"graphql error": {respond: func(w http.ResponseWriter) {
			json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"type": "NOT_FOUND", "message": "nope"}}})
		}},
		"graphql rate limit": {respond: func(w http.ResponseWriter) {
			json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"type": "RATE_LIMITED", "message": "slow down"}}})
		}, is: entityacquire.ErrLimit},
		"http rate limit": {respond: func(w http.ResponseWriter) {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]any{"message": "API rate limit exceeded"})
		}, is: entityacquire.ErrLimit},
		"server error": {respond: func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"message": "bad"})
		}},
		"malformed body": {respond: func(w http.ResponseWriter) {
			w.Write([]byte("{"))
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeEntityGraphQL{base: issueBase()}
			if tc.mutate != nil {
				f.base = tc.mutate(f.base)
				f.typename = "Issue"
			}
			if tc.respond != nil {
				f.respond = func(w http.ResponseWriter, _ gqlRequest) bool {
					tc.respond(w)
					return true
				}
			}
			a := newEntityTest(t, f)
			a.c.afterFunc = func(time.Duration) <-chan time.Time {
				ch := make(chan time.Time, 1)
				ch <- time.Time{}
				return ch
			}
			_, err := a.ReadBase(context.Background(), []entityacquire.Need{entityacquire.NeedState})
			require.Error(t, err)
			if tc.is != nil {
				assert.ErrorIs(t, err, tc.is)
			}
		})
	}
}

func TestEntityAdapterReadPageLabels(t *testing.T) {
	f := &fakeEntityGraphQL{base: issueBase(), conns: map[string][]map[string]any{
		"labels": {labelPage(3, "bug", "p1"), labelPage(3, "docs")},
	}}
	a := newEntityTest(t, f)
	_, err := a.ReadBase(context.Background(), nil)
	require.NoError(t, err)

	req := entityacquire.PageRequest{Need: entityacquire.NeedLabels, Kind: ne.KindWorkItem, Proof: entityacquire.ProofComplete}
	page, err := a.ReadPage(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, []string{"bug", "p1"}, page.Members.Labels.Known)
	assert.Equal(t, "c1", page.Next)
	assert.Equal(t, entityUpdated, page.Revision)
	assert.False(t, page.Filtered)

	req.Cursor = page.Next
	page, err = a.ReadPage(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, []string{"docs"}, page.Members.Labels.Known)
	assert.Empty(t, page.Next)
	assert.Contains(t, f.pageQueries()[0], "... on Issue")
}

func TestEntityAdapterReadPageAssignees(t *testing.T) {
	f := &fakeEntityGraphQL{base: issueBase(), conns: map[string][]map[string]any{
		"assignedActors": {{"nodes": []any{
			map[string]any{"__typename": "User", "login": "alice", "databaseId": 42},
			map[string]any{"__typename": "Bot", "login": "copilot", "databaseId": 9},
			map[string]any{"__typename": "Mannequin"},
		}}},
	}}
	a := newEntityTest(t, f)
	_, err := a.ReadBase(context.Background(), nil)
	require.NoError(t, err)
	page, err := a.ReadPage(context.Background(), entityacquire.PageRequest{Need: entityacquire.NeedAssignees, Kind: ne.KindWorkItem})
	require.NoError(t, err)
	got := page.Members.Assignees.Known
	require.Len(t, got, 3)
	assert.Equal(t, ne.Known("42"), got[0].ID)
	assert.Equal(t, ne.Known(ne.ActorBot), got[1].Kind)
	assert.Equal(t, ne.Unknown[string](ne.ReasonUnsupported), got[2].ID)
	assert.True(t, got[2].Login.IsNull())
}

func TestEntityAdapterReadPagePullRequestCollections(t *testing.T) {
	f := &fakeEntityGraphQL{base: pullBase(), conns: map[string][]map[string]any{
		"files": {{"totalCount": 2, "nodes": []any{
			map[string]any{"path": "main.go"}, map[string]any{"path": "docs/a b.md"},
		}}},
		"commits": {{"totalCount": 1, "nodes": []any{map[string]any{"commit": map[string]any{"oid": "deadbeef"}}}}},
	}}
	a := newEntityTest(t, f)
	_, err := a.ReadBase(context.Background(), nil)
	require.NoError(t, err)

	page, err := a.ReadPage(context.Background(), entityacquire.PageRequest{Need: entityacquire.NeedChangedFiles, Kind: ne.KindChangeProposal})
	require.NoError(t, err)
	assert.Equal(t, entityUpdated+"@"+entityHead, page.Revision)
	cp, ok := page.Members.ChangeProposal.Value()
	require.True(t, ok)
	require.Len(t, cp.ChangedFiles.Known, 2)
	assert.Equal(t, "docs/a b.md", cp.ChangedFiles.Known[1].Path)
	assert.Equal(t, ne.Unknown[string](ne.ReasonUnsupported), cp.ChangedFiles.Known[0].Patch)

	page, err = a.ReadPage(context.Background(), entityacquire.PageRequest{Need: entityacquire.NeedCommits, Kind: ne.KindChangeProposal})
	require.NoError(t, err)
	cp, ok = page.Members.ChangeProposal.Value()
	require.True(t, ok)
	assert.Equal(t, []string{"deadbeef"}, cp.Commits.Known)
	for _, q := range f.pageQueries() {
		assert.Contains(t, q, "... on PullRequest")
		assert.Contains(t, q, "headRefOid")
	}
}

func TestEntityAdapterReadPageErrors(t *testing.T) {
	commits := func(total int, oid any) map[string]any {
		return map[string]any{"totalCount": total, "nodes": []any{map[string]any{"commit": map[string]any{"oid": oid}}}}
	}
	cases := map[string]struct {
		base    map[string]any
		need    entityacquire.Need
		kind    ne.Kind
		conns   map[string][]map[string]any
		pageID  string
		respond func(w http.ResponseWriter) bool
		noBase  bool
		is      error
	}{
		"before base":   {base: issueBase(), need: entityacquire.NeedLabels, kind: ne.KindWorkItem, noBase: true},
		"kind mismatch": {base: issueBase(), need: entityacquire.NeedLabels, kind: ne.KindChangeProposal},
		"unsupported collection": {
			base: pullBase(), need: entityacquire.NeedThreads, kind: ne.KindChangeProposal, is: entityacquire.ErrUnsupported,
		},
		"inapplicable collection": {
			base: issueBase(), need: entityacquire.NeedCommits, kind: ne.KindWorkItem, is: entityacquire.ErrUnsupported,
		},
		"no total count": {
			base: pullBase(), need: entityacquire.NeedCommits, kind: ne.KindChangeProposal,
			conns: map[string][]map[string]any{"commits": {{"nodes": []any{}}}},
		},
		"commit without id": {
			base: pullBase(), need: entityacquire.NeedCommits, kind: ne.KindChangeProposal,
			conns: map[string][]map[string]any{"commits": {commits(1, "")}},
		},
		"unsafe path": {
			base: pullBase(), need: entityacquire.NeedChangedFiles, kind: ne.KindChangeProposal,
			conns: map[string][]map[string]any{"files": {{"totalCount": 1, "nodes": []any{map[string]any{"path": "../etc/passwd"}}}}},
		},
		"label without name": {
			base: issueBase(), need: entityacquire.NeedLabels, kind: ne.KindWorkItem,
			conns: map[string][]map[string]any{"labels": {labelPage(1, "")}},
		},
		"other entity": {
			base: issueBase(), need: entityacquire.NeedLabels, kind: ne.KindWorkItem, pageID: "I_other",
			conns: map[string][]map[string]any{"labels": {labelPage(0)}},
		},
		"entity gone": {
			base: issueBase(), need: entityacquire.NeedLabels, kind: ne.KindWorkItem, is: forge.ErrNotFound,
			respond: func(w http.ResponseWriter) bool {
				json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": nil}})
				return true
			},
		},
		"no connection": {
			base: issueBase(), need: entityacquire.NeedLabels, kind: ne.KindWorkItem,
			respond: func(w http.ResponseWriter) bool {
				json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{
					"issueOrPullRequest": map[string]any{"__typename": "Issue", "id": "I_kwDOExample53", "updatedAt": entityUpdated},
				}}})
				return true
			},
		},
		"next page without cursor": {
			base: issueBase(), need: entityacquire.NeedLabels, kind: ne.KindWorkItem,
			respond: func(w http.ResponseWriter) bool {
				json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{
					"issueOrPullRequest": map[string]any{
						"__typename": "Issue", "id": "I_kwDOExample53", "updatedAt": entityUpdated,
						"conn": map[string]any{"pageInfo": map[string]any{"hasNextPage": true}, "nodes": []any{}},
					},
				}}})
				return true
			},
		},
		"rate limited": {
			base: issueBase(), need: entityacquire.NeedLabels, kind: ne.KindWorkItem, is: entityacquire.ErrLimit,
			respond: func(w http.ResponseWriter) bool {
				json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"type": "RATE_LIMITED", "message": "slow"}}})
				return true
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeEntityGraphQL{base: tc.base, conns: tc.conns, pageID: tc.pageID}
			if tc.respond != nil {
				f.respond = func(w http.ResponseWriter, req gqlRequest) bool {
					if _, isBase := req.Variables["author"]; isBase {
						return false
					}
					return tc.respond(w)
				}
			}
			a := newEntityTest(t, f)
			if !tc.noBase {
				_, err := a.ReadBase(context.Background(), nil)
				require.NoError(t, err)
			}
			_, err := a.ReadPage(context.Background(), entityacquire.PageRequest{Need: tc.need, Kind: tc.kind})
			require.Error(t, err)
			if tc.is != nil {
				assert.ErrorIs(t, err, tc.is)
			}
		})
	}
}

func TestEntityAdapterRereadResetsCount(t *testing.T) {
	f := &fakeEntityGraphQL{base: issueBase(), conns: map[string][]map[string]any{
		"labels": {labelPage(1, "bug")},
	}}
	a := newEntityTest(t, f)
	_, err := a.ReadBase(context.Background(), nil)
	require.NoError(t, err)
	req := entityacquire.PageRequest{Need: entityacquire.NeedLabels, Kind: ne.KindWorkItem}
	for range 2 {
		_, err := a.ReadPage(context.Background(), req)
		require.NoError(t, err, "a reread from the first page restarts the member count")
	}
}

func TestSafeRepoPath(t *testing.T) {
	for p, want := range map[string]bool{
		"main.go": true, "a/b/c.txt": true, "dir/.hidden": true,
		"": false, "/abs": false, "a//b": false, "a/./b": false, "a/../b": false, "..": false,
		"a\x00b": false, "line\nbreak": false, "trailing/": false,
	} {
		assert.Equal(t, want, safeRepoPath(p), p)
	}
}

// acquireGitHub runs a selective acquisition through the GitHub adapter.
func acquireGitHub(t *testing.T, f *fakeEntityGraphQL, predicate string) *entityacquire.Result {
	t.Helper()
	a := newEntityTest(t, f)
	res, err := entityacquire.Acquire(context.Background(), entityacquire.Request{Predicate: predicate, Adapter: a})
	require.NoError(t, err)
	return res
}

func TestEntityAdapterAcquireStateOnlyReadsNoHistory(t *testing.T) {
	b := issueBase()
	b["state"] = "OPEN"
	f := &fakeEntityGraphQL{base: b, conns: map[string][]map[string]any{"labels": {labelPage(1, "bug")}}}
	res := acquireGitHub(t, f, `entity.state == "open"`)
	assert.Equal(t, entityacquire.StatusSettled, res.Status)
	assert.Equal(t, "true", res.Value.String())
	assert.Equal(t, 0, res.PageReads)
	require.Len(t, f.requests, 1, "a state-only predicate makes only the base read")
	assert.Equal(t, false, f.requests[0].Variables["author"])
}

func TestEntityAdapterAcquireWitnessSettlesPartialLabels(t *testing.T) {
	f := &fakeEntityGraphQL{base: issueBase(), conns: map[string][]map[string]any{
		"labels":         {labelPage(3, "bug", "p1"), labelPage(3, "docs")},
		"assignedActors": {{"nodes": []any{}}},
	}}
	res := acquireGitHub(t, f, `"bug" in entity.labels`)
	assert.Equal(t, entityacquire.StatusSettled, res.Status)
	assert.Equal(t, "true", res.Value.String())
	queries := f.pageQueries()
	require.Len(t, queries, 1, "a known witness settles after the first page")
	assert.Contains(t, queries[0], "conn: labels(")
}

func TestEntityAdapterAcquireNegativeNeedsCompleteLabels(t *testing.T) {
	f := &fakeEntityGraphQL{base: issueBase(), conns: map[string][]map[string]any{
		"labels": {labelPage(3, "bug", "p1"), labelPage(3, "docs")},
	}}
	res := acquireGitHub(t, f, `!("wontfix" in entity.labels)`)
	assert.Equal(t, entityacquire.StatusSettled, res.Status)
	assert.Equal(t, "true", res.Value.String())
	assert.Len(t, f.pageQueries(), 2)
	require.Len(t, res.Reads, 1)
	assert.Equal(t, ne.CoverageComplete, res.Reads[0].Coverage.Status)
}

func TestEntityAdapterAcquireRevisionChangeIsInconsistent(t *testing.T) {
	f := &fakeEntityGraphQL{base: issueBase(), pageUpdatedAt: "2026-11-24T10:05:00Z", conns: map[string][]map[string]any{
		"labels": {labelPage(1, "bug")},
	}}
	res := acquireGitHub(t, f, `"bug" in entity.labels`)
	assert.Equal(t, entityacquire.StatusInconsistent, res.Status)
	assert.True(t, res.Retryable())
	_, err := res.ResolvedEntity()
	require.Error(t, err)
}

func TestEntityAdapterAcquireUnsupportedIsNotEmpty(t *testing.T) {
	f := &fakeEntityGraphQL{base: pullBase()}
	res := acquireGitHub(t, f, `size(entity.threads) == 0`)
	assert.Equal(t, entityacquire.StatusIncomplete, res.Status)
	assert.True(t, res.Retryable())
	require.Len(t, res.Reads, 1)
	assert.Equal(t, ne.CoverageUnsupported, res.Reads[0].Coverage.Status)
	assert.Empty(t, f.pageQueries(), "an unsupported collection makes no provider read")
}

func TestEntityAdapterAcquireResultCapIsPartial(t *testing.T) {
	f := &fakeEntityGraphQL{base: pullBase(), conns: map[string][]map[string]any{
		"commits": {{"totalCount": 300, "nodes": []any{map[string]any{"commit": map[string]any{"oid": "a"}}}}},
	}}
	res := acquireGitHub(t, f, `size(entity.change_proposal.commits) < 250`)
	assert.Equal(t, entityacquire.StatusIncomplete, res.Status)
	require.Len(t, res.Reads, 1)
	assert.Equal(t, ne.CoveragePartial, res.Reads[0].Coverage.Status)
}

func TestEntityAdapterReadPageResultCapKeepsMembers(t *testing.T) {
	f := &fakeEntityGraphQL{base: pullBase(), conns: map[string][]map[string]any{
		"commits": {{"totalCount": 300, "nodes": []any{map[string]any{"commit": map[string]any{"oid": "a"}}}}},
	}}
	a := newEntityTest(t, f)
	_, err := a.ReadBase(context.Background(), nil)
	require.NoError(t, err)
	req := entityacquire.PageRequest{Need: entityacquire.NeedCommits, Kind: ne.KindChangeProposal}
	page, err := a.ReadPage(context.Background(), req)
	require.NoError(t, err, "the capped page keeps its members")
	cp, ok := page.Members.ChangeProposal.Value()
	require.True(t, ok)
	assert.Equal(t, []string{"a"}, cp.Commits.Known)
	require.NotEmpty(t, page.Next, "the page is not terminal, so it can never read complete")

	req.Cursor = page.Next
	_, err = a.ReadPage(context.Background(), req)
	assert.ErrorIs(t, err, entityacquire.ErrLimit, "the limit is reported on the following read")
	assert.Len(t, f.pageQueries(), 1, "reporting the limit makes no provider read")
}

func TestEntityAdapterAcquireWitnessOnCappedPageSettles(t *testing.T) {
	f := &fakeEntityGraphQL{base: pullBase(), conns: map[string][]map[string]any{
		"commits": {{"totalCount": 300, "nodes": []any{map[string]any{"commit": map[string]any{"oid": "a"}}}}},
	}}
	res := acquireGitHub(t, f, `"a" in entity.change_proposal.commits`)
	assert.Equal(t, entityacquire.StatusSettled, res.Status)
	assert.Equal(t, "true", res.Value.String())
}

func TestEntityAdapterBaseQueryAliasesState(t *testing.T) {
	// GitHub types state differently for issues and pull requests, so an
	// unaliased selection in both fragments fails query validation.
	assert.Contains(t, entityBaseQuery, "issueState: state")
	assert.Contains(t, entityBaseQuery, "pullRequestState: state")
	for line := range strings.SplitSeq(entityBaseQuery, "\n") {
		assert.NotEqual(t, "state", strings.TrimSpace(line), "state must be aliased")
	}
}

func reactionNode(id, content string, user any) map[string]any {
	return map[string]any{"id": id, "content": content, "createdAt": "2026-11-02T08:00:00Z", "user": user}
}

func TestEntityAdapterReadPageReactions(t *testing.T) {
	f := &fakeEntityGraphQL{base: issueBase(), conns: map[string][]map[string]any{
		"reactions": {{"totalCount": 3, "nodes": []any{
			reactionNode("REA_1", "THUMBS_UP", map[string]any{"__typename": "User", "login": "alice", "databaseId": 42}),
			reactionNode("REA_2", "SPARKLES", map[string]any{"__typename": "User", "login": "carol", "databaseId": 44}),
			map[string]any{"id": "REA_3", "content": "HEART", "createdAt": "not a time", "user": nil},
		}}},
	}}
	a := newEntityTest(t, f)
	_, err := a.ReadBase(context.Background(), nil)
	require.NoError(t, err)
	page, err := a.ReadPage(context.Background(), entityacquire.PageRequest{Need: entityacquire.NeedReactions, Kind: ne.KindWorkItem})
	require.NoError(t, err)
	assert.Empty(t, page.Next, "a single page is a complete, consistent read")
	assert.Equal(t, entityUpdated, page.Revision)
	got := page.Members.Reactions.Known
	require.Len(t, got, 3)
	assert.Equal(t, "REA_1", got[0].ID)
	assert.Equal(t, ne.Known("+1"), got[0].Content)
	assert.Equal(t, ne.Known("42"), got[0].Actor.ID)
	assert.Equal(t, ne.Known(ne.ActorHuman), got[0].Actor.Kind)
	assert.Equal(t, ne.Known(ne.Timestamp("2026-11-02T08:00:00.000000000Z")), got[0].CreatedAt)
	assert.Equal(t, ne.Unknown[string](ne.ReasonUnsupported), got[1].Content, "an unmapped reaction is unknown")
	assert.Equal(t, ne.Known("heart"), got[2].Content)
	assert.Equal(t, ne.Unknown[ne.Timestamp](ne.ReasonFailed), got[2].CreatedAt)
	assert.Equal(t, ne.Unknown[string](ne.ReasonUnsupported), got[2].Actor.ID, "an unreported reactor stays unresolved")
	assert.Equal(t, ne.Unknown[string](ne.ReasonUnsupported), got[2].Actor.Login)
	assert.Equal(t, ne.Unknown[ne.ActorKind](ne.ReasonUnsupported), got[2].Actor.Kind)
	assert.Contains(t, f.pageQueries()[0], "conn: reactions(first: $first, after: $cursor)")
}

func TestEntityAdapterReadPageReactionsSpanningPagesAreLimited(t *testing.T) {
	user := map[string]any{"__typename": "User", "login": "alice", "databaseId": 42}
	f := &fakeEntityGraphQL{base: issueBase(), conns: map[string][]map[string]any{
		"reactions": {
			{"totalCount": 2, "nodes": []any{reactionNode("REA_1", "EYES", user)}},
			{"totalCount": 2, "nodes": []any{reactionNode("REA_2", "EYES", user)}},
		},
	}}
	a := newEntityTest(t, f)
	_, err := a.ReadBase(context.Background(), nil)
	require.NoError(t, err)
	req := entityacquire.PageRequest{Need: entityacquire.NeedReactions, Kind: ne.KindWorkItem}
	page, err := a.ReadPage(context.Background(), req)
	require.NoError(t, err, "the first page keeps its members as witnesses")
	require.Len(t, page.Members.Reactions.Known, 1)
	require.NotEmpty(t, page.Next)

	req.Cursor = page.Next
	_, err = a.ReadPage(context.Background(), req)
	assert.ErrorIs(t, err, entityacquire.ErrLimit, "reaction changes do not advance the revision, so later pages are never read")
	assert.Len(t, f.pageQueries(), 1)
}

func labeledEvent(id, label, at string, actor any) map[string]any {
	return map[string]any{"__typename": "LabeledEvent", "id": id, "createdAt": at, "label": map[string]any{"name": label}, "actor": actor}
}

func unlabeledEvent(id, label, at string) map[string]any {
	return map[string]any{"__typename": "UnlabeledEvent", "id": id, "createdAt": at, "label": map[string]any{"name": label}}
}

// labelTimeline serves label events newest page first; each page lists its
// events oldest first, as GitHub does.
func labelTimeline() map[string][]map[string]any {
	alice := map[string]any{"__typename": "User", "login": "alice", "databaseId": 42}
	bot := map[string]any{"__typename": "Bot", "login": "triage-bot", "databaseId": 9}
	return map[string][]map[string]any{"timelineItems": {
		{"nodes": []any{
			labeledEvent("LE_3", "bug", "2026-11-03T00:00:00Z", alice),
			unlabeledEvent("UE_4", "wontfix", "2026-11-04T00:00:00Z"),
			labeledEvent("LE_5", "p1", "2026-11-05T00:00:00Z", bot),
		}},
		{"nodes": []any{
			labeledEvent("LE_1", "wontfix", "2026-11-01T00:00:00Z", alice),
			labeledEvent("LE_2", "bug", "2026-11-02T00:00:00Z", nil),
			labeledEvent("LE_6", "docs", "2026-11-02T12:00:00Z", nil),
		}},
	}}
}

func TestEntityAdapterReadPageLabelApplications(t *testing.T) {
	f := &fakeEntityGraphQL{base: issueBase(), conns: labelTimeline()}
	a := newEntityTest(t, f)
	_, err := a.ReadBase(context.Background(), nil)
	require.NoError(t, err)
	req := entityacquire.PageRequest{Need: entityacquire.NeedLabelApplications, Kind: ne.KindWorkItem}
	page, err := a.ReadPage(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "c1", page.Next)
	assert.Equal(t, entityUpdated, page.Revision)
	got := page.Members.LabelApplications.Known
	require.Len(t, got, 2, "wontfix was removed after its last application")
	assert.Equal(t, "LE_5", got[0].ID)
	assert.Equal(t, ne.Known("p1"), got[0].Label)
	assert.Equal(t, ne.Known(ne.ActorBot), got[0].Actor.Kind)
	assert.Equal(t, "LE_3", got[1].ID)
	assert.Equal(t, ne.Known("bug"), got[1].Label)
	assert.Equal(t, ne.Timestamp("2026-11-03T00:00:00.000000000Z"), got[1].CreatedAt)
	assert.Equal(t, ne.Known(got[1].CreatedAt), got[1].RevisedAt)
	assert.Equal(t, ne.Known("42"), got[1].Actor.ID)
	assert.Equal(t, ne.Unknown[bool](ne.ReasonNotLoaded), got[1].IsEntityAuthor)

	req.Cursor = page.Next
	page, err = a.ReadPage(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, page.Next)
	got = page.Members.LabelApplications.Known
	require.Len(t, got, 1, "older events of labels already decided are skipped")
	assert.Equal(t, "LE_6", got[0].ID)
	assert.Equal(t, ne.Unknown[string](ne.ReasonUnsupported), got[0].Actor.ID, "an unreported actor stays unresolved")

	q := f.pageQueries()[0]
	assert.Contains(t, q, "conn: timelineItems(itemTypes: [LABELED_EVENT, UNLABELED_EVENT], last: $first, before: $cursor)")
	assert.Contains(t, q, "pageInfo { hasPreviousPage startCursor }")
}

func TestEntityAdapterLabelApplicationsRereadRestarts(t *testing.T) {
	f := &fakeEntityGraphQL{base: issueBase(), conns: labelTimeline()}
	a := newEntityTest(t, f)
	_, err := a.ReadBase(context.Background(), nil)
	require.NoError(t, err)
	req := entityacquire.PageRequest{Need: entityacquire.NeedLabelApplications, Kind: ne.KindWorkItem}
	for range 2 {
		page, err := a.ReadPage(context.Background(), req)
		require.NoError(t, err)
		assert.Len(t, page.Members.LabelApplications.Known, 2, "a reread from the newest page forgets earlier labels")
	}
}

func TestEntityAdapterReadPageCollectionErrors(t *testing.T) {
	cases := map[string]struct {
		need  entityacquire.Need
		conns map[string][]map[string]any
	}{
		"reaction without id": {
			need:  entityacquire.NeedReactions,
			conns: map[string][]map[string]any{"reactions": {{"totalCount": 1, "nodes": []any{reactionNode("", "EYES", nil)}}}},
		},
		"reaction result cap": {
			need:  entityacquire.NeedReactions,
			conns: map[string][]map[string]any{"reactions": {{"totalCount": 5, "nodes": []any{}}}},
		},
		"label event without label": {
			need: entityacquire.NeedLabelApplications,
			conns: map[string][]map[string]any{"timelineItems": {{"nodes": []any{
				map[string]any{"__typename": "LabeledEvent", "id": "LE_1", "createdAt": "2026-11-01T00:00:00Z", "label": nil},
			}}}},
		},
		"label event without id": {
			need:  entityacquire.NeedLabelApplications,
			conns: map[string][]map[string]any{"timelineItems": {{"nodes": []any{labeledEvent("", "bug", "2026-11-01T00:00:00Z", nil)}}}},
		},
		"label event without time": {
			need:  entityacquire.NeedLabelApplications,
			conns: map[string][]map[string]any{"timelineItems": {{"nodes": []any{labeledEvent("LE_1", "bug", "yesterday", nil)}}}},
		},
		"unexpected timeline item": {
			need: entityacquire.NeedLabelApplications,
			conns: map[string][]map[string]any{"timelineItems": {{"nodes": []any{
				map[string]any{"__typename": "ClosedEvent", "id": "CE_1", "createdAt": "2026-11-01T00:00:00Z"},
			}}}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeEntityGraphQL{base: issueBase(), conns: tc.conns}
			a := newEntityTest(t, f)
			_, err := a.ReadBase(context.Background(), nil)
			require.NoError(t, err)
			req := entityacquire.PageRequest{Need: tc.need, Kind: ne.KindWorkItem}
			page, err := a.ReadPage(context.Background(), req)
			if err == nil {
				// A result cap hands back the members read so far and
				// reports the limit on the following read.
				req.Cursor = page.Next
				_, err = a.ReadPage(context.Background(), req)
				assert.ErrorIs(t, err, entityacquire.ErrLimit)
			}
			require.Error(t, err)
		})
	}
}

func TestEntityAdapterAcquireLabelApplicationWitnessSettles(t *testing.T) {
	f := &fakeEntityGraphQL{base: issueBase(), conns: labelTimeline()}
	res := acquireGitHub(t, f, `entity.label_applications.exists(l, l.label == "bug")`)
	assert.Equal(t, entityacquire.StatusSettled, res.Status)
	assert.Equal(t, "true", res.Value.String())
	queries := f.pageQueries()
	require.Len(t, queries, 1, "the newest page holds the witness")
	assert.Contains(t, queries[0], "conn: timelineItems(")
}

func TestEntityAdapterAcquireLabelApplicationsAreSortedByTimeThenID(t *testing.T) {
	t.Run("multi-page read is ordered across pages", func(t *testing.T) {
		f := &fakeEntityGraphQL{base: issueBase(), conns: labelTimeline()}
		res := acquireGitHub(t, f, `entity.label_applications[0].label == "docs" && entity.label_applications[1].label == "bug" && entity.label_applications[2].label == "p1"`)
		assert.Equal(t, entityacquire.StatusSettled, res.Status)
		assert.Equal(t, "true", res.Value.String())
		assert.Len(t, f.pageQueries(), 2)
		require.Len(t, res.Reads, 1)
		assert.Equal(t, ne.CoverageComplete, res.Reads[0].Coverage.Status)
	})
	t.Run("single page with equal timestamps is ordered by ID", func(t *testing.T) {
		f := &fakeEntityGraphQL{base: issueBase(), conns: map[string][]map[string]any{"timelineItems": {
			{"nodes": []any{
				labeledEvent("LE_B", "bug", "2026-11-01T00:00:00Z", nil),
				labeledEvent("LE_A", "docs", "2026-11-01T00:00:00Z", nil),
				labeledEvent("LE_C", "p1", "2026-10-31T00:00:00Z", nil),
			}},
		}}}
		res := acquireGitHub(t, f, `entity.label_applications[0].id == "LE_C" && entity.label_applications[1].id == "LE_A" && entity.label_applications[2].id == "LE_B"`)
		assert.Equal(t, entityacquire.StatusSettled, res.Status)
		assert.Equal(t, "true", res.Value.String())
		require.Len(t, res.Reads, 1)
		assert.Equal(t, ne.CoverageComplete, res.Reads[0].Coverage.Status)
	})
	t.Run("equal timestamps across pages are ordered by ID", func(t *testing.T) {
		f := &fakeEntityGraphQL{base: issueBase(), conns: map[string][]map[string]any{"timelineItems": {
			{"nodes": []any{labeledEvent("LE_B", "bug", "2026-11-01T00:00:00Z", nil)}},
			{"nodes": []any{labeledEvent("LE_A", "docs", "2026-11-01T00:00:00Z", nil)}},
		}}}
		res := acquireGitHub(t, f, `entity.label_applications[0].id == "LE_A" && entity.label_applications[1].id == "LE_B"`)
		assert.Equal(t, entityacquire.StatusSettled, res.Status)
		assert.Equal(t, "true", res.Value.String())
	})
}

func TestEntityAdapterAcquireRemovedLabelNeedsCompleteApplications(t *testing.T) {
	f := &fakeEntityGraphQL{base: pullBase(), conns: labelTimeline()}
	res := acquireGitHub(t, f, `!entity.label_applications.exists(l, l.label == "wontfix")`)
	assert.Equal(t, entityacquire.StatusSettled, res.Status)
	assert.Equal(t, "true", res.Value.String())
	assert.Len(t, f.pageQueries(), 2)
	require.Len(t, res.Reads, 1)
	assert.Equal(t, ne.CoverageComplete, res.Reads[0].Coverage.Status)
	assert.Equal(t, entityUpdated+"@"+entityHead, res.Reads[0].Coverage.SourceRevision)
}

func TestEntityAdapterAcquireReactions(t *testing.T) {
	user := map[string]any{"__typename": "User", "login": "alice", "databaseId": 42}
	t.Run("empty single page is complete", func(t *testing.T) {
		f := &fakeEntityGraphQL{base: issueBase(), conns: map[string][]map[string]any{
			"reactions": {{"totalCount": 0, "nodes": []any{}}},
		}}
		res := acquireGitHub(t, f, `size(entity.reactions) == 0`)
		assert.Equal(t, entityacquire.StatusSettled, res.Status)
		assert.Equal(t, "true", res.Value.String())
	})
	t.Run("multi-page count stays partial", func(t *testing.T) {
		f := &fakeEntityGraphQL{base: issueBase(), conns: map[string][]map[string]any{
			"reactions": {
				{"totalCount": 2, "nodes": []any{reactionNode("REA_1", "EYES", user)}},
				{"totalCount": 2, "nodes": []any{reactionNode("REA_2", "EYES", user)}},
			},
		}}
		res := acquireGitHub(t, f, `size(entity.reactions) == 2`)
		assert.Equal(t, entityacquire.StatusIncomplete, res.Status)
		assert.True(t, res.Retryable())
		require.Len(t, res.Reads, 1)
		assert.Equal(t, ne.CoveragePartial, res.Reads[0].Coverage.Status)
	})
	t.Run("witness on first page settles", func(t *testing.T) {
		f := &fakeEntityGraphQL{base: issueBase(), conns: map[string][]map[string]any{
			"reactions": {
				{"totalCount": 2, "nodes": []any{reactionNode("REA_1", "THUMBS_UP", user)}},
				{"totalCount": 2, "nodes": []any{reactionNode("REA_2", "EYES", user)}},
			},
		}}
		res := acquireGitHub(t, f, `entity.reactions.exists(r, r.content == "+1")`)
		assert.Equal(t, entityacquire.StatusSettled, res.Status)
		assert.Equal(t, "true", res.Value.String())
	})
}

func ghUser(login string, id int) map[string]any {
	return map[string]any{"__typename": "User", "login": login, "databaseId": id}
}

func reviewNode(state string, author any) map[string]any {
	return map[string]any{"state": state, "submittedAt": "2026-11-03T08:00:00Z", "author": author}
}

func TestEntityAdapterReadPageCurrentApprovals(t *testing.T) {
	f := &fakeEntityGraphQL{base: pullBase(), conns: map[string][]map[string]any{
		"latestOpinionatedReviews": {{"totalCount": 7, "nodes": []any{
			reviewNode("APPROVED", map[string]any{"__typename": "Mannequin", "login": "imported"}),
			reviewNode("APPROVED", ghUser("dave", 50)),
			reviewNode("CHANGES_REQUESTED", ghUser("carol", 44)),
			reviewNode("APPROVED", nil),
			reviewNode("APPROVED", map[string]any{"__typename": "Bot", "login": "review-bot", "databaseId": 9}),
			reviewNode("DISMISSED", ghUser("erin", 45)),
			map[string]any{"state": "APPROVED", "submittedAt": nil, "author": ghUser("frank", 100)},
		}}},
	}}
	a := newEntityTest(t, f)
	_, err := a.ReadBase(context.Background(), nil)
	require.NoError(t, err)
	page, err := a.ReadPage(context.Background(), entityacquire.PageRequest{Need: entityacquire.NeedCurrentApprovals, Kind: ne.KindChangeProposal})
	require.NoError(t, err)
	assert.Empty(t, page.Next, "a single page is a complete, consistent read")
	assert.Equal(t, entityUpdated+"@"+entityHead, page.Revision)
	got := page.Members.CurrentApprovals.Known
	require.Len(t, got, 5, "changes requested and dismissed reviews are not approvals")
	assert.Equal(t, ne.Known("100"), got[0].Actor.ID, "sorted by actor ID in byte order")
	assert.Equal(t, ne.Unknown[ne.Timestamp](ne.ReasonFailed), got[0].ApprovedAt)
	assert.Equal(t, ne.Known("50"), got[1].Actor.ID)
	assert.Equal(t, ne.Known("dave"), got[1].Actor.Login)
	assert.Equal(t, ne.Known(ne.Timestamp("2026-11-03T08:00:00.000000000Z")), got[1].ApprovedAt)
	assert.Equal(t, ne.Unknown[bool](ne.ReasonNotLoaded), got[1].IsEntityAuthor)
	assert.Equal(t, ne.Known("9"), got[2].Actor.ID)
	assert.Equal(t, ne.Known(ne.ActorBot), got[2].Actor.Kind)
	assert.Equal(t, ne.Known("imported"), got[3].Actor.Login, "approvers without an ID follow, in source order")
	assert.Equal(t, ne.Unknown[string](ne.ReasonUnsupported), got[3].Actor.ID)
	assert.Equal(t, ne.Unknown[string](ne.ReasonUnsupported), got[4].Actor.Login, "an unreported approver stays unresolved")
	q := f.pageQueries()[0]
	assert.Contains(t, q, "conn: latestOpinionatedReviews(first: $first, after: $cursor)")
	assert.NotContains(t, q, "extra: ")
}

func TestEntityAdapterReadPageReviewers(t *testing.T) {
	f := &fakeEntityGraphQL{
		base: pullBase(),
		conns: map[string][]map[string]any{"reviewRequests": {{"totalCount": 4, "nodes": []any{
			map[string]any{"requestedReviewer": ghUser("alice", 42)},
			map[string]any{"requestedReviewer": map[string]any{"__typename": "Team", "databaseId": 7, "name": "Core"}},
			map[string]any{"requestedReviewer": nil},
			map[string]any{"requestedReviewer": map[string]any{"__typename": "Bot", "login": "review-bot", "databaseId": 9}},
		}}}},
		extra: map[string]any{"totalCount": 6, "pageInfo": map[string]any{"hasNextPage": false}, "nodes": []any{
			reviewNode("APPROVED", ghUser("alice", 42)),
			reviewNode("CHANGES_REQUESTED", ghUser("carol", 44)),
			reviewNode("COMMENTED", ghUser("erin", 45)),
			reviewNode("DISMISSED", ghUser("dave", 46)),
			reviewNode("SOMETHING_NEW", ghUser("gina", 47)),
			reviewNode("COMMENTED", nil),
		}},
	}
	a := newEntityTest(t, f)
	_, err := a.ReadBase(context.Background(), nil)
	require.NoError(t, err)
	page, err := a.ReadPage(context.Background(), entityacquire.PageRequest{Need: entityacquire.NeedReviewers, Kind: ne.KindChangeProposal})
	require.NoError(t, err)
	assert.Empty(t, page.Next)
	assert.Equal(t, entityUpdated+"@"+entityHead, page.Revision)
	got := page.Members.Reviewers.Known
	require.Len(t, got, 9)
	type row struct {
		kind  ne.Field[ne.ActorKind]
		id    ne.Field[string]
		state ne.Field[ne.ReviewerState]
	}
	unknownState := ne.Unknown[ne.ReviewerState](ne.ReasonUnsupported)
	want := []row{
		{ne.Known(ne.ActorBot), ne.Known("9"), ne.Known(ne.ReviewerUnreviewed)},
		{ne.Known(ne.ActorHuman), ne.Known("42"), unknownState},
		{ne.Known(ne.ActorHuman), ne.Known("44"), ne.Known(ne.ReviewerChangesRequested)},
		{ne.Known(ne.ActorHuman), ne.Known("45"), ne.Known(ne.ReviewerReviewed)},
		{ne.Known(ne.ActorHuman), ne.Known("46"), ne.Known(ne.ReviewerDismissed)},
		{ne.Known(ne.ActorHuman), ne.Known("47"), unknownState},
		{ne.Known(ne.ActorTeam), ne.Known("7"), ne.Known(ne.ReviewerUnreviewed)},
		{ne.Unknown[ne.ActorKind](ne.ReasonUnsupported), ne.Unknown[string](ne.ReasonUnsupported), unknownState},
		{ne.Unknown[ne.ActorKind](ne.ReasonUnsupported), ne.Unknown[string](ne.ReasonUnsupported), unknownState},
	}
	for i, w := range want {
		assert.Equal(t, w, row{got[i].Subject.Kind, got[i].Subject.ID, got[i].State}, "reviewer %d", i)
	}
	team := got[6].Subject
	assert.Equal(t, ne.Known("Core"), team.Name)
	assert.True(t, team.Login.IsZero(), "a team reference carries only kind, ID, and name")
	assert.True(t, team.Role.IsZero())
	assert.True(t, got[0].Subject.Name.IsZero(), "an actor subject has no team name")
	q := f.pageQueries()[0]
	assert.Contains(t, q, "conn: reviewRequests(first: $first, after: $cursor)")
	assert.Contains(t, q, "extra: latestReviews(first: $first)")
	assert.Contains(t, q, "... on Team { databaseId name }")
}

func TestEntityAdapterReviewerTeamWithoutName(t *testing.T) {
	f := &fakeEntityGraphQL{
		base: pullBase(),
		conns: map[string][]map[string]any{"reviewRequests": {{"totalCount": 1, "nodes": []any{
			map[string]any{"requestedReviewer": map[string]any{"__typename": "Team", "databaseId": 7}},
		}}}},
		extra: map[string]any{"totalCount": 0, "nodes": []any{}},
	}
	a := newEntityTest(t, f)
	_, err := a.ReadBase(context.Background(), nil)
	require.NoError(t, err)
	page, err := a.ReadPage(context.Background(), entityacquire.PageRequest{Need: entityacquire.NeedReviewers, Kind: ne.KindChangeProposal})
	require.NoError(t, err)
	require.Len(t, page.Members.Reviewers.Known, 1)
	assert.Equal(t, ne.Unknown[string](ne.ReasonUnsupported), page.Members.Reviewers.Known[0].Subject.Name)
}

func TestEntityAdapterReadPageReviewCollectionErrors(t *testing.T) {
	alice := ghUser("alice", 42)
	request := func(subject any) map[string]any { return map[string]any{"requestedReviewer": subject} }
	requests := func(total int, nodes ...any) map[string][]map[string]any {
		return map[string][]map[string]any{"reviewRequests": {{"totalCount": total, "nodes": nodes}}}
	}
	latest := func(total any, next bool, nodes ...any) map[string]any {
		return map[string]any{"totalCount": total, "pageInfo": map[string]any{"hasNextPage": next}, "nodes": nodes}
	}
	approvals := func(nodes ...any) map[string][]map[string]any {
		return map[string][]map[string]any{"latestOpinionatedReviews": {{"totalCount": len(nodes), "nodes": nodes}}}
	}
	cases := map[string]struct {
		need  entityacquire.Need
		conns map[string][]map[string]any
		extra map[string]any
		is    error
	}{
		"pending approval":          {need: entityacquire.NeedCurrentApprovals, conns: approvals(reviewNode("PENDING", alice))},
		"unknown approval state":    {need: entityacquire.NeedCurrentApprovals, conns: approvals(reviewNode("SOMETHING_NEW", alice))},
		"duplicate approver":        {need: entityacquire.NeedCurrentApprovals, conns: approvals(reviewNode("APPROVED", alice), reviewNode("APPROVED", alice))},
		"team without id":           {need: entityacquire.NeedReviewers, conns: requests(1, request(map[string]any{"__typename": "Team", "name": "Core"})), extra: latest(0, false)},
		"duplicate request":         {need: entityacquire.NeedReviewers, conns: requests(2, request(alice), request(alice)), extra: latest(0, false)},
		"duplicate latest review":   {need: entityacquire.NeedReviewers, conns: requests(0), extra: latest(2, false, reviewNode("APPROVED", alice), reviewNode("COMMENTED", alice))},
		"pending latest review":     {need: entityacquire.NeedReviewers, conns: requests(0), extra: latest(1, false, reviewNode("PENDING", alice))},
		"no second connection":      {need: entityacquire.NeedReviewers, conns: requests(0)},
		"requests span pages":       {need: entityacquire.NeedReviewers, conns: requests(2, request(alice)), extra: latest(0, false), is: entityacquire.ErrLimit},
		"latest reviews span pages": {need: entityacquire.NeedReviewers, conns: requests(0), extra: latest(1, true, reviewNode("APPROVED", alice)), is: entityacquire.ErrLimit},
		"latest reviews uncounted":  {need: entityacquire.NeedReviewers, conns: requests(0), extra: latest(nil, false), is: entityacquire.ErrLimit},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeEntityGraphQL{base: pullBase(), conns: tc.conns, extra: tc.extra}
			a := newEntityTest(t, f)
			_, err := a.ReadBase(context.Background(), nil)
			require.NoError(t, err)
			page, err := a.ReadPage(context.Background(), entityacquire.PageRequest{Need: tc.need, Kind: ne.KindChangeProposal})
			require.Error(t, err)
			assert.Nil(t, page)
			if tc.is != nil {
				assert.ErrorIs(t, err, tc.is)
			} else {
				assert.NotErrorIs(t, err, entityacquire.ErrLimit)
			}
		})
	}
}

func TestEntityAdapterReviewCollectionsDoNotApplyToIssues(t *testing.T) {
	f := &fakeEntityGraphQL{base: issueBase()}
	a := newEntityTest(t, f)
	_, err := a.ReadBase(context.Background(), nil)
	require.NoError(t, err)
	for _, need := range []entityacquire.Need{entityacquire.NeedReviewers, entityacquire.NeedCurrentApprovals, entityacquire.NeedReviews} {
		_, err := a.ReadPage(context.Background(), entityacquire.PageRequest{Need: need, Kind: ne.KindWorkItem})
		assert.ErrorIs(t, err, entityacquire.ErrUnsupported, "%s", need)
	}
	assert.Empty(t, f.pageQueries())
}

func TestEntityAdapterAcquireCurrentApprovals(t *testing.T) {
	t.Run("empty single page is complete", func(t *testing.T) {
		f := &fakeEntityGraphQL{base: pullBase(), conns: map[string][]map[string]any{
			"latestOpinionatedReviews": {{"totalCount": 1, "nodes": []any{reviewNode("DISMISSED", ghUser("carol", 44))}}},
		}}
		res := acquireGitHub(t, f, `size(entity.current_approvals) == 0`)
		assert.Equal(t, entityacquire.StatusSettled, res.Status)
		assert.Equal(t, "true", res.Value.String())
		require.Len(t, res.Reads, 1)
		assert.Equal(t, ne.CoverageComplete, res.Reads[0].Coverage.Status)
		assert.Equal(t, entityUpdated+"@"+entityHead, res.Reads[0].Coverage.SourceRevision)
	})
	pages := func() map[string][]map[string]any {
		return map[string][]map[string]any{"latestOpinionatedReviews": {
			{"totalCount": 2, "nodes": []any{reviewNode("APPROVED", ghUser("carol", 44))}},
			{"totalCount": 2, "nodes": []any{reviewNode("APPROVED", ghUser("dave", 50))}},
		}}
	}
	t.Run("witness on first page settles", func(t *testing.T) {
		f := &fakeEntityGraphQL{base: pullBase(), conns: pages()}
		res := acquireGitHub(t, f, `entity.current_approvals.exists(a, a.actor.login == "carol")`)
		assert.Equal(t, entityacquire.StatusSettled, res.Status)
		assert.Equal(t, "true", res.Value.String())
		assert.Len(t, f.pageQueries(), 1)
	})
	t.Run("count over more than one page stays partial", func(t *testing.T) {
		f := &fakeEntityGraphQL{base: pullBase(), conns: pages()}
		res := acquireGitHub(t, f, `size(entity.current_approvals) >= 2`)
		assert.Equal(t, entityacquire.StatusIncomplete, res.Status)
		assert.True(t, res.Retryable())
		assert.Len(t, f.pageQueries(), 1, "later pages are never read")
		require.Len(t, res.Reads, 1)
		assert.Equal(t, ne.CoveragePartial, res.Reads[0].Coverage.Status)
	})
}

func TestEntityAdapterAcquireReviewers(t *testing.T) {
	reviewers := func(next bool) *fakeEntityGraphQL {
		return &fakeEntityGraphQL{
			base: pullBase(),
			conns: map[string][]map[string]any{"reviewRequests": {{"totalCount": 1, "nodes": []any{
				map[string]any{"requestedReviewer": map[string]any{"__typename": "Team", "databaseId": 7, "name": "Core"}},
			}}}},
			extra: map[string]any{"totalCount": 1, "pageInfo": map[string]any{"hasNextPage": next}, "nodes": []any{
				reviewNode("COMMENTED", ghUser("carol", 44)),
			}},
		}
	}
	t.Run("team request is a team subject", func(t *testing.T) {
		f := reviewers(false)
		res := acquireGitHub(t, f, `entity.reviewers.exists(r, r.subject.kind == "team" && r.subject.name == "Core" && r.state == "unreviewed")`)
		assert.Equal(t, entityacquire.StatusSettled, res.Status)
		assert.Equal(t, "true", res.Value.String())
		assert.Len(t, f.pageQueries(), 1)
	})
	t.Run("absence needs a complete read", func(t *testing.T) {
		f := reviewers(false)
		res := acquireGitHub(t, f, `!entity.reviewers.exists(r, r.state == "changes_requested")`)
		assert.Equal(t, entityacquire.StatusSettled, res.Status)
		assert.Equal(t, "true", res.Value.String())
		require.Len(t, res.Reads, 1)
		assert.Equal(t, ne.CoverageComplete, res.Reads[0].Coverage.Status)
	})
	t.Run("a read spanning pages is partial and keeps no witness", func(t *testing.T) {
		f := reviewers(true)
		res := acquireGitHub(t, f, `entity.reviewers.exists(r, r.subject.kind == "team")`)
		assert.Equal(t, entityacquire.StatusIncomplete, res.Status)
		assert.True(t, res.Retryable())
		require.Len(t, res.Reads, 1)
		assert.Equal(t, ne.CoveragePartial, res.Reads[0].Coverage.Status)
		assert.Zero(t, res.Reads[0].Members)
	})
}

func commentNode(id, at string, author any) map[string]any {
	return map[string]any{
		"id": id, "url": "https://github.com/owner/repo/issues/7#" + id, "createdAt": at,
		"lastEditedAt": nil, "author": author, "editor": nil,
	}
}

func submittedReview(id, state, at string, author any) map[string]any {
	return map[string]any{"id": id, "state": state, "submittedAt": at, "lastEditedAt": nil, "author": author, "editor": nil}
}

func edited(node map[string]any, at string, editor any) map[string]any {
	node["lastEditedAt"], node["editor"] = at, editor
	return node
}

func TestEntityAdapterReadPageComments(t *testing.T) {
	noURL := commentNode("IC_4", "2026-11-02T08:00:00Z", ghUser("dave", 46))
	delete(noURL, "url")
	f := &fakeEntityGraphQL{base: issueBase(), conns: map[string][]map[string]any{
		"comments": {{"totalCount": 6, "nodes": []any{
			commentNode("IC_3", "2026-11-03T08:00:00Z", ghUser("alice", 42)),
			edited(commentNode("IC_2", "2026-11-02T08:00:00Z", ghUser("bob", 43)), "2026-11-05T09:30:00Z", ghUser("maint", 60)),
			commentNode("IC_1", "2026-11-02T08:00:00Z", map[string]any{"__typename": "Bot", "login": "ci-bot", "databaseId": 9}),
			edited(commentNode("IC_5", "2026-11-04T08:00:00Z", ghUser("carol", 44)), "2026-11-04T09:00:00Z", nil),
			edited(commentNode("IC_6", "2026-11-04T10:00:00Z", nil), "not a time", ghUser("erin", 45)),
			noURL,
		}}},
	}}
	a := newEntityTest(t, f)
	_, err := a.ReadBase(context.Background(), nil)
	require.NoError(t, err)
	page, err := a.ReadPage(context.Background(), entityacquire.PageRequest{Need: entityacquire.NeedComments, Kind: ne.KindWorkItem})
	require.NoError(t, err)
	assert.Empty(t, page.Next, "a single page is a complete, consistent read")
	assert.Equal(t, entityUpdated, page.Revision)
	got := page.Members.Comments.Known
	require.Len(t, got, 6)
	ids := make([]string, len(got))
	for i, c := range got {
		ids[i] = c.ID
		assert.Equal(t, ne.Unknown[string](ne.ReasonUnsupported), c.Text, "text needs the content-safety filter")
		assert.Equal(t, ne.Unknown[string](ne.ReasonUnsupported), c.Command)
		assert.Equal(t, ne.Null[string](), c.ThreadID, "issue comments are unthreaded")
		assert.Equal(t, ne.Unknown[bool](ne.ReasonNotLoaded), c.IsEntityAuthor)
		assert.Empty(t, c.ParentID)
	}
	assert.Equal(t, []string{"IC_1", "IC_2", "IC_4", "IC_3", "IC_5", "IC_6"}, ids, "sorted by created_at, then ID")

	bot := got[0]
	assert.Equal(t, ne.Known("9"), bot.Actor.ID)
	assert.Equal(t, ne.Known(ne.ActorBot), bot.Actor.Kind)
	assert.Equal(t, ne.Timestamp("2026-11-02T08:00:00.000000000Z"), bot.CreatedAt)
	assert.Equal(t, ne.Known(bot.CreatedAt), bot.RevisedAt, "an unedited comment's revision is its creation")
	assert.Equal(t, ne.Known("https://github.com/owner/repo/issues/7#IC_1"), bot.URL)

	editedByMaint := got[1]
	assert.Equal(t, ne.Known("60"), editedByMaint.Actor.ID, "an edited comment's actor is its current editor")
	assert.Equal(t, ne.Known("maint"), editedByMaint.Actor.Login)
	assert.Equal(t, ne.Known(ne.Timestamp("2026-11-05T09:30:00.000000000Z")), editedByMaint.RevisedAt)

	assert.Equal(t, ne.Unknown[string](ne.ReasonFailed), got[2].URL)
	assert.Equal(t, ne.Known("42"), got[3].Actor.ID)

	unreportedEditor := got[4]
	assert.Equal(t, ne.Unknown[string](ne.ReasonUnsupported), unreportedEditor.Actor.ID, "an unreported editor never borrows the author")
	assert.Equal(t, ne.Unknown[ne.ActorKind](ne.ReasonUnsupported), unreportedEditor.Actor.Kind)

	badEdit := got[5]
	assert.Equal(t, ne.Known("45"), badEdit.Actor.ID)
	assert.Equal(t, ne.Unknown[ne.Timestamp](ne.ReasonFailed), badEdit.RevisedAt)

	q := f.pageQueries()[0]
	assert.Contains(t, q, "conn: comments(first: $first, after: $cursor)")
	assert.Contains(t, q, "lastEditedAt author { __typename login")
	assert.Contains(t, q, "editor { __typename login")
}

func TestEntityAdapterReadPageReviews(t *testing.T) {
	f := &fakeEntityGraphQL{base: pullBase(), conns: map[string][]map[string]any{
		"reviews": {{"totalCount": 5, "nodes": []any{
			// Created first, but submitted after the other reviews.
			submittedReview("PRR_1", "APPROVED", "2026-11-06T08:00:00Z", ghUser("alice", 42)),
			submittedReview("PRR_2", "CHANGES_REQUESTED", "2026-11-03T08:00:00Z", ghUser("carol", 44)),
			edited(submittedReview("PRR_3", "COMMENTED", "2026-11-04T08:00:00Z", ghUser("dave", 46)), "2026-11-04T07:00:00Z", ghUser("dave", 46)),
			submittedReview("PRR_4", "DISMISSED", "2026-11-05T08:00:00Z", ghUser("erin", 45)),
			submittedReview("PRR_5", "SOMETHING_NEW", "2026-11-05T09:00:00Z", ghUser("gina", 47)),
		}}},
	}}
	a := newEntityTest(t, f)
	_, err := a.ReadBase(context.Background(), nil)
	require.NoError(t, err)
	page, err := a.ReadPage(context.Background(), entityacquire.PageRequest{Need: entityacquire.NeedReviews, Kind: ne.KindChangeProposal})
	require.NoError(t, err)
	assert.Empty(t, page.Next)
	assert.Equal(t, entityUpdated+"@"+entityHead, page.Revision)
	got := page.Members.Reviews.Known
	require.Len(t, got, 5)
	type row struct {
		id    string
		state ne.Field[ne.ReviewState]
	}
	want := []row{
		{"PRR_2", ne.Known(ne.ReviewChangesRequested)},
		{"PRR_3", ne.Known(ne.ReviewCommented)},
		{"PRR_4", ne.Known(ne.ReviewDismissed)},
		{"PRR_5", ne.Unknown[ne.ReviewState](ne.ReasonUnsupported)},
		{"PRR_1", ne.Known(ne.ReviewApproved)},
	}
	for i, w := range want {
		assert.Equal(t, w, row{got[i].ID, got[i].ReviewState}, "review %d", i)
		assert.Equal(t, ne.Unknown[string](ne.ReasonUnsupported), got[i].Text)
		assert.Equal(t, ne.Unknown[string](ne.ReasonUnsupported), got[i].Command)
	}
	assert.Equal(t, ne.Timestamp("2026-11-06T08:00:00.000000000Z"), got[4].CreatedAt, "a review is created at submission")
	assert.Equal(t, ne.Known("42"), got[4].Actor.ID)
	assert.Equal(t, ne.Known("46"), got[1].Actor.ID)
	assert.Equal(t, ne.Unknown[ne.Timestamp](ne.ReasonUnsupported), got[1].RevisedAt, "an edit before submission is not a revision time")
	assert.Contains(t, f.pageQueries()[0], "conn: reviews(states: [APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED], first: $first, after: $cursor)")
}

func TestEntityAdapterReadPageActionErrors(t *testing.T) {
	alice := ghUser("alice", 42)
	noID := commentNode("", "2026-11-02T08:00:00Z", alice)
	cases := map[string]struct {
		base map[string]any
		need entityacquire.Need
		conn map[string][]map[string]any
	}{
		"comment without id": {issueBase(), entityacquire.NeedComments, map[string][]map[string]any{"comments": {{"totalCount": 1, "nodes": []any{noID}}}}},
		"comment without time": {issueBase(), entityacquire.NeedComments, map[string][]map[string]any{"comments": {{"totalCount": 1, "nodes": []any{
			commentNode("IC_1", "", alice),
		}}}}},
		"pending review": {pullBase(), entityacquire.NeedReviews, map[string][]map[string]any{"reviews": {{"totalCount": 1, "nodes": []any{
			submittedReview("PRR_1", "PENDING", "2026-11-02T08:00:00Z", alice),
		}}}}},
		"review without submission time": {pullBase(), entityacquire.NeedReviews, map[string][]map[string]any{"reviews": {{"totalCount": 1, "nodes": []any{
			map[string]any{"id": "PRR_1", "state": "APPROVED", "submittedAt": nil, "author": alice},
		}}}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeEntityGraphQL{base: tc.base, conns: tc.conn}
			a := newEntityTest(t, f)
			b, err := a.ReadBase(context.Background(), nil)
			require.NoError(t, err)
			page, err := a.ReadPage(context.Background(), entityacquire.PageRequest{Need: tc.need, Kind: b.Entity.Kind})
			require.Error(t, err)
			assert.Nil(t, page)
			assert.NotErrorIs(t, err, entityacquire.ErrLimit)
			assert.NotErrorIs(t, err, entityacquire.ErrUnsupported)
		})
	}
}

func reviewComment(id, state, at string, author any) map[string]any {
	n := commentNode(id, at, author)
	n["url"] = "https://github.com/owner/repo/pull/7#discussion_" + id
	n["state"] = state
	return n
}

func reviewThread(id any, total any, next bool, comments ...any) map[string]any {
	return map[string]any{"id": id, "comments": map[string]any{
		"totalCount": total, "pageInfo": map[string]any{"hasNextPage": next}, "nodes": comments,
	}}
}

func reviewThreads(total any, next bool, threads ...any) map[string]any {
	return map[string]any{"totalCount": total, "pageInfo": map[string]any{"hasNextPage": next}, "nodes": threads}
}

func conversation(total int, comments ...any) map[string]any {
	return map[string]any{"totalCount": total, "nodes": comments}
}

func TestEntityAdapterIssueThreadsAreEmpty(t *testing.T) {
	f := &fakeEntityGraphQL{base: issueBase()}
	a := newEntityTest(t, f)
	_, err := a.ReadBase(context.Background(), nil)
	require.NoError(t, err)
	page, err := a.ReadPage(context.Background(), entityacquire.PageRequest{Need: entityacquire.NeedThreads, Kind: ne.KindWorkItem})
	require.NoError(t, err)
	assert.Empty(t, page.Next)
	assert.Equal(t, entityUpdated, page.Revision)
	require.NotNil(t, page.Members.Threads.Known)
	assert.Empty(t, page.Members.Threads.Known)
	assert.Empty(t, f.pageQueries(), "an issue's threads need no provider read")

	res := acquireGitHub(t, &fakeEntityGraphQL{base: issueBase()}, `size(entity.threads) == 0`)
	assert.Equal(t, entityacquire.StatusSettled, res.Status)
	assert.Equal(t, "true", res.Value.String())
	require.Len(t, res.Reads, 1)
	assert.Equal(t, ne.CoverageComplete, res.Reads[0].Coverage.Status)
	assert.Equal(t, entityUpdated, res.Reads[0].Coverage.SourceRevision)
}

func TestEntityAdapterReadPagePullRequestComments(t *testing.T) {
	f := &fakeEntityGraphQL{base: pullBase(), conns: map[string][]map[string]any{
		"comments": {conversation(2,
			commentNode("IC_2", "2026-11-04T08:00:00Z", ghUser("alice", 42)),
			commentNode("IC_1", "2026-11-02T08:00:00Z", ghUser("bob", 43)),
		)},
	}, extra: reviewThreads(2, false,
		reviewThread("PRRT_1", 2, false,
			reviewComment("PRRC_1", "SUBMITTED", "2026-11-03T08:00:00Z", ghUser("carol", 44)),
			edited(reviewComment("PRRC_2", "SUBMITTED", "2026-11-05T08:00:00Z", ghUser("dave", 46)), "2026-11-06T08:00:00Z", ghUser("maint", 60)),
		),
		reviewThread("PRRT_2", 2, false,
			reviewComment("PRRC_3", "PENDING", "2026-11-01T08:00:00Z", ghUser("erin", 45)),
			reviewComment("PRRC_4", "SUBMITTED", "2026-11-04T08:00:00Z", map[string]any{"__typename": "Bot", "login": "ci-bot", "databaseId": 9}),
		),
	)}
	a := newEntityTest(t, f)
	_, err := a.ReadBase(context.Background(), nil)
	require.NoError(t, err)
	page, err := a.ReadPage(context.Background(), entityacquire.PageRequest{Need: entityacquire.NeedComments, Kind: ne.KindChangeProposal})
	require.NoError(t, err)
	assert.Empty(t, page.Next, "one response holding every connection is a complete, consistent read")
	assert.Equal(t, entityUpdated+"@"+entityHead, page.Revision)
	got := page.Members.Comments.Known
	type row struct {
		id     string
		thread ne.Field[string]
	}
	rows := make([]row, len(got))
	for i, c := range got {
		rows[i] = row{c.ID, c.ThreadID}
		assert.Equal(t, ne.Unknown[string](ne.ReasonUnsupported), c.Text, "text needs the content-safety filter")
		assert.Equal(t, ne.Unknown[string](ne.ReasonUnsupported), c.Command)
	}
	assert.Equal(t, []row{
		{"IC_1", ne.Null[string]()},
		{"PRRC_1", ne.Known("PRRT_1")},
		{"IC_2", ne.Null[string]()},
		{"PRRC_4", ne.Known("PRRT_2")},
		{"PRRC_2", ne.Known("PRRT_1")},
	}, rows, "sorted by created_at, then ID; a pending draft comment is left out")
	assert.Equal(t, ne.Known("https://github.com/owner/repo/pull/7#discussion_PRRC_1"), got[1].URL)
	assert.Equal(t, ne.Known(ne.ActorBot), got[3].Actor.Kind)
	assert.Equal(t, ne.Known("60"), got[4].Actor.ID, "an edited review-thread comment's actor is its current editor")
	assert.Equal(t, ne.Known(ne.Timestamp("2026-11-06T08:00:00.000000000Z")), got[4].RevisedAt)

	q := f.pageQueries()[0]
	assert.Contains(t, q, "conn: comments(first: $first, after: $cursor)")
	assert.Contains(t, q, "extra: reviewThreads(first: $first) { totalCount pageInfo { hasNextPage } nodes { id comments(first: $first)")
	assert.Contains(t, q, "nodes { state id url createdAt lastEditedAt author")
}

func TestEntityAdapterPullRequestCommentsSpanningPagesAreLimited(t *testing.T) {
	carol := reviewComment("PRRC_1", "SUBMITTED", "2026-11-03T08:00:00Z", ghUser("carol", 44))
	dave := commentNode("IC_1", "2026-11-02T08:00:00Z", ghUser("dave", 46))
	cases := map[string]struct {
		conns []map[string]any
		extra map[string]any
	}{
		"conversation spans pages": {
			conns: []map[string]any{conversation(2, dave), conversation(2, commentNode("IC_2", "2026-11-04T08:00:00Z", ghUser("erin", 45)))},
			extra: reviewThreads(1, false, reviewThread("PRRT_1", 1, false, carol)),
		},
		"conversation count mismatch": {
			conns: []map[string]any{conversation(3, dave)},
			extra: reviewThreads(1, false, reviewThread("PRRT_1", 1, false, carol)),
		},
		"threads span pages": {
			conns: []map[string]any{conversation(1, dave)},
			extra: reviewThreads(101, true, reviewThread("PRRT_1", 1, false, carol)),
		},
		"threads uncounted": {
			conns: []map[string]any{conversation(1, dave)},
			extra: reviewThreads(nil, false, reviewThread("PRRT_1", 1, false, carol)),
		},
		"thread comments span pages": {
			conns: []map[string]any{conversation(1, dave)},
			extra: reviewThreads(1, false, reviewThread("PRRT_1", 101, true, carol)),
		},
		"thread comments uncounted": {
			conns: []map[string]any{conversation(1, dave)},
			extra: reviewThreads(1, false, reviewThread("PRRT_1", nil, false, carol)),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeEntityGraphQL{base: pullBase(), conns: map[string][]map[string]any{"comments": tc.conns}, extra: tc.extra}
			a := newEntityTest(t, f)
			_, err := a.ReadBase(context.Background(), nil)
			require.NoError(t, err)
			req := entityacquire.PageRequest{Need: entityacquire.NeedComments, Kind: ne.KindChangeProposal}
			page, err := a.ReadPage(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, entityLimitCursor, page.Next)
			ids := []string{}
			for _, c := range page.Members.Comments.Known {
				ids = append(ids, c.ID)
			}
			assert.Equal(t, []string{"IC_1", "PRRC_1"}, ids, "the members read stay witnesses")
			req.Cursor = page.Next
			_, err = a.ReadPage(context.Background(), req)
			assert.ErrorIs(t, err, entityacquire.ErrLimit)
			assert.Len(t, f.pageQueries(), 1, "no further page is read")
		})
	}
}

func TestEntityAdapterPullRequestCommentErrors(t *testing.T) {
	alice := ghUser("alice", 42)
	cases := map[string]map[string]any{
		"thread without id":         reviewThreads(1, false, reviewThread("", 0, false)),
		"thread without comments":   reviewThreads(1, false, map[string]any{"id": "PRRT_1"}),
		"comment with no state":     reviewThreads(1, false, reviewThread("PRRT_1", 1, false, commentNode("PRRC_1", "2026-11-03T08:00:00Z", alice))),
		"comment with odd state":    reviewThreads(1, false, reviewThread("PRRT_1", 1, false, reviewComment("PRRC_1", "SOMETHING_NEW", "2026-11-03T08:00:00Z", alice))),
		"thread comment without id": reviewThreads(1, false, reviewThread("PRRT_1", 1, false, reviewComment("", "SUBMITTED", "2026-11-03T08:00:00Z", alice))),
		"no threads connection":     nil,
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeEntityGraphQL{base: pullBase(), conns: map[string][]map[string]any{"comments": {conversation(0)}}, extra: extra}
			a := newEntityTest(t, f)
			_, err := a.ReadBase(context.Background(), nil)
			require.NoError(t, err)
			page, err := a.ReadPage(context.Background(), entityacquire.PageRequest{Need: entityacquire.NeedComments, Kind: ne.KindChangeProposal})
			require.Error(t, err)
			assert.Nil(t, page)
			assert.NotErrorIs(t, err, entityacquire.ErrLimit)
			assert.NotErrorIs(t, err, entityacquire.ErrUnsupported)
		})
	}
}

func TestEntityAdapterAcquireComments(t *testing.T) {
	pages := func() map[string][]map[string]any {
		return map[string][]map[string]any{"comments": {
			{"totalCount": 2, "nodes": []any{commentNode("IC_1", "2026-11-02T08:00:00Z", ghUser("carol", 44))}},
			{"totalCount": 2, "nodes": []any{commentNode("IC_2", "2026-11-03T08:00:00Z", ghUser("dave", 50))}},
		}}
	}
	t.Run("single page is complete", func(t *testing.T) {
		f := &fakeEntityGraphQL{base: issueBase(), conns: map[string][]map[string]any{
			"comments": {{"totalCount": 1, "nodes": []any{commentNode("IC_1", "2026-11-02T08:00:00Z", ghUser("carol", 44))}}},
		}}
		res := acquireGitHub(t, f, `!entity.comments.exists(c, c.actor.login == "mallory")`)
		assert.Equal(t, entityacquire.StatusSettled, res.Status)
		assert.Equal(t, "true", res.Value.String())
		require.Len(t, res.Reads, 1)
		assert.Equal(t, ne.CoverageComplete, res.Reads[0].Coverage.Status)
		assert.Equal(t, entityUpdated, res.Reads[0].Coverage.SourceRevision)
	})
	t.Run("witness on first page settles", func(t *testing.T) {
		f := &fakeEntityGraphQL{base: issueBase(), conns: pages()}
		res := acquireGitHub(t, f, `entity.comments.exists(c, c.actor.login == "carol")`)
		assert.Equal(t, entityacquire.StatusSettled, res.Status)
		assert.Equal(t, "true", res.Value.String())
		assert.Len(t, f.pageQueries(), 1)
	})
	t.Run("count over more than one page stays partial", func(t *testing.T) {
		f := &fakeEntityGraphQL{base: issueBase(), conns: pages()}
		res := acquireGitHub(t, f, `size(entity.comments) >= 2`)
		assert.Equal(t, entityacquire.StatusIncomplete, res.Status)
		assert.True(t, res.Retryable())
		assert.Len(t, f.pageQueries(), 1, "later pages are never read")
		require.Len(t, res.Reads, 1)
		assert.Equal(t, ne.CoveragePartial, res.Reads[0].Coverage.Status)
	})
	t.Run("a command predicate stays unknown", func(t *testing.T) {
		f := &fakeEntityGraphQL{base: issueBase(), conns: map[string][]map[string]any{
			"comments": {{"totalCount": 1, "nodes": []any{commentNode("IC_1", "2026-11-02T08:00:00Z", ghUser("carol", 44))}}},
		}}
		res := acquireGitHub(t, f, `entity.comments.exists(c, c.command == "/fs-fix")`)
		assert.Equal(t, entityacquire.StatusIncomplete, res.Status, "unfiltered text never settles a command predicate")
		assert.True(t, res.Retryable())
	})
	t.Run("pull request review-thread comment witness settles", func(t *testing.T) {
		f := &fakeEntityGraphQL{base: pullBase(), conns: map[string][]map[string]any{"comments": {conversation(0)}},
			extra: reviewThreads(2, true, reviewThread("PRRT_1", 1, false, reviewComment("PRRC_1", "SUBMITTED", "2026-11-03T08:00:00Z", ghUser("carol", 44))))}
		res := acquireGitHub(t, f, `entity.comments.exists(c, c.actor.login == "carol" && c.thread_id == "PRRT_1")`)
		assert.Equal(t, entityacquire.StatusSettled, res.Status)
		assert.Equal(t, "true", res.Value.String())
		assert.Len(t, f.pageQueries(), 1)
	})
	t.Run("pull request comment absence over one response is complete", func(t *testing.T) {
		f := &fakeEntityGraphQL{base: pullBase(), conns: map[string][]map[string]any{
			"comments": {conversation(1, commentNode("IC_1", "2026-11-02T08:00:00Z", ghUser("dave", 46)))},
		}, extra: reviewThreads(1, false, reviewThread("PRRT_1", 2, false,
			reviewComment("PRRC_1", "SUBMITTED", "2026-11-03T08:00:00Z", ghUser("carol", 44)),
			reviewComment("PRRC_2", "PENDING", "2026-11-04T08:00:00Z", ghUser("mallory", 66)),
		))}
		res := acquireGitHub(t, f, `!entity.comments.exists(c, c.actor.login == "mallory") && size(entity.comments) == 2`)
		assert.Equal(t, entityacquire.StatusSettled, res.Status)
		assert.Equal(t, "true", res.Value.String(), "a pending draft comment is not an action")
		require.Len(t, res.Reads, 1)
		assert.Equal(t, ne.CoverageComplete, res.Reads[0].Coverage.Status)
		assert.Equal(t, entityUpdated+"@"+entityHead, res.Reads[0].Coverage.SourceRevision)
	})
	t.Run("pull request thread comments over more than one page stay partial", func(t *testing.T) {
		f := &fakeEntityGraphQL{base: pullBase(), conns: map[string][]map[string]any{"comments": {conversation(0)}},
			extra: reviewThreads(1, false, reviewThread("PRRT_1", 101, true, reviewComment("PRRC_1", "SUBMITTED", "2026-11-03T08:00:00Z", ghUser("carol", 44))))}
		res := acquireGitHub(t, f, `!entity.comments.exists(c, c.actor.login == "mallory")`)
		assert.Equal(t, entityacquire.StatusIncomplete, res.Status)
		assert.True(t, res.Retryable())
		assert.Len(t, f.pageQueries(), 1, "no further page is read")
		require.Len(t, res.Reads, 1)
		assert.Equal(t, ne.CoveragePartial, res.Reads[0].Coverage.Status)
		assert.Equal(t, 1, res.Reads[0].Members, "the members read stay witnesses")
	})
}

func TestEntityAdapterAcquireReviews(t *testing.T) {
	t.Run("approval witness settles", func(t *testing.T) {
		f := &fakeEntityGraphQL{base: pullBase(), conns: map[string][]map[string]any{"reviews": {
			{"totalCount": 2, "nodes": []any{submittedReview("PRR_1", "APPROVED", "2026-11-03T08:00:00Z", ghUser("carol", 44))}},
			{"totalCount": 2, "nodes": []any{submittedReview("PRR_2", "COMMENTED", "2026-11-04T08:00:00Z", ghUser("dave", 46))}},
		}}}
		res := acquireGitHub(t, f, `entity.reviews.exists(r, r.review_state == "approved" && r.actor.login == "carol")`)
		assert.Equal(t, entityacquire.StatusSettled, res.Status)
		assert.Equal(t, "true", res.Value.String())
		assert.Len(t, f.pageQueries(), 1)
	})
	t.Run("absence needs a complete read", func(t *testing.T) {
		f := &fakeEntityGraphQL{base: pullBase(), conns: map[string][]map[string]any{"reviews": {
			{"totalCount": 1, "nodes": []any{submittedReview("PRR_1", "COMMENTED", "2026-11-03T08:00:00Z", ghUser("carol", 44))}},
		}}}
		res := acquireGitHub(t, f, `!entity.reviews.exists(r, r.review_state == "changes_requested")`)
		assert.Equal(t, entityacquire.StatusSettled, res.Status)
		assert.Equal(t, "true", res.Value.String())
		require.Len(t, res.Reads, 1)
		assert.Equal(t, ne.CoverageComplete, res.Reads[0].Coverage.Status)
	})
	t.Run("absence over more than one page stays partial", func(t *testing.T) {
		f := &fakeEntityGraphQL{base: pullBase(), conns: map[string][]map[string]any{"reviews": {
			{"totalCount": 2, "nodes": []any{submittedReview("PRR_1", "COMMENTED", "2026-11-03T08:00:00Z", ghUser("carol", 44))}},
			{"totalCount": 2, "nodes": []any{submittedReview("PRR_2", "CHANGES_REQUESTED", "2026-11-04T08:00:00Z", ghUser("dave", 46))}},
		}}}
		res := acquireGitHub(t, f, `!entity.reviews.exists(r, r.review_state == "changes_requested")`)
		assert.Equal(t, entityacquire.StatusIncomplete, res.Status)
		assert.True(t, res.Retryable())
		assert.Len(t, f.pageQueries(), 1)
		require.Len(t, res.Reads, 1)
		assert.Equal(t, ne.CoveragePartial, res.Reads[0].Coverage.Status)
	})
}
