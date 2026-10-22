package repos

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// ---------------------------------------------------------------------------
// GitLab trigger-only service account lifecycle (#8437)
// ---------------------------------------------------------------------------
//
// GitLab.com Free refuses per-user protected-branch merge grants, but it
// accepts a deploy key in a protected branch's allowed_to_push list and then
// lets the user who added the key start pipelines on that branch. This file
// converges a dedicated, trigger-only project service account that owns such
// a key and a pipeline trigger token, using only project-scoped authority:
//
//  1. Create the account (its display name carries a random nonce that is
//     recorded before the create, so an interrupted create is recognised).
//  2. Temporarily elevate it to Maintainer and mint a short-lived bootstrap
//     PAT, because only a Maintainer can add a deploy key or trigger token
//     and GitLab records the calling user as their owner.
//  3. Add a push-capable deploy key with the bootstrap PAT. The key pair is
//     generated in memory, its fingerprint is recorded before the create,
//     and the private key is discarded: it is never needed again.
//  4. Grant that key push on the branch's exact-name protected-branch rule
//     with the installer's authority. A wildcard-only branch defers.
//  5. Create the pipeline trigger token with the bootstrap PAT. A trigger
//     that is already published is revoked before the account is elevated.
//  6. Revoke the bootstrap PAT and return the account to Developer, then
//     confirm its effective access (which includes inherited access) is
//     exactly Developer.
//  7. Only then hand the new trigger value to the caller's publisher, so no
//     usable trigger token exists while the account is a Maintainer.
//
// Steps 2-7 are the Maintainer bootstrap. Creating a pipeline schedule needs no
// elevation, so the schedule migration of #8508 (gitlab_trigger_schedules.go)
// mints its own short-lived PAT while the account stays at Developer, creates
// the schedules with it, then revokes it and confirms Developer access again.
//
// Every step records its intent or result in a protected, unmasked ledger
// (forge.VarGitLabTriggerAccount) before the next mutating call, so an
// interrupted install or uninstall leaves a retriable obligation. Removal
// deletes only resources whose recorded ID, nonce-derived name and, for the
// key, fingerprint all match; anything else fails closed rather than being
// removed by inference. The account must never be the Poller role identity:
// a PAT on an identity with a deploy-key grant could write the branch.
//
// The dispatch route that uses these resources is selected only while the
// activation gate passes and the activation is recorded (#8438). The account
// then also owns the managed polling schedules (gitlab_trigger_schedules.go,
// #8508). Every public entry point requires the project lease.

const (
	gitLabTriggerAccountStateVersion = 1
	gitLabTriggerAccountNamePrefix   = "fullsend-trigger-"
	gitLabTriggerBootstrapPATPrefix  = "fullsend-trigger-bootstrap-"

	gitLabTriggerPhaseBootstrapping = "bootstrapping"
	gitLabTriggerPhaseReady         = "ready"
	gitLabTriggerPhaseRotating      = "rotating"
	gitLabTriggerPhaseRemoving      = "removing"
)

// gitLabTriggerBootstrapPATScopes are the scopes the temporary bootstrap PAT
// needs to add a deploy key and a pipeline trigger token, and the temporary
// schedule PAT needs to create pipeline schedules as the account.
var gitLabTriggerBootstrapPATScopes = []string{"api"}

// GitLabDeployKey is the non-secret metadata of a project deploy key.
type GitLabDeployKey struct {
	ID                int
	Title             string
	FingerprintSHA256 string
	// CanPush reports whether GitLab lets the key push. A key that cannot push
	// does not name the trigger account in the branch's push list, so it is
	// never reported ready.
	CanPush bool
}

// GitLabTriggerAccountAPI is the installer-authority (project Maintainer or
// Owner) subset of the GitLab API that the trigger-account lifecycle uses.
type GitLabTriggerAccountAPI interface {
	GetAuthenticatedUserID(ctx context.Context) (int, error)
	ListProjectServiceAccounts(ctx context.Context, owner, repo string) ([]GitLabServiceAccount, error)
	CreateProjectServiceAccount(ctx context.Context, owner, repo, name string) (GitLabServiceAccount, error)
	DeleteProjectServiceAccount(ctx context.Context, owner, repo string, userID int) error
	// GetProjectMemberAccessLevel returns forge.ErrNotFound (wrapped) when the
	// user is not a member.
	GetProjectMemberAccessLevel(ctx context.Context, owner, repo string, userID int64) (int, error)
	AddProjectMember(ctx context.Context, owner, repo string, userID int64, accessLevel int) error
	UpdateProjectMemberAccessLevel(ctx context.Context, owner, repo string, userID int64, accessLevel int) error
	CreateServiceAccountPAT(ctx context.Context, owner, repo string, userID int, name string, scopes []string, expiresAt string) (*ProjectAccessToken, error)
	ListServiceAccountPATs(ctx context.Context, owner, repo string, userID int) ([]ProjectAccessToken, error)
	RevokeServiceAccountPAT(ctx context.Context, owner, repo string, userID, tokenID int) error
	HasExactProtectedBranchRule(ctx context.Context, owner, repo, branch string) (bool, error)
	GrantProtectedBranchPushDeployKey(ctx context.Context, owner, repo, branch string, deployKeyID int) error
	ProtectedBranchPushDeployKeyEntryID(ctx context.Context, owner, repo, branch string, deployKeyID int) (int, error)
	RevokeProtectedBranchPushDeployKeyEntry(ctx context.Context, owner, repo, branch string, entryID, deployKeyID int) error
	ListProjectDeployKeys(ctx context.Context, owner, repo string) ([]GitLabDeployKey, error)
	DeleteProjectDeployKey(ctx context.Context, owner, repo string, keyID int) error
	ListPipelineTriggerTokens(ctx context.Context, owner, repo string) ([]forge.PipelineTriggerToken, error)
	RevokePipelineTriggerToken(ctx context.Context, owner, repo string, tokenID int64) error
	// AccountSSHKeyIDs returns the usable SSH authentication keys registered
	// on the account. The trigger account owns none: a key outlives PAT
	// revocation and would carry the account's protected-branch push grant.
	AccountSSHKeyIDs(ctx context.Context, userID int) ([]int, error)
	// AccountActiveJobIDs returns the unfinished project jobs that run as the
	// account (or as a user GitLab did not report). A job's CI_JOB_TOKEN keeps
	// the account's project role until the job ends, so revoking a trigger
	// token does not end its authority.
	AccountActiveJobIDs(ctx context.Context, owner, repo string, userID int) ([]int, error)
	// AccountScheduleIDs returns the pipeline schedules whose reported owner is
	// exactly the account. Schedules with no reported owner are not included.
	AccountScheduleIDs(ctx context.Context, owner, repo string, userID int) ([]int, error)
	// UnknownOwnerScheduleIDs returns the pipeline schedules GitLab reported
	// without an owner. Whose authority they carry is unknown.
	UnknownOwnerScheduleIDs(ctx context.Context, owner, repo string) ([]int, error)
}

// GitLabTriggerBootstrapAPI is the subset of the GitLab API called with the
// trigger account's temporary bootstrap PAT, so GitLab records the trigger
// account as the owner of what it creates.
type GitLabTriggerBootstrapAPI interface {
	CreateProjectDeployKey(ctx context.Context, owner, repo, title, publicKey string, canPush bool) (GitLabDeployKey, error)
	CreatePipelineTriggerToken(ctx context.Context, owner, repo, description string) (*forge.PipelineTriggerToken, error)
	// CreatePipelineSchedule creates a pipeline schedule without pipeline
	// variables on ref and returns its ID. GitLab records the caller, the
	// trigger account, as the schedule's owner. With active false the create
	// request itself asks for a disabled schedule, so it is never active.
	CreatePipelineSchedule(ctx context.Context, owner, repo, ref, description, cron string, active bool) (int64, error)
}

// GitLabTriggerAccountOptions configures one trigger-account operation.
type GitLabTriggerAccountOptions struct {
	Owner, Repo string
	// Branch is the exact protected branch the trigger identity may start
	// pipelines on. Wildcard patterns are rejected.
	Branch string
	// PollerUserIDs are every account that holds or may hold the Poller role
	// credential. The trigger account must be none of them.
	PollerUserIDs []int
	// PollerInventoryVerified must be true: the caller asserts PollerUserIDs
	// is the complete, authoritative Poller inventory. It lets a verified
	// empty inventory be told apart from an omitted one, and every operation
	// rejects an unverified inventory before touching GitLab.
	PollerInventoryVerified bool
	// Lease proves the caller holds the project lease
	// (LockGitLabProjectLease). Every public entry point refuses to run
	// without it, so two overlapping runs cannot write the shared ledger from
	// stale snapshots or elevate the same account concurrently.
	Lease *GitLabProjectLease
	API   GitLabTriggerAccountAPI
	// Bootstrap returns a client authenticated with the bootstrap PAT.
	Bootstrap func(token string) (GitLabTriggerBootstrapAPI, error)
	// PublishTriggerToken stores the new trigger token value for the
	// dispatch route. It is called once per created token.
	PublishTriggerToken func(ctx context.Context, tokenID int64, token string) error
	// UnpublishTriggerToken removes the published value. It runs before every
	// revocation of a recorded or nonce-owned trigger token, so the sink never
	// keeps a value for a token that is gone, and it must succeed when nothing
	// is published. Nil means the sink holds nothing to remove.
	UnpublishTriggerToken func(ctx context.Context) error
	// State stores the ledger as a protected CI/CD variable.
	State forge.Client
	// EnforceWebhookSafety requires the same project-wide trigger safety
	// invariants as the webhook before publishing or retaining this account's
	// bearer. Installer entry points set it; containment and tests can opt in
	// without requiring unrelated scaffold state.
	EnforceWebhookSafety bool
	// DeferRetirement keeps previously published generations usable until the
	// installer has repointed and verified the managed webhook. The installer
	// calls RetireGitLabTriggerAccount after that cutover.
	DeferRetirement bool
	// ReactivateSchedules mirrors --reactivate-schedules: a disabled managed
	// polling schedule the trigger account owns is replaced by an active one.
	// The installer cannot edit a schedule the account owns, so the install's
	// schedule repair, which runs first, leaves those for this stage.
	ReactivateSchedules bool
	// Now and Rand are injectable for tests.
	Now  func() time.Time
	Rand io.Reader
}

// GitLabTriggerAccountResult reports the converged resources. Deferred is a
// non-empty reason when a required capability or project setting is missing,
// in which case no resource was created by this call.
type GitLabTriggerAccountResult struct {
	Ready          bool
	Deferred       string
	AccountID      int
	DeployKeyID    int
	GrantEntryID   int
	TriggerTokenID int64
}

// gitLabTriggerAccountState is the ledger document. It never stores token
// values or private keys.
type gitLabTriggerAccountState struct {
	Version   int    `json:"version"`
	Phase     string `json:"phase"`
	Branch    string `json:"branch"`
	Nonce     string `json:"nonce"`
	AccountID int    `json:"account_id,omitempty"`
	Elevated  bool   `json:"elevated,omitempty"`
	PATID     int    `json:"pat_id,omitempty"`
	// PATPending is written before the bootstrap PAT is created and kept
	// until every PAT carrying the bootstrap name is confirmed revoked, so a
	// lost create response cannot leave an active PAT with no ledger marker.
	PATPending           bool   `json:"pat_pending,omitempty"`
	DeployKeyFingerprint string `json:"deploy_key_fingerprint,omitempty"`
	DeployKeyID          int    `json:"deploy_key_id,omitempty"`
	GrantEntryID         int    `json:"grant_entry_id,omitempty"`
	TriggerTokenID       int64  `json:"trigger_token_id,omitempty"`
	// TriggerTokenDigest binds the published variable to the trigger ID without
	// recording its bearer value in the unmasked ledger.
	TriggerTokenDigest string `json:"trigger_token_digest,omitempty"`
	TriggerPublished   bool   `json:"trigger_published,omitempty"`
	// PublicationPending distinguishes a saved new digest from a completed
	// replacement. While it is true, the retired bearer may still occupy the
	// sink (or the sink may be empty between clear and publish).
	PublicationPending bool `json:"publication_pending,omitempty"`
	// PublishStarted is written before the trigger value is stored in the
	// token sink and kept until the sink is removed. From then on the value
	// may have left this process, so the generation is never raised to
	// Maintainer again: repair and rotation start a fresh generation instead.
	PublishStarted bool `json:"publish_started,omitempty"`
	// Retired lists earlier generations whose trigger credential was already
	// distributed. Each keeps its own resource IDs at Developer until the
	// current generation is published and the retirement completes; install
	// and uninstall retry it. Retired entries never nest.
	Retired []gitLabTriggerAccountState `json:"retired,omitempty"`
}

