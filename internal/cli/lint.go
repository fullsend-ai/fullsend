package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newLintCmd() *cobra.Command {
	var fullsendDir string
	var forgeFlag string
	var strict bool
	var offline bool

	cmd := &cobra.Command{
		Use:   "lint [agent-name]",
		Short: "Lint configuration and harness definitions",
		Long: `Lint config.yaml and the harness definitions of the agents it registers.

With no argument, every enabled agent is resolved, composed, and linted.

With an agent name, only that registered agent is resolved, composed, and
linted. config.yaml itself is still validated, and an invalid file still
fails the command. An unknown or disabled agent name is an error. Invalid
agents[] entries for other agents do not stop the selected agent from
being linted: they are reported as a single warning (an error with
--strict); run fullsend lint without an argument to inspect them.`,
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentName := ""
			if len(args) == 1 {
				agentName = args[0]
				if agentName == "" {
					return errors.New("agent name must not be empty")
				}
			}
			return runLint(cmd, fullsendDir, forgeFlag, agentName, strict, offline)
		},
	}

	addFullsendDirFlag(cmd, &fullsendDir)
	cmd.Flags().StringVar(&forgeFlag, "forge", "", `forge platform to use (e.g. "github", "gitlab"); auto-detected from config.yaml or CI env vars when omitted`)
	cmd.Flags().BoolVar(&strict, "strict", false, "exit non-zero on warning-level diagnostics (deprecations), not just error-level ones")
	cmd.Flags().BoolVar(&offline, "offline", false, "reject network fetches; only use cached remote resources")

	return cmd
}

