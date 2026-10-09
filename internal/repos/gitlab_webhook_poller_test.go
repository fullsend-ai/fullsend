package repos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const webhookPollerUserID = 4004

// fakeTriggerOwner models the Poller service account on a webhookFake:
// membership changes update the fake's effective access map, and tokens
// it creates are owned by the Poller.
type fakeTriggerOwner struct {
	c   webhookFake
	uid int64

	uidErr       error
	managedErr   error // returned by ManagedPollerUserIDs
	createErr    error
	elevateErr   error
	restoreErrs  int   // number of Developer updates that fail
	restoreLevel int   // when set, a "successful" restore leaves this level
	ownerID      int64 // when set, overrides the minted token's owner

	// cancelAfterCreate, when set, cancels the operation context right
	// after the trigger token is created (the elevation window).
	cancelAfterCreate context.CancelFunc

	levelAtCreate int
	sets          []int

	// triggersAtCreate is the managed trigger inventory when the Poller
	// holds Maintainer access for the create call.
	triggersAtCreate []forge.PipelineTriggerToken
	restoreErrMsg    string // text of a failed Developer update; defaults to a generic one

	containErr error
	contained  []int64 // user IDs whose credential was contained

	quiescenceErr error // no server-side drain guarantee
	unsafeErr     error // returned by VerifyPollerElevationSafe
	verified      int   // number of safety verifications

	// Credential lifecycle model (#8083). runtimeActive is the distributed
	// runtime credential, bootstrapActive the installer-only bootstrap one.
	runtimeActive       bool
	bootstrapActive     bool
	runtimeRevokeErr    error
	bootstrapCreateErr  error
	bootstrapRevokeErrs int // number of bootstrap revocations that fail
	// bootstrapRevokeSkip is the number of initial bootstrap revocations that
	// succeed before bootstrapRevokeErrs applies (the install's orphan sweep
	// runs first).
	bootstrapRevokeSkip int
	runtimeCreateErr    error
	// managedIDs is the managed Poller inventory returned by
	// ManagedPollerUserIDs; bootstrapRevokedFor records the accounts whose
	// bootstrap credentials were revoked.
	managedIDs          []int64
	bootstrapRevokedFor []int64
	// runtimeToken overrides the replacement runtime credential value.
	runtimeToken string
	// events is the ordered credential and membership event log.
	events []string
	// runtimeActiveAtElevate and bootstrapActiveAtPublish capture the
	// credential state at the moments that matter.
	runtimeActiveAtElevate   bool
	bootstrapActiveAtPublish bool
	levelAtPublish           int
	hooksAtPublish           int
	bootstrapAtTrigger       string
	published                int
	revokedRuntime           int
}

const fakeBootstrapToken = "fake-bootstrap-credential-value"

func (f *fakeTriggerOwner) VerifyPollerElevationSafe(context.Context, string, string, int64) error {
	f.verified++
	return f.unsafeErr
}

func (f *fakeTriggerOwner) VerifyPollerCredentialsRevoked(ctx context.Context, owner, repo string, uid int64) error {
	return f.VerifyPollerElevationSafe(ctx, owner, repo, uid)
}

// The fake models a synchronous server: no requests or jobs complete later.
func (f *fakeTriggerOwner) VerifyPollerQuiescence(context.Context, string, string, int64) error {
	return f.quiescenceErr
}

func (f *fakeTriggerOwner) ContainPoller(ctx context.Context, _, _ string, userID int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.contained = append(f.contained, userID)
	f.runtimeActive = false
	f.bootstrapActive = false
	return f.containErr
}

func (f *fakeTriggerOwner) CreatePollerBootstrap(context.Context, string, string, int64) (*PollerBootstrap, error) {
	f.events = append(f.events, "create-bootstrap")
	if f.bootstrapCreateErr != nil {
		return nil, f.bootstrapCreateErr
	}
	f.bootstrapActive = true
	return &PollerBootstrap{ID: 9001, Token: fakeBootstrapToken}, nil
}

func (f *fakeTriggerOwner) ManagedPollerUserIDs(context.Context, string, string) ([]int64, error) {
	if f.managedErr != nil {
		return nil, f.managedErr
	}
	if len(f.managedIDs) == 0 {
		return nil, forge.ErrNotFound
	}
	return f.managedIDs, nil
}

func (f *fakeTriggerOwner) RevokePollerBootstrap(_ context.Context, _, _ string, uid int64) error {
	f.bootstrapRevokedFor = append(f.bootstrapRevokedFor, uid)
	f.events = append(f.events, "revoke-bootstrap")
	if f.bootstrapRevokeSkip > 0 {
		f.bootstrapRevokeSkip--
	} else if f.bootstrapRevokeErrs > 0 {
		f.bootstrapRevokeErrs--
		return errors.New("500 internal error")
	}
	f.bootstrapActive = false
	return nil
}

func (f *fakeTriggerOwner) RevokePollerRuntimeCredentials(ctx context.Context, owner, repo string, _ int64) error {
	f.events = append(f.events, "revoke-runtime")
	if f.runtimeRevokeErr != nil {
		return f.runtimeRevokeErr
	}
	f.revokedRuntime++
	f.runtimeActive = false
	return f.c.DeleteRepoSecret(ctx, owner, repo, forge.SecretGitLabPollerToken)
}

