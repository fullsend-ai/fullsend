package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// tryLoadFullsendConfig attempts to load an org or per-repo config.yaml
// from the given path. Returns nil without error when the file is absent
// (best-effort). The returned ConfigWriter provides a unified view of
// both org and per-repo configs with ensured defaults.
func tryLoadFullsendConfig(path string, printer *ui.Printer) config.ConfigWriter {
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			printer.StepWarn("Fullsend config unreadable (remote resource allowlist unavailable): " + err.Error())
		}
		return nil
	}
	writer, err := config.LoadConfigWriter(filepath.Dir(path), config.LoadOpts{MissingOK: false})
	if err != nil {
		printer.StepWarn("Config malformed (remote resource allowlist unavailable): " + err.Error())
		return nil
	}
	writer.SetAllowedRemoteResources(config.EnsureDefaultAllowedRemoteResources(writer.AllowedResources()))
	return writer
}

// tryLoadOrgConfig loads an org or per-repo config.yaml (best-effort).
var tryLoadOrgConfig = tryLoadFullsendConfig

// requireFullsendConfig loads an org or per-repo config.yaml from the
// given path with strict error handling. Returns differentiated errors
// for missing files, unreadable files, and parse failures.
func requireFullsendConfig(path string, printer *ui.Printer) (config.ConfigWriter, error) {
	writer, err := config.LoadConfigWriter(filepath.Dir(path), config.LoadOpts{MissingOK: false})
	if err != nil {
		printer.StepFail("Failed to load fullsend config")
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("URL-referenced resources require a config.yaml with allowed_remote_resources (expected at %s)", path)
		}
		return nil, fmt.Errorf("reading fullsend config for remote resource validation: %w", err)
	}
	writer.SetAllowedRemoteResources(config.EnsureDefaultAllowedRemoteResources(writer.AllowedResources()))
	return writer, nil
}

// requireOrgConfig loads an org or per-repo config.yaml (strict).
var requireOrgConfig = requireFullsendConfig