// runLint resolves and composes every enabled agent's harness exactly as
// fullsend run does — resolveAgentSource for resolution (config entries,
// URL fetches, agents-repo fallback for override-only entries) and
// LoadWithBase for composition (base chains, forge overlays for the
// effective platform) — then applies run's path resolution and file
// validation (ResolveRelativeTo, ValidateFilesExist) so a harness run
// would reject fails lint too, and reports harness lint diagnostics per
// agent.
//
// When only is non-empty, the per-agent work is scoped to the registered
// agent it names; see selectLintAgent.
func runLint(cmd *cobra.Command, fullsendDir, forgeFlag, only string, strict, offline bool) error {
	absFullsendDir, err := filepath.Abs(fullsendDir)
	if err != nil {
		return err
	}

	printer := ui.New(cmd.OutOrStdout())

	// Layered config load (config.yaml over config.base.yaml), same as
	// run/lock. Lint requires a config: without one there are no
	// registered agents to lint.
	cfg, err := loadLockConfig(filepath.Join(absFullsendDir, "config.yaml"))
	if err != nil {
		return err
	}
	if cfg == nil {
		return fmt.Errorf("no config.yaml in %s: nothing to lint", absFullsendDir)
	}

	policy := fetch.DefaultPolicy
	policy.Offline = offline

	forgePlatform, err := detectForgePlatform(forgeFlag, cfg)
	if err != nil {
		return fmt.Errorf("invalid --forge flag: %w", err)
	}

	composeGitToken, tokenErr := resolveToken()
	if tokenErr != nil {
		printer.StepWarn("Git token not available; private repo fetches may fail")
	}

	// Same fetch machinery as run, so lint is read-only for config and
	// forge state but not for .fullsend-cache: URL resolution populates
	// the cache and appends to the audit log.
	composeOpts := harness.ComposeOpts{
		WorkspaceRoot: absFullsendDir,
		FetchPolicy:   policy,
		AuditLogPath:  filepath.Join(absFullsendDir, ".fullsend-cache", "fetch-audit.jsonl"),
		ForgePlatform: forgePlatform,
		OrgAllowlist:  cfg.AllowedResources(),
		Config:        harness.BuildConfigMap(cfg),
		GitToken:      composeGitToken,
	}

	// Always create a GitHub client for the agents-repo fallback —
	// fullsend-ai/agents is public, so unauthenticated requests work when
	// no GitHub token is available (same as fullsend run).
	fallbackForgeClient := gh.New(composeGitToken)

	var errs []error
	errDiags := 0
	warnDiags := 0
	// Semantic validation, the same checks config-writing commands
	// enforce: loadPerRepoLayers only unmarshals, so without this a
	// config.yaml with an unsupported version or an unknown runtime
	// lints clean. Reported as a diagnostic (not a load error) so the
	// agents still lint and every problem is reported together. A scoped
	// lint skips the agents[] checks here: selectLintAgent validates the
	// selected agent's entries itself and reports other agents' as a
	// warning.
	var configErr error
	if only == "" {
		configErr = cfg.Validate()
	} else {
		configErr = cfg.ValidateGlobal()
	}
	if configErr != nil {
		emitDiagnosticWithContext(printer, "config", harness.Diagnostic{
			Severity: harness.SeverityError,
			Field:    "config.yaml",
			Message:  configErr.Error(),
		})
		errDiags++
	}
	entries := cfg.AgentEntries()
	// resolveCfg is what per-agent resolution sees. A scoped lint narrows
	// its agents list to the selected agent's entries, because
	// resolveAgentSource validates the whole list and would otherwise
	// fail on another agent's invalid entry.
	var resolveCfg config.ConfigReader = cfg
	if only != "" {
		selected, siblingErrs, err := selectLintAgent(entries, only, cfg.AllowedResources())
		if siblingErrs > 0 {
			noun, verb := "agents", "have"
			if siblingErrs == 1 {
				noun, verb = "agent", "has"
			}
			emitDiagnosticWithContext(printer, "config", harness.Diagnostic{
				Severity: harness.SeverityWarning,
				Field:    "agents",
				Message:  fmt.Sprintf("%d other %s %s lint errors; run `fullsend lint` to inspect them", siblingErrs, noun, verb),
			})
			warnDiags++
		}
		if err != nil {
			errs = append(errs, err)
		}
		entries = selected
		resolveCfg = lintScopedConfig{ConfigReader: cfg, entries: selected}
	} else if err := config.ValidateAgentEntries(entries, cfg.AllowedResources()); err != nil {
		// resolveAgentSource validates the whole agents list before
		// resolving any one agent, so an invalid entry (for example a
		// trigger that does not compile) would fail every agent with the
		// same error. Report it once — not at all when cfg.Validate already
		// reported the same error — and skip per-agent resolution.
		if configErr == nil || !strings.Contains(configErr.Error(), err.Error()) {
			errs = append(errs, fmt.Errorf("invalid agent config: %w", err))
		}
		entries = nil
	}
	for _, entry := range entries {
		if !entry.IsEnabled() {
			continue
		}
		agentName := entry.DerivedName()
		// Last-writer-wins: an enabled entry followed by a later disable
		// entry for the same name is effectively disabled. Skip it like any
		// other disabled agent instead of failing resolution.
		if config.IsAgentExplicitlyDisabled(entries, agentName) {
			continue
		}
		harnessPath, fetchDeps, err := resolveAgentSource(cmd.Context(), absFullsendDir, agentName, fallbackForgeClient, resolveCfg, composeOpts, printer)
		if err != nil {
			errs = append(errs, fmt.Errorf("resolving harness for agent %q: %w", agentName, err))
			continue
		}
		agentOpts := composeOpts
		if len(fetchDeps) > 0 && fetchDeps[0].URL != "" {
			agentOpts.SourceURL = fetchDeps[0].URL
		}
		h, _, err := harness.LoadWithBase(cmd.Context(), harnessPath, agentOpts)
		if err != nil {
			errs = append(errs, fmt.Errorf("loading harness for agent %q: %w", agentName, err))
			continue
		}
		// Same path resolution as fullsend run: relative paths resolve
		// against the configuration directory, and paths escaping it are
		// rejected. ${VAR} paths are left alone — they expand at run time.
		if err := h.ResolveRelativeTo(absFullsendDir); err != nil {
			errs = append(errs, fmt.Errorf("resolving paths for agent %q: %w", agentName, err))
			continue
		}
		// Same ${VAR} reference check as fullsend run, minus the host
		// environment: only the keys run always refuses (OIDC credentials,
		// provider-only keys) fail; every other variable is assumed to be
		// set at run time.
		if err := h.ValidateRunnerEnvWith(func(key string) (string, bool) {
			return "", !harnessExpansionDenied(key)
		}); err != nil {
			errs = append(errs, fmt.Errorf("validating env for agent %q: %w", agentName, err))
			continue
		}
		// Same allowlist checks as fullsend run: the harness allowlist must
		// be covered by config.yaml's, and every URL reference must match
		// one of the two. String comparison only, nothing is fetched.
		if h.HasURLReferences() {
			if err := h.ValidateAllowedRemoteResources(cfg.AllowedResources()); err != nil {
				errs = append(errs, fmt.Errorf("validating allowed remote resources for agent %q: %w", agentName, err))
				continue
			}
			if err := validateURLReferencesAllowed(h, cfg.AllowedResources()); err != nil {
				errs = append(errs, fmt.Errorf("validating allowed remote resources for agent %q: %w", agentName, err))
				continue
			}
		}
		for _, diag := range h.Lint() {
			emitDiagnosticWithContext(printer, agentName, diag)
			if diag.Severity == harness.SeverityError {
				errDiags++
			} else {
				warnDiags++
			}
		}
		// Same file validation as fullsend run, so lint catches at commit
		// time what run would reject: a missing policy, or a referenced
		// file that does not exist. URL references are skipped — lint
		// validates what is on disk and does not fetch remotes.
		if err := h.ValidateFilesExist(); err != nil {
			errs = append(errs, fmt.Errorf("validating files for agent %q: %w", agentName, err))
			continue
		}
		// ValidateFilesExist leaves local OpenShell profiles and provider
		// definitions to run's local ResolveHarness pass, which parses
		// them. Run the same pass so a missing or malformed local profile
		// or provider fails lint too. It sees only the local entries, so
		// no URL reference is fetched.
		if local := localProfilesAndProviders(h); len(local.OpenShellProfiles()) > 0 || len(local.Providers) > 0 {
			if _, err := resolve.ResolveHarness(cmd.Context(), local, resolve.ResolveOpts{WorkspaceRoot: absFullsendDir}); err != nil {
				errs = append(errs, fmt.Errorf("resolving local profiles/providers for agent %q: %w", agentName, err))
			}
		}
	}
	if errDiags > 0 {
		errs = append(errs, fmt.Errorf("lint found %d error-level diagnostic(s)", errDiags))
	}
	if strict && warnDiags > 0 {
		errs = append(errs, fmt.Errorf("lint found %d warning-level diagnostic(s) (--strict)", warnDiags))
	}
	return errors.Join(errs...)
}

