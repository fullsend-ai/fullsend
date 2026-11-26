package forgejo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// fakeIssues is a stateful Forgejo issue, comment, and label API for the
// repository o/r, served under a path prefix. It records every request.
type fakeIssues struct {
	mu       sync.Mutex
	prefix   string
	labels   []issueLabel
	issues   map[int]*fakeIssue
	comments map[int]*fakeComment
	nextID   int64
	requests []string
	auth     []string
	bodies   []string
	// serverLabelFilter makes the issue listing apply the labels query
	// the way Forgejo does: unknown names are discarded.
	serverLabelFilter bool
}

type fakeIssue struct {
	title    string
	body     string
	state    string
	isPR     bool
	labelIDs []int64
	timeline []map[string]any
}

type fakeComment struct {
	id     int
	issue  int
	body   string
	author string
}

func newFakeIssues(t *testing.T, prefix string) (*fakeIssues, *LiveClient) {
	t.Helper()
	f := &fakeIssues{prefix: prefix, issues: map[int]*fakeIssue{}, comments: map[int]*fakeComment{}, nextID: 100}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+prefix+"/", "secret-token", srv.Client())
	require.NoError(t, err)
	return f, c
}

func (f *fakeIssues) id() int64 {
	f.nextID++
	return f.nextID
}

func (f *fakeIssues) label(id int64) issueLabel {
	for _, l := range f.labels {
		if l.ID == id {
			return l
		}
	}
	return issueLabel{ID: id}
}

func (f *fakeIssues) issueJSON(n int, is *fakeIssue) map[string]any {
	labels := make([]map[string]any, 0, len(is.labelIDs))
	for _, id := range is.labelIDs {
		l := f.label(id)
		labels = append(labels, map[string]any{"id": l.ID, "name": l.Name})
	}
	out := map[string]any{
		"id": 1000 + n, "number": n, "title": is.title, "body": is.body, "state": is.state,
		"html_url": fmt.Sprintf("https://forgejo.example/o/r/issues/%d", n), "labels": labels, "pull_request": nil,
	}
	if is.isPR {
		out["pull_request"] = map[string]any{"merged": false}
	}
	return out
}

func (f *fakeIssues) commentJSON(c *fakeComment) map[string]any {
	return map[string]any{
		"id": c.id, "body": c.body, "user": map[string]any{"login": c.author},
		"html_url":   fmt.Sprintf("https://forgejo.example/o/r/issues/%d#issuecomment-%d", c.issue, c.id),
		"created_at": "2026-01-02T03:04:05+00:00",
	}
}

func notFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found"})
}

// page returns the slice of items on the requested page.
func page[T any](r *http.Request, items []T) []T {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if p < 1 || limit < 1 {
		return items
	}
	start := min((p-1)*limit, len(items))
	return items[start:min(start+limit, len(items))]
}

func (f *fakeIssues) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw := map[string]any{}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&raw)
	}
	b, _ := json.Marshal(raw)
	f.requests = append(f.requests, r.Method+" "+r.URL.EscapedPath())
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.bodies = append(f.bodies, string(b))

	base := f.prefix + "/api/v1/repos/o/r"
	if !strings.HasPrefix(r.URL.Path, base+"/") {
		notFound(w)
		return
	}
	segs := strings.Split(strings.TrimPrefix(r.URL.Path, base+"/"), "/")
	route := r.Method + " " + segs[0]
	switch {
	case route == "GET labels" && len(segs) == 1:
		writeJSON(w, http.StatusOK, page(r, f.labels))
	case route == "POST labels" && len(segs) == 1:
		l := issueLabel{ID: f.id(), Name: raw["name"].(string)}
		f.labels = append(f.labels, l)
		writeJSON(w, http.StatusCreated, l)
	case segs[0] == "issues" && len(segs) >= 2 && segs[1] == "comments":
		f.serveComment(w, r, segs, raw)
	case route == "POST issues" && len(segs) == 1:
		n := len(f.issues) + 1
		is := &fakeIssue{title: raw["title"].(string), body: raw["body"].(string), state: "open"}
		if ids, ok := raw["labels"].([]any); ok {
			for _, id := range ids {
				is.labelIDs = append(is.labelIDs, int64(id.(float64)))
			}
		}
		f.issues[n] = is
		writeJSON(w, http.StatusCreated, f.issueJSON(n, is))
	case route == "GET issues" && len(segs) == 1:
		f.listIssues(w, r)
	case segs[0] == "issues" && len(segs) >= 2:
		f.serveIssue(w, r, segs, raw)
	default:
		notFound(w)
	}
}

