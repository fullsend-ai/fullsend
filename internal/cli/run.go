package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/binary"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/envfile"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/fetchsvc"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/gitfetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/mintclient"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/statuscomment"
	"github.com/fullsend-ai/fullsend/internal/telemetry"
	"github.com/fullsend-ai/fullsend/internal/ui"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	// maxContextScanDepth is the maximum directory depth for scanning context
	// files. Shared between host-side (scanRepoContextFiles) and sandbox-side
	// (buildScanContextCommand) scans to ensure parity.
	maxContextScanDepth = 5

	// metricsFile is the filename written to the run directory with behavioral metrics.
	metricsFile = "metrics.json"

	// Default agents repository for runtime fallback when an agent is not
	// registered in config. The binary resolves the commit SHA for the
	// floating version tag (config.DefaultUpstreamRef) and fetches the
	// harness dynamically.
	defaultAgentsRepoOwner = "fullsend-ai"
	defaultAgentsRepoName  = "agents"
)

// defaultAgentsRepoURLPrefix is the base URL for fetching agent harnesses
// from the agents repository. It is a var (not const) to allow test overrides.
var defaultAgentsRepoURLPrefix = "https://raw.githubusercontent.com/fullsend-ai/agents/"

// defaultAgentsRepoKnownAgents lists first-party agents available in the
// fullsend-ai/agents repository. Only these agents are eligible for the
// runtime fallback — custom agents are never tried against the agents repo.
//
// This is a transitional mechanism to support agent extraction (see
// docs/plans/agent-extraction-to-agents-repo.md). It will be removed once
// all users have migrated to config-driven agent registration (ADR 0058
// Phase 5 / extraction plan Step 7).
var defaultAgentsRepoKnownAgents = map[string]bool{
	"triage":     true,
	"code":       true,
	"fix":        true,
	"review":     true,
	"retro":      true,
	"prioritize": true,
}

// statusMintToken is the test seam for minting tokens. Shared by both
// setupStatusNotifier (status comment tokens) and mintAgentToken (agent
// runtime tokens). Tests that override it affect both paths.
var statusMintToken = mintclient.MintToken

// agentWorkingDirExcludes lists directory patterns that agents may create
// during execution but must never commit. These are added to
// .git/info/exclude before the agent runs so git ignores them entirely.
var agentWorkingDirExcludes = []string{
	".agentready/",
	".fullsend-workspace/",
}

// resolveFlags groups CLI flags that control remote resource resolution.
type resolveFlags struct {
	offline      bool
	maxDepth     int
	maxResources int
	treeFetcher  gitfetch.TreeFetchFunc // injected by tests; nil means use default
	gitToken     string                 // injected by tests; empty means resolve from env
}

// statusOpts holds the optional status notification parameters for a run.
type statusOpts struct {
	runURL     string
	statusRepo string
	statusNum  int
	mintURL    string
}

// aggregateMetrics holds accumulated behavioral metrics across retry iterations.
type aggregateMetrics struct {
	NumTurns     int     `json:"num_turns"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	TokenUsage   struct {
		Input         int `json:"input"`
		Output        int `json:"output"`
		CacheCreation int `json:"cache_creation"`
		CacheRead     int `json:"cache_read"`
	} `json:"token_usage"`
	Iterations int    `json:"iterations"`
	ToolCalls  int    `json:"tool_calls"`
	Model      string `json:"model,omitempty"`
}

func writeMetricsJSON(dir string, m aggregateMetrics) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, metricsFile), append(data, '\n'), 0o644)
}

var (
	errParsingConfigRuntime = errors.New("parsing config for runtime selection")
	errResolvingRuntime     = errors.New("resolving runtime")
)

func resolveBackendFromConfigData(orgConfigData []byte) (agentruntime.Backend, error) {
	if isOrgConfigData(orgConfigData) {
		orgCfg, orgErr := config.ParseOrgConfig(orgConfigData)
		if orgErr != nil {
			return agentruntime.Backend{}, fmt.Errorf("%w: %w", errParsingConfigRuntime, orgErr)
		}
		backend, resolveErr := agentruntime.ResolveFromConfig(orgCfg)
		if resolveErr != nil {
			return agentruntime.Backend{}, fmt.Errorf("%w: %w", errResolvingRuntime, resolveErr)
		}
		return backend, nil
	}
	perRepoCfg, perRepoErr := config.ParsePerRepoConfig(orgConfigData)
	if perRepoErr != nil {
		return agentruntime.Backend{}, fmt.Errorf("%w: %w", errParsingConfigRuntime, perRepoErr)
	}
	backend, resolveErr := agentruntime.ResolveFromPerRepoConfig(perRepoCfg)
	if resolveErr != nil {
		return agentruntime.Backend{}, fmt.Errorf("%w: %w", errResolvingRuntime, resolveErr)
	}
	return backend, nil
}

func isOrgConfigData(data []byte) bool {
	text := string(data)
	if strings.Contains(text, "fullsend per-repo configuration") {
		return false
	}
	if strings.Contains(text, "fullsend organization configuration") {
		return true
	}
	var probe struct {
		Dispatch *struct {
			Platform string `yaml:"platform"`
		} `yaml:"dispatch"`
		Defaults *struct {
			Roles []string `yaml:"roles"`
		} `yaml:"defaults"`
		Repos map[string]any `yaml:"repos"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return false
	}
	return probe.Dispatch != nil || probe.Defaults != nil || len(probe.Repos) > 0
}

func backendFromConfigFile(path string) (agentruntime.Backend, string, error) {
	data, readErr := os.ReadFile(path)
	source := path
	if readErr != nil && os.IsNotExist(readErr) {
		alt := filepath.Join(filepath.Dir(path), ".fullsend", "config.yaml")
		data, readErr = os.ReadFile(alt)
		if readErr == nil {
			source = alt
		}
	}
	if readErr == nil {
		backend, resolveErr := resolveBackendFromConfigData(data)
		if resolveErr != nil {
			return agentruntime.Backend{}, source, resolveErr
		}
		return backend, source, nil
	}
	if os.IsNotExist(readErr) {
		return agentruntime.Default(), "default (config not found)", nil
	}
	return agentruntime.Backend{}, source, fmt.Errorf("reading config.yaml for runtime selection: %w", readErr)
}

func newRunCmd() *cobra.Command {
	var fullsendDir string
	var outputBase string
	var targetRepo string
	var fullsendBinary string
	var envFiles []string
	var noPostScript bool
	var debugFilter string
	var keepSandbox bool
	var forgeFlag string
	var rFlags resolveFlags
	var sOpts statusOpts

	cmd := &cobra.Command{
		Use:   "run <agent-name>",
		Short: "Run an agent",
		Long:  "Execute an agent by name: read its harness YAML, set up the sandbox, and run the agent.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentName := args[0]
			printer := ui.New(os.Stdout)
			return runAgent(cmd.Context(), agentName, fullsendDir, outputBase, targetRepo, fullsendBinary, envFiles, noPostScript, debugFilter, forgeFlag, rFlags, sOpts, printer, keepSandbox)
		},
	}

	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "path to the .fullsend configuration directory")
	cmd.Flags().StringVar(&outputBase, "output-dir", "", "base directory for run output (default: /tmp/fullsend)")
	cmd.Flags().StringVar(&targetRepo, "target-repo", "", "path to the target repository")
	cmd.Flags().StringVar(&fullsendBinary, "fullsend-binary", "", "path to a Linux fullsend binary to copy into the sandbox (default: current executable)")
	cmd.Flags().StringArrayVar(&envFiles, "env-file", nil, "load environment variables from a dotenv file (repeatable)")
	cmd.Flags().BoolVar(&noPostScript, "no-post-script", false, "skip post-script execution (agent still runs full inference)")
	cmd.Flags().BoolVar(&keepSandbox, "keep-sandbox", false, "skip sandbox and download directory deletion after the run (useful for post-failure inspection)")
	cmd.Flags().StringVar(&debugFilter, "debug", "", `enable Claude Code debug logging with optional category filter (e.g. "api,hooks")`)
	cmd.Flags().Lookup("debug").NoOptDefVal = "*"
	cmd.Flags().StringVar(&forgeFlag, "forge", "", `forge platform to use (e.g. "github", "gitlab"); auto-detected from CI env vars when omitted`)
	cmd.Flags().BoolVar(&rFlags.offline, "offline", false, "reject network fetches; only use cached remote resources")
	cmd.Flags().IntVar(&rFlags.maxDepth, "max-depth", resolve.DefaultMaxDepth, "maximum dependency depth for transitive resolution (0 disables)")
	cmd.Flags().IntVar(&rFlags.maxResources, "max-resources", resolve.DefaultMaxResources, "maximum total remote resources per harness")
	cmd.Flags().StringVar(&sOpts.runURL, "run-url", "", "URL of the CI/CD run for status comments")
	cmd.Flags().StringVar(&sOpts.statusRepo, "status-repo", "", "repository (owner/repo) for status comments")
	cmd.Flags().IntVar(&sOpts.statusNum, "status-number", 0, "issue/PR number for status comments")
	cmd.Flags().StringVar(&sOpts.mintURL, "mint-url", "", "mint service URL for on-demand status tokens (default: $FULLSEND_MINT_URL)")
	_ = cmd.MarkFlagRequired("fullsend-dir")
	_ = cmd.MarkFlagRequired("target-repo")

	return cmd
}

