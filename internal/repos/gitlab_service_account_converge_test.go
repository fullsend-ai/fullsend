package repos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstallConvergesLegacyServiceAccounts(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleAnalyst, gitlabroles.RoleCoder)
	// A replacement Poller must gain protected-ref access before publication.
	seedRepo(fc, "group", "project", "main")
	fc.ProtectedBranchRules["group/project/main"] = maintainerOnlyRule("main")
	legacy := &fakeTokens{}
	sa := newFakeSAAPI()
	state := rotationStateFile{Roles: map[string]rotationRoleState{}}
	for i, rec := range gitlabroles.BuiltinRegistry().Registrations() {
		id := i + 1
		legacy.seed(ProjectAccessToken{ID: id, Name: rec.Credential.TokenName, Active: true, ExpiresAt: GitLabPATExpiresAt(now), UserID: 100 + id})
		state.Roles[string(rec.Name)] = rotationRoleState{CreatedTokenIDs: []int{id}, Phase: rotationPhaseIdle, IncomingID: id, DistributedAt: now.Format(time.RFC3339)}
	}
	require.NoError(t, writeRotationState(ctx, fc, "group", "project", state))
	c := ServiceAccountTokenClient{SA: sa, Legacy: legacy, VerifyToken: acceptReplacementToken, ManagedLegacyTokenIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
		return ManagedGitLabLegacyTokenIDs(ctx, fc, owner, repo)
	}}
	c.AccountCreated = func(ctx context.Context, owner, repo string, account GitLabServiceAccount) error {
		return RecordManagedGitLabServiceAccount(ctx, fc, owner, repo, account)
	}
	c.ManagedAccountIDs = func(ctx context.Context, owner, repo string) ([]int, error) {
		return ManagedGitLabRoleAccountIDs(ctx, fc, owner, repo)
	}
	cfg := RoleProvisionConfig{Owner: "group", Repo: "project", Client: fc, Tokens: c, Now: now}
	result, err := ProvisionGitLabRoleCredentials(ctx, cfg)
	require.NoError(t, err)
	require.Empty(t, result.Failed)
	require.Len(t, result.Created, 3)
	require.Len(t, sa.accounts, 3)
	assert.Empty(t, legacy.revoked, "old credentials remain usable for in-flight jobs")
	current, _, err := loadRotationState(ctx, fc, "group", "project")
	require.NoError(t, err)
	for _, rs := range current.Roles {
		assert.Greater(t, rs.ManagedUserID, 500)
		assert.Len(t, rs.OutgoingIDs, 1)
	}
	result, err = ProvisionGitLabRoleCredentials(ctx, cfg)
	require.NoError(t, err)
	require.Empty(t, result.Failed)
	assert.Empty(t, result.Created)
	assert.Len(t, sa.accounts, 3)
	assert.Equal(t, 9003, sa.nextToken)
	rotated, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{Owner: "group", Repo: "project", Client: fc, Tokens: c, Now: now.Add(25 * time.Hour)})
	require.NoError(t, err)
	assert.Empty(t, rotated.Failed)
	assert.Len(t, legacy.revoked, 3)
}

func acceptReplacementToken(context.Context, string, string, *ProjectAccessToken) error { return nil }

func TestInstallConvergenceRequiresReplacementVerifier(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	legacy := &fakeTokens{}
	sa := newFakeSAAPI()
	rec := gitlabroles.BuiltinRegistry().Registrations()[0]
	for _, r := range gitlabroles.BuiltinRegistry().Registrations() {
		if r.Name == gitlabroles.RoleCoder {
			rec = r
		}
	}
	legacy.seed(ProjectAccessToken{ID: 1, Name: rec.Credential.TokenName, Active: true, ExpiresAt: GitLabPATExpiresAt(now), UserID: 101})
	state := rotationStateFile{Roles: map[string]rotationRoleState{string(rec.Name): {CreatedTokenIDs: []int{1}, Phase: rotationPhaseIdle, IncomingID: 1, DistributedAt: now.Format(time.RFC3339)}}}
	require.NoError(t, writeRotationState(ctx, fc, "group", "project", state))
	c := ServiceAccountTokenClient{SA: sa, Legacy: legacy, ManagedLegacyTokenIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
		return ManagedGitLabLegacyTokenIDs(ctx, fc, owner, repo)
	}}
	result := RoleProvisionResult{}
	convergeInstalledServiceAccount(ctx, RoleProvisionConfig{Owner: "group", Repo: "project", Client: fc, Tokens: c, Now: now}, rec, now, true, &result)
	require.Len(t, result.Failed, 1)
	assert.Contains(t, result.Failed[0].Reason, "verification unavailable")
	assert.Empty(t, sa.accounts, "nothing is minted without a verifier")
	assert.Empty(t, legacy.revoked)
}

func TestServiceAccountReplacementRetriesAfterFailedDistribution(t *testing.T) {
	ctx := context.Background()
	sa := newFakeSAAPI()
	legacy := &fakeTokens{listed: []ProjectAccessToken{{ID: 1}}}
	c := ServiceAccountTokenClient{SA: sa, Legacy: legacy, ManagedLegacyTokenIDs: recordedLegacyTokens(legacy)}
	got, err := c.needsServiceAccountReplacement(ctx, "g", "p", rotationRoleState{Phase: rotationPhaseFailed, OutgoingIDs: []int{1}})
	require.NoError(t, err)
	assert.True(t, got, "a failed replacement write must not hide a still-owned legacy credential")
	got, err = c.needsServiceAccountReplacement(ctx, "g", "p", rotationRoleState{Phase: rotationPhaseFailed, OutgoingIDs: []int{99}})
	require.NoError(t, err)
	assert.False(t, got)
}

func TestServiceAccountDeletionRefusesUnresolvedSuppliedOwnership(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {ManagedUserID: 501},
		"coder":  {Supplied: true},
	}}))
	sa := newFakeSAAPI()
	n, err := (ServiceAccountTokenClient{SA: sa, VerifyAccountDeletion: func(context.Context, string, string, int) error { return nil }}).DeleteManagedServiceAccounts(ctx, fc, "g", "p")
	require.ErrorIs(t, err, ErrPollerSuppliedUnresolved)
	assert.Zero(t, n)
	state, _, readErr := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, readErr)
	assert.Equal(t, 501, state.Roles["poller"].ManagedUserID)
}

func TestInitialDistributionFillsOwnershipOnlyEntry(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{"coder": {ManagedUserID: 501}}}))
	require.NoError(t, recordInitialDistribution(ctx, fc, "g", "p", gitlabroles.RoleCoder, 7, "2026-11-01", now))
	state, _, err := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	rs := state.Roles["coder"]
	assert.Equal(t, 501, rs.ManagedUserID)
	assert.Equal(t, 7, rs.IncomingID)
	assert.NotEmpty(t, rs.DistributedAt)
	assert.Equal(t, rotationPhaseIdle, rs.Phase)
	// A populated entry is never overwritten by a later initial proof.
	require.NoError(t, recordInitialDistribution(ctx, fc, "g", "p", gitlabroles.RoleCoder, 8, "2026-11-02", now))
	state, _, err = loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, 7, state.Roles["coder"].IncomingID)
}

func TestCleanupPreflightRejectsMalformedRotationStateWithCleaner(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation] = "invalid"
	fc.VariablesExist["g/p/"+forge.VarGitLabRoleRotation] = true
	_, err := CleanupGitLabRoleIdentityLocked(context.Background(), GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: ServiceAccountTokenClient{SA: newFakeSAAPI()}})
	require.Error(t, err)
	assert.Equal(t, "invalid", fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation])
}

func TestServiceAccountReplacementSelection(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name               string
		rs                 rotationRoleState
		failSA, failLegacy error
		want, wantErr      bool
	}{
		{"unknown", rotationRoleState{}, nil, nil, false, false},
		{"supplied", rotationRoleState{Supplied: true, IncomingID: 1}, nil, nil, false, false},
		{"legacy", rotationRoleState{IncomingID: 1}, nil, nil, true, false},
		{"resume", rotationRoleState{IncomingID: 1, Phase: rotationPhaseDistributing}, nil, nil, true, false},
		{"unsupported", rotationRoleState{IncomingID: 1}, forge.ErrNotFound, nil, false, false},
		{"inventory failed", rotationRoleState{IncomingID: 1}, errors.New("offline"), nil, false, true},
		{"legacy failed", rotationRoleState{IncomingID: 1}, nil, errors.New("offline"), false, true},
		{"unlisted", rotationRoleState{IncomingID: 99}, nil, nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sa := newFakeSAAPI()
			sa.failList = tc.failSA
			legacy := &fakeTokens{listed: []ProjectAccessToken{{ID: 1}}, failList: tc.failLegacy}
			got, err := (ServiceAccountTokenClient{SA: sa, Legacy: legacy, ManagedLegacyTokenIDs: recordedLegacyTokens(legacy)}).needsServiceAccountReplacement(ctx, "g", "p", tc.rs)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantErr, err != nil)
		})
	}
}