func (f *fakeTriggerOwner) CreatePollerRuntimeToken(context.Context, string, string, int64, string) (*PollerRuntimeToken, error) {
	f.events = append(f.events, "publish-runtime")
	f.bootstrapActiveAtPublish = f.bootstrapActive
	f.levelAtPublish = f.c.ProjectMemberAccess[f.uid]
	f.hooksAtPublish = len(f.c.hooks())
	if f.runtimeCreateErr != nil {
		return nil, f.runtimeCreateErr
	}
	f.published++
	f.runtimeActive = true
	if f.runtimeToken != "" {
		return &PollerRuntimeToken{ID: 9002, Token: f.runtimeToken}, nil
	}
	return &PollerRuntimeToken{ID: 9002, Token: "glpat-new-runtime-token"}, nil
}

func newPollerOwner(c webhookFake) *fakeTriggerOwner {
	c.ProjectMemberAccess[webhookPollerUserID] = forge.GitLabAccessLevelDeveloper
	c.VariableValues[webhookTestOwner+"/"+webhookTestRepo+"/"+forge.SecretGitLabPollerToken] = "glpat-old-runtime-token"
	return &fakeTriggerOwner{c: c, uid: webhookPollerUserID, runtimeActive: true}
}

func (f *fakeTriggerOwner) PollerUserID(context.Context, string, string) (int64, error) {
	return f.uid, f.uidErr
}

func (f *fakeTriggerOwner) CreatePipelineTriggerTokenAsPoller(ctx context.Context, owner, repo, description string, bootstrap *PollerBootstrap) (*forge.PipelineTriggerToken, error) {
	f.events = append(f.events, "create-trigger")
	if bootstrap != nil {
		f.bootstrapAtTrigger = bootstrap.Token
	}
	f.levelAtCreate = f.c.ProjectMemberAccess[f.uid]
	f.triggersAtCreate = f.c.triggers()
	if f.createErr != nil {
		return nil, f.createErr
	}
	prev := f.c.TriggerTokenOwnerID
	f.c.TriggerTokenOwnerID = f.uid
	if f.ownerID != 0 {
		f.c.TriggerTokenOwnerID = f.ownerID
	}
	defer func() { f.c.TriggerTokenOwnerID = prev }()
	tok, err := f.c.CreatePipelineTriggerToken(ctx, owner, repo, description)
	if f.cancelAfterCreate != nil {
		f.cancelAfterCreate()
	}
	return tok, err
}

func (f *fakeTriggerOwner) SetProjectMemberAccessLevel(ctx context.Context, _, _ string, userID int64, level int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.sets = append(f.sets, level)
	if level == forge.GitLabAccessLevelMaintainer {
		f.events = append(f.events, "elevate")
		f.runtimeActiveAtElevate = f.runtimeActive
	} else {
		f.events = append(f.events, "restore")
	}
	if level == forge.GitLabAccessLevelMaintainer && f.elevateErr != nil {
		return f.elevateErr
	}
	if level == forge.GitLabAccessLevelDeveloper && f.restoreErrs > 0 {
		f.restoreErrs--
		if f.restoreErrMsg != "" {
			return errors.New(f.restoreErrMsg)
		}
		return errors.New("500 internal error")
	}
	if level == forge.GitLabAccessLevelDeveloper && f.restoreLevel != 0 {
		level = f.restoreLevel
	}
	f.c.ProjectMemberAccess[userID] = level
	return nil
}

func ensureWebhookAsPoller(c webhookFake, to GitLabTriggerOwner, rotate bool) (GitLabWebhookResult, error) {
	return EnsureGitLabWebhookFastPathWithTriggerOwner(context.Background(), c, to, webhookTestBase, webhookTestOwner, webhookTestRepo, rotate, false)
}

func TestEnsureGitLabWebhookFastPath_PollerOwnsTrigger(t *testing.T) {
	c := newWebhookFake()
	// The admin identity is a Maintainer: a trigger it minted would be
	// rejected, so the fast path depends on the Poller owning the token.
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	po := newPollerOwner(c)

	res, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err)

	assert.Equal(t, "update", res.Action)
	assert.Equal(t, forge.GitLabAccessLevelMaintainer, po.levelAtCreate, "the trigger is created under temporary Maintainer access")
	assert.Equal(t, []int{forge.GitLabAccessLevelMaintainer, forge.GitLabAccessLevelDeveloper}, po.sets)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID], "Developer access is restored")
	require.Len(t, c.triggers(), 1)
	assert.Equal(t, int64(webhookPollerUserID), c.triggers()[0].OwnerID)
	require.Len(t, c.hooks(), 1, "the webhook is enabled once the Poller owns the trigger")
	assert.Equal(t, c.CreatedTriggerTokens[0].Token, c.variable(forge.SecretTriggerToken))
	assert.Equal(t, forge.PipelineVarOverrideNoOneAllowed, c.PipelineVarOverrideRoles[webhookTestOwner+"/"+webhookTestRepo])
	assert.Contains(t, strings.Join(res.Details, "\n"), "restored and verified Developer access")
	assertNoCredentialLeak(t, c, res)

	// A reinstall reuses the Poller-owned trigger without re-elevating.
	again, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err)
	assert.Equal(t, "none", again.Action)
	assert.Len(t, po.sets, 2)
}

func TestEnsureGitLabWebhookFastPath_PollerUnavailableMintsAsAdmin(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
	po := newPollerOwner(c)
	po.uidErr = fmt.Errorf("reading poller token: %w", forge.ErrNotFound)

	res, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err)
	assert.Equal(t, "update", res.Action)
	assert.Empty(t, po.sets)
	assert.Equal(t, int64(webhookDeveloperUserID), c.triggers()[0].OwnerID)
}