func runAgent(ctx context.Context, agentName, fullsendDir, outputBase, targetRepo, fullsendBinary string, envFiles []string, noPostScript bool, debug string, forgeFlag string, rFlags resolveFlags, sOpts statusOpts, printer *ui.Printer, keepSandbox bool) (runErr error) {
	printer.Banner(Version())
	printer.Blank()
	printer.Header("Running agent: " + agentName)
	printer.Blank()

	if rFlags.maxDepth < 0 {
		return fmt.Errorf("--max-depth must be >= 0, got %d", rFlags.maxDepth)
	}
	if rFlags.maxResources < 1 {
		return fmt.Errorf("--max-resources must be >= 1, got %d", rFlags.maxResources)
	}

	// 0. Load env files before anything else so vars are available for harness expansion.
	for _, ef := range envFiles {
		if err := envfile.Load(ef); err != nil {
			return fmt.Errorf("loading env file %s: %w", ef, err)
		}
	}

	absFullsendDir, err := filepath.Abs(fullsendDir)
	if err != nil {
		return fmt.Errorf("resolving fullsend dir: %w", err)
	}

	// 1. Resolve and load harness.
	harnessStart := time.Now()

	forgePlatform, err := detectForgePlatform(forgeFlag)
	if err != nil {
		printer.StepFail("Invalid --forge flag")
		return err
	}

	policy := fetch.DefaultPolicy
	policy.Offline = rFlags.offline

	// Best-effort org config loading — provides the allowlist for base
	// harness fetching and the agent registry for config-driven resolution.
	// If the file is missing or unparseable we proceed without it;
	// HasURLReferences will enforce its presence later if needed.
	orgConfigPath := filepath.Join(absFullsendDir, "config.yaml")
	orgCfg := tryLoadOrgConfig(orgConfigPath, printer)
	// Fallback for absent config; EnsureDefaultAllowedRemoteResources
	// handles the omitted-field case when a config is present.
	orgAllowlist := config.DefaultAllowedRemoteResources()
	if orgCfg != nil {
		orgAllowlist = orgCfg.AllowedResources()
	}

	composeGitToken := rFlags.gitToken
	if composeGitToken == "" {
		var tokenErr error
		composeGitToken, tokenErr = resolveToken()
		if tokenErr != nil {
			printer.StepWarn("Git token not available; private repo skill fetches may fail")
		}
	}

	composeOpts := harness.ComposeOpts{
		WorkspaceRoot: absFullsendDir,
		FetchPolicy:   policy,
		AuditLogPath:  filepath.Join(absFullsendDir, ".fullsend-cache", "fetch-audit.jsonl"),
		ForgePlatform: forgePlatform,
		OrgAllowlist:  orgAllowlist,
		TreeFetcher:   rFlags.treeFetcher,
		GitToken:      composeGitToken,
	}

	// Resolve agent source: config agents take precedence, then agents repo
	// fallback, then disk harnesses.
	var fallbackForgeClient forge.Client
	if composeGitToken != "" {
		fallbackForgeClient = gh.New(composeGitToken)
	}
	harnessPath, fetchDeps, err := resolveAgentSource(ctx, absFullsendDir, agentName, fallbackForgeClient, orgCfg, composeOpts, printer)
	if err != nil {
		return err
	}

	printer.StepStart("Loading harness: " + harnessPath)

	// If the agent was fetched from a URL, forward the source URL so
	// LoadWithBase can resolve relative resources even without a base:
	// field (ADR-0045 resource resolution for config-registered agents).
	if len(fetchDeps) > 0 && fetchDeps[0].URL != "" {
		composeOpts.SourceURL = fetchDeps[0].URL
	}

	// If the harness has a URL base and org config failed to load,
	// load it strictly now so LoadWithBase gets a proper error path
	// rather than an unhelpful "URL base requires allowed_remote_resources".
	if orgCfg == nil {
		if rawH, rawErr := harness.LoadRaw(harnessPath); rawErr == nil && rawH.Base != "" && harness.IsURL(rawH.Base) {
			var err error
			orgCfg, err = requireOrgConfig(orgConfigPath, printer)
			if err != nil {
				return err
			}
			composeOpts.OrgAllowlist = orgCfg.AllowedResources()
		}
	}

	h, baseDeps, err := harness.LoadWithBase(ctx, harnessPath, composeOpts)
	if err != nil {
		printer.StepFail("Failed to load harness")
		return fmt.Errorf("loading harness: %w", err)
	}

	allDeps := append(fetchDeps, baseDeps...)
	for _, dep := range allDeps {
		if dep.CacheHit {
			printer.StepInfo(fmt.Sprintf("Base: %s (cache hit)", dep.URL))
		} else {
			printer.StepInfo(fmt.Sprintf("Base: %s (fetched)", dep.URL))
		}
		if dep.Warning != "" {
			printer.StepWarn(fmt.Sprintf("Base: %s", dep.Warning))
		}
	}

	if err := h.ResolveRelativeTo(absFullsendDir); err != nil {
		printer.StepFail("Path validation failed")
		return fmt.Errorf("resolving paths: %w", err)
	}

	// Declare result outside the URL references block so it's accessible
	// later for profile import and provider resolution.
	var result resolve.ResolveResult

	if h.HasURLReferences() {
		if orgCfg == nil {
			var err error
			orgCfg, err = requireOrgConfig(orgConfigPath, printer)
			if err != nil {
				return err
			}
		}

		if err := h.ValidateAllowedRemoteResources(orgCfg.AllowedResources()); err != nil {
			printer.StepFail("Remote resource allowlist validation failed")
			return fmt.Errorf("validating allowed remote resources: %w", err)
		}

		// Check for a lock file with a current entry for this harness.
		usedLock := false

		lockPath := filepath.Join(absFullsendDir, "lock.yaml")
		lf, lockErr := lock.Load(lockPath)
		if lockErr != nil {
			printer.StepWarn("Could not load lock file: " + lockErr.Error())
		}

		if lf != nil {
			if entry := lf.Lookup(agentName); entry != nil {
				harnessData, hashErr := os.ReadFile(harnessPath)
				if hashErr != nil {
					return fmt.Errorf("reading harness file for lock check: %w", hashErr)
				}
				harnessHash := fetch.ComputeSHA256(harnessData)

				if entry.IsStale(harnessHash) {
					printer.StepWarn(fmt.Sprintf("Harness has changed since lock file was generated. Run 'fullsend lock %s --fullsend-dir %s' to update.", agentName, fullsendDir))
				} else {
					printer.StepStart("Using pinned dependencies from lock file")
					lockResult, lockResolveErr := resolveFromLock(h, entry, absFullsendDir, printer)
					if lockResolveErr != nil {
						printer.StepFail("Lock file resolution failed: " + lockResolveErr.Error())
						printer.StepWarn("Falling back to normal resolution")
					} else {
						result = lockResult
						usedLock = true
						printer.StepDone(fmt.Sprintf("Resolved %d dependencies from lock file", len(result.Deps)))
					}
				}
			}
		}

		if !usedLock {
			resolveGitToken := rFlags.gitToken
			if resolveGitToken == "" {
				var tokenErr error
				resolveGitToken, tokenErr = resolveToken()
				if tokenErr != nil {
					printer.StepWarn("Git token not available; private repo skill fetches may fail")
				}
			}

			var resolveErr error
			result, resolveErr = resolve.ResolveHarness(ctx, h, resolve.ResolveOpts{
				WorkspaceRoot: absFullsendDir,
				FetchPolicy:   policy,
				AuditLogPath:  filepath.Join(absFullsendDir, ".fullsend-cache", "fetch-audit.jsonl"),
				MaxDepth:      rFlags.maxDepth,
				MaxResources:  rFlags.maxResources,
				TreeFetcher:   rFlags.treeFetcher,
				GitToken:      resolveGitToken,
			})
			if resolveErr != nil {
				printer.StepFail("Remote resource resolution failed")
				return fmt.Errorf("resolving remote resources: %w", resolveErr)
			}
		}

		for _, dep := range result.Deps {
			if dep.CacheHit {
				printer.StepInfo(fmt.Sprintf("Resolved %s (cache hit)", dep.URL))
			} else {
				printer.StepInfo(fmt.Sprintf("Fetched %s -> %s", dep.URL, dep.LocalPath))
			}
			if dep.Warning != "" {
				printer.StepWarn(dep.Warning)
			}
		}
	}

	if resolved, overridden := applySandboxImageOverride(h.Image); overridden {
		printer.StepInfo(fmt.Sprintf("Image override via FULLSEND_SANDBOX_IMAGE: %s -> %s", h.Image, resolved))
		h.Image = resolved
	}

	// Mint agent token when a mint URL and harness role are both available.
	// Runs before env expansion so minted tokens flow into RunnerEnv and
	// host_files via os.Getenv automatically.
	mintURL := sOpts.mintURL
	if mintURL == "" {
		mintURL = os.Getenv("FULLSEND_MINT_URL")
	}
	minted, mintCleanup, err := mintAgentToken(ctx, h.Role, mintURL, printer)
	if err != nil {
		return fmt.Errorf("agent token minting failed: %w", err)
	}
	if mintCleanup != nil {
		defer mintCleanup()
	}
	if !minted && mintURL == "" {
		printer.StepWarn("No --mint-url provided; skipping token minting for role " + h.Role)
	}

	// Expand env vars in runner_env values. FULLSEND_DIR is injected so
	// harness configs can reference files relative to the fullsend directory
	// (e.g., ${FULLSEND_DIR}/schemas/triage-result.schema.json).
	expander := func(key string) string {
		if key == "FULLSEND_DIR" {
			return absFullsendDir
		}
		return os.Getenv(key)
	}
	lookup := func(key string) (string, bool) {
		if key == "FULLSEND_DIR" {
			return absFullsendDir, true
		}
		return os.LookupEnv(key)
	}
	if err := h.ValidateRunnerEnvWith(lookup); err != nil {
		printer.StepFail("Environment validation failed")
		return fmt.Errorf("validating env: %w", err)
	}
	for k, v := range h.RunnerEnv {
		h.RunnerEnv[k] = os.Expand(v, expander)
	}

	// Expand ${VAR} references in env.runner and env.sandbox (ADR 0055).
	if h.Env != nil {
		for k, v := range h.Env.Runner {
			h.Env.Runner[k] = os.Expand(v, expander)
		}
		for k, v := range h.Env.Sandbox {
			h.Env.Sandbox[k] = os.Expand(v, expander)
		}
	}

	// Expand ${VAR} references in validation_loop.schema so the path
	// resolves before ValidateFilesExist stat-checks it.
	if h.ValidationLoop != nil && strings.Contains(h.ValidationLoop.Schema, "${") {
		h.ValidationLoop.Schema = os.Expand(h.ValidationLoop.Schema, expander)
	}

	if err := h.ValidateFilesExist(); err != nil {
		printer.StepFail("File validation failed")
		return fmt.Errorf("validating files: %w", err)
	}
	// Ensure scripts are executable. The GitHub Contents API does not
	// preserve file permissions, so scripts written via admin install
	// may lack the execute bit.
	for _, script := range h.Scripts() {
		if script != "" {
			if chmodErr := os.Chmod(script, 0o755); chmodErr != nil {
				printer.StepWarn("Could not chmod " + script + ": " + chmodErr.Error())
			}
		}
	}
	printer.StepDone(fmt.Sprintf("Harness loaded (%.1fs)", time.Since(harnessStart).Seconds()))

	// Run lint checks before merging env.runner into RunnerEnv so that
	// Lint() sees the original YAML state and only warns when runner_env
	// was actually declared (not when env.runner entries are merged in).
	for _, diag := range h.Lint() {
		emitDiagnostic(printer, diag)
	}

	// ADR 0055: build effective runner env — start with runner_env,
	// overlay env.runner so the new field takes precedence.
	effectiveRunnerEnv := make(map[string]string)
	for k, v := range h.RunnerEnv {
		effectiveRunnerEnv[k] = v
	}
	if h.Env != nil {
		for k, v := range h.Env.Runner {
			effectiveRunnerEnv[k] = v
		}
	}
	// NOTE: after this point h.RunnerEnv contains the merged effective set
	// (runner_env + env.runner), not just the declared runner_env entries.
	h.RunnerEnv = effectiveRunnerEnv

	// Print plan.
	printer.KeyValue("Agent", h.Agent)
	if h.Role != "" {
		printer.KeyValue("Role", h.Role)
	}
	if h.Slug != "" {
		printer.KeyValue("Slug", h.Slug)
	}
	if h.Policy != "" {
		printer.KeyValue("Policy", h.Policy)
	}
	if h.Model != "" {
		printer.KeyValue("Model", h.Model)
	}
	if h.Image != "" {
		printer.KeyValue("Image", h.Image)
	}
	if len(h.Providers) > 0 {
		printer.KeyValue("Providers", strings.Join(h.Providers, ", "))
	}
	if len(h.Skills) > 0 {
		printer.KeyValue("Skills", strings.Join(h.Skills, ", "))
	}
	if len(h.Plugins) > 0 {
		printer.KeyValue("Plugins", strings.Join(h.Plugins, ", "))
	}
	if h.AgentInput != "" {
		printer.KeyValue("Agent input", h.AgentInput)
	}
	if h.PreScript != "" {
		printer.KeyValue("Pre-script", h.PreScript)
	}
	if h.PostScript != "" {
		if noPostScript {
			printer.KeyValue("Post-script", h.PostScript+" (SKIPPED: --no-post-script)")
		} else {
			printer.KeyValue("Post-script", h.PostScript)
		}
	}
	if h.TimeoutMinutes > 0 {
		printer.KeyValue("Timeout", fmt.Sprintf("%d minutes", h.TimeoutMinutes))
	}
	printer.Blank()

	// 1b. Log token scope for debugging cross-org issues (see #1321).
	// Non-fatal: if the check fails (e.g., non-installation token), log a
	// warning and continue.
	if ghToken := os.Getenv("GH_TOKEN"); ghToken != "" {
		repos, err := fetchTokenScope(context.Background(), ghToken, "https://api.github.com")
		if err != nil {
			printer.StepWarn("Token scope check: " + err.Error())
		} else if len(repos) > 0 {
			printer.KeyValue("Token scoped to", strings.Join(repos, ", "))
		} else if repos != nil {
			printer.StepWarn("Token is an installation token but has access to 0 repositories")
		}
	}

	// 1c. Set up status notifications (comments on the issue/PR).
	// Lives in the CLI layer (not harness or post-script) so it wraps the
	// entire run lifecycle including sandbox setup, validation loop, and
	// post-script — and can report cancellation/failure even when the
	// sandbox never starts. See #1859.
	if sOpts.statusRepo != "" && sOpts.statusNum > 0 {
		notifier, notifyErr := setupStatusNotifier(absFullsendDir, h.Role, sOpts, printer)
		if notifyErr != nil {
			printer.StepWarn("Status notifications disabled: " + notifyErr.Error())
		} else {
			description := titleCase(strings.ReplaceAll(agentName, "-", " "))
			if err := notifier.PostStart(ctx, description); err != nil {
				printer.StepWarn("Failed to post start status: " + err.Error())
			} else {
				printer.StepDone("Posted start status comment")
			}
			defer func() {
				status := "success"
				if ctx.Err() != nil {
					status = "cancelled"
				} else if runErr != nil {
					status = "failure"
				}
				dCtx, dCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
				defer dCancel()
				if err := notifier.PostCompletion(dCtx, description, status); err != nil {
					printer.StepWarn("Failed to post completion status: " + err.Error())
				}
			}()
		}
	}

	// 2. Check openshell availability.
	openshellStart := time.Now()
	printer.StepStart("Checking openshell availability")
	if err := sandbox.EnsureAvailable(); err != nil {
		printer.StepFail("openshell not available")
		return fmt.Errorf("openshell is required: %w", err)
	}
	printer.StepDone(fmt.Sprintf("openshell available (%.1fs)", time.Since(openshellStart).Seconds()))

	// 2a. Check that a gateway is running.
	gatewayStart := time.Now()
	printer.StepStart("Checking gateway")
	if err := sandbox.CheckGateway(); err != nil {
		printer.StepFail("Gateway not running")
		return fmt.Errorf("gateway check failed: %w", err)
	}
	printer.StepDone(fmt.Sprintf("Gateway available (%.1fs)", time.Since(gatewayStart).Seconds()))

	// 2b. Validate referential integrity before any gateway mutations.
	// Dedupe URL-resolved providers (last-wins) so shadowed entries from
	// base composition don't trigger false integrity errors.
	result.Providers = dedupResolvedProviders(result.Providers)
	if w, intErr := checkProviderProfileIntegrity(result.Providers, result.Profiles); intErr != nil {
		printer.StepFail("Provider references unknown profile type")
		return intErr
	} else if w != "" {
		printer.StepWarn(w)
	}

	// 2c. Ensure providers v2 is enabled and import profiles + providers.
	// Profiles are a providers-v2 concept (ADR 0065), so EnableProvidersV2
	// must run before any profile import — both URL-resolved and directory.
	// Only harness-declared and URL-resolved providers are loaded and created;
	// directory providers not referenced by this harness are skipped entirely.
	result.Profiles = dedupResolvedProfiles(result.Profiles)
	allProviderNames := append([]string{}, h.Providers...)
	if len(h.Providers) > 0 || len(result.Providers) > 0 || len(result.Profiles) > 0 {
		// Enable provider-backed policy composition on the gateway.
		provV2Start := time.Now()
		printer.StepStart("Enabling providers v2")
		if err := sandbox.EnableProvidersV2(); err != nil {
			printer.StepFail("Failed to enable providers v2")
			return fmt.Errorf("enabling providers v2: %w", err)
		}
		printer.StepDone(fmt.Sprintf("Providers v2 enabled (%.1fs)", time.Since(provV2Start).Seconds()))

		// Import URL-resolved profiles to the gateway.
		for _, rp := range result.Profiles {
			profileStart := time.Now()
			printer.StepStart("Importing profile: " + rp.ID)
			if err := sandbox.ImportProfile(ctx, rp.ID, rp.LocalPath); err != nil {
				printer.StepFail("Failed to import profile " + rp.ID)
				return fmt.Errorf("importing profile %q: %w", rp.ID, err)
			}
			printer.StepDone(fmt.Sprintf("Profile imported: %s (%.1fs)", rp.ID, time.Since(profileStart).Seconds()))
		}

		// Import provider profiles (if profiles/ directory exists).
		profilesDir := filepath.Join(absFullsendDir, "profiles")
		dirProfileStart := time.Now()
		printer.StepStart("Importing provider profiles")
		if err := sandbox.ImportProfiles(profilesDir); err != nil {
			printer.StepFail("Failed to import provider profiles")
			return fmt.Errorf("importing provider profiles: %w", err)
		}
		printer.StepDone(fmt.Sprintf("Provider profiles imported (%.1fs)", time.Since(dirProfileStart).Seconds()))

		providersDir := filepath.Join(absFullsendDir, "providers")
		declared := make(map[string]struct{}, len(h.Providers))
		for _, p := range h.Providers {
			declared[p] = struct{}{}
		}
		localDefs, err := harness.LoadProviderDefs(providersDir, declared)
		if err != nil {
			printer.StepFail("Failed to load provider definitions")
			return fmt.Errorf("loading provider definitions: %w", err)
		}

		allDefs, shadowedProviders := mergeProviderDefs(localDefs, result.Providers)
		for _, name := range shadowedProviders {
			printer.StepWarn(fmt.Sprintf("Local provider %q shadows URL-resolved provider of the same name", name))
		}
		urlProviderNames := make(map[string]bool, len(result.Providers))
		for _, rp := range result.Providers {
			urlProviderNames[rp.Def.Name] = true
		}
		for _, name := range shadowedProviders {
			delete(urlProviderNames, name)
		}

		var (
			mu   sync.Mutex
			wg   sync.WaitGroup
			errs []error
		)
		for _, pd := range allDefs {
			wg.Add(1)
			go func(pd harness.ProviderDef) {
				defer wg.Done()
				providerStart := time.Now()
				printer.StepStart("Ensuring provider: " + pd.Name)
				if err := sandbox.EnsureProvider(ctx, pd.Name, pd.Type, pd.Credentials, pd.Config, urlProviderNames[pd.Name]); err != nil {
					printer.StepFail("Failed to create provider " + pd.Name)
					mu.Lock()
					errs = append(errs, fmt.Errorf("ensuring provider %q: %w", pd.Name, err))
					mu.Unlock()
					return
				}
				printer.StepDone(fmt.Sprintf("Provider ready: %s (%.1fs)", pd.Name, time.Since(providerStart).Seconds()))
			}(pd)
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return err
		}
		created := make(map[string]struct{}, len(allDefs))
		for _, pd := range allDefs {
			created[pd.Name] = struct{}{}
		}
		for _, p := range h.Providers {
			if _, ok := created[p]; !ok {
				printer.StepWarn(fmt.Sprintf("Provider %q declared in harness but no definition found in %s", p, providersDir))
			}
		}

		allProviderNames = sandboxProviderNames(h.Providers, result.Providers)
	}

	workItemID := resolveWorkItemID()

	// 3. Create run directory and initialise tracer.
	sandboxName := fmt.Sprintf("agent-%s-%d-%d", agentName, os.Getpid(), time.Now().Unix())
	if outputBase == "" {
		outputBase = filepath.Join(os.TempDir(), "fullsend")
	}
	runDir := filepath.Join(outputBase, sandboxName)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return fmt.Errorf("creating run directory: %w", err)
	}

	// OTel SDK tracer (ADR 0050). Setup creates a TracerProvider with a
	// file exporter (always) and an OTLP exporter (when configured). On
	// failure it returns a noop tracer so telemetry never affects the run.
	// An inbound TRACEPARENT is adopted via the W3C propagator so the root
	// span continues the parent trace.
	var lastExitCode int
	var transcriptErrorOverride bool
	var runCount int
	var aggMetrics aggregateMetrics
	tracer, tracingCleanup := telemetry.Setup(runDir, Version())
	tid := resolveTraceIdentity(ctx, tracer, os.Getenv("TRACEPARENT"), os.Getenv("TRACESTATE"), []attribute.KeyValue{
		attribute.String("fullsend.agent", agentName),
		attribute.String("fullsend.work_item_id", workItemID),
		attribute.String("gen_ai.operation.name", "invoke_agent"),
		attribute.String("gen_ai.agent.name", agentName),
	})
	ctx = tid.Ctx
	rootSpan := tid.RootSpan
	traceparent := tid.Traceparent
	securityTraceID := security.GenerateTraceID()
	rootSpan.SetAttributes(attribute.String("fullsend.security_trace_id", securityTraceID))

	defer func() {
		exitCode := telemetryExitCode(lastExitCode, runErr)

		rootSpan.SetAttributes(
			attribute.Int("exit_code", exitCode),
		)
		if runCount > 0 {
			rootSpan.SetAttributes(
				attribute.String("gen_ai.request.model", aggMetrics.Model),
				attribute.Int("gen_ai.usage.input_tokens", aggMetrics.TokenUsage.Input),
				attribute.Int("gen_ai.usage.output_tokens", aggMetrics.TokenUsage.Output),
				attribute.Int("gen_ai.usage.cache_creation.input_tokens", aggMetrics.TokenUsage.CacheCreation),
				attribute.Int("gen_ai.usage.cache_read.input_tokens", aggMetrics.TokenUsage.CacheRead),
				attribute.Float64("fullsend.cost_usd", roundUSD(aggMetrics.TotalCostUSD)),
				attribute.Int("fullsend.num_turns", aggMetrics.NumTurns),
				attribute.Int("fullsend.tool_calls", aggMetrics.ToolCalls),
				attribute.Int("fullsend.iterations", runCount),
			)
		}

		if runErr != nil {
			rootSpan.SetStatus(codes.Error, runErr.Error())
		} else {
			rootSpan.SetStatus(codes.Ok, "")
		}
		rootSpan.End()

		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tracingCleanup(flushCtx)
	}()

	// 4. Run pre-script on the host (if configured).
	if h.PreScript != "" {
		preStart := time.Now()
		printer.StepStart("Running pre-script: " + h.PreScript)
		preCmd := exec.Command(h.PreScript)
		preCmd.Env = childScriptEnv(h.RunnerEnv, traceparent)
		preCmd.Stdout = os.Stdout
		preCmd.Stderr = os.Stderr
		if err := preCmd.Run(); err != nil {
			printer.StepFail("Pre-script failed")
			return fmt.Errorf("running pre-script: %w", err)
		}
		printer.StepDone(fmt.Sprintf("Pre-script completed (%.1fs)", time.Since(preStart).Seconds()))
	}

	// 4a. Create sandbox.
	createStart := time.Now()
	printer.StepStart("Creating sandbox: " + sandboxName)
	_, sandboxSpan := tracer.Start(ctx, "sandbox_create", trace.WithAttributes(
		attribute.String("gen_ai.operation.name", "create_agent"),
	))

	readyTimeout := time.Duration(h.SandboxTimeoutSeconds) * time.Second
	if err := sandbox.CreateWithRetry(sandboxName, allProviderNames, h.Image, h.Policy, sandbox.DefaultMaxCreateAttempts, readyTimeout); err != nil {
		sandboxSpan.SetStatus(codes.Error, err.Error())
		sandboxSpan.End()
		printer.StepFail("Failed to create sandbox")
		return fmt.Errorf("creating sandbox: %w", err)
	}
	sandboxSpan.SetStatus(codes.Ok, "")
	sandboxSpan.End()

	// validationPassed is declared here (before the post-script defer) so the
	// defer closure can guard on it. The post-script must only run when
	// validation has passed — running it on unvalidated output would violate
	// ADR 0022's zero-trust model.
	var validationPassed bool

	// repoExtractedOK tracks whether hostRepositoryDownloadDir is safe
	// and corresponds to the validated iteration. It is false when:
	//   - the last SafeDownload call failed (dir may be missing/unsanitized), or
	//   - the post-loop sweep validated an earlier iteration (dir holds a
	//     different iteration's checkout than what was validated).
	// Callers (validation, post-script) must not use the dir when false.
	var repoExtractedOK bool

	// validatedIterNum records which iteration passed validation (1-based),
	// or 0 if none. Set by inline validation (step 9e break) or the
	// post-loop sweep. The post-script defer uses it to communicate
	// FULLSEND_VALIDATED_ITERATION_DIR to the post-script so it selects
	// the correct iteration's output rather than blindly taking the last.
	var validatedIterNum int

	// Download-dir cleanup is registered first so LIFO runs it last —
	// after the post-script defer has finished using it.
	hostRepositoryDownloadDir := filepath.Join(os.TempDir(), sandboxName)
	defer func() {
		if keepSandbox {
			return
		}
		if err := forceRemoveAll(hostRepositoryDownloadDir); err != nil {
			printer.StepWarn("Failed to remove download dir: " + err.Error())
		} else {
			printer.StepDone(fmt.Sprintf("Download directory removed: %s", hostRepositoryDownloadDir))
		}
	}()

	// Post-script runs after sandbox cleanup (defers are LIFO).
	// When a validation_loop is configured, the post-script only runs if
	// validation passed (ADR 0022). When no validation_loop exists (e.g.,
	// the code agent), the post-script runs unconditionally after a
	// successful agent run — the post-script itself is responsible for
	// any output checks it needs.
	if h.PostScript != "" {
		defer func() {
			if noPostScript {
				printer.StepWarn(fmt.Sprintf("Skipping post-script %s: --no-post-script", h.PostScript))
				return
			}
			if h.ValidationLoop != nil && !validationPassed {
				printer.StepWarn("Skipping post-script: validation did not pass")
				return
			}
			if runErr != nil {
				printer.StepWarn("Skipping post-script: agent run failed")
				return
			}
			if transcriptErrorOverride {
				printer.StepWarn("Skipping post-script: agent reported error via transcript")
				return
			}
			postStart := time.Now()
			printer.StepStart("Running post-script: " + h.PostScript)
			postCmd := exec.Command(h.PostScript)
			postCmd.Dir = runDir
			postCmd.Env = childScriptEnv(h.RunnerEnv, traceparent)
			// Override REPO_DIR from childScriptEnv: the harness value points to a fixed
			// location, but sandbox output is now extracted to a temp dir. exec uses
			// last-value-wins so this append takes precedence. TODO(fullsend-ai/agents#191):
			// remove REPO_DIR from RunnerEnv entirely once harnesses no longer set it.
			//
			// Pass REPO_DIR only when repoExtractedOK is true: the last
			// SafeDownload succeeded AND corresponds to the validated
			// iteration (repoExtractedOK is forced false by the sweep when
			// it validates an earlier iteration — see its doc comment).
			// Passing a stale or missing dir would expose the post-script
			// to unsanitized or wrong-iteration content.
			//
			// post-fix.sh and post-code.sh both fail closed on an empty
			// REPO_DIR in their own script logic (via ${REPO_DIR:-repo} +
			// directory existence check) — both need actual repo content to
			// push. The other validation_loop post-scripts (post-review.sh,
			// post-triage.sh, post-retro.sh, post-prioritize.sh) don't
			// reference REPO_DIR at all; they rely on
			// FULLSEND_VALIDATED_ITERATION_DIR (set below) to select the
			// correct iteration's output. code.yaml has no validation_loop,
			// so post-code.sh cannot currently observe an empty REPO_DIR in
			// practice — a SafeDownload failure is fatal for it and this
			// defer never runs — but the check in its script is real, not a
			// dead branch, and would activate the moment code.yaml gained a
			// validation_loop. Because there is no per-iteration repo
			// checkout, post-fix.sh (and post-code.sh, were it to gain a
			// validation_loop) cannot recover a sweep-validated non-final
			// iteration's repo state — it fails closed with "Extracted repo
			// not found" instead of pushing. See #5393 follow-up.
			postRepoDir, postValidatedIterDir := postScriptRepoEnv(h, runDir, hostRepositoryDownloadDir, repoExtractedOK, validatedIterNum)
			postCmd.Env = append(postCmd.Env, fmt.Sprintf("REPO_DIR=%s", postRepoDir))
			// FULLSEND_VALIDATED_ITERATION_DIR tells the post-script which
			// iteration's output was validated. Without this, post-scripts
			// that scan for the last iteration-*/output would pick up
			// unvalidated output when the sweep validated an earlier
			// iteration. Empty when no validation loop is configured or
			// when no iteration passed validation (the post-script is
			// skipped in the latter case, so this is defensive).
			if postValidatedIterDir != "" {
				postCmd.Env = append(postCmd.Env, fmt.Sprintf("FULLSEND_VALIDATED_ITERATION_DIR=%s", postValidatedIterDir))
			}
			postCmd.Stdout = os.Stdout
			postCmd.Stderr = os.Stderr
			if err := postCmd.Run(); err != nil {
				printer.StepFail("Post-script failed: " + err.Error())
				if runErr == nil {
					runErr = fmt.Errorf("post-script %s failed: %w", h.PostScript, err)
				}
			} else {
				printer.StepDone(fmt.Sprintf("Post-script completed (%.1fs)", time.Since(postStart).Seconds()))
			}
		}()
	}
	defer func() {
		// Collect OpenShell logs before sandbox deletion for post-mortem debugging.
		collectOpenshellLogs(sandboxName, runDir, printer)

		if keepSandbox {
			printer.StepWarn(fmt.Sprintf("Sandbox kept (--keep-sandbox): %s", sandboxName))
			printer.StepInfo(fmt.Sprintf("openshell sandbox exec --tty --name %s -- bash", sandboxName))
			return
		}

		cleanupStart := time.Now()
		printer.StepStart("Cleaning up sandbox")
		if err := sandbox.Delete(sandboxName); err != nil {
			printer.StepWarn("Sandbox cleanup failed: " + err.Error())
		} else {
			printer.StepDone(fmt.Sprintf("Sandbox deleted (%.1fs)", time.Since(cleanupStart).Seconds()))
		}
	}()
	printer.StepDone(fmt.Sprintf("Sandbox created (%.1fs)", time.Since(createStart).Seconds()))

	// 5. Resolve target repo path (needed by bootstrap for env vars).
	hostRepositoryDir, err := filepath.Abs(targetRepo)
	if err != nil {
		return fmt.Errorf("resolving target repo path: %w", err)
	}
	repoName := filepath.Base(hostRepositoryDir)
	remoteRepositoryDir := fmt.Sprintf("%s/%s", sandbox.SandboxWorkspace, repoName)

	// 6. Start runtime fetch service (Phase 4, ADR-0038).
	var fetchEnvVal fetchServiceEnv
	startFetch, deprecationWarning := shouldStartFetchService(h)
	if deprecationWarning != "" {
		printer.StepWarn(deprecationWarning)
	}
	if startFetch {
		env, fetchShutdown, fetchErr := setupFetchService(ctx, rFlags.treeFetcher, rFlags.gitToken, h, resolveToken, fetchsvc.ServiceConfig{
			Harness:       h,
			FetchPolicy:   fetch.DefaultPolicy,
			WorkspaceRoot: absFullsendDir,
			AuditLogPath:  filepath.Join(absFullsendDir, ".fullsend-cache", "fetch-audit.jsonl"),
			TraceID:       securityTraceID,
			SandboxName:   sandboxName,
			MaxFetches:    h.EffectiveMaxRuntimeFetches(),
			Uploader:      &fetchsvc.SandboxUploader{},
			SkillDestDir:  sandbox.SandboxClaudeConfig + "/skills",
		}, printer.StepWarn)
		if fetchErr != nil {
			printer.StepWarn("Runtime fetch service failed to start: " + fetchErr.Error())
		} else {
			defer fetchShutdown()
			fetchEnvVal = env
		}
	}

	// 7. Bootstrap sandbox.
	var backend agentruntime.Backend
	orgConfigPath = filepath.Join(absFullsendDir, "config.yaml")
	backend, configSource, backendErr := backendFromConfigFile(orgConfigPath)
	if backendErr != nil {
		switch {
		case errors.Is(backendErr, errParsingConfigRuntime):
			printer.StepFail("Failed to parse config.yaml")
		case errors.Is(backendErr, errResolvingRuntime):
			printer.StepFail("Failed to resolve runtime")
		default:
			printer.StepFail("Failed to load config.yaml")
		}
		return backendErr
	}
	fmt.Fprintf(os.Stderr, "runtime: selected %q from %s\n", backend.Runtime.Name(), configSource)
	rt := backend.Runtime
	tx := backend.Transcripts
	bootstrapStart := time.Now()
	printer.StepStart("Bootstrapping sandbox")
	boot := newHarnessBootstrap(h, sandboxName, agentName)
	if h.SecurityEnabled() {
		// Scan all runtime content before upload so warnings surface together.
		// Host files could change between scan and upload; the runner owns the host FS here.
		if err := scanRuntimeContent(boot, h.FailModeClosed()); err != nil {
			printer.StepFail("Failed to bootstrap sandbox")
			return err
		}
	}
	if err := bootstrapCommon(sandboxName, fullsendBinary, h); err != nil {
		printer.StepFail("Failed to bootstrap sandbox")
		return err
	}
	if err := bootstrapEnv(sandboxName, remoteRepositoryDir, h, rt.EnvExports(), fetchEnvVal); err != nil {
		printer.StepFail("Failed to bootstrap sandbox")
		return err
	}
	if err := rt.Bootstrap(boot); err != nil {
		printer.StepFail("Failed to bootstrap sandbox")
		return err
	}
	printer.StepDone(fmt.Sprintf("Sandbox bootstrapped (%.1fs)", time.Since(bootstrapStart).Seconds()))

	// 8. Make project code available (copy repo root into a named subdirectory).
	copyStart := time.Now()
	printer.StepStart("Copying project code into sandbox")
	if err := sandbox.UploadDir(sandboxName, hostRepositoryDir, remoteRepositoryDir); err != nil {
		printer.StepFail("Failed to copy project code")
		return fmt.Errorf("copying project code: %w", err)
	}
	printer.StepDone(fmt.Sprintf("Project code copied to %s/ (%.1fs)", repoName, time.Since(copyStart).Seconds()))

	// 8a. Inject org-level AGENTS.md if the target repo does not have one.
	// The scaffold ships a default AGENTS.md with baseline behavioral
	// guidelines. Skills already instruct agents to read AGENTS.md from
	// the project root — this ensures there is something to read even
	// when the target repo has not authored its own.
	agentsMDAvailable := hasAgentsMD(hostRepositoryDir)
	if !agentsMDAvailable {
		orgAgentsMD := filepath.Join(absFullsendDir, "AGENTS.md")
		if _, err := os.Stat(orgAgentsMD); err == nil {
			if err := sandbox.UploadFile(sandboxName, orgAgentsMD, remoteRepositoryDir+"/AGENTS.md"); err != nil {
				printer.StepWarn("Could not inject org AGENTS.md: " + err.Error())
			} else {
				agentsMDAvailable = true
				// Hide the injected file from git status so agents don't stage it.
				excludeCmd := fmt.Sprintf("echo 'AGENTS.md' >> %s/.git/info/exclude", remoteRepositoryDir)
				if _, _, _, err := sandbox.Exec(sandboxName, excludeCmd, 5*time.Second); err != nil {
					printer.StepWarn("Could not add AGENTS.md to git exclude: " + err.Error())
				}
				printer.StepDone("Injected org-level AGENTS.md (target repo has none)")
			}
		}
	}

	// 8a.1. Inject a minimal CLAUDE.md pointer when running Claude Code
	// against repos that have AGENTS.md but no CLAUDE.md. Claude Code
	// auto-loads CLAUDE.md into its system context but does not read
	// AGENTS.md by default. Without this bridge file, agents are
	// effectively context-blind in repos that only have AGENTS.md.
	if rt.Name() == "claude" && agentsMDAvailable && !hasClaudeMD(hostRepositoryDir) {
		injectClaudeMDPointer(sandboxName, remoteRepositoryDir, printer)
	}

	// 8a-2. Exclude agent working directories from git tracking.
	// Agents may create working directories (e.g. .agentready/) during
	// execution. These must never appear in commits. Adding them to
	// .git/info/exclude ensures git status/add ignores them entirely.
	if err := excludeAgentWorkingDirs(sandboxName, remoteRepositoryDir, printer); err != nil {
		printer.StepWarn("Could not exclude agent working dirs: " + err.Error())
	}

	// 8b. Copy agent-input files (if configured).
	if h.AgentInput != "" {
		inputStart := time.Now()
		printer.StepStart("Copying agent-input files into sandbox")
		remoteInput := fmt.Sprintf("%s/agent-input", sandbox.SandboxWorkspace)
		mkInputCmd := fmt.Sprintf("mkdir -p %s", remoteInput)
		if _, _, _, err := sandbox.Exec(sandboxName, mkInputCmd, 10*time.Second); err != nil {
			return fmt.Errorf("creating agent-input dir in sandbox: %w", err)
		}
		if err := sandbox.Upload(sandboxName, h.AgentInput+"/.", remoteInput+"/"); err != nil {
			printer.StepFail("Failed to copy agent-input files")
			return fmt.Errorf("copying agent-input files: %w", err)
		}
		printer.StepDone(fmt.Sprintf("Agent-input files copied (%.1fs)", time.Since(inputStart).Seconds()))
	}

	// 8c. Make the target repo read-only if the harness opts in.
	// Runs after all repo-directory writes (8a, 8a-2) are complete.
	// Excludes .git/ so git operations (index.lock, etc.) still work.
	if h.ReadonlyRepo {
		// -prune skips .git traversal entirely; -not -type l prevents symlink traversal.
		// chown ensures the sandbox user (run_as_user) owns .git/ after root-owned extraction.
		chmodCmd := fmt.Sprintf(
			"find %s -path '*/.git' -prune -o -not -type l -exec chmod a-w {} + && chown -R sandbox:sandbox %s/.git && chmod -R u+w %s/.git",
			remoteRepositoryDir, remoteRepositoryDir, remoteRepositoryDir,
		)
		if _, stderr, exitCode, err := sandbox.Exec(sandboxName, chmodCmd, 30*time.Second); err != nil {
			printer.StepFail("Could not make repo read-only: " + err.Error())
			return fmt.Errorf("Read-only repo enforcement failed: %w", err)
		} else if exitCode != 0 {
			printer.StepFail("Could not make repo read-only (exit " + fmt.Sprintf("%d", exitCode) + "): " + stderr)
			return fmt.Errorf("read-only repo enforcement failed: exit code %d", exitCode)
		}
		printer.StepDone("Target repo set to read-only")
	}

	// 8d. Host-side scan (Path A): scan the target repo's context files
	// (CLAUDE.md, AGENTS.md, SKILL.md, etc.) before the agent processes them.
	// The target branch may contain attacker-controlled files from a PR.
	if h.SecurityEnabled() {
		printer.StepStart("Scanning target repo context files")
		findings := scanRepoContextFiles(hostRepositoryDir)
		if security.HasCriticalFindings(findings) {
			if h.FailModeClosed() {
				printer.StepFail("BLOCKED: critical injection findings in target repo context files")
				return fmt.Errorf("target repo context scan blocked: critical injection findings")
			}
			printer.StepWarn("Target repo has critical injection findings (fail_mode: open)")
		} else if len(findings) > 0 {
			printer.StepWarn(fmt.Sprintf("Target repo context scan: %d finding(s)", len(findings)))
		} else {
			printer.StepDone("Target repo context files clean")
		}
	}

	// 9a. Display trace ID (generated earlier for fetch service audit logging).
	printer.KeyValue("Trace ID", securityTraceID)
	if err := injectTraceID(sandboxName, securityTraceID); err != nil {
		printer.StepWarn("Could not inject trace ID into sandbox: " + err.Error())
	}

	// 9b. Pre-agent security scan (sandbox-internal, Path B).
	// Scans context files (CLAUDE.md, AGENTS.md, .cursorrules, agent defs,
	// SKILL.md) that were just copied into the sandbox.
	if h.SecurityEnabled() {
		printer.StepStart("Running pre-agent security scan")
		scanCmd := buildScanContextCommand(remoteRepositoryDir, securityTraceID)
		stdout, stderr, exitCode, execErr := sandbox.Exec(sandboxName, scanCmd, 60*time.Second)
		if execErr != nil {
			printer.StepFail("Security scan failed: " + execErr.Error())
			if h.FailModeClosed() {
				return fmt.Errorf("pre-agent security scan failed: %w", execErr)
			}
			printer.StepWarn("Continuing despite scan failure (fail_mode: open)")
		} else if exitCode != 0 {
			printer.StepWarn("Security scan findings:\n" + stdout)
			if stderr != "" {
				printer.StepWarn("Scan stderr: " + stderr)
			}
			if h.FailModeClosed() {
				printer.StepFail("BLOCKED: pre-agent scan detected critical findings")
				return fmt.Errorf("pre-agent security scan blocked: critical findings detected")
			}
			printer.StepWarn("Continuing despite findings (fail_mode: open)")
		} else {
			printer.StepDone("Pre-agent scan passed")
		}
	}

	// 9b-2. Pre-flight GitHub API connectivity check.
	// Validates that the sandbox can reach api.github.com through the proxy
	// before starting the agent. Without this, agents that depend on gh CLI
	// burn their entire timeout on doomed API calls. See #2143.
	{
		preflightStart := time.Now()
		printer.StepStart("Checking GitHub API connectivity from sandbox")
		result, connectErr := checkSandboxGitHubConnectivity(sandboxName)
		if connectErr != nil {
			printer.StepFail("GitHub API unreachable from sandbox")
			return fmt.Errorf("pre-flight connectivity check: %w", connectErr)
		}
		if result.Skipped {
			printer.StepInfo("GitHub API check skipped: " + result.SkipReason)
		} else {
			printer.StepDone(fmt.Sprintf("GitHub API reachable from sandbox (%.1fs)", time.Since(preflightStart).Seconds()))
		}
	}

	// 9c. Run agent with validation loop.
	agentBaseName := agentName
	var pluginDirs []string
	for _, p := range h.Plugins {
		pluginDirs = append(pluginDirs, fmt.Sprintf("%s/plugins/%s", rt.ConfigDir(), filepath.Base(p)))
	}

	timeout := time.Duration(h.TimeoutMinutes) * time.Minute
	if timeout == 0 {
		timeout = 30 * time.Minute
	}

	maxIterations := 1
	if h.ValidationLoop != nil && h.ValidationLoop.MaxIterations > 0 {
		maxIterations = h.ValidationLoop.MaxIterations
	}

	// Dual-phase validation design (#5393):
	//
	// Phase 1 — inline validation (step 9e): runs after each iteration.
	// If the iteration passes, we break immediately without waiting for
	// remaining iterations. This is an early-exit optimization that
	// avoids burning agent compute when a good result is already in hand.
	//
	// Phase 2 — post-loop sweep (postLoopValidationSweep): runs only when
	// no iteration passed inline. This is the #5393 fix: it catches the
	// case where an iteration produced valid output but its inline
	// validation was skipped because SafeDownload failed on that same
	// iteration (extraction failure triggers `continue`, bypassing 9e).
	// The sweep re-validates all completed iteration directories, latest
	// first, using only the output files (TARGET_REPO_DIR is empty because
	// hostRepositoryDownloadDir may not correspond to the validated
	// iteration).
	//
	// Both phases are necessary: removing inline validation would force
	// every run to exhaust all maxIterations even when iteration 1 passes,
	// while removing the sweep would regress #5393.

	oidcCtx, oidcCancel := context.WithCancel(context.Background())
	var oidcWg sync.WaitGroup
	if oidcURL := os.Getenv("FULLSEND_GCP_OIDC_URL"); oidcURL != "" {
		oidcAuth, err := readOIDCAuthFile(os.Getenv("FULLSEND_GCP_OIDC_AUTH_FILE"))
		if err != nil {
			printer.StepWarn("OIDC token refresh disabled: " + err.Error())
		} else {
			// GHA OIDC tokens expire after 5 min; sandbox setup can exceed that.
			if err := refreshOIDCToken(oidcCtx, sandboxName, oidcURL, oidcAuth); err != nil {
				printer.StepWarn("Initial OIDC refresh failed (will retry): " + err.Error())
			} else {
				printer.StepDone("OIDC token refreshed, background refresh enabled (WIF mode)")
			}
			oidcWg.Add(1)
			go func() {
				defer oidcWg.Done()
				runOIDCRefresh(oidcCtx, sandboxName, oidcURL, oidcAuth, printer)
			}()
		}
	}
	defer func() {
		oidcCancel()
		oidcWg.Wait()
	}()

	for iteration := 1; iteration <= maxIterations; iteration++ {
		runCount = iteration
		transcriptErrorOverride = false

		// Each iteration gets its own subdirectory for output and transcripts.
		iterDir := filepath.Join(runDir, fmt.Sprintf("iteration-%d", iteration))
		iterOutputDir := filepath.Join(iterDir, "output")
		iterTranscriptDir := filepath.Join(iterDir, "transcripts")
		if err := os.MkdirAll(iterDir, 0o755); err != nil {
			return fmt.Errorf("creating iteration directory: %w", err)
		}

		if maxIterations > 1 {
			printer.Blank()
			printer.Header(fmt.Sprintf("Iteration %d of %d", iteration, maxIterations))
		}

		// Clear sandbox-side output and transcripts so the next iteration starts fresh.
		if iteration > 1 {
			if clearErr := rt.ClearIterationArtifacts(sandboxName); clearErr != nil {
				printer.StepWarn("Failed to clear sandbox output: " + clearErr.Error())
			}
		}

		// 9a. Run agent.
		printer.StepStart("Running agent")
		printer.Blank()

		agentStart := time.Now()
		heartbeatDone := make(chan struct{})
		go runHeartbeat(printer, agentStart, timeout, heartbeatDone)

		agentCtx, agentSpan := tracer.Start(ctx, "agent", trace.WithAttributes(agentSpanStartAttrs(iteration, agentName)...))
		var metrics agentruntime.RunMetrics
		exitCode, runErr := rt.Run(agentCtx, agentruntime.RunParams{
			SandboxName:   sandboxName,
			AgentBaseName: agentBaseName,
			Model:         h.Model,
			RepoDir:       remoteRepositoryDir,
			FullsendDir:   absFullsendDir,
			PluginDirs:    pluginDirs,
			Debug:         debug,
			Timeout:       timeout,
			OutputPath:    filepath.Join(iterDir, "output.jsonl"),
		}, printer, agentStart, &metrics)
		close(heartbeatDone)

		agentSpan.SetAttributes(agentSpanEndAttrs(iteration, exitCode, rt.System(), &metrics)...)
		if runErr != nil {
			agentSpan.SetStatus(codes.Error, runErr.Error())
		} else {
			agentSpan.SetStatus(codes.Ok, "")
		}
		agentSpan.End()

		// Accumulate behavioral metrics across iterations.
		aggregateRunMetrics(&aggMetrics, &metrics, iteration)

		if runErr != nil {
			printer.StepFail("Agent execution failed")
			// Record the real exit code (rt.Run returns -1 when the agent never
			// started) so the telemetry summary reports the failure faithfully
			// instead of collapsing every infra failure to a generic 1.
			lastExitCode = exitCode
			// Write partial metrics before returning so downstream judges
			// (e.g., max_turns, max_cost) can inspect what happened.
			if err := writeMetricsJSON(runDir, aggMetrics); err != nil {
				printer.StepWarn("Failed to write metrics.json: " + err.Error())
			}
			return fmt.Errorf("running agent (iteration %d): %w", iteration, runErr)
		}
		lastExitCode = exitCode

		// Check the tee'd output.jsonl for is_error:true result events.
		// Claude Code may exit 0 on API/infrastructure failures (e.g.,
		// invalid_grant, quota exhaustion) while setting is_error:true in
		// the transcript. Treat these as failures so downstream gating
		// (transcript surfacing, post-script skip) can act. See #2786.
		if exitCode == 0 {
			outputJSONL := filepath.Join(iterDir, "output.jsonl")
			if te, ok := tx.ParseTranscriptFile(outputJSONL); ok && te.IsError {
				printer.StepWarn(fmt.Sprintf("Agent exited with code 0 but transcript contains error: %s", te.ErrorMessage))
				lastExitCode = 1
				transcriptErrorOverride = true
			}
		}

		printer.Blank()
		// Non-zero exit is a warning, not a failure — the validation loop is the success gate.
		if lastExitCode == 0 {
			printer.StepDone(fmt.Sprintf("Agent exited with code %d (%.1fs)", exitCode, time.Since(agentStart).Seconds()))
		} else {
			printer.StepWarn(fmt.Sprintf("Agent exited with code %d", lastExitCode))
		}

		// 9b. Extract output files.
		extractStart := time.Now()
		printer.StepStart("Extracting output files")
		remoteSrc := fmt.Sprintf("%s/output", sandbox.SandboxWorkspace)
		extracted, extractErr := sandbox.ExtractOutputFiles(sandboxName, remoteSrc, iterOutputDir)
		if extractErr != nil {
			printer.StepWarn("Failed to extract output files: " + extractErr.Error())
		} else if len(extracted) == 0 {
			printer.StepInfo("No output files found")
		} else {
			for _, f := range extracted {
				printer.StepInfo(f)
			}
			printer.StepDone(fmt.Sprintf("Extracted %d output file(s) (%.1fs)", len(extracted), time.Since(extractStart).Seconds()))
		}

		// 9c. Extract transcripts for this iteration.
		transcriptStart := time.Now()
		printer.StepStart("Extracting transcripts")
		if err := tx.ExtractTranscripts(sandboxName, agentName, iterTranscriptDir); err != nil {
			printer.StepWarn("Failed to extract transcripts: " + err.Error())
		} else {
			printer.StepDone(fmt.Sprintf("Transcripts extracted (%.1fs)", time.Since(transcriptStart).Seconds()))
		}

		// Extract debug log if --debug was enabled.
		if debug != "" {
			debugDst := filepath.Join(iterDir, "claude-debug.log")
			if err := tx.ExtractDebugLog(sandboxName, debugDst, debug); err != nil {
				printer.StepWarn("Failed to extract debug log: " + err.Error())
			} else {
				printer.StepInfo("Extracted claude-debug.log")
			}
		}

		// 9d. Extract target repo back to host. SafeDownload removes dangerous
		// symlinks (absolute or repo-escaping) and .git/hooks/ to prevent sandbox escape.
		//
		// SafeDownload is a security boundary: it combines Download with
		// sanitizeDownload, which strips dangerous symlinks. If either step
		// fails, the on-disk content may contain unsanitized symlinks that
		// could reach the post-script. SafeDownload failures are therefore
		// always treated as fatal for the current iteration's repo state —
		// we clean up the directory and skip to the next iteration.
		//
		// The forceRemoveAll pre-clear guards against a stale destination:
		// whether "openshell sandbox download" fully replaces a non-empty
		// destination directory or merges onto it is not verified anywhere
		// in this codebase (it's an external binary). Rather than assume
		// replace semantics, a failed pre-clear is treated the same as a
		// SafeDownload failure with a validation loop: skip this
		// iteration's repo state instead of extracting into a directory of
		// unknown provenance.
		if clearErr := forceRemoveAll(hostRepositoryDownloadDir); clearErr != nil {
			if h.ValidationLoop != nil {
				printer.StepWarn(fmt.Sprintf("Failed to clear local repo %s (skipping repo extraction this iteration): %v", hostRepositoryDownloadDir, clearErr))
				repoExtractedOK = false
				continue
			}
			return fmt.Errorf("clearing local repo %s before extraction: %w", hostRepositoryDownloadDir, clearErr)
		}

		repoExtractStart := time.Now()
		printer.StepStart("Extracting target repo")
		if err := sandbox.SafeDownload(sandboxName, remoteRepositoryDir, hostRepositoryDownloadDir); err != nil {
			if es := tx.ParseTranscriptErrors(iterTranscriptDir); len(es) > 0 {
				tx.EmitTranscriptErrors(os.Stderr, es)
			}
			if h.ValidationLoop != nil {
				// SafeDownload failed — the repo directory may contain
				// unsanitized content (sanitizeDownload aborts on first
				// error, leaving subsequent dangerous symlinks intact).
				// Clean up to prevent unsanitized content from reaching
				// validation or the post-script, then continue the retry
				// loop. Output files (extracted in 9b) are unaffected.
				printer.StepWarn(fmt.Sprintf("Failed to extract target repo (cleaning up): %v", err))
				if rmErr := forceRemoveAll(hostRepositoryDownloadDir); rmErr != nil {
					printer.StepWarn(fmt.Sprintf("Failed to clean up repo dir after extraction failure: %v", rmErr))
				}
				repoExtractedOK = false
				continue
			}
			return fmt.Errorf("extracting target repo (iteration %d): %w", iteration, err)
		}
		repoExtractedOK = true
		printer.StepDone(fmt.Sprintf("Target repo extracted to %s (%.1fs)", hostRepositoryDownloadDir, time.Since(repoExtractStart).Seconds()))

		// 9e. Run validation.
		if h.ValidationLoop == nil {
			break
		}

		valStart := time.Now()
		printer.StepStart("Running validation: " + h.ValidationLoop.Script)
		valCmd := exec.Command(h.ValidationLoop.Script)
		valCmd.Dir = iterDir
		// At this point repoExtractedOK is always true: SafeDownload
		// failure sets it to false and continues (skipping step 9e),
		// while success sets it to true immediately above. Pass the
		// repo dir directly.
		valCmd.Env = append(os.Environ(), validationEnv(h, hostRepositoryDownloadDir, runDir)...)
		valOut, valErr := valCmd.CombinedOutput()

		if valErr == nil {
			printer.StepDone(fmt.Sprintf("Validation passed: %s (%.1fs)", strings.TrimSpace(string(valOut)), time.Since(valStart).Seconds()))
			validationPassed = true
			validatedIterNum = iteration
			break
		}

		printer.StepFail("Validation failed: " + validationFailMessage(valOut, valErr))
		if iteration < maxIterations {
			printer.StepInfo(fmt.Sprintf("Will retry (%d iterations remaining)", maxIterations-iteration))
		}
	}

	// Post-loop validation sweep: if no iteration passed validation
	// inline (e.g., because extraction failed on the iteration that
	// produced valid output), check all completed iterations starting
	// from the latest. This ensures a successful retry's output is
	// found even when earlier steps in that iteration failed. See #5393.
	if h.ValidationLoop != nil && !validationPassed {
		sweep := postLoopValidationSweep(h, runDir, runCount, repoExtractedOK, printer)
		validationPassed = sweep.passed
		repoExtractedOK = sweep.repoExtractedOK
		validatedIterNum = sweep.validatedIter
	}

	// Write aggregated behavioral metrics.
	if err := writeMetricsJSON(runDir, aggMetrics); err != nil {
		printer.StepWarn("Failed to write metrics.json: " + err.Error())
	}

	// 9e-bis. Surface transcript errors in workflow logs (GitHub Actions).
	// Parse transcript JSONL files and emit ::error:: annotations so operators
	// can diagnose failures without downloading artifacts. This runs
	// regardless of exit code because Claude Code may exit 0 with
	// is_error:true on API/infrastructure failures. See #704, #2786.
	lastIterDir := filepath.Join(runDir, fmt.Sprintf("iteration-%d", runCount))
	lastTranscriptDir := filepath.Join(lastIterDir, "transcripts")
	if errorSummaries := tx.ParseTranscriptErrors(lastTranscriptDir); len(errorSummaries) > 0 {
		printer.StepWarn(fmt.Sprintf("Found %d transcript error(s) — emitting to workflow log", len(errorSummaries)))
		tx.EmitTranscriptErrors(os.Stderr, errorSummaries)
	}

	// 9f. Post-agent output scan — redact secrets from extracted output.
	if h.SecurityEnabled() {
		printer.StepStart("Running post-agent output scan")
		if err := scanOutputFiles(runDir, securityTraceID, printer); err != nil {
			printer.StepWarn("Output scan error: " + err.Error())
		}

		// Extract sandbox-side security findings for audit trail.
		findingsDir := filepath.Join(runDir, "security")
		if err := os.MkdirAll(findingsDir, 0o755); err == nil {
			remoteFindingsDir := sandbox.SandboxWorkspace + "/.security/"
			if dlErr := sandbox.Download(sandboxName, remoteFindingsDir, findingsDir); dlErr != nil {
				printer.StepInfo("No sandbox security findings to extract")
			} else {
				printer.StepDone("Security findings extracted")
			}
		}

		findingsJSONL := filepath.Join(runDir, "security", "findings.jsonl")
		if _, statErr := os.Stat(findingsJSONL); statErr == nil {
			cv, verifyErr := security.VerifyChain(findingsJSONL)
			if verifyErr != nil {
				printer.StepWarn("Audit log verification error: " + verifyErr.Error())
			} else if !cv.Valid {
				printer.StepFail(fmt.Sprintf("Audit log integrity check FAILED: %s", cv.BrokenMsg))
				return fmt.Errorf("audit log integrity check failed: %s", cv.BrokenMsg)
			} else if cv.Entries > 0 {
				printer.StepDone(fmt.Sprintf("Audit log integrity verified (%d entries)", cv.Entries))
			}
		}
	}

	// 10. Print results.
	printer.Blank()
	printer.Header("Results")
	printer.KeyValue("Run directory", runDir)
	if keepSandbox {
		printer.KeyValue("Download directory", hostRepositoryDownloadDir)
	} else {
		printer.KeyValue("Download directory", hostRepositoryDownloadDir+" (removed after run; use --keep-sandbox to retain)")
	}
	printer.KeyValue("Agent exit code", fmt.Sprintf("%d", lastExitCode))
	printer.KeyValue("Agent runs", fmt.Sprintf("%d", runCount))
	printer.KeyValue("Trace ID", securityTraceID)
	if h.ValidationLoop != nil {
		if validationPassed {
			printer.KeyValue("Validation", "passed")
		} else {
			printer.KeyValue("Validation", "failed")
		}
	}
	printer.Blank()

	if h.ValidationLoop != nil && !validationPassed {
		return fmt.Errorf("validation failed after %d iteration(s)", runCount)
	}

	return nil
}

