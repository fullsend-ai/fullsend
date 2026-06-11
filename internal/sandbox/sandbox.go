package sandbox

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// SandboxWorkspace is the workspace directory inside the sandbox.
	SandboxWorkspace = "/sandbox/workspace" //nolint:gosec // not a credential
	// SandboxClaudeConfig is the Claude config directory inside the sandbox.
	SandboxClaudeConfig = "/sandbox/claude-config" //nolint:gosec // not a credential

	readyTimeout    = 120 * time.Second
	readyPoll       = 2 * time.Second
	readyCtxBuffer  = 10 * time.Second
	maxReadyTimeout = 600 * time.Second
	transferTimeout = 5 * time.Minute

	DefaultMaxCreateAttempts = 3
	retryInitialBackoff      = 5 * time.Second
	retryMaxBackoff          = 15 * time.Second
)

func sanitizeDownload(localDir string) error {
	absLocal, err := filepath.Abs(localDir)
	if err != nil {
		return err
	}
	absLocal, err = filepath.EvalSymlinks(absLocal)
	if err != nil {
		return err
	}

	return filepath.WalkDir(absLocal, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			target, readErr := os.Readlink(path)
			if readErr != nil {
				return os.Remove(path)
			}
			// Absolute targets always point outside the repo root.
			if filepath.IsAbs(target) {
				return os.Remove(path)
			}
			// Use EvalSymlinks, not filepath.Clean: Clean is textual and misses
			// chains where an in-repo dir-symlink is used as a component
			// (e.g. "sub/link/../../etc/passwd" cleans to inside the repo but
			// follows the link to outside). Fall back to remove on error
			// (dangling or looping).
			rawPath := filepath.Dir(path) + string(filepath.Separator) + target
			resolved, evalErr := filepath.EvalSymlinks(rawPath)
			if evalErr != nil {
				return os.Remove(path)
			}
			if !strings.HasPrefix(resolved+string(filepath.Separator), absLocal+string(filepath.Separator)) {
				return os.Remove(path)
			}
			return nil
		}

		if d.IsDir() && d.Name() == "hooks" && filepath.Base(filepath.Dir(path)) == ".git" {
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("removing .git/hooks: %w", err)
			}
			return filepath.SkipDir
		}

		// Remove AppleDouble (._*) files inside .git/ directories.
		// These are created by macOS bsdtar when archiving files with
		// extended attributes and corrupt git when extracted on Linux.
		if !d.IsDir() && strings.HasPrefix(d.Name(), "._") && inGitDir(path, absLocal) {
			return os.Remove(path)
		}

		return nil
	})
}

// inGitDir reports whether path is inside a ".git" directory under root.
func inGitDir(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	for _, component := range strings.Split(filepath.Dir(rel), string(filepath.Separator)) {
		if component == ".git" {
			return true
		}
	}
	return false
}

// EnsureProvider creates or updates a provider on the gateway. Credential
// values may contain ${VAR} references which are expanded from the host
// environment before being passed to openshell.
//
// Credentials use the bare-key form (--credential KEY) so that secret values
// never appear on the process command line. The expanded values are injected
// into the child process environment, where openshell reads them directly.
// See https://docs.nvidia.com/openshell/latest/sandboxes/manage-providers#bare-key-form
func EnsureProvider(name, providerType string, credentials, config map[string]string) error {
	args, extraEnv, secrets := buildProviderArgs(name, providerType, credentials, config)

	cmd := exec.Command("openshell", args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Redact known credential values from error output.
		outStr := string(out)
		for _, s := range secrets {
			outStr = strings.ReplaceAll(outStr, s, "***")
		}
		return fmt.Errorf("provider create %q failed: %s", name, outStr)
	}
	return nil
}

// buildProviderArgs constructs the CLI args and child environment entries for
// openshell provider create. Credentials use the bare-key form (--credential KEY)
// so secret values never appear on the process command line. The expanded values
// are returned as extra env vars to be set on the child process.
// See https://docs.nvidia.com/openshell/latest/sandboxes/manage-providers#bare-key-form
func buildProviderArgs(name, providerType string, credentials, config map[string]string) (args, extraEnv, secrets []string) {
	args = []string{"provider", "create",
		"--name", name,
		"--type", providerType,
	}

	for k, v := range credentials {
		expanded := os.ExpandEnv(v)
		if expanded != "" {
			secrets = append(secrets, expanded)
		}
		extraEnv = append(extraEnv, fmt.Sprintf("%s=%s", k, expanded))
		args = append(args, "--credential", k)
	}
	for k, v := range config {
		expanded := os.ExpandEnv(v)
		args = append(args, "--config", k+"="+expanded)
	}

	return args, extraEnv, secrets
}

