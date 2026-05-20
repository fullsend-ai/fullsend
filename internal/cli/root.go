package cli

import (
	"strings"

	"github.com/spf13/cobra"
)

var version = "dev"
var buildSHA = "dev"

// Version returns the CLI version string set at build time.
func Version() string {
	return version
}

// BuildSHA returns the git commit SHA embedded at build time.
func BuildSHA() string {
	return buildSHA
}

// FullsendRef returns the ref string used to pin scaffold uses: lines.
// For release builds: "<sha>  # v<version>".
// For dev builds: "<sha>  # main (dev)".
func FullsendRef() string {
	sha := resolvedBuildSHA()
	if isReleasedVersion(version) {
		v := version
		if !strings.HasPrefix(v, "v") {
			v = "v" + v
		}
		return sha + "  # " + v
	}
	return sha + "  # main (dev)"
}

// resolvedBuildSHA returns buildSHA when set via ldflags (goreleaser /
// make go-build). For unset dev builds (go run) it returns "main" so that
// the scaffolded ref resolves to the live HEAD of the main branch rather
// than an invalid placeholder.
func resolvedBuildSHA() string {
	if buildSHA != "dev" {
		return buildSHA
	}
	return "main"
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "fullsend",
		Short:         "Autonomous agentic development for GitHub organizations",
		Long:          "fullsend automates the setup and management of agentic development pipelines for GitHub organizations.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	cmd.AddCommand(newAdminCmd())
	cmd.AddCommand(newRunCmd())
	cmd.AddCommand(newScanCmd())
	cmd.AddCommand(newPostReviewCmd())
	cmd.AddCommand(newPostCommentCmd())
	return cmd
}

// Execute runs the root command.
func Execute() error {
	return newRootCmd().Execute()
}
