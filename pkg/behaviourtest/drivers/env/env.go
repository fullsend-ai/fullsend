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
	// (`dev`, `stage`, `github-emulator` for a GitHub emulator at
	// BEHAVIOUR_GITHUB_URL, or `forgejo-dev`), from ENVIRONMENT. Empty
	// defaults to `dev` so local `make behaviour-test` works without
	// setting it. CI sets this to match the GitHub Environment on the
	// behaviour job. `forgejo-dev` selects the Forgejo SCM, CI, and
	// install drivers.
	Environment string

	// Capabilities lists environment capabilities the runner declares,
	// from the comma-separated BEHAVIOUR_CAPABILITIES env var. Scenarios
	// tagged @requires:capability:<name> are skipped unless <name> is
	// declared — the gate for coverage of behavior that only exists past
	// a certain dependency version (e.g. an agents-repo release).
	Capabilities []string
}

// Driver selections and the environment that requires them.
const (
	// EnvironmentForgejoDev is the ENVIRONMENT value that selects the
	// Forgejo drivers.
	EnvironmentForgejoDev = "forgejo-dev"
	// SCMForgejo is the BEHAVIOUR_SCM value of the Forgejo SCM driver.
	SCMForgejo = "forgejo"
	// CIForgejoActions is the BEHAVIOUR_CI value of the Forgejo Actions
	// CI driver.
	CIForgejoActions = "forgejoactions"
)

// LoadRunnerConfig reads the runner configuration from the environment.
// BEHAVIOUR_SCM and BEHAVIOUR_CI default to the Forgejo drivers when
// ENVIRONMENT=forgejo-dev and to the GitHub drivers otherwise; explicit
// values are kept as given, and Validate rejects a mix.
func LoadRunnerConfig() RunnerConfig {
	environment := stringsTrimOrDefault(os.Getenv("ENVIRONMENT"), "dev")
	defaultSCM, defaultCI := "github", "githubactions"
	if environment == EnvironmentForgejoDev {
		defaultSCM, defaultCI = SCMForgejo, CIForgejoActions
	}
	return RunnerConfig{
		SCM:          stringsTrimOrDefault(os.Getenv("BEHAVIOUR_SCM"), defaultSCM),
		CI:           stringsTrimOrDefault(os.Getenv("BEHAVIOUR_CI"), defaultCI),
		InstallMode:  stringsTrimOrDefault(os.Getenv("BEHAVIOUR_INSTALL_MODE"), "per-repo"),
		Environment:  environment,
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
	if c.SCM != "github" && c.SCM != "gitlab" && c.SCM != SCMForgejo {
		return fmt.Errorf("unsupported BEHAVIOUR_SCM %q", c.SCM)
	}
	if c.CI != "githubactions" && c.CI != CIForgejoActions {
		return fmt.Errorf("unsupported BEHAVIOUR_CI %q", c.CI)
	}
	if c.Environment != "dev" && c.Environment != "stage" && c.Environment != "github-emulator" && c.Environment != EnvironmentForgejoDev {
		return fmt.Errorf("unsupported ENVIRONMENT %q (want dev, stage, github-emulator, or %s)", c.Environment, EnvironmentForgejoDev)
	}
	// The Forgejo drivers run only together and only in forgejo-dev, so
	// a Forgejo selection never mixes with GitHub or GitLab drivers.
	forgejoEnv := c.Environment == EnvironmentForgejoDev
	if forgejoEnv && c.SCM != SCMForgejo {
		return fmt.Errorf("ENVIRONMENT=%s requires BEHAVIOUR_SCM=%s, got %q", EnvironmentForgejoDev, SCMForgejo, c.SCM)
	}
	if forgejoEnv && c.CI != CIForgejoActions {
		return fmt.Errorf("ENVIRONMENT=%s requires BEHAVIOUR_CI=%s, got %q", EnvironmentForgejoDev, CIForgejoActions, c.CI)
	}
	if !forgejoEnv && c.SCM == SCMForgejo {
		return fmt.Errorf("BEHAVIOUR_SCM=%s requires ENVIRONMENT=%s, got %q", SCMForgejo, EnvironmentForgejoDev, c.Environment)
	}
	if !forgejoEnv && c.CI == CIForgejoActions {
		return fmt.Errorf("BEHAVIOUR_CI=%s requires ENVIRONMENT=%s, got %q", CIForgejoActions, EnvironmentForgejoDev, c.Environment)
	}
	return nil
}

func stringsTrimOrDefault(value, fallback string) string {
	if v := strings.TrimSpace(value); v != "" {
		return v
	}
	return fallback
}
