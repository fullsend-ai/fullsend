package entityacquire

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/entitycel"
	"github.com/fullsend-ai/fullsend/internal/normentity"
)

// Adapter errors. An adapter wraps one of these to classify a failed read;
// any other error is a failed read.
var (
	// ErrUnsupported: the provider or kind cannot supply this collection.
	// Its coverage becomes unsupported, never complete-empty.
	ErrUnsupported = errors.New("entityacquire: read unsupported by provider")
	// ErrLimit: a provider limit (rate limit, page cap, result window)
	// stopped the read. The members already read stay known witnesses and
	// the coverage stays partial.
	ErrLimit = errors.New("entityacquire: provider limit reached")
)

// Adapter is a provider adapter for one candidate entity. It owns query
// planning: it translates each logical need into provider-native reads,
// filters, and pagination.
//
// An adapter MUST only return members that exist at the reported source
// revision. It MAY apply a narrowing provider filter (for example a
// most-recent window or a server-side match) when the request permits it,
// and MUST then report the page as Filtered. Acquire never derives complete
// coverage from a filtered page, so a filter can only supply witnesses.
type Adapter interface {
	// ReadBase reads the entity's identity and the requested scalar fact
	// groups, with the source revision of that read. A requested group the
	// provider cannot supply must be set unknown with reason unsupported;
	// unrequested groups may be left absent.
	ReadBase(ctx context.Context, facts []Need) (*Base, error)
	// ReadPage reads one page of one collection.
	ReadPage(ctx context.Context, req PageRequest) (*Page, error)
}

// Base is the result of [Adapter.ReadBase].
type Base struct {
	// Entity holds identity and the requested fact groups. Collections are
	// ignored; Acquire fills them from pages.
	Entity *normentity.Entity
	// Revision identifies the source state the read observed (for example
	// a provider updated-at or version). Empty when the provider cannot
	// establish one, which prevents any collection from becoming complete.
	Revision string
}

// PageRequest asks for one page of one collection.
type PageRequest struct {
	// Need is the collection to read.
	Need Need
	// Proof is the plan's proof requirement for the collection.
	Proof Proof
	// Kind is the entity's kind.
	Kind normentity.Kind
	// Cursor continues a previous page; empty requests the first page.
	Cursor string
	// AllowFilter permits a narrowing provider filter. It is true only for
	// a witness-proof collection that has not yet been broadened.
	AllowFilter bool
}

// Page is one page of one collection.
type Page struct {
	// Members is an entity fragment holding only this page's members in the
	// requested collection (for change_proposal.* collections, inside a
	// known ChangeProposal). Other members are ignored.
	Members *normentity.Entity
	// Next is the provider cursor of the following page, or empty when the
	// provider reports that this page is the last one.
	Next string
	// Filtered is true when a narrowing provider filter was applied, so the
	// members are witnesses only and absence cannot be concluded.
	Filtered bool
	// Revision is the source revision this page was read at, or empty when
	// the provider cannot establish one.
	Revision string
}

// Status classifies an acquisition's result.
type Status string

// Acquisition statuses.
const (
	// StatusSettled: the evidence settles the predicate; Value is True or
	// False.
	StatusSettled Status = "settled"
	// StatusIncomplete: the planned reads ended (exhausted, unsupported,
	// failed, limited, or out of budget) without settling the predicate.
	// The candidate is retryable and is never a non-match.
	StatusIncomplete Status = "incomplete"
	// StatusInconsistent: the source revision changed between reads, so no
	// result is produced from the mixed evidence. The candidate is
	// retryable.
	StatusInconsistent Status = "inconsistent"
)

// DefaultMaxReads bounds the collection page reads of one acquisition when
// [Request.MaxReads] is zero.
const DefaultMaxReads = 64

// Request is one acquisition.
type Request struct {
	// Predicate is the entity-source CEL predicate.
	Predicate string
	// Event is the prompting event in map form, or nil for an eventless
	// evaluation (event == null).
	Event map[string]any
	// Adapter is the selected provider adapter for the candidate entity.
	Adapter Adapter
	// MaxReads bounds collection page reads; zero means DefaultMaxReads.
	MaxReads int
}

// CollectionRead is the bounded evidence of one planned collection.
type CollectionRead struct {
	Need     Need
	Proof    Proof
	Pages    int
	Members  int
	Filtered bool
	Coverage normentity.Coverage
}

