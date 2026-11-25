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
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{
			"id": "R_1", "nameWithOwner": "Owner/Repo", "issueOrPullRequest": f.base,
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
	conn["pageInfo"] = map[string]any{"hasNextPage": next, "endCursor": map[bool]string{true: "c" + strconv.Itoa(idx+1)}[next]}
	updated := entityUpdated
	if f.pageUpdatedAt != "" {
		updated = f.pageUpdatedAt
	}
	id := f.base["id"]
	if f.pageID != "" {
		id = f.pageID
	}
	json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{
		"issueOrPullRequest": map[string]any{
			"__typename": f.typename, "id": id, "updatedAt": updated, "headRefOid": entityHead, "conn": conn,
		},
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
		"author": map[string]any{"__typename": "User", "login": "alice", "databaseId": 42},
	}
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
	assert.Equal(t, ne.Unknown[ne.Actor](ne.ReasonUnsupported), e.Lifecycle.ClosedBy)
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
			base: issueBase(), need: entityacquire.NeedComments, kind: ne.KindWorkItem, is: entityacquire.ErrUnsupported,
		},
		"inapplicable collection": {
			base: issueBase(), need: entityacquire.NeedCommits, kind: ne.KindWorkItem, is: entityacquire.ErrUnsupported,
		},
		"result cap": {
			base: pullBase(), need: entityacquire.NeedCommits, kind: ne.KindChangeProposal,
			conns: map[string][]map[string]any{"commits": {commits(300, "a")}}, is: entityacquire.ErrLimit,
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
	f := &fakeEntityGraphQL{base: issueBase()}
	res := acquireGitHub(t, f, `size(entity.comments) == 0`)
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
