package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/entrypoint"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
)

func newEntrypointCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "entrypoint",
		Short: "Run a pipeline stage",
		Long:  "Entrypoint commands are invoked by the CI/CD pipeline to execute agent stages.",
	}
	var scm string
	cmd.PersistentFlags().StringVar(&scm, "scm", "github", "SCM backend")
	cmd.AddCommand(newCodeStageCmd(&scm))
	return cmd
}

func newCodeStageCmd(scm *string) *cobra.Command {
	return &cobra.Command{
		Use:     "code",
		Aliases: []string{"implementation"},
		Short:   "Run the code agent stage",
		Long:    "Runs the code agent against a pre-checked-out workspace, validates output, and opens a PR.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if *scm != "github" {
				return fmt.Errorf("unsupported SCM backend: %s", *scm)
			}

			env, err := entrypoint.LoadEnv()
			if err != nil {
				return fmt.Errorf("load environment: %w", err)
			}

			client := gh.New(env.BotToken)
			runner := &entrypoint.ExecRunner{}
			safeEnv := entrypoint.SanitizeEnv(os.Environ())

			result, err := entrypoint.RunCode(cmd.Context(), env, runner, client, safeEnv)
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Branch: %s\nPR: %s\n", result.Branch, result.PRURL)
			return nil
		},
	}
}
