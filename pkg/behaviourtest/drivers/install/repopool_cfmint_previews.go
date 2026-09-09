// repopool_cfmint_previews.go implements the RepoPoolCFMintPreviews
// driver. It deploys a temporary Cloudflare Worker preview mint for
// behaviour tests and constructs a unified Driver that owns allocation,
// deallocation, ensure, and teardown.
//
// The preview mint is self-contained: all configuration (PEMs,
// allowlists, provenance) is passed at deploy time. Teardown
// abandons the preview alias.
package install

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/e2etest"
)

// cfmintConfig holds parameters for the CF mint driver. This is an
// internal type — the constructor reads values from env or computes
// them from the org name. Callers do not construct or see this type.
type cfmintConfig struct {
	pemDir            string
	suiteName         string
	allowedOrgs       string
	perRepoWIFRepos   string
	workflowHostRepos string
	appSet            string
}

// NewRepoPoolCFMintPreviews is a Factory that deploys a CF preview
// mint and returns a unified Driver owning allocation, deallocation,
// ensure, and teardown.
//
// Driver-specific config is read from env: PEM dir from TEST_*_PEM
// env vars (materialized internally), suite name from
// BEHAVIOUR_SUITE_NAME (default "bt"), and app set from
// BEHAVIOUR_APP_SET (default "fullsend-test"). Pool size defaults to
// DefaultPoolSize. WIF and workflow-host repo lists are computed from
// the org and pool size.
func NewRepoPoolCFMintPreviews(
	org string,
	client forge.Client,
	token, binary, gcpProjectID string,
	logf func(string, ...any),
) (Driver, error) {
	poolSize := envPoolSize(logf)

	pemDir, err := setupCFMintPEMDir()
	if err != nil {
		return nil, fmt.Errorf("cfmint factory: materializing PEMs: %w", err)
	}
	// PEMs are only needed for mint deploy. Clean up as soon as the
	// factory returns (deploy has completed or failed by that point).
	if pemDir != "" {
		defer os.RemoveAll(pemDir)
	}

	cfg := cfmintConfig{
		pemDir:            pemDir,
		suiteName:         envSuiteName(),
		allowedOrgs:       "", // per-repo mode — no org-level allowlist
		perRepoWIFRepos:   buildRepoList(org, poolSize),
		workflowHostRepos: buildRepoList(org, poolSize),
		appSet:            envAppSet(),
	}

	md, err := newCFMintDriver(client, token, binary, gcpProjectID, logf, cfg)
	if err != nil {
		return nil, fmt.Errorf("cfmint factory: creating mint driver: %w", err)
	}

	return buildCFMintDriver(org, md, client, token, binary, gcpProjectID, poolSize, logf)
}

// Compile-time check: NewRepoPoolCFMintPreviews satisfies Factory.
var _ Factory = NewRepoPoolCFMintPreviews

// buildCFMintDriver deploys the mint and constructs the composed driver.
// Extracted from NewRepoPoolCFMintPreviews so the deploy → compose path
// can be tested with a fake mintDriver.
func buildCFMintDriver(
	org string,
	md mintDriver,
	client forge.Client,
	token, binary, gcpProjectID string,
	poolSize int,
	logf func(string, ...any),
) (Driver, error) {
	ctx := context.Background()
	mintURL, err := md.Install(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("cfmint factory: deploying mint: %w", err)
	}

	// Create the ensurer with the deployed mint URL.
	e2eCfg := e2etest.EnvConfig{
		MintURL:      mintURL,
		GCPProjectID: gcpProjectID,
	}
	ens := newRepoEnsurer(e2eCfg, client, token, binary, logf)

	// Construct and return the composed driver.
	d, err := newComposedDriver(org, md, ens, poolSize, logf)
	if err != nil {
		return nil, err
	}
	return withRateLimitReporter(d, client), nil
}

// cfmintMintDriver deploys a CF Worker preview mint and uses the derived
// preview URL as the mint endpoint for fullsend github setup.
type cfmintMintDriver struct {
	client       forge.Client
	token        string
	binary       string
	gcpProjectID string
	logf         func(string, ...any)
	cfg          cfmintConfig
	workerName   string
	previewAlias string // set during Install
	cliRunner    CLIRunnerFunc
}

// Compile-time check that cfmintMintDriver implements mintDriver.
var _ mintDriver = (*cfmintMintDriver)(nil)

