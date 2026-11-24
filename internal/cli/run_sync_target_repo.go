package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// targetRepoSyncError ends an otherwise passing run whose requested target
// repository synchronization was skipped, failed, or blocked. The post-script
// gate still allows post_script to run (unless suppressed by a transcript
// error override) while runAgent returns this non-nil error.
type targetRepoSyncError struct {
	message string
}

func (e *targetRepoSyncError) Error() string {
	return e.message
}

// syncOutputExcludeRel returns the relative path from hostRepositoryDir to outputBase
// when outputBase is safely contained inside hostRepositoryDir (including multi-segment
// paths such as "build/output" or ".fullsend/runs"). Returns ok=false when outputBase
// is outside the repo, identical to the repo root, or invalid.
func syncOutputExcludeRel(hostRepositoryDir, outputBase string) (string, bool) {
	if hostRepositoryDir == "" || outputBase == "" {
		return "", false
	}
	absRepo, err := filepath.Abs(hostRepositoryDir)
	if err != nil {
		return "", false
	}
	absOut, err := filepath.Abs(outputBase)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(absRepo, absOut)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		return "", false
	}
	return filepath.Clean(rel), true
}

// syncChanges tracks relative paths added, modified, or deleted during target
// repository synchronization so callers can audit every write (including
// gitignored paths that do not appear in plain `git status` / `git diff`).
type syncChanges struct {
	Added    []string
	Modified []string
	Deleted  []string
}

func (c *syncChanges) formatManifest() string {
	if c == nil || (len(c.Added) == 0 && len(c.Modified) == 0 && len(c.Deleted) == 0) {
		return "# no changes\n"
	}
	var b strings.Builder
	writeGroup := func(prefix string, paths []string) {
		sorted := append([]string(nil), paths...)
		sort.Strings(sorted)
		for _, p := range sorted {
			b.WriteString(prefix)
			b.WriteByte(' ')
			b.WriteString(p)
			b.WriteByte('\n')
		}
	}
	writeGroup("A", c.Added)
	writeGroup("M", c.Modified)
	writeGroup("D", c.Deleted)
	return b.String()
}

func (c *syncChanges) summary() string {
	if c == nil {
		return "0 added, 0 modified, 0 deleted"
	}
	return fmt.Sprintf("%d added, %d modified, %d deleted", len(c.Added), len(c.Modified), len(c.Deleted))
}

func (c *syncChanges) hasChanges() bool {
	return c != nil && (len(c.Added) > 0 || len(c.Modified) > 0 || len(c.Deleted) > 0)
}

// targetRepoSyncResult captures the outcome of step 9g synchronization for Results
// display, download-directory retention, and terminal error propagation.
type targetRepoSyncResult struct {
	status              string
	changesPath         string
	preserveDownloadDir bool
	err                 error
}

const maxContextFileSize int64 = 1 << 20 // 1 MB

type repoContextFinding struct {
	relPath string
	finding security.Finding
}

type contextFindingKey struct {
	Scanner  string
	Name     string
	Severity string
	Detail   string
	Position int
}

func makeContextFindingKey(f security.Finding) contextFindingKey {
	return contextFindingKey{
		Scanner:  f.Scanner,
		Name:     f.Name,
		Severity: f.Severity,
		Detail:   f.Detail,
		Position: f.Position,
	}
}

// diffNewContextFindings returns findings present in current that were not
// already present in baseline (matched as a multiset by finding fields).
// file_too_large and scan_error findings from added/modified files are never
// suppressed by baseline entries because the sandbox bytes changed and remain
// unverified.
func diffNewContextFindings(current []repoContextFinding, baseline []security.Finding) []security.Finding {
	if len(current) == 0 {
		return nil
	}
	baseCounts := make(map[contextFindingKey]int, len(baseline))
	for _, f := range baseline {
		baseCounts[makeContextFindingKey(f)]++
	}
	var added []security.Finding
	for _, cf := range current {
		f := cf.finding
		if f.Name == "file_too_large" || f.Name == "scan_error" {
			added = append(added, f)
			continue
		}
		k := makeContextFindingKey(f)
		if baseCounts[k] > 0 {
			baseCounts[k]--
			continue
		}
		added = append(added, f)
	}
	return added
}

// filterExcludedContextFindings drops findings whose repo-relative path is
// excluded from synchronization (for example, runner-injected bridge files or
// the output dir). Matches on the structured relPath field rather than parsing
// Finding.Detail so directory names containing ": " cannot bypass exclusion
// checks.
func filterExcludedContextFindings(findings []repoContextFinding, excludes []string) []repoContextFinding {
	if len(findings) == 0 {
		return findings
	}
	filtered := make([]repoContextFinding, 0, len(findings))
	for _, f := range findings {
		if f.relPath != "" && isExcludedPath(f.relPath, excludes) {
			continue
		}
		filtered = append(filtered, f)
	}
	return filtered
}

func countPreSyncBlockingFindings(findings []security.Finding) (criticalCount, unscannableCount int) {
	for _, f := range findings {
		if f.Severity == "critical" {
			criticalCount++
		}
		if f.Name == "file_too_large" || f.Name == "scan_error" {
			unscannableCount++
		}
	}
	return criticalCount, unscannableCount
}

func newContextScanErrorFinding(relPath, severity, detail string) repoContextFinding {
	return repoContextFinding{
		relPath: relPath,
		finding: security.Finding{
			Scanner:  "context_injection",
			Name:     "scan_error",
			Severity: severity,
			Detail:   detail,
			Position: -1,
		},
	}
}

