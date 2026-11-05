package repos

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// PollerCredentialProvenance reads the Poller's credential provenance. A name
// match alone never proves fullsend minted a credential, so callers use this
// provenance to avoid revoking or re-membering a supplied credential. An
// unreadable or invalid state is an error so callers fail closed; a missing
// document or Poller entry is reported as unknown provenance, which callers
// must not treat as managed.
func PollerCredentialProvenance(ctx context.Context, client forge.Client, owner, repo string) (RoleProvenance, error) {
	provs, err := RoleCredentialProvenances(ctx, client, owner, repo)
	if err != nil {
		return RoleProvenance{}, err
	}
	return provs[gitlabroles.RolePoller], nil
}

// RoleCredentialProvenances reads the credential provenance of every role that
// has a rotation-state entry, keyed by role. A role without an entry is absent
// from the map, which callers must treat as unknown rather than managed.
func RoleCredentialProvenances(ctx context.Context, client forge.Client, owner, repo string) (map[gitlabroles.Role]RoleProvenance, error) {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("reading GitLab role rotation state: %w", err)
	}
	out := make(map[gitlabroles.Role]RoleProvenance, len(state.Roles))
	for role, rs := range state.Roles {
		if err := validateRecordedExclusionIDs(role, rs); err != nil {
			return nil, err
		}
		out[gitlabroles.Role(role)] = provenanceOf(rs)
	}
	return out, nil
}

// RecordSuppliedExclusions durably adds administrator-owned account IDs to a
// role's exclusions. A role with no entry gets an exclusions-only entry, so the
// exclusions are never silently dropped. It is idempotent and writes only when
// something is new. Callers hold the project lease.
func RecordSuppliedExclusions(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, ids []int) error {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return fmt.Errorf("reading rotation state before recording supplied exclusions: %w", err)
	}
	cur := state.Roles[string(role)]
	before := len(cur.ExcludedUserIDs)
	for _, id := range ids {
		cur.excludeOwner(id)
	}
	if len(cur.ExcludedUserIDs) == before {
		return nil
	}
	state.Roles[string(role)] = cur
	return writeRotationState(ctx, client, owner, repo, state)
}

// RecordSuppliedOwners durably records the resolved owner of each role whose
// installed credential is administrator-supplied but whose rotation entry has
// no recorded owner. Once recorded, a later attempt reads the owner from state
// instead of re-authenticating with a credential that may since have been
// deleted. Roles that are not supplied or already have an owner are left alone,
// and every other recorded field of an updated entry is preserved. It is
// idempotent and writes only when something changes. Callers hold the project
// lease.
func RecordSuppliedOwners(ctx context.Context, client forge.Client, owner, repo string, owners map[gitlabroles.Role]int) error {
	if len(owners) == 0 {
		return nil
	}
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return fmt.Errorf("reading rotation state before recording supplied owners: %w", err)
	}
	changed := false
	for role, id := range owners {
		cur, ok := state.Roles[string(role)]
		if id <= 0 || !ok || !provenanceOf(cur).Supplied || cur.SuppliedUserID != 0 {
			continue
		}
		cur.excludeOwner(id)
		cur.Supplied = true
		cur.SuppliedUserID = id
		state.Roles[string(role)] = cur
		changed = true
	}
	if !changed {
		return nil
	}
	return writeRotationState(ctx, client, owner, repo, state)
}

// SuppliedIdentity is the GitLab owner and token ID of an administrator-supplied
// credential, resolved by authenticating with it.
//
// When Described is set the token's own metadata (its name, scopes, expiry and
// state as GitLab reports them for the authenticated token) is populated too,
// and enrollment rejects a credential that does not satisfy the role's
// credential policy (see validateSuppliedCredentialPolicy).
type SuppliedIdentity struct {
	UserID  int
	TokenID int

	Described bool
	Name      string
	Scopes    []string
	ExpiresAt string
	Active    bool
	Revoked   bool
}

// ErrInstalledCredentialRejected reports that GitLab authoritatively refused
// the installed role credential (it is revoked, expired or otherwise no longer
// authenticates). Any other failure to authenticate it is only an
// unverifiable result and never evidence that the credential is unhealthy.
var ErrInstalledCredentialRejected = errors.New("installed role credential no longer authenticates")

// ErrSuppliedVerificationUnavailable reports that the token client cannot
// authenticate a supplied credential or verify its owner's project access, so
// the credential is not enrolled.
var ErrSuppliedVerificationUnavailable = errors.New("supplied credential verification is unavailable: the token client cannot authenticate credentials and verify project access")

// suppliedCredentialPolicyDate is GitLab's expires_at layout.
const suppliedCredentialPolicyDate = "2006-01-02"

