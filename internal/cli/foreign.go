package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newForeignCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "foreign",
		Short: "Manage cross-org mint authorization on a target org or repo",
		Long: "Manage FULLSEND_FOREIGN_<role>_REPOS variables that authorize foreign workflows to mint tokens.\n\n" +
			"Without --repo, manages org-level variables (installation-wide grants).\n" +
			"With --repo, manages repo-level variables (repo-scoped grants).\n\n" +
			"--repo accepts owner/repo (e.g. acme/api) so --org can be omitted,\n" +
			"or --org <org> --repo <name> as the split form.",
	}
	cmd.AddCommand(newForeignAllowCmd())
	cmd.AddCommand(newForeignListCmd())
	cmd.AddCommand(newForeignRevokeCmd())
	return cmd
}

func newForeignAllowCmd() *cobra.Command {
	var org, repo string
	cmd := &cobra.Command{
		Use:   "allow",
		Short: "Authorize a foreign org/repo to mint for a role on this org or repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			role, err := cmd.Flags().GetString("role")
			if err != nil {
				return err
			}
			caller, err := cmd.Flags().GetString("caller")
			if err != nil {
				return err
			}
			target, err := resolveForeignTarget(org, repo)
			if err != nil {
				return err
			}
			if err := mintcore.ValidateRoleName(role); err != nil {
				return fmt.Errorf("invalid --role: %w", err)
			}
			if err := validateForeignCaller(caller); err != nil {
				return err
			}

			token, err := resolveToken()
			if err != nil {
				return err
			}
			client := newGitHubLiveClient(token, "")
			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			varName := mintcore.ForeignVariableName(role)

			var allowlist []string
			if target.isRepoLevel() {
				allowlist, err = loadRepoForeignAllowlist(ctx, client, target.org, target.repo, varName)
			} else {
				allowlist, err = loadForeignAllowlist(ctx, client, target.org, varName)
			}
			if err != nil {
				return err
			}

			alreadyListed := containsForeignCaller(allowlist, caller)
			if !alreadyListed {
				allowlist = append(allowlist, caller)
			}
			value := strings.Join(allowlist, ", ")

			targetLabel := target.label()
			if target.isRepoLevel() {
				printer.StepStart(fmt.Sprintf("Updating %s on repo %s", varName, targetLabel))
				if err := client.CreateOrUpdateRepoVariable(ctx, target.org, target.repo, varName, value); err != nil {
					printer.StepFail(fmt.Sprintf("Failed to update %s on repo %s", varName, targetLabel))
					return err
				}
			} else {
				if alreadyListed {
					printer.StepStart(fmt.Sprintf("Ensuring %s is org-wide on %s", varName, targetLabel))
				} else {
					printer.StepStart(fmt.Sprintf("Updating %s on %s", varName, targetLabel))
				}
				if err := client.CreateOrUpdateOrgVariableAll(ctx, target.org, varName, value); err != nil {
					printer.StepFail(fmt.Sprintf("Failed to update %s", varName))
					return err
				}
			}
			if alreadyListed {
				printer.StepDone(fmt.Sprintf("%s already lists %q on %s", varName, caller, targetLabel))
			} else {
				printer.StepDone(fmt.Sprintf("Added %q to %s on %s", caller, varName, targetLabel))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Target GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "", "Target repository (owner/repo or bare name with --org)")
	cmd.Flags().String("role", "", "Agent role (e.g. e2e)")
	cmd.Flags().String("caller", "", "Foreign caller: org/repo or bare org")
	_ = cmd.MarkFlagRequired("role")
	_ = cmd.MarkFlagRequired("caller")
	return cmd
}

func newForeignListCmd() *cobra.Command {
	var org, repo string
	var role string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List foreign caller allowlists on an org or repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := resolveForeignTarget(org, repo)
			if err != nil {
				return err
			}
			if role != "" {
				if err := mintcore.ValidateRoleName(role); err != nil {
					return fmt.Errorf("invalid --role: %w", err)
				}
			}

			token, err := resolveToken()
			if err != nil {
				return err
			}
			client := newGitHubLiveClient(token, "")
			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			targetLabel := target.label()

			if target.isRepoLevel() {
				return listRepoForeign(ctx, client, printer, target, role)
			}

			if role != "" {
				varName := mintcore.ForeignVariableName(role)
				allowlist, err := loadForeignAllowlist(ctx, client, target.org, varName)
				if err != nil {
					return err
				}
				if len(allowlist) == 0 {
					printer.StepInfo(fmt.Sprintf("%s: (not set)", varName))
					return nil
				}
				printer.StepInfo(fmt.Sprintf("%s:", varName))
				for _, entry := range allowlist {
					printer.StepInfo(fmt.Sprintf("  - %s", entry))
				}
				return nil
			}

			vars, err := client.ListOrgVariables(ctx, target.org)
			if err != nil {
				return err
			}
			var foreign []struct {
				role      string
				allowlist []string
			}
			for _, v := range vars {
				roleName, ok := parseForeignVariableName(v.Name)
				if !ok {
					continue
				}
				foreign = append(foreign, struct {
					role      string
					allowlist []string
				}{role: roleName, allowlist: mintcore.ParseForeignAllowlist(v.Value)})
			}
			if len(foreign) == 0 {
				printer.StepInfo(fmt.Sprintf("No FULLSEND_FOREIGN_* variables found on %s", targetLabel))
				return nil
			}
			sort.Slice(foreign, func(i, j int) bool { return foreign[i].role < foreign[j].role })
			for _, entry := range foreign {
				printer.StepInfo(fmt.Sprintf("%s:", mintcore.ForeignVariableName(entry.role)))
				if len(entry.allowlist) == 0 {
					printer.StepInfo("  (empty)")
					continue
				}
				for _, caller := range entry.allowlist {
					printer.StepInfo(fmt.Sprintf("  - %s", caller))
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Target GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "", "Target repository (owner/repo or bare name with --org)")
	cmd.Flags().StringVar(&role, "role", "", "Filter to a single role (optional)")
	return cmd
}

func listRepoForeign(ctx context.Context, client *gh.LiveClient, printer *ui.Printer, target foreignTarget, role string) error {
	targetLabel := target.label()
	if role != "" {
		varName := mintcore.ForeignVariableName(role)
		allowlist, err := loadRepoForeignAllowlist(ctx, client, target.org, target.repo, varName)
		if err != nil {
			return err
		}
		if len(allowlist) == 0 {
			printer.StepInfo(fmt.Sprintf("%s on %s: (not set)", varName, targetLabel))
			return nil
		}
		printer.StepInfo(fmt.Sprintf("%s on %s:", varName, targetLabel))
		for _, entry := range allowlist {
			printer.StepInfo(fmt.Sprintf("  - %s", entry))
		}
		return nil
	}

	vars, err := client.ListRepoVariables(ctx, target.org, target.repo)
	if err != nil {
		return err
	}
	var foreign []struct {
		role      string
		allowlist []string
	}
	for name, value := range vars {
		roleName, ok := parseForeignVariableName(name)
		if !ok {
			continue
		}
		foreign = append(foreign, struct {
			role      string
			allowlist []string
		}{role: roleName, allowlist: mintcore.ParseForeignAllowlist(value)})
	}
	if len(foreign) == 0 {
		printer.StepInfo(fmt.Sprintf("No FULLSEND_FOREIGN_* variables found on %s", targetLabel))
		return nil
	}
	sort.Slice(foreign, func(i, j int) bool { return foreign[i].role < foreign[j].role })
	for _, entry := range foreign {
		printer.StepInfo(fmt.Sprintf("%s:", mintcore.ForeignVariableName(entry.role)))
		if len(entry.allowlist) == 0 {
			printer.StepInfo("  (empty)")
			continue
		}
		for _, caller := range entry.allowlist {
			printer.StepInfo(fmt.Sprintf("  - %s", caller))
		}
	}
	return nil
}

func newForeignRevokeCmd() *cobra.Command {
	var org, repo string
	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Remove a foreign caller from a role allowlist",
		RunE: func(cmd *cobra.Command, args []string) error {
			role, err := cmd.Flags().GetString("role")
			if err != nil {
				return err
			}
			caller, err := cmd.Flags().GetString("caller")
			if err != nil {
				return err
			}
			target, err := resolveForeignTarget(org, repo)
			if err != nil {
				return err
			}
			if err := mintcore.ValidateRoleName(role); err != nil {
				return fmt.Errorf("invalid --role: %w", err)
			}
			if err := validateForeignCaller(caller); err != nil {
				return err
			}

			token, err := resolveToken()
			if err != nil {
				return err
			}
			client := newGitHubLiveClient(token, "")
			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			varName := mintcore.ForeignVariableName(role)
			targetLabel := target.label()

			var allowlist []string
			if target.isRepoLevel() {
				allowlist, err = loadRepoForeignAllowlist(ctx, client, target.org, target.repo, varName)
			} else {
				allowlist, err = loadForeignAllowlist(ctx, client, target.org, varName)
			}
			if err != nil {
				return err
			}

			updated, changed := removeForeignCaller(allowlist, caller)
			if !changed {
				printer.StepInfo(fmt.Sprintf("%q not in %s on %s", caller, varName, targetLabel))
				return nil
			}

			printer.StepStart(fmt.Sprintf("Updating %s on %s", varName, targetLabel))
			if len(updated) == 0 {
				if target.isRepoLevel() {
					if err := client.DeleteRepoVariable(ctx, target.org, target.repo, varName); err != nil {
						printer.StepFail(fmt.Sprintf("Failed to delete %s on %s", varName, targetLabel))
						return err
					}
				} else {
					if err := client.DeleteOrgVariable(ctx, target.org, varName); err != nil {
						printer.StepFail(fmt.Sprintf("Failed to delete %s", varName))
						return err
					}
				}
				printer.StepDone(fmt.Sprintf("Removed %q; deleted empty %s on %s", caller, varName, targetLabel))
				return nil
			}
			value := strings.Join(updated, ", ")
			if target.isRepoLevel() {
				if err := client.CreateOrUpdateRepoVariable(ctx, target.org, target.repo, varName, value); err != nil {
					printer.StepFail(fmt.Sprintf("Failed to update %s on %s", varName, targetLabel))
					return err
				}
			} else {
				if err := client.CreateOrUpdateOrgVariableAll(ctx, target.org, varName, value); err != nil {
					printer.StepFail(fmt.Sprintf("Failed to update %s", varName))
					return err
				}
			}
			printer.StepDone(fmt.Sprintf("Removed %q from %s on %s", caller, varName, targetLabel))
			return nil
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Target GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "", "Target repository (owner/repo or bare name with --org)")
	cmd.Flags().String("role", "", "Agent role (e.g. e2e)")
	cmd.Flags().String("caller", "", "Foreign caller to remove")
	_ = cmd.MarkFlagRequired("role")
	_ = cmd.MarkFlagRequired("caller")
	return cmd
}

// foreignTarget holds the resolved org and optional repo for foreign
// variable operations. When repo is non-empty, operations target a
// repo-level variable; otherwise they target an org-level variable.
type foreignTarget struct {
	org  string
	repo string
}

// resolveForeignTarget parses --org and --repo flags into a foreignTarget.
//
// --repo accepts two forms:
//   - owner/repo (full form, --org can be omitted)
//   - repo       (short form, --org is required)
//
// Without --repo, --org is required and the target is org-level.
func resolveForeignTarget(org, repo string) (foreignTarget, error) {
	if repo == "" {
		// Org-level mode: --org is required.
		if org == "" {
			return foreignTarget{}, fmt.Errorf("--org is required (or use --repo owner/repo)")
		}
		if err := validateOrgName(org); err != nil {
			return foreignTarget{}, err
		}
		return foreignTarget{org: org}, nil
	}

	// Repo-level mode.
	if strings.Contains(repo, "/") {
		// Full owner/repo form.
		parts := strings.SplitN(repo, "/", 2)
		repoOrg, repoName := parts[0], parts[1]
		if org != "" && !strings.EqualFold(org, repoOrg) {
			return foreignTarget{}, fmt.Errorf("--org %q conflicts with owner in --repo %q", org, repo)
		}
		if err := validateOrgName(repoOrg); err != nil {
			return foreignTarget{}, fmt.Errorf("invalid owner in --repo: %w", err)
		}
		if repoName == "" || !githubRepoPattern.MatchString(repoName) {
			return foreignTarget{}, fmt.Errorf("invalid repo name in --repo %q", repo)
		}
		return foreignTarget{org: repoOrg, repo: repoName}, nil
	}

	// Short repo name: --org is required.
	if org == "" {
		return foreignTarget{}, fmt.Errorf("--org is required when --repo is a bare name (or use --repo owner/repo)")
	}
	if err := validateOrgName(org); err != nil {
		return foreignTarget{}, err
	}
	if !githubRepoPattern.MatchString(repo) {
		return foreignTarget{}, fmt.Errorf("invalid repo name in --repo %q", repo)
	}
	return foreignTarget{org: org, repo: repo}, nil
}

func (t foreignTarget) isRepoLevel() bool {
	return t.repo != ""
}

func (t foreignTarget) label() string {
	if t.repo != "" {
		return t.org + "/" + t.repo
	}
	return t.org
}

func loadForeignAllowlist(ctx context.Context, client *gh.LiveClient, org, varName string) ([]string, error) {
	value, exists, err := client.GetOrgVariable(ctx, org, varName)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	return mintcore.ParseForeignAllowlist(value), nil
}

func loadRepoForeignAllowlist(ctx context.Context, client *gh.LiveClient, owner, repo, varName string) ([]string, error) {
	value, exists, err := client.GetRepoVariable(ctx, owner, repo, varName)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	return mintcore.ParseForeignAllowlist(value), nil
}

func parseForeignVariableName(name string) (role string, ok bool) {
	const prefix = "FULLSEND_FOREIGN_"
	const suffix = "_REPOS"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return "", false
	}
	role = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix))
	if err := mintcore.ValidateRoleName(role); err != nil {
		return "", false
	}
	return role, true
}

func validateForeignCaller(caller string) error {
	caller = strings.TrimSpace(caller)
	if caller == "" {
		return fmt.Errorf("--caller must not be empty")
	}
	if strings.Contains(caller, "/") {
		parts := strings.SplitN(caller, "/", 2)
		if err := validateOrgName(parts[0]); err != nil {
			return fmt.Errorf("invalid org in --caller: %w", err)
		}
		if parts[1] == "" || !githubRepoPattern.MatchString(parts[1]) {
			return fmt.Errorf("invalid repo in --caller %q", caller)
		}
		return nil
	}
	return validateOrgName(caller)
}

func containsForeignCaller(allowlist []string, caller string) bool {
	for _, entry := range allowlist {
		if strings.EqualFold(entry, caller) {
			return true
		}
	}
	return false
}

func removeForeignCaller(allowlist []string, caller string) ([]string, bool) {
	var out []string
	changed := false
	for _, entry := range allowlist {
		if strings.EqualFold(entry, caller) {
			changed = true
			continue
		}
		out = append(out, entry)
	}
	return out, changed
}