func bootstrapCommon(sandboxName, fullsendBinary string, h *harness.Harness) error {
	// Runner-level dirs only; Claude hook scripts live under workspace/.claude/
	// and are created in installClaudeHooks when ClaudeHooksBootstrap is present.
	mkdirCmd := fmt.Sprintf("mkdir -p %s/bin %s/.env.d %s/.security",
		sandbox.SandboxWorkspace, sandbox.SandboxWorkspace, sandbox.SandboxWorkspace)
	if _, _, _, err := sandbox.Exec(sandboxName, mkdirCmd, 10*time.Second); err != nil {
		return fmt.Errorf("creating workspace dirs: %w", err)
	}

	// Copy fullsend binary into sandbox so `fullsend scan context` works.
	// The pre-agent security scan runs inside the sandbox and needs the
	// fullsend CLI to scan context files.
	localBinary := fullsendBinary
	var tmpBinaryDir string
	if localBinary == "" {
		if needsCrossCompilation() {
			targetArch := sandboxArch()
			result, err := binary.ResolveForRun(version, targetArch)
			if err != nil {
				if h.FailModeClosed() {
					return fmt.Errorf("could not obtain linux/%s binary for security scan (fail_mode: closed): %w\nUse --fullsend-binary to provide a pre-built Linux binary", targetArch, err)
				}
				fmt.Fprintf(os.Stderr, "WARNING: could not obtain linux/%s binary: %v\n", targetArch, err)
				fmt.Fprintf(os.Stderr, "WARNING: skipping sandbox-side security scan (fail_mode: open). Use --fullsend-binary to provide a pre-built Linux binary.\n")
				localBinary = ""
			} else {
				tmpBinaryDir = result.TmpDir
				localBinary = result.Path
			}
		} else {
			var err error
			localBinary, err = os.Executable()
			if err != nil {
				return fmt.Errorf("finding fullsend executable: %w", err)
			}
		}
	}
	if tmpBinaryDir != "" {
		defer os.RemoveAll(tmpBinaryDir)
	}
	if localBinary != "" {
		if err := binary.ValidateLinuxBinary(localBinary, sandboxArch()); err != nil {
			return fmt.Errorf("fullsend binary %q is not valid for the sandbox: %w\nSet FULLSEND_SANDBOX_ARCH to override the target architecture", localBinary, err)
		}
		// Use UploadDir (tarball-based) instead of Upload for the binary.
		// Upload silently fails for large files (~16MB); the tarball
		// approach compresses and extracts reliably inside the sandbox.
		remoteBinDir := fmt.Sprintf("%s/bin", sandbox.SandboxWorkspace)
		remoteBinary := fmt.Sprintf("%s/fullsend", remoteBinDir)
		tmpDir, err := os.MkdirTemp("", "fullsend-bin-upload-*")
		if err != nil {
			return fmt.Errorf("creating temp dir for binary upload: %w", err)
		}
		defer os.RemoveAll(tmpDir)
		if err := copyFile(localBinary, filepath.Join(tmpDir, "fullsend")); err != nil {
			return fmt.Errorf("staging fullsend binary: %w", err)
		}
		if err := sandbox.UploadDir(sandboxName, tmpDir, remoteBinDir); err != nil {
			return fmt.Errorf("copying fullsend binary to sandbox: %w", err)
		}
		chmodCmd := fmt.Sprintf("chmod +x %s", remoteBinary)
		if _, _, _, err := sandbox.Exec(sandboxName, chmodCmd, 10*time.Second); err != nil {
			return fmt.Errorf("chmod fullsend binary: %w", err)
		}
	}

	// Copy the self-check script into the sandbox so agents can validate
	// output JSON against their schema before finishing. See #1107.
	checkScript, err := scaffold.FullsendRepoFile("scripts/fullsend-check-output")
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: could not load self-check script: %v\n", err)
	} else if err := func() error {
		tmpCheck, err := os.CreateTemp("", "fullsend-check-output-*")
		if err != nil {
			return fmt.Errorf("creating temp file: %w", err)
		}
		defer os.Remove(tmpCheck.Name())
		if _, err := tmpCheck.Write(checkScript); err != nil {
			tmpCheck.Close()
			return fmt.Errorf("writing temp file: %w", err)
		}
		tmpCheck.Close()
		// Safe: remoteBin is built from the SandboxWorkspace constant.
		remoteBin := fmt.Sprintf("%s/bin/fullsend-check-output", sandbox.SandboxWorkspace)
		if err := sandbox.UploadFile(sandboxName, tmpCheck.Name(), remoteBin); err != nil {
			return fmt.Errorf("uploading to sandbox: %w", err)
		}
		if _, _, _, err := sandbox.Exec(sandboxName, fmt.Sprintf("chmod +x %s", remoteBin), 10*time.Second); err != nil {
			return fmt.Errorf("chmod: %w", err)
		}
		return nil
	}(); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: could not install self-check script: %v\n", err)
	}

	return nil
}

