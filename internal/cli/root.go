package cli

import (
	"context"
	"strings"

	"github.com/spf13/cobra"
)

var version = "dev"
var commitSHA = "dev"

// Version returns the CLI version string set at build time.
func Version() string {
	return version
}

// CommitSHA returns the git commit SHA set at build time.
func CommitSHA() string {
	return commitSHA
}

// resolveBuildVersion returns the commit SHA and normalized version tag
// from build-time ldflags. Release builds (commitSHA is a real SHA)
// return ("abc123def", "v0.42.0"). Dev builds return ("", "").
// This is the single source of truth for CLI version resolution — both
// scaffold pinning (resolveUpstreamRef) and agents-repo pinning
// (resolveAgentsRef) derive their values from it.
func resolveBuildVersion() (sha, tag string) {
	if commitSHA != "" && commitSHA != "dev" {
		v := strings.TrimPrefix(version, "v")
		if v == "" || v == "dev" {
			return "", ""
		}
		return commitSHA, "v" + v
	}
	return "", ""
}

// resolveUpstreamRef returns the SHA and version tag for pinning scaffold
// workflow refs. Delegates to resolveBuildVersion; dev builds return empty
// strings, causing the render layer to fall back to config.DefaultUpstreamRef.
func resolveUpstreamRef() (ref, tag string) {
	return resolveBuildVersion()
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "fullsend",
		Short:         "Autonomous agentic development for Git-hosted organizations",
		Long:          "fullsend automates the setup and management of agentic development pipelines for Git-hosted organizations.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	cmd.AddCommand(newAgentCmd())
	cmd.AddCommand(newAdminCmd())
	cmd.AddCommand(newGitHubCmd())
	cmd.AddCommand(newInferenceCmd())
	cmd.AddCommand(newLockCmd())
	cmd.AddCommand(newMintCmd())
	cmd.AddCommand(newFetchSkillCmd())
	cmd.AddCommand(newDispatchCmd())
	cmd.AddCommand(newRunCmd())
	cmd.AddCommand(newScanCmd())
	cmd.AddCommand(newReposCmd())
	cmd.AddCommand(newPostReviewCmd())
	cmd.AddCommand(newIssuesCmd())
	cmd.AddCommand(newPostCommentCmd())
	cmd.AddCommand(newReconcileStatusCmd())
	cmd.AddCommand(newPollCmd())
	cmd.AddCommand(newEvalMeasureCmd())
	return cmd
}

// Execute runs the root command with the given context.
func Execute(ctx context.Context) error {
	return newRootCmd().ExecuteContext(ctx)
}