func TestCreatedAccountProofAndCredentialFailure(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	for _, account := range []GitLabServiceAccount{{ID: 0}, {ID: 77, Name: "unregistered"}} {
		require.Error(t, RecordManagedGitLabServiceAccount(ctx, fc, "g", "p", account))
	}
	require.NoError(t, RecordManagedGitLabServiceAccount(ctx, fc, "g", "p", GitLabServiceAccount{ID: 77, Name: gitlabroles.PollerTokenName}))
	state, _, err := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, 77, state.Roles["poller"].ManagedUserID)
	for _, mode := range []string{"validate", "record"} {
		t.Run(mode, func(t *testing.T) {
			sa := newFakeSAAPI()
			c := ServiceAccountTokenClient{SA: sa}
			verifyCalled, createdCalled := false, false
			if mode == "validate" {
				c.VerifyToken = func(context.Context, string, string, *ProjectAccessToken) error {
					verifyCalled = true
					return errors.New("invalid")
				}
			} else {
				c.AccountCreated = func(context.Context, string, string, GitLabServiceAccount) error {
					createdCalled = true
					return errors.New("state failed")
				}
			}
			tok, err := c.CreateProjectAccessToken(ctx, "g", "p", gitlabroles.PollerTokenName, []string{"api"}, 30, validRolePATExpiry())
			require.Error(t, err)
			assert.Nil(t, tok)
			if mode == "validate" {
				assert.True(t, verifyCalled, "ownership verification must run, not an earlier expiry rejection")
				assert.Len(t, sa.revoked, 1)
			} else {
				assert.True(t, createdCalled, "account-created callback must run, not an earlier expiry rejection")
				assert.Empty(t, sa.tokens)
			}
		})
	}
}

func TestServiceAccountProofErrors(t *testing.T) {
	for _, mode := range []string{"read", "registry", "state", "write"} {
		t.Run(mode, func(t *testing.T) {
			fc := forge.NewFakeClient()
			switch mode {
			case "read":
				fc.Errors["GetRepoVariable"] = errors.New("offline")
			case "registry":
				fc.VariableValues["g/p/"+forge.VarGitLabRoleRegistry] = "invalid"
			case "state":
				fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation] = "invalid"
			case "write":
				fc.Errors["UpdateCIVariable"] = errors.New("offline")
			}
			require.Error(t, RecordManagedGitLabServiceAccount(context.Background(), fc, "g", "p", GitLabServiceAccount{ID: 77, Name: gitlabroles.PollerTokenName}))
		})
	}
}

func TestServiceAccountStatusErrors(t *testing.T) {
	for _, mode := range []string{"nil", "state", "level"} {
		t.Run(mode, func(t *testing.T) {
			fc := forge.NewFakeClient()
			sa := newFakeSAAPI()
			sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.PollerTokenName}}
			c := ServiceAccountTokenClient{SA: sa}
			status := &RepoStatus{}
			if mode == "nil" {
				c.SA = nil
			}
			if mode == "state" {
				fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation] = "invalid"
			}
			if mode == "level" {
				sa.failLevel = errors.New("offline")
			}
			assert.Equal(t, mode != "nil", c.AppendGitLabServiceAccountStatus(context.Background(), fc, "g", "p", status))
			if mode != "nil" {
				assert.NotEmpty(t, status.GitLabRoleDiagnostics)
			}
		})
	}
}

func TestServiceAccountDeletionInventoryErrors(t *testing.T) {
	for _, mode := range []string{"nil", "state", "absent", "forbidden"} {
		t.Run(mode, func(t *testing.T) {
			fc := forge.NewFakeClient()
			sa := newFakeSAAPI()
			c := ServiceAccountTokenClient{SA: sa}
			if mode == "nil" {
				c.SA = nil
			}
			if mode == "state" {
				fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation] = "invalid"
			}
			if mode == "absent" {
				sa.failList = forge.ErrNotFound
			}
			if mode == "forbidden" {
				sa.failList = forge.ErrForbidden
			}
			n, err := c.DeleteManagedServiceAccounts(context.Background(), fc, "g", "p")
			assert.Zero(t, n)
			assert.Equal(t, mode == "state" || mode == "forbidden", err != nil)
			assert.Empty(t, fc.DeletedProjectServiceAccounts)
		})
	}
}

func TestServiceAccountCleanupRetainsOwnershipWhenAPIUnavailable(t *testing.T) {
	ctx := context.Background()
	const upstreamSecret = "upstream fixture credential must not be reported"
	for _, source := range []struct {
		name  string
		err   error
		nilSA bool
	}{
		{name: "not-found", err: forge.ErrNotFound},
		{name: "not-supported", err: forge.ErrNotSupported},
		{name: "missing-client", err: forge.ErrNotSupported, nilSA: true},
	} {
		t.Run(source.name, func(t *testing.T) {
			for _, ownership := range []string{"managed", "cross-role-excluded", "cross-role-supplied", "none"} {
				t.Run(ownership, func(t *testing.T) {
					fc := forge.NewFakeClient()
					state := rotationStateFile{Roles: map[string]rotationRoleState{}}
					if ownership != "none" {
						state.Roles["poller"] = rotationRoleState{ManagedUserID: 501}
					}
					switch ownership {
					case "cross-role-excluded":
						state.Roles["coder"] = rotationRoleState{ExcludedUserIDs: []int{501}}
					case "cross-role-supplied":
						state.Roles["coder"] = rotationRoleState{SuppliedUserID: 501}
					}
					require.NoError(t, writeRotationState(ctx, fc, "g", "p", state))
					sa := newFakeSAAPI()
					sa.failList = errors.Join(source.err, errors.New(upstreamSecret))
					c := ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{}}
					if source.nilSA {
						c.SA = nil
					}
					result, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{
						Owner: "g", Repo: "p", Client: fc, Tokens: c,
					})
					assert.Zero(t, result.AccountsDeleted)
					assert.Empty(t, fc.DeletedProjectServiceAccounts)
					if ownership == "managed" {
						require.Error(t, err)
						assert.ErrorIs(t, err, source.err)
						assert.NotContains(t, err.Error(), upstreamSecret)
						assert.Contains(t, err.Error(), "ownership retained for retry")
						retained, _, readErr := loadRotationState(ctx, fc, "g", "p")
						require.NoError(t, readErr)
						assert.Equal(t, state, retained)
						assert.True(t, fc.VariablesExist["g/p/"+forge.VarGitLabRoleRotation])
					} else {
						require.NoError(t, err)
						retired, _, readErr := loadRotationState(ctx, fc, "g", "p")
						require.NoError(t, readErr)
						assert.Zero(t, retired.Roles["poller"].ManagedUserID)
						if ownership != "none" {
							assert.Contains(t, retired.Roles["coder"].ExcludedUserIDs, 501)
						}
					}
				})
			}
		})
	}
}

func TestServiceAccountDeletionWithoutClientStillReadsOwnership(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation] = "invalid"
	n, err := (ServiceAccountTokenClient{}).DeleteManagedServiceAccounts(context.Background(), fc, "g", "p")
	require.Error(t, err)
	assert.Zero(t, n)
	assert.Equal(t, "invalid", fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation])
}

func TestServiceAccountDeletionRedactsRemoteErrors(t *testing.T) {
	ctx := context.Background()
	const echoedFixture = "echoed credential fixture"
	for _, operation := range []string{"inventory", "PATs", "deletion"} {
		t.Run(operation, func(t *testing.T) {
			cause := errors.New("remote operation failed")
			remoteErr := errors.Join(cause, errors.New(echoedFixture))
			fc := &accountDeletingClient{FakeClient: forge.NewFakeClient()}
			sa := newFakeSAAPI()
			sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.PollerTokenName}}
			state := rotationStateFile{Roles: map[string]rotationRoleState{
				"poller": {ManagedUserID: 501},
			}}
			require.NoError(t, writeRotationState(ctx, fc, "g", "p", state))
			switch operation {
			case "inventory":
				sa.failList = remoteErr
			case "PATs":
				sa.failPATs = remoteErr
			case "deletion":
				fc.fail = remoteErr
			}
			n, err := (ServiceAccountTokenClient{SA: sa, VerifyAccountDeletion: func(context.Context, string, string, int) error { return nil }}).DeleteManagedServiceAccounts(ctx, fc, "g", "p")
			require.Error(t, err)
			assert.ErrorIs(t, err, cause)
			assert.NotContains(t, err.Error(), echoedFixture)
			assert.Zero(t, n)
			assert.Empty(t, fc.deleted)
			retained, _, readErr := loadRotationState(ctx, fc, "g", "p")
			require.NoError(t, readErr)
			assert.Equal(t, state, retained)
		})
	}
}

func TestInstallConvergenceFailsClosed(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"state", "inventory", "supplied", "dry-run"} {
		t.Run(mode, func(t *testing.T) {
			fc := forge.NewFakeClient()
			sa := newFakeSAAPI()
			legacy := &fakeTokens{listed: []ProjectAccessToken{{ID: 1, Name: gitlabroles.PollerTokenName, Active: true}}}
			state := rotationStateFile{Roles: map[string]rotationRoleState{"poller": {IncomingID: 1}}}
			if mode == "supplied" {
				state.Roles["poller"] = rotationRoleState{Supplied: true, IncomingID: 1}
			}
			require.NoError(t, writeRotationState(ctx, fc, "g", "p", state))
			if mode == "state" {
				fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation] = "invalid"
			}
			if mode == "inventory" {
				sa.failList = errors.New("offline")
			}
			cfg := RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: ServiceAccountTokenClient{SA: sa, Legacy: legacy, ManagedLegacyTokenIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
				return ManagedGitLabLegacyTokenIDs(ctx, fc, owner, repo)
			}}, DryRun: mode == "dry-run"}
			result := RoleProvisionResult{}
			rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RolePoller)
			convergeInstalledServiceAccount(ctx, cfg, rec, time.Now(), true, &result)
			assert.Empty(t, sa.accounts)
			assert.Empty(t, legacy.revoked)
			if mode == "state" || mode == "inventory" || mode == "supplied" {
				assert.Len(t, result.Failed, 1)
			} else {
				assert.Empty(t, result.Failed)
			}
		})
	}
}

