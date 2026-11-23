package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// This file holds the untrusted-tree half of the download sanitizer: the
// metadata walk, native-Git validation, and the SafeDownload orchestration
// that publishes a sanitized tree. The trusted-state round trip — capture
// before upload, restore after download — lives in snapshot.go.

// ErrRepositoryValidation marks a failure in post-download repository
// processing: sanitization, root validation, or shallow reconciliation after
// the download itself succeeded. It names the phase only and wraps the cause,
// so errors.Is and errors.As still report the underlying condition (for
// example context.DeadlineExceeded or an fs error). Transport, staging, and
// publication failures are not tagged.
var ErrRepositoryValidation = errors.New("repository validation failed")

// ErrRepositoryRejected marks a direct, deterministic content rejection: the
// same downloaded output cannot pass. Timeouts, Git query failures, and
// filesystem errors are never tagged.
var ErrRepositoryRejected = errors.New("repository rejected")

func rejectRepository(err error) error { return fmt.Errorf("%w: %w", ErrRepositoryRejected, err) }

// errSymlink wraps symlink-related os.Remove failures during
// sanitizeDownload with the symlink path and target, so error messages
// in cleanup reporting carry enough context to diagnose which symlink
// failed and why.
type errSymlink struct {
	Path   string // filesystem path of the symlink
	Target string // symlink target (empty if unreadable)
	Err    error  // underlying error (e.g., from os.Remove)
}

func (e *errSymlink) Error() string {
	if e.Target != "" {
		return fmt.Sprintf("symlink %s -> %s: %v", e.Path, e.Target, e.Err)
	}
	return fmt.Sprintf("symlink %s: %v", e.Path, e.Err)
}

func (e *errSymlink) Unwrap() error {
	return e.Err
}

