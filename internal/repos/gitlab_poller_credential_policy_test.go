package repos

import (
	"context"
	"errors"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func emptyPollerCredentialOperations() GitLabPollerContainmentOperations {
	return GitLabPollerContainmentOperations{
		GitLabPollerCredentialInventory: GitLabPollerCredentialInventory{
			Tokens:            func(context.Context) ([]ProjectAccessToken, error) { return nil, nil },
			UnmanagedTriggers: func(context.Context) ([]int64, error) { return nil, nil },
			SSHKeys:           func(context.Context) ([]int, error) { return nil, nil },
			Jobs:              func(context.Context) ([]int, error) { return nil, nil },
			Schedules:         func(context.Context) ([]int, error) { return nil, nil },
		},
		RevokeToken:                  func(context.Context, int) error { return nil },
		InstalledCredentialBelongsTo: func(context.Context) (bool, error) { return false, nil },
		DeleteInstalledCredential:    func(context.Context) error { return nil },
	}
}

func TestPollerCredentialPolicyRejectsRemainingPaths(t *testing.T) {
	ctx := context.Background()
	require.NoError(t, VerifyGitLabPollerCredentials(ctx, 77, false, emptyPollerCredentialOperations().GitLabPollerCredentialInventory))
	require.Error(t, VerifyGitLabPollerCredentials(ctx, 77, false, GitLabPollerCredentialInventory{}))
	for _, path := range []string{"PAT", "trigger", "SSH", "job", "schedule"} {
		for _, failure := range []bool{false, true} {
			t.Run(path+map[bool]string{false: "-present", true: "-unreadable"}[failure], func(t *testing.T) {
				inventory := emptyPollerCredentialOperations()
				var err error
				if failure {
					err = errors.New("inventory unavailable")
				}
				switch path {
				case "PAT":
					inventory.Tokens = func(context.Context) ([]ProjectAccessToken, error) {
						return []ProjectAccessToken{{ID: 1, Name: "unmanaged", Active: true}}, err
					}
				case "trigger":
					inventory.UnmanagedTriggers = func(context.Context) ([]int64, error) { return []int64{1}, err }
				case "SSH":
					inventory.SSHKeys = func(context.Context) ([]int, error) { return []int{1}, err }
				case "job":
					inventory.Jobs = func(context.Context) ([]int, error) { return []int{1}, err }
				case "schedule":
					inventory.Schedules = func(context.Context) ([]int, error) { return []int{1}, err }
				}
				require.Error(t, VerifyGitLabPollerCredentials(ctx, 77, false, inventory.GitLabPollerCredentialInventory))
				if failure {
					var unsafe *PollerElevationUnsafeError
					assert.False(t, errors.As(VerifyGitLabPollerCredentials(ctx, 77, false, inventory.GitLabPollerCredentialInventory), &unsafe))
				}
			})
		}
	}
	inventory := emptyPollerCredentialOperations()
	inventory.Tokens = func(context.Context) ([]ProjectAccessToken, error) {
		return []ProjectAccessToken{{ID: 1, Name: gitlabroles.PollerTokenName, Active: true}, {ID: 2, Name: gitlabroles.PollerBootstrapTokenName, Active: true}, {ID: 3, Name: "unmanaged", Revoked: true}}, nil
	}
	require.NoError(t, VerifyGitLabPollerCredentials(ctx, 77, false, inventory.GitLabPollerCredentialInventory))
	require.Error(t, VerifyGitLabPollerCredentials(ctx, 77, true, inventory.GitLabPollerCredentialInventory))
}

func TestPollerContainmentPreservesUnmanagedPaths(t *testing.T) {
	ctx := context.Background()
	require.ErrorIs(t, ContainGitLabPoller(ctx, 77, GitLabPollerContainmentOperations{}), ErrPollerContainmentIncomplete)
	inventory := emptyPollerCredentialOperations()
	require.NoError(t, ContainGitLabPoller(ctx, 77, inventory))
	var revoked []int
	var attributed, deleted bool
	inventory.InstalledCredentialBelongsTo = func(context.Context) (bool, error) { attributed = true; return true, nil }
	inventory.DeleteInstalledCredential = func(context.Context) error { deleted = true; return nil }
	inventory.Tokens = func(context.Context) ([]ProjectAccessToken, error) {
		assert.True(t, attributed)
		tokens := []ProjectAccessToken{{ID: 2, Name: "unmanaged", Active: true}, {ID: 3, Revoked: true}}
		if len(revoked) == 0 {
			tokens = append(tokens, ProjectAccessToken{ID: 1, Name: gitlabroles.PollerTokenName, Active: true})
		}
		return tokens, nil
	}
	inventory.RevokeToken = func(_ context.Context, id int) error { revoked = append(revoked, id); return nil }
	inventory.UnmanagedTriggers = func(context.Context) ([]int64, error) { return []int64{4}, nil }
	inventory.SSHKeys = func(context.Context) ([]int, error) { return []int{5}, nil }
	inventory.Jobs = func(context.Context) ([]int, error) { return []int{6}, nil }
	inventory.Schedules = func(context.Context) ([]int, error) { return []int{7}, nil }
	err := ContainGitLabPoller(ctx, 77, inventory)
	require.ErrorIs(t, err, ErrPollerContainmentIncomplete)
	assert.Equal(t, []int{1}, revoked)
	assert.True(t, deleted)
	for _, text := range []string{"not managed", "SSH", "unfinished", "schedule"} {
		assert.Contains(t, err.Error(), text)
	}
}

func TestPollerContainmentCollectsInventoryFailures(t *testing.T) {
	inventory := emptyPollerCredentialOperations()
	cause := errors.New("inventory unavailable")
	inventory.Tokens = func(context.Context) ([]ProjectAccessToken, error) { return nil, cause }
	inventory.UnmanagedTriggers = func(context.Context) ([]int64, error) { return nil, cause }
	inventory.SSHKeys = func(context.Context) ([]int, error) { return nil, cause }
	inventory.Jobs = func(context.Context) ([]int, error) { return nil, cause }
	inventory.Schedules = func(context.Context) ([]int, error) { return nil, cause }
	inventory.InstalledCredentialBelongsTo = func(context.Context) (bool, error) { return false, cause }
	require.ErrorIs(t, ContainGitLabPoller(context.Background(), 77, inventory), cause)
}