// Result is the outcome of [Acquire].
type Result struct {
	Plan *Plan
	// Entity is the acquired entity with per-collection coverage. Under
	// StatusInconsistent it mixes source revisions and must not be
	// evaluated; use [Result.ResolvedEntity].
	Entity *normentity.Entity
	// Value is the predicate result: True or False only when Settled.
	Value  entitycel.Result
	Status Status
	Reason string
	// Revision is the base read's source revision.
	Revision string
	// Reads records each planned collection in canonical order.
	Reads []CollectionRead
	// BaseReads and PageReads count adapter calls.
	BaseReads int
	PageReads int
}

// Retryable reports whether the candidate must stay retryable: the result
// is not settled.
func (r *Result) Retryable() bool { return r.Status != StatusSettled }

// ResolvedEntity returns the acquired entity for evaluation through the
// entity eligibility seam. It fails for an inconsistent acquisition, whose
// facts come from different source revisions.
func (r *Result) ResolvedEntity() (*normentity.Entity, error) {
	if r.Status == StatusInconsistent {
		return nil, fmt.Errorf("entity acquisition inconsistent: %s", r.Reason)
	}
	if r.Entity == nil {
		return nil, errors.New("entity acquisition produced no entity")
	}
	return r.Entity, nil
}

// collState tracks one planned collection.
type collState struct {
	read     CollectionRead
	cursor   string
	done     bool
	filtered bool // the current read sequence is filtered
	broad    bool // filtering is no longer allowed
	// broaden is set when a filtered read was exhausted without settling
	// the predicate: the next read discards its members and rereads the
	// collection without a filter.
	broaden bool
	// seqPages counts the pages accepted in the current read sequence (it
	// restarts when the read broadens).
	seqPages int
	// unverified is sticky for the current read sequence: some accepted
	// page reported no source revision, so the sequence's provenance was
	// never established and it can never become complete.
	unverified bool
}

