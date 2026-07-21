package repos

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// UpgradeConfig holds configuration for a batch upgrade operation.
type UpgradeConfig struct {
	Manifest       *Manifest
	RefOverride    string
	RepoFilter     []string
	DryRun         bool
	Force          bool
	Direct         bool
	MaxConcurrency int
}

// UpgradeResult holds the outcome of upgrading a single repo.
type UpgradeResult struct {
	Owner      string
	Repo       string
	OldRef     string
	NewRef     string
	Upgraded   bool
	Skipped    bool
	SkipReason string
	Error      error
}

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

// shimRefPattern matches all @ref occurrences in fullsend-ai/fullsend workflow uses: lines.
// The trailing comment group uses [ \t]* (not \s*) to avoid matching across newlines.
var shimRefPattern = regexp.MustCompile(
	`(uses:\s+` + shimOwner + `/` + shimRepo + `/[^@]+@)\S+([ \t]*#.*)?`,
)

// replaceShimRef replaces the @ref (and optional trailing # tag comment) in all
// fullsend-ai/fullsend uses: lines within a workflow file. The newRef and
// newTag are formatted as "newRef # newTag" when newTag is non-empty and
// differs from newRef.
func replaceShimRef(content []byte, newRef, newTag string) ([]byte, bool) {
	suffix := newRef
	if newTag != "" && newTag != newRef {
		suffix = newRef + " # " + newTag
	}

	safe := strings.ReplaceAll(suffix, "$", "$$")
	replaced := shimRefPattern.ReplaceAllString(string(content), "${1}"+safe)
	changed := replaced != string(content)
	return []byte(replaced), changed
}

// Upgrade upgrades the scaffold shim ref across repos in the manifest.
// It reads each repo's current workflow file, determines whether an upgrade
// is needed, and commits the updated workflow with the new ref.
func Upgrade(ctx context.Context, cfg UpgradeConfig,
	client forge.Client,
	commitFn ScaffoldCommitFunc,
	progress ProgressFunc) ([]UpgradeResult, error) {

	if progress == nil {
		progress = func(_, _, _ string) {}
	}

	resolved, err := cfg.Manifest.ExpandGlobs(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("resolving repos: %w", err)
	}

	if len(cfg.RepoFilter) > 0 {
		resolved, err = filterRepos(resolved, cfg.RepoFilter)
		if err != nil {
			return nil, err
		}
	}

	if cfg.MaxConcurrency <= 0 || cfg.MaxConcurrency > 32 {
		return nil, fmt.Errorf("concurrency must be between 1 and 32, got %d", cfg.MaxConcurrency)
	}

	results := make([]UpgradeResult, len(resolved))
	sem := make(chan struct{}, cfg.MaxConcurrency)
	var wg sync.WaitGroup

	for i, rr := range resolved {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return nil, ctx.Err()
		}
		wg.Add(1)
		go func(idx int, rr ResolvedRepo) {
			defer wg.Done()
			defer func() { <-sem }()

			resolvedCfg := cfg.Manifest.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Entry)
			result := upgradeRepo(ctx, client, commitFn, rr.Owner, rr.Repo, resolvedCfg, cfg, progress)
			results[idx] = result
		}(i, rr)
	}
	wg.Wait()

	return results, nil
}

