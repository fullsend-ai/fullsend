package evalmeasure

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// PlatformTelemetryFile is the host recorder's JSONL at the top of runDir.
// It matches internal/telemetry.TelemetryFile. Nested copies under
// iteration-N/output/ are agent-writable and must not be scored.
const PlatformTelemetryFile = "run-telemetry.jsonl"

// hostRunDirPattern matches the legacy agent-<name>-<pid>-<unix> format.
// name is lowercased when the sandbox is created; charset matches run
// (ToLower only), so pid/unix must be the trailing numeric pair.
var hostRunDirPattern = regexp.MustCompile(`^agent-(.+)-([0-9]+)-([0-9]+)$`)

// newHostRunDirPattern matches the current fs-<slug>-<hex> format where
// slug is a 3-character lowercase alphanumeric abbreviation of the agent
// name. Both patterns are checked so existing run directories from before
// the naming change are still discovered.
var newHostRunDirPattern = regexp.MustCompile(`^fs-([a-z0-9]{3})-([0-9a-f]+)$`)

// FindPlatformTelemetry returns run-telemetry.jsonl files that sit at the
// top of outputDir itself (when outputDir is a runDir) or at the top of
// a host-created child runDir (when outputDir is the CI output base).
//
// Host runDirs use one of two naming schemes:
//   - Legacy: agent-<name>-<pid>-<unix>  (full agent name in the directory)
//   - Current: fs-<slug>-<hex>           (3-char agent slug + hash)
//
// When agent is non-empty, only children whose embedded agent name (legacy)
// or slug (current) matches are considered. For the current scheme, the
// slug is a lossy 3-character abbreviation — agents with the same prefix
// (e.g. "review" and "reverse" both map to "rev") are indistinguishable
// from the directory name alone. In practice this is acceptable because a
// single CI job runs one agent at a time.
//
// If several match, only the newest platform file is scored — leftover
// sibling directories from a previous job are ignored.
//
// Matching child runDirs outrank a root-level run-telemetry.jsonl under
// outputDir so an agent-planted file at the CI output base cannot displace
// the real host recorder. If any matching child runDir exists (even
// without a platform file yet), the root file is ignored. Nested
// iteration-N/output/ copies are never walked.
func FindPlatformTelemetry(outputDir, agent string) ([]string, error) {
	wantAgent := strings.ToLower(agent)
	paths, sawMatch, err := findChildPlatformTelemetry(outputDir, wantAgent)
	if err != nil {
		return nil, err
	}
	if sawMatch {
		return paths, nil
	}

	direct := filepath.Join(outputDir, PlatformTelemetryFile)
	if st, err := os.Stat(direct); err == nil && !st.IsDir() {
		// outputDir is a runDir (or has only a root-level file and no
		// matching child): score only the platform file at the top.
		return []string{direct}, nil
	}
	return nil, nil
}

// findChildPlatformTelemetry looks for child directories matching either the
// legacy agent-<name>-<pid>-<unix> or current fs-<slug>-<hex> naming scheme.
// sawMatch is true when at least one directory matched a pattern (and
// agent filter), whether or not PlatformTelemetryFile was present.
func findChildPlatformTelemetry(outputDir, wantAgent string) (paths []string, sawMatch bool, err error) {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	wantSlug := agentSlug(wantAgent)
	var bestPath string
	var bestMod time.Time
	foundFile := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !matchesRunDir(e.Name(), wantAgent, wantSlug) {
			continue
		}
		sawMatch = true
		p := filepath.Join(outputDir, e.Name(), PlatformTelemetryFile)
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			continue
		}
		if !foundFile || st.ModTime().After(bestMod) {
			bestPath = p
			bestMod = st.ModTime()
			foundFile = true
		}
	}
	if !foundFile {
		return nil, sawMatch, nil
	}
	return []string{bestPath}, sawMatch, nil
}

// IsHostRunDir reports whether dirName is a host-created run directory
// name (legacy agent-<name>-<pid>-<unix> or current fs-<slug>-<hex>),
// for any agent.
func IsHostRunDir(dirName string) bool {
	return matchesRunDir(dirName, "", "")
}

// matchesRunDir reports whether dirName matches either the legacy
// agent-<name>-<pid>-<unix> or current fs-<slug>-<hex> naming scheme,
// optionally filtering by agent name.
func matchesRunDir(dirName, wantAgent, wantSlug string) bool {
	// Try legacy format first.
	if m := hostRunDirPattern.FindStringSubmatch(dirName); m != nil {
		return wantAgent == "" || m[1] == wantAgent
	}
	// Try current format.
	if m := newHostRunDirPattern.FindStringSubmatch(dirName); m != nil {
		return wantAgent == "" || m[1] == wantSlug
	}
	return false
}

// agentSlug returns the first 3 lowercase alphanumeric characters of the
// agent name. Returns "" for empty names (no filtering). This mirrors the
// slug derivation in internal/cli.agentSlug but avoids a cross-package
// dependency — the two must stay in sync.
func agentSlug(name string) string {
	if name == "" {
		return ""
	}
	const slugLen = 3
	var slug []byte
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			slug = append(slug, byte(r))
			if len(slug) == slugLen {
				return string(slug)
			}
		}
	}
	if len(slug) == 0 {
		return "unk"
	}
	for len(slug) < slugLen {
		slug = append(slug, slug[len(slug)-1])
	}
	return string(slug)
}
