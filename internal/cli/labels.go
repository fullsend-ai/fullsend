package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newLabelsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "labels",
		Short: "Manage issue and pull request labels",
	}
	cmd.AddCommand(newLabelsEnsureCmd())
	cmd.AddCommand(newLabelsCopyCmd())
	return cmd
}

func newLabelsEnsureCmd() *cobra.Command {
	var (
		repo   string
		number int
		action string
		label  string
		token  string
	)

	cmd := &cobra.Command{
		Use:   "ensure",
		Short: "Add or remove a label on an issue or pull request",
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(os.Stdout)
			if token == "" {
				token = os.Getenv("GITHUB_TOKEN")
			}
			if token == "" {
				return fmt.Errorf("--token or GITHUB_TOKEN required")
			}
			if number <= 0 {
				return fmt.Errorf("--number must be a positive integer, got %d", number)
			}
			if !labelNamePattern.MatchString(label) {
				return fmt.Errorf("invalid label name %q", label)
			}
			parts := strings.SplitN(repo, "/", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return fmt.Errorf("--repo must be in owner/repo format, got %q", repo)
			}
			owner, repoName := parts[0], parts[1]

			client := gh.New(token)
			printer.Header("Labels Ensure")
			switch action {
			case "add":
				printer.StepStart(fmt.Sprintf("Adding label %q to #%d", label, number))
				if err := client.AddIssueLabels(cmd.Context(), owner, repoName, number, label); err != nil {
					return err
				}
				printer.StepDone("Label added")
			case "remove":
				printer.StepStart(fmt.Sprintf("Removing label %q from #%d", label, number))
				if err := client.RemoveIssueLabel(cmd.Context(), owner, repoName, number, label); err != nil {
					return err
				}
				printer.StepDone("Label removed")
			default:
				return fmt.Errorf("--action must be add or remove, got %q", action)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "repository in owner/repo format (required)")
	cmd.Flags().IntVar(&number, "number", 0, "issue or pull request number (required)")
	cmd.Flags().StringVar(&action, "action", "", "add or remove (required)")
	cmd.Flags().StringVar(&label, "label", "", "label name (required)")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token (default: $GITHUB_TOKEN)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("number")
	_ = cmd.MarkFlagRequired("action")
	_ = cmd.MarkFlagRequired("label")

	return cmd
}

func newLabelsCopyCmd() *cobra.Command {
	var (
		repo       string
		fromNumber int
		toNumber   int
		prefix     string
		token      string
	)

	cmd := &cobra.Command{
		Use:   "copy",
		Short: "Copy labels matching a prefix from one issue/PR to another",
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(os.Stdout)
			if token == "" {
				token = os.Getenv("GITHUB_TOKEN")
			}
			if token == "" {
				return fmt.Errorf("--token or GITHUB_TOKEN required")
			}
			if fromNumber <= 0 || toNumber <= 0 {
				return fmt.Errorf("--from and --to must be positive integers")
			}
			parts := strings.SplitN(repo, "/", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return fmt.Errorf("--repo must be in owner/repo format, got %q", repo)
			}
			owner, repoName := parts[0], parts[1]

			client := gh.New(token)
			source, err := client.GetIssue(cmd.Context(), owner, repoName, fromNumber)
			if err != nil {
				return fmt.Errorf("reading source #%d: %w", fromNumber, err)
			}

			var toCopy []string
			for _, l := range source.Labels {
				if strings.HasPrefix(l, prefix) {
					toCopy = append(toCopy, l)
				}
			}
			if len(toCopy) == 0 {
				printer.StepInfo("No matching labels to copy")
				return nil
			}

			printer.Header("Labels Copy")
			printer.StepStart(fmt.Sprintf("Copying %d label(s) from #%d to #%d", len(toCopy), fromNumber, toNumber))
			if err := client.AddIssueLabels(cmd.Context(), owner, repoName, toNumber, toCopy...); err != nil {
				return err
			}
			printer.StepDone("Labels copied")
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "repository in owner/repo format (required)")
	cmd.Flags().IntVar(&fromNumber, "from", 0, "source issue or PR number (required)")
	cmd.Flags().IntVar(&toNumber, "to", 0, "destination issue or PR number (required)")
	cmd.Flags().StringVar(&prefix, "prefix", "workflow-change-", "copy labels with this prefix")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token (default: $GITHUB_TOKEN)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("to")

	return cmd
}