func TestEnsureGitLabWebhookFastPath_PollerUserLookupError(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.uidErr = errors.New("401 unauthorized")

	_, err := ensureWebhookAsPoller(c, po, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving the GitLab Poller identity")
	assert.Empty(t, c.CreatedTriggerTokens)
	assert.Empty(t, c.hooks())
}

func TestEnsureGitLabWebhookFastPath_PollerElevationRefusedDefers(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	po := newPollerOwner(c)
	po.elevateErr = fmt.Errorf("update member: %w", forge.ErrForbidden)

	res, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err, "a project access token Poller defers nonfatally")
	assert.Equal(t, "deferred", res.Action)
	assert.Empty(t, c.CreatedTriggerTokens, "no admin-owned trigger is minted as a fallback")
	assert.Empty(t, c.hooks())
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])
	joined := strings.Join(res.Details, "\n")
	assert.Contains(t, joined, "temporary Maintainer access")
	assert.Contains(t, joined, "service account")
	assertNoCredentialLeak(t, c, res)
}

// Elevation is refused, without any membership change, when the Poller
// credential's storage or token inventory cannot be verified as safe.
func TestEnsureGitLabWebhookFastPath_PollerUnsafeStorageRefusesElevation(t *testing.T) {
	for name, unsafeErr := range map[string]error{
		"unsafe storage":     &PollerElevationUnsafeError{Reason: "the variable is not masked"},
		"unverifiable":       errors.New("500 internal error"),
		"extra active token": &PollerElevationUnsafeError{Reason: "another active token"},
	} {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
			po := newPollerOwner(c)
			po.unsafeErr = unsafeErr

			res, err := ensureWebhookAsPoller(c, po, false)
			require.NoError(t, err)
			assert.Equal(t, "deferred", res.Action)
			assert.Equal(t, 1, po.verified)
			assert.Empty(t, po.sets, "the Poller membership is never changed")
			assert.Empty(t, c.CreatedTriggerTokens)
			assert.Empty(t, c.hooks())
			assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])
			assert.Contains(t, strings.Join(res.Details, "\n"), "was not raised to temporary Maintainer access")
			assertNoCredentialLeak(t, c, res)
		})
	}
}

func TestEnsureGitLabWebhookFastPath_PollerElevationErrorFails(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.elevateErr = errors.New("500 internal error")

	_, err := ensureWebhookAsPoller(c, po, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "temporary Maintainer access")
	assert.Empty(t, c.CreatedTriggerTokens)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])
}

func TestEnsureGitLabWebhookFastPath_PollerCreateFailureRestoresDeveloper(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.createErr = errors.New("400 bad request")

	_, err := ensureWebhookAsPoller(c, po, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating pipeline trigger token as the Poller identity")
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])
	assert.Empty(t, c.hooks())
}

func TestEnsureGitLabWebhookFastPath_PollerRestoreRetriedOnce(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.restoreErrs = 1

	res, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err)
	assert.Equal(t, "update", res.Action)
	assert.Equal(t, []int{forge.GitLabAccessLevelMaintainer, forge.GitLabAccessLevelDeveloper, forge.GitLabAccessLevelDeveloper}, po.sets)
	require.Len(t, c.hooks(), 1)
}

func TestEnsureGitLabWebhookFastPath_PollerRestoreFailureDisablesFastPath(t *testing.T) {
	for name, setup := range map[string]func(*fakeTriggerOwner){
		"restore fails":      func(po *fakeTriggerOwner) { po.restoreErrs = 2 },
		"verify still above": func(po *fakeTriggerOwner) { po.restoreLevel = forge.GitLabAccessLevelMaintainer },
	} {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			ownedBy(c, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
			// An existing compliant fast path is disabled too: the Poller
			// may still hold Maintainer access.
			ensureWebhook(t, c, false, false)
			require.Len(t, c.hooks(), 1)
			po := newPollerOwner(c)
			setup(po)

			res, err := ensureWebhookAsPoller(c, po, true)
			require.Error(t, err)
			var restoreErr *gitlabPollerRestoreError
			require.ErrorAs(t, err, &restoreErr)
			assert.Contains(t, err.Error(), "Set that member's project role to Developer")
			assert.Contains(t, err.Error(), "polling and webhook dispatch are unavailable")
			assert.NotContains(t, err.Error(), "remain in effect")
			assert.Equal(t, "deferred", res.Action)
			assert.Empty(t, c.triggers(), "every managed trigger, including the new one, is revoked")
			assert.Empty(t, c.hooks(), "the managed webhook is removed")
			assert.Equal(t, []int64{webhookPollerUserID}, po.contained, "the Poller credential is contained")
			for _, tok := range c.CreatedTriggerTokens {
				assert.NotContains(t, err.Error(), tok.Token)
			}
		})
	}
}

func TestEnsureGitLabWebhookFastPath_PollerOwnerMismatchRevoked(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.ownerID = webhookMaintainerUserID

	_, err := ensureWebhookAsPoller(c, po, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reports owner user ID")
	require.Len(t, c.CreatedTriggerTokens, 1)
	assert.Contains(t, c.RevokedTriggerTokenIDs, c.CreatedTriggerTokens[0].ID)
	assert.Empty(t, c.hooks())
}

func TestEnsureGitLabWebhookFastPath_PollerAboveDeveloperCorrectedFirst(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	c.ProjectMemberAccess[webhookPollerUserID] = forge.GitLabAccessLevelMaintainer

	res, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err)
	assert.Equal(t, "update", res.Action)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, po.sets[0])
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])

	// When the stale elevation cannot be corrected, nothing is minted.
	c2 := newWebhookFake()
	po2 := newPollerOwner(c2)
	c2.ProjectMemberAccess[webhookPollerUserID] = forge.GitLabAccessLevelOwner
	po2.restoreErrs = 2
	_, err = ensureWebhookAsPoller(c2, po2, false)
	var restoreErr *gitlabPollerRestoreError
	require.ErrorAs(t, err, &restoreErr)
	assert.Empty(t, c2.CreatedTriggerTokens)
}

