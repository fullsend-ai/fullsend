package repos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

var roleRotateLocks sync.Map // owner/repo/role -> *sync.Mutex

// errRotationLockLost is returned by mergeRoleState when a concurrent
// process has reclaimed a role's rotation lock since this holder last
// verified it owns the lock.
var errRotationLockLost = errors.New("gitlab role rotation lock lost to a concurrent claim")

const (
	rotationPhaseIdle         = "idle"
	rotationPhaseDistributing = "distributing"
	rotationPhaseOverlapping  = "overlapping"
	rotationPhaseFailed       = "failed"

	defaultRotateLockTTL = 10 * time.Minute
)

// RoleRotateConfig is the input to RotateGitLabRoleCredentials.
type RoleRotateConfig struct {
	Owner    string
	Repo     string
	Client   forge.Client
	Tokens   ProjectAccessTokenClient
	Registry gitlabroles.Registry
	// ConvergeServiceAccounts bypasses the recent-distribution skip only when
	// install positively identified a legacy token requiring replacement.
	ConvergeServiceAccounts bool
	// Roles limits rotation to these names. Empty means every
	// own-credential registered role.
	Roles []gitlabroles.Role
	// Force rotates even when the current PAT is not yet due.
	Force bool
	// LeadTime is how far ahead of expiry a credential is due.
	// Zero uses gitlabroles.DefaultRotationLead.
	LeadTime time.Duration
	// GracePeriod is how long the previous PAT stays active after a
	// successful distribution so in-flight jobs can finish. Zero uses
	// gitlabroles.DefaultRotationGrace.
	GracePeriod time.Duration
	Now         time.Time
	DryRun      bool
	Holder      string
	LockTTL     time.Duration
	// ProvidedCredentials keeps each replacement PAT and its resolved identity
	// together. Credential values must never be logged.
	ProvidedCredentials map[gitlabroles.Role]ProvidedRoleCredential
	// ResolveSuppliedOwner attributes the installed administrator-supplied
	// credential of a role to its GitLab user. Replacing such a credential with a
	// fullsend-minted one makes the owner impossible to attribute afterwards, so
	// when rotation state records a supplied credential without its owner the
	// owner is resolved and durably recorded first. Without a resolver, or when
	// it fails, that role is not rotated.
	ResolveSuppliedOwner func(ctx context.Context, role gitlabroles.Role) (int, error)
}

// RoleRotateResult is the observable outcome of a rotation run.
// Token values are not included.
type RoleRotateResult struct {
	Report      gitlabroles.Report
	Rotated     []gitlabroles.Role
	Skipped     []gitlabroles.Role
	Reused      []gitlabroles.Role
	Failed      []RoleProvisionFailure
	Overlapping []gitlabroles.Role
	RolledBack  []gitlabroles.Role
	Cleaned     []gitlabroles.Role
	InProgress  []gitlabroles.Role
	DryRun      bool
	Diagnostics []string
}

type rotationStateFile struct {
	Roles map[string]rotationRoleState `json:"roles"`
}

// rotationStateEnvelope versions the persisted format without exposing wire
// metadata to lifecycle snapshots. Version zero is the legacy unversioned form.
type rotationStateEnvelope struct {
	Version int                          `json:"version"`
	Roles   map[string]rotationRoleState `json:"roles"`
}

const gitLabRoleRotationStateVersion = 1