type accountDeletingClient struct {
	*forge.FakeClient
	deleted []int
	fail    error
}

func (c *accountDeletingClient) DeleteProjectServiceAccount(_ context.Context, _, _ string, id int) error {
	if c.fail != nil {
		return c.fail
	}
	c.deleted = append(c.deleted, id)
	return nil
}

func TestServiceAccountDeletionRequiresDurableOwnership(t *testing.T) {
	ctx := context.Background()
	for _, failure := range []string{"", "active", "inventory", "resource", "delete", "missing verifier"} {
		t.Run(failure, func(t *testing.T) {
			fc := &accountDeletingClient{FakeClient: forge.NewFakeClient()}
			sa := newFakeSAAPI()
			sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.PollerTokenName}, {ID: 502, Name: gitlabroles.CoderTokenName}}
			sa.tokens[501] = []ProjectAccessToken{{ID: 1, Name: gitlabroles.PollerTokenName, Revoked: true}}
			require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{"poller": {ManagedUserID: 501, IncomingID: 1}}}))
			c := ServiceAccountTokenClient{SA: sa, VerifyAccountDeletion: func(context.Context, string, string, int) error { return nil }}
			switch failure {
			case "active":
				sa.tokens[501][0] = ProjectAccessToken{ID: 2, Active: true, Name: "personal"}
			case "inventory":
				sa.failPATs = errors.New("offline")
			case "missing verifier":
				c.VerifyAccountDeletion = nil
			case "resource":
				c.VerifyAccountDeletion = func(context.Context, string, string, int) error { return errors.New("owned resource") }
			case "delete":
				fc.fail = errors.New("offline")
			}
			n, err := c.DeleteManagedServiceAccounts(ctx, fc, "g", "p")
			if failure == "" {
				require.NoError(t, err)
				assert.Equal(t, 1, n)
				assert.Equal(t, []int{501}, fc.deleted)
			} else {
				require.Error(t, err)
				assert.Zero(t, n)
				assert.Empty(t, fc.deleted)
			}
		})
	}
}

func TestServiceAccountStatusDetailsAndDrift(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.PollerTokenName}, {ID: 502, Name: gitlabroles.CoderTokenName}}
	sa.members[501], sa.members[502] = 40, 30
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{"poller": {ManagedUserID: 501}}}))
	status := &RepoStatus{}
	c := ServiceAccountTokenClient{SA: sa}
	assert.True(t, c.AppendGitLabServiceAccountStatus(ctx, fc, "g", "p", status))
	require.Len(t, status.GitLabServiceAccounts, 2)
	assert.Len(t, status.Drifts, 1)
	assert.Contains(t, status.GitLabRoleDiagnostics[0], "effective access 30")
	sa.failList = forge.ErrForbidden
	assert.True(t, c.AppendGitLabServiceAccountStatus(ctx, fc, "g", "p", status))
	assert.Contains(t, status.GitLabRoleDiagnostics[len(status.GitLabRoleDiagnostics)-1], "unavailable")
}

func TestServiceAccountDeletionPreservesCrossRoleExclusions(t *testing.T) {
	ctx := context.Background()
	fc := &accountDeletingClient{FakeClient: forge.NewFakeClient()}
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.PollerTokenName}}
	sa.members[501] = gitlabroles.DeveloperAccessLevel
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {ManagedUserID: 501}, "coder": {SuppliedUserID: 501, ExcludedUserIDs: []int{501}},
	}}))
	c := ServiceAccountTokenClient{SA: sa}
	n, err := c.DeleteManagedServiceAccounts(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Empty(t, fc.deleted)
	status := &RepoStatus{}
	assert.False(t, c.AppendGitLabServiceAccountStatus(ctx, fc, "g", "p", status))
	require.Len(t, status.GitLabServiceAccounts, 1)
	assert.False(t, status.GitLabServiceAccounts[0].Managed)
}

func TestInstallConvergenceUnavailableDoesNotRotateLegacyAgain(t *testing.T) {
	for _, capabilityErr := range []error{forge.ErrForbidden, forge.ErrNotFound, forge.ErrNotSupported} {
		t.Run(capabilityErr.Error(), func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
			fc := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleAnalyst, gitlabroles.RoleCoder)
			legacy := &fakeTokens{}
			sa := newFakeSAAPI()
			sa.failCreate = capabilityErr // Listing succeeds; creation does not.
			state := rotationStateFile{Roles: map[string]rotationRoleState{}}
			for i, rec := range gitlabroles.BuiltinRegistry().Registrations() {
				id := i + 1
				legacy.seed(ProjectAccessToken{ID: id, Name: rec.Credential.TokenName, Active: true, ExpiresAt: GitLabPATExpiresAt(now), UserID: 100 + id})
				state.Roles[string(rec.Name)] = rotationRoleState{CreatedTokenIDs: []int{id}, Phase: rotationPhaseIdle, IncomingID: id, DistributedAt: now.Add(-time.Hour).Format(time.RFC3339)}
			}
			require.NoError(t, writeRotationState(ctx, fc, "group", "project", state))
			before, _, err := loadRotationState(ctx, fc, "group", "project")
			require.NoError(t, err)
			c := ServiceAccountTokenClient{SA: sa, Legacy: legacy, VerifyToken: acceptReplacementToken, ManagedLegacyTokenIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
				return ManagedGitLabLegacyTokenIDs(ctx, fc, owner, repo)
			}}
			for attempt := range 2 {
				result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{Owner: "group", Repo: "project", Client: fc, Tokens: c, Now: now.Add(time.Duration(attempt) * time.Hour)})
				require.NoError(t, err)
				require.Empty(t, result.Failed)
				assert.Empty(t, result.Created)
				assert.Contains(t, result.Diagnostics, "poller: service-account creation unavailable; keeping the existing legacy credential")
				after, _, err := loadRotationState(ctx, fc, "group", "project")
				require.NoError(t, err)
				assert.Equal(t, before.Roles, after.Roles, "no credential IDs or distribution/grace timestamps may change")
			}
			assert.Empty(t, legacy.created)
			assert.Empty(t, legacy.revoked)
			assert.Empty(t, sa.accounts)
		})
	}
}

func TestServiceAccountStatusVerificationFailuresAreDrift(t *testing.T) {
	for _, mode := range []string{"renamed", "inventory", "ownership", "level", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			fc := forge.NewFakeClient()
			sa := newFakeSAAPI()
			sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.PollerTokenName}}
			sa.members[501] = gitlabroles.DeveloperAccessLevel
			c := ServiceAccountTokenClient{SA: sa,
				ManagedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{501}, nil },
				ManagedAccountNames: func(context.Context, string, string) (map[int]string, error) {
					return map[int]string{501: gitlabroles.PollerTokenName}, nil
				},
			}
			switch mode {
			case "renamed":
				sa.accounts[0].Name = "renamed-poller"
			case "inventory":
				sa.failList = errors.New(leakToken)
			case "ownership":
				fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation] = "invalid"
			case "level":
				sa.failLevel = errors.New(leakToken)
			case "unsupported":
				sa.failList = forge.ErrForbidden
			}
			status := &RepoStatus{GitLabRolesReady: true}
			changed := c.AppendGitLabServiceAccountStatus(context.Background(), fc, "g", "p", status)
			if mode == "unsupported" {
				assert.False(t, changed)
				assert.True(t, status.GitLabRolesReady, "capability absence does not invalidate a verified legacy credential")
				assert.Empty(t, status.Drifts)
			} else {
				assert.True(t, changed)
				assert.False(t, status.GitLabRolesReady)
				assert.True(t, status.GitLabRolesPartial)
				require.Len(t, status.Drifts, 1)
				assert.Contains(t, status.GitLabRoleDiagnostics[0], "unverified")
				if mode == "renamed" {
					assert.Contains(t, status.Drifts[0].Actual, "unexpected display name")
					assert.Contains(t, status.Drifts[0].Actual, "501")
				}
			}
			for _, diagnostic := range status.GitLabRoleDiagnostics {
				assert.NotContains(t, diagnostic, leakToken)
			}
		})
	}
}

func TestServiceAccountStatusUnavailableDistinguishesManagedOwnership(t *testing.T) {
	for _, unavailable := range []string{"forbidden", "not found", "not supported", "no client"} {
		for _, ownership := range []string{"managed", "legacy", "excluded", "supplied", "unreadable"} {
			t.Run(unavailable+"/"+ownership, func(t *testing.T) {
				ctx := context.Background()
				fc := forge.NewFakeClient()
				sa := newFakeSAAPI()
				switch unavailable {
				case "forbidden":
					sa.failList = forge.ErrForbidden
				case "not found":
					sa.failList = forge.ErrNotFound
				case "not supported":
					sa.failList = forge.ErrNotSupported
				}
				rs := rotationRoleState{}
				switch ownership {
				case "managed":
					rs.ManagedUserID = 501
				case "excluded":
					rs.ManagedUserID = 501
					rs.ExcludedUserIDs = []int{501}
				case "supplied":
					rs.ManagedUserID = 501
					rs.SuppliedUserID = 501
				}
				require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{"poller": rs}}))
				if ownership == "unreadable" {
					fc.Errors["GetRepoVariable"] = errors.New(leakToken)
				}
				c := ServiceAccountTokenClient{SA: sa}
				if unavailable == "no client" {
					c.SA = nil
				}
				status := &RepoStatus{GitLabRolesReady: true, GitLabServiceAccounts: []GitLabServiceAccountStatus{{ID: 501, Name: "stale"}}}
				wantDrift := ownership == "managed" || ownership == "unreadable"
				assert.Equal(t, wantDrift, c.AppendGitLabServiceAccountStatus(ctx, fc, "g", "p", status))
				assert.Equal(t, !wantDrift, status.GitLabRolesReady)
				assert.Empty(t, status.GitLabServiceAccounts, "failed inventories must not retain stale account details")
				if wantDrift {
					require.Len(t, status.Drifts, 1)
					assert.Equal(t, "gitlab-service-accounts", status.Drifts[0].Field)
				} else {
					assert.Empty(t, status.Drifts)
				}
				for _, diagnostic := range status.GitLabRoleDiagnostics {
					assert.NotContains(t, diagnostic, leakToken)
				}
			})
		}
	}
}

