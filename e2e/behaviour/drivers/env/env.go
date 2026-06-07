package env

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

const defaultTestRepo = "test-repo"

// Setup validates that a test org is ready for behaviour tests.
type Setup interface {
	Validate(ctx context.Context, org string) error
	TestRepo() string
}

// PerOrg validates per-org fullsend installation with an enrolled test repo.
type PerOrg struct {
	Client   forge.Client
	RepoName string
}

func NewPerOrg(client forge.Client) *PerOrg {
	return &PerOrg{Client: client, RepoName: defaultTestRepo}
}

func (p *PerOrg) TestRepo() string {
	if p.RepoName == "" {
		return defaultTestRepo
	}
	return p.RepoName
}

func (p *PerOrg) Validate(ctx context.Context, org string) error {
	if _, err := p.Client.GetRepo(ctx, org, forge.ConfigRepoName); err != nil {
		return fmt.Errorf("org %s missing %s repo: %w", org, forge.ConfigRepoName, err)
	}
	cfgData, err := p.Client.GetFileContent(ctx, org, forge.ConfigRepoName, "config.yaml")
	if err != nil {
		return fmt.Errorf("reading config.yaml: %w", err)
	}
	cfg, err := config.ParseOrgConfig(cfgData)
	if err != nil {
		return fmt.Errorf("parsing config.yaml: %w", err)
	}
	repoCfg, ok := cfg.Repos[p.TestRepo()]
	if !ok || !repoCfg.Enabled {
		return fmt.Errorf("org %s does not have enrolled repo %q", org, p.TestRepo())
	}
	if cfg.Defaults.Runtime != "dummy" {
		return fmt.Errorf("org %s config defaults.runtime is %q, want dummy for behaviour tests", org, cfg.Defaults.Runtime)
	}
	if _, err := p.Client.GetRepo(ctx, org, p.TestRepo()); err != nil {
		return fmt.Errorf("test repo %s/%s not found: %w", org, p.TestRepo(), err)
	}
	return nil
}

// RunnerConfig holds behaviour test runner configuration from environment.
type RunnerConfig struct {
	SCM         string
	CI          string
	InstallMode string
}

func LoadRunnerConfig() RunnerConfig {
	return RunnerConfig{
		SCM:         stringsTrimOrDefault(os.Getenv("BEHAVIOUR_SCM"), "github"),
		CI:          stringsTrimOrDefault(os.Getenv("BEHAVIOUR_CI"), "githubactions"),
		InstallMode: stringsTrimOrDefault(os.Getenv("BEHAVIOUR_INSTALL_MODE"), "per-org"),
	}
}

func (c RunnerConfig) Validate() error {
	if c.InstallMode != "per-org" {
		return fmt.Errorf("behaviour tests v1 only support BEHAVIOUR_INSTALL_MODE=per-org, got %q", c.InstallMode)
	}
	if c.SCM != "github" {
		return fmt.Errorf("unsupported BEHAVIOUR_SCM %q", c.SCM)
	}
	if c.CI != "githubactions" {
		return fmt.Errorf("unsupported BEHAVIOUR_CI %q", c.CI)
	}
	return nil
}

func stringsTrimOrDefault(value, fallback string) string {
	if v := strings.TrimSpace(value); v != "" {
		return v
	}
	return fallback
}
