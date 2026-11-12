package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// tryLoadFullsendConfig attempts to load a .fullsend config.yaml from the
// given path. Returns nil without error when the file is absent
// (best-effort). The returned ConfigWriter has ensured defaults.
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

// tryLoadOrgConfig is an alias of tryLoadFullsendConfig (best-effort).
var tryLoadOrgConfig = tryLoadFullsendConfig

// tryLoadLayeredFullsendConfig attempts to load the layered fullsend
// config (config.yaml over config.base.yaml) from fullsendDir.
// Unlike tryLoadFullsendConfig, it does not require config.yaml to
// exist on disk: a base-only layout (config.base.yaml present,
// config.yaml absent) is honored, matching loadLockConfig's semantics.
// Returns nil without error when neither layer exists, or when the
// config is malformed (best-effort, with a warning printed via
// printer in the malformed case). The returned ConfigWriter has
// ensured defaults.
func tryLoadLayeredFullsendConfig(fullsendDir string, printer *ui.Printer) config.ConfigWriter {
	writer, err := config.LoadConfigWriter(fullsendDir, config.LoadOpts{MissingOK: false})
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			printer.StepWarn("Config malformed (remote resource allowlist unavailable): " + err.Error())
		}
		return nil
	}
	writer.SetAllowedRemoteResources(config.EnsureDefaultAllowedRemoteResources(writer.AllowedResources()))
	return writer
}

// requireFullsendConfig loads a .fullsend config.yaml from the
// given path with strict error handling. Returns differentiated errors
// for missing files, unreadable files, and parse failures; the
// missing-file error names URL-referenced resources.
func requireFullsendConfig(path string, printer *ui.Printer) (config.ConfigWriter, error) {
	return requireFullsendConfigFor(path, printer, "URL-referenced resources require")
}

// requireFullsendConfigFor is requireFullsendConfig with the lead of the
// missing-file message supplied by the caller, so each gate names its own
// requirement. Unreadable and unparsable configs keep their own errors
// either way: only a config.yaml that is absent is reported as missing.
func requireFullsendConfigFor(path string, printer *ui.Printer, missingLead string) (config.ConfigWriter, error) {
	writer, err := config.LoadConfigWriter(filepath.Dir(path), config.LoadOpts{MissingOK: false})
	if err != nil {
		printer.StepFail("Failed to load fullsend config")
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s a config.yaml with allowed_remote_resources (expected at %s)", missingLead, path)
		}
		return nil, fmt.Errorf("reading fullsend config for remote resource validation: %w", err)
	}
	writer.SetAllowedRemoteResources(config.EnsureDefaultAllowedRemoteResources(writer.AllowedResources()))
	return writer, nil
}

// requireOrgConfig is an alias of requireFullsendConfig (strict).
var requireOrgConfig = requireFullsendConfig

// requireOrgConfigFor is an alias of requireFullsendConfigFor (strict).
var requireOrgConfigFor = requireFullsendConfigFor
