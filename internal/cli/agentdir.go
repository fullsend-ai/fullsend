package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// registerAgentDirFlag registers --agent-dir as the primary flag for specifying
// the base directory containing agent definitions, and --fullsend-dir as a
// deprecated hidden alias. Callers must invoke resolveAgentDirFlag in their RunE
// to merge the values, enforce mutual exclusivity, and emit any deprecation
// warnings.
func registerAgentDirFlag(cmd *cobra.Command, target *string) {
	cmd.Flags().StringVar(target, "agent-dir", "", "base directory containing agent definitions")
	cmd.Flags().String("fullsend-dir", "", "deprecated: use --agent-dir instead")
	_ = cmd.Flags().MarkHidden("fullsend-dir")
}

// resolveAgentDirFlag merges --agent-dir and --fullsend-dir values. When only
// --fullsend-dir is given, a deprecation warning is printed to stderr and the
// value is forwarded to target. Returns an error when both flags are specified
// or when neither is provided (the flag is effectively required).
func resolveAgentDirFlag(cmd *cobra.Command, target *string) error {
	agentDirChanged := cmd.Flags().Changed("agent-dir")
	fullsendDirChanged := cmd.Flags().Changed("fullsend-dir")

	if agentDirChanged && fullsendDirChanged {
		return fmt.Errorf("--agent-dir and --fullsend-dir are mutually exclusive; use --agent-dir")
	}

	if fullsendDirChanged {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: --fullsend-dir is deprecated, use --agent-dir instead")
		val, _ := cmd.Flags().GetString("fullsend-dir")
		*target = val
	}

	if !agentDirChanged && !fullsendDirChanged {
		return fmt.Errorf("required flag \"agent-dir\" not set")
	}

	return nil
}
