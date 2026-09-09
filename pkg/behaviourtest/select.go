package behaviourtest

import (
	"fmt"
	"strconv"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	gaci "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/githubactions"
	glci "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/gitlabci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	scmgl "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/gitlab"
	"github.com/fullsend-ai/fullsend/pkg/e2etest"
)

// installFactoryFor selects the install driver based on ENVIRONMENT:
//
//	dev   → CF preview mint (ephemeral per run)
//	stage → durable CF mint at stage-mint.fullsend.sh
func installFactoryFor(environment string) install.Factory {
	if environment == "stage" {
		return install.NewRepoPoolCFMintStage
	}
	return install.NewRepoPoolCFMintPreviews
}

// orgPoolForEnvironment returns the STAGE org (halfsend) directly, or the
// numbered DEV pool. STAGE does not use the numbered pool orgs.
func orgPoolForEnvironment(environment string) []string {
	if environment == "stage" {
		return []string{install.StageOrg}
	}
	return e2etest.OrgPool()
}

func newSCMDriver(name string, client forge.Client) (scm.Driver, error) {
	switch name {
	case "github":
		return scmgh.New(client), nil
	case "gitlab":
		// TODO: client is a GitHub forge.Client (from e2etest.NewLiveClient).
		// When BEHAVIOUR_SCM=gitlab is used in CI, this must be replaced with
		// a GitLab-backed forge.Client. Currently latent: no CI job sets
		// BEHAVIOUR_SCM=gitlab and @skip:gitlab tag removal is still pending.
		return scmgl.New(client), nil
	default:
		return nil, fmt.Errorf("unsupported BEHAVIOUR_SCM %q", name)
	}
}

func newCIDriver(name string, client forge.Client, token string) (ci.Driver, error) {
	switch name {
	case "githubactions":
		return gaci.New(client, token), nil
	case "gitlabci":
		// TODO: client is a GitHub forge.Client (from e2etest.NewLiveClient).
		// When BEHAVIOUR_CI=gitlabci is used in CI, this must be replaced with
		// a GitLab-backed forge.Client. Currently latent: no CI job sets
		// BEHAVIOUR_CI=gitlabci.
		return glci.New(client, token), nil
	default:
		return nil, fmt.Errorf("unsupported BEHAVIOUR_CI %q", name)
	}
}

// resolveConcurrency returns godog worker count. raw is the
// GODOG_CONCURRENCY env value; empty means default to capacity.
// overCapacity is true when the resolved value exceeds capacity
// (caller warns; excess workers block in AllocateRepo).
func resolveConcurrency(raw string, capacity int) (n int, overCapacity bool, err error) {
	n = capacity
	if raw != "" {
		parsed, convErr := strconv.Atoi(raw)
		if convErr != nil || parsed < 1 {
			return 0, false, fmt.Errorf("GODOG_CONCURRENCY must be a positive integer, got %q", raw)
		}
		n = parsed
	}
	return n, n > capacity, nil
}
