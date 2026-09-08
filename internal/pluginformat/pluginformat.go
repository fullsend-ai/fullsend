// Package pluginformat decides which runtime loads a `plugins:` entry.
//
// A plugin directory belongs to one of two families (ADR 0094): a manifest
// bundle a runtime reads at startup (a Claude plugin, marked by plugin.json
// at its root or by Claude Code's own .claude-plugin/plugin.json), or
// a code module the runtime loads and executes (pi's `-e <dir>`
// extensions). One harness key lists both, so something has to tell them
// apart per entry — that is this package.
//
// It is a leaf: it imports neither internal/harness nor internal/runtime,
// because both import it (harness validates entries with it, the runtime
// filters the entries of its own kind with it).
package pluginformat

import (
	"fmt"
	"os"
	"path/filepath"
)

// Kind is the runtime family a plugin directory belongs to. The zero value
// is the undetected kind: Detect and DetectTree return it together with the
// problem string that says why neither family claimed the directory.
type Kind string

const (
	// KindClaude is a Claude Code plugin: a directory carrying one of the
	// claudeMarkerFiles, uploaded into the runtime's plugins/ directory.
	KindClaude Kind = "claude"
	// KindPi is a pi extension: a directory pi's `-e <dir>` loader resolves
	// an entry point in, uploaded and loaded as code.
	KindPi Kind = "pi"
)

// claudeMarkerFiles are the Claude-plugin markers, either of which claims
// a directory: plugin.json at the root is fullsend's own convention
// (fetchBasePlugin has always required it for a base plugin), and
// .claude-plugin/plugin.json is the manifest Claude Code itself defines
// (Codex reads that path too). Claude Code treats its manifest as
// optional; fullsend does not — a directory with neither marker is not a
// plugin any runtime here would load.
var claudeMarkerFiles = []string{"plugin.json", ".claude-plugin/plugin.json"}

// Detect reports the kind of a local plugin directory. The second return is
// empty on success and, when no family claims the directory, says why —
// both halves of the verdict, so the harness author does not have to guess
// which one was meant. The error is reserved for a directory that cannot be
// read or holds an entry no runtime may load (a symlink, a special file, a
// name the sandbox preflight could not reproduce).
//
// The Claude markers are checked first, and a directory that has one is
// never put through pi's rule: a Claude plugin that bundles a Node MCP
// server ships a package.json whose "main" resolves, which would otherwise
// make it look like a pi extension as well.
func Detect(dir string) (Kind, string, error) {
	// Lstat, not Stat: a marker that is itself a symlink is an entry the
	// scan and the upload rules refuse, so it must not claim the directory.
	for _, marker := range claudeMarkerFiles {
		info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(marker)))
		if err == nil && info.Mode().IsRegular() {
			return KindClaude, "", nil
		}
	}
	problem, err := piDirLoadProblem(dir)
	if err != nil {
		return "", "", err
	}
	if problem == "" {
		return KindPi, "", nil
	}
	return "", notAKindProblem(problem), nil
}

// DetectTree is Detect for a fetched tree (relative slash path → content),
// the form base composition and the forge fetchers work in. It applies the
// same precedence and returns the same verdict; a tree carries no symlinks
// or special files, so there is no error return.
func DetectTree(files map[string][]byte) (Kind, string) {
	for _, marker := range claudeMarkerFiles {
		if _, ok := files[marker]; ok {
			return KindClaude, ""
		}
	}
	if problem := PiTreeLoadProblem(files); problem != "" {
		return "", notAKindProblem(problem)
	}
	return KindPi, ""
}

// TreeEntriesProblem walks a plugin directory and reports the first entry
// no runtime may load — a symlink, a special file, a name the sandbox
// preflight could not reproduce (ExtensionEntryProblem) — or "" when the
// tree is clean. The pi detector applies the same rule as part of its
// own walk; a Claude plugin is claimed by its marker alone, so harness
// validation calls this for it separately. The rule is one for every
// kind: the upload would carry a symlink's target into the sandbox, and
// the injection scan (which refuses the same entries) only runs when
// security is enabled.
func TreeEntriesProblem(dir string) (string, error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	var problem string
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == root {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		if problem = ExtensionEntryProblem(filepath.ToSlash(rel), d.Type()); problem != "" {
			return filepath.SkipAll
		}
		return nil
	})
	return problem, err
}

func notAKindProblem(piProblem string) string {
	return fmt.Sprintf("not a Claude plugin (no plugin.json or .claude-plugin/plugin.json) and not a pi extension (%s)", piProblem)
}
