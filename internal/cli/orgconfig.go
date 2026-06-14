package cli

import (
	"fmt"
	"os"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// tryLoadOrgConfig attempts to load org config from the given path.
// Returns nil without error when the file is absent (best-effort).
// Logs warnings via printer for non-ENOENT read errors and parse errors.
func tryLoadOrgConfig(path string, printer *ui.Printer) *config.OrgConfig {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			printer.StepWarn("Org config unreadable (remote resource allowlist unavailable): " + err.Error())
		}
		return nil
	}
	cfg, parseErr := config.ParseOrgConfig(data)
	if parseErr != nil {
		printer.StepWarn("Org config malformed (remote resource allowlist unavailable): " + parseErr.Error())
		return nil
	}
	return cfg
}

// requireOrgConfig loads org config from the given path with strict error
// handling. Returns differentiated errors for missing files, unreadable
// files, and parse failures.
func requireOrgConfig(path string, printer *ui.Printer) (*config.OrgConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		printer.StepFail("Failed to load org config")
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("URL-referenced resources require an org-level config.yaml with allowed_remote_resources (expected at %s)", path)
		}
		return nil, fmt.Errorf("reading org config for remote resource validation: %w", err)
	}
	cfg, parseErr := config.ParseOrgConfig(data)
	if parseErr != nil {
		printer.StepFail("Failed to parse org config")
		return nil, fmt.Errorf("parsing org config: %w", parseErr)
	}
	return cfg, nil
}