// newCFMintDriver creates a CF mint driver. Returns an error if the
// configuration is invalid (missing PEMs, empty suite name).
func newCFMintDriver(
	client forge.Client,
	token, binary, gcpProjectID string,
	logf func(string, ...any),
	cfg cfmintConfig,
) (mintDriver, error) {
	if cfg.pemDir == "" {
		return nil, fmt.Errorf("cfmint: PEMDir is required (no PEMs materialized)")
	}
	entries, err := os.ReadDir(cfg.pemDir)
	if err != nil {
		return nil, fmt.Errorf("cfmint: reading PEM dir: %w", err)
	}
	hasPEM := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".pem") {
			hasPEM = true
			break
		}
	}
	if !hasPEM {
		return nil, fmt.Errorf("cfmint: PEM dir %s contains no .pem files", cfg.pemDir)
	}
	if cfg.suiteName == "" {
		return nil, fmt.Errorf("cfmint: SuiteName is required")
	}

	return &cfmintMintDriver{
		client:       client,
		token:        token,
		binary:       binary,
		gcpProjectID: gcpProjectID,
		logf:         logf,
		cfg:          cfg,
		workerName:   CFMintWorkerName(cfg.suiteName),
		cliRunner:    e2etest.TryRunCLI,
	}, nil
}

// CFMintWorkerName derives the CF Worker script name from a suite name.
// The name clearly indicates it is the e2e/BT worker for that suite.
func CFMintWorkerName(suiteName string) string {
	return suiteName + "-mint"
}

// ParseCFMintURLFromOutput extracts the mint URL printed by `fullsend
// mint deploy`. The CLI prints a line like:
//
//	Worker deployed at https://<alias>-<worker>.<subdomain>.workers.dev
//
// This is the canonical way to obtain the preview URL from a deploy
// invocation; callers should not re-derive it because the correct
// workers.dev subdomain is only known at deploy time.
func ParseCFMintURLFromOutput(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "Worker deployed at") {
			continue
		}
		idx := strings.Index(line, "https://")
		if idx < 0 {
			continue
		}
		url := strings.TrimRight(line[idx:], " \t\n\r.,;")
		return url
	}
	return ""
}

// GenerateCFMintPreviewAlias creates a unique preview alias for a BT run.
// Format: bt-<8-hex-chars> (e.g., bt-a1b2c3d4). The alias satisfies
// the CF preview alias validation: 2-63 lowercase alphanumeric or hyphens.
func GenerateCFMintPreviewAlias() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating preview alias: %w", err)
	}
	return fmt.Sprintf("bt-%x", b), nil
}

