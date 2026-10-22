package repos

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyGitLabAccountDeletion(t *testing.T) {
	for _, blocked := range []string{"", "ssh", "jobs", "schedules", "triggers", "inventory errors"} {
		t.Run(blocked, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := map[string]int{}
			failures := map[string]error{}
			inspect := func(name string) func(context.Context) ([]int, error) {
				return func(got context.Context) ([]int, error) {
					assert.Equal(t, ctx, got)
					calls[name]++
					if blocked == "inventory errors" {
						failures[name] = errors.New(name + " unavailable")
						return nil, failures[name]
					}
					if blocked == name {
						return []int{9}, nil
					}
					return nil, nil
				}
			}
			err := VerifyGitLabAccountDeletion(ctx, 77, GitLabAccountDeletionInventory{
				SSHKeys: inspect("ssh"), Jobs: inspect("jobs"), Schedules: inspect("schedules"), Triggers: inspect("triggers"),
			})
			if blocked == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			for _, name := range []string{"ssh", "jobs", "schedules", "triggers"} {
				assert.Equal(t, 1, calls[name], "all resource checks run even when another fails")
			}
			for _, failure := range failures {
				assert.ErrorIs(t, err, failure, "independent inventory errors are retained")
			}
		})
	}
}

func TestVerifyGitLabAccountDeletion_IncompleteInventory(t *testing.T) {
	inspect := func(context.Context) ([]int, error) { return nil, nil }
	for _, mode := range []string{"ssh", "jobs", "schedules", "triggers", "zero ID", "negative ID"} {
		t.Run(mode, func(t *testing.T) {
			inventory := GitLabAccountDeletionInventory{SSHKeys: inspect, Jobs: inspect, Schedules: inspect, Triggers: inspect}
			id := 77
			switch mode {
			case "ssh":
				inventory.SSHKeys = nil
			case "jobs":
				inventory.Jobs = nil
			case "schedules":
				inventory.Schedules = nil
			case "triggers":
				inventory.Triggers = nil
			case "zero ID":
				id = 0
			case "negative ID":
				id = -1
			}
			require.Error(t, VerifyGitLabAccountDeletion(context.Background(), id, inventory))
		})
	}
}