// lintScopedConfig narrows a config's agents list to one agent's entries
// for a scoped lint; every other setting comes from the full config.
type lintScopedConfig struct {
	config.ConfigReader
	entries []config.AgentEntry
}

func (c lintScopedConfig) AgentEntries() []config.AgentEntry { return c.entries }

// selectLintAgent picks the agents[] entries registering name (matched
// case-insensitively, as agent names are) for a scoped lint. It returns
// those entries when they are valid and the agent is enabled, and an error
// when name is not registered, its entries are invalid, or it is disabled.
// siblingErrs counts the other agents whose entries fail validation: they
// are not linted, but are reported so a scoped lint does not hide them.
//
// Entries are validated per agent because ValidateAgentEntries keys every
// check, including duplicate detection, on the entry's name: validating
// each name's entries on their own reports exactly the errors validating
// the whole list would, without one agent's error masking the others.
func selectLintAgent(entries []config.AgentEntry, name string, allowlist []string) ([]config.AgentEntry, int, error) {
	groups := make(map[string][]config.AgentEntry)
	var order []string
	for _, e := range entries {
		key := strings.ToLower(e.DerivedName())
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], e)
	}

	selectedKey := strings.ToLower(name)
	siblingErrs := 0
	for _, key := range order {
		if key != selectedKey && config.ValidateAgentEntries(groups[key], allowlist) != nil {
			siblingErrs++
		}
	}

	selected, ok := groups[selectedKey]
	if !ok {
		var enabled []string
		for _, key := range order {
			group := groups[key]
			if n := group[len(group)-1].DerivedName(); n != "" && !config.IsAgentExplicitlyDisabled(group, n) {
				enabled = append(enabled, n)
			}
		}
		if len(enabled) == 0 {
			return nil, siblingErrs, fmt.Errorf("agent %q is not registered in config.yaml, and no enabled agents are", name)
		}
		sort.Strings(enabled)
		return nil, siblingErrs, fmt.Errorf("agent %q is not registered in config.yaml; registered agents that can be linted: %s", name, strings.Join(enabled, ", "))
	}
	if err := config.ValidateAgentEntries(selected, allowlist); err != nil {
		return nil, siblingErrs, fmt.Errorf("invalid agent config: %w", err)
	}
	if config.IsAgentExplicitlyDisabled(selected, name) {
		return nil, siblingErrs, fmt.Errorf("agent %q is disabled in config.yaml (enabled: false); enable it to lint it", name)
	}
	return selected, siblingErrs, nil
}

