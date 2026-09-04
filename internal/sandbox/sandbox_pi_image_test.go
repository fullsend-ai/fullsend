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

// The sandbox image bakes pi's config paths as ENV defaults for ad-hoc
// invocations; they must agree with the constants PiRuntime.EnvExports uses.
func TestSandboxImagePiDefaults(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "images", "sandbox", "Containerfile"))
	require.NoError(t, err)
	containerfile := string(data)
	for _, want := range []string{
		`PI_CODING_AGENT_DIR="` + SandboxPiConfig + `"`,
		`PI_CODING_AGENT_SESSION_DIR="` + SandboxPiConfig + `/sessions"`,
		`PI_OFFLINE="1"`,
		`PI_SKIP_VERSION_CHECK="1"`,
		`PI_TELEMETRY="0"`,
		// The vetted extension set lives where PiRuntime expects to -e it from
		// (runtime.piVertexExtensionPath = SandboxPiExtensionsDir + "/anthropic-vertex",
		//  runtime.piXaiVertexExtensionPath = SandboxPiExtensionsDir + "/xai-vertex").
		`ARG PI_EXTENSIONS_DIR=` + SandboxPiExtensionsDir,
		`"${PI_EXTENSIONS_DIR}/anthropic-vertex"`,
		`"${PI_EXTENSIONS_DIR}/xai-vertex"`,
	} {
		assert.Contains(t, containerfile, want)
	}
}

// TestSandboxImagePinsAreRenovateTracked asserts every runtime version pin in
// the sandbox Containerfile (PI_*_VERSION, CODEX_VERSION) has a matching
// renovate.json customManager. A pin with
// no manager is invisible: it silently stays on whatever version it was added
// at, and the only signal is a comment telling a human to check tags by hand.
// This caught the pi-xai-vertex pin shipping untracked (#6571).
func TestSandboxImagePinsAreRenovateTracked(t *testing.T) {
	t.Parallel()
	cfRaw, err := os.ReadFile(filepath.Join("..", "..", "images", "sandbox", "Containerfile"))
	require.NoError(t, err, "sandbox Containerfile must be readable")
	containerfile := string(cfRaw)

	renovateRaw, err2 := os.ReadFile(filepath.Join("..", "..", "renovate.json"))
	require.NoError(t, err2, "renovate.json must be readable")
	var renovate struct {
		CustomManagers []struct {
			MatchStrings []string `json:"matchStrings"`
		} `json:"customManagers"`
	}
	require.NoError(t, json.Unmarshal(renovateRaw, &renovate), "renovate.json must be valid JSON")

	pins := regexp.MustCompile(`(?m)^ARG (PI_[A-Z_]*VERSION|CODEX_VERSION)=`).FindAllStringSubmatch(containerfile, -1)
	require.NotEmpty(t, pins, "expected at least one runtime version pin")

	for _, pin := range pins {
		name := pin[1]
		var tracked bool
		for _, m := range renovate.CustomManagers {
			for _, ms := range m.MatchStrings {
				if strings.Contains(ms, name) {
					tracked = true
				}
			}
		}
		assert.True(t, tracked, "%s has no renovate.json customManager tracking it", name)
	}
}

// Tarball-pinned pi extensions are identified twice: by the download URL the
// Containerfile fetches, and by the depNameTemplate the renovate.json
// customManager opens bump PRs against. TestSandboxImagePinsAreRenovateTracked
// only proves a manager exists for the ARG, so a swap that moved the download
// to a new owner but left the depNameTemplate behind would keep every test
// green while Renovate tracked the abandoned repo's tags (#7019).
//
// Driven from renovate.json rather than from the URLs, so a pin whose URL stops
// matching fails loudly instead of dropping out of the loop.
func TestSandboxImageExtensionPinsMatchRenovateDepNames(t *testing.T) {
	t.Parallel()
	cfRaw, err := os.ReadFile(filepath.Join("..", "..", "images", "sandbox", "Containerfile"))
	require.NoError(t, err, "sandbox Containerfile must be readable")
	containerfile := string(cfRaw)

	renovateRaw, err2 := os.ReadFile(filepath.Join("..", "..", "renovate.json"))
	require.NoError(t, err2, "renovate.json must be readable")
	var renovate struct {
		CustomManagers []struct {
			MatchStrings       []string `json:"matchStrings"`
			DepNameTemplate    string   `json:"depNameTemplate"`
			DatasourceTemplate string   `json:"datasourceTemplate"`
		} `json:"customManagers"`
	}
	require.NoError(t, json.Unmarshal(renovateRaw, &renovate), "renovate.json must be valid JSON")

	const remoteScheme = "https:" + "//"
	argOf := regexp.MustCompile(`ARG (PI_[A-Z_]*VERSION)=`)

	var checked int
	for _, m := range renovate.CustomManagers {
		if m.DatasourceTemplate != "github-tags" {
			continue
		}
		for _, ms := range m.MatchStrings {
			hit := argOf.FindStringSubmatch(ms)
			if hit == nil {
				continue
			}
			argName := hit[1]
			checked++

			// Every token in the Containerfile that interpolates this pin; the
			// remote ones are the downloads Renovate is meant to track.
			tokens := regexp.MustCompile(
				`\S*\$\{`+regexp.QuoteMeta(argName)+`\}\S*`,
			).FindAllString(containerfile, -1)

			var urls []string
			for _, tk := range tokens {
				if u := strings.Trim(tk, `"'`+"`"+`\\`); strings.HasPrefix(u, remoteScheme) {
					urls = append(urls, u)
				}
			}
			require.NotEmpty(t, urls,
				"renovate.json tracks %s as %q but no Containerfile download URL interpolates that ARG",
				argName, m.DepNameTemplate)

			for _, u := range urls {
				assert.Contains(t, u, "/"+m.DepNameTemplate+"/",
					"%s downloads from %q but renovate.json tracks %q; Renovate would bump against the wrong repo",
					argName, u, m.DepNameTemplate)
			}
		}
	}
	require.NotZero(t, checked, "expected at least one github-tags customManager pinning a PI_* extension")
}