// bootstrapEnv writes environment variables to a .env file in the sandbox and
// copies host files.
//
// The .env file contains infrastructure vars (PATH, CLAUDE_CONFIG_DIR) and
// sources all env files from .env.d/. Application-specific env vars (e.g.
// Vertex AI credentials) are delivered as expanded env files via host_files
// with expand: true.
//
// host_files entries copy files from the host into the sandbox at specified
// destination paths. Src values may contain ${VAR} references expanded from
// the host environment. When expand is true, file content is also expanded.
// fetchServiceEnv holds the address and token of the runtime fetch service
// started by the runner. When non-empty, bootstrapEnv injects them as
// environment variables so the in-sandbox fullsend fetch-skill subcommand
// can reach the runner.
type fetchServiceEnv struct {
	addr  string // host:port
	token string // bearer token
}

const deprecatedImplicitFetchWarning = "Harness declares allowed_remote_resources without allow_runtime_fetch: true; " +
	"the runtime fetch service will start for backward compatibility, but this behavior is " +
	"deprecated — add allow_runtime_fetch: true to the harness to silence this warning"

// shouldStartFetchService decides whether the runtime fetch HTTP service
// should be started, and returns a deprecation warning if the harness relies
// on the legacy implicit opt-in via allowed_remote_resources.
func shouldStartFetchService(h *harness.Harness) (start bool, deprecationWarning string) {
	if h.HasURLSkills() || h.AllowRuntimeFetch {
		return true, ""
	}
	if len(h.AllowedRemoteResources) > 0 {
		return true, deprecatedImplicitFetchWarning
	}
	return false, ""
}