func (s gitLabTriggerAccountState) accountName() string {
	return gitLabTriggerAccountNamePrefix + s.Nonce
}

func (s gitLabTriggerAccountState) patName() string {
	return gitLabTriggerBootstrapPATPrefix + s.Nonce
}

// deployKeyTitle and triggerDescription share the account name, so every
// resource the lifecycle creates is attributable to one recorded nonce.
func (s gitLabTriggerAccountState) deployKeyTitle() string { return s.accountName() }

func (s gitLabTriggerAccountState) triggerDescription() string { return s.accountName() }

// sameDeployKeyFingerprint compares a recorded fingerprint with one GitLab
// reported. The ledger stores the "SHA256:<digest>" form that ssh produces,
// while GitLab's fingerprint_sha256 is the bare digest, so the prefix is
// ignored on both sides. An empty fingerprint never matches.
func sameDeployKeyFingerprint(recorded, reported string) bool {
	recorded = strings.TrimPrefix(recorded, "SHA256:")
	reported = strings.TrimPrefix(reported, "SHA256:")
	return recorded != "" && recorded == reported
}

func validGitLabTriggerNonce(nonce string) bool {
	if len(nonce) != 16 {
		return false
	}
	_, err := hex.DecodeString(nonce)
	return err == nil && strings.ToLower(nonce) == nonce
}

func decodeGitLabTriggerAccountState(raw string) (gitLabTriggerAccountState, error) {
	name := forge.VarGitLabTriggerAccount
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var st gitLabTriggerAccountState
	if err := dec.Decode(&st); err != nil {
		return gitLabTriggerAccountState{}, fmt.Errorf("decode %s: %w", name, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: unexpected content after the JSON document", name)
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: %w", name, err)
	}
	if st.Version != gitLabTriggerAccountStateVersion {
		return gitLabTriggerAccountState{}, fmt.Errorf("unsupported %s version %d; use a compatible CLI", name, st.Version)
	}
	switch st.Phase {
	case gitLabTriggerPhaseBootstrapping, gitLabTriggerPhaseReady, gitLabTriggerPhaseRotating, gitLabTriggerPhaseRemoving:
	default:
		return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: unknown phase %q", name, st.Phase)
	}
	if !validGitLabTriggerNonce(st.Nonce) {
		return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: nonce must be 16 lowercase hex characters", name)
	}
	if st.Branch == "" || strings.Contains(st.Branch, "*") {
		return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: branch must be an exact branch name", name)
	}
	if st.AccountID < 0 || st.PATID < 0 || st.DeployKeyID < 0 || st.GrantEntryID < 0 || st.TriggerTokenID < 0 {
		return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: resource IDs must not be negative", name)
	}
	if st.AccountID == 0 && (st.Elevated || st.PATID != 0 || st.PATPending) {
		return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: account-scoped entries recorded without an account", name)
	}
	if st.DeployKeyID != 0 && st.DeployKeyFingerprint == "" {
		return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: deploy key recorded without its fingerprint", name)
	}
	if st.GrantEntryID != 0 && st.DeployKeyID == 0 {
		return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: branch grant recorded without its deploy key", name)
	}
	if st.TriggerPublished && st.TriggerTokenID == 0 {
		return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: published trigger recorded without its ID", name)
	}
	if st.TriggerPublished && !st.PublishStarted {
		return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: published trigger recorded without the publication marker", name)
	}
	if st.TriggerTokenDigest != "" && !validTriggerTokenDigest(st.TriggerTokenDigest) {
		return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: trigger token digest must be 64 lowercase hex characters", name)
	}
	if st.PublicationPending && (!st.PublishStarted || st.TriggerPublished || st.TriggerTokenID == 0 || st.TriggerTokenDigest == "") {
		return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: pending publication lacks an unpublished trigger and bearer digest", name)
	}
	seen := map[string]bool{st.Nonce: true}
	for i, old := range st.Retired {
		switch {
		case len(old.Retired) != 0:
			return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: retired generation %d nests another generation", name, i)
		case !validGitLabTriggerNonce(old.Nonce) || seen[old.Nonce]:
			return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: retired generation %d has an invalid or duplicate nonce", name, i)
		case old.Branch == "" || strings.Contains(old.Branch, "*"):
			return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: retired generation %d branch must be an exact branch name", name, i)
		case old.AccountID < 0 || old.PATID < 0 || old.DeployKeyID < 0 || old.GrantEntryID < 0 || old.TriggerTokenID < 0:
			return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: retired generation %d resource IDs must not be negative", name, i)
		case old.DeployKeyID != 0 && old.DeployKeyFingerprint == "", old.GrantEntryID != 0 && old.DeployKeyID == 0:
			return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: retired generation %d records a grant or key without its key identity", name, i)
		case old.AccountID == 0 && (old.Elevated || old.PATID != 0 || old.PATPending):
			return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: retired generation %d records account-scoped entries without an account", name, i)
		case old.TriggerTokenDigest != "" && !validTriggerTokenDigest(old.TriggerTokenDigest):
			return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: retired generation %d has an invalid trigger token digest", name, i)
		case old.PublicationPending:
			return gitLabTriggerAccountState{}, fmt.Errorf("invalid %s: retired generation %d retains a pending publication", name, i)
		}
		seen[old.Nonce] = true
	}
	return st, nil
}

func validTriggerTokenDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	b, err := hex.DecodeString(digest)
	return err == nil && len(b) == sha256.Size && digest == strings.ToLower(digest)
}

func triggerTokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// gitLabTriggerAccountOp carries one lifecycle run.
type gitLabTriggerAccountOp struct {
	opts GitLabTriggerAccountOptions
	st   gitLabTriggerAccountState
	// generationIDs names all recorded trigger accounts when processing a
	// current/retired hand-back sequence.
	generationIDs map[int64]bool
	// exists reports whether the ledger variable is present.
	exists bool
	// retired marks the working copy of a retired generation: it never
	// touches the token sink, which belongs to the current generation.
	retired bool
	// persist replaces the ledger write for a retired generation's copy.
	persist func(ctx context.Context) error
	// uninstalling marks a removal run: the account's managed schedules are
	// deleted without an installer-owned copy, because uninstall removes
	// every fullsend schedule.
	uninstalling bool
}

// errTriggerAccountUnleased refuses a lifecycle run whose caller does not hold
// the project lease.
var errTriggerAccountUnleased = errors.New("GitLab trigger account refused: the caller does not hold the project lease (LockGitLabProjectLease)")

// triggerDeferral is a safe, recoverable stop: nothing unsafe was done, the
// ledger keeps every obligation, and a later run can continue.
type triggerDeferral struct{ reason string }

func (d *triggerDeferral) Error() string { return d.reason }

// branchChangeReason is the documented deferral when the protected branch is
// not the one the ledger recorded. fullsend does not migrate grants between
// branches: uninstall removes the recorded resources on the recorded branch,
// then install creates them on the new one.
func branchChangeReason(recorded, requested string) string {
	return fmt.Sprintf("trigger account resources are recorded for protected branch %q, not %q; fullsend does not move grants between branches. Run repos uninstall to remove them from %q, then run install again", recorded, requested, recorded)
}

func requireGitLabTriggerLease(opts GitLabTriggerAccountOptions) error {
	if !opts.Lease.holds(opts.Owner, opts.Repo) {
		return errTriggerAccountUnleased
	}
	return nil
}

func newGitLabTriggerAccountOp(ctx context.Context, opts GitLabTriggerAccountOptions) (*gitLabTriggerAccountOp, error) {
	if opts.API == nil || opts.State == nil {
		return nil, errors.New("GitLab trigger account: API and state store are required")
	}
	if !opts.PollerInventoryVerified {
		return nil, errors.New("GitLab trigger account: a verified Poller identity inventory is required so the trigger account can be kept separate from the Poller credential")
	}
	return loadGitLabTriggerAccountOp(ctx, opts)
}

// loadGitLabTriggerAccountOp reads the ledger without requiring the Poller
// inventory, which containment-only recovery must not depend on.
func loadGitLabTriggerAccountOp(ctx context.Context, opts GitLabTriggerAccountOptions) (*gitLabTriggerAccountOp, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Rand == nil {
		opts.Rand = rand.Reader
	}
	raw, exists, err := opts.State.GetRepoVariable(ctx, opts.Owner, opts.Repo, forge.VarGitLabTriggerAccount)
	if err != nil {
		return nil, safeAPIError("reading "+forge.VarGitLabTriggerAccount, err)
	}
	op := &gitLabTriggerAccountOp{opts: opts, exists: exists}
	if !exists {
		return op, nil
	}
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("invalid %s: the ledger is blank; it must not be emptied by hand", forge.VarGitLabTriggerAccount)
	}
	st, err := decodeGitLabTriggerAccountState(raw)
	if err != nil {
		return nil, err
	}
	op.st = st
	return op, nil
}

func (op *gitLabTriggerAccountOp) save(ctx context.Context) error {
	op.st.Version = gitLabTriggerAccountStateVersion
	if op.persist != nil {
		return op.persist(ctx)
	}
	raw, err := json.Marshal(op.st)
	if err != nil {
		return err
	}
	if err := op.opts.State.UpdateCIVariable(ctx, op.opts.Owner, op.opts.Repo, forge.VarGitLabTriggerAccount, string(raw), true); err != nil {
		return safeAPIError("recording "+forge.VarGitLabTriggerAccount, err)
	}
	op.exists = true
	return nil
}

func (op *gitLabTriggerAccountOp) result(ready bool) GitLabTriggerAccountResult {
	return GitLabTriggerAccountResult{
		Ready:          ready,
		AccountID:      op.st.AccountID,
		DeployKeyID:    op.st.DeployKeyID,
		GrantEntryID:   op.st.GrantEntryID,
		TriggerTokenID: op.st.TriggerTokenID,
	}
}

// ConvergeGitLabTriggerAccount creates or repairs the trigger-only service
// account and its protected-ref resources. It is safe to re-run after any
// interruption: the ledger records each obligation before the matching
// GitLab call. The bootstrap PAT is revoked and the account returned to
// Developer before the result reports Ready.
func ConvergeGitLabTriggerAccount(ctx context.Context, opts GitLabTriggerAccountOptions) (GitLabTriggerAccountResult, error) {
	if err := requireGitLabTriggerLease(opts); err != nil {
		return GitLabTriggerAccountResult{}, err
	}
	return convergeGitLabTriggerAccountLocked(ctx, opts)
}