func TestEnsureGitLabWebhookFastPath_PollerWithoutDeveloperDefers(t *testing.T) {
	for name, setup := range map[string]func(webhookFake){
		"not a member": func(c webhookFake) { delete(c.ProjectMemberAccess, webhookPollerUserID) },
		"reporter":     func(c webhookFake) { c.ProjectMemberAccess[webhookPollerUserID] = 20 },
	} {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			po := newPollerOwner(c)
			setup(c)

			res, err := ensureWebhookAsPoller(c, po, false)
			require.NoError(t, err)
			assert.Equal(t, "deferred", res.Action)
			assert.Empty(t, po.sets)
			assert.Empty(t, c.CreatedTriggerTokens)
			assert.Contains(t, strings.Join(res.Details, "\n"), "Poller identity")
		})
	}
}

func TestEnsureGitLabWebhookFastPath_PollerAccessLookupError(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	c.Errors = map[string]error{"GetProjectMemberAccessLevel": errors.New("500 internal error")}

	res, err := ensureWebhookAsPoller(c, po, false)
	var restoreErr *gitlabPollerRestoreError
	require.ErrorAs(t, err, &restoreErr, "an unverifiable Poller is restored and, failing verification, contained")
	assert.Equal(t, "deferred", res.Action)
	assert.Equal(t, []int64{webhookPollerUserID}, po.contained)
	assert.Empty(t, c.CreatedTriggerTokens)
}

func TestEnsureGitLabWebhookFastPath_PollerDeferralPreservesCompliantFastPath(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
	ensureWebhook(t, c, false, false)
	po := newPollerOwner(c)
	po.elevateErr = forge.ErrForbidden

	res, err := ensureWebhookAsPoller(c, po, true)
	require.NoError(t, err)
	assert.Equal(t, "deferred", res.Action)
	require.Len(t, c.hooks(), 1, "the working fast path is kept")
	require.Len(t, c.triggers(), 1)
	assert.Contains(t, strings.Join(res.Details, "\n"), "Preserved the existing compliant")
}

// ctxStrictClient fails the requests the Poller flow issues after
// elevation when their context is already canceled, as a real HTTP client
// does.
type ctxStrictClient struct {
	webhookFake
}

func (c ctxStrictClient) GetProjectMemberAccessLevel(ctx context.Context, owner, repo string, userID int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return c.webhookFake.GetProjectMemberAccessLevel(ctx, owner, repo, userID)
}

func (c ctxStrictClient) RevokePipelineTriggerToken(ctx context.Context, owner, repo string, tokenID int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.webhookFake.RevokePipelineTriggerToken(ctx, owner, repo, tokenID)
}

func (c ctxStrictClient) ListPipelineTriggerTokens(ctx context.Context, owner, repo string) ([]forge.PipelineTriggerToken, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.webhookFake.ListPipelineTriggerTokens(ctx, owner, repo)
}

func (c ctxStrictClient) ListProjectHooks(ctx context.Context, owner, repo string) ([]forge.ProjectHook, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.webhookFake.ListProjectHooks(ctx, owner, repo)
}

func (c ctxStrictClient) ListRepoVariables(ctx context.Context, owner, repo string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.webhookFake.ListRepoVariables(ctx, owner, repo)
}

