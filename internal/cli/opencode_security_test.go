package cli

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateOpenCodeSecurity(t *testing.T) {
	for _, runtimeName := range []string{"opencode", "pi", "claude", "codex", "dummy"} {
		for _, enabled := range []bool{true, false} {
			t.Run(runtimeName+"/security-enabled="+strconv.FormatBool(enabled), func(t *testing.T) {
				err := validateOpenCodeSecurity(runtimeName, enabled)
				if runtimeName == "opencode" && !enabled {
					require.ErrorContains(t, err, "security.enabled: true")
				} else {
					require.NoError(t, err)
				}
			})
		}
	}
}
