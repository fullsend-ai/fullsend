package binary

import "strings"

// IsReleasedVersion returns true if version looks like a release tag
// (e.g. "0.4.0", "v0.4.0") rather than a dev build (e.g. "dev",
// "0.4.0-3-gabcdef", "0.4.0-vendored").
func IsReleasedVersion(v string) bool {
	v = strings.TrimPrefix(v, "v")
	if v == "" || v == "dev" {
		return false
	}
	// A released version is purely digits and dots (e.g. "0.4.0").
	for _, c := range v {
		if c != '.' && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
