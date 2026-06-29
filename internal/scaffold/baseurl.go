package scaffold

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

const (
	harnessBaseURLPrefix = "https://raw.githubusercontent.com/fullsend-ai/fullsend/"
	harnessURLPath       = "internal/scaffold/fullsend-repo/harness/"
)

// ExternalAgent describes an agent harness hosted outside the fullsend scaffold.
type ExternalAgent struct {
	URLPrefix   string // raw.githubusercontent.com base, e.g. "https://raw.githubusercontent.com/fullsend-ai/agents/"
	CommitSHA   string // pinned commit in the external repo
	HarnessPath string // path prefix within the repo, e.g. "harness/"
	ContentHash string // SHA-256 hex digest of the remote harness file
}

// externalAgents maps harness names to agent definitions hosted in standalone repos.
var externalAgents = map[string]ExternalAgent{
	"triage": {
		URLPrefix:   "https://raw.githubusercontent.com/fullsend-ai/agents/",
		CommitSHA:   "b929ce3d411bf45e01e1459361221bdee6803912",
		HarnessPath: "harness/",
		ContentHash: "16d87496f672dab3fda3a448477ee85b67cdfb7cec625563fbeecb65dc0ce0cc",
	},
}

// IsExternalAgent reports whether the named harness is hosted outside the scaffold.
func IsExternalAgent(name string) bool {
	_, ok := externalAgents[name]
	return ok
}

var (
	validHarnessName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	validCommitSHA   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// HarnessBaseURL returns the raw.githubusercontent.com URL for a harness
// template at a specific commit SHA. For external agents the commitSHA
// parameter is ignored — the pinned SHA from the external registry is used.
// The URL does not include an integrity hash fragment — use
// HarnessBaseURLWithHash for that.
func HarnessBaseURL(harnessName, commitSHA string) (string, error) {
	if !validHarnessName.MatchString(harnessName) {
		return "", fmt.Errorf("invalid harness name %q: must match %s", harnessName, validHarnessName.String())
	}
	if ext, ok := externalAgents[harnessName]; ok {
		return ext.URLPrefix + ext.CommitSHA + "/" + ext.HarnessPath + harnessName + ".yaml", nil
	}
	if !validCommitSHA.MatchString(commitSHA) {
		return "", fmt.Errorf("invalid commit SHA %q: must be a 40-character lowercase hex string", commitSHA)
	}
	return harnessBaseURLPrefix + commitSHA + "/" + harnessURLPath + harnessName + ".yaml", nil
}

// HarnessContentHash returns the SHA-256 hex digest of a harness template.
// For external agents it returns the pinned hash from the registry. For
// scaffold agents it computes the hash from the embedded file content.
func HarnessContentHash(harnessName string) (string, error) {
	if !validHarnessName.MatchString(harnessName) {
		return "", fmt.Errorf("invalid harness name %q: must match %s", harnessName, validHarnessName.String())
	}
	if ext, ok := externalAgents[harnessName]; ok {
		return ext.ContentHash, nil
	}
	data, err := content.ReadFile("fullsend-repo/harness/" + harnessName + ".yaml")
	if err != nil {
		return "", fmt.Errorf("unknown harness %q: %w", harnessName, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// HarnessBaseURLWithHash returns the full base URL for a scaffold harness
// template, including the #sha256=... integrity hash fragment.
func HarnessBaseURLWithHash(harnessName, commitSHA string) (string, error) {
	base, err := HarnessBaseURL(harnessName, commitSHA)
	if err != nil {
		return "", err
	}
	hash, err := HarnessContentHash(harnessName)
	if err != nil {
		return "", err
	}
	return base + "#sha256=" + hash, nil
}

// HarnessNames returns the sorted list of all known harness template names,
// merging embedded scaffold harnesses with externally registered agents
// (e.g., ["code", "fix", "triage"]).
func HarnessNames() ([]string, error) {
	entries, err := fs.ReadDir(content, "fullsend-repo/harness")
	if err != nil {
		return nil, fmt.Errorf("reading embedded harness directory: %w", err)
	}
	seen := make(map[string]bool)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if name := e.Name(); strings.HasSuffix(name, ".yaml") {
			seen[strings.TrimSuffix(name, ".yaml")] = true
		}
	}
	for name := range externalAgents {
		seen[name] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
