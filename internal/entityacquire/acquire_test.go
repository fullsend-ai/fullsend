package entityacquire

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/entitycel"
	ne "github.com/fullsend-ai/fullsend/internal/normentity"
)

const rev1 = "2026-11-24T10:00:00Z"

// fakeAdapter serves pages from fixed sequences and records every read.
// Cursors are page indexes rendered as "p<n>".
type fakeAdapter struct {
	base     *ne.Entity
	rev      string
	baseErr  error
	pages    map[Need][]*Page
	filtered map[Need][]*Page
	errs     map[Need]map[int]error
	cancel   context.CancelFunc

	baseFacts [][]Need
	reads     []PageRequest
}

func (f *fakeAdapter) ReadBase(_ context.Context, facts []Need) (*Base, error) {
	f.baseFacts = append(f.baseFacts, facts)
	if f.baseErr != nil {
		return nil, f.baseErr
	}
	e := *f.base
	return &Base{Entity: &e, Revision: f.rev}, nil
}

func (f *fakeAdapter) ReadPage(_ context.Context, req PageRequest) (*Page, error) {
	f.reads = append(f.reads, req)
	if f.cancel != nil {
		f.cancel()
		return nil, context.Canceled
	}
	idx := 0
	if req.Cursor != "" {
		n, err := strconv.Atoi(req.Cursor[1:])
		if err != nil {
			return nil, err
		}
		idx = n
	}
	if err := f.errs[req.Need][idx]; err != nil {
		return nil, err
	}
	seq := f.pages[req.Need]
	if req.AllowFilter && f.filtered[req.Need] != nil {
		seq = f.filtered[req.Need]
	}
	if idx >= len(seq) {
		return nil, fmt.Errorf("no page %d of %s", idx, req.Need)
	}
	return seq[idx], nil
}

func (f *fakeAdapter) readNeeds() []Need {
	var out []Need
	for _, r := range f.reads {
		out = append(out, r.Need)
	}
	return out
}

func workItem() *ne.Entity {
	return &ne.Entity{
		Kind:     ne.KindWorkItem,
		Source:   ne.Source{System: ne.SystemGitHub, Host: "github.com", Repository: "o/r", RepositoryID: "1"},
		SourceID: "100",
		Key:      "#7",
		Number:   7,
		URL:      "https://github.com/o/r/issues/7",
		State:    ne.Known(ne.StateOpen),
		Title:    ne.Known("Fix it"),
	}
}

func changeProposal(cp ne.Field[ne.ChangeProposal]) *ne.Entity {
	e := workItem()
	e.Kind = ne.KindChangeProposal
	e.ChangeProposal = cp
	return e
}

func comment(id, text string) ne.Comment {
	var c ne.Comment
	c.ID = id
	c.CreatedAt = "2026-01-02T03:04:05.000000000Z"
	c.Text = ne.Known(text)
	c.Command = ne.Null[string]()
	if len(text) > 0 && text[0] == '/' {
		c.Command = ne.Known(text)
	}
	return c
}

// pagesOf builds a page sequence; each element is one page's fragment.
func pagesOf(rev string, filtered bool, frags ...*ne.Entity) []*Page {
	out := make([]*Page, len(frags))
	for i, f := range frags {
		next := ""
		if i < len(frags)-1 {
			next = "p" + strconv.Itoa(i+1)
		}
		out[i] = &Page{Members: f, Next: next, Filtered: filtered, Revision: rev}
	}
	return out
}

func commentsFrag(cs ...ne.Comment) *ne.Entity {
	return &ne.Entity{Comments: ne.List[ne.Comment]{Known: cs}}
}

func labelsFrag(ls ...string) *ne.Entity {
	return &ne.Entity{Labels: ne.List[string]{Known: ls}}
}

func acquire(t *testing.T, expr string, a Adapter) *Result {
	t.Helper()
	res, err := Acquire(context.Background(), Request{Predicate: expr, Adapter: a})
	require.NoError(t, err)
	return res
}

func readOf(res *Result, n Need) CollectionRead {
	for _, r := range res.Reads {
		if r.Need == n {
			return r
		}
	}
	return CollectionRead{}
}

