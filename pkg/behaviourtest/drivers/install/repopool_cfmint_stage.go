// repopool_cfmint_stage.go implements the RepoPoolCFMintStage driver.
// It deploys a durable Cloudflare Worker mint at stage-mint.fullsend.sh
// and constructs a unified Driver that owns allocation, deallocation,
// ensure, and teardown. Unlike the preview driver, the durable Worker
// persists across runs — teardown is a no-op.
//
// The STAGE driver uses non-vendored per-repo install mode with
// --fullsend-ref=main so that pool repos reference the latest code on
// the default branch instead of a vendored binary.
package install

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
)

const (
	// StageMintCustomDomain is the custom domain for the STAGE mint.
	StageMintCustomDomain = "stage-mint.fullsend.sh"

	// StageMintURL is the HTTPS URL for the STAGE mint. This is the
	// custom domain URL used for github setup --mint-url.
	StageMintURL = "https://" + StageMintCustomDomain

	// StageMintWorkerName is the CF Worker script name for the STAGE mint.
	StageMintWorkerName = "stage-mint"

	// StageOrg is the GitHub org used for STAGE behaviour tests.
	// Unlike the DEV pool orgs (halfsend-01 … halfsend-12), the STAGE
	// driver uses a single org. Within this org, a repo pool is used
	// just like in the DEV drivers.
	StageOrg = "halfsend"

	// stageFullsendRef is the ref passed to --fullsend-ref for
	// non-vendored installs. Using "main" means pool repos reference
	// the latest code on the default branch.
	stageFullsendRef = "main"
)

// stageMintConfig holds parameters for the STAGE durable mint deploy.
type stageMintConfig struct {
	pemDir            string
	perRepoWIFRepos   string
	workflowHostRepos string
	appSet            string
	// statusGitHubGroup enables GitHub user-token status auth for an
	// ORG/TEAM; see cfmintConfig.statusGitHubGroup.
	statusGitHubGroup string
}

// NewRepoPoolCFMintStage is a Factory for the STAGE pool: the single
// unsuffixed halfsend org (StagePoolLayout). It returns the shared
// slot-leasing Driver, which deploys the durable CF Worker mint at
// stage-mint.fullsend.sh when the first slot is leased.
//
// The STAGE driver differs from the preview driver in three ways:
//   - Deploys a durable (non-preview) Worker with a custom domain
//   - Uses non-vendored per-repo install with --fullsend-ref=main
//   - Teardown is a no-op (the durable Worker persists across runs)
//
// The pool layout must contain exactly StageOrg ("halfsend"); the
// factory returns an error for any other layout.
func NewRepoPoolCFMintStage(pool PoolConfig) (Driver, error) {
	if err := pool.validate(); err != nil {
		return nil, fmt.Errorf("stage cfmint factory: %w", err)
	}
	if orgs := pool.Layout.Orgs(); len(orgs) != 1 || orgs[0] != StageOrg {
		return nil, fmt.Errorf("stage cfmint factory: pool must be the single org %q, got %v", StageOrg, orgs)
	}
	if !cfmintPEMsConfigured() {
		return nil, fmt.Errorf("stage cfmint factory: PEMDir is required (no PEMs materialized: set the TEST_*_PEM env vars)")
	}
	plan, err := planCFMints(pool.Layout)
	if err != nil {
		return nil, fmt.Errorf("stage cfmint factory: %w", err)
	}
	logf := poolLogf(pool)
	if err := prepareWranglerForMints(context.Background(), logf); err != nil {
		return nil, fmt.Errorf("stage cfmint factory: %w", err)
	}

	newMint := func(group mintGroup, cred orgCredential) (mintDriver, func(), error) {
		pemDir, err := setupCFMintPEMDir()
		if err != nil {
			return nil, nil, fmt.Errorf("materializing PEMs: %w", err)
		}
		// PEMs are only needed for the deploy; the caller runs cleanup as
		// soon as it has finished.
		cleanup := func() {
			if pemDir != "" {
				_ = os.RemoveAll(pemDir)
			}
		}
		cfg := stageMintConfig{
			pemDir:            pemDir,
			perRepoWIFRepos:   buildRepoList(StageOrg, pool.Layout.SlotsPerOrg),
			workflowHostRepos: group.workflowHostRepos,
			appSet:            envAppSet(),
			statusGitHubGroup: plan.statusGroup,
		}
		md, err := newStageMintDriver(cred.client, cred.token, pool.Binary, pool.GCPProjectID, logf, cfg)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("creating mint driver: %w", err)
		}
		return md, cleanup, nil
	}

	return buildPoolDriver(pool, plan, newMint, stageSetupOpts(), startTraceCollector(logf))
}

