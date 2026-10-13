package repos

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// Service-account creation refuses to mint a credential it cannot authenticate,
// before any account, membership or token mutation, as legacy creation does.
func TestServiceAccountCreateRefusesWithoutVerifier(t *testing.T) {
	t.Parallel()
	sa := newFakeSAAPI()
	c := ServiceAccountTokenClient{SA: sa, ManagedAccountIDs: sa.ownedIDs}

	tok, err := c.CreateProjectAccessToken(context.Background(), "g", "p", gitlabroles.PollerTokenName, gitlabroles.TokenScopes(), gitlabroles.DeveloperAccessLevel, validRolePATExpiry())
	require.Error(t, err)
	assert.Nil(t, tok)
	assert.Contains(t, err.Error(), "verification unavailable")
	assert.Empty(t, sa.accounts, "no service account is created")
	assert.Empty(t, sa.members, "no membership is granted")
	assert.Empty(t, sa.tokens, "no token is minted")
}

// Composing the Poller access check must not turn a missing authentication
// callback into an apparent verifier.
func TestWithPollerPipelineAccessKeepsNilVerifierNil(t *testing.T) {
	t.Parallel()
	c := ServiceAccountTokenClient{}.withPollerPipelineAccess(forge.NewFakeClient())
	assert.Nil(t, c.VerifyToken)

	c = ServiceAccountTokenClient{VerifyToken: acceptReplacementToken}.withPollerPipelineAccess(forge.NewFakeClient())
	assert.NotNil(t, c.VerifyToken)
}

// A rotation through a client with no VerifyToken mints and publishes nothing
// for any role, including the Poller whose access check is composed in.
func TestRotateGitLabRoleCredentials_NilVerifierMintsAndPublishesNothing(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	for _, role := range []gitlabroles.Role{gitlabroles.RoleAnalyst, gitlabroles.RoleCoder, gitlabroles.RolePoller} {
		for _, backend := range []string{"service-account", "legacy"} {
			t.Run(string(role)+"/"+backend, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				fc := seededRoleClient(t, role)
				require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
					`{"version":2,"roles":{"`+string(role)+`":{"phase":"idle","distributed_at":"2026-09-01T00:00:00Z"}}}`, true))
				legacy := &fakeTokens{}
				sa := newFakeSAAPI()
				tokens := ServiceAccountTokenClient{Legacy: legacy, ManagedAccountIDs: sa.ownedIDs}
				if backend == "service-account" {
					tokens.SA = sa
				}
				before := len(fc.CreatedSecrets)

				result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
					Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
					Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{role}, Force: true, Now: now,
				})
				require.NoError(t, err)
				assert.Empty(t, result.Rotated)
				assert.Len(t, fc.CreatedSecrets, before, "nothing is published")
				assert.Empty(t, sa.tokens, "no service-account token is minted")
				assert.Empty(t, legacy.created, "no legacy token is minted")
			})
		}
	}
}