func TestAcquireStateOnlyReadsNoHistory(t *testing.T) {
	a := &fakeAdapter{base: workItem(), rev: rev1}
	res := acquire(t, `entity.state == "open"`, a)

	assert.Equal(t, StatusSettled, res.Status)
	assert.Equal(t, entitycel.True, res.Value)
	assert.False(t, res.Retryable())
	assert.Equal(t, [][]Need{{NeedState}}, a.baseFacts)
	assert.Empty(t, a.reads, "a state-only predicate must not read any collection")
	assert.Zero(t, res.PageReads)
	assert.Empty(t, res.Reads)
	assert.True(t, res.Entity.Comments.IsZero(), "unplanned history stays unread")
	got, err := res.ResolvedEntity()
	require.NoError(t, err)
	assert.Same(t, res.Entity, got)
}

func TestAcquireReadsOnlyRequiredCategory(t *testing.T) {
	a := &fakeAdapter{
		base: workItem(), rev: rev1,
		pages: map[Need][]*Page{
			NeedComments: pagesOf(rev1, false, commentsFrag(comment("1", "hi")), commentsFrag(comment("2", "/fs-code"))),
			NeedLabels:   pagesOf(rev1, false, labelsFrag("bug")),
		},
	}
	res := acquire(t, `entity.comments.exists(c, c.command == "/fs-code")`, a)

	assert.Equal(t, StatusSettled, res.Status)
	assert.Equal(t, entitycel.True, res.Value)
	assert.Equal(t, [][]Need{nil}, a.baseFacts)
	assert.Equal(t, []Need{NeedComments, NeedComments}, a.readNeeds())
	assert.Equal(t, ProofWitness, a.reads[0].Proof)
	assert.Equal(t, ne.KindWorkItem, a.reads[0].Kind)
	assert.Equal(t, "p1", a.reads[1].Cursor)
	assert.True(t, res.Entity.Labels.IsZero())
	assert.Equal(t, ne.CoverageComplete, res.Entity.Comments.Coverage.Status)
	assert.Equal(t, rev1, res.Entity.Comments.Coverage.SourceRevision)
}

func TestAcquirePositiveWitnessSettlesPartialCollection(t *testing.T) {
	a := &fakeAdapter{
		base: workItem(), rev: rev1,
		pages: map[Need][]*Page{
			NeedComments: pagesOf(rev1, false,
				commentsFrag(comment("1", "/fs-code")),
				commentsFrag(comment("2", "later"))),
		},
	}
	res := acquire(t, `entity.comments.exists(c, c.command == "/fs-code")`, a)

	assert.Equal(t, StatusSettled, res.Status)
	assert.Equal(t, entitycel.True, res.Value)
	assert.Equal(t, 1, res.PageReads, "the witness settles the result before the next page")
	r := readOf(res, NeedComments)
	assert.Equal(t, ne.CoveragePartial, r.Coverage.Status)
	assert.Equal(t, 1, r.Members)
	assert.Equal(t, ne.CoveragePartial, res.Entity.Comments.Coverage.Status)
}

func TestAcquireNegativeAndAggregate(t *testing.T) {
	twoPages := func() map[Need][]*Page {
		return map[Need][]*Page{NeedComments: pagesOf(rev1, false,
			commentsFrag(comment("1", "a")), commentsFrag(comment("2", "b")))}
	}
	tests := []struct {
		name     string
		expr     string
		pages    map[Need][]*Page
		errs     map[Need]map[int]error
		rev      string
		status   Status
		value    entitycel.Result
		coverage ne.CoverageStatus
	}{
		{
			name: "negative over complete coverage settles", expr: `!entity.comments.exists(c, c.command == "/fs-code")`,
			pages: twoPages(), rev: rev1, status: StatusSettled, value: entitycel.True, coverage: ne.CoverageComplete,
		},
		{
			name: "negative stopped by a provider limit is unresolved", expr: `!entity.comments.exists(c, c.command == "/fs-code")`,
			pages: twoPages(), rev: rev1, errs: map[Need]map[int]error{NeedComments: {1: fmt.Errorf("secondary rate limit: %w", ErrLimit)}},
			status: StatusIncomplete, value: entitycel.Unknown, coverage: ne.CoveragePartial,
		},
		{
			name: "aggregate over an unsupported read is unresolved", expr: `size(entity.comments) == 0`,
			rev: rev1, errs: map[Need]map[int]error{NeedComments: {0: fmt.Errorf("no comments API: %w", ErrUnsupported)}},
			status: StatusIncomplete, value: entitycel.Unknown, coverage: ne.CoverageUnsupported,
		},
		{
			name: "aggregate over a failed read is unresolved", expr: `size(entity.comments) == 0`,
			rev: rev1, errs: map[Need]map[int]error{NeedComments: {0: errors.New("HTTP 502")}},
			status: StatusIncomplete, value: entitycel.Unknown, coverage: ne.CoverageFailed,
		},
		{
			name: "aggregate over a successfully empty read settles", expr: `size(entity.comments) == 0`,
			pages: map[Need][]*Page{NeedComments: pagesOf(rev1, false, commentsFrag())}, rev: rev1,
			status: StatusSettled, value: entitycel.True, coverage: ne.CoverageComplete,
		},
		{
			name: "aggregate without a source revision is unresolved", expr: `size(entity.comments) == 0`,
			pages:  map[Need][]*Page{NeedComments: pagesOf("", false, commentsFrag())},
			status: StatusIncomplete, value: entitycel.Unknown, coverage: ne.CoverageUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &fakeAdapter{base: workItem(), rev: tt.rev, pages: tt.pages, errs: tt.errs}
			res := acquire(t, tt.expr, a)
			assert.Equal(t, tt.status, res.Status)
			assert.Equal(t, tt.value, res.Value)
			assert.Equal(t, tt.status != StatusSettled, res.Retryable())
			assert.Equal(t, tt.coverage, readOf(res, NeedComments).Coverage.Status)
			assert.Equal(t, tt.coverage, res.Entity.Comments.Coverage.Status)
		})
	}
}