// convergeGitLabTriggerAccountLocked is ConvergeGitLabTriggerAccount for a
// caller that already verified the lease, such as rotation.
func convergeGitLabTriggerAccountLocked(ctx context.Context, opts GitLabTriggerAccountOptions) (GitLabTriggerAccountResult, error) {
	if opts.Branch == "" || strings.Contains(opts.Branch, "*") {
		return GitLabTriggerAccountResult{}, fmt.Errorf("GitLab trigger account: branch %q must be an exact branch name", opts.Branch)
	}
	if opts.Bootstrap == nil || opts.PublishTriggerToken == nil {
		return GitLabTriggerAccountResult{}, errors.New("GitLab trigger account: bootstrap client factory and trigger publisher are required")
	}
	op, err := newGitLabTriggerAccountOp(ctx, opts)
	if err != nil {
		return GitLabTriggerAccountResult{}, err
	}
	if op.exists {
		if op.st.Phase == gitLabTriggerPhaseRemoving {
			return GitLabTriggerAccountResult{}, errors.New("GitLab trigger account: an uninstall is still in progress; re-run uninstall to finish it before installing again")
		}
	}
	// Any lifecycle run withdraws the dispatch activation first; the caller
	// records it again only after the activation gate passes on the result.
	if err := op.clearDispatchActive(ctx); err != nil {
		return GitLabTriggerAccountResult{}, err
	}
	// Contain an unfinished bootstrap before any deferral or inventory read
	// can return early and leave the account elevated.
	if err := op.containBootstrap(ctx); err != nil {
		return GitLabTriggerAccountResult{}, err
	}
	// A published credential is contained before any deferral, inventory read
	// or replacement creation can return or fail: the sink is checked across
	// every publication-owning generation (an interrupted rotation can leave a
	// retired generation's live token in the sink while the current one never
	// published) and checks the live override role itself, so a failed role
	// read is contained too. The error is returned before anything else runs.
	if err := op.containUnsafePublishedSink(ctx); err != nil {
		return GitLabTriggerAccountResult{}, err
	}
	if reason, err := op.webhookSafetyDeferral(ctx); reason != "" || err != nil {
		return GitLabTriggerAccountResult{Deferred: reason}, err
	}
	if op.exists && op.st.Branch != opts.Branch {
		// Fail closed with a recoverable path: the ledger keeps recording the
		// branch every grant was made on, so uninstall removes them exactly,
		// and nothing is granted on the new branch.
		res := op.result(false)
		res.Deferred = branchChangeReason(op.st.Branch, opts.Branch)
		return res, nil
	}
	if reason, err := op.deferral(ctx); err != nil || reason != "" {
		return GitLabTriggerAccountResult{Deferred: reason}, err
	}
	if !op.exists {
		nonce := make([]byte, 8)
		if _, err := io.ReadFull(op.opts.Rand, nonce); err != nil {
			return GitLabTriggerAccountResult{}, fmt.Errorf("GitLab trigger account: generating nonce: %w", err)
		}
		op.st = gitLabTriggerAccountState{Phase: gitLabTriggerPhaseBootstrapping, Branch: opts.Branch, Nonce: hex.EncodeToString(nonce)}
		if err := op.save(ctx); err != nil {
			return GitLabTriggerAccountResult{}, err
		}
	}
	if err := op.converge(ctx); err != nil {
		var d *triggerDeferral
		if errors.As(err, &d) {
			res := op.result(false)
			res.Deferred = d.reason
			return res, nil
		}
		return op.result(false), err
	}
	return op.result(true), nil
}

// gitLabDispatchActivation is the activation record stored in
// forge.VarGitLabDispatchActivation. It binds to the ledger generation it was
// written for: the scaffold honors it only while the ledger is ready and every
// field below still equals the ledger's, so an older CLI that rotates,
// repairs or removes the account (and does not know this variable) leaves it
// stale, never active. The activation is not part of the ledger because older
// decoders reject unknown ledger fields before they read the version.
type gitLabDispatchActivation struct {
	Version        int    `json:"version"`
	Nonce          string `json:"nonce"`
	AccountID      int    `json:"account_id"`
	DeployKeyID    int    `json:"deploy_key_id"`
	GrantEntryID   int    `json:"grant_entry_id"`
	TriggerTokenID int64  `json:"trigger_token_id"`
}

const gitLabDispatchActivationVersion = 1

// activationFor derives the activation record that matches the working copy of
// the ledger.
func (op *gitLabTriggerAccountOp) activationFor() gitLabDispatchActivation {
	return gitLabDispatchActivation{
		Version:        gitLabDispatchActivationVersion,
		Nonce:          op.st.Nonce,
		AccountID:      op.st.AccountID,
		DeployKeyID:    op.st.DeployKeyID,
		GrantEntryID:   op.st.GrantEntryID,
		TriggerTokenID: op.st.TriggerTokenID,
	}
}

// clearDispatchActive withdraws the recorded dispatch activation, if any. It
// deletes the activation variable even when no ledger exists, so an orphaned
// record never outlives the ledger it was bound to. The ledger itself is not
// rewritten: older decoders keep reading it unchanged.
func (op *gitLabTriggerAccountOp) clearDispatchActive(ctx context.Context) error {
	if op.opts.State == nil {
		return nil
	}
	return withdrawGitLabDispatchActivation(ctx, op.opts.State, op.opts.Owner, op.opts.Repo)
}

// withdrawGitLabDispatchActivation deletes the activation variable. A missing
// variable is success.
func withdrawGitLabDispatchActivation(ctx context.Context, client forge.Client, owner, repo string) error {
	if err := client.DeleteRepoVariable(ctx, owner, repo, forge.VarGitLabDispatchActivation); err != nil && !forge.IsNotFound(err) {
		return safeAPIError("withdrawing "+forge.VarGitLabDispatchActivation, err)
	}
	return nil
}

// SetGitLabTriggerDispatchActive records (or withdraws) the dispatch
// activation. Callers pass active=true only when
// CheckGitLabTriggerDispatchGate returned Ready for the same project state.
// Activation requires a ready ledger and is written as a record bound to that
// ledger generation; withdrawing deletes the record and is a no-op when none
// exists.
func SetGitLabTriggerDispatchActive(ctx context.Context, opts GitLabTriggerAccountOptions, active bool) error {
	if err := requireGitLabTriggerLease(opts); err != nil {
		return err
	}
	if opts.State == nil {
		return errors.New("GitLab trigger account: a state store is required")
	}
	if !active {
		return withdrawGitLabDispatchActivation(ctx, opts.State, opts.Owner, opts.Repo)
	}
	op, err := loadGitLabTriggerAccountOp(ctx, opts)
	if err != nil {
		return err
	}
	if !op.exists {
		return errors.New("GitLab trigger account: dispatch cannot be activated without a ledger")
	}
	if op.st.Phase != gitLabTriggerPhaseReady {
		return fmt.Errorf("GitLab trigger account: dispatch cannot be activated while the ledger phase is %q", op.st.Phase)
	}
	if op.st.AccountID <= 0 || op.st.TriggerTokenID <= 0 || !op.st.TriggerPublished {
		return errors.New("GitLab trigger account: dispatch cannot be activated before the account and its published trigger token are recorded")
	}
	raw, err := json.Marshal(op.activationFor())
	if err != nil {
		return err
	}
	// Written unmasked and protected: pipelines on the protected default branch
	// read it, and it holds no secret. If the write fails the activation is
	// withdrawn as far as possible so a half-applied update never stays active.
	if err := opts.State.UpdateCIVariable(ctx, opts.Owner, opts.Repo, forge.VarGitLabDispatchActivation, string(raw), true); err != nil {
		return errors.Join(safeAPIError("recording "+forge.VarGitLabDispatchActivation, err), withdrawGitLabDispatchActivation(ctx, opts.State, opts.Owner, opts.Repo))
	}
	return nil
}

// containBootstrap revokes a recorded bootstrap PAT and returns an elevated
// account to Developer before anything else runs. When containment cannot
// complete it fails with an explicit error instead of letting the run report
// a deferral or a different failure while the account stays elevated.
func (op *gitLabTriggerAccountOp) containBootstrap(ctx context.Context) error {
	if !op.exists || (!op.st.Elevated && op.st.PATID == 0 && !op.st.PATPending) {
		return nil
	}
	_, found, err := op.findOwnedAccount(ctx)
	if err != nil {
		return fmt.Errorf("GitLab trigger account: an unfinished bootstrap could not be contained: %w", err)
	}
	if !found {
		// A deleted account holds no token and has no role left to restore.
		op.st.Elevated, op.st.PATID, op.st.PATPending = false, 0, false
		return op.save(ctx)
	}
	if err := op.endBootstrap(ctx); err != nil {
		return fmt.Errorf("GitLab trigger account: an unfinished bootstrap could not be contained: %w", err)
	}
	return nil
}

// overrideRoleDeferral returns a reason when the live pipeline-variable
// override role is not no_one_allowed. A live trigger credential is only safe
// to publish while overrides are disabled, so a trigger request cannot inject
// variables. The lifecycle verifies the live setting itself rather than relying
// on its caller having applied the restriction.
func (op *gitLabTriggerAccountOp) overrideRoleDeferral(ctx context.Context) (string, error) {
	role, err := op.opts.State.GetPipelineVariablesMinimumOverrideRole(ctx, op.opts.Owner, op.opts.Repo)
	if err != nil {
		return "", safeAPIError("reading the pipeline-variable override role", err)
	}
	if role != forge.PipelineVarOverrideNoOneAllowed {
		return fmt.Sprintf("ci_pipeline_variables_minimum_override_role is %s, not no_one_allowed; fullsend does not publish a trigger credential while pipeline variable overrides are allowed. Stop legacy polling, migrate schedule variables, and apply the restriction to enable the trigger account", displayPipelineVarRole(role)), nil
	}
	return "", nil
}

// deferral returns a reason when the project cannot host the trigger account.
func (op *gitLabTriggerAccountOp) deferral(ctx context.Context) (string, error) {
	o, r, b := op.opts.Owner, op.opts.Repo, op.opts.Branch
	exact, err := op.opts.API.HasExactProtectedBranchRule(ctx, o, r, b)
	if err != nil {
		return "", safeAPIError("reading the exact protected-branch rule for "+b, err)
	}
	if !exact {
		return fmt.Sprintf("branch %s has no exact-name protected-branch rule; fullsend does not change wildcard rules, which also cover other branches. Protect %s by its exact name to enable the trigger account", b, b), nil
	}
	if reason, err := op.overrideRoleDeferral(ctx); err != nil || reason != "" {
		return reason, err
	}
	if _, err := op.opts.API.ListProjectServiceAccounts(ctx, o, r); err != nil {
		if serviceAccountsUnavailable(err) {
			return "project service accounts are unavailable to the installer on this GitLab instance or tier", nil
		}
		return "", safeAPIError("listing project service accounts", err)
	}
	return "", nil
}

func (op *gitLabTriggerAccountOp) converge(ctx context.Context) error {
	if err := op.checkSinkFree(ctx); err != nil {
		return err
	}
	if err := op.ensureAccount(ctx); err != nil {
		return err
	}
	for _, id := range op.opts.PollerUserIDs {
		if id == op.st.AccountID {
			return fmt.Errorf("GitLab trigger account %d is also a Poller role identity; the trigger identity must stay separate from the Poller credential", id)
		}
	}
	// An unsafe published sink is contained before any unrelated readiness
	// check can return: the account is proven to be the recorded, nonce-named
	// one above, and revokeOwnedTriggers revokes only a trigger that carries the
	// recorded description and is owned by it. Foreign-resource and deploy-key
	// failures therefore cannot leave an exposed trigger usable.
	sinkOK, sinkContained := false, false
	if op.st.PublishStarted {
		var err error
		if sinkOK, err = op.sinkHealthy(ctx); err != nil {
			return err
		}
		if !sinkOK {
			if err := op.containUnsafeSink(ctx); err != nil {
				return err
			}
			sinkContained = true
		}
	}
	if err := op.checkForeignPATs(ctx); err != nil {
		return err
	}
	if err := op.checkForeignResources(ctx); err != nil {
		return err
	}
	keyOK, err := op.deployKeyPresent(ctx)
	if err != nil {
		return err
	}
	triggerOK, err := op.triggerPresent(ctx)
	if err != nil {
		return err
	}
	if op.st.PublishStarted {
		if op.st.PublicationPending && !op.st.TriggerPublished && sinkOK && triggerOK && op.st.TriggerTokenDigest != "" {
			// The publish call can succeed and the following ledger save can
			// fail or be interrupted. The protected sink's value and the owned
			// trigger prove publication completed; finish the recorded step.
			op.st.TriggerPublished = true
			op.st.PublicationPending = false
			if err := op.save(ctx); err != nil {
				return err
			}
		}
		// The trigger value left this process, and a trigger request GitLab
		// already accepted can still become a job after any snapshot, so this
		// identity is never raised to Maintainer again. Anything that needs
		// elevation, or a sink that is missing or unsafe (its value is not
		// recoverable and may be exposed), starts a fresh generation; the old
		// one stays at Developer until the new one is published.
		sinkOK = sinkOK && triggerOK
		if !keyOK || !triggerOK || !sinkOK || op.st.TriggerTokenDigest == "" || !op.st.TriggerPublished {
			if !sinkOK && !sinkContained {
				// The trigger is missing, or the sink was not found unsafe
				// above: remove the variable fullsend owns and revoke its
				// ownership-verified trigger before the replacement exists.
				if err := op.containUnsafeSink(ctx); err != nil {
					return err
				}
			}
			if err := op.startFreshGeneration(ctx); err != nil {
				return err
			}
			return op.converge(ctx)
		}
	}
	if !keyOK || !triggerOK || op.st.PATID != 0 || op.st.PATPending || op.st.Elevated {
		if err := op.bootstrap(ctx, keyOK, triggerOK); err != nil {
			return err
		}
	} else if err := op.ensureMembership(ctx, forge.GitLabAccessLevelDeveloper); err != nil {
		return err
	}
	if err := op.ensureGrant(ctx); err != nil {
		return err
	}
	op.st.Phase = gitLabTriggerPhaseReady
	if err := op.save(ctx); err != nil {
		return err
	}
	if op.opts.DeferRetirement {
		return nil
	}
	return op.retirePrevious(ctx)
}