// scanSyncChangedContextFiles scans every added or modified context file in
// downloadDir that would be synchronized to targetDir, with no nesting depth
// limit and without skipping directories such as node_modules, vendor, .venv,
// or __pycache__. Unchanged entries matching preRunManifest are skipped because
// sync will not write them.
func scanSyncChangedContextFiles(downloadDir, targetDir string, excludes []string, preRunManifest map[string]string) []repoContextFinding {
	absDownload, err := filepath.Abs(downloadDir)
	if err != nil {
		return []repoContextFinding{newContextScanErrorFinding("", "high", fmt.Sprintf("could not resolve download directory: %v", err))}
	}
	absDownloadResolved := absDownload
	if resolved, evalErr := filepath.EvalSymlinks(absDownload); evalErr == nil {
		absDownloadResolved = resolved
	}
	absTarget := targetDir
	if abs, absErr := filepath.Abs(targetDir); absErr == nil {
		absTarget = abs
	}
	absTargetResolved := absTarget
	if resolved, evalErr := filepath.EvalSymlinks(absTarget); evalErr == nil {
		absTargetResolved = resolved
	}
	dstCaseInsensitive := detectCaseInsensitiveDir(absTarget)
	preRunLowerMap := buildLowerManifestMap(preRunManifest)

	pipeline := security.InputPipeline()
	var allFindings []repoContextFinding

	walkErr := filepath.WalkDir(absDownload, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			relPath := relOrAbs(absDownload, path)
			if relPath != "." && isExcludedPath(relPath, excludes) {
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			allFindings = append(allFindings, newContextScanErrorFinding(relPath, "medium", fmt.Sprintf("could not access %s: %v", relPath, err)))
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(absDownload, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		if isExcludedPath(rel, excludes) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !security.ShouldScan(stripHFSIgnorable(d.Name())) {
			return nil
		}

		manifestKey, hasCaseRename := resolveManifestKeyAndCaseRename(absDownload, rel, dstCaseInsensitive, preRunManifest, preRunLowerMap)
		baseCaseChanged := hasCaseRename && filepath.Base(manifestKey) != filepath.Base(rel)

		if d.Type()&os.ModeSymlink != 0 {
			linkTarget, resolvedTarget, safe := resolveSafeInRepoSymlink(path, absDownloadResolved)
			if !safe {
				return nil
			}
			fi, statErr := os.Stat(resolvedTarget)
			if statErr != nil {
				allFindings = append(allFindings, newContextScanErrorFinding(rel, "medium", fmt.Sprintf("%s: could not stat symlink target: %v", rel, statErr)))
				return nil
			}
			if !fi.Mode().IsRegular() {
				return nil
			}
			linkUnchanged := !baseCaseChanged && isSandboxEntryUnchanged(preRunManifest, manifestKey, "symlink:"+linkTarget)
			if linkUnchanged {
				targetRel, tRelErr := filepath.Rel(absDownloadResolved, resolvedTarget)
				if tRelErr == nil && targetRel != "." && targetRel != ".." && !strings.HasPrefix(targetRel, ".."+string(filepath.Separator)) {
					// Verify the host pre-run symlink resolved to the same relative target
					// path so retargeting an intermediate symlink in a chain cannot bypass
					// the pre-sync scan.
					_, preRunResolved, preRunSafe := resolveSafeInRepoSymlink(filepath.Join(absTarget, manifestKey), absTargetResolved)
					preRunTargetRel, preRelErr := filepath.Rel(absTargetResolved, preRunResolved)
					sameTargetPath := preRunSafe && preRelErr == nil &&
						(preRunTargetRel == targetRel || (dstCaseInsensitive && strings.EqualFold(preRunTargetRel, targetRel)))
					if sameTargetPath {
						tKey, tCaseRename := resolveManifestKeyAndCaseRename(absDownload, targetRel, dstCaseInsensitive, preRunManifest, preRunLowerMap)
						tBaseChanged := tCaseRename && filepath.Base(tKey) != filepath.Base(targetRel)
						if !tBaseChanged && preRunManifest != nil {
							if _, existed := preRunManifest[tKey]; existed {
								targetDigest, tdErr := computeHostEntryDigest(resolvedTarget, fi)
								if tdErr != nil {
									allFindings = append(allFindings, newContextScanErrorFinding(rel, "medium", fmt.Sprintf("%s: could not read symlink target: %v", rel, tdErr)))
									return nil
								}
								if isSandboxEntryUnchanged(preRunManifest, tKey, targetDigest) {
									return nil
								}
							}
						}
					}
				}
			}
			allFindings = append(allFindings, scanSingleContextFile(resolvedTarget, rel, fi, pipeline)...)
			return nil
		}

		if !d.Type().IsRegular() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			allFindings = append(allFindings, newContextScanErrorFinding(rel, "medium", fmt.Sprintf("%s: could not stat file: %v", rel, infoErr)))
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if !baseCaseChanged && preRunManifest != nil {
			if _, existed := preRunManifest[manifestKey]; existed {
				srcDigest, digestErr := computeHostEntryDigest(path, info)
				if digestErr != nil {
					allFindings = append(allFindings, newContextScanErrorFinding(rel, "medium", fmt.Sprintf("%s: could not read file: %v", rel, digestErr)))
					return nil
				}
				if isSandboxEntryUnchanged(preRunManifest, manifestKey, srcDigest) {
					return nil
				}
			}
		}
		allFindings = append(allFindings, scanSingleContextFile(path, rel, info, pipeline)...)
		return nil
	})
	if walkErr != nil {
		allFindings = append(allFindings, newContextScanErrorFinding("", "high", fmt.Sprintf("walk terminated: %v", walkErr)))
	}
	return allFindings
}

func isSandboxEntryUnchanged(preRunManifest map[string]string, manifestKey, srcDigest string) bool {
	if preRunManifest == nil {
		return false
	}
	recorded, ok := preRunManifest[manifestKey]
	return ok && normalizeManifestDigest(recorded) == srcDigest
}

// syncTargetRepoDirFn is a package seam so tests can inject sync failures
// without modifying the host filesystem permissions or failing upstream steps.
var syncTargetRepoDirFn = syncTargetRepoDirWithReport

// syncGateInputs holds the run-outcome fields evaluated to decide whether
// target repository synchronization may proceed.
type syncGateInputs struct {
	Enabled                bool
	RepoExtractedOK        bool
	HasValidationLoop      bool
	ValidationPassed       bool
	InlineValidationPassed bool
	TimedOut               bool
	TranscriptErr          bool
	ExitCode               int
	ExitReason             string
	ValidatedIter          int
	RunCount               int
}

// hasTerminalRunError reports whether runTerminalError will return a non-nil
// error for these run outcomes.
func (g syncGateInputs) hasTerminalRunError() bool {
	return runTerminalError(g.HasValidationLoop, g.ValidationPassed, g.TimedOut, g.ExitCode, g.ExitReason, g.RunCount, 0, 0) != nil
}

// targetRepoSyncInput bundles the gate state, paths, security settings, and
// callbacks for runTargetRepoSync.
type targetRepoSyncInput struct {
	Gate                   syncGateInputs
	DownloadDir            string
	TargetDir              string
	OutputBase             string
	RunDir                 string
	RunnerInjectedExcludes []string
	PreRunManifest         map[string]string
	StrippedSymlinks       []string
	SecurityEnabled        bool
	FailModeClosed         bool
	PreRunContextFindings  []security.Finding
	Printer                *ui.Printer
	SyncFn                 func(srcDir, dstDir string, excludes []string, preRunManifest map[string]string, strippedSymlinks []string, printer *ui.Printer) (*syncChanges, error)
}

// syncTargetRepoSkipReason evaluates whether target repo sync should proceed,
// aligning its terminal outcome check with runTerminalError and returning a
// human-readable skip reason when synchronization is bypassed.
func syncTargetRepoSkipReason(g syncGateInputs) (bool, string) {
	if !g.Enabled {
		return false, ""
	}
	if g.TranscriptErr {
		return false, "transcript error"
	}
	if g.HasValidationLoop {
		if !g.ValidationPassed {
			if g.TimedOut {
				return false, "timed out"
			}
			return false, "validation did not pass"
		}
		if !g.InlineValidationPassed {
			return false, "validation passed only in post-loop sweep without TARGET_REPO_DIR"
		}
		if g.ValidatedIter > 0 && g.ValidatedIter != g.RunCount {
			return false, fmt.Sprintf("validated iteration %d is not the last downloaded iteration %d", g.ValidatedIter, g.RunCount)
		}
	} else {
		if g.TimedOut {
			return false, "timed out"
		}
		if g.ExitCode != 0 && !(g.ExitCode > 0 && isBehavioralExitSubtype(g.ExitReason)) {
			return false, fmt.Sprintf("agent exited with code %d", g.ExitCode)
		}
	}
	if !g.RepoExtractedOK {
		return false, "repository extraction failed"
	}
	return true, ""
}

// runTargetRepoSync executes step 9g synchronization when requested and gated
// OK, returning the status, synced-files manifest path, download-directory
// retention flag, and any *targetRepoSyncError for runAgent to surface after
// post_script runs.
func runTargetRepoSync(in targetRepoSyncInput) targetRepoSyncResult {
	if !in.Gate.Enabled {
		return targetRepoSyncResult{}
	}
	shouldSync, skipReason := syncTargetRepoSkipReason(in.Gate)
	if !shouldSync {
		in.Printer.StepWarn("Skipping target repository synchronization: " + skipReason)
		res := targetRepoSyncResult{status: fmt.Sprintf("skipped (%s)", skipReason)}
		if !in.Gate.hasTerminalRunError() {
			res.preserveDownloadDir = in.Gate.RepoExtractedOK
			res.err = &targetRepoSyncError{message: "target repository synchronization skipped: " + skipReason}
		}
		return res
	}
	if in.PreRunManifest == nil {
		const reason = "pre-run manifest snapshot unavailable"
		in.Printer.StepWarn("Skipping target repository synchronization: " + reason)
		return targetRepoSyncResult{
			status:              fmt.Sprintf("skipped (%s)", reason),
			preserveDownloadDir: in.Gate.RepoExtractedOK,
			err:                 &targetRepoSyncError{message: "target repository synchronization skipped: " + reason},
		}
	}

	syncExcludes := []string{".git"}
	if rel, ok := syncOutputExcludeRel(in.TargetDir, in.OutputBase); ok {
		syncExcludes = append(syncExcludes, rel)
	}
	syncExcludes = append(syncExcludes, in.RunnerInjectedExcludes...)

	// Scan added/modified downloaded context files BEFORE writing anything into
	// targetDir so fail_mode: closed blocks sync before untrusted or unscannable
	// context files hit disk.
	if in.SecurityEnabled {
		downloadFindings := filterExcludedContextFindings(
			scanSyncChangedContextFiles(in.DownloadDir, in.TargetDir, syncExcludes, in.PreRunManifest),
			syncExcludes,
		)
		newFindings := diffNewContextFindings(downloadFindings, in.PreRunContextFindings)
		critCount, unscannableCount := countPreSyncBlockingFindings(newFindings)
		if critCount > 0 || unscannableCount > 0 {
			if in.FailModeClosed {
				var status, stepMsg, errMsg string
				switch {
				case critCount > 0 && unscannableCount > 0:
					status = "blocked (critical injection findings and unscannable downloaded context files)"
					stepMsg = fmt.Sprintf("BLOCKED: %d new critical injection finding(s) and %d unscannable context file(s) in downloaded context files; skipping target repository sync", critCount, unscannableCount)
					errMsg = fmt.Sprintf("target repository synchronization blocked: %d new critical injection finding(s) and %d unscannable context file(s) in downloaded context files", critCount, unscannableCount)
				case critCount > 0:
					status = "blocked (critical injection findings in downloaded context files)"
					stepMsg = fmt.Sprintf("BLOCKED: %d new critical injection finding(s) in downloaded context files; skipping target repository sync", critCount)
					errMsg = fmt.Sprintf("target repository synchronization blocked: %d new critical injection finding(s) in downloaded context files", critCount)
				default:
					status = "blocked (unscannable downloaded context files)"
					stepMsg = fmt.Sprintf("BLOCKED: %d unscannable downloaded context file(s) (oversized or unreadable); skipping target repository sync", unscannableCount)
					errMsg = fmt.Sprintf("target repository synchronization blocked: %d unscannable downloaded context file(s) (oversized or unreadable)", unscannableCount)
				}
				in.Printer.StepFail(stepMsg)
				return targetRepoSyncResult{
					status:              status,
					preserveDownloadDir: in.Gate.RepoExtractedOK,
					err:                 &targetRepoSyncError{message: errMsg},
				}
			}
			switch {
			case critCount > 0 && unscannableCount > 0:
				in.Printer.StepWarn(fmt.Sprintf("Pre-sync security scan: %d new critical finding(s) and %d unscannable downloaded context file(s) (oversized or unreadable, fail_mode: open); inspect before building or running tools", critCount, unscannableCount))
			case critCount > 0:
				in.Printer.StepWarn(fmt.Sprintf("Pre-sync security scan: %d new critical finding(s) in downloaded context files (fail_mode: open); inspect before building or running tools", critCount))
			default:
				in.Printer.StepWarn(fmt.Sprintf("Pre-sync security scan: %d unscannable downloaded context file(s) (oversized or unreadable, fail_mode: open); inspect before building or running tools", unscannableCount))
			}
		} else if len(newFindings) > 0 {
			in.Printer.StepWarn(fmt.Sprintf("Pre-sync security scan: %d new finding(s) in downloaded context files", len(newFindings)))
		}
	}

	syncStart := time.Now()
	in.Printer.StepStart("Synchronizing sandbox changes to target repository")
	changes, err := in.SyncFn(in.DownloadDir, in.TargetDir, syncExcludes, in.PreRunManifest, in.StrippedSymlinks, in.Printer)
	if err != nil {
		in.Printer.StepWarn("Target repository synchronization failed: " + err.Error())
		res := targetRepoSyncResult{
			status:              fmt.Sprintf("failed (%v)", err),
			preserveDownloadDir: in.Gate.RepoExtractedOK,
			err:                 &targetRepoSyncError{message: fmt.Sprintf("target repository synchronization failed: %v", err)},
		}
		if in.RunDir != "" && changes.hasChanges() {
			manifestPath := filepath.Join(in.RunDir, "synced-files.txt")
			singleLineErr := strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(err.Error())
			header := fmt.Sprintf("# sync aborted: %s; partial changes below\n", singleLineErr)
			if writeErr := os.WriteFile(manifestPath, []byte(header+changes.formatManifest()), 0o644); writeErr != nil {
				in.Printer.StepWarn("Could not write partial synced files manifest: " + writeErr.Error())
			} else {
				res.changesPath = fmt.Sprintf("%s (%s; partial — sync aborted)", manifestPath, changes.summary())
			}
		}
		return res
	}
	in.Printer.StepDone(fmt.Sprintf("Target repository synchronized to %s (%s, %.1fs)", in.TargetDir, changes.summary(), time.Since(syncStart).Seconds()))

	res := targetRepoSyncResult{
		status: fmt.Sprintf("synchronized to %s", in.TargetDir),
	}
	if in.RunDir != "" {
		manifestPath := filepath.Join(in.RunDir, "synced-files.txt")
		if writeErr := os.WriteFile(manifestPath, []byte(changes.formatManifest()), 0o644); writeErr != nil {
			in.Printer.StepWarn("Could not write synced files manifest: " + writeErr.Error())
		} else {
			res.changesPath = fmt.Sprintf("%s (%s)", manifestPath, changes.summary())
		}
	}
	return res
}

// recordTargetRepoManifest snapshots all relative paths inside repoDir (excluding
// any .git subtree and excluded paths, matched case-insensitively) before sandbox
// upload so sync can detect deletions, preserve host-only entries and pre-existing
// unsafe symlinks, and avoid overwriting concurrent host edits.
func recordTargetRepoManifest(repoDir string, excludes ...string) (map[string]string, error) {
	absRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, err
	}
	absRepoResolved := absRepo
	if resolved, evalErr := filepath.EvalSymlinks(absRepo); evalErr == nil {
		absRepoResolved = resolved
	}
	manifest := make(map[string]string)
	err = filepath.WalkDir(absRepo, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path != absRepo && (errors.Is(walkErr, os.ErrPermission) || errors.Is(walkErr, os.ErrNotExist)) {
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			return walkErr
		}
		rel, err := filepath.Rel(absRepo, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if isExcludedPath(rel, excludes) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			if errors.Is(infoErr, os.ErrPermission) || errors.Is(infoErr, os.ErrNotExist) {
				return nil
			}
			return infoErr
		}
		digest, digestErr := computeHostEntryDigest(path, info)
		if digestErr != nil {
			if errors.Is(digestErr, os.ErrPermission) || errors.Is(digestErr, os.ErrNotExist) {
				return nil
			}
			return digestErr
		}
		if d.Type()&os.ModeSymlink != 0 && strings.HasPrefix(digest, "symlink:") &&
			(wouldSanitizeStripSymlink(path, absRepoResolved) || symlinkResolvesIntoExcludedPath(path, absRepoResolved, excludes)) {
			digest = "symlink-stripped:" + strings.TrimPrefix(digest, "symlink:")
		}
		manifest[rel] = digest
		return nil
	})
	if err != nil {
		return nil, err
	}
	return manifest, nil
}

