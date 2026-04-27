package scaffold

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

//go:embed all:fullsend-repo
var content embed.FS

// optionalAgents lists agent roles whose scaffold files are NOT installed
// unless the role is explicitly present in the org's configured roles.
// Core agents (triage, code, review) are always installed.
var optionalAgents = map[string]bool{
	"scribe": true,
}

// IsOptionalAgent reports whether the given role is an optional agent.
func IsOptionalAgent(role string) bool {
	return optionalAgents[role]
}

// FullsendRepoFile returns the content of a file from the fullsend-repo scaffold.
// The path is relative to the fullsend-repo root (e.g., ".github/workflows/triage.yml").
func FullsendRepoFile(path string) ([]byte, error) {
	return content.ReadFile("fullsend-repo/" + path)
}

// WalkFullsendRepo calls fn for each file in the fullsend-repo scaffold.
// Paths passed to fn are relative to the fullsend-repo root.
// This includes ALL files — core and optional agents alike.
func WalkFullsendRepo(fn func(path string, content []byte) error) error {
	return walkScaffold(nil, fn)
}

// WalkFullsendRepoForRoles calls fn for each scaffold file that should be
// installed for the given set of roles. Files belonging to optional agents
// (e.g. scribe) are skipped unless that role appears in roles.
// Pass nil to include everything (same as WalkFullsendRepo).
func WalkFullsendRepoForRoles(roles []string, fn func(path string, content []byte) error) error {
	if roles == nil {
		return walkScaffold(nil, fn)
	}
	roleSet := make(map[string]bool, len(roles))
	for _, r := range roles {
		roleSet[r] = true
	}
	return walkScaffold(roleSet, fn)
}

// isOptionalAgentFile checks whether path belongs to an optional agent
// that is NOT in the provided roleSet. Shared files are never skipped.
func isOptionalAgentFile(path string, roleSet map[string]bool) bool {
	if roleSet == nil {
		return false
	}
	for agent := range optionalAgents {
		if fileMatchesAgent(path, agent) && !roleSet[agent] {
			return true
		}
	}
	return false
}

// fileMatchesAgent returns true if the scaffold path is specific to the
// named agent. It checks known per-agent directory conventions.
func fileMatchesAgent(path string, agent string) bool {
	segments := []string{
		"agents/" + agent + ".md",
		"env/" + agent + ".env",
		".github/workflows/" + agent + ".yml",
		"harness/" + agent + ".yaml",
		"policies/" + agent + ".yaml",
		"schemas/" + agent + "-result.schema.json",
		"scripts/pre-" + agent + ".sh",
		"scripts/post-" + agent + ".sh",
	}
	for _, s := range segments {
		if path == s {
			return true
		}
	}
	return strings.HasPrefix(path, "skills/"+agent+"/") ||
		strings.HasPrefix(path, "skills/"+agent+"-")
}

func walkScaffold(roleSet map[string]bool, fn func(path string, content []byte) error) error {
	return fs.WalkDir(content, "fullsend-repo", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Strip the "fullsend-repo/" prefix so callers get repo-relative paths.
		relPath := path[len("fullsend-repo/"):]
		if isOptionalAgentFile(relPath, roleSet) {
			return nil
		}
		data, readErr := content.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
		return fn(relPath, data)
	})
}