func TestEnsureGitLabWebhookFastPath_PollerRestoredAfterCancellation(t *testing.T) {
	base := newWebhookFake()
	ownedBy(base, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	po := newPollerOwner(base)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	po.cancelAfterCreate = cancel

	_, err := EnsureGitLabWebhookFastPathWithTriggerOwner(ctx, ctxStrictClient{base}, po, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
	require.Error(t, err)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, base.ProjectMemberAccess[webhookPollerUserID],
		"restoration and verification run although the operation context was canceled")
	assert.Equal(t, []int{forge.GitLabAccessLevelMaintainer, forge.GitLabAccessLevelDeveloper}, po.sets)
}

// Canceling during the elevation window must not leave the newly minted
// trigger live: the owner verification fails on the canceled context, and
// the compensating revocation still reaches GitLab.
func TestEnsureGitLabWebhookFastPath_CancellationRevokesNewTriggerOnOwnerVerification(t *testing.T) {
	base := newWebhookFake()
	ownedBy(base, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	po := newPollerOwner(base)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	po.cancelAfterCreate = cancel

	_, err := EnsureGitLabWebhookFastPathWithTriggerOwner(ctx, ctxStrictClient{base}, po, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
	require.Error(t, err)
	assert.Empty(t, base.triggers(), "the new trigger is revoked on a context detached from cancellation")
	assert.Empty(t, base.hooks())
	assert.Empty(t, base.variable(forge.SecretTriggerToken))
}

// A rotation canceled the same way revokes only the rejected replacement and
// preserves the existing compliant fast path.
func TestEnsureGitLabWebhookFastPath_CancellationPreservesCompliantFastPath(t *testing.T) {
	base := newWebhookFake()
	ownedBy(base, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
	ensureWebhook(t, base, false, false)
	require.Len(t, base.triggers(), 1)
	existing := base.triggers()[0].ID
	po := newPollerOwner(base)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	po.cancelAfterCreate = cancel

	res, err := EnsureGitLabWebhookFastPathWithTriggerOwner(ctx, ctxStrictClient{base}, po, webhookTestBase, webhookTestOwner, webhookTestRepo, true, false)
	require.Error(t, err)
	assert.Equal(t, "deferred", res.Action)
	require.Len(t, base.triggers(), 1, "only the rejected replacement is revoked")
	assert.Equal(t, existing, base.triggers()[0].ID)
	require.Len(t, base.hooks(), 1, "the compliant webhook is preserved")
}

// secretCancelClient cancels the operation context while storing the trigger
// secret and fails the store, as when the caller is interrupted mid-request.
type secretCancelClient struct {
	ctxStrictClient
	cancel context.CancelFunc
}

func (c secretCancelClient) CreateRepoSecret(_ context.Context, _, _, name, _ string) error {
	if name == forge.SecretTriggerToken {
		c.cancel()
		return errors.New("storing secret interrupted")
	}
	return nil
}

func TestEnsureGitLabWebhookFastPath_CancellationRevokesNewTriggerOnSecretStoreFailure(t *testing.T) {
	base := newWebhookFake()
	ownedBy(base, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := EnsureGitLabWebhookFastPathWithTriggerOwner(ctx, secretCancelClient{ctxStrictClient{base}, cancel}, nil, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
	require.ErrorContains(t, err, "storing")
	assert.Empty(t, base.triggers(), "the orphaned trigger is revoked on a context detached from cancellation")
}

func TestEnsureGitLabWebhookFastPath_PollerRestoreFailureAfterCancellationTearsDown(t *testing.T) {
	base := newWebhookFake()
	ownedBy(base, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
	po := newPollerOwner(base)
	po.restoreErrs = 2
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	po.cancelAfterCreate = cancel

	res, err := EnsureGitLabWebhookFastPathWithTriggerOwner(ctx, ctxStrictClient{base}, po, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
	var restoreErr *gitlabPollerRestoreError
	require.ErrorAs(t, err, &restoreErr)
	assert.Equal(t, "deferred", res.Action)
	assert.Empty(t, base.triggers(), "the Poller-owned trigger is revoked on a context detached from cancellation")
	assert.Empty(t, base.hooks())
	assert.Equal(t, []int{forge.GitLabAccessLevelMaintainer, forge.GitLabAccessLevelDeveloper, forge.GitLabAccessLevelDeveloper}, po.sets)
	assert.Equal(t, []int64{webhookPollerUserID}, po.contained, "the Poller credential is contained even after cancellation")
}

// A Poller left at Maintainer by an interrupted rotation, with its own
// trigger still live, is restored before safety reconciliation can reject
// the owner and tear the working fast path down.
func TestEnsureGitLabWebhookFastPath_LeftoverElevationRestoredWithExistingTrigger(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	po := newPollerOwner(c)
	_, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err)
	require.Len(t, c.triggers(), 1)
	require.Len(t, c.hooks(), 1)
	po.sets = nil
	c.ProjectMemberAccess[webhookPollerUserID] = forge.GitLabAccessLevelMaintainer

	res, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err)
	assert.Equal(t, "update", res.Action)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])
	assert.Equal(t, []int{forge.GitLabAccessLevelDeveloper}, po.sets)
	assert.Len(t, c.triggers(), 1, "the Poller-owned trigger is kept")
	assert.Len(t, c.hooks(), 1, "the webhook is kept")
	assert.Contains(t, strings.Join(res.Details, "\n"), "Restored the GitLab Poller identity")
	assert.Empty(t, po.contained)
}

// An unmet readiness gate returns before minting, but must not leave the
// installed Poller identity elevated.
func TestEnsureGitLabWebhookFastPath_LeftoverElevationRestoredWhenReadinessUnmet(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	c.ProjectMemberAccess[webhookPollerUserID] = forge.GitLabAccessLevelMaintainer
	c.FileContents[webhookTestOwner+"/"+webhookTestRepo+"/"+fullsendPipelineInclude] = []byte("include: []\n")

	res, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err)
	assert.Equal(t, "deferred", res.Action)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])
	assert.Equal(t, []int{forge.GitLabAccessLevelDeveloper}, po.sets)
	assert.Empty(t, c.CreatedTriggerTokens)
	assert.Contains(t, strings.Join(res.Details, "\n"), "Restored the GitLab Poller identity")
}

// When leftover elevation cannot be corrected ahead of the early returns,
// the fast path is torn down and the Poller credential is contained.
func TestEnsureGitLabWebhookFastPath_LeftoverElevationRestoreFailureContainsPoller(t *testing.T) {
	for name, mutate := range map[string]func(c webhookFake){
		"readiness met": func(webhookFake) {},
		"readiness unmet": func(c webhookFake) {
			c.FileContents[webhookTestOwner+"/"+webhookTestRepo+"/"+fullsendPipelineInclude] = []byte("include: []\n")
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			mutate(c)
			po := newPollerOwner(c)
			c.ProjectMemberAccess[webhookPollerUserID] = forge.GitLabAccessLevelMaintainer
			po.restoreErrs = 2

			res, err := ensureWebhookAsPoller(c, po, false)
			var restoreErr *gitlabPollerRestoreError
			require.ErrorAs(t, err, &restoreErr)
			assert.Equal(t, "deferred", res.Action)
			assert.Equal(t, []int64{webhookPollerUserID}, po.contained)
			assert.Empty(t, c.triggers())
			assert.Empty(t, c.hooks())
			assert.Empty(t, c.CreatedTriggerTokens)
			assert.Contains(t, strings.Join(res.Details, "\n"), "managed personal access tokens")
		})
	}
}

