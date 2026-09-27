package install

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
)

func TestVerifyActors_AcceptsActiveMembers(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.OrgMemberships = map[string]forge.OrgMembership{
		"org/fstest-write":  {State: "active", Role: "member"},
		"org/fstest-triage": {State: "active", Role: "member"},
	}
	e := &repoEnsurer{
		client: fc,
		logf:   t.Logf,
		actorGrants: []actorGrant{
			{login: "fstest-write", permission: "write"},
			{login: "fstest-triage", permission: "triage"},
		},
	}

	require.NoError(t, e.verifyActors(context.Background(), "org"))
	assert.Empty(t, fc.AddedCollaborators)
}

func TestVerifyActors_NoGrantsSkipsClient(t *testing.T) {
	// stubClient has no organization membership API; with no grants it is never asked.
	e := &repoEnsurer{client: &stubClient{}, logf: t.Logf}
	require.NoError(t, e.verifyActors(context.Background(), "org"))
}

func TestVerifyActors_ClientWithoutOrgAPI(t *testing.T) {
	e := &repoEnsurer{
		client:      &stubClient{},
		logf:        t.Logf,
		actorGrants: []actorGrant{{login: "fstest-write", permission: "write"}},
	}
	err := e.verifyActors(context.Background(), "org")
	require.ErrorContains(t, err, "no organization membership API")
}

func TestVerifyActors_MissingMembership(t *testing.T) {
	fc := forge.NewFakeClient()
	e := &repoEnsurer{
		client:      fc,
		logf:        t.Logf,
		actorGrants: []actorGrant{{login: "fstest-write", permission: "write"}},
	}
	err := e.verifyActors(context.Background(), "org")
	require.ErrorContains(t, err, "fstest-write is not a member of org")
	require.ErrorContains(t, err, "hack/setup-new-e2e-org.sh")
	assert.Empty(t, fc.AddedCollaborators)
}

func TestVerifyActors_PendingMembership(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.OrgMemberships = map[string]forge.OrgMembership{
		"org/fstest-write": {State: "pending", Role: "member"},
	}
	e := &repoEnsurer{
		client:      fc,
		logf:        t.Logf,
		actorGrants: []actorGrant{{login: "fstest-write", permission: "write"}},
	}
	err := e.verifyActors(context.Background(), "org")
	require.ErrorContains(t, err, "not an active member")
	require.ErrorContains(t, err, "state=pending")
}

func TestVerifyActors_MembershipLookupError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["GetOrgMembership"] = errors.New("forbidden")
	e := &repoEnsurer{
		client:      fc,
		logf:        t.Logf,
		actorGrants: []actorGrant{{login: "fstest-write", permission: "write"}},
	}
	err := e.verifyActors(context.Background(), "org")
	require.ErrorContains(t, err, "checking membership of fstest-write in org")
}

func TestVerifyActors_OutsiderMustNotBeMember(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.OrgMemberships = map[string]forge.OrgMembership{
		"org/fstest-outsider": {State: "active", Role: "member"},
	}
	e := &repoEnsurer{client: fc, logf: t.Logf, outsiderLogin: "fstest-outsider"}
	err := e.verifyActors(context.Background(), "org")
	require.ErrorContains(t, err, "outsider must remain outside the organization")
}

func TestVerifyActors_OutsiderAbsentIsOK(t *testing.T) {
	fc := forge.NewFakeClient()
	e := &repoEnsurer{client: fc, logf: t.Logf, outsiderLogin: "fstest-outsider"}
	require.NoError(t, e.verifyActors(context.Background(), "org"))
}

func TestVerifyActors_OutsiderLookupError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["GetOrgMembership"] = errors.New("timeout")
	e := &repoEnsurer{client: fc, logf: t.Logf, outsiderLogin: "fstest-outsider"}
	err := e.verifyActors(context.Background(), "org")
	require.ErrorContains(t, err, "checking outsider membership of fstest-outsider in org")
}

func TestVerifyActors_CachesPerOrg(t *testing.T) {
	sc := &stubClientWithOrgAccess{
		memberships: map[string]forge.OrgMembership{
			"fstest-write": {State: "active", Role: "member"},
		},
	}
	e := &repoEnsurer{
		client:      sc,
		logf:        t.Logf,
		actorGrants: []actorGrant{{login: "fstest-write", permission: "write"}},
	}

	require.NoError(t, e.verifyActors(context.Background(), "org"))
	require.NoError(t, e.verifyActors(context.Background(), "org"))
	assert.Equal(t, 1, sc.memberCalls)
	assert.Equal(t, 0, sc.addCalls)

	require.NoError(t, e.verifyActors(context.Background(), "other-org"))
	assert.Equal(t, 2, sc.memberCalls)
}

func TestActorGrantsFromEnv_UnsetPATsYieldNoGrants(t *testing.T) {
	for _, a := range actorGrantEnv {
		t.Setenv(a.patEnv, "")
	}
	assert.Empty(t, actorGrantsFromEnv(context.Background(), t.Logf))
}

func TestOutsiderLoginFromEnv_UnsetPATYieldsEmpty(t *testing.T) {
	t.Setenv(outsiderPATEnv, "")
	login, err := outsiderLoginFromEnv(context.Background())
	require.NoError(t, err)
	assert.Empty(t, login)
}

// stubClientWithOrgAccess adds the organization membership API to stubClient.
type stubClientWithOrgAccess struct {
	stubClient
	forge.GitHubExtensions
	memberships map[string]forge.OrgMembership
	memberCalls int
	addCalls    int
}

func (s *stubClientWithOrgAccess) GetOrgMembership(_ context.Context, _, username string) (forge.OrgMembership, error) {
	s.memberCalls++
	if s.memberships != nil {
		if m, ok := s.memberships[username]; ok {
			return m, nil
		}
	}
	return forge.OrgMembership{}, forge.ErrNotFound
}

func (s *stubClientWithOrgAccess) AddCollaborator(_ context.Context, _, _, _, _ string) error {
	s.addCalls++
	return errors.New("AddCollaborator must not be called")
}

func TestEnsurer_VerifiesActorsOnRecreatedRepo(t *testing.T) {
	speedUpValidateRetries(t)
	sc := &stubClientWithOrgAccess{
		stubClient: stubClient{installed: true},
		memberships: map[string]forge.OrgMembership{
			"fstest-write": {State: "active", Role: "member"},
		},
	}
	e := &repoEnsurer{
		e2eCfg:      e2etest.EnvConfig{MintURL: "https://mint.test"},
		client:      sc,
		binary:      "/usr/bin/fullsend",
		token:       "tok",
		runCLI:      noopCLI,
		setupOpts:   common.DefaultGitHubSetupOpts(),
		settle:      noopSettle,
		logf:        t.Logf,
		ensured:     make(map[string]struct{}),
		actorGrants: []actorGrant{{login: "fstest-write", permission: "write"}},
	}

	require.NoError(t, e.EnsureRepo(context.Background(), "org", "test-repo-03"))
	assert.Equal(t, int32(1), sc.deleteRepoCalled.Load())
	assert.Equal(t, 1, sc.memberCalls)
	assert.Equal(t, 0, sc.addCalls)
}
