// Package entityview builds the harness-specific authorized entity view that
// entity-source CEL evaluates (ADR 0098, ADR 0147, ADR 0148, ADR 0151).
//
// The input is a shared, possibly partial [normentity.Document], the selected
// harness's pinned authorization policy, provenance the document cannot carry
// (such as the current title editor), and provider-verified actor-role
// evidence from live lookups or the ADR 0148 actor-verdict cache. [Build]
// returns a private copy of the document in which:
//
//   - admitted actor-attributed values and records stay at their normalized
//     paths, with their actors' role fields replaced by the verified evidence
//     actually used for admission;
//   - records whose actor is known not to meet the harness's gate are
//     removed; the collection's coverage is unchanged because it remains
//     accurate for the authorized view;
//   - records whose attribution, role evidence, or source revision cannot be
//     established are removed and leave the collection's coverage
//     non-complete, so an absence or aggregate test cannot be conclusive;
//   - singular actor-attributed values (title, body, lifecycle actors, thread
//     resolvers, extension values) whose current actor is not admitted become
//     unknown, never empty, false, or null.
//
// The view is entity-view admission only. It does not perform or replace
// event-actor authorization, trusted eventless invocation provenance, or the
// ADR 0133 model-bound content admission gate; passing CEL over the view does
// not authorize staging content or acting on it. The view is built per harness
// and never written back to the shared entity or its cache entry, because
// harnesses with different minimums admit different facts.
//
// Contracts: docs/normative/authorization/v2/README.md ("Entity-source CEL
// evaluation") and docs/normative/normalized-entity/v1/README.md.
package entityview

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/actorcache"
	"github.com/fullsend-ai/fullsend/internal/normentity"
)

// Gate names the single authorization gate a [View] represents.
const Gate = "entity_view_admission"

// Errors returned by [Build].
var (
	// ErrInvalidInput reports a missing or invalid document, check time, or
	// role-evidence configuration.
	ErrInvalidInput = errors.New("entityview: invalid input")
	// ErrInvalidPolicy reports a missing policy identity or an effective
	// human minimum outside triage, write, maintain, and admin. The harness
	// is ineligible; no default is substituted.
	ErrInvalidPolicy = errors.New("entityview: invalid harness policy")
)

// Policy is the selected harness's effective authorization policy, pinned for
// one evaluation cycle.
type Policy struct {
	// Identity is the pinned policy identity recorded in the evidence.
	Identity string
	// HumanMinRole is the harness's resolved effective human_min_role. It
	// must be triage, write, maintain, or admin.
	HumanMinRole normentity.Role
}

// Provenance carries current-revision attribution that the normalized
// document does not represent itself. A missing entry is unresolved
// attribution, never an admitted actor.
type Provenance struct {
	// SourceRevision is the source revision established for this check.
	// When set, a collection whose coverage records a different revision is
	// stale, and a complete coverage that records no revision cannot prove
	// completeness at this revision.
	SourceRevision string
	// TitleEditor is the source-authenticated actor of the title's current
	// revision. The entity author does not substitute for it.
	TitleEditor *normentity.Actor
	// Assigners maps an assignee's source actor ID to the actor of the
	// assigning action.
	Assigners map[string]normentity.Actor
	// ReviewerAttribution maps a reviewer subject's source ID to the actor
	// responsible for that entry's current assignment or standing.
	ReviewerAttribution map[string]normentity.Actor
}

// Verdict is a completed, provider-verified actor-role result.
type Verdict struct {
	// Kind is the verified actor kind.
	Kind normentity.ActorKind
	// Role is the verified effective human role, or none for a bot.
	Role normentity.Role
	// BotRole is the exact registered bot role; empty for a human and for a
	// verified bot no-match.
	BotRole string
	// Verified must be true; failed or partial lookups are unusable.
	Verified bool
	// LookupStart is when the authoritative lookup started.
	LookupStart time.Time
	// PermissionSource names the authority that produced the verdict.
	PermissionSource string
}

// CacheScope holds the non-actor dimensions of an [actorcache.Key].
type CacheScope struct {
	Tenant           string
	Repository       string
	Policy           string
	PolicyRevision   string
	PermissionSource string
	Ref              string
	SourceRevision   string
}

