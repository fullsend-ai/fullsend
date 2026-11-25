// Package entityacquire selectively acquires the normalized-entity facts and
// history an entity-source CEL predicate needs (ADR 0147).
//
// It has two provider-neutral halves:
//
//   - [DerivePlan] analyses a predicate's CEL dependencies and returns the
//     logical data needs (scalar fact groups and collections of the
//     normalized-entity v1 contract) together with each collection's proof
//     requirement. It knows nothing about provider APIs.
//   - [Acquire] drives a provider [Adapter], which owns query planning and
//     translates each logical need into its native reads, filters, and
//     pagination. Acquire merges what the adapter returns, records
//     independent collection coverage, checks source-revision consistency
//     across reads, and re-evaluates the predicate after every read so it
//     stops as soon as the evidence settles the result.
//
// Unread, unsupported, failed, limited, and inconsistent data never reads as
// an empty collection or a false result: it stays a non-complete coverage and
// leaves the result unknown, which callers report as a retryable deferral.
package entityacquire

import (
	"fmt"
	"slices"

	"github.com/google/cel-go/common"
	"github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/parser"

	"github.com/fullsend-ai/fullsend/internal/entitycel"
	"github.com/fullsend-ai/fullsend/internal/normentity"
)

// Need is one logical data category of the normalized-entity v1 contract: a
// group of scalar facts, or one collection with its own coverage.
type Need string

// Scalar fact groups. Identity members (kind, source, id, source_id, key,
// number, url) belong to every base read and are not needs.
const (
	NeedState          Need = "state"
	NeedTitle          Need = "title"
	NeedAuthor         Need = "author"
	NeedCreatedAt      Need = "created_at"
	NeedUpdatedAt      Need = "updated_at"
	NeedLifecycle      Need = "lifecycle"
	NeedBody           Need = "body"
	NeedExtensions     Need = "extensions"
	NeedWorkItem       Need = "work_item"
	NeedChangeProposal Need = "change_proposal"
	NeedConversation   Need = "conversation"
)

// Collections. Each has independent coverage, so one can be read without
// reading another.
const (
	NeedLabels            Need = "labels"
	NeedAssignees         Need = "assignees"
	NeedComments          Need = "comments"
	NeedReviews           Need = "reviews"
	NeedReviewers         Need = "reviewers"
	NeedCurrentApprovals  Need = "current_approvals"
	NeedLabelApplications Need = "label_applications"
	NeedThreads           Need = "threads"
	NeedReactions         Need = "reactions"
	NeedChecks            Need = "checks"
	NeedChangedFiles      Need = "change_proposal.changed_files"
	NeedCommits           Need = "change_proposal.commits"
)

// factNeeds lists every scalar fact group in canonical order.
var factNeeds = []Need{
	NeedState, NeedTitle, NeedAuthor, NeedCreatedAt, NeedUpdatedAt,
	NeedLifecycle, NeedBody, NeedExtensions,
	NeedWorkItem, NeedChangeProposal, NeedConversation,
}

// collectionNeeds lists every collection in canonical (read) order.
var collectionNeeds = []Need{
	NeedLabels, NeedAssignees, NeedComments, NeedReviews, NeedReviewers,
	NeedCurrentApprovals, NeedLabelApplications, NeedThreads, NeedReactions,
	NeedChecks, NeedChangedFiles, NeedCommits,
}

// identityMembers are entity members every base read supplies.
var identityMembers = map[string]bool{
	"kind": true, "source": true, "id": true, "source_id": true,
	"key": true, "number": true, "url": true,
}

// IsCollection reports whether n is a collection need.
func (n Need) IsCollection() bool { return slices.Contains(collectionNeeds, n) }

// AppliesTo reports whether the need applies to an entity of kind k. A need
// that does not apply is absent from the entity and is never read.
func (n Need) AppliesTo(k normentity.Kind) bool {
	switch n {
	case NeedWorkItem:
		return k == normentity.KindWorkItem
	case NeedConversation:
		return k == normentity.KindConversation
	case NeedChangeProposal, NeedReviews, NeedReviewers, NeedCurrentApprovals,
		NeedChecks, NeedChangedFiles, NeedCommits:
		return k == normentity.KindChangeProposal
	}
	return true
}

// Proof is what a collection read must establish before the predicate's use
// of that collection can settle.
type Proof int

