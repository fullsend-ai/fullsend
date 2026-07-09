package harnessdispatch

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// LoadConfigDir reads and parses config.yaml from configDir.
func LoadConfigDir(configDir string) (*config.PerRepoConfig, error) {
	cfgPath := filepath.Join(configDir, "config.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return config.NewPerRepoConfig(nil, ""), nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}
	cfg, err := config.ParsePerRepoConfig(data)
	if err != nil {
		orgCfg, orgErr := config.ParseOrgConfig(data)
		if orgErr != nil {
			return nil, fmt.Errorf("parsing config: %w", err)
		}
		return &config.PerRepoConfig{
			KillSwitch:             orgCfg.KillSwitch,
			Agents:                 orgCfg.Agents,
			AllowedRemoteResources: orgCfg.AllowedRemoteResources,
		}, nil
	}
	return cfg, nil
}
