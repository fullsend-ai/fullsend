package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/poll"
)

func newPollCmd() *cobra.Command {
	var (
		forgeFlag    string
		projectPath  string
		gitlabURL    string
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

			// Build the event router from config + agents-repo known agents.
			router, err := buildRouter(fullsendDir)
			if err != nil {
				return fmt.Errorf("build event router: %w", err)
			}

			pipelineRef := os.Getenv("CI_COMMIT_REF_NAME")
			if pipelineRef == "" {
				pipelineRef = os.Getenv("CI_DEFAULT_BRANCH")
			}
			if pipelineRef == "" {
				return fmt.Errorf("CI_COMMIT_REF_NAME or CI_DEFAULT_BRANCH is required for pipeline dispatch")
			}

			opts := poll.Options{
				SlashCommandsOnly: slashCommandsOnly,
				BotUserID:         botUserID,
				GitLabURL:         gitlabURL,
				PipelineRef:       pipelineRef,
				PollJobURL:        os.Getenv("CI_JOB_URL"),
			}

			poller := poll.New(pollClient, router, projectPath, opts)
			return poller.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&forgeFlag, "forge", "", "Forge platform (required: gitlab)")
	_ = cmd.MarkFlagRequired("forge")
	cmd.Flags().StringVar(&projectPath, "project", "", "GitLab project path (default: $CI_PROJECT_PATH)")
	cmd.Flags().StringVar(&gitlabURL, "gitlab-url", "https://gitlab.com", "GitLab instance URL")
	cmd.Flags().StringVar(&pollModeFlag, "poll-mode", "", "Poll mode: fast (slash commands only) or full")
	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "base directory containing the .fullsend layout")
	_ = cmd.MarkFlagRequired("fullsend-dir")

	cmd.Hidden = true
	return cmd
}

// buildRouter constructs a HarnessRouter from config-registered agents
// and the known first-party agents available via agents-repo fallback.
func buildRouter(fullsendDir string) (*dispatch.HarnessRouter, error) {
	cfg, err := config.LoadConfig(fullsendDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	seen := make(map[string]bool)
	var names []string

	entries := cfg.AgentEntries()
	for i := len(entries) - 1; i >= 0; i-- {
		name := entries[i].DerivedName()
		lower := strings.ToLower(name)
		if !seen[lower] {
			seen[lower] = true
			if entries[i].IsEnabled() {
				names = append(names, name)
			}
		}
	}

	for name := range defaultAgentsRepoKnownAgents {
		if !seen[name] && !config.IsAgentExplicitlyDisabled(entries, name) {
			seen[name] = true
			names = append(names, name)
		}
	}

	return dispatch.NewHarnessRouter(names), nil
}