// setupFetchService resolves a git token for runtime fetching and starts
// the HTTP fetch service. It returns the service address/token as a
// fetchServiceEnv, a shutdown function, and any error.
func setupFetchService(ctx context.Context, treeFetcher gitfetch.TreeFetchFunc, gitToken string, h *harness.Harness, resolveToken func() (string, error), cfg fetchsvc.ServiceConfig, warn func(string)) (fetchServiceEnv, func(), error) {
	cfg.TreeFetcher = treeFetcher
	if gitToken != "" {
		cfg.GitToken = gitToken
	} else if h.HasURLSkills() || h.AllowRuntimeFetch || len(h.AllowedRemoteResources) > 0 {
		if token, err := resolveToken(); err == nil {
			cfg.GitToken = token
		} else {
			warn(fmt.Sprintf("Git token unavailable, runtime fetches for uncached skills will fail: %v", err))
		}
	}

	addr, token, shutdown, err := startFetchService(ctx, cfg)
	if err != nil {
		return fetchServiceEnv{}, nil, err
	}
	return fetchServiceEnv{addr: addr, token: token}, shutdown, nil
}

// validEnvKeyRe matches POSIX-portable environment variable names.
// Keys that don't match are skipped to prevent shell injection.
var validEnvKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedSandboxKeys are infrastructure env vars that env.sandbox must not
// shadow. These are set by the runner in bootstrapEnv and overriding them
// from harness YAML could break sandbox operation, or are security-sensitive
// vars that could influence sandbox execution (e.g. shared library injection,
// auto-sourced shell startup files).
// NOTE: keep in sync with bootstrapEnv exports below for FULLSEND_* keys.
var reservedSandboxKeys = map[string]bool{
	"PATH":                     true,
	"HOME":                     true,
	"SHELL":                    true,
	"LD_PRELOAD":               true,
	"LD_LIBRARY_PATH":          true,
	"BASH_ENV":                 true,
	"ENV":                      true,
	"FULLSEND_FETCH_URL":       true,
	"FULLSEND_FETCH_TOKEN":     true,
	"FULLSEND_OUTPUT_DIR":      true,
	"FULLSEND_OUTPUT_SCHEMA":   true,
	"FULLSEND_OUTPUT_FILE":     true,
	"FULLSEND_TARGET_REPO_DIR": true,
}

// buildSandboxEnvLines generates export lines for env.sandbox values (ADR 0055).
// Values have already been expanded by the caller. Each value is single-quoted
// with internal single quotes escaped. Keys that are not valid shell identifiers
// are silently skipped.
func buildSandboxEnvLines(h *harness.Harness) []string {
	if h.Env == nil || len(h.Env.Sandbox) == 0 {
		return nil
	}
	keys := make([]string, 0, len(h.Env.Sandbox))
	for k := range h.Env.Sandbox {
		if !validEnvKeyRe.MatchString(k) {
			fmt.Fprintf(os.Stderr, "WARNING: env.sandbox key %q is not a valid POSIX identifier; skipping\n", k)
			continue
		}
		if reservedSandboxKeys[k] {
			fmt.Fprintf(os.Stderr, "WARNING: env.sandbox key %q is reserved for runner infrastructure; skipping\n", k)
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)

	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		v := h.Env.Sandbox[k]
		escaped := strings.ReplaceAll(v, "'", "'\\''")
		lines = append(lines, fmt.Sprintf("export %s='%s'", k, escaped))
	}
	return lines
}

func bootstrapEnv(sandboxName, remoteRepositoryDir string, h *harness.Harness, runtimeEnvExports []string, fetchEnv ...fetchServiceEnv) error {
	remoteEnvFile := sandbox.SandboxWorkspace + "/.env"
	outputDir := sandbox.SandboxWorkspace + "/output"

	var lines []string

	// Infrastructure vars.
	pathExport := fmt.Sprintf("export PATH=%s/bin", sandbox.SandboxWorkspace)
	pathExport += ":/usr/local/go/bin"
	pathExport += ":$HOME/go/bin"
	pathExport += ":$PATH"

	lines = append(lines, pathExport)
	lines = append(lines, runtimeEnvExports...)
	lines = append(lines, fmt.Sprintf("export FULLSEND_OUTPUT_DIR=%s", outputDir))
	lines = append(lines, fmt.Sprintf("export FULLSEND_TARGET_REPO_DIR=%s", remoteRepositoryDir))

	// Expose output schema and expected filename inside the sandbox so
	// agents can self-check output with fullsend-check-output. See #1107.
	// Prefer validation_loop.schema (already resolved by compose); fall
	// back to the legacy RunnerEnv path for backward compatibility.
	remoteSchemaPath := sandbox.SandboxWorkspace + "/.fullsend/output-schema.json"
	var schemaHost string
	if h.ValidationLoop != nil && h.ValidationLoop.Schema != "" {
		schemaHost = h.ValidationLoop.Schema
	} else if v, ok := h.RunnerEnv["FULLSEND_OUTPUT_SCHEMA"]; ok && v != "" {
		schemaHost = v
	}
	if schemaHost != "" {
		if _, statErr := os.Stat(schemaHost); statErr != nil {
			fmt.Fprintf(os.Stderr, "WARNING: schema file not found on host: %s\n", schemaHost)
		} else {
			mkdirCmd := fmt.Sprintf("mkdir -p %s/.fullsend", sandbox.SandboxWorkspace)
			if _, _, _, execErr := sandbox.Exec(sandboxName, mkdirCmd, 10*time.Second); execErr != nil {
				fmt.Fprintf(os.Stderr, "WARNING: could not create .fullsend dir for schema: %v\n", execErr)
			} else if uploadErr := sandbox.UploadFile(sandboxName, schemaHost, remoteSchemaPath); uploadErr != nil {
				fmt.Fprintf(os.Stderr, "WARNING: could not upload output schema: %v\n", uploadErr)
			} else {
				// Safe: remoteSchemaPath is built from the SandboxWorkspace constant.
				lines = append(lines, fmt.Sprintf("export FULLSEND_OUTPUT_SCHEMA=%s", remoteSchemaPath))
			}
		}
	}
	if outputFile, ok := h.RunnerEnv["FULLSEND_OUTPUT_FILE"]; ok && outputFile != "" {
		lines = append(lines, fmt.Sprintf("export FULLSEND_OUTPUT_FILE='%s'", strings.ReplaceAll(outputFile, "'", "'\\''")))
	}

	// Runtime fetch service env vars (Phase 4, ADR-0038).
	if len(fetchEnv) > 0 && fetchEnv[0].addr != "" {
		escAddr := strings.ReplaceAll(fetchEnv[0].addr, "'", "'\\''")
		escToken := strings.ReplaceAll(fetchEnv[0].token, "'", "'\\''")
		lines = append(lines, fmt.Sprintf("export FULLSEND_FETCH_URL='http://%s/fetch'", escAddr))
		lines = append(lines, fmt.Sprintf("export FULLSEND_FETCH_TOKEN='%s'", escToken))
	}

	// Source all env files from .env.d/ (populated by host_files with expand: true).
	lines = append(lines, fmt.Sprintf("for f in %s/.env.d/*.env; do [ -f \"$f\" ] && . \"$f\"; done", sandbox.SandboxWorkspace))

	// ADR 0055: export env.sandbox vars. Placed after .env.d sourcing so
	// env.sandbox takes precedence on collision — the common use case is
	// overriding a single var from a shared host_files .env file.
	lines = append(lines, buildSandboxEnvLines(h)...)

	content := strings.Join(lines, "\n") + "\n"

	tmpFile, err := os.CreateTemp("", "fullsend-env-*.sh")
	if err != nil {
		return fmt.Errorf("creating temp env file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(content); err != nil {
		tmpFile.Close()
		return fmt.Errorf("writing temp env file: %w", err)
	}
	tmpFile.Close()

	if err := sandbox.UploadFile(sandboxName, tmpFile.Name(), remoteEnvFile); err != nil {
		return fmt.Errorf("copying .env file to sandbox: %w", err)
	}

	// Copy host files into the sandbox.
	for _, hf := range h.HostFiles {
		hostPath := os.ExpandEnv(hf.Src)
		if hostPath == "" {
			if hf.Optional {
				continue
			}
			return fmt.Errorf("host_files: src %q expanded to empty string", hf.Src)
		}
		if hf.Optional {
			if _, err := os.Stat(hostPath); err != nil {
				continue
			}
		}

		if hf.Expand {
			// Read file, expand ${VAR} in content, write expanded version.
			// Uses shell-safe quoting so user-authored values (e.g.
			// HUMAN_INSTRUCTION) containing shell metacharacters do not
			// cause syntax errors when the file is sourced. (#408, #615)
			raw, err := os.ReadFile(hostPath)
			if err != nil {
				return fmt.Errorf("reading host file %s for expansion: %w", hf.Src, err)
			}
			expanded := shellSafeExpandEnv(string(raw))

			tmp, err := os.CreateTemp("", "fullsend-expand-*")
			if err != nil {
				return fmt.Errorf("creating temp file for expanded %s: %w", hf.Src, err)
			}
			if _, err := tmp.WriteString(expanded); err != nil {
				tmp.Close()
				os.Remove(tmp.Name())
				return fmt.Errorf("writing expanded %s: %w", hf.Src, err)
			}
			tmp.Close()

			if err := sandbox.UploadFile(sandboxName, tmp.Name(), hf.Dest); err != nil {
				os.Remove(tmp.Name())
				return fmt.Errorf("copying expanded file %s to %s: %w", hf.Src, hf.Dest, err)
			}
			os.Remove(tmp.Name())
		} else {
			if err := sandbox.UploadFile(sandboxName, hostPath, hf.Dest); err != nil {
				return fmt.Errorf("copying host file %s to %s: %w", hf.Src, hf.Dest, err)
			}
		}

		// TODO(#345): remove this once admin install preserves the executable
		// bit when writing files to .fullsend/. The GitHub Contents API commits
		// everything as 100644, so scripts lose +x. Force it back for anything
		// landing in a bin/ directory.
		// https://github.com/fullsend-ai/fullsend/issues/345#issuecomment-4300740512
		if strings.Contains(hf.Dest, "/bin/") {
			chmodCmd := fmt.Sprintf("chmod +x %s", hf.Dest)
			if _, _, _, execErr := sandbox.Exec(sandboxName, chmodCmd, 10*time.Second); execErr != nil {
				return fmt.Errorf("chmod host file %s in sandbox: %w", hf.Dest, execErr)
			}
		}
	}

	return nil
}

// shellSafeExpandEnv expands ${VAR} references in text using the host
// environment, escaping characters that are special inside double quotes
// (", $, `, \) so the result is safe to source as a shell script.
// Templates use the standard export FOO="${FOO}" pattern; this function
// ensures substituted values cannot break out of the double-quote context.
// Fixes #408, #615.
func shellSafeExpandEnv(text string) string {
	return os.Expand(text, func(key string) string {
		return escapeForDoubleQuotes(os.Getenv(key))
	})
}

// escapeForDoubleQuotes escapes the four characters that have special
// meaning inside double-quoted shell strings: backslash, double quote,
// dollar sign, and backtick. Order matters: backslash must be escaped
// first to avoid double-escaping the others.
func escapeForDoubleQuotes(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, `$`, `\$`)
	s = strings.ReplaceAll(s, "`", "\\`")
	return s
}

// validationFailMessage returns a human-readable message for a validation
// script failure. When the script produces output, that output is used;
// otherwise it falls back to the exec error string (e.g. ENOENT / EACCES).
func validationFailMessage(output []byte, execErr error) string {
	if msg := strings.TrimSpace(string(output)); msg != "" {
		return msg
	}
	return execErr.Error()
}

// postScriptRepoEnv computes the REPO_DIR and FULLSEND_VALIDATED_ITERATION_DIR
// values for the post-script's environment. Extracted from the post-script
// defer closure for testability.
//
// repoDir is hostRepositoryDownloadDir when repoExtractedOK is true, empty
// otherwise — see the call site's doc comment for why repoExtractedOK can be
// false. validatedIterDir points at the validated iteration's output
// directory when a validation loop is configured and an iteration passed;
// it is empty when there's no validation loop (the post-script's own
// last-iteration scan is used instead) or when no iteration passed (the
// post-script is skipped entirely in that case, so this is defensive).
func postScriptRepoEnv(h *harness.Harness, runDir, hostRepositoryDownloadDir string, repoExtractedOK bool, validatedIterNum int) (repoDir, validatedIterDir string) {
	if repoExtractedOK {
		repoDir = hostRepositoryDownloadDir
	}
	if h.ValidationLoop != nil && validatedIterNum > 0 {
		validatedIterDir = filepath.Join(runDir, fmt.Sprintf("iteration-%d/output", validatedIterNum))
	}
	return repoDir, validatedIterDir
}

// sweepResult holds the outcome of a post-loop validation sweep.
type sweepResult struct {
	passed          bool // true if any iteration's validation passed
	validatedIter   int  // which iteration passed (0 if none)
	repoExtractedOK bool // false when the validated iteration != runCount
}

