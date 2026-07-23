package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/poll"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

func newPollCmd() *cobra.Command {
	var (
		forgeFlag    string
		projectPath  string
		gitlabURL    string
		outputPath   string
		pollModeFlag string
		fullsendDir  string
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

			glClient, err := gitlab.New(forgeToken, gitlab.WithBaseURL(gitlabURL))
			if err != nil {
				return fmt.Errorf("create GitLab client: %w", err)
			}
			pollClient := gitlab.NewPollClient(glClient)

			botUserID, err := pollClient.GetAuthenticatedUserID(cmd.Context())
			if err != nil {
				return fmt.Errorf("resolve bot user ID: %w", err)
			}

			// Build the event router from config + scaffold agents.
			router, err := buildRouter(fullsendDir)
			if err != nil {
				return fmt.Errorf("build event router: %w", err)
			}

			opts := poll.Options{
				SlashCommandsOnly: slashCommandsOnly,
				BotUserID:         botUserID,
				OutputPath:        outputPath,
				GitLabURL:         gitlabURL,
			}

			poller := poll.New(pollClient, router, projectPath, opts)
			return poller.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&forgeFlag, "forge", "", "Forge platform (required: gitlab)")
	_ = cmd.MarkFlagRequired("forge")
	cmd.Flags().StringVar(&projectPath, "project", "", "GitLab project path (default: $CI_PROJECT_PATH)")
	cmd.Flags().StringVar(&gitlabURL, "gitlab-url", "https://gitlab.com", "GitLab instance URL")
	cmd.Flags().StringVar(&outputPath, "output", "", "Path to write dispatches JSON")
	cmd.Flags().StringVar(&pollModeFlag, "poll-mode", "", "Poll mode: fast (slash commands only) or full")
	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "base directory containing the .fullsend layout")
	_ = cmd.MarkFlagRequired("fullsend-dir")

	cmd.Hidden = true
	cmd.AddCommand(newPollGenerateChildPipelineCmd())
	return cmd
}

// buildRouter constructs a HarnessRouter from the merged set of
// scaffold default agents and config-registered agents.
func buildRouter(fullsendDir string) (*dispatch.HarnessRouter, error) {
	cfg, err := config.LoadConfig(fullsendDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	scaffoldNames, err := scaffold.HarnessNames()
	if err != nil {
		return nil, fmt.Errorf("list scaffold harnesses: %w", err)
	}

	nameOnly := func(string, string) (string, error) { return "", nil }
	merged, err := config.MergedAgents(scaffoldNames, "-", cfg.AgentEntries(), nameOnly)
	if err != nil {
		return nil, fmt.Errorf("merge agents: %w", err)
	}

	names := make([]string, 0, len(merged))
	for _, a := range merged {
		names = append(names, a.Name)
	}

	return dispatch.NewHarnessRouter(names), nil
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