// validateSuppliedCredentialPolicy checks an authenticated administrator-
// supplied credential against the policy fullsend applies to the credentials it
// mints for the role: the token must be active and not revoked, carry the
// role's token name and exactly the scopes fullsend mints, and expire within
// the permitted lifetime without already having expired. Identities without
// metadata (Described unset) are not checked. The error never contains token
// material.
func validateSuppliedCredentialPolicy(id SuppliedIdentity, tokenName string, now time.Time) error {
	if !id.Described {
		return nil
	}
	if !id.Active || id.Revoked {
		return errors.New("the supplied credential is not an active token")
	}
	if id.Name != tokenName {
		return fmt.Errorf("the supplied credential's token name %q is not the role's token name %q", id.Name, tokenName)
	}
	want := slices.Clone(gitlabroles.TokenScopes())
	got := slices.Clone(id.Scopes)
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		return fmt.Errorf("the supplied credential's scopes %v are not exactly the required scopes %v", id.Scopes, gitlabroles.TokenScopes())
	}
	if id.ExpiresAt == "" {
		return errors.New("the supplied credential has no expiry")
	}
	exp, err := time.Parse(suppliedCredentialPolicyDate, id.ExpiresAt)
	if err != nil {
		return errors.New("the supplied credential has a malformed expiry")
	}
	if exp.Before(startOfUTCDay(now)) {
		return errors.New("the supplied credential has already expired")
	}
	limit, err := time.Parse(suppliedCredentialPolicyDate, GitLabPATExpiresAt(now))
	if err != nil {
		return fmt.Errorf("computing the permitted credential lifetime: %w", err)
	}
	if exp.After(limit) {
		return errors.New("the supplied credential expires later than the permitted lifetime")
	}
	return nil
}

func startOfUTCDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// installedCredentialHealth reports whether the authenticated installed
// credential is still usable: active, not revoked, and not past its expiry
// date. A token with no expiry never expires. An expiry that is present but
// malformed is not treated as healthy.
func installedCredentialHealth(id SuppliedIdentity, now time.Time) (healthy bool, reason string) {
	if !id.Active || id.Revoked {
		return false, "it is revoked or inactive"
	}
	if id.ExpiresAt == "" {
		return true, ""
	}
	exp, err := time.Parse(suppliedCredentialPolicyDate, id.ExpiresAt)
	if err != nil {
		return false, "its expiry is malformed"
	}
	// GitLab treats expires_at as the last valid day (UTC).
	if exp.Before(startOfUTCDay(now)) {
		return false, "it has expired"
	}
	return true, ""
}

// recordSuppliedEnrollment records an administrator-provided credential's
// enrollment as an explicit supplied transition. It replaces whatever entry the
// role holds, including a managed one or the stale supplied entry left when the
// credential variable was removed but its rotation state remained: it records
// the new owner and token ID (when the identity resolved) with distribution
// proof, permanently excludes both the new owner and any previously recorded
// supplied owner, and preserves creation records, managed-account ownership,
// earlier exclusions, any rotation lock and Poller generations. Managed tokens
// the previous entry still tracked (its incoming token and outgoing
// obligations) stay recorded as outgoing obligations instead of being
// forgotten. The enrolled entry stays idle so its distribution proof is
// unchanged, which means nothing retires them automatically until a later
// managed replacement runs grace cleanup; their count is returned so the caller
// can report that they stay usable until revoked or expired. An unresolved
// identity (zero UserID) records no owner; the entry stays supplied so later
// operations attribute it or fail closed. It returns a rollback that restores
// the role's previous entry, for use when the credential then fails to publish:
// an ownerless supplied entry with no installed secret would otherwise fail
// every later attribution closed and refuse an ordinary retry. The rollback
// keeps the new owner excluded. Callers hold the project lease.
func recordSuppliedEnrollment(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, id SuppliedIdentity, now time.Time) (func(context.Context) error, int, error) {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return nil, 0, fmt.Errorf("reading rotation state before enrollment proof: %w", err)
	}
	previous, hadPrevious := state.Roles[string(role)]
	enrolled := rotationRoleState{
		CreatedTokenIDs: previous.CreatedTokenIDs,
		ManagedUserID:   previous.ManagedUserID,
		OutgoingIDs:     retainedManagedTokenIDs(previous, id.TokenID),
		Holder:          previous.Holder,
		LockUntil:       previous.LockUntil,
		Phase:           rotationPhaseIdle,
		DistributedAt:   now.UTC().Format(time.RFC3339),
		Supplied:        true,
		ExcludedUserIDs: append([]int(nil), previous.ExcludedUserIDs...),
		// Poller identity generations describe accounts, not this credential.
		GenerationCurrentUserID:  previous.GenerationCurrentUserID,
		GenerationPendingUserID:  previous.GenerationPendingUserID,
		GenerationPendingPhase:   previous.GenerationPendingPhase,
		GenerationRetiringUserID: previous.GenerationRetiringUserID,
	}
	enrolled.excludeOwner(previous.SuppliedUserID)
	if id.UserID > 0 {
		enrolled.excludeOwner(id.UserID)
		enrolled.SuppliedUserID = id.UserID
		if id.TokenID > 0 {
			enrolled.SuppliedTokenID = id.TokenID
		}
	}
	state.Roles[string(role)] = enrolled
	if err := writeRotationState(ctx, client, owner, repo, state); err != nil {
		return nil, 0, err
	}
	rollback := func(ctx context.Context) error {
		cur, _, err := loadRotationState(ctx, client, owner, repo)
		if err != nil {
			return fmt.Errorf("reading rotation state before enrollment rollback: %w", err)
		}
		switch {
		case hadPrevious:
			restored := previous
			restored.ExcludedUserIDs = append([]int(nil), enrolled.ExcludedUserIDs...)
			cur.Roles[string(role)] = restored
		case id.UserID > 0:
			// Keep the replacement owner's exclusion in an exclusions-only entry.
			cur.Roles[string(role)] = rotationRoleState{ExcludedUserIDs: []int{id.UserID}}
		default:
			delete(cur.Roles, string(role))
		}
		return writeRotationState(ctx, client, owner, repo, cur)
	}
	return rollback, len(enrolled.OutgoingIDs), nil
}

