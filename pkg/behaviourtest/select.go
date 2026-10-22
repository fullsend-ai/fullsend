package behaviourtest

import (
	"context"
	"fmt"
	"strconv"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	gaci "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/githubactions"
	glci "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/gitlabci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	scmgl "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/gitlab"
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

// poolConfigFor returns the slot pool and runtime dependencies the
// install factory needs: the environment's layout (derived DEV orgs
// halfsend-01 … halfsend-12, or the unsuffixed STAGE org halfsend; see
// install.PoolLayoutForEnvironment), one installation token per org via
// e2etest.TokenForOrg, and live forge clients.
func poolConfigFor(environment string, e2eCfg e2etest.EnvConfig, binary string, logf func(string, ...any)) install.PoolConfig {
	return install.PoolConfig{
		Layout: install.PoolLayoutForEnvironment(environment, logf),
		TokenFor: func(ctx context.Context, org string) (string, error) {
			return e2etest.TokenForOrg(ctx, e2eCfg, org)
		},
		NewClient:    func(token string) forge.Client { return e2etest.NewLiveClient(token) },
		Binary:       binary,
		GCPProjectID: e2eCfg.GCPProjectID,
		Logf:         logf,
	}
}

// orgDriversFor returns the World.OrgDrivers hook: it builds the SCM and
// CI drivers for one leased org from that org's client and token.
func orgDriversFor(scmName, ciName string) func(forge.Client, string) (scm.Driver, ci.Driver, error) {
	return func(client forge.Client, token string) (scm.Driver, ci.Driver, error) {
		scmDriver, err := newSCMDriver(scmName, client)
		if err != nil {
			return nil, nil, err
		}
		ciDriver, err := newCIDriver(ciName, client, token)
		if err != nil {
			return nil, nil, err
		}
		return scmDriver, ciDriver, nil
	}
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
// GODOG_CONCURRENCY env value; empty means defaultN (capped at
// capacity). Aggregate concurrency is controlled independently of the
// pool's slot capacity, which spans every pool org. overCapacity is true
// when the resolved value exceeds capacity (caller warns; excess workers
// block while allocating).
func resolveConcurrency(raw string, defaultN, capacity int) (n int, overCapacity bool, err error) {
	n = min(defaultN, capacity)
	if raw != "" {
		parsed, convErr := strconv.Atoi(raw)
		if convErr != nil || parsed < 1 {
			return 0, false, fmt.Errorf("GODOG_CONCURRENCY must be a positive integer, got %q", raw)
		}
		n = parsed
	}
	return n, n > capacity, nil
}