// CFMintDeployArgs builds the CLI arguments for `fullsend mint deploy --platform=cloudflare`.
// Exported so unit tests can verify arg construction without shelling out.
//
// --allowed-orgs and --workflow-host-repos are always passed (even when
// empty) so the CLI sees them as explicitly changed. The CLI uses
// "flag changed" semantics: omitted flags preserve existing Worker
// bindings, while explicitly-empty values clear them. For per-repo
// mode the caller should set AllowedOrgs to "" to avoid dual-enrollment.
func CFMintDeployArgs(alias, workerName string, cfg cfmintConfig) []string {
	args := []string{
		"mint", "deploy",
		"--platform", "cloudflare",
		"--preview", alias,
		"--worker-name", workerName,
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

// CFMintTeardownArgs builds the CLI arguments for `fullsend mint delete --platform=cloudflare`.
// Exported so unit tests can verify arg construction without shelling out.
func CFMintTeardownArgs(previewAlias, workerName string) []string {
	return []string{
		"mint", "delete",
		"--platform", "cloudflare",
		"--preview", previewAlias,
		"--worker-name", workerName,
		"--yolo",
	}
}

func (d *cfmintMintDriver) Install(_ context.Context, org string) (string, error) {
	alias, err := GenerateCFMintPreviewAlias()
	if err != nil {
		return "", err
	}
	d.previewAlias = alias

	mintURL, err := d.deployCFMint(alias, org)
	if err != nil {
		return "", fmt.Errorf("deploying CF preview mint for BT: %w", err)
	}

	return mintURL, nil
}

func (d *cfmintMintDriver) Teardown(_ context.Context) error {
	return d.teardownPreview()
}

// deployCFMint deploys a Cloudflare Worker preview mint and returns the
// deploy-reported preview URL (which includes the account's workers.dev
// subdomain).
func (d *cfmintMintDriver) deployCFMint(alias, org string) (string, error) {
	args := CFMintDeployArgs(alias, d.workerName, d.cfg)

	d.logf("[cfmint] deploying preview mint: fullsend %s", strings.Join(args, " "))
	output, err := d.cliRunner(d.binary, d.token, args...)
	if err != nil {
		return "", fmt.Errorf("mint deploy --platform=cloudflare --preview=%s: %w", alias, err)
	}

	mintURL := ParseCFMintURLFromOutput(output)
	if mintURL == "" {
		return "", fmt.Errorf("mint deploy --platform=cloudflare --preview=%s: could not parse mint URL from deploy output", alias)
	}
	d.logf("[cfmint] preview mint deployed at %s", mintURL)
	return mintURL, nil
}

// teardownPreview tears down the CF preview mint if one was deployed.
// Returns an error on delete failure so Finalize can join it with any
// lease-leak error.
func (d *cfmintMintDriver) teardownPreview() error {
	if d.previewAlias == "" {
		return nil
	}
	args := CFMintTeardownArgs(d.previewAlias, d.workerName)

	d.logf("[cfmint] tearing down preview mint: fullsend %s", strings.Join(args, " "))
	if _, err := d.cliRunner(d.binary, d.token, args...); err != nil {
		return fmt.Errorf("preview mint teardown (alias=%s): %w", d.previewAlias, err)
	}
	d.logf("[cfmint] preview mint torn down (alias=%s)", d.previewAlias)
	return nil
}

// --- Env helpers ---

// envSuiteName returns the BT suite name from env or a default.
func envSuiteName() string {
	if v := os.Getenv("BEHAVIOUR_SUITE_NAME"); v != "" {
		return v
	}
	return "bt"
}

// envAppSet returns the app set for PEM bootstrap from env or a default.
func envAppSet() string {
	if v := os.Getenv("BEHAVIOUR_APP_SET"); v != "" {
		return v
	}
	return "fullsend-test"
}

// envPoolSize returns the pool size from env or DefaultPoolSize.
// When BEHAVIOUR_POOL_SIZE is set but cannot be parsed as a positive
// integer, a warning is logged via logf and the default is used.
func envPoolSize(logf func(string, ...any)) int {
	if v := os.Getenv("BEHAVIOUR_POOL_SIZE"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
		logf("WARNING: BEHAVIOUR_POOL_SIZE=%q is not a valid positive integer, using default %d", v, DefaultPoolSize)
	}
	return DefaultPoolSize
}

// buildRepoList constructs a comma-separated list of org/test-repo-NN.
func buildRepoList(org string, poolSize int) string {
	repos := make([]string, poolSize)
	for i := range poolSize {
		repos[i] = fmt.Sprintf("%s/test-repo-%02d", org, i+1)
	}
	return strings.Join(repos, ",")
}

// --- PEM materialization (cfmint-specific) ---

// cfmintPEMRoleEnvVars maps PEM role names to the environment variables
// the CI workflow provides (TEST_*_PEM secrets wired in e2e.yml).
var cfmintPEMRoleEnvVars = map[string]string{
	"fullsend":   "TEST_FULLSEND_PEM",
	"triage":     "TEST_TRIAGE_PEM",
	"coder":      "TEST_CODER_PEM",
	"review":     "TEST_REVIEW_PEM",
	"retro":      "TEST_RETRO_PEM",
	"prioritize": "TEST_PRIORITIZE_PEM",
}

// setupCFMintPEMDir materializes TEST_*_PEM environment variables into
// {role}.pem files inside a temporary directory. Returns the directory
// path, or "" when no PEM env vars are set (e.g. local dev). The caller
// is responsible for removing the directory after deploy has used it.
//
// On any failure after creating the temp dir the directory is removed
// before returning so PEM material is never leaked on disk.
func setupCFMintPEMDir() (string, error) {
	var found bool
	for _, envVar := range cfmintPEMRoleEnvVars {
		if os.Getenv(envVar) != "" {
			found = true
			break
		}
	}
	if !found {
		return "", nil
	}

	dir, err := os.MkdirTemp("", "cfmint-pems-*")
	if err != nil {
		return "", fmt.Errorf("creating PEM temp dir: %w", err)
	}
	for role, envVar := range cfmintPEMRoleEnvVars {
		pem := os.Getenv(envVar)
		if pem == "" {
			continue
		}
		path := filepath.Join(dir, role+".pem")
		if writeErr := os.WriteFile(path, []byte(pem), 0600); writeErr != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("writing PEM file %s: %w", role+".pem", writeErr)
		}
	}
	return dir, nil
}
