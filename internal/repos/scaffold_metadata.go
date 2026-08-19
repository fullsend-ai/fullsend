package repos

import (
	"context"
	"fmt"
	"regexp"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// ScaffoldPRMetadata holds commit/PR metadata for scaffold file delivery.
// Values differ based on whether this is a fresh install or a version upgrade.
type ScaffoldPRMetadata struct {
	CommitMsg string
	PRTitle   string
	PRBody    string
	Branch    string

	// DisallowPRFallback, when true, makes direct delivery fail with an
	// error when branch protection blocks the push, instead of silently
	// falling back to PR delivery. Uninstall flows set this: --direct is
	// the documented escape hatch for repos whose deployed shim predates
	// the ScaffoldUninstallBranch self-dispatch exclusion, so a silent PR
	// fallback would open exactly the PR the uninstall pre-flight exists
	// to prevent — and misreport the delivery as direct. The zero value
	// preserves the fallback for install flows, where PR delivery is
	// always safe.
	DisallowPRFallback bool
}

const (
	// defaultScaffoldPRBody is the PR body for fresh installations.
	// Only used within this package.
	defaultScaffoldPRBody = "This PR adds the fullsend scaffold files for per-repo installation.\n\n" +
		"Merge this PR to activate fullsend workflows."

	// DefaultScaffoldBranch is the branch name for fresh installations.
	// This value is also hardcoded (it cannot import a Go constant) in the
	// pull_request_target/pull_request_review self-dispatch exclusion in
	// internal/scaffold/fullsend-repo/templates/shim-per-repo.yaml and
	// shim-workflow-call.yaml, and covered by TestShimScaffoldBranchFilter
	// in internal/scaffold/workflow_call_alignment_test.go. Keep all three
	// in sync if this value ever changes.
	DefaultScaffoldBranch = "fullsend/scaffold-install"

	// ScaffoldUninstallBranch is the branch name for per-repo uninstall PRs
	// (scaffold-file deletions delivered via PR instead of --direct). Like
	// DefaultScaffoldBranch, this value is hardcoded in the shim templates'
	// self-dispatch exclusion and covered by TestShimScaffoldBranchFilter —
	// keep all three in sync if this value ever changes. It must NOT be
	// merged into DefaultScaffoldBranch: reusing the install branch would
	// let an uninstall run silently commit deletions onto an unrelated,
	// still-open install PR on that branch (closeStaleScaffoldPRs treats
	// same-branch PRs as "ours" and skips them, so it would not be closed
	// first).
	ScaffoldUninstallBranch = "fullsend/scaffold-uninstall"

	// ScaffoldBumpBranchPrefix is the branch prefix for version upgrades.
	ScaffoldBumpBranchPrefix = "fullsend/bump-"
)

// versionCommentPattern matches "# vX.Y.Z" traceability comments in
// scaffold workflow files. The version tag is captured in group 1.
var versionCommentPattern = regexp.MustCompile(`# (v\d+\.\d+\.\d+(?:-[a-zA-Z0-9.-]+)?)`)

// ScaffoldMetadataOpts holds optional pre-fetched values for
// BuildScaffoldPRMetadata. When provided, the corresponding API call is
// skipped, avoiding redundant network round-trips in batch flows.
type ScaffoldMetadataOpts struct {
	// GuardInstalled, when non-nil, overrides the guard-variable check.
	// true means the repo already has fullsend installed (upgrade path);
	// false means fresh install.
	GuardInstalled *bool

	// OldVersion, when non-empty, overrides the existing-version detection
	// from the workflow file. Only meaningful on the upgrade path.
	OldVersion string
}

// BuildScaffoldPRMetadata returns PR metadata appropriate for the operation
// type: fresh install vs. version upgrade. It checks whether the target repo
// already has fullsend installed (via the guard variable) and, for upgrades,
// attempts to detect the previous version from the existing workflow file.
//
// Callers that already know the guard state or old version can pass them via
// opts to skip the redundant API calls.
func BuildScaffoldPRMetadata(ctx context.Context, client forge.Client,
	owner, repo, upstreamTag string, opts ...ScaffoldMetadataOpts) ScaffoldPRMetadata {

	var o ScaffoldMetadataOpts
	if len(opts) > 0 {
		o = opts[0]
	}

	// Determine whether fullsend is already installed.
	installed := false
	if o.GuardInstalled != nil {
		installed = *o.GuardInstalled
	} else {
		guardVal, guardExists, err := client.GetRepoVariable(ctx, owner, repo, forge.PerRepoGuardVar)
		if err == nil && guardExists && guardVal == "true" {
			installed = true
		}
	}

	if !installed {
		return freshInstallMetadata()
	}

	// Upgrade path — guard variable exists and is "true".
	oldVersion := o.OldVersion
	if oldVersion == "" {
		oldVersion = detectExistingVersion(ctx, client, owner, repo)
	}
	return upgradeMetadata(oldVersion, upstreamTag)
}

// freshInstallMetadata returns metadata for a fresh per-repo installation.
func freshInstallMetadata() ScaffoldPRMetadata {
	return ScaffoldPRMetadata{
		CommitMsg: "chore: initialize fullsend per-repo installation",
		PRTitle:   "chore: initialize fullsend per-repo installation",
		PRBody:    defaultScaffoldPRBody,
		Branch:    DefaultScaffoldBranch,
	}
}

// upgradeMetadata returns metadata for a version upgrade. It uses the old
// and new version tags to produce descriptive commit messages and PR titles.
func upgradeMetadata(oldVersion, newVersion string) ScaffoldPRMetadata {
	switch {
	case oldVersion != "" && newVersion != "":
		return ScaffoldPRMetadata{
			CommitMsg: fmt.Sprintf("chore: bump fullsend from %s to %s", oldVersion, newVersion),
			PRTitle:   fmt.Sprintf("chore: bump fullsend from %s to %s", oldVersion, newVersion),
			PRBody: fmt.Sprintf("This PR updates the fullsend reusable workflow pin from %s to %s.",
				oldVersion, newVersion),
			Branch: ScaffoldBumpBranchPrefix + newVersion,
		}
	case newVersion != "":
		return ScaffoldPRMetadata{
			CommitMsg: fmt.Sprintf("chore: bump fullsend to %s", newVersion),
			PRTitle:   fmt.Sprintf("chore: bump fullsend to %s", newVersion),
			PRBody:    fmt.Sprintf("This PR updates the fullsend reusable workflow pin to %s.", newVersion),
			Branch:    ScaffoldBumpBranchPrefix + newVersion,
		}
	default:
		return ScaffoldPRMetadata{
			CommitMsg: "chore: update fullsend per-repo installation",
			PRTitle:   "chore: update fullsend per-repo installation",
			PRBody:    "This PR updates the fullsend scaffold files.",
			Branch:    DefaultScaffoldBranch,
		}
	}
}

// detectExistingVersion reads the per-repo shim workflow from the target
// repository and extracts the version tag from the traceability comment
// (e.g., "# v0.25.2"). Returns an empty string if the file does not exist
// or no version comment is found.
func detectExistingVersion(ctx context.Context, client forge.Client,
	owner, repo string) string {

	content, err := client.GetFileContent(ctx, owner, repo,
		".github/workflows/fullsend.yaml")
	if err != nil {
		return ""
	}
	matches := versionCommentPattern.FindSubmatch(content)
	if len(matches) > 1 {
		return string(matches[1])
	}
	return ""
}
