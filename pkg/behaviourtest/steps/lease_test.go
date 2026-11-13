package steps

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// fakeLeaseDriver is an install.LeaseDriver returning a fixed lease.
type fakeLeaseDriver struct {
	install.Driver
	lease      install.RepoLease
	allocErr   error
	allocs     int
	deallocs   []string
	deallocErr error
}

func (d *fakeLeaseDriver) AllocateLease(context.Context) (install.RepoLease, error) {
	d.allocs++
	if d.allocErr != nil {
		return install.RepoLease{}, d.allocErr
	}
	return d.lease, nil
}

func (d *fakeLeaseDriver) DeallocateLease(_ context.Context, org, repo string) error {
	d.deallocs = append(d.deallocs, org+"/"+repo)
	return d.deallocErr
}

// recordingOrgDrivers returns an OrgDrivers hook that records the
// client and token it was given and returns fresh fake drivers.
type recordingOrgDrivers struct {
	client forge.Client
	token  string
	scm    scm.Driver
	ci     ci.Driver
	err    error
}

func (r *recordingOrgDrivers) build(client forge.Client, token string) (scm.Driver, ci.Driver, error) {
	r.client, r.token = client, token
	if r.err != nil {
		return nil, nil, r.err
	}
	r.scm = &fakeDispatchSCM{}
	r.ci = &fakeBranchCI{}
	return r.scm, r.ci, nil
}

func TestGivenEnrolledTestRepository_BindsWorldToLeasedOrg(t *testing.T) {
	client := forge.NewFakeClient()
	ld := &fakeLeaseDriver{lease: install.RepoLease{
		Org: "halfsend-07", Repo: "test-repo-03", Token: "tok-07",
		Client: client, MintURL: "https://mint.halfsend-07",
		MintStatusGroup:       "halfsend-11/mint-status",
		MintWorkflowHostRepos: []string{"halfsend-07/test-repo-03", "halfsend-08/test-repo-03"},
	}}
	od := &recordingOrgDrivers{}
	templateSCM := &fakeDispatchSCM{}
	w := &world.World{
		Driver: ld, Org: "halfsend-01", Token: "tok-01", RepoOwner: "halfsend-01",
		SCM: templateSCM, OrgDrivers: od.build,
	}

	require.NoError(t, givenEnrolledTestRepository(context.Background(), w))
	assert.Equal(t, "halfsend-07", w.Org)
	assert.Equal(t, "halfsend-07", w.RepoOwner)
	assert.Equal(t, "test-repo-03", w.RepoName)
	assert.Equal(t, "halfsend-07/test-repo-03", w.RepoFull)
	assert.Equal(t, "tok-07", w.Token)
	assert.Equal(t, "https://mint.halfsend-07", w.MintURL)
	assert.Equal(t, "halfsend-11/mint-status", w.MintStatusGroup, "the lease's status group, not the org's")
	assert.Equal(t, []string{"halfsend-07/test-repo-03", "halfsend-08/test-repo-03"}, w.MintWorkflowHostRepos)
	assert.Equal(t, "test-repo-03", w.LeasedRepoName)
	assert.Equal(t, "halfsend-07", w.LeasedRepoOrg)
	assert.Same(t, client, od.client, "drivers built from the leased org's client")
	assert.Equal(t, "tok-07", od.token)
	assert.Same(t, od.scm, w.SCM)
	assert.Same(t, od.ci, w.CI)

	// A second "Given the enrolled test repository" does not lease again.
	require.NoError(t, givenEnrolledTestRepository(context.Background(), w))
	assert.Equal(t, 1, ld.allocs)
}

func TestGivenEnrolledTestRepository_BindFailureReleasesLease(t *testing.T) {
	ld := &fakeLeaseDriver{lease: install.RepoLease{Org: "halfsend-02", Repo: "test-repo-01", Client: forge.NewFakeClient()}}
	od := &recordingOrgDrivers{err: errors.New("bad scm")}
	w := &world.World{Driver: ld, Org: "halfsend-01", OrgDrivers: od.build}

	err := givenEnrolledTestRepository(context.Background(), w)
	require.ErrorContains(t, err, "bad scm")
	assert.Equal(t, []string{"halfsend-02/test-repo-01"}, ld.deallocs, "an unbound lease is released at once")
	assert.Empty(t, w.LeasedRepoName, "the After hook has nothing to release")
	assert.Equal(t, "halfsend-01", w.Org)
}

func TestGivenEnrolledTestRepository_LeaseWithoutClientFails(t *testing.T) {
	ld := &fakeLeaseDriver{lease: install.RepoLease{Org: "halfsend-02", Repo: "test-repo-01"}, deallocErr: errors.New("gone")}
	w := &world.World{Driver: ld, OrgDrivers: (&recordingOrgDrivers{}).build}

	err := givenEnrolledTestRepository(context.Background(), w)
	require.ErrorContains(t, err, "no forge client")
	assert.ErrorContains(t, err, "releasing the lease: gone")
}

func TestGivenEnrolledTestRepository_AllocateLeaseError(t *testing.T) {
	w := &world.World{Driver: &fakeLeaseDriver{allocErr: errors.New("no capacity")}}
	err := givenEnrolledTestRepository(context.Background(), w)
	require.ErrorContains(t, err, "allocating repo: no capacity")
}

