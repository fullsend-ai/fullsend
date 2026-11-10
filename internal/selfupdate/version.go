package selfupdate

import (
	"regexp"
	"strconv"
	"strings"
)

// releaseVersionRe matches release versions such as "0.53.0", "v0.53.0"
// and "v0.54.0-rc.1". Anything else is a development build. Candidates
// start at rc.1, because rc 0 stands for a final release.
var releaseVersionRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-rc\.([1-9]\d*))?$`)

// version is a parsed fullsend release version.
type version struct {
	major, minor, patch int
	// rc is the release-candidate number, or 0 for a final release.
	rc int
}

// parseVersion parses a release version. ok is false for anything else.
func parseVersion(s string) (v version, ok bool) {
	m := releaseVersionRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return version{}, false
	}
	nums := make([]int, 4)
	for i, part := range m[1:] {
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return version{}, false
		}
		nums[i] = n
	}
	return version{major: nums[0], minor: nums[1], patch: nums[2], rc: nums[3]}, true
}

// compare returns -1, 0, or 1 when v is older than, equal to, or newer than
// o. A release candidate is older than the final release it precedes.
func (v version) compare(o version) int {
	for _, d := range [...]int{v.major - o.major, v.minor - o.minor, v.patch - o.patch} {
		if d != 0 {
			return sign(d)
		}
	}
	switch {
	case v.rc == o.rc:
		return 0
	case v.rc == 0:
		return 1
	case o.rc == 0:
		return -1
	default:
		return sign(v.rc - o.rc)
	}
}

// tag returns the git tag for v, for example "v0.54.0-rc.1".
func (v version) tag() string {
	t := "v" + strconv.Itoa(v.major) + "." + strconv.Itoa(v.minor) + "." + strconv.Itoa(v.patch)
	if v.rc > 0 {
		t += "-rc." + strconv.Itoa(v.rc)
	}
	return t
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}
