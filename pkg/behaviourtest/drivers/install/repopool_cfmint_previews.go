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

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

// cfmintConfig holds parameters for the CF mint driver. This is an
// internal type — the constructor reads values from env or computes
// them from the mint group. Callers do not construct or see this type.
//
// The preview mint is deployed with --public (PER_REPO_WIF_REPOS=*):
// any repository may be a caller, so no caller list has to fit in a
// Worker variable or be kept enrolled. Workflow-host checks, OIDC
// verification, role permissions and repo-scope checks still apply.
type cfmintConfig struct {
	pemDir            string
	suiteName         string
	workflowHostRepos string
	appSet            string
	// statusGitHubGroup is the ORG/TEAM passed as --status-github-group
	// (with --status-auth=oidc,github), so members of that pool org's
	// mint-status team can query /v1/status with a GitHub user token.
	// Empty leaves status auth at the CLI default (OIDC only).
	statusGitHubGroup string
	// traceEndpoint and traceHeadersFile point the preview mint at the
	// job-scoped trace collector (--otlp-traces-endpoint,
	// --otlp-headers-file). Empty when the collector is not running.
	traceEndpoint    string
	traceHeadersFile string
}

// NewRepoPoolCFMintPreviews is a Factory for the DEV pool. It returns
// the shared slot-leasing Driver, which packs the pool orgs into the
// fewest mint groups whose WORKFLOW_HOST_REPOS fits a Worker variable
// (planMintGroups), deploys one public CF preview mint per group the
// first time a slot in any of its orgs is leased, and tears each down
// once at Finalize.
//
// Driver-specific config is read from env: PEMs from TEST_*_PEM env
// vars (materialized for each deploy and removed after it), suite name
// from BEHAVIOUR_SUITE_NAME (default "bt"), and app set from
// BEHAVIOUR_APP_SET (default "fullsend-test"). A group's workflow-host
// list is computed from its orgs and the layout's slots per org, and
// carries each slot's mint-probe host (see buildWorkflowHostRepoList).
// Every mint's status group is the mint-status team of one pool org,
// picked at random once per job.
func NewRepoPoolCFMintPreviews(pool PoolConfig) (Driver, error) {
	if err := pool.validate(); err != nil {
		return nil, fmt.Errorf("cfmint factory: %w", err)
	}
	plan, err := planCFMints(pool.Layout)
	if err != nil {
		return nil, fmt.Errorf("cfmint factory: %w", err)
	}
	if !cfmintPEMsConfigured() {
		return nil, fmt.Errorf("cfmint factory: PEMDir is required (no PEMs materialized: set the TEST_*_PEM env vars)")
	}
	logf := poolLogf(pool)
	if err := prepareWranglerForMints(context.Background(), logf); err != nil {
		return nil, fmt.Errorf("cfmint factory: %w", err)
	}

	// Start the trace collector before any mint so each preview mint can
	// be deployed with it as its trace destination. Collection is
	// diagnostic: if the headers file cannot be written, a mint is
	// deployed without tracing and the collector still serves workflows.
	traces := startTraceCollector(logf)

	logf("[cfmint] %d preview mint group(s); status group %s", len(plan.groups), plan.statusGroup)
	newMint := func(group mintGroup, cred orgCredential) (mintDriver, func(), error) {
		pemDir, err := setupCFMintPEMDir()
		if err != nil {
			return nil, nil, fmt.Errorf("materializing PEMs: %w", err)
		}
		// PEMs (and the trace headers file) are only needed for the
		// deploy; the caller runs cleanup as soon as it has finished.
		removePEMs := func() {
			if pemDir != "" {
				_ = os.RemoveAll(pemDir)
			}
		}
		traceEndpoint, traceHeadersFile, cleanupHeaders, err := traces.mintTracing()
		if err != nil {
			logf("[trace-collector] WARNING: preview mint of %s will not export traces: %v", group, err)
		}
		cleanup := func() {
			cleanupHeaders()
			removePEMs()
		}
		cfg := cfmintConfig{
			pemDir:            pemDir,
			suiteName:         envSuiteName(),
			workflowHostRepos: group.workflowHostRepos,
			appSet:            envAppSet(),
			statusGitHubGroup: plan.statusGroup,
			traceEndpoint:     traceEndpoint,
			traceHeadersFile:  traceHeadersFile,
		}
		md, err := newCFMintDriver(cred.client, cred.token, pool.Binary, pool.GCPProjectID, logf, cfg)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("creating mint driver: %w", err)
		}
		return md, cleanup, nil
	}

	// playbackSetupOpts returns normal "dummy"-runtime opts unless
	// PLAYBACK_RUNTIME is set in the environment, in which case this same
	// factory backs a playback suite (see NewPlaybackDriver) instead of
	// the standard behaviour suite.
	setupOpts := playbackSetupOpts()
	setupOpts.AppSet = envAppSet()
	return buildPoolDriver(pool, plan, newMint, setupOpts, traces)
}