func (f *fakeIssues) listIssues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var want []int64
	if f.serverLabelFilter && q.Get("labels") != "" {
		for name := range strings.SplitSeq(q.Get("labels"), ",") {
			if l, ok := findLabel(f.labels, name); ok {
				want = append(want, l.ID)
			}
		}
	}
	var out []map[string]any
	for n := 1; n <= len(f.issues); n++ {
		is := f.issues[n]
		if is.state != q.Get("state") || (is.isPR && q.Get("type") == "issues") {
			continue
		}
		match := true
		for _, id := range want {
			match = match && slices.Contains(is.labelIDs, id)
		}
		if match {
			out = append(out, f.issueJSON(n, is))
		}
	}
	writeJSON(w, http.StatusOK, page(r, out))
}

func (f *fakeIssues) serveIssue(w http.ResponseWriter, r *http.Request, segs []string, raw map[string]any) {
	n, _ := strconv.Atoi(segs[1])
	is, ok := f.issues[n]
	if !ok {
		notFound(w)
		return
	}
	sub := strings.Join(segs[2:], "/")
	switch {
	case sub == "" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, f.issueJSON(n, is))
	case sub == "" && r.Method == http.MethodPatch:
		is.state = raw["state"].(string)
		writeJSON(w, http.StatusCreated, f.issueJSON(n, is))
	case sub == "labels" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, f.issueJSON(n, is)["labels"])
	case sub == "labels" && r.Method == http.MethodPost:
		for _, v := range raw["labels"].([]any) {
			id := int64(v.(float64))
			if !slices.Contains(is.labelIDs, id) {
				is.labelIDs = append(is.labelIDs, id)
				is.timeline = append(is.timeline, map[string]any{
					"type": "label", "body": "1", "created_at": "2026-01-01T00:00:00Z",
					"label": f.label(id), "user": map[string]any{"login": "bot"},
				})
			}
		}
		writeJSON(w, http.StatusOK, f.issueJSON(n, is)["labels"])
	case strings.HasPrefix(sub, "labels/") && r.Method == http.MethodDelete:
		id, _ := strconv.ParseInt(segs[3], 10, 64)
		var kept []int64
		for _, x := range is.labelIDs {
			if x != id {
				kept = append(kept, x)
			}
		}
		if len(kept) == len(is.labelIDs) {
			notFound(w)
			return
		}
		is.labelIDs = kept
		is.timeline = append(is.timeline, map[string]any{
			"type": "label", "body": "", "label": f.label(id), "user": map[string]any{"login": "bot"},
		})
		w.WriteHeader(http.StatusNoContent)
	case sub == "timeline" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, page(r, is.timeline))
	case sub == "comments" && r.Method == http.MethodGet:
		var out []map[string]any
		for id := 0; id <= int(f.nextID); id++ {
			if c, ok := f.comments[id]; ok && c.issue == n {
				out = append(out, f.commentJSON(c))
			}
		}
		writeJSON(w, http.StatusOK, out)
	case sub == "comments" && r.Method == http.MethodPost:
		c := &fakeComment{id: int(f.id()), issue: n, body: raw["body"].(string), author: "bot"}
		f.comments[c.id] = c
		writeJSON(w, http.StatusCreated, f.commentJSON(c))
	default:
		notFound(w)
	}
}