// sanitizeDownload sanitizes one downloaded tree against the pre-upload snapshot.
func sanitizeDownload(localDir string, snapshot RepositorySnapshot) error {
	var trusted map[string]gitConfigFormat
	if snapshot.isCheckout && (snapshot.format != (gitConfigFormat{}) || len(snapshot.stores) > 0) {
		trusted = make(map[string]gitConfigFormat, len(snapshot.stores)+1)
		if snapshot.format != (gitConfigFormat{}) {
			trusted[".git"] = snapshot.format
		}
		for key, store := range snapshot.stores {
			trusted[key] = store.format
		}
	}
	absLocal, err := filepath.Abs(localDir)
	if err != nil {
		return err
	}
	absLocal, err = filepath.EvalSymlinks(absLocal)
	if err != nil {
		return err
	}

	var reftableDirs []string
	reftableSeen := make(map[string]bool)
	worktreeDirs := make(map[string]bool)
	configDirs := make(gitConfigDirs)
	// gitDirs records the git dirs Git actually reads: `.git` directories and
	// module stores with Git's own layout. A file named HEAD planted in info/,
	// refs/, or another metadata directory therefore cannot promote it.
	gitDirs := make(map[string]bool)
	var pendingGitFiles []string
	var bareDirs []string
	err = filepath.WalkDir(absLocal, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		inGit, rel := inGitDir(path, absLocal)
		folded := ""
		if inGit {
			folded = strings.ToLower(rel)
			if d.IsDir() && isGitDirRoot(path, absLocal, folded, gitDirs) {
				gitDirs[path] = true
			}
		}
		// A Git-shaped directory that is not a recognized git dir root is a
		// repository this walk never sanitized, wherever it sits: Git adopts
		// a directory with HEAD, objects, and refs alone, and its hooks and
		// executable config would run when a host command enters it. The
		// name matches case-insensitively, because a case-insensitive host
		// adopts a lowercase head too. Collection is deferred so gitfile
		// validation and the worktree rejection report their more specific
		// errors first.
		if strings.EqualFold(d.Name(), "HEAD") {
			shaped, shapeErr := headExposesGitDir(path, func(dir string) bool {
				return dir == absLocal || gitDirs[dir]
			})
			if shapeErr != nil {
				return shapeErr
			}
			if shaped {
				bareDirs = append(bareDirs, filepath.Dir(path))
			}
		}
		// Hooks and per-worktree config are executable or a second config
		// source wherever Git reads them: the root .git, every module store,
		// nested checkouts, and case variants. The legacy remotes/ and
		// branches/ files are config sources for remote names missing from
		// the rebuilt config. Remove the entry itself so a symlink or file
		// named like the directory cannot survive. Linked worktrees keep
		// their own copies under .git/worktrees/ and are rejected after the
		// walk instead.
		for _, name := range []string{"hooks", "config.worktree", "remotes", "branches"} {
			if inGit && isGitMetadataEntry(path, gitDirs, name) {
				if err := os.RemoveAll(path); err != nil {
					return fmt.Errorf("removing git metadata %s: %w", path, err)
				}
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		// A config is the one file whose executable keys host Git would honour;
		// it must be a regular file and is rebuilt after the walk.
		if inGit && isGitMetadataEntry(path, gitDirs, "config") {
			if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
				return rejectRepository(fmt.Errorf("git config %s is not a regular file", path))
			}
			configDirs[filepath.Dir(path)] = true
		}

		// Filter metadata that can redirect commit ancestry.
		if inGit {
			// Name path relative to the nearest git dir Git reads it from; the
			// outer .git-relative path in folded is the fallback.
			if root, ok := nearestGitDirRoot(path, absLocal, gitDirs); ok {
				if metadataPath, err := filepath.Rel(root, path); err == nil {
					if metadataPath == "." {
						folded = ""
					} else {
						folded = strings.ToLower(filepath.ToSlash(metadataPath))
					}
				}
			}
			// A submodule name may itself contain a .git component (Git only
			// rejects ".." in names). The directory would then act as a git
			// dir root that hides the store below it from every modules/
			// rule; real stores never contain one, so it fails closed.
			if strings.EqualFold(d.Name(), ".git") && strings.HasPrefix(strings.ToLower(filepath.ToSlash(rel)), "modules/") {
				return rejectRepository(fmt.Errorf("unsupported module store layout %s", path))
			}
			// An unrecognised directory under modules/ that Git could still
			// accept as a git dir keeps agent-controlled metadata this walk
			// never sanitizes: git adopts a store with HEAD plus objects and
			// refs alone, with no config, and derives store names from the
			// submodule path, so they may contain slashes. Recognized stores
			// are recorded in gitDirs before their children are visited and
			// re-fold this path, so a HEAD still named under modules/ belongs
			// to a store the walk did not recognize.
			if strings.EqualFold(d.Name(), "HEAD") && strings.HasPrefix(folded, "modules/") {
				return rejectRepository(fmt.Errorf("unsupported module store layout %s", filepath.Dir(path)))
			}
			// Reftable stores are verified natively after the walk, but their
			// on-disk shape is validated here first. Every entry beneath the
			// store must be a regular file: a directory, symlink, or special
			// file could redirect or block native Git.
			if folded == "reftable" {
				if d.Type()&fs.ModeSymlink != 0 || !d.IsDir() {
					return rejectRepository(fmt.Errorf("git reftable storage %s is not a real directory", path))
				}
				gitDir := filepath.Dir(path)
				if !reftableSeen[gitDir] {
					reftableSeen[gitDir] = true
					reftableDirs = append(reftableDirs, gitDir)
				}
				return nil
			}
			if strings.HasPrefix(folded, "reftable/") {
				if !d.Type().IsRegular() {
					return rejectRepository(fmt.Errorf("git reftable storage entry %s is not a regular file", path))
				}
				return nil
			}
			if folded == "worktrees" {
				worktreeDirs[filepath.Dir(path)] = true
			}
			switch folded {
			case "":
				// A symlinked .git hides metadata from this walk at any depth;
				// root gitfiles redirect it outside the inspected directory.
				if d.Type()&fs.ModeSymlink != 0 {
					return rejectRepository(fmt.Errorf("repository git metadata %s is a symlink", path))
				}
				if filepath.Dir(path) == absLocal && !d.IsDir() {
					return rejectRepository(fmt.Errorf("repository git metadata %s is not a real directory", path))
				}
				if !d.IsDir() {
					// A nested .git file is an initialized-submodule gitfile. Let
					// native Git resolve it and require the target inside the
					// owning checkout's module store, which this walk sanitizes.
					if !d.Type().IsRegular() {
						return rejectRepository(fmt.Errorf("repository git metadata %s is not a regular gitfile", path))
					}
					// Defer validation until the entire walk has collected
					// all gitDirs. A valid hidden-dir submodule worktree
					// `.config/dep/.git` sorts before `.git/modules`, so a
					// one-pass check would reject before the target store is
					// known. Deferred validation still runs before any later
					// native Git that could trust unvalidated config, and
					// unsafe gitfiles are still rejected after the walk.
					pendingGitFiles = append(pendingGitFiles, path)
				}
			case "commondir":
				return rejectRepository(fmt.Errorf("unsupported git metadata redirect %s", path))
			case "objects/info/alternates", "objects/info/http-alternates":
				// Alternate stores can carry unfiltered commit graphs.
				return rejectRepository(fmt.Errorf("unsupported git metadata alternate object store %s", path))
			case "refs", "info", "objects", "objects/info":
				// These containers must be real directories: Git follows
				// symlinks that the directory walker does not.
				if d.Type()&fs.ModeSymlink != 0 || !d.IsDir() {
					return rejectRepository(fmt.Errorf("git metadata container %s is not a real directory", path))
				}
			case "packed-refs":
				// WalkDir has already read a directory entry before the
				// callback; removing it here would make the walk read a
				// deleted path. Reject the layout instead.
				if d.IsDir() {
					return rejectRepository(fmt.Errorf("unsupported git metadata storage %s", path))
				}
				if !d.Type().IsRegular() {
					return removeGitMetadata(path)
				}
				return filterPackedRefs(path)
			case "shallow":
				// Every git dir's boundary is reconciled after the walk: the
				// root's and each trusted module store's against the trusted
				// host snapshot, and every other store's boundary is removed.
				// Defer the entry so the generic symlink handling below
				// cannot discard a supplied symlink before that validation
				// rejects it. The name matched case-insensitively, so a case
				// variant would defer too and skip the generic symlink
				// handling.
				if !d.Type().IsRegular() {
					return rejectRepository(fmt.Errorf("git shallow metadata %s is not a regular file", path))
				}
				return nil
			default:
				if ancestryMetadata(folded) {
					if err := removeGitMetadata(path); err != nil {
						return err
					}
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if _, rel := inGitDir(path, absLocal); rel != "" {
				rel = strings.ToLower(rel)
				if rel == "modules" || strings.HasPrefix(rel, "modules/") || strings.Contains(rel, "/modules/") {
					// A symlinked module store or ancestor would point Git at
					// config and hooks this walk never sanitizes.
					return rejectRepository(fmt.Errorf("module store path %s is a symlink", path))
				}
			}
			target, readErr := os.Readlink(path)
			if readErr != nil {
				if rmErr := os.Remove(path); rmErr != nil {
					return &errSymlink{Path: path, Err: rmErr}
				}
				return nil
			}
			// Absolute targets always point outside the repo root.
			if filepath.IsAbs(target) {
				if rmErr := os.Remove(path); rmErr != nil {
					return &errSymlink{Path: path, Target: target, Err: rmErr}
				}
				return nil
			}
			// Use EvalSymlinks, not filepath.Clean: Clean is textual and misses
			// chains where an in-repo dir-symlink is used as a component
			// (e.g. "sub/link/../../etc/passwd" cleans to inside the repo but
			// follows the link to outside). Fall back to remove on error
			// (dangling or looping).
			rawPath := filepath.Dir(path) + string(filepath.Separator) + target
			resolved, evalErr := filepath.EvalSymlinks(rawPath)
			if evalErr != nil {
				if rmErr := os.Remove(path); rmErr != nil {
					return &errSymlink{Path: path, Target: target, Err: rmErr}
				}
				return nil
			}
			if !strings.HasPrefix(resolved+string(filepath.Separator), absLocal+string(filepath.Separator)) {
				if rmErr := os.Remove(path); rmErr != nil {
					return &errSymlink{Path: path, Target: target, Err: rmErr}
				}
				return nil
			}
			return nil
		}

		// Remove AppleDouble (._*) files inside .git/ directories.
		// These are created by macOS bsdtar when archiving files with
		// extended attributes and corrupt git when extracted on Linux.
		if !d.IsDir() && strings.HasPrefix(d.Name(), "._") && inGit {
			return os.Remove(path)
		}

		return nil
	})
	if err != nil {
		return err
	}

	// Validate deferred nested gitfiles after the walk has collected all gitDirs.
	// A valid hidden-dir worktree sorts before its module store, so the target
	// is now known. Unsafe gitfiles are still rejected here before any later
	// native Git that could act on unvalidated config.
	for _, gitfile := range pendingGitFiles {
		if err := validateNestedGitFile(gitfile, absLocal, gitDirs); err != nil {
			return err
		}
	}

	// Fail closed if a trusted module store is still present on disk but was
	// not recognized and sanitized. A deleted config (or other layout change)
	// makes isSupportedModuleGitDir fail, so gitDirs would not contain the
	// store and its hooks/ancestry would survive unsanitized, yet
	// restoreTrustedStoreConfigs would later recreate the config. An empty or
	// otherwise modified layout is treated the same; a legitimately deleted
	// store (path absent) is preserved.
	if trusted != nil {
		for key := range trusted {
			if key == ".git" {
				continue
			}
			trustedPath := filepath.Join(absLocal, filepath.FromSlash(key))
			info, statErr := os.Lstat(trustedPath)
			if statErr != nil {
				if errors.Is(statErr, fs.ErrNotExist) {
					continue
				}
				return statErr
			}
			// Defense in depth: the walk also rejects symlinked module store
			// paths, but check the trusted path directly so the closure does
			// not depend on the walk having classified it.
			if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
				return rejectRepository(fmt.Errorf("trusted module store %s is not a real directory", trustedPath))
			}
			found := gitDirs[trustedPath]
			if !found {
				if dirInfo, err := os.Stat(trustedPath); err == nil {
					for dir := range gitDirs {
						if info2, err := os.Stat(dir); err == nil && os.SameFile(dirInfo, info2) {
							found = true
							break
						}
					}
				}
			}
			if !found {
				return rejectRepository(fmt.Errorf("trusted module store %s is present but was not sanitized as a git directory", trustedPath))
			}
		}
	}

	// A module store the snapshot does not name gets the root's rule: a
	// shallow boundary it returns is agent-chosen ancestry with no trusted
	// state to check against, so a non-empty marker is rejected rather than
	// stripped — stripping it would make truncated history look complete —
	// and an empty marker is removed. Trusted boundaries are restored later,
	// and a nested checkout keeps its boundary: its content round-trips as
	// ordinary worktree content, and a host-present one is rejected before
	// upload.
	for gitDir := range gitDirs {
		relKey, relErr := filepath.Rel(absLocal, gitDir)
		if relErr != nil {
			return relErr
		}
		relKey = filepath.ToSlash(relKey)
		if !strings.HasPrefix(strings.ToLower(relKey), ".git/modules/") {
			continue
		}
		if _, ok := trusted[relKey]; ok {
			continue
		}
		if err := restoreTrustedShallowAt(gitDir, shallowState{}); err != nil {
			return fmt.Errorf("reconciling module store %s: %w", relKey, err)
		}
	}

	// Reject registered worktrees in every git dir, files-based or reftable:
	// their private directories carry hooks, per-worktree config, and commondir
	// redirects that this walk does not sanitize.
	for gitDir := range gitDirs {
		if worktreeDirs[gitDir] {
			return rejectRepository(fmt.Errorf("unsupported git worktree metadata %s", filepath.Join(gitDir, "worktrees")))
		}
	}

	// Reject the Git-shaped directories the walk collected: an agent can
	// return a repository shape the walk never sanitized, whose executable
	// configuration would reach a host command that enters it.
	if len(bareDirs) > 0 {
		return rejectRepository(fmt.Errorf("unsupported bare-style Git directory %s", bareDirs[0]))
	}

	// Rebuild every collected config from its declared format alone before any
	// later native Git command reads the tree, so no downloaded include, hook
	// path, fsmonitor command, filter, or helper can run.
	if err := rebuildGitConfigs(absLocal, configDirs, trusted); err != nil {
		return err
	}

	// Only after the walk accepted the layout, verify and clean each store.
	// The staging parent, not the untrusted tree, is the command's cwd.
	cwd := filepath.Dir(absLocal)
	for _, gitDir := range reftableDirs {
		if err := sanitizeReftableStore(cwd, gitDir); err != nil {
			return err
		}
	}
	return nil
}

// inGitDir reports whether path is inside a .git directory and returns its path
// below the outermost .git. The .git entry itself returns true, "" so the
// root symlink check can reject it. Ordinary nested directory-form .git repos
// are filtered the same way; alternate layouts are not resolved.
func inGitDir(path, root string) (bool, string) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false, ""
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		if strings.EqualFold(part, ".git") {
			// Metadata comparisons use slash-separated paths on every
			// platform; filesystem paths stay native at call sites.
			return true, filepath.ToSlash(filepath.Join(parts[i+1:]...))
		}
	}
	return false, ""
}