// Compile-time check: NewRepoPoolCFMintPreviews satisfies Factory.
var _ Factory = NewRepoPoolCFMintPreviews

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
// The preview is public (--public, PER_REPO_WIF_REPOS=*), so
// --per-repo-wif-repos is never passed; the CLI rejects it next to
// --public. --workflow-host-repos is always passed (even when empty) so
// the CLI sees it as explicitly changed: omitted flags preserve existing
// Worker bindings, while explicitly-empty values clear them.
func CFMintDeployArgs(alias, workerName string, cfg cfmintConfig) []string {
	args := []string{
		"mint", "deploy",
		"--platform", "cloudflare",
		"--preview", alias,
		"--worker-name", workerName,
		"--pem-dir", cfg.pemDir,
		"--public",
		"--workflow-host-repos", cfg.workflowHostRepos,
	}
	if cfg.appSet != "" {
		args = append(args, "--app-set", cfg.appSet)
	}
	args = appendStatusAuthArgs(args, cfg.statusGitHubGroup)
	if cfg.traceEndpoint != "" {
		args = append(args, "--otlp-traces-endpoint", cfg.traceEndpoint)
		if cfg.traceHeadersFile != "" {
			args = append(args, "--otlp-headers-file", cfg.traceHeadersFile)
		}
	}
	return args
}

// appendStatusAuthArgs appends the flags that enable GitHub user-token
// auth on the mint's /v1/status for members of group (ORG/TEAM). OIDC
// status auth stays enabled. An empty group appends nothing.
func appendStatusAuthArgs(args []string, group string) []string {
	if group == "" {
		return args
	}
	return append(args, "--status-auth", "oidc,github", "--status-github-group", group)
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

func (d *cfmintMintDriver) Install(_ context.Context) (string, error) {
	alias, err := GenerateCFMintPreviewAlias()
	if err != nil {
		return "", err
	}
	d.previewAlias = alias

	mintURL, err := d.deployCFMint(alias)
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
func (d *cfmintMintDriver) deployCFMint(alias string) (string, error) {
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

// envConfigPreset returns the optional config base layer preset path or
// URL from BEHAVIOUR_CONFIG_PRESET. Empty when unset, so github setup
// is invoked without --config.
func envConfigPreset() string {
	return os.Getenv("BEHAVIOUR_CONFIG_PRESET")
}

// AppSet returns the GitHub App set the behaviour mints are deployed
// with: BEHAVIOUR_APP_SET, or "fullsend-test" when unset. Its role Apps
// have the slugs "<app set>-<role>", so a registered bot's login is
// "<app set>-<role>[bot]".
func AppSet() string {
	return envAppSet()
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

// buildWorkflowHostRepoList constructs one org's part of a mint's
// WORKFLOW_HOST_REPOS: every org/test-repo-NN plus its per-slot
// mint-probe host org/test-repo-NN-mint-host (MintProbeHostRepoSuffix).
// A reusable workflow in a probe host can mint for the leased repo that
// calls it; repo-scope checks keep it from minting for any other
// repository without the required grant.
func buildWorkflowHostRepoList(org string, poolSize int) string {
	repos := make([]string, 0, 2*poolSize)
	for i := range poolSize {
		name := fmt.Sprintf("%s/test-repo-%02d", org, i+1)
		repos = append(repos, name, name+MintProbeHostRepoSuffix)
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

// cfmintPEMsConfigured reports whether any TEST_*_PEM env var is set, so
// the factories can fail before scenarios run instead of at an org's
// first mint deploy.
func cfmintPEMsConfigured() bool {
	for _, envVar := range cfmintPEMRoleEnvVars {
		if os.Getenv(envVar) != "" {
			return true
		}
	}
	return false
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
