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
	// gettingStartedCatalog documents the primary /fs-* slash commands so users
	// discover them at their first touchpoint — the fresh-install PR. It mirrors
	// the per-org onboarding catalog (GETTING_STARTED_SECTION in
	// scripts/reconcile-repos.sh); both surfaces are independently pinned to
	// dispatch.yml's routing (per-repo by TestPerRepoOnboardingCatalog, per-org by
	// TestReconcileReposSlashCommandCatalog) so they cannot drift apart. See #2165.
	gettingStartedCatalog = "\n\n## Getting started\n\n" +
		"Once this PR is merged, interact with fullsend by commenting one of these " +
		"slash commands. The supported target (issue and/or pull request) is shown for each:\n\n" +
		"- `/fs-triage` (issue or PR) — Invoke the [triage](https://fullsend.sh/docs/agents/triage) agent to categorize, label, and assess an issue.\n" +
		"- `/fs-code` (issue only) — Invoke the [code](https://fullsend.sh/docs/agents/code) agent to implement a fix for an issue and open a PR.\n" +
		"- `/fs-review` (PR only) — Invoke the [review](https://fullsend.sh/docs/agents/review) agent to review a pull request.\n" +
		"- `/fs-fix` (PR only) — Invoke the [fix](https://fullsend.sh/docs/agents/fix) agent to address review feedback on a pull request.\n" +
		"- `/fs-retro` (issue or PR) — Invoke the [retro](https://fullsend.sh/docs/agents/retro) agent to analyze completed work and propose improvements.\n" +
		"- `/fs-prioritize` (issue or PR) — Invoke the [prioritize](https://fullsend.sh/docs/agents/prioritize) agent to score an issue for project board ranking."

	// defaultScaffoldPRBody is the PR body for fresh installations.
	// Only used within this package.
	defaultScaffoldPRBody = "This PR adds the fullsend scaffold files for per-repo installation.\n\n" +
		"Merge this PR to activate fullsend workflows." + gettingStartedCatalog

	// DefaultScaffoldBranch is the branch name for fresh installations. It
	// is also reused for uninstall PR delivery (see UninstallPRMetadata)
	// since already-deployed per-repo shims only exclude this branch name
	// from triggering the fullsend dispatch job.
	DefaultScaffoldBranch = "fullsend/scaffold-install"

	// defaultUninstallPRBody is the PR body for uninstall file removals.
	defaultUninstallPRBody = "This PR removes the fullsend scaffold files from this repository.\n\n" +
		"Merge this PR to complete the workflow teardown. Repository variables " +
		"and secrets are removed separately by `repos uninstall` and do not " +
		"require this PR to merge first."

	// ScaffoldBumpBranchPrefix is the branch prefix for version upgrades.
	ScaffoldBumpBranchPrefix = "fullsend/bump-"
)

// versionCommentPattern matches "# vX.Y.Z" traceability comments in
// scaffold workflow files. The version tag is captured in group 1.
var versionCommentPattern = regexp.MustCompile(`# (v\d+\.\d+\.\d+(?:-[a-zA-Z0-9.-]+)?)`)

// ScaffoldMetadataOpts holds pre-fetched values for BuildScaffoldPRMetadata.
type ScaffoldMetadataOpts struct {
	// GuardInstalled indicates whether the repo already has fullsend
	// installed (true = upgrade path, false = fresh install). When nil,
	// the function defaults to fresh-install metadata.
	GuardInstalled *bool

	// OldVersion, when non-empty, overrides the existing-version detection
	// from the workflow file. Only meaningful on the upgrade path.
	OldVersion string
}

// BuildScaffoldPRMetadata returns PR metadata appropriate for the operation
// type: fresh install vs. version upgrade. Callers must indicate whether
// the repo is already installed via opts.GuardInstalled; when nil the
// function defaults to fresh-install metadata.
func BuildScaffoldPRMetadata(ctx context.Context, client forge.Client,
	owner, repo, upstreamTag string, opts ...ScaffoldMetadataOpts) ScaffoldPRMetadata {

	var o ScaffoldMetadataOpts
	if len(opts) > 0 {
		o = opts[0]
	}

	installed := o.GuardInstalled != nil && *o.GuardInstalled

	if !installed {
		return freshInstallMetadata()
	}

	// Upgrade path — repo was already installed.
	oldVersion := o.OldVersion
	if oldVersion == "" {
		oldVersion = detectExistingVersion(ctx, client, owner, repo)
	}
	return upgradeMetadata(oldVersion, upstreamTag)
}

// UninstallPRMetadata returns commit/PR metadata for scaffold file removal.
//
// Branch intentionally reuses DefaultScaffoldBranch rather than a distinct
// uninstall branch name: already-deployed per-repo shims (see
// internal/scaffold/fullsend-repo/templates/shim-per-repo.yaml and
// shim-workflow-call.yaml) only skip dispatch for
// head.ref == "fullsend/scaffold-install". A separate uninstall branch name
// would fail open and let the teardown PR trigger the fullsend dispatch job
// (with live WIF/mint credentials) against itself.
//
// Known limitation: because install and uninstall share a branch, an
// in-flight PR from one operation is not closed or relabeled when the other
// operation runs against the same repo (closeStaleScaffoldPRs in
// internal/layers/commit.go skips the current branch, and
// commitBranchAndPR treats an "already exists" PR as success without
// updating its title or body). If `repos install`/`converge` and
// `repos uninstall` race on the same repo, the surviving PR's title and body
// can describe the opposite of what its diff now does (e.g., an
// "initialize fullsend" PR whose diff deletes the workflow files, or vice
// versa). This is accepted for now — see docs/cli/repos.md's `repos
// uninstall` section — rather than adding branch-content detection or a
// forge "update PR title/body" call.
func UninstallPRMetadata() ScaffoldPRMetadata {
	return ScaffoldPRMetadata{
		CommitMsg: "chore: remove fullsend workflow",
		PRTitle:   "chore: remove fullsend workflow",
		PRBody:    defaultUninstallPRBody,
		Branch:    DefaultScaffoldBranch,
	}
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

// RuntimeSection renders the "Runtime" section appended to the scaffold PR
// body so the reviewer of a setup PR sees which agent runtime the repo will
// use and how to change it. An empty runtime means the default.
func RuntimeSection(runtime string) string {
	if runtime == "" {
		runtime = "claude"
	}
	return "\n\n## Runtime\n\n" +
		fmt.Sprintf("Agents in this repository run on **%s**", runtime) +
		" (`runtime:` in `.fullsend/config.yaml`). To change it later, edit that key, " +
		"re-run `fullsend github setup <owner/repo> --runtime <claude|pi|codex>`, or override a " +
		"single run with `fullsend run --runtime`. To put one agent on another runtime or model, " +
		"set runtime/model/effort on its `agents:` entry in the same file (`fullsend agent set <name> --runtime pi`). " +
		"See https://github.com/fullsend-ai/fullsend/blob/main/docs/runtimes.md."
}