// isGitDirRoot reports whether dir is a git dir whose direct metadata Git
// reads. `.git` directories qualify by name. A module store also needs Git's
// layout (HEAD, config, objects, refs) and must not sit inside another git
// dir's metadata directory, so a planted HEAD cannot hide real entries.
func isGitDirRoot(dir, absLocal, foldedRel string, gitDirs map[string]bool) bool {
	if strings.EqualFold(filepath.Base(dir), ".git") || gitDirs[dir] {
		return true
	}
	if !strings.HasPrefix(foldedRel, "modules/") || !isSupportedModuleGitDir(dir) {
		return false
	}
	if root, ok := nearestGitDirRoot(dir, absLocal, gitDirs); ok {
		if rel, err := filepath.Rel(root, dir); err == nil {
			rel = strings.ToLower(filepath.ToSlash(rel))
			// A nested git dir inside another store's metadata is not Git's
			// layout, so a planted HEAD there cannot hide real entries.
			for _, name := range []string{"refs", "logs", "info", "objects", "hooks", "worktrees", "reftable"} {
				if rel == name || strings.HasPrefix(rel, name+"/") {
					return false
				}
			}
		}
	}
	return true
}

// isSupportedModuleGitDir requires regular files and real directories for a module store.
func isSupportedModuleGitDir(dir string) bool {
	if !regularFile(filepath.Join(dir, "HEAD")) || !regularFile(filepath.Join(dir, "config")) {
		return false
	}
	for _, name := range []string{"objects", "refs"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.IsDir() {
			return false
		}
	}
	return true
}