func (f *fakeIssues) serveComment(w http.ResponseWriter, r *http.Request, segs []string, raw map[string]any) {
	if len(segs) != 3 {
		notFound(w)
		return
	}
	id, _ := strconv.Atoi(segs[2])
	c, ok := f.comments[id]
	if !ok {
		notFound(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, f.commentJSON(c))
	case http.MethodPatch:
		c.body = raw["body"].(string)
		writeJSON(w, http.StatusOK, f.commentJSON(c))
	case http.MethodDelete:
		delete(f.comments, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		notFound(w)
	}
}

func (f *fakeIssues) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func TestIssues_LifecycleAtRootAndUnderPrefix(t *testing.T) {
	for _, prefix := range []string{"", "/forgejo"} {
		t.Run("prefix="+prefix, func(t *testing.T) {
			ctx := context.Background()
			f, c := newFakeIssues(t, prefix)
			api := "/api/v1/repos/o/r"

			// EnsureLabel creates a missing label once and is then a no-op,
			// including for a differently cased name.
			require.NoError(t, c.EnsureLabel(ctx, "o", "r", "ready-to-code"))
			require.NoError(t, c.EnsureLabel(ctx, "o", "r", "ready-to-code"))
			require.NoError(t, c.EnsureLabel(ctx, "o", "r", "Ready-To-Code"))
			assert.Equal(t, 1, f.count("POST "+prefix+api+"/labels"))
			require.Len(t, f.labels, 1)
			readyID := f.labels[0].ID

			// CreateIssue resolves existing labels to IDs and creates missing ones.
			issue, err := c.CreateIssue(ctx, "o", "r", "Bug", "It broke", "ready-to-code", "triaged")
			require.NoError(t, err)
			assert.Equal(t, 1, issue.Number)
			assert.Equal(t, int64(1001), issue.ID)
			assert.Equal(t, "https://forgejo.example/o/r/issues/1", issue.URL)
			assert.Equal(t, []string{"ready-to-code", "triaged"}, issue.Labels)
			require.Len(t, f.labels, 2)
			assert.Contains(t, f.bodies, fmt.Sprintf(`{"body":"It broke","labels":[%d,%d],"title":"Bug"}`, readyID, f.labels[1].ID))

			got, err := c.GetIssue(ctx, "o", "r", 1)
			require.NoError(t, err)
			assert.Equal(t, "open", got.State)
			assert.False(t, got.IsPullRequest)
			assert.Equal(t, "It broke", got.Body)

			// AddIssueLabels creates an unknown label and applies it by ID.
			require.NoError(t, c.AddIssueLabels(ctx, "o", "r", 1, "needs-info"))
			require.NoError(t, c.AddIssueLabels(ctx, "o", "r", 1))
			got, err = c.GetIssue(ctx, "o", "r", 1)
			require.NoError(t, err)
			assert.Equal(t, []string{"ready-to-code", "triaged", "needs-info"}, got.Labels)

			events, err := c.ListIssueLabelEvents(ctx, "o", "r", 1)
			require.NoError(t, err)
			require.Len(t, events, 1)
			assert.Equal(t, forge.IssueLabelEvent{LabelName: "needs-info", ActorLogin: "bot", CreatedAt: "2026-01-01T00:00:00Z"}, events[0])

			// RemoveIssueLabel removes by the issue's own label ID; a second
			// removal reports not found. Removal events are not label events.
			require.NoError(t, c.RemoveIssueLabel(ctx, "o", "r", 1, "needs-info"))
			err = c.RemoveIssueLabel(ctx, "o", "r", 1, "needs-info")
			assert.True(t, forge.IsNotFound(err), "%v", err)
			events, err = c.ListIssueLabelEvents(ctx, "o", "r", 1)
			require.NoError(t, err)
			assert.Len(t, events, 1)

			// Comments.
			created, err := c.CreateIssueComment(ctx, "o", "r", 1, "hello")
			require.NoError(t, err)
			assert.Equal(t, "hello", created.Body)
			assert.Equal(t, "bot", created.Author)
			assert.Equal(t, "2026-01-02T03:04:05+00:00", created.CreatedAt)
			assert.Contains(t, created.HTMLURL, "#issuecomment-")
			_, err = c.CreateIssueComment(ctx, "o", "r", 1, "second")
			require.NoError(t, err)

			require.NoError(t, c.UpdateIssueComment(ctx, "o", "r", created.ID, "edited"))
			one, err := c.GetIssueComment(ctx, "o", "r", created.ID)
			require.NoError(t, err)
			assert.Equal(t, "edited", one.Body)

			all, err := c.ListIssueComments(ctx, "o", "r", 1)
			require.NoError(t, err)
			require.Len(t, all, 2)
			assert.Equal(t, []string{"edited", "second"}, []string{all[0].Body, all[1].Body})

			require.NoError(t, c.DeleteIssueComment(ctx, "o", "r", created.ID))
			_, err = c.GetIssueComment(ctx, "o", "r", created.ID)
			assert.True(t, forge.IsNotFound(err), "%v", err)
			assert.True(t, forge.IsNotFound(c.DeleteIssueComment(ctx, "o", "r", created.ID)))
			assert.True(t, forge.IsNotFound(c.UpdateIssueComment(ctx, "o", "r", created.ID, "x")))

			require.NoError(t, c.CloseIssue(ctx, "o", "r", 1))
			got, err = c.GetIssue(ctx, "o", "r", 1)
			require.NoError(t, err)
			assert.Equal(t, "closed", got.State)

			for _, r := range f.requests {
				assert.Contains(t, r, " "+prefix+api+"/", "every request stays on the configured instance and repository")
			}
			for _, a := range f.auth {
				assert.Equal(t, "token secret-token", a)
			}
		})
	}
}

func TestListOpenIssues_FiltersPullRequestsAndLabels(t *testing.T) {
	ctx := context.Background()
	f, c := newFakeIssues(t, "/forgejo")
	f.serverLabelFilter = true
	f.labels = []issueLabel{{ID: 1, Name: "bug"}, {ID: 2, Name: "ready"}}
	for n := 1; n <= 2*issuePageSize+3; n++ {
		is := &fakeIssue{title: "t" + strconv.Itoa(n), state: "open"}
		switch n % 4 {
		case 0:
			is.labelIDs = []int64{1, 2}
		case 1:
			is.labelIDs = []int64{1}
		case 2:
			is.state = "closed"
		}
		f.issues[n] = is
	}
	f.issues[4].isPR = true

	all, err := c.ListOpenIssues(ctx, "o", "r")
	require.NoError(t, err)
	assert.Len(t, all, 2*issuePageSize+3-26-1, "closed issues and the pull request are excluded")
	assert.Equal(t, 2, f.count("GET /forgejo/api/v1/repos/o/r/issues"), "the listing paginates")

	both, err := c.ListOpenIssues(ctx, "o", "r", "bug", "ready")
	require.NoError(t, err)
	for _, is := range both {
		assert.Equal(t, []string{"bug", "ready"}, is.Labels)
		assert.NotEqual(t, 4, is.Number, "pull requests are excluded")
	}
	assert.Len(t, both, 24)

	// Forgejo discards unknown label names from its filter; the client
	// still requires every requested label.
	none, err := c.ListOpenIssues(ctx, "o", "r", "bug", "no-such-label")
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestListOpenIssues_ExcludesPullRequestsTheServerReturns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "issues", r.URL.Query().Get("type"))
		assert.Equal(t, "open", r.URL.Query().Get("state"))
		_, _ = w.Write([]byte(`[{"number":1,"labels":[]},{"number":2,"pull_request":{"merged":false}}]`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "t", srv.Client())
	require.NoError(t, err)
	got, err := c.ListOpenIssues(context.Background(), "o", "r")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, 1, got[0].Number)
}

