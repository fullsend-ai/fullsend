// factory_cfmint.go implements the CFMint factory. It deploys a
// temporary Cloudflare Worker preview mint for behaviour tests and
// constructs a unified Driver that owns repo creation, deletion, and
// teardown.
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
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
	"github.com/fullsend-ai/fullsend/pkg/e2etest"
)

// cfmintConfig holds parameters for the CF mint driver.
type cfmintConfig struct {
	pemDir            string
	suiteName         string
	allowedOrgs       string
	perRepoWIFRepos   string
	workflowHostRepos string
	appSet            string
}

// NewCFMintFactory is a Factory that deploys a CF preview mint and
// returns a unified Driver owning repo creation, deletion, and teardown.
//
// PER_REPO_WIF_REPOS and WORKFLOW_HOST_REPOS are set to "*" so no
// per-repo enrollment is needed. The fullsend ref is resolved via
// EnvFullsendRef().
func NewCFMintFactory(
	org string,
	client forge.Client,
	token, binary, gcpProjectID string,
	logf func(string, ...any),
) (Driver, error) {
	pemDir, err := setupCFMintPEMDir()
	if err != nil {
		return nil, fmt.Errorf("cfmint factory: materializing PEMs: %w", err)
	}
	if pemDir != "" {
		defer os.RemoveAll(pemDir)
	}

	cfg := cfmintConfig{
		pemDir:            pemDir,
		suiteName:         envSuiteName(),
		allowedOrgs:       "",
		perRepoWIFRepos:   "*",
		workflowHostRepos: "*",
		appSet:            envAppSet(),
	}

	md, err := newCFMintDriver(client, token, binary, gcpProjectID, logf, cfg)
	if err != nil {
		return nil, fmt.Errorf("cfmint factory: creating mint driver: %w", err)
	}

	return buildCFMintDriver(org, md, client, token, binary, gcpProjectID, e2etest.TryRunCLI, logf)
}

var _ Factory = NewCFMintFactory

// buildCFMintDriver deploys the mint and constructs the composed driver.
func buildCFMintDriver(
	org string,
	md mintDriver,
	client forge.Client,
	token, binary, gcpProjectID string,
	runCLI CLIRunnerFunc,
	logf func(string, ...any),
) (Driver, error) {
	ctx := context.Background()
	mintURL, err := md.Install(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("cfmint factory: deploying mint: %w", err)
	}

	fullsendRef := common.EnvFullsendRef()

	var wifProvider string
	if gcpProjectID != "" {
		var wifErr error
		wifProvider, wifErr = common.ResolveOrgWIFProvider(binary, token, org, gcpProjectID, runCLI, logf)
		if wifErr != nil {
			return nil, fmt.Errorf("cfmint factory: resolving org WIF provider: %w", wifErr)
		}
	}

	e2eCfg := e2etest.EnvConfig{
		MintURL:      mintURL,
		GCPProjectID: gcpProjectID,
		WIFProvider:  wifProvider,
	}
	ens := newRepoEnsurer(e2eCfg, client, token, binary, fullsendRef, logf)

	return newComposedDriver(org, md, ens, client, logf), nil
}

// cfmintMintDriver deploys a CF Worker preview mint.
type cfmintMintDriver struct {
	client       forge.Client
	token        string
	binary       string
	gcpProjectID string
	logf         func(string, ...any)
	cfg          cfmintConfig
	workerName   string
	previewAlias string
	cliRunner    CLIRunnerFunc
}

var _ mintDriver = (*cfmintMintDriver)(nil)

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

func CFMintWorkerName(suiteName string) string {
	return suiteName + "-mint"
}

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

func GenerateCFMintPreviewAlias() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating preview alias: %w", err)
	}
	return fmt.Sprintf("bt-%x", b), nil
}

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

func envSuiteName() string {
	if v := os.Getenv("BEHAVIOUR_SUITE_NAME"); v != "" {
		return v
	}
	return "bt"
}

func envAppSet() string {
	if v := os.Getenv("BEHAVIOUR_APP_SET"); v != "" {
		return v
	}
	return "fullsend-test"
}

// --- PEM materialization ---

var cfmintPEMRoleEnvVars = map[string]string{
	"fullsend":   "TEST_FULLSEND_PEM",
	"triage":     "TEST_TRIAGE_PEM",
	"coder":      "TEST_CODER_PEM",
	"review":     "TEST_REVIEW_PEM",
	"retro":      "TEST_RETRO_PEM",
	"prioritize": "TEST_PRIORITIZE_PEM",
}

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
