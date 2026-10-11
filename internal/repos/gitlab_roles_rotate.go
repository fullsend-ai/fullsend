package repos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// ProvidedTokens maps a role name to an administrator-supplied
	// replacement PAT. Values must never be logged.
	ProvidedTokens map[gitlabroles.Role]string
	// ConvergeServiceAccounts bypasses the recent-distribution skip only when
	// installation is replacing a legacy credential with a service account.
	ConvergeServiceAccounts bool
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

// rotationStateFile is the in-memory rotation-state document.
// writeRotationState persists it inside a rotationStateEnvelope carrying
// gitLabRoleRotationStateVersion; the version is wire metadata only.
type rotationStateFile struct {
	Roles map[string]rotationRoleState `json:"roles"`
}

// rotationStateEnvelope is the persisted rotation-state document. Version
// zero is the legacy unversioned form ({"roles":...}, no "version" key);
// writeRotationState always emits gitLabRoleRotationStateVersion.
type rotationStateEnvelope struct {
	Version int                          `json:"version"`
	Roles   map[string]rotationRoleState `json:"roles"`
}

// gitLabRoleRotationStateVersion is the rotation-state format version this
// binary writes and the highest version it reads.
//
// Version 2 is the first version ever written with a version marker. It
// carries the ownership fields (created_token_ids, managed_user_id,
// supplied_user_id, supplied_token_id, supplied, excluded_user_ids) that
// decide which tokens may be selected as outgoing credentials and revoked
// by grace cleanup. Version 1 is deliberately skipped: CLIs built with the
// tolerant reader (#8233) accept versions 0 and 1 but do not apply those
// ownership rules, so a version 1 document could let them select or revoke
// an administrator-supplied credential and rewrite the document without
// its marker. Those CLIs reject version 2 before touching any credential,
// and CLIs older still reject the unknown "version" key, so rotation and
// status operations of those CLIs, which consult rotation state before they
// mutate, fail closed (#8242). Older provisioning and uninstall paths do not
// consult it first and can still publish or revoke credentials, so operators
// must upgrade every CLI.
const gitLabRoleRotationStateVersion = 2

// maxReadableRotationStateVersion is the highest rotation-state version
// loadRotationState accepts. It equals gitLabRoleRotationStateVersion;
// tests lower it to exercise the reader of an older CLI.
var maxReadableRotationStateVersion = gitLabRoleRotationStateVersion