func TestEnsureGitLabWebhookFastPath_PollerContainmentFailureReported(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.restoreErrs = 2
	po.containErr = errors.New("500 internal error")

	res, err := ensureWebhookAsPoller(c, po, false)
	var restoreErr *gitlabPollerRestoreError
	require.ErrorAs(t, err, &restoreErr)
	require.ErrorContains(t, err, "containing the GitLab Poller credential")
	assert.Equal(t, []int64{webhookPollerUserID}, po.contained)
	assert.NotContains(t, strings.Join(res.Details, "\n"), "managed personal access tokens", "a failed containment is not reported as done")
}

// An unmanaged active token left on the Poller account is reported as
// incomplete containment, not as a successful revocation.
func TestEnsureGitLabWebhookFastPath_PollerContainmentIncompleteReported(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.restoreErrs = 2
	po.containErr = fmt.Errorf("%w: 1 active personal access token(s) remain", ErrPollerContainmentIncomplete)

	res, err := ensureWebhookAsPoller(c, po, false)
	require.ErrorIs(t, err, ErrPollerContainmentIncomplete)
	require.ErrorContains(t, err, "does not manage")
	assert.NotContains(t, strings.Join(res.Details, "\n"), "managed personal access tokens", "incomplete containment is not reported as done")
}

// unmetReadiness makes the repository fail the readiness gate, so the fast
// path returns before minting.
func unmetReadiness(c webhookFake) {
	c.FileContents[webhookTestOwner+"/"+webhookTestRepo+"/"+fullsendPipelineInclude] = []byte("include: []\n")
}

// An unexpected identity lookup failure is reported, even when an unmet
// readiness gate would otherwise return early, and changes no membership.
func TestEnsureGitLabWebhookFastPath_PollerIdentityLookupErrorWithEarlyDeferral(t *testing.T) {
	for name, mutate := range map[string]func(c webhookFake){
		"readiness met":   func(webhookFake) {},
		"readiness unmet": unmetReadiness,
	} {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			mutate(c)
			po := newPollerOwner(c)
			c.ProjectMemberAccess[webhookPollerUserID] = forge.GitLabAccessLevelMaintainer
			po.uidErr = errors.New("401 unauthorized")

			_, err := ensureWebhookAsPoller(c, po, false)
			require.ErrorContains(t, err, "resolving the GitLab Poller identity")
			assert.Empty(t, po.sets, "an unidentified account is not modified")
			assert.Empty(t, po.contained)
		})
	}
}

// levelErrClient fails the first n effective-access reads.
type levelErrClient struct {
	webhookFake
	n *int
}

func (c levelErrClient) GetProjectMemberAccessLevel(ctx context.Context, owner, repo string, userID int64) (int, error) {
	if *c.n > 0 {
		*c.n--
		return 0, errors.New("500 internal error")
	}
	return c.webhookFake.GetProjectMemberAccessLevel(ctx, owner, repo, userID)
}

// A managed Poller left at Maintainer whose effective access cannot be read
// is restored and verified even when readiness is unmet, and contained when
// verification keeps failing.
func TestEnsureGitLabWebhookFastPath_PollerAccessLookupErrorWithEarlyDeferral(t *testing.T) {
	t.Run("verification succeeds on restore", func(t *testing.T) {
		c := newWebhookFake()
		unmetReadiness(c)
		po := newPollerOwner(c)
		c.ProjectMemberAccess[webhookPollerUserID] = forge.GitLabAccessLevelMaintainer
		failures := 1
		client := levelErrClient{c, &failures}

		res, err := EnsureGitLabWebhookFastPathWithTriggerOwner(context.Background(), client, po, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
		require.NoError(t, err)
		assert.Equal(t, "deferred", res.Action)
		assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])
		assert.Equal(t, []int{forge.GitLabAccessLevelDeveloper}, po.sets)
		assert.Empty(t, po.contained)
	})

	t.Run("verification fails", func(t *testing.T) {
		c := newWebhookFake()
		unmetReadiness(c)
		po := newPollerOwner(c)
		c.ProjectMemberAccess[webhookPollerUserID] = forge.GitLabAccessLevelMaintainer
		c.Errors = map[string]error{"GetProjectMemberAccessLevel": errors.New("500 internal error")}

		res, err := ensureWebhookAsPoller(c, po, false)
		var restoreErr *gitlabPollerRestoreError
		require.ErrorAs(t, err, &restoreErr)
		assert.Equal(t, "deferred", res.Action)
		assert.Equal(t, []int64{webhookPollerUserID}, po.contained)
		assert.Empty(t, c.triggers())
		assert.Empty(t, c.hooks())
	})
}

// A Poller confirmed to be absent from the project is left alone.
func TestEnsureGitLabWebhookFastPath_PollerNotAMemberUnchanged(t *testing.T) {
	c := newWebhookFake()
	unmetReadiness(c)
	po := newPollerOwner(c)
	delete(c.ProjectMemberAccess, webhookPollerUserID)

	_, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err)
	assert.Empty(t, po.sets)
	assert.Empty(t, po.contained)
}

// teardownBlockClient makes webhook teardown consume its whole deadline: the
// listing request blocks until a deadline-bound context expires.
type teardownBlockClient struct {
	webhookFake
}