// Proof requirements, weakest first.
const (
	// ProofWitness: every use is existential in a positive context (exists,
	// or `in` over the collection), so one known matching member over a
	// partial collection may settle the result. The adapter may read
	// incrementally, and may use a narrowing provider filter, because every
	// member it returns is a real member.
	ProofWitness Proof = iota + 1
	// ProofComplete: some use is negative or aggregate (size, all,
	// exists_one, filter, map, equality, negation, or the collection used as
	// a value), so settling generally needs complete coverage. The adapter
	// must read without narrowing filters.
	ProofComplete
)

func (p Proof) String() string {
	switch p {
	case ProofWitness:
		return "witness"
	case ProofComplete:
		return "complete"
	}
	return "none"
}

// Plan is the provider-neutral acquisition plan of one predicate.
//
// The proof requirements are a planning hint for the adapter. They never
// decide the result: the coverage-aware CEL evaluation in [entitycel] does,
// so a hint that is too weak costs extra reads but cannot produce a wrong
// answer.
type Plan struct {
	// Facts lists the scalar fact groups to read, in canonical order.
	Facts []Need
	// Collections maps each collection to read to its proof requirement.
	Collections map[Need]Proof
	// Conservative is true when the predicate uses the entity in a way the
	// analysis cannot narrow (the whole entity as a value, or a dynamic
	// member key), so the plan requests every category with complete proof.
	Conservative bool
}

// CollectionOrder returns the planned collections in canonical order.
func (p *Plan) CollectionOrder() []Need {
	var out []Need
	for _, n := range collectionNeeds {
		if _, ok := p.Collections[n]; ok {
			out = append(out, n)
		}
	}
	return out
}

// useContext is how a referenced value is used by its enclosing expression.
type useContext int

const (
	// usePositive: the value only flows to the result through &&, ||, a
	// conditional branch, or an exists/all body, so a true witness cannot be
	// overturned by more data.
	usePositive useContext = iota
	// useOther: any other use (negation, comparison, function argument,
	// literal member, conditional test, ...).
	useOther
)

// DerivePlan compiles the predicate against the entity-source CEL
// environment and derives its acquisition plan from the entity paths it
// references. It returns an error for a predicate that does not compile;
// callers must deny such a predicate, never treat it as false.
func DerivePlan(predicate string) (*Plan, error) {
	if err := entitycel.Validate(predicate); err != nil {
		return nil, err
	}
	// Analyse the unexpanded form: comprehension macros stay calls such as
	// `exists`, so each use of a collection keeps its operator. The
	// expression already passed the checker above.
	p, err := parser.NewParser()
	if err != nil {
		return nil, fmt.Errorf("creating CEL parser: %w", err)
	}
	parsed, iss := p.Parse(common.NewTextSource(predicate))
	if iss != nil && len(iss.GetErrors()) > 0 {
		return nil, fmt.Errorf("parsing entity-source predicate: %s", iss.ToDisplayString())
	}
	a := &analysis{facts: map[Need]bool{}, collections: map[Need]Proof{}}
	a.visit(parsed.Expr(), usePositive)
	return a.plan(), nil
}

type analysis struct {
	facts        map[Need]bool
	collections  map[Need]Proof
	conservative bool
}

func (a *analysis) plan() *Plan {
	if a.conservative {
		for _, n := range factNeeds {
			a.facts[n] = true
		}
		for _, n := range collectionNeeds {
			a.collections[n] = ProofComplete
		}
	}
	// changed_files and commits live inside change_proposal, so reading
	// them needs the change proposal itself.
	if _, ok := a.collections[NeedChangedFiles]; ok {
		a.facts[NeedChangeProposal] = true
	}
	if _, ok := a.collections[NeedCommits]; ok {
		a.facts[NeedChangeProposal] = true
	}
	out := &Plan{Collections: a.collections, Conservative: a.conservative}
	for _, n := range factNeeds {
		if a.facts[n] {
			out.Facts = append(out.Facts, n)
		}
	}
	return out
}