// retainedManagedTokenIDs returns the managed tokens a role entry still tracks:
// its outgoing obligations plus its incoming token, without duplicates,
// non-positive IDs, or the enrolled credential's own token ID. Ownership and
// exclusion checks stay with grace cleanup, which verifies each token before
// revoking it.
func retainedManagedTokenIDs(previous rotationRoleState, enrolledTokenID int) []int {
	var out []int
	for _, id := range append(append([]int(nil), previous.OutgoingIDs...), previous.IncomingID) {
		if id > 0 && id != enrolledTokenID && !containsInt(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// recordProvisioningIntent persists a recoverable intent before missing-secret
// provisioning publishes a managed credential. The entry moves to the
// incomplete distributing phase with the replacement token as incoming, so a
// failed completion write leaves state that needsRecovery resolves on the next
// run. Without it, a fresh credential would have no incoming ID or distribution
// proof and read as unknown provenance, which the ownership resolvers refuse
// project-wide; a role entry that still describes a removed supplied
// credential (the secret was removed but its rotation state remained) would be
// retained as that credential forever. The intent applies to a role with no
// entry, to an entry carrying only ownership or exclusion records (the
// first-distribution shapes), and to a supplied entry, which keeps its supplied
// provenance and permanent exclusions until completion lands. Any other entry
// already carries lifecycle state and yields a nil rollback. The rollback
// restores the previous entry (or removes the intent-only entry) for use when
// the credential then fails to publish. Callers hold the project lease.
func recordProvisioningIntent(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, tokenID int) (func(context.Context) error, error) {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("reading rotation state before recording the provisioning intent: %w", err)
	}
	previous, ok := state.Roles[string(role)]
	if tokenID == 0 || (ok && !provenanceOf(previous).Supplied && !initialDistributionEligible(previous, tokenID)) {
		return nil, nil
	}
	intent := previous
	intent.ExcludedUserIDs = append([]int(nil), previous.ExcludedUserIDs...)
	intent.excludeOwner(previous.SuppliedUserID)
	intent.Phase = rotationPhaseDistributing
	intent.IncomingID = tokenID
	state.Roles[string(role)] = intent
	if err := writeRotationState(ctx, client, owner, repo, state); err != nil {
		return nil, err
	}
	rollback := func(ctx context.Context) error {
		cur, _, err := loadRotationState(ctx, client, owner, repo)
		if err != nil {
			return fmt.Errorf("reading rotation state before provisioning intent rollback: %w", err)
		}
		if !ok {
			delete(cur.Roles, string(role))
			return writeRotationState(ctx, client, owner, repo, cur)
		}
		restored := previous
		restored.ExcludedUserIDs = append([]int(nil), intent.ExcludedUserIDs...)
		cur.Roles[string(role)] = restored
		return writeRotationState(ctx, client, owner, repo, cur)
	}
	return rollback, nil
}

// recordProvisionedDistribution records distribution proof for a managed
// credential that missing-secret provisioning just minted and published. A role
// with no lifecycle entry, or one carrying only ownership or exclusion records,
// takes the create-if-absent proof of recordInitialDistribution. A role whose
// entry carries the provisioning intent for this token, or still describes a
// supplied credential (the secret was removed but its rotation state
// remained), takes an explicit managed-replacement transition instead: the
// published credential is the managed one, so the supplied provenance is cleared, the previous supplied owner stays permanently
// excluded, and creation records, managed-account ownership, earlier
// exclusions, unconfirmed outgoing IDs, any rotation lock and Poller
// generations are preserved. Callers hold the project lease.
func recordProvisionedDistribution(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, tokenID int, expiresAt string, now time.Time) error {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return fmt.Errorf("reading rotation state before provisioned distribution proof: %w", err)
	}
	existing, ok := state.Roles[string(role)]
	// An entry still carrying this token as the incomplete distributing intent
	// (recordProvisioningIntent) completes through the same transition; its
	// supplied clearing below is a no-op for a managed entry.
	completesIntent := ok && existing.Phase == rotationPhaseDistributing && existing.IncomingID == tokenID
	if !ok || (!provenanceOf(existing).Supplied && !completesIntent) {
		return recordInitialDistribution(ctx, client, owner, repo, role, tokenID, expiresAt, now)
	}
	existing.excludeOwner(existing.SuppliedUserID)
	existing.Supplied, existing.SuppliedUserID, existing.SuppliedTokenID, existing.SuppliedDistributed = false, 0, 0, false
	existing.Phase = rotationPhaseIdle
	if len(existing.OutgoingIDs) > 0 {
		existing.Phase = rotationPhaseOverlapping
	}
	existing.IncomingID = tokenID
	existing.DistributedAt = now.UTC().Format(time.RFC3339)
	existing.ExpiresAt = expiresAt
	existing.Error = ""
	state.Roles[string(role)] = existing
	return writeRotationState(ctx, client, owner, repo, state)
}

// applySuppliedTransition marks rs as describing an administrator-supplied
// credential owned by id. A previously recorded supplied owner is moved into the
// permanent exclusions and is not carried over as the new credential's owner;
// the new owner (when resolved) is excluded and recorded. An unresolved
// identity leaves the entry supplied without an owner.
func applySuppliedTransition(rs *rotationRoleState, id SuppliedIdentity) {
	rs.excludeOwner(rs.SuppliedUserID)
	rs.Supplied = true
	rs.SuppliedUserID, rs.SuppliedTokenID = 0, 0
	if id.UserID > 0 {
		rs.excludeOwner(id.UserID)
		rs.SuppliedUserID = id.UserID
		if id.TokenID > 0 {
			rs.SuppliedTokenID = id.TokenID
		}
	}
}

// resolveSuppliedIdentity authenticates a supplied credential value through the
// token client. A client without that capability yields a zero identity, which
// records nothing.
func resolveSuppliedIdentity(ctx context.Context, tokens ProjectAccessTokenClient, provided string) (SuppliedIdentity, error) {
	sa, ok := normalizeServiceAccountClient(tokens).(ServiceAccountTokenClient)
	if !ok || sa.SuppliedCredentialIdentity == nil {
		return SuppliedIdentity{}, nil
	}
	id, err := sa.SuppliedCredentialIdentity(ctx, provided)
	if err != nil {
		return SuppliedIdentity{}, safeAPIError("resolving the supplied credential owner", err)
	}
	return id, nil
}

// verifiedSuppliedIdentity authenticates an administrator-supplied credential
// for enrollment and enforces the role's credential policy and the owner's
// effective access on the target project. A failure to authenticate or to
// verify, an identity without metadata from a client that can describe
// credentials, a policy violation and an unacceptable access level are all
// errors, so the caller reports a per-role failure and neither records
// enrollment nor publishes the value. A token client that cannot authenticate
// supplied credentials or verify the owner's project access is also an error:
// enrollment is never published unverified. The error never contains token
// material.
func verifiedSuppliedIdentity(ctx context.Context, tokens ProjectAccessTokenClient, owner, repo, provided, tokenName string, now time.Time) (SuppliedIdentity, error) {
	sa, ok := normalizeServiceAccountClient(tokens).(ServiceAccountTokenClient)
	if !ok || sa.SuppliedCredentialIdentity == nil || sa.VerifySuppliedAccess == nil {
		return SuppliedIdentity{}, ErrSuppliedVerificationUnavailable
	}
	id, err := resolveSuppliedIdentity(ctx, tokens, provided)
	if err != nil {
		return SuppliedIdentity{}, fmt.Errorf("the supplied credential could not be verified: %w", err)
	}
	if !id.Described {
		return SuppliedIdentity{}, errors.New("the supplied credential's token metadata could not be verified")
	}
	if err := validateSuppliedCredentialPolicy(id, tokenName, now); err != nil {
		return SuppliedIdentity{}, err
	}
	if err := sa.VerifySuppliedAccess(ctx, owner, repo, id); err != nil {
		return SuppliedIdentity{}, err
	}
	// A supplied Poller dispatches pipelines on the protected default branch
	// like a minted one, so its owner needs that access before publication.
	if tokenName == gitlabroles.PollerTokenName && sa.VerifySuppliedPollerAccess != nil {
		if err := sa.VerifySuppliedPollerAccess(ctx, owner, repo, id.UserID); err != nil {
			return SuppliedIdentity{}, safeAPIError("verifying the supplied Poller's protected-branch pipeline access", err)
		}
	}
	return id, nil
}

// establishSuppliedPollerAccess grants a supplied Poller's owner protected-
// default-branch pipeline access and confirms it, immediately before the
// credential is published and after its owner is recorded in rotation state. It
// is a no-op for other roles and for clients without the callback. An error
// means the credential must not be published.
func establishSuppliedPollerAccess(ctx context.Context, tokens ProjectAccessTokenClient, owner, repo, tokenName string, id SuppliedIdentity) error {
	if tokenName != gitlabroles.PollerTokenName {
		return nil
	}
	sa, ok := normalizeServiceAccountClient(tokens).(ServiceAccountTokenClient)
	if !ok || sa.EstablishSuppliedPollerAccess == nil {
		return nil
	}
	if err := sa.EstablishSuppliedPollerAccess(ctx, owner, repo, id.UserID); err != nil {
		return safeAPIError("establishing the supplied Poller's protected-branch pipeline access", err)
	}
	return nil
}

// suppliedPollerGrantProbe records whether a supplied Poller's owner could
// already create pipelines on the protected default branch before enrollment
// establishes access, and which merge-access entries the rule already listed.
// A grant is only attributed to an enrollment that found no access first, so a
// pre-existing administrator grant is never reported or removed as this
// enrollment's. The recorded entry IDs let rollback recognize the exact entry
// the enrollment added.
type suppliedPollerGrantProbe struct {
	applies       bool
	preKnown      bool
	hadAccess     bool
	priorMergeIDs map[int]struct{}
}

// probeSuppliedPollerGrant reads the protected-branch rule before
// establishSuppliedPollerAccess. It applies only to a Poller with a resolved
// owner; an unreadable or unsupported rule leaves the pre-state unknown.
func probeSuppliedPollerGrant(ctx context.Context, client forge.Client, owner, repo, tokenName string, id SuppliedIdentity) suppliedPollerGrantProbe {
	if tokenName != gitlabroles.PollerTokenName || id.UserID <= 0 || client == nil {
		return suppliedPollerGrantProbe{}
	}
	_, rule, err := loadDefaultBranchRule(ctx, client, owner, repo)
	if err != nil {
		return suppliedPollerGrantProbe{applies: !forge.IsNotSupported(err)}
	}
	p := suppliedPollerGrantProbe{applies: true, preKnown: true, hadAccess: PollerCanCreatePipeline(rule, []int{id.UserID})}
	if rule != nil {
		p.priorMergeIDs = make(map[int]struct{}, len(rule.MergeAccessLevels))
		for _, l := range rule.MergeAccessLevels {
			if l.ID > 0 {
				p.priorMergeIDs[l.ID] = struct{}{}
			}
		}
	}
	return p
}

// rollback undoes the merge grant a failed supplied-Poller enrollment added on
// the protected default branch and returns a warning only when a grant may
// remain, or "" when none was added or it was removed.
//
// The grant is attributed to this enrollment only when the pre-enrollment read
// showed the owner without access and the reread now shows a user merge entry
// for the owner whose entry ID was not in the pre-enrollment rule. Only that
// exact entry is removed, by entry ID: never by user ID, and never an entry
// that existed before enrollment, so an administrator's own grant is untouched.
// When the pre-state is unknown, the reread fails, the entry carries no ID, or
// removal fails, the grant cannot be safely removed and is reported for manual
// removal instead (see retainedDiagnostic).
//
// Reread and removal use a fresh bounded context so they still run after a
// cancelled enrollment context, and warnings never echo server text.
func (p suppliedPollerGrantProbe) rollback(ctx context.Context, client forge.Client, owner, repo, role string, id SuppliedIdentity) string {
	if !p.applies || (p.preKnown && p.hadAccess) {
		return ""
	}
	if !p.preKnown {
		return p.retainedDiagnostic(ctx, client, owner, repo, role, id)
	}
	cleanupCtx, cancel := gitlabCleanupContext(ctx)
	defer cancel()
	branch, rule, err := loadDefaultBranchRule(cleanupCtx, client, owner, repo)
	if err != nil {
		return p.retainedNote(role, id, branch, rule, err)
	}
	if !PollerCanCreatePipeline(rule, []int{id.UserID}) {
		return ""
	}
	var added []int
	unremovable := false
	for _, l := range rule.MergeAccessLevels {
		if l.UserID != id.UserID {
			continue
		}
		if _, existed := p.priorMergeIDs[l.ID]; l.ID > 0 && !existed {
			added = append(added, l.ID)
		} else {
			unremovable = true
		}
	}
	if len(added) == 0 && !unremovable {
		// Access comes from something other than a user entry this enrollment
		// could have added, so there is nothing of its own to undo.
		return ""
	}
	for _, entryID := range added {
		if err := client.RevokeProtectedBranchMergeUserEntry(cleanupCtx, owner, repo, branch, entryID, id.UserID); err != nil {
			return fmt.Sprintf("%s: enrollment failed and removing the merge access this enrollment granted the supplied Poller (user ID %d) on protected branch %q failed; the grant (protected-branch entry %d) was retained, so remove it in Settings → Repository → Protected branches if the credential is not enrolled", role, id.UserID, branch, entryID)
		}
	}
	if unremovable {
		return p.retainedNote(role, id, branch, rule, nil)
	}
	return ""
}

// retainedDiagnostic reports a merge grant that a failed supplied-Poller
// enrollment may have left on the protected default branch, or "" when none
// was added by it, without attempting removal. It is used when removal is not
// safe: the pre-enrollment state is unknown, or publication may have succeeded
// so the grant may still be needed. Removing a grant by user ID could also
// remove an administrator's own entry, so the grant is reported for manual
// removal. The reread uses a fresh bounded context so it still runs after a
// cancelled enrollment context, and it never echoes server text.
func (p suppliedPollerGrantProbe) retainedDiagnostic(ctx context.Context, client forge.Client, owner, repo, role string, id SuppliedIdentity) string {
	if !p.applies || (p.preKnown && p.hadAccess) {
		return ""
	}
	cleanupCtx, cancel := gitlabCleanupContext(ctx)
	defer cancel()
	branch, rule, err := loadDefaultBranchRule(cleanupCtx, client, owner, repo)
	return p.retainedNote(role, id, branch, rule, err)
}

// retainedNote words the warning for a rule read (or its failure) after a
// failed enrollment.
func (p suppliedPollerGrantProbe) retainedNote(role string, id SuppliedIdentity, branch string, rule *forge.ProtectedBranchRule, err error) string {
	switch {
	case err != nil:
		return fmt.Sprintf("%s: enrollment failed and the protected-branch merge access of the supplied Poller (user ID %d) could not be reread; a grant added by this enrollment may remain on the default branch, so inspect Settings → Repository → Protected branches and remove it if the credential is not enrolled", role, id.UserID)
	case !PollerCanCreatePipeline(rule, []int{id.UserID}):
		return ""
	case !p.preKnown:
		return fmt.Sprintf("%s: enrollment failed and the supplied Poller (user ID %d) can create pipelines on protected branch %q; a grant added by this enrollment may remain, so remove it in Settings → Repository → Protected branches if the credential is not enrolled", role, id.UserID, branch)
	default:
		return fmt.Sprintf("%s: enrollment failed after this enrollment granted the supplied Poller (user ID %d) merge access on protected branch %q; the grant was retained, so remove it in Settings → Repository → Protected branches if the credential is not enrolled", role, id.UserID, branch)
	}
}

// suppliedRoleSecretAbsent reports whether a read-back positively shows the
// install-path role secret is still absent after a failed write. A write error
// does not prove the value was not committed, so an unreadable secret or a
// present one is not "absent". The read-back runs on a bounded context detached
// from cancellation so a cancelled enrollment still resolves it.
func suppliedRoleSecretAbsent(ctx context.Context, client forge.Client, owner, repo, secret string) bool {
	readCtx, cancel := gitlabCleanupContext(ctx)
	defer cancel()
	exists, err := client.RepoSecretExists(readCtx, owner, repo, secret)
	return err == nil && !exists
}

// replacementNotInstalled reports whether the installed role credential
// positively authenticates as a different owner than the supplied replacement,
// or holds no value, which proves a failed replacement write was not committed.
// A client that cannot authenticate installed credentials, a failed
// authentication, or an installed credential owned by the replacement's owner
// leaves it unproven. The check runs on a bounded context detached from
// cancellation.
func replacementNotInstalled(ctx context.Context, tokens ProjectAccessTokenClient, owner, repo, secret string, replacement SuppliedIdentity) bool {
	sa, ok := normalizeServiceAccountClient(tokens).(ServiceAccountTokenClient)
	if !ok || sa.InstalledRoleCredentialSelf == nil || replacement.UserID <= 0 {
		return false
	}
	checkCtx, cancel := gitlabCleanupContext(ctx)
	defer cancel()
	installed, present, err := sa.InstalledRoleCredentialSelf(checkCtx, owner, repo, secret)
	if err != nil {
		return false
	}
	if !present {
		return true
	}
	return installed.UserID > 0 && installed.UserID != replacement.UserID
}

// settleSuppliedPollerGrant resolves the merge grant of a failed supplied-Poller
// enrollment after a failed credential write: it rolls the grant back when the
// write is proven not to have been committed, and otherwise reports the grant
// as retained, because a credential that may be installed still needs the
// access. It returns the warning to surface, or "".
func (p suppliedPollerGrantProbe) settleSuppliedPollerGrant(ctx context.Context, client forge.Client, owner, repo, role string, id SuppliedIdentity, writeNotCommitted bool) string {
	if writeNotCommitted {
		return p.rollback(ctx, client, owner, repo, role, id)
	}
	return p.retainedDiagnostic(ctx, client, owner, repo, role, id)
}

// installedCredentialCheck is the outcome of authenticating an installed
// administrator-supplied role credential.
type installedCredentialCheck int

const (
	// installedCredentialUnchecked: the token client cannot authenticate the
	// installed credential, so nothing is known about its health.
	installedCredentialUnchecked installedCredentialCheck = iota
	// installedCredentialHealthy: it authenticates and has not expired.
	installedCredentialHealthy
	// installedCredentialUnhealthy: GitLab refused it, it holds no value, or the
	// authenticated token is revoked, inactive or expired.
	installedCredentialUnhealthy
	// installedCredentialUnverifiable: authentication failed for a reason that
	// does not prove the credential is bad (network failure, throttling, server
	// error), so it must be left untouched.
	installedCredentialUnverifiable
)

// suppliedTokenPositivelyUnusable reports whether a listed token is known to be
// revoked, inactive or expired. A malformed or unparseable expiry is not proof
// of unusability, so such a token is still subject to owner-access verification.
func suppliedTokenPositivelyUnusable(tok ProjectAccessToken, now time.Time) bool {
	if !tok.Active || tok.Revoked {
		return true
	}
	expiresAt := strings.TrimSpace(tok.ExpiresAt)
	if expiresAt == "" {
		return false
	}
	exp, err := time.Parse(suppliedCredentialPolicyDate, expiresAt)
	if err != nil {
		return false
	}
	// GitLab treats expires_at as the last valid day (UTC).
	return exp.Before(startOfUTCDay(now))
}

// verifyListedSuppliedAccess reverifies that the owner of each listed enrolled
// supplied credential still holds exactly effective Developer access. Rotation
// and status both call it for credentials the token inventory lists, which the
// installed-secret check does not cover. A client without a verifier, or an
// owner that cannot be determined, is an error: access that was not checked is
// never treated as verified. A listed credential that is positively revoked, inactive
// or expired is unusable whatever its owner's access, so it is not verified here
// (a malformed expiry is not positive proof, so it is still verified):
// the lifecycle diagnosis reports it, and rotation replaces it as it does an
// unhealthy credential outside the inventory.
func (c ServiceAccountTokenClient) verifyListedSuppliedAccess(ctx context.Context, owner, repo string, rs rotationRoleState, matches []ProjectAccessToken, now time.Time) error {
	if c.VerifySuppliedAccess == nil {
		return errors.New("the owner's effective project access cannot be verified as Developer: no access verifier is configured")
	}
	for _, tok := range suppliedCredentialTokens(rs, matches) {
		if suppliedTokenPositivelyUnusable(tok, now) {
			continue
		}
		userID := tok.UserID
		if userID <= 0 {
			userID = rs.SuppliedUserID
		}
		if userID <= 0 {
			return errors.New("the owner's effective project access cannot be verified as Developer: the credential carries no owner user ID")
		}
		if err := c.VerifySuppliedAccess(ctx, owner, repo, SuppliedIdentity{UserID: userID}); err != nil {
			return fmt.Errorf("the owner's effective project access is not verified as Developer: %w", err)
		}
	}
	return nil
}

// checkInstalledSuppliedCredential authenticates the installed role secret
// through the token client. The returned reason never contains token material.
func checkInstalledSuppliedCredential(ctx context.Context, tokens ProjectAccessTokenClient, owner, repo, secretName string, now time.Time) (installedCredentialCheck, SuppliedIdentity, string) {
	sa, ok := normalizeServiceAccountClient(tokens).(ServiceAccountTokenClient)
	if !ok || sa.InstalledRoleCredentialSelf == nil {
		return installedCredentialUnchecked, SuppliedIdentity{}, ""
	}
	id, installed, err := sa.InstalledRoleCredentialSelf(ctx, owner, repo, secretName)
	switch {
	case errors.Is(err, ErrInstalledCredentialRejected):
		return installedCredentialUnhealthy, SuppliedIdentity{}, "it no longer authenticates"
	case err != nil:
		return installedCredentialUnverifiable, SuppliedIdentity{}, safeAPIError("authenticating the installed credential", err).Error()
	case !installed:
		return installedCredentialUnhealthy, SuppliedIdentity{}, "its secret holds no value"
	}
	if healthy, reason := installedCredentialHealth(id, now); !healthy {
		return installedCredentialUnhealthy, id, reason
	}
	// Enrollment checked the owner's access once; membership can change later.
	// An owner whose access is now elevated, or cannot be verified, is reported
	// without touching the administrator-owned account or revoking its token.
	// A missing verifier or an owner without a user ID cannot prove the ceiling
	// holds, so neither is reported as healthy.
	if sa.VerifySuppliedAccess == nil {
		return installedCredentialUnverifiable, id, "the owner's effective project access cannot be verified as Developer: no access verifier is configured"
	}
	if id.UserID <= 0 {
		return installedCredentialUnverifiable, id, "the owner's effective project access cannot be verified as Developer: the credential carries no owner user ID"
	}
	if err := sa.VerifySuppliedAccess(ctx, owner, repo, id); err != nil {
		return installedCredentialUnverifiable, id, "the owner's effective project access is not verified as Developer: " + err.Error()
	}
	return installedCredentialHealthy, id, ""
}

// AppendGitLabSuppliedCredentialStatus authenticates every enrolled
// administrator-supplied role credential that lies outside the token
// inventories and reports the same health classification rotation applies. A
// credential GitLab refuses, one that holds no value and an expired one are
// drift; one that cannot be verified for a transient reason, or because the
// client cannot authenticate installed credentials, is reported as unverified,
// never as healthy. A credential whose supplied publication is unresolved is
// also reported as unverified. tokens is the project inventory the
// lifecycle diagnosis used. It reports whether entries were added to
// status.Drifts.
func (c ServiceAccountTokenClient) AppendGitLabSuppliedCredentialStatus(ctx context.Context, client forge.Client, owner, repo string, tokens []ProjectAccessToken, now time.Time, status *RepoStatus) bool {
	if status == nil || client == nil {
		return false
	}
	reg, present, err := LoadGitLabRoleState(ctx, client, owner, repo)
	if err != nil {
		return false
	}
	rotation, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return false
	}
	seen := make(map[string]struct{}, len(status.Drifts))
	for _, d := range status.Drifts {
		seen[d.Field] = struct{}{}
	}
	changed := false
	for _, rec := range reg.Registrations() {
		secret := rec.Credential.SecretName
		if rec.Credential.Kind == gitlabroles.CredentialReuse || secret == "" || !present[secret] {
			continue
		}
		rs := rotation.Roles[string(rec.Name)]
		field := "gitlab-role:" + string(rec.Name)
		// An interrupted or ambiguously failed supplied publication leaves the
		// installed credential unknown and holds automatic rotation, so the
		// role must not read as ready and drift-free. This holds even when the
		// previous, managed provenance was restored after a failed write that
		// may have committed.
		if providedPublicationUnresolved(rs) {
			if _, ok := seen[field]; !ok {
				seen[field] = struct{}{}
				serviceAccountStatusUnverified(status, field, fmt.Sprintf("the administrator-supplied credential for role %s has an unresolved publication; the installed credential is unknown and automatic rotation is held", rec.Name))
				changed = true
			}
			continue
		}
		// Only an enrolled, idle and distributed credential is checked.
		if !provenanceOf(rs).Supplied || rs.IncomingID != 0 || rs.Phase != rotationPhaseIdle || rs.DistributedAt == "" {
			continue
		}
		// A listed credential is judged by the token inventory, but its owner's
		// project access can still change after enrollment, so it is reverified.
		if matches := tokensNamed(tokens, roleTokenName(rec)); suppliedCredentialListed(rs, matches) {
			if err := c.verifyListedSuppliedAccess(ctx, owner, repo, rs, matches, now); err != nil {
				if _, ok := seen[field]; !ok {
					seen[field] = struct{}{}
					serviceAccountStatusUnverified(status, field, fmt.Sprintf("the administrator-supplied credential for role %s: %s", rec.Name, err))
					changed = true
				}
			}
			continue
		}
		check, _, reason := checkInstalledSuppliedCredential(ctx, c, owner, repo, secret, now)
		switch check {
		case installedCredentialUnchecked:
			// Without installed-credential authentication nothing is known about
			// an outside-inventory credential, so it is never reported healthy.
			if _, ok := seen[field]; !ok {
				seen[field] = struct{}{}
				serviceAccountStatusUnverified(status, field, fmt.Sprintf("the administrator-supplied credential for role %s could not be verified: installed-credential authentication is unavailable", rec.Name))
				changed = true
			}
		case installedCredentialUnhealthy:
			status.GitLabRolesPartial = status.GitLabRolesPartial || status.GitLabRolesReady
			status.GitLabRolesReady = false
			status.GitLabRoleDiagnostics = append(status.GitLabRoleDiagnostics, fmt.Sprintf("Role %s: the administrator-supplied credential is not healthy (%s)", rec.Name, reason))
			if _, ok := seen[field]; !ok {
				seen[field] = struct{}{}
				status.Drifts = append(status.Drifts, Drift{Field: field, Expected: "valid", Actual: "unhealthy: " + reason})
				changed = true
			}
		case installedCredentialUnverifiable:
			if _, ok := seen[field]; !ok {
				seen[field] = struct{}{}
				serviceAccountStatusUnverified(status, field, fmt.Sprintf("the administrator-supplied credential for role %s could not be verified: %s", rec.Name, reason))
				changed = true
			}
		}
	}
	return changed
}
