package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/agentnew"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// agentNewFlags carries the `agent new` flag values plus whether each was
// given, so a spec file (-f) and command-line flags can be merged with flags
// winning — the same technique `agent set` uses.
type agentNewFlags struct {
	fullsendDir    string
	specFile       string
	role           string
	description    string
	on             string
	trigger        string
	model          string
	effort         string
	runtime        string
	slug           string
	image          string
	timeoutMinutes int
	validationLoop bool
	noRegister     bool
	force          bool
	dryRun         bool

	changed func(string) bool
}

func newAgentNewCmd() *cobra.Command {
	var f agentNewFlags

	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Generate a complete custom agent",
		Long: `Generate a complete, valid, runnable custom agent and register it.

Writes the harness, agent definition, result schema and post-script, copies
the sandbox policy, providers and profiles it needs when they are absent,
validates the result with the same loader dispatch uses, and adds the agent
to config.yaml.

The agent is generated with a trigger, because an agent without one
registers and validates but is silently never dispatched.

Examples:
  fullsend agent new lint-docs --fullsend-dir .fullsend \
    --role triage --description "Check docs changes for broken links"

  fullsend agent new lint-docs --fullsend-dir .fullsend --on label:needs-docs

  fullsend agent new -f lint-docs.agent.yaml --fullsend-dir .fullsend

` + agentnew.RoleHelp(),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.changed = cmd.Flags().Changed
			var name string
			if len(args) == 1 {
				name = args[0]
			}
			printer := ui.New(os.Stdout)
			return runAgentNew(cmd.Context(), name, f, printer)
		},
	}

	cmd.Flags().StringVar(&f.fullsendDir, "fullsend-dir", "", "path to the .fullsend configuration directory")
	cmd.Flags().StringVarP(&f.specFile, "file", "f", "", "read the agent definition from a spec YAML file")
	cmd.Flags().StringVar(&f.role, "role", agentnew.DefaultRole, "mint role the agent runs as (see the table below)")
	cmd.Flags().StringVar(&f.description, "description", "", "one-line description of what the agent does")
	cmd.Flags().StringVar(&f.on, "on", "", "trigger preset: "+strings.Join(agentnew.PresetNames(), ", ")+" (default command:/fs-<name>)")
	cmd.Flags().StringVar(&f.trigger, "trigger", "", "raw CEL trigger expression; mutually exclusive with --on")
	cmd.Flags().StringVar(&f.model, "model", agentnew.DefaultModel, "model for the agent")
	cmd.Flags().StringVar(&f.effort, "effort", agentnew.DefaultEffort, "effort level (low, medium, high, xhigh, max)")
	cmd.Flags().StringVar(&f.runtime, "runtime", "", "agent runtime recorded in config.yaml (claude, pi or codex)")
	cmd.Flags().StringVar(&f.slug, "slug", "", "harness slug (default: <owner>-<name> from the origin remote)")
	cmd.Flags().StringVar(&f.image, "image", "", "sandbox image (default: the fleet's pin for this role)")
	cmd.Flags().IntVar(&f.timeoutMinutes, "timeout-minutes", agentnew.DefaultTimeoutMinutes, "agent timeout in minutes")
	cmd.Flags().BoolVar(&f.validationLoop, "validation-loop", false,
		"add a validation_loop that checks agent output against the schema (needs python3 with the jsonschema package on the runner)")
	cmd.Flags().BoolVar(&f.noRegister, "no-register", false, "write the files but do not add the agent to config.yaml")
	cmd.Flags().BoolVar(&f.force, "force", false, "overwrite generated files that already exist (never overwrites shared scaffold assets)")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "validate and report what would be written without writing anything")
	_ = cmd.MarkFlagRequired("fullsend-dir")
	return cmd
}

