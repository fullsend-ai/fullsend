// Package actorcache implements the bounded actor-verdict cache that
// ADR 0148 permits for entity-source CEL.
//
// The cache stores only a completed, provider-verified actor-role verdict: a
// verified effective human role (including verified absence) or an exact
// registered bot-role lookup result (including a verified no-match). It never
// stores harness threshold results, CEL decisions, label-transition grants,
// display logins, or entity-specific facts such as authorship or assignment;
// those stay current on every check.
//
// Entries expire at an absolute time measured from the authoritative lookup's
// start and capped at [HardMaxAge]. Hits never extend that expiry. Without a
// positive platform maximum age, cross-check reuse is disabled and every Get
// misses. Known authorization changes invalidate entries independently of
// expiry.
//
// The cache MUST NOT be used for event-actor admission or ADR 0133
// model-bound record admission; those gates resolve fresh authority. A cache
// instance is host-owned and MUST NOT be reachable from a sandbox or agent.
//
// See docs/ADRs/0148-reuse-verified-actor-roles-for-entity-cel.md and the
// "Bounded CEL actor-verdict cache" section of
// docs/normative/authorization/v2/README.md.
package actorcache

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// HardMaxAge is the absolute cap on how long a verdict may be reused,
// measured from the authoritative lookup's start (ADR 0148). A platform
// policy may choose a shorter age; nothing may widen this cap.
const HardMaxAge = 5 * time.Minute

// actorIDPrefix versions the canonical actor identifier format.
const actorIDPrefix = "fullsend:actor:v1:"

// ActorID is the canonical Fullsend actor identifier, analogous to the
// normalized entity ID. Its exact form is
// `fullsend:actor:v1:<system>:<host>:<source_id>`, where each component is
// independently percent-encoded from its UTF-8 bytes (RFC 3986 unreserved
// bytes stay literal; every other byte becomes `%XX` with uppercase hex).
//
// The source ID is the provider's immutable actor ID (for example a GitHub
// user database ID or a Jira account ID), never a login or display name.
// An ActorID identifies a person or service account; on its own it is not a
// cache key. Use [Key] to bind the authorization scope.
type ActorID string

// NewActorID builds the canonical actor identifier from the normalized source
// system (for example "github"), the source host (lowercased, including a
// non-default port when present), and the provider's immutable actor ID.
func NewActorID(system, host, sourceID string) (ActorID, error) {
	if system == "" || host == "" || sourceID == "" {
		return "", errors.New("actorcache: actor ID requires system, host, and source ID")
	}
	return ActorID(actorIDPrefix +
		escapeComponent(system) + ":" +
		escapeComponent(strings.ToLower(host)) + ":" +
		escapeComponent(sourceID)), nil
}

// valid reports whether id has the canonical shape produced by NewActorID.
func (id ActorID) valid() bool {
	rest, ok := strings.CutPrefix(string(id), actorIDPrefix)
	if !ok {
		return false
	}
	parts := strings.Split(rest, ":")
	return len(parts) == 3 && !slices.Contains(parts, "")
}

// escapeComponent percent-encodes every byte that is not an RFC 3986
// unreserved character, using uppercase hexadecimal digits.
func escapeComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isUnreserved(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0F])
	}
	return b.String()
}

func isUnreserved(c byte) bool {
	switch {
	case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9':
		return true
	case c == '-', c == '.', c == '_', c == '~':
		return true
	}
	return false
}

// Key scopes a cached verdict. Authorization Contract v2 requires the key to
// bind the provider, tenant, canonical repository or project, immutable actor
// ID, verified actor kind, effective authorization policy and its revision,
// and permission source, plus the selected ref and the OWNERS/OWNERS_ALIASES
// or bot-registry revision when applicable. Entries whose keys differ in any
// field are distinct; a mismatched entry is a miss.
type Key struct {
	// Actor is the canonical actor identifier; it binds the provider (source
	// system and host) and the immutable actor ID.
	Actor ActorID
	// Kind is the verified actor kind.
	Kind normevent.ActorKind
	// Tenant identifies the provider tenant (for example the GitHub
	// organization or installation, GitLab group, or Jira site).
	Tenant string
	// Repository is the immutable repository or project ID, not a path.
	Repository string
	// Policy identifies the effective authorization policy.
	Policy string
	// PolicyRevision is the effective policy's revision or digest.
	PolicyRevision string
	// PermissionSource names the authority that produced the verdict (for
	// example the provider collaborator API, OWNERS files, or the bot
	// registry).
	PermissionSource string
	// Ref is the selected ref when the permission source is ref-dependent;
	// empty when not applicable.
	Ref string
	// SourceRevision binds the permission source's revision when applicable:
	// the presence or absence and content or revision of both OWNERS and
	// OWNERS_ALIASES, or the bot-registry revision. Empty when the source
	// exposes no revision; the entry's lookup start then defines its epoch.
	SourceRevision string
}