func TestBindLease_NameOnlyLeaseKeepsWorldOrg(t *testing.T) {
	scmDriver := &fakeDispatchSCM{}
	w := &world.World{Org: "org", Token: "tok", SCM: scmDriver, OrgDrivers: (&recordingOrgDrivers{err: errors.New("unused")}).build}
	require.NoError(t, bindLease(w, install.RepoLease{Repo: "test-repo-01"}))
	assert.Equal(t, "org", w.Org)
	assert.Equal(t, "tok", w.Token)
	assert.Same(t, scmDriver, w.SCM)
}

func TestBindLease_WithoutOrgDriversKeepsDrivers(t *testing.T) {
	scmDriver := &fakeDispatchSCM{}
	w := &world.World{Org: "org", Token: "tok", SCM: scmDriver}
	require.NoError(t, bindLease(w, install.RepoLease{Org: "halfsend", Repo: "test-repo-01"}))
	assert.Equal(t, "halfsend", w.Org)
	assert.Equal(t, "tok", w.Token, "an empty lease token keeps the World's")
	assert.Same(t, scmDriver, w.SCM)
}

// stillOwnsLeaseDriver is a fakeLeaseDriver that also verifies lease
// ownership with a fixed answer.
type stillOwnsLeaseDriver struct {
	*fakeLeaseDriver
	owns bool
}

func (d *stillOwnsLeaseDriver) StillOwnsLease(context.Context, string, string) (bool, error) {
	return d.owns, nil
}

func orgLease(client forge.Client) install.RepoLease {
	return install.RepoLease{Org: "halfsend-07", Repo: "test-repo-03", Token: "tok-07", Client: client}
}

// The ownership check is installed when the scenario acquires its
// leased repository, so step-time mutations (mutationCtx) are guarded
// during the scenario, not only for cleanup in the After hook.
func TestGivenEnrolledTestRepository_InstallsLeaseOwnership(t *testing.T) {
	for _, owns := range []bool{true, false} {
		ld := &stillOwnsLeaseDriver{fakeLeaseDriver: &fakeLeaseDriver{lease: orgLease(forge.NewFakeClient())}, owns: owns}
		w := &world.World{Driver: ld, OrgDrivers: (&recordingOrgDrivers{}).build}

		require.NoError(t, givenEnrolledTestRepository(context.Background(), w))
		require.NotNil(t, w.LeaseOwned, "the check is installed at acquisition")
		got, err := w.LeaseOwned(context.Background())
		require.NoError(t, err)
		assert.Equal(t, owns, got, "the check delegates to the driver's verifier")
	}
}

// A driver that leases across orgs but cannot verify fails closed: the
// installed check errors, so a guarded mutation refuses to write.
func TestGivenEnrolledTestRepository_UnverifiableLeaseFailsClosed(t *testing.T) {
	ld := &fakeLeaseDriver{lease: orgLease(forge.NewFakeClient())}
	w := &world.World{Driver: ld, OrgDrivers: (&recordingOrgDrivers{}).build}

	require.NoError(t, givenEnrolledTestRepository(context.Background(), w))
	require.NotNil(t, w.LeaseOwned)
	owns, err := w.LeaseOwned(context.Background())
	assert.False(t, owns)
	assert.ErrorContains(t, err, "cannot verify lease ownership of halfsend-07/test-repo-03")
}

// A name-only lease has no shared slot to lose: no check is installed.
func TestGivenEnrolledTestRepository_NameOnlyLeaseHasNoCheck(t *testing.T) {
	ld := &fakeLeaseDriver{lease: install.RepoLease{Repo: "test-repo-03", Client: forge.NewFakeClient()}}
	w := &world.World{Driver: ld, OrgDrivers: (&recordingOrgDrivers{}).build}

	require.NoError(t, givenEnrolledTestRepository(context.Background(), w))
	assert.Nil(t, w.LeaseOwned)
}

// The normal scenario path guards the gateway api-key write: a lease
// lost after allocation stops the secret write without any manual
// LeaseOwned assignment (the After hook installs nothing during steps).
func TestGivenEnrolledTestRepository_LostLeaseStopsGatewaySecretWrite(t *testing.T) {
	client := &guardedSecretClient{FakeClient: forge.NewFakeClient()}
	ld := &stillOwnsLeaseDriver{fakeLeaseDriver: &fakeLeaseDriver{lease: orgLease(client)}, owns: false}
	w := &world.World{Driver: ld, OrgDrivers: func(c forge.Client, token string) (scm.Driver, ci.Driver, error) {
		return scmgh.New(c), &fakeBranchCI{}, nil
	}}

	require.NoError(t, givenEnrolledTestRepository(context.Background(), w))
	t.Setenv(envInferenceGatewayURL, "https://gw.example")
	t.Setenv(envInferenceGatewayTestKey, "test-gateway-key-value")

	err := givenTestInferenceGatewayAPIKey(w, "")
	require.ErrorIs(t, err, errLeaseLost)
	assert.Zero(t, client.writes, "no secret is written through the normal allocation path on a lost lease")
}
