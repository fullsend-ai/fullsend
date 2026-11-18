package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/selfupdate"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// selfUpdater runs a self-update. *selfupdate.Updater implements it.
type selfUpdater interface {
	Run(ctx context.Context, opts selfupdate.Options) (selfupdate.Result, error)
}

// newSelfUpdaterFn builds the updater the self-update command runs.
// Override in tests to return a fake.
var newSelfUpdaterFn = func() selfUpdater { return selfupdate.New() }

func newSelfUpdateCmd() *cobra.Command {
	var targetVersion string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "self-update",
		Short: "Update this fullsend binary to the latest release",
		Long: `Replace the running fullsend binary with a release from GitHub Releases.

The release's checksums.txt must carry a valid Sigstore signature from the
fullsend release workflow for that exact tag, and the downloaded archive must
match its recorded SHA256. If either check fails, the binary is left as is.

This command only updates the local CLI binary. To update the fullsend
workflows and configuration installed in a repository, use
"fullsend repos install".`,
		Example: `  # Update to the latest release
  fullsend self-update

  # Show what would change without downloading anything
  fullsend self-update --dry-run

  # Roll back to a specific release (v0.19.0 or later). If it came out
  # before self-update was added, update by hand from then on.
  fullsend self-update --version v0.52.0`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSelfUpdate(cmd, targetVersion, dryRun)
		},
	}

	cmd.Flags().StringVar(&targetVersion, "version", "", "release to install, such as v0.53.0 (default: the latest release)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be updated without replacing anything; with --version, check that the release exists")

	return cmd
}

func runSelfUpdate(cmd *cobra.Command, targetVersion string, dryRun bool) error {
	printer := ui.New(cmd.OutOrStdout())

	res, err := newSelfUpdaterFn().Run(cmd.Context(), selfupdate.Options{
		CurrentVersion: version,
		TargetVersion:  targetVersion,
		DryRun:         dryRun,
	})
	if err != nil {
		return err
	}

	switch res.Status {
	case selfupdate.StatusUpToDate:
		printer.StepDone(fmt.Sprintf("fullsend %s is already installed", res.To))
	case selfupdate.StatusAhead:
		printer.StepInfo(fmt.Sprintf("fullsend %s is newer than the latest release (%s); nothing to do", res.From, res.To))
	case selfupdate.StatusWouldUpdate:
		printer.StepInfo(fmt.Sprintf("Would update fullsend %s → %s (%s)", res.From, res.To, res.Path))
	case selfupdate.StatusUpdated:
		printer.StepDone(fmt.Sprintf("Updated fullsend %s → %s (%s)", res.From, res.To, res.Path))
	}
	return nil
}
