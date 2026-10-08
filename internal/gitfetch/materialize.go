package gitfetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const (
	// maxLinkDepth bounds how many symlinked directories may be nested
	// inside one another during materialization.
	maxLinkDepth = 16
	// maxDirVisits bounds the directories a materializing walk enters, so
	// directory links that fan out without holding files cannot make the
	// walk run long before the file limits trip.
	maxDirVisits = 10 * MaxFiles
	// localCacheDirName is fullsend's resource cache directory, which sits
	// inside .fullsend/ and is never part of a definition tree.
	localCacheDirName = ".fullsend-cache"
	// capFix is the fix the file-count and size limit errors name.
	capFix = "point source at a sub-directory that holds only the plugin, or move large files out of it"
)

// errTooLarge reports a file that does not fit the remaining size budget.
var errTooLarge = errors.New("file exceeds the remaining size budget")

func sizeLimitError() error {
	return fmt.Errorf("total content size exceeds %d MiB limit; %s", maxTotalMiB, capFix)
}

// LinkError reports a symlink that materialization refuses. Path is the
// link's slash path relative to the tree root and Target the link text as
// written in the repository.
type LinkError struct {
	Path    string
	Target  string
	Problem string
}

func (e *LinkError) Error() string {
	return fmt.Sprintf("%s is a symlink to %s, which %s", e.Path, e.Target, e.Problem)
}

// RepoToplevel returns the top-level directory of the git work tree that
// holds dir, as `git rev-parse --show-toplevel` reports it (a real path).
func RepoToplevel(ctx context.Context, dir string) (string, error) {
	out, err := localGit(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return "", fmt.Errorf("git rev-parse --show-toplevel in %q printed nothing", dir)
	}
	return top, nil
}

// ReadLocalTree reads the files git tracks under source, a slash path
// relative to repoRoot ("." for the root), into a relative slash path →
// content map. Only paths in git's index are read, with their working-tree
// content, so untracked and ignored files, nested repositories and
// submodule contents are never part of the tree; a tracked file missing
// from the working tree is an error. Paths under any of the exclude slash
// paths (relative to repoRoot) are left out. Tracked symlinks follow the
// rules of FetchTreeWithOptions with MaterializeSymlinks, with the tracked
// files as the tree: a link to an untracked file dangles. The MaxFiles and
// total-size limits apply. The result hashes and caches exactly like a
// fetched tree.
//
// git runs with explicit arguments, no shell, system config disabled and
// core.fsmonitor off, so repository config cannot make it run a command;
// rev-parse and ls-files run no hooks.
func ReadLocalTree(ctx context.Context, repoRoot, source string, exclude []string) (map[string][]byte, error) {
	realRoot, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		return nil, err
	}
	source = path.Clean(source)
	srcRoot, err := sourceRoot(realRoot, source)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%q does not exist in %q", source, realRoot)
	}
	if err != nil {
		return nil, err
	}
	tracked, err := trackedPaths(ctx, realRoot, source, exclude)
	if err != nil {
		return nil, err
	}
	if len(tracked) == 0 {
		return nil, fmt.Errorf("%q holds no files git tracks; commit or `git add` the definition's files", source)
	}

	// Copy the tracked entries into a scratch tree and materialize that:
	// a link to anything git does not track then dangles, and every link
	// rule is the one fetched trees get.
	scratch, err := os.MkdirTemp("", "fullsend-localtree-*")
	if err != nil {
		return nil, fmt.Errorf("creating scratch directory: %w", err)
	}
	defer os.RemoveAll(scratch)
	if err := copyTracked(realRoot, srcRoot, scratch, tracked); err != nil {
		return nil, err
	}
	files, err := materializeTree(scratch, "the source directory", []string{".git", localCacheDirName})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%q holds no files git tracks; commit or `git add` the definition's files", source)
	}
	return files, nil
}