// Compile-time check: NewRepoPoolCFMintStage satisfies Factory.
var _ Factory = NewRepoPoolCFMintStage

// stageSetupOpts returns the STAGE install options: non-vendored with
// --fullsend-ref=main. Runtime picks up PLAYBACK_RUNTIME when set, so
// this factory can also back a playback suite (see NewPlaybackDriver)
// in the stage environment.
func stageSetupOpts() common.GitHubSetupOpts {
	return common.GitHubSetupOpts{
		AppSet:       envAppSet(),
		Vendor:       false,
		FullsendRef:  stageFullsendRef,
		ConfigPreset: envConfigPreset(),
		Runtime:      os.Getenv("PLAYBACK_RUNTIME"),
	}
}

// stageMintMintDriver deploys a durable CF Worker mint with a custom
// domain. Unlike cfmintMintDriver it does not use preview aliases.
type stageMintMintDriver struct {
	client       forge.Client
	token        string
	binary       string
	gcpProjectID string
	logf         func(string, ...any)
	cfg          stageMintConfig
	cliRunner    CLIRunnerFunc
}

// Compile-time check that stageMintMintDriver implements mintDriver.
var _ mintDriver = (*stageMintMintDriver)(nil)

// newStageMintDriver creates a stage durable-mint driver that deploys
// a CF Worker with a custom domain instead of a preview alias.
func newStageMintDriver(
	client forge.Client,
	token, binary, gcpProjectID string,
	logf func(string, ...any),
	cfg stageMintConfig,
) (mintDriver, error) {
	if cfg.pemDir == "" {
		return nil, fmt.Errorf("stage cfmint: PEMDir is required (no PEMs materialized)")
	}
	entries, err := os.ReadDir(cfg.pemDir)
	if err != nil {
		return nil, fmt.Errorf("stage cfmint: reading PEM dir: %w", err)
	}
	hasPEM := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".pem") {
			hasPEM = true
			break
		}
	}
	if !hasPEM {
		return nil, fmt.Errorf("stage cfmint: PEM dir %s contains no .pem files", cfg.pemDir)
	}

	return &stageMintMintDriver{
		client:       client,
		token:        token,
		binary:       binary,
		gcpProjectID: gcpProjectID,
		logf:         logf,
		cfg:          cfg,
		cliRunner:    e2etest.TryRunCLI,
	}, nil
}

// StageMintDeployArgs builds the CLI arguments for a durable
// `fullsend mint deploy --platform=cloudflare` with a custom domain.
func StageMintDeployArgs(cfg stageMintConfig) []string {
	args := []string{
		"mint", "deploy",
		"--platform", "cloudflare",
		"--worker-name", StageMintWorkerName,
		"--custom-domain", StageMintCustomDomain,
		"--pem-dir", cfg.pemDir,
		"--per-repo-wif-repos", cfg.perRepoWIFRepos,
		"--workflow-host-repos", cfg.workflowHostRepos,
		// Hardcoded: stage BT mint allows all workflow basenames.
		// Unlike the other flags, this is not configurable per-caller —
		// stage always permits any workflow file.
		"--allowed-workflow-files", "*",
	}
	if cfg.appSet != "" {
		args = append(args, "--app-set", cfg.appSet)
	}
	args = appendStatusAuthArgs(args, cfg.statusGitHubGroup)
	return args
}

func (d *stageMintMintDriver) Install(_ context.Context) (string, error) {
	d.logf("[stage-cfmint] deploying durable mint at %s", StageMintURL)
	args := StageMintDeployArgs(d.cfg)

	d.logf("[stage-cfmint] running fullsend %s", strings.Join(args, " "))
	if _, err := d.cliRunner(d.binary, d.token, args...); err != nil {
		return "", fmt.Errorf("stage mint deploy --platform=cloudflare --custom-domain=%s: %w", StageMintCustomDomain, err)
	}

	// The mint URL is the custom domain — deterministic, no parsing needed.
	d.logf("[stage-cfmint] durable mint deployed at %s", StageMintURL)
	return StageMintURL, nil
}

// Teardown is a no-op for the durable stage mint. The Worker persists
// across runs; only preview aliases are torn down.
func (d *stageMintMintDriver) Teardown(_ context.Context) error {
	d.logf("[stage-cfmint] teardown is a no-op for the durable stage mint")
	return nil
}
