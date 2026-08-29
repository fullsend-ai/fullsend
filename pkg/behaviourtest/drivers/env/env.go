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
	// Environment is the mint/infra target the suite is talking to
	// (`dev` or `stage`), from ENVIRONMENT. Empty defaults to `dev`
	// so local `make behaviour-test` works without setting it. CI
	// sets this to match the GitHub Environment on the behaviour job.
	Environment string

	// Capabilities lists environment capabilities the runner declares,
	// from the comma-separated BEHAVIOUR_CAPABILITIES env var. Scenarios
	// tagged @requires:capability:<name> are skipped unless <name> is
	// declared — the gate for coverage of behavior that only exists past
	// a certain dependency version (e.g. an agents-repo release).
	Capabilities []string
}

func LoadRunnerConfig() RunnerConfig {
	return RunnerConfig{
		SCM:          stringsTrimOrDefault(os.Getenv("BEHAVIOUR_SCM"), "github"),
		CI:           stringsTrimOrDefault(os.Getenv("BEHAVIOUR_CI"), "githubactions"),
		InstallMode:  stringsTrimOrDefault(os.Getenv("BEHAVIOUR_INSTALL_MODE"), "per-repo"),
		Environment:  stringsTrimOrDefault(os.Getenv("ENVIRONMENT"), "dev"),
		Capabilities: splitCapabilities(os.Getenv("BEHAVIOUR_CAPABILITIES")),
	}
}

// HasCapability reports whether the runner declared the named capability.
func (c RunnerConfig) HasCapability(name string) bool {
	for _, declared := range c.Capabilities {
		if declared == name {
			return true
		}
	}
	return false
}

func splitCapabilities(raw string) []string {
	var caps []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			caps = append(caps, p)
		}
	}
	return caps
}

func (c RunnerConfig) Validate() error {
	if c.InstallMode != "per-repo" {
		return fmt.Errorf("behaviour tests v1 only support BEHAVIOUR_INSTALL_MODE=per-repo, got %q", c.InstallMode)
	}
	if c.SCM != "github" && c.SCM != "gitlab" {
		return fmt.Errorf("unsupported BEHAVIOUR_SCM %q", c.SCM)
	}
	if c.CI != "githubactions" {
		return fmt.Errorf("unsupported BEHAVIOUR_CI %q", c.CI)
	}
	if c.Environment != "dev" && c.Environment != "stage" {
		return fmt.Errorf("unsupported ENVIRONMENT %q (want dev or stage)", c.Environment)
	}
	return nil
}

func stringsTrimOrDefault(value, fallback string) string {
	if v := strings.TrimSpace(value); v != "" {
		return v
	}
	return fallback
}