// RoleEvidence supplies the actor-role verdicts admission may use.
//
// A live verdict, from an authoritative lookup during this check, is used
// first while it is younger than [actorcache.HardMaxAge]. Otherwise an ADR
// 0148 cache entry may be used, only when MaxAge is positive (a platform
// policy enables reuse), a scope is configured for the actor's kind, and the
// entry is younger than MaxAge. The cache is read, never written: it holds
// entity-CEL role evidence only, and this package never consults it for any
// other gate.
type RoleEvidence struct {
	// Live holds verdicts from authoritative lookups made for this check.
	Live map[actorcache.ActorID]Verdict
	// Cache is the optional ADR 0148 actor-verdict cache.
	Cache actorcache.Cache
	// CacheShared reports whether Cache is a shared host cache rather than a
	// process-local one, for audit evidence.
	CacheShared bool
	// MaxAge is the platform policy's maximum reuse age. It is capped at
	// [actorcache.HardMaxAge]; a non-positive value disables cache reuse.
	MaxAge time.Duration
	// HumanScope and BotScope scope cache keys for human and bot actors. A
	// nil scope disables cache reuse for that kind.
	HumanScope *CacheScope
	BotScope   *CacheScope
}

// Input is everything needed to build one harness's authorized view.
type Input struct {
	// Document is the shared normalized entity. It is not modified.
	Document *normentity.Document
	// Policy is the selected harness's pinned authorization policy.
	Policy Policy
	// Provenance is attribution not carried by the document.
	Provenance Provenance
	// Roles is the actor-role evidence available to this check.
	Roles RoleEvidence
	// Now is the trusted check time used to judge verdict age.
	Now time.Time
}

// View is a harness-specific authorized entity view and its evidence.
type View struct {
	// Document is the authorized view, with the same field paths and record
	// shapes as the input document and View.HumanMinRole set to the
	// harness minimum. It is a private copy.
	Document *normentity.Document
	// Evidence is the bounded authorization and coverage evidence.
	Evidence Evidence
}

// Outcome is an admission result for one actor-attributed value or record.
type Outcome string

// Admission outcomes.
const (
	// Admitted values remain in the view.
	Admitted Outcome = "admitted"
	// Excluded values belong to an actor known not to meet the gate.
	Excluded Outcome = "excluded"
	// Unresolved values lack attribution, role evidence, or a current
	// source revision.
	Unresolved Outcome = "unresolved"
)

// Diagnostic reasons recorded in evidence and in downgraded coverage.
const (
	ReasonAttributionUnresolved = "attribution_unresolved"
	ReasonRoleUnresolved        = "role_unresolved"
	ReasonBelowMinimum          = "below_harness_minimum"
	ReasonUnregisteredBot       = "bot_role_unregistered"
	ReasonNotAPerson            = "actor_not_a_person"
	ReasonStaleSourceRevision   = "stale_source_revision"
	ReasonUnboundRevision       = "source_revision_unbound"
	ReasonApprovalNotRecomputed = "approval_not_recomputed_for_harness"
)

// VerdictSource identifies where a role verdict came from.
type VerdictSource string

// Verdict sources.
const (
	SourceLive        VerdictSource = "live"
	SourceLocalCache  VerdictSource = "local_cache"
	SourceSharedCache VerdictSource = "shared_cache"
	SourceNone        VerdictSource = "none"
)

// Counts tallies admission outcomes.
type Counts struct {
	Admitted   int
	Excluded   int
	Unresolved int
}

func (c *Counts) add(o Outcome) {
	switch o {
	case Admitted:
		c.Admitted++
	case Excluded:
		c.Excluded++
	default:
		c.Unresolved++
	}
}

// CollectionEvidence records one collection's admission and coverage.
type CollectionEvidence struct {
	Counts
	// InputCoverage and ViewCoverage are the coverage statuses before and
	// after admission.
	InputCoverage normentity.CoverageStatus
	ViewCoverage  normentity.CoverageStatus
	// Reason explains a coverage downgrade; empty when there was none.
	Reason string
}

// FieldEvidence records the admission of one singular entity field.
type FieldEvidence struct {
	Outcome Outcome
	Reason  string
}