// rotationRoleState is the persisted per-role rotation state. Every
// field is omitempty so unused fields are not written.
type rotationRoleState struct {
	Phase         string `json:"phase,omitempty"`
	Holder        string `json:"holder,omitempty"`
	LockUntil     string `json:"lock_until,omitempty"`
	IncomingID    int    `json:"incoming_id,omitempty"`
	OutgoingIDs   []int  `json:"outgoing_ids,omitempty"`
	DistributedAt string `json:"distributed_at,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	Error         string `json:"error,omitempty"`

	// CreatedTokenIDs records only token IDs returned by a successful
	// creation by Fullsend, so cleanup never revokes a token it did not
	// mint.
	CreatedTokenIDs []int `json:"created_token_ids,omitempty"`
	// ManagedUserID records a service account positively created by
	// Fullsend for this role.
	ManagedUserID int `json:"managed_user_id,omitempty"`
	// SuppliedUserID is the GitLab user that owns an administrator-
	// supplied credential for this role.
	SuppliedUserID int `json:"supplied_user_id,omitempty"`
	// SuppliedTokenID is the GitLab token ID of the enrolled
	// administrator-supplied credential.
	SuppliedTokenID int `json:"supplied_token_id,omitempty"`
	// Supplied records that the installed credential was enrolled by an
	// administrator rather than minted by Fullsend.
	Supplied bool `json:"supplied,omitempty"`
	// SuppliedDistributed records that the enrolled supplied credential
	// was distributed to the role secret.
	SuppliedDistributed bool `json:"supplied_distributed,omitempty"`
	// ExcludedUserIDs are GitLab users that owned administrator-supplied
	// credentials and must never be treated as Fullsend-managed.
	ExcludedUserIDs []int `json:"excluded_user_ids,omitempty"`

	// Poller identity generations (#8210). Accounts are recorded by
	// numeric ID only, never by name and never with token material.
	//
	// GenerationCurrentUserID is the Poller account whose runtime
	// credential is published.
	GenerationCurrentUserID int `json:"generation_current_user_id,omitempty"`
	// GenerationPendingUserID is the fresh Poller account being handed
	// off, if any.
	GenerationPendingUserID int `json:"generation_pending_user_id,omitempty"`
	// GenerationPendingPhase is the handoff phase of the pending
	// generation. It is distinct from Phase, which is the credential
	// rotation phase.
	GenerationPendingPhase string `json:"generation_pending_phase,omitempty"`
	// GenerationRetiringUserID is the superseded Poller account still
	// being retired.
	GenerationRetiringUserID int `json:"generation_retiring_user_id,omitempty"`
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
	// A typed-nil service-account client counts as no token client.
	cfg.Tokens = normalizeServiceAccountClient(cfg.Tokens)
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

	if cfg.Tokens == nil && len(cfg.ProvidedTokens) == 0 {
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
	// Project-wide supplied and excluded accounts recorded in rotation state
	// are frozen before any replacement selection, membership change or token
	// creation, so direct rotation protects them without a supplied-account
	// callback, as convergence and provisioning already do.
	//
	// Any other token client does not enforce exclusions itself, so the same
	// recorded owners are applied to outgoing selection and grace revocation.
	//
	// excludedOwners is that project-wide set of administrator-owned account
	// IDs. Tokens owned by these accounts are never selected as outgoing nor
	// revoked at grace cleanup, whatever the concrete token client.
	unresolvedOwner := false
	var excludedOwners []int
	// resolvedOwners are the supplied-credential owners learned only through the
	// token client's SuppliedOwnerIDs capability. They are persisted when the
	// supplied provenance is cleared, so a later run still excludes them.
	var resolvedOwners []int
	if sa, ok := normalizeServiceAccountClient(cfg.Tokens).(ServiceAccountTokenClient); ok {
		wrapped, frozen, exclErr := sa.withProjectExclusions(ctx, cfg.Owner, cfg.Repo, state)
		if exclErr != nil {
			reason := "project-wide account exclusions could not be resolved; no credential created"
			if errors.Is(exclErr, ErrSuppliedCredentialUnresolved) && unresolvedSuppliedOwner(state) {
				reason += ownerlessSuppliedRecoveryHint
			}
			result.Failed = append(result.Failed, RoleProvisionFailure{
				Role: rec.Name, Secret: secret, Reason: reason,
			})
			return
		}
		// A replacement Poller must hold protected-ref pipeline access before it
		// is published, whatever the configured verifier checks.
		cfg.Tokens = wrapped.withPollerPipelineAccess(cfg.Client)
		excludedOwners = frozen
		// Owners attributed only by authenticating are persisted before this
		// run can replace or retire any credential, so a replacement never
		// loses the original owner's exclusion and a later expiry of the
		// credential does not make its owner unattributable.
		if !cfg.DryRun && wrapped.AttributeSuppliedOwners != nil && unresolvedSuppliedOwner(state) {
			attributed, attrErr := wrapped.AttributeSuppliedOwners(ctx, cfg.Owner, cfg.Repo)
			if attrErr == nil {
				attrErr = RecordSuppliedOwners(ctx, cfg.Client, cfg.Owner, cfg.Repo, attributed)
			}
			if attrErr != nil {
				result.Failed = append(result.Failed, RoleProvisionFailure{
					Role: rec.Name, Secret: secret, Reason: "supplied credential owners could not be recorded; no credential rotated" + ownerlessSuppliedRecoveryHint,
				})
				return
			}
			fresh, _, freshErr := loadRotationState(ctx, cfg.Client, cfg.Owner, cfg.Repo)
			if freshErr != nil {
				result.Failed = append(result.Failed, RoleProvisionFailure{
					Role: rec.Name, Secret: secret, Reason: "reading rotation state failed",
				})
				return
			}
			state = fresh
			rs = state.Roles[string(rec.Name)]
		}
		// Frozen owners that are not yet recorded were learned only through
		// SuppliedAccountIDs. Persist them whether or not the optional
		// AttributeSuppliedOwners callback is set, so a replacement that
		// clears the supplied provenance never loses them.
		recorded := recordedExcludedOwners(state)
		for _, id := range frozen {
			if !containsInt(recorded, id) {
				resolvedOwners = appendExcludedID(resolvedOwners, id)
			}
		}
	} else {
		// A supplied credential whose owner was never recorded cannot be
		// attributed by recorded exclusions alone, so a same-named
		// administrator-supplied token could become an outgoing obligation and
		// be revoked. An ownership-aware client can resolve them through the
		// optional SuppliedOwnerIDs capability, as cleanup does; the resolved IDs
		// join the recorded exclusions. Otherwise grace cleanup and replacement
		// are refused below, once the role is known to need them. A healthy,
		// not-due enrollment is still skipped without error.
		excludedOwners = recordedExcludedOwners(state)
		if cfg.Tokens != nil && unresolvedSuppliedOwner(state) {
			if ids, resolveErr := resolveSuppliedOwnersViaCapability(ctx, cfg.Tokens, cfg.Owner, cfg.Repo); resolveErr != nil {
				unresolvedOwner = true
			} else {
				for _, id := range ids {
					excludedOwners = appendExcludedID(excludedOwners, id)
					resolvedOwners = appendExcludedID(resolvedOwners, id)
				}
				sort.Ints(excludedOwners)
			}
		}
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
	// Owners resolved only through the client capability become permanent
	// exclusions in rs before any replacement is published, so every write of rs
	// below (the provided-token transition, the distributing phase before
	// CreateRepoSecret, failure records and the completed state) carries them.
	// Both publishing paths refuse to publish when their pre-publication write
	// fails, so an interrupted or partly failed publication cannot leave the
	// replaced credential's owner unrecorded.
	if !cfg.DryRun {
		for _, id := range resolvedOwners {
			rs.excludeOwner(id)
		}
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
	current := currentListed(matches)
	if !cfg.DryRun && unresolvedOwner && len(rs.OutgoingIDs) > 0 {
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: outgoing tokens were kept and not revoked because supplied credential ownership is unresolved", rec.Name))
	}
	if !cfg.DryRun && !unresolvedOwner {
		prevOutgoing, prevPhase := len(rs.OutgoingIDs), rs.Phase
		cleaned := cleanupOutgoing(ctx, cfg, &rs, now, grace, listed, excludedOwners)
		if cleaned {
			result.Cleaned = append(result.Cleaned, rec.Name)
		}
		// Dropping an excluded owner's outgoing obligation changes durable state
		// without revoking anything, so it is persisted whether or not a token was
		// revoked.
		if cleaned || len(rs.OutgoingIDs) != prevOutgoing || rs.Phase != prevPhase {
			_ = mergeRoleState(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec.Name, holder, now, rs, &state)
			matches = tokensNamed(*listed, tokenName)
			current = currentListed(matches)
		}
		if dist, err := time.Parse(time.RFC3339, rs.DistributedAt); err == nil && rs.Phase == rotationPhaseOverlapping && !now.Before(dist.Add(grace)) {
			for _, id := range rs.OutgoingIDs {
				if listedTokenOwnerUnverified(*listed, id, excludedOwners) {
					result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: outgoing token id %d was kept and not revoked because its owner cannot be verified while administrator-owned accounts are excluded", rec.Name, id))
				}
			}
		}
	}

	// An enrolled supplied credential listed under the role's token name is
	// judged on its own lifecycle. Historical managed tokens that stay listed
	// after a managed-to-supplied enrollment must not make a revoked or expiring
	// supplied credential read as healthy.
	reportMatches := matches
	if provenanceOf(rs).Supplied {
		if supplied := suppliedCredentialTokens(rs, matches); len(supplied) > 0 {
			reportMatches = supplied
			// The idempotency and distribution checks below must also judge the
			// enrolled credential, not a historical managed token whose expiry
			// happens to equal the fresh expiry.
			current = currentListed(supplied)
		}
	}
	rr := roleReportFrom(ctx, cfg, rec, reportMatches, now, lead)
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
	distributionProven := (rs.IncomingID != 0 && rs.IncomingID == current.ID && rs.DistributedAt != "") ||
		providedDistributionProven
	needsRecovery := rs.IncomingID != 0 && (rs.Phase == rotationPhaseDistributing || rs.Phase == rotationPhaseFailed)
	alreadyFresh := !cfg.Force && current.ID != 0 && current.Active && current.ExpiresAt == freshExpiry &&
		distributionProven && !needsRecovery
	// The operational inventory lists only project access tokens and project
	// service-account PATs, so an administrator-supplied personal access token
	// has no snapshot to verify. A healthy supplied enrollment with distribution
	// proof is retained rather than read as unverified and replaced on every
	// unforced run; its replacement is an explicit --gitlab-role-token
	// enrollment or a forced rotation.
	if !cfg.Force && distributionProven && provenanceOf(rs).Supplied && !suppliedCredentialListed(rs, matches) &&
		strings.TrimSpace(cfg.ProvidedTokens[rec.Name]) == "" {
		result.Skipped = append(result.Skipped, rec.Name)
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf(
			"%s: administrator-supplied credential is outside the lifecycle inventory; retained without verifying that it still authenticates or has not expired (re-enroll with --gitlab-role-token to replace it)", rec.Name))
		return
	}
	if !cfg.Force && !cfg.ConvergeServiceAccounts && strings.TrimSpace(cfg.ProvidedTokens[rec.Name]) == "" {
		retained, reason, lookupErr := legacyCredentialWithoutProvenance(ctx, cfg, rec, rs, matches)
		if lookupErr != nil {
			// The installed secret's presence could not be established, so the
			// credential is left untouched rather than treated as absent.
			result.Failed = append(result.Failed, RoleProvisionFailure{
				Role: rec.Name, Secret: secret,
				Reason: "could not verify whether a legacy credential is installed; credential left untouched: " + lookupErr.Error(),
			})
			return
		}
		if retained {
			result.Skipped = append(result.Skipped, rec.Name)
			result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: %s", rec.Name, reason))
			return
		}
	}
	if alreadyFresh || (recentlyDistributed(rs, now) && !gitlabroles.RoleDueForRotation(rr) && !cfg.ConvergeServiceAccounts) {
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

	if provided := strings.TrimSpace(cfg.ProvidedTokens[rec.Name]); provided != "" {
		refused := rotateProvided(ctx, cfg, rec, secret, provided, now, matches, holder, &state, &rs, result)
		if !cfg.DryRun && !refused {
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

	if unresolvedOwner {
		result.Failed = append(result.Failed, RoleProvisionFailure{
			Role: rec.Name, Secret: secret, Reason: "supplied credential ownership is unresolved; no credential rotated",
		})
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: %s; refusing to rotate", rec.Name, ErrSuppliedCredentialUnresolved))
		return
	}

	if cfg.DryRun {
		result.Rotated = append(result.Rotated, rec.Name)
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: would rotate (%s)", rec.Name, secret))
		return
	}

	if rs.Phase == rotationPhaseDistributing && rs.IncomingID != 0 {
		// The state may be stale after a crash immediately after the CI
		// variable was updated. The incoming token may therefore be the
		// live credential; never revoke it without proof that distribution
		// did not complete. Keep it in the outgoing set and let the normal
		// grace cleanup retire it after a replacement is distributed.
		if !containsInt(rs.OutgoingIDs, rs.IncomingID) {
			rs.OutgoingIDs = append(rs.OutgoingIDs, rs.IncomingID)
		}
		rs.Phase = rotationPhaseFailed
		rs.Error = "previous distribution state was incomplete; preserving incoming token for safe recovery"
	}

	expiresAt := GitLabPATExpiresAt(now)
	tok, err := cfg.Tokens.CreateProjectAccessToken(ctx, cfg.Owner, cfg.Repo, tokenName,
		gitlabroles.TokenScopes(), gitlabroles.DeveloperAccessLevel, expiresAt)
	if err != nil {
		var unavailable *serviceAccountUnavailableError
		if cfg.ConvergeServiceAccounts && errors.As(err, &unavailable) {
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

	// Tokens owned by a recorded excluded account, or whose owner cannot be
	// established while exclusions exist, never become outgoing obligations, so
	// grace cleanup cannot revoke them (generic clients only).
	outgoing := activeIDsExcept(tokensWithClearedOwner(matches, excludedOwners), tok.ID)
	if current.ID != 0 && current.ID != tok.ID && len(tokensWithClearedOwner(tokensWithID(matches, current.ID), excludedOwners)) > 0 {
		outgoing = uniqueInts(append(outgoing, current.ID))
	}
	// An earlier outgoing obligation that grace cleanup could not discharge stays
	// owed even when the token is absent from this run's operational matches
	// (renamed, or omitted because its owner is unverified). It is dropped only
	// when an excluded owner positively owns it.
	for _, id := range rs.OutgoingIDs {
		if id == tok.ID || id == 0 || listedTokenOwnedByExcluded(*listed, id, excludedOwners) {
			continue
		}
		outgoing = uniqueInts(append(outgoing, id))
	}
	if len(excludedOwners) > 0 {
		unverified := 0
		for _, m := range matches {
			if m.ID != tok.ID && m.Active && !m.Revoked && m.UserID <= 0 {
				unverified++
			}
		}
		if unverified > 0 {
			result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: %d same-named active token(s) were not scheduled for revocation because their owner cannot be verified while administrator-owned accounts are excluded", rec.Name, unverified))
		}
	}
	rs.Phase = rotationPhaseDistributing
	rs.CreatedTokenIDs = uniqueInts(append(rs.CreatedTokenIDs, tok.ID))
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
		if tok.ID != 0 {
			if revErr := cfg.Tokens.RevokeProjectAccessToken(ctx, cfg.Owner, cfg.Repo, tok.ID); revErr != nil {
				result.Diagnostics = append(result.Diagnostics, fmt.Sprintf(
					"%s: replacement PAT id %d left for cleanup after failed distribution", rec.Name, tok.ID))
			}
		}
		rs.Phase = rotationPhaseFailed
		rs.IncomingID = 0
		rs.Error = "storing replacement credential failed"
		_ = mergeRoleState(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec.Name, holder, now, rs, &state)
		result.RolledBack = append(result.RolledBack, rec.Name)
		result.Failed = append(result.Failed, RoleProvisionFailure{
			Role: rec.Name, Secret: secret,
			Reason: "storing replacement credential failed; previous credential left in place",
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
	rs.Error = ""
	// The managed replacement is now the published credential, so the previous
	// supplied credential's provenance no longer describes it. Its owner stays
	// permanently excluded; owners resolved only through the client capability
	// were already added to rs and persisted by the distributing-phase write
	// before publication.
	rs.excludeOwner(rs.SuppliedUserID)
	rs.Supplied, rs.SuppliedUserID, rs.SuppliedTokenID, rs.SuppliedDistributed = false, 0, 0, false
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

// legacyCredentialWithoutProvenance reports whether an installed role secret is
// a legacy project-access-token credential that lacks positive creation
// provenance. The service-account client deliberately leaves such a token out of
// its operational inventory (names and distribution backfill never confer
// ownership), so an empty inventory match is not evidence that the credential is
// missing. Provisioning already leaves it untouched; unforced automatic rotation
// must not mint a replacement and overwrite it either. It is replaced only by an
// explicit forced rotation or --gitlab-role-token enrollment. A nil
// ManagedLegacyTokenIDs callback supplies no ownership evidence, so it is an
// empty allowlist rather than a bypass. A failed secret-existence lookup is
// returned as an error so the caller leaves the credential untouched.
func legacyCredentialWithoutProvenance(ctx context.Context, cfg RoleRotateConfig, rec gitlabroles.Registration, rs rotationRoleState, matches []ProjectAccessToken) (bool, string, error) {
	c, ok := normalizeServiceAccountClient(cfg.Tokens).(ServiceAccountTokenClient)
	if !ok || len(matches) > 0 {
		return false, "", nil
	}
	if rs.ManagedUserID > 0 || hasPositiveInt(rs.CreatedTokenIDs) || provenanceOf(rs).Supplied {
		return false, "", nil
	}
	exists, err := cfg.Client.RepoSecretExists(ctx, cfg.Owner, cfg.Repo, rec.Credential.SecretName)
	if err != nil {
		return false, "", safeAPIError("checking the installed role secret", err)
	}
	if !exists {
		return false, "", nil
	}
	if owned, err := c.managedLegacyIDs(ctx, cfg.Owner, cfg.Repo); err == nil && rs.IncomingID != 0 && containsInt(owned, rs.IncomingID) {
		return false, "", nil
	}
	return true, "legacy token creation provenance is unverified; credential left untouched; manual recovery or explicit supplied-credential enrollment required", nil
}

// ownerlessSuppliedRecoveryHint is the administrator recovery for a supplied
// credential recorded without its owner. Attribution authenticates with the
// installed role credential, and the project-wide exclusions are resolved
// before provisioning or rotation processes any --gitlab-role-token value, so
// passing a replacement token again cannot recover a credential that no longer
// authenticates or whose variable was removed.
const ownerlessSuppliedRecoveryHint = "; if the supplied credential expires, is revoked or its variable is removed before the owner is recorded, install and rotation fail closed for the whole project and --gitlab-role-token alone does not recover: set a working credential in the role's CI/CD variable by hand, then rerun install so its owner is recorded"

// rotateProvided enrolls an administrator-provided replacement. It returns
// true when nothing was published because the supplied transition could not be
// recorded first, so the caller must not persist rs.
func rotateProvided(ctx context.Context, cfg RoleRotateConfig, rec gitlabroles.Registration, secret, provided string, now time.Time, matches []ProjectAccessToken, holder string, state *rotationStateFile, rs *rotationRoleState, result *RoleRotateResult) (refused bool) {
	if !canMaskGitLabValue(provided) {
		result.Failed = append(result.Failed, RoleProvisionFailure{
			Role: rec.Name, Secret: secret,
			Reason: "administrator-provided credential cannot be masked (must be a single line of at least 8 characters using GitLab's allowed charset)",
		})
		return false
	}
	if cfg.DryRun {
		result.Rotated = append(result.Rotated, rec.Name)
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: would enroll replacement (%s)", rec.Name, secret))
		return false
	}
	// Resolve the replacement's owner and persist its supplied transition,
	// with both the previous and the replacement owner excluded, before
	// publishing it, and refuse to publish when that write fails: a published
	// administrator-supplied credential still described by the previous entry
	// could be inventoried and revoked as fullsend-minted after a crash or a
	// failed later write. Only the provenance is written here; the lifecycle
	// fields keep describing the credential that is still installed, so a
	// failed or interrupted publication stays retryable.
	identity, identityErr := resolveSuppliedIdentity(ctx, cfg.Tokens, provided)
	intent := *rs
	intent.ExcludedUserIDs = append([]int(nil), rs.ExcludedUserIDs...)
	applySuppliedTransition(&intent, identity)
	// The previous credential's completed distribution proof must not describe
	// the replacement while it is only pending: an interruption before
	// CreateRepoSecret would otherwise leave state claiming the replacement is
	// installed. A failed phase invalidates that proof until publication
	// succeeds and the completed state is written below.
	intent.Phase = rotationPhaseFailed
	intent.Error = "administrator-provided replacement publication pending"
	if err := mergeRoleState(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec.Name, holder, now, intent, state); err != nil {
		result.Failed = append(result.Failed, RoleProvisionFailure{
			Role: rec.Name, Secret: secret,
			Reason: "recording administrator-provided replacement state failed; credential not stored; previous credential left in place",
		})
		return true
	}
	// Keep the exclusions even if publication fails and the previous
	// provenance is restored below.
	rs.ExcludedUserIDs = intent.ExcludedUserIDs
	if err := cfg.Client.CreateRepoSecret(ctx, cfg.Owner, cfg.Repo, secret, provided); err != nil {
		rs.Phase = rotationPhaseFailed
		rs.Error = "storing administrator-provided replacement failed"
		// A write error does not prove the variable update was not committed,
		// and the installed value cannot be read back to tell. The replacement's
		// owner is excluded when it resolved, so restoring the previous
		// provenance is safe then. When it did not resolve, nothing protects the
		// owner of a replacement that may be installed, so keep the ownerless
		// supplied transition recorded before publication: later operations
		// attribute the installed credential or fail closed. Passing
		// --gitlab-role-token again does not recover this state, because the
		// project-wide exclusions are resolved before any provided credential
		// is processed; an administrator restores a working credential by hand
		// (see ownerlessSuppliedRecoveryHint).
		if identity.UserID <= 0 {
			applySuppliedTransition(rs, identity)
		}
		result.Failed = append(result.Failed, RoleProvisionFailure{
			Role: rec.Name, Secret: secret,
			Reason: "storing administrator-provided replacement failed; previous credential left in place",
		})
		return false
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
	// The replaced supplied credential's owner stays permanently excluded. The
	// replacement is attributed to its own owner, resolved before publication;
	// when that could not be resolved the old owner is not carried over as the
	// replacement's, and the entry stays supplied-without-owner so later
	// operations attribute it or fail closed.
	applySuppliedTransition(rs, identity)
	if identityErr != nil {
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf(
			"%s: recording the enrolled replacement's owner failed%s", rec.Name, ownerlessSuppliedRecoveryHint))
	}
	result.Rotated = append(result.Rotated, rec.Name)
	// Do not imply that grace cleanup will retire any other active
	// same-named PAT: OutgoingIDs is nil above, so cleanupOutgoing has nothing
	// to act on and will never revoke a leftover automatically. When the
	// replacement's own token ID resolved it is excluded from the leftover
	// count and the others are reported as deliberately retained; otherwise
	// every match is counted because the replacement cannot be told apart.
	if identityErr == nil && identity.TokenID > 0 {
		if leftover := activeIDsExcept(matches, identity.TokenID); len(leftover) > 0 {
			result.Diagnostics = append(result.Diagnostics, fmt.Sprintf(
				"%s: enrolled administrator-provided replacement (%s); %d other active project access token(s) sharing this role's token name were deliberately retained and not scheduled for automatic revocation -- revoke them manually if they are no longer needed",
				rec.Name, secret, len(leftover)))
		} else {
			result.Diagnostics = append(result.Diagnostics, fmt.Sprintf(
				"%s: enrolled administrator-provided replacement (%s)", rec.Name, secret))
		}
	} else if leftover := activeIDsExcept(matches, 0); len(leftover) > 0 {
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf(
			"%s: enrolled administrator-provided replacement (%s); %d other active project access token(s) sharing this role's token name were not scheduled for automatic revocation because the replacement's own GitLab token ID is unknown -- confirm they are not the just-enrolled replacement and revoke them manually if so",
			rec.Name, secret, len(leftover)))
	} else {
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf(
			"%s: enrolled administrator-provided replacement (%s)", rec.Name, secret))
	}
	return false
}

// GitLabOutgoingTokenVerifier is the optional authoritative inventory capability
// used only to retire outgoing rotation obligations that are already inactive.
type GitLabOutgoingTokenVerifier interface {
	ConfirmOutgoingTokenInactive(ctx context.Context, owner, repo string, tokenID int) (bool, error)
}

func cleanupOutgoing(ctx context.Context, cfg RoleRotateConfig, rs *rotationRoleState, now time.Time, grace time.Duration, listed *[]ProjectAccessToken, excludedOwners []int) bool {
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
		// A token owned by a recorded excluded account is not this role's to
		// revoke, even if it entered the outgoing set through a client that does
		// not enforce exclusions. Drop the obligation without revoking it.
		if listedTokenOwnedByExcluded(*listed, id, excludedOwners) {
			continue
		}
		// While exclusions exist, a token whose owner cannot be established (no
		// positive owner, or absent from the listing) may belong to an excluded
		// account. It is never revoked through a generic client; the obligation is
		// kept unless the verifier proves the token is already inactive.
		unverified := listedTokenOwnerUnverified(*listed, id, excludedOwners)
		// A complete inventory can discharge a missing or inactive outgoing
		// credential, while generic revocation retains its strict error contract.
		// The inactivity check is only an optimization: when inactivity cannot
		// be established (for example legacy fallback with no service-account
		// client, or an unsupported listing), fall through to the
		// authorization-enforcing revoke and keep the obligation only if that
		// fails.
		inactive := false
		if verifier, ok := cfg.Tokens.(GitLabOutgoingTokenVerifier); ok {
			if confirmed, err := verifier.ConfirmOutgoingTokenInactive(ctx, cfg.Owner, cfg.Repo, id); err == nil {
				inactive = confirmed
			}
		}
		if !inactive {
			if unverified {
				remaining = append(remaining, id)
				continue
			}
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

// excludeOwner records an administrator-owned account as permanently excluded
// from fullsend's management. A zero ID is ignored.
func (rs *rotationRoleState) excludeOwner(id int) {
	if id > 0 && !containsInt(rs.ExcludedUserIDs, id) {
		rs.ExcludedUserIDs = append(rs.ExcludedUserIDs, id)
		sort.Ints(rs.ExcludedUserIDs)
	}
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

func loadRotationState(ctx context.Context, client forge.Client, owner, repo string) (rotationStateFile, []string, error) {
	out := rotationStateFile{Roles: map[string]rotationRoleState{}}
	raw, exists, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabRoleRotation)
	if err != nil {
		return out, nil, err
	}
	if !exists || strings.TrimSpace(raw) == "" {
		return out, nil, nil
	}
	// Unknown fields are tolerated (no DisallowUnknownFields) during the
	// rotation-state format transition so this binary can read state
	// written by a newer one. Unknown fields are dropped if this binary
	// rewrites the state.
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	var envelope rotationStateEnvelope
	if err := dec.Decode(&envelope); err != nil {
		return out, nil, fmt.Errorf("decode GitLab role rotation state: %w", err)
	}
	// A valid JSON prefix followed by anything else is a damaged document, not
	// a smaller one: reading only the prefix could drop recorded exclusions.
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return out, nil, errors.New("decode GitLab role rotation state: unexpected data after the JSON document")
	}
	if envelope.Version < 0 || envelope.Version > maxReadableRotationStateVersion {
		return out, nil, fmt.Errorf("unsupported GitLab role rotation state version %d; use a compatible CLI", envelope.Version)
	}
	file := rotationStateFile{Roles: envelope.Roles}
	if file.Roles == nil {
		file.Roles = map[string]rotationRoleState{}
	}
	for role, rs := range file.Roles {
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

// writeRotationState persists file as a gitLabRoleRotationStateVersion
// document. Every write carries the version marker, including rewrites of
// state that was read in the legacy unversioned form.
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
	if created := uniqueInts(append(append([]int(nil), rs.CreatedTokenIDs...), state.Roles[string(role)].CreatedTokenIDs...)); len(created) > 0 {
		rs.CreatedTokenIDs = created
	}
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
func recordInitialDistribution(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, tokenID int, expiresAt string, now time.Time) error {
	// Re-read immediately before writing and make this proof create-if-absent.
	// Initial provisioning must never overwrite a rotation that started after
	// the caller's earlier inventory read.
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return fmt.Errorf("reading rotation state before initial distribution proof: %w", err)
	}
	if existing, exists := state.Roles[string(role)]; exists {
		// An ownership-only entry (account recorded before its first PAT was
		// minted) or a token-only creation record (the legacy fallback wrote
		// CreatedTokenIDs for the distributed token) carries no lifecycle proof
		// yet; the first distribution fills it in without touching ownership,
		// creation records, exclusions or any other recorded field. An
		// exclusions-only entry (left behind by uninstall, or written for another
		// role's supplied owner) also carries no lifecycle proof, so it receives
		// the first distribution too, including a supplied enrollment with no
		// token ID, and keeps its ExcludedUserIDs.
		lifecycleEmpty := existing.Phase == "" && existing.Holder == "" && existing.LockUntil == "" &&
			existing.IncomingID == 0 && existing.DistributedAt == "" && len(existing.OutgoingIDs) == 0 &&
			!existing.Supplied && existing.SuppliedUserID == 0
		ownershipOnly := tokenID > 0 && (existing.ManagedUserID > 0 || containsInt(existing.CreatedTokenIDs, tokenID))
		exclusionsOnly := len(existing.ExcludedUserIDs) > 0 && existing.ManagedUserID == 0 && len(existing.CreatedTokenIDs) == 0 &&
			existing.SuppliedTokenID == 0 && existing.Error == "" && existing.ExpiresAt == "" &&
			existing.GenerationCurrentUserID == 0 && existing.GenerationPendingUserID == 0 &&
			existing.GenerationPendingPhase == "" && existing.GenerationRetiringUserID == 0
		if !lifecycleEmpty || !(ownershipOnly || exclusionsOnly) {
			return nil
		}
		existing.Phase = rotationPhaseIdle
		existing.IncomingID = tokenID
		existing.DistributedAt = now.UTC().Format(time.RFC3339)
		existing.ExpiresAt = expiresAt
		state.Roles[string(role)] = existing
		return writeRotationState(ctx, client, owner, repo, state)
	}
	rs := rotationRoleState{
		Phase:         rotationPhaseIdle,
		IncomingID:    tokenID,
		DistributedAt: now.UTC().Format(time.RFC3339),
		ExpiresAt:     expiresAt,
	}
	state.Roles[string(role)] = rs
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
		CreatedTokenIDs: uniqueInts(append(append([]int(nil), previous.CreatedTokenIDs...), tokenID)),
		ManagedUserID:   previous.ManagedUserID,
		Phase:           phase,
		IncomingID:      tokenID,
		OutgoingIDs:     outgoing,
		DistributedAt:   now.UTC().Format(time.RFC3339),
		ExpiresAt:       expiresAt,
		ExcludedUserIDs: append([]int(nil), previous.ExcludedUserIDs...),
		// Poller identity generations describe accounts, not this credential.
		GenerationCurrentUserID:  previous.GenerationCurrentUserID,
		GenerationPendingUserID:  previous.GenerationPendingUserID,
		GenerationPendingPhase:   previous.GenerationPendingPhase,
		GenerationRetiringUserID: previous.GenerationRetiringUserID,
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
	if err := recordInitialDistribution(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec.Name, tokenID, expiresAt, now); err != nil {
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

// recordedExcludedOwners is the project-wide set of administrator-owned account
// IDs recorded in rotation state: every supplied owner and excluded user ID.
func recordedExcludedOwners(state rotationStateFile) []int {
	var excluded []int
	for _, rs := range state.Roles {
		excluded = appendExcludedID(excluded, rs.SuppliedUserID)
		for _, id := range rs.ExcludedUserIDs {
			excluded = appendExcludedID(excluded, id)
		}
	}
	sort.Ints(excluded)
	return excluded
}

// tokensWithClearedOwner drops tokens that exclusions forbid touching. When
// exclusions exist, only a token whose owner is positively known and not
// excluded remains: an unattributable token could belong to an excluded account.
func tokensWithClearedOwner(tokens []ProjectAccessToken, excluded []int) []ProjectAccessToken {
	if len(excluded) == 0 {
		return tokens
	}
	var out []ProjectAccessToken
	for _, tok := range tokens {
		if tok.UserID > 0 && !containsInt(excluded, tok.UserID) {
			out = append(out, tok)
		}
	}
	return out
}

func listedTokenOwnedByExcluded(listed []ProjectAccessToken, id int, excluded []int) bool {
	if len(excluded) == 0 {
		return false
	}
	for _, tok := range listed {
		if tok.ID == id {
			return tok.UserID > 0 && containsInt(excluded, tok.UserID)
		}
	}
	return false
}

// listedTokenOwnerUnverified reports whether exclusions exist and the token's
// owner cannot be established from the listing, either because the token is not
// listed or because it reports no positive owner. Such a token is never revoked
// through a generic client.
func listedTokenOwnerUnverified(listed []ProjectAccessToken, id int, excluded []int) bool {
	if len(excluded) == 0 {
		return false
	}
	for _, tok := range listed {
		if tok.ID == id {
			return tok.UserID <= 0
		}
	}
	return true
}

func tokensWithID(listed []ProjectAccessToken, id int) []ProjectAccessToken {
	var out []ProjectAccessToken
	for _, tok := range listed {
		if tok.ID == id {
			out = append(out, tok)
		}
	}
	return out
}

// suppliedCredentialListed reports whether a same-named token in matches could
// be the enrolled administrator-supplied credential.
func suppliedCredentialListed(rs rotationRoleState, matches []ProjectAccessToken) bool {
	return len(suppliedCredentialTokens(rs, matches)) > 0
}

// suppliedCredentialTokens returns the same-named tokens in matches that could
// be the enrolled administrator-supplied credential. Tokens fullsend itself
// recorded (created, incoming, or outgoing) are historical managed credentials
// that stay listed after a managed-to-supplied enrollment and never stand in for
// the supplied one. A known SuppliedTokenID must match exactly; otherwise a known
// owner must match (or be unreported). With neither recorded, any unrecorded
// same-named token is conservatively treated as the supplied credential.
func suppliedCredentialTokens(rs rotationRoleState, matches []ProjectAccessToken) []ProjectAccessToken {
	var out []ProjectAccessToken
	for _, tok := range matches {
		if containsInt(rs.CreatedTokenIDs, tok.ID) || containsInt(rs.OutgoingIDs, tok.ID) || (rs.IncomingID != 0 && rs.IncomingID == tok.ID) {
			if tok.ID != rs.SuppliedTokenID {
				continue
			}
		}
		switch {
		case rs.SuppliedTokenID > 0:
			if tok.ID == rs.SuppliedTokenID {
				out = append(out, tok)
			}
		case rs.SuppliedUserID > 0 && tok.UserID > 0:
			if tok.UserID == rs.SuppliedUserID {
				out = append(out, tok)
			}
		default:
			out = append(out, tok)
		}
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

// hasPositiveInt reports whether values holds at least one positive ID. The
// CLI records only positive IDs from successful creates, so zero or negative
// entries in hand-edited state are not creation provenance.
func hasPositiveInt(values []int) bool {
	for _, value := range values {
		if value > 0 {
			return true
		}
	}
	return false
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
		applyAdministratorEnrollmentProof(&rep, reg, rotation)
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
// project access token snapshot to confirm.
func applyAdministratorEnrollmentProof(report *gitlabroles.Report, reg gitlabroles.Registry, rotation rotationStateFile) {
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
		if report.Roles[i].Lifecycle != gitlabroles.LifecycleUnverified {
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
		if ok && state.IncomingID == 0 && state.Phase == rotationPhaseIdle && state.DistributedAt != "" {
			report.Roles[i].Lifecycle = gitlabroles.LifecycleOK
		}
	}
	gitlabroles.RefreshLifecycleDiagnostics(report, oldLifecycleDiagnostics)
}