// validateURLReferencesAllowed applies run's per-URL allowlist check
// (resolve.ResolveHarness, resolveFromLock) without fetching: every URL
// reference must match the harness's allowed_remote_resources or the
// config.yaml allowlist.
func validateURLReferencesAllowed(h *harness.Harness, orgAllowlist []string) error {
	type ref struct{ field, url string }
	var refs []ref
	add := func(field, raw string) {
		if harness.IsURL(raw) {
			clean, _, _ := harness.ParseIntegrityHash(raw)
			refs = append(refs, ref{field, clean})
		}
	}
	add("agent", h.Agent)
	add("policy", h.Policy)
	for i, sk := range h.Skills {
		add(fmt.Sprintf("skills[%d].source", i), sk.Source)
		for k, val := range sk.Overrides {
			if val != nil {
				add(fmt.Sprintf("skills[%d].overrides.%s", i, k), *val)
			}
		}
	}
	for i, p := range h.Plugins {
		add(fmt.Sprintf("plugins[%d].path", i), p.Path)
	}
	for i, p := range h.OpenShellProfiles() {
		add(fmt.Sprintf("openshell.profiles[%d]", i), p)
	}
	for i, p := range h.Providers {
		add(fmt.Sprintf("providers[%d]", i), p)
	}
	for _, r := range refs {
		if h.MatchingAllowedPrefix(r.url) == "" && harness.MatchingAllowedPrefixInList(r.url, orgAllowlist) == "" {
			return fmt.Errorf("%s: URL %q is not in allowed_remote_resources", r.field, r.url)
		}
	}
	return nil
}

// localProfilesAndProviders returns a harness carrying only h's local
// OpenShell profiles and provider definitions — what run's local
// ResolveHarness pass parses after URL resolution has consumed the rest.
func localProfilesAndProviders(h *harness.Harness) *harness.Harness {
	local := &harness.Harness{}
	for _, p := range h.OpenShellProfiles() {
		if !harness.IsURL(p) {
			if local.OpenShell == nil {
				local.OpenShell = &harness.OpenShellConfig{}
			}
			local.OpenShell.Profiles = append(local.OpenShell.Profiles, p)
		}
	}
	for _, p := range h.Providers {
		if !harness.IsURL(p) && harness.IsProviderPath(p) {
			local.Providers = append(local.Providers, p)
		}
	}
	return local
}