// nearestGitDirRoot returns the nearest enclosing git dir root of path.
func nearestGitDirRoot(path, absLocal string, gitDirs map[string]bool) (string, bool) {
	for dir := filepath.Dir(path); inDir(dir, absLocal); dir = filepath.Dir(dir) {
		if gitDirs[dir] || strings.EqualFold(filepath.Base(dir), ".git") {
			return dir, true
		}
		if dir == absLocal {
			break
		}
	}
	return "", false
}

// isGitMetadataEntry reports whether path is an entry Git reads directly from a
// git dir root recorded by the walk (case-insensitively).
func isGitMetadataEntry(path string, gitDirs map[string]bool, name string) bool {
	if !strings.EqualFold(filepath.Base(path), name) {
		return false
	}
	dir := filepath.Dir(path)
	return gitDirs[dir] || strings.EqualFold(filepath.Base(dir), ".git")
}

// regularFile reports whether path is a regular file.
func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// inDir reports whether path is dir or inside it.
func inDir(path, dir string) bool {
	return path == dir || strings.HasPrefix(path+string(filepath.Separator), dir+string(filepath.Separator))
}

// owningCheckout returns the real .git directory of the checkout a gitfile
// belongs to: the nearest ancestor with a .git directory, skipping the gitfile
// itself.
func owningCheckout(gitfile, absLocal string) (string, error) {
	for dir := filepath.Dir(gitfile); inDir(dir, absLocal); dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, ".git")
		if candidate != gitfile {
			if info, err := os.Lstat(candidate); err == nil && info.IsDir() {
				return candidate, nil
			}
		}
		if dir == absLocal {
			break
		}
	}
	return "", fmt.Errorf("no owning git directory above %s", gitfile)
}

// resolveGitfileDir returns the git directory a gitfile redirects to: the
// raw path Git's own resolution opens, and that path with symlinks resolved.
// The capture scan and the download walk share it so both agree on where a
// gitfile points. cwd must be outside any untrusted tree.
func resolveGitfileDir(cwd, gitfile string) (raw, resolved string, err error) {
	out, err := runBoundedGit(cwd, "", nil, "rev-parse", "--resolve-git-dir", gitfile)
	if err != nil {
		return "", "", fmt.Errorf("gitfile %s is not a valid gitdir redirect: %w", gitfile, err)
	}
	raw = strings.TrimSuffix(string(out), "\n")
	resolved, err = filepath.EvalSymlinks(raw)
	if err != nil {
		return "", "", fmt.Errorf("gitfile %s target is not reachable: %w", gitfile, err)
	}
	return raw, resolved, nil
}

// headExposesGitDir reports whether a HEAD-named entry at path exposes a
// Git-shaped directory that excluded does not already account for: the
// parent directory is Git-shaped and is not excluded as a walk root or a
// recognized git dir. The capture scan and the download walk share it so
// their rules cannot drift.
func headExposesGitDir(path string, excluded func(dir string) bool) (bool, error) {
	dir := filepath.Dir(path)
	if excluded(dir) {
		return false, nil
	}
	return gitDirEntriesPresent(dir)
}

