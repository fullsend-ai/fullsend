package repos

import (
	"context"
	"errors"
	"fmt"
)

// GitLabAccountDeletionInventory supplies resource IDs that could remain
// usable or owned by an account. Adapters include resources with unknown owners
// conservatively. Every callback is required and must exhaust its inventory.
type GitLabAccountDeletionInventory struct {
	SSHKeys   func(context.Context) ([]int, error)
	Jobs      func(context.Context) ([]int, error)
	Schedules func(context.Context) ([]int, error)
	Triggers  func(context.Context) ([]int, error)
}

// VerifyGitLabAccountDeletion refuses deletion if credentials or owned
// resources remain, or any inventory cannot be verified. PATs are checked
// separately by managed-account cleanup before this resource check.
func VerifyGitLabAccountDeletion(ctx context.Context, userID int, inventory GitLabAccountDeletionInventory) error {
	if userID <= 0 || inventory.SSHKeys == nil || inventory.Jobs == nil || inventory.Schedules == nil || inventory.Triggers == nil {
		return fmt.Errorf("service account deletion requires a positive account ID and complete resource inventory")
	}
	keys, keyErr := inventory.SSHKeys(ctx)
	jobs, jobErr := inventory.Jobs(ctx)
	schedules, scheduleErr := inventory.Schedules(ctx)
	triggers, triggerErr := inventory.Triggers(ctx)
	if err := errors.Join(keyErr, jobErr, scheduleErr, triggerErr); err != nil {
		return err
	}
	if len(keys)+len(jobs)+len(schedules)+len(triggers) > 0 {
		return fmt.Errorf("service account %d has SSH credentials, unfinished jobs or owned schedules/triggers; not deleted", userID)
	}
	return nil
}