func TestGetIssue_PullRequest(t *testing.T) {
	f, c := newFakeIssues(t, "")
	f.issues[7] = &fakeIssue{state: "open", isPR: true}
	got, err := c.GetIssue(context.Background(), "o", "r", 7)
	require.NoError(t, err)
	assert.True(t, got.IsPullRequest)
}

func TestIssues_NotFound(t *testing.T) {
	ctx := context.Background()
	_, c := newFakeIssues(t, "/forgejo")
	_, err := c.GetIssue(ctx, "o", "r", 42)
	assert.True(t, forge.IsNotFound(err), "%v", err)
	assert.True(t, forge.IsNotFound(c.CloseIssue(ctx, "o", "r", 42)))
	assert.True(t, forge.IsNotFound(c.AddIssueLabels(ctx, "o", "r", 42, "x")))
	assert.True(t, forge.IsNotFound(c.RemoveIssueLabel(ctx, "o", "r", 42, "x")))
	_, err = c.ListIssueComments(ctx, "o", "r", 42)
	assert.True(t, forge.IsNotFound(err))
	_, err = c.CreateIssueComment(ctx, "o", "r", 42, "x")
	assert.True(t, forge.IsNotFound(err))
	_, err = c.ListIssueLabelEvents(ctx, "o", "r", 42)
	assert.True(t, forge.IsNotFound(err))
	_, err = c.ListOpenIssues(ctx, "o", "missing")
	assert.True(t, forge.IsNotFound(err))
	assert.True(t, forge.IsNotFound(c.EnsureLabel(ctx, "o", "missing", "x")))
	_, err = c.CreateIssue(ctx, "o", "missing", "t", "b")
	assert.True(t, forge.IsNotFound(err))
}