// validateNestedGitFile resolves a nested .git gitfile with native Git and
// requires the target to be a symlink-free path inside the owning checkout's
// module store, which this walk sanitizes. A redirect through a symlinked
// module container (or anywhere else) fails closed.
func validateNestedGitFile(gitfile, absLocal string, gitDirs map[string]bool) error {
	raw, resolved, err := resolveGitfileDir(filepath.Dir(absLocal), gitfile)
	if err != nil {
		return rejectRepository(err)
	}
	owning, err := owningCheckout(gitfile, absLocal)
	if err != nil {
		return rejectRepository(fmt.Errorf("gitfile %s: %w", gitfile, err))
	}
	modules := filepath.Join(owning, "modules")
	if info, err := os.Lstat(modules); err != nil || info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return rejectRepository(fmt.Errorf("gitfile %s: %s is not a real module store directory", gitfile, modules))
	}
	if !inDir(resolved, modules) {
		return rejectRepository(fmt.Errorf("gitfile %s redirects outside %s", gitfile, modules))
	}
	if resolved != filepath.Clean(raw) {
		return rejectRepository(fmt.Errorf("gitfile %s target redirects through a symlink", gitfile))
	}
	// Only a git dir this walk recorded as sanitized may be used: a target
	// nested inside another store's objects, or one without the layout a real
	// store has, is never inspected and must not be published. Compare by
	// identity, not path text: a case-insensitive filesystem can name the same
	// recorded directory with different casing, while a case-sensitive one
	// keeps distinct names distinct.
	resolvedInfo, err := os.Stat(resolved)
	if err != nil {
		return rejectRepository(fmt.Errorf("gitfile %s target %s cannot be inspected: %w", gitfile, resolved, err))
	}
	sanitized := false
	for dir := range gitDirs {
		if info, statErr := os.Stat(dir); statErr == nil && os.SameFile(resolvedInfo, info) {
			sanitized = true
			break
		}
	}
	if !sanitized {
		return rejectRepository(fmt.Errorf("gitfile %s target %s is not a sanitized git dir", gitfile, resolved))
	}
	return nil
}

// ancestryMetadata reports whether a lowercased .git-relative path holds an
// ancestry override: loose replacement refs, grafts, or commit-graph caches
// (the monolithic file and the split commit-graphs directory).
func ancestryMetadata(rel string) bool {
	rel = filepath.ToSlash(rel)
	if rel == "refs/replace" || rel == "info/grafts" {
		return true
	}
	return filepath.ToSlash(filepath.Dir(rel)) == "objects/info" && strings.HasPrefix(filepath.Base(rel), "commit-graph")
}

func removeGitMetadata(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("removing git metadata %s: %w", path, err)
	}
	return nil
}

// replaceRefRecord matches an ordinary packed-refs line in the refs/replace
// namespace. Go's POSIX [[:space:]] class is ASCII-only, so ref names that
// contain non-ASCII whitespace do not match the separator.
var replaceRefRecord = regexp.MustCompile(`^[^[:space:]]+[[:space:]]+refs/replace/`)

const (
	maxPackedRefsBytes = 32 << 20
	maxShallowBytes    = 4 << 20
)

// readBoundedFile reads up to limit+1 bytes to detect oversized files.
func readBoundedFile(path string, limit int64) ([]byte, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, false, err
	}
	return data, int64(len(data)) > limit, nil
}

// filterPackedRefs drops refs/replace lines and one immediately following
// peeled line, keeping every other line byte-for-byte. Matching is
// case-sensitive because Git ref names are. The rewrite uses a sibling
// temporary file so a hardlinked packed-refs target is replaced, not truncated.
func filterPackedRefs(path string) error {
	data, oversized, err := readBoundedFile(path, maxPackedRefsBytes)
	if err != nil {
		return fmt.Errorf("reading git metadata %s: %w", path, err)
	}
	if oversized {
		return rejectRepository(fmt.Errorf("git metadata %s is larger than %d bytes", path, maxPackedRefsBytes))
	}

	var out strings.Builder
	out.Grow(len(data))
	changed, droppingPeeled := false, false
	for _, line := range strings.SplitAfter(string(data), "\n") {
		if line == "" {
			continue
		}
		switch {
		case droppingPeeled && strings.HasPrefix(line, "^"):
			droppingPeeled = false
			changed = true
		case !strings.HasPrefix(line, "#") && replaceRefRecord.MatchString(line):
			droppingPeeled = true
			changed = true
		default:
			droppingPeeled = false
			out.WriteString(line)
		}
	}
	if !changed {
		return nil
	}
	return replaceFileContents(path, []byte(out.String()))
}

// replaceFileContents atomically replaces path via a sibling temporary file.
func replaceFileContents(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("rewriting git metadata %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("rewriting git metadata %s: %w", path, err)
	}
	tmpName := tmp.Name()
	err = tmp.Chmod(info.Mode().Perm())
	if err == nil {
		_, err = tmp.Write(data)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmpName, path)
	}
	if err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rewriting git metadata %s: %w", path, err)
	}
	return nil
}