// validate reports whether every required scope dimension is present.
func (k Key) validate() error {
	switch {
	case !k.Actor.valid():
		return fmt.Errorf("%w: actor ID %q is not canonical", ErrInvalidKey, k.Actor)
	case k.Kind != normevent.ActorHuman && k.Kind != normevent.ActorBot:
		return fmt.Errorf("%w: unknown actor kind %q", ErrInvalidKey, k.Kind)
	case k.Tenant == "":
		return fmt.Errorf("%w: tenant is required", ErrInvalidKey)
	case k.Repository == "":
		return fmt.Errorf("%w: repository is required", ErrInvalidKey)
	case k.Policy == "":
		return fmt.Errorf("%w: policy is required", ErrInvalidKey)
	case k.PolicyRevision == "":
		return fmt.Errorf("%w: policy revision is required", ErrInvalidKey)
	case k.PermissionSource == "":
		return fmt.Errorf("%w: permission source is required", ErrInvalidKey)
	}
	return nil
}

// Entry is a cached actor-role verdict. Its fields are the authorization
// subset of the normalized-entity actor shape; the login and per-entity
// routing facts are deliberately excluded because they must stay current.
type Entry struct {
	// Role is the verified effective human role, or [normevent.RoleNone] for
	// a bot. A human's verified absence of access is a concrete role such as
	// [normevent.RoleNone] or [normevent.RoleExternal].
	Role normevent.ActorRole
	// BotRole is the exact registered Fullsend bot role resolved by the
	// provider-backed lookup; empty for a human and for a verified bot
	// no-match.
	BotRole string
	// RoleVerified must be true: only a completed authoritative lookup may be
	// cached. Failed, timed-out, unresolved, or partially verified results
	// and caller claims are rejected.
	RoleVerified bool
	// LookupStart is when the authoritative lookup started. Expiry is
	// measured from this instant and never extended.
	LookupStart time.Time
}

// validate checks that e is a completed verdict consistent with kind.
func (e Entry) validate(kind normevent.ActorKind) error {
	if !e.RoleVerified {
		return ErrUnverified
	}
	if e.LookupStart.IsZero() {
		return fmt.Errorf("%w: lookup start is required", ErrUncertainAge)
	}
	switch kind {
	case normevent.ActorBot:
		if e.Role != normevent.RoleNone {
			return fmt.Errorf("%w: bot role must be %q, got %q", ErrInvalidEntry, normevent.RoleNone, e.Role)
		}
	case normevent.ActorHuman:
		if e.BotRole != "" {
			return fmt.Errorf("%w: human actor cannot carry a bot role", ErrInvalidEntry)
		}
		if !knownHumanRole(e.Role) {
			return fmt.Errorf("%w: unknown human role %q", ErrInvalidEntry, e.Role)
		}
	}
	return nil
}

func knownHumanRole(r normevent.ActorRole) bool {
	switch r {
	case normevent.RoleAdmin, normevent.RoleMaintain, normevent.RoleWrite,
		normevent.RoleTriage, normevent.RoleRead, normevent.RoleNone,
		normevent.RoleExternal:
		return true
	}
	return false
}

// Errors returned by [Cache.Put].
var (
	// ErrInvalidKey reports a key missing a required scope dimension.
	ErrInvalidKey = errors.New("actorcache: invalid key")
	// ErrInvalidEntry reports a verdict whose shape does not match its kind.
	ErrInvalidEntry = errors.New("actorcache: invalid entry")
	// ErrUnverified reports a verdict from an incomplete or failed lookup.
	ErrUnverified = errors.New("actorcache: verdict is not verified")
	// ErrUncertainAge reports a verdict whose age cannot be established.
	ErrUncertainAge = errors.New("actorcache: verdict age is uncertain")
	// ErrInvalidated reports a verdict whose lookup started no later than a
	// known authorization change for the same actor.
	ErrInvalidated = errors.New("actorcache: verdict predates invalidation")
)

// Cache is the bounded actor-verdict cache for entity-source CEL.
//
// Implementations must be safe for concurrent use, enforce absolute expiry
// from [Entry.LookupStart] with a trusted clock, never extend expiry on a
// hit, and treat an entry of uncertain age as unusable.
type Cache interface {
	// Get returns the unexpired verdict stored under key, or false on a miss.
	// A miss requires a new authoritative lookup.
	Get(key Key) (Entry, bool)
	// Put stores a completed verdict under key. It rejects invalid keys,
	// unverified or malformed verdicts, verdicts of uncertain age, and
	// verdicts whose lookup started no later than the actor's most recent
	// invalidation. An already-expired verdict is not stored.
	Put(key Key, entry Entry) error
	// Invalidate drops every entry for actor across all scopes. Callers use
	// it when a known role, membership, OWNERS, OWNERS_ALIASES, bot-registry,
	// or effective-policy change affects the actor.
	Invalidate(actor ActorID)
}
