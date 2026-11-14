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

func (h *Harness) normalizeImageMounts() {
	if h.ImageMounts != nil {
		h.ImageMounts = mergeImageMounts(nil, h.ImageMounts)
	}
	for _, config := range h.Forge {
		if config != nil && config.ImageMounts != nil {
			config.ImageMounts = mergeImageMounts(nil, config.ImageMounts)
		}
	}
	for i := range h.Overlays {
		if h.Overlays[i].ImageMounts != nil {
			h.Overlays[i].ImageMounts = mergeImageMounts(nil, h.Overlays[i].ImageMounts)
		}
	}
}

func reservedImageMountPaths() []string {
	paths := []string{reservedHostFileBinDir, "/sandbox/opencode-config"}
	paths = append(paths, reservedHostFileExact...)
	paths = append(paths, reservedHostFileDirs...)
	return paths
}

func validateImageMounts(mounts []ImageMount, hostFiles []HostFile, field string) error {
	hostFilesField := "host_files"
	if strings.HasSuffix(field, "image_mounts") {
		prefix := strings.TrimSuffix(field, "image_mounts")
		if prefix != "" {
			hostFilesField = prefix + "host_files"
		}
	}
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
		for _, reserved := range reservedImageMountPaths() {
			if pathsOverlap(mount.Dest, reserved) {
				return fmt.Errorf("%s.dest %q overlaps runner-owned path %q", prefix, mount.Dest, reserved)
			}
		}
		for j, hostFile := range hostFiles {
			if pathsOverlap(mount.Dest, hostFile.Dest) {
				return fmt.Errorf("%s.dest %q overlaps %s[%d].dest %q", prefix, mount.Dest, hostFilesField, j, hostFile.Dest)
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