func TestServiceAccountStatusMissingRecordedAccount(t *testing.T) {
	for _, ownership := range []string{"managed", "excluded", "supplied"} {
		for _, unrelated := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unrelated=%t", ownership, unrelated), func(t *testing.T) {
				ctx := context.Background()
				fc := forge.NewFakeClient()
				sa := newFakeSAAPI()
				if unrelated {
					sa.accounts = []GitLabServiceAccount{{ID: 502, Name: gitlabroles.PollerTokenName}}
					sa.members[502] = gitlabroles.DeveloperAccessLevel
				}
				rs := rotationRoleState{ManagedUserID: 501}
				if ownership == "excluded" {
					rs.ExcludedUserIDs = []int{501}
				}
				if ownership == "supplied" {
					rs.SuppliedUserID = 501
				}
				require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{"poller": rs}}))
				status := &RepoStatus{GitLabRolesReady: true}
				wantDrift := ownership == "managed"
				assert.Equal(t, wantDrift, (ServiceAccountTokenClient{SA: sa}).AppendGitLabServiceAccountStatus(ctx, fc, "g", "p", status))
				assert.Equal(t, !wantDrift, status.GitLabRolesReady)
				if wantDrift {
					require.Len(t, status.Drifts, 1)
					assert.Equal(t, "gitlab-service-account:501", status.Drifts[0].Field)
					assert.Contains(t, status.Drifts[0].Actual, "missing from inventory")
					assert.True(t, status.GitLabRolesPartial)
				} else {
					assert.Empty(t, status.Drifts)
				}
			})
		}
	}
}

func TestReinstallReconcilesManagedRoleMembership(t *testing.T) {
	for _, level := range []int{20, 40} {
		for _, failure := range []string{"", "update", "inherited", "verify"} {
			t.Run(fmt.Sprintf("%d/%s", level, failure), func(t *testing.T) {
				ctx := context.Background()
				fc := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleAnalyst, gitlabroles.RoleCoder)
				sa := newFakeSAAPI()
				c := ServiceAccountTokenClient{SA: sa}
				c.AccountCreated = func(ctx context.Context, owner, repo string, account GitLabServiceAccount) error {
					return RecordManagedGitLabServiceAccount(ctx, fc, owner, repo, account)
				}
				cfg := RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c}
				first, err := ProvisionGitLabRoleCredentials(ctx, cfg)
				require.NoError(t, err)
				require.Empty(t, first.Failed)
				for _, account := range sa.accounts {
					sa.members[int64(account.ID)] = level
				}
				switch failure {
				case "update":
					sa.failUpdate = errors.New(leakToken)
				case "inherited":
					sa.inherited = map[int64]int{}
					for _, account := range sa.accounts {
						sa.inherited[int64(account.ID)] = 40
					}
				case "verify":
					sa.failLevel = errors.New(leakToken)
				}
				second, err := ProvisionGitLabRoleCredentials(ctx, cfg)
				require.NoError(t, err)
				if failure == "" {
					require.Empty(t, second.Failed)
					for _, level := range sa.members {
						assert.Equal(t, 30, level)
					}
				} else {
					require.Len(t, second.Failed, 3)
					for _, fail := range second.Failed {
						assert.NotContains(t, fail.Reason, leakToken)
					}
				}
				assert.Empty(t, second.Created)
				assert.Len(t, sa.accounts, 3)
			})
		}
	}
}

func TestServiceAccountInvalidAccessClearsReadiness(t *testing.T) {
	for _, level := range []int{20, 40} {
		t.Run(fmt.Sprint(level), func(t *testing.T) {
			ctx := context.Background()
			fc := forge.NewFakeClient()
			sa := newFakeSAAPI()
			sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.CoderTokenName}}
			sa.members[501] = level
			require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{"coder": {ManagedUserID: 501}}}))
			status := &RepoStatus{GitLabRolesReady: true}
			assert.True(t, (ServiceAccountTokenClient{SA: sa}).AppendGitLabServiceAccountStatus(ctx, fc, "g", "p", status))
			assert.False(t, status.GitLabRolesReady)
			assert.True(t, status.GitLabRolesPartial)
			require.Len(t, status.Drifts, 1)
		})
	}
}

func TestAbsentOutgoingServiceAccountTokenCleanup(t *testing.T) {
	for _, inventory := range []string{"complete", "no legacy", "legacy forbidden", "service forbidden", "legacy delete 404", "inactive"} {
		t.Run(inventory, func(t *testing.T) {
			ctx := context.Background()
			sa := newFakeSAAPI()
			legacy := &fakeTokens{}
			c := ServiceAccountTokenClient{SA: sa, Legacy: legacy, ManagedLegacyTokenIDs: recordedLegacyTokens(legacy)}
			switch inventory {
			case "no legacy":
				c.Legacy = nil
			case "legacy forbidden":
				legacy.failList = forge.ErrForbidden
			case "service forbidden":
				sa.failList = forge.ErrForbidden
			case "inactive":
				sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.PollerTokenName}}
				sa.tokens[501] = []ProjectAccessToken{{ID: 88, Name: gitlabroles.PollerTokenName, Active: false, Revoked: true}}
			case "legacy delete 404":
				legacy.seed(ProjectAccessToken{ID: 88, Active: true})
				legacy.failRevoke = forge.ErrNotFound
			}
			now := time.Now()
			rs := rotationRoleState{Phase: rotationPhaseOverlapping, IncomingID: 99, OutgoingIDs: []int{88}, DistributedAt: now.Add(-48 * time.Hour).Format(time.RFC3339)}
			listed := []ProjectAccessToken{}
			cleaned := cleanupOutgoing(ctx, RoleRotateConfig{Owner: "g", Repo: "p", Tokens: c}, &rs, now, 24*time.Hour, &listed, nil)
			if inventory == "complete" || inventory == "no legacy" || inventory == "inactive" {
				assert.True(t, cleaned)
				assert.Empty(t, rs.OutgoingIDs)
				assert.Equal(t, rotationPhaseIdle, rs.Phase)
			} else {
				assert.False(t, cleaned)
				assert.Equal(t, []int{88}, rs.OutgoingIDs)
				assert.Equal(t, rotationPhaseOverlapping, rs.Phase)
			}

		})
	}
}

func TestGitLabLeaseHolderIsOpaque(t *testing.T) {
	a, err := newGitLabLeaseHolder()
	require.NoError(t, err)
	b, err := newGitLabLeaseHolder()
	require.NoError(t, err)
	assert.Regexp(t, "^[a-f0-9]{32}$", a)
	assert.NotEqual(t, a, b)
}

func TestLegacyConvergencePreservesUnverifiedSameNamedToken(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.CoderTokenName, Active: true, UserID: 71, ExpiresAt: GitLabPATExpiresAt(now)})
	legacy.seed(ProjectAccessToken{ID: 2, Name: gitlabroles.CoderTokenName, Active: true, UserID: 72, ExpiresAt: GitLabPATExpiresAt(now)})
	require.NoError(t, fc.CreateRepoSecret(ctx, "g", "p", "FULLSEND_GITLAB_CODER_TOKEN", "managed-existing-secret"))
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{"coder": {CreatedTokenIDs: []int{1}, IncomingID: 1, Phase: rotationPhaseIdle, DistributedAt: now.Format(time.RFC3339)}}}))
	sa := newFakeSAAPI()
	c := ServiceAccountTokenClient{SA: sa, Legacy: legacy, VerifyToken: acceptReplacementToken, ManagedLegacyTokenIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
		return ManagedGitLabLegacyTokenIDs(ctx, fc, owner, repo)
	}}
	c.AccountCreated = func(ctx context.Context, owner, repo string, account GitLabServiceAccount) error {
		return RecordManagedGitLabServiceAccount(ctx, fc, owner, repo, account)
	}
	clock := now
	c.Now = func() time.Time { return clock }
	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c, Now: now})
	require.NoError(t, err)
	require.Empty(t, result.Failed)
	state, _, err := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, []int{1}, state.Roles["coder"].OutgoingIDs)
	clock = now.Add(25 * time.Hour)
	rotated, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c, Now: clock})
	require.NoError(t, err)
	require.Empty(t, rotated.Failed)
	assert.NotContains(t, legacy.revoked, 2)
	_, _, err = revokeGitLabIdentityTokens(ctx, c, "g", "p")
	require.NoError(t, err)
	assert.NotContains(t, legacy.revoked, 2)
	require.Error(t, c.RevokeProjectAccessToken(ctx, "g", "p", 2))
	assert.NotContains(t, legacy.revoked, 2)
}

