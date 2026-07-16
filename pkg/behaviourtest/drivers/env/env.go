package env

import (
	"fmt"
	"os"
	"strings"
)

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
		InstallMode: stringsTrimOrDefault(os.Getenv("BEHAVIOUR_INSTALL_MODE"), "per-repo"),
	}
}

func (c RunnerConfig) Validate() error {
	if c.InstallMode != "per-repo" {
		return fmt.Errorf("behaviour tests v1 only support BEHAVIOUR_INSTALL_MODE=per-repo, got %q", c.InstallMode)
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