// validateGitCheckout checks that tree is a real Git worktree root whose known
// .git directory resolves as itself, without modifying tree. It is read-only
// and intended for trusted host inputs before upload; downloads must pass
// sanitizeDownload first. It does not reject every unsupported repository
// format, and the query does not honor an alternate repository selected by the
// caller's inherited environment.
func validateGitCheckout(tree string) error {
	absTree, err := filepath.Abs(tree)
	if err != nil {
		return err
	}
	gitDir := filepath.Join(absTree, ".git")

	// The known .git must be a real directory: a gitfile or symlink points at
	// a layout this check does not inspect, so reject before the query can
	// follow it.
	info, err := os.Lstat(gitDir)
	if err != nil {
		return fmt.Errorf("checking git checkout %s: %w", absTree, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("unsupported git layout in %s: .git is not a real directory; use a normal .git checkout", absTree)
	}

	// The host Git is the layout oracle for the known .git; there is no
	// discovery fallback to a parent or bare root. The shared helper keeps
	// inherited selectors, config, and tracing out of the query and bounds it.
	// An empty helper gitDir leaves the layout query without a --git-dir.
	out, err := runBoundedGit(filepath.Dir(absTree), "", nil, "rev-parse", "--resolve-git-dir", gitDir)
	if err != nil {
		return fmt.Errorf("checking git checkout %s: %w", absTree, err)
	}
	// Only one trailing newline is removed; path whitespace is significant.
	if resolved := strings.TrimSuffix(string(out), "\n"); resolved != gitDir {
		return fmt.Errorf("checking git checkout %s: git directory resolved to %q", absTree, resolved)
	}

	// The repository config names the ref backend. Reftable needs Git 2.53's
	// strict ref verification, so it is gated on the native version; an
	// unknown backend is never assumed safe.
	storage, err := readRefStorage(filepath.Dir(absTree), gitDir)
	if err != nil {
		return fmt.Errorf("checking git checkout %s: %w", absTree, err)
	}
	switch storage {
	case "files":
	case "reftable":
		supported, err := nativeGitSupportsReftable(filepath.Dir(absTree))
		if err != nil {
			return fmt.Errorf("checking git checkout %s: %w", absTree, err)
		}
		if !supported {
			return fmt.Errorf("checking git checkout %s: reftable storage requires Git 2.53 or newer", absTree)
		}
	default:
		return fmt.Errorf("checking git checkout %s: unsupported git ref storage %q", absTree, storage)
	}
	return nil
}

// ValidateRepositoryRoot validates tree as a plain directory or a real .git
// checkout, reporting whether it is plain. A root with a .git entry keeps the
// strict checkout validation and is never treated as plain; plain roots are
// classified by entry presence without invoking Git.
// gitDirEntriesPresent reports whether dir's entries match Git's
// is_git_directory() layout by presence: a HEAD plus either a commondir
// redirect or both objects and refs. Entry names match case-insensitively,
// since a case-insensitive host adopts a lowercase head, and types are not
// checked, so unusual entries still fail closed. rev-parse is avoided
// because untrusted metadata can redirect or block reads.
func gitDirEntriesPresent(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	present := make(map[string]bool, 4)
	for _, entry := range entries {
		for _, name := range []string{"HEAD", "commondir", "objects", "refs"} {
			if strings.EqualFold(entry.Name(), name) {
				present[name] = true
			}
		}
	}
	return present["HEAD"] && (present["commondir"] || (present["objects"] && present["refs"])), nil
}

// ValidateRepositoryRoot reports whether tree is a plain directory (no Git
// metadata shape at its root) or a checkout whose root .git validates.
func ValidateRepositoryRoot(tree string) (plain bool, err error) {
	absTree, err := filepath.Abs(tree)
	if err != nil {
		return false, err
	}

	_, err = os.Lstat(filepath.Join(absTree, ".git"))
	switch {
	case err == nil:
		return false, validateGitCheckout(absTree)
	case !errors.Is(err, fs.ErrNotExist):
		return false, fmt.Errorf("checking git checkout %s: %w", absTree, err)
	}

	shaped, err := gitDirEntriesPresent(absTree)
	if err != nil {
		return false, fmt.Errorf("checking git checkout %s: inspecting root entries: %w", absTree, err)
	}
	if shaped {
		return false, rejectRepository(fmt.Errorf("checking git checkout %s: unsupported git layout: root has Git directory metadata but no .git directory; use a normal .git checkout or a plain directory", absTree))
	}
	return true, nil
}

// runBoundedGit runs one native Git command with isolated config and a fixed
// deadline, returning stdout only on success. A non-empty gitDir is passed as
// an absolute --git-dir, and cwd must be outside any untrusted tree.
func runBoundedGit(cwd, gitDir string, stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if gitDir != "" {
		args = append([]string{"--no-replace-objects", "--git-dir=" + gitDir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	// Inherited Git selectors and tracing are omitted, and global and system
	// config are disabled so repository config is the only config source.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"LC_ALL=C",
		"TMPDIR=" + os.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	}
	cmd.WaitDelay = time.Second
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("git %s: %w", gitSubcommand(args), ctxErr)
	}
	if runErr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("git %s: %w: %s", gitSubcommand(args), runErr, msg)
		}
		return nil, fmt.Errorf("git %s: %w", gitSubcommand(args), runErr)
	}
	return stdout.Bytes(), nil
}

// gitSubcommand returns the command name for error messages, skipping global
// options and the values of -c.
func gitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-c":
			i++
		case strings.HasPrefix(args[i], "-"):
		default:
			return args[i]
		}
	}
	return "git"
}

