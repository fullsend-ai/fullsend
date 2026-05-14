package cli

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
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

	"github.com/fullsend-ai/fullsend/internal/envfile"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newRunCmd() *cobra.Command {
	var fullsendDir string
	var outputBase string
	var targetRepo string
	var fullsendBinary string
	var envFiles []string
	var noPostScript bool

	cmd := &cobra.Command{
		Use:   "run <agent-name>",
		Short: "Run an agent",
		Long:  "Execute an agent by name: read its harness YAML, set up the sandbox, and run the agent.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentName := args[0]
			printer := ui.New(os.Stdout)
			return runAgent(agentName, fullsendDir, outputBase, targetRepo, fullsendBinary, envFiles, noPostScript, printer)
		},
	}

	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "base directory containing the .fullsend layout")
	cmd.Flags().StringVar(&outputBase, "output-dir", "", "base directory for run output (default: /tmp/fullsend)")
	cmd.Flags().StringVar(&targetRepo, "target-repo", "", "path to the target repository")
	cmd.Flags().StringVar(&fullsendBinary, "fullsend-binary", "", "path to a Linux fullsend binary to copy into the sandbox (default: current executable)")
	cmd.Flags().StringArrayVar(&envFiles, "env-file", nil, "load environment variables from a dotenv file (repeatable)")
	cmd.Flags().BoolVar(&noPostScript, "no-post-script", false, "skip post-script execution (agent still runs full inference)")
	_ = cmd.MarkFlagRequired("fullsend-dir")
	_ = cmd.MarkFlagRequired("target-repo")

	return cmd
}