func upgradeRepo(ctx context.Context, client forge.Client,
	commitFn ScaffoldCommitFunc,
	owner, repo string,
	resolvedCfg ResolvedConfig,
	cfg UpgradeConfig,
	progress ProgressFunc) UpgradeResult {

	repoFullName := owner + "/" + repo
	result := UpgradeResult{Owner: owner, Repo: repo}

	targetRef := cfg.RefOverride
	if targetRef == "" {
		targetRef = resolvedCfg.FullsendRef
	}
	if targetRef == "" {
		result.Skipped = true
		result.SkipReason = "no target ref configured"
		return result
	}
	if !IsValidRef(targetRef) {
		result.Error = fmt.Errorf("ref %q contains invalid characters; only alphanumeric, dot, underscore, and hyphen are allowed", targetRef)
		return result
	}
	result.NewRef = targetRef

	if isFloatingRef(targetRef) {
		result.Skipped = true
		result.SkipReason = "floating tag, skipped"
		return result
	}

	progress(repoFullName, "read", "Reading workflow file")

	content, workflowPath, err := readWorkflowContent(ctx, client, owner, repo)
	if err != nil {
		result.Error = fmt.Errorf("reading workflow: %w", err)
		return result
	}
	if content == nil {
		result.Skipped = true
		result.SkipReason = "workflow file not found"
		return result
	}

	currentRef := extractWorkflowRef(content)
	result.OldRef = currentRef

	if isFloatingRef(currentRef) {
		result.Skipped = true
		result.SkipReason = "floating tag, skipped"
		return result
	}

	if !cfg.Force && isSemver(currentRef) && isSemver(targetRef) {
		if compareSemver(currentRef, targetRef) > 0 {
			result.Skipped = true
			result.SkipReason = fmt.Sprintf("current %s is newer than target %s (use --force to override)", currentRef, targetRef)
			return result
		}
	}

	if cfg.DryRun {
		// Check if any uses: lines would change without resolving the SHA,
		// so DryRun never makes API calls that could fail.
		_, changed := replaceShimRef(content, targetRef, "")
		if !changed {
			result.Skipped = true
			result.SkipReason = "no uses: lines matched for replacement"
			return result
		}
		result.Upgraded = true
		msg := fmt.Sprintf("Would upgrade %s → %s", currentRef, targetRef)
		if isSHARef(currentRef) {
			msg += " (SHA will be resolved at upgrade time)"
		}
		progress(repoFullName, "dry-run", msg)
		return result
	}

	// Preserve SHA pinning: if the current ref is a SHA, resolve the target
	// tag to its commit SHA and write @<sha> # <tag>. If the current ref is
	// a tag, keep tag-only format (@<tag>).
	var newContent []byte
	var changed bool
	if isSHARef(currentRef) {
		sha, err := client.GetRef(ctx, shimOwner, shimRepo, "tags/"+targetRef)
		if err != nil {
			result.Error = fmt.Errorf("resolving tag %s to SHA: %w", targetRef, err)
			return result
		}
		newContent, changed = replaceShimRef(content, sha, targetRef)
	} else {
		newContent, changed = replaceShimRef(content, targetRef, "")
	}
	if !changed {
		result.Skipped = true
		result.SkipReason = "no uses: lines matched for replacement"
		return result
	}

	progress(repoFullName, "upgrade", fmt.Sprintf("Upgrading %s → %s", currentRef, targetRef))

	files := []forge.TreeFile{{
		Path:    workflowPath,
		Content: newContent,
		Mode:    "100644",
	}}

	if err := commitFn(ctx, owner, repo, files, cfg.Direct); err != nil {
		result.Error = fmt.Errorf("committing upgrade: %w", err)
		return result
	}

	result.Upgraded = true
	progress(repoFullName, "done", fmt.Sprintf("Upgraded %s → %s", currentRef, targetRef))
	return result
}

// readWorkflowContent tries each known shim workflow path and returns
// the content and path of the first one found, or (nil, "", nil) if none.
func readWorkflowContent(ctx context.Context, client forge.Client, owner, repo string) ([]byte, string, error) {
	for _, path := range workflowPaths {
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

// isFloatingRef returns true for refs that are not pinned versions —
// branch names, "latest", or non-semver floating tags like "v0".
func isFloatingRef(ref string) bool {
	if ref == "" {
		return false
	}
	lower := strings.ToLower(ref)
	if lower == "latest" || lower == "main" || lower == "master" {
		return true
	}
	// A partial semver ref (e.g., "v0", "v1", "v1.2") is floating because it
	// does not pin to a specific patch version and cannot be reliably compared.
	if isPartialVersionTag(ref) {
		return true
	}
	return false
}

// partialVersionPattern matches version tags that are missing the patch
// component: "v0", "v1", "v1.2", etc. Full semver (vX.Y.Z) is NOT matched.
var partialVersionPattern = regexp.MustCompile(`^v\d+(\.\d+)?$`)

func isPartialVersionTag(ref string) bool {
	return partialVersionPattern.MatchString(ref)
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
	const maxSafe = ^uint64(0)/10 - 1
	var n uint64
	for _, c := range s {
		if n > maxSafe {
			return ^uint64(0)
		}
		n = n*10 + uint64(c-'0')
	}
	return n
}

// UpgradeMint verifies the token mint deployment matches the manifest configuration.
func UpgradeMint(ctx context.Context, manifest *Manifest,
	provisioner WIFProvisioner,
	progress ProgressFunc) error {

	if progress == nil {
		progress = func(_, _, _ string) {}
	}

	progress("mint", "discover", "Checking current mint deployment")

	discovery, err := provisioner.DiscoverMint(ctx)
	if err != nil {
		return fmt.Errorf("discovering mint: %w", err)
	}

	if discovery.URL == "" {
		return fmt.Errorf("mint discovery returned empty URL")
	}

	progress("mint", "discover", fmt.Sprintf("Found mint at %s", discovery.URL))

	if discovery.URL != manifest.Mint.URL {
		return fmt.Errorf("discovered mint URL %q does not match manifest mint URL %q", discovery.URL, manifest.Mint.URL)
	}

	progress("mint", "done", "Mint verified successfully")
	return nil
}