func TestAcquireRevisionChangeProducesNoResult(t *testing.T) {
	a := &fakeAdapter{
		base: workItem(), rev: rev1,
		pages: map[Need][]*Page{
			NeedComments: pagesOf("2026-11-24T10:05:00Z", false, commentsFrag(comment("1", "/fs-code"))),
		},
	}
	res := acquire(t, `entity.comments.exists(c, c.command == "/fs-code")`, a)

	assert.Equal(t, StatusInconsistent, res.Status)
	assert.Equal(t, entitycel.Unknown, res.Value)
	assert.True(t, res.Retryable())
	assert.Contains(t, res.Reason, "source revision changed")
	_, err := res.ResolvedEntity()
	assert.Error(t, err)
}

func TestAcquireFilteredReads(t *testing.T) {
	expr := `entity.comments.exists(c, c.command == "/fs-code")`
	t.Run("filtered witness settles", func(t *testing.T) {
		a := &fakeAdapter{
			base: workItem(), rev: rev1,
			filtered: map[Need][]*Page{NeedComments: pagesOf(rev1, true, commentsFrag(comment("9", "/fs-code")))},
		}
		res := acquire(t, expr, a)
		assert.Equal(t, StatusSettled, res.Status)
		assert.Equal(t, entitycel.True, res.Value)
		assert.True(t, a.reads[0].AllowFilter)
		assert.True(t, readOf(res, NeedComments).Filtered)
	})
	t.Run("exhausted filter never proves absence and broadens", func(t *testing.T) {
		a := &fakeAdapter{
			base: workItem(), rev: rev1,
			filtered: map[Need][]*Page{NeedComments: pagesOf(rev1, true, commentsFrag(comment("1", "a")))},
			pages:    map[Need][]*Page{NeedComments: pagesOf(rev1, false, commentsFrag(comment("1", "a"), comment("2", "b")))},
		}
		res := acquire(t, expr, a)
		assert.Equal(t, StatusSettled, res.Status)
		assert.Equal(t, entitycel.False, res.Value)
		require.Len(t, a.reads, 2)
		assert.True(t, a.reads[0].AllowFilter)
		assert.False(t, a.reads[1].AllowFilter, "the broadened read must not filter")
		assert.Empty(t, a.reads[1].Cursor)
		assert.Len(t, res.Entity.Comments.Known, 2, "filtered members are not duplicated")
		assert.Equal(t, ne.CoverageComplete, res.Entity.Comments.Coverage.Status)
	})
	t.Run("filter on a complete-proof read fails closed", func(t *testing.T) {
		a := &fakeAdapter{
			base: workItem(), rev: rev1,
			pages: map[Need][]*Page{NeedComments: pagesOf(rev1, true, commentsFrag())},
		}
		res := acquire(t, `size(entity.comments) == 0`, a)
		assert.False(t, a.reads[0].AllowFilter)
		assert.Equal(t, StatusIncomplete, res.Status)
		assert.Equal(t, ne.CoverageFailed, res.Entity.Comments.Coverage.Status)
	})
	t.Run("switching to a filter mid-collection fails closed", func(t *testing.T) {
		p := pagesOf(rev1, false, commentsFrag(comment("1", "a")), commentsFrag(comment("2", "b")))
		p[1].Filtered = true
		a := &fakeAdapter{base: workItem(), rev: rev1, pages: map[Need][]*Page{NeedComments: p}}
		res := acquire(t, expr, a)
		assert.Equal(t, StatusIncomplete, res.Status)
		assert.Equal(t, ne.CoverageFailed, res.Entity.Comments.Coverage.Status)
	})
}