// Acquire derives the predicate's plan, reads the base facts, and then reads
// planned collections page by page, round-robin, re-evaluating after each
// read until the predicate settles or the reads end.
//
// It returns an error for a predicate that does not compile or evaluate, for
// a malformed entity from the adapter (wrapping
// [entitycel.ErrMalformedEntity]), for a failed base read, and when ctx is
// done. Evidence problems are not errors: they leave the result unsettled.
func Acquire(ctx context.Context, req Request) (*Result, error) {
	if req.Adapter == nil {
		return nil, errors.New("entityacquire: no provider adapter")
	}
	plan, err := DerivePlan(req.Predicate)
	if err != nil {
		return nil, err
	}
	maxReads := req.MaxReads
	if maxReads <= 0 {
		maxReads = DefaultMaxReads
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	base, err := req.Adapter.ReadBase(ctx, plan.Facts)
	if err != nil {
		return nil, fmt.Errorf("reading entity base: %w", err)
	}
	if base == nil || base.Entity == nil {
		return nil, errors.New("reading entity base: adapter returned no entity")
	}
	// A shallow copy: every planned collection is replaced below, so the
	// adapter's slices are never appended to.
	entity := *base.Entity
	res := &Result{Plan: plan, Entity: &entity, Revision: base.Revision, BaseReads: 1}
	// observedRev is the first non-empty page revision seen when the base
	// established none; every later page must match it.
	var observedRev string

	var states []*collState
	for _, n := range plan.CollectionOrder() {
		if !n.AppliesTo(entity.Kind) {
			continue
		}
		st := &collState{read: CollectionRead{Need: n, Proof: plan.Collections[n]}}
		cov := normentity.Coverage{Status: normentity.CoverageNotLoaded}
		if !resetList(&entity, n, cov) {
			// changed_files/commits while change_proposal is unresolved:
			// the whole change proposal is unknown, reading its
			// collections cannot help.
			st.done = true
			cov.Reason = "change proposal unresolved"
			setUnresolvedCoverage(&entity, n, cov)
		}
		st.read.Coverage = cov
		states = append(states, st)
	}
	finish := func(status Status, reason string) (*Result, error) {
		res.Status, res.Reason = status, reason
		for _, st := range states {
			res.Reads = append(res.Reads, st.read)
		}
		return res, nil
	}

	for {
		v, err := entitycel.Evaluate(ctx, req.Predicate, &entity, req.Event)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if v != entitycel.Unknown {
			res.Value = v
			return finish(StatusSettled, "predicate is "+v.String())
		}
		st := nextOpen(states)
		if st == nil {
			return finish(StatusIncomplete, "planned reads ended without settling the predicate")
		}
		if res.PageReads >= maxReads {
			for _, s := range states {
				if !s.done {
					s.done = true
					s.read.Coverage = withReason(s.read.Coverage, "read budget exhausted")
					setCoverage(&entity, s.read.Need, s.read.Coverage)
				}
			}
			return finish(StatusIncomplete, fmt.Sprintf("read budget of %d pages exhausted", maxReads))
		}
		res.PageReads++
		inconsistent, err := readPage(ctx, req.Adapter, &entity, base.Revision, &observedRev, st)
		if err != nil {
			return nil, err
		}
		if inconsistent != "" {
			res.Value = entitycel.Unknown
			return finish(StatusInconsistent, inconsistent)
		}
	}
}

// nextOpen returns the open collection with the fewest pages read, so
// collections are read round-robin in canonical order.
func nextOpen(states []*collState) *collState {
	var best *collState
	for _, st := range states {
		if st.done {
			continue
		}
		if best == nil || st.read.Pages < best.read.Pages {
			best = st
		}
	}
	return best
}

// readPage reads and merges one page of st. It returns a non-empty reason
// when the page shows the source changed since the base read.
func readPage(ctx context.Context, a Adapter, entity *normentity.Entity, baseRev string, observedRev *string, st *collState) (string, error) {
	n := st.read.Need
	if st.broaden {
		// Filtered members are discarded so the unfiltered reread does not
		// duplicate them.
		st.broaden, st.broad, st.filtered = false, true, false
		st.seqPages, st.unverified = 0, false
		st.cursor = ""
		st.read.Members = 0
		resetList(entity, n, normentity.Coverage{Status: normentity.CoverageNotLoaded})
	}
	req := PageRequest{
		Need:        n,
		Proof:       st.read.Proof,
		Kind:        entity.Kind,
		Cursor:      st.cursor,
		AllowFilter: st.read.Proof == ProofWitness && !st.broad,
	}
	page, err := a.ReadPage(ctx, req)
	st.read.Pages++
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		st.done = true
		status := normentity.CoverageFailed
		switch {
		case errors.Is(err, ErrUnsupported):
			status = normentity.CoverageUnsupported
		case errors.Is(err, ErrLimit):
			status = normentity.CoveragePartial
		}
		st.read.Coverage = normentity.Coverage{Status: status, Reason: err.Error()}
		setCoverage(entity, n, st.read.Coverage)
		return "", nil
	}
	fail := func(reason string) (string, error) {
		st.done = true
		st.read.Coverage = normentity.Coverage{Status: normentity.CoverageFailed, Reason: reason}
		setCoverage(entity, n, st.read.Coverage)
		return "", nil
	}
	if page == nil || page.Members == nil {
		return fail("adapter returned no page")
	}
	if page.Filtered && !req.AllowFilter {
		return fail("adapter filtered a read that required an unfiltered result")
	}
	if page.Next != "" && page.Next == st.cursor {
		return fail("pagination did not advance")
	}
	// Every page is checked against the base revision, or, when the base
	// established none, against the first revision any page reported.
	refRev := baseRev
	if refRev == "" {
		refRev = *observedRev
	}
	if page.Revision != "" {
		if refRev != "" && page.Revision != refRev {
			return fmt.Sprintf("source revision changed from %q to %q while reading %s", refRev, page.Revision, n), nil
		}
		if refRev == "" {
			*observedRev = page.Revision
		}
	}
	if st.seqPages > 0 && page.Filtered != st.filtered {
		return fail("adapter changed filter mode mid-collection")
	}
	added, ok := appendMembers(entity, page.Members, n)
	if !ok {
		return fail("page carries no members container for " + string(n))
	}
	st.read.Members += added
	st.seqPages++
	st.filtered = page.Filtered
	if page.Revision == "" {
		st.unverified = true
	}
	st.read.Filtered = st.read.Filtered || page.Filtered
	st.cursor = page.Next

	switch {
	case page.Next != "":
		st.read.Coverage = normentity.Coverage{Status: normentity.CoveragePartial, SourceRevision: page.Revision}
	case page.Filtered:
		// The filtered result is exhausted. Its members are witnesses
		// only, so absence cannot be concluded: if they do not settle the
		// predicate, the next read broadens.
		st.broaden = true
		st.read.Coverage = normentity.Coverage{Status: normentity.CoveragePartial, Reason: "filtered read exhausted", SourceRevision: page.Revision}
	case baseRev == "" || st.unverified:
		st.done = true
		st.read.Coverage = normentity.Coverage{Status: normentity.CoverageUnknown, Reason: "source revision not established"}
	default:
		st.done = true
		st.read.Coverage = normentity.Coverage{Status: normentity.CoverageComplete, SourceRevision: baseRev}
	}
	setCoverage(entity, n, st.read.Coverage)
	return "", nil
}

