package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/poll"
)

func newPollCmd() *cobra.Command {
	var (
		forgeFlag    string
		projectPath  string
		gitlabURL    string
		outputPath   string
		pollModeFlag string
	)

	cmd := &cobra.Command{
		Use:   "poll",
		Short: "Poll GitLab API for new events and dispatch agent stages",
		RunE: func(cmd *cobra.Command, args []string) error {
			if forgeFlag != "gitlab" {
				return fmt.Errorf("poll command currently supports --forge gitlab only (got %q)", forgeFlag)
			}

			forgeToken := os.Getenv("FULLSEND_FORGE_TOKEN")
			if forgeToken == "" {
				return fmt.Errorf("FULLSEND_FORGE_TOKEN is required")
			}

			if projectPath == "" {
				projectPath = os.Getenv("CI_PROJECT_PATH")
			}
			if projectPath == "" {
				return fmt.Errorf("--project or CI_PROJECT_PATH is required")
			}

			slashCommandsOnly := pollModeFlag == "fast" || os.Getenv("FULLSEND_POLL_MODE") == "fast"

			// The GitLab client is not yet implemented (Phase 1).
			// This command will be fully wired when the GitLab forge
			// client provides a type satisfying poll.GitLabClient.
			_ = forgeToken
			_ = gitlabURL

			var botUserID int
			// botUserID will be resolved via client.GetAuthenticatedUser
			// once the GitLab client is available.

			opts := poll.Options{
				SlashCommandsOnly: slashCommandsOnly,
				BotUserID:         botUserID,
				OutputPath:        outputPath,
				GitLabURL:         gitlabURL,
			}

			// TODO(phase1): Replace nil client and router with real
			// implementations once the GitLab forge client exists.
			poller := poll.New(nil, nil, projectPath, opts)
			return poller.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&forgeFlag, "forge", "", "Forge platform (required: gitlab)")
	_ = cmd.MarkFlagRequired("forge")
	cmd.Flags().StringVar(&projectPath, "project", "", "GitLab project path (default: $CI_PROJECT_PATH)")
	cmd.Flags().StringVar(&gitlabURL, "gitlab-url", "https://gitlab.com", "GitLab instance URL")
	cmd.Flags().StringVar(&outputPath, "output", "", "Path to write dispatches JSON")
	cmd.Flags().StringVar(&pollModeFlag, "poll-mode", "", "Poll mode: fast (slash commands only) or full")

	cmd.Hidden = true
	cmd.AddCommand(newPollGenerateChildPipelineCmd())
	return cmd
}

func newPollGenerateChildPipelineCmd() *cobra.Command {
	var (
		dispatchesPath string
		outputPath     string
	)

	cmd := &cobra.Command{
		Use:   "generate-child-pipeline",
		Short: "Generate child pipeline YAML from dispatches JSON",
		RunE: func(cmd *cobra.Command, args []string) error {
			return poll.GenerateChildPipelineFromFile(dispatchesPath, outputPath)
		},
	}

	cmd.Flags().StringVar(&dispatchesPath, "dispatches", "dispatches.json", "Path to dispatches JSON file")
	cmd.Flags().StringVar(&outputPath, "output", "child-pipeline.yml", "Path to write child pipeline YAML")

	return cmd
}