// readRefStorage returns the repository's declared ref backend. Local config
// is read without includes, and an absent key means the default files backend.
// Any other query failure is fatal rather than assumed to be files.
func readRefStorage(cwd, gitDir string) (string, error) {
	out, err := runBoundedGit(cwd, gitDir, nil, "config", "--no-includes", "--local", "--get", "extensions.refStorage")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "files", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// nativeGitSupportsReftable reports whether native Git meets the 2.53 minimum
// used for reftable verification. Only the major and minor version fields are
// parsed; an unrecognized version is an error, not a guess.
func nativeGitSupportsReftable(cwd string) (bool, error) {
	out, err := runBoundedGit(cwd, "", nil, "--version")
	if err != nil {
		return false, err
	}
	fields := strings.Fields(string(out))
	if len(fields) < 3 || fields[0] != "git" || fields[1] != "version" {
		return false, fmt.Errorf("unrecognized git version %q", strings.TrimSpace(string(out)))
	}
	parts := strings.SplitN(fields[2], ".", 3)
	if len(parts) < 2 {
		return false, fmt.Errorf("unrecognized git version %q", fields[2])
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false, fmt.Errorf("unrecognized git version %q", fields[2])
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false, fmt.Errorf("unrecognized git version %q", fields[2])
	}
	return major > 2 || (major == 2 && minor >= 53), nil
}

// sanitizeReftableStore removes replacement refs from one validated reftable
// store using native Git against a fresh trusted scratch repository with an
// empty template, so downloaded config and hooks cannot run. Only the
// already-validated flat reftable files are copied; the cleaned tables replace
// the download's store.
func sanitizeReftableStore(cwd, gitDir string) error {
	// A reftable's real HEAD lives in the copied tables, so the source HEAD
	// file is only a sentinel; reject a malformed one rather than proceed.
	head, err := os.Lstat(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return rejectRepository(fmt.Errorf("inspecting HEAD in %s: %w", gitDir, err))
		}
		return fmt.Errorf("inspecting HEAD in %s: %w", gitDir, err)
	}
	if !head.Mode().IsRegular() {
		return rejectRepository(fmt.Errorf("git HEAD %s is not a regular file", filepath.Join(gitDir, "HEAD")))
	}

	storage, err := readRefStorage(cwd, gitDir)
	if err != nil {
		return fmt.Errorf("reading reftable storage %s: %w", gitDir, err)
	}
	if storage != "reftable" {
		return rejectRepository(fmt.Errorf("git reftable storage %s is not declared as reftable", gitDir))
	}
	supported, err := nativeGitSupportsReftable(cwd)
	if err != nil {
		return fmt.Errorf("reading git version for %s: %w", gitDir, err)
	}
	if !supported {
		return fmt.Errorf("git reftable storage %s requires Git 2.53 or newer", gitDir)
	}
	format, err := readObjectFormat(cwd, gitDir)
	if err != nil {
		return fmt.Errorf("reading object format for %s: %w", gitDir, err)
	}

	scratch, err := os.MkdirTemp(cwd, ".fullsend-refs-*")
	if err != nil {
		return fmt.Errorf("creating trusted reftable store for %s: %w", gitDir, err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	if _, err := runBoundedGit(cwd, "", nil,
		"init", "--quiet", "--bare", "--template=", "--ref-format=reftable", "--object-format="+format, scratch); err != nil {
		return fmt.Errorf("creating trusted reftable store for %s: %w", gitDir, err)
	}

	scratchStore := filepath.Join(scratch, "reftable")
	if err := os.RemoveAll(scratchStore); err != nil {
		return fmt.Errorf("replacing trusted reftable store for %s: %w", gitDir, err)
	}
	// The sanitize walk already required every reftable entry to be a regular
	// file, so CopyFS cannot introduce a source symlink or special file.
	if err := os.CopyFS(scratchStore, os.DirFS(filepath.Join(gitDir, "reftable"))); err != nil {
		return fmt.Errorf("copying reftable store %s: %w", gitDir, err)
	}
	if err := removeReplacementRefs(cwd, scratch); err != nil {
		return fmt.Errorf("sanitizing reftable store %s: %w", gitDir, err)
	}
	if err := os.RemoveAll(filepath.Join(gitDir, "reftable")); err != nil {
		return fmt.Errorf("replacing reftable store %s: %w", gitDir, err)
	}
	if err := os.Rename(scratchStore, filepath.Join(gitDir, "reftable")); err != nil {
		return fmt.Errorf("replacing reftable store %s: %w", gitDir, err)
	}
	return nil
}

// readObjectFormat returns the repository's declared object format, defaulting
// to sha1 when local config does not declare one. Any other query failure or
// unsupported format is fatal.
func readObjectFormat(cwd, gitDir string) (string, error) {
	out, err := runBoundedGit(cwd, gitDir, nil, "config", "--no-includes", "--local", "--get", "extensions.objectFormat")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "sha1", nil
		}
		return "", err
	}
	switch format := strings.TrimSpace(string(out)); format {
	case "sha1", "sha256":
		return format, nil
	default:
		return "", fmt.Errorf("unsupported git object format %q", format)
	}
}

// removeReplacementRefs verifies and cleans a trusted scratch store without
// dereferencing symbolic refs. The symref-target override catches a finding
// that --strict alone leaves informational.
func removeReplacementRefs(cwd, gitDir string) error {
	if _, err := runBoundedGit(cwd, gitDir, nil,
		"-c", "fsck.symrefTargetIsNotARef=error", "refs", "verify", "--strict"); err != nil {
		return fmt.Errorf("verifying refs in %s: %w", gitDir, err)
	}

	listed, err := runBoundedGit(cwd, gitDir, nil, "replace", "--list", "--format=short")
	if err != nil {
		return fmt.Errorf("listing replacement refs in %s: %w", gitDir, err)
	}
	var batch []byte
	// Git terminates every listed suffix with one newline. Never trim the
	// suffix itself: bytes such as U+00A0 are legal in a ref name, and
	// normalizing them either skips a ref or deletes a different one.
	if listing := strings.TrimSuffix(string(listed), "\n"); listing != "" {
		for _, name := range strings.Split(listing, "\n") {
			if name == "" {
				return fmt.Errorf("unexpected empty replacement ref name in %s", gitDir)
			}
			// Pass raw suffixes over NUL-delimited stdin, not through a shell.
			// The final relist fails if replacement refs remain.
			batch = append(batch, "delete refs/replace/"...)
			batch = append(batch, name...)
			batch = append(batch, 0, 0)
		}
	}
	if len(batch) > 0 {
		if _, err := runBoundedGit(cwd, gitDir, batch, "update-ref", "--no-deref", "--stdin", "-z"); err != nil {
			return fmt.Errorf("removing replacement refs in %s: %w", gitDir, err)
		}
	}

	if _, err := runBoundedGit(cwd, gitDir, nil,
		"-c", "fsck.symrefTargetIsNotARef=error", "refs", "verify", "--strict"); err != nil {
		return fmt.Errorf("verifying cleaned refs in %s: %w", gitDir, err)
	}
	remain, err := runBoundedGit(cwd, gitDir, nil, "replace", "--list", "--format=short")
	if err != nil {
		return fmt.Errorf("relisting replacement refs in %s: %w", gitDir, err)
	}
	if len(remain) != 0 {
		return fmt.Errorf("replacement refs remain in %s", gitDir)
	}
	return nil
}

