package repos

import (
	"context"
	"errors"
	"fmt"
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
	legacy := &fakeTokens{}
	sa := newFakeSAAPI()
	state := rotationStateFile{Roles: map[string]rotationRoleState{}}
	for i, rec := range gitlabroles.BuiltinRegistry().Registrations() {
		id := i + 1
		legacy.seed(ProjectAccessToken{ID: id, Name: rec.Credential.TokenName, Active: true, ExpiresAt: GitLabPATExpiresAt(now), UserID: 100 + id})
		state.Roles[string(rec.Name)] = rotationRoleState{Phase: rotationPhaseIdle, IncomingID: id, DistributedAt: now.Format(time.RFC3339)}
	}
	require.NoError(t, writeRotationState(ctx, fc, "group", "project", state))
	c := ServiceAccountTokenClient{SA: sa, Legacy: legacy}
	c.AccountCreated = func(ctx context.Context, owner, repo string, account GitLabServiceAccount) error {
		return RecordManagedServiceAccount(ctx, fc, owner, repo, account)
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
			got, err := (ServiceAccountTokenClient{SA: sa, Legacy: legacy}).needsServiceAccountReplacement(ctx, "g", "p", tc.rs)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantErr, err != nil)
		})
	}
}

func TestCreatedAccountProofAndCredentialFailure(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	for _, account := range []GitLabServiceAccount{{ID: 0}, {ID: 77, Name: "unregistered"}} {
		require.Error(t, RecordManagedServiceAccount(ctx, fc, "g", "p", account))
	}
	require.NoError(t, RecordManagedServiceAccount(ctx, fc, "g", "p", GitLabServiceAccount{ID: 77, Name: gitlabroles.PollerTokenName}))
	state, _, err := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, 77, state.Roles["poller"].ManagedUserID)
	assert.False(t, state.Roles["poller"].exclusionsOnly())
	for _, mode := range []string{"validate", "record"} {
		t.Run(mode, func(t *testing.T) {
			sa := newFakeSAAPI()
			c := ServiceAccountTokenClient{SA: sa}
			if mode == "validate" {
				c.VerifyToken = func(context.Context, string, string, *ProjectAccessToken) error { return errors.New("invalid") }
			} else {
				c.AccountCreated = func(context.Context, string, string, GitLabServiceAccount) error { return errors.New("state failed") }
			}
			tok, err := c.CreateProjectAccessToken(ctx, "g", "p", gitlabroles.PollerTokenName, []string{"api"}, 30, "2027-01-01")
			require.Error(t, err)
			assert.Nil(t, tok)
			if mode == "validate" {
				assert.Len(t, sa.revoked, 1)
			} else {
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
			require.Error(t, RecordManagedServiceAccount(context.Background(), fc, "g", "p", GitLabServiceAccount{ID: 77, Name: gitlabroles.PollerTokenName}))
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
			n, err := c.deleteManagedServiceAccounts(context.Background(), fc, "g", "p")
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
	n, err := (ServiceAccountTokenClient{}).deleteManagedServiceAccounts(context.Background(), fc, "g", "p")
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
			n, err := (ServiceAccountTokenClient{SA: sa, VerifyAccountDeletion: func(context.Context, string, string, int) error { return nil }}).deleteManagedServiceAccounts(ctx, fc, "g", "p")
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
			cfg := RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: ServiceAccountTokenClient{SA: sa, Legacy: legacy}, DryRun: mode == "dry-run"}
			result := RoleProvisionResult{}
			rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RolePoller)
			convergeInstalledServiceAccount(ctx, cfg, rec, time.Now(), &result)
			assert.Empty(t, sa.accounts)
			assert.Empty(t, legacy.revoked)
			if mode == "state" || mode == "inventory" {
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
			n, err := c.deleteManagedServiceAccounts(ctx, fc, "g", "p")
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
	n, err := c.deleteManagedServiceAccounts(ctx, fc, "g", "p")
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
				state.Roles[string(rec.Name)] = rotationRoleState{Phase: rotationPhaseIdle, IncomingID: id, DistributedAt: now.Add(-time.Hour).Format(time.RFC3339)}
			}
			require.NoError(t, writeRotationState(ctx, fc, "group", "project", state))
			before, _, err := loadRotationState(ctx, fc, "group", "project")
			require.NoError(t, err)
			c := ServiceAccountTokenClient{SA: sa, Legacy: legacy}
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
					return RecordManagedServiceAccount(ctx, fc, owner, repo, account)
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
			c := ServiceAccountTokenClient{SA: sa, Legacy: legacy}
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
			cleaned := cleanupOutgoing(ctx, RoleRotateConfig{Owner: "g", Repo: "p", Tokens: c}, &rs, now, 24*time.Hour, &listed)
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
