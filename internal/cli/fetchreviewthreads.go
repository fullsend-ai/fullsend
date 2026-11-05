package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/spf13/cobra"
)

func newFetchReviewThreadsCmd() *cobra.Command {
	var (
		repo      string
		pr        int
		forgeName string
		token     string
		baseURL   string
	)

	cmd := &cobra.Command{
		Use:   "fetch-review-threads",
		Short: "Fetch pull-request review threads through the forge client",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if pr <= 0 {
				return fmt.Errorf("--pr must be a positive integer, got %d", pr)
			}
			parts := strings.SplitN(repo, "/", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return fmt.Errorf("--repo must be in owner/repo format, got %q", repo)
			}

			client, err := resolvePostReviewClient(forgeName, token, baseURL)
			if err != nil {
				return err
			}
			threads, err := client.ListPullRequestReviewThreads(cmd.Context(), parts[0], parts[1], pr)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(threads)
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "repository in owner/repo format (required)")
	cmd.Flags().IntVar(&pr, "pr", 0, "pull request number (required)")
	cmd.Flags().StringVar(&forgeName, "forge", repos.ForgeGitHub, "forge backend (github or gitlab)")
	cmd.Flags().StringVar(&token, "token", "", "forge token (defaults to the forge environment token)")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "forge API base URL")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("pr")
	return cmd
}
