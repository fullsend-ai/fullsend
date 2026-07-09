package harnessdispatch

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// KillSwitchActive reads kill_switch from config in configDir.
func KillSwitchActive(configDir string) (bool, error) {
	cfgPath := filepath.Join(configDir, "config.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading config: %w", err)
	}
	cfg, err := config.ParsePerRepoConfig(data)
	if err != nil {
		// Try org config format for flexibility in tests.
		orgCfg, orgErr := config.ParseOrgConfig(data)
		if orgErr != nil {
			return false, fmt.Errorf("parsing config: %w", err)
		}
		return orgCfg.KillSwitch, nil
	}
	return cfg.KillSwitch, nil
}