// ActorEvidence records the verdict used for one distinct actor.
type ActorEvidence struct {
	Actor actorcache.ActorID
	Kind  normentity.ActorKind
	// Source is where the verdict came from; none when it was unresolved.
	Source VerdictSource
	// Outcome is the actor's admission under this harness's policy.
	Outcome          Outcome
	Reason           string
	Role             normentity.Role
	BotRole          string
	PermissionSource string
	LookupStart      time.Time
	// Age is measured from LookupStart to the check time.
	Age time.Duration
}

// Evidence is the bounded audit evidence for one view. It lists distinct
// actors and per-collection counts, never raw content or a roster of every
// examined record.
type Evidence struct {
	// Gate is always [Gate]; the evidence authorizes nothing else.
	Gate           string
	Entity         normentity.ID
	PolicyIdentity string
	HumanMinRole   normentity.Role
	SourceRevision string
	CheckTime      time.Time
	// CacheMaxAge is the effective cache reuse age; zero when disabled.
	CacheMaxAge time.Duration
	// Fields covers singular entity-level values such as title and body.
	Fields map[string]FieldEvidence
	// Collections covers entity-level collections by specification name.
	Collections map[string]CollectionEvidence
	// Values aggregates nested singular values such as thread resolvers,
	// thread comment memberships, and extension values.
	Values map[string]Counts
	// Actors lists each distinct actor whose verdict was consulted, sorted.
	Actors []ActorEvidence
	// Totals sums the admission outcomes of every actor-attributed record
	// and value. Thread comment memberships mirror their comments' outcomes
	// and are not counted again.
	Totals Counts
}

// Build returns the authorized view of in.Document for the harness policy in
// in.Policy. It never modifies in.Document.
func Build(in Input) (*View, error) {
	if err := validatePolicy(in.Policy); err != nil {
		return nil, err
	}
	if in.Document == nil {
		return nil, fmt.Errorf("%w: document is required", ErrInvalidInput)
	}
	if err := in.Document.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if in.Now.IsZero() {
		return nil, fmt.Errorf("%w: check time is required", ErrInvalidInput)
	}
	doc, err := clone(in.Document)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}

	maxAge := min(in.Roles.MaxAge, actorcache.HardMaxAge)
	if maxAge < 0 || in.Roles.Cache == nil {
		maxAge = 0
	}
	b := &builder{
		in:     in,
		system: string(doc.Entity.Source.System),
		host:   doc.Entity.Source.Host,
		maxAge: maxAge,
		memo:   map[actorKey]*decision{},
		ev: Evidence{
			Gate:           Gate,
			Entity:         doc.Entity.ID,
			PolicyIdentity: in.Policy.Identity,
			HumanMinRole:   in.Policy.HumanMinRole,
			SourceRevision: in.Provenance.SourceRevision,
			CheckTime:      in.Now,
			CacheMaxAge:    maxAge,
			Fields:         map[string]FieldEvidence{},
			Collections:    map[string]CollectionEvidence{},
			Values:         map[string]Counts{},
		},
	}
	b.entity(&doc.Entity)
	doc.View.HumanMinRole = in.Policy.HumanMinRole

	for _, d := range b.memo {
		if d.evidence != nil {
			b.ev.Actors = append(b.ev.Actors, *d.evidence)
		}
	}
	slices.SortFunc(b.ev.Actors, func(x, y ActorEvidence) int {
		if c := strings.Compare(string(x.Actor), string(y.Actor)); c != 0 {
			return c
		}
		return strings.Compare(string(x.Kind), string(y.Kind))
	})
	return &View{Document: doc, Evidence: b.ev}, nil
}

func validatePolicy(p Policy) error {
	if p.Identity == "" {
		return fmt.Errorf("%w: policy identity is required", ErrInvalidPolicy)
	}
	switch p.HumanMinRole {
	case normentity.RoleTriage, normentity.RoleWrite, normentity.RoleMaintain, normentity.RoleAdmin:
		return nil
	}
	return fmt.Errorf("%w: human_min_role %q is not triage, write, maintain, or admin", ErrInvalidPolicy, p.HumanMinRole)
}

// clone deep-copies a document through its specified serialization, which
// preserves unknown markers, nulls, absent members, and coverage.
func clone(d *normentity.Document) (*normentity.Document, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	var out normentity.Document
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