// sinkOwned reports whether the ledger proves fullsend wrote the token sink:
// any generation started publishing into it.
func (op *gitLabTriggerAccountOp) sinkOwned() bool {
	if op.st.PublishStarted {
		return true
	}
	for _, old := range op.st.Retired {
		if old.PublishStarted {
			return true
		}
	}
	return false
}

// checkSinkFree refuses to continue when a variable named like the token sink
// exists although the ledger never recorded writing it: it belongs to an
// administrator and is neither overwritten nor deleted.
func (op *gitLabTriggerAccountOp) checkSinkFree(ctx context.Context) error {
	if op.sinkOwned() {
		return nil
	}
	exists, err := op.opts.State.RepoSecretExists(ctx, op.opts.Owner, op.opts.Repo, forge.SecretGitLabTriggerAccountToken)
	if err != nil {
		return safeAPIError("checking "+forge.SecretGitLabTriggerAccountToken, err)
	}
	if exists {
		return fmt.Errorf("%s already exists and fullsend did not create it; fullsend does not overwrite or delete it. Remove or rename that variable, then re-run install", forge.SecretGitLabTriggerAccountToken)
	}
	return nil
}

// triggerSinkProblems returns why the live token sink is unsafe or missing,
// or an error when its attributes cannot be read.
func triggerSinkProblems(ctx context.Context, client forge.Client, owner, repo string) ([]string, error) {
	name := forge.SecretGitLabTriggerAccountToken
	prot, err := client.GetRepoSecretProtection(ctx, owner, repo, name)
	if err != nil {
		return nil, safeAPIError("reading "+name, err)
	}
	if !prot.Exists {
		return []string{name + " is missing"}, nil
	}
	var problems []string
	if !prot.Masked {
		problems = append(problems, name+" is not masked")
	}
	if !prot.Protected {
		problems = append(problems, name+" is not protected")
	}
	if prot.FileType {
		problems = append(problems, name+" is a file-type variable")
	}
	if prot.EnvironmentScoped {
		problems = append(problems, name+" is limited to an environment scope")
	}
	return problems, nil
}

func (op *gitLabTriggerAccountOp) sinkHealthy(ctx context.Context) (bool, error) {
	problems, err := triggerSinkProblems(ctx, op.opts.State, op.opts.Owner, op.opts.Repo)
	if err != nil || len(problems) != 0 {
		return false, err
	}
	vars, err := op.opts.State.ListRepoVariables(ctx, op.opts.Owner, op.opts.Repo)
	if err != nil {
		return false, safeAPIError("reading the trigger token sink", err)
	}
	value := vars[forge.SecretGitLabTriggerAccountToken]
	if value == "" {
		return false, nil
	}
	// Existing accounts predate the digest. Keep their healthy bearer usable
	// until convergence creates a fingerprinted generation and moves the hook.
	if op.st.TriggerTokenDigest == "" {
		return true, nil
	}
	return triggerTokenDigest(value) == op.st.TriggerTokenDigest, nil
}

// startFreshGeneration moves the current generation to the retired list and
// records a new, empty generation, in one ledger write so ownership of every
// resource of both generations is recorded before the new one creates any.
func (op *gitLabTriggerAccountOp) startFreshGeneration(ctx context.Context) error {
	// The previous generation's trigger value was distributed, so the account
	// that owns it must be at effective Developer access before it is replaced
	// and while the replacement is being built. An administrator may have
	// raised it out of band; a failed replacement would otherwise leave it
	// elevated. Fail closed, before the ledger moves, when that cannot be
	// verified.
	if err := op.restorePreviousToDeveloper(ctx); err != nil {
		return fmt.Errorf("GitLab trigger account %d could not be verified at Developer access before its replacement; nothing was replaced: %w", op.st.AccountID, err)
	}
	nonce := make([]byte, 8)
	if _, err := io.ReadFull(op.opts.Rand, nonce); err != nil {
		return fmt.Errorf("GitLab trigger account: generating nonce: %w", err)
	}
	old := op.st
	old.Retired = nil
	old.PublicationPending = false
	// A legacy generation has no recorded bearer digest. Bind its healthy
	// published sink before moving it to the retired list, so an interrupted
	// replacement can distinguish that exact old value from a later override.
	if old.PublishStarted && old.TriggerPublished && old.TriggerTokenDigest == "" {
		problems, err := triggerSinkProblems(ctx, op.opts.State, op.opts.Owner, op.opts.Repo)
		if err != nil {
			return err
		}
		if len(problems) == 0 {
			vars, err := op.opts.State.ListRepoVariables(ctx, op.opts.Owner, op.opts.Repo)
			if err != nil {
				return safeAPIError("reading the retired trigger token sink", err)
			}
			if value := vars[forge.SecretGitLabTriggerAccountToken]; value != "" {
				old.TriggerTokenDigest = triggerTokenDigest(value)
			}
		}
	}
	phase := gitLabTriggerPhaseBootstrapping
	if old.Phase == gitLabTriggerPhaseRotating {
		phase = gitLabTriggerPhaseRotating
	}
	op.st = gitLabTriggerAccountState{
		Phase: phase, Branch: old.Branch, Nonce: hex.EncodeToString(nonce),
		Retired: append(slices.Clone(op.st.Retired), old),
	}
	return op.save(ctx)
}

// restorePreviousToDeveloper returns the current generation's account to
// Developer and confirms its effective access is exactly Developer. The account
// is proven to be the recorded, nonce-named one first. An account that no
// longer exists holds no role, so there is nothing to restore.
func (op *gitLabTriggerAccountOp) restorePreviousToDeveloper(ctx context.Context) error {
	if op.st.AccountID == 0 {
		return nil
	}
	_, found, err := op.findOwnedAccount(ctx)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	return op.ensureMembership(ctx, forge.GitLabAccessLevelDeveloper)
}