// postLoopValidationSweep runs the validation script against each completed
// iteration directory, starting from the latest (runCount) and working
// backwards. It returns the first iteration that passes, or signals that
// none passed. When the passing iteration is not runCount, repoExtractedOK
// is set to false because hostRepositoryDownloadDir holds a different
// iteration's repo checkout — the post-script must not use it.
func postLoopValidationSweep(h *harness.Harness, runDir string, runCount int, currentRepoExtractedOK bool, printer *ui.Printer) sweepResult {
	for i := runCount; i >= 1; i-- {
		iterDir := filepath.Join(runDir, fmt.Sprintf("iteration-%d", i))
		valStart := time.Now()
		printer.StepStart(fmt.Sprintf("Post-loop validation (iteration %d): %s", i, h.ValidationLoop.Script))
		valCmd := exec.Command(h.ValidationLoop.Script)
		valCmd.Dir = iterDir
		valCmd.Env = append(os.Environ(), validationEnv(h, "", runDir)...)
		valOut, valErr := valCmd.CombinedOutput()

		if valErr == nil {
			printer.StepDone(fmt.Sprintf("Validation passed (iteration %d): %s (%.1fs)", i, strings.TrimSpace(string(valOut)), time.Since(valStart).Seconds()))
			repoOK := currentRepoExtractedOK
			if i != runCount {
				repoOK = false
			}
			return sweepResult{passed: true, validatedIter: i, repoExtractedOK: repoOK}
		}
		printer.StepWarn(fmt.Sprintf("Post-loop validation failed (iteration %d): %s", i, validationFailMessage(valOut, valErr)))
	}
	return sweepResult{passed: false, repoExtractedOK: currentRepoExtractedOK}
}

// envToList converts a map of env vars to a sorted list of KEY=VALUE strings.
// toTelemetryMetrics maps fullsend's aggregate run metrics onto the telemetry
// summary metrics — the same numbers already written to metrics.json, no new
// accounting.
// roundUSD rounds a dollar amount to cents for the telemetry output;
// metrics.json keeps full precision.
func roundUSD(c float64) float64 { return math.Round(c*100) / 100 }

// agentSpanStartAttrs builds the span_start attributes for one agent
// iteration: the iteration counter plus the OTEL GenAI semconv identity
// (gen_ai.operation.name, gen_ai.agent.name) named by ADR 0050.
func agentSpanStartAttrs(iteration int, agentName string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.Int("iteration", iteration),
		attribute.String("gen_ai.operation.name", "invoke_agent"),
		attribute.String("gen_ai.agent.name", agentName),
	}
}

func agentSpanEndAttrs(iteration, exitCode int, system string, m *agentruntime.RunMetrics) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.Int("iteration", iteration),
		attribute.Int("exit_code", exitCode),
		attribute.String("gen_ai.system", system),
		attribute.String("gen_ai.request.model", m.Model),
		attribute.Int("gen_ai.usage.input_tokens", m.InputTokens),
		attribute.Int("gen_ai.usage.output_tokens", m.OutputTokens),
		attribute.Int("gen_ai.usage.cache_creation.input_tokens", m.CacheCreationInputTokens),
		attribute.Int("gen_ai.usage.cache_read.input_tokens", m.CacheReadInputTokens),
		attribute.Float64("fullsend.cost_usd", roundUSD(m.TotalCostUSD)),
		attribute.Int("fullsend.tool_calls", int(m.ToolCalls.Load())),
	}
}

// aggregateRunMetrics folds one iteration's metrics into the cross-iteration
// aggregate: tokens/cost/turns/tool calls are summed; the last non-empty model
// wins. iteration records the highest iteration reached.
func aggregateRunMetrics(agg *aggregateMetrics, m *agentruntime.RunMetrics, iteration int) {
	agg.NumTurns += m.NumTurns
	agg.TotalCostUSD += m.TotalCostUSD
	agg.TokenUsage.Input += m.InputTokens
	agg.TokenUsage.Output += m.OutputTokens
	agg.TokenUsage.CacheCreation += m.CacheCreationInputTokens
	agg.TokenUsage.CacheRead += m.CacheReadInputTokens
	agg.ToolCalls += int(m.ToolCalls.Load())
	agg.Iterations = iteration
	if m.Model != "" {
		agg.Model = m.Model
	}
}

// resolveWorkItemID returns a stable cross-run correlation key for the work
// item being processed, sourced from existing run env (ADR 0049 leaves these
// context vars unchanged). The preference order yields a globally-unique,
// human-meaningful key; it falls back to "unknown" so Level 1's zero-config
// promise always holds.
func resolveWorkItemID() string {
	if v := strings.TrimSpace(os.Getenv("ISSUE_KEY")); v != "" {
		return v // source-neutral canonical key, e.g. "owner/repo#123" or "PROJ-123"
	}
	repo := strings.TrimSpace(os.Getenv("REPO_FULL_NAME"))
	num := strings.TrimSpace(os.Getenv("ISSUE_NUMBER"))
	if repo != "" && num != "" {
		return repo + "#" + num
	}
	if v := strings.TrimSpace(os.Getenv("GITHUB_ISSUE_URL")); v != "" {
		return v
	}
	if num != "" {
		return num
	}
	return "unknown"
}

// telemetryExitCode maps the run's final state to the exit code recorded on
// the root span: the agent's last exit code, or 1 when the run failed for a
// non-agent reason (lastExitCode 0 with a non-nil error) so a failure is never
// reported as success.
func telemetryExitCode(lastExitCode int, runErr error) int {
	if runErr != nil && lastExitCode == 0 {
		return 1
	}
	return lastExitCode
}

// traceIdentity holds the resolved trace context for a run.
type traceIdentity struct {
	Ctx         context.Context
	RootSpan    trace.Span
	Traceparent string
	SpanKind    trace.SpanKind
}

// resolveTraceIdentity extracts an inbound W3C traceparent, starts the root
// span, and computes the propagated traceparent with flag preservation.
func resolveTraceIdentity(ctx context.Context, tracer trace.Tracer, inboundTP, inboundTS string, spanAttrs []attribute.KeyValue) traceIdentity {
	ctx = propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{
		"traceparent": inboundTP,
		"tracestate":  inboundTS,
	})
	inboundSC := trace.SpanContextFromContext(ctx)

	spanKind := trace.SpanKindInternal
	opts := []trace.SpanStartOption{trace.WithAttributes(spanAttrs...)}
	if inboundSC.IsRemote() {
		spanKind = trace.SpanKindConsumer
		opts = append(opts, trace.WithSpanKind(spanKind))
	}

	ctx, rootSpan := tracer.Start(ctx, "run", opts...)

	propagatedFlags := rootSpan.SpanContext().TraceFlags()
	if inboundSC.IsValid() && inboundSC.IsRemote() && !inboundSC.IsSampled() {
		propagatedFlags = inboundSC.TraceFlags()
	}
	traceparent := telemetry.TraceparentWithFlags(rootSpan.SpanContext(), propagatedFlags)

	return traceIdentity{
		Ctx:         ctx,
		RootSpan:    rootSpan,
		Traceparent: traceparent,
		SpanKind:    spanKind,
	}
}

// childScriptEnv builds the environment for a host-side child script (pre- or
// post-script): the harness RunnerEnv layered over the process environment,
// plus the W3C TRACEPARENT for trace propagation (ADR 0050 Level 1). Any
// TRACEPARENT already present — inherited from the process environment or
// set in runner_env — is filtered out first: env lookups resolve the first
// match, so a stale value would shadow fullsend's own, and fullsend's trace
// identity never derives from runner_env (issue #2779). An empty traceparent
// (telemetry disabled) is omitted rather than emitted blank.
func childScriptEnv(runnerEnv map[string]string, traceparent string) []string {
	merged := append(os.Environ(), envToList(runnerEnv)...)
	env := make([]string, 0, len(merged)+1)
	for _, e := range merged {
		if !strings.HasPrefix(e, "TRACEPARENT=") {
			env = append(env, e)
		}
	}
	if traceparent != "" {
		env = append(env, "TRACEPARENT="+traceparent)
	}
	return env
}

// validationEnv builds the extra environment entries for the validation
// script. It includes RunnerEnv, TARGET_REPO_DIR, FULLSEND_RUN_DIR, and —
// when the harness specifies a validation_loop.schema — FULLSEND_OUTPUT_SCHEMA
// pointing to the host-side cached schema path.
func validationEnv(h *harness.Harness, hostRepoDir, runDir string) []string {
	env := append(envToList(h.RunnerEnv),
		fmt.Sprintf("TARGET_REPO_DIR=%s", hostRepoDir),
		fmt.Sprintf("FULLSEND_RUN_DIR=%s", runDir),
	)
	if h.ValidationLoop != nil && h.ValidationLoop.Schema != "" {
		env = append(env, fmt.Sprintf("FULLSEND_OUTPUT_SCHEMA=%s", h.ValidationLoop.Schema))
	}
	return env
}

func envToList(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	list := make([]string, 0, len(env))
	for _, k := range keys {
		list = append(list, fmt.Sprintf("%s=%s", k, env[k]))
	}
	return list
}

// openTeeReader wraps r in an io.TeeReader that copies to the file at
// outputPath, returning the reader and a closer. If outputPath is empty or
// the file cannot be created, r is returned unchanged and the warn is logged.
func openTeeReader(r io.Reader, outputPath string, printer *ui.Printer) (io.Reader, func()) {
	if outputPath == "" {
		return r, func() {}
	}
	f, err := os.Create(outputPath)
	if err != nil {
		printer.StepWarn("Failed to create claude-output.jsonl: " + err.Error())
		return r, func() {}
	}
	return io.TeeReader(r, f), func() { f.Close() }
}

var heartbeatInterval = 30 * time.Second

func runHeartbeat(printer *ui.Printer, start time.Time, timeout time.Duration, done <-chan struct{}) {
	runHeartbeatTo(os.Stderr, printer, start, timeout, done)
}

func runHeartbeatTo(w io.Writer, printer *ui.Printer, start time.Time, timeout time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	isCI := os.Getenv("GITHUB_ACTIONS") == "true"

	for {
		select {
		case <-done:
			if isCI {
				elapsed := time.Since(start).Truncate(time.Second)
				fmt.Fprintf(w, "::notice::Agent completed (%s)\n", elapsed)
			}
			return
		case <-ticker.C:
			elapsed := time.Since(start).Truncate(time.Second)
			remaining := (timeout - elapsed).Truncate(time.Second)
			msg := fmt.Sprintf("Agent running (%s elapsed, %s remaining)", elapsed, remaining)
			printer.Heartbeat(msg)
		}
	}
}

func readOIDCAuthFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("FULLSEND_GCP_OIDC_AUTH_FILE not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading OIDC auth file: %w", err)
	}
	val := strings.TrimSpace(string(data))
	if val == "" {
		return "", fmt.Errorf("OIDC auth file is empty")
	}
	return val, nil
}

var oidcRefreshInterval = 4 * time.Minute

func runOIDCRefresh(ctx context.Context, sandboxName, oidcURL, oidcAuth string, printer *ui.Printer) {
	ticker := time.NewTicker(oidcRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := refreshOIDCToken(ctx, sandboxName, oidcURL, oidcAuth); err != nil {
				if ctx.Err() != nil {
					return
				}
				printer.StepWarn("OIDC token refresh failed: " + err.Error())
			} else {
				printer.StepDone("OIDC token refreshed")
			}
		}
	}
}

var oidcHTTPClient = &http.Client{Timeout: 120 * time.Second} // matches pre-refactor shared httpClient timeout

func refreshOIDCToken(ctx context.Context, sandboxName, oidcURL, oidcAuth string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", oidcURL, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", oidcAuth)

	resp, err := oidcHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetching OIDC token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("OIDC endpoint returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return fmt.Errorf("reading OIDC token response: %w", err)
	}
	if len(body) == 0 {
		return fmt.Errorf("OIDC endpoint returned empty token")
	}
	if !json.Valid(body) {
		return fmt.Errorf("OIDC endpoint returned non-JSON response")
	}

	tmpFile, err := os.CreateTemp("", "fullsend-oidc-*.token")
	if err != nil {
		return fmt.Errorf("creating temp token file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(body); err != nil {
		tmpFile.Close()
		return fmt.Errorf("writing temp token file: %w", err)
	}
	tmpFile.Close()

	remotePath := sandbox.SandboxWorkspace + "/.gcp-oidc-token"
	if err := sandbox.UploadFile(sandboxName, tmpFile.Name(), remotePath); err != nil {
		return fmt.Errorf("copying token to sandbox: %w", err)
	}

	return nil
}

// buildScanContextCommand builds the command to run `fullsend scan context`
// inside the sandbox. It finds known context files (including SKILL.md in
// skill directories) in the repo directory and passes them as arguments.
func buildScanContextCommand(repoDir, traceID string) string {
	// Defense-in-depth: validate traceID before shell interpolation. Uses
	// IsShellSafeTraceID (not IsValidTraceID) because the id may have been
	// adopted from an inbound W3C traceparent (issue #2779), so it is not
	// necessarily UUID v4.
	if !security.IsShellSafeTraceID(traceID) {
		// Should never happen with internal generation, but fail safely.
		traceID = "invalid-trace-id"
	}
	// Use find to locate context files, then pass them to fullsend scan context.
	// This runs inside the sandbox where fullsend is available.
	// Quote repoDir to prevent shell injection via directory names.
	escapedDir := strings.ReplaceAll(repoDir, "'", "'\\''")

	// Build -iname arguments from ScannableFiles to keep the lists in sync.
	var inames []string
	seen := map[string]bool{}
	for name := range security.ScannableFiles {
		lower := strings.ToLower(name)
		if seen[lower] {
			continue
		}
		seen[lower] = true
		inames = append(inames, fmt.Sprintf("-iname '%s'", lower))
	}
	// Add files only relevant for find (not in ScannableFiles).
	for _, extra := range []string{".cursorignore"} {
		if !seen[extra] {
			inames = append(inames, fmt.Sprintf("-iname '%s'", extra))
		}
	}
	sort.Strings(inames) // deterministic ordering
	inameExpr := strings.Join(inames, " -o ")

	// Source .env to get PATH where fullsend is installed
	envFile := sandbox.SandboxWorkspace + "/.env"

	return fmt.Sprintf(
		". %s && FULLSEND_TRACE_ID='%s' find '%s' -maxdepth %d -type f \\( %s \\) -exec fullsend scan context {} +",
		envFile, traceID, escapedDir, maxContextScanDepth, inameExpr,
	)
}

// collectOpenshellLogs extracts OpenShell logs (sandbox and gateway sources)
// into <runDir>/logs/ before sandbox deletion. Failures are warned but never
// block the run — log collection is best-effort.
func collectOpenshellLogs(sandboxName, runDir string, printer *ui.Printer) {
	if runDir == "" {
		return
	}

	logsDir := filepath.Join(runDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		printer.StepWarn("Failed to create logs directory: " + err.Error())
		return
	}

	printer.StepStart("Collecting OpenShell logs")
	collected := 0

	sources := []struct {
		name string
		file string
	}{
		{"sandbox", "openshell-sandbox.log"},
		{"gateway", "openshell-gateway.log"},
	}

	for _, src := range sources {
		output, err := sandbox.CollectLogs(sandboxName, src.name)
		if err != nil {
			printer.StepWarn(fmt.Sprintf("Could not collect %s logs: %s", src.name, err.Error()))
			continue
		}
		logPath := filepath.Join(logsDir, src.file)
		if err := os.WriteFile(logPath, []byte(output), 0o644); err != nil {
			printer.StepWarn(fmt.Sprintf("Could not write %s: %s", src.file, err.Error()))
			continue
		}
		collected++
	}

	if collected > 0 {
		printer.StepDone(fmt.Sprintf("Collected %d OpenShell log source(s) to %s", collected, logsDir))
	}
}

// relOrAbs returns path relative to base, falling back to the absolute path if Rel fails.
func relOrAbs(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}

// excludeAgentWorkingDirs adds agent working directory patterns to
// .git/info/exclude so they are invisible to git status and git add.
func excludeAgentWorkingDirs(sandboxName, repoDir string, printer *ui.Printer) error {
	var lines []string
	for _, pattern := range agentWorkingDirExcludes {
		lines = append(lines, pattern)
	}
	if len(lines) == 0 {
		return nil
	}
	payload := strings.Join(lines, "\n")
	excludeCmd := fmt.Sprintf("printf '%%s\\n' '%s' >> %s/.git/info/exclude",
		payload, repoDir)
	if _, _, _, err := sandbox.Exec(sandboxName, excludeCmd, 5*time.Second); err != nil {
		return fmt.Errorf("writing git exclude: %w", err)
	}
	return nil
}

// hasAgentsMD checks whether the repo directory contains an AGENTS.md file
// in any common casing.
func hasAgentsMD(repoDir string) bool {
	for _, name := range []string{"AGENTS.md", "agents.md", "Agents.md"} {
		if _, err := os.Stat(filepath.Join(repoDir, name)); err == nil {
			return true
		}
	}
	return false
}

// hasClaudeMD checks whether the repo directory contains a CLAUDE.md file
// in any common casing.
func hasClaudeMD(repoDir string) bool {
	for _, name := range []string{"CLAUDE.md", "claude.md", "Claude.md", ".claude.md"} {
		if _, err := os.Stat(filepath.Join(repoDir, name)); err == nil {
			return true
		}
	}
	return false
}

// claudeMDPointerContent is the content injected into CLAUDE.md when a repo
// has AGENTS.md but no CLAUDE.md.
const claudeMDPointerContent = "Project rules and instructions live in [AGENTS.md](AGENTS.md). Read that file now — it is the single source of truth for all agent-facing guidance in this repo.\n"

// sandboxExecFunc is the signature for sandbox command execution, extracted
// for testability.
type sandboxExecFunc func(sandboxName, command string, timeout time.Duration) (stdout, stderr string, exitCode int, err error)

// injectClaudeMDPointer writes a minimal CLAUDE.md bridge file directly
// inside the sandbox and excludes it from git tracking.
func injectClaudeMDPointer(sandboxName, remoteRepositoryDir string, printer *ui.Printer) {
	doInjectClaudeMDPointer(sandboxName, remoteRepositoryDir, printer, sandbox.Exec)
}