// withReason keeps an open collection's non-complete status (partial, or
// not_loaded when no page was read) and records why reading stopped.
func withReason(c normentity.Coverage, reason string) normentity.Coverage {
	c.Reason = reason
	return c
}

// listField returns the addressable normentity.List value of collection n,
// or false when it has no container (a change_proposal.* collection while
// the change proposal is not known).
func listField(e *normentity.Entity, n Need) (reflect.Value, bool) {
	ev := reflect.ValueOf(e).Elem()
	var name string
	switch n {
	case NeedChangedFiles, NeedCommits:
		cp := e.ChangeProposal.Ptr()
		if cp == nil {
			return reflect.Value{}, false
		}
		ev = reflect.ValueOf(cp).Elem()
		name = map[Need]string{NeedChangedFiles: "ChangedFiles", NeedCommits: "Commits"}[n]
	default:
		name = map[Need]string{
			NeedLabels: "Labels", NeedAssignees: "Assignees", NeedComments: "Comments",
			NeedReviews: "Reviews", NeedReviewers: "Reviewers", NeedCurrentApprovals: "CurrentApprovals",
			NeedLabelApplications: "LabelApplications", NeedThreads: "Threads",
			NeedReactions: "Reactions", NeedChecks: "Checks",
		}[n]
	}
	if name == "" {
		return reflect.Value{}, false
	}
	return ev.FieldByName(name), true
}

// resetList empties collection n with coverage cov. It also records the
// coverage of an unresolved change proposal's collections.
func resetList(e *normentity.Entity, n Need, cov normentity.Coverage) bool {
	f, ok := listField(e, n)
	if !ok {
		setUnresolvedCoverage(e, n, cov)
		return false
	}
	known := f.FieldByName("Known")
	known.Set(reflect.MakeSlice(known.Type(), 0, 0))
	f.FieldByName("Coverage").Set(reflect.ValueOf(cov))
	return true
}

func setCoverage(e *normentity.Entity, n Need, cov normentity.Coverage) {
	f, ok := listField(e, n)
	if !ok {
		setUnresolvedCoverage(e, n, cov)
		return
	}
	f.FieldByName("Coverage").Set(reflect.ValueOf(cov))
}

func setUnresolvedCoverage(e *normentity.Entity, n Need, cov normentity.Coverage) {
	switch n {
	case NeedChangedFiles:
		e.UnresolvedChangeProposal.ChangedFiles = cov
	case NeedCommits:
		e.UnresolvedChangeProposal.Commits = cov
	}
}

// appendMembers appends the page fragment's members of n to e and returns
// how many were added.
func appendMembers(e, frag *normentity.Entity, n Need) (int, bool) {
	dst, ok := listField(e, n)
	if !ok {
		return 0, false
	}
	src, ok := listField(frag, n)
	if !ok {
		return 0, false
	}
	sk := src.FieldByName("Known")
	dk := dst.FieldByName("Known")
	dk.Set(reflect.AppendSlice(dk, sk))
	if n == NeedLabelApplications {
		// An adapter may read pages newest first, but the contract orders
		// known actions by created_at, then ID, across all pages read.
		slices.SortStableFunc(e.LabelApplications.Known, func(a, b normentity.LabelApplication) int {
			if c := strings.Compare(string(a.CreatedAt), string(b.CreatedAt)); c != 0 {
				return c
			}
			return strings.Compare(a.ID, b.ID)
		})
	}
	return sk.Len(), true
}
