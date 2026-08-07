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
}

const (
	// DefaultScaffoldPRBody is the PR body for fresh installations.
	DefaultScaffoldPRBody = "This PR adds the fullsend scaffold files for per-repo installation.\n\n" +
		"Merge this PR to activate fullsend workflows."

	// DefaultScaffoldBranch is the branch name for fresh installations.
	DefaultScaffoldBranch = "fullsend/scaffold-install"

	// scaffoldBumpBranchPrefix is the branch prefix for version upgrades.
	scaffoldBumpBranchPrefix = "fullsend/bump-"
)

// versionCommentPattern matches "# vX.Y.Z" traceability comments in
// scaffold workflow files. The version tag is captured in group 1.
var versionCommentPattern = regexp.MustCompile(`# (v\d+\.\d+\.\d+(?:-[a-zA-Z0-9.]+)?)`)

// BuildScaffoldPRMetadata returns PR metadata appropriate for the operation
// type: fresh install vs. version upgrade. It checks whether the target repo
// already has fullsend installed (via the guard variable) and, for upgrades,
// attempts to detect the previous version from the existing workflow file.
func BuildScaffoldPRMetadata(ctx context.Context, client forge.Client,
	owner, repo, upstreamTag string) ScaffoldPRMetadata {

	guardVal, guardExists, err := client.GetRepoVariable(ctx, owner, repo, forge.PerRepoGuardVar)
	if err != nil || !guardExists || guardVal != "true" {
		// Fresh install.
		return freshInstallMetadata()
	}

	// Upgrade path — guard variable exists and is "true".
	oldVersion := detectExistingVersion(ctx, client, owner, repo)
	return upgradeMetadata(oldVersion, upstreamTag)
}

// freshInstallMetadata returns metadata for a fresh per-repo installation.
func freshInstallMetadata() ScaffoldPRMetadata {
	return ScaffoldPRMetadata{
		CommitMsg: "chore: initialize fullsend per-repo installation",
		PRTitle:   "chore: initialize fullsend per-repo installation",
		PRBody:    DefaultScaffoldPRBody,
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
			Branch: scaffoldBumpBranchPrefix + newVersion,
		}
	case newVersion != "":
		return ScaffoldPRMetadata{
			CommitMsg: fmt.Sprintf("chore: bump fullsend to %s", newVersion),
			PRTitle:   fmt.Sprintf("chore: bump fullsend to %s", newVersion),
			PRBody:    fmt.Sprintf("This PR updates the fullsend reusable workflow pin to %s.", newVersion),
			Branch:    scaffoldBumpBranchPrefix + newVersion,
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
