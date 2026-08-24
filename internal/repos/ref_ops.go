package repos

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// shimOwner and shimRepo identify the fullsend-ai/fullsend repo whose
// tags are resolved when preserving SHA pinning during upgrade.
const (
	shimOwner = "fullsend-ai"
	shimRepo  = "fullsend"
)

// safeRefPattern validates that a ref contains only characters safe for
// GitHub Actions uses: lines.
var safeRefPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// shaRefPattern matches git commit SHAs (7–40 hex characters, case-insensitive).
// Used to detect whether a workflow ref is SHA-pinned vs. tag-pinned.
// Note: tag names that happen to be 7–40 hex characters (e.g. "deadbeef")
// would be treated as SHA-pinned. This is extremely unlikely since version
// tags follow the vX.Y.Z convention.
var shaRefPattern = regexp.MustCompile(`(?i)^[0-9a-f]{7,40}$`)

// isSHARef reports whether ref looks like a git commit SHA
// (7–40 hex characters, case-insensitive).
func isSHARef(ref string) bool {
	return shaRefPattern.MatchString(ref)
}

// IsValidRef reports whether ref contains only characters safe for use in
// GitHub Actions workflow uses: lines.
func IsValidRef(ref string) bool {
	return ref != "" && safeRefPattern.MatchString(ref)
}

// replaceShimRef replaces the @ref (and optional trailing annotation) in all
// fullsend-ai/fullsend uses: lines within a workflow file, using the
// forge-specific shim ref pattern. GitHub uses YAML comment annotation
// "ref # tag"; GitLab uses parenthesized "ref (tag)".
func replaceShimRef(content []byte, newRef, newTag string, fc ForgeConfig, forgeName string) ([]byte, bool) {
	suffix := formatRefAnnotation(newRef, newTag, forgeName)

	safe := strings.ReplaceAll(suffix, "$", "$$")
	replaced := fc.ShimRefPattern.ReplaceAllString(string(content), "${1}"+safe)
	changed := replaced != string(content)
	return []byte(replaced), changed
}

// formatRefAnnotation formats a ref with its human-readable annotation.
// GitHub uses YAML comment syntax ("sha # tag") since annotations appear
// inline in uses: lines. GitLab uses parenthesized syntax ("sha (tag)")
// since annotations appear inside YAML comment markers.
func formatRefAnnotation(ref, tag, forgeName string) string {
	if tag == "" || tag == ref {
		return ref
	}
	if forgeName == ForgeGitLab {
		return ref + " (" + tag + ")"
	}
	return ref + " # " + tag
}

// collectGitLabUpgradeTemplates collects the GitLab CI template files
// (agent, poll) for inclusion in an upgrade commit. The dispatch file
// is excluded because the upgrade path handles it separately via
// replaceShimRef. The targetRef is used as the fullsend version
// embedded in the before_script install block.
func collectGitLabUpgradeTemplates(runnerTags []string, targetRef string) ([]forge.TreeFile, error) {
	installFiles, err := scaffold.CollectGitLabPerRepoInstallFiles(runnerTags, targetRef, "")
	if err != nil {
		return nil, err
	}
	var files []forge.TreeFile
	for _, f := range installFiles {
		// Skip the dispatch file — upgrade handles it via replaceShimRef.
		// Skip the root pipeline file — users may have customized it
		// (e.g. adding workflow:rules for push events).
		if f.Path == ".gitlab/ci/fullsend-dispatch.yml" || f.Path == ".gitlab-ci.yml" {
			continue
		}
		files = append(files, forge.TreeFile{
			Path:    f.Path,
			Content: f.Content,
			Mode:    f.Mode,
		})
	}
	return files, nil
}

// readWorkflowContent tries each known shim workflow path and returns
// the content and path of the first one found, or (nil, "", nil) if none.
func readWorkflowContent(ctx context.Context, client forge.Client, owner, repo string, fc ForgeConfig) ([]byte, string, error) {
	for _, path := range fc.WorkflowPaths {
		content, err := client.GetFileContent(ctx, owner, repo, path)
		if err != nil {
			if forge.IsNotFound(err) {
				continue
			}
			return nil, "", err
		}
		return content, path, nil
	}
	return nil, "", nil
}

func skipReasonForNoChange(currentRef, targetRef string) string {
	if currentRef == targetRef || isSHARef(currentRef) {
		return fmt.Sprintf("already at %s", targetRef)
	}
	return "no uses: lines matched for replacement"
}

// isSemver returns true if the ref looks like a semver version tag (vX.Y.Z with optional pre-release).
var semverPattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)`)

func isSemver(ref string) bool {
	return semverPattern.MatchString(ref)
}

// semverFullPattern captures major, minor, patch, and optional prerelease suffix.
// Build metadata (+...) is excluded per semver 2.0.0 §10.
var semverFullPattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-([^+]+))?`)

// compareSemver compares two semver refs (vX.Y.Z or vX.Y.Z-pre format).
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
// Prerelease versions are less than their release counterparts
// (e.g., v2.3.0-rc1 < v2.3.0). Prerelease identifiers are compared
// per semver 2.0.0 §11: split by ".", numeric segments compared as
// integers, string segments compared lexically, numeric < string,
// and fewer fields is less when all preceding fields are equal.
func compareSemver(a, b string) int {
	am := semverFullPattern.FindStringSubmatch(a)
	bm := semverFullPattern.FindStringSubmatch(b)
	if am == nil || bm == nil {
		return 0
	}
	for i := 1; i <= 3; i++ {
		av := parseUint(am[i])
		bv := parseUint(bm[i])
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	aPre := am[4]
	bPre := bm[4]
	if aPre == "" && bPre == "" {
		return 0
	}
	if aPre == "" {
		return 1
	}
	if bPre == "" {
		return -1
	}
	return comparePrerelease(aPre, bPre)
}

// comparePrerelease compares dot-separated prerelease identifiers per
// semver 2.0.0 §11.
func comparePrerelease(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	n := len(as)
	if len(bs) < n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		if c := comparePreID(as[i], bs[i]); c != 0 {
			return c
		}
	}
	if len(as) < len(bs) {
		return -1
	}
	if len(as) > len(bs) {
		return 1
	}
	return 0
}

func comparePreID(a, b string) int {
	aNum, aIsNum := tryParseUint(a)
	bNum, bIsNum := tryParseUint(b)
	switch {
	case aIsNum && bIsNum:
		if aNum < bNum {
			return -1
		}
		if aNum > bNum {
			return 1
		}
		return 0
	case aIsNum:
		return -1
	case bIsNum:
		return 1
	default:
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
		return 0
	}
}

func tryParseUint(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	return parseUint(s), true
}

func parseUint(s string) uint64 {
	const max = ^uint64(0)
	const maxDiv10 = max / 10
	const maxMod10 = byte(max % 10)
	var n uint64
	for _, c := range s {
		d := byte(c - '0')
		if n > maxDiv10 || (n == maxDiv10 && d > maxMod10) {
			return max
		}
		n = n*10 + uint64(d)
	}
	return n
}
