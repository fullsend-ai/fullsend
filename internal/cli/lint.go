package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/resolve"
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
		Long: `Load the fullsend configuration and every harness it declares (local
files and config-registered agents) and report validation errors and
deprecation warnings.

Never starts an agent sandbox or mutates the forge; it only reads config.yaml
and the harness files it references.

Exits non-zero on structural errors. Pass --strict to also fail on
deprecation warnings.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(os.Stdout)
			return runLint(cmd.Context(), fullsendDir, forgeFlag, strict, offline, printer)
		},
	}

	addFullsendDirFlag(cmd, &fullsendDir)
	cmd.Flags().StringVar(&forgeFlag, "forge", "", `forge platform to resolve before linting (e.g. "github"); omit to check every forge variant declared on each harness`)
	cmd.Flags().BoolVar(&strict, "strict", false, "exit non-zero when deprecation warnings are found, not just structural errors")
	cmd.Flags().BoolVar(&offline, "offline", false, "reject network fetches; only use cached or local resources")

	return cmd
}

// lintResult tallies outcomes across runLint.
type lintResult struct {
	checked  int
	errors   int
	warnings int
}

// lintTarget is one harness to lint: a local file or a config registration.
type lintTarget struct {
	name       string
	fromConfig bool
}

// runLint validates the harnesses, config registrations and referenced
// resources under fullsendDir without running an agent. With strict, warnings
// are treated as errors; with offline, remote resources are not fetched.
func runLint(ctx context.Context, fullsendDir, forgeFlag string, strict, offline bool, printer *ui.Printer) error {
	return runLintWithFlags(ctx, fullsendDir, forgeFlag, strict, resolveFlags{offline: offline}, printer)
}

// runLintWithFlags is runLint with explicit resolve flags (for tests).
func runLintWithFlags(ctx context.Context, fullsendDir, forgeFlag string, strict bool, rFlags resolveFlags, printer *ui.Printer) error {
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

	// A missing config directory would otherwise lint nothing and pass.
	if info, statErr := os.Stat(absFullsendDir); statErr != nil || !info.IsDir() {
		return fmt.Errorf("--fullsend-dir: %q is not an existing directory", fullsendDir)
	}
	// Canonicalize the root: registered-path resolution returns real paths,
	// and containment checks compare lexically before resolving symlinks.
	if realDir, evalErr := filepath.EvalSymlinks(absFullsendDir); evalErr == nil {
		absFullsendDir = realDir
	}

	var result lintResult

	orgCfg, cfgUsable := lintConfig(absFullsendDir, &result, printer)

	harnessDir := filepath.Join(absFullsendDir, "harness")
	agentNames, err := discoverHarnessNames(harnessDir)
	if err != nil {
		return err
	}

	layers := partialBaseLayers(absFullsendDir, agentNames)
	targets := make([]lintTarget, 0, len(agentNames))
	for _, n := range agentNames {
		if layers[n] {
			continue
		}
		targets = append(targets, lintTarget{name: n})
	}

	// Config registrations take precedence over local files in runAgent,
	// so lint them independently; lintOneAgent dedupes by resolved path.
	if cfgUsable && orgCfg != nil {
		registered, regErr := harness.RegisteredAgents(orgCfg)
		if regErr != nil {
			printer.StepWarn("Could not discover config-registered agents: " + agentruntime.SanitizeForDisplay(regErr.Error()))
		} else {
			regSeen := make(map[string]bool, len(registered))
			var regNames []string
			for _, ra := range registered {
				// RegisteredAgents lists every enabled entry; a later
				// disabling entry (last-writer-wins) overrides it.
				if config.IsAgentExplicitlyDisabled(orgCfg.AgentEntries(), ra.Name) {
					continue
				}
				if !regSeen[ra.Name] {
					regSeen[ra.Name] = true
					regNames = append(regNames, ra.Name)
				}
			}
			sort.Strings(regNames)
			for _, n := range regNames {
				targets = append(targets, lintTarget{name: n, fromConfig: true})
			}
		}
	}

	if len(targets) == 0 {
		printer.StepWarn("No harness files found locally or in config")
	}

	policy := fetch.DefaultPolicy
	policy.Offline = rFlags.offline

	var orgAllowlist []string
	switch {
	case !cfgUsable:
		// Config is unusable, so its allowlist can't be trusted: deny all
		// and stay offline rather than fall back to the default allowlist.
		orgAllowlist = nil
		policy.Offline = true
	case orgCfg != nil:
		orgAllowlist = orgCfg.AllowedResources()
	default:
		orgAllowlist = config.DefaultAllowedRemoteResources()
	}

	// Resolve the git token once; skipped when nothing can be fetched.
	rFlags.gitToken = lintGitToken(rFlags.gitToken, !policy.Offline, printer)

	seenPaths := make(map[string]bool)
	for _, t := range targets {
		lintOneAgent(ctx, t, absFullsendDir, forgeFlag, orgCfg, orgAllowlist, rFlags, policy, seenPaths, &result, printer)
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

// checkLocalHarnessFile rejects a discovered local harness file that harness
// loading (a bare os.ReadFile) must not touch: one outside the config
// directory (including via symlink), not a regular file, or over the size cap.
// Linting runs on checkouts of untrusted content, so a symlink to /dev/zero or
// to a file outside the workspace must not be read.
func checkLocalHarnessFile(path, absFullsendDir string) error {
	if _, err := resolve.ReadContainedFile(path, absFullsendDir); err != nil {
		return fmt.Errorf("harness file: %w", err)
	}
	return nil
}

// partialBaseLayers returns the local harness names that another local
// harness inherits via base: and that lack agent or role. They only validate
// once composed, so a retained target that inherits them covers them. A
// partial layer no retained target reaches (e.g. a self-referencing or
// mutually-referencing pair) is not suppressed, so its cycle or missing
// fields are reported.
func partialBaseLayers(absFullsendDir string, names []string) map[string]bool {
	quiet := ui.New(io.Discard)
	nameByPath := make(map[string]string, len(names))
	rawByPath := make(map[string]*harness.Harness, len(names))
	for _, n := range names {
		p, err := resolveHarnessPath(absFullsendDir, n, quiet)
		if err != nil || checkLocalHarnessFile(p, absFullsendDir) != nil {
			continue
		}
		raw, err := harness.LoadRaw(p)
		if err != nil {
			continue
		}
		p = filepath.Clean(p)
		nameByPath[p] = n
		rawByPath[p] = raw
	}

	// localBase maps each harness path to its local base path, if any.
	localBase := make(map[string]string, len(rawByPath))
	candidates := make(map[string]bool)
	for p, raw := range rawByPath {
		if raw.Base == "" || harness.IsURL(raw.Base) {
			continue
		}
		base := raw.Base
		if !filepath.IsAbs(base) {
			base = filepath.Join(filepath.Dir(p), base)
		}
		base = filepath.Clean(base)
		localBase[p] = base
		if b := rawByPath[base]; b != nil && (b.Agent == "" || b.Role == "") {
			candidates[base] = true
		}
	}

	// Suppress only candidates reachable by walking base chains from a
	// retained (non-candidate) target.
	layers := make(map[string]bool)
	for p := range rawByPath {
		if candidates[p] {
			continue
		}
		seen := map[string]bool{p: true}
		for cur := p; ; {
			next, ok := localBase[cur]
			if !ok || seen[next] || rawByPath[next] == nil {
				break
			}
			seen[next] = true
			if candidates[next] {
				layers[nameByPath[next]] = true
			}
			cur = next
		}
	}
	return layers
}

// lintGitToken returns the explicit token if set, else the standard chain;
// nothing is resolved when canFetch is false.
func lintGitToken(explicit string, canFetch bool, printer *ui.Printer) string {
	if explicit != "" || !canFetch {
		return explicit
	}
	token, err := resolveToken()
	if err != nil {
		printer.StepWarn("Git token not available; private repo fetches may fail")
	}
	return token
}

// lintConfig validates the layered config (config.yaml over config.base.yaml)
// and reports config-level deprecations. It returns the loaded config (nil if
// absent or unreadable) and whether it is usable for agent discovery.
func lintConfig(absFullsendDir string, result *lintResult, printer *ui.Printer) (loaded config.ConfigReader, usable bool) {
	// config.base.yaml alone is a supported configuration, so check both.
	label := config.OverlayConfigFile
	haveAny := false
	var baseData []byte
	for _, name := range []string{config.OverlayConfigFile, config.BaseConfigFile} {
		// Like harnesses, providers and profiles, a config layer must be a
		// bounded regular file inside the workspace: a symlink to a
		// runner-local file must not be parsed (its content could leak
		// through parse diagnostics in CI logs).
		data, statErr := resolve.ReadContainedFile(filepath.Join(absFullsendDir, name), absFullsendDir)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			printer.StepFail(lintDiagLine(name, statErr.Error()))
			result.errors++
			return nil, false
		}
		if name == config.BaseConfigFile {
			baseData = data
		}
		if !haveAny {
			label = name
		}
		haveAny = true
	}
	if !haveAny {
		printer.StepInfo("config.yaml: not found (optional)")
		return nil, true
	}

	cfg, err := config.LoadConfigWriter(absFullsendDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		printer.StepFail(lintDiagLine(label, err.Error()))
		result.errors++
		return nil, false
	}
	if err := cfg.Validate(); err != nil {
		printer.StepFail(lintDiagLine(label, err.Error()))
		result.errors++
		return cfg, false
	}
	// Validate() skips settings inherited from config.base.yaml, so validate
	// that layer too.
	if baseData != nil {
		baseCfg, parseErr := config.ParsePerRepoConfigWriterLayered(baseData, nil)
		if parseErr == nil {
			// Validate as a layer: its URL agents may be authorized by the
			// overlay's allowed_remote_resources (checked above).
			parseErr = config.ValidateManagedLayer(baseCfg)
		}
		if parseErr != nil {
			printer.StepFail(lintDiagLine(config.BaseConfigFile, parseErr.Error()))
			result.errors++
			return cfg, false
		}
	}
	// config.forge is not covered by Validate.
	invalidForge := false
	if pr, ok := cfg.(config.PerRepoConfigReader); ok {
		if f := pr.ConfigForge(); f != "" && !harness.ValidForgePlatform(f) {
			printer.StepFail(lintDiagLine(label, fmt.Sprintf("forge: %q is not a valid forge platform (valid: %s)", f, harness.ForgeKeyList())))
			result.errors++
			invalidForge = true
		}
	}
	// Validate the effective allowlist here too: a config with no harnesses
	// would otherwise never have it checked. This runs even when the forge is
	// invalid so a malformed allowlist still triggers the deny-all fallback.
	if err := harness.ValidateOrgAllowlist(cfg.AllowedResources()); err != nil {
		printer.StepFail(lintDiagLine(label, err.Error()))
		result.errors++
		return cfg, false
	}
	if invalidForge {
		return cfg, true
	}
	printer.StepDone(label + ": valid")

	return cfg, true
}

// lintOneAgent loads one harness (across its forge variants) and reports
// validation failures and Lint() diagnostics. Like lockOneAgent it stops
// short of resolve.ResolveHarness, so transitive URLs are not fetched.
func lintOneAgent(ctx context.Context, target lintTarget, absFullsendDir, forgeFlag string, orgCfg config.ConfigReader, orgAllowlist []string, rFlags resolveFlags, policy fetch.FetchPolicy, seenPaths map[string]bool, result *lintResult, printer *ui.Printer) {
	agentName := target.name
	if target.fromConfig {
		agentName += " (config-registered)"
	}

	var (
		harnessPath string
		fetchDeps   []resolve.Dependency
		err         error
	)
	if target.fromConfig {
		harnessPath, fetchDeps, err = resolveRegisteredAgent(ctx, absFullsendDir, target.name, orgCfg, rFlags, policy, printer)
	} else {
		harnessPath, err = resolveHarnessPath(absFullsendDir, target.name, printer)
	}
	// A URL-registered harness resolves to a fetched cache file outside the
	// workspace; every local harness (discovered or config-registered) is
	// checked before LoadRaw reads it.
	if err == nil && len(fetchDeps) == 0 {
		err = checkLocalHarnessFile(harnessPath, absFullsendDir)
	}
	if err != nil {
		result.checked++
		printer.StepFail(lintDiagLine(agentName, err.Error()))
		result.errors++
		return
	}

	// Relative resources of a URL-registered harness resolve against its source.
	sourceURL := ""
	if len(fetchDeps) > 0 {
		sourceURL = fetchDeps[0].URL
	}

	// Dedupe by resolved path plus source URL (the cache gives distinct URLs
	// with identical bytes the same path).
	pathKey := filepath.Clean(harnessPath) + "\x00" + sourceURL
	if seenPaths[pathKey] {
		return
	}
	seenPaths[pathKey] = true
	result.checked++

	forgePlatforms, err := lintForgePlatforms(harnessPath, forgeFlag, orgCfg)
	if err != nil {
		printer.StepFail(lintDiagLine(agentName, err.Error()))
		result.errors++
		return
	}

	// Like run and lock, a URL base needs a config to authorize it. (A nil
	// allowlist means the config is unusable; composition denies it below.)
	if orgCfg == nil && orgAllowlist != nil {
		if raw, rawErr := harness.LoadRaw(harnessPath); rawErr == nil && raw.Base != "" && harness.IsURL(raw.Base) {
			printer.StepFail(lintDiagLine(agentName, "URL base requires config.yaml or config.base.yaml with allowed_remote_resources"))
			result.errors++
			return
		}
	}

	// With no config, URL bases anywhere in the chain are unauthorized, so
	// composition gets a deny-all allowlist (before any fetch or cache write).
	// The other checks keep the default allowlist.
	composeAllowlist := orgAllowlist
	if orgCfg == nil {
		composeAllowlist = nil
	}

	// No config at all (a nil allowlist means an unusable config instead).
	requireConfig := orgCfg == nil && orgAllowlist != nil

	// A missing or non-per-repo config reads as an empty map at runtime
	// (EvaluateOverlay), so config terms are known, not unknown. Only an
	// unusable config (nil allowlist) leaves them unknown.
	configMap := harness.BuildConfigMap(orgCfg)
	if configMap == nil && (orgCfg != nil || orgAllowlist != nil) {
		configMap = map[string]any{}
	}

	linted := make(map[string]bool) // dedupe identical diagnostics across forge variants
	hadError := false

	for _, platform := range forgePlatforms {
		label := agentName
		if platform != "" {
			label = fmt.Sprintf("%s (forge: %s)", agentName, platform)
		}

		// Composition has no event, so event-conditioned overlays are
		// dropped; lint once normally, then once per overlay forced on.
		var overlayCount int
		var overlayWhens []string
		for variant := -1; variant < overlayCount; variant++ {
			var layerCount int
			opts := harness.ComposeOpts{
				WorkspaceRoot: absFullsendDir,
				SourceURL:     sourceURL,
				FetchPolicy:   policy,
				ForgePlatform: platform,
				OrgAllowlist:  composeAllowlist,
				GitToken:      rFlags.gitToken,
				TreeFetcher:   rFlags.treeFetcher,
				Config:        configMap,
				// Counts overlays across the whole base chain.
				OverlayCount: &layerCount,
			}
			if variant < 0 {
				// Learn each overlay's condition on the normal pass.
				opts.OverlayWhens = &overlayWhens
			}
			variantLabel := label
			if variant >= 0 {
				// An overlay the selected forge/config can never match
				// would fail variants the runtime never composes.
				if variant < len(overlayWhens) && !harness.OverlayWhenPossible(overlayWhens[variant], platform, opts.Config) {
					continue
				}
				opts.ForceOverlays = map[int]bool{variant: true}
				if variant < len(overlayWhens) {
					opts.ForceWhens = map[string]bool{overlayWhens[variant]: true}
				}
				variantLabel = fmt.Sprintf("%s [overlay %d]", label, variant)
			}

			ok, deferred := lintLoadedHarness(ctx, harnessPath, opts, variantLabel, agentName, absFullsendDir, orgAllowlist, requireConfig, linted, result, printer)
			if variant < 0 {
				overlayCount = layerCount
				if deferred {
					// The empty-event composition lacks a field an overlay
					// may supply; the forced variants decide.
					continue
				}
			}
			if !ok {
				hadError = true
				if variant < 0 {
					// Overlay variants would repeat the same failure.
					break
				}
			}
		}
	}

	if !hadError {
		printer.StepDone(agentruntime.SanitizeForDisplay(lintAgentLabel(agentName, forgePlatforms)))
	}
}

// lintForgePlatforms selects the forge platform(s) to lint under: --forge
// wins, then lockForgePlatforms. A harness with overlays or a base is linted
// under the config's forge, or every platform if config names none, since
// composition drops forge-conditioned config for an empty platform.
func lintForgePlatforms(harnessPath, forgeFlag string, orgCfg config.ConfigReader) ([]string, error) {
	platforms, err := lockForgePlatforms(harnessPath, forgeFlag)
	if err != nil || forgeFlag != "" || len(platforms) != 1 || platforms[0] != "" {
		return platforms, err
	}

	raw, err := harness.LoadRaw(harnessPath)
	if err != nil {
		return nil, fmt.Errorf("loading harness for forge discovery: %w", err)
	}
	if len(raw.Overlays) == 0 && raw.Base == "" {
		return platforms, nil
	}

	if pr, ok := orgCfg.(config.PerRepoConfigReader); ok {
		if f := pr.ConfigForge(); f != "" && harness.ValidForgePlatform(f) {
			return []string{f}, nil
		}
	}
	return harness.ValidForgePlatforms(), nil
}

// lintAgentLabel formats the "<agent>: OK" summary line.
func lintAgentLabel(agentName string, forgePlatforms []string) string {
	if len(forgePlatforms) > 1 {
		return fmt.Sprintf("%s: OK (%d forge variants)", agentName, len(forgePlatforms))
	}
	return agentName + ": OK"
}

// lintDiagLine formats a sanitized "<scope>: <message>" line; both parts
// can carry untrusted harness content (e.g. workflow-command injection).
func lintDiagLine(scope, message string) string {
	return agentruntime.SanitizeForDisplay(fmt.Sprintf("%s: %s", scope, message))
}

// lintResourceFiles parses the local provider and OpenShell profile files a
// harness references, using the runtime's parsing and required-field checks.
// Files must lie inside absFullsendDir (symlinks resolved), be regular and
// bounded in size, as in ResolveHarness. URLs are skipped; bare provider names
// are checked against providers/ the way run loads them.
func lintResourceFiles(h *harness.Harness, absFullsendDir string) error {
	local := func(p string) bool {
		return p != "" && !harness.IsURL(p) && harness.IsProviderPath(p) && filepath.IsAbs(p)
	}
	hasBare := false
	var providers []resolve.ResolvedProvider
	for i, p := range h.Providers {
		if p != "" && !harness.IsURL(p) && !harness.IsProviderPath(p) && !filepath.IsAbs(p) {
			hasBare = true
		}
		if !local(p) {
			continue
		}
		content, err := resolve.ReadContainedFile(p, absFullsendDir)
		if err != nil {
			return fmt.Errorf("providers[%d]: %w", i, err)
		}
		def, err := resolve.ParseProviderFile(content, i, p)
		if err != nil {
			return err
		}
		providers = append(providers, resolve.ResolvedProvider{Def: def, LocalPath: p})
	}
	if hasBare {
		if err := lintBareProviderDefs(filepath.Join(absFullsendDir, "providers"), absFullsendDir); err != nil {
			return err
		}
	}
	// The run path rejects providers whose type no declared profile supplies.
	// Remote profiles are not fetched, so their ids are unknown: skip the
	// check then. run also adds the generated GitLab forge profile.
	profiles := []resolve.ResolvedProfile{{ID: "fullsend-gitlab-forge"}}
	remoteProfile := false
	for i, p := range h.OpenShellProfiles() {
		if harness.IsURL(p) {
			remoteProfile = true
		}
		if !local(p) {
			continue
		}
		content, err := resolve.ReadContainedFile(p, absFullsendDir)
		if err != nil {
			return fmt.Errorf("openshell.profiles[%d]: %w", i, err)
		}
		id, err := resolve.ParseProfileID(content)
		if err != nil {
			return fmt.Errorf("openshell.profiles[%d]: %w (from %q)", i, err, p)
		}
		profiles = append(profiles, resolve.ResolvedProfile{ID: id, LocalPath: p})
	}
	if remoteProfile {
		return nil
	}
	return checkProviderProfileIntegrity(providers, profiles)
}

// lintPluginContainment requires every local plugin directory to lie inside
// absFullsendDir (symlinks resolved) before it is inspected: an absolute or
// symlinked plugin path in an untrusted checkout must not make lint read
// directories elsewhere on the runner. The caller must have made the paths
// absolute (ResolveRelativeTo) and must run this before CheckGenerated, which
// walks plugin directories; URL plugins are not fetched here and are skipped.
func lintPluginContainment(h *harness.Harness, absFullsendDir string) error {
	for i, e := range h.Plugins {
		if e.Path == "" || harness.IsURL(e.Path) {
			continue
		}
		if !resolve.IsContainedPath(e.Path, absFullsendDir) {
			return fmt.Errorf("plugins[%d]: path %q is outside workspace root", i, e.Path)
		}
	}
	return nil
}

// lintBareProviderDefs structurally validates the local provider definitions
// that bare provider names select. run loads (and parses) every YAML file in
// providers/ via harness.LoadProviderDefs, so one malformed file fails the
// run; this applies the same parsing rules without creating providers or
// expanding credentials, and reads files with ReadContainedFile.
func lintBareProviderDefs(providersDir, root string) error {
	entries, err := os.ReadDir(providersDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading providers dir: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
			continue
		}
		data, err := resolve.ReadContainedFile(filepath.Join(providersDir, name), root)
		if err != nil {
			return fmt.Errorf("provider file %q: %w", name, err)
		}
		if _, err := harness.ParseProviderDef(data); err != nil {
			return fmt.Errorf("provider file %q: %w", name, err)
		}
	}
	return nil
}

// anyOverlayPossible reports whether any overlay condition can match under
// opts' forge platform and config.
func anyOverlayPossible(whens []string, opts harness.ComposeOpts) bool {
	for _, w := range whens {
		if harness.OverlayWhenPossible(w, opts.ForgePlatform, opts.Config) {
			return true
		}
	}
	return false
}

// lintLoadedHarness composes one harness variant and reports errors and
// diagnostics (skipping those already in linted). passed is false on error.
// deferred is true when a missing validation_loop.script was not reported
// because the composition has overlays (and no forced overlay): the empty
// event drops event-conditioned overlays that may supply the script, so the
// caller's forced-overlay variants decide instead.
func lintLoadedHarness(ctx context.Context, harnessPath string, opts harness.ComposeOpts, label, agentName, absFullsendDir string, orgAllowlist []string, requireConfig bool, linted map[string]bool, result *lintResult, printer *ui.Printer) (passed, deferred bool) {
	h, _, loadErr := harness.LoadWithBase(ctx, harnessPath, opts)
	if loadErr != nil {
		if errors.Is(loadErr, harness.ErrValidationLoopScriptRequired) && opts.ForceOverlays == nil && opts.OverlayWhens != nil && anyOverlayPossible(*opts.OverlayWhens, opts) {
			return true, true
		}
		printer.StepFail(lintDiagLine(label, loadErr.Error()))
		result.errors++
		return false, false
	}

	ok := true

	// allowed_remote_resources also governs runtime fetching; validate always.
	if err := h.ValidateAllowedRemoteResources(orgAllowlist); err != nil {
		printer.StepFail(lintDiagLine(label, err.Error()))
		result.errors++
		return false, false
	}

	// Like run and lock, any URL reference (not just a URL base) needs a
	// config to authorize it.
	if requireConfig && h.HasURLReferences() {
		printer.StepFail(lintDiagLine(label, "URL resources require config.yaml or config.base.yaml with allowed_remote_resources"))
		result.errors++
		return false, false
	}

	// URL references must be allowlisted; check without fetching.
	if err := h.ValidateRemoteResourceAuthorization(orgAllowlist); err != nil {
		printer.StepFail(lintDiagLine(label, err.Error()))
		result.errors++
		return false, false
	}

	// Plugin directories must be contained before CheckGenerated walks them,
	// so resolve paths first (CheckGenerated's own resolution is then a no-op).
	if err := h.ResolveRelativeTo(absFullsendDir); err != nil {
		printer.StepFail(lintDiagLine(label, fmt.Sprintf("resolving paths: %v", err)))
		result.errors++
		return false, false
	}
	if err := lintPluginContainment(h, absFullsendDir); err != nil {
		printer.StepFail(lintDiagLine(label, err.Error()))
		result.errors++
		return false, false
	}

	diags, checkErr := harness.CheckGenerated(h, absFullsendDir)
	if checkErr != nil {
		printer.StepFail(lintDiagLine(label, checkErr.Error()))
		result.errors++
		ok = false
	} else if err := lintResourceFiles(h, absFullsendDir); err != nil {
		printer.StepFail(lintDiagLine(label, err.Error()))
		result.errors++
		ok = false
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
	return ok, false
}