func TestLegacyBackfillDoesNotEstablishCreationOwnership(t *testing.T) {
	ctx := context.Background()
	fc := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleAnalyst, gitlabroles.RoleCoder)
	for _, name := range []string{"FULLSEND_GITLAB_POLLER_TOKEN", "FULLSEND_GITLAB_ANALYST_TOKEN"} {
		require.NoError(t, fc.CreateRepoSecret(ctx, "g", "p", name, "existing-secret"))
	}
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 2, Name: gitlabroles.CoderTokenName, Active: true, ExpiresAt: GitLabPATExpiresAt(time.Now())})
	require.NoError(t, fc.CreateRepoSecret(ctx, "g", "p", "FULLSEND_GITLAB_CODER_TOKEN", "existing-unverified-secret"))
	c := ServiceAccountTokenClient{SA: newFakeSAAPI(), Legacy: legacy, ManagedLegacyTokenIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
		return ManagedGitLabLegacyTokenIDs(ctx, fc, owner, repo)
	}}
	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c})
	require.NoError(t, err)
	assert.Empty(t, result.Created)
	assert.Empty(t, legacy.revoked)
	ids, err := ManagedGitLabLegacyTokenIDs(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Empty(t, ids)
	assert.Contains(t, strings.Join(result.Diagnostics, " "), "manual recovery")
}

func TestActiveOutgoingServiceAccountCleanupWithUnavailableLegacy(t *testing.T) {
	for _, fail := range []error{forge.ErrForbidden, forge.ErrNotSupported} {
		t.Run(fail.Error(), func(t *testing.T) {
			sa := newFakeSAAPI()
			sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.CoderTokenName}}
			sa.tokens[501] = []ProjectAccessToken{{ID: 88, Name: gitlabroles.CoderTokenName, Active: true}}
			c := ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{failList: fail}, ManagedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{501}, nil }}
			now := time.Now()
			rs := rotationRoleState{Phase: rotationPhaseOverlapping, IncomingID: 99, OutgoingIDs: []int{88}, DistributedAt: now.Add(-48 * time.Hour).Format(time.RFC3339)}
			listed := []ProjectAccessToken{}
			assert.True(t, cleanupOutgoing(context.Background(), RoleRotateConfig{Owner: "g", Repo: "p", Tokens: c}, &rs, now, 24*time.Hour, &listed, nil))
			assert.Equal(t, []int{88}, sa.revoked)
			assert.Empty(t, rs.OutgoingIDs)
		})
	}
}

type cancellationAwareRoleAccounts struct {
	*fakeSAAPI
	checks int
}

func (a *cancellationAwareRoleAccounts) ListServiceAccountPATs(ctx context.Context, owner, repo string, id int) ([]ProjectAccessToken, error) {
	a.checks++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.fakeSAAPI.ListServiceAccountPATs(ctx, owner, repo, id)
}
func (a *cancellationAwareRoleAccounts) RevokeServiceAccountPAT(ctx context.Context, owner, repo string, id, token int) error {
	a.checks++
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.fakeSAAPI.RevokeServiceAccountPAT(ctx, owner, repo, id, token)
}

func TestElevatedOwnedRoleContainment(t *testing.T) {
	for _, role := range []gitlabroles.Role{gitlabroles.RoleAnalyst, gitlabroles.RoleCoder} {
		for _, attribution := range []string{"owned", "different", "unverified"} {
			t.Run(string(role)+"/"+attribution, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				fc := forge.NewFakeClient()
				rec, _ := gitlabroles.BuiltinRegistry().Lookup(role)
				require.NoError(t, fc.CreateRepoSecret(ctx, "g", "p", rec.Credential.SecretName, "installed-secret"))
				require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{string(role): {ManagedUserID: 501, IncomingID: 12, CreatedTokenIDs: []int{12}, Phase: rotationPhaseIdle}}}))
				sa := &cancellationAwareRoleAccounts{fakeSAAPI: newFakeSAAPI()}
				sa.accounts = []GitLabServiceAccount{{ID: 501, Name: rec.Credential.TokenName}}
				sa.members[501] = 40
				sa.inherited = map[int64]int{501: 40}
				sa.tokens[501] = []ProjectAccessToken{{ID: 12, Name: rec.Credential.TokenName, Active: true}}
				c := ServiceAccountTokenClient{SA: sa, InstalledRoleCredentialOwner: func(ctx context.Context, owner, repo, secret string) (int, error) {
					require.NoError(t, ctx.Err())
					switch attribution {
					case "different":
						return 999, nil
					case "unverified":
						return 0, errors.New(leakToken)
					}
					return 501, nil
				}}
				cancel()
				result := &RoleProvisionResult{}
				convergeInstalledServiceAccount(ctx, RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c}, rec, time.Now(), true, result)
				require.Len(t, result.Failed, 1)
				assert.Equal(t, []int{12}, sa.revoked)
				assert.GreaterOrEqual(t, sa.checks, 3)
				for _, tok := range sa.tokens[501] {
					assert.False(t, tok.Active)
				}
				exists, err := fc.RepoSecretExists(context.Background(), "g", "p", rec.Credential.SecretName)
				require.NoError(t, err)
				assert.Equal(t, attribution != "owned", exists)
				state, _, err := loadRotationState(context.Background(), fc, "g", "p")
				require.NoError(t, err)
				assert.Equal(t, 501, state.Roles[string(role)].ManagedUserID)
				assert.NotContains(t, strings.Join(result.Diagnostics, " "), leakToken)
			})
		}
	}
}

func TestSuppliedAccountRenamePreservesCredential(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: "administrator-renamed-account"}}
	sa.tokens[501] = []ProjectAccessToken{{ID: 12, Name: "administrator-token", Active: true, ExpiresAt: GitLabPATExpiresAt(now)}}
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{"coder": {Supplied: true, SuppliedUserID: 501, SuppliedTokenID: 12, SuppliedDistributed: true, Phase: rotationPhaseIdle, DistributedAt: now.Format(time.RFC3339)}}}))
	c := ServiceAccountTokenClient{SA: sa, SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{501}, nil }, CurrentSuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{501}, nil }, SuppliedTokenIDs: func(context.Context, string, string) (map[int][]SuppliedTokenRef, error) {
		return map[int][]SuppliedTokenRef{501: {{ID: 12, Name: gitlabroles.CoderTokenName}}}, nil
	}}
	tokens, err := c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	assert.Equal(t, gitlabroles.CoderTokenName, tokens[0].Name)
	strict, err := c.ListProjectAccessTokensStrict(ctx, "g", "p")
	require.NoError(t, err)
	assert.Empty(t, strict)
	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c, Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Now: now})
	require.NoError(t, err)
	assert.Empty(t, result.Rotated)
	assert.Empty(t, sa.revoked)
}

func TestReplacementPreservesLegacyCreationProofForGraceCleanup(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fc := forge.NewFakeClient()
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 7, Name: gitlabroles.PollerTokenName, Active: true})
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{"poller": {ManagedUserID: 501, CreatedTokenIDs: []int{7, 8}, IncomingID: 8, OutgoingIDs: []int{7}, Phase: rotationPhaseOverlapping}}}))
	require.NoError(t, recordReplacementDistribution(ctx, fc, "g", "p", gitlabroles.RolePoller, 9, "", now, false))
	state, _, err := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	rs := state.Roles["poller"]
	assert.ElementsMatch(t, []int{7, 8, 9}, rs.CreatedTokenIDs)
	c := ServiceAccountTokenClient{SA: newFakeSAAPI(), Legacy: legacy, ManagedLegacyTokenIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
		return ManagedGitLabLegacyTokenIDs(ctx, fc, owner, repo)
	}}
	listed := []ProjectAccessToken{}
	assert.True(t, cleanupOutgoing(ctx, RoleRotateConfig{Owner: "g", Repo: "p", Tokens: c}, &rs, now.Add(25*time.Hour), 24*time.Hour, &listed, nil))
	assert.Equal(t, []int{7}, legacy.revoked)
	assert.Empty(t, rs.OutgoingIDs)
}

func TestRoleContainmentReportsFailedRevocation(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	sa := newFakeSAAPI()
	sa.tokens[501] = []ProjectAccessToken{{ID: 12, Name: gitlabroles.CoderTokenName, Active: true}}
	sa.failRevoke = errors.New(leakToken)
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{"coder": {ManagedUserID: 501}}}))
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	err := (ServiceAccountTokenClient{SA: sa}).containOwnedRole(ctx, fc, "g", "p", rec, 501)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), leakToken)
	assert.Contains(t, err.Error(), "active token 12 remains")
	state, _, err := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, 501, state.Roles["coder"].ManagedUserID)
}