// EnsureAvailable checks that the openshell binary is in PATH.
func EnsureAvailable() error {
	_, err := exec.LookPath("openshell")
	if err != nil {
		return fmt.Errorf("openshell not found in PATH: %w", err)
	}
	return nil
}

// CheckGateway verifies that an openshell gateway is already running.
// The gateway must be started externally (e.g. in CI via the action.yml steps)
// before invoking fullsend run.
func CheckGateway() error {
	out, err := exec.Command("openshell", "gateway", "list").CombinedOutput()
	if err != nil {
		return fmt.Errorf("no openshell gateway running (openshell gateway list: %s) -- start openshell-gateway before running fullsend", strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) == "" {
		return fmt.Errorf("no openshell gateway configured -- start openshell-gateway before running fullsend")
	}
	return nil
}

// effectiveReadyTimeout returns the sandbox ready timeout to use. Priority:
// explicit override (from harness config) > FULLSEND_SANDBOX_READY_TIMEOUT
// env var > package default.
func effectiveReadyTimeout(override time.Duration) time.Duration {
	t := readyTimeout
	if override > 0 {
		t = override
	} else if envVal := os.Getenv("FULLSEND_SANDBOX_READY_TIMEOUT"); envVal != "" {
		if d, err := time.ParseDuration(envVal); err == nil && d > 0 {
			t = d
		}
	}
	if t > maxReadyTimeout {
		t = maxReadyTimeout
	}
	return t
}

// Create creates a persistent OpenShell sandbox and waits for it to be ready.
// It retries up to DefaultMaxCreateAttempts times with exponential backoff,
// deleting the failed sandbox between attempts.
func Create(name string, providers []string, image, policy string) error {
	return CreateWithRetry(name, providers, image, policy, DefaultMaxCreateAttempts, 0)
}

// CreateWithRetry creates a sandbox, retrying up to maxAttempts times with
// exponential backoff on failure. Between attempts the failed sandbox is
// deleted to avoid name conflicts. If readyTimeoutOverride is positive, it
// overrides the default ready timeout.
func CreateWithRetry(name string, providers []string, image, policy string, maxAttempts int, readyTimeoutOverride time.Duration) error {
	if maxAttempts < 1 {
		return fmt.Errorf("maxAttempts must be >= 1, got %d", maxAttempts)
	}

	timeout := effectiveReadyTimeout(readyTimeoutOverride)

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		lastErr = createOnce(name, providers, image, policy, timeout)
		if lastErr == nil {
			return nil
		}

		if delErr := Delete(name); delErr != nil {
			fmt.Fprintf(os.Stderr, "  Warning: cleanup of sandbox %s failed: %v\n", name, delErr)
		}

		if attempt < maxAttempts {
			shift := uint(attempt - 1)
			if shift > 30 {
				shift = 30
			}
			backoff := retryInitialBackoff * time.Duration(1<<shift)
			if backoff > retryMaxBackoff {
				backoff = retryMaxBackoff
			}
			fmt.Fprintf(os.Stderr, "  Sandbox creation attempt %d/%d failed (%v), retrying in %s...\n", attempt, maxAttempts, lastErr, backoff)
			time.Sleep(backoff)
		}
	}
	return fmt.Errorf("sandbox creation failed after %d attempts: %w", maxAttempts, lastErr)
}