// doInjectClaudeMDPointer is the testable core of injectClaudeMDPointer.
func doInjectClaudeMDPointer(sandboxName, remoteRepositoryDir string, printer *ui.Printer, execFn sandboxExecFunc) {
	writeCmd := fmt.Sprintf("printf '%%s' %q > %s/CLAUDE.md", claudeMDPointerContent, remoteRepositoryDir)
	if _, _, _, err := execFn(sandboxName, writeCmd, 5*time.Second); err != nil {
		printer.StepWarn("Could not inject CLAUDE.md: " + err.Error())
		return
	}
	excludeCmd := fmt.Sprintf("echo 'CLAUDE.md' >> %s/.git/info/exclude", remoteRepositoryDir)
	if _, _, _, err := execFn(sandboxName, excludeCmd, 5*time.Second); err != nil {
		printer.StepWarn("Could not add CLAUDE.md to git exclude: " + err.Error())
	}
	printer.StepDone("Injected CLAUDE.md pointer to AGENTS.md (target repo has none)")
}

// scanRepoContextFiles walks the target repo directory for known context
// files (CLAUDE.md, AGENTS.md, SKILL.md, etc.) and runs the InputPipeline
// on each. Returns all findings across scanned files.
func scanRepoContextFiles(repoDir string) []security.Finding {
	const maxContextFileSize int64 = 1 << 20 // 1 MB

	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "vendor": true,
		"__pycache__": true, ".venv": true,
	}

	pipeline := security.InputPipeline()
	var allFindings []security.Finding

	err := filepath.WalkDir(repoDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			relPath := relOrAbs(repoDir, path)
			allFindings = append(allFindings, security.Finding{
				Scanner:  "context_injection",
				Name:     "scan_error",
				Severity: "medium",
				Detail:   fmt.Sprintf("could not access %s: %v", relPath, walkErr),
				Position: -1,
			})
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			rel := relOrAbs(repoDir, path)
			// find -maxdepth N allows N levels below start; separator count maps to depth-1.
			if rel != "." && strings.Count(rel, string(os.PathSeparator)) >= maxContextScanDepth-1 {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if !security.ShouldScan(d.Name()) {
			return nil
		}
		relPath := relOrAbs(repoDir, path)
		info, err := d.Info()
		if err != nil {
			allFindings = append(allFindings, security.Finding{
				Scanner:  "context_injection",
				Name:     "scan_error",
				Severity: "medium",
				Detail:   fmt.Sprintf("%s: could not stat file: %v", relPath, err),
				Position: -1,
			})
			return nil
		}
		if info.Size() > maxContextFileSize {
			allFindings = append(allFindings, security.Finding{
				Scanner:  "context_injection",
				Name:     "file_too_large",
				Severity: "medium",
				Detail:   fmt.Sprintf("%s: skipped, exceeds %d byte limit (%d bytes)", relPath, maxContextFileSize, info.Size()),
				Position: -1,
			})
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			allFindings = append(allFindings, security.Finding{
				Scanner:  "context_injection",
				Name:     "scan_error",
				Severity: "medium",
				Detail:   fmt.Sprintf("%s: could not read file: %v", relPath, err),
				Position: -1,
			})
			return nil
		}
		result := pipeline.Scan(string(content))
		for i := range result.Findings {
			result.Findings[i].Detail = fmt.Sprintf("%s: %s", relPath, result.Findings[i].Detail)
		}
		allFindings = append(allFindings, result.Findings...)
		return nil
	})
	if err != nil {
		allFindings = append(allFindings, security.Finding{
			Scanner:  "context_injection",
			Name:     "scan_error",
			Severity: "high",
			Detail:   fmt.Sprintf("walk terminated: %v", err),
			Position: -1,
		})
	}

	return allFindings
}

