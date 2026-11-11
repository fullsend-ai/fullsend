package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/artifacts"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

func newStageArtifactsCmd() *cobra.Command {
	var (
		outputDir   string
		stagedDir   string
		fullsendDir string
		eventFile   string
	)
	cmd := &cobra.Command{
		Use:   "stage-artifacts",
		Short: "Build the staged copy of a run's output that CI uploads",
		Long: `Resolve the run artifact level from config.yaml (artifacts.content and
artifacts.by_source, ADR 0161) for the run's source, then copy what that
level allows from --output-dir into --artifact-dir. CI uploads only the
staged directory; --output-dir is never modified.

  all       everything in --output-dir (the default)
  metadata  only metrics.json, eval-measurements.jsonl and
            run-telemetry.jsonl (with content capture stripped) from
            each run directory
  none      nothing; CI skips the upload

The run's source is the normalized event's source.system, read from
--event-file or, without it, from the dispatch payload fullsend run reads.
A source with no by_source entry gets artifacts.content.

Prints the resolved level on stdout. Fails closed: when the level cannot
be resolved or the copy fails, --artifact-dir is left empty and the command
exits non-zero.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			level, err := runStageArtifacts(outputDir, stagedDir, fullsendDir, eventFile, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), level)
			return err
		},
	}
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "run output directory written by fullsend run (required)")
	cmd.Flags().StringVar(&stagedDir, "artifact-dir", "", "directory to build the staged copy in; emptied first (required)")
	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", ".", "fullsend config directory holding config.yaml")
	cmd.Flags().StringVar(&eventFile, "event-file", "", "normalized event JSON passed to fullsend run, if any")
	_ = cmd.MarkFlagRequired("output-dir")
	_ = cmd.MarkFlagRequired("artifact-dir")
	return cmd
}

// runStageArtifacts empties stagedDir, resolves the artifact level and
// stages the copy. Any failure after the directory is emptied leaves it
// empty, so the raw output is never uploaded in its place.
func runStageArtifacts(outputDir, stagedDir, fullsendDir, eventFile string, stderr io.Writer) (string, error) {
	if err := artifacts.PrepareStagedDir(outputDir, stagedDir); err != nil {
		return "", err
	}
	level, err := resolveArtifactLevel(fullsendDir, eventFile)
	if err != nil {
		fmt.Fprintf(stderr, "fullsend: artifact level could not be resolved; nothing is staged: %v\n", err)
		return "", fmt.Errorf("resolving artifact level: %w", err)
	}
	if err := artifacts.Stage(outputDir, stagedDir, level); err != nil {
		fmt.Fprintf(stderr, "fullsend: staging run artifact failed; nothing is staged: %v\n", err)
		return "", fmt.Errorf("staging artifacts: %w", err)
	}
	return level, nil
}

// resolveArtifactLevel loads config.yaml the way fullsend run does for
// runtime selection and resolves the level for the run's source.
func resolveArtifactLevel(fullsendDir, eventFile string) (string, error) {
	absFullsendDir, err := filepath.Abs(fullsendDir)
	if err != nil {
		return "", err
	}
	source, err := artifactRunSource(absFullsendDir, eventFile)
	if err != nil {
		return "", err
	}
	rc, err := loadRunConfig(filepath.Join(absFullsendDir, config.OverlayConfigFile))
	if err != nil {
		return "", err
	}
	return config.ResolveArtifactContent(rc.perRepo, source)
}

// artifactRunSource returns the run's event.source.system, from the same
// inputs fullsend run reads: --event-file when given, else the dispatch
// payload. An empty result means the run has no normalized event (for
// example a manual local run). A dispatch payload that claims a Jira
// source without a valid normalized event is an error, as in run.
func artifactRunSource(absFullsendDir, eventFile string) (string, error) {
	if eventFile != "" {
		data, err := os.ReadFile(eventFile)
		if err != nil {
			return "", fmt.Errorf("reading event file: %w", err)
		}
		ev, err := normevent.ParseJSON(data)
		if err != nil {
			return "", fmt.Errorf("parsing event file: %w", err)
		}
		return string(ev.Source.System), nil
	}
	var source string
	if eventMap := extractNormalizedEventFromDispatch(absFullsendDir); eventMap != nil {
		source = extractMapString(eventMap, "source", "system")
	}
	if dispatchJiraClaimUnresolved(eventFile, source, absFullsendDir) {
		return "", errors.New("dispatch payload declares a Jira source but its normalized event is invalid")
	}
	return source, nil
}
