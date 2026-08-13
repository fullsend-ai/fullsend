// Package cfmint implements an install.MintDriver that deploys a temporary
// Cloudflare Worker preview mint for behaviour tests. The preview mint is
// self-contained: all configuration (PEMs, allowlists, provenance) is
// passed at deploy time. Teardown abandons the preview alias.
//
// The driver only manages the mint lifecycle (deploy + teardown). It does
// not run github setup, post-install validation, or per-repo teardown on
// any target repository — that responsibility belongs to the composed
// install.Driver which handles leased pool repos on demand via
// AllocateRepo.
package cfmint

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/e2etest"
)

// Config holds parameters for the CF mint driver. The caller provides
// these from test-infra data; the driver does not hardcode org/repo/pool
// assumptions.
type Config struct {
	// PEMDir is the directory containing {role}.pem files materialized
	// from TEST_*_PEM env vars. Required; the driver fails early if
	// empty or if the directory contains no .pem files.
	PEMDir string

	// SuiteName is used to derive the worker name. For example,
	// suite "bt" produces worker "bt-mint". Different suites get
	// different workers to avoid collisions.
	SuiteName string

	// AllowedOrgs is a comma-separated list of allowed GitHub orgs.
	// Passed to --allowed-orgs on deploy.
	AllowedOrgs string

	// PerRepoWIFRepos is a comma-separated list of repos for per-repo
	// WIF. Passed to --per-repo-wif-repos on deploy.
	PerRepoWIFRepos string

	// WorkflowHostRepos is a comma-separated list of repos whose
	// vendored workflows are allowed to mint tokens. Passed to
	// --workflow-host-repos on deploy. The caller builds the list
	// from pool naming conventions; the driver does not hardcode
	// org/repo assumptions.
	WorkflowHostRepos string

	// AppSet is the app set name for PEM bootstrap. Passed to --app-set
	// on deploy so the CLI verifies PEMs against the correct GitHub Apps.
	// For example, test PEMs use "fullsend-test" while production uses
	// "fullsend-ai". When empty, the CLI uses its own default.
	AppSet string
}

// driver deploys a CF Worker preview mint and uses the derived preview
// URL as the mint endpoint for fullsend github setup.
type driver struct {
	client       forge.Client
	token        string
	binary       string
	gcpProjectID string
	logf         func(string, ...any)
	cfg          Config
	workerName   string
	previewAlias string // set during Install
	cliRunner    install.CLIRunnerFunc
}

// NewDriver creates a CF mint install driver. Returns an error if the
// configuration is invalid (missing PEMs, empty suite name).
func NewDriver(
	client forge.Client,
	token, binary, gcpProjectID string,
	logf func(string, ...any),
	cfg Config,
) (install.MintDriver, error) {
	if cfg.PEMDir == "" {
		return nil, fmt.Errorf("cfmint: PEMDir is required (no PEMs materialized)")
	}
	entries, err := os.ReadDir(cfg.PEMDir)
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
		return nil, fmt.Errorf("cfmint: PEM dir %s contains no .pem files", cfg.PEMDir)
	}
	if cfg.SuiteName == "" {
		return nil, fmt.Errorf("cfmint: SuiteName is required")
	}

	return &driver{
		client:       client,
		token:        token,
		binary:       binary,
		gcpProjectID: gcpProjectID,
		logf:         logf,
		cfg:          cfg,
		workerName:   WorkerName(cfg.SuiteName),
		cliRunner:    e2etest.TryRunCLI,
	}, nil
}

// WorkerName derives the CF Worker script name from a suite name.
// The name clearly indicates it is the e2e/BT worker for that suite.
func WorkerName(suiteName string) string {
	return suiteName + "-mint"
}

// ParseMintURLFromOutput extracts the mint URL printed by `fullsend
// mint deploy`. The CLI prints a line like:
//
//	✓ Worker deployed at https://<alias>-<worker>.<subdomain>.workers.dev
//
// This is the canonical way to obtain the preview URL from a deploy
// invocation; callers should not re-derive it because the correct
// workers.dev subdomain is only known at deploy time.
func ParseMintURLFromOutput(output string) string {
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

// GeneratePreviewAlias creates a unique preview alias for a BT run.
// Format: bt-<8-hex-chars> (e.g., bt-a1b2c3d4). The alias satisfies
// the CF preview alias validation: 2-63 lowercase alphanumeric or hyphens.
func GeneratePreviewAlias() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating preview alias: %w", err)
	}
	return fmt.Sprintf("bt-%x", b), nil
}