// createOnce creates a persistent OpenShell sandbox and waits for it to be
// ready. If providers are given, they are passed as --provider flags. If image
// is non-empty, it is passed as --from to start the sandbox from a container
// image. If policy is non-empty, it is applied at creation time via --policy.
func createOnce(name string, providers []string, image, policy string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout+readyCtxBuffer)
	defer cancel()

	args := []string{
		"sandbox", "create",
		"--name", name,
		"--keep",
		"--no-auto-providers",
		"--no-tty",
	}
	if image != "" {
		args = append(args, "--from", image)
	}
	if policy != "" {
		args = append(args, "--policy", policy)
	}
	for _, p := range providers {
		args = append(args, "--provider", p)
	}
	// Without a command, sandbox create starts an interactive shell and
	// blocks until it exits. Pass `true` so it returns immediately.
	args = append(args, "--", "true")

	cmd := exec.CommandContext(ctx, "openshell", args...)
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()

	if err != nil {
		check := exec.CommandContext(ctx, "openshell", "sandbox", "get", name)
		if checkErr := check.Run(); checkErr != nil {
			return fmt.Errorf("sandbox create failed: %s", string(out))
		}
	}

	// Wait for sandbox to be fully ready (image pull can take a while).
	deadline := time.Now().Add(timeout)
	var lastOutput, lastStderr string
	for time.Now().Before(deadline) {
		check := exec.CommandContext(ctx, "openshell", "sandbox", "get", name)
		var stdoutBuf, stderrBuf strings.Builder
		check.Stdout = &stdoutBuf
		check.Stderr = &stderrBuf
		checkErr := check.Run()
		lastOutput = stdoutBuf.String()
		lastStderr = stderrBuf.String()
		if checkErr == nil && strings.Contains(lastOutput, "Ready") {
			return nil
		}
		time.Sleep(readyPoll)
	}

	// Collect sandbox logs to help diagnose the failure.
	supervisorLogs, _ := CollectLogs(name, "supervisor")
	gatewayLogs, _ := CollectLogs(name, "gateway")

	containerLogs := collectPodmanLogs(name)

	return fmt.Errorf("sandbox %q not ready after %s\nstdout: %s\nstderr: %s\nsupervisor logs: %s\ngateway logs: %s\ncontainer logs: %s",
		name, timeout, lastOutput, lastStderr, supervisorLogs, gatewayLogs, containerLogs)
}

// Delete deletes a sandbox, returning any error for the caller to log.
func Delete(name string) error {
	out, err := exec.Command("openshell", "sandbox", "delete", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sandbox delete %q failed: %s", name, string(out))
	}
	return nil
}

// Exec runs a command inside a sandbox and returns stdout, stderr, and exit code.
func Exec(sandboxName, command string, timeout time.Duration) (stdout, stderr string, exitCode int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout+10*time.Second)
	defer cancel()

	timeoutSecs := fmt.Sprintf("%d", int(timeout.Seconds()))

	cmd := exec.CommandContext(ctx, "openshell", "sandbox", "exec",
		"--name", sandboxName,
		"--no-tty",
		"--timeout", timeoutSecs,
		"--", "sh", "-c", command,
	)

	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()
	exitCode = -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	if runErr != nil && cmd.ProcessState == nil {
		return "", "", exitCode, fmt.Errorf("openshell exec failed to start: %w", runErr)
	}

	if exitCode == 124 {
		return stdoutBuf.String(), stderrBuf.String(), exitCode,
			fmt.Errorf("command timed out after %s", timeout)
	}

	return stdoutBuf.String(), stderrBuf.String(), exitCode, nil
}

// ExecStreamReader runs a command inside a sandbox, returning an io.ReadCloser for
// stdout so the caller can parse structured output. Stderr is forwarded to the
// given writer. The caller must read stdout to completion, then call cmd.Wait().
func ExecStreamReader(sandboxName, command string, timeout time.Duration, stderrW io.Writer) (io.ReadCloser, *exec.Cmd, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	timeoutSecs := fmt.Sprintf("%d", int(timeout.Seconds()))

	cmd := exec.CommandContext(ctx, "openshell", "sandbox", "exec",
		"--name", sandboxName,
		"--no-tty",
		"--timeout", timeoutSecs,
		"--", "sh", "-c", command,
	)
	cmd.Stderr = stderrW

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("starting openshell exec: %w", err)
	}

	return stdout, cmd, cancel, nil
}

// Upload copies a local file or directory into a sandbox.
func Upload(sandboxName, localPath, remotePath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), transferTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "openshell", "sandbox", "upload",
		sandboxName,
		localPath,
		remotePath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("upload to sandbox %q timed out after %s", sandboxName, transferTimeout)
		}
		return fmt.Errorf("upload to sandbox %q failed: %s: %w", sandboxName, string(out), err)
	}
	return nil
}