// resolveAgentNewOptions merges defaults, the spec file and command-line
// flags into a validated Options. Flags win over spec keys.
func resolveAgentNewOptions(name string, f agentNewFlags) (opts agentnew.Options, runtimeName string, slugWarning string, err error) {
	opts = agentnew.Options{
		Role:           f.role,
		Model:          f.model,
		Effort:         f.effort,
		TimeoutMinutes: f.timeoutMinutes,
		ValidationLoop: f.validationLoop,
	}
	runtimeName = f.runtime
	on, trigger := f.on, f.trigger
	slug, image, description := f.slug, f.image, f.description

	if f.specFile != "" {
		if name != "" {
			return opts, "", "", fmt.Errorf("pass either a name or -f, not both")
		}
		spec, specErr := agentnew.LoadSpecFile(f.specFile)
		if specErr != nil {
			return opts, "", "", specErr
		}
		name = spec.Name
		// Spec values apply only where the flag was not given.
		if !f.changed("role") && spec.Role != "" {
			opts.Role = spec.Role
		}
		if !f.changed("model") && spec.Model != "" {
			opts.Model = spec.Model
		}
		if !f.changed("effort") && spec.Effort != "" {
			opts.Effort = spec.Effort
		}
		if !f.changed("timeout-minutes") && spec.TimeoutMinutes != 0 {
			opts.TimeoutMinutes = spec.TimeoutMinutes
		}
		if !f.changed("validation-loop") {
			opts.ValidationLoop = spec.ValidationLoop
		}
		if !f.changed("runtime") {
			runtimeName = spec.Runtime
		}
		if !f.changed("description") {
			description = spec.Description
		}
		if !f.changed("slug") {
			slug = spec.Slug
		}
		if !f.changed("image") {
			image = spec.Image
		}
		if !f.changed("on") && !f.changed("trigger") {
			on, trigger = spec.On, spec.Trigger
		}
	}

	if name == "" {
		return opts, "", "", fmt.Errorf("an agent name is required: `fullsend agent new <name>` or -f <spec.yaml>")
	}
	opts.Name = name

	if on != "" && trigger != "" {
		return opts, "", "", fmt.Errorf("--on and --trigger are mutually exclusive")
	}
	switch {
	case trigger != "":
		opts.Trigger = trigger
	default:
		if on == "" {
			on = agentnew.DefaultOn
		}
		expanded, expandErr := agentnew.ExpandTrigger(on, name)
		if expandErr != nil {
			return opts, "", "", expandErr
		}
		opts.Trigger = expanded
	}

	if description == "" {
		description = "Custom " + name + " agent."
	}
	opts.Description = description

	if slug == "" {
		owner := agentnew.GitConfigOwner(f.fullsendDir)
		if owner == "" {
			slugWarning = fmt.Sprintf("Could not read an owner from the origin remote; using slug %q. Pass --slug to set it.",
				agentnew.DeriveSlug(name, ""))
		}
		slug = agentnew.DeriveSlug(name, owner)
	}
	opts.Slug = slug

	if image == "" {
		role, roleErr := agentnew.LookupRole(opts.Role)
		if roleErr != nil {
			return opts, "", "", roleErr
		}
		image = role.Image
	}
	opts.Image = image

	if runtimeName != "" && !slices.Contains(config.ValidRuntimes(), runtimeName) {
		return opts, "", "", fmt.Errorf("runtime %q is not valid (allowed: %s)",
			runtimeName, strings.Join(config.ValidRuntimes(), ", "))
	}

	if validateErr := opts.Validate(); validateErr != nil {
		return opts, "", "", validateErr
	}
	return opts, runtimeName, slugWarning, nil
}

