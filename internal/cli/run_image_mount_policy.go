package cli

import (
	"fmt"
	"path"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"gopkg.in/yaml.v3"
)

type imageMountFilesystemPolicy struct {
	FilesystemPolicy struct {
		ReadOnly  []string `yaml:"read_only"`
		ReadWrite []string `yaml:"read_write"`
	} `yaml:"filesystem_policy"`
}

// checkImageMountPolicyAccess checks only whether each mount destination is
// covered by an effective read-only or read-write filesystem path. It is not
// a general Landlock policy evaluator; the read-only image mount itself still
// prevents writes.
func checkImageMountPolicyAccess(policyYAML []byte, policyName string, mounts []harness.ImageMount) error {
	if len(mounts) == 0 {
		return nil
	}
	var effective imageMountFilesystemPolicy
	if err := yaml.Unmarshal(policyYAML, &effective); err != nil {
		return fmt.Errorf("parsing effective sandbox policy %q for image mounts: %w", policyName, err)
	}
	allowedPaths := append(effective.FilesystemPolicy.ReadOnly, effective.FilesystemPolicy.ReadWrite...)
	for _, mount := range mounts {
		if imageMountPathAllowed(mount.Dest, allowedPaths) {
			continue
		}
		return fmt.Errorf("effective sandbox policy %q does not grant filesystem access to image mount destination %q; add that path or an allowed ancestor to filesystem_policy.read_only or filesystem_policy.read_write", policyName, mount.Dest)
	}
	return nil
}

func imageMountPathAllowed(target string, allowedPaths []string) bool {
	if !path.IsAbs(target) || path.Clean(target) != target {
		return false
	}
	target = path.Clean(target)
	for _, allowed := range allowedPaths {
		allowed = strings.TrimSpace(allowed)
		if !path.IsAbs(allowed) {
			continue
		}
		allowed = path.Clean(allowed)
		if target == allowed || allowed == "/" || strings.HasPrefix(target, strings.TrimSuffix(allowed, "/")+"/") {
			return true
		}
	}
	return false
}
