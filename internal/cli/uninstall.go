package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	forgegithub "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/fullsend-ai/fullsend/internal/uninstall"
)

func newUninstallCmd() *cobra.Command {
	var yolo bool

	cmd := &cobra.Command{
		Use:   "uninstall <org>",
		Short: "Remove fullsend from a GitHub organization",
		Long: `Remove fullsend from a GitHub organization by deleting the .fullsend
configuration repository, removing the GitHub App installation, and
directing you to delete the app registration.

For safety, you must type the organization name to confirm — unless
you pass --yolo.

The app name is read from .fullsend/config.yaml so the correct app
is uninstalled.

Examples:
  # Uninstall with confirmation prompt
  fullsend uninstall my-org

  # Uninstall without confirmation
  fullsend uninstall my-org --yolo`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			org := args[0]
			printer := ui.DefaultPrinter()

			token, source := resolveToken(cmd.Context())
			if token == "" {
				printer.ErrorBox("Authentication required",
					"No GitHub token found. fullsend checks these sources:\n"+
						"  1. GH_TOKEN environment variable\n"+
						"  2. GITHUB_TOKEN environment variable\n"+
						"  3. gh CLI credentials (run: gh auth login)")
				return fmt.Errorf("no GitHub token found")
			}
			printer.StepDone(fmt.Sprintf("Authenticated via %s", source))

			client := forgegithub.NewLiveClient(token)

			un := uninstall.New(
				client,
				printer,
				stdinConfirmPrompter{},
				appsetup.DefaultBrowser{},
				token,
			)

			return un.Run(cmd.Context(), uninstall.Options{
				Org:  org,
				Yolo: yolo,
			})
		},
	}

	cmd.Flags().BoolVar(&yolo, "yolo", false,
		"Skip confirmation prompt (dangerous)")

	return cmd
}

// stdinConfirmPrompter implements uninstall.Prompter using stdin.
type stdinConfirmPrompter struct{}

func (stdinConfirmPrompter) ConfirmWithInput(prompt, expected string) (bool, error) {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(line) == expected, nil
}

// Ensure stdinConfirmPrompter satisfies the interface at compile time.
var _ uninstall.Prompter = stdinConfirmPrompter{}

// Ensure DefaultBrowser satisfies the uninstall.BrowserOpener interface.
var _ uninstall.BrowserOpener = appsetup.DefaultBrowser{}