func TestUnresolvedSuppliedOwnerPreventsContainment(t *testing.T) {
	for _, mode := range []string{"resolved", "failed", "no resolver"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			fc := forge.NewFakeClient()
			rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
			require.NoError(t, fc.CreateRepoSecret(ctx, "g", "p", rec.Credential.SecretName, "supplied-secret"))
			require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{"coder": {ManagedUserID: 501, IncomingID: 12, Supplied: true, Phase: rotationPhaseIdle}}}))
			sa := newFakeSAAPI()
			sa.accounts = []GitLabServiceAccount{{ID: 501, Name: rec.Credential.TokenName}}
			sa.members[501] = 40
			sa.tokens[501] = []ProjectAccessToken{{ID: 12, Name: rec.Credential.TokenName, Active: true}}
			calls := 0
			c := ServiceAccountTokenClient{SA: sa, InstalledRoleCredentialOwner: func(context.Context, string, string, string) (int, error) {
				t.Fatal("containment attribution must not run")
				return 0, nil
			}}
			if mode != "no resolver" {
				c.SuppliedAccountIDs = func(context.Context, string, string) ([]int, error) {
					calls++
					if mode == "failed" {
						return nil, errors.New(leakToken)
					}
					return []int{501}, nil
				}
			}
			result := &RoleProvisionResult{}
			convergeInstalledServiceAccount(ctx, RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c}, rec, time.Now(), true, result)
			if mode == "resolved" {
				assert.Empty(t, result.Failed)
			} else {
				require.Len(t, result.Failed, 1)
				assert.Contains(t, result.Failed[0].Reason, "ownership unresolved")
				assert.NotContains(t, result.Failed[0].Reason, leakToken)
			}
			assert.Empty(t, sa.revoked)
			assert.Equal(t, 40, sa.members[501])
			assert.True(t, sa.tokens[501][0].Active)
			exists, err := fc.RepoSecretExists(ctx, "g", "p", rec.Credential.SecretName)
			require.NoError(t, err)
			assert.True(t, exists)
			if mode != "no resolver" {
				assert.Equal(t, 1, calls)
			}
		})
	}
}

func TestSharedSuppliedTokenReportedForEveryRole(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fc := seededRoleClient(t, gitlabroles.RoleAnalyst, gitlabroles.RoleCoder)
	for _, secret := range []string{"FULLSEND_GITLAB_ANALYST_TOKEN", "FULLSEND_GITLAB_CODER_TOKEN"} {
		require.NoError(t, fc.CreateRepoSecret(ctx, "g", "p", secret, "shared-supplied-secret"))
	}
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: "supplied-account"}}
	sa.tokens[501] = []ProjectAccessToken{{ID: 12, Name: "shared-pat", Active: true, ExpiresAt: GitLabPATExpiresAt(now)}}
	state := rotationStateFile{Roles: map[string]rotationRoleState{}}
	for _, role := range []string{"analyst", "coder"} {
		state.Roles[role] = rotationRoleState{Supplied: true, SuppliedUserID: 501, SuppliedTokenID: 12, SuppliedDistributed: true, Phase: rotationPhaseIdle, DistributedAt: now.Format(time.RFC3339)}
	}
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", state))
	c := ServiceAccountTokenClient{SA: sa, SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{501}, nil }, SuppliedTokenIDs: func(context.Context, string, string) (map[int][]SuppliedTokenRef, error) {
		return map[int][]SuppliedTokenRef{501: {{ID: 12, Name: gitlabroles.AnalystTokenName}, {ID: 12, Name: gitlabroles.CoderTokenName}}}, nil
	}}
	tokens, err := c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	require.Len(t, tokens, 2)
	assert.ElementsMatch(t, []string{gitlabroles.AnalystTokenName, gitlabroles.CoderTokenName}, []string{tokens[0].Name, tokens[1].Name})
	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c, Roles: []gitlabroles.Role{gitlabroles.RoleAnalyst, gitlabroles.RoleCoder}, Now: now})
	require.NoError(t, err)
	assert.Empty(t, result.Rotated)
	assert.Empty(t, result.Failed)
	assert.Empty(t, sa.revoked)
	for _, report := range result.Report.Roles {
		if report.Name == gitlabroles.RoleAnalyst || report.Name == gitlabroles.RoleCoder {
			assert.Equal(t, gitlabroles.LifecycleOK, report.Lifecycle)
		}
	}
}

func TestMissingSecretContainsElevatedManagedRole(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{"coder": {ManagedUserID: 501, IncomingID: 12, CreatedTokenIDs: []int{12}, Phase: rotationPhaseIdle}}}))
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: rec.Credential.TokenName}}
	sa.members[501] = 40
	sa.inherited = map[int64]int{501: 40}
	sa.tokens[501] = []ProjectAccessToken{{ID: 12, Name: rec.Credential.TokenName, Active: true}}
	c := ServiceAccountTokenClient{SA: sa, InstalledRoleCredentialOwner: func(context.Context, string, string, string) (int, error) { return 0, nil }}
	result := &RoleProvisionResult{}
	present := map[string]bool{}
	for _, other := range gitlabroles.BuiltinRegistry().Registrations() {
		present[other.Credential.SecretName] = other.Name != rec.Name
	}
	provisionOwnRoles(ctx, RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c}, gitlabroles.BuiltinRegistry(), present, result)
	require.Len(t, result.Failed, 1)
	assert.Equal(t, rec.Name, result.Failed[0].Role)
	assert.Equal(t, []int{12}, sa.revoked)
	assert.False(t, sa.tokens[501][0].Active)
	assert.Empty(t, result.Created)
	state, _, err := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, 501, state.Roles["coder"].ManagedUserID)
}

func TestCreationBoundaryContainsElevatedOwnedRole(t *testing.T) {
	for _, failure := range []string{"inherited", "membership", "unresolved"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			fc := forge.NewFakeClient()
			rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
			rs := rotationRoleState{ManagedUserID: 501, IncomingID: 12, CreatedTokenIDs: []int{12}}
			require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{"coder": rs}}))
			sa := newFakeSAAPI()
			sa.accounts = []GitLabServiceAccount{{ID: 501, Name: rec.Credential.TokenName}}
			sa.members[501] = 40
			sa.inherited = map[int64]int{501: 40}
			sa.tokens[501] = []ProjectAccessToken{{ID: 12, Name: rec.Credential.TokenName, Active: true}}
			if failure == "membership" {
				sa.failUpdate = errors.New("offline")
			}
			c := ServiceAccountTokenClient{SA: sa, ContainmentClient: fc, ManagedAccountIDs: sa.ownedIDs}
			if failure == "unresolved" {
				c.SuppliedAccountIDs = func(context.Context, string, string) ([]int, error) { return nil, ErrPollerSuppliedUnresolved }
			}
			_, err := c.CreateProjectAccessToken(ctx, "g", "p", rec.Credential.TokenName, gitlabroles.TokenScopes(), 30, validRolePATExpiry())
			require.Error(t, err)
			assert.Len(t, sa.accounts, 1, "the seeded owned account is reused; no account is created")
			if failure == "unresolved" {
				assert.Empty(t, sa.revoked)
			} else {
				assert.Equal(t, []int{12}, sa.revoked)
			}
		})
	}
}

func TestOutgoingInactivityInspectsExcludedAndRenamedPATs(t *testing.T) {
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.CoderTokenName}}
	sa.tokens[501] = []ProjectAccessToken{{ID: 12, Name: "renamed", Active: true}}
	c := ServiceAccountTokenClient{SA: sa, SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{501}, nil }}
	inactive, err := c.ConfirmOutgoingTokenInactive(context.Background(), "g", "p", 12)
	require.NoError(t, err)
	assert.False(t, inactive)
	sa.tokens[501][0].Revoked = true
	inactive, err = c.ConfirmOutgoingTokenInactive(context.Background(), "g", "p", 12)
	require.NoError(t, err)
	assert.True(t, inactive)
}

func TestPollerMigrationPreservesSecretWhenProtectedGrantFails(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	seedRepo(fc, "group", "project", "main")
	rule := maintainerOnlyRule("main")
	rule.MergeAccessLevels = append(rule.MergeAccessLevels, forge.ProtectedBranchAccess{UserID: 70})
	fc.ProtectedBranchRules["group/project/main"] = rule
	fc.Errors["GrantProtectedBranchMergeUser"] = errors.New("grant denied")
	require.NoError(t, writeRotationState(ctx, fc, "group", "project", rotationStateFile{Roles: map[string]rotationRoleState{"poller": {IncomingID: 1, CreatedTokenIDs: []int{1}, Phase: rotationPhaseIdle, DistributedAt: now.Add(-time.Hour).Format(time.RFC3339)}}}))
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.PollerTokenName, Active: true, UserID: 70, ExpiresAt: GitLabPATExpiresAt(now)})
	sa := newFakeSAAPI()
	c := ServiceAccountTokenClient{SA: sa, Legacy: legacy, ManagedLegacyTokenIDs: recordedLegacyTokens(legacy), VerifyToken: func(ctx context.Context, owner, repo string, tok *ProjectAccessToken) error {
		return VerifyGitLabReplacementPipelineAccess(ctx, fc, owner, repo, tok.UserID)
	}}
	result := &RoleProvisionResult{}
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RolePoller)
	convergeInstalledServiceAccount(ctx, RoleProvisionConfig{Owner: "group", Repo: "project", Client: fc, Tokens: c}, rec, now, true, result)
	require.NotEmpty(t, result.Failed)
	assert.NotEmpty(t, sa.revoked)
	assert.Empty(t, legacy.revoked)
	exists, err := fc.RepoSecretExists(ctx, "group", "project", forge.SecretGitLabPollerToken)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Empty(t, result.Created)
}

