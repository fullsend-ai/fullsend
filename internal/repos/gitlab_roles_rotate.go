package repos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	Mode     gitlabroles.Mode
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
}

// RoleRotateResult is the observable outcome of a rotation run.
// Token values are not included.
type RoleRotateResult struct {
	Mode            gitlabroles.Mode
	Report          gitlabroles.Report
	Rotated         []gitlabroles.Role
	Skipped         []gitlabroles.Role
	Reused          []gitlabroles.Role
	Failed          []RoleProvisionFailure
	Overlapping     []gitlabroles.Role
	RolledBack      []gitlabroles.Role
	Cleaned         []gitlabroles.Role
	InProgress      []gitlabroles.Role
	SharedPreserved bool
	DryRun          bool
	Diagnostics     []string
}

type rotationStateFile struct {
	Roles map[string]rotationRoleState `json:"roles"`
}

type rotationRoleState struct {
	Phase         string `json:"phase,omitempty"`
	Holder        string `json:"holder,omitempty"`
	LockUntil     string `json:"lock_until,omitempty"`
	IncomingID    int    `json:"incoming_id,omitempty"`
	OutgoingIDs   []int  `json:"outgoing_ids,omitempty"`
	DistributedAt string `json:"distributed_at,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	Error         string `json:"error,omitempty"`
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
func RotateGitLabRoleCredentials(ctx context.Context, cfg RoleRotateConfig) (RoleRotateResult, error) {
	result := RoleRotateResult{SharedPreserved: true, DryRun: cfg.DryRun}
	if cfg.Client == nil {
		return result, fmt.Errorf("GitLab role rotation requires a forge client")
	}
	operationLock := gitlabRoleOperationLock(cfg.Owner, cfg.Repo)
	operationLock.Lock()
	defer operationLock.Unlock()
	mode := cfg.Mode
	if mode == "" {
		mode = gitlabroles.ModeDisabled
	}
	if !mode.Valid() {
		return result, fmt.Errorf("%w: %q", gitlabroles.ErrInvalidMode, mode)
	}
	result.Mode = mode
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

	if mode.UsesSharedOnly() && !cfg.Force && len(cfg.Roles) == 0 {
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf(
			"mode=%s: role credentials unused; skip rotation (pass --rotate-gitlab-roles to refresh leftover secrets)", mode))
		present, presErr := gitLabRolePresence(ctx, cfg.Client, cfg.Owner, cfg.Repo, reg)
		if presErr != nil {
			return result, fmt.Errorf("reading GitLab role credential presence: %w", presErr)
		}
		result.Report = gitlabroles.Diagnose(mode, present, reg)
		result.Diagnostics = append(result.Diagnostics, result.Report.Diagnostics...)
		return result, nil
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
	result.Report = gitlabroles.DiagnoseLifecycle(mode, present, reg, snapshotsFrom(listed), now, lead)
	result.Diagnostics = append(result.Diagnostics, result.Report.Diagnostics...)
	sortRoleLists(&result)
	if secretLeakRotate(result) != "" {
		return RoleRotateResult{SharedPreserved: true}, fmt.Errorf("internal error: rotation result leaked a secret value")
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
	current := currentListed(matches)
	if !cfg.DryRun {
		cleaned := cleanupOutgoing(ctx, cfg, &rs, now, grace, listed)
		if cleaned {
			result.Cleaned = append(result.Cleaned, rec.Name)
			_ = mergeRoleState(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec.Name, holder, now, rs, &state)
			matches = tokensNamed(*listed, tokenName)
			current = currentListed(matches)
		}
	}

	rr := roleReportFrom(ctx, cfg, rec, matches, now, lead)
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
	if alreadyFresh || (recentlyDistributed(rs, now) && !gitlabroles.RoleDueForRotation(rr)) {
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
		rotateProvided(ctx, cfg, rec, secret, provided, now, matches, &rs, result)
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

	outgoing := activeIDsExcept(matches, tok.ID)
	if current.ID != 0 && current.ID != tok.ID {
		outgoing = uniqueInts(append(outgoing, current.ID))
	}
	rs.Phase = rotationPhaseDistributing
	rs.IncomingID = tok.ID
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

func rotateProvided(ctx context.Context, cfg RoleRotateConfig, rec gitlabroles.Registration, secret, provided string, now time.Time, matches []ProjectAccessToken, rs *rotationRoleState, result *RoleRotateResult) {
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
	if err := cfg.Client.CreateRepoSecret(ctx, cfg.Owner, cfg.Repo, secret, provided); err != nil {
		rs.Phase = rotationPhaseFailed
		rs.Error = "storing administrator-provided replacement failed"
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
		if err := cfg.Tokens.RevokeProjectAccessToken(ctx, cfg.Owner, cfg.Repo, id); err != nil {
			remaining = append(remaining, id)
			continue
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
	var file rotationStateFile
	if err := dec.Decode(&file); err != nil {
		return out, nil, fmt.Errorf("decode GitLab role rotation state: %w", err)
	}
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

func writeRotationState(ctx context.Context, client forge.Client, owner, repo string, file rotationStateFile) error {
	if file.Roles == nil {
		file.Roles = map[string]rotationRoleState{}
	}
	raw, err := json.Marshal(file)
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
	if _, exists := state.Roles[string(role)]; exists {
		return nil
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
	rep := gitlabroles.DiagnoseLifecycle(gitlabroles.ModeEnforced, present, cfg.Registry, snapshotsFrom(matches), now, lead)
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
	if n := check(string(result.Mode)); n != "" {
		return n
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
	mode, reg, present, err := LoadGitLabRoleState(ctx, client, owner, repo)
	if err != nil {
		return false
	}
	rep := gitlabroles.DiagnoseLifecycle(mode, present, reg, snapshotsFrom(tokens), now, gitlabroles.DefaultRotationLead)
	rotation, _, rotationErr := loadRotationState(ctx, client, owner, repo)
	if rotationErr == nil {
		applyAdministratorEnrollmentProof(&rep, reg, rotation)
	}
	status.GitLabRoleMode = string(rep.Mode)
	status.GitLabRolesReady = rep.Ready
	status.GitLabRolesPartial = rep.Partial
	status.GitLabRoleDiagnostics = rep.Diagnostics
	lifecycle := make(map[gitlabroles.Role]gitlabroles.LifecycleState, len(rep.Roles))
	for _, rr := range rep.Roles {
		lifecycle[rr.Name] = rr.Lifecycle
	}
	if gitLabRoleReadinessRequired(mode) {
		builtin := appendBuiltinRoleReadiness(status, present, reg, lifecycle)
		registered := appendRegisteredRoleReadiness(status, present, reg, lifecycle)
		status.GitLabRolesReady = status.GitLabRolesReady && builtin.Ready && registered.Ready
	}
	if !mode.RequiresRoleCredentials() {
		return false
	}
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