func runAgentNew(ctx context.Context, name string, f agentNewFlags, printer *ui.Printer) error {
	opts, runtimeName, slugWarning, err := resolveAgentNewOptions(name, f)
	if err != nil {
		return err
	}
	if slugWarning != "" {
		printer.StepWarn(slugWarning)
	}

	// Reported first so a missing directory does not surface as a failure to
	// read config.yaml from inside it.
	if err := agentnew.ValidateFullsendDir(f.fullsendDir); err != nil {
		return err
	}
	// Checked before Generate writes anything: registration happens last, so
	// a name already in config.yaml would otherwise be reported only after
	// the files had landed, leaving the directory changed by a failed run.
	if !f.noRegister {
		if err := ensureAgentNameFree(f.fullsendDir, opts.Name); err != nil {
			return err
		}
	}

	result, err := agentnew.Generate(opts, f.fullsendDir, f.force, f.dryRun)
	if err != nil {
		return err
	}
	for _, diag := range result.Diagnostics {
		printer.StepWarn(diag.String())
	}

	if f.dryRun {
		printer.StepInfo(fmt.Sprintf("Dry run: would create agent %q in %s", opts.Name, f.fullsendDir))
		for _, w := range result.Written {
			printer.Raw("  " + w + "\n")
		}
		for _, s := range result.SkippedShared {
			printer.Raw("  " + s + "  (already present, would be left unchanged)\n")
		}
		for _, rf := range result.Rendered {
			printer.Raw("\n--- " + rf.Path + " ---\n")
			printer.Raw(string(rf.Data))
		}
		printer.StepInfo("Nothing was written and no agent was registered")
		return nil
	}

	printer.StepDone(fmt.Sprintf("Created agent %q in %s", opts.Name, f.fullsendDir))
	for _, w := range result.Written {
		printer.Raw("  " + w + "\n")
	}
	for _, s := range result.SkippedShared {
		printer.Raw("  " + s + "  (already present, left unchanged)\n")
	}

	if f.noRegister {
		printer.StepInfo("Not registered (--no-register). Register it later with:")
		printer.Raw(fmt.Sprintf("  fullsend agent add %s --fullsend-dir %s\n", result.HarnessPath, f.fullsendDir))
	} else {
		if err := runAgentAdd(ctx, result.HarnessPath, opts.Name, f.fullsendDir, nil, printer); err != nil {
			return fmt.Errorf("agent files were written but registration failed: %w", err)
		}
		if runtimeName != "" {
			if err := runAgentSet(f.fullsendDir, opts.Name, agentSetFlags{
				runtime: runtimeName, runtimeSet: true,
			}, printer); err != nil {
				return fmt.Errorf("agent registered but setting the runtime failed: %w", err)
			}
		}
	}

	printNextSteps(opts, f, printer)
	return nil
}

// printNextSteps tells the user what to edit, how to test locally, and how to
// fire the agent in CI. The CI line is derived from the same trigger that
// went into the harness, so the instruction and the CEL cannot disagree.
func printNextSteps(opts agentnew.Options, f agentNewFlags, printer *ui.Printer) {
	printer.Raw("\nNext:\n")
	printer.Raw(fmt.Sprintf("  1. Fill in the marked sections of agents/%s.md — that file is the agent's prompt.\n", opts.Name))
	printer.Raw(fmt.Sprintf("  2. Test locally:\n       fullsend run %s --fullsend-dir %s \\\n         --target-repo . --env-file .env.local\n",
		opts.Name, f.fullsendDir))
	printer.Raw("     .env.local needs GITHUB_ISSUE_URL, ANTHROPIC_VERTEX_PROJECT_ID, CLOUD_ML_REGION\n")
	printer.Raw("     and GH_TOKEN. See docs/guides/user/running-agents-locally.md.\n")
	if cmd := slashCommandFromTrigger(opts.Trigger); cmd != "" {
		printer.Raw(fmt.Sprintf("  3. Commit %s, then comment `%s` on an issue or pull request to run it in CI.\n",
			filepath.Clean(f.fullsendDir), cmd))
	} else {
		printer.Raw(fmt.Sprintf("  3. Commit %s, then trigger it with an event matching the harness `trigger:`.\n",
			filepath.Clean(f.fullsendDir)))
	}
}

// slashCommandFromTrigger recovers the slash command from a generated
// trigger so the printed instruction always matches the emitted CEL.
func slashCommandFromTrigger(trigger string) string {
	const marker = "event.transition.comment.command == "
	idx := strings.Index(trigger, marker)
	if idx < 0 {
		return ""
	}
	rest := trigger[idx+len(marker):]
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	end := strings.Index(rest[1:], `"`)
	if end < 0 {
		return ""
	}
	return rest[1 : 1+end]
}

// ensureAgentNameFree reports whether an agent of this name is already
// registered, without mutating anything. It mirrors the check runAgentAdd
// makes, so the two cannot disagree about what counts as a collision.
func ensureAgentNameFree(fullsendDir, name string) error {
	absDir, err := filepath.Abs(fullsendDir)
	if err != nil {
		return fmt.Errorf("resolving fullsend dir: %w", err)
	}
	cfg, err := loadAgentConfig(filepath.Join(absDir, config.OverlayConfigFile))
	if err != nil {
		return err
	}
	if _, found := findAgentByName(cfg.AgentEntries(), name); found {
		return fmt.Errorf("agent %q already exists in config; pick another name or run `fullsend agent remove %s` first", name, name)
	}
	return nil
}
