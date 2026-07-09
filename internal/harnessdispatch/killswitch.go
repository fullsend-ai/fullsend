package harnessdispatch

// KillSwitchActive reads kill_switch from config in configDir.
func KillSwitchActive(configDir string) (bool, error) {
	cfg, err := LoadConfigDir(configDir)
	if err != nil {
		return false, err
	}
	return cfg.KillSwitch, nil
}