// An authentication-only VerifyToken must not let convergence publish a
// replacement Poller that lacks protected-ref pipeline access: the access check
// is composed into the verifier, so the invalid replacement is revoked and the
// working secret is preserved.
func TestPollerConvergenceChecksPipelineAccessWithAuthenticationOnlyVerifier(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	seedRepo(fc, "group", "project", "main")
	rule := maintainerOnlyRule("main")
	rule.MergeAccessLevels = append(rule.MergeAccessLevels, forge.ProtectedBranchAccess{UserID: 70})
	fc.ProtectedBranchRules["group/project/main"] = rule
	fc.Errors["GrantProtectedBranchMergeUser"] = errors.New("grant denied")
	require.NoError(t, writeRotationState(ctx, fc, "group", "project", rotationStateFile{Roles: map[string]rotationRoleState{"poller": {IncomingID: 1, CreatedTokenIDs: []int{1}, Phase: rotationPhaseIdle, DistributedAt: now.Add(-time.Hour).Format(time.RFC3339)}}}))
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.PollerTokenName, Active: true, UserID: 70, ExpiresAt: GitLabPATExpiresAt(now)})
	sa := newFakeSAAPI()
	c := ServiceAccountTokenClient{SA: sa, Legacy: legacy, ManagedLegacyTokenIDs: recordedLegacyTokens(legacy), VerifyToken: acceptReplacementToken}
	result := &RoleProvisionResult{}
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RolePoller)
	convergeInstalledServiceAccount(ctx, RoleProvisionConfig{Owner: "group", Repo: "project", Client: fc, Tokens: c}, rec, now, true, result)
	require.NotEmpty(t, result.Failed)
	assert.NotEmpty(t, sa.revoked, "the unverified replacement credential is revoked")
	assert.Empty(t, legacy.revoked)
	assert.Empty(t, result.Created)
}

func TestDirectRotationContainsInheritedManagedRole(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	require.NoError(t, writeRotationState(ctx, fc, "group", "project", rotationStateFile{Roles: map[string]rotationRoleState{"coder": {ManagedUserID: 501, IncomingID: 12, CreatedTokenIDs: []int{12}, Phase: rotationPhaseIdle, DistributedAt: now.Add(-time.Hour).Format(time.RFC3339)}}}))
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.CoderTokenName}}
	sa.members[501] = 40
	sa.inherited = map[int64]int{501: 40}
	sa.tokens[501] = []ProjectAccessToken{{ID: 12, Name: gitlabroles.CoderTokenName, Active: true, ExpiresAt: GitLabPATExpiresAt(now)}}
	c := ServiceAccountTokenClient{SA: sa, ContainmentClient: fc, ManagedAccountIDs: sa.ownedIDs}
	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{Owner: "group", Repo: "project", Client: fc, Tokens: c, Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true, Now: now})
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.Equal(t, []int{12}, sa.revoked)
	assert.Empty(t, result.Rotated)
	assert.Len(t, sa.accounts, 1, "the seeded owned account is reused; no account is created")
	state, _, err := loadRotationState(ctx, fc, "group", "project")
	require.NoError(t, err)
	assert.Equal(t, 501, state.Roles["coder"].ManagedUserID)
}

// Exclusions recorded in rotation state apply to replacement, minting and
// membership even when the client has no SuppliedAccountIDs callback.
func TestConvergenceAppliesRecordedExclusionsWithoutResolver(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fc := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleCoder)
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	require.NoError(t, writeRotationState(ctx, fc, "group", "project", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {ExcludedUserIDs: []int{501}},
		"coder":  {ManagedUserID: 501, IncomingID: 12, CreatedTokenIDs: []int{12}, Phase: rotationPhaseIdle, DistributedAt: now.Add(-time.Hour).Format(time.RFC3339)},
	}}))
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 12, Name: rec.Credential.TokenName, Active: true, UserID: 70, ExpiresAt: GitLabPATExpiresAt(now)})
	sa := newFakeSAAPI()
	sa.nextUser = 600 // a replacement account must not collide with the seeded ID
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: rec.Credential.TokenName}}
	sa.members[501] = 20
	c := ServiceAccountTokenClient{
		SA: sa, Legacy: legacy, VerifyToken: acceptReplacementToken,
		ManagedLegacyTokenIDs: recordedLegacyTokens(legacy),
		ManagedAccountIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
			return ManagedGitLabRoleAccountIDs(ctx, fc, owner, repo)
		},
	}
	result := &RoleProvisionResult{}
	convergeInstalledServiceAccount(ctx, RoleProvisionConfig{Owner: "group", Repo: "project", Client: fc, Tokens: c}, rec, now, true, result)

	assert.Equal(t, 20, sa.members[501], "excluded account membership is untouched")
	assert.Empty(t, sa.tokens[501], "no credential is minted for the excluded account")
	assert.Empty(t, sa.revoked)
}

// A legacy role (ManagedUserID == 0) migrating to a service account must also
// honor exclusions recorded under other roles, even though the ownership
// resolver still reports the excluded account and there is no
// SuppliedAccountIDs callback.
func TestLegacyMigrationAppliesRecordedExclusionsWithoutManagedUserID(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fc := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleCoder)
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	require.NoError(t, writeRotationState(ctx, fc, "group", "project", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {ExcludedUserIDs: []int{501}},
		"coder":  {IncomingID: 12, CreatedTokenIDs: []int{12}, Phase: rotationPhaseIdle, DistributedAt: now.Add(-time.Hour).Format(time.RFC3339)},
	}}))
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 12, Name: rec.Credential.TokenName, Active: true, UserID: 70, ExpiresAt: GitLabPATExpiresAt(now)})
	sa := newFakeSAAPI()
	sa.nextUser = 600 // a replacement account must not collide with the seeded ID
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: rec.Credential.TokenName}}
	sa.members[501] = 20
	c := ServiceAccountTokenClient{
		SA: sa, Legacy: legacy, VerifyToken: acceptReplacementToken,
		ManagedLegacyTokenIDs: recordedLegacyTokens(legacy),
		ManagedAccountIDs:     func(context.Context, string, string) ([]int, error) { return []int{501}, nil },
	}
	result := &RoleProvisionResult{}
	convergeInstalledServiceAccount(ctx, RoleProvisionConfig{Owner: "group", Repo: "project", Client: fc, Tokens: c}, rec, now, true, result)

	assert.Equal(t, 20, sa.members[501], "excluded account membership is untouched")
	assert.Empty(t, sa.tokens[501], "no credential is minted for the excluded account")
	assert.Empty(t, sa.revoked)
}

// A legacy role (ManagedUserID == 0) fails closed, creating nothing, when
// another role's supplied ownership is unresolved.
func TestLegacyMigrationFailsClosedOnUnresolvedSuppliedOwnership(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fc := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleCoder)
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	require.NoError(t, writeRotationState(ctx, fc, "group", "project", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {Supplied: true},
		"coder":  {IncomingID: 12, CreatedTokenIDs: []int{12}, Phase: rotationPhaseIdle, DistributedAt: now.Add(-time.Hour).Format(time.RFC3339)},
	}}))
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 12, Name: rec.Credential.TokenName, Active: true, UserID: 70, ExpiresAt: GitLabPATExpiresAt(now)})
	sa := newFakeSAAPI()
	c := ServiceAccountTokenClient{
		SA: sa, Legacy: legacy, VerifyToken: acceptReplacementToken,
		ManagedLegacyTokenIDs: recordedLegacyTokens(legacy),
	}
	result := &RoleProvisionResult{}
	convergeInstalledServiceAccount(ctx, RoleProvisionConfig{Owner: "group", Repo: "project", Client: fc, Tokens: c}, rec, now, true, result)

	require.Len(t, result.Failed, 1)
	assert.Contains(t, result.Failed[0].Reason, "ownership unresolved")
	assert.Empty(t, sa.createdSAs, "no account is created")
	assert.Empty(t, sa.tokens)
	assert.Empty(t, sa.revoked)
}

// Initial distribution fills a token-only creation record written by the
// legacy fallback, preserving the creation record and recorded exclusions.
func TestInitialDistributionFillsTokenOnlyCreationRecord(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {CreatedTokenIDs: []int{7}, ExcludedUserIDs: []int{501}},
	}}))
	require.NoError(t, recordInitialDistribution(ctx, fc, "g", "p", gitlabroles.RoleCoder, 7, "2026-11-01", now))
	state, _, err := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	rs := state.Roles["coder"]
	assert.Equal(t, 7, rs.IncomingID)
	assert.NotEmpty(t, rs.DistributedAt)
	assert.Equal(t, rotationPhaseIdle, rs.Phase)
	assert.Equal(t, []int{7}, rs.CreatedTokenIDs)
	assert.Equal(t, []int{501}, rs.ExcludedUserIDs)

	// A creation record for a different token is not lifecycle proof for this one.
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {CreatedTokenIDs: []int{6}},
	}}))
	require.NoError(t, recordInitialDistribution(ctx, fc, "g", "p", gitlabroles.RoleCoder, 7, "2026-11-01", now))
	state, _, err = loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Zero(t, state.Roles["coder"].IncomingID)
}

// Missing-secret provisioning applies exclusions recorded under another role,
// even when the ownership resolver still reports the excluded account and the
// client has no SuppliedAccountIDs callback: the account's membership, PATs
// and revocations stay untouched and a fresh account is used instead.
func TestMissingSecretProvisioningAppliesRecordedExclusions(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {ExcludedUserIDs: []int{501}},
	}}))
	sa := newFakeSAAPI()
	sa.nextUser = 600 // a new account must not collide with the seeded ID
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: rec.Credential.TokenName}}
	sa.members[501] = 20
	c := ServiceAccountTokenClient{
		SA:                sa,
		ManagedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{501}, nil },
	}
	result := &RoleProvisionResult{}
	present := map[string]bool{}
	for _, other := range gitlabroles.BuiltinRegistry().Registrations() {
		present[other.Credential.SecretName] = other.Name != rec.Name
	}
	provisionOwnRoles(ctx, RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c}, gitlabroles.BuiltinRegistry(), present, result)

	assert.Equal(t, 20, sa.members[501], "excluded account membership is untouched")
	assert.Empty(t, sa.tokens[501], "no credential is minted for the excluded account")
	assert.Empty(t, sa.revoked)
	assert.Equal(t, []string{rec.Credential.TokenName}, sa.createdSAs, "a new account is created instead of reusing the excluded one")
	assert.Equal(t, []gitlabroles.Role{rec.Name}, result.Created)
	assert.Empty(t, result.Failed)
}

