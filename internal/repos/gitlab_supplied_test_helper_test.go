package repos

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/stretchr/testify/require"
)

// suppliedCoderEnrolledState records an enrolled, idle and distributed
// supplied coder credential whose owner and token are known.
const suppliedCoderEnrolledState = `{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-10-01T00:00:00Z","supplied":true,"supplied_user_id":777,"supplied_token_id":13,"excluded_user_ids":[777]}}}`

// describedCoderIdentity is the identity of a supplied Coder credential that
// satisfies the role's credential policy for tests that run at a fixed clock in
// October 2026. Enrollment rejects identities without token metadata when the
// client can authenticate supplied credentials.
func describedCoderIdentity(userID, tokenID int) SuppliedIdentity {
	return describedIdentity(gitlabroles.CoderTokenName, userID, tokenID)
}

// coderSecretWrites counts the coder token secret writes recorded by fc.
func coderSecretWrites(fc *forge.FakeClient) int {
	n := 0
	for _, s := range fc.CreatedSecrets {
		if s.Name == forge.SecretGitLabCoderToken {
			n++
		}
	}
	return n
}

// readRoleState returns the recorded rotation state of role in fc.
func readRoleState(t *testing.T, fc *forge.FakeClient, role gitlabroles.Role) rotationRoleState {
	t.Helper()
	var state rotationStateFile
	require.NoError(t, json.Unmarshal([]byte(fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]), &state))
	return state.Roles[string(role)]
}

// allowSuppliedAccess is a VerifySuppliedAccess callback that accepts every
// owner, for tests that exercise enrollment rather than the access check.
func allowSuppliedAccess(context.Context, string, string, SuppliedIdentity) error { return nil }

// describedIdentity is a supplied credential identity named tokenName that
// satisfies the role credential policy for tests that run at the fixed clock
// verifyingTokens uses (September 2026).
func describedIdentity(tokenName string, userID, tokenID int) SuppliedIdentity {
	return SuppliedIdentity{
		UserID: userID, TokenID: tokenID, Described: true, Name: tokenName,
		Scopes: []string{"api"}, ExpiresAt: "2027-06-01", Active: true,
	}
}

// healthyInstalledTokens wraps a generic token client so the installed
// supplied credential authenticates as a healthy, unexpired credential owned by
// user 777 whose project access is accepted. Rotation refuses to retain an
// enrolled supplied credential through a client that cannot verify it.
func healthyInstalledTokens(inner ProjectAccessTokenClient) ServiceAccountTokenClient {
	return ServiceAccountTokenClient{
		Legacy: inner,
		InstalledRoleCredentialSelf: func(context.Context, string, string, string) (SuppliedIdentity, bool, error) {
			return SuppliedIdentity{UserID: 777, TokenID: 13, Active: true, ExpiresAt: "2027-06-01"}, true, nil
		},
		VerifySuppliedAccess: allowSuppliedAccess,
		VerifyToken:          acceptReplacementToken,
	}
}

// rejectedInstalledTokens is healthyInstalledTokens for an installed supplied
// credential GitLab no longer accepts (revoked or expired).
func rejectedInstalledTokens(inner ProjectAccessTokenClient) ServiceAccountTokenClient {
	c := healthyInstalledTokens(inner)
	c.InstalledRoleCredentialSelf = func(context.Context, string, string, string) (SuppliedIdentity, bool, error) {
		return SuppliedIdentity{}, true, ErrInstalledCredentialRejected
	}
	return c
}

// verifyingTokens wraps a generic token client so supplied enrollment can
// verify credentials: every supplied value authenticates as identity and its
// owner's project access is accepted. Enrollment refuses a token client that
// lacks either capability.
func verifyingTokens(inner ProjectAccessTokenClient, identity SuppliedIdentity) ServiceAccountTokenClient {
	return ServiceAccountTokenClient{
		Legacy: inner,
		Now:    func() time.Time { return time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC) },
		SuppliedCredentialIdentity: func(context.Context, string) (SuppliedIdentity, error) {
			return identity, nil
		},
		VerifySuppliedAccess: allowSuppliedAccess,
		VerifyToken:          acceptReplacementToken,
	}
}