func normalizeManifestDigest(digest string) string {
	if strings.HasPrefix(digest, "symlink-stripped:") {
		return "symlink:" + strings.TrimPrefix(digest, "symlink-stripped:")
	}
	return digest
}

func computeHostEntryDigest(path string, info os.FileInfo) (string, error) {
	if info.IsDir() {
		return "dir", nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		return "symlink:" + target, nil
	}
	if info.Mode().IsRegular() {
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return "", err
		}
		return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
	}
	return "other", nil
}

func hasConcurrentHostEdit(targetPath, rel string, dstInfo os.FileInfo, preRunManifest map[string]string) (bool, error) {
	if preRunManifest == nil {
		return false, nil
	}
	recorded, ok := preRunManifest[rel]
	if !ok {
		return true, nil
	}
	curDigest, err := computeHostEntryDigest(targetPath, dstInfo)
	if err != nil {
		return false, err
	}
	return curDigest != normalizeManifestDigest(recorded), nil
}

func checkCanReplaceHostTarget(targetPath, rel string, preRunManifest map[string]string) error {
	fi, err := os.Lstat(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.IsDir() {
		if preRunManifest != nil {
			return filepath.WalkDir(targetPath, func(subPath string, d os.DirEntry, walkErr error) error {
				if walkErr != nil {
					if os.IsNotExist(walkErr) {
						return nil
					}
					return walkErr
				}
				if subPath == targetPath {
					return nil
				}
				sub, relErr := filepath.Rel(targetPath, subPath)
				if relErr != nil {
					return relErr
				}
				subRel := filepath.Join(rel, sub)
				info, infoErr := d.Info()
				if infoErr != nil {
					if os.IsNotExist(infoErr) {
						return nil
					}
					return infoErr
				}
				changed, chkErr := hasConcurrentHostEdit(subPath, subRel, info, preRunManifest)
				if chkErr != nil {
					return chkErr
				}
				if changed {
					return fmt.Errorf("refusing to replace directory %q containing host-only or concurrently modified entry %q", rel, subRel)
				}
				return nil
			})
		}
		return nil
	}
	changed, err := hasConcurrentHostEdit(targetPath, rel, fi, preRunManifest)
	if err != nil {
		return err
	}
	if changed {
		return fmt.Errorf("refusing to replace concurrently modified host entry %q", rel)
	}
	return nil
}

func checkPathNotUnderResolvedGit(absDstResolved, targetPath, rel string) error {
	probe := targetPath
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			resolvedPath := resolved
			if len(suffix) > 0 {
				resolvedPath = filepath.Join(append([]string{resolved}, suffix...)...)
			}
			relFromRoot, relErr := filepath.Rel(absDstResolved, resolvedPath)
			if relErr == nil && relFromRoot != "." && relFromRoot != ".." && !strings.HasPrefix(relFromRoot, ".."+string(filepath.Separator)) {
				if hasGitPathSegment(relFromRoot) {
					return fmt.Errorf("refusing to access resolved path inside protected .git directory for %q", rel)
				}
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return nil
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return nil
		}
		suffix = append([]string{filepath.Base(probe)}, suffix...)
		probe = parent
	}
}