func TestMinimizeComment_NotSupported(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer srv.Close()
	c, err := New(srv.URL, "t", srv.Client())
	require.NoError(t, err)
	err = c.MinimizeComment(context.Background(), "node", "OUTDATED")
	assert.True(t, forge.IsNotSupported(err), "%v", err)
	assert.Zero(t, hits, "no request is sent for an unsupported operation")
}

// stubServer answers every request with handler and returns a client for it.
func stubServer(t *testing.T, handler http.HandlerFunc) *LiveClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "secret-token", srv.Client())
	require.NoError(t, err)
	return c
}

func TestIssues_APIErrorsOmitToken(t *testing.T) {
	ctx := context.Background()
	c := stubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "token lacks write:issue"})
	})
	calls := map[string]func() error{
		"CreateIssue":        func() error { _, err := c.CreateIssue(ctx, "o", "r", "t", "b"); return err },
		"CreateIssueLabels":  func() error { _, err := c.CreateIssue(ctx, "o", "r", "t", "b", "x"); return err },
		"AddIssueLabels":     func() error { return c.AddIssueLabels(ctx, "o", "r", 1, "x") },
		"EnsureLabel":        func() error { return c.EnsureLabel(ctx, "o", "r", "x") },
		"GetIssue":           func() error { _, err := c.GetIssue(ctx, "o", "r", 1); return err },
		"CloseIssue":         func() error { return c.CloseIssue(ctx, "o", "r", 1) },
		"ListOpenIssues":     func() error { _, err := c.ListOpenIssues(ctx, "o", "r"); return err },
		"ListLabelEvents":    func() error { _, err := c.ListIssueLabelEvents(ctx, "o", "r", 1); return err },
		"RemoveIssueLabel":   func() error { return c.RemoveIssueLabel(ctx, "o", "r", 1, "x") },
		"ListIssueComments":  func() error { _, err := c.ListIssueComments(ctx, "o", "r", 1); return err },
		"CreateIssueComment": func() error { _, err := c.CreateIssueComment(ctx, "o", "r", 1, "b"); return err },
		"GetIssueComment":    func() error { _, err := c.GetIssueComment(ctx, "o", "r", 1); return err },
		"UpdateIssueComment": func() error { return c.UpdateIssueComment(ctx, "o", "r", 1, "b") },
		"DeleteIssueComment": func() error { return c.DeleteIssueComment(ctx, "o", "r", 1) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			var apiErr *APIError
			require.True(t, errors.As(err, &apiErr), "%v", err)
			assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
			assert.Contains(t, err.Error(), "token lacks write:issue")
			assert.NotContains(t, err.Error(), "secret-token")
		})
	}
}