// Missing-secret provisioning fails closed, creating nothing, when the
// project-wide supplied-account exclusions cannot be attributed.
func TestMissingSecretProvisioningFailsClosedOnUnresolvedExclusions(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: rec.Credential.TokenName}}
	sa.members[501] = 20
	c := ServiceAccountTokenClient{
		SA:                 sa,
		ManagedAccountIDs:  func(context.Context, string, string) ([]int, error) { return []int{501}, nil },
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return nil, ErrPollerSuppliedUnresolved },
	}
	result := &RoleProvisionResult{}
	present := map[string]bool{}
	for _, other := range gitlabroles.BuiltinRegistry().Registrations() {
		present[other.Credential.SecretName] = other.Name != rec.Name
	}
	provisionOwnRoles(ctx, RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c}, gitlabroles.BuiltinRegistry(), present, result)

	// Convergence also resolves the project-wide exclusions for roles whose
	// secret is present, so every registered role fails closed.
	var failedRoles []gitlabroles.Role
	for _, failure := range result.Failed {
		failedRoles = append(failedRoles, failure.Role)
	}
	assert.Contains(t, failedRoles, rec.Name)
	assert.Len(t, result.Failed, len(gitlabroles.BuiltinRegistry().Registrations()))
	assert.Empty(t, result.Created)
	assert.Empty(t, sa.createdSAs)
	assert.Equal(t, 20, sa.members[501])
	assert.Empty(t, sa.tokens[501])
}

// A forbidden/not-found class from membership repair is a failure, not
// service-account capability absence: it is reported rather than swallowed as
// "keeping the existing legacy credential".
func TestInstallConvergenceReportsForbiddenMembershipRepairAsFailure(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RolePoller)
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 1, Name: rec.Credential.TokenName, Active: true, ExpiresAt: GitLabPATExpiresAt(now), UserID: 101})
	require.NoError(t, writeRotationState(ctx, fc, "group", "project", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {CreatedTokenIDs: []int{1}, Phase: rotationPhaseIdle, IncomingID: 1, DistributedAt: now.Add(-time.Hour).Format(time.RFC3339)},
	}}))
	sa := newFakeSAAPI()
	sa.failAdd = forge.ErrForbidden // Account creation succeeds; membership repair does not.
	c := ServiceAccountTokenClient{SA: sa, Legacy: legacy, VerifyToken: acceptReplacementToken, ManagedLegacyTokenIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
		return ManagedGitLabLegacyTokenIDs(ctx, fc, owner, repo)
	}}
	result := &RoleProvisionResult{}
	convergeInstalledServiceAccount(ctx, RoleProvisionConfig{Owner: "group", Repo: "project", Client: fc, Tokens: c, Now: now}, rec, now, true, result)

	require.NotEmpty(t, result.Failed)
	assert.NotContains(t, strings.Join(result.Diagnostics, " "), "keeping the existing legacy credential")
	assert.Empty(t, legacy.revoked)
}

// Cleanup applies exclusions recorded in rotation state before revoking
// anything, even with no SuppliedAccountIDs callback: an excluded account that
// the ownership resolver still reports keeps its role-named PATs.
func TestCleanupAppliesRecordedExclusionsBeforeRevocation(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	state := rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {ExcludedUserIDs: []int{501}},
	}}
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", state))
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.CoderTokenName}}
	sa.tokens[501] = []ProjectAccessToken{{ID: 7, Name: gitlabroles.CoderTokenName, Active: true}}
	c := ServiceAccountTokenClient{
		SA:                sa,
		ManagedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{501}, nil },
	}
	result, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c})
	require.NoError(t, err)
	assert.Empty(t, sa.revoked, "an excluded account's credentials are never revoked")
	assert.Zero(t, result.TokensRevoked)
	assert.True(t, sa.tokens[501][0].Active)
	assert.Empty(t, fc.DeletedProjectServiceAccounts)
}

// Unresolved supplied ownership is refused in the preflight, before cleanup
// deletes variables or revokes tokens.
func TestCleanupRefusesUnresolvedSuppliedOwnershipBeforeDestruction(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {ManagedUserID: 501},
		"coder":  {Supplied: true},
	}}))
	secret := forge.VarGitLabRoleRegistry
	fc.VariablesExist["g/p/"+secret] = true
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.PollerTokenName}}
	sa.tokens[501] = []ProjectAccessToken{{ID: 7, Name: gitlabroles.PollerTokenName, Active: true}}
	c := ServiceAccountTokenClient{SA: sa}
	err := PreflightGitLabRoleCleanupOwnership(ctx, fc, "g", "p", c)
	require.ErrorIs(t, err, ErrSuppliedCredentialUnresolved)
	_, err = CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: c})
	require.ErrorIs(t, err, ErrSuppliedCredentialUnresolved)
	assert.Empty(t, sa.revoked)
	assert.True(t, fc.VariablesExist["g/p/"+secret], "variables are untouched")
	assert.True(t, fc.VariablesExist["g/p/"+forge.VarGitLabRoleRotation])
}

// A negative managed account ID is malformed ownership evidence: the uninstall
// preflight fails closed and cleanup leaves variables, tokens and rotation
// state untouched.
func TestCleanupPreflightRejectsNegativeManagedUserID(t *testing.T) {
	ctx := context.Background()
	for _, tokens := range []ProjectAccessTokenClient{&fakeTokens{}, ServiceAccountTokenClient{SA: newFakeSAAPI()}} {
		fc := forge.NewFakeClient()
		require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
			"poller": {ManagedUserID: -501},
		}}))
		secret := forge.VarGitLabRoleRegistry
		fc.VariablesExist["g/p/"+secret] = true
		before := fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation]
		require.Error(t, PreflightGitLabRoleCleanupOwnership(ctx, fc, "g", "p", tokens))
		_, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: tokens})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be negative")
		assert.True(t, fc.VariablesExist["g/p/"+secret])
		assert.Equal(t, before, fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation])
		if ft, ok := tokens.(*fakeTokens); ok {
			assert.Empty(t, ft.revoked)
		}
	}
}

// An exclusions-only entry (left by uninstall) receives initial distribution
// proof, including a supplied enrollment with no token ID, and keeps its
// exclusions.
func TestInitialDistributionFillsExclusionsOnlyEntry(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	for _, tokenID := range []int{0, 7} {
		t.Run(fmt.Sprintf("token-%d", tokenID), func(t *testing.T) {
			fc := forge.NewFakeClient()
			require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
				"coder": {ExcludedUserIDs: []int{501}},
			}}))
			require.NoError(t, recordInitialDistribution(ctx, fc, "g", "p", gitlabroles.RoleCoder, tokenID, "", now))
			state, _, err := loadRotationState(ctx, fc, "g", "p")
			require.NoError(t, err)
			rs := state.Roles["coder"]
			assert.Equal(t, tokenID, rs.IncomingID)
			assert.NotEmpty(t, rs.DistributedAt)
			assert.Equal(t, rotationPhaseIdle, rs.Phase)
			assert.Equal(t, []int{501}, rs.ExcludedUserIDs)
		})
	}

	// An entry carrying lifecycle or lock state is never overwritten.
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {ExcludedUserIDs: []int{501}, Holder: "other"},
	}}))
	require.NoError(t, recordInitialDistribution(ctx, fc, "g", "p", gitlabroles.RoleCoder, 7, "", now))
	state, _, err := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Zero(t, state.Roles["coder"].IncomingID)
}

func TestInstallConvergenceDryRunPlansMembershipDrift(t *testing.T) {
	ctx := context.Background()
	for _, level := range []int{20, 40} {
		t.Run(fmt.Sprintf("level-%d", level), func(t *testing.T) {
			fc := forge.NewFakeClient()
			sa := newFakeSAAPI()
			sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.PollerTokenName}}
			sa.members[501] = level
			require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{"poller": {ManagedUserID: 501, IncomingID: 1}}}))
			cfg := RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: ServiceAccountTokenClient{SA: sa}, DryRun: true}
			result := RoleProvisionResult{}
			rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RolePoller)
			convergeInstalledServiceAccount(ctx, cfg, rec, time.Now(), false, &result)
			assert.Empty(t, result.Failed)
			require.Len(t, result.Diagnostics, 1)
			assert.Contains(t, result.Diagnostics[0], "would be reconciled to Developer")
			assert.Equal(t, level, sa.members[501], "dry run must not mutate membership")
		})
	}
}

func TestWithProjectExclusionsRejectsMalformedRecordedIDs(t *testing.T) {
	for name, rs := range map[string]rotationRoleState{
		"zero excluded":     {ExcludedUserIDs: []int{0}},
		"negative excluded": {ExcludedUserIDs: []int{501, -1}},
		"negative supplied": {SuppliedUserID: -1},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := (ServiceAccountTokenClient{}).withProjectExclusions(context.Background(), "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{"coder": rs}})
			require.Error(t, err)
		})
	}
}