// resolveSafeInRepoSymlink resolves linkPath relative to absRootResolved and
// reports whether the symlink would survive sandbox.sanitizeDownload (readable,
// relative, non-dangling/non-looping, and resolving inside absRootResolved).
func resolveSafeInRepoSymlink(linkPath, absRootResolved string) (linkTarget, resolvedTarget string, safe bool) {
	target, readErr := os.Readlink(linkPath)
	if readErr != nil || filepath.IsAbs(target) {
		return "", "", false
	}
	rawPath := filepath.Dir(linkPath) + string(filepath.Separator) + target
	resolved, evalErr := filepath.EvalSymlinks(rawPath)
	if evalErr != nil {
		return "", "", false
	}
	if !strings.HasPrefix(resolved+string(filepath.Separator), absRootResolved+string(filepath.Separator)) {
		return "", "", false
	}
	return target, resolved, true
}

// wouldSanitizeStripSymlink reports whether linkPath would have been removed by
// sandbox.sanitizeDownload (unreadable link, absolute target, dangling/looping
// target, or target resolving outside absRootResolved).
func wouldSanitizeStripSymlink(linkPath, absRootResolved string) bool {
	_, _, safe := resolveSafeInRepoSymlink(linkPath, absRootResolved)
	return !safe
}

// symlinkResolvesIntoExcludedPath reports whether a relative symlink inside
// absRootResolved resolves to a path matched by excludes (such as an excluded
// output directory omitted from sandbox upload, which causes the link to dangle
// in the sandbox and be stripped by sandbox.sanitizeDownload).
func symlinkResolvesIntoExcludedPath(linkPath, absRootResolved string, excludes []string) bool {
	if len(excludes) == 0 {
		return false
	}
	_, resolved, safe := resolveSafeInRepoSymlink(linkPath, absRootResolved)
	if !safe {
		return false
	}
	relTarget, relErr := filepath.Rel(absRootResolved, resolved)
	if relErr != nil || relTarget == "." || relTarget == ".." || strings.HasPrefix(relTarget, ".."+string(filepath.Separator)) {
		return false
	}
	for _, ex := range excludes {
		eq, sub := hasPathPrefixFold(relTarget, ex)
		if eq || sub {
			return true
		}
	}
	return false
}

func hasMultipleLinks(fi os.FileInfo) bool {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Nlink > 1
	}
	return false
}

type dirPermEntry struct {
	path string
	perm os.FileMode
}

type pruneDirEntry struct {
	path     string
	origPerm os.FileMode
	chmodded bool
}

func flipASCIICase(s string) (string, bool) {
	b := []byte(s)
	flipped := false
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z':
			b[i] = c - ('a' - 'A')
			flipped = true
		case c >= 'A' && c <= 'Z':
			b[i] = c + ('a' - 'A')
			flipped = true
		}
	}
	if !flipped {
		return s, false
	}
	return string(b), true
}

