package repos

import (
	"context"
	"errors"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCleanupRequiresManagedAccountCapability(t *testing.T) {
	for _, tokens := range []ProjectAccessTokenClient{nil, &fakeTokens{}} {
		for _, exclusion := range []string{"none", "supplied", "excluded"} {
			t.Run(exclusion, func(t *testing.T) {
				ctx := context.Background()
				fc := forge.NewFakeClient()
				state := rotationStateFile{Roles: map[string]rotationRoleState{"coder": {ManagedUserID: 77}}}
				switch exclusion {
				case "supplied":
					state.Roles["analyst"] = rotationRoleState{SuppliedUserID: 77}
				case "excluded":
					state.Roles["analyst"] = rotationRoleState{ExcludedUserIDs: []int{77}}
				}
				require.NoError(t, writeRotationState(ctx, fc, "g", "p", state))
				_, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: tokens})
				if exclusion == "none" {
					require.ErrorContains(t, err, "ownership retained for retry")
					got, _, readErr := loadRotationState(ctx, fc, "g", "p")
					require.NoError(t, readErr)
					assert.Equal(t, state, got)
					assert.Empty(t, fc.DeletedSecrets)
				} else {
					require.NoError(t, err)
				}
			})
		}
	}
}

func TestCleanupOwnershipReadErrorIsRedacted(t *testing.T) {
	const credential = "installer credential echo fixture"
	cause := errors.Join(forge.ErrForbidden, errors.New(credential))
	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{"GetRepoVariable": cause}
	ctx := context.Background()
	checks := []func() error{
		func() error {
			_, err := CleanupGitLabRoleIdentityLocked(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc})
			return err
		},
		func() error {
			_, err := (ServiceAccountTokenClient{}).deleteManagedServiceAccounts(ctx, fc, "g", "p")
			return err
		},
		func() error {
			return retireRotationState(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc}, &GitLabRoleCleanupResult{})
		},
	}
	for _, check := range checks {
		err := check()
		require.Error(t, err)
		assert.ErrorIs(t, err, forge.ErrForbidden)
		assert.NotContains(t, err.Error(), credential)
	}
}

func TestCleanupRetirementWriteErrorsAreRedacted(t *testing.T) {
	ctx := context.Background()
	const credential = "installer credential echoed during retirement"
	for _, operation := range []string{"DeleteRepoVariable", "UpdateCIVariable"} {
		t.Run(operation, func(t *testing.T) {
			fc := forge.NewFakeClient()
			state := rotationStateFile{Roles: map[string]rotationRoleState{}}
			if operation == "UpdateCIVariable" {
				state.Roles["coder"] = rotationRoleState{ExcludedUserIDs: []int{77}}
			}
			require.NoError(t, writeRotationState(ctx, fc, "g", "p", state))
			fc.Errors = map[string]error{operation: errors.Join(forge.ErrForbidden, errors.New(credential))}
			err := retireRotationState(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc}, &GitLabRoleCleanupResult{})
			require.Error(t, err)
			assert.ErrorIs(t, err, forge.ErrForbidden)
			assert.NotContains(t, err.Error(), credential)
			got, _, readErr := loadRotationState(ctx, fc, "g", "p")
			require.NoError(t, readErr)
			assert.Equal(t, state, got)
		})
	}
}