type rotationRoleState struct {
	// ManagedUserID records a service account positively created by Fullsend.
	// It survives token revocation so uninstall retries can verify ownership.
	ManagedUserID int    `json:"managed_user_id,omitempty"`
	Phase         string `json:"phase,omitempty"`
	Holder        string `json:"holder,omitempty"`
	LockUntil     string `json:"lock_until,omitempty"`
	IncomingID    int    `json:"incoming_id,omitempty"`
	OutgoingIDs   []int  `json:"outgoing_ids,omitempty"`
	DistributedAt string `json:"distributed_at,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	Error         string `json:"error,omitempty"`
	// SuppliedUserID is the GitLab user that owns an administrator-supplied
	// credential, resolved when the credential was enrolled. It keeps the
	// owner's account out of fullsend's management after the credential
	// expires or is revoked, when it can no longer authenticate to say who
	// it belongs to.
	SuppliedUserID int `json:"supplied_user_id,omitempty"`
	// SuppliedTokenID is the GitLab token ID of the enrolled administrator-
	// supplied credential, when it was resolved at enrollment. Lifecycle
	// analysis then follows only that token on the owner's account. Zero means
	// unresolved, and every token of the owner's account stands in for it.
	SuppliedTokenID int `json:"supplied_token_id,omitempty"`
	// Supplied records that the installed credential was enrolled by an
	// administrator, whether or not its owner could be resolved. It is
	// independent of the phase so a failed replacement attempt keeps it.
	Supplied bool `json:"supplied,omitempty"`
	// SuppliedDistributed records that the enrolled supplied credential was
	// successfully published. It is tracked independently of the managed-token
	// rotation fields (incoming_id, phase, outgoing_ids), which a supplied
	// enrollment leaves in place so cleanup of managed tokens continues, so a
	// healthy supplied credential is retained over such an entry. It is cleared
	// when a replacement enrollment starts or fails to publish, so that attempt
	// is retried.
	SuppliedDistributed bool `json:"supplied_distributed,omitempty"`
	// ExcludedUserIDs are the GitLab users that own administrator-supplied
	// credentials this role has ever had. They are append-only: a managed
	// replacement or another supplied owner never erases an earlier exclusion,
	// so those accounts stay out of provisioning, reconciliation, inventory,
	// and automatic revocation for as long as the state exists.
	ExcludedUserIDs []int `json:"excluded_user_ids,omitempty"`
}

// clone returns a copy that shares no slice storage with rs.
func (rs rotationRoleState) clone() rotationRoleState {
	rs.OutgoingIDs = append([]int(nil), rs.OutgoingIDs...)
	rs.ExcludedUserIDs = append([]int(nil), rs.ExcludedUserIDs...)
	return rs
}

// exclusionsOnly reports whether the entry records nothing but supplied-account
// exclusions: no provenance, lifecycle phase, or token tracking.
func (rs rotationRoleState) exclusionsOnly() bool {
	return rs.Phase == "" && rs.Holder == "" && rs.LockUntil == "" && rs.IncomingID == 0 &&
		len(rs.OutgoingIDs) == 0 && rs.DistributedAt == "" && rs.ExpiresAt == "" && rs.Error == "" &&
		rs.SuppliedUserID == 0 && rs.SuppliedTokenID == 0 && !rs.Supplied && !rs.SuppliedDistributed && rs.ManagedUserID == 0
}

// excludeOwner records an administrator-owned account as permanently excluded
// from fullsend's management. A zero ID is ignored.
func (rs *rotationRoleState) excludeOwner(id int) {
	if id > 0 && !containsInt(rs.ExcludedUserIDs, id) {
		rs.ExcludedUserIDs = append(rs.ExcludedUserIDs, id)
		sort.Ints(rs.ExcludedUserIDs)
	}
}

// markSupplied records an administrator-supplied credential owned by userID
// (zero when unresolved), keeping every earlier exclusion.
func (rs *rotationRoleState) markSupplied(userID int) {
	rs.excludeOwner(rs.SuppliedUserID)
	rs.excludeOwner(userID)
	rs.Supplied = true
	rs.SuppliedDistributed = false
	rs.SuppliedUserID = userID
	// A new enrollment names a different token than any earlier one; callers
	// that resolved it record it with setSuppliedToken afterwards.
	rs.SuppliedTokenID = 0
}

// setSuppliedToken records the GitLab token ID of the enrolled supplied
// credential owned by userID. It applies only to the owner just recorded by
// markSupplied, and an unresolved ID leaves the entry unchanged.
func (rs *rotationRoleState) setSuppliedToken(userID, tokenID int) {
	if tokenID > 0 && userID > 0 && rs.Supplied && rs.SuppliedUserID == userID {
		rs.SuppliedTokenID = tokenID
	}
}

// markManaged records that a fullsend-minted credential replaced a supplied one.
// The previous supplied owner's exclusion is retained.
func (rs *rotationRoleState) markManaged() {
	rs.excludeOwner(rs.SuppliedUserID)
	rs.Supplied = false
	rs.SuppliedDistributed = false
	rs.SuppliedUserID = 0
	rs.SuppliedTokenID = 0
}

// RotateGitLabRoleCredentials replaces due (or Force) own-credential
// GitLab role PATs without touching FULLSEND_FORGE_TOKEN.
//
// Create-then-distribute is used instead of GitLab's rotate-in-place
// API so the previous credential stays valid until grace cleanup:
// in-flight jobs that already hold the old token keep working. A
// failed create or distribution rolls the attempt back (revokes only
// the unused replacement) and leaves the last known-good secret in
// place. Concurrent callers for the same role are serialized and
// idempotent within gitlabroles.IdempotentRotationWindow.
func RotateGitLabRoleCredentials(ctx context.Context, cfg RoleRotateConfig) (_ RoleRotateResult, err error) {
	result := RoleRotateResult{DryRun: cfg.DryRun}
	if cfg.Client == nil {
		return result, fmt.Errorf("GitLab role rotation requires a forge client")
	}
	release, lockErr := LockGitLabProject(ctx, cfg.Client, cfg.Owner, cfg.Repo, cfg.DryRun)
	if lockErr != nil {
		return result, lockErr
	}
	defer release(&err)
	reg := cfg.Registry
	if len(reg.Registrations()) == 0 {
		reg = gitlabroles.BuiltinRegistry()
	}
	cfg.Registry = reg
	now := cfg.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	lead := cfg.LeadTime
	if lead <= 0 {
		lead = gitlabroles.DefaultRotationLead
	}
	grace := cfg.GracePeriod
	switch {
	case cfg.GracePeriod < 0:
		grace = 0
	case cfg.GracePeriod == 0:
		grace = gitlabroles.DefaultRotationGrace
	}
	holder := strings.TrimSpace(cfg.Holder)
	if holder == "" {
		holder = fmt.Sprintf("rotate-%d", now.UnixNano())
	}
	lockTTL := cfg.LockTTL
	if lockTTL <= 0 {
		lockTTL = defaultRotateLockTTL
	}

	if cfg.Tokens == nil && len(cfg.ProvidedCredentials) == 0 {
		if cfg.Force {
			return result, fmt.Errorf("GitLab role rotation requires a token client or administrator-provided credentials")
		}
		result.Diagnostics = append(result.Diagnostics, "no GitLab token client; skip rotation")
		return result, nil
	}

	var listed []ProjectAccessToken
	if cfg.Tokens != nil {
		toks, err := cfg.Tokens.ListProjectAccessTokens(ctx, cfg.Owner, cfg.Repo)
		if err != nil {
			return result, fmt.Errorf("listing GitLab project access tokens: %w", err)
		}
		listed = toks
	}

	if _, presErr := gitLabRolePresence(ctx, cfg.Client, cfg.Owner, cfg.Repo, reg); presErr != nil {
		return result, fmt.Errorf("reading GitLab role credential presence: %w", presErr)
	}
	want := wantedRoles(cfg.Roles, reg)

	for _, rec := range reg.Registrations() {
		if rec.Credential.Kind == gitlabroles.CredentialReuse {
			if roleWanted(want, rec.Name) {
				result.Reused = append(result.Reused, rec.Name)
			}
			continue
		}
		if !roleWanted(want, rec.Name) {
			continue
		}
		rotateOneRole(ctx, cfg, rec, holder, lockTTL, now, lead, grace, &listed, &result)
	}

	if cfg.Tokens != nil && !cfg.DryRun {
		if toks, err := cfg.Tokens.ListProjectAccessTokens(ctx, cfg.Owner, cfg.Repo); err == nil {
			listed = toks
		}
	}
	present, presErr := gitLabRolePresence(ctx, cfg.Client, cfg.Owner, cfg.Repo, reg)
	if presErr != nil {
		return result, fmt.Errorf("reading GitLab role credential presence: %w", presErr)
	}
	result.Report = gitlabroles.DiagnoseLifecycle(present, reg, snapshotsFrom(listed), now, lead)
	result.Diagnostics = append(result.Diagnostics, result.Report.Diagnostics...)
	sortRoleLists(&result)
	if secretLeakRotate(result) != "" {
		return RoleRotateResult{}, fmt.Errorf("internal error: rotation result leaked a secret value")
	}
	return result, nil
}

func rotateOneRole(ctx context.Context, cfg RoleRotateConfig, rec gitlabroles.Registration, holder string, lockTTL time.Duration, now time.Time, lead, grace time.Duration, listed *[]ProjectAccessToken, result *RoleRotateResult) {
	unlock := lockGitLabRoleRotation(cfg.Owner, cfg.Repo, rec.Name)
	defer unlock()

	if cfg.Tokens != nil {
		if toks, err := cfg.Tokens.ListProjectAccessTokens(ctx, cfg.Owner, cfg.Repo); err == nil {
			*listed = toks
		}
	}

	secret := rec.Credential.SecretName
	tokenName := rec.Credential.TokenName
	if tokenName == "" {
		tokenName = gitlabroles.CustomTokenName(rec.Name)
	}

	state, stateDiags, stateErr := loadRotationState(ctx, cfg.Client, cfg.Owner, cfg.Repo)
	result.Diagnostics = append(result.Diagnostics, stateDiags...)
	if stateErr != nil {
		result.Failed = append(result.Failed, RoleProvisionFailure{
			Role: rec.Name, Secret: secret, Reason: "reading rotation state failed",
		})
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: rotation state is invalid or unavailable; refusing to rotate", rec.Name))
		return
	}
	rs := state.Roles[string(rec.Name)]
	if otherHoldsRotationLock(rs, holder, now) {
		result.InProgress = append(result.InProgress, rec.Name)
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: rotation in progress; skipping", rec.Name))
		return
	}

	if !cfg.DryRun {
		lockUntil := now.Add(lockTTL).Format(time.RFC3339)
		// Claim via claimRoleRotationLock (re-read immediately before
		// writing) instead of writeRotationState of the state snapshot
		// loaded above, so a sibling role's concurrent update landing
		// between that load and this write is not silently reverted.
		// claimRoleRotationLock copies only Holder/LockUntil onto the
		// freshly re-read role entry -- it never overwrites phase,
		// incoming_id, outgoing_ids, or distributed_at with this
		// holder's own (possibly stale) pre-claim snapshot in rs. That
		// matters even when the lock is free: a process that loaded an
		// unlocked document, stalled through a concurrent winner's full
		// mint/distribute/lock-release, and only now claims must not
		// revert that winner's completed state to a fresh idle entry,
		// which would make distributionProven false below and cause a
		// redundant mint. A different, still-valid holder currently
		// owning the lock still rejects the claim outright.
		if err := claimRoleRotationLock(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec.Name, holder, lockUntil, now, &state); err != nil {
			if errors.Is(err, errRotationLockLost) {
				result.InProgress = append(result.InProgress, rec.Name)
				result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: concurrent rotation won the lock; skipping", rec.Name))
				return
			}
			result.Failed = append(result.Failed, RoleProvisionFailure{
				Role: rec.Name, Secret: secret, Reason: "writing rotation lock failed",
			})
			return
		}
		// The forge variable API has no compare-and-swap operation. Re-read
		// immediately after claiming so a concurrent process that overwrote
		// this claim is detected before minting a token. The in-process lock
		// remains a fast path, but cannot provide cross-process exclusion.
		claimed, _, err := loadRotationState(ctx, cfg.Client, cfg.Owner, cfg.Repo)
		if err != nil {
			result.Failed = append(result.Failed, RoleProvisionFailure{
				Role: rec.Name, Secret: secret, Reason: "verifying rotation lock failed",
			})
			return
		}
		if claimed.Roles[string(rec.Name)].Holder != holder {
			result.InProgress = append(result.InProgress, rec.Name)
			result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: concurrent rotation won the lock; skipping", rec.Name))
			return
		}
		// Build every subsequent write on this freshly re-read document,
		// not the snapshot loaded before the lock claim, so a sibling
		// role's concurrent update landing in between is not silently
		// reverted the next time this role's state is persisted.
		state = claimed
		rs = state.Roles[string(rec.Name)]
	}
	defer func() {
		if cfg.DryRun {
			return
		}
		st, _, err := loadRotationState(ctx, cfg.Client, cfg.Owner, cfg.Repo)
		if err != nil {
			return
		}
		cur := st.Roles[string(rec.Name)]
		if cur.Holder == holder {
			cur.Holder = ""
			cur.LockUntil = ""
			st.Roles[string(rec.Name)] = cur
			_ = writeRotationState(ctx, cfg.Client, cfg.Owner, cfg.Repo, st)
		}
	}()

	matches := tokensNamed(*listed, tokenName)
	installed := installedCredentialTokens(*listed, matches, tokenName, rs)
	current := currentListed(installed)
	if !cfg.DryRun {
		cleaned := cleanupOutgoing(ctx, cfg, &rs, now, grace, listed)
		if cleaned {
			result.Cleaned = append(result.Cleaned, rec.Name)
			_ = mergeRoleState(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec.Name, holder, now, rs, &state)
			matches = tokensNamed(*listed, tokenName)
			installed = installedCredentialTokens(*listed, matches, tokenName, rs)
			current = currentListed(installed)
		}
	}

	// The lifecycle follows the installed credential: for an enrolled supplied
	// credential with a recorded token ID, that token alone, so an unrelated
	// healthy token carrying the role's name cannot mask its revocation or
	// expiry. The other same-named tokens stay in matches for cleanup.
	rr := roleReportFrom(ctx, cfg, rec, installed, now, lead)
	freshExpiry := GitLabPATExpiresAt(now)
	// The live inventory alone can never distinguish a legitimately
	// distributed credential from an unrecorded orphan: one left behind
	// by a crash before any rotation-state write landed (including the
	// single-active-PAT case — that lone token may be an orphan minted
	// moments before a crash wiped out the write that would have
	// recorded it), one still mid-distribution, or one left over from an
	// overlapping/failed attempt. Only trust "already rotated" or "not
	// due" when the rotation state itself proves this process (or
	// initial provisioning, which also records this proof) is the one
	// that distributed the current live token: IncomingID matches it and
	// DistributedAt is set. Otherwise proceed so the recovery path below
	// can run.
	// Administrator-provided enrollment (rotateProvided, and the mirrored
	// proof recorded by provisionOwnRoles) has no GitLab token ID to put
	// in IncomingID, since only the secret value is supplied -- it
	// proves distribution by writing phase=idle with DistributedAt set
	// and IncomingID left at zero. Do not extend this to phase=failed: a
	// failed administrator-provided enrollment must still be retried.
	providedDistributionProven := rs.IncomingID == 0 && rs.Phase == rotationPhaseIdle && rs.DistributedAt != ""
	// A recorded successful supplied publication proves distribution whatever
	// managed-token state a surviving entry still carries: that state only
	// tracks managed tokens awaiting cleanup, not the installed credential.
	suppliedDistributionProven := rs.Supplied && rs.SuppliedDistributed && rs.DistributedAt != ""
	distributionProven := (rs.IncomingID != 0 && rs.IncomingID == current.ID && rs.DistributedAt != "") ||
		providedDistributionProven || suppliedDistributionProven
	needsRecovery := !suppliedDistributionProven && rs.IncomingID != 0 &&
		(rs.Phase == rotationPhaseDistributing || rs.Phase == rotationPhaseFailed)
	alreadyFresh := !cfg.Force && current.ID != 0 && current.Active && current.ExpiresAt == freshExpiry &&
		distributionProven && !needsRecovery
	if alreadyFresh || (recentlyDistributed(rs, now) && !gitlabroles.RoleDueForRotation(rr) && !needsRecovery && !cfg.ConvergeServiceAccounts) {
		result.Skipped = append(result.Skipped, rec.Name)
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: already rotated (idempotent)", rec.Name))
		if rs.Phase == rotationPhaseOverlapping || rr.Overlapping {
			result.Overlapping = append(result.Overlapping, rec.Name)
		}
		return
	}
	if !cfg.Force && !gitlabroles.RoleDueForRotation(rr) && distributionProven && !needsRecovery {
		result.Skipped = append(result.Skipped, rec.Name)
		if rr.Overlapping || rs.Phase == rotationPhaseOverlapping {
			result.Overlapping = append(result.Overlapping, rec.Name)
		}
		return
	}
	if cfg.DryRun && !cfg.Force && !gitlabroles.RoleDueForRotation(rr) && !needsRecovery {
		result.Skipped = append(result.Skipped, rec.Name)
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf(
			"%s: healthy credential would be retained; live install would backfill distribution proof", rec.Name))
		return
	}

	if provided := strings.TrimSpace(cfg.ProvidedCredentials[rec.Name].Token); provided != "" {
		rotateProvided(ctx, cfg, rec, holder, secret, provided, now, matches, &rs, &state, result)
		if !cfg.DryRun {
			if err := mergeRoleState(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec.Name, holder, now, rs, &state); err != nil {
				result.Failed = append(result.Failed, RoleProvisionFailure{
					Role: rec.Name, Secret: secret,
					Reason: "recording administrator-provided distribution state failed",
				})
			}
		}
		return
	}
	if cfg.Tokens == nil {
		result.Failed = append(result.Failed, RoleProvisionFailure{
			Role: rec.Name, Secret: secret,
			Reason: "no GitLab token client and no administrator-provided credential",
		})
		return
	}

	if cfg.DryRun {
		result.Rotated = append(result.Rotated, rec.Name)
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: would rotate (%s)", rec.Name, secret))
		return
	}

	var unconfirmedIncomingID int
	if rs.IncomingID != 0 && (rs.Phase == rotationPhaseDistributing || rs.Phase == rotationPhaseFailed) {
		// The state may be stale after a crash immediately after the CI
		// variable was updated. The incoming token may therefore be the
		// live credential; never revoke it without proof that distribution
		// did not complete. Keep it in the outgoing set and let the normal
		// grace cleanup retire it after a replacement is distributed.
		unconfirmedIncomingID = rs.IncomingID
		if !containsInt(rs.OutgoingIDs, rs.IncomingID) {
			rs.OutgoingIDs = append(rs.OutgoingIDs, rs.IncomingID)
		}
		rs.Phase = rotationPhaseFailed
		rs.Error = "previous distribution state was incomplete; preserving incoming token for safe recovery"
	}

	// The installed credential is administrator-supplied but its owner was never
	// recorded. Replacing it would leave nothing that can attribute the owner's
	// account, so resolve and durably record the owner before minting anything.
	if provenanceOf(rs).Supplied && rs.SuppliedUserID == 0 {
		ownerID := 0
		if cfg.ResolveSuppliedOwner != nil {
			ownerID, _ = cfg.ResolveSuppliedOwner(ctx, rec.Name)
		}
		if ownerID <= 0 {
			result.Failed = append(result.Failed, RoleProvisionFailure{
				Role: rec.Name, Secret: secret,
				Reason: "installed administrator-supplied credential has no recorded owner and cannot be attributed; not replaced (re-enroll it with --gitlab-role-token)",
			})
			return
		}
		pre := rs
		pre.markSupplied(ownerID)
		if err := mergeRoleState(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec.Name, holder, now, pre, &state); err != nil {
			result.Failed = append(result.Failed, RoleProvisionFailure{
				Role: rec.Name, Secret: secret,
				Reason: "recording the administrator-supplied credential owner failed; credential not replaced",
			})
			return
		}
		rs = pre
	}

	// Resolve the project-wide administrator-owned accounts before minting, so
	// an unresolvable exclusion set fails the role with nothing to undo.
	excludedOwners, err := projectExcludedOwners(ctx, cfg, state, rs)
	if err != nil {
		result.Failed = append(result.Failed, RoleProvisionFailure{
			Role: rec.Name, Secret: secret,
			Reason: "administrator-supplied credential owners could not be resolved; credential not replaced",
		})
		return
	}

	expiresAt := GitLabPATExpiresAt(now)
	tok, err := cfg.Tokens.CreateProjectAccessToken(ctx, cfg.Owner, cfg.Repo, tokenName,
		gitlabroles.TokenScopes(), gitlabroles.DeveloperAccessLevel, expiresAt)
	if err != nil {
		if cfg.ConvergeServiceAccounts && serviceAccountsUnavailable(err) {
			result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: service-account creation unavailable; keeping the existing legacy credential", rec.Name))
			return
		}
		rs.Phase = rotationPhaseFailed
		rs.Error = "project access token creation failed"
		_ = mergeRoleState(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec.Name, holder, now, rs, &state)
		result.Failed = append(result.Failed, RoleProvisionFailure{
			Role: rec.Name, Secret: secret, Reason: rs.Error,
		})
		return
	}
	if tok == nil || strings.TrimSpace(tok.Token) == "" {
		if tok != nil && tok.ID != 0 {
			_ = cfg.Tokens.RevokeProjectAccessToken(ctx, cfg.Owner, cfg.Repo, tok.ID)
		}
		rs.Phase = rotationPhaseFailed
		rs.Error = "project access token creation returned no value"
		_ = mergeRoleState(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec.Name, holder, now, rs, &state)
		result.Failed = append(result.Failed, RoleProvisionFailure{
			Role: rec.Name, Secret: secret, Reason: rs.Error,
		})
		return
	}
	if !canMaskGitLabValue(tok.Token) {
		_ = cfg.Tokens.RevokeProjectAccessToken(ctx, cfg.Owner, cfg.Repo, tok.ID)
		rs.Phase = rotationPhaseFailed
		rs.Error = "replacement credential cannot be masked"
		_ = mergeRoleState(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec.Name, holder, now, rs, &state)
		result.Failed = append(result.Failed, RoleProvisionFailure{
			Role: rec.Name, Secret: secret, Reason: rs.Error,
		})
		return
	}

	// Tokens owned by administrator-supplied accounts stay visible in the
	// lifecycle inventory but are never scheduled for revocation: the
	// revocation boundary refuses them, so an ID recorded here would never
	// clear and the role would stay in the overlapping phase.
	// Operational inventories may omit an unavailable legacy source. Durable
	// outgoing IDs remain obligations until cleanup confirms revocation or
	// absence; a later rotation must not erase them during the grace period.
	outgoing := uniqueInts(append(append([]int(nil), rs.OutgoingIDs...), activeIDsExcept(withoutExcludedOwners(matches, excludedOwners), tok.ID)...))
	if current.ID != 0 && current.ID != tok.ID && !ownedByExcluded(current, excludedOwners) {
		outgoing = uniqueInts(append(outgoing, current.ID))
	}
	// Recovery must retain the potentially published token even when an
	// operational inventory temporarily omits its source. Its recorded ID is
	// positive minting evidence; revocation still enforces owner exclusions.
	if unconfirmedIncomingID != 0 && unconfirmedIncomingID != tok.ID {
		outgoing = uniqueInts(append(outgoing, unconfirmedIncomingID))
	}
	rs.Phase = rotationPhaseDistributing
	// Invalidate the supplied distribution proof before publishing a managed
	// replacement, so an interrupted write cannot suppress recovery. Retain
	// the supplied identity and exclusions until publication succeeds.
	rs.SuppliedDistributed = false
	rs.IncomingID = tok.ID
	// Account ownership may have been recorded by creation during the mint.
	// Never infer account ownership merely from a PAT minted on an existing
	// same-named account: an administrator might have created that identity.
	createdState, _, createdErr := loadRotationState(ctx, cfg.Client, cfg.Owner, cfg.Repo)
	if createdErr != nil {
		cleanupCtx, cancel := gitlabCleanupContext(ctx)
		_ = cfg.Tokens.RevokeProjectAccessToken(cleanupCtx, cfg.Owner, cfg.Repo, tok.ID)
		cancel()
		result.Failed = append(result.Failed, RoleProvisionFailure{Role: rec.Name, Secret: secret, Reason: "reading created account ownership failed"})
		return
	}
	rs.ManagedUserID = createdState.Roles[string(rec.Name)].ManagedUserID
	rs.OutgoingIDs = outgoing
	rs.ExpiresAt = expiresAt
	rs.Error = ""
	if err := mergeRoleState(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec.Name, holder, now, rs, &state); err != nil {
		if revokeErr := cfg.Tokens.RevokeProjectAccessToken(ctx, cfg.Owner, cfg.Repo, tok.ID); revokeErr != nil {
			result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: replacement PAT id %d left for cleanup after state failure", rec.Name, tok.ID))
		}
		result.Failed = append(result.Failed, RoleProvisionFailure{
			Role: rec.Name, Secret: secret,
			Reason: "recording distribution state failed; replacement was not distributed",
		})
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: replacement was not distributed because rotation state could not be recorded", rec.Name))
		return
	}

	if err := cfg.Client.CreateRepoSecret(ctx, cfg.Owner, cfg.Repo, secret, tok.Token); err != nil {
		// A failed response can follow a committed variable update. The forge
		// secret interface cannot read values back, so publication cannot be
		// ruled out. Preserve the active incoming token and distributing state
		// for recovery rather than revoke a potentially installed credential.
		rs.Error = "storing replacement credential failed; publication outcome unconfirmed"
		_ = mergeRoleState(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec.Name, holder, now, rs, &state)
		result.Failed = append(result.Failed, RoleProvisionFailure{
			Role: rec.Name, Secret: secret,
			Reason: "storing replacement credential failed; incoming credential and recovery state retained because publication could not be ruled out",
		})
		return
	}

	*listed = append(*listed, ProjectAccessToken{
		ID: tok.ID, Name: tokenName, Active: true, ExpiresAt: expiresAt,
	})
	rs.Phase = rotationPhaseOverlapping
	if len(outgoing) == 0 {
		rs.Phase = rotationPhaseIdle
	}
	rs.DistributedAt = now.Format(time.RFC3339)
	rs.IncomingID = tok.ID
	rs.OutgoingIDs = outgoing
	rs.ExpiresAt = expiresAt
	// A fullsend-minted credential replaces any supplied one. The entry stops
	// describing the installed credential as supplied, but the supplied
	// owner's account stays excluded from management.
	rs.markManaged()
	rs.Error = ""
	if err := mergeRoleState(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec.Name, holder, now, rs, &state); err != nil {
		result.Failed = append(result.Failed, RoleProvisionFailure{
			Role: rec.Name, Secret: secret,
			Reason: "recording completed distribution state failed; replacement remains active",
		})
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: replacement was distributed but completed rotation state could not be recorded", rec.Name))
		return
	}

	result.Rotated = append(result.Rotated, rec.Name)
	if len(outgoing) > 0 {
		result.Overlapping = append(result.Overlapping, rec.Name)
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf(
			"%s: replacement distributed; previous credential remains usable for in-flight jobs until grace cleanup", rec.Name))
	} else {
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: replacement distributed (%s)", rec.Name, secret))
	}
}

func rotateProvided(ctx context.Context, cfg RoleRotateConfig, rec gitlabroles.Registration, holder, secret, provided string, now time.Time, matches []ProjectAccessToken, rs *rotationRoleState, state *rotationStateFile, result *RoleRotateResult) {
	if !canMaskGitLabValue(provided) {
		result.Failed = append(result.Failed, RoleProvisionFailure{
			Role: rec.Name, Secret: secret,
			Reason: "administrator-provided credential cannot be masked (must be a single line of at least 8 characters using GitLab's allowed charset)",
		})
		return
	}
	if cfg.DryRun {
		result.Rotated = append(result.Rotated, rec.Name)
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: would enroll replacement (%s)", rec.Name, secret))
		return
	}
	// Persist supplied provenance and the owner's exclusion before the supplied
	// credential is published, so an interruption or write failure can never
	// leave the new secret classified by the previous (possibly managed) entry.
	// Fail closed: without durable provenance the credential is not stored.
	pre := *rs
	// An installed supplied credential whose owner was never recorded is about to
	// be overwritten, so its owner is attributed and excluded first. When it
	// cannot be attributed the provenance stays unresolved and nothing is
	// replaced: the administrator must attribute it explicitly.
	if provenanceOf(*rs).Supplied && rs.SuppliedUserID == 0 {
		outgoingOwner := 0
		if cfg.ResolveSuppliedOwner != nil {
			outgoingOwner, _ = cfg.ResolveSuppliedOwner(ctx, rec.Name)
		}
		if outgoingOwner <= 0 {
			result.Failed = append(result.Failed, RoleProvisionFailure{
				Role: rec.Name, Secret: secret,
				Reason: "installed administrator-supplied credential has no recorded owner and cannot be attributed; not replaced (its owner must be attributed before it can be replaced)",
			})
			return
		}
		pre.markSupplied(outgoingOwner)
	}
	// The replacement owner's exclusion is persisted before publication. When
	// the owner could not be attributed, the entry is persisted as an explicit
	// unresolved supplied enrollment instead: every destructive and membership
	// operation fails closed on it until the installed credential's owner is
	// attributed, so a commit-then-error store (or a failed completed-state
	// write) can never leave the replacement under the previous provenance.
	// The installed token ID stays untouched whenever the owner is resolved.
	replacementOwner := cfg.ProvidedCredentials[rec.Name].OwnerID
	if replacementOwner > 0 {
		pre.excludeOwner(replacementOwner)
	} else {
		pre.markSupplied(0)
	}
	// The previous credential's distribution proof must not vouch for whatever
	// ends up installed: a publication that commits but is never recorded as
	// complete is retried by the next run rather than trusted. The failed phase
	// is cleared only when the completed state is written below.
	pre.SuppliedDistributed = false
	pre.Phase = rotationPhaseFailed
	pre.Error = "administrator-provided replacement publication in progress or unconfirmed"
	if err := mergeRoleState(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec.Name, holder, now, pre, state); err != nil {
		result.Failed = append(result.Failed, RoleProvisionFailure{
			Role: rec.Name, Secret: secret,
			Reason: "recording administrator-provided credential provenance failed; credential not stored",
		})
		return
	}
	*rs = pre
	if err := cfg.Client.CreateRepoSecret(ctx, cfg.Owner, cfg.Repo, secret, provided); err != nil {
		rs.Phase = rotationPhaseFailed
		rs.Error = "storing administrator-provided replacement failed; publication outcome unconfirmed"
		// The failed attempt must be retried, not treated as proven.
		rs.SuppliedDistributed = false
		result.Failed = append(result.Failed, RoleProvisionFailure{
			Role: rec.Name, Secret: secret,
			Reason: "storing administrator-provided replacement failed; previous credential left in place",
		})
		return
	}
	// The administrator-provided replacement's own GitLab token ID is
	// not known here (only its secret value was supplied). If that
	// replacement is itself a project access token GitLab already lists
	// under this role's token name (the documented free-tier workflow,
	// since creating project access tokens via the API requires GitLab
	// Premium/Ultimate), it cannot be distinguished from a genuinely
	// leftover same-named PAT among matches. Recording every active
	// same-named token in outgoing_ids would let grace cleanup revoke
	// the just-enrolled replacement itself. Until the replacement's own
	// ID can be resolved, do not schedule any same-named active PAT for
	// grace revocation.
	rs.Phase = rotationPhaseIdle
	rs.IncomingID = 0
	rs.OutgoingIDs = nil
	rs.DistributedAt = now.Format(time.RFC3339)
	rs.ExpiresAt = ""
	rs.Error = ""
	rs.markSupplied(cfg.ProvidedCredentials[rec.Name].OwnerID)
	rs.setSuppliedToken(cfg.ProvidedCredentials[rec.Name].OwnerID, cfg.ProvidedCredentials[rec.Name].TokenID)
	rs.SuppliedDistributed = true
	result.Rotated = append(result.Rotated, rec.Name)
	// Do not imply that grace cleanup will retire any other active
	// same-named PAT: OutgoingIDs is nil above (its own ID cannot be
	// resolved to exclude it), so cleanupOutgoing has nothing to act on
	// and will never revoke a leftover automatically. Surface that as an
	// explicit manual action instead.
	if leftover := activeIDsExcept(matches, 0); len(leftover) > 0 {
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf(
			"%s: enrolled administrator-provided replacement (%s); %d other active project access token(s) sharing this role's token name were not scheduled for automatic revocation because the replacement's own GitLab token ID is unknown -- confirm they are not the just-enrolled replacement and revoke them manually if so",
			rec.Name, secret, len(leftover)))
	} else {
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf(
			"%s: enrolled administrator-provided replacement (%s)", rec.Name, secret))
	}
}

// GitLabOutgoingTokenVerifier is the optional authoritative inventory capability
// used only to retire outgoing rotation obligations that are already inactive.
type GitLabOutgoingTokenVerifier interface {
	ConfirmOutgoingTokenInactive(ctx context.Context, owner, repo string, tokenID int) (bool, error)
}

func cleanupOutgoing(ctx context.Context, cfg RoleRotateConfig, rs *rotationRoleState, now time.Time, grace time.Duration, listed *[]ProjectAccessToken) bool {
	if cfg.Tokens == nil || len(rs.OutgoingIDs) == 0 {
		return false
	}
	// Only revoke previous PATs after a successful distribution and
	// the in-flight grace period. An incomplete (distributing) attempt
	// must not drop the last known-good token.
	if rs.Phase != rotationPhaseOverlapping || rs.DistributedAt == "" {
		return false
	}
	dist, err := time.Parse(time.RFC3339, rs.DistributedAt)
	if err != nil || now.Before(dist.Add(grace)) {
		return false
	}
	cleaned := false
	remaining := make([]int, 0, len(rs.OutgoingIDs))
	for _, id := range rs.OutgoingIDs {
		if id == rs.IncomingID {
			continue
		}
		// A complete inventory can discharge a missing or inactive outgoing
		// credential, while generic revocation retains its strict error contract.
		inactive := false
		if verifier, ok := cfg.Tokens.(GitLabOutgoingTokenVerifier); ok {
			var err error
			inactive, err = verifier.ConfirmOutgoingTokenInactive(ctx, cfg.Owner, cfg.Repo, id)
			if err != nil {
				// An inventory that cannot prove absence is not a reason to
				// skip revocation: an ownership-checked revoke through the
				// available inventory still retires a token that inventory
				// positively identifies. If it cannot, the obligation stays.
				inactive = false
			}
		}
		if !inactive {
			if err := cfg.Tokens.RevokeProjectAccessToken(ctx, cfg.Owner, cfg.Repo, id); err != nil {
				remaining = append(remaining, id)
				continue
			}
		}
		deactivateListedToken(listed, id)
		cleaned = true
	}
	rs.OutgoingIDs = remaining
	if len(remaining) == 0 && rs.Phase == rotationPhaseOverlapping {
		rs.Phase = rotationPhaseIdle
	}
	return cleaned
}

func recentlyDistributed(rs rotationRoleState, now time.Time) bool {
	if rs.IncomingID == 0 || rs.DistributedAt == "" {
		return false
	}
	dist, err := time.Parse(time.RFC3339, rs.DistributedAt)
	if err != nil {
		return false
	}
	return now.Sub(dist) >= 0 && now.Sub(dist) <= gitlabroles.IdempotentRotationWindow
}

func otherHoldsRotationLock(rs rotationRoleState, holder string, now time.Time) bool {
	if rs.Holder == "" || rs.Holder == holder || rs.LockUntil == "" {
		return false
	}
	until, err := time.Parse(time.RFC3339, rs.LockUntil)
	if err != nil {
		return false
	}
	return now.Before(until)
}

func lockGitLabRoleRotation(owner, repo string, role gitlabroles.Role) func() {
	key := owner + "/" + repo + "/" + string(role)
	v, _ := roleRotateLocks.LoadOrStore(key, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// RoleProvenance is what rotation state records about a role's installed
// credential (the Poller's, or any other role's).
type RoleProvenance struct {
	// Known is true when rotation state has an entry for the role. Without one,
	// who minted the installed credential is unknown, not "managed".
	Known bool
	// Supplied is true when the credential was enrolled by an administrator
	// (--gitlab-role-token): no GitLab token ID, idle, distributed, or an
	// explicitly recorded owner.
	Supplied bool
	// UserID is the recorded owner of a supplied credential, or zero when it
	// was not resolved at enrollment.
	UserID int
	// TokenID is the recorded GitLab token ID of a supplied credential, or zero
	// when it was not resolved at enrollment.
	TokenID int
	// ExcludedUserIDs are every administrator-owned account recorded for the
	// role, including owners of earlier supplied credentials that a managed
	// or another supplied credential has since replaced. They are never managed.
	ExcludedUserIDs []int
}

// PollerProvenance is the Poller's RoleProvenance, kept for existing callers.
type PollerProvenance = RoleProvenance

// PollerCredentialProvenance reads the Poller's credential provenance. A name
// match alone never proves fullsend minted a credential, so callers use this
// provenance to avoid revoking or re-membering a supplied credential. An
// unreadable or invalid state is an error so callers fail closed; a missing
// document or Poller entry is reported as unknown provenance, which callers
// must not treat as managed.
func PollerCredentialProvenance(ctx context.Context, client forge.Client, owner, repo string) (PollerProvenance, error) {
	provs, err := RoleCredentialProvenances(ctx, client, owner, repo)
	if err != nil {
		return PollerProvenance{}, err
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
		out[gitlabroles.Role(role)] = provenanceOf(rs)
	}
	return out, nil
}

func provenanceOf(rs rotationRoleState) RoleProvenance {
	excluded := append([]int(nil), rs.ExcludedUserIDs...)
	if rs.SuppliedUserID != 0 && !containsInt(excluded, rs.SuppliedUserID) {
		excluded = append(excluded, rs.SuppliedUserID)
	}
	prov := RoleProvenance{UserID: rs.SuppliedUserID, TokenID: rs.SuppliedTokenID, ExcludedUserIDs: excluded}
	switch {
	case rs.Supplied || rs.SuppliedUserID != 0:
		prov.Known, prov.Supplied = true, true
	case rs.IncomingID == 0 && len(rs.OutgoingIDs) == 0 && rs.Phase == rotationPhaseIdle && rs.DistributedAt != "":
		// Enrollment shape written before supplied credentials were flagged.
		prov.Known, prov.Supplied = true, true
	case rs.validManagedTokenEvidence():
		// Positive evidence that fullsend minted a token for this role.
		prov.Known = true
	}
	// Anything else (an empty entry, an unrecognized phase, or an entry with no
	// minted-token or supplied-owner evidence) stays unknown, never managed.
	return prov
}

// validManagedTokenEvidence refuses malformed lifecycle state as authorization
// evidence. A missing phase is tolerated for older token-tracking entries, but
// an unknown phase or nonpositive token ID never proves managed ownership.
func (rs rotationRoleState) validManagedTokenEvidence() bool {
	switch rs.Phase {
	case "", rotationPhaseIdle, rotationPhaseDistributing, rotationPhaseOverlapping, rotationPhaseFailed:
	default:
		return false
	}
	if rs.IncomingID < 0 {
		return false
	}
	for _, id := range rs.OutgoingIDs {
		if id <= 0 {
			return false
		}
	}
	return rs.IncomingID > 0 || len(rs.OutgoingIDs) > 0
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
// deleted. Roles that are not supplied or already have an owner are left alone.
// It is idempotent and writes only when something changes. Callers hold the
// project lease.
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
		cur.markSupplied(id)
		state.Roles[string(role)] = cur
		changed = true
	}
	if !changed {
		return nil
	}
	return writeRotationState(ctx, client, owner, repo, state)
}

func loadRotationState(ctx context.Context, client forge.Client, owner, repo string) (rotationStateFile, []string, error) {
	out := rotationStateFile{Roles: map[string]rotationRoleState{}}
	raw, exists, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabRoleRotation)
	if err != nil {
		return out, nil, err
	}
	if !exists || strings.TrimSpace(raw) == "" {
		return out, nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	var envelope rotationStateEnvelope
	if err := dec.Decode(&envelope); err != nil {
		return out, nil, fmt.Errorf("decode GitLab role rotation state: %w", err)
	}
	if envelope.Version != 0 && envelope.Version != gitLabRoleRotationStateVersion {
		return out, nil, fmt.Errorf("unsupported GitLab role rotation state version %d; use a compatible CLI", envelope.Version)
	}
	file := rotationStateFile{Roles: envelope.Roles}
	if file.Roles == nil {
		file.Roles = map[string]rotationRoleState{}
	}
	for role, rs := range file.Roles {
		if rs.SuppliedUserID < 0 || rs.SuppliedTokenID < 0 || rs.ManagedUserID < 0 {
			return out, nil, fmt.Errorf("invalid GitLab role rotation state %q: identity and supplied token IDs must not be negative", role)
		}
		for _, id := range rs.ExcludedUserIDs {
			if id <= 0 {
				return out, nil, fmt.Errorf("invalid GitLab role rotation state %q: excluded account IDs must be positive", role)
			}
		}
		for field, rawTime := range map[string]string{"lock_until": rs.LockUntil, "distributed_at": rs.DistributedAt, "expires_at": rs.ExpiresAt} {
			if rawTime == "" {
				continue
			}
			if _, err := time.Parse(time.RFC3339, rawTime); err != nil {
				if field == "expires_at" {
					if _, dayErr := time.Parse("2006-01-02", rawTime); dayErr == nil {
						continue
					}
				}
				return out, nil, fmt.Errorf("invalid GitLab role rotation state %s.%s: %w", role, field, err)
			}
		}
	}
	return file, nil, nil
}

func writeRotationState(ctx context.Context, client forge.Client, owner, repo string, file rotationStateFile) error {
	if file.Roles == nil {
		file.Roles = map[string]rotationRoleState{}
	}
	raw, err := json.Marshal(rotationStateEnvelope{Version: gitLabRoleRotationStateVersion, Roles: file.Roles})
	if err != nil {
		return err
	}
	return client.UpdateCIVariable(ctx, owner, repo, forge.VarGitLabRoleRotation, string(raw), true)
}

// claimRoleRotationLock re-reads the rotation-state document immediately
// before writing, then persists this holder's lock claim onto that
// freshly read role entry by overwriting only Holder and LockUntil. It
// never replaces phase, incoming_id, outgoing_ids, or distributed_at
// with this holder's own (possibly stale) pre-claim snapshot: a process
// that loaded an unlocked document, stalled through a concurrent
// winner's complete mint/distribute/lock-release, and only now claims
// must not revert that winner's completed state to a fresh idle entry --
// doing so would make distributionProven false for the freshly claimed
// role and cause a redundant mint.
//
// The claim is rejected with errRotationLockLost, and nothing is
// written, if the freshly read document shows a different, still-valid
// holder currently owns the lock.
//
// Like mergeRoleState, this fails closed: if the rotation-state document
// cannot be re-read, the claim is not attempted and the read error is
// returned rather than writing over an uninitialized document.
func claimRoleRotationLock(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, holder, lockUntil string, now time.Time, state *rotationStateFile) error {
	fresh, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return fmt.Errorf("re-reading rotation state before lock claim: %w", err)
	}
	*state = fresh
	if state.Roles == nil {
		state.Roles = map[string]rotationRoleState{}
	}
	cur := state.Roles[string(role)]
	if otherHoldsRotationLock(cur, holder, now) {
		return errRotationLockLost
	}
	cur.Holder = holder
	cur.LockUntil = lockUntil
	state.Roles[string(role)] = cur
	return writeRotationState(ctx, client, owner, repo, *state)
}

// mergeRoleState re-reads the rotation-state document immediately before
// writing rs for role, so this write does not silently discard a sibling
// role's concurrent update that landed after *state was last loaded in
// this call. *state is updated in place (to the freshly read document,
// with role's entry set to rs) so later reads in the same call see the
// merged result. If the re-read fails, nothing is written and the error
// is returned: an uninitialized or stale *state is never treated as a
// safe fallback, since writeRotationState persists the whole multi-role
// document and a fail-open write on a transient read failure would
// clobber every other role's lock, incoming_id, and outgoing_ids.
//
// When holder is non-empty, the write aborts with errRotationLockLost if
// the freshly read document shows role's lock now held by a different,
// still-valid holder: a concurrent process has since reclaimed the lock
// (for example, this holder's lockTTL lapsed mid-rotation), and this
// write must not clobber that process's state. Pass an empty holder to
// skip this check; this is only correct for recordInitialDistribution,
// which writes proof for a role that is not under this rotation lock at
// all (initial provisioning happens outside RotateGitLabRoleCredentials).
// The rotation lock claim itself uses claimRoleRotationLock, not
// mergeRoleState, and always passes a non-empty holder.
func mergeRoleState(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, holder string, now time.Time, rs rotationRoleState, state *rotationStateFile) error {
	fresh, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return fmt.Errorf("re-reading rotation state before write: %w", err)
	}
	*state = fresh
	if state.Roles == nil {
		state.Roles = map[string]rotationRoleState{}
	}
	if holder != "" && otherHoldsRotationLock(state.Roles[string(role)], holder, now) {
		return errRotationLockLost
	}
	// Creation provenance is persisted independently while minting. A failed
	// mint must not overwrite it with the pre-creation lifecycle snapshot.
	// Only creation recording or state retirement changes account ownership.
	rs.ManagedUserID = state.Roles[string(role)].ManagedUserID
	state.Roles[string(role)] = rs
	return writeRotationState(ctx, client, owner, repo, *state)
}

// recordInitialDistribution writes rotation-state proof for a role's PAT
// created by initial provisioning (provisionOwnRoles), outside of
// RotateGitLabRoleCredentials. Without this, a later rotation run cannot
// tell a healthy just-provisioned credential apart from an unrecorded
// orphan token and would immediately treat it as due for replacement.
// The failure is non-fatal to provisioning; the caller only logs a
// diagnostic, since the secret itself was already stored successfully.
//
// replaceExisting is for provisioning a secret that is genuinely missing under
// the project lease: the managed credential just minted replaces whatever
// provenance a surviving entry still carries (for example a supplied credential
// whose secret was removed), keeping historical exclusions and cleanup IDs. The
// backfill path passes false and never overwrites an existing entry.
func recordInitialDistribution(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, tokenID int, expiresAt string, now time.Time, replaceExisting bool) error {
	_, _, err := recordInitialDistributionWithPrior(ctx, client, owner, repo, role, tokenID, expiresAt, now, replaceExisting)
	return err
}

// recordInitialDistributionWithPrior is recordInitialDistribution that also
// returns the role entry it found before writing (hadPrior is false when there
// was none), so a caller whose publication then fails can hand it to
// discardInitialDistribution and restore the earlier token tracking.
func recordInitialDistributionWithPrior(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, tokenID int, expiresAt string, now time.Time, replaceExisting bool) (prior rotationRoleState, hadPrior bool, err error) {
	// Re-read immediately before writing and make this proof create-if-absent.
	// Initial provisioning must never overwrite a rotation that started after
	// the caller's earlier inventory read.
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return rotationRoleState{}, false, fmt.Errorf("reading rotation state before initial distribution proof: %w", err)
	}
	existing, exists := state.Roles[string(role)]
	prior, hadPrior = existing.clone(), exists
	if exists && !existing.exclusionsOnly() {
		if !replaceExisting {
			return prior, hadPrior, nil
		}
		rs := existing.clone()
		// The previous supplied owner stays excluded, and a previous managed
		// token that was never retired is queued for cleanup.
		rs.markManaged()
		if existing.IncomingID != 0 && existing.IncomingID != tokenID && !containsInt(rs.OutgoingIDs, existing.IncomingID) {
			rs.OutgoingIDs = append(rs.OutgoingIDs, existing.IncomingID)
		}
		rs.Phase = rotationPhaseIdle
		if len(rs.OutgoingIDs) > 0 {
			rs.Phase = rotationPhaseOverlapping
		}
		rs.IncomingID = tokenID
		rs.DistributedAt = now.UTC().Format(time.RFC3339)
		rs.ExpiresAt = expiresAt
		rs.Error = ""
		state.Roles[string(role)] = rs
		return prior, hadPrior, writeRotationState(ctx, client, owner, repo, state)
	}
	// An exclusions-only entry (left by uninstall to keep supplied accounts
	// protected) carries no distribution state, so the proof is created over it
	// with its exclusions intact.
	rs := rotationRoleState{
		Phase:           rotationPhaseIdle,
		IncomingID:      tokenID,
		DistributedAt:   now.UTC().Format(time.RFC3339),
		ExpiresAt:       expiresAt,
		ExcludedUserIDs: existing.ExcludedUserIDs,
	}
	state.Roles[string(role)] = rs
	return prior, hadPrior, writeRotationState(ctx, client, owner, repo, state)
}

// discardInitialDistribution undoes recordInitialDistributionWithPrior for
// tokenID when the credential it describes was never installed. An entry that no
// longer names tokenID is left alone. When there was an entry before the record
// (hadPrior), that entry is restored as it was, so token IDs it tracked for
// cleanup (an earlier incoming ID and any outgoing IDs) are not forgotten.
// Without one, exclusions are kept as an exclusions-only entry.
func discardInitialDistribution(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, tokenID int, prior rotationRoleState, hadPrior bool) error {
	if tokenID == 0 {
		return nil
	}
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return fmt.Errorf("reading rotation state to discard initial distribution proof: %w", err)
	}
	cur, ok := state.Roles[string(role)]
	if !ok || cur.IncomingID != tokenID {
		return nil
	}
	switch {
	case hadPrior:
		state.Roles[string(role)] = prior
	case len(cur.ExcludedUserIDs) > 0:
		state.Roles[string(role)] = rotationRoleState{ExcludedUserIDs: cur.ExcludedUserIDs}
	default:
		delete(state.Roles, string(role))
	}
	return writeRotationState(ctx, client, owner, repo, state)
}

// recordSuppliedEnrollment writes rotation-state proof for an administrator-
// supplied credential, including its resolved owner so the owner's account stays
// excluded from management even when the credential later stops authenticating.
// An existing entry keeps its token tracking and earlier exclusions; only its
// provenance changes to supplied.
func recordSuppliedEnrollment(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, userID int, now time.Time) error {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return fmt.Errorf("reading rotation state before supplied enrollment proof: %w", err)
	}
	// The credential just installed is the supplied one whatever the previous
	// entry says, so its provenance is recorded over a surviving managed entry.
	// That entry's token IDs and phase stay, so cleanup of the managed tokens it
	// tracks is unaffected.
	cur := state.Roles[string(role)]
	cur.markSupplied(userID)
	if cur.Phase == "" && cur.DistributedAt == "" && cur.IncomingID == 0 && len(cur.OutgoingIDs) == 0 {
		cur.Phase = rotationPhaseIdle
		cur.DistributedAt = now.UTC().Format(time.RFC3339)
	}
	// The caller has already stored the secret, so the supplied publication is
	// proven independently of any surviving managed-token state.
	cur.SuppliedDistributed = true
	if cur.DistributedAt == "" {
		cur.DistributedAt = now.UTC().Format(time.RFC3339)
	}
	state.Roles[string(role)] = cur
	return writeRotationState(ctx, client, owner, repo, state)
}

// recordSuppliedTokenID durably records the GitLab token ID of the enrolled
// supplied credential of role, so lifecycle analysis follows that token alone
// rather than every token of the owner's account. It applies only while the
// role's recorded supplied owner is userID. Callers hold the project lease. An
// unresolved ID writes nothing.
func recordSuppliedTokenID(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, userID, tokenID int) error {
	if tokenID <= 0 || userID <= 0 {
		return nil
	}
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return fmt.Errorf("reading rotation state before recording the supplied token ID: %w", err)
	}
	cur, ok := state.Roles[string(role)]
	if !ok {
		return nil
	}
	before := cur.SuppliedTokenID
	cur.setSuppliedToken(userID, tokenID)
	if cur.SuppliedTokenID == before {
		return nil
	}
	state.Roles[string(role)] = cur
	return writeRotationState(ctx, client, owner, repo, state)
}

// recordSuppliedProvenance durably records supplied ownership and the owner's
// exclusion for role before the administrator-supplied credential is published.
// It carries no distribution proof (that follows via recordSuppliedEnrollment
// once the secret is stored), so a failed store leaves only the conservative
// classification. Callers hold the project lease and must not publish the
// credential when this fails.
func recordSuppliedProvenance(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, userID int) error {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return fmt.Errorf("reading rotation state before supplied provenance: %w", err)
	}
	cur := state.Roles[string(role)]
	cur.markSupplied(userID)
	state.Roles[string(role)] = cur
	return writeRotationState(ctx, client, owner, repo, state)
}

// recordReplacementDistribution records rotation-state proof for a
// replacement credential that the caller created and stored after revoking the
// role's previous credential, under the project lease. Unlike
// recordInitialDistribution it replaces an existing role entry: that entry's
// IncomingID names the credential just revoked, so leaving it would misreport
// the healthy replacement as due for rotation after the idempotency window.
// Outgoing IDs whose revocation is not confirmed are preserved. When
// previousRevocationUnconfirmed is set, the previous IncomingID is preserved
// as an outgoing ID too, since its revocation failed and it may still be live.
func recordReplacementDistribution(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, tokenID int, expiresAt string, now time.Time, previousRevocationUnconfirmed bool) error {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return fmt.Errorf("reading rotation state before replacement distribution proof: %w", err)
	}
	// The caller revoked only the managed runtime PATs on the selected
	// service account, so outgoing IDs recorded earlier (for example a legacy
	// project access token queued by role rotation) are not confirmed revoked
	// and stay scheduled for grace cleanup under an overlapping phase.
	var outgoing []int
	previous := state.Roles[string(role)]
	for _, id := range previous.OutgoingIDs {
		if id != tokenID && !containsInt(outgoing, id) {
			outgoing = append(outgoing, id)
		}
	}
	if previousRevocationUnconfirmed && previous.IncomingID != 0 && previous.IncomingID != tokenID && !containsInt(outgoing, previous.IncomingID) {
		outgoing = append(outgoing, previous.IncomingID)
	}
	phase := rotationPhaseIdle
	if len(outgoing) > 0 {
		phase = rotationPhaseOverlapping
	}
	// The replacement is fullsend-minted, but the exclusion of any
	// administrator-owned account the previous entry recorded must survive.
	replacement := rotationRoleState{
		ManagedUserID:   previous.ManagedUserID,
		Phase:           phase,
		IncomingID:      tokenID,
		OutgoingIDs:     outgoing,
		DistributedAt:   now.UTC().Format(time.RFC3339),
		ExpiresAt:       expiresAt,
		ExcludedUserIDs: previous.ExcludedUserIDs,
	}
	replacement.excludeOwner(previous.SuppliedUserID)
	state.Roles[string(role)] = replacement
	return writeRotationState(ctx, client, owner, repo, state)
}

// backfillInitialDistributionProof records rotation-state distribution
// proof for a role whose secret was already present before this
// provisioning run (the present[secret] skip path in provisionOwnRoles),
// but only when the role has no rotation-state entry yet. Without this,
// a role provisioned before rotation-state tracking existed (the
// pre-#7500 #7498 path) is indistinguishable from a crash orphan: the
// first RotateGitLabRoleCredentials run against it would mint a
// replacement for every such role even though DiagnoseLifecycle reports
// it healthy.
//
// When a live PAT matching this role's token name can be listed, its ID
// and expiry are recorded as proof, mirroring the freshly-minted-PAT
// path in provisionOwnRoles. When no token client is configured at all
// (for example free-tier enrollment, where project access tokens cannot
// be created or listed via the API), an idle/IncomingID=0/DistributedAt
// proof is recorded instead, mirroring the administrator-provided-
// credential path, since no GitLab token ID can ever be resolved in that
// setup. When a token client *is* configured but lists no PAT matching
// this role's token name, the credential is left unproven: that is a
// genuinely unverified state (the secret exists but nothing backs it),
// not a healthy pre-existing token missing only its proof, and it must
// stay eligible for the normal due-for-rotation handling rather than
// being fabricated into a false "proven" state.
//
// An existing rotation-state entry for the role is left untouched -- it
// may reflect a genuine in-progress or due state that this backfill must
// never overwrite. Failure to read state or list tokens is non-fatal: it
// only adds a diagnostic, since the secret itself is already present.
func backfillInitialDistributionProof(ctx context.Context, cfg RoleProvisionConfig, rec gitlabroles.Registration, now time.Time, result *RoleProvisionResult) {
	state, _, err := loadRotationState(ctx, cfg.Client, cfg.Owner, cfg.Repo)
	if err != nil {
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf(
			"%s: reading rotation state to backfill distribution proof failed; a future rotation run may treat this pre-existing credential as unproven and replace it", rec.Name))
		return
	}
	if _, exists := state.Roles[string(rec.Name)]; exists {
		return
	}
	tokenID := 0
	expiresAt := ""
	if cfg.Tokens != nil {
		toks, err := cfg.Tokens.ListProjectAccessTokens(ctx, cfg.Owner, cfg.Repo)
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, fmt.Sprintf(
				"%s: listing GitLab project access tokens to backfill distribution proof failed; a future rotation run may treat this pre-existing credential as unproven and replace it", rec.Name))
			return
		}
		tokenName := rec.Credential.TokenName
		if tokenName == "" {
			tokenName = gitlabroles.CustomTokenName(rec.Name)
		}
		current := currentListed(tokensNamed(toks, tokenName))
		if current.ID == 0 {
			// No live PAT backs this secret: a genuinely unverified
			// credential, not a healthy one merely missing its proof.
			// Leave it unproven for the normal due-for-rotation checks.
			return
		}
		tokenID = current.ID
		expiresAt = current.ExpiresAt
	}
	if err := recordInitialDistribution(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec.Name, tokenID, expiresAt, now, false); err != nil {
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf(
			"%s: recording rotation-state distribution proof failed; a future rotation run will treat this credential as unproven and replace it", rec.Name))
	}
}

func snapshotsFrom(toks []ProjectAccessToken) []gitlabroles.TokenSnapshot {
	out := make([]gitlabroles.TokenSnapshot, 0, len(toks))
	for _, tok := range toks {
		out = append(out, gitlabroles.TokenSnapshot{
			ID:        tok.ID,
			Name:      tok.Name,
			Active:    tok.Active,
			ExpiresAt: tok.ExpiresAt,
			Revoked:   tok.Revoked,
		})
	}
	return out
}

func tokensNamed(listed []ProjectAccessToken, name string) []ProjectAccessToken {
	var out []ProjectAccessToken
	for _, tok := range listed {
		if tok.Name == name {
			out = append(out, tok)
		}
	}
	return out
}

// installedCredentialTokens returns the tokens that determine the lifecycle of
// a role's installed credential. Normally that is every token matching the
// role's name. When the installed credential is an enrolled supplied one whose
// token ID was recorded, it is that token alone, looked up by ID in the whole
// inventory (the token may carry any name) and reported under tokenName; a
// recorded token the inventory no longer lists yields none, so the role reads
// as unverified instead of being judged by an unrelated same-named token.
func installedCredentialTokens(listed, matches []ProjectAccessToken, tokenName string, rs rotationRoleState) []ProjectAccessToken {
	if !rs.Supplied || rs.SuppliedTokenID <= 0 {
		return matches
	}
	for _, tok := range listed {
		if tok.ID == rs.SuppliedTokenID {
			tok.Name = tokenName
			return []ProjectAccessToken{tok}
		}
	}
	return nil
}

// currentListed picks the "current" active PAT out of the tokens
// matching a role's token name, mirroring currentToken in lifecycle.go
// (../gitlabroles/lifecycle.go) so distributionProven compares
// IncomingID against a stable selection: the active, non-revoked token
// with the latest parseable expiry, falling back to the highest ID when
// no expiry parses. A dated winner is never displaced by an unparseable
// expiry, and equal expiries (as GitLab assigns per calendar day) break
// ties on the higher token ID so a same-day replacement is recognized
// as current rather than whichever token happens to be listed first.
func currentListed(matches []ProjectAccessToken) ProjectAccessToken {
	var best ProjectAccessToken
	var bestExp time.Time
	bestHas := false
	for _, tok := range matches {
		if !tok.Active || tok.Revoked {
			continue
		}
		exp, err := time.Parse("2006-01-02", strings.TrimSpace(tok.ExpiresAt))
		if err != nil {
			// Never let an unparseable expiry displace a winner that
			// already has a valid parsed expiry.
			if !bestHas && (best.ID == 0 || tok.ID > best.ID) {
				best = tok
			}
			continue
		}
		if !bestHas || exp.After(bestExp) || (exp.Equal(bestExp) && tok.ID > best.ID) {
			best, bestExp, bestHas = tok, exp, true
		}
	}
	return best
}

// suppliedOwnerSource is implemented by token clients that know the
// project-wide set of administrator-owned accounts.
type suppliedOwnerSource interface {
	SuppliedOwnerIDs(ctx context.Context, owner, repo string) ([]int, error)
}

// projectExcludedOwners returns every administrator-owned account that
// fullsend must never revoke tokens of: the exclusions recorded for any role
// in the rotation state plus the set the token client applies at its
// revocation boundary, so rotation never schedules a revocation the boundary
// would refuse. An error means the set could not be established.
func projectExcludedOwners(ctx context.Context, cfg RoleRotateConfig, state rotationStateFile, rs rotationRoleState) ([]int, error) {
	var out []int
	add := func(id int) {
		if id > 0 && !containsInt(out, id) {
			out = append(out, id)
		}
	}
	for _, other := range state.Roles {
		add(other.SuppliedUserID)
		for _, id := range other.ExcludedUserIDs {
			add(id)
		}
	}
	add(rs.SuppliedUserID)
	for _, id := range rs.ExcludedUserIDs {
		add(id)
	}
	if src, ok := cfg.Tokens.(suppliedOwnerSource); ok {
		ids, err := src.SuppliedOwnerIDs(ctx, cfg.Owner, cfg.Repo)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			add(id)
		}
	}
	return out, nil
}

// ownedByExcluded reports whether tok belongs to an account that is
// administrator-owned, which fullsend must never revoke. While any exclusion
// exists, a token whose owner is unknown is treated the same way, matching
// the revocation boundary.
func ownedByExcluded(tok ProjectAccessToken, excluded []int) bool {
	if len(excluded) == 0 {
		return false
	}
	return tok.UserID == 0 || containsInt(excluded, tok.UserID)
}

// withoutExcludedOwners drops tokens owned by administrator-owned accounts.
func withoutExcludedOwners(matches []ProjectAccessToken, excluded []int) []ProjectAccessToken {
	var out []ProjectAccessToken
	for _, tok := range matches {
		if !ownedByExcluded(tok, excluded) {
			out = append(out, tok)
		}
	}
	return out
}

func activeIDsExcept(matches []ProjectAccessToken, except int) []int {
	var ids []int
	for _, tok := range matches {
		if tok.Active && !tok.Revoked && tok.ID != except && tok.ID != 0 {
			ids = append(ids, tok.ID)
		}
	}
	return ids
}

func uniqueInts(ids []int) []int {
	seen := make(map[int]struct{}, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// deactivateListedToken marks the token with the given ID inactive in
// listed (in place) so later lifecycle analysis in this call treats it
// as revoked. It does not remove the entry from the slice.
func deactivateListedToken(listed *[]ProjectAccessToken, id int) {
	if listed == nil {
		return
	}
	out := (*listed)[:0]
	for _, tok := range *listed {
		if tok.ID == id {
			tok.Active = false
		}
		out = append(out, tok)
	}
	*listed = out
}

func wantedRoles(roles []gitlabroles.Role, reg gitlabroles.Registry) map[gitlabroles.Role]struct{} {
	if len(roles) == 0 {
		return nil
	}
	out := make(map[gitlabroles.Role]struct{}, len(roles))
	for _, r := range roles {
		out[r] = struct{}{}
	}
	for changed := true; changed; {
		changed = false
		for role := range out {
			rec, ok := reg.Lookup(role)
			if !ok || rec.Credential.Kind != gitlabroles.CredentialReuse {
				continue
			}
			if _, exists := out[rec.Credential.ReuseOf]; !exists {
				out[rec.Credential.ReuseOf] = struct{}{}
				changed = true
			}
		}
	}
	return out
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func roleWanted(want map[gitlabroles.Role]struct{}, name gitlabroles.Role) bool {
	if want == nil {
		return true
	}
	_, ok := want[name]
	return ok
}

func roleReportFrom(ctx context.Context, cfg RoleRotateConfig, rec gitlabroles.Registration, matches []ProjectAccessToken, now time.Time, lead time.Duration) gitlabroles.RoleReport {
	present := map[string]bool{}
	if rec.Credential.SecretName != "" {
		exists, err := cfg.Client.RepoSecretExists(ctx, cfg.Owner, cfg.Repo, rec.Credential.SecretName)
		if err == nil {
			present[rec.Credential.SecretName] = exists
		}
	}
	rep := gitlabroles.DiagnoseLifecycle(present, cfg.Registry, snapshotsFrom(matches), now, lead)
	for _, got := range rep.Roles {
		if got.Name == rec.Name {
			return got
		}
	}
	return gitlabroles.RoleReport{
		Name:       rec.Name,
		Kind:       rec.Kind,
		SecretName: rec.Credential.SecretName,
		TokenName:  rec.Credential.TokenName,
		ReuseOf:    rec.Credential.ReuseOf,
		State:      gitlabroles.RoleStateUnconfigured,
		Lifecycle:  gitlabroles.LifecycleUnconfigured,
	}
}

func sortRoleLists(result *RoleRotateResult) {
	sortRoles := func(s []gitlabroles.Role) {
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	}
	sortRoles(result.Rotated)
	sortRoles(result.Skipped)
	sortRoles(result.Reused)
	sortRoles(result.Overlapping)
	sortRoles(result.RolledBack)
	sortRoles(result.Cleaned)
	sortRoles(result.InProgress)
}

func secretLeakRotate(result RoleRotateResult) string {
	needles := []string{"glpat-", "glptt-", "gldt-"}
	check := func(s string) string {
		lower := strings.ToLower(s)
		for _, n := range needles {
			if strings.Contains(lower, n) {
				return n
			}
		}
		return ""
	}
	for _, d := range result.Diagnostics {
		if n := check(d); n != "" {
			return n
		}
	}
	for _, f := range result.Failed {
		if n := check(f.Reason); n != "" {
			return n
		}
		if n := check(string(f.Role)); n != "" {
			return n
		}
		if n := check(f.Secret); n != "" {
			return n
		}
	}
	for _, d := range result.Report.Diagnostics {
		if n := check(d); n != "" {
			return n
		}
	}
	return ""
}

// EnrichGitLabRoleStatus re-runs DiagnoseLifecycle with a token
// inventory so repos status can report expiry, revocation, and
// overlapping replacements. tokens may be nil. It reports whether any
// lifecycle drift entries were newly added to status.Drifts; callers
// must not assume a false return means the repo is otherwise free of
// drift, only that this call did not add to it.
func EnrichGitLabRoleStatus(ctx context.Context, client forge.Client, owner, repo string, tokens []ProjectAccessToken, now time.Time, status *RepoStatus) bool {
	if status == nil || client == nil {
		return false
	}
	reg, present, err := LoadGitLabRoleState(ctx, client, owner, repo)
	if err != nil {
		return false
	}
	rep := gitlabroles.DiagnoseLifecycle(present, reg, snapshotsFrom(tokens), now, gitlabroles.DefaultRotationLead)
	rotation, _, rotationErr := loadRotationState(ctx, client, owner, repo)
	if rotationErr == nil {
		applyAdministratorEnrollmentProof(&rep, reg, rotation, tokens, now)
	}
	status.GitLabRolesReady = rep.Ready
	status.GitLabRolesPartial = rep.Partial
	status.GitLabRoleDiagnostics = rep.Diagnostics
	lifecycle := make(map[gitlabroles.Role]gitlabroles.LifecycleState, len(rep.Roles))
	for _, rr := range rep.Roles {
		lifecycle[rr.Name] = rr.Lifecycle
	}
	builtin := appendBuiltinRoleReadiness(status, present, reg, lifecycle)
	registered := appendRegisteredRoleReadiness(status, present, reg, lifecycle)
	status.GitLabRolesReady = status.GitLabRolesReady && builtin.Ready && registered.Ready
	before := len(status.Drifts)
	seen := make(map[string]struct{}, len(status.Drifts))
	for _, d := range status.Drifts {
		seen[d.Field] = struct{}{}
	}
	for _, rr := range rep.Roles {
		switch rr.Lifecycle {
		case gitlabroles.LifecycleExpired, gitlabroles.LifecycleRevoked:
			field := "gitlab-role:" + string(rr.Name)
			if _, ok := seen[field]; ok {
				continue
			}
			status.Drifts = append(status.Drifts, Drift{
				Field:    field,
				Expected: "valid",
				Actual:   string(rr.Lifecycle),
			})
		}
	}
	return len(status.Drifts) > before
}

// applyAdministratorEnrollmentProof treats an administrator-provided
// credential (enrolled via --gitlab-role-token, which has no GitLab
// token ID to verify against the project-token inventory) as healthy
// when rotation state already recorded a distributed, idle proof for
// it. Without this, DiagnoseLifecycle would otherwise classify such a
// credential as unverified forever, since it never has a matching
// project access token snapshot to confirm. When the enrollment recorded the
// credential's token ID, enrollment proves only distribution: a successfully
// read inventory that no longer lists that token keeps the role unverified, and
// a listed token determines the lifecycle itself (revoked, expired, expiring, or
// healthy) rather than being assumed healthy.
func applyAdministratorEnrollmentProof(report *gitlabroles.Report, reg gitlabroles.Registry, rotation rotationStateFile, tokens []ProjectAccessToken, now time.Time) {
	if report == nil {
		return
	}
	oldLifecycleDiagnostics := 0
	for _, role := range report.Roles {
		if role.Lifecycle != "" && role.Lifecycle != gitlabroles.LifecycleUnconfigured && role.Lifecycle != gitlabroles.LifecycleOK {
			oldLifecycleDiagnostics++
		}
	}
	for i := range report.Roles {
		current := report.Roles[i].Lifecycle
		if current == "" || current == gitlabroles.LifecycleUnconfigured {
			continue
		}
		proofRole := report.Roles[i].Name
		seen := map[gitlabroles.Role]bool{}
		for !seen[proofRole] {
			seen[proofRole] = true
			registration, ok := reg.Lookup(proofRole)
			if !ok || registration.Credential.ReuseOf == "" {
				break
			}
			proofRole = registration.Credential.ReuseOf
		}
		state, ok := rotation.Roles[string(proofRole)]
		enrolledIdx := -1
		if ok && state.SuppliedTokenID > 0 && state.Supplied && !state.SuppliedDistributed && state.Phase == rotationPhaseFailed {
			// A replacement publication is in progress or unconfirmed: the
			// recorded token names the previous credential, but the installed
			// one may already be the replacement. Do not report the role
			// healthy on the previous token's state until the next run
			// reconciles the installed credential.
			if current == gitlabroles.LifecycleOK {
				report.Roles[i].Lifecycle = gitlabroles.LifecycleUnverified
			}
			continue
		}
		if ok && state.SuppliedTokenID > 0 {
			// The name-based lifecycle groups tokens by name only, so an
			// unrelated healthy token carrying the role's name could mask the
			// enrolled token. Judge the enrolled token itself, whatever the
			// name-based lifecycle reported.
			enrolledIdx = slices.IndexFunc(tokens, func(t ProjectAccessToken) bool { return t.ID == state.SuppliedTokenID })
			if enrolledIdx < 0 {
				report.Roles[i].Lifecycle = gitlabroles.LifecycleUnverified
				continue
			}
		} else if current != gitlabroles.LifecycleUnverified {
			continue
		}
		if enrolledIdx >= 0 {
			// The recorded token's own state decides the lifecycle, so a
			// revoked, expired, or expiring enrolled credential is never
			// reported healthy. The entry may still carry a surviving managed
			// rotation's incoming ID and phase (they track managed tokens for
			// cleanup), so neither gates the enrolled token's own lifecycle.
			report.Roles[i].ExpiresAt = ""
			report.Roles[i].Overlapping = false
			gitlabroles.AnnotateTokenLifecycle(&report.Roles[i], snapshotsFrom(tokens[enrolledIdx : enrolledIdx+1])[0], now, gitlabroles.DefaultRotationLead)
			continue
		}
		// A recorded successful supplied publication proves distribution on its
		// own, whatever managed-token state a surviving entry still carries
		// (the same proof the rotation path recognizes).
		suppliedProven := ok && state.Supplied && state.SuppliedDistributed && state.DistributedAt != ""
		idleProven := ok && state.IncomingID == 0 && state.Phase == rotationPhaseIdle && state.DistributedAt != ""
		if suppliedProven || idleProven {
			report.Roles[i].Lifecycle = gitlabroles.LifecycleOK
		}
	}
	gitlabroles.RefreshLifecycleDiagnostics(report, oldLifecycleDiagnostics)
}

// RecoverSuppliedPollerProvenance establishes supplied provenance from an
// explicitly provided Poller credential when rotation state records none, so a
// install interrupted between storing the Poller secret and recording its
// provenance can be recovered with --gitlab-role-token. It runs under the
// project lease, only for a credential whose owner the caller resolved, and
// never when provenance is already known (a known supplied credential whose
// owner was not recorded is repaired). The same-named account is never
// adopted as managed: the provided credential replaces the installed secret and
// its owner is recorded as an administrator-owned exclusion. It reports whether
// anything was recovered.
func RecoverSuppliedPollerProvenance(ctx context.Context, client forge.Client, owner, repo, token string, ownerID int, dryRun bool, resolveOutgoing func(ctx context.Context, role gitlabroles.Role) (int, error)) (recovered bool, err error) {
	return RecoverSuppliedRoleProvenance(ctx, client, owner, repo, gitlabroles.RolePoller, token, ownerID, dryRun, resolveOutgoing)
}

// RecoverSuppliedRoleProvenance is RecoverSuppliedPollerProvenance for any
// registered own-credential role (Poller, Analyst, Coder, or a custom role). The
// role's secret name comes from the trusted registry; a role that is not
// registered or reuses another role's credential recovers nothing.
//
// When the installed credential is known to be supplied but its owner was never
// recorded, resolveOutgoing attributes that owner so it is durably excluded
// before the credential is overwritten. Without a resolver, or when it cannot
// attribute the owner, nothing is replaced and the provenance stays unresolved.
func RecoverSuppliedRoleProvenance(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, token string, ownerID int, dryRun bool, resolveOutgoing func(ctx context.Context, role gitlabroles.Role) (int, error)) (recovered bool, err error) {
	return RecoverSuppliedRoleProvenanceForToken(ctx, client, owner, repo, role, token, ownerID, 0, dryRun, resolveOutgoing)
}

// RecoverSuppliedRoleProvenanceForToken is RecoverSuppliedRoleProvenance that
// also records tokenID, the GitLab token ID of the provided credential, when it
// is known.
func RecoverSuppliedRoleProvenanceForToken(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, token string, ownerID, tokenID int, dryRun bool, resolveOutgoing func(ctx context.Context, role gitlabroles.Role) (int, error)) (recovered bool, err error) {
	token = strings.TrimSpace(token)
	if dryRun || token == "" || ownerID <= 0 || !canMaskGitLabValue(token) {
		return false, nil
	}
	release, lockErr := LockGitLabProject(ctx, client, owner, repo, false)
	if lockErr != nil {
		return false, lockErr
	}
	defer release(&err)
	raw, _, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabRoleRegistry)
	if err != nil {
		return false, safeAPIError(fmt.Sprintf("reading %s to recover %q credential provenance", forge.VarGitLabRoleRegistry, role), err)
	}
	reg, err := gitlabroles.ParseRegistry(raw)
	if err != nil {
		return false, err
	}
	rec, ok := reg.Lookup(role)
	if !ok || rec.Credential.Kind == gitlabroles.CredentialReuse || rec.Credential.SecretName == "" {
		return false, nil
	}
	provs, err := RoleCredentialProvenances(ctx, client, owner, repo)
	if err != nil {
		return false, safeAPIError(fmt.Sprintf("reading %q credential provenance", role), err)
	}
	// Known provenance needs no recovery, except a supplied credential whose
	// owner was never recorded: it stays unattributable once the installed token
	// expires, so a resolved replacement owner is recorded here. Earlier
	// exclusions are retained by markSupplied.
	if prov := provs[role]; prov.Known && (!prov.Supplied || prov.UserID != 0) {
		return false, nil
	}
	// An installed credential whose owner is not recorded (a supplied credential
	// enrolled without an owner, or one of unknown provenance such as after an
	// interrupted enrollment) is overwritten below, so its owner is attributed
	// and durably excluded first, as well as the replacement's. Without an
	// installed credential there is nothing to attribute.
	if prov := provs[role]; !prov.Known || (prov.Supplied && prov.UserID == 0) {
		// Known supplied provenance is recorded before the secret is
		// published, so it does not prove a credential is installed: check
		// actual presence in both cases.
		installed, existsErr := client.RepoSecretExists(ctx, owner, repo, rec.Credential.SecretName)
		if existsErr != nil {
			return false, safeAPIError(fmt.Sprintf("checking whether the %q credential is installed before replacing it", role), existsErr)
		}
		if installed {
			outgoingOwner := 0
			if resolveOutgoing != nil {
				outgoingOwner, _ = resolveOutgoing(ctx, role)
			}
			if outgoingOwner <= 0 {
				return false, fmt.Errorf("the installed %q credential has no recorded owner and cannot be attributed; not replaced (its owner must be attributed before it can be replaced)", role)
			}
			if err := RecordSuppliedExclusions(ctx, client, owner, repo, role, []int{outgoingOwner}); err != nil {
				return false, safeAPIError(fmt.Sprintf("recording the outgoing %q credential owner exclusion", role), err)
			}
		}
	}
	// Provenance first, so an interruption never leaves the provided secret
	// without a record of who owns it.
	if err := recordSuppliedProvenance(ctx, client, owner, repo, role, ownerID); err != nil {
		return false, safeAPIError(fmt.Sprintf("recording the %q supplied credential provenance", role), err)
	}
	if err := client.CreateRepoSecret(ctx, owner, repo, rec.Credential.SecretName, token); err != nil {
		return false, safeAPIError(fmt.Sprintf("storing the administrator-provided %q credential to recover its provenance", role), err)
	}
	if err := recordSuppliedEnrollment(ctx, client, owner, repo, role, ownerID, time.Now()); err != nil {
		return false, safeAPIError(fmt.Sprintf("recording the %q supplied credential enrollment", role), err)
	}
	if err := recordSuppliedTokenID(ctx, client, owner, repo, role, ownerID, tokenID); err != nil {
		return false, safeAPIError(fmt.Sprintf("recording the %q supplied credential token ID", role), err)
	}
	return true, nil
}
