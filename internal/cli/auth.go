package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/authorization"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/sticky"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

var labelNamePattern = regexp.MustCompile(`^[a-zA-Z0-9._/:\ +\-]+$`)

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authorization gate checks for elevated agent permissions",
	}
	cmd.AddCommand(newAuthCheckCmd())
	return cmd
}

func newAuthCheckCmd() *cobra.Command {
	var (
		gateName           string
		repo               string
		number             int
		phase              string
		changedFilesPath   string
		triggerCommentID   int
		jsonOut            bool
		apply              bool
		token              string
	)

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Verify label-gated authorization for an issue or PR",
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
			parts := strings.SplitN(repo, "/", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return fmt.Errorf("--repo must be in owner/repo format, got %q", repo)
			}
			owner, repoName := parts[0], parts[1]

			g := authorization.GateByName(gateName)
			if g == nil {
				return fmt.Errorf("unknown gate %q", gateName)
			}

			authPhase := authorization.Phase(phase)
			switch authPhase {
			case authorization.PhasePreRun, authorization.PhaseMint, authorization.PhasePrePush:
			default:
				return fmt.Errorf("--phase must be pre-run, mint, or pre-push, got %q", phase)
			}

			var changedFiles []string
			if changedFilesPath != "" {
				raw, err := readBody(changedFilesPath)
				if err != nil {
					return fmt.Errorf("reading changed files: %w", err)
				}
				changedFiles = authorization.ParseChangedFiles(string(raw))
			}

			client := gh.New(token)
			target := authorization.Target{Owner: owner, Repo: repoName, Number: number}
			opts := authorization.Options{
				ChangedFiles:     changedFiles,
				TriggerCommentID: triggerCommentID,
			}

			result, err := authorization.Evaluate(cmd.Context(), client, *g, target, authPhase, opts)
			if err != nil {
				return err
			}

			if result.Status == authorization.StatusOK {
				if jsonOut {
					payload := map[string]any{"status": result.Status}
					if len(result.Elevations) > 0 {
						payload["elevations"] = result.Elevations
					}
					enc := json.NewEncoder(os.Stdout)
					enc.SetEscapeHTML(false)
					if err := enc.Encode(payload); err != nil {
						return err
					}
				} else {
					for _, name := range result.Elevations {
						fmt.Println(name)
					}
				}
				return nil
			}

			if apply {
				if err := authorization.ApplyMutations(cmd.Context(), client, *g, target, result.Status); err != nil {
					return fmt.Errorf("applying label mutations: %w", err)
				}
				body := authorization.CommentBody(*g, authPhase, result.Status)
				if body != "" {
					cfg := sticky.Config{Marker: authorization.StickyMarker(*g)}
					if _, err := sticky.Post(cmd.Context(), client, owner, repoName, number, body, cfg, printer); err != nil {
						return fmt.Errorf("posting auth comment: %w", err)
					}
				}
			}

			exitCode := authExitCode(result.Status)
			return newExitError(exitCode, "%s", result.Status)
		},
	}

	cmd.Flags().StringVar(&gateName, "gate", "", "authorization gate name (required)")
	cmd.Flags().StringVar(&repo, "repo", "", "repository in owner/repo format (required)")
	cmd.Flags().IntVar(&number, "number", 0, "issue or pull request number (required)")
	cmd.Flags().StringVar(&phase, "phase", "", "check phase: pre-run, mint, or pre-push (required)")
	cmd.Flags().StringVar(&changedFilesPath, "changed-files", "", "newline-separated changed file paths, or '-' for stdin (pre-push)")
	cmd.Flags().IntVar(&triggerCommentID, "trigger-comment-id", 0, "workflow trigger comment ID to exempt from stale detection")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit structured JSON result on stdout")
	cmd.Flags().BoolVar(&apply, "apply", false, "mutate labels and post sticky comment on failure")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token (default: $GITHUB_TOKEN)")
	_ = cmd.MarkFlagRequired("gate")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("number")
	_ = cmd.MarkFlagRequired("phase")

	return cmd
}

func authExitCode(status authorization.Status) int {
	switch status {
	case authorization.StatusBlocked:
		return AuthExitBlocked
	case authorization.StatusStale, authorization.StatusUnauthorizedPush:
		return AuthExitStaleOrUnauth
	default:
		return 1
	}
}
