package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/binary"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/envfile"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/fetchsvc"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/mintclient"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/statuscomment"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	// maxContextScanDepth is the maximum directory depth for scanning context
	// files. Shared between host-side (scanRepoContextFiles) and sandbox-side
	// (buildScanContextCommand) scans to ensure parity.
	maxContextScanDepth = 5
)

// agentWorkingDirExcludes lists directory patterns that agents may create
// during execution but must never commit. These are added to
// .git/info/exclude before the agent runs so git ignores them entirely.
var statusMintToken = mintclient.MintToken

var agentWorkingDirExcludes = []string{
	".agentready/",
	".fullsend-workspace/",
}

// resolveFlags groups CLI flags that control remote resource resolution.
type resolveFlags struct {
	offline      bool
	maxDepth     int
	maxResources int
	forgeClient  forge.Client // injected by tests; nil means construct from env
}

// statusOpts holds the optional status notification parameters for a run.
type statusOpts struct {
	runURL     string
	statusRepo string
	statusNum  int
	mintURL    string
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

	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "base directory containing the .fullsend layout")
	cmd.Flags().StringVar(&outputBase, "output-dir", "", "base directory for run output (default: /tmp/fullsend)")
	cmd.Flags().StringVar(&targetRepo, "target-repo", "", "path to the target repository")
	cmd.Flags().StringVar(&fullsendBinary, "fullsend-binary", "", "path to a Linux fullsend binary to copy into the sandbox (default: current executable)")
	cmd.Flags().StringArrayVar(&envFiles, "env-file", nil, "load environment variables from a dotenv file (repeatable)")
	cmd.Flags().BoolVar(&noPostScript, "no-post-script", false, "skip post-script execution (agent still runs full inference)")
	cmd.Flags().BoolVar(&keepSandbox, "keep-sandbox", false, "skip sandbox deletion after the run (useful for post-failure inspection)")
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
	harnessPath, err := resolveHarnessPath(absFullsendDir, agentName, printer)
	if err != nil {
		return err
	}
	harnessStart := time.Now()
	printer.StepStart("Loading harness: " + harnessPath)

	forgePlatform, err := detectForgePlatform(forgeFlag)
	if err != nil {
		printer.StepFail("Invalid --forge flag")
		return err
	}

	policy := fetch.DefaultPolicy
	policy.Offline = rFlags.offline

	// Best-effort org config loading — provides the allowlist for base
	// harness fetching. If the file is missing or unparseable we proceed
	// without it; HasURLReferences will enforce its presence later if needed.
	orgConfigPath := filepath.Join(absFullsendDir, "config.yaml")
	orgCfg := tryLoadOrgConfig(orgConfigPath, printer)
	var orgAllowlist []string
	if orgCfg != nil {
		orgAllowlist = orgCfg.AllowedRemoteResources
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
			orgAllowlist = orgCfg.AllowedRemoteResources
		}
	}

	h, baseDeps, err := harness.LoadWithBase(ctx, harnessPath, harness.ComposeOpts{
		WorkspaceRoot: absFullsendDir,
		FetchPolicy:   policy,
		AuditLogPath:  filepath.Join(absFullsendDir, ".fullsend-cache", "fetch-audit.jsonl"),
		ForgePlatform: forgePlatform,
		OrgAllowlist:  orgAllowlist,
	})
	if err != nil {
		printer.StepFail("Failed to load harness")
		return fmt.Errorf("loading harness: %w", err)
	}

	for _, dep := range baseDeps {
		if dep.CacheHit {
			printer.StepInfo(fmt.Sprintf("Base: %s (cache hit)", dep.URL))
		} else {
			printer.StepInfo(fmt.Sprintf("Base: %s (fetched)", dep.URL))
		}
	}

	if err := h.ResolveRelativeTo(absFullsendDir); err != nil {
		printer.StepFail("Path validation failed")
		return fmt.Errorf("resolving paths: %w", err)
	}

	if h.HasURLReferences() {
		if orgCfg == nil {
			var err error
			orgCfg, err = requireOrgConfig(orgConfigPath, printer)
			if err != nil {
				return err
			}
		}

		if err := h.ValidateAllowedRemoteResources(orgCfg.AllowedRemoteResources); err != nil {
			printer.StepFail("Remote resource allowlist validation failed")
			return fmt.Errorf("validating allowed remote resources: %w", err)
		}

		// Check for a lock file with a current entry for this harness.
		var deps []resolve.Dependency
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
					lockDeps, lockResolveErr := resolveFromLock(h, entry, absFullsendDir, printer)
					if lockResolveErr != nil {
						printer.StepFail("Lock file resolution failed: " + lockResolveErr.Error())
						printer.StepWarn("Falling back to normal resolution")
					} else {
						deps = lockDeps
						usedLock = true
						printer.StepDone(fmt.Sprintf("Resolved %d dependencies from lock file", len(deps)))
					}
				}
			}
		}

		if !usedLock {
			var forgeClient forge.Client
			if h.HasURLSkills() {
				if rFlags.forgeClient != nil {
					forgeClient = rFlags.forgeClient
				} else {
					token, tokenErr := resolveToken()
					if tokenErr != nil {
						printer.StepFail("Skill URLs require a GitHub token (set GH_TOKEN, GITHUB_TOKEN, or run 'gh auth login')")
						return fmt.Errorf("skill URLs require a GitHub token: %w", tokenErr)
					}
					forgeClient = gh.New(token)
				}
			}

			var resolveErr error
			deps, resolveErr = resolve.ResolveHarness(ctx, h, resolve.ResolveOpts{
				WorkspaceRoot: absFullsendDir,
				FetchPolicy:   policy,
				AuditLogPath:  filepath.Join(absFullsendDir, ".fullsend-cache", "fetch-audit.jsonl"),
				MaxDepth:      rFlags.maxDepth,
				MaxResources:  rFlags.maxResources,
				ForgeClient:   forgeClient,
			})
			if resolveErr != nil {
				printer.StepFail("Remote resource resolution failed")
				return fmt.Errorf("resolving remote resources: %w", resolveErr)
			}
		}

		for _, dep := range deps {
			if dep.CacheHit {
				printer.StepInfo(fmt.Sprintf("Resolved %s (cache hit)", dep.URL))
			} else {
				printer.StepInfo(fmt.Sprintf("Fetched %s -> %s", dep.URL, dep.LocalPath))
			}
		}
	}

	if resolved, overridden := applySandboxImageOverride(h.Image); overridden {
		printer.StepInfo(fmt.Sprintf("Image override via FULLSEND_SANDBOX_IMAGE: %s -> %s", h.Image, resolved))
		h.Image = resolved
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

	// Run lint checks and report any diagnostics (non-fatal).
	for _, diag := range h.Lint() {
		emitDiagnostic(printer, diag)
	}

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
		notifier, notifyErr := setupStatusNotifier(absFullsendDir, agentName, sOpts, printer)
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

	// 2b. Ensure providers exist on the gateway (if any declared).
	if len(h.Providers) > 0 {
		providersDir := filepath.Join(absFullsendDir, "providers")
		providerDefs, err := harness.LoadProviderDefs(providersDir)
		if err != nil {
			printer.StepFail("Failed to load provider definitions")
			return fmt.Errorf("loading provider definitions: %w", err)
		}
		for _, pd := range providerDefs {
			providerStart := time.Now()
			printer.StepStart("Ensuring provider: " + pd.Name)
			if err := sandbox.EnsureProvider(pd.Name, pd.Type, pd.Credentials, pd.Config); err != nil {
				printer.StepFail("Failed to create provider " + pd.Name)
				return fmt.Errorf("ensuring provider %q: %w", pd.Name, err)
			}
			printer.StepDone(fmt.Sprintf("Provider ready: %s (%.1fs)", pd.Name, time.Since(providerStart).Seconds()))
		}
	}

	// 2c. Run pre-script on the host (if configured).
	if h.PreScript != "" {
		preStart := time.Now()
		printer.StepStart("Running pre-script: " + h.PreScript)
		preCmd := exec.Command(h.PreScript)
		preCmd.Env = append(os.Environ(), envToList(h.RunnerEnv)...)
		preCmd.Stdout = os.Stdout
		preCmd.Stderr = os.Stderr
		if err := preCmd.Run(); err != nil {
			printer.StepFail("Pre-script failed")
			return fmt.Errorf("running pre-script: %w", err)
		}
		printer.StepDone(fmt.Sprintf("Pre-script completed (%.1fs)", time.Since(preStart).Seconds()))
	}

	// 3. Create sandbox.
	sandboxName := fmt.Sprintf("agent-%s-%d-%d", agentName, os.Getpid(), time.Now().Unix())
	createStart := time.Now()
	printer.StepStart("Creating sandbox: " + sandboxName)

	readyTimeout := time.Duration(h.SandboxTimeoutSeconds) * time.Second
	if err := sandbox.CreateWithRetry(sandboxName, h.Providers, h.Image, h.Policy, sandbox.DefaultMaxCreateAttempts, readyTimeout); err != nil {
		printer.StepFail("Failed to create sandbox")
		return fmt.Errorf("creating sandbox: %w", err)
	}
	if outputBase == "" {
		outputBase = filepath.Join(os.TempDir(), "fullsend")
	}
	runDir := filepath.Join(outputBase, sandboxName)

	// validationPassed is declared here (before the post-script defer) so the
	// defer closure can guard on it. The post-script must only run when
	// validation has passed — running it on unvalidated output would violate
	// ADR 0022's zero-trust model.
	var validationPassed bool

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
			postStart := time.Now()
			printer.StepStart("Running post-script: " + h.PostScript)
			postCmd := exec.Command(h.PostScript)
			postCmd.Dir = runDir
			postCmd.Env = append(os.Environ(), envToList(h.RunnerEnv)...)
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

	// 4. Resolve target repo path (needed by bootstrap for env vars).
	hostRepositoryDir, err := filepath.Abs(targetRepo)
	if err != nil {
		return fmt.Errorf("resolving target repo path: %w", err)
	}
	repoName := filepath.Base(hostRepositoryDir)
	remoteRepositoryDir := fmt.Sprintf("%s/%s", sandbox.SandboxWorkspace, repoName)

	// 5. Generate trace ID for security finding and audit log correlation.
	traceID := security.GenerateTraceID()

	// 6. Start runtime fetch service (Phase 4, ADR-0038).
	var fetchEnvVal fetchServiceEnv
	startFetch, deprecationWarning := shouldStartFetchService(h)
	if deprecationWarning != "" {
		printer.StepWarn(deprecationWarning)
	}
	if startFetch {
		env, fetchShutdown, fetchErr := setupFetchService(ctx, rFlags.forgeClient, h, resolveToken, fetchsvc.ServiceConfig{
			Harness:       h,
			FetchPolicy:   fetch.DefaultPolicy,
			WorkspaceRoot: absFullsendDir,
			AuditLogPath:  filepath.Join(absFullsendDir, ".fullsend-cache", "fetch-audit.jsonl"),
			TraceID:       traceID,
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
	backend := agentruntime.Default()
	rt := backend.Runtime
	tx := backend.Transcripts
	bootstrapStart := time.Now()
	printer.StepStart("Bootstrapping sandbox")
	boot := newHarnessBootstrap(h, sandboxName)
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

	// 8c. Host-side scan (Path A): scan the target repo's context files
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
	printer.KeyValue("Trace ID", traceID)
	if err := injectTraceID(sandboxName, traceID); err != nil {
		printer.StepWarn("Could not inject trace ID into sandbox: " + err.Error())
	}

	// 9b. Pre-agent security scan (sandbox-internal, Path B).
	// Scans context files (CLAUDE.md, AGENTS.md, .cursorrules, agent defs,
	// SKILL.md) that were just copied into the sandbox.
	if h.SecurityEnabled() {
		printer.StepStart("Running pre-agent security scan")
		scanCmd := buildScanContextCommand(remoteRepositoryDir, traceID)
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

	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return fmt.Errorf("creating run directory: %w", err)
	}

	oidcCtx, oidcCancel := context.WithCancel(context.Background())
	var oidcWg sync.WaitGroup
	if oidcURL := os.Getenv("FULLSEND_GCP_OIDC_URL"); oidcURL != "" {
		oidcAuth, err := readOIDCAuthFile(os.Getenv("FULLSEND_GCP_OIDC_AUTH_FILE"))
		if err != nil {
			printer.StepWarn("OIDC token refresh disabled: " + err.Error())
		} else {
			printer.StepDone("OIDC token refresh enabled (WIF mode)")
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

	var lastExitCode int
	var runCount int

	for iteration := 1; iteration <= maxIterations; iteration++ {
		runCount = iteration

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

		var metrics agentruntime.RunMetrics
		exitCode, runErr := rt.Run(ctx, agentruntime.RunParams{
			SandboxName:   sandboxName,
			AgentBaseName: agentBaseName,
			Model:         h.Model,
			RepoDir:       remoteRepositoryDir,
			PluginDirs:    pluginDirs,
			Debug:         debug,
			Timeout:       timeout,
			OutputPath:    filepath.Join(iterDir, "output.jsonl"),
		}, printer, agentStart, &metrics)
		close(heartbeatDone)

		if runErr != nil {
			printer.StepFail("Agent execution failed")
			return fmt.Errorf("running agent (iteration %d): %w", iteration, runErr)
		}
		lastExitCode = exitCode

		printer.Blank()
		// Non-zero exit is a warning, not a failure — the validation loop is the success gate.
		if exitCode == 0 {
			printer.StepDone(fmt.Sprintf("Agent exited with code %d (%.1fs)", exitCode, time.Since(agentStart).Seconds()))
		} else {
			printer.StepWarn(fmt.Sprintf("Agent exited with code %d", exitCode))
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
		if clearErr := os.RemoveAll(hostRepositoryDir); clearErr != nil {
			return fmt.Errorf("clearing local repo %s before extraction: %w", hostRepositoryDir, clearErr)
		}
		repoExtractStart := time.Now()
		printer.StepStart("Extracting target repo")
		if err := sandbox.SafeDownload(sandboxName, remoteRepositoryDir, hostRepositoryDir); err != nil {
			if es := tx.ParseTranscriptErrors(iterTranscriptDir); len(es) > 0 {
				tx.EmitTranscriptErrors(os.Stderr, es)
			}
			return fmt.Errorf("extracting target repo (iteration %d): %w", iteration, err)
		}
		printer.StepDone(fmt.Sprintf("Target repo extracted to %s (%.1fs)", hostRepositoryDir, time.Since(repoExtractStart).Seconds()))

		// 9e. Run validation.
		if h.ValidationLoop == nil {
			break
		}

		valStart := time.Now()
		printer.StepStart("Running validation: " + h.ValidationLoop.Script)
		valCmd := exec.Command(h.ValidationLoop.Script)
		valCmd.Dir = iterDir
		valCmd.Env = append(os.Environ(),
			append(envToList(h.RunnerEnv),
				fmt.Sprintf("TARGET_REPO_DIR=%s", hostRepositoryDir),
				fmt.Sprintf("FULLSEND_RUN_DIR=%s", runDir),
			)...,
		)
		valOut, valErr := valCmd.CombinedOutput()

		if valErr == nil {
			printer.StepDone(fmt.Sprintf("Validation passed: %s (%.1fs)", strings.TrimSpace(string(valOut)), time.Since(valStart).Seconds()))
			validationPassed = true
			break
		}

		printer.StepFail("Validation failed: " + validationFailMessage(valOut, valErr))
		if iteration < maxIterations {
			printer.StepInfo(fmt.Sprintf("Will retry (%d iterations remaining)", maxIterations-iteration))
		}
	}

	// 9e-bis. Surface transcript errors in workflow logs (GitHub Actions).
	// When the agent exits non-zero, parse transcript JSONL files and emit
	// ::error:: annotations so operators can diagnose failures without
	// downloading artifacts. See #704.
	if lastExitCode != 0 {
		lastIterDir := filepath.Join(runDir, fmt.Sprintf("iteration-%d", runCount))
		lastTranscriptDir := filepath.Join(lastIterDir, "transcripts")
		if errorSummaries := tx.ParseTranscriptErrors(lastTranscriptDir); len(errorSummaries) > 0 {
			printer.StepWarn(fmt.Sprintf("Found %d transcript error(s) — emitting to workflow log", len(errorSummaries)))
			tx.EmitTranscriptErrors(os.Stderr, errorSummaries)
		}
	}

	// 9f. Post-agent output scan — redact secrets from extracted output.
	if h.SecurityEnabled() {
		printer.StepStart("Running post-agent output scan")
		if err := scanOutputFiles(runDir, traceID, printer); err != nil {
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
	printer.KeyValue("Agent exit code", fmt.Sprintf("%d", lastExitCode))
	printer.KeyValue("Agent runs", fmt.Sprintf("%d", runCount))
	printer.KeyValue("Trace ID", traceID)
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

// setupFetchService resolves a forge client for runtime fetching and starts
// the HTTP fetch service. It returns the service address/token as a
// fetchServiceEnv, a shutdown function, and any error.
func setupFetchService(ctx context.Context, forgeClient forge.Client, h *harness.Harness, resolveToken func() (string, error), cfg fetchsvc.ServiceConfig, warn func(string)) (fetchServiceEnv, func(), error) {
	if forgeClient != nil {
		cfg.ForgeClient = forgeClient
	} else if h.HasURLSkills() || h.AllowRuntimeFetch || len(h.AllowedRemoteResources) > 0 {
		if token, err := resolveToken(); err == nil {
			cfg.ForgeClient = gh.New(token)
		} else {
			warn(fmt.Sprintf("Forge token unavailable, runtime fetches for uncached skills will fail: %v", err))
		}
	}

	addr, token, shutdown, err := startFetchService(ctx, cfg)
	if err != nil {
		return fetchServiceEnv{}, nil, err
	}
	return fetchServiceEnv{addr: addr, token: token}, shutdown, nil
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
	remoteSchemaPath := sandbox.SandboxWorkspace + "/.fullsend/output-schema.json"
	if schemaHost, ok := h.RunnerEnv["FULLSEND_OUTPUT_SCHEMA"]; ok && schemaHost != "" {
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

// envToList converts a map of env vars to a sorted list of KEY=VALUE strings.
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
	// Defense-in-depth: validate traceID before shell interpolation even though
	// GenerateTraceID() only produces safe hex characters.
	if !security.IsValidTraceID(traceID) {
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
	if !security.IsValidTraceID(traceID) {
		return fmt.Errorf("invalid trace ID format: %q", traceID)
	}
	// Safe: IsValidTraceID() above ensures traceID matches UUID v4 format only.
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

func setupStatusNotifier(fullsendDir string, agentName string, sOpts statusOpts, printer *ui.Printer) (*statuscomment.Notifier, error) {
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
	if data, err := os.ReadFile(orgConfigPath); err == nil {
		orgCfg, parseErr := config.ParseOrgConfig(data)
		if parseErr != nil {
			printer.StepWarn("Failed to parse config.yaml for status notifications: " + parseErr.Error())
		} else if orgCfg.Defaults.StatusNotifications != nil {
			notifyCfg = *orgCfg.Defaults.StatusNotifications
		}
	} else if !os.IsNotExist(err) {
		printer.StepWarn("Failed to read config.yaml for status notifications: " + err.Error())
	}

	sha := os.Getenv("GITHUB_SHA")
	// In cross-repo workflow_dispatch mode, GITHUB_SHA is the dispatching
	// repo's default branch HEAD — not the PR's head commit. Prefer the
	// PR head SHA from the event payload when available. See #2045.
	if prSHA := prHeadSHAFromEventPath(os.Getenv("GITHUB_EVENT_PATH")); prSHA != "" {
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

	role := resolveRole(agentName)
	n.SetClientFactory(func(ctx context.Context) (forge.Client, error) {
		result, err := statusMintToken(ctx, mintclient.MintRequest{
			MintURL: mintURL,
			Role:    role,
			Repos:   []string{repo},
		})
		if err != nil {
			return nil, fmt.Errorf("minting status token: %w", err)
		}
		if os.Getenv("GITHUB_ACTIONS") == "true" && mintTokenPattern.MatchString(result.Token) {
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