// restorePublishedAccessToDeveloper proves each publication-owning generation's
// account is the recorded, nonce-named one and confirms its effective access is
// exactly Developer, restoring it when it is not. The current generation and
// every retired generation that started publishing are covered, because an
// interrupted replacement can leave a retired generation's live token in the
// sink. Every generation is attempted and the errors are joined.
func (op *gitLabTriggerAccountOp) restorePublishedAccessToDeveloper(ctx context.Context) error {
	var errs []error
	if op.st.PublishStarted {
		if err := op.restorePreviousToDeveloper(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	for i := range op.st.Retired {
		if !op.st.Retired[i].PublishStarted {
			continue
		}
		sub := &gitLabTriggerAccountOp{opts: op.opts, st: op.st.Retired[i], exists: true, retired: true}
		if err := sub.restorePreviousToDeveloper(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// containUnsafePublishedSink removes a published token sink that is present
// but unsafe, and revokes the generation's ownership-verified trigger, before
// a deferral is returned: a deferral must not leave an exposed value usable.
// The sink is removed only because the ledger records that fullsend wrote it,
// and the trigger only when it carries the recorded description and belongs to
// the recorded account (revokeOwnedTriggers). PublishStarted and the trigger
// ID stay in the ledger as the cleanup obligation, so a later install starts a
// replacement generation and retires this one. A missing sink holds no
// recoverable value, but an earlier containment may have deleted it and failed
// to revoke the trigger, so the revocation is retried.
//
// The sink may be owned by a retired generation while the current one never
// published (an interrupted replacement), so every generation whose
// publication is recorded is covered, not only the current one.
//
// A published credential is also unsafe, whatever the sink's attributes, when
// the pipeline-variable override role is not no_one_allowed or cannot be read:
// those attributes do not stop variable injection through the trigger API. The
// sink is then removed and every owned trigger revoked.
func (op *gitLabTriggerAccountOp) containUnsafePublishedSink(ctx context.Context) error {
	if !op.exists || op.retired || !op.sinkOwned() {
		return nil
	}
	if reason, safetyErr := op.webhookSafetyDeferral(ctx); reason != "" || safetyErr != nil {
		// The value has already been published, so a failed or unreadable
		// project invariant requires ownership-verified revocation and sink
		// removal before convergence can return or publish a replacement.
		accessErr := op.restorePublishedAccessToDeveloper(ctx)
		return errors.Join(safetyErr, accessErr, op.removeRevokeAndSave(ctx, true))
	}
	// A distributed trigger value is only safe while its account holds exactly
	// Developer access, whatever the sink and the override role look like. The
	// access of every publication-owning generation is therefore restored
	// first, independently of every sink and role read below, so an unreadable,
	// missing or unsafe sink never leaves an account raised out of band. A step
	// that fails does not suppress the others and every error is joined.
	accessErr := op.restorePublishedAccessToDeveloper(ctx)
	contained, sinkErr := op.containBySinkState(ctx)
	errs := []error{sinkErr}
	if accessErr != nil {
		errs = append(errs, fmt.Errorf("GitLab trigger account: Developer access of a publishing generation could not be verified, so its published credential was contained: %w", accessErr))
		if !contained {
			// Developer access cannot be established, so the sink is removed
			// and the owned triggers are revoked independently, keeping the
			// retry obligations.
			errs = append(errs, op.removeRevokeAndSave(ctx, true))
		}
	}
	return errors.Join(errs...)
}

func (op *gitLabTriggerAccountOp) webhookSafetyDeferral(ctx context.Context) (string, error) {
	if !op.opts.EnforceWebhookSafety {
		return "", nil
	}
	branch := op.opts.Branch
	if branch == "" {
		branch = op.st.Branch
	}
	return gitlabTriggerProjectSafetyProblem(ctx, op.opts.State, op.opts.Owner, op.opts.Repo, branch)
}

// containBySinkState removes the sink and revokes the owned triggers when the
// override role or the sink attributes make the published credential unsafe,
// and retries the revocation for a missing sink. contained reports whether the
// removal and revocation ran, so the caller does not repeat them.
func (op *gitLabTriggerAccountOp) containBySinkState(ctx context.Context) (contained bool, err error) {
	reason, roleErr := op.overrideRoleDeferral(ctx)
	if roleErr != nil || reason != "" {
		return true, op.removeRevokeAndSave(ctx, true)
	}
	prot, err := op.opts.State.GetRepoSecretProtection(ctx, op.opts.Owner, op.opts.Repo, forge.SecretGitLabTriggerAccountToken)
	if err != nil {
		return true, errors.Join(safeAPIError("reading "+forge.SecretGitLabTriggerAccountToken, err), op.removeRevokeAndSave(ctx, true))
	}
	if !prot.Exists {
		// The variable may already be gone because an earlier containment
		// deleted it and then failed to revoke the trigger. A missing sink
		// holds no recoverable value, so retry the ownership-verified
		// revocation before the deferral stands; it is idempotent.
		if op.retiredBearerAwaitsReplacement() {
			// The old webhook still uses a retired trigger during the gap
			// between clearing the old variable and publishing the new one.
			// Project safety and every published owner's access were checked
			// before this sink check; keep the retired bearer until cutover.
			return false, nil
		}
		return true, op.removeRevokeAndSave(ctx, false)
	}
	problems, err := triggerSinkProblems(ctx, op.opts.State, op.opts.Owner, op.opts.Repo)
	if err != nil {
		return true, errors.Join(err, op.removeRevokeAndSave(ctx, true))
	}
	if len(problems) > 0 {
		return true, op.removeRevokeAndSave(ctx, true)
	}
	vars, err := op.opts.State.ListRepoVariables(ctx, op.opts.Owner, op.opts.Repo)
	if err != nil {
		return true, errors.Join(safeAPIError("reading the trigger token sink", err), op.removeRevokeAndSave(ctx, true))
	}
	value := vars[forge.SecretGitLabTriggerAccountToken]
	// A retired generation may still occupy the sink after the new publication
	// marker is saved but before the variable is replaced. Accept only a
	// matching retired digest in that window; once current publication finishes,
	// a retired value cannot make an unrelated replacement appear safe.
	bound := false
	if value != "" && op.st.PublishStarted {
		bound = op.st.TriggerTokenDigest == "" || triggerTokenDigest(value) == op.st.TriggerTokenDigest
	}
	if value != "" && !bound && (!op.st.PublishStarted || op.st.PublicationPending) {
		for _, old := range op.st.Retired {
			bound = bound || old.PublishStarted && old.TriggerTokenDigest != "" && triggerTokenDigest(value) == old.TriggerTokenDigest
		}
	}
	if !bound {
		return true, op.removeRevokeAndSave(ctx, true)
	}
	return false, nil
}

// retiredBearerAwaitsReplacement reports whether an empty sink is the expected
// gap between clearing a retired generation's variable and publishing the
// current generation's. That holds while the current generation has not
// published and either its own publication is pending or a retired generation
// still has a published trigger. The second case survives an interrupted
// retry: startFreshGeneration clears PublicationPending when it retires the
// pending generation, but the generation the webhook still uses stays
// published until webhook cutover or an explicit containment revokes it and
// clears its marker (removeAndRevokePublished).
func (op *gitLabTriggerAccountOp) retiredBearerAwaitsReplacement() bool {
	if len(op.st.Retired) == 0 || op.st.TriggerPublished {
		return false
	}
	if op.st.PublicationPending {
		return true
	}
	for _, old := range op.st.Retired {
		if old.PublishStarted && old.TriggerPublished {
			return true
		}
	}
	return false
}

// removeAndRevokePublished optionally removes the token sink, then revokes the
// ownership-verified trigger of the current generation (when it published) and
// of every retired generation that published, without touching their other
// ledger obligations. PublishStarted and the trigger IDs stay recorded so the
// retirement of each generation revokes and verifies again.
//
// Every step is attempted independently: a failed variable deletion does not
// suppress revocation, and one generation's failed revocation does not suppress
// the others. A generation's TriggerPublished marker is cleared only after its
// own revocation succeeded, and every error is joined. The caller persists the
// ledger even when an error is returned.
//
// The sink deletion and each generation's revocation run on their own bounded
// context detached from ctx's cancellation (as endBootstrap does), so a step
// that exhausts its deadline never suppresses the following ones.
func (op *gitLabTriggerAccountOp) removeAndRevokePublished(ctx context.Context, removeSink bool) error {
	var errs []error
	op.st.PublicationPending = false
	if removeSink && op.opts.UnpublishTriggerToken != nil {
		if err := gitlabCleanupStep(ctx, op.opts.UnpublishTriggerToken); err != nil {
			errs = append(errs, safeAPIError("removing the unsafe pipeline trigger token variable", err))
		}
	}
	if op.st.PublishStarted {
		if err := gitlabCleanupStep(ctx, op.revokeOwnedTriggers); err != nil {
			errs = append(errs, err)
		} else {
			op.st.TriggerPublished = false
		}
	}
	for i := range op.st.Retired {
		if !op.st.Retired[i].PublishStarted {
			continue
		}
		sub := &gitLabTriggerAccountOp{opts: op.opts, st: op.st.Retired[i], exists: true, retired: true}
		if err := gitlabCleanupStep(ctx, sub.revokeOwnedTriggers); err != nil {
			errs = append(errs, err)
			continue
		}
		op.st.Retired[i].TriggerPublished = false
	}
	return errors.Join(errs...)
}

// gitlabCleanupStep runs fn on its own bounded context detached from ctx's
// cancellation, so a failed, canceled or deadline-exhausting step never
// skips the next one.
func gitlabCleanupStep(ctx context.Context, fn func(context.Context) error) error {
	cctx, cancel := gitlabCleanupContext(ctx)
	defer cancel()
	return fn(cctx)
}

// removeRevokeAndSave runs removeAndRevokePublished and persists the ledger
// whether or not it failed, so the generations whose revocation completed are
// recorded and the unresolved obligations stay for an idempotent retry.
func (op *gitLabTriggerAccountOp) removeRevokeAndSave(ctx context.Context, removeSink bool) error {
	var markerErr error
	if op.st.PublicationPending {
		// Persist the end of the benign clear-before-publish window before
		// deleting an unsafe sink. If this write fails, keep the sink so a
		// retry can still distinguish unsafe containment from that window;
		// revoke every owned trigger now even though the deletion must wait.
		op.st.PublicationPending = false
		markerErr = gitlabCleanupStep(ctx, op.save)
		if markerErr != nil {
			removeSink = false
		}
	}
	err := op.removeAndRevokePublished(ctx, removeSink)
	if sErr := gitlabCleanupStep(ctx, op.save); sErr != nil {
		err = errors.Join(err, sErr)
	}
	return errors.Join(markerErr, err)
}

// containFailedReplacementPublication removes a possibly exposed replacement
// value and revokes only its trigger. A retired trigger may still serve the
// webhook until cutover, so publication failure must not retire it. The
// pending marker preserves that obligation across a retry while the sink is
// empty; project-wide safety failures still revoke every generation.
func (op *gitLabTriggerAccountOp) containFailedReplacementPublication(ctx context.Context) error {
	var errs []error
	if op.opts.UnpublishTriggerToken != nil {
		if err := gitlabCleanupStep(ctx, op.opts.UnpublishTriggerToken); err != nil {
			errs = append(errs, safeAPIError("removing the failed replacement trigger token variable", err))
		}
	}
	if err := gitlabCleanupStep(ctx, op.revokeOwnedTriggers); err != nil {
		errs = append(errs, err)
	} else {
		op.st.TriggerPublished = false
	}
	if err := gitlabCleanupStep(ctx, op.save); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// retirePrevious removes every retired generation with the same ownership
// checks as uninstall: its trigger token, branch grant, deploy key and
// account. Each step clears its ledger entry only once it succeeded, so a
// failure keeps the obligation and a later install or uninstall resumes.
func (op *gitLabTriggerAccountOp) retirePrevious(ctx context.Context) error {
	for len(op.st.Retired) > 0 {
		sub := &gitLabTriggerAccountOp{opts: op.opts, st: op.st.Retired[0], exists: true, retired: true, uninstalling: op.uninstalling}
		sub.persist = func(ctx context.Context) error {
			op.st.Retired[0] = sub.st
			return op.save(ctx)
		}
		err := errors.Join(
			sub.containBootstrap(ctx),
		)
		if err == nil {
			err = sub.removeProtectedRefResources(ctx)
		}
		if err == nil {
			err = sub.removeAccount(ctx)
		}
		if err != nil {
			return fmt.Errorf("retiring a previous GitLab trigger account generation (recorded in %s; re-run install or uninstall to resume): %w", forge.VarGitLabTriggerAccount, err)
		}
		op.st.Retired = op.st.Retired[1:]
		if err := op.save(ctx); err != nil {
			return err
		}
	}
	return nil
}

// RetireGitLabTriggerAccount completes deferred generation cleanup after the
// caller has verified that the managed webhook no longer uses a retired token.
// An unfinished replacement keeps the old generation for a later retry. This
// call does not alter dispatch activation or provision replacement resources.
func RetireGitLabTriggerAccount(ctx context.Context, opts GitLabTriggerAccountOptions) error {
	if err := requireGitLabTriggerLease(opts); err != nil {
		return err
	}
	op, err := newGitLabTriggerAccountOp(ctx, opts)
	if err != nil || !op.exists || len(op.st.Retired) == 0 {
		return err
	}
	if op.st.Phase != gitLabTriggerPhaseReady || !op.st.TriggerPublished {
		return nil
	}
	return op.retirePrevious(ctx)
}

// GitLabTriggerAccountRetirementPending reports whether a ready replacement
// has an earlier generation to clean up. An unfinished replacement keeps its
// retired generation without probing the webhook or reporting a cleanup
// failure. Callers use this under the project lease before checking cutover.
func GitLabTriggerAccountRetirementPending(ctx context.Context, client forge.Client, owner, repo string) (bool, error) {
	op, err := loadGitLabTriggerAccountOp(ctx, GitLabTriggerAccountOptions{Owner: owner, Repo: repo, State: client})
	if err != nil {
		return false, err
	}
	return op.exists && op.st.Phase == gitLabTriggerPhaseReady && op.st.TriggerPublished && len(op.st.Retired) > 0, nil
}

// removeSink deletes the token sink when the ledger proves fullsend wrote it
// and clears every generation's publication marker.
func (op *gitLabTriggerAccountOp) removeSink(ctx context.Context) error {
	if !op.sinkOwned() {
		return nil
	}
	if op.opts.UnpublishTriggerToken != nil {
		if err := op.opts.UnpublishTriggerToken(ctx); err != nil {
			return safeAPIError("removing the published pipeline trigger token", err)
		}
	}
	op.st.PublishStarted, op.st.TriggerPublished, op.st.PublicationPending = false, false, false
	for i := range op.st.Retired {
		op.st.Retired[i].PublishStarted, op.st.Retired[i].TriggerPublished = false, false
	}
	return op.save(ctx)
}

// findOwnedAccount returns the inventory entry for the recorded account, or
// the single account carrying the recorded nonce name when no ID is recorded.
func (op *gitLabTriggerAccountOp) findOwnedAccount(ctx context.Context) (GitLabServiceAccount, bool, error) {
	accounts, err := op.opts.API.ListProjectServiceAccounts(ctx, op.opts.Owner, op.opts.Repo)
	if err != nil {
		return GitLabServiceAccount{}, false, safeAPIError("listing project service accounts", err)
	}
	name := op.st.accountName()
	var matches []GitLabServiceAccount
	for _, a := range accounts {
		if op.st.AccountID != 0 && a.ID == op.st.AccountID {
			if a.Name != name {
				return GitLabServiceAccount{}, false, fmt.Errorf("GitLab service account %d does not carry the recorded trigger account name; refusing to treat it as fullsend-owned", a.ID)
			}
			return a, true, nil
		}
		if op.st.AccountID == 0 && a.Name == name {
			matches = append(matches, a)
		}
	}
	switch {
	case len(matches) > 1:
		return GitLabServiceAccount{}, false, fmt.Errorf("found %d GitLab service accounts named %q; refusing to choose one", len(matches), name)
	case len(matches) == 1:
		return matches[0], true, nil
	}
	return GitLabServiceAccount{}, false, nil
}

func (op *gitLabTriggerAccountOp) ensureAccount(ctx context.Context) error {
	acct, found, err := op.findOwnedAccount(ctx)
	if err != nil {
		return err
	}
	if found {
		if op.st.AccountID == 0 {
			op.st.AccountID = acct.ID
			return op.save(ctx)
		}
		return nil
	}
	if op.st.AccountID != 0 {
		return fmt.Errorf("recorded GitLab trigger account %d no longer exists; run uninstall to clear its remaining resources, then install again", op.st.AccountID)
	}
	created, err := op.opts.API.CreateProjectServiceAccount(ctx, op.opts.Owner, op.opts.Repo, op.st.accountName())
	if err != nil {
		return safeAPIError("creating the GitLab trigger service account", err)
	}
	if created.ID <= 0 {
		return errors.New("creating the GitLab trigger service account: response carried no account ID")
	}
	op.st.AccountID = created.ID
	return op.save(ctx)
}

// checkForeignPATs fails closed when the trigger account holds any active PAT
// when no bootstrap is in progress: such a token could write the protected
// branch. Every bootstrap PAT of this lifecycle is revoked by containBootstrap
// or endBootstrap before this runs, so an active token that carries the
// bootstrap name is not fullsend's either, and fullsend does not revoke
// credentials it cannot prove it owns.
func (op *gitLabTriggerAccountOp) checkForeignPATs(ctx context.Context) error {
	pats, err := op.opts.API.ListServiceAccountPATs(ctx, op.opts.Owner, op.opts.Repo, op.st.AccountID)
	if err != nil {
		return safeAPIError("listing the GitLab trigger account's tokens", err)
	}
	for _, p := range pats {
		if p.Active && !p.Revoked {
			return fmt.Errorf("GitLab trigger account %d holds active personal access token %d that fullsend did not create; revoke it, then re-run install", op.st.AccountID, p.ID)
		}
	}
	return nil
}

// checkForeignResources fails closed when the trigger account owns anything
// fullsend did not create: an SSH key, a pipeline schedule that is not one of
// the managed polling schedules, or a pipeline trigger token whose description
// is not the recorded one. Each would let a holder act as the account,
// including while it is elevated, and fullsend removes only what it can prove
// it owns.
//
// The managed polling schedules (PipelineScheduleSpecs) the account owns are
// fullsend's: ConvergeGitLabTriggerSchedules creates them once dispatch is
// activated (#8508). They are recognised by their exact description and are
// handed back to the installer before the account is ever raised to
// Maintainer (createWithBootstrap) or deleted (removeAccount).
func (op *gitLabTriggerAccountOp) checkForeignResources(ctx context.Context) error {
	o, r, id := op.opts.Owner, op.opts.Repo, op.st.AccountID
	keys, err := op.opts.API.AccountSSHKeyIDs(ctx, id)
	if err != nil {
		return safeAPIError("listing the GitLab trigger account's SSH keys", err)
	}
	if len(keys) > 0 {
		return fmt.Errorf("GitLab trigger account %d has %d SSH authentication key(s) that fullsend did not create; remove them, then re-run install", id, len(keys))
	}
	_, _, foreignSchedules, err := op.accountSchedules(ctx)
	if err != nil {
		return err
	}
	if len(foreignSchedules) > 0 {
		return fmt.Errorf("GitLab trigger account %d owns %d pipeline schedule(s) that fullsend did not create (IDs %v); remove or reassign them, then re-run install", id, len(foreignSchedules), foreignSchedules)
	}
	// A schedule GitLab reported without an owner is not attributed to this
	// account: it may or may not run as it. Elevation is refused until an
	// administrator removes or reassigns it, and the message says so.
	ownerless, err := op.opts.API.UnknownOwnerScheduleIDs(ctx, o, r)
	if err != nil {
		return safeAPIError("listing pipeline schedules with an unknown owner", err)
	}
	if len(ownerless) > 0 {
		return fmt.Errorf("%d pipeline schedule(s) have no reported owner (IDs %v); fullsend cannot tell whether they run as GitLab trigger account %d and does not delete them by inference. Remove or reassign them, then re-run install", len(ownerless), ownerless, id)
	}
	toks, err := op.opts.API.ListPipelineTriggerTokens(ctx, o, r)
	if err != nil {
		return safeAPIError("listing pipeline trigger tokens", err)
	}
	// A trigger token whose owner GitLab did not report may act as this
	// account, so the account is not elevated while one exists. Its ID is
	// reported; fullsend never deletes it by inference.
	var unknownOwner []int64
	for _, t := range toks {
		if t.OwnerID == 0 {
			unknownOwner = append(unknownOwner, t.ID)
		}
	}
	if len(unknownOwner) > 0 {
		return fmt.Errorf("pipeline trigger token(s) %v have an unknown owner; fullsend cannot prove they are not the GitLab trigger account's and does not delete them by inference. Revoke them, then re-run install", unknownOwner)
	}
	for _, t := range toks {
		if t.OwnerID == int64(id) && t.Description != op.st.triggerDescription() {
			return fmt.Errorf("pipeline trigger token %d is owned by GitLab trigger account %d but does not carry the recorded trigger description; refusing to treat it as fullsend-owned. Revoke it, then re-run install", t.ID, id)
		}
	}
	return nil
}

// checkNoUnfinishedJobs defers while a job still runs as the trigger account.
// A previously published trigger may already have started a pipeline whose
// CI_JOB_TOKEN keeps the account's role: revoking the token does not end it,
// so the account must not be raised to Maintainer until those jobs finish.
func (op *gitLabTriggerAccountOp) checkNoUnfinishedJobs(ctx context.Context) error {
	jobs, err := op.opts.API.AccountActiveJobIDs(ctx, op.opts.Owner, op.opts.Repo, op.st.AccountID)
	if err != nil {
		return safeAPIError("listing the GitLab trigger account's unfinished jobs", err)
	}
	if len(jobs) == 0 {
		return nil
	}
	// The trigger tokens are already revoked; record that the account is not
	// serving a published trigger so status does not report it ready.
	if op.st.Phase == gitLabTriggerPhaseReady {
		op.st.Phase = gitLabTriggerPhaseBootstrapping
		if err := op.save(ctx); err != nil {
			return err
		}
	}
	return &triggerDeferral{reason: fmt.Sprintf("%d unfinished job(s) still run as the GitLab trigger account; its role is not raised again until they finish, because their job tokens keep the account's authority. Re-run install once they end", len(jobs))}
}

// ensureMembership sets the account's direct membership and then confirms
// its effective access level, which includes access inherited from groups.
// It fails closed when the effective level is not exactly level, so inherited
// access that a direct update cannot lower is never reported as converged.
func (op *gitLabTriggerAccountOp) ensureMembership(ctx context.Context, level int) error {
	o, r, id := op.opts.Owner, op.opts.Repo, int64(op.st.AccountID)
	current, err := op.opts.API.GetProjectMemberAccessLevel(ctx, o, r, id)
	switch {
	case forge.IsNotFound(err):
		err = op.opts.API.AddProjectMember(ctx, o, r, id, level)
	case err != nil:
		return safeAPIError("reading the GitLab trigger account's membership", err)
	case current != level:
		err = op.opts.API.UpdateProjectMemberAccessLevel(ctx, o, r, id, level)
	}
	if err != nil {
		return safeAPIError("setting the GitLab trigger account's membership", err)
	}
	// Always confirm the effective level, even when the first read already
	// matched: that read is not trusted to include inherited group access.
	effective, err := op.opts.API.GetProjectMemberAccessLevel(ctx, o, r, id)
	if err != nil {
		return safeAPIError("confirming the GitLab trigger account's membership", err)
	}
	if effective != level {
		return fmt.Errorf("GitLab trigger account %d has effective access level %d after its membership was set to %d; remove the inherited group or project access that overrides it", op.st.AccountID, effective, level)
	}
	return nil
}

func (op *gitLabTriggerAccountOp) deployKeyPresent(ctx context.Context) (bool, error) {
	if op.st.DeployKeyFingerprint == "" {
		return false, nil
	}
	key, found, err := op.findOwnedDeployKey(ctx)
	if err != nil {
		return false, err
	}
	if !found {
		// The key is gone. Its grant went with it.
		op.st.DeployKeyID, op.st.GrantEntryID, op.st.DeployKeyFingerprint = 0, 0, ""
		return false, op.save(ctx)
	}
	if !key.CanPush {
		// Uninstall still removes the key: findOwnedDeployKey does not require
		// push, only this readiness check does.
		return false, fmt.Errorf("deploy key %d cannot push; fullsend does not change it. Run repos uninstall to remove it, then install again", key.ID)
	}
	if op.st.DeployKeyID == 0 {
		op.st.DeployKeyID = key.ID
		return true, op.save(ctx)
	}
	return true, nil
}

// findOwnedDeployKey finds the recorded deploy key. The title must carry the
// nonce and the fingerprint must match the one recorded before creation.
func (op *gitLabTriggerAccountOp) findOwnedDeployKey(ctx context.Context) (GitLabDeployKey, bool, error) {
	keys, err := op.opts.API.ListProjectDeployKeys(ctx, op.opts.Owner, op.opts.Repo)
	if err != nil {
		return GitLabDeployKey{}, false, safeAPIError("listing project deploy keys", err)
	}
	for _, k := range keys {
		byID := op.st.DeployKeyID != 0 && k.ID == op.st.DeployKeyID
		byIntent := op.st.DeployKeyID == 0 && k.Title == op.st.deployKeyTitle()
		if !byID && !byIntent {
			continue
		}
		if k.Title != op.st.deployKeyTitle() || !sameDeployKeyFingerprint(op.st.DeployKeyFingerprint, k.FingerprintSHA256) {
			return GitLabDeployKey{}, false, fmt.Errorf("deploy key %d does not match the recorded trigger key title and fingerprint; refusing to treat it as fullsend-owned", k.ID)
		}
		return k, true, nil
	}
	return GitLabDeployKey{}, false, nil
}

func (op *gitLabTriggerAccountOp) triggerPresent(ctx context.Context) (bool, error) {
	if op.st.TriggerTokenID != 0 && (op.st.TriggerPublished || op.st.PublicationPending) {
		tok, found, err := op.findTrigger(ctx, op.st.TriggerTokenID)
		if err != nil {
			return false, err
		}
		if found && tok.OwnerID == int64(op.st.AccountID) {
			return true, nil
		}
		if found {
			return false, fmt.Errorf("pipeline trigger token %d is not owned by GitLab trigger account %d", tok.ID, op.st.AccountID)
		}
		// A recorded publication may already have distributed this value.
		// Start a fresh generation rather than elevating this one.
		return false, nil
	}
	return false, nil
}

func (op *gitLabTriggerAccountOp) findTrigger(ctx context.Context, id int64) (forge.PipelineTriggerToken, bool, error) {
	toks, err := op.opts.API.ListPipelineTriggerTokens(ctx, op.opts.Owner, op.opts.Repo)
	if err != nil {
		return forge.PipelineTriggerToken{}, false, safeAPIError("listing pipeline trigger tokens", err)
	}
	for _, t := range toks {
		if t.ID != id {
			continue
		}
		if t.Description != op.st.triggerDescription() {
			return forge.PipelineTriggerToken{}, false, fmt.Errorf("pipeline trigger token %d does not carry the recorded trigger description; refusing to treat it as fullsend-owned", t.ID)
		}
		return t, true, nil
	}
	return forge.PipelineTriggerToken{}, false, nil
}

// revokeUnpublishedTriggers revokes the recorded trigger token and any token
// that carries the recorded description and is owned by the trigger account
// but whose value was never published, which is the residue of an interrupted
// create. A recorded token must still carry the recorded description and be
// owned by the trigger account; otherwise this fails closed and the ledger
// keeps the obligation.
func (op *gitLabTriggerAccountOp) revokeUnpublishedTriggers(ctx context.Context) error {
	// The sink holds this generation's value only after it started
	// publishing. A retired generation, or one that never published, must not
	// delete a sink another generation or an administrator owns.
	if !op.retired && op.st.PublishStarted && op.opts.UnpublishTriggerToken != nil {
		if err := op.opts.UnpublishTriggerToken(ctx); err != nil {
			return safeAPIError("removing the published pipeline trigger token", err)
		}
	}
	if err := op.revokeOwnedTriggers(ctx); err != nil {
		return err
	}
	op.st.TriggerTokenID, op.st.TriggerPublished, op.st.PublishStarted, op.st.PublicationPending = 0, false, false, false
	return op.save(ctx)
}

// containUnsafeSink removes the token sink and revokes the generation's
// ownership-verified trigger token, in that order, without touching the
// generation's other ledger obligations. It runs before a replacement
// generation starts, so no usable copy of an exposed value outlives the
// decision to replace it. PublishStarted and the trigger ID stay recorded:
// the retirement of this generation revokes and verifies again.
func (op *gitLabTriggerAccountOp) containUnsafeSink(ctx context.Context) error {
	var errs []error
	if op.opts.UnpublishTriggerToken != nil {
		if err := op.opts.UnpublishTriggerToken(ctx); err != nil {
			errs = append(errs, safeAPIError("removing the unsafe pipeline trigger token variable", err))
		}
	}
	// A failed deletion must not suppress the revocation.
	if err := op.revokeOwnedTriggers(ctx); err != nil {
		errs = append(errs, err)
	} else {
		op.st.TriggerPublished = false
	}
	return errors.Join(errs...)
}

// revokeOwnedTriggers revokes the recorded trigger token and every token that
// carries the recorded description and is owned by the trigger account. A
// recorded token must carry the recorded description and be owned by the
// trigger account; otherwise it fails closed without revoking it.
func (op *gitLabTriggerAccountOp) revokeOwnedTriggers(ctx context.Context) error {
	toks, err := op.opts.API.ListPipelineTriggerTokens(ctx, op.opts.Owner, op.opts.Repo)
	if err != nil {
		return safeAPIError("listing pipeline trigger tokens", err)
	}
	ownedByAccount := func(t forge.PipelineTriggerToken) bool {
		return op.st.AccountID != 0 && t.OwnerID == int64(op.st.AccountID)
	}
	for _, t := range toks {
		if op.st.TriggerTokenID != 0 && t.ID == op.st.TriggerTokenID {
			if t.Description != op.st.triggerDescription() {
				return fmt.Errorf("pipeline trigger token %d does not carry the recorded trigger description; refusing to treat it as fullsend-owned", t.ID)
			}
			if !ownedByAccount(t) {
				return fmt.Errorf("pipeline trigger token %d is not owned by GitLab trigger account %d; refusing to revoke it", t.ID, op.st.AccountID)
			}
		} else if t.Description != op.st.triggerDescription() || !ownedByAccount(t) {
			continue
		}
		if err := op.opts.API.RevokePipelineTriggerToken(ctx, op.opts.Owner, op.opts.Repo, t.ID); err != nil && !forge.IsNotFound(err) {
			return safeAPIError("revoking an unpublished pipeline trigger token", err)
		}
	}
	return nil
}

// revokeBootstrapPATs revokes the recorded bootstrap PAT and any other token
// on the trigger account that carries the bootstrap name.
func (op *gitLabTriggerAccountOp) revokeBootstrapPATs(ctx context.Context) error {
	o, r, id := op.opts.Owner, op.opts.Repo, op.st.AccountID
	pats, err := op.opts.API.ListServiceAccountPATs(ctx, o, r, id)
	if err != nil && !forge.IsNotFound(err) {
		return safeAPIError("listing the GitLab trigger account's tokens", err)
	}
	// A deleted account has no tokens left to revoke.
	for _, p := range pats {
		if p.Revoked {
			continue
		}
		byID := op.st.PATID != 0 && p.ID == op.st.PATID
		byName := p.Name == op.st.patName()
		if byID && !byName {
			return fmt.Errorf("GitLab trigger account %d token %d does not carry the recorded bootstrap token name; refusing to treat it as fullsend-owned", id, p.ID)
		}
		if !byID && !byName {
			continue
		}
		if err := op.opts.API.RevokeServiceAccountPAT(ctx, o, r, id, p.ID); err != nil && !forge.IsNotFound(err) {
			return safeAPIError("revoking the GitLab trigger bootstrap token", err)
		}
	}
	// Every active PAT carrying the bootstrap name is revoked, so neither the
	// recorded ID nor the creation obligation is outstanding any more.
	op.st.PATID, op.st.PATPending = 0, false
	return op.save(ctx)
}

// pendingTrigger is a trigger token created during bootstrap whose value is
// held in memory until the account is back at Developer.
type pendingTrigger struct {
	id    int64
	value string
}

// bootstrap elevates the account, mints a bootstrap PAT, creates whatever of
// the deploy key and trigger token is missing, then always revokes the PAT
// and returns the account to Developer, even when a create step failed. A new
// trigger value is published only after that, and a trigger that is already
// published is revoked before the account is elevated again, so no usable
// trigger token exists while the account holds Maintainer.
func (op *gitLabTriggerAccountOp) bootstrap(ctx context.Context, keyOK, triggerOK bool) error {
	// A recorded PAT from an interrupted run has no recoverable value.
	if err := op.revokeBootstrapPATs(ctx); err != nil {
		return err
	}
	var pending *pendingTrigger
	if !keyOK || !triggerOK {
		// Revoke every recorded or nonce-owned trigger before the account is
		// elevated, whether or not the ledger says it was published: a
		// publication that took effect but reported an error, or whose ledger
		// write failed, leaves a live token the ledger does not know about.
		if err := op.revokeUnpublishedTriggers(ctx); err != nil {
			return err
		}
		triggerOK = false
		// Remove the account's scheduled source before the final active-job
		// check. Otherwise a schedule could start a job after the check and
		// retain its job token while the account is raised to Maintainer.
		if _, err := op.releaseSchedules(ctx, true); err != nil {
			return fmt.Errorf("handing the managed pipeline schedules back before raising the GitLab trigger account: %w", err)
		}
		if err := op.checkNoUnfinishedJobs(ctx); err != nil {
			return err
		}
		var err error
		pending, err = op.createWithBootstrap(ctx, keyOK, triggerOK)
		if err != nil {
			if cleanupErr := op.endBootstrap(ctx); cleanupErr != nil {
				return errors.Join(err, cleanupErr)
			}
			return err
		}
	}
	if err := op.endBootstrap(ctx); err != nil {
		return err
	}
	if pending == nil {
		return nil
	}
	// Record that the value may leave this process before it is written, and
	// clear whatever a previous generation left in the sink so the new value
	// is stored with fresh attributes.
	op.st.TriggerTokenDigest = triggerTokenDigest(pending.value)
	op.st.PublishStarted = true
	op.st.PublicationPending = true
	if err := op.save(ctx); err != nil {
		return err
	}
	if op.opts.UnpublishTriggerToken != nil {
		if err := op.opts.UnpublishTriggerToken(ctx); err != nil {
			return safeAPIError("clearing the pipeline trigger token sink", err)
		}
	}
	if err := op.opts.PublishTriggerToken(ctx, pending.id, pending.value); err != nil {
		pubErr := safeAPIError("publishing the pipeline trigger token", err)
		// The publisher may have written the value with unsafe attributes and
		// then failed to delete it, so contain it now instead of waiting for the
		// next install: remove the variable and revoke the ownership-verified
		// trigger on a bounded context detached from ctx's cancellation. The
		// ledger keeps every obligation that did not complete.
		cctx, cancel := gitlabCleanupContext(ctx)
		defer cancel()
		if len(op.st.Retired) > 0 {
			return errors.Join(pubErr, op.containFailedReplacementPublication(cctx))
		}
		op.st.PublicationPending = false
		return errors.Join(pubErr, op.removeRevokeAndSave(cctx, true))
	}
	op.st.TriggerPublished = true
	op.st.PublicationPending = false
	return op.save(ctx)
}

func (op *gitLabTriggerAccountOp) createWithBootstrap(ctx context.Context, keyOK, triggerOK bool) (*pendingTrigger, error) {
	o, r := op.opts.Owner, op.opts.Repo
	op.st.Elevated = true
	if err := op.save(ctx); err != nil {
		return nil, err
	}
	if err := op.ensureMembership(ctx, forge.GitLabAccessLevelMaintainer); err != nil {
		return nil, err
	}
	// Record the obligation before the create call: a lost response leaves an
	// active PAT whose ID is unknown, and only this marker keeps the next run
	// from treating that PAT as foreign once demotion has succeeded.
	op.st.PATPending = true
	if err := op.save(ctx); err != nil {
		return nil, err
	}
	expires := op.opts.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	pat, err := op.opts.API.CreateServiceAccountPAT(ctx, o, r, op.st.AccountID, op.st.patName(), gitLabTriggerBootstrapPATScopes, expires)
	if err != nil {
		return nil, safeAPIError("creating the GitLab trigger bootstrap token", err)
	}
	if pat == nil || pat.ID <= 0 || pat.Token == "" {
		return nil, errors.New("creating the GitLab trigger bootstrap token: response carried no token")
	}
	op.st.PATID = pat.ID
	if err := op.save(ctx); err != nil {
		return nil, err
	}
	client, err := op.opts.Bootstrap(pat.Token)
	if err != nil {
		return nil, safeAPIError("authenticating as the GitLab trigger account", err)
	}
	if !keyOK {
		if err := op.createDeployKey(ctx, client); err != nil {
			return nil, err
		}
	}
	if !triggerOK {
		return op.createTrigger(ctx, client)
	}
	return nil, nil
}

func (op *gitLabTriggerAccountOp) createDeployKey(ctx context.Context, client GitLabTriggerBootstrapAPI) error {
	pub, priv, err := ed25519.GenerateKey(op.opts.Rand)
	if err != nil {
		return fmt.Errorf("generating the trigger deploy key: %w", err)
	}
	// The private key is never needed: the key only names the trigger
	// account in the protected branch's push list.
	clear(priv)
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return fmt.Errorf("encoding the trigger deploy key: %w", err)
	}
	op.st.DeployKeyFingerprint = ssh.FingerprintSHA256(sshPub)
	op.st.DeployKeyID, op.st.GrantEntryID = 0, 0
	if err := op.save(ctx); err != nil {
		return err
	}
	publicKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	key, err := client.CreateProjectDeployKey(ctx, op.opts.Owner, op.opts.Repo, op.st.deployKeyTitle(), publicKey, true)
	if err != nil {
		return safeAPIError("creating the trigger deploy key", err)
	}
	if key.ID <= 0 {
		return errors.New("creating the trigger deploy key: response carried no key ID")
	}
	if key.FingerprintSHA256 != "" && !sameDeployKeyFingerprint(op.st.DeployKeyFingerprint, key.FingerprintSHA256) {
		return fmt.Errorf("created deploy key %d reports an unexpected fingerprint", key.ID)
	}
	op.st.DeployKeyID = key.ID
	if err := op.save(ctx); err != nil {
		return err
	}
	if !key.CanPush {
		return fmt.Errorf("created deploy key %d is not push-capable; it would not grant the trigger account push on the protected branch", key.ID)
	}
	return nil
}

// createTrigger creates the trigger token and records its ID. The value is
// returned, not published: the caller publishes it once the account is back
// at Developer.
func (op *gitLabTriggerAccountOp) createTrigger(ctx context.Context, client GitLabTriggerBootstrapAPI) (*pendingTrigger, error) {
	if err := op.revokeUnpublishedTriggers(ctx); err != nil {
		return nil, err
	}
	tok, err := client.CreatePipelineTriggerToken(ctx, op.opts.Owner, op.opts.Repo, op.st.triggerDescription())
	if err != nil {
		return nil, safeAPIError("creating the pipeline trigger token", err)
	}
	if tok == nil || tok.ID <= 0 || tok.Token == "" {
		return nil, errors.New("creating the pipeline trigger token: response carried no token")
	}
	op.st.TriggerTokenID = tok.ID
	if err := op.save(ctx); err != nil {
		return nil, err
	}
	return &pendingTrigger{id: tok.ID, value: tok.Token}, nil
}

// endBootstrap revokes every bootstrap PAT and returns the account to
// Developer. The revocation, the demotion and the ledger write each run on
// their own bounded context detached from ctx's cancellation, so a failed,
// canceled or deadline-exhausting step never skips the next one. Each
// obligation stays recorded until its own action succeeds, and every failure
// is joined into the result.
func (op *gitLabTriggerAccountOp) endBootstrap(ctx context.Context) error {
	step := func(fn func(context.Context) error) error {
		cctx, cancel := gitlabCleanupContext(ctx)
		defer cancel()
		return fn(cctx)
	}
	errs := []error{step(op.revokeBootstrapPATs)}
	if err := step(func(c context.Context) error {
		return op.ensureMembership(c, forge.GitLabAccessLevelDeveloper)
	}); err != nil {
		errs = append(errs, err)
	} else {
		op.st.Elevated = false
	}
	// Persist whatever was resolved, including a PAT cleared in memory whose
	// own ledger write failed.
	errs = append(errs, step(op.save))
	return errors.Join(errs...)
}

func (op *gitLabTriggerAccountOp) ensureGrant(ctx context.Context) error {
	o, r, b := op.opts.Owner, op.opts.Repo, op.st.Branch
	if err := op.opts.API.GrantProtectedBranchPushDeployKey(ctx, o, r, b, op.st.DeployKeyID); err != nil {
		return safeAPIError("granting the trigger deploy key push on "+b, err)
	}
	entry, err := op.opts.API.ProtectedBranchPushDeployKeyEntryID(ctx, o, r, b, op.st.DeployKeyID)
	if err != nil {
		return safeAPIError("reading the trigger deploy key grant on "+b, err)
	}
	if entry <= 0 {
		return fmt.Errorf("the trigger deploy key grant on %q could not be read back", b)
	}
	if entry != op.st.GrantEntryID {
		op.st.GrantEntryID = entry
		return op.save(ctx)
	}
	return nil
}

// RemoveGitLabTriggerAccount removes every resource the ledger proves
// fullsend owns, in reverse dependency order, and then deletes the ledger.
// A failure leaves the ledger in the removing phase so a re-run resumes.
// Resources that fail an ownership check are not removed and the ledger is
// kept, so the obligation is never forgotten.
func RemoveGitLabTriggerAccount(ctx context.Context, opts GitLabTriggerAccountOptions) error {
	if err := requireGitLabTriggerLease(opts); err != nil {
		return err
	}
	op, err := newGitLabTriggerAccountOp(ctx, opts)
	if err != nil {
		return err
	}
	op.uninstalling = true
	// Withdraw the dispatch activation before any resource it relies on is
	// removed, whether or not a ledger remains, so no pipeline selects the
	// child-pipeline route while the account is being torn down.
	if err := op.clearDispatchActive(ctx); err != nil {
		return err
	}
	if !op.exists {
		return nil
	}
	op.st.Phase = gitLabTriggerPhaseRemoving
	if err := op.save(ctx); err != nil {
		return err
	}
	// Contain an interrupted bootstrap first, so a teardown failure below
	// cannot leave the account elevated with a live bootstrap PAT.
	if err := op.containBootstrap(ctx); err != nil {
		return err
	}
	if err := op.removeSink(ctx); err != nil {
		// A failed variable deletion must not leave any generation's trigger
		// live: revoke the ownership-verified triggers of every published
		// generation independently, report both failures, and keep the ledger
		// (and its removing phase) so a retry resumes.
		return errors.Join(err, op.removeRevokeAndSave(ctx, false))
	}
	if err := op.retirePrevious(ctx); err != nil {
		return err
	}
	if err := op.removeProtectedRefResources(ctx); err != nil {
		return err
	}
	if err := op.removeAccount(ctx); err != nil {
		return err
	}
	if err := op.opts.State.DeleteRepoVariable(ctx, op.opts.Owner, op.opts.Repo, forge.VarGitLabTriggerAccount); err != nil && !forge.IsNotFound(err) {
		return safeAPIError("deleting "+forge.VarGitLabTriggerAccount, err)
	}
	return nil
}

// removeProtectedRefResources revokes the trigger token, the branch grant
// and the deploy key, clearing each from the ledger only after it is gone.
func (op *gitLabTriggerAccountOp) removeProtectedRefResources(ctx context.Context) error {
	if err := op.revokeUnpublishedTriggers(ctx); err != nil {
		return err
	}
	o, r, b := op.opts.Owner, op.opts.Repo, op.st.Branch
	// Prove the recorded key is fullsend's before touching any grant that
	// names it: a mismatch preserves both the key and its grant.
	var key GitLabDeployKey
	keyFound := false
	if op.st.DeployKeyFingerprint != "" {
		var err error
		key, keyFound, err = op.findOwnedDeployKey(ctx)
		if err != nil {
			return err
		}
	}
	if op.st.DeployKeyID != 0 {
		entry := op.st.GrantEntryID
		if entry == 0 {
			var err error
			entry, err = op.opts.API.ProtectedBranchPushDeployKeyEntryID(ctx, o, r, b, op.st.DeployKeyID)
			if err != nil {
				return safeAPIError("reading the trigger deploy key grant on "+b, err)
			}
		}
		if entry != 0 {
			if err := op.opts.API.RevokeProtectedBranchPushDeployKeyEntry(ctx, o, r, b, entry, op.st.DeployKeyID); err != nil {
				return safeAPIError("revoking the trigger deploy key grant on "+b, err)
			}
		}
		op.st.GrantEntryID = 0
		if err := op.save(ctx); err != nil {
			return err
		}
	}
	if op.st.DeployKeyFingerprint == "" {
		return nil
	}
	if keyFound {
		if err := op.opts.API.DeleteProjectDeployKey(ctx, o, r, key.ID); err != nil {
			return safeAPIError("deleting the trigger deploy key", err)
		}
	}
	op.st.DeployKeyID, op.st.DeployKeyFingerprint = 0, ""
	return op.save(ctx)
}

// checkNoForeignAccountState is the pre-deletion counterpart of the install
// checks. It runs after fullsend's own PATs, trigger tokens and deploy key are
// removed, so any PAT, SSH key, schedule or trigger token it finds belongs to
// someone else. Unfinished jobs are deliberately not a blocker: the account
// is already at Developer with its trigger revoked, and rotation relies on
// retiring a generation whose job is still running.
func (op *gitLabTriggerAccountOp) checkNoForeignAccountState(ctx context.Context) error {
	if err := op.checkForeignPATs(ctx); err != nil {
		return err
	}
	return op.checkForeignResources(ctx)
}

func (op *gitLabTriggerAccountOp) removeAccount(ctx context.Context) error {
	acct, found, err := op.findOwnedAccount(ctx)
	if err != nil {
		return err
	}
	if found {
		for _, id := range op.opts.PollerUserIDs {
			if id == acct.ID {
				return fmt.Errorf("GitLab service account %d is a Poller role identity; refusing to delete it as the trigger account", id)
			}
		}
		// Account-scoped cleanup starts only once the account is proven to be
		// the recorded, nonce-named trigger account and not a Poller identity.
		// An account recovered by its nonce name (the create response or the
		// ledger write was lost) is recorded first, so the account-scoped calls
		// below address it and the ledger keeps the obligation.
		if op.st.AccountID == 0 {
			op.st.AccountID = acct.ID
			if err := op.save(ctx); err != nil {
				return err
			}
		}
		// Name-based bootstrap PAT revocation needs a persisted bootstrap
		// obligation, whether or not the account ID was just recovered: a ready
		// account, or one recovered by nonce on an earlier attempt, holds no
		// bootstrap PAT from this lifecycle unless the ledger records one.
		// Without that proof the PAT is left intact and reaches the
		// foreign-PAT check below, which refuses the deletion.
		if op.st.PATID != 0 || op.st.PATPending {
			if err := op.revokeBootstrapPATs(ctx); err != nil {
				return err
			}
		}
		// The managed schedules the account owns are handed back to the
		// installer (rotation and repair keep polling) or, on uninstall,
		// deleted, before the account they run as is deleted.
		if _, err := op.releaseSchedules(ctx, !op.uninstalling); err != nil {
			return err
		}
		// Deleting the account invalidates every credential it owns, so refuse
		// while anything fullsend did not create remains: an active PAT, an SSH
		// key, a schedule or a foreign trigger token. An inventory that cannot
		// be read also refuses, and the ledger keeps the obligation.
		if err := op.checkNoForeignAccountState(ctx); err != nil {
			return err
		}
		if err := op.opts.API.DeleteProjectServiceAccount(ctx, op.opts.Owner, op.opts.Repo, acct.ID); err != nil {
			return safeAPIError("deleting the GitLab trigger service account", err)
		}
	}
	op.st.AccountID, op.st.Elevated, op.st.PATID, op.st.PATPending = 0, false, 0, false
	return op.save(ctx)
}

// ContainGitLabTriggerAccount is containment-only recovery. Under the project
// lease it revokes every recorded bootstrap PAT and returns an account the
// ledger records as elevated to Developer, for the current generation and
// every retired one, and it creates nothing: no account, key, grant, trigger
// or variable. It does not need the Poller inventory, so a malformed or
// unreadable inventory, or a failure in any other install stage, cannot keep
// it from running. It also withdraws a recorded dispatch activation. It is a
// no-op when no ledger exists. A failure is returned
// and the ledger keeps every obligation that was not completed.
func ContainGitLabTriggerAccount(ctx context.Context, opts GitLabTriggerAccountOptions) error {
	if err := requireGitLabTriggerLease(opts); err != nil {
		return err
	}
	if opts.API == nil || opts.State == nil {
		return errors.New("GitLab trigger account: API and state store are required")
	}
	op, err := loadGitLabTriggerAccountOp(ctx, opts)
	if err != nil {
		return err
	}
	// The dispatch activation is withdrawn first: containment runs before every
	// install stage, so a later stage that fails before the lifecycle can
	// withdraw it never leaves a recorded activation behind (#8438). The
	// install re-records it only after the activation gate passes again. A
	// failed withdrawal stops containment's other work from being skipped but
	// is still reported.
	clearErr := op.clearDispatchActive(ctx)
	if !op.exists {
		return clearErr
	}
	errs := []error{clearErr, op.containBootstrap(ctx)}
	for i := range op.st.Retired {
		sub := &gitLabTriggerAccountOp{opts: op.opts, st: op.st.Retired[i], exists: true, retired: true}
		sub.persist = func(ctx context.Context) error {
			op.st.Retired[i] = sub.st
			return op.save(ctx)
		}
		errs = append(errs, sub.containBootstrap(ctx))
	}
	// A published credential is revoked, and its sink removed, when the sink
	// is unsafe or the pipeline-variable override restriction is weakened or
	// unreadable, whether or not any later install stage can converge.
	errs = append(errs, op.containUnsafePublishedSink(ctx))
	return errors.Join(errs...)
}

// SelectedGitLabRepos returns the GitLab repositories an install run with the
// given filter targets, so containment-only recovery can cover every one of
// them before any install stage can fail or skip it.
//
// A manifest entry whose glob cannot be expanded does not hide the others:
// when the whole selection fails, each GitLab entry is resolved on its own, so
// explicitly listed repos and globs that did resolve are still returned. The
// error then describes what could not be resolved, and the caller still
// contains every returned repo. A glob that never resolves cannot name repos,
// so it has nothing to contain.
func SelectedGitLabRepos(ctx context.Context, m *Manifest, clients ForgeClientFactory, filter []string) ([]ResolvedRepo, error) {
	selected, err := selectManifestRepos(ctx, m, clients, filter)
	if err == nil {
		return onlyGitLabRepos(selected), nil
	}
	if m == nil || m.GitLab == nil {
		return nil, err
	}
	var partial []ResolvedRepo
	seen := make(map[string]bool)
	for _, entry := range m.GitLab.Repos {
		platform := *m.GitLab
		platform.Repos = []RepoEntry{entry}
		single := *m
		single.GitHub, single.GitLab = nil, &platform
		resolved, entryErr := selectManifestRepos(ctx, &single, clients, filter)
		if entryErr != nil {
			continue
		}
		for _, rr := range onlyGitLabRepos(resolved) {
			key := strings.ToLower(rr.Owner + "/" + rr.Repo)
			if !seen[key] {
				seen[key] = true
				partial = append(partial, rr)
			}
		}
	}
	return partial, err
}

func onlyGitLabRepos(selected []ResolvedRepo) []ResolvedRepo {
	var out []ResolvedRepo
	for _, rr := range selected {
		if rr.Forge == ForgeGitLab {
			out = append(out, rr)
		}
	}
	return out
}

// RotateGitLabTriggerAccount replaces the whole trigger identity: a fresh
// generation with its own account, deploy key, branch grant and trigger token.
// The previous generation stays at Developer, is never raised again, and is
// retired with the same ownership checks as uninstall only after the new
// token is published. The ledger records the rotating phase and the retired
// generation in one write, so an interruption at any point is resumed by
// re-running rotate or install.
func RotateGitLabTriggerAccount(ctx context.Context, opts GitLabTriggerAccountOptions) (GitLabTriggerAccountResult, error) {
	if err := requireGitLabTriggerLease(opts); err != nil {
		return GitLabTriggerAccountResult{}, err
	}
	op, err := newGitLabTriggerAccountOp(ctx, opts)
	if err != nil {
		return GitLabTriggerAccountResult{}, err
	}
	if !op.exists || (op.st.Phase != gitLabTriggerPhaseReady && op.st.Phase != gitLabTriggerPhaseRotating) {
		return GitLabTriggerAccountResult{}, errors.New("GitLab trigger account: rotation needs a ready trigger account; run install first")
	}
	if err := op.clearDispatchActive(ctx); err != nil {
		return GitLabTriggerAccountResult{}, err
	}
	if op.st.Branch != opts.Branch {
		if err := op.containUnsafePublishedSink(ctx); err != nil {
			return GitLabTriggerAccountResult{}, err
		}
		res := op.result(false)
		res.Deferred = branchChangeReason(op.st.Branch, opts.Branch)
		return res, nil
	}
	if reason, err := op.webhookSafetyDeferral(ctx); reason != "" || err != nil {
		if cErr := op.containUnsafePublishedSink(ctx); cErr != nil {
			return GitLabTriggerAccountResult{}, errors.Join(err, cErr)
		}
		return GitLabTriggerAccountResult{Deferred: reason}, err
	}
	// Defer before the ledger moves to a replacement generation: a weakened
	// project must not start a rotation that would publish a new credential.
	if reason, err := op.overrideRoleDeferral(ctx); err != nil || reason != "" {
		// An unreadable role is unverified, so the published credential is
		// contained then as well; containment re-reads the role itself.
		if cErr := op.containUnsafePublishedSink(ctx); cErr != nil {
			return GitLabTriggerAccountResult{}, errors.Join(err, cErr)
		}
		return GitLabTriggerAccountResult{Deferred: reason}, err
	}
	if op.st.Phase != gitLabTriggerPhaseRotating {
		op.st.Phase = gitLabTriggerPhaseRotating
		var err error
		if op.st.PublishStarted {
			// The distributed identity is never raised again: the replacement
			// is a fresh generation, and the old one is retired after the new
			// value is published.
			err = op.startFreshGeneration(ctx)
		} else {
			err = op.save(ctx)
		}
		if err != nil {
			return GitLabTriggerAccountResult{}, err
		}
	}
	// Converge creates the replacement generation, returns the phase to
	// ready, and retires the previous generation.
	return convergeGitLabTriggerAccountLocked(ctx, opts)
}
