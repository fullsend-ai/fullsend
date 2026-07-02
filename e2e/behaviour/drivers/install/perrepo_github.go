//go:build behaviour

package install

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fullsend-ai/fullsend/e2e/admin"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

const (
	perRepoTestRepo   = "test-repo"
	defaultMintRegion = "us-central1"
	// Vendored per-repo: issue_comment triggers the fullsend.yaml shim, which
	// workflow_calls reusable-dispatch → reusable-triage synchronously.
	perRepoTriageWorkflow = "fullsend.yaml"
	perRepoAgentWorkflow  = "reusable-triage.yml"
	perRepoAgentArtifact  = "fullsend-triage"
)

// perRepoDriver installs fullsend in per-repo mode via fullsend github setup.
type perRepoDriver struct {
	e2eCfg admin.EnvConfig
	client forge.Client
	token  string
	binary string
	logf   func(string, ...any)
}

type perRepoState struct {
	org  string
	repo string
}

func (s *perRepoState) Mode() string               { return "per-repo" }
func (s *perRepoState) TestRepo() string           { return s.repo }
func (s *perRepoState) ConfigOwner() string        { return s.org }
func (s *perRepoState) ConfigRepo() string         { return s.repo }
func (s *perRepoState) ConfigPathPrefix() string   { return ".fullsend" }
func (s *perRepoState) TriageWorkflowRepo() string { return s.repo }
func (s *perRepoState) TriageWorkflowFile() string { return perRepoTriageWorkflow }
func (s *perRepoState) AgentWorkflowFile() string  { return perRepoAgentWorkflow }
func (s *perRepoState) AgentArtifactName() string  { return perRepoAgentArtifact }

func newPerRepoDriver(e2eCfg admin.EnvConfig, client forge.Client, token, binary string, logf func(string, ...any)) Driver {
	return &perRepoDriver{
		e2eCfg: e2eCfg,
		client: client,
		token:  token,
		binary: binary,
		logf:   logf,
	}
}

func (d *perRepoDriver) Install(ctx context.Context, org string) (State, error) {
	repo := perRepoTestRepo
	target := org + "/" + repo

	args := []string{
		"github", "setup", target,
		"--vendor", "--direct",
		"--skip-app-setup",
		"--mint-url", d.e2eCfg.MintURL,
		"--runtime", "dummy",
	}
	if project := strings.TrimSpace(d.e2eCfg.GCPProjectID); project != "" {
		args = append(args, "--inference-project", project)
	}
	if wif := strings.TrimSpace(d.e2eCfg.WIFProvider); wif != "" {
		args = append(args, "--inference-wif-provider", wif)
	}

	d.logf("[install] running fullsend %s", strings.Join(args, " "))
	if _, err := admin.TryRunCLI(d.binary, d.token, args...); err != nil {
		return nil, fmt.Errorf("github setup %s: %w", target, err)
	}

	if err := d.enrollRepoInMint(target); err != nil {
		return nil, err
	}

	st := &perRepoState{org: org, repo: repo}
	if err := validatePerRepoPostInstall(ctx, d.client, org, repo); err != nil {
		return nil, err
	}
	return st, nil
}

// perRepoMintEnrollArgs returns CLI args for enrolling a vendored per-repo install
// in mint (PER_REPO_WIF_REPOS + repo-scoped WIF provider).
func perRepoMintEnrollArgs(repoFullName, gcpProject string) []string {
	return []string{
		"mint", "enroll", repoFullName,
		"--project", gcpProject,
		"--region", defaultMintRegion,
	}
}

func (d *perRepoDriver) enrollRepoInMint(repoFullName string) error {
	project := admin.MintEnrollProjectID(d.e2eCfg)
	if project == "" {
		d.logf("[install] skipping mint enroll for %s (no mint GCP project; set E2E_GCP_MINT_PROJECT_ID or E2E_GCP_PROJECT_ID, or use hosted mint URL)", repoFullName)
		return nil
	}

	args := perRepoMintEnrollArgs(repoFullName, project)
	d.logf("[install] mint enroll project=%s wif_provider=%s gcp_principal=%s", project, d.e2eCfg.WIFProvider, gcpCredentialPrincipal())
	d.logf("[install] running fullsend %s", strings.Join(args, " "))
	if _, err := admin.TryRunCLI(d.binary, d.token, args...); err != nil {
		return fmt.Errorf("mint enroll %s: %w", repoFullName, err)
	}
	return nil
}

// gcpCredentialPrincipal returns a short label for the active GCP credential
// (service account email or external account) to aid CI debugging.
func gcpCredentialPrincipal() string {
	path := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"))
	if path == "" {
		return "unknown"
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "unreadable"
	}
	var creds struct {
		ClientEmail string `json:"client_email"`
		Type        string `json:"type"`
		ServiceURL  string `json:"service_account_impersonation_url"`
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return "unparsed"
	}
	if creds.ClientEmail != "" {
		return creds.ClientEmail
	}
	if creds.Type != "" {
		return creds.Type
	}
	if creds.ServiceURL != "" {
		return creds.ServiceURL
	}
	return "unknown"
}

func (d *perRepoDriver) Teardown(ctx context.Context, org string, state State) error {
	repo := state.TestRepo()
	d.logf("[install] tearing down per-repo install on %s/%s", org, repo)
	admin.TeardownPerRepoInstall(ctx, d.client, d.token, org, repo, d.logf)
	return nil
}

func validatePerRepoPostInstall(ctx context.Context, client forge.Client, org, repo string) error {
	shimPath := ".github/workflows/fullsend.yaml"
	if _, err := client.GetFileContent(ctx, org, repo, shimPath); err != nil {
		return fmt.Errorf("post-install: missing %s on %s/%s: %w", shimPath, org, repo, err)
	}

	cfgPath := filepath.Join(".fullsend", "config.yaml")
	cfgData, err := client.GetFileContent(ctx, org, repo, cfgPath)
	if err != nil {
		return fmt.Errorf("post-install: reading %s: %w", cfgPath, err)
	}
	cfg, err := config.ParsePerRepoConfig(cfgData)
	if err != nil {
		return fmt.Errorf("post-install: parsing %s: %w", cfgPath, err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("post-install: invalid %s: %w", cfgPath, err)
	}
	if cfg.Runtime != "dummy" {
		return fmt.Errorf("post-install: %s runtime is %q, want dummy", cfgPath, cfg.Runtime)
	}

	markerPath := scaffold.VendoredMarkerPath()
	if _, err := client.GetFileContent(ctx, org, repo, markerPath); err != nil {
		return fmt.Errorf("post-install: missing vendored marker %s: %w", markerPath, err)
	}
	if _, err := client.GetFileContent(ctx, org, repo, layers.VendoredBinaryPathPerRepo); err != nil {
		return fmt.Errorf("post-install: missing vendored binary at %s: %w", layers.VendoredBinaryPathPerRepo, err)
	}
	return nil
}
