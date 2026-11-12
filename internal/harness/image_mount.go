package harness

import (
	"fmt"
	"path"
	"strings"
	"unicode"
)

// mergeImageMounts combines inherited and local image mounts. A later entry
// replaces an earlier entry at the same destination while retaining the
// destination's position in the result.
func mergeImageMounts(base, child []ImageMount) []ImageMount {
	indexes := make(map[string]int, len(base)+len(child))
	merged := make([]ImageMount, 0, len(base)+len(child))
	for _, mount := range append(append([]ImageMount(nil), base...), child...) {
		if index, ok := indexes[mount.Dest]; ok {
			merged[index] = mount
			continue
		}
		indexes[mount.Dest] = len(merged)
		merged = append(merged, mount)
	}
	return merged
}

func validateImageMounts(mounts []ImageMount, hostFiles []HostFile, field string) error {
	for i, mount := range mounts {
		prefix := fmt.Sprintf("%s[%d]", field, i)
		if mount.Image == "" || strings.TrimSpace(mount.Image) != mount.Image || strings.Contains(mount.Image, "${") || strings.IndexFunc(mount.Image, unicode.IsSpace) >= 0 || strings.Contains(mount.Image, "://") {
			return fmt.Errorf("%s.image must be an OCI image reference without whitespace or variable expansion", prefix)
		}
		if mount.Dest == "" {
			return fmt.Errorf("%s.dest is required", prefix)
		}
		if !path.IsAbs(mount.Dest) {
			return fmt.Errorf("%s.dest %q must be an absolute path", prefix, mount.Dest)
		}
		for _, part := range strings.Split(mount.Dest, "/") {
			if part == ".." {
				return fmt.Errorf("%s.dest %q must not contain a \"..\" path component", prefix, mount.Dest)
			}
		}
		if path.Clean(mount.Dest) != mount.Dest {
			return fmt.Errorf("%s.dest %q must use a clean absolute path", prefix, mount.Dest)
		}
		if mount.Dest == "/" {
			return fmt.Errorf("%s.dest must not replace the container root", prefix)
		}
		for j, hostFile := range hostFiles {
			if pathsOverlap(mount.Dest, hostFile.Dest) {
				return fmt.Errorf("%s.dest %q overlaps host_files[%d].dest %q", prefix, mount.Dest, j, hostFile.Dest)
			}
		}
		for j := 0; j < i; j++ {
			other := mounts[j]
			if mount.Dest != other.Dest && pathsOverlap(mount.Dest, other.Dest) {
				return fmt.Errorf("%s.dest %q overlaps %s[%d].dest %q", prefix, mount.Dest, field, j, other.Dest)
			}
		}
	}
	return nil
}

func pathsOverlap(a, b string) bool {
	a, b = path.Clean(a), path.Clean(b)
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