func TestAcquireMalformedPagesFailClosed(t *testing.T) {
	expr := `size(entity.comments) == 0`
	stuck := pagesOf(rev1, false, commentsFrag(), commentsFrag())
	stuck[1].Next = "p1"
	tests := map[string]*Page{
		"nil page":     nil,
		"no members":   {Revision: rev1},
		"stuck cursor": nil,
	}
	for name, page := range tests {
		t.Run(name, func(t *testing.T) {
			seq := []*Page{page}
			if name == "stuck cursor" {
				seq = stuck
			}
			a := &fakeAdapter{base: workItem(), rev: rev1, pages: map[Need][]*Page{NeedComments: seq}}
			res := acquire(t, expr, a)
			assert.Equal(t, StatusIncomplete, res.Status)
			assert.Equal(t, ne.CoverageFailed, res.Entity.Comments.Coverage.Status)
		})
	}
}

func TestAcquireReadBudget(t *testing.T) {
	a := &fakeAdapter{
		base: workItem(), rev: rev1,
		pages: map[Need][]*Page{NeedComments: pagesOf(rev1, false,
			commentsFrag(comment("1", "a")), commentsFrag(comment("2", "b")), commentsFrag())},
	}
	res, err := Acquire(context.Background(), Request{
		Predicate: `!entity.comments.exists(c, c.command == "/fs-code")`, Adapter: a, MaxReads: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusIncomplete, res.Status)
	assert.Equal(t, 2, res.PageReads)
	assert.Contains(t, res.Reason, "budget")
	cov := res.Entity.Comments.Coverage
	assert.Equal(t, ne.CoveragePartial, cov.Status)
	assert.Equal(t, "read budget exhausted", cov.Reason)
}

func TestAcquireReadBudgetLeavesUnreadCollectionsNotLoaded(t *testing.T) {
	a := &fakeAdapter{
		base: workItem(), rev: rev1,
		pages: map[Need][]*Page{
			NeedLabels:   pagesOf(rev1, false, labelsFrag("a"), labelsFrag("b")),
			NeedComments: pagesOf(rev1, false, commentsFrag()),
		},
	}
	res, err := Acquire(context.Background(), Request{
		Predicate: `size(entity.labels) == 0 || size(entity.comments) == 0`, Adapter: a, MaxReads: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusIncomplete, res.Status)
	assert.Equal(t, ne.CoveragePartial, res.Entity.Labels.Coverage.Status)
	assert.Equal(t, ne.CoverageNotLoaded, res.Entity.Comments.Coverage.Status)
	assert.Equal(t, "read budget exhausted", res.Entity.Comments.Coverage.Reason)
}

func TestAcquireRoundRobin(t *testing.T) {
	a := &fakeAdapter{
		base: workItem(), rev: rev1,
		pages: map[Need][]*Page{
			NeedLabels:   pagesOf(rev1, false, labelsFrag("a"), labelsFrag("b"), labelsFrag("c")),
			NeedComments: pagesOf(rev1, false, commentsFrag(comment("1", "/fs-code"))),
		},
	}
	res := acquire(t, `entity.labels.exists(l, l == "ready") || entity.comments.exists(c, c.command == "/fs-code")`, a)
	assert.Equal(t, StatusSettled, res.Status)
	assert.Equal(t, []Need{NeedLabels, NeedComments}, a.readNeeds())
}

func TestAcquireSkipsInapplicableCollections(t *testing.T) {
	a := &fakeAdapter{base: workItem(), rev: rev1}
	res := acquire(t, `entity.kind == "change_proposal" && entity.reviews.exists(r, r.review_state == "approved")`, a)
	assert.Equal(t, StatusSettled, res.Status)
	assert.Equal(t, entitycel.False, res.Value)
	assert.Empty(t, a.reads)
}

func TestAcquireChangeProposalCollections(t *testing.T) {
	expr := `entity.change_proposal.commits.exists(c, c == "abc")`
	t.Run("known change proposal", func(t *testing.T) {
		frag := &ne.Entity{ChangeProposal: ne.Known(ne.ChangeProposal{Commits: ne.List[string]{Known: []string{"abc"}}})}
		a := &fakeAdapter{
			base:  changeProposal(ne.Known(ne.ChangeProposal{Draft: ne.Known(false)})),
			rev:   rev1,
			pages: map[Need][]*Page{NeedCommits: pagesOf(rev1, false, frag)},
		}
		res := acquire(t, expr, a)
		assert.Equal(t, StatusSettled, res.Status)
		assert.Equal(t, entitycel.True, res.Value)
		assert.Equal(t, [][]Need{{NeedChangeProposal}}, a.baseFacts)
		cp, _ := res.Entity.ChangeProposal.Value()
		assert.Equal(t, ne.CoverageComplete, cp.Commits.Coverage.Status)
	})
	t.Run("page without a change proposal fails closed", func(t *testing.T) {
		a := &fakeAdapter{
			base:  changeProposal(ne.Known(ne.ChangeProposal{})),
			rev:   rev1,
			pages: map[Need][]*Page{NeedCommits: pagesOf(rev1, false, &ne.Entity{})},
		}
		res := acquire(t, expr, a)
		assert.Equal(t, StatusIncomplete, res.Status)
		assert.Equal(t, ne.CoverageFailed, readOf(res, NeedCommits).Coverage.Status)
	})
	t.Run("unresolved change proposal is not read", func(t *testing.T) {
		a := &fakeAdapter{base: changeProposal(ne.Unknown[ne.ChangeProposal](ne.ReasonFailed)), rev: rev1}
		res := acquire(t, expr, a)
		assert.Equal(t, StatusIncomplete, res.Status)
		assert.Empty(t, a.reads)
		assert.Equal(t, ne.CoverageNotLoaded, res.Entity.UnresolvedChangeProposal.Commits.Status)
	})
}

func TestAcquireErrors(t *testing.T) {
	ctx := context.Background()
	_, err := Acquire(ctx, Request{Predicate: `true`})
	assert.ErrorContains(t, err, "no provider adapter")

	_, err = Acquire(ctx, Request{Predicate: `entity.state ==`, Adapter: &fakeAdapter{base: workItem()}})
	assert.Error(t, err)

	_, err = Acquire(ctx, Request{Predicate: `true`, Adapter: &fakeAdapter{baseErr: errors.New("HTTP 500")}})
	assert.ErrorContains(t, err, "reading entity base")

	_, err = Acquire(ctx, Request{Predicate: `true`, Adapter: nilBase{}})
	assert.ErrorContains(t, err, "no entity")

	_, err = Acquire(ctx, Request{Predicate: `event.kind == "x"`, Adapter: &fakeAdapter{base: workItem()}})
	assert.Error(t, err, "an evaluation error is returned, never a false result")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = Acquire(cancelled, Request{Predicate: `true`, Adapter: &fakeAdapter{base: workItem()}})
	assert.ErrorIs(t, err, context.Canceled)

	mid, cancelMid := context.WithCancel(ctx)
	defer cancelMid()
	_, err = Acquire(mid, Request{
		Predicate: `size(entity.comments) == 0`,
		Adapter:   &fakeAdapter{base: workItem(), rev: rev1, cancel: cancelMid},
	})
	assert.ErrorIs(t, err, context.Canceled)
}

func TestAcquireWithEvent(t *testing.T) {
	a := &fakeAdapter{base: workItem(), rev: rev1}
	res, err := Acquire(context.Background(), Request{
		Predicate: `event != null && event.kind == "comment" && entity.state == "open"`,
		Event:     map[string]any{"kind": "comment"},
		Adapter:   a,
	})
	require.NoError(t, err)
	assert.Equal(t, entitycel.True, res.Value)
}

func TestResolvedEntityWithoutEntity(t *testing.T) {
	_, err := (&Result{Status: StatusIncomplete}).ResolvedEntity()
	assert.Error(t, err)
}

type nilBase struct{}

func (nilBase) ReadBase(context.Context, []Need) (*Base, error)      { return &Base{}, nil }
func (nilBase) ReadPage(context.Context, PageRequest) (*Page, error) { return nil, nil }
