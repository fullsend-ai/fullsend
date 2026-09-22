package behaviourtest

import (
	"fmt"
	"strings"
)

// SuiteOptions configures RunSuite. The surface is intentionally
// narrow: feature paths and fixture root differ per consumer, while
// driver selection, tags, and concurrency continue to come from
// environment variables (BEHAVIOUR_SCM, BEHAVIOUR_CI, ENVIRONMENT,
// GODOG_TAGS, GODOG_CONCURRENCY, and the rest of the existing runner
// config).
type SuiteOptions struct {
	// FeaturePaths are godog feature file or directory paths, relative
	// to the test working directory (the test package when invoked via
	// `go test ./...`).
	FeaturePaths []string

	// FixturesRoot is the module-relative directory that contains
	// fixtures/ (e.g. "e2e/behaviour" in this repo, "behaviour" in
	// fullsend-ai/agents).
	FixturesRoot string
}

func (o SuiteOptions) validate() error {
	if strings.TrimSpace(o.FixturesRoot) == "" {
		return fmt.Errorf("FixturesRoot is required")
	}
	if len(o.FeaturePaths) == 0 {
		return fmt.Errorf("FeaturePaths is required")
	}
	for i, p := range o.FeaturePaths {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("FeaturePaths[%d] is empty", i)
		}
	}
	return nil
}
