package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newRunCmd() *cobra.Command {
	var fullsendDir string
	var outputBase string
	var targetRepo string

	cmd := &cobra.Command{
		Use:   "run <agent-name>",
		Short: "Run an agent",
		Long:  "Execute an agent by name: read its harness YAML, set up the sandbox, and run the agent.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentName := args[0]
			printer := ui.New(os.Stdout)
			return runAgent(agentName, fullsendDir, outputBase, targetRepo, printer)
		},
	}

	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "base directory containing the .fullsend layout")
	cmd.Flags().StringVar(&outputBase, "output-dir", "", "base directory for run output (default: /tmp/fullsend)")
	cmd.Flags().StringVar(&targetRepo, "target-repo", "", "path to the target repository")
	_ = cmd.MarkFlagRequired("fullsend-dir")
	_ = cmd.MarkFlagRequired("target-repo")

	return cmd
}

func runAgent(agentName, fullsendDir, outputBase, targetRepo string, printer *ui.Printer) (runErr error) {
	printer.Banner()
	printer.Blank()
	printer.Header("Running agent: " + agentName)
	printer.Blank()

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
	if h.AgentInput != "" {
		printer.KeyValue("Agent input", h.AgentInput)
	}
	if h.PreScript != "" {
		printer.KeyValue("Pre-script", h.PreScript)
	}
	if h.PostScript != "" {
		printer.KeyValue("Post-script", h.PostScript)
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
		cleanupStart := time.Now()
		printer.StepStart("Cleaning up sandbox")
		if err := sandbox.Delete(sandboxName); err != nil {
			printer.StepWarn("Sandbox cleanup failed: " + err.Error())
		} else {
			printer.StepDone(fmt.Sprintf("Sandbox deleted (%.1fs)", time.Since(cleanupStart).Seconds()))
		}
	}()
	printer.StepDone(fmt.Sprintf("Sandbox created (%.1fs)", time.Since(createStart).Seconds()))

	// 4. Get SSH config.
	sshConfig, err := sandbox.GetSSHConfig(sandboxName)
	if err != nil {
		printer.StepFail("Failed to get SSH config")
		return err
	}

	sshConfigFile, err := os.CreateTemp("", "openshell-ssh-*.config")
	if err != nil {
		return fmt.Errorf("creating SSH config temp file: %w", err)
	}
	sshConfigPath := sshConfigFile.Name()
	if _, err := sshConfigFile.WriteString(sshConfig); err != nil {
		sshConfigFile.Close()
		os.Remove(sshConfigPath)
		return fmt.Errorf("writing SSH config: %w", err)
	}
	sshConfigFile.Close()
	defer os.Remove(sshConfigPath)

	// 6. Resolve target repo path (needed by bootstrap for env vars).
	repoSrc, err := filepath.Abs(targetRepo)
	if err != nil {
		return fmt.Errorf("resolving target repo path: %w", err)
	}
	repoName := filepath.Base(repoSrc)
	repoDir := fmt.Sprintf("%s/%s", sandbox.SandboxWorkspace, repoName)

	// 7. Bootstrap sandbox.
	bootstrapStart := time.Now()
	printer.StepStart("Bootstrapping sandbox")
	if err := bootstrapSandbox(sshConfigPath, sandboxName, repoDir, h); err != nil {
		printer.StepFail("Failed to bootstrap sandbox")
		return err
	}
	printer.StepDone(fmt.Sprintf("Sandbox bootstrapped (%.1fs)", time.Since(bootstrapStart).Seconds()))

	// 8. Make project code available (copy repo root into a named subdirectory).
	copyStart := time.Now()
	printer.StepStart("Copying project code into sandbox")
	mkRepoCmd := fmt.Sprintf("mkdir -p %s", repoDir)
	if _, _, _, err := sandbox.SSH(sshConfigPath, sandboxName, mkRepoCmd, 10*time.Second); err != nil {
		return fmt.Errorf("creating repo dir in sandbox: %w", err)
	}
	if err := sandbox.SCP(sshConfigPath, sandboxName, repoSrc+"/.", repoDir+"/"); err != nil {
		printer.StepFail("Failed to copy project code")
		return fmt.Errorf("copying project code: %w", err)
	}
	printer.StepDone(fmt.Sprintf("Project code copied to %s/ (%.1fs)", repoName, time.Since(copyStart).Seconds()))

	// 8b. Copy agent-input files (if configured).
	if h.AgentInput != "" {
		inputStart := time.Now()
		printer.StepStart("Copying agent-input files into sandbox")
		remoteInput := fmt.Sprintf("%s/agent-input", sandbox.SandboxWorkspace)
		mkInputCmd := fmt.Sprintf("mkdir -p %s", remoteInput)
		if _, _, _, err := sandbox.SSH(sshConfigPath, sandboxName, mkInputCmd, 10*time.Second); err != nil {
			return fmt.Errorf("creating agent-input dir in sandbox: %w", err)
		}
		if err := sandbox.SCP(sshConfigPath, sandboxName, h.AgentInput+"/.", remoteInput+"/"); err != nil {
			printer.StepFail("Failed to copy agent-input files")
			return fmt.Errorf("copying agent-input files: %w", err)
		}
		printer.StepDone(fmt.Sprintf("Agent-input files copied (%.1fs)", time.Since(inputStart).Seconds()))
	}

	// 9a. Generate trace ID for security finding correlation.
	traceID := security.GenerateTraceID()
	printer.KeyValue("Trace ID", traceID)
	if err := injectTraceID(sshConfigPath, sandboxName, traceID); err != nil {
		printer.StepWarn("Could not inject trace ID into sandbox: " + err.Error())
	}

	// 9b. Pre-agent security scan (sandbox-internal, Path B).
	// Scans context files (CLAUDE.md, AGENTS.md, .cursorrules, agent defs)
	// that were just copied into the sandbox.
	if h.SecurityEnabled() {
		printer.StepStart("Running pre-agent security scan")
		scanCmd := buildScanContextCommand(repoDir, traceID)
		stdout, stderr, exitCode, sshErr := sandbox.SSH(sshConfigPath, sandboxName, scanCmd, 60*time.Second)
		if sshErr != nil {
			printer.StepFail("Security scan SSH failed: " + sshErr.Error())
			if h.FailModeClosed() {
				return fmt.Errorf("pre-agent security scan failed: %w", sshErr)
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
	claudeCmd := buildClaudeCommand(agentBaseName, h.Model, repoDir)

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
			if _, _, _, clearErr := sandbox.SSH(sshConfigPath, sandboxName, clearCmd, 10*time.Second); clearErr != nil {
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
		exitCode, runErr := runAgentWithProgress(sshConfigPath, sandboxName, claudeCmd, timeout, printer, agentStart, &metrics)
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
		extracted, extractErr := sandbox.ExtractOutputFiles(sshConfigPath, sandboxName, remoteSrc, iterOutputDir)
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
		if err := sandbox.ExtractTranscripts(sshConfigPath, sandboxName, agentName, iterTranscriptDir); err != nil {
			printer.StepWarn("Failed to extract transcripts: " + err.Error())
		} else {
			printer.StepDone(fmt.Sprintf("Transcripts extracted (%.1fs)", time.Since(transcriptStart).Seconds()))
		}

		// 9d. Extract target repo back to host. Uses rsync with --no-links
		// and --exclude .git/hooks/ to prevent sandbox escape via symlinks
		// or injected git hooks.
		repoExtractStart := time.Now()
		printer.StepStart("Extracting target repo")
		if err := sandbox.RsyncFrom(sshConfigPath, sandboxName, repoDir, repoSrc); err != nil {
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
			if scpErr := sandbox.SCPFrom(sshConfigPath, sandboxName, remoteFindingsDir, findingsDir); scpErr != nil {
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

func bootstrapSandbox(sshConfigPath, sandboxName, repoDir string, h *harness.Harness) error {
	// Create workspace structure and Claude config dir for transcripts.
	// Agent and skill definitions go in CLAUDE_CONFIG_DIR so `claude --agent`
	// finds them regardless of the repo's own .claude/ directory. When
	// CLAUDE_CONFIG_DIR is set, Claude uses it instead of ~/.claude/.
	mkdirCmd := fmt.Sprintf("mkdir -p %s/agents %s/skills %s/hooks %s/bin %s/.env.d %s/.security %s %s/.claude/hooks",
		sandbox.SandboxClaudeConfig, sandbox.SandboxClaudeConfig, sandbox.SandboxClaudeConfig, sandbox.SandboxWorkspace, sandbox.SandboxWorkspace, sandbox.SandboxWorkspace, sandbox.SandboxClaudeConfig, sandbox.SandboxWorkspace)
	if _, _, _, err := sandbox.SSH(sshConfigPath, sandboxName, mkdirCmd, 10*time.Second); err != nil {
		return fmt.Errorf("creating workspace dirs: %w", err)
	}

	// Copy fullsend binary into sandbox so `fullsend scan context` works.
	// The pre-agent security scan runs inside the sandbox and needs the
	// fullsend CLI to scan context files.
	fullsendBinary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding fullsend executable: %w", err)
	}
	remoteBinary := fmt.Sprintf("%s/bin/fullsend", sandbox.SandboxWorkspace)
	if err := sandbox.SCP(sshConfigPath, sandboxName, fullsendBinary, remoteBinary); err != nil {
		return fmt.Errorf("copying fullsend binary to sandbox: %w", err)
	}
	chmodCmd := fmt.Sprintf("chmod +x %s", remoteBinary)
	if _, _, _, err := sandbox.SSH(sshConfigPath, sandboxName, chmodCmd, 10*time.Second); err != nil {
		return fmt.Errorf("chmod fullsend binary: %w", err)
	}

	// Copy agent definition to $CLAUDE_CONFIG_DIR/agents/.
	if err := sandbox.SCP(sshConfigPath, sandboxName, h.Agent,
		fmt.Sprintf("%s/agents/", sandbox.SandboxClaudeConfig)); err != nil {
		return fmt.Errorf("copying agent definition: %w", err)
	}

	// Copy skills (SCP -r copies the entire directory tree, including any
	// scripts/, references/, and assets/ bundled with the skill per the
	// agentskills.io specification).
	for _, skillPath := range h.Skills {
		if err := sandbox.SCP(sshConfigPath, sandboxName, skillPath,
			fmt.Sprintf("%s/skills/", sandbox.SandboxClaudeConfig)); err != nil {
			return fmt.Errorf("copying skill %q: %w", skillPath, err)
		}
	}

	// Write .env file (infrastructure vars) and copy host files.
	if err := bootstrapEnv(sshConfigPath, sandboxName, repoDir, h); err != nil {
		return fmt.Errorf("bootstrapping environment: %w", err)
	}

	// Install security hooks if enabled.
	if h.SecurityEnabled() {
		if err := bootstrapSecurityHooks(sshConfigPath, sandboxName, h); err != nil {
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
func bootstrapEnv(sshConfigPath, sandboxName, repoDir string, h *harness.Harness) error {
	remoteEnvFile := sandbox.SandboxWorkspace + "/.env"
	outputDir := sandbox.SandboxWorkspace + "/output"

	var lines []string

	// Infrastructure vars.
	lines = append(lines, fmt.Sprintf("export PATH=%s/bin:$PATH", sandbox.SandboxWorkspace))
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

	if err := sandbox.SCP(sshConfigPath, sandboxName, tmpFile.Name(), remoteEnvFile); err != nil {
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

			if err := sandbox.SCP(sshConfigPath, sandboxName, tmp.Name(), hf.Dest); err != nil {
				os.Remove(tmp.Name())
				return fmt.Errorf("copying expanded file %s to %s: %w", hf.Src, hf.Dest, err)
			}
			os.Remove(tmp.Name())
		} else {
			if err := sandbox.SCP(sshConfigPath, sandboxName, hostPath, hf.Dest); err != nil {
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
			if _, _, _, sshErr := sandbox.SSH(sshConfigPath, sandboxName, chmodCmd, 10*time.Second); sshErr != nil {
				return fmt.Errorf("chmod host file %s in sandbox: %w", hf.Dest, sshErr)
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

func runAgentWithProgress(sshConfigPath, sandboxName, claudeCmd string, timeout time.Duration, printer *ui.Printer, start time.Time, metrics *RunMetrics) (int, error) {
	stdout, cmd, cancel, err := sandbox.SSHStreamReader(sshConfigPath, sandboxName, claudeCmd, timeout, os.Stderr)
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
		return exitCode, fmt.Errorf("ssh failed: %w", waitErr)
	}

	return exitCode, nil
}

const heartbeatInterval = 30 * time.Second

func runHeartbeat(printer *ui.Printer, start time.Time, timeout time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	isCI := os.Getenv("GITHUB_ACTIONS") == "true"

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			elapsed := time.Since(start).Truncate(time.Second)
			remaining := (timeout - elapsed).Truncate(time.Second)
			msg := fmt.Sprintf("Agent running (%s elapsed, %s remaining)", elapsed, remaining)
			if isCI {
				fmt.Fprintf(os.Stderr, "::notice::%s\n", msg)
			}
			printer.Heartbeat(msg)
		}
	}
}

func buildClaudeCommand(agentName, model, repoDir string) string {
	envFile := sandbox.SandboxWorkspace + "/.env"

	// Defense-in-depth: escape single quotes even though Validate() rejects them.
	safe := strings.ReplaceAll(agentName, "'", "'\\''")

	modelFlag := ""
	if model != "" {
		modelFlag = fmt.Sprintf("--model '%s' ", strings.ReplaceAll(model, "'", "'\\''"))
	}

	return fmt.Sprintf(
		// --verbose increases log output in the job log. If artifact upload is
		// added to this workflow, consider whether verbose output should be
		// redacted or made conditional via an env var.
		"cd %s && source %s && claude --print --verbose --output-format stream-json %s--agent '%s' --dangerously-skip-permissions 'Run the agent task'",
		repoDir, envFile, modelFlag, safe,
	)
}

// buildScanContextCommand builds the SSH command to run `fullsend scan context`
// inside the sandbox. It finds known context files in the repo directory and
// passes them as arguments.
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
		"source %s && FULLSEND_TRACE_ID='%s' find '%s' -maxdepth 3 -type f \\( %s \\) -exec fullsend scan context {} +",
		envFile, traceID, escapedDir, inameExpr,
	)
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
func bootstrapSecurityHooks(sshConfigPath, sandboxName string, h *harness.Harness) error {
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
		if err := sandbox.SCP(sshConfigPath, sandboxName, tmpFile.Name(), remotePath); err != nil {
			os.Remove(tmpFile.Name())
			return fmt.Errorf("copying hook %s to sandbox: %w", name, err)
		}
		os.Remove(tmpFile.Name())

		// Make executable.
		chmodCmd := fmt.Sprintf("chmod +x %s", remotePath)
		if _, _, _, err := sandbox.SSH(sshConfigPath, sandboxName, chmodCmd, 10*time.Second); err != nil {
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
	if err := sandbox.SCP(sshConfigPath, sandboxName, tmpSettings.Name(), remoteSettings); err != nil {
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
			if _, _, _, err := sandbox.SSH(sshConfigPath, sandboxName, envCmd, 10*time.Second); err != nil {
				return fmt.Errorf("setting TIRITH_FAIL_ON: %w", err)
			}
		}

		// When tirith is enabled (default), mark it as required so the hook
		// fails closed if the binary is missing from the sandbox image.
		if harness.BoolDefault(tirithCfg.Enabled, true) {
			envCmd := fmt.Sprintf("echo 'export TIRITH_REQUIRED=1' >> %s/.env", sandbox.SandboxWorkspace)
			if _, _, _, err := sandbox.SSH(sshConfigPath, sandboxName, envCmd, 10*time.Second); err != nil {
				return fmt.Errorf("setting TIRITH_REQUIRED: %w", err)
			}
		}
	}

	return nil
}

// injectTraceID appends the FULLSEND_TRACE_ID to the sandbox .env file.
func injectTraceID(sshConfigPath, sandboxName, traceID string) error {
	if !security.IsValidTraceID(traceID) {
		return fmt.Errorf("invalid trace ID format: %q", traceID)
	}
	// Safe: IsValidTraceID() above ensures traceID matches UUID v4 format only.
	cmd := fmt.Sprintf("echo 'export FULLSEND_TRACE_ID=%s' >> %s/.env", traceID, sandbox.SandboxWorkspace)
	_, _, _, err := sandbox.SSH(sshConfigPath, sandboxName, cmd, 10*time.Second)
	return err
}