// trackedPaths lists the index entries under source with
// `git ls-files --cached --stage` and returns their paths relative to
// source, sorted. Gitlinks (submodules) are skipped, as are paths under
// exclude and paths with a .git or .fullsend-cache segment.
func trackedPaths(ctx context.Context, realRoot, source string, exclude []string) ([]string, error) {
	out, err := localGit(ctx, realRoot, "ls-files", "-z", "--cached", "--stage", "--", source)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var paths []string
	for _, rec := range strings.Split(string(out), "\x00") {
		if rec == "" {
			continue
		}
		meta, full, ok := strings.Cut(rec, "\t")
		if !ok {
			return nil, fmt.Errorf("unexpected git ls-files output %q", rec)
		}
		if strings.HasPrefix(meta, "160000 ") {
			continue
		}
		rel := full
		if source != "." {
			var under bool
			rel, under = strings.CutPrefix(full, source+"/")
			if !under {
				continue
			}
		}
		if seen[rel] || underAny(full, exclude) || hasSegment(rel, ".git", localCacheDirName) {
			continue
		}
		seen[rel] = true
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	return paths, nil
}

// copyTracked copies each tracked path from srcRoot into dst: a regular
// file's content, or a symlink's link text. The MaxFiles and size limits
// apply as the copy runs. Regular files are opened through an os.Root on
// srcRoot (see readTrackedFile).
func copyTracked(repoRoot, srcRoot, dst string, tracked []string) error {
	if len(tracked) > MaxFiles {
		return fmt.Errorf("directory listing exceeded maximum of %d files; %s", MaxFiles, capFix)
	}
	// Open the source through the checkout's root, so a source directory
	// swapped for a symlink after sourceRoot checked it cannot lead the
	// reads out of the checkout.
	top, err := os.OpenRoot(repoRoot)
	if err != nil {
		return err
	}
	defer top.Close()
	rel, err := filepath.Rel(repoRoot, srcRoot)
	if err != nil {
		return err
	}
	root, err := top.OpenRoot(rel)
	if err != nil {
		return fmt.Errorf("opening the source inside the checkout: %w; run again once the working tree is settled", err)
	}
	defer root.Close()
	realDirs := map[string]bool{srcRoot: true}
	var total int64
	for _, rel := range tracked {
		src := filepath.Join(srcRoot, filepath.FromSlash(rel))
		// A tracked path never sits below a symlink in git's index, but
		// the working tree may have replaced a directory with one since.
		parent := filepath.Dir(src)
		if !realDirs[parent] {
			real, err := filepath.EvalSymlinks(parent)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if err == nil && real != parent {
				return fmt.Errorf("%q is tracked by git, but a directory on its path is a symlink in the working tree; restore the directory", rel)
			}
			realDirs[parent] = err == nil
		}
		info, err := os.Lstat(src)
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%q is tracked by git but missing from the working tree; restore it or `git rm` it", rel)
		}
		if err != nil {
			return err
		}
		out := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			return err
		}
		switch mode := info.Mode(); {
		case mode&os.ModeSymlink != 0:
			target, err := os.Readlink(src)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, out); err != nil {
				return err
			}
		case mode.IsRegular():
			data, err := readTrackedFile(root, rel, info, maxTotal-total)
			if errors.Is(err, errTooLarge) {
				return sizeLimitError()
			}
			if err != nil {
				return fmt.Errorf("reading %q: %w", rel, err)
			}
			total += int64(len(data))
			if err := os.WriteFile(out, data, 0o600); err != nil {
				return err
			}
		case mode.IsDir():
			return fmt.Errorf("%q is tracked by git as a file but is a directory in the working tree; restore it", rel)
		default:
			return fmt.Errorf("unsupported file type at %q", rel)
		}
	}
	return nil
}

// sourceRoot returns the real path of the directory subpath (a slash path,
// "" or "." for the root) inside checkoutRoot. No component of subpath
// may be a symlink (ADR 0130 rule 2), and the result must be inside the
// checkout and not inside .git. A missing component returns an error
// that wraps fs.ErrNotExist.
func sourceRoot(checkoutRoot, subpath string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(checkoutRoot)
	if err != nil {
		return "", err
	}
	cur := realRoot
	if subpath != "" && subpath != "." {
		segs := strings.Split(path.Clean(subpath), "/")
		for i, seg := range segs {
			cur = filepath.Join(cur, seg)
			rel := strings.Join(segs[:i+1], "/")
			info, err := os.Lstat(cur)
			if err != nil {
				return "", err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("%q is a symlink in the repository; point source at the real directory", rel)
			}
			if !info.IsDir() {
				return "", fmt.Errorf("%q is not a directory in the repository; point source at a directory", rel)
			}
		}
	}
	real, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return "", err
	}
	if !containsPath(realRoot, real) {
		return "", fmt.Errorf("%q resolves outside the repository; point source at a directory inside it", subpath)
	}
	if rel, relErr := filepath.Rel(realRoot, real); relErr == nil && hasSegment(filepath.ToSlash(rel), ".git") {
		return "", fmt.Errorf("%q is inside .git/, which is never part of a definition; point source at a directory in the working tree", subpath)
	}
	return real, nil
}