// visit walks e. ctx is how e's value is used by its parent.
func (a *analysis) visit(e ast.Expr, ctx useContext) {
	if path, ok := entityPath(e); ok {
		a.record(path, ctx)
		return
	}
	switch e.Kind() {
	case ast.SelectKind:
		a.visit(e.AsSelect().Operand(), useOther)
	case ast.CallKind:
		a.visitCall(e.AsCall(), ctx)
	case ast.ListKind:
		for _, el := range e.AsList().Elements() {
			a.visit(el, useOther)
		}
	case ast.MapKind:
		for _, ent := range e.AsMap().Entries() {
			m := ent.AsMapEntry()
			a.visit(m.Key(), useOther)
			a.visit(m.Value(), useOther)
		}
	case ast.StructKind:
		for _, f := range e.AsStruct().Fields() {
			a.visit(f.AsStructField().Value(), useOther)
		}
	case ast.ComprehensionKind:
		// Not produced without macros; handled conservatively anyway.
		c := e.AsComprehension()
		for _, sub := range []ast.Expr{c.IterRange(), c.AccuInit(), c.LoopCondition(), c.LoopStep(), c.Result()} {
			a.visit(sub, useOther)
		}
	}
}

func (a *analysis) visitCall(call ast.CallExpr, ctx useContext) {
	args := call.Args()
	switch fn := call.FunctionName(); {
	case (fn == operators.LogicalAnd || fn == operators.LogicalOr) && !call.IsMemberFunction():
		for _, arg := range args {
			a.visit(arg, ctx)
		}
		return
	case fn == operators.Conditional && len(args) == 3:
		a.visit(args[0], useOther)
		a.visit(args[1], ctx)
		a.visit(args[2], ctx)
		return
	case fn == operators.In && len(args) == 2:
		a.visit(args[0], useOther)
		a.visit(args[1], ctx)
		return
	case call.IsMemberFunction() && (fn == "exists" || fn == "all") && len(args) == 2:
		rangeCtx := useOther
		if fn == "exists" {
			rangeCtx = ctx
		}
		a.visit(call.Target(), rangeCtx)
		a.visit(args[0], useOther)
		a.visit(args[1], ctx)
		return
	}
	if call.IsMemberFunction() {
		a.visit(call.Target(), useOther)
	}
	for _, arg := range args {
		a.visit(arg, useOther)
	}
}

// record adds the needs of one referenced entity path.
func (a *analysis) record(path []string, ctx useContext) {
	if len(path) == 0 {
		a.conservative = true
		return
	}
	head := path[0]
	if identityMembers[head] {
		return
	}
	if head == string(NeedChangeProposal) {
		if len(path) == 1 {
			a.facts[NeedChangeProposal] = true
			a.addCollection(NeedChangedFiles, useOther)
			a.addCollection(NeedCommits, useOther)
			return
		}
		switch sub := Need(head + "." + path[1]); sub {
		case NeedChangedFiles, NeedCommits:
			if len(path) > 2 {
				ctx = useOther
			}
			a.addCollection(sub, ctx)
			return
		}
		a.facts[NeedChangeProposal] = true
		return
	}
	n := Need(head)
	switch {
	case n.IsCollection():
		if len(path) > 1 {
			ctx = useOther
		}
		a.addCollection(n, ctx)
	case slices.Contains(factNeeds, n):
		a.facts[n] = true
	}
	// Any other member is not part of the entity contract: it is always
	// absent, so it needs no read.
}

func (a *analysis) addCollection(n Need, ctx useContext) {
	p := ProofWitness
	if ctx != usePositive {
		p = ProofComplete
	}
	a.collections[n] = max(a.collections[n], p)
}

// entityPath returns the member names of a chain of field selections and
// constant string indexes rooted at the `entity` variable. A presence test
// (`has(entity.x)`) is a selection like any other.
func entityPath(e ast.Expr) ([]string, bool) {
	var rev []string
	for {
		switch e.Kind() {
		case ast.IdentKind:
			if e.AsIdent() != entitycel.RootEntity {
				return nil, false
			}
			slices.Reverse(rev)
			return rev, true
		case ast.SelectKind:
			s := e.AsSelect()
			rev = append(rev, s.FieldName())
			e = s.Operand()
		case ast.CallKind:
			c := e.AsCall()
			if c.FunctionName() != operators.Index || c.IsMemberFunction() || len(c.Args()) != 2 {
				return nil, false
			}
			key := c.Args()[1]
			if key.Kind() != ast.LiteralKind {
				return nil, false
			}
			s, ok := key.AsLiteral().(types.String)
			if !ok {
				return nil, false
			}
			rev = append(rev, string(s))
			e = c.Args()[0]
		default:
			return nil, false
		}
	}
}
