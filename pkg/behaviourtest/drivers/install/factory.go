package install

import (
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/e2etest"
)

// NewDriver returns the install driver for the configured BEHAVIOUR_INSTALL_MODE.
func NewDriver(
	cfg env.RunnerConfig,
	e2eCfg e2etest.EnvConfig,
	client forge.Client,
	token, binary string,
	logf func(string, ...any),
) (Driver, error) {
	switch cfg.InstallMode {
	case "per-repo":
		return newPerRepoDriver(e2eCfg, client, token, binary, logf), nil
	default:
		return nil, fmt.Errorf("unsupported BEHAVIOUR_INSTALL_MODE %q", cfg.InstallMode)
	}
}