// localGit runs git in dir with explicit arguments and no shell. System
// config is not read, core.fsmonitor (a repository-config command hook)
// is forced off, optional index locks are skipped and pathspecs are
// literal. Repository-location variables inherited from a caller's git
// hook (GIT_DIR, GIT_INDEX_FILE, ...) are dropped so dir decides the
// repository. stderr is returned in the error verbatim.
func localGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	full := append([]string{"-c", "core.fsmonitor=false", "-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	env := make([]string, 0, len(os.Environ())+4)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if repoLocationEnv[name] {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_LITERAL_PATHSPECS=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("git %s: %w: %s", args[0], err, msg)
		}
		return nil, fmt.Errorf("git %s: %w", args[0], err)
	}
	return stdout.Bytes(), nil
}

var repoLocationEnv = map[string]bool{
	"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_COMMON_DIR": true,
	"GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_NAMESPACE": true,
	"GIT_PREFIX": true, "GIT_LITERAL_PATHSPECS": true, "GIT_GLOB_PATHSPECS": true,
	"GIT_NOGLOB_PATHSPECS": true, "GIT_ICASE_PATHSPECS": true,
}

// underAny reports whether slash path p is one of prefixes or below one.
func underAny(p string, prefixes []string) bool {
	for _, pre := range prefixes {
		if p == pre || strings.HasPrefix(p, pre+"/") {
			return true
		}
	}
	return false
}

// hasSegment reports whether slash path p has a segment in names.
func hasSegment(p string, names ...string) bool {
	for _, seg := range strings.Split(p, "/") {
		for _, n := range names {
			if seg == n {
				return true
			}
		}
	}
	return false
}

// treeWalker materializes one tree. root is the walk root with its own
// symlinks resolved, so containment checks compare real paths (on macOS
// the temp directory itself sits behind /var -> /private/var).
type treeWalker struct {
	root      string
	treeLabel string
	skip      map[string]bool
	files     map[string][]byte
	total     int64
	dirVisits int
}

// materializeTree reads walkRoot, which the caller has checked with
// sourceRoot (or created itself), into a path → content map.
func materializeTree(walkRoot, treeLabel string, skip []string) (map[string][]byte, error) {
	root, err := filepath.EvalSymlinks(walkRoot)
	if err != nil {
		return nil, err
	}
	w := &treeWalker{
		root:      root,
		treeLabel: treeLabel,
		skip:      make(map[string]bool, len(skip)),
		files:     make(map[string][]byte),
	}
	for _, name := range skip {
		w.skip[name] = true
	}
	chain := map[string]bool{root: true}
	if err := w.walkDir(root, "", chain, 0); err != nil {
		return nil, err
	}
	return w.files, nil
}

// walkDir walks the real directory realDir, whose entries are stored under
// the slash prefix rel. chain holds the real directories on the current
// path, so a directory link back to one of them is reported as a loop.
func (w *treeWalker) walkDir(realDir, rel string, chain map[string]bool, linkDepth int) error {
	w.dirVisits++
	if w.dirVisits > maxDirVisits {
		return fmt.Errorf("walked more than %d directories; symlinked directories multiply %s past what can be uploaded; replace some of the directory links with the files", maxDirVisits, w.treeLabel)
	}
	entries, err := os.ReadDir(realDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if w.skip[name] {
			continue
		}
		p := filepath.Join(realDir, name)
		r := path.Join(rel, name)
		switch mode := e.Type(); {
		case mode.IsDir():
			chain[p] = true
			err := w.walkDir(p, r, chain, linkDepth)
			delete(chain, p)
			if err != nil {
				return err
			}
		case mode&os.ModeSymlink != 0:
			if err := w.link(p, r, chain, linkDepth); err != nil {
				return err
			}
		case mode.IsRegular():
			if err := w.addFile(r, p); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported file type at %q", r)
		}
	}
	return nil
}