// shellQuote wraps s in single quotes with internal single quotes escaped,
// making it safe to interpolate into a sh -c command string.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// UploadFile copies a single local file into a sandbox at a specific remote path.
// It checks if the remotePath is a file and if it is not it tries to fix it. This is
// because of `openshell sandbox upload` in a git environment. Check
// https://github.com/NVIDIA/OpenShell/issues/1740 for more information. When that gets
// addressed, this can go away.
func UploadFile(sandboxName, localPath, remotePath string) error {
	if err := Upload(sandboxName, localPath, remotePath); err != nil {
		return err
	}

	_, _, exitCode, err := Exec(sandboxName, fmt.Sprintf("test -f %s", shellQuote(remotePath)), 1*time.Second)
	if err != nil {
		return err
	}

	if exitCode != 0 {
		wrongPath := fmt.Sprintf("%s/%s", remotePath, filepath.Base(localPath))
		_, _, exitCode, err := Exec(sandboxName, fmt.Sprintf("test -f %s", shellQuote(wrongPath)), 1*time.Second)
		if err != nil {
			return err
		}

		if exitCode != 0 {
			return fmt.Errorf("checking for file: %s", wrongPath)
		}

		tmpPath := fmt.Sprintf("/tmp/%s", filepath.Base(remotePath))
		stdout, stderr, exitCode, err := Exec(sandboxName, fmt.Sprintf("mv %s %s", shellQuote(wrongPath), shellQuote(tmpPath)), 1*time.Second)
		if err != nil {
			return err
		}
		if exitCode != 0 {
			return fmt.Errorf("fixing UploadFile path: %s, %s", stdout, stderr)
		}

		stdout, stderr, exitCode, err = Exec(sandboxName, fmt.Sprintf("rm -r %s", shellQuote(remotePath)), 1*time.Second)
		if err != nil {
			return err
		}
		if exitCode != 0 {
			return fmt.Errorf("fixing UploadFile path: %s, %s", stdout, stderr)
		}

		stdout, stderr, exitCode, err = Exec(sandboxName, fmt.Sprintf("mv %s %s", shellQuote(tmpPath), shellQuote(remotePath)), 1*time.Second)
		if err != nil {
			return err
		}
		if exitCode != 0 {
			return fmt.Errorf("fixing UploadFile path: %s, %s", stdout, stderr)
		}
	}
	return nil
}

// UploadDir uploads a local directory into a sandbox, preserving symlinks.
// openshell sandbox upload dereferences symlinks; this builds a local tarball
// with --no-dereference, uploads it, and extracts it in the sandbox.
func UploadDir(sandboxName, localPath, remotePath string) error {
	tmp, err := os.CreateTemp("", "openshell-upload-*.tar.gz")
	if err != nil {
		return fmt.Errorf("creating temp tarball: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	tarCmd := exec.Command("tar", "-czf", tmpPath, "-C", localPath, ".")
	// Suppress macOS AppleDouble (._*) files in the tarball. On macOS,
	// bsdtar generates ._* companion files for any file with extended
	// attributes. These corrupt .git after a sandbox round-trip.
	// COPYFILE_DISABLE is a no-op on Linux.
	tarCmd.Env = append(os.Environ(), "COPYFILE_DISABLE=1")
	if out, tarErr := tarCmd.CombinedOutput(); tarErr != nil {
		return fmt.Errorf("creating tarball of %q: %s: %w", localPath, string(out), tarErr)
	}

	remoteTar := fmt.Sprintf("/tmp/fs-upload-%s.tar.gz", sandboxName)
	if err := Upload(sandboxName, tmpPath, remoteTar); err != nil {
		return err
	}

	extractCmd := fmt.Sprintf("mkdir -p %s && tar -xzf %s -C %s && rm %s", remotePath, remoteTar, remotePath, remoteTar)
	_, stderr, exitCode, err := Exec(sandboxName, extractCmd, transferTimeout)
	if err != nil {
		return fmt.Errorf("extracting tarball in sandbox %q: %w", sandboxName, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("extracting tarball in sandbox %q: exit %d: %s", sandboxName, exitCode, stderr)
	}
	return nil
}

// Download copies a file or directory from a sandbox to the local machine.
// The localPath is always treated as a directory by openshell — for single-file
// downloads use DownloadFile instead.
func Download(sandboxName, remotePath, localPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), transferTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "openshell", "sandbox", "download",
		sandboxName,
		remotePath,
		localPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("download from sandbox %q timed out after %s", sandboxName, transferTimeout)
		}
		return fmt.Errorf("download from sandbox %q failed: %s: %w", sandboxName, string(out), err)
	}
	return nil
}

// DownloadFile copies a single file from a sandbox to a specific local path.
// openshell sandbox download always treats the destination as a directory, so
// this downloads to the parent directory and renames if the resulting filename
// differs from the desired local name.
func DownloadFile(sandboxName, remotePath, localPath string) error {
	destDir := filepath.Dir(localPath)
	downloadedPath := filepath.Join(destDir, filepath.Base(remotePath))

	os.Remove(downloadedPath)
	if err := Download(sandboxName, remotePath, destDir); err != nil {
		return err
	}
	if downloadedPath != localPath {
		return os.Rename(downloadedPath, localPath)
	}
	return nil
}

// SafeDownload copies a directory from a sandbox to the local machine and then
// sanitizes the result by removing dangerous symlinks (absolute or repo-escaping) and .git/hooks/.
func SafeDownload(sandboxName, remoteDir, localDir string) error {
	if err := Download(sandboxName, remoteDir, localDir); err != nil {
		return err
	}
	return sanitizeDownload(localDir)
}