func TestIssues_LabelWriteFailures(t *testing.T) {
	ctx := context.Background()
	// The label listing succeeds but creating the missing label fails.
	c := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, []issueLabel{})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "boom"})
	})
	err := c.EnsureLabel(ctx, "o", "r", "x")
	assert.ErrorContains(t, err, `creating label "x"`)
	assert.True(t, forge.IsTransient(err))
	assert.Error(t, c.AddIssueLabels(ctx, "o", "r", 1, "x"))

	// A created label without an ID is rejected rather than applied as 0.
	c = stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, []issueLabel{})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"name": "x"})
	})
	assert.ErrorContains(t, c.EnsureLabel(ctx, "o", "r", "x"), "has no ID")

	// The labels are resolved but applying them fails.
	c = stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, []issueLabel{{ID: 3, Name: "x"}})
			return
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "nope"})
	})
	assert.ErrorContains(t, c.AddIssueLabels(ctx, "o", "r", 1, "x"), "nope")

	// The issue carries the label but deleting it fails.
	c = stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, []issueLabel{{ID: 3, Name: "x"}})
			return
		}
		assert.Equal(t, "/api/v1/repos/o/r/issues/1/labels/3", r.URL.Path)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "boom"})
	})
	assert.ErrorContains(t, c.RemoveIssueLabel(ctx, "o", "r", 1, "X"), "boom")
}

func TestIssues_RejectsResponsesWithoutIdentity(t *testing.T) {
	ctx := context.Background()
	c := stubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusCreated, map[string]string{})
	})
	_, err := c.CreateIssue(ctx, "o", "r", "t", "b")
	assert.ErrorContains(t, err, "has no number")
	_, err = c.CreateIssueComment(ctx, "o", "r", 1, "b")
	assert.ErrorContains(t, err, "has no ID")

	// Forgejo answers 204 with no body for an ID that is not a comment.
	c = stubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	_, err = c.GetIssueComment(ctx, "o", "r", 9)
	assert.True(t, forge.IsNotFound(err), "%v", err)
}

func TestIssues_PaginationLimits(t *testing.T) {
	ctx := context.Background()
	full := func(item any) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			items := make([]any, issuePageSize)
			for i := range items {
				items[i] = item
			}
			writeJSON(w, http.StatusOK, items)
		}
	}
	c := stubServer(t, full(map[string]any{"id": 1, "name": "other"}))
	err := c.EnsureLabel(ctx, "o", "r", "x")
	assert.True(t, errors.Is(err, forge.ErrPaginationLimit), "%v", err)

	c = stubServer(t, full(map[string]any{"number": 1}))
	_, err = c.ListOpenIssues(ctx, "o", "r")
	assert.True(t, errors.Is(err, forge.ErrPaginationLimit), "%v", err)

	c = stubServer(t, full(map[string]any{"type": "comment"}))
	_, err = c.ListIssueLabelEvents(ctx, "o", "r", 1)
	assert.True(t, errors.Is(err, forge.ErrPaginationLimit), "%v", err)
}

func TestIssues_LabelListingPaginates(t *testing.T) {
	ctx := context.Background()
	f, c := newFakeIssues(t, "")
	for i := range issuePageSize + 5 {
		f.labels = append(f.labels, issueLabel{ID: int64(i + 1), Name: "l" + strconv.Itoa(i)})
	}
	last := "l" + strconv.Itoa(issuePageSize+4)
	require.NoError(t, c.EnsureLabel(ctx, "o", "r", last))
	assert.Equal(t, 2, f.count("GET /api/v1/repos/o/r/labels"))
	assert.Zero(t, f.count("POST "), "a label on a later page is found, not duplicated")
}

func TestFindLabel_PrefersExactMatch(t *testing.T) {
	labels := []issueLabel{{ID: 1, Name: "Bug"}, {ID: 2, Name: "bug"}}
	l, ok := findLabel(labels, "bug")
	require.True(t, ok)
	assert.Equal(t, int64(2), l.ID)
	l, ok = findLabel(labels, "BUG")
	require.True(t, ok)
	assert.Equal(t, int64(1), l.ID)
	_, ok = findLabel(labels, "feature")
	assert.False(t, ok)
}
