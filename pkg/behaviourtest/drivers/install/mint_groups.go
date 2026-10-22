package install

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
)

// maxWorkerVarBytes is the ceiling for a mint's serialized
// WORKFLOW_HOST_REPOS value. Cloudflare limits each Worker variable to
// 5 KB (https://developers.cloudflare.com/workers/platform/limits/);
// 5,000 bytes keeps a margin under it.
const maxWorkerVarBytes = 5000

// mintGroup is a set of pool orgs served by one mint. index is 1-based
// and stable for a given layout.
type mintGroup struct {
	index int
	orgs  []string
	// workflowHostRepos is the mint's serialized WORKFLOW_HOST_REPOS
	// (buildWorkflowHostRepoList over orgs), or "" when the driver does
	// not configure the mint's workflow hosts (external mint).
	workflowHostRepos string
}

// String names the group in logs and errors.
func (g mintGroup) String() string {
	if len(g.orgs) == 0 {
		return fmt.Sprintf("mint group %d", g.index)
	}
	if len(g.orgs) == 1 {
		return fmt.Sprintf("mint group %d (%s)", g.index, g.orgs[0])
	}
	return fmt.Sprintf("mint group %d (%s…%s)", g.index, g.orgs[0], g.orgs[len(g.orgs)-1])
}

// mintPlan maps every pool org to the mint group serving it, plus the
// job's status group: the ORG/TEAM every mint of the job is deployed
// with as --status-github-group ("" leaves status auth unconfigured).
type mintPlan struct {
	groups      []mintGroup
	groupOf     map[string]int // org -> index into groups
	statusGroup string
}

func newMintPlan(groups []mintGroup, statusGroup string) *mintPlan {
	p := &mintPlan{groups: groups, groupOf: map[string]int{}, statusGroup: statusGroup}
	for i, g := range groups {
		for _, org := range g.orgs {
			p.groupOf[org] = i
		}
	}
	return p
}

// singleMintPlan serves every org of layout from one mint whose
// workflow hosts and status group the driver does not configure.
func singleMintPlan(layout PoolLayout) *mintPlan {
	return newMintPlan([]mintGroup{{index: 1, orgs: layout.Orgs()}}, "")
}

// planMintGroups packs layout's orgs, in order, into the fewest
// consecutive groups whose actual serialized WORKFLOW_HOST_REPOS stays
// within maxBytes. It is recomputed from the layout on every run, so a
// changed org count or slot count regroups automatically. An org whose
// own list exceeds maxBytes cannot be served by any mint and is an
// error (splitting one org's slots across mints is not supported).
//
// Every org lists the same number of slots, so greedy packing in order
// yields the fewest groups.
func planMintGroups(layout PoolLayout, maxBytes int) ([]mintGroup, error) {
	var groups []mintGroup
	var cur []string
	for _, org := range layout.Orgs() {
		if alone := len(buildWorkflowHostRepoList(org, layout.SlotsPerOrg)); alone > maxBytes {
			return nil, fmt.Errorf("org %s alone needs a %d-byte WORKFLOW_HOST_REPOS for %d slots, over the %d-byte limit of one mint",
				org, alone, layout.SlotsPerOrg, maxBytes)
		}
		candidate := append(append([]string(nil), cur...), org)
		if len(groupWorkflowHostRepos(candidate, layout.SlotsPerOrg)) > maxBytes {
			groups = append(groups, mintGroup{orgs: cur})
			candidate = []string{org}
		}
		cur = candidate
	}
	if len(cur) > 0 {
		groups = append(groups, mintGroup{orgs: cur})
	}
	for i := range groups {
		groups[i].index = i + 1
		groups[i].workflowHostRepos = groupWorkflowHostRepos(groups[i].orgs, layout.SlotsPerOrg)
	}
	return groups, nil
}

// groupWorkflowHostRepos is the WORKFLOW_HOST_REPOS value of a mint
// serving orgs: each org's buildWorkflowHostRepoList, joined.
func groupWorkflowHostRepos(orgs []string, slotsPerOrg int) string {
	lists := make([]string, len(orgs))
	for i, org := range orgs {
		lists[i] = buildWorkflowHostRepoList(org, slotsPerOrg)
	}
	return strings.Join(lists, ",")
}

// pickStatusOrg picks the org whose mint-status team every mint of the
// job authorizes. Overridable in tests.
var pickStatusOrg = func(orgs []string) string {
	return orgs[rand.IntN(len(orgs))]
}

// planCFMints builds the CF mint drivers' plan for layout: packed
// groups, and one mint-status team picked at random from the pool orgs
// once per job. The team need not belong to an org of a given mint.
func planCFMints(layout PoolLayout) (*mintPlan, error) {
	groups, err := planMintGroups(layout, maxWorkerVarBytes)
	if err != nil {
		return nil, err
	}
	// layout is validated (OrgCount > 0), so there is an org to pick.
	return newMintPlan(groups, mintStatusGitHubGroup(pickStatusOrg(layout.Orgs()))), nil
}

// sharedMint is one group's mint, shared by every org of the group.
// The first acquire deploys it and every org that acquires it holds a
// reference; the last release tears it down. Deploy and teardown run
// under mu, so concurrent first leases in different orgs of the group
// trigger a single deployment, and a failed deploy is retried by the
// next acquire.
type sharedMint struct {
	// deploy builds and installs the group's mint with cred, the
	// credential of the org whose lease triggered it. On error it must
	// leave nothing deployed.
	deploy func(ctx context.Context, cred orgCredential) (mintDriver, string, error)

	mu   sync.Mutex
	md   mintDriver
	url  string
	refs int
}

func (s *sharedMint) acquire(ctx context.Context, cred orgCredential) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.md == nil {
		md, url, err := s.deploy(ctx, cred)
		if err != nil {
			return "", err
		}
		s.md, s.url = md, url
	}
	s.refs++
	return s.url, nil
}

func (s *sharedMint) release(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refs == 0 {
		return nil
	}
	s.refs--
	if s.refs > 0 || s.md == nil {
		return nil
	}
	md := s.md
	s.md, s.url = nil, ""
	return md.Teardown(ctx)
}

// sharedMintRef is one org's reference to its group's sharedMint. It is
// the org's mintDriver: Install acquires the shared mint (deploying it
// if needed) and Teardown releases this org's reference, so the shared
// driver's per-org teardown removes each deployed mint once.
type sharedMintRef struct {
	shared *sharedMint
	cred   orgCredential

	mu   sync.Mutex
	held bool
}

var _ mintDriver = (*sharedMintRef)(nil)

func (r *sharedMintRef) Install(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.held {
		r.shared.mu.Lock()
		defer r.shared.mu.Unlock()
		return r.shared.url, nil
	}
	url, err := r.shared.acquire(ctx, r.cred)
	if err != nil {
		return "", err
	}
	r.held = true
	return url, nil
}

func (r *sharedMintRef) Teardown(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.held {
		return nil
	}
	r.held = false
	return r.shared.release(ctx)
}
