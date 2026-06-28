package cli

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/mintclient"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

var mintTokenPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func newMintTokenCmd() *cobra.Command {
	var (
		role     string
		repos    string
		mintURL  string
		audience string
	)

	cmd := &cobra.Command{
		Use:   "token",
		Short: "Mint a short-lived GitHub installation token via OIDC",
		Long: `Exchange a GitHub Actions OIDC token for a role-scoped installation token
via the fullsend token mint service. Requires id-token: write permission
on the calling GitHub Actions job (no GCP access needed).

Prints the token to stdout for capture in shell scripts.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if mintURL == "" {
				mintURL = os.Getenv("FULLSEND_MINT_URL")
			}
			if mintURL == "" {
				return fmt.Errorf("--mint-url or FULLSEND_MINT_URL required")
			}

			repoList := strings.Split(repos, ",")
			var filtered []string
			for _, r := range repoList {
				if trimmed := strings.TrimSpace(r); trimmed != "" {
					filtered = append(filtered, trimmed)
				}
			}
			if len(filtered) == 0 {
				return fmt.Errorf("--repos must contain at least one repo name")
			}
			for _, r := range filtered {
				if !mintcore.RepoNamePattern.MatchString(r) {
					return fmt.Errorf("invalid repo name %q: must match %s", r, mintcore.RepoNamePattern.String())
				}
			}

			role = resolveRole(role)
			if err := mintcore.ValidateRoleName(role); err != nil {
				return fmt.Errorf("invalid role: %w", err)
			}

			result, err := mintclient.MintToken(cmd.Context(), mintclient.MintRequest{
				MintURL:  mintURL,
				Role:     role,
				Repos:    filtered,
				Audience: audience,
			})
			if err != nil {
				return fmt.Errorf("minting token: %w", err)
			}

			if !mintTokenPattern.MatchString(result.Token) {
				return fmt.Errorf("mint returned token with unexpected characters")
			}

			if os.Getenv("GITHUB_ACTIONS") == "true" {
				fmt.Fprintf(os.Stderr, "::add-mask::%s\n", result.Token)
			}

			fmt.Fprint(cmd.OutOrStdout(), result.Token)
			return nil
		},
	}

	cmd.Flags().StringVar(&role, "role", "", "agent role name (e.g. triage, coder, review)")
	cmd.Flags().StringVar(&repos, "repos", "", "comma-separated repo names to scope the token to")
	cmd.Flags().StringVar(&mintURL, "mint-url", "", "mint service URL (default: $FULLSEND_MINT_URL)")
	cmd.Flags().StringVar(&audience, "audience", "fullsend-mint", "OIDC audience claim")
	_ = cmd.MarkFlagRequired("role")
	_ = cmd.MarkFlagRequired("repos")

	return cmd
}
