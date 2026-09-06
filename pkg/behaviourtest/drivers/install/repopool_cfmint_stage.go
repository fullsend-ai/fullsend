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

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
	"github.com/fullsend-ai/fullsend/pkg/e2etest"
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
	allowedOrgs       string
	perRepoWIFRepos   string
	workflowHostRepos string
	appSet            string
}

// NewRepoPoolCFMintStage is a Factory that deploys a durable CF Worker
// mint at stage-mint.fullsend.sh and returns a unified Driver owning
// allocation, deallocation, ensure, and teardown for the halfsend org.
//
// The STAGE driver differs from the preview driver in three ways:
//   - Deploys a durable (non-preview) Worker with a custom domain
//   - Uses non-vendored per-repo install with --fullsend-ref=main
//   - Teardown is a no-op (the durable Worker persists across runs)
//
// The org parameter must equal StageOrg ("halfsend"); the driver
// returns an error if a different org is passed.
func NewRepoPoolCFMintStage(
	org string,
	client forge.Client,
	token, binary, gcpProjectID string,
	logf func(string, ...any),
) (Driver, error) {
	if org != StageOrg {
		return nil, fmt.Errorf("stage cfmint factory: org must be %q, got %q", StageOrg, org)
	}
	poolSize := envPoolSize(logf)

	pemDir, err := setupCFMintPEMDir()
	if err != nil {
		return nil, fmt.Errorf("stage cfmint factory: materializing PEMs: %w", err)
	}
	// PEMs are only needed for mint deploy. Clean up as soon as the
	// factory returns (deploy has completed or failed by that point).
	if pemDir != "" {
		defer os.RemoveAll(pemDir)
	}

	cfg := stageMintConfig{
		pemDir:            pemDir,
		allowedOrgs:       "", // per-repo mode — no org-level allowlist
		perRepoWIFRepos:   buildRepoList(StageOrg, poolSize),
		workflowHostRepos: buildRepoList(StageOrg, poolSize),
		appSet:            envAppSet(),
	}

	md, err := newStageMintDriver(client, token, binary, gcpProjectID, logf, cfg)
	if err != nil {
		return nil, fmt.Errorf("stage cfmint factory: creating mint driver: %w", err)
	}

	return buildStageMintDriver(md, client, token, binary, gcpProjectID, poolSize, logf)
}

// Compile-time check: NewRepoPoolCFMintStage satisfies Factory.
var _ Factory = NewRepoPoolCFMintStage

// buildStageMintDriver deploys the durable mint and constructs the
// composed driver. Extracted so the deploy → compose path can be
// tested with a fake mintDriver.
func buildStageMintDriver(
	md mintDriver,
	client forge.Client,
	token, binary, gcpProjectID string,
	poolSize int,
	logf func(string, ...any),
) (Driver, error) {
	ctx := context.Background()
	mintURL, err := md.Install(ctx, StageOrg)
	if err != nil {
		return nil, fmt.Errorf("stage cfmint factory: deploying mint: %w", err)
	}

	// Non-vendored setup opts — use --fullsend-ref=main.
	setupOpts := common.GitHubSetupOpts{
		Vendor:      false,
		FullsendRef: stageFullsendRef,
	}

	e2eCfg := e2etest.EnvConfig{
		MintURL:      mintURL,
		GCPProjectID: gcpProjectID,
	}
	ens := newRepoEnsurerWithOpts(e2eCfg, client, token, binary, setupOpts, logf)

	d, err := newComposedDriver(StageOrg, md, ens, poolSize, logf)
	if err != nil {
		return nil, err
	}
	return withRateLimitReporter(d, client), nil
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
		"--allowed-orgs", cfg.allowedOrgs,
		"--per-repo-wif-repos", cfg.perRepoWIFRepos,
		"--workflow-host-repos", cfg.workflowHostRepos,
	}
	if cfg.appSet != "" {
		args = append(args, "--app-set", cfg.appSet)
	}
	return args
}

func (d *stageMintMintDriver) Install(_ context.Context, _ string) (string, error) {
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