func runAgent(agentName, fullsendDir, outputBase, targetRepo, fullsendBinary string, envFiles []string, noPostScript bool, printer *ui.Printer) (runErr error) {
	printer.Banner()
	printer.Blank()
	printer.Header("Running agent: " + agentName)
	printer.Blank()

	// 0. Load env files before anything else so vars are available for harness expansion.
	for _, ef := range envFiles {
		if err := envfile.Load(ef); err != nil {
			return fmt.Errorf("loading env file %s: %w", ef, err)
		}
	}

	// 1. Resolve and load harness.
	harnessPath := filepath.Join(fullsendDir, "harness", agentName+".yaml")
	harnessStart := time.Now()
	printer.StepStart("Loading harness: " + harnessPath)

	h, err := harness.Load(harnessPath)
	if err != nil {
		printer.StepFail("Failed to load harness")
		return fmt.Errorf("loading harness: %w", err)
	}

	absFullsendDir, err := filepath.Abs(fullsendDir)
	if err != nil {
		return fmt.Errorf("resolving fullsend dir: %w", err)
	}
	if err := h.ResolveRelativeTo(absFullsendDir); err != nil {
		printer.StepFail("Path validation failed")
		return fmt.Errorf("resolving paths: %w", err)
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
	if err := h.ValidateRunnerEnvWith(expander); err != nil {
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

	// Print plan.
	printer.KeyValue("Agent", h.Agent)
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

	// 2. Check openshell availability.
	openshellStart := time.Now()
	printer.StepStart("Checking openshell availability")
	if err := sandbox.EnsureAvailable(); err != nil {
		printer.StepFail("openshell not available")
		return fmt.Errorf("openshell is required: %w", err)
	}
	printer.StepDone(fmt.Sprintf("openshell available (%.1fs)", time.Since(openshellStart).Seconds()))

	// 2a. Ensure a gateway is running.
	gatewayStart := time.Now()
	printer.StepStart("Ensuring gateway")
	if err := sandbox.EnsureGateway(); err != nil {
		printer.StepFail("Failed to start gateway")
		return fmt.Errorf("starting gateway: %w", err)
	}
	printer.StepDone(fmt.Sprintf("Gateway ready (%.1fs)", time.Since(gatewayStart).Seconds()))

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

	if err := sandbox.Create(sandboxName, h.Providers, h.Image, h.Policy); err != nil {
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
	repoSrc, err := filepath.Abs(targetRepo)
	if err != nil {
		return fmt.Errorf("resolving target repo path: %w", err)
	}
	repoName := filepath.Base(repoSrc)
	repoDir := fmt.Sprintf("%s/%s", sandbox.SandboxWorkspace, repoName)

	// 7. Bootstrap sandbox.
	bootstrapStart := time.Now()
	printer.StepStart("Bootstrapping sandbox")
	if err := bootstrapSandbox(sandboxName, repoDir, fullsendBinary, h); err != nil {
		printer.StepFail("Failed to bootstrap sandbox")
		return err
	}
	printer.StepDone(fmt.Sprintf("Sandbox bootstrapped (%.1fs)", time.Since(bootstrapStart).Seconds()))

	// 8. Make project code available (copy repo root into a named subdirectory).
	copyStart := time.Now()
	printer.StepStart("Copying project code into sandbox")
	mkRepoCmd := fmt.Sprintf("mkdir -p %s", repoDir)
	if _, _, _, err := sandbox.Exec(sandboxName, mkRepoCmd, 10*time.Second); err != nil {
		return fmt.Errorf("creating repo dir in sandbox: %w", err)
	}
	if err := sandbox.Upload(sandboxName, repoSrc+"/.", repoDir+"/"); err != nil {
		printer.StepFail("Failed to copy project code")
		return fmt.Errorf("copying project code: %w", err)
	}
	printer.StepDone(fmt.Sprintf("Project code copied to %s/ (%.1fs)", repoName, time.Since(copyStart).Seconds()))

	// 8a. Inject org-level AGENTS.md if the target repo does not have one.
	// The scaffold ships a default AGENTS.md with baseline behavioral
	// guidelines. Skills already instruct agents to read AGENTS.md from
	// the project root — this ensures there is something to read even
	// when the target repo has not authored its own.
	if !hasAgentsMD(repoSrc) {
		orgAgentsMD := filepath.Join(absFullsendDir, "AGENTS.md")
		if _, err := os.Stat(orgAgentsMD); err == nil {
			if err := sandbox.Upload(sandboxName, orgAgentsMD, repoDir+"/AGENTS.md"); err != nil {
				printer.StepWarn("Could not inject org AGENTS.md: " + err.Error())
			} else {
				// Hide the injected file from git status so agents don't stage it.
				excludeCmd := fmt.Sprintf("echo 'AGENTS.md' >> %s/.git/info/exclude", repoDir)
				if _, _, _, err := sandbox.Exec(sandboxName, excludeCmd, 5*time.Second); err != nil {
					printer.StepWarn("Could not add AGENTS.md to git exclude: " + err.Error())
				}
				printer.StepDone("Injected org-level AGENTS.md (target repo has none)")
			}
		}
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
		findings := scanRepoContextFiles(repoSrc)
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

	// 9a. Generate trace ID for security finding correlation.
	traceID := security.GenerateTraceID()
	printer.KeyValue("Trace ID", traceID)
	if err := injectTraceID(sandboxName, traceID); err != nil {
		printer.StepWarn("Could not inject trace ID into sandbox: " + err.Error())
	}

	// 9b. Pre-agent security scan (sandbox-internal, Path B).
	// Scans context files (CLAUDE.md, AGENTS.md, .cursorrules, agent defs,
	// SKILL.md) that were just copied into the sandbox.
	if h.SecurityEnabled() {
		printer.StepStart("Running pre-agent security scan")
		scanCmd := buildScanContextCommand(repoDir, traceID)
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

	// 9c. Run agent with validation loop.
	agentBaseName := strings.TrimSuffix(filepath.Base(h.Agent), ".md")
	var pluginDirs []string
	for _, p := range h.Plugins {
		pluginDirs = append(pluginDirs, fmt.Sprintf("%s/plugins/%s", sandbox.SandboxClaudeConfig, filepath.Base(p)))
	}
	claudeCmd := buildClaudeCommand(agentBaseName, h.Model, repoDir, pluginDirs)

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
			clearCmd := fmt.Sprintf("rm -rf %s/output/* %s/*.jsonl",
				sandbox.SandboxWorkspace, sandbox.SandboxClaudeConfig)
			if _, _, _, clearErr := sandbox.Exec(sandboxName, clearCmd, 10*time.Second); clearErr != nil {
				printer.StepWarn("Failed to clear sandbox output: " + clearErr.Error())
			}
		}

		// 9a. Run agent.
		printer.StepStart("Running agent")
		printer.Blank()

		agentStart := time.Now()
		heartbeatDone := make(chan struct{})
		go runHeartbeat(printer, agentStart, timeout, heartbeatDone)

		var metrics RunMetrics
		exitCode, runErr := runAgentWithProgress(sandboxName, claudeCmd, timeout, printer, agentStart, &metrics)
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
		if err := sandbox.ExtractTranscripts(sandboxName, agentName, iterTranscriptDir); err != nil {
			printer.StepWarn("Failed to extract transcripts: " + err.Error())
		} else {
			printer.StepDone(fmt.Sprintf("Transcripts extracted (%.1fs)", time.Since(transcriptStart).Seconds()))
		}

		// 9d. Extract target repo back to host. SafeDownload removes symlinks
		// and .git/hooks/ after download to prevent sandbox escape.
		repoExtractStart := time.Now()
		printer.StepStart("Extracting target repo")
		if err := sandbox.SafeDownload(sandboxName, repoDir, repoSrc); err != nil {
			printer.StepWarn("Failed to extract target repo: " + err.Error())
		} else {
			printer.StepDone(fmt.Sprintf("Target repo extracted to %s (%.1fs)", repoSrc, time.Since(repoExtractStart).Seconds()))
		}

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
				fmt.Sprintf("TARGET_REPO_DIR=%s", repoSrc),
				fmt.Sprintf("FULLSEND_RUN_DIR=%s", runDir),
			)...,
		)
		valOut, valErr := valCmd.CombinedOutput()

		if valErr == nil {
			printer.StepDone(fmt.Sprintf("Validation passed: %s (%.1fs)", strings.TrimSpace(string(valOut)), time.Since(valStart).Seconds()))
			validationPassed = true
			break
		}

		printer.StepFail("Validation failed: " + strings.TrimSpace(string(valOut)))
		if iteration < maxIterations {
			printer.StepInfo(fmt.Sprintf("Will retry (%d iterations remaining)", maxIterations-iteration))
		}
	}

	// 9e. Post-agent output scan — redact secrets from extracted output.
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

func bootstrapSandbox(sandboxName, repoDir, fullsendBinary string, h *harness.Harness) error {
	// Create workspace structure and Claude config dir for transcripts.
	// Agent and skill definitions go in CLAUDE_CONFIG_DIR so `claude --agent`
	// finds them regardless of the repo's own .claude/ directory. When
	// CLAUDE_CONFIG_DIR is set, Claude uses it instead of ~/.claude/.
	mkdirCmd := fmt.Sprintf("mkdir -p %s/agents %s/skills %s/hooks %s/plugins %s/bin %s/.env.d %s/.security %s %s/.claude/hooks",
		sandbox.SandboxClaudeConfig, sandbox.SandboxClaudeConfig, sandbox.SandboxClaudeConfig, sandbox.SandboxClaudeConfig, sandbox.SandboxWorkspace, sandbox.SandboxWorkspace, sandbox.SandboxWorkspace, sandbox.SandboxClaudeConfig, sandbox.SandboxWorkspace)
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
			dir, binPath, err := resolveLinuxBinary(targetArch)
			if err != nil {
				if h.FailModeClosed() {
					return fmt.Errorf("could not obtain linux/%s binary for security scan (fail_mode: closed): %w\nUse --fullsend-binary to provide a pre-built Linux binary", targetArch, err)
				}
				fmt.Fprintf(os.Stderr, "WARNING: could not obtain linux/%s binary: %v\n", targetArch, err)
				fmt.Fprintf(os.Stderr, "WARNING: skipping sandbox-side security scan (fail_mode: open). Use --fullsend-binary to provide a pre-built Linux binary.\n")
				localBinary = ""
			} else {
				tmpBinaryDir = dir
				localBinary = binPath
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
		if err := validateLinuxBinary(localBinary); err != nil {
			return fmt.Errorf("fullsend binary %q is not valid for the sandbox: %w", localBinary, err)
		}
		remoteBinary := fmt.Sprintf("%s/bin/fullsend", sandbox.SandboxWorkspace)
		if err := sandbox.Upload(sandboxName, localBinary, remoteBinary); err != nil {
			return fmt.Errorf("copying fullsend binary to sandbox: %w", err)
		}
		chmodCmd := fmt.Sprintf("chmod +x %s", remoteBinary)
		if _, _, _, err := sandbox.Exec(sandboxName, chmodCmd, 10*time.Second); err != nil {
			return fmt.Errorf("chmod fullsend binary: %w", err)
		}
	}

	// Host-side scan (Path A): check agent definition and skills for injection
	// before copying into sandbox. Complements the in-sandbox scan (Path B).
	// Uses stderr (not printer) because bootstrapSandbox has no printer param.
	var scanPipeline *security.Pipeline
	if h.SecurityEnabled() {
		scanPipeline = security.InputPipeline()
	}

	if scanPipeline != nil {
		content, err := os.ReadFile(h.Agent)
		if err != nil {
			if h.FailModeClosed() {
				return fmt.Errorf("cannot scan agent definition %q: %w", h.Agent, err)
			}
			fmt.Fprintf(os.Stderr, "WARNING: could not read agent definition %q for scan: %v\n", h.Agent, err)
		} else {
			result := scanPipeline.Scan(string(content))
			if security.HasCriticalFindings(result.Findings) {
				if h.FailModeClosed() {
					return fmt.Errorf("agent definition %q blocked: critical injection findings", h.Agent)
				}
				fmt.Fprintf(os.Stderr, "WARNING: agent definition %q has critical injection findings (fail_mode: open)\n", h.Agent)
			} else if len(result.Findings) > 0 {
				fmt.Fprintf(os.Stderr, "WARNING: agent definition %q has %d injection finding(s)\n", h.Agent, len(result.Findings))
			}
		}
	}

	// Copy agent definition to $CLAUDE_CONFIG_DIR/agents/.
	if err := sandbox.Upload(sandboxName, h.Agent,
		fmt.Sprintf("%s/agents/", sandbox.SandboxClaudeConfig)); err != nil {
		return fmt.Errorf("copying agent definition: %w", err)
	}

	// Copy skills (Upload copies the entire directory tree, including any
	// scripts/, references/, and assets/ bundled with the skill per the
	// agentskills.io specification).
	for _, skillPath := range h.Skills {
		if scanPipeline != nil {
			// Try common casings — Linux filesystems are case-sensitive.
			// Keep in sync with security.ScannableFiles["skill.md"].
			var skillContent []byte
			for _, name := range []string{"SKILL.md", "skill.md", "Skill.md"} {
				if c, err := os.ReadFile(filepath.Join(skillPath, name)); err == nil {
					skillContent = c
					break
				}
			}
			if skillContent == nil {
				// No SKILL.md found in any casing — not an error, skill may
				// use scripts only. But in fail-closed, warn about unscanned skill.
				if h.FailModeClosed() {
					fmt.Fprintf(os.Stderr, "WARNING: skill %q has no SKILL.md to scan\n", skillPath)
				}
			} else {
				result := scanPipeline.Scan(string(skillContent))
				if security.HasCriticalFindings(result.Findings) {
					if h.FailModeClosed() {
						return fmt.Errorf("skill %q blocked: critical injection findings in SKILL.md", skillPath)
					}
					fmt.Fprintf(os.Stderr, "WARNING: skill %q has critical injection findings (fail_mode: open)\n", skillPath)
				} else if len(result.Findings) > 0 {
					fmt.Fprintf(os.Stderr, "WARNING: skill %q has %d injection finding(s)\n", skillPath, len(result.Findings))
				}
			}
		}

		if err := sandbox.Upload(sandboxName, skillPath,
			fmt.Sprintf("%s/skills/", sandbox.SandboxClaudeConfig)); err != nil {
			return fmt.Errorf("copying skill %q: %w", skillPath, err)
		}
	}

	// Scan plugin definitions for injection before copying into sandbox.
	if scanPipeline != nil {
		for _, pluginPath := range h.Plugins {
			for _, name := range []string{"plugin.json", ".lsp.json"} {
				content, err := os.ReadFile(filepath.Join(pluginPath, name))
				if err != nil {
					continue
				}
				result := scanPipeline.Scan(string(content))
				if security.HasCriticalFindings(result.Findings) {
					if h.FailModeClosed() {
						return fmt.Errorf("plugin %q blocked: critical injection findings in %s", pluginPath, name)
					}
					fmt.Fprintf(os.Stderr, "WARNING: plugin %q has critical injection findings in %s (fail_mode: open)\n", pluginPath, name)
				} else if len(result.Findings) > 0 {
					fmt.Fprintf(os.Stderr, "WARNING: plugin %q has %d injection finding(s) in %s\n", pluginPath, len(result.Findings), name)
				}
			}
		}
	}

	// Install plugins as marketplace-cached plugins so Claude Code registers
	// the LSP tool.
	if len(h.Plugins) > 0 {
		if err := bootstrapPlugins(sandboxName, h.Plugins); err != nil {
			return fmt.Errorf("bootstrapping plugins: %w", err)
		}
	}

	// Write .env file (infrastructure vars) and copy host files.
	if err := bootstrapEnv(sandboxName, repoDir, h); err != nil {
		return fmt.Errorf("bootstrapping environment: %w", err)
	}

	// Install security hooks if enabled.
	if h.SecurityEnabled() {
		if err := bootstrapSecurityHooks(sandboxName, h); err != nil {
			return fmt.Errorf("bootstrapping security hooks: %w", err)
		}
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
func bootstrapEnv(sandboxName, repoDir string, h *harness.Harness) error {
	remoteEnvFile := sandbox.SandboxWorkspace + "/.env"
	outputDir := sandbox.SandboxWorkspace + "/output"

	var lines []string

	// Infrastructure vars.
	pathExport := fmt.Sprintf("export PATH=%s/bin", sandbox.SandboxWorkspace)
	if len(h.Plugins) > 0 {
		pathExport += ":/usr/local/go/bin"
	}
	pathExport += ":$PATH"
	lines = append(lines, pathExport)
	lines = append(lines, fmt.Sprintf("export CLAUDE_CONFIG_DIR=%s", sandbox.SandboxClaudeConfig))
	lines = append(lines, fmt.Sprintf("export FULLSEND_OUTPUT_DIR=%s", outputDir))
	lines = append(lines, fmt.Sprintf("export FULLSEND_TARGET_REPO_DIR=%s", repoDir))

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

	if err := sandbox.Upload(sandboxName, tmpFile.Name(), remoteEnvFile); err != nil {
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
			raw, err := os.ReadFile(hostPath)
			if err != nil {
				return fmt.Errorf("reading host file %s for expansion: %w", hf.Src, err)
			}
			expanded := os.ExpandEnv(string(raw))

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

			if err := sandbox.Upload(sandboxName, tmp.Name(), hf.Dest); err != nil {
				os.Remove(tmp.Name())
				return fmt.Errorf("copying expanded file %s to %s: %w", hf.Src, hf.Dest, err)
			}
			os.Remove(tmp.Name())
		} else {
			if err := sandbox.Upload(sandboxName, hostPath, hf.Dest); err != nil {
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

func runAgentWithProgress(sandboxName, claudeCmd string, timeout time.Duration, printer *ui.Printer, start time.Time, metrics *RunMetrics) (int, error) {
	stdout, cmd, cancel, err := sandbox.ExecStreamReader(sandboxName, claudeCmd, timeout, os.Stderr)
	if err != nil {
		return -1, err
	}
	defer cancel()

	if parseErr := progressParser(stdout, printer, start, metrics); parseErr != nil {
		fmt.Fprintf(os.Stderr, "  progress parser: %v\n", sanitizeOutput(parseErr.Error()))
		cancel()
		io.Copy(io.Discard, stdout)
	}

	waitErr := cmd.Wait()
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	if waitErr != nil && cmd.ProcessState == nil {
		return exitCode, fmt.Errorf("openshell exec failed: %w", waitErr)
	}

	return exitCode, nil
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

func refreshOIDCToken(ctx context.Context, sandboxName, oidcURL, oidcAuth string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", oidcURL, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", oidcAuth)

	resp, err := httpClient.Do(req)
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
	if err := sandbox.Upload(sandboxName, tmpFile.Name(), remotePath); err != nil {
		return fmt.Errorf("copying token to sandbox: %w", err)
	}

	return nil
}

func buildClaudeCommand(agentName, model, repoDir string, pluginDirs []string) string {
	envFile := sandbox.SandboxWorkspace + "/.env"

	// Defense-in-depth: escape single quotes even though Validate() rejects them.
	safe := strings.ReplaceAll(agentName, "'", "'\\''")

	modelFlag := ""
	if model != "" {
		modelFlag = fmt.Sprintf("--model '%s' ", strings.ReplaceAll(model, "'", "'\\''"))
	}

	var pluginDirParts []string
	for _, pd := range pluginDirs {
		pluginDirParts = append(pluginDirParts, fmt.Sprintf("--plugin-dir '%s'", strings.ReplaceAll(pd, "'", "'\\''")))
	}
	pluginDirFlags := ""
	if len(pluginDirParts) > 0 {
		pluginDirFlags = strings.Join(pluginDirParts, " ") + " "
	}

	return fmt.Sprintf(
		// --verbose increases log output in the job log. If artifact upload is
		// added to this workflow, consider whether verbose output should be
		// redacted or made conditional via an env var.
		"cd %s && . %s && claude --print --verbose --output-format stream-json %s%s--agent '%s' --dangerously-skip-permissions 'Run the agent task'",
		repoDir, envFile, modelFlag, pluginDirFlags, safe,
	)
}

// maxContextScanDepth is the maximum directory depth for scanning context
// files. Shared between host-side (scanRepoContextFiles) and sandbox-side
// (buildScanContextCommand) scans to ensure parity.
const maxContextScanDepth = 5

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

	// Source .env to get PATH with /tmp/workspace/bin where fullsend is installed.
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

// scanOutputFiles runs the secret redactor on extracted output files,
// recursively walking all subdirectories (iteration-N/output/, etc.).
func scanOutputFiles(outputDir, traceID string, printer *ui.Printer) error {
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		printer.StepInfo("No output files to scan")
		return nil
	}

	redactor := security.NewSecretRedactor()
	redacted := 0
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

		result := redactor.Scan(string(content))
		if len(result.Findings) > 0 {
			redacted += len(result.Findings)
			relPath, _ := filepath.Rel(outputDir, path)
			for _, f := range result.Findings {
				printer.StepWarn(fmt.Sprintf("Redacted [%s] in %s: %s", f.Name, relPath, f.Detail))
				security.AppendFinding(findingsPath,
					security.TracedFinding{
						TraceID:   traceID,
						Timestamp: time.Now().UTC().Format(time.RFC3339),
						Phase:     "host_output",
						Finding:   f,
					})
			}
			if writeErr := os.WriteFile(path, []byte(result.Sanitized), 0o644); writeErr != nil {
				printer.StepWarn(fmt.Sprintf("Could not write redacted %s: %v", relPath, writeErr))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	if redacted > 0 {
		printer.StepWarn(fmt.Sprintf("Redacted %d secret(s) from output files", redacted))
	} else {
		printer.StepDone("Output files clean — no secrets found")
	}
	return nil
}

// bootstrapSecurityHooks installs Claude Code hook scripts and settings.json
// inside the sandbox. Hook scripts are embedded in the binary via go:embed.
func bootstrapSecurityHooks(sandboxName string, h *harness.Harness) error {
	// Write hook scripts.
	hookFiles := security.HookFiles(h)
	for name, content := range hookFiles {
		tmpFile, err := os.CreateTemp("", "fullsend-hook-*")
		if err != nil {
			return fmt.Errorf("creating temp file for hook %s: %w", name, err)
		}
		if _, err := tmpFile.Write(content); err != nil {
			tmpFile.Close()
			os.Remove(tmpFile.Name())
			return fmt.Errorf("writing hook %s: %w", name, err)
		}
		tmpFile.Close()

		remotePath := fmt.Sprintf("%s/.claude/hooks/%s", sandbox.SandboxWorkspace, name)
		if err := sandbox.Upload(sandboxName, tmpFile.Name(), remotePath); err != nil {
			os.Remove(tmpFile.Name())
			return fmt.Errorf("copying hook %s to sandbox: %w", name, err)
		}
		os.Remove(tmpFile.Name())

		// Make executable.
		chmodCmd := fmt.Sprintf("chmod +x %s", remotePath)
		if _, _, _, err := sandbox.Exec(sandboxName, chmodCmd, 10*time.Second); err != nil {
			return fmt.Errorf("chmod hook %s: %w", name, err)
		}
	}

	// Generate and install .claude/settings.json.
	settingsJSON, err := security.GenerateClaudeSettings(h)
	if err != nil {
		return fmt.Errorf("generating claude settings: %w", err)
	}

	tmpSettings, err := os.CreateTemp("", "fullsend-settings-*.json")
	if err != nil {
		return fmt.Errorf("creating temp settings file: %w", err)
	}
	if _, err := tmpSettings.Write(settingsJSON); err != nil {
		tmpSettings.Close()
		os.Remove(tmpSettings.Name())
		return fmt.Errorf("writing settings: %w", err)
	}
	tmpSettings.Close()

	remoteSettings := fmt.Sprintf("%s/.claude/settings.json", sandbox.SandboxWorkspace)
	if err := sandbox.Upload(sandboxName, tmpSettings.Name(), remoteSettings); err != nil {
		os.Remove(tmpSettings.Name())
		return fmt.Errorf("copying settings.json to sandbox: %w", err)
	}
	os.Remove(tmpSettings.Name())

	// Set Tirith env vars if configured.
	if h.Security != nil && h.Security.SandboxHooks != nil &&
		h.Security.SandboxHooks.Tirith != nil {
		tirithCfg := h.Security.SandboxHooks.Tirith

		if tirithCfg.FailOn != "" {
			// FailOn is validated by harness.validateSecurity() to be one of: critical, high, medium.
			// Quote the value defensively in case validation is ever relaxed.
			escapedFailOn := strings.ReplaceAll(tirithCfg.FailOn, "'", "'\\''")
			envCmd := fmt.Sprintf("echo 'export TIRITH_FAIL_ON=%s' >> %s/.env",
				escapedFailOn, sandbox.SandboxWorkspace)
			if _, _, _, err := sandbox.Exec(sandboxName, envCmd, 10*time.Second); err != nil {
				return fmt.Errorf("setting TIRITH_FAIL_ON: %w", err)
			}
		}

		// When tirith is enabled (default), mark it as required so the hook
		// fails closed if the binary is missing from the sandbox image.
		if harness.BoolDefault(tirithCfg.Enabled, true) {
			envCmd := fmt.Sprintf("echo 'export TIRITH_REQUIRED=1' >> %s/.env", sandbox.SandboxWorkspace)
			if _, _, _, err := sandbox.Exec(sandboxName, envCmd, 10*time.Second); err != nil {
				return fmt.Errorf("setting TIRITH_REQUIRED: %w", err)
			}
		}
	}

	return nil
}

// bootstrapPlugins installs Claude Code plugins as marketplace-cached plugins.
// Claude Code's LSP tool only registers when lspServers config comes from a
// marketplace plugin definition. This function replicates the file structure
// from https://github.com/anthropics/claude-plugins-official (public repo).
// Schema: https://json.schemastore.org/claude-code-marketplace.json
// When Claude Code adds SEED_DIR support in --print mode, this can be replaced
// with: CLAUDE_CODE_PLUGIN_SEED_DIR pointed at a pre-built plugin directory.
func bootstrapPlugins(sandboxName string, plugins []string) error {
	const marketplace = "claude-plugins-official"
	const version = "1.0.0"
	pluginsBase := sandbox.SandboxClaudeConfig + "/plugins"
	mktBase := pluginsBase + "/marketplaces/" + marketplace

	// Create all directories and README stubs in a single batched command.
	var mkdirParts, echoParts []string
	mkdirParts = append(mkdirParts, mktBase+"/.claude-plugin")
	for _, p := range plugins {
		name := filepath.Base(p)
		cacheDir := fmt.Sprintf("%s/cache/%s/%s/%s", pluginsBase, marketplace, name, version)
		mkdirParts = append(mkdirParts, mktBase+"/plugins/"+name, cacheDir)
		echoParts = append(echoParts,
			fmt.Sprintf("echo '# %s' > %s/README.md", name, cacheDir),
			fmt.Sprintf("echo '# %s' > %s/plugins/%s/README.md", name, mktBase, name),
		)
	}
	batchCmd := "mkdir -p " + strings.Join(mkdirParts, " ")
	if len(echoParts) > 0 {
		batchCmd += " && " + strings.Join(echoParts, " && ")
	}
	if _, _, _, err := sandbox.Exec(sandboxName, batchCmd, 10*time.Second); err != nil {
		return fmt.Errorf("creating marketplace dirs: %w", err)
	}

	// Upload plugin directories into sandbox.
	for _, pluginPath := range plugins {
		if err := sandbox.Upload(sandboxName, pluginPath,
			fmt.Sprintf("%s/plugins/", sandbox.SandboxClaudeConfig)); err != nil {
			return fmt.Errorf("copying plugin %q: %w", pluginPath, err)
		}
	}

	// Build and upload marketplace config files.
	configs, err := buildPluginConfigs(plugins, pluginsBase, mktBase, marketplace, version)
	if err != nil {
		return fmt.Errorf("building plugin configs: %w", err)
	}
	for _, entry := range configs {
		tmp, err := os.CreateTemp("", "fullsend-plugin-*.json")
		if err != nil {
			return fmt.Errorf("creating temp file for %s: %w", filepath.Base(entry.path), err)
		}
		if _, err := tmp.Write(entry.data); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return fmt.Errorf("writing %s: %w", filepath.Base(entry.path), err)
		}
		tmp.Close()
		uploadErr := sandbox.Upload(sandboxName, tmp.Name(), entry.path)
		os.Remove(tmp.Name())
		if uploadErr != nil {
			return fmt.Errorf("uploading %s: %w", filepath.Base(entry.path), uploadErr)
		}
	}
	return nil
}

type pluginConfigEntry struct {
	path string
	data []byte
}

// buildPluginConfigs builds the marketplace JSON config files for the given plugins.
// Returns entries for marketplace.json, known_marketplaces.json, installed_plugins.json,
// and settings.json.
func buildPluginConfigs(plugins []string, pluginsBase, mktBase, marketplace, version string) ([]pluginConfigEntry, error) {
	var mktPlugins []any
	installedPlugins := map[string]any{}
	enabledPlugins := map[string]bool{}
	ts := "2026-01-01T00:00:00.000Z"

	for _, pluginPath := range plugins {
		name := filepath.Base(pluginPath)
		qualifiedName := name + "@" + marketplace
		cacheDir := fmt.Sprintf("%s/cache/%s/%s/%s", pluginsBase, marketplace, name, version)

		mp := map[string]any{
			"name": name, "version": version,
			"source": "./plugins/" + name, "category": "development",
		}
		if data, err := os.ReadFile(filepath.Join(pluginPath, ".lsp.json")); err == nil {
			var servers map[string]any
			if json.Unmarshal(data, &servers) == nil {
				mp["lspServers"] = servers
			}
		}
		mktPlugins = append(mktPlugins, mp)
		installedPlugins[qualifiedName] = []map[string]string{{
			"scope": "user", "installPath": cacheDir, "version": version,
			"installedAt": ts, "lastUpdated": ts,
		}}
		enabledPlugins[qualifiedName] = true
	}

	entries := []struct {
		path string
		data any
	}{
		{mktBase + "/.claude-plugin/marketplace.json", map[string]any{
			"$schema": "https://anthropic.com/claude-code/marketplace.schema.json",
			"name":    marketplace,
			"owner":   map[string]string{"name": "Anthropic", "email": "support@anthropic.com"},
			"plugins": mktPlugins,
		}},
		{pluginsBase + "/known_marketplaces.json", map[string]any{
			marketplace: map[string]any{
				"source":          map[string]string{"source": "github", "repo": "anthropics/claude-plugins-official"},
				"installLocation": mktBase, "lastUpdated": ts,
			},
		}},
		{pluginsBase + "/installed_plugins.json", map[string]any{
			"version": 2, "plugins": installedPlugins,
		}},
		{sandbox.SandboxClaudeConfig + "/settings.json", map[string]any{
			"enabledPlugins": enabledPlugins,
		}},
	}

	var result []pluginConfigEntry
	for _, entry := range entries {
		data, err := json.Marshal(entry.data)
		if err != nil {
			return nil, fmt.Errorf("marshaling %s: %w", filepath.Base(entry.path), err)
		}
		result = append(result, pluginConfigEntry{path: entry.path, data: data})
	}
	return result, nil
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

// validateLinuxBinary checks that the file at path is a Linux ELF executable
// for the expected sandbox architecture. Returns a descriptive error if the
// file is missing, not ELF, not Linux, or the wrong architecture.
func validateLinuxBinary(path string) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("not a valid ELF binary (is this a macOS Mach-O?): %w", err)
	}
	defer f.Close()

	if f.OSABI != elf.ELFOSABI_NONE && f.OSABI != elf.ELFOSABI_LINUX {
		return fmt.Errorf("ELF OS/ABI is %s, expected Linux or NONE", f.OSABI)
	}

	arch := sandboxArch()
	archToMachine := map[string]elf.Machine{
		"amd64": elf.EM_X86_64,
		"arm64": elf.EM_AARCH64,
	}
	if expected, ok := archToMachine[arch]; ok && f.Machine != expected {
		return fmt.Errorf("ELF machine is %s, expected %s for %s (set FULLSEND_SANDBOX_ARCH to override)", f.Machine, expected, arch)
	}
	return nil
}

var validArchs = map[string]bool{"amd64": true, "arm64": true}

// sandboxArch returns the target architecture for the sandbox binary.
// Defaults to the host arch (correct when sandbox image matches host, e.g.
// arm64 Mac → arm64 sandbox image). Override with FULLSEND_SANDBOX_ARCH
// when the sandbox image uses a different architecture (e.g. amd64 image
// on an arm64 host via emulation). Only amd64 and arm64 are supported.
func sandboxArch() string {
	if arch := os.Getenv("FULLSEND_SANDBOX_ARCH"); arch != "" {
		if !validArchs[arch] {
			fmt.Fprintf(os.Stderr, "WARNING: FULLSEND_SANDBOX_ARCH=%q is not a supported architecture (amd64, arm64), using host arch %s\n", arch, runtime.GOARCH)
			return runtime.GOARCH
		}
		return arch
	}
	return runtime.GOARCH
}

// resolveLinuxBinary obtains a Linux fullsend binary for the given arch.
// Strategy: download from GitHub Release first (fast, no toolchain needed),
// fall back to cross-compilation if the download fails or version is "dev".
// Returns the temp directory (caller must clean up), the binary path, and any error.
func resolveLinuxBinary(arch string) (tmpDir string, binaryPath string, err error) {
	tmpDir, err = os.MkdirTemp("", "fullsend-linux-*")
	if err != nil {
		return "", "", fmt.Errorf("creating temp dir: %w", err)
	}
	binaryPath = filepath.Join(tmpDir, "fullsend")

	// 1. Released version → download matching release asset.
	if isReleasedVersion(version) {
		fmt.Fprintf(os.Stderr, "Downloading fullsend %s for linux/%s from GitHub Release...\n", version, arch)
		if dlErr := downloadReleaseBinary(version, arch, binaryPath); dlErr == nil {
			fmt.Fprintf(os.Stderr, "Downloaded fullsend for linux/%s\n", arch)
			return tmpDir, binaryPath, nil
		} else {
			fmt.Fprintf(os.Stderr, "WARNING: release download failed: %v\n", dlErr)
		}
	}

	// 2. Dev build → try cross-compilation (requires Go toolchain + module in CWD).
	fmt.Fprintf(os.Stderr, "Cross-compiling fullsend for linux/%s...\n", arch)
	if ccErr := crossCompileFullsend(arch, binaryPath); ccErr == nil {
		fmt.Fprintf(os.Stderr, "Cross-compiled fullsend for linux/%s\n", arch)
		return tmpDir, binaryPath, nil
	} else {
		fmt.Fprintf(os.Stderr, "WARNING: cross-compilation failed: %v\n", ccErr)
	}

	// 3. Last resort → download latest release (version won't match exactly,
	//    but the scan context command interface is stable across patch versions).
	fmt.Fprintf(os.Stderr, "Downloading latest fullsend release for linux/%s...\n", arch)
	if dlErr := downloadLatestReleaseBinary(arch, binaryPath); dlErr == nil {
		fmt.Fprintf(os.Stderr, "Downloaded latest fullsend for linux/%s\n", arch)
		return tmpDir, binaryPath, nil
	} else {
		fmt.Fprintf(os.Stderr, "WARNING: latest release download failed: %v\n", dlErr)
	}

	os.RemoveAll(tmpDir)
	return "", "", fmt.Errorf("all strategies failed for linux/%s: provide --fullsend-binary or install Go toolchain", arch)
}

// isReleasedVersion returns true if version looks like a release tag
// (e.g. "0.4.0", "v0.4.0") rather than a dev build (e.g. "dev",
// "0.4.0-3-gabcdef", "0.4.0-vendored").
func isReleasedVersion(v string) bool {
	v = strings.TrimPrefix(v, "v")
	if v == "" || v == "dev" {
		return false
	}
	// A released version is purely digits and dots (e.g. "0.4.0").
	for _, c := range v {
		if c != '.' && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

var releaseBaseURL = "https://github.com/fullsend-ai/fullsend/releases/download"

var httpClient = &http.Client{Timeout: 120 * time.Second}

// downloadReleaseBinary downloads the fullsend binary for linux/{arch} from
// the GitHub Release matching the given version, verifies its SHA256 checksum
// against the release checksums.txt, and writes it to destPath.
func downloadReleaseBinary(ver, arch, destPath string) error {
	cleanVer := strings.TrimPrefix(ver, "v")
	assetName := fmt.Sprintf("fullsend_%s_linux_%s.tar.gz", cleanVer, arch)

	expectedHash, err := downloadChecksumForAsset(ver, assetName)
	if err != nil {
		return fmt.Errorf("fetching checksum for %s: %w", assetName, err)
	}

	url := fmt.Sprintf("%s/v%s/%s", releaseBaseURL, cleanVer, assetName)
	resp, err := httpClient.Get(url) //nolint:gosec // URL is constructed from known constants
	if err != nil {
		return fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s returned %d", url, resp.StatusCode)
	}

	const maxDownloadSize = 200 * 1024 * 1024 // 200 MB compressed
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(resp.Body, maxDownloadSize)); err != nil {
		return fmt.Errorf("reading %s: %w", assetName, err)
	}

	h := sha256.Sum256(buf.Bytes())
	actualHash := hex.EncodeToString(h[:])
	if actualHash != expectedHash {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", assetName, actualHash, expectedHash)
	}

	return extractFullsendFromTarGz(bytes.NewReader(buf.Bytes()), destPath)
}

// downloadChecksumForAsset fetches the checksums.txt from the GitHub Release
// for the given version and returns the SHA256 hash for assetName.
// GoReleaser format: "<sha256>  <filename>\n"
func downloadChecksumForAsset(ver, assetName string) (string, error) {
	cleanVer := strings.TrimPrefix(ver, "v")
	url := fmt.Sprintf("%s/v%s/checksums.txt", releaseBaseURL, cleanVer)

	resp, err := httpClient.Get(url) //nolint:gosec // URL is constructed from known constants
	if err != nil {
		return "", fmt.Errorf("fetching checksums: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s returned %d", url, resp.StatusCode)
	}

	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 64*1024))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == assetName {
			hash := strings.ToLower(parts[0])
			if len(hash) != 64 {
				return "", fmt.Errorf("invalid hash length for %s in checksums.txt", assetName)
			}
			if _, err := hex.DecodeString(hash); err != nil {
				return "", fmt.Errorf("invalid hex hash for %s in checksums.txt: %w", assetName, err)
			}
			return hash, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading checksums: %w", err)
	}
	return "", fmt.Errorf("asset %s not found in checksums.txt", assetName)
}

// downloadLatestReleaseBinary resolves the latest release tag from the GitHub
// API and downloads the Linux binary for the given arch.
func downloadLatestReleaseBinary(arch, destPath string) error {
	tag, err := resolveLatestReleaseTag()
	if err != nil {
		return err
	}
	return downloadReleaseBinary(tag, arch, destPath)
}

func resolveLatestReleaseTag() (string, error) {
	resp, err := httpClient.Get("https://api.github.com/repos/fullsend-ai/fullsend/releases/latest") //nolint:gosec
	if err != nil {
		return "", fmt.Errorf("fetching latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&release); err != nil {
		return "", fmt.Errorf("parsing release JSON: %w", err)
	}
	if release.TagName == "" {
		return "", fmt.Errorf("empty tag_name in latest release")
	}
	return release.TagName, nil
}

const maxBinarySize = 500 * 1024 * 1024 // 500 MB — reasonable upper bound for a Go binary

// extractFullsendFromTarGz reads a tar.gz stream and extracts the "fullsend"
// binary to destPath.
func extractFullsendFromTarGz(r io.Reader, destPath string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("fullsend binary not found in archive")
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}
		clean := filepath.Clean(hdr.Name)
		if strings.Contains(clean, "..") || filepath.IsAbs(clean) {
			continue
		}
		if filepath.Base(clean) == "fullsend" && hdr.Typeflag == tar.TypeReg {
			f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
			if err != nil {
				return fmt.Errorf("creating %s: %w", destPath, err)
			}
			n, copyErr := io.Copy(f, io.LimitReader(tr, maxBinarySize+1))
			if copyErr != nil {
				f.Close()
				return fmt.Errorf("extracting fullsend: %w", copyErr)
			}
			if n > maxBinarySize {
				f.Close()
				os.Remove(destPath)
				return fmt.Errorf("binary exceeds maximum size (%d bytes)", maxBinarySize)
			}
			return f.Close()
		}
	}
}

// crossCompileFullsend builds a Linux fullsend binary for the given arch
// and writes it to destPath. Requires the Go toolchain.
func crossCompileFullsend(arch, destPath string) error {
	goPath, lookErr := exec.LookPath("go")
	if lookErr != nil {
		return fmt.Errorf("Go toolchain not found — install Go or use a released version of fullsend: %w", lookErr)
	}

	// Find the module root so `go build ./cmd/fullsend/` resolves correctly
	// regardless of the caller's working directory.
	modRootCmd := exec.Command(goPath, "env", "GOMOD")
	modOutput, err := modRootCmd.Output()
	if err != nil {
		return fmt.Errorf("finding module root: %w", err)
	}
	modPath := strings.TrimSpace(string(modOutput))
	if modPath == "" || modPath == os.DevNull {
		return fmt.Errorf("not in a Go module — run from the fullsend source tree or use a released version")
	}
	modRoot := filepath.Dir(modPath)

	buildCmd := exec.Command(goPath, "build",
		"-ldflags", fmt.Sprintf("-X github.com/fullsend-ai/fullsend/internal/cli.version=%s-crosscompiled", version),
		"-o", destPath,
		"./cmd/fullsend/",
	)
	buildCmd.Dir = modRoot
	buildCmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto", "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("cross-compiling for linux/%s: %w", arch, err)
	}
	return nil
}