// sanitizeAndRestore makes one downloaded tree safe to publish. The order is
// the security property: sanitizeDownload removes dangerous symlinks, hooks,
// per-worktree config, and ancestry overrides, and rebuilds every git config
// from its declared format, before any host state is written into the tree;
// the root is then validated with native Git against trusted config only.
// A checkout input must come back a checkout, because a plain output could
// resolve Git metadata through a parent repository on later host use. The
// root .git/shallow is replaced with the state captured from the host before
// upload, so the sandbox cannot introduce or narrow that boundary.
func sanitizeAndRestore(stagingDir string, snapshot RepositorySnapshot) error {
	if err := sanitizeDownload(stagingDir, snapshot); err != nil {
		return err
	}
	if err := restoreTrustedConfig(stagingDir, snapshot); err != nil {
		return err
	}
	if err := restoreTrustedStoreConfigs(stagingDir, snapshot.stores); err != nil {
		return err
	}
	plain, err := ValidateRepositoryRoot(stagingDir)
	if err != nil {
		return err
	}
	if snapshot.isCheckout && plain {
		return rejectRepository(fmt.Errorf("downloaded directory %s is a plain directory, but the input repository was a git checkout; a checkout input must remain a git checkout", stagingDir))
	}
	return restoreTrustedShallow(stagingDir, snapshot.shallow)
}

// safeDownloadAttempts is the number of full download attempts
// SafeDownload makes into a fresh staging directory. OpenShell retries
// every tar-extraction failure internally, including deterministic
// failures, and directory retries unpack into the same destination. The
// outer attempt can recover only when the earlier failure was transient;
// a deterministic failure will repeat. Track NVIDIA/OpenShell#4805 and
// remove this workaround once upstream stages each directory retry.
const safeDownloadAttempts = 2

// SafeDownload copies a directory from a sandbox to the local machine and
// makes it safe to publish. Each attempt downloads into a fresh staging
// directory, runs sanitizeAndRestore on it, and replaces localDir only when
// that succeeds, so a partial extraction from a failed attempt cannot collide
// with a later one and the destination is left untouched on failure. Download
// failures are retried; a tree that fails sanitization or validation is
// rejected without a retry.
func SafeDownload(sandboxName, remoteDir, localDir string, snapshot RepositorySnapshot) error {
	localDir = filepath.Clean(localDir)
	parent := filepath.Dir(localDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("creating download parent %s: %w", parent, err)
	}

	var errs []error
	for attempt := 1; attempt <= safeDownloadAttempts; attempt++ {
		stagingDir, err := os.MkdirTemp(parent, ".fullsend-safe-download-*")
		if err != nil {
			return fmt.Errorf("creating download staging directory: %w", err)
		}

		if err := Download(sandboxName, remoteDir, stagingDir); err != nil {
			errs = append(errs, fmt.Errorf("attempt %d/%d: %w", attempt, safeDownloadAttempts, err))
			if cleanupErr := cleanupStagingDir(stagingDir); cleanupErr != nil {
				errs = append(errs, fmt.Errorf("cleaning staging directory after failed attempt: %w", cleanupErr))
			}
			continue
		}

		err = sanitizeAndRestore(stagingDir, snapshot)
		if err != nil {
			// One phase marker for everything above, before cleanup joins its
			// own errors: the cause keeps its identity via the second %w.
			err = fmt.Errorf("%w: %w", ErrRepositoryValidation, err)
			cleanupErr := cleanupStagingDir(stagingDir)
			if cleanupErr == nil {
				return fmt.Errorf("sanitizing downloaded directory: %w", err)
			}
			return errors.Join(
				fmt.Errorf("sanitizing downloaded directory: %w", err),
				fmt.Errorf("cleaning staging directory after sanitization failure: %w", cleanupErr),
			)
		}
		if err := replaceDownload(stagingDir, localDir); err != nil {
			cleanupErr := cleanupStagingDir(stagingDir)
			if cleanupErr == nil {
				return err
			}
			return errors.Join(err, fmt.Errorf("cleaning staging directory after publish failure: %w", cleanupErr))
		}
		fmt.Fprintf(os.Stderr, "  sandbox download succeeded on attempt %d/%d\n", attempt, safeDownloadAttempts)
		return nil
	}

	return fmt.Errorf("safe download from sandbox failed after %d attempts: %w", safeDownloadAttempts, errors.Join(errs...))
}

// cleanupStagingDir removes a staging tree after restoring write permission
// to its directories. Permission restoration is best effort and does not
// follow symlinks; os.RemoveAll provides the authoritative cleanup result.
func cleanupStagingDir(stagingDir string) error {
	_ = filepath.WalkDir(stagingDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			parent := filepath.Dir(path)
			if info, err := os.Lstat(parent); err == nil && info.IsDir() {
				_ = os.Chmod(parent, 0o755)
			}
			return nil
		}
		if entry.IsDir() {
			_ = os.Chmod(path, 0o755)
		}
		return nil
	})
	if err := os.RemoveAll(stagingDir); err != nil {
		return fmt.Errorf("removing staging directory %s: %w", stagingDir, err)
	}
	return nil
}

// replaceDownload publishes a sanitized staging directory at localDir by
// removing the existing destination and renaming the staged tree. The staging
// directory is created in localDir's parent, so the rename stays on the same
// filesystem; replacement is not failure-preserving.
func replaceDownload(stagingDir, localDir string) error {
	if err := os.RemoveAll(localDir); err != nil {
		return fmt.Errorf("replacing existing download directory %s: %w", localDir, err)
	}
	if err := os.Rename(stagingDir, localDir); err != nil {
		return fmt.Errorf("publishing downloaded directory to %s: %w", localDir, err)
	}
	return nil
}
