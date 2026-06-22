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
		Short: "Manage cross-org mint authorization on a target org",
		Long:  "Manage FULLSEND_FOREIGN_<role>_REPOS org variables that authorize foreign workflows to mint tokens for this org.",
	}
	cmd.AddCommand(newForeignAllowCmd())
	cmd.AddCommand(newForeignListCmd())
	cmd.AddCommand(newForeignRevokeCmd())
	return cmd
}

func newForeignAllowCmd() *cobra.Command {
	var org string
	cmd := &cobra.Command{
		Use:   "allow",
		Short: "Authorize a foreign org/repo to mint for a role on this org",
		RunE: func(cmd *cobra.Command, args []string) error {
			role, err := cmd.Flags().GetString("role")
			if err != nil {
				return err
			}
			caller, err := cmd.Flags().GetString("caller")
			if err != nil {
				return err
			}
			if org == "" {
				return fmt.Errorf("--org is required")
			}
			if err := validateOrgName(org); err != nil {
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
			client := gh.New(token)
			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			varName := mintcore.ForeignVariableName(role)
			allowlist, err := loadForeignAllowlist(ctx, client, org, varName)
			if err != nil {
				return err
			}

			alreadyListed := containsForeignCaller(allowlist, caller)
			if !alreadyListed {
				allowlist = append(allowlist, caller)
			}
			value := strings.Join(allowlist, ", ")

			if alreadyListed {
				printer.StepStart(fmt.Sprintf("Ensuring %s is org-wide on %s", varName, org))
			} else {
				printer.StepStart(fmt.Sprintf("Updating %s on %s", varName, org))
			}
			if err := client.CreateOrUpdateOrgVariableAll(ctx, org, varName, value); err != nil {
				printer.StepFail(fmt.Sprintf("Failed to update %s", varName))
				return err
			}
			if alreadyListed {
				printer.StepDone(fmt.Sprintf("%s already lists %q (org-wide visibility ensured)", varName, caller))
			} else {
				printer.StepDone(fmt.Sprintf("Added %q to %s", caller, varName))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Target GitHub organization")
	cmd.Flags().String("role", "", "Agent role (e.g. e2e)")
	cmd.Flags().String("caller", "", "Foreign caller: org/repo or bare org")
	_ = cmd.MarkFlagRequired("role")
	_ = cmd.MarkFlagRequired("caller")
	return cmd
}

func newForeignListCmd() *cobra.Command {
	var org string
	var role string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List foreign caller allowlists on an org",
		RunE: func(cmd *cobra.Command, args []string) error {
			if org == "" {
				return fmt.Errorf("--org is required")
			}
			if err := validateOrgName(org); err != nil {
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
			client := gh.New(token)
			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			if role != "" {
				varName := mintcore.ForeignVariableName(role)
				allowlist, err := loadForeignAllowlist(ctx, client, org, varName)
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

			vars, err := client.ListOrgVariables(ctx, org)
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
				printer.StepInfo("No FULLSEND_FOREIGN_* variables found")
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
	cmd.Flags().StringVar(&role, "role", "", "Filter to a single role (optional)")
	return cmd
}

func newForeignRevokeCmd() *cobra.Command {
	var org string
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
			if org == "" {
				return fmt.Errorf("--org is required")
			}
			if err := validateOrgName(org); err != nil {
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
			client := gh.New(token)
			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			varName := mintcore.ForeignVariableName(role)
			allowlist, err := loadForeignAllowlist(ctx, client, org, varName)
			if err != nil {
				return err
			}

			updated, changed := removeForeignCaller(allowlist, caller)
			if !changed {
				printer.StepInfo(fmt.Sprintf("%q not in %s", caller, varName))
				return nil
			}

			printer.StepStart(fmt.Sprintf("Updating %s on %s", varName, org))
			if len(updated) == 0 {
				if err := client.DeleteOrgVariable(ctx, org, varName); err != nil {
					printer.StepFail(fmt.Sprintf("Failed to delete %s", varName))
					return err
				}
				printer.StepDone(fmt.Sprintf("Removed %q; deleted empty %s", caller, varName))
				return nil
			}
			value := strings.Join(updated, ", ")
			if err := client.CreateOrUpdateOrgVariableAll(ctx, org, varName, value); err != nil {
				printer.StepFail(fmt.Sprintf("Failed to update %s", varName))
				return err
			}
			printer.StepDone(fmt.Sprintf("Removed %q from %s", caller, varName))
			return nil
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Target GitHub organization")
	cmd.Flags().String("role", "", "Agent role (e.g. e2e)")
	cmd.Flags().String("caller", "", "Foreign caller to remove")
	_ = cmd.MarkFlagRequired("role")
	_ = cmd.MarkFlagRequired("caller")
	return cmd
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