func (c teardownBlockClient) ListPipelineTriggerTokens(ctx context.Context, owner, repo string) ([]forge.PipelineTriggerToken, error) {
	if _, bounded := ctx.Deadline(); bounded {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return c.webhookFake.ListPipelineTriggerTokens(ctx, owner, repo)
}

// Credential containment runs on its own budget, so a teardown that exhausts
// its deadline cannot leave the credential usable.
func TestEnsureGitLabWebhookFastPath_ContainmentSurvivesTeardownExhaustion(t *testing.T) {
	prev := gitlabCleanupTimeout
	gitlabCleanupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { gitlabCleanupTimeout = prev })

	c := newWebhookFake()
	po := newPollerOwner(c)
	c.ProjectMemberAccess[webhookPollerUserID] = forge.GitLabAccessLevelMaintainer
	po.restoreErrs = 2

	res, err := EnsureGitLabWebhookFastPathWithTriggerOwner(context.Background(), teardownBlockClient{c}, po, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
	var restoreErr *gitlabPollerRestoreError
	require.ErrorAs(t, err, &restoreErr)
	require.ErrorIs(t, err, context.DeadlineExceeded, "the teardown failure is retained")
	assert.Equal(t, "deferred", res.Action)
	assert.Equal(t, []int64{webhookPollerUserID}, po.contained, "containment still ran with a live context")
	assert.Contains(t, strings.Join(res.Details, "\n"), "managed personal access tokens")
}

// Poller-owned managed triggers (and so the webhook that carries one) must
// not stay live while the Poller is raised to Maintainer.
func TestEnsureGitLabWebhookFastPath_RotationRevokesPollerOwnedTriggerBeforeElevation(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	po := newPollerOwner(c)
	_, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err)
	require.Len(t, c.triggers(), 1)
	oldID := c.triggers()[0].ID
	po.sets = nil

	res, err := ensureWebhookAsPoller(c, po, true)
	require.NoError(t, err)
	assert.Equal(t, forge.GitLabAccessLevelMaintainer, po.levelAtCreate)
	assert.Empty(t, po.triggersAtCreate, "no managed trigger owned by the Poller is live during the elevation")
	assert.Contains(t, c.RevokedTriggerTokenIDs, oldID)
	require.Len(t, c.triggers(), 1)
	assert.NotEqual(t, oldID, c.triggers()[0].ID, "the replacement is minted after the old trigger is gone")
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])
	require.Len(t, c.hooks(), 1, "dispatch resumes once Developer access is restored")
	assert.Contains(t, strings.Join(res.Details, "\n"), "before raising the Poller")
	assertNoCredentialLeak(t, c, res)
}

// Triggers owned by other identities are not touched by the elevation guard.
func TestEnsureGitLabWebhookFastPath_RotationKeepsOtherOwnersTriggersLiveUntilReplaced(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
	ensureWebhook(t, c, false, false)
	require.Len(t, c.triggers(), 1)
	po := newPollerOwner(c)

	_, err := ensureWebhookAsPoller(c, po, true)
	require.NoError(t, err)
	assert.Len(t, po.triggersAtCreate, 1, "a trigger owned by someone else is not revoked ahead of the replacement")
}

func TestEnsureGitLabWebhookFastPath_UnflaggedInstallTransfersTriggerOwnership(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprint(refused), func(t *testing.T) {
			c := newWebhookFake()
			ownedBy(c, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
			ensureWebhook(t, c, false, false)
			oldID := c.triggers()[0].ID
			po := newPollerOwner(c)
			if refused {
				po.elevateErr = forge.ErrForbidden
			}
			_, err := ensureWebhookAsPoller(c, po, false)
			require.NoError(t, err)
			require.Len(t, c.triggers(), 1)
			if refused {
				assert.Equal(t, oldID, c.triggers()[0].ID)
				assert.NotContains(t, c.RevokedTriggerTokenIDs, oldID)
			} else {
				assert.EqualValues(t, webhookPollerUserID, c.triggers()[0].OwnerID)
				assert.Contains(t, c.RevokedTriggerTokenIDs, oldID)
				newID := c.triggers()[0].ID
				_, err = ensureWebhookAsPoller(c, po, false)
				require.NoError(t, err)
				assert.Equal(t, newID, c.triggers()[0].ID)
			}
			assertNoCredentialLeak(t, c, GitLabWebhookResult{})
		})
	}
}

// When elevation is then refused, the revoked trigger left nothing compliant
// to preserve, so the managed fast path is removed.
func TestEnsureGitLabWebhookFastPath_ElevationRefusedAfterRevokingPollerTriggerTearsDown(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	po := newPollerOwner(c)
	_, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err)
	require.Len(t, c.hooks(), 1)
	po.elevateErr = fmt.Errorf("update member: %w", forge.ErrForbidden)

	res, err := ensureWebhookAsPoller(c, po, true)
	require.NoError(t, err)
	assert.Equal(t, "deferred", res.Action)
	assert.Empty(t, c.triggers())
	assert.Empty(t, c.hooks(), "the webhook whose trigger was revoked is removed")
	assert.NotContains(t, strings.Join(res.Details, "\n"), "Preserved")
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])
}

// noopRevokeTriggerClient acknowledges trigger revocation without removing
// the trigger, modeling a successful DELETE after which the trigger is still
// listed.
type noopRevokeTriggerClient struct {
	webhookFake
}

