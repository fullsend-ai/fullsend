package repos

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

func TestMergeRoleStatePreservesFreshCreationOwnership(t *testing.T) {
	for _, staleID := range []int{0, 77} {
		t.Run(strconv.Itoa(staleID), func(t *testing.T) {
			ctx := context.Background()
			client := forge.NewFakeClient()
			fresh := rotationStateFile{Roles: map[string]rotationRoleState{
				"poller": {ManagedUserID: 90, IncomingID: 7},
				"coder":  {ManagedUserID: 91},
			}}
			require.NoError(t, writeRotationState(ctx, client, "g", "p", fresh))
			stale := rotationRoleState{ManagedUserID: staleID, Phase: rotationPhaseFailed, Error: "mint failed"}
			var snapshot rotationStateFile
			require.NoError(t, mergeRoleState(ctx, client, "g", "p", gitlabroles.RolePoller, "", time.Now(), stale, &snapshot))
			stored, _, err := loadRotationState(ctx, client, "g", "p")
			require.NoError(t, err)
			assert.Equal(t, 90, stored.Roles["poller"].ManagedUserID)
			assert.Equal(t, rotationPhaseFailed, stored.Roles["poller"].Phase)
			assert.Equal(t, 91, stored.Roles["coder"].ManagedUserID)
		})
	}
}