// scanOutputFiles runs the output security pipeline (unicode normalization and
// secret redaction) on extracted output files, recursively walking all
// subdirectories (iteration-N/output/, etc.).
func scanOutputFiles(outputDir, traceID string, printer *ui.Printer) error {
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		printer.StepInfo("No output files to scan")
		return nil
	}

	pipeline := security.OutputPipeline()
	findingCount := 0
	findingsPath := filepath.Join(outputDir, "security", "findings.jsonl")

	err := filepath.WalkDir(outputDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			// Skip the security findings directory itself.
			if d.Name() == "security" {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip the telemetry JSONL (metadata-only, still open for append).
		if path == filepath.Join(outputDir, telemetry.TelemetryFile) {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			relPath, _ := filepath.Rel(outputDir, path)
			printer.StepWarn(fmt.Sprintf("Could not read %s: %v", relPath, readErr))
			return nil
		}

		text := string(content)
		result := pipeline.Scan(text)
		if len(result.Findings) > 0 {
			findingCount += len(result.Findings)
			relPath, _ := filepath.Rel(outputDir, path)
			for _, f := range result.Findings {
				printer.StepWarn(fmt.Sprintf("Sanitized [%s] in %s: %s", f.Name, relPath, f.Detail))
				security.AppendFinding(findingsPath,
					security.TracedFinding{
						TraceID:   traceID,
						Timestamp: time.Now().UTC().Format(time.RFC3339),
						Phase:     "host_output",
						Finding:   f,
					})
			}
			// Sanitized may be empty when all content was invisible characters.
			out := result.Sanitized
			if writeErr := os.WriteFile(path, []byte(out), 0o644); writeErr != nil {
				printer.StepWarn(fmt.Sprintf("Could not write sanitized %s: %v", relPath, writeErr))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	if findingCount > 0 {
		printer.StepWarn(fmt.Sprintf("Sanitized %d finding(s) in output files", findingCount))
	} else {
		printer.StepDone("Output files clean — no issues found")
	}
	return nil
}

// injectTraceID appends the FULLSEND_TRACE_ID to the sandbox .env file.
func injectTraceID(sandboxName, traceID string) error {
	if !security.IsShellSafeTraceID(traceID) {
		return fmt.Errorf("invalid trace ID format: %q", traceID)
	}
	// Safe: IsShellSafeTraceID() above ensures traceID is only hex and dashes.
	cmd := fmt.Sprintf("echo 'export FULLSEND_TRACE_ID=%s' >> %s/.env", traceID, sandbox.SandboxWorkspace)
	_, _, _, err := sandbox.Exec(sandboxName, cmd, 10*time.Second)
	return err
}

// applySandboxImageOverride replaces image with the FULLSEND_SANDBOX_IMAGE env
// var value when set. Returns the resolved image and whether an override was applied.
func applySandboxImageOverride(image string) (string, bool) {
	if override := os.Getenv("FULLSEND_SANDBOX_IMAGE"); override != "" {
		return override, true
	}
	return image, false
}

// needsCrossCompilation reports whether the host binary cannot run inside the
// sandbox (Linux). True when running on macOS or any non-Linux OS.
func needsCrossCompilation() bool {
	return runtime.GOOS != "linux"
}

// copyFile copies src to dst, preserving permissions.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	info, err := in.Stat()
	if err != nil {
		return err
	}
	return os.Chmod(dst, info.Mode())
}

// sandboxArch returns the target architecture for the sandbox binary.
// Defaults to the host arch (correct when sandbox image matches host, e.g.
// arm64 Mac → arm64 sandbox image). Override with FULLSEND_SANDBOX_ARCH
// when the sandbox image uses a different architecture (e.g. amd64 image
// on an arm64 host via emulation). Only amd64 and arm64 are supported.
func sandboxArch() string {
	if arch := os.Getenv("FULLSEND_SANDBOX_ARCH"); arch != "" {
		if !binary.ValidArch(arch) {
			fmt.Fprintf(os.Stderr, "WARNING: FULLSEND_SANDBOX_ARCH=%q is not a supported architecture (amd64, arm64), using host arch %s\n", arch, runtime.GOARCH)
			return runtime.GOARCH
		}
		return arch
	}
	return runtime.GOARCH
}

// detectForgePlatform determines the forge platform from the CLI flag or CI
// environment variables. Precedence: explicit flag > GITHUB_ACTIONS > GITLAB_CI.
// Returns an error if the flag value is not a recognized forge key.
func detectForgePlatform(flag string) (string, error) {
	if flag != "" {
		if !harness.ValidForgePlatform(flag) {
			return "", fmt.Errorf("--forge: %q is not a valid forge platform (valid: %s)", flag, harness.ForgeKeyList())
		}
		return flag, nil
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		return "github", nil
	}
	if os.Getenv("GITLAB_CI") == "true" {
		return "gitlab", nil
	}
	return "", nil
}

func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// setupStatusNotifier creates a status comment notifier. The role parameter
// accepts either a raw harness role (e.g. "code") or a canonical role
// (e.g. "coder"); it is resolved via resolveRole internally.
func setupStatusNotifier(fullsendDir string, role string, sOpts statusOpts, printer *ui.Printer) (*statuscomment.Notifier, error) {
	parts := strings.SplitN(sOpts.statusRepo, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("--status-repo must be in owner/repo format, got %q", sOpts.statusRepo)
	}
	owner, repo := parts[0], parts[1]

	mintURL := sOpts.mintURL
	if mintURL == "" {
		mintURL = os.Getenv("FULLSEND_MINT_URL")
	}
	if mintURL == "" {
		return nil, fmt.Errorf("no mint URL available (set --mint-url or FULLSEND_MINT_URL)")
	}

	var notifyCfg config.StatusNotificationConfig
	orgConfigPath := filepath.Join(fullsendDir, "config.yaml")
	if orgCfg := tryLoadFullsendConfig(orgConfigPath, printer); orgCfg != nil {
		// tryLoadFullsendConfig returns a ConfigWriter which may wrap either
		// orgConfig or perRepoConfig. Only orgConfig implements OrgConfigReader
		// (and carries StatusNotifications). When the loaded config is per-repo,
		// this assertion intentionally falls through, leaving notifyCfg at its
		// zero value — per-repo configs do not support status notifications.
		if ocr, ok := orgCfg.(config.OrgConfigReader); ok && ocr.StatusNotifications() != nil {
			notifyCfg = *ocr.StatusNotifications()
		}
	}

	sha := os.Getenv("GITHUB_SHA")
	// Prefer explicit PR_HEAD_SHA (set by per-repo workflow_call callers
	// where GITHUB_EVENT_PATH lacks the dispatched event_payload wrapper).
	// Fall back to extracting from event payload (per-org workflow_dispatch).
	if prSHA := os.Getenv("PR_HEAD_SHA"); prSHA != "" {
		sha = prSHA
	} else if prSHA := prHeadSHAFromEventPath(os.Getenv("GITHUB_EVENT_PATH")); prSHA != "" {
		sha = prSHA
	}
	runID := os.Getenv("GITHUB_RUN_ID")
	if runID == "" {
		runID = fmt.Sprintf("%d", time.Now().UnixNano())
	}

	n := statuscomment.New(nil, notifyCfg, owner, repo, sOpts.statusNum, sOpts.runURL, sha, runID)
	n.SetWarnFunc(func(format string, args ...any) {
		printer.StepWarn(fmt.Sprintf(format, args...))
	})

	canonRole := resolveRole(role)
	n.SetClientFactory(func(ctx context.Context) (forge.Client, error) {
		result, err := statusMintToken(ctx, mintclient.MintRequest{
			MintURL: mintURL,
			Role:    canonRole,
			Repos:   []string{repo},
		})
		if err != nil {
			return nil, fmt.Errorf("minting status token: %w", err)
		}
		if !mintTokenPattern.MatchString(result.Token) {
			return nil, fmt.Errorf("minted status token contains unexpected characters")
		}
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			fmt.Fprintf(os.Stderr, "::add-mask::%s\n", result.Token)
		}
		return gh.New(result.Token), nil
	})

	return n, nil
}

// prHeadSHAFromEventPath extracts pull_request.head.sha from the event
// payload embedded in a workflow_dispatch event file. For workflow_dispatch
// events, the file contains {"inputs": {"event_payload": "<json-string>"}}.
// Returns empty string if the file is unreadable or the field is absent.
func prHeadSHAFromEventPath(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// The workflow_dispatch event has inputs.event_payload as a JSON string.
	var event struct {
		Inputs struct {
			EventPayload string `json:"event_payload"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(data, &event); err != nil || event.Inputs.EventPayload == "" {
		return ""
	}
	var payload struct {
		PullRequest struct {
			Head struct {
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal([]byte(event.Inputs.EventPayload), &payload); err != nil {
		return ""
	}
	return payload.PullRequest.Head.SHA
}

// emitDiagnostic prints a harness lint diagnostic with severity-appropriate formatting.
// Warnings use StepWarn, errors use StepFail. This ensures future SeverityError
// diagnostics are visually distinct from warnings.
func emitDiagnostic(printer *ui.Printer, diag harness.Diagnostic) {
	switch diag.Severity {
	case harness.SeverityError:
		printer.StepFail(diag.String())
	default:
		printer.StepWarn(diag.String())
	}
}

// emitDiagnosticWithContext prints a diagnostic with additional context (e.g., agent name).
// Used by lock --all where multiple harnesses are processed and context helps identify which.
func emitDiagnosticWithContext(printer *ui.Printer, context string, diag harness.Diagnostic) {
	msg := fmt.Sprintf("%s: %s", context, diag.String())
	switch diag.Severity {
	case harness.SeverityError:
		printer.StepFail(msg)
	default:
		printer.StepWarn(msg)
	}
}

type tokenVar struct {
	Name  string
	Value string // empty = use minted token
}

// roleTokenVars maps canonical role names to the additional env vars they
// require beyond GH_TOKEN. These match the vars declared in
// forge.github.runner_env across the harness YAML files.
var roleTokenVars = map[string][]tokenVar{
	"coder":  {{Name: "PUSH_TOKEN"}, {Name: "PUSH_TOKEN_SOURCE", Value: "github-app"}},
	"review": {{Name: "REVIEW_TOKEN"}},
}

// mintAgentToken mints a GitHub App installation token for the agent's role
// and sets the appropriate env vars so RunnerEnv expansion and host_files
// expansion pick them up. Returns (minted bool, cleanup func, err).
// The caller should defer cleanup() to clear tokens from the process env.
func mintAgentToken(ctx context.Context, role, mintURL string, printer *ui.Printer) (bool, func(), error) {
	if mintURL == "" || role == "" {
		return false, func() {}, nil
	}

	repos, err := resolveMintRepos()
	if err != nil {
		return false, nil, fmt.Errorf("resolving mint repos for role %s: %w", role, err)
	}

	role = resolveRole(role)
	if err := mintcore.ValidateRoleName(role); err != nil {
		return false, nil, fmt.Errorf("invalid role: %w", err)
	}
	printer.StepStart("Minting agent token (role: " + role + ")")

	result, err := mintAgentTokenWithRetry(ctx, role, mintURL, repos, printer)
	if err != nil {
		return false, nil, err
	}

	// TODO(ADR-0045 R22): use forge platform context instead of raw env check.
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		fmt.Fprintf(os.Stderr, "::add-mask::%s\n", result.Token)
	}

	// NOTE: os.Setenv is not goroutine-safe. Minting MUST complete
	// before any goroutines that read env vars (sandbox streaming,
	// post-script execution) are launched.
	originals := make(map[string]string)
	envVars := []string{"GH_TOKEN"}
	if v, ok := os.LookupEnv("GH_TOKEN"); ok {
		originals["GH_TOKEN"] = v
	}
	os.Setenv("GH_TOKEN", result.Token)

	for _, tv := range roleTokenVars[role] {
		if v, ok := os.LookupEnv(tv.Name); ok {
			originals[tv.Name] = v
		}
		if tv.Value != "" {
			os.Setenv(tv.Name, tv.Value)
		} else {
			os.Setenv(tv.Name, result.Token)
		}
		envVars = append(envVars, tv.Name)
	}

	cleanup := func() {
		for _, v := range envVars {
			if orig, ok := originals[v]; ok {
				os.Setenv(v, orig)
			} else {
				os.Unsetenv(v)
			}
		}
	}

	expiresAt := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || r == '-' || r == ':' || r == 'T' || r == 'Z' || r == '+' || r == '.' {
			return r
		}
		return -1
	}, result.ExpiresAt)
	printer.StepDone("Agent token minted (expires " + expiresAt + ")")
	return true, cleanup, nil
}

// mintTokenMaxAttempts is the total number of minting attempts — the initial
// attempt plus mintTokenMaxAttempts-1 retries — before mintAgentTokenWithRetry
// gives up.
const mintTokenMaxAttempts = 4

// mintTokenMaxBackoff caps the delay mintTokenBackoff can return, guarding
// against overflow or runaway waits if mintTokenMaxAttempts ever grows.
const mintTokenMaxBackoff = 8 * time.Second

// mintTokenBackoff computes the delay before the retry that follows the
// given failed attempt (1-indexed), doubling each time: 2s, 4s, 8s for the
// default mintTokenMaxAttempts of 4. It is a package variable so tests can
// shorten it and avoid real sleeps.
var mintTokenBackoff = func(attempt int) time.Duration {
	shift := attempt - 1
	if shift > 10 {
		shift = 10
	}
	backoff := 2 * time.Second * time.Duration(uint64(1)<<uint(shift))
	if backoff > mintTokenMaxBackoff {
		backoff = mintTokenMaxBackoff
	}
	return backoff
}

// mintAgentTokenWithRetry retries only the token-shape validation performed
// on top of statusMintToken's response — a malformed token on an otherwise
// successful mint call, the exact failure reported in issue #5377 ("minted
// agent token contains unexpected characters"). Issue #5377 suspects, but
// never confirms, that this is a transient response glitch; retrying it
// tests that suspicion without re-litigating it. statusMintToken
// (mintclient.MintToken) already retries transient request failures (5xx,
// network errors) internally and fails fast on permanent ones (4xx), so its
// errors are returned as-is rather than retried a second time here — doing
// so would retry permanent failures pointlessly and compound latency on
// persistent transient ones.
func mintAgentTokenWithRetry(ctx context.Context, role, mintURL string, repos []string, printer *ui.Printer) (*mintclient.MintResult, error) {
	var lastErr error
	for attempt := 1; attempt <= mintTokenMaxAttempts; attempt++ {
		result, err := statusMintToken(ctx, mintclient.MintRequest{
			MintURL: mintURL,
			Role:    role,
			Repos:   repos,
		})
		if err != nil {
			return nil, fmt.Errorf("minting agent token for role %s: %w", role, err)
		}
		if mintTokenPattern.MatchString(result.Token) {
			return result, nil
		}
		lastErr = fmt.Errorf("minted agent token contains unexpected characters")

		if attempt < mintTokenMaxAttempts {
			backoff := mintTokenBackoff(attempt)
			printer.StepWarn(fmt.Sprintf("Minting agent token failed (attempt %d/%d): %v — retrying in %s", attempt, mintTokenMaxAttempts, lastErr, backoff))
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil, fmt.Errorf("minting agent token for role %s failed after %d attempts: %w", role, mintTokenMaxAttempts, lastErr)
}

// resolveMintRepos determines which repos to request token access for.
// MINT_REPOS (comma-separated) takes precedence, falling back to extracting
// the repo name from REPO_FULL_NAME (owner/repo → repo).
func resolveMintRepos() ([]string, error) {
	if v := os.Getenv("MINT_REPOS"); v != "" {
		var repos []string
		for _, r := range strings.Split(v, ",") {
			if trimmed := strings.TrimSpace(r); trimmed != "" {
				repos = append(repos, trimmed)
			}
		}
		if len(repos) > 0 {
			if err := validateRepoNames(repos); err != nil {
				return nil, err
			}
			return repos, nil
		}
	}

	fullName := os.Getenv("REPO_FULL_NAME")
	if fullName == "" {
		return nil, fmt.Errorf("MINT_REPOS or REPO_FULL_NAME must be set for token minting")
	}

	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return nil, fmt.Errorf("REPO_FULL_NAME must be in owner/repo format, got %q", fullName)
	}
	repo := parts[1]
	if !mintcore.RepoNamePattern.MatchString(repo) {
		return nil, fmt.Errorf("invalid repo name %q from REPO_FULL_NAME: must match %s", repo, mintcore.RepoNamePattern.String())
	}
	return []string{repo}, nil
}

func validateRepoNames(repos []string) error {
	for _, r := range repos {
		if !mintcore.RepoNamePattern.MatchString(r) {
			return fmt.Errorf("invalid repo name %q in MINT_REPOS: must match %s", r, mintcore.RepoNamePattern.String())
		}
	}
	return nil
}

// resolveAgentSource resolves the harness path for an agent, checking
// config-registered agents first, then falling back to the agents repo
// (fullsend-ai/agents), then to disk-based lookup.
// Returns the local filesystem path to the harness (cached for URL sources)
// and any fetch dependencies from URL-based agent resolution.
func resolveAgentSource(ctx context.Context, fullsendDir, agentName string, forgeClient forge.Client, orgCfg config.ConfigReader, composeOpts harness.ComposeOpts, printer *ui.Printer) (string, []harness.Dependency, error) {
	if orgCfg == nil || len(orgCfg.AgentEntries()) == 0 {
		if path, deps, ok := tryAgentsRepoFallback(ctx, agentName, forgeClient, composeOpts, printer); ok {
			return path, deps, nil
		}
		path, err := resolveHarnessPath(fullsendDir, agentName, printer)
		return path, nil, err
	}

	if err := config.ValidateAgentEntries(orgCfg.AgentEntries(), orgCfg.AllowedResources()); err != nil {
		return "", nil, fmt.Errorf("invalid agent config: %w", err)
	}

	sha := commitSHA
	if sha == "dev" {
		sha = ""
	}
	scaffoldNames, snErr := scaffold.HarnessNames()
	if snErr != nil {
		return "", nil, fmt.Errorf("listing scaffold harnesses: %w", snErr)
	}

	merged, err := config.MergedAgents(scaffoldNames, sha, orgCfg.AgentEntries(), scaffold.HarnessBaseURLWithHash)
	if err != nil {
		return "", nil, fmt.Errorf("building merged agent set: %w", err)
	}

	agent := config.LookupMergedAgent(merged, agentName)
	if agent == nil || !agent.IsConfig {
		// An explicitly disabled agent must not fall through to the
		// agents-repo fallback or disk lookup — that would silently
		// re-enable it. Return a clear error instead.
		if config.IsAgentExplicitlyDisabled(orgCfg.AgentEntries(), agentName) {
			printer.StepFail(fmt.Sprintf("Agent %s is disabled in config", agentName))
			return "", nil, fmt.Errorf("agent %q is explicitly disabled in config", agentName)
		}
		if path, deps, ok := tryAgentsRepoFallback(ctx, agentName, forgeClient, composeOpts, printer); ok {
			return path, deps, nil
		}
		path, err := resolveHarnessPath(fullsendDir, agentName, printer)
		return path, nil, err
	}

	entry := findConfigAgentEntry(orgCfg.AgentEntries(), agent.Name)
	if entry == nil {
		return "", nil, fmt.Errorf("config agent %s: entry not found", agent.Name)
	}

	if harness.IsURL(agent.Source) {
		printer.StepStart(fmt.Sprintf("Fetching agent harness: %s", agent.Name))
	}
	resolved, err := harness.ResolveRegisteredPath(ctx, fullsendDir, *entry, orgCfg.AllowedResources(), composeOpts)
	if err != nil {
		if harness.IsURL(agent.Source) {
			printer.StepFail("Failed to fetch agent harness")
		}
		return "", nil, fmt.Errorf("resolving config agent %s: %w", agent.Name, err)
	}
	if harness.IsURL(agent.Source) {
		printer.StepDone(fmt.Sprintf("Agent %s resolved from config (URL)", agent.Name))
		return resolved.Path, []harness.Dependency{resolved.Dep}, nil
	}
	printer.StepDone(fmt.Sprintf("Agent %s resolved from config (local path)", agent.Name))
	return resolved.Path, nil, nil
}

func findConfigAgentEntry(agents []config.AgentEntry, name string) *config.AgentEntry {
	lower := strings.ToLower(name)
	for i := len(agents) - 1; i >= 0; i-- {
		if strings.ToLower(agents[i].DerivedName()) == lower && agents[i].IsEnabled() {
			return &agents[i]
		}
	}
	return nil
}

// tryAgentsRepoFallback attempts to resolve an agent from the default agents
// repository (fullsend-ai/agents) by fetching the latest harness from the
// main branch. This is a transitional mechanism to support the extraction of
// first-party agents into a separate repository (fullsend-ai/agents) without
// requiring config changes from existing users.
//
// Returns (path, deps, true) on success, or ("", nil, false) if the fallback
// should be skipped (offline, no forge client, agent not known, not allowlisted, etc.).
// All errors are non-fatal — the caller falls through to disk-based lookup.
func tryAgentsRepoFallback(ctx context.Context, agentName string, forgeClient forge.Client, composeOpts harness.ComposeOpts, printer *ui.Printer) (string, []harness.Dependency, bool) {
	normalizedName := strings.ToLower(agentName)
	if !defaultAgentsRepoKnownAgents[normalizedName] {
		return "", nil, false
	}
	if composeOpts.FetchPolicy.Offline {
		return "", nil, false
	}
	if forgeClient == nil {
		return "", nil, false
	}

	allowlist := composeOpts.OrgAllowlist

	tagRef := "tags/" + config.DefaultUpstreamRef
	tagSHA, err := forgeClient.GetRef(ctx, defaultAgentsRepoOwner, defaultAgentsRepoName, tagRef)
	if err != nil {
		printer.StepWarn(fmt.Sprintf("Could not resolve %s/%s@%s: %v", defaultAgentsRepoOwner, defaultAgentsRepoName, config.DefaultUpstreamRef, err))
		return "", nil, false
	}
	if !commitSHAPattern.MatchString(tagSHA) {
		printer.StepWarn(fmt.Sprintf("Invalid SHA from %s/%s@%s: %q", defaultAgentsRepoOwner, defaultAgentsRepoName, config.DefaultUpstreamRef, tagSHA))
		return "", nil, false
	}

	rawURL := defaultAgentsRepoURLPrefix + tagSHA + "/harness/" + normalizedName + ".yaml"

	if harness.MatchingAllowedPrefixInList(rawURL, allowlist) == "" {
		printer.StepWarn(fmt.Sprintf("Agents repo fallback skipped for %s: URL not in allowed_remote_resources", agentName))
		return "", nil, false
	}

	shortSHA := tagSHA
	if len(shortSHA) > 12 {
		shortSHA = shortSHA[:12]
	}
	printer.StepStart(fmt.Sprintf("Fetching agent %s from %s/%s@%s", agentName, defaultAgentsRepoOwner, defaultAgentsRepoName, shortSHA))

	content, err := fetch.FetchURL(ctx, rawURL, composeOpts.FetchPolicy)
	if err != nil {
		printer.StepWarn(fmt.Sprintf("Failed to fetch agent %s from agents repo: %v", agentName, err))
		return "", nil, false
	}

	// Content is fetched once and used directly — no self-referential hash
	// verification. Supply-chain integrity relies on the commit-pinned URL,
	// TLS transport, and the org allowlist. Config-registered agents get
	// stronger pinning because their hashes are set at enrollment time.
	contentHash := fetch.ComputeSHA256(content)

	if err := fetch.CachePut(composeOpts.WorkspaceRoot, rawURL, content); err != nil {
		printer.StepWarn(fmt.Sprintf("Failed to cache agents repo content: %v", err))
		return "", nil, false
	}

	cachePath, err := fetch.CachePath(composeOpts.WorkspaceRoot, contentHash)
	if err != nil {
		printer.StepWarn(fmt.Sprintf("Failed to resolve cache path for agent %s: %v", agentName, err))
		return "", nil, false
	}
	localPath := filepath.Join(cachePath, "content")

	if composeOpts.AuditLogPath != "" {
		if err := fetch.AppendFetchAudit(composeOpts.AuditLogPath, fetch.FetchAuditEntry{
			TraceID:   composeOpts.TraceID,
			FetchTime: time.Now().UTC(),
			URL:       rawURL,
			SHA256:    contentHash,
			FetchType: "static",
			AllowedBy: harness.MatchingAllowedPrefixInList(rawURL, allowlist),
			CacheHit:  false,
		}); err != nil {
			printer.StepWarn(fmt.Sprintf("Failed to write fetch audit log: %v", err))
		}
	}

	dep := harness.Dependency{
		Field:     "base",
		URL:       rawURL,
		LocalPath: localPath,
		SHA256:    contentHash,
		FetchedAt: time.Now().UTC(),
		Type:      "file",
	}

	printer.StepDone(fmt.Sprintf("Agent %s resolved from %s/%s@%s", agentName, defaultAgentsRepoOwner, defaultAgentsRepoName, config.DefaultUpstreamRef))
	return localPath, []harness.Dependency{dep}, true
}

// containedLocalPath resolves a relative source path against baseDir and
// verifies the result stays within baseDir. Returns an error for absolute
// paths or paths that escape via traversal.
func containedLocalPath(baseDir, source string) (string, error) {
	if filepath.IsAbs(source) {
		return "", fmt.Errorf("local path must be relative, not absolute")
	}
	resolved := filepath.Clean(filepath.Join(baseDir, source))
	if rel, err := filepath.Rel(baseDir, resolved); err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("local path %q escapes fullsend directory", source)
	}
	// Resolve symlinks and re-check containment to prevent symlink escape.
	real, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", err
	}
	realBase, err := filepath.EvalSymlinks(baseDir)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(realBase, real); err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("local path %q escapes fullsend directory via symlink", source)
	}
	return real, nil
}

// dedupResolvedProviders removes duplicate providers by Name, keeping the last
// occurrence (child overrides base, since base entries come first from
// composition).
func dedupResolvedProviders(providers []resolve.ResolvedProvider) []resolve.ResolvedProvider {
	if len(providers) <= 1 {
		return providers
	}
	seen := make(map[string]int, len(providers))
	for i, rp := range providers {
		seen[rp.Def.Name] = i
	}
	deduped := make([]resolve.ResolvedProvider, 0, len(seen))
	for i, rp := range providers {
		if seen[rp.Def.Name] == i {
			deduped = append(deduped, rp)
		}
	}
	return deduped
}

func dedupResolvedProfiles(profiles []resolve.ResolvedProfile) []resolve.ResolvedProfile {
	if len(profiles) <= 1 {
		return profiles
	}
	seen := make(map[string]int, len(profiles))
	for i, rp := range profiles {
		seen[rp.ID] = i
	}
	deduped := make([]resolve.ResolvedProfile, 0, len(seen))
	for i, rp := range profiles {
		if seen[rp.ID] == i {
			deduped = append(deduped, rp)
		}
	}
	return deduped
}

// mergeProviderDefs merges local and URL-resolved provider definitions.
// Local defs have highest precedence; among URL-resolved defs, last
// occurrence wins (child over base). The returned slice is deterministically
// ordered: local defs first, then URL-resolved names in sorted order.
// shadowed returns the names of URL-resolved providers that were overridden
// by a local provider of the same name.
func mergeProviderDefs(localDefs []harness.ProviderDef, urlProviders []resolve.ResolvedProvider) (allDefs []harness.ProviderDef, shadowed []string) {
	seen := make(map[string]bool, len(localDefs)+len(urlProviders))
	allDefs = make([]harness.ProviderDef, 0, len(localDefs)+len(urlProviders))
	for _, ld := range localDefs {
		seen[ld.Name] = true
		allDefs = append(allDefs, ld)
	}
	lastByName := make(map[string]resolve.ResolvedProvider, len(urlProviders))
	for _, rp := range urlProviders {
		lastByName[rp.Def.Name] = rp
	}
	urlNames := make([]string, 0, len(lastByName))
	for name := range lastByName {
		if !seen[name] {
			urlNames = append(urlNames, name)
		} else {
			shadowed = append(shadowed, name)
		}
	}
	sort.Strings(urlNames)
	sort.Strings(shadowed)
	for _, name := range urlNames {
		allDefs = append(allDefs, lastByName[name].Def)
	}
	return allDefs, shadowed
}

// sandboxProviderNames returns the provider names that should be attached to
// the sandbox: harness-declared (local) names plus URL-resolved names.
// Directory providers not declared in the harness are excluded — they may
// exist on the gateway for other harnesses but must not widen this sandbox's
// credential scope.
func sandboxProviderNames(harnessProviders []string, resolved []resolve.ResolvedProvider) []string {
	seen := make(map[string]bool, len(harnessProviders)+len(resolved))
	names := make([]string, 0, len(harnessProviders)+len(resolved))
	for _, n := range harnessProviders {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, rp := range resolved {
		if !seen[rp.Def.Name] {
			seen[rp.Def.Name] = true
			names = append(names, rp.Def.Name)
		}
	}
	return names
}

// forceRemoveAll restores owner-write permission on all directories under
// path, then removes the entire tree. This handles directories left read-only
// by the readonly_repo sandbox enforcement — os.RemoveAll alone fails with
// EACCES when parent directories lack write permission (the unlinkat syscall
// requires write permission on the containing directory).
//
// Assumption: the readonly_repo enforcement only strips write permission
// (chmod a-w), preserving read and execute bits. WalkDir performs a
// single-pass traversal and cannot retry children after fixing a parent, so
// if a directory also lacked read+execute permissions its children would be
// silently skipped. This is currently unreachable, but callers should be
// aware of the limitation if the permission model changes.
func forceRemoveAll(path string) error {
	// Best-effort permission restore; if WalkDir itself fails on a
	// directory it cannot read, we fix the parent and continue.
	// Chmod errors are logged for operator visibility — if chmod fails
	// for an unexpected reason (e.g. TOCTOU race), the subsequent
	// os.RemoveAll error alone may not indicate the root cause.
	filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error { //nolint:errcheck // best-effort walk; errors are logged individually and the final os.RemoveAll is the authoritative result
		if err != nil {
			// Can't stat p — likely the parent directory lacks +rx.
			// Restore the parent so the next iteration can proceed.
			// Lstat guard: verify the parent is a real directory before
			// chmod to avoid following symlinks (defense-in-depth;
			// SafeDownload already strips dangerous symlinks).
			parent := filepath.Dir(p)
			if fi, lstatErr := os.Lstat(parent); lstatErr == nil && fi.IsDir() {
				if chmodErr := os.Chmod(parent, 0o755); chmodErr != nil {
					fmt.Fprintf(os.Stderr, "WARNING: forceRemoveAll: chmod parent %s: %v\n", parent, chmodErr)
				}
			}
			return nil
		}
		if d.IsDir() {
			if chmodErr := os.Chmod(p, 0o755); chmodErr != nil {
				fmt.Fprintf(os.Stderr, "WARNING: forceRemoveAll: chmod %s: %v\n", p, chmodErr)
			}
		}
		return nil
	})
	return os.RemoveAll(path)
}

// checkProviderProfileIntegrity validates that every URL-resolved provider
// references a profile id that was also URL-resolved. Returns an error
// describing the first mismatch, or nil if all references are valid.
// Returns a non-empty warning string (no error) when providers exist but
// no profiles were resolved.
func checkProviderProfileIntegrity(providers []resolve.ResolvedProvider, profiles []resolve.ResolvedProfile) (warning string, err error) {
	if len(providers) == 0 {
		return "", nil
	}
	if len(profiles) == 0 {
		return "URL-resolved providers present but no URL-resolved profiles — referential integrity not verified", nil
	}
	profileIDs := make(map[string]bool, len(profiles))
	for _, rp := range profiles {
		profileIDs[rp.ID] = true
	}
	var mismatches []string
	for _, rp := range providers {
		if !profileIDs[rp.Def.Type] {
			mismatches = append(mismatches, fmt.Sprintf("%q (type %q)", rp.Def.Name, rp.Def.Type))
		}
	}
	if len(mismatches) > 0 {
		return "", fmt.Errorf(
			"providers reference unknown openshell.profiles types: %s — if these profiles are gateway-resident, move the providers to a local providers/ directory instead",
			strings.Join(mismatches, ", "))
	}
	return "", nil
}
