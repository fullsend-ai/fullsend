package gitlablifecycle

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

// TriggerAccountAPI adapts the live GitLab client to
// repos.GitLabTriggerAccountAPI. Every call uses installer authority.
type TriggerAccountAPI struct {
	c     *gitlab.LiveClient
	owner *TriggerOwner
}

var _ repos.GitLabTriggerAccountAPI = TriggerAccountAPI{}

func (a TriggerAccountAPI) GetAuthenticatedUserID(ctx context.Context) (int, error) {
	return a.c.GetAuthenticatedUserID(ctx)
}

// NewTriggerAccountAPI binds the trigger-account lifecycle to a live client.
func NewTriggerAccountAPI(admin *gitlab.LiveClient) TriggerAccountAPI {
	return TriggerAccountAPI{c: admin, owner: NewTriggerOwner(admin)}
}

func (a TriggerAccountAPI) ListProjectServiceAccounts(ctx context.Context, owner, repo string) ([]repos.GitLabServiceAccount, error) {
	accounts, err := a.c.ListProjectServiceAccounts(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	out := make([]repos.GitLabServiceAccount, len(accounts))
	for i, sa := range accounts {
		out[i] = repos.GitLabServiceAccount{ID: sa.ID, Name: sa.Name}
	}
	return out, nil
}

func (a TriggerAccountAPI) CreateProjectServiceAccount(ctx context.Context, owner, repo, name string) (repos.GitLabServiceAccount, error) {
	sa, err := a.c.CreateProjectServiceAccount(ctx, owner, repo, name)
	if err != nil {
		return repos.GitLabServiceAccount{}, err
	}
	return repos.GitLabServiceAccount{ID: sa.ID, Name: sa.Name}, nil
}

func (a TriggerAccountAPI) DeleteProjectServiceAccount(ctx context.Context, owner, repo string, userID int) error {
	return a.c.DeleteProjectServiceAccount(ctx, owner, repo, userID)
}

func (a TriggerAccountAPI) GetProjectMemberAccessLevel(ctx context.Context, owner, repo string, userID int64) (int, error) {
	return a.c.GetProjectMemberAccessLevel(ctx, owner, repo, userID)
}

func (a TriggerAccountAPI) AddProjectMember(ctx context.Context, owner, repo string, userID int64, accessLevel int) error {
	return a.c.AddProjectMember(ctx, owner, repo, int(userID), accessLevel)
}

func (a TriggerAccountAPI) UpdateProjectMemberAccessLevel(ctx context.Context, owner, repo string, userID int64, accessLevel int) error {
	return a.c.UpdateProjectMemberAccessLevel(ctx, owner, repo, int(userID), accessLevel)
}

func (a TriggerAccountAPI) CreateServiceAccountPAT(ctx context.Context, owner, repo string, userID int, name string, scopes []string, expiresAt string) (*repos.ProjectAccessToken, error) {
	tok, err := a.c.CreateServiceAccountPAT(ctx, owner, repo, userID, name, scopes, expiresAt)
	if err != nil {
		return nil, err
	}
	return &repos.ProjectAccessToken{ID: tok.ID, Name: tok.Name, Token: tok.Token, Active: tok.Active, ExpiresAt: tok.ExpiresAt, Revoked: tok.Revoked, UserID: tok.UserID}, nil
}

func (a TriggerAccountAPI) ListServiceAccountPATs(ctx context.Context, owner, repo string, userID int) ([]repos.ProjectAccessToken, error) {
	toks, err := a.c.ListServiceAccountPATs(ctx, owner, repo, userID)
	if err != nil {
		return nil, err
	}
	out := make([]repos.ProjectAccessToken, len(toks))
	for i, t := range toks {
		out[i] = repos.ProjectAccessToken{ID: t.ID, Name: t.Name, Active: t.Active, ExpiresAt: t.ExpiresAt, Revoked: t.Revoked, UserID: t.UserID}
	}
	return out, nil
}

func (a TriggerAccountAPI) RevokeServiceAccountPAT(ctx context.Context, owner, repo string, userID, tokenID int) error {
	return a.c.RevokeServiceAccountPAT(ctx, owner, repo, userID, tokenID)
}

func (a TriggerAccountAPI) HasExactProtectedBranchRule(ctx context.Context, owner, repo, branch string) (bool, error) {
	return a.c.HasExactProtectedBranchRule(ctx, owner, repo, branch)
}

func (a TriggerAccountAPI) GrantProtectedBranchPushDeployKey(ctx context.Context, owner, repo, branch string, deployKeyID int) error {
	return a.c.GrantProtectedBranchPushDeployKey(ctx, owner, repo, branch, deployKeyID)
}

func (a TriggerAccountAPI) ProtectedBranchPushDeployKeyEntryID(ctx context.Context, owner, repo, branch string, deployKeyID int) (int, error) {
	return a.c.ProtectedBranchPushDeployKeyEntryID(ctx, owner, repo, branch, deployKeyID)
}

func (a TriggerAccountAPI) RevokeProtectedBranchPushDeployKeyEntry(ctx context.Context, owner, repo, branch string, entryID, deployKeyID int) error {
	return a.c.RevokeProtectedBranchPushDeployKeyEntry(ctx, owner, repo, branch, entryID, deployKeyID)
}

func (a TriggerAccountAPI) ListProjectDeployKeys(ctx context.Context, owner, repo string) ([]repos.GitLabDeployKey, error) {
	keys, err := a.c.ListProjectDeployKeys(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	out := make([]repos.GitLabDeployKey, len(keys))
	for i, k := range keys {
		out[i] = repos.GitLabDeployKey{ID: k.ID, Title: k.Title, FingerprintSHA256: k.FingerprintSHA256, CanPush: k.CanPush}
	}
	return out, nil
}

func (a TriggerAccountAPI) DeleteProjectDeployKey(ctx context.Context, owner, repo string, keyID int) error {
	return a.c.DeleteProjectDeployKey(ctx, owner, repo, keyID)
}

func (a TriggerAccountAPI) ListPipelineTriggerTokens(ctx context.Context, owner, repo string) ([]forge.PipelineTriggerToken, error) {
	return a.c.ListPipelineTriggerTokens(ctx, owner, repo)
}

func (a TriggerAccountAPI) RevokePipelineTriggerToken(ctx context.Context, owner, repo string, tokenID int64) error {
	return a.c.RevokePipelineTriggerToken(ctx, owner, repo, tokenID)
}

func (a TriggerAccountAPI) AccountSSHKeyIDs(ctx context.Context, userID int) ([]int, error) {
	return a.owner.PollerSSHKeyIDs(ctx, int64(userID))
}

func (a TriggerAccountAPI) AccountActiveJobIDs(ctx context.Context, owner, repo string, userID int) ([]int, error) {
	return a.owner.PollerActiveJobIDs(ctx, owner, repo, int64(userID))
}

// AccountScheduleIDs matches the exact owner ID only. A schedule GitLab
// reported without an owner is not attributed to the account here; it is
// reported separately by UnknownOwnerScheduleIDs.
func (a TriggerAccountAPI) AccountScheduleIDs(ctx context.Context, owner, repo string, userID int) ([]int, error) {
	schedules, err := a.c.ListProjectPipelineSchedules(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("listing pipeline schedules: %w", err)
	}
	var ids []int
	for _, s := range schedules {
		if s.OwnerID != 0 && s.OwnerID == userID {
			ids = append(ids, s.ID)
		}
	}
	return ids, nil
}

// UnknownOwnerScheduleIDs returns the schedules GitLab reported without an
// owner.
func (a TriggerAccountAPI) UnknownOwnerScheduleIDs(ctx context.Context, owner, repo string) ([]int, error) {
	schedules, err := a.c.ListProjectPipelineSchedules(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("listing pipeline schedules: %w", err)
	}
	var ids []int
	for _, s := range schedules {
		if s.OwnerID == 0 {
			ids = append(ids, s.ID)
		}
	}
	return ids, nil
}

// bootstrapClient is the client authenticated with the bootstrap PAT. It
// creates what GitLab must record the trigger account as owning.
type bootstrapClient struct{ c *gitlab.LiveClient }

func (b bootstrapClient) CreateProjectDeployKey(ctx context.Context, owner, repo, title, publicKey string, canPush bool) (repos.GitLabDeployKey, error) {
	key, err := b.c.CreateProjectDeployKey(ctx, owner, repo, title, publicKey, canPush)
	if err != nil {
		return repos.GitLabDeployKey{}, err
	}
	return repos.GitLabDeployKey{ID: key.ID, Title: key.Title, FingerprintSHA256: key.FingerprintSHA256, CanPush: key.CanPush}, nil
}

func (b bootstrapClient) CreatePipelineTriggerToken(ctx context.Context, owner, repo, description string) (*forge.PipelineTriggerToken, error) {
	return b.c.CreatePipelineTriggerToken(ctx, owner, repo, description)
}

// CreatePipelineSchedule creates a variable-free schedule owned by the
// trigger account (#8508).
func (b bootstrapClient) CreatePipelineSchedule(ctx context.Context, owner, repo, ref, description, cron string, active bool) (int64, error) {
	return b.c.CreatePipelineScheduleWithActive(ctx, owner, repo, ref, description, cron, nil, active)
}

// VerifiedPollerUserIDs is the authoritative Poller identity inventory the
// trigger account is kept separate from: every managed Poller service account,
// including the current, retiring and abandoned Poller generations recorded in
// FULLSEND_GITLAB_POLLER_GENERATIONS, plus every account that owns an
// administrator-supplied role credential. A project with no Poller account at
// all is a verified empty inventory (ErrNotFound); any other failure to read
// the inventory is returned so the caller stops instead of guessing.
func (o *TriggerOwner) VerifiedPollerUserIDs(ctx context.Context, owner, repo string) ([]int, error) {
	managed, err := o.recordedPollerUserIDs(ctx, owner, repo)
	if err != nil && !forge.IsNotFound(err) {
		return nil, fmt.Errorf("resolving the Poller identity inventory: %w", err)
	}
	excluded, _, err := o.ResolveExcludedAccounts(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("resolving administrator-supplied credential owners: %w", err)
	}
	ids := make([]int, 0, len(managed)+len(excluded))
	for _, id := range slices.Concat(managed, excluded) {
		if id > 0 && !slices.Contains(ids, int(id)) {
			ids = append(ids, int(id))
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// ContainmentOptions builds the options for containment-only recovery
// (repos.ContainGitLabTriggerAccount). It needs no Poller inventory and
// carries no way to create resources: no bootstrap client and no publisher.
// It can only remove the token sink, so a published credential whose
// pipeline-variable override restriction is weakened can be contained.
func (o *TriggerOwner) ContainmentOptions(owner, repo string, lease *repos.GitLabProjectLease) repos.GitLabTriggerAccountOptions {
	return repos.GitLabTriggerAccountOptions{
		Owner: owner, Repo: repo,
		Lease: lease,
		API:   NewTriggerAccountAPI(o.Admin),
		UnpublishTriggerToken: func(ctx context.Context) error {
			err := o.Admin.DeleteRepoSecret(ctx, owner, repo, forge.SecretGitLabTriggerAccountToken)
			if err != nil && !forge.IsNotFound(err) {
				return err
			}
			return nil
		},
		State:                o.Admin,
		EnforceWebhookSafety: true,
	}
}

// TriggerAccountOptions builds the lifecycle options for one project: the
// live API adapter, the verified Poller inventory, the project lease the
// caller holds, the bootstrap client factory, and the trigger-token sink.
//
// The sink is the masked, protected CI/CD variable
// FULLSEND_GITLAB_TRIGGER_ACCOUNT_TOKEN. Scheduled polling uses the account's
// job token, not this variable; the managed webhook embeds it while the
// dispatch activation is recorded (#8509). A value is accepted only after GitLab reports
// the variable masked, protected, not file-typed and not environment-scoped; otherwise it is deleted
// and publication fails, so the ledger keeps the obligation and a re-run
// revokes the token and publishes a replacement. The name is reserved to this
// lifecycle and only ever deleted while the ledger exists.
func (o *TriggerOwner) TriggerAccountOptions(ctx context.Context, owner, repo, branch string, lease *repos.GitLabProjectLease) (repos.GitLabTriggerAccountOptions, error) {
	pollers, err := o.VerifiedPollerUserIDs(ctx, owner, repo)
	if err != nil {
		return repos.GitLabTriggerAccountOptions{}, err
	}
	return o.triggerAccountOptions(owner, repo, branch, pollers, lease), nil
}

// triggerAccountOptions assembles the options around a verified inventory.
func (o *TriggerOwner) triggerAccountOptions(owner, repo, branch string, pollers []int, lease *repos.GitLabProjectLease) repos.GitLabTriggerAccountOptions {
	return repos.GitLabTriggerAccountOptions{
		Owner: owner, Repo: repo, Branch: branch,
		PollerUserIDs:           pollers,
		PollerInventoryVerified: true,
		Lease:                   lease,
		API:                     NewTriggerAccountAPI(o.Admin),
		Bootstrap: func(token string) (repos.GitLabTriggerBootstrapAPI, error) {
			c, err := o.NewClient(token, o.Admin.BaseURL())
			if err != nil {
				return nil, err
			}
			return bootstrapClient{c: c}, nil
		},
		PublishTriggerToken: func(ctx context.Context, _ int64, token string) error {
			return o.publishTriggerToken(ctx, owner, repo, token)
		},
		UnpublishTriggerToken: func(ctx context.Context) error {
			err := o.Admin.DeleteRepoSecret(ctx, owner, repo, forge.SecretGitLabTriggerAccountToken)
			if err != nil && !forge.IsNotFound(err) {
				return err
			}
			return nil
		},
		State:                o.Admin,
		EnforceWebhookSafety: true,
	}
}

func (o *TriggerOwner) publishTriggerToken(ctx context.Context, owner, repo, token string) error {
	name := forge.SecretGitLabTriggerAccountToken
	if err := o.Admin.CreateRepoSecret(ctx, owner, repo, name, token); err != nil {
		return err
	}
	prot, err := o.Admin.GetRepoSecretProtection(ctx, owner, repo, name)
	if err == nil && prot.Exists && prot.Masked && prot.Protected && !prot.FileType && !prot.EnvironmentScoped {
		return nil
	}
	// Never leave a credential in a sink that is not protected.
	cause := errors.New("the trigger token variable is not masked and protected")
	if err != nil {
		cause = err
	}
	if delErr := o.Admin.DeleteRepoSecret(ctx, owner, repo, name); delErr != nil && !forge.IsNotFound(delErr) {
		return errors.Join(cause, delErr)
	}
	return cause
}

var _ repos.GitLabTriggerAccountUninstaller = UninstallTokenClient{}

// RemoveGitLabTriggerAccountForUninstall removes the trigger account's
// resources under the project lease the uninstall holds. It does nothing when
// no ledger exists, and it fails closed, keeping FULLSEND_GITLAB_TRIGGER_ACCOUNT,
// when the Poller inventory cannot be verified or any removal step fails.
func (u UninstallTokenClient) RemoveGitLabTriggerAccountForUninstall(ctx context.Context, client forge.Client, lease *repos.GitLabProjectLease, owner, repo string) error {
	// Contain before the Poller inventory is read: an unreadable inventory
	// must not leave an interrupted bootstrap PAT live or the account elevated.
	if err := u.ContainGitLabTriggerAccountForUninstall(ctx, client, lease, owner, repo); err != nil {
		return err
	}
	_, exists, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabTriggerAccount)
	if err != nil {
		return repos.SafeAPIError("reading "+forge.VarGitLabTriggerAccount, err)
	}
	if !exists {
		return nil
	}
	opts, err := u.owner.TriggerAccountOptions(ctx, owner, repo, "", lease)
	if err != nil {
		return err
	}
	return repos.RemoveGitLabTriggerAccount(ctx, opts)
}

// ContainGitLabTriggerAccountForUninstall revokes an interrupted bootstrap's
// tokens and restores Developer access under the project lease the uninstall
// holds. It needs no Poller inventory, creates nothing and removes nothing.
func (u UninstallTokenClient) ContainGitLabTriggerAccountForUninstall(ctx context.Context, client forge.Client, lease *repos.GitLabProjectLease, owner, repo string) error {
	_, exists, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabTriggerAccount)
	if err != nil {
		return repos.SafeAPIError("reading "+forge.VarGitLabTriggerAccount, err)
	}
	if !exists {
		return nil
	}
	return repos.ContainGitLabTriggerAccount(ctx, u.owner.ContainmentOptions(owner, repo, lease))
}
