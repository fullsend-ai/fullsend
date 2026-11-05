package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGoVersionPinIsRenovateTracked asserts the Go toolchain version pinned
// in images/code/Containerfile and images/runner/Containerfile has a
// matching renovate.json customManager for each file. Without one, Renovate
// never opens a bump PR for GO_VERSION and the pin silently drifts behind
// go.mod's own toolchain requirement — the gap reported in #7901. This
// mirrors TestSandboxImagePinsAreRenovateTracked, which catches the same
// class of gap for the PI_*/CODEX pins in the sandbox image.
func TestGoVersionPinIsRenovateTracked(t *testing.T) {
	t.Parallel()

	renovateRaw, err := os.ReadFile(filepath.Join("..", "..", "renovate.json"))
	require.NoError(t, err, "renovate.json must be readable")
	var renovate struct {
		CustomManagers []struct {
			ManagerFilePatterns []string `json:"managerFilePatterns"`
			MatchStrings        []string `json:"matchStrings"`
		} `json:"customManagers"`
	}
	require.NoError(t, json.Unmarshal(renovateRaw, &renovate), "renovate.json must be valid JSON")

	goVersionPin := regexp.MustCompile(`(?m)^ARG GO_VERSION=`)

	for _, cf := range []string{
		filepath.Join("images", "code", "Containerfile"),
		filepath.Join("images", "runner", "Containerfile"),
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", cf))
		require.NoErrorf(t, err, "%s must be readable", cf)
		require.Regexpf(t, goVersionPin, string(data),
			"%s is expected to pin ARG GO_VERSION", cf)

		slashPath := filepath.ToSlash(cf)
		var tracked bool
		for _, m := range renovate.CustomManagers {
			var fileMatches bool
			for _, fp := range m.ManagerFilePatterns {
				if strings.Contains(fp, slashPath) {
					fileMatches = true
					break
				}
			}
			if !fileMatches {
				continue
			}
			for _, ms := range m.MatchStrings {
				if strings.Contains(ms, "GO_VERSION") {
					tracked = true
				}
			}
		}
		assert.Truef(t, tracked,
			"%s pins GO_VERSION but renovate.json has no customManager tracking it for that file", cf)
	}
}