func detectCaseInsensitiveDir(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err == nil {
		names := make(map[string]bool, len(entries))
		for _, entry := range entries {
			names[entry.Name()] = true
		}
		for _, entry := range entries {
			flipped, ok := flipASCIICase(entry.Name())
			if !ok || names[flipped] {
				continue
			}
			origInfo, origErr := os.Lstat(filepath.Join(dir, entry.Name()))
			flipInfo, flipErr := os.Lstat(filepath.Join(dir, flipped))
			if origErr == nil {
				return flipErr == nil && os.SameFile(origInfo, flipInfo)
			}
		}
	}
	tmp, tmpErr := os.CreateTemp(dir, ".fullsend-case-probe-*")
	if tmpErr != nil {
		return false
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()
	flipped, ok := flipASCIICase(filepath.Base(tmpPath))
	if !ok {
		return false
	}
	origInfo, origErr := os.Lstat(tmpPath)
	flipInfo, flipErr := os.Lstat(filepath.Join(dir, flipped))
	return origErr == nil && flipErr == nil && os.SameFile(origInfo, flipInfo)
}

func hasExactRelPath(rootDir, rel string) bool {
	cur := rootDir
	for _, seg := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		if seg == "" || seg == "." {
			continue
		}
		entries, err := os.ReadDir(cur)
		if err != nil {
			return false
		}
		matched := false
		for _, entry := range entries {
			if entry.Name() == seg {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
		cur = filepath.Join(cur, seg)
	}
	return true
}

func buildLowerManifestMap(preRunManifest map[string]string) map[string]string {
	if len(preRunManifest) == 0 {
		return nil
	}
	lowerMap := make(map[string]string, len(preRunManifest))
	ambiguous := make(map[string]bool)
	for k := range preRunManifest {
		lk := strings.ToLower(k)
		if ambiguous[lk] {
			continue
		}
		if existing, seen := lowerMap[lk]; seen && existing != k {
			delete(lowerMap, lk)
			ambiguous[lk] = true
			continue
		}
		lowerMap[lk] = k
	}
	return lowerMap
}

func resolveManifestKeyAndCaseRename(absSrc, rel string, dstCaseInsensitive bool, preRunManifest, preRunLowerMap map[string]string) (string, bool) {
	if !dstCaseInsensitive || preRunManifest == nil {
		return rel, false
	}
	if _, exact := preRunManifest[rel]; exact {
		return rel, false
	}
	origRel, ok := preRunLowerMap[strings.ToLower(rel)]
	if !ok || origRel == rel || hasExactRelPath(absSrc, origRel) {
		return rel, false
	}
	return origRel, true
}

func lookupPruneManifestEntry(rel string, dstCaseInsensitive bool, preRunManifest, preRunLowerMap map[string]string) (string, string, bool) {
	if recorded, ok := preRunManifest[rel]; ok {
		return rel, recorded, true
	}
	if dstCaseInsensitive && preRunLowerMap != nil {
		if origRel, ok := preRunLowerMap[strings.ToLower(rel)]; ok {
			if recorded, found := preRunManifest[origRel]; found {
				return origRel, recorded, true
			}
		}
	}
	return "", "", false
}

func renameCaseOnlyPath(oldPath, newPath string) error {
	parent := filepath.Dir(newPath)
	tmpFile, err := os.CreateTemp(parent, ".tmp-sync-case-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	_ = tmpFile.Close()
	if err := os.Remove(tmpPath); err != nil {
		return err
	}
	if err := os.Rename(oldPath, tmpPath); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, newPath); err != nil {
		_ = os.Rename(tmpPath, oldPath)
		return err
	}
	return nil
}

// syncTargetRepoDirWithReport mirrors additions, modifications, and deletions from
// srcDir into dstDir and returns the list of added, modified, and deleted paths.
func syncTargetRepoDirWithReport(srcDir, dstDir string, excludes []string, preRunManifest map[string]string, strippedSymlinks []string, printer *ui.Printer) (*syncChanges, error) {
	changes := &syncChanges{}
	if err := syncTargetRepoDirInternal(srcDir, dstDir, excludes, preRunManifest, strippedSymlinks, printer, nil, changes); err != nil {
		return changes, err
	}
	return changes, nil
}

// targetRepoSyncer holds shared path, manifest, and change-tracking state across
// the read-only pre-flight, copy, and prune phases of syncTargetRepoDirInternal.
type targetRepoSyncer struct {
	absSrc         string
	absSrcResolved string
	absDst         string
	absDstResolved string
	rootReadOnly   bool

	dstCaseInsensitive bool
	preRunManifest     map[string]string
	preRunLowerMap     map[string]string
	allExcludes        []string

	safeDownloadStripped      map[string]bool
	safeDownloadStrippedLower map[string]bool
	skippedUnsafeSrc          map[string]bool
	skippedUnsafeSrcLower     map[string]bool

	preflightSrcEntries  map[string]bool
	preflightSrcLower    map[string]bool
	preflightCaseRenamed map[string]bool

	caseRenamedPreRun map[string]bool
	srcEntries        map[string]bool
	srcDigests        map[string]string

	dirPerms    []dirPermEntry
	dirsToPrune []pruneDirEntry

	printer *ui.Printer
	changes *syncChanges
}

func (s *targetRepoSyncer) recordSkippedUnsafeSrc(rel string) {
	s.skippedUnsafeSrc[rel] = true
	if s.skippedUnsafeSrcLower != nil {
		s.skippedUnsafeSrcLower[strings.ToLower(rel)] = true
	}
}

func (s *targetRepoSyncer) isPreservedStrippedSymlink(rel string) (fromSafeDownload, fromUnsafeSrc bool) {
	fromSafeDownload = s.safeDownloadStripped[rel] || (s.safeDownloadStrippedLower != nil && s.safeDownloadStrippedLower[strings.ToLower(rel)])
	fromUnsafeSrc = s.skippedUnsafeSrc[rel] || (s.skippedUnsafeSrcLower != nil && s.skippedUnsafeSrcLower[strings.ToLower(rel)])
	return fromSafeDownload, fromUnsafeSrc
}

func (s *targetRepoSyncer) recordPreflightSrc(rel string) {
	s.preflightSrcEntries[rel] = true
	if s.preflightSrcLower != nil {
		s.preflightSrcLower[strings.ToLower(rel)] = true
	}
}

func (s *targetRepoSyncer) recordAdded(p string) {
	if s.changes != nil {
		s.changes.Added = append(s.changes.Added, p)
	}
}

func (s *targetRepoSyncer) recordModified(p string) {
	if s.changes != nil {
		s.changes.Modified = append(s.changes.Modified, p)
	}
}

func (s *targetRepoSyncer) recordDeleted(p string) {
	if s.changes != nil {
		s.changes.Deleted = append(s.changes.Deleted, p)
	}
}

// collectNestedGitExcludes pre-scans absDst for any nested .git entries
// (submodules, nested git repos, gitlinks) and records their relative paths in
// allExcludes so their parent directories are protected from deletion or replacement.
func (s *targetRepoSyncer) collectNestedGitExcludes() {
	_ = filepath.WalkDir(s.absDst, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if pathSegmentsEqualFold(d.Name(), ".git") {
			rel, rerr := filepath.Rel(s.absDst, path)
			if rerr == nil && rel != "." && !pathSegmentsEqualFold(rel, ".git") {
				s.allExcludes = append(s.allExcludes, rel)
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
}

// restorePendingDirPerms restores temporary owner-write permissions on
// pre-existing read-only host directories and chmodded prune directories
// deepest-first on all return paths (including mid-walk errors).
func (s *targetRepoSyncer) restorePendingDirPerms() {
	var pending []dirPermEntry
	pending = append(pending, s.dirPerms...)
	for _, dp := range s.dirsToPrune {
		if dp.chmodded {
			pending = append(pending, dirPermEntry{path: dp.path, perm: dp.origPerm})
		}
	}
	if len(pending) == 0 {
		return
	}
	sep := string(filepath.Separator)
	sort.SliceStable(pending, func(i, j int) bool {
		di := strings.Count(pending[i].path, sep)
		dj := strings.Count(pending[j].path, sep)
		if di != dj {
			return di > dj
		}
		return len(pending[i].path) > len(pending[j].path)
	})
	for _, entry := range pending {
		_ = os.Chmod(entry.path, entry.perm)
	}
}

// syncTargetRepoDirInternal implements target repository sync. When preRunManifest is
// non-nil, entries unchanged in the sandbox preserve any concurrent host edits or
// deletions (host edit wins), entries absent from both srcDir and preRunManifest
// (host-only files created during an agent run), symlinks recorded as
// "symlink-stripped:" at pre-run, and entries whose sandbox symlink was stripped
// by SafeDownload or skipped as unsafe are preserved, and a read-only pre-flight
// conflict check runs before any host mutation so true conflicts leave dstDir
// untouched. When preRunManifest is nil, pruning is disabled (fail-closed).
func syncTargetRepoDirInternal(srcDir, dstDir string, excludes []string, preRunManifest map[string]string, strippedSymlinks []string, printer *ui.Printer, forceDstCaseInsensitive *bool, changes *syncChanges) error {
	absSrc, err := filepath.Abs(srcDir)
	if err != nil {
		return fmt.Errorf("resolving source directory: %w", err)
	}
	absSrcResolved := absSrc
	if resolved, evalErr := filepath.EvalSymlinks(absSrc); evalErr == nil {
		absSrcResolved = resolved
	}
	absDst, err := filepath.Abs(dstDir)
	if err != nil {
		return fmt.Errorf("resolving destination directory: %w", err)
	}
	if absSrc == absDst {
		return nil
	}

	if err := os.MkdirAll(absDst, 0o755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}
	rootReadOnly := false
	if fi, statErr := os.Stat(absDst); statErr == nil && fi.Mode().Perm()&0o200 == 0 {
		rootReadOnly = true
		origRootPerm := fi.Mode().Perm()
		if err := os.Chmod(absDst, origRootPerm|0o700); err != nil {
			return fmt.Errorf("ensuring destination directory is writable: %w", err)
		}
		defer func() {
			_ = os.Chmod(absDst, origRootPerm)
		}()
	}

	dstCaseInsensitive := false
	if forceDstCaseInsensitive != nil {
		dstCaseInsensitive = *forceDstCaseInsensitive
	} else {
		dstCaseInsensitive = detectCaseInsensitiveDir(absDst)
	}
	absDstResolved := absDst
	if resolved, evalErr := filepath.EvalSymlinks(absDst); evalErr == nil {
		absDstResolved = resolved
	}

	s := &targetRepoSyncer{
		absSrc:               absSrc,
		absSrcResolved:       absSrcResolved,
		absDst:               absDst,
		absDstResolved:       absDstResolved,
		rootReadOnly:         rootReadOnly,
		dstCaseInsensitive:   dstCaseInsensitive,
		preRunManifest:       preRunManifest,
		preRunLowerMap:       buildLowerManifestMap(preRunManifest),
		allExcludes:          append([]string(nil), excludes...),
		safeDownloadStripped: make(map[string]bool, len(strippedSymlinks)),
		skippedUnsafeSrc:     make(map[string]bool),
		preflightSrcEntries:  make(map[string]bool),
		preflightCaseRenamed: make(map[string]bool),
		caseRenamedPreRun:    make(map[string]bool),
		srcEntries:           make(map[string]bool),
		srcDigests:           make(map[string]string),
		printer:              printer,
		changes:              changes,
	}
	if dstCaseInsensitive {
		if len(strippedSymlinks) > 0 {
			s.safeDownloadStrippedLower = make(map[string]bool, len(strippedSymlinks))
		}
		s.skippedUnsafeSrcLower = make(map[string]bool)
		s.preflightSrcLower = make(map[string]bool)
	}
	for _, stripped := range strippedSymlinks {
		cleaned := filepath.Clean(stripped)
		if cleaned == "" || cleaned == "." {
			continue
		}
		s.safeDownloadStripped[cleaned] = true
		if s.safeDownloadStrippedLower != nil {
			s.safeDownloadStrippedLower[strings.ToLower(cleaned)] = true
		}
	}

	s.collectNestedGitExcludes()

	if err := s.preflightCopy(); err != nil {
		return fmt.Errorf("copying files to target repository: %w", err)
	}
	if s.preRunManifest != nil {
		if err := s.preflightPrune(); err != nil {
			return fmt.Errorf("pruning deleted files from target repository: %w", err)
		}
	}

	defer s.restorePendingDirPerms()

	if err := s.applyCopy(); err != nil {
		return fmt.Errorf("copying files to target repository: %w", err)
	}
	if s.preRunManifest != nil {
		if err := s.applyPrune(); err != nil {
			return fmt.Errorf("pruning deleted files from target repository: %w", err)
		}
	}
	return s.applyFinalDirPerms()
}

// preflightCopy verifies every sandbox change against host state without mutating
// absDst and caches source file digests for applyCopy so regular files are hashed once.
func (s *targetRepoSyncer) preflightCopy() error {
	return filepath.WalkDir(s.absSrc, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(s.absSrc, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if isExcludedPath(rel, s.allExcludes) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		targetPath := filepath.Join(s.absDst, rel)
		manifestKey, hasCaseRename := resolveManifestKeyAndCaseRename(s.absSrc, rel, s.dstCaseInsensitive, s.preRunManifest, s.preRunLowerMap)
		baseCaseChanged := hasCaseRename && filepath.Base(manifestKey) != filepath.Base(rel)
		preflightHostPath := targetPath
		if hasCaseRename {
			preflightHostPath = filepath.Join(s.absDst, manifestKey)
		}

		if d.Type()&os.ModeSymlink != 0 {
			if isAncestorOfExcludedPath(rel, s.allExcludes) {
				return fmt.Errorf("cannot replace ancestor of excluded path %q with symlink", rel)
			}
			if hasCaseRename && isAncestorOfExcludedPath(manifestKey, s.allExcludes) {
				return fmt.Errorf("cannot replace ancestor of excluded path %q with symlink", manifestKey)
			}
			if wouldSanitizeStripSymlink(path, s.absSrcResolved) {
				s.recordSkippedUnsafeSrc(rel)
				return nil
			}
			s.recordPreflightSrc(rel)
			if baseCaseChanged {
				s.preflightCaseRenamed[manifestKey] = true
			}
			if err := checkPathNotUnderResolvedGit(s.absDstResolved, targetPath, rel); err != nil {
				return err
			}
			if hasCaseRename {
				if err := checkPathNotUnderResolvedGit(s.absDstResolved, preflightHostPath, manifestKey); err != nil {
					return err
				}
			}
			target, readErr := os.Readlink(path)
			if readErr != nil {
				return readErr
			}
			srcDigest := "symlink:" + target
			if !baseCaseChanged && isSandboxEntryUnchanged(s.preRunManifest, manifestKey, srcDigest) {
				return nil
			}
			if !baseCaseChanged {
				if fi, statErr := os.Lstat(preflightHostPath); statErr == nil && fi.Mode()&os.ModeSymlink != 0 {
					if curTarget, curErr := os.Readlink(preflightHostPath); curErr == nil && curTarget == target {
						return nil
					}
				}
			}
			return checkCanReplaceHostTarget(preflightHostPath, manifestKey, s.preRunManifest)
		}

		if d.IsDir() {
			s.recordPreflightSrc(rel)
			if baseCaseChanged {
				s.preflightCaseRenamed[manifestKey] = true
			}
			if err := checkPathNotUnderResolvedGit(s.absDstResolved, targetPath, rel); err != nil {
				return err
			}
			if hasCaseRename {
				if err := checkPathNotUnderResolvedGit(s.absDstResolved, preflightHostPath, manifestKey); err != nil {
					return err
				}
			}
			fi, statErr := os.Lstat(preflightHostPath)
			if statErr == nil {
				if fi.IsDir() {
					return nil
				}
				if !baseCaseChanged && isSandboxEntryUnchanged(s.preRunManifest, manifestKey, "dir") {
					return nil
				}
				if isAncestorOfExcludedPath(rel, s.allExcludes) {
					return fmt.Errorf("cannot replace ancestor of excluded path %q with directory", rel)
				}
				if hasCaseRename && isAncestorOfExcludedPath(manifestKey, s.allExcludes) {
					return fmt.Errorf("cannot replace ancestor of excluded path %q with directory", manifestKey)
				}
				return checkCanReplaceHostTarget(preflightHostPath, manifestKey, s.preRunManifest)
			}
			if !os.IsNotExist(statErr) {
				if !baseCaseChanged && isSandboxEntryUnchanged(s.preRunManifest, manifestKey, "dir") {
					return nil
				}
				return statErr
			}
			return nil
		}

		if !d.Type().IsRegular() {
			s.recordPreflightSrc(rel)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			s.recordPreflightSrc(rel)
			return nil
		}
		s.recordPreflightSrc(rel)
		if baseCaseChanged {
			s.preflightCaseRenamed[manifestKey] = true
		}

		if err := checkPathNotUnderResolvedGit(s.absDstResolved, targetPath, rel); err != nil {
			return err
		}
		if hasCaseRename {
			if err := checkPathNotUnderResolvedGit(s.absDstResolved, preflightHostPath, manifestKey); err != nil {
				return err
			}
		}

		srcDigest, err := computeHostEntryDigest(path, info)
		if err != nil {
			return err
		}
		s.srcDigests[rel] = srcDigest
		sandboxUnchanged := !baseCaseChanged && isSandboxEntryUnchanged(s.preRunManifest, manifestKey, srcDigest)

		dstInfo, dstStatErr := os.Lstat(preflightHostPath)
		if os.IsNotExist(dstStatErr) {
			return nil
		}
		if dstStatErr != nil {
			if sandboxUnchanged {
				return nil
			}
			return dstStatErr
		}
		if !dstInfo.Mode().IsRegular() {
			if sandboxUnchanged {
				return nil
			}
			if isAncestorOfExcludedPath(rel, s.allExcludes) {
				return fmt.Errorf("cannot replace ancestor of excluded path %q with file", rel)
			}
			if hasCaseRename && isAncestorOfExcludedPath(manifestKey, s.allExcludes) {
				return fmt.Errorf("cannot replace ancestor of excluded path %q with file", manifestKey)
			}
			return checkCanReplaceHostTarget(preflightHostPath, manifestKey, s.preRunManifest)
		}

		wantPerm := (dstInfo.Mode().Perm() &^ 0o111) | (info.Mode().Perm() & 0o111)
		dstDigest, dstDigestErr := computeHostEntryDigest(preflightHostPath, dstInfo)
		checkConcurrent := func() error {
			if s.preRunManifest == nil {
				return nil
			}
			recorded, ok := s.preRunManifest[manifestKey]
			if !ok || dstDigestErr != nil || dstDigest != normalizeManifestDigest(recorded) {
				return fmt.Errorf("refusing to overwrite concurrently modified host file %q", manifestKey)
			}
			return nil
		}
		if dstDigestErr == nil && dstDigest == srcDigest && !baseCaseChanged {
			if dstInfo.Mode().Perm() == wantPerm {
				return nil
			}
			return checkConcurrent()
		}
		if sandboxUnchanged {
			return nil
		}
		return checkConcurrent()
	})
}

// preflightPrune verifies every sandbox-deleted host entry against preRunManifest
// without mutating absDst so deletion-side concurrent host edits or unreadable
// host files abort cleanly without partial writes.
func (s *targetRepoSyncer) preflightPrune() error {
	return filepath.WalkDir(s.absDst, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		rel, err := filepath.Rel(s.absDst, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if isExcludedPath(rel, s.allExcludes) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		inSrc := s.preflightSrcEntries[rel] || (s.preflightSrcLower != nil && s.preflightSrcLower[strings.ToLower(rel)])
		if inSrc {
			return nil
		}
		if fromSD, fromUnsafe := s.isPreservedStrippedSymlink(rel); fromSD || fromUnsafe {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if isAncestorOfExcludedPath(rel, s.allExcludes) {
			return nil
		}
		manifestKey, recorded, ok := lookupPruneManifestEntry(rel, s.dstCaseInsensitive, s.preRunManifest, s.preRunLowerMap)
		if !ok {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if s.preflightCaseRenamed[manifestKey] {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 && strings.HasPrefix(recorded, "symlink-stripped:") {
			return nil
		}
		if recorded == "other" {
			return nil
		}
		if err := checkPathNotUnderResolvedGit(s.absDstResolved, path, rel); err != nil {
			return err
		}
		if d.IsDir() {
			if recorded != "dir" {
				return fmt.Errorf("refusing to delete concurrently modified host entry %q", manifestKey)
			}
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			if os.IsNotExist(infoErr) {
				return nil
			}
			return fmt.Errorf("checking host entry %q before prune: %w", rel, infoErr)
		}
		changed, chkErr := hasConcurrentHostEdit(path, manifestKey, info, s.preRunManifest)
		if chkErr != nil {
			if os.IsNotExist(chkErr) {
				return nil
			}
			return fmt.Errorf("checking host entry %q before prune: %w", rel, chkErr)
		}
		if changed {
			return fmt.Errorf("refusing to delete concurrently modified host entry %q", manifestKey)
		}
		return nil
	})
}

// applyCopy copies and updates files, directories, and safe symlinks from absSrc
// into absDst while respecting exclusions, manifest conflict rules, and host permissions.
func (s *targetRepoSyncer) applyCopy() error {
	return filepath.WalkDir(s.absSrc, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(s.absSrc, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if isExcludedPath(rel, s.allExcludes) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		targetPath := filepath.Join(s.absDst, rel)
		manifestKey, hasCaseRename := resolveManifestKeyAndCaseRename(s.absSrc, rel, s.dstCaseInsensitive, s.preRunManifest, s.preRunLowerMap)
		baseCaseChanged := hasCaseRename && filepath.Base(manifestKey) != filepath.Base(rel)
		hostCheckPath := targetPath
		if baseCaseChanged {
			hostCheckPath = filepath.Join(filepath.Dir(targetPath), filepath.Base(manifestKey))
		}

		if d.Type()&os.ModeSymlink != 0 {
			if isAncestorOfExcludedPath(rel, s.allExcludes) {
				return fmt.Errorf("cannot replace ancestor of excluded path %q with symlink", rel)
			}
			if hasCaseRename && isAncestorOfExcludedPath(manifestKey, s.allExcludes) {
				return fmt.Errorf("cannot replace ancestor of excluded path %q with symlink", manifestKey)
			}
			if wouldSanitizeStripSymlink(path, s.absSrcResolved) {
				s.recordSkippedUnsafeSrc(rel)
				if s.printer != nil {
					s.printer.StepWarn(fmt.Sprintf("Skipping unsafe symlink during target repo sync: %s", rel))
				}
				return nil
			}
			s.srcEntries[rel] = true
			if err := checkPathNotUnderResolvedGit(s.absDstResolved, targetPath, rel); err != nil {
				return err
			}
			if baseCaseChanged {
				if err := checkPathNotUnderResolvedGit(s.absDstResolved, hostCheckPath, manifestKey); err != nil {
					return err
				}
			}
			target, readErr := os.Readlink(path)
			if readErr != nil {
				return readErr
			}
			srcDigest := "symlink:" + target
			if !baseCaseChanged && isSandboxEntryUnchanged(s.preRunManifest, manifestKey, srcDigest) {
				return nil
			}
			if !baseCaseChanged {
				if fi, statErr := os.Lstat(targetPath); statErr == nil && fi.Mode()&os.ModeSymlink != 0 {
					if curTarget, curErr := os.Readlink(targetPath); curErr == nil && curTarget == target {
						return nil
					}
				}
			}
			_, hostExistedErr := os.Lstat(hostCheckPath)
			hostExisted := hostExistedErr == nil
			if err := checkCanReplaceHostTarget(hostCheckPath, manifestKey, s.preRunManifest); err != nil {
				return err
			}
			if baseCaseChanged {
				s.caseRenamedPreRun[manifestKey] = true
			}
			if err := os.RemoveAll(hostCheckPath); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(target, targetPath); err != nil {
				return err
			}
			if hostExisted {
				s.recordModified(rel)
			} else {
				s.recordAdded(rel)
			}
			return nil
		}

		if d.IsDir() {
			s.srcEntries[rel] = true
			if err := checkPathNotUnderResolvedGit(s.absDstResolved, targetPath, rel); err != nil {
				return err
			}
			if baseCaseChanged {
				if err := checkPathNotUnderResolvedGit(s.absDstResolved, hostCheckPath, manifestKey); err != nil {
					return err
				}
			}
			fi, statErr := os.Lstat(hostCheckPath)
			if statErr == nil {
				if fi.IsDir() {
					if baseCaseChanged {
						if err := renameCaseOnlyPath(hostCheckPath, targetPath); err != nil {
							return err
						}
						s.caseRenamedPreRun[manifestKey] = true
					}
					origPerm := fi.Mode().Perm()
					if origPerm&0o200 == 0 {
						if err := os.Chmod(targetPath, origPerm|0o700); err != nil {
							return err
						}
						s.dirPerms = append(s.dirPerms, dirPermEntry{path: targetPath, perm: origPerm})
					}
					return nil
				}
				if !baseCaseChanged && isSandboxEntryUnchanged(s.preRunManifest, manifestKey, "dir") {
					return nil
				}
				if isAncestorOfExcludedPath(rel, s.allExcludes) {
					return fmt.Errorf("cannot replace ancestor of excluded path %q with directory", rel)
				}
				if hasCaseRename && isAncestorOfExcludedPath(manifestKey, s.allExcludes) {
					return fmt.Errorf("cannot replace ancestor of excluded path %q with directory", manifestKey)
				}
				if err := checkCanReplaceHostTarget(hostCheckPath, manifestKey, s.preRunManifest); err != nil {
					return err
				}
				if baseCaseChanged {
					s.caseRenamedPreRun[manifestKey] = true
				}
				if err := os.Remove(hostCheckPath); err != nil {
					return err
				}
			} else if !os.IsNotExist(statErr) {
				if !baseCaseChanged && isSandboxEntryUnchanged(s.preRunManifest, manifestKey, "dir") {
					return nil
				}
				return statErr
			} else if !baseCaseChanged && isSandboxEntryUnchanged(s.preRunManifest, manifestKey, "dir") {
				// Host deleted a pre-existing directory while sandbox kept it; skip recreating
				// it unless the agent writes a new or modified file underneath.
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			perm := info.Mode().Perm()
			if !s.rootReadOnly && perm&0o200 == 0 {
				perm |= 0o200
			}
			if err := os.MkdirAll(targetPath, perm|0o700); err != nil {
				return err
			}
			if err := os.Chmod(targetPath, perm|0o700); err != nil {
				return err
			}
			if perm&0o200 == 0 {
				s.dirPerms = append(s.dirPerms, dirPermEntry{path: targetPath, perm: perm})
			}
			return nil
		}

		// Guard against non-regular special files (FIFOs, sockets, device nodes)
		// before opening or touching destination entries.
		if !d.Type().IsRegular() {
			s.srcEntries[rel] = true
			if s.printer != nil {
				s.printer.StepWarn(fmt.Sprintf("Skipping non-regular file during target repo sync: %s", rel))
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			s.srcEntries[rel] = true
			if s.printer != nil {
				s.printer.StepWarn(fmt.Sprintf("Skipping non-regular file during target repo sync: %s", rel))
			}
			return nil
		}
		s.srcEntries[rel] = true

		if err := checkPathNotUnderResolvedGit(s.absDstResolved, targetPath, rel); err != nil {
			return err
		}
		if baseCaseChanged {
			if err := checkPathNotUnderResolvedGit(s.absDstResolved, hostCheckPath, manifestKey); err != nil {
				return err
			}
		}

		srcDigest, ok := s.srcDigests[rel]
		if !ok {
			srcDigest, err = computeHostEntryDigest(path, info)
			if err != nil {
				return err
			}
		}
		sandboxUnchanged := !baseCaseChanged && isSandboxEntryUnchanged(s.preRunManifest, manifestKey, srcDigest)

		dstInfo, dstStatErr := os.Lstat(hostCheckPath)
		hostExisted := dstStatErr == nil
		if dstStatErr == nil && !dstInfo.Mode().IsRegular() {
			if sandboxUnchanged {
				return nil
			}
			if isAncestorOfExcludedPath(rel, s.allExcludes) {
				return fmt.Errorf("cannot replace ancestor of excluded path %q with file", rel)
			}
			if hasCaseRename && isAncestorOfExcludedPath(manifestKey, s.allExcludes) {
				return fmt.Errorf("cannot replace ancestor of excluded path %q with file", manifestKey)
			}
			if err := checkCanReplaceHostTarget(hostCheckPath, manifestKey, s.preRunManifest); err != nil {
				return err
			}
			if baseCaseChanged {
				s.caseRenamedPreRun[manifestKey] = true
			}
			if err := os.RemoveAll(hostCheckPath); err != nil {
				return err
			}
			dstStatErr = os.ErrNotExist
		}

		wantPerm := info.Mode().Perm()
		if dstStatErr == nil && dstInfo.Mode().IsRegular() {
			// Git only tracks the executable bit; always keep the existing host file's
			// non-exec permissions while propagating executable-bit changes from sandbox.
			wantPerm = (dstInfo.Mode().Perm() &^ 0o111) | (wantPerm & 0o111)
		} else {
			wantPerm &^= 0o022
			if !s.rootReadOnly && wantPerm&0o200 == 0 {
				wantPerm |= 0o200
			}
		}

		if os.IsNotExist(dstStatErr) {
			if !hostExisted && isSandboxEntryUnchanged(s.preRunManifest, manifestKey, srcDigest) {
				// Sandbox copy is unchanged since upload while the user deleted the host file; preserve the host deletion.
				return nil
			}
		} else if dstStatErr != nil {
			if sandboxUnchanged {
				return nil
			}
			return dstStatErr
		}

		removeOldPath := ""
		if dstStatErr == nil && dstInfo.Mode().IsRegular() {
			dstDigest, dstDigestErr := computeHostEntryDigest(hostCheckPath, dstInfo)
			checkConcurrent := func() error {
				if s.preRunManifest == nil {
					return nil
				}
				recorded, ok := s.preRunManifest[manifestKey]
				if !ok || dstDigestErr != nil || dstDigest != normalizeManifestDigest(recorded) {
					return fmt.Errorf("refusing to overwrite concurrently modified host file %q", manifestKey)
				}
				return nil
			}
			if dstDigestErr == nil && dstDigest == srcDigest && !baseCaseChanged {
				if dstInfo.Mode().Perm() == wantPerm {
					return nil
				}
				if err := checkConcurrent(); err != nil {
					return err
				}
				if !hasMultipleLinks(dstInfo) {
					if err := os.Chmod(hostCheckPath, wantPerm); err != nil {
						return err
					}
					s.recordModified(rel)
					return nil
				}
			} else {
				if sandboxUnchanged {
					// Sandbox copy is unchanged since pre-run while the user edited the host file; host edit wins.
					return nil
				}
				if err := checkConcurrent(); err != nil {
					return err
				}
				if baseCaseChanged {
					removeOldPath = hostCheckPath
					s.caseRenamedPreRun[manifestKey] = true
				}
			}
		}

		if err := writeAtomicFile(targetPath, path, wantPerm, removeOldPath); err != nil {
			return err
		}
		_ = os.Chtimes(targetPath, time.Now(), info.ModTime())
		if hostExisted {
			s.recordModified(rel)
		} else {
			s.recordAdded(rel)
		}
		return nil
	})
}

// applyPrune removes pre-existing host files and empty directories that were
// deleted in absSrc while preserving excludes, stripped symlinks, non-regular
// host entries, and host-only or concurrently modified entries.
func (s *targetRepoSyncer) applyPrune() error {
	err := filepath.WalkDir(s.absDst, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		rel, err := filepath.Rel(s.absDst, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if isExcludedPath(rel, s.allExcludes) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if !s.srcEntries[rel] {
			manifestKey, recorded, ok := lookupPruneManifestEntry(rel, s.dstCaseInsensitive, s.preRunManifest, s.preRunLowerMap)
			if !ok || s.caseRenamedPreRun[manifestKey] {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 && strings.HasPrefix(recorded, "symlink-stripped:") {
				return nil
			}
			if recorded == "other" {
				if s.printer != nil {
					s.printer.StepWarn(fmt.Sprintf("Preserving host entry %q (non-regular file not synchronized from sandbox)", rel))
				}
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if fromSD, fromUnsafe := s.isPreservedStrippedSymlink(rel); fromSD || fromUnsafe {
				if fromSD && !fromUnsafe && s.printer != nil {
					s.printer.StepWarn(fmt.Sprintf("Preserving host entry %q (sandbox symlink stripped by SafeDownload)", rel))
				}
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if isAncestorOfExcludedPath(rel, s.allExcludes) {
				return nil
			}
			if err := checkPathNotUnderResolvedGit(s.absDstResolved, path, rel); err != nil {
				return err
			}
			if d.IsDir() {
				if recorded != "dir" {
					return fmt.Errorf("refusing to delete concurrently modified host entry %q", manifestKey)
				}
				entry := pruneDirEntry{path: path}
				if info, infoErr := d.Info(); infoErr == nil && info.Mode().Perm()&0o200 == 0 {
					entry.origPerm = info.Mode().Perm()
					if chmodErr := os.Chmod(path, entry.origPerm|0o700); chmodErr == nil {
						entry.chmodded = true
					}
				}
				s.dirsToPrune = append(s.dirsToPrune, entry)
				return nil
			}
			info, infoErr := d.Info()
			if infoErr != nil {
				if os.IsNotExist(infoErr) {
					return nil
				}
				return fmt.Errorf("checking host entry %q before prune: %w", rel, infoErr)
			}
			changed, chkErr := hasConcurrentHostEdit(path, manifestKey, info, s.preRunManifest)
			if chkErr != nil {
				if os.IsNotExist(chkErr) {
					return nil
				}
				return fmt.Errorf("checking host entry %q before prune: %w", rel, chkErr)
			}
			if changed {
				return fmt.Errorf("refusing to delete concurrently modified host entry %q", manifestKey)
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			s.recordDeleted(rel)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Remove empty pruned directories deepest-first, leaving any directory that
	// still holds protected descendants (excludes, stripped symlinks, host-only files).
	for i := len(s.dirsToPrune) - 1; i >= 0; i-- {
		dp := s.dirsToPrune[i]
		entries, readErr := os.ReadDir(dp.path)
		if readErr == nil && len(entries) == 0 {
			if rmErr := os.Remove(dp.path); rmErr != nil {
				return rmErr
			}
			s.dirsToPrune[i].chmodded = false
		} else if dp.chmodded {
			_ = os.Chmod(dp.path, dp.origPerm)
			s.dirsToPrune[i].chmodded = false
		}
	}
	return nil
}

// applyFinalDirPerms restores final directory permissions deepest-first so
// read-only host directories retain their original modes after sync completes.
func (s *targetRepoSyncer) applyFinalDirPerms() error {
	for i := len(s.dirPerms) - 1; i >= 0; i-- {
		if err := os.Chmod(s.dirPerms[i].path, s.dirPerms[i].perm); err != nil {
			return fmt.Errorf("restoring directory permissions: %w", err)
		}
	}
	s.dirPerms = nil
	return nil
}

// writeAtomicFile copies srcPath to targetPath atomically by streaming to a
// temporary file in the same directory, setting permissions, optionally removing
// an old case-variant entry, and renaming over targetPath. This breaks existing
// hardlinks without buffering large files into memory.
func writeAtomicFile(targetPath, srcPath string, perm os.FileMode, removeBeforeRename string) error {
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	tmp, err := os.CreateTemp(dir, ".tmp-sync-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := io.Copy(tmp, srcFile); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if removeBeforeRename != "" && removeBeforeRename != targetPath {
		if err := os.Remove(removeBeforeRename); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// isHFSIgnorable reports whether r is one of the 16 Unicode code points ignored
// by macOS HFS+ filename comparison (Apple TN1150 / git core.protectHFS).
func isHFSIgnorable(r rune) bool {
	return (r >= 0x200C && r <= 0x200F) ||
		(r >= 0x202A && r <= 0x202E) ||
		(r >= 0x206A && r <= 0x206F) ||
		r == 0xFEFF
}

// stripHFSIgnorable returns s with all HFS+-ignorable code points removed.
func stripHFSIgnorable(s string) string {
	hasIgnorable := false
	for _, r := range s {
		if isHFSIgnorable(r) {
			hasIgnorable = true
			break
		}
	}
	if !hasIgnorable {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isHFSIgnorable(r) {
			return -1
		}
		return r
	}, s)
}

func pathSegmentsEqualFold(a, b string) bool {
	return strings.EqualFold(stripHFSIgnorable(a), stripHFSIgnorable(b))
}

func hasGitPathSegment(relPath string) bool {
	relClean := filepath.Clean(relPath)
	for _, part := range strings.Split(relClean, string(filepath.Separator)) {
		if pathSegmentsEqualFold(part, ".git") {
			return true
		}
	}
	return false
}

func hasPathPrefixFold(path, prefix string) (equal bool, strictPrefix bool) {
	pClean := filepath.Clean(path)
	xClean := filepath.Clean(prefix)
	pSegs := strings.Split(pClean, string(filepath.Separator))
	xSegs := strings.Split(xClean, string(filepath.Separator))
	if len(pSegs) < len(xSegs) {
		return false, false
	}
	for i := range xSegs {
		if !pathSegmentsEqualFold(pSegs[i], xSegs[i]) {
			return false, false
		}
	}
	return len(pSegs) == len(xSegs), len(pSegs) > len(xSegs)
}

func isExcludedPath(relPath string, excludes []string) bool {
	// Match any path segment named ".git" case-insensitively after stripping
	// HFS+-ignorable Unicode code points (root .git, submodules, nested repos).
	if hasGitPathSegment(relPath) {
		return true
	}
	for _, ex := range excludes {
		eq, sub := hasPathPrefixFold(relPath, ex)
		if eq || sub {
			return true
		}
	}
	return false
}

func isAncestorOfExcludedPath(relPath string, excludes []string) bool {
	for _, ex := range excludes {
		_, strict := hasPathPrefixFold(ex, relPath)
		if strict {
			return true
		}
	}
	return false
}