func (c noopRevokeTriggerClient) RevokePipelineTriggerToken(context.Context, string, string, int64) error {
	return nil
}

// A successful revocation response is not proof of absence: a managed
// Poller-owned trigger that is still listed refuses the elevation.
func TestEnsureGitLabWebhookFastPath_RevokedTriggerStillListedRefusesElevation(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	po := newPollerOwner(c)
	_, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err)
	require.Len(t, c.triggers(), 1)
	po.sets = nil

	_, err = EnsureGitLabWebhookFastPathWithTriggerOwner(context.Background(), noopRevokeTriggerClient{c}, po, webhookTestBase, webhookTestOwner, webhookTestRepo, true, false)
	require.Error(t, err)
	assert.ErrorContains(t, err, "still listed after revocation")
	assert.NotContains(t, po.sets, forge.GitLabAccessLevelMaintainer, "the Poller was not raised")
}

var errLevelSentinel = errors.New("effective access read failed")

// Restoration failures must not echo server text, which can carry the
// administrative token or the installed Poller token.
func TestEnsureGitLabWebhookFastPath_RestoreErrorsWithholdServerText(t *testing.T) {
	const adminSecret = "glpat-admin-secret-value"
	const pollerSecret = "glpat-poller-secret-value"

	t.Run("membership update", func(t *testing.T) {
		c := newWebhookFake()
		po := newPollerOwner(c)
		po.restoreErrs = 2
		po.restoreErrMsg = "401 {\"message\":\"bad token " + adminSecret + "\"}"

		_, err := ensureWebhookAsPoller(c, po, false)
		var restoreErr *gitlabPollerRestoreError
		require.ErrorAs(t, err, &restoreErr)
		assert.NotContains(t, err.Error(), adminSecret)
		assert.Contains(t, err.Error(), "server error text withheld")
		assert.ErrorContains(t, restoreErr.Unwrap(), "updating the Poller membership to Developer")
	})

	t.Run("effective access verification", func(t *testing.T) {
		c := newWebhookFake()
		po := newPollerOwner(c)
		c.ProjectMemberAccess[webhookPollerUserID] = forge.GitLabAccessLevelMaintainer
		c.Errors = map[string]error{"GetProjectMemberAccessLevel": fmt.Errorf("500 %s %w", pollerSecret, errLevelSentinel)}

		_, err := ensureWebhookAsPoller(c, po, false)
		var restoreErr *gitlabPollerRestoreError
		require.ErrorAs(t, err, &restoreErr)
		assert.NotContains(t, err.Error(), pollerSecret)
		assert.ErrorIs(t, err, errLevelSentinel, "the original error stays unwrappable")
	})
}

// A failed install still reconciles the Poller: an elevated Poller is
// restored even though webhook setup never ran.
func TestReconcileGitLabWebhookSafetyWithTriggerOwner_RestoresElevatedPoller(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	po := newPollerOwner(c)
	_, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err)
	po.sets = nil
	c.ProjectMemberAccess[webhookPollerUserID] = forge.GitLabAccessLevelMaintainer

	res, err := ReconcileGitLabWebhookSafetyWithTriggerOwner(context.Background(), c, po, webhookTestOwner, webhookTestRepo, false)
	require.NoError(t, err)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, c.ProjectMemberAccess[webhookPollerUserID])
	assert.Equal(t, []int{forge.GitLabAccessLevelDeveloper}, po.sets)
	assert.Empty(t, po.contained)
	assert.Contains(t, strings.Join(res.Details, "\n"), "Restored the GitLab Poller identity")
	assert.Equal(t, "update", res.Action)
}

// When that restoration cannot be verified, the Poller credential is
// contained and the managed fast path is removed.
func TestReconcileGitLabWebhookSafetyWithTriggerOwner_ContainsUnrestorablePoller(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	po := newPollerOwner(c)
	_, err := ensureWebhookAsPoller(c, po, false)
	require.NoError(t, err)
	c.ProjectMemberAccess[webhookPollerUserID] = forge.GitLabAccessLevelMaintainer
	po.restoreErrs = 2

	res, err := ReconcileGitLabWebhookSafetyWithTriggerOwner(context.Background(), c, po, webhookTestOwner, webhookTestRepo, false)
	var restoreErr *gitlabPollerRestoreError
	require.ErrorAs(t, err, &restoreErr)
	assert.Equal(t, "deferred", res.Action)
	assert.Equal(t, []int64{webhookPollerUserID}, po.contained)
	assert.Empty(t, c.triggers())
	assert.Empty(t, c.hooks())
}

// Dry runs change nothing, and a nil trigger owner leaves the Poller alone.
func TestReconcileGitLabWebhookSafetyWithTriggerOwner_DryRunAndNilOwnerLeavePollerAlone(t *testing.T) {
	for name, tc := range map[string]struct {
		dryRun bool
		nilTO  bool
	}{"dry run": {dryRun: true}, "nil owner": {nilTO: true}} {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			po := newPollerOwner(c)
			c.ProjectMemberAccess[webhookPollerUserID] = forge.GitLabAccessLevelMaintainer
			var to GitLabTriggerOwner = po
			if tc.nilTO {
				to = nil
			}
			_, err := ReconcileGitLabWebhookSafetyWithTriggerOwner(context.Background(), c, to, webhookTestOwner, webhookTestRepo, tc.dryRun)
			require.NoError(t, err)
			assert.Equal(t, forge.GitLabAccessLevelMaintainer, c.ProjectMemberAccess[webhookPollerUserID])
			assert.Empty(t, po.sets)
		})
	}
}
