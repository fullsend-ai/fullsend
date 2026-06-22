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

var (
	validHarnessName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	validCommitSHA   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// HarnessBaseURL returns the raw.githubusercontent.com URL for a scaffold
// harness template at a specific commit SHA. The URL does not include an
// integrity hash fragment — use HarnessBaseURLWithHash for that.
func HarnessBaseURL(harnessName, commitSHA string) (string, error) {
	if !validHarnessName.MatchString(harnessName) {
		return "", fmt.Errorf("invalid harness name %q: must match %s", harnessName, validHarnessName.String())
	}
	if !validCommitSHA.MatchString(commitSHA) {
		return "", fmt.Errorf("invalid commit SHA %q: must be a 40-character lowercase hex string", commitSHA)
	}
	return harnessBaseURLPrefix + commitSHA + "/" + harnessURLPath + harnessName + ".yaml", nil
}

// HarnessContentHash returns the SHA-256 hex digest of the embedded scaffold
// harness template. This hash matches what raw.githubusercontent.com serves
// for the release commit the CLI was built from.
func HarnessContentHash(harnessName string) (string, error) {
	if !validHarnessName.MatchString(harnessName) {
		return "", fmt.Errorf("invalid harness name %q: must match %s", harnessName, validHarnessName.String())
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

// HarnessNames returns the sorted list of harness template names
// available in the embedded scaffold (e.g., ["code", "fix", "triage"]).
func HarnessNames() ([]string, error) {
	entries, err := fs.ReadDir(content, "fullsend-repo/harness")
	if err != nil {
		return nil, fmt.Errorf("reading embedded harness directory: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if name := e.Name(); strings.HasSuffix(name, ".yaml") {
			names = append(names, strings.TrimSuffix(name, ".yaml"))
		}
	}
	sort.Strings(names)
	return names, nil
}