// CollectLogs runs `openshell logs <name> --source <source> -n 0` and returns
// the log output. The -n 0 flag requests all available log lines (no limit).
// This is a host-side command that talks to the gateway — no SSH needed.
func CollectLogs(name, source string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "openshell", "logs", name, "--source", source, "-n", "0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("openshell logs %q --source %s timed out after 30s", name, source)
		}
		return "", fmt.Errorf("openshell logs %q --source %s: %s", name, source, string(out))
	}
	return string(out), nil
}

const (
	podmanLogTimeout   = 15 * time.Second
	maxContainerLogs   = 1 << 20 // 1 MB
	podmanLogTailLines = "200"
)

// collectPodmanLogs gathers recent container logs for diagnostics when a
// sandbox fails to become ready. Filters by sandbox name prefix, caps
// per-container output with --tail, and limits total size.
func collectPodmanLogs(sandboxName string) string {
	if _, err := exec.LookPath("podman"); err != nil {
		return "(podman not available on this host)"
	}

	ctx, cancel := context.WithTimeout(context.Background(), podmanLogTimeout)
	defer cancel()

	listCmd := exec.CommandContext(ctx, "podman", "ps", "-a",
		"--filter", "name="+sandboxName,
		"--format", "{{.Names}}")
	listOut, listErr := listCmd.Output()
	if listErr != nil {
		return fmt.Sprintf("(podman ps failed: %v)", listErr)
	}

	names := strings.TrimSpace(string(listOut))
	if names == "" {
		return "(no matching containers)"
	}

	var b strings.Builder
	for _, cname := range strings.Split(names, "\n") {
		cname = strings.TrimSpace(cname)
		if cname == "" {
			continue
		}
		logCmd := exec.CommandContext(ctx, "podman", "logs", "--tail", podmanLogTailLines, cname)
		logOut, logErr := logCmd.CombinedOutput()
		if logErr != nil {
			chunk := fmt.Sprintf("=== %s === (log collection failed: %v)\n", cname, logErr)
			if b.Len()+len(chunk) > maxContainerLogs {
				b.WriteString("... (truncated)\n")
				break
			}
			b.WriteString(chunk)
			continue
		}
		chunk := fmt.Sprintf("=== %s ===\n%s\n", cname, string(logOut))
		if b.Len()+len(chunk) > maxContainerLogs {
			b.WriteString("... (truncated)\n")
			break
		}
		b.WriteString(chunk)
	}
	return b.String()
}

// ExtractOutputFiles copies all files under a remote directory in the sandbox
// to a local output directory, preserving relative paths.
func ExtractOutputFiles(sandboxName, remoteDir, localDir string) ([]string, error) {
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating local output dir: %w", err)
	}

	root, err := os.OpenRoot(localDir)
	if err != nil {
		return nil, fmt.Errorf("opening output root: %w", err)
	}
	defer root.Close()

	stdout, _, _, err := Exec(sandboxName,
		fmt.Sprintf("find %s -type f 2>/dev/null || true", remoteDir),
		10*time.Second,
	)
	if err != nil {
		return nil, fmt.Errorf("listing output files: %w", err)
	}

	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return nil, nil
	}
	lines := strings.Split(trimmed, "\n")

	var extracted []string
	for _, remotePath := range lines {
		remotePath = strings.TrimSpace(remotePath)
		if remotePath == "" {
			continue
		}
		relPath := strings.TrimPrefix(remotePath, remoteDir)
		relPath = strings.TrimPrefix(relPath, "/")

		if dir := filepath.Dir(relPath); dir != "." {
			if mkErr := root.MkdirAll(dir, 0o755); mkErr != nil {
				fmt.Fprintf(os.Stderr, "  Skipping (dir rejected): %s: %v\n", relPath, mkErr)
				continue
			}
		}

		// Validate path stays within localDir (kernel-enforced), then remove
		// the probe file so DownloadFile can write the actual content.
		f, createErr := root.Create(relPath)
		if createErr != nil {
			fmt.Fprintf(os.Stderr, "  Skipping (path rejected): %s: %v\n", relPath, createErr)
			continue
		}
		f.Close()

		localPath := filepath.Join(localDir, relPath)
		os.Remove(localPath)

		if dlErr := DownloadFile(sandboxName, remotePath, localPath); dlErr != nil {
			fmt.Fprintf(os.Stderr, "  Failed to copy %s: %v\n", relPath, dlErr)
			continue
		}
		extracted = append(extracted, localPath)
	}

	return extracted, nil
}
