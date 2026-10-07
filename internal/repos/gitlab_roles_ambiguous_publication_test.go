package repos

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type ambiguousPublicationClient struct {
	*forge.FakeClient
	commitStore bool
	failStore   bool
}

func (c *ambiguousPublicationClient) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	if !c.failStore || c.commitStore {
		if err := c.FakeClient.CreateRepoSecret(ctx, owner, repo, name, value); err != nil {
			return err
		}
	}
	if c.failStore {
		return errors.New("publication response lost")
	}
	return nil
}

// The operational inventory can omit one token's source without losing the
// ability to revoke it later. This exercises recovery from persisted IDs.
type ambiguousPublicationTokens struct {
	*fakeTokens
	omitID int
}

func (c *ambiguousPublicationTokens) ListProjectAccessTokens(ctx context.Context, owner, repo string) ([]ProjectAccessToken, error) {
	listed, err := c.fakeTokens.ListProjectAccessTokens(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	out := make([]ProjectAccessToken, 0, len(listed))
	for _, tok := range listed {
		if tok.ID == c.omitID {
			continue
		}
		if tok.UserID == 0 {
			tok.UserID = 71
		}
		out = append(out, tok)
	}
	return out, nil
}

func TestRotateGitLabRolesRecoversAmbiguousPublication(t *testing.T) {
	for _, provenance := range []string{"supplied", "managed"} {
		t.Run(provenance, func(t *testing.T) {
			for _, publication := range []struct {
				name   string
				commit bool
			}{
				{name: "committed-response-lost", commit: true},
				{name: "uncommitted-response-lost"},
			} {
				t.Run(publication.name, func(t *testing.T) {
					ctx := context.Background()
					now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
					fc := seededRoleClient(t, gitlabroles.RoleAnalyst)
					rs := rotationRoleState{Phase: rotationPhaseIdle, IncomingID: 50, DistributedAt: now.Add(-time.Hour).Format(time.RFC3339)}
					ownerID := 71
					if provenance == "supplied" {
						rs.IncomingID = 0
						rs.markSupplied(70)
						rs.setSuppliedToken(70, 50)
						rs.SuppliedDistributed = true
						ownerID = 70
					}
					require.NoError(t, writeRotationState(ctx, fc, "group", "project", rotationStateFile{
						Roles: map[string]rotationRoleState{"analyst": rs},
					}))
					tokens := &ambiguousPublicationTokens{fakeTokens: &fakeTokens{}}
					tokens.seed(ProjectAccessToken{ID: 50, Name: gitlabroles.AnalystTokenName, Active: true, UserID: ownerID, ExpiresAt: GitLabPATExpiresAt(now)})
					client := &ambiguousPublicationClient{FakeClient: fc, commitStore: publication.commit, failStore: true}
					cfg := RoleRotateConfig{Owner: "group", Repo: "project", Client: client, Tokens: tokens,
						Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleAnalyst}, Force: true, Now: now}
					first, err := RotateGitLabRoleCredentials(ctx, cfg)
					require.NoError(t, err)
					require.Len(t, first.Failed, 1)
					assert.Empty(t, first.RolledBack)
					state, _, err := loadRotationState(ctx, fc, "group", "project")
					require.NoError(t, err)
					pending := state.Roles["analyst"]
					require.NotZero(t, pending.IncomingID)
					assert.Equal(t, rotationPhaseDistributing, pending.Phase)
					assert.False(t, pending.SuppliedDistributed)
					assert.Empty(t, tokens.revoked, "a potentially published token stays active")
					if publication.commit {
						require.NotEmpty(t, fc.CreatedSecrets)
						stored := fc.CreatedSecrets[len(fc.CreatedSecrets)-1]
						assert.Equal(t, forge.SecretGitLabAnalystToken, stored.Name)
						assert.Equal(t, tokens.created[0].Token, stored.Value)
					}

					// The next ordinary rotation must recover even though the old
					// supplied credential is healthy and the pending token is omitted.
					client.failStore = false
					tokens.omitID = pending.IncomingID
					cfg.Force = false
					// A failed recovery mint must retain the same incoming ID
					// through phase=failed for another ordinary retry.
					tokens.failCreate = map[string]error{gitlabroles.AnalystTokenName: errors.New("recovery mint unavailable")}
					failedRecovery, err := RotateGitLabRoleCredentials(ctx, cfg)
					require.NoError(t, err)
					require.Len(t, failedRecovery.Failed, 1)
					state, _, err = loadRotationState(ctx, fc, "group", "project")
					require.NoError(t, err)
					assert.Equal(t, rotationPhaseFailed, state.Roles["analyst"].Phase)
					assert.Equal(t, pending.IncomingID, state.Roles["analyst"].IncomingID)
					assert.False(t, state.Roles["analyst"].SuppliedDistributed)
					assert.NotContains(t, tokens.revoked, pending.IncomingID)
					tokens.failCreate = nil
					second, err := RotateGitLabRoleCredentials(ctx, cfg)
					require.NoError(t, err)
					assert.Empty(t, second.Failed)
					assert.Contains(t, second.Rotated, gitlabroles.RoleAnalyst)
					state, _, err = loadRotationState(ctx, fc, "group", "project")
					require.NoError(t, err)
					complete := state.Roles["analyst"]
					assert.False(t, complete.Supplied)
					assert.Contains(t, complete.OutgoingIDs, pending.IncomingID)
					assert.NotContains(t, tokens.revoked, pending.IncomingID, "recovery honors the in-flight grace period")
					if provenance == "supplied" {
						assert.Contains(t, complete.ExcludedUserIDs, 70)
						assert.NotContains(t, complete.OutgoingIDs, 50)
					}

					cfg.Now = now.Add(25 * time.Hour)
					third, err := RotateGitLabRoleCredentials(ctx, cfg)
					require.NoError(t, err)
					assert.Empty(t, third.Failed)
					assert.Contains(t, tokens.revoked, pending.IncomingID)
					assert.NotContains(t, tokens.revoked, complete.IncomingID)
					if provenance == "supplied" {
						assert.NotContains(t, tokens.revoked, 50)
					}
				})
			}
		})
	}
}