func (d *driver) Install(_ context.Context, org string) (install.State, error) {
	alias, err := GeneratePreviewAlias()
	if err != nil {
		return nil, err
	}
	d.previewAlias = alias

	mintURL, err := d.deployCFMint(alias, org)
	if err != nil {
		return nil, fmt.Errorf("deploying CF preview mint for BT: %w", err)
	}

	// The driver only manages the mint lifecycle. Per-repo github setup
	// and post-install validation are handled by the composed
	// install.Driver for each leased pool repo.
	return install.NewPerRepoState(org, "", mintURL), nil
}

func (d *driver) Teardown(_ context.Context, _ string, _ install.State) error {
	d.teardownPreview()
	return nil
}

// DeployArgs builds the CLI arguments for `fullsend mint deploy --platform=cloudflare`.
// Exported so unit tests can verify arg construction without shelling out.
//
// --allowed-orgs and --workflow-host-repos are always passed (even when
// empty) so the CLI sees them as explicitly changed. The CLI uses
// "flag changed" semantics: omitted flags preserve existing Worker
// bindings, while explicitly-empty values clear them. For per-repo
// mode the caller should set AllowedOrgs to "" to avoid dual-enrollment.
func DeployArgs(alias, workerName string, cfg Config) []string {
	args := []string{
		"mint", "deploy",
		"--platform", "cloudflare",
		"--preview", alias,
		"--worker-name", workerName,
		"--pem-dir", cfg.PEMDir,
		"--allowed-orgs", cfg.AllowedOrgs,
		"--per-repo-wif-repos", cfg.PerRepoWIFRepos,
		"--workflow-host-repos", cfg.WorkflowHostRepos,
	}
	if cfg.AppSet != "" {
		args = append(args, "--app-set", cfg.AppSet)
	}
	return args
}

// deployCFMint deploys a Cloudflare Worker preview mint and returns the
// deploy-reported preview URL (which includes the account's workers.dev
// subdomain).
func (d *driver) deployCFMint(alias, org string) (string, error) {
	args := DeployArgs(alias, d.workerName, d.cfg)

	d.logf("[cfmint] deploying preview mint: fullsend %s", strings.Join(args, " "))
	output, err := d.cliRunner(d.binary, d.token, args...)
	if err != nil {
		return "", fmt.Errorf("mint deploy --platform=cloudflare --preview=%s: %w", alias, err)
	}

	mintURL := ParseMintURLFromOutput(output)
	if mintURL == "" {
		return "", fmt.Errorf("mint deploy --platform=cloudflare --preview=%s: could not parse mint URL from deploy output", alias)
	}
	d.logf("[cfmint] preview mint deployed at %s", mintURL)
	return mintURL, nil
}

// TeardownArgs builds the CLI arguments for `fullsend mint delete --platform=cloudflare`.
// Exported so unit tests can verify arg construction without shelling out.
func TeardownArgs(previewAlias, workerName string) []string {
	return []string{
		"mint", "delete",
		"--platform", "cloudflare",
		"--preview", previewAlias,
		"--worker-name", workerName,
		"--yolo",
	}
}

// teardownPreview tears down the CF preview mint if one was deployed.
func (d *driver) teardownPreview() {
	if d.previewAlias == "" {
		return
	}
	args := TeardownArgs(d.previewAlias, d.workerName)

	d.logf("[cfmint] tearing down preview mint: fullsend %s", strings.Join(args, " "))
	if _, err := d.cliRunner(d.binary, d.token, args...); err != nil {
		// Log but don't fail — the preview is ephemeral and will
		// expire. A teardown failure should not mask test results.
		d.logf("[cfmint] preview mint teardown failed: %v", err)
	} else {
		d.logf("[cfmint] preview mint torn down (alias=%s)", d.previewAlias)
	}
}