// link materializes the symlink at real path p (tree path r).
func (w *treeWalker) link(p, r string, chain map[string]bool, linkDepth int) error {
	raw, err := os.Readlink(p)
	if err != nil {
		return err
	}
	linkErr := func(problem string) error {
		return &LinkError{Path: r, Target: raw, Problem: problem}
	}
	leaves := "leaves " + w.treeLabel + "; point the link inside the repository or commit the file"
	// An absolute target names a path on the machine that checked the
	// tree out, never a path inside it, so it is refused even when it
	// happens to resolve inside the walk root.
	if filepath.IsAbs(raw) {
		return linkErr("is an absolute path, so it " + leaves)
	}
	// Check the link text before resolving it: under sparse checkout a
	// target outside the fetched sub-directory is not on disk at all, and
	// "leaves the tree" is the useful verdict, not "does not exist".
	lexical := filepath.Join(filepath.Dir(p), raw)
	if !w.within(lexical) {
		return linkErr(leaves)
	}
	// Check the skipped names in the link text too: a local tree is read
	// from a copy without them, where such a link would only dangle.
	if err := w.skippedTarget(lexical, linkErr); err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return linkErr("does not exist; commit the target or remove the link")
		case isLinkLoop(err):
			return linkErr("cannot be resolved because the links form a loop; remove one of them")
		default:
			return linkErr(fmt.Sprintf("cannot be resolved (%v); fix the link or remove it", err))
		}
	}
	if !w.within(real) {
		return linkErr(leaves)
	}
	if err := w.skippedTarget(real, linkErr); err != nil {
		return err
	}
	info, err := os.Stat(real)
	if err != nil {
		return err
	}
	switch {
	case info.Mode().IsRegular():
		return w.addFile(r, real)
	case info.IsDir():
		if chain[real] {
			return linkErr("is a directory that contains the link, so the tree would never end; remove the link or point it at a file")
		}
		if linkDepth+1 > maxLinkDepth {
			return linkErr(fmt.Sprintf("is nested more than %d symlinked directories deep; replace some of the links with the files", maxLinkDepth))
		}
		chain[real] = true
		err := w.walkDir(real, r, chain, linkDepth+1)
		delete(chain, real)
		return err
	default:
		return fmt.Errorf("unsupported file type at %q (target of the symlink %q)", r, raw)
	}
}

// skippedTarget refuses a link whose target p (inside w.root) has a
// skipped segment such as .git.
func (w *treeWalker) skippedTarget(p string, linkErr func(string) error) error {
	rel, err := filepath.Rel(w.root, p)
	if err != nil {
		return nil
	}
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if w.skip[seg] {
			return linkErr(fmt.Sprintf("points into %s/, which is never part of %s; remove the link", seg, w.treeLabel))
		}
	}
	return nil
}

// isLinkLoop reports whether a filepath.EvalSymlinks error means the links
// form a loop: ELOOP from the system, or the walker's own hop limit.
func isLinkLoop(err error) bool {
	return errors.Is(err, syscall.ELOOP) || strings.Contains(err.Error(), "too many links")
}

// within reports whether p is w.root or below it. The comparison uses
// filepath.Rel on absolute paths rather than a string prefix, so a root of
// /tmp/a does not admit /tmp/ab.
func (w *treeWalker) within(p string) bool {
	return containsPath(w.root, p)
}

// containsPath reports whether path p is root or inside it. Both are made
// absolute and cleaned; the caller resolves symlinks first when it means
// real paths.
func containsPath(root, p string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absP, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absP)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// validOutputPath reports whether rel is a clean, relative, slash-separated
// tree path with no "..", NUL or backslash, as the cache and the sandbox
// upload require.
func validOutputPath(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.ContainsAny(rel, "\\\x00") {
		return false
	}
	if path.Clean(rel) != rel {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." || seg == "." {
			return false
		}
	}
	return true
}

func (w *treeWalker) addFile(rel, realPath string) error {
	if !validOutputPath(rel) {
		return fmt.Errorf("%q is not a path the tree can hold (it must be relative, clean, without .., NUL or backslash); rename the file", rel)
	}
	if len(w.files) >= MaxFiles {
		return fmt.Errorf("directory listing exceeded maximum of %d files; %s", MaxFiles, capFix)
	}
	data, err := readBounded(realPath, maxTotal-w.total)
	if errors.Is(err, errTooLarge) {
		return sizeLimitError()
	}
	if err != nil {
		return fmt.Errorf("reading %q: %w", rel, err)
	}
	w.total += int64(len(data))
	w.files[rel] = data
	return nil
}

// readTrackedFile reads the tracked file rel (a slash path) through root,
// which refuses a path that leaves the root. The opened descriptor must be
// a regular file and the file the caller's Lstat returned as checked, so a
// path replaced after the check is refused. The read is bounded by limit,
// as in readBounded.
func readTrackedFile(root *os.Root, rel string, checked os.FileInfo, limit int64) ([]byte, error) {
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(info, checked) {
		return nil, fmt.Errorf("%q changed while it was read; run again once the working tree is settled", rel)
	}
	return readBoundedFile(f, info, limit)
}

// readBounded reads the regular file p when it holds at most limit bytes
// and returns errTooLarge otherwise, without reading more than limit+1
// bytes: the size is checked before the read, and the read is capped in
// case the file grows in between.
func readBounded(p string, limit int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return readBoundedFile(f, info, limit)
}

// readBoundedFile is readBounded for an open file f whose fstat is info.
func readBoundedFile(f *os.File, info os.FileInfo, limit int64) ([]byte, error) {
	if info.Size() > limit {
		return nil, errTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errTooLarge
	}
	return data, nil
}
