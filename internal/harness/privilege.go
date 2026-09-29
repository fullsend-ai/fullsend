package harness

import (
	"fmt"
	"regexp"
	"strings"
)

// Run-stage keys accepted by privilege_levels (ADR 0073). validation_loop is
// not a YAML key: it inherits the runtime level because it shares the
// runtime security context.
const (
	PrivilegeStagePreScript      = "pre_script"
	PrivilegeStageRuntime        = "runtime"
	PrivilegeStagePostScript     = "post_script"
	PrivilegeStageDefault        = "default"
	PrivilegeStageValidationLoop = "validation_loop"

	// DefaultPrivilegeLevel is used when privilege_levels is omitted or a
	// run-stage (and default) is unset. Preserves pre-ADR-0073 write tokens.
	DefaultPrivilegeLevel = "write"
)

// validPrivilegeLevelName mirrors mintcore.LevelPattern — duplicated to
// avoid coupling harness→mintcore.
var validPrivilegeLevelName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

var validPrivilegeStages = map[string]struct{}{
	PrivilegeStagePreScript:  {},
	PrivilegeStageRuntime:    {},
	PrivilegeStagePostScript: {},
	PrivilegeStageDefault:    {},
}

// PrivilegeLevelForStage returns the mint privilege level for a run-stage.
// validation_loop inherits runtime. Unlisted stages inherit default, then
// DefaultPrivilegeLevel ("write") so omitting privilege_levels is a no-op.
func (h *Harness) PrivilegeLevelForStage(stage string) string {
	if stage == PrivilegeStageValidationLoop {
		stage = PrivilegeStageRuntime
	}
	if h == nil || len(h.PrivilegeLevels) == 0 {
		return DefaultPrivilegeLevel
	}
	if level := strings.TrimSpace(h.PrivilegeLevels[stage]); level != "" {
		return level
	}
	if level := strings.TrimSpace(h.PrivilegeLevels[PrivilegeStageDefault]); level != "" {
		return level
	}
	return DefaultPrivilegeLevel
}

func (h *Harness) validatePrivilegeLevels() error {
	if h == nil || len(h.PrivilegeLevels) == 0 {
		return nil
	}
	for stage, level := range h.PrivilegeLevels {
		if _, ok := validPrivilegeStages[stage]; !ok {
			return fmt.Errorf("privilege_levels: unknown run-stage %q (allowed: pre_script, runtime, post_script, default)", stage)
		}
		if strings.TrimSpace(level) == "" {
			return fmt.Errorf("privilege_levels.%s: level is required", stage)
		}
		if !validPrivilegeLevelName.MatchString(level) {
			return fmt.Errorf("privilege_levels.%s: invalid level name %q (allowed: lowercase letter first, then a-z, 0-9, _, -; max 32 chars)", stage, level)
		}
	}
	return nil
}
