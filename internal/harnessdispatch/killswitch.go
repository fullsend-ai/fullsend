package harnessdispatch

import "github.com/fullsend-ai/fullsend/internal/config"

// KillSwitchActive reads kill_switch from config in configDir.
func KillSwitchActive(configDir string) (bool, error) {
	cfg, err := config.LoadFromDir(configDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		return false, err
	}
	return cfg.KillSwitch, nil
}
