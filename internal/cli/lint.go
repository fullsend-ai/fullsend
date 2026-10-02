package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newLintCmd() *cobra.Command {
	var fullsendDir string
	var forgeFlag string
	var strict bool
	var offline bool

	cmd := &cobra.Command{
		Use:   "lint",
		Short: "Validate .fullsend config and harnesses without running an agent",
		Long: `Load the repo's fullsend configuration and every harness it declares —
local harness files plus config-registered agents — and report structural
validation failures and deprecation diagnostics.

This command never starts an agent sandbox and never mutates the forge (no
PR comments, no label or status changes); it only reads config.yaml and the
harness files it references. The same command works locally and as a CI
gate.

Diagnostic coverage includes items tracked in #7155 that previously had no
warning at all (e.g. implicit allow_runtime_fetch, GITHUB_ISSUE_URL, and
per-org installation mode), not only the fields Harness.Lint() already knew
about (runner_env, forge).

Exits non-zero when any config or harness fails structural validation. Pass
--strict to also fail on deprecation warnings, which is appropriate for a CI
gate that wants to block merges on deprecated usage.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(os.Stdout)
			return runLint(cmd.Context(), fullsendDir, forgeFlag, strict, offline, printer)
		},
	}

	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", ".fullsend", "path to the .fullsend configuration directory")
	cmd.Flags().StringVar(&forgeFlag, "forge", "", `forge platform to resolve before linting (e.g. "github"); omit to check every forge variant declared on each harness`)
	cmd.Flags().BoolVar(&strict, "strict", false, "exit non-zero when deprecation warnings are found, not just structural errors")
	cmd.Flags().BoolVar(&offline, "offline", false, "reject network fetches; only use cached or local resources")

	return cmd
}

// lintResult tallies outcomes across runLint so the final summary and exit
// code reflect everything checked, not just the first failure.
type lintResult struct {
	checked  int
	errors   int
	warnings int
}

func runLint(ctx context.Context, fullsendDir, forgeFlag string, strict, offline bool, printer *ui.Printer) error {
	printer.Banner(Version())
	printer.Header("Linting fullsend configuration")
	printer.Blank()

	absFullsendDir, err := filepath.Abs(fullsendDir)
	if err != nil {
		return fmt.Errorf("resolving fullsend dir: %w", err)
	}

	if forgeFlag != "" && !harness.ValidForgePlatform(forgeFlag) {
		return fmt.Errorf("--forge: %q is not a valid forge platform (valid: %s)", forgeFlag, harness.ForgeKeyList())
	}

	var result lintResult

	orgCfg, cfgUsable := lintConfig(absFullsendDir, &result, printer)

	harnessDir := filepath.Join(absFullsendDir, "harness")
	agentNames, err := discoverHarnessNames(harnessDir)
	if err != nil {
		return err
	}

	if cfgUsable && orgCfg != nil {
		registered, regErr := harness.RegisteredAgents(orgCfg)
		if regErr != nil {
			printer.StepWarn("Could not discover config-registered agents: " + agentruntime.SanitizeForDisplay(regErr.Error()))
		} else {
			localSet := make(map[string]bool, len(agentNames))
			for _, n := range agentNames {
				localSet[n] = true
			}
			for _, ra := range registered {
				if !localSet[ra.Name] {
					localSet[ra.Name] = true
					agentNames = append(agentNames, ra.Name)
				}
			}
			sort.Strings(agentNames)
		}
	}

	if len(agentNames) == 0 {
		printer.StepWarn("No harness files found locally or in config")
	}

	policy := fetch.DefaultPolicy
	policy.Offline = offline
	rFlags := resolveFlags{offline: offline}

	var orgAllowlist []string
	if orgCfg != nil {
		orgAllowlist = orgCfg.AllowedResources()
	} else {
		orgAllowlist = config.DefaultAllowedRemoteResources()
	}

	for _, name := range agentNames {
		lintOneAgent(ctx, name, absFullsendDir, forgeFlag, orgCfg, orgAllowlist, rFlags, policy, &result, printer)
	}

	printer.Blank()
	summary := fmt.Sprintf("Checked %d harness(es): %d error(s), %d warning(s)", result.checked, result.errors, result.warnings)
	switch {
	case result.errors > 0:
		printer.StepFail(summary)
		return fmt.Errorf("fullsend lint found %d structural error(s)", result.errors)
	case strict && result.warnings > 0:
		printer.StepFail(summary)
		return fmt.Errorf("fullsend lint found %d deprecation warning(s) (--strict)", result.warnings)
	default:
		printer.StepDone(summary)
		return nil
	}
}

// lintConfig loads and validates config.yaml (when present), reporting
// structural errors and config-level deprecation diagnostics. It returns the
// loaded config (nil when absent or unreadable) and whether the config is
// valid enough to use for config-registered agent discovery.
func lintConfig(absFullsendDir string, result *lintResult, printer *ui.Printer) (config.ConfigReader, bool) {
	configPath := filepath.Join(absFullsendDir, "config.yaml")
	if _, statErr := os.Stat(configPath); statErr != nil {
		if os.IsNotExist(statErr) {
			printer.StepInfo("config.yaml: not found (optional for per-repo installs)")
			return nil, true
		}
		printer.StepFail("config.yaml: " + agentruntime.SanitizeForDisplay(statErr.Error()))
		result.errors++
		return nil, false
	}

	cfg, err := config.LoadConfigWriter(absFullsendDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		printer.StepFail("config.yaml: " + agentruntime.SanitizeForDisplay(err.Error()))
		result.errors++
		return nil, false
	}
	if err := cfg.Validate(); err != nil {
		printer.StepFail("config.yaml: " + agentruntime.SanitizeForDisplay(err.Error()))
		result.errors++
		return cfg, false
	}
	printer.StepDone("config.yaml: valid")

	// Per-org installation mode is deprecated (ADR 0044) in favor of
	// per-repo installs; this is the one static signal available for the
	// "fullsend admin install" and "per-org install mode" items tracked in
	// #7155, neither of which otherwise emit a warning anywhere.
	if cfg.IsOrgMode() {
		printer.StepWarn("config.yaml: per-org installation mode (as produced by `fullsend admin install`) is deprecated; migrate to per-repo installation, e.g. via `fullsend repos migrate` (see ADR 0044)")
		result.warnings++
	}

	return cfg, true
}

// lintOneAgent loads a single harness (local file or config-registered
// agent, across every forge variant it declares) and reports structural
// validation failures and Lint() diagnostics. It mirrors the loading steps
// lockOneAgent (internal/cli/lock.go) uses before dependency resolution,
// but stops short of resolve.ResolveHarness — fetching every transitive
// skill/policy/provider URL is unnecessary just to check structure and
// deprecations, and would make a CI lint gate far more network-dependent
// than it needs to be.
func lintOneAgent(ctx context.Context, agentName, absFullsendDir, forgeFlag string, orgCfg config.ConfigReader, orgAllowlist []string, rFlags resolveFlags, policy fetch.FetchPolicy, result *lintResult, printer *ui.Printer) {
	result.checked++

	harnessPath, _, err := resolveHarnessForLock(ctx, absFullsendDir, agentName, orgCfg, rFlags, policy, printer)
	if err != nil {
		printer.StepFail(lintDiagLine(agentName, err.Error()))
		result.errors++
		return
	}

	forgePlatforms, err := lockForgePlatforms(harnessPath, forgeFlag)
	if err != nil {
		printer.StepFail(lintDiagLine(agentName, err.Error()))
		result.errors++
		return
	}

	linted := make(map[string]bool) // dedupe identical diagnostics across forge variants
	hadError := false

	for _, platform := range forgePlatforms {
		label := agentName
		if platform != "" {
			label = fmt.Sprintf("%s (forge: %s)", agentName, platform)
		}

		h, _, loadErr := harness.LoadWithBase(ctx, harnessPath, harness.ComposeOpts{
			WorkspaceRoot: absFullsendDir,
			FetchPolicy:   policy,
			ForgePlatform: platform,
			OrgAllowlist:  orgAllowlist,
			GitToken:      rFlags.gitToken,
			TreeFetcher:   rFlags.treeFetcher,
			Config:        harness.BuildConfigMap(orgCfg),
		})
		if loadErr != nil {
			printer.StepFail(lintDiagLine(label, loadErr.Error()))
			result.errors++
			hadError = true
			continue
		}

		if h.HasURLReferences() {
			if err := h.ValidateAllowedRemoteResources(orgAllowlist); err != nil {
				printer.StepFail(lintDiagLine(label, err.Error()))
				result.errors++
				hadError = true
				continue
			}
		}

		diags, checkErr := harness.CheckGenerated(h, absFullsendDir)
		if checkErr != nil {
			printer.StepFail(lintDiagLine(label, checkErr.Error()))
			result.errors++
			hadError = true
		} else if err := h.ValidatePluginDirs(); err != nil {
			printer.StepFail(lintDiagLine(label, err.Error()))
			result.errors++
			hadError = true
		}

		for _, diag := range diags {
			key := diag.String()
			if linted[key] {
				continue
			}
			linted[key] = true
			emitDiagnosticWithContext(printer, agentName, diag)
			if diag.Severity == harness.SeverityError {
				result.errors++
			} else {
				result.warnings++
			}
		}
	}

	if !hadError {
		printer.StepDone(agentruntime.SanitizeForDisplay(label(agentName, forgePlatforms)))
	}
}

// label formats the "<agent>: OK" summary line, noting the forge variant
// count when a harness declares more than one.
func label(agentName string, forgePlatforms []string) string {
	if len(forgePlatforms) > 1 {
		return fmt.Sprintf("%s: OK (%d forge variants)", agentName, len(forgePlatforms))
	}
	return agentName + ": OK"
}

// lintDiagLine formats a "<context>: <message>" line for printer.StepFail,
// sanitizing both parts. context (an agent name or forge-variant label) and
// message (an error string) can carry content lifted verbatim from an
// untrusted harness file — fullsend lint runs as a CI gate over PR branches
// — so neither is safe to print unsanitized: a crafted value containing
// control characters and GitHub Actions workflow-command syntax could
// inject a spurious log line.
func lintDiagLine(context, message string) string {
	return agentruntime.SanitizeForDisplay(fmt.Sprintf("%s: %s", context, message))
}
