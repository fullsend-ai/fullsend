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
	SandboxWorkspace = "/tmp/workspace" //nolint:gosec // not a credential
	// SandboxClaudeConfig is the Claude config directory inside the sandbox.
	SandboxClaudeConfig = "/tmp/claude-config" //nolint:gosec // not a credential

	createTimeout   = 65 * time.Second
	readyTimeout    = 60 * time.Second
	readyPoll       = 2 * time.Second
	transferTimeout = 5 * time.Minute
)

func sanitizeDownload(localDir string) error {
	return filepath.WalkDir(localDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return os.Remove(path)
		}

		if d.IsDir() && d.Name() == "hooks" && filepath.Base(filepath.Dir(path)) == ".git" {
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("removing .git/hooks: %w", err)
			}
			return filepath.SkipDir
		}

		return nil
	})
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

// EnsureGateway starts a local gateway if none is active. It is idempotent —
// if a gateway is already running the command is a no-op.
func EnsureGateway() error {
	// Check if a gateway is already active.
	check := exec.Command("openshell", "gateway", "info")
	if err := check.Run(); err == nil {
		return nil
	}

	cmd := exec.Command("openshell", "gateway", "start")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gateway start failed: %s", string(out))
	}
	return nil
}

// Create creates a persistent OpenShell sandbox and waits for it to be ready.
// If providers are given, they are passed as --provider flags. If image is
// non-empty, it is passed as --from to start the sandbox from a container image.
// If policy is non-empty, it is applied at creation time via --policy.
func Create(name string, providers []string, image, policy string) error {
	ctx, cancel := context.WithTimeout(context.Background(), createTimeout)
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
		check := exec.Command("openshell", "sandbox", "get", name)
		if checkErr := check.Run(); checkErr != nil {
			return fmt.Errorf("sandbox create failed: %s", string(out))
		}
	}

	// Wait for sandbox to be fully ready (image pull can take a while).
	deadline := time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		check := exec.Command("openshell", "sandbox", "get", name)
		output, checkErr := check.Output()
		if checkErr == nil && strings.Contains(string(output), "Ready") {
			return nil
		}
		time.Sleep(readyPoll)
	}

	return fmt.Errorf("sandbox %q not ready after %s", name, readyTimeout)
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

// RestoreSymlinks recreates git-tracked symlinks after a sandbox upload.
// openshell sandbox upload dereferences symlinks into real directories;
// this reads the git index for mode-120000 entries and recreates them.
// See https://github.com/NVIDIA/OpenShell/issues/1425
func RestoreSymlinks(sandboxName, repoDir string) error {
	// Shell-escape repoDir to prevent injection
	escapedDir := strings.ReplaceAll(repoDir, "'", "'\\''")

	// Use tab as field separator to handle paths with spaces correctly.
	// git ls-files --stage format: <mode> <sha> <stage>\t<path>
	// We split the first field on spaces to extract mode and sha, then use tab to get the full path.
	cmd := fmt.Sprintf(
		`set -euo pipefail; cd '%s' && git ls-files --stage | awk -F'\t' '/^120000 /{split($1,a," "); print a[2]"\t"$2}' | while IFS=$'\t' read -r sha path; do [ -n "$path" ] || continue; target=$(git cat-file blob "$sha"); rm -rf "$path"; mkdir -p "$(dirname "$path")"; ln -s "$target" "$path"; done`,
		escapedDir,
	)
	_, stderr, exitCode, err := Exec(sandboxName, cmd, 60*time.Second)
	if err != nil {
		return fmt.Errorf("restoring symlinks in sandbox %q: %w", sandboxName, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("restoring symlinks in sandbox %q: exit %d: %s", sandboxName, exitCode, stderr)
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
// sanitizes the result by removing symlinks and .git/hooks/.
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

// ExtractTranscripts copies Claude transcript files (.jsonl) from the sandbox
// to a local output directory.
func ExtractTranscripts(sandboxName, agentName, outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}

	root, err := os.OpenRoot(outputDir)
	if err != nil {
		return fmt.Errorf("opening output root: %w", err)
	}
	defer root.Close()

	stdout, _, _, err := Exec(sandboxName,
		fmt.Sprintf("find %s -name '*.jsonl' 2>/dev/null || true", SandboxClaudeConfig),
		10*time.Second,
	)
	if err != nil {
		return fmt.Errorf("finding transcripts: %w", err)
	}

	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		fmt.Fprintf(os.Stderr, "  [%s] No transcripts found\n", agentName)
		return nil
	}
	files := strings.Split(trimmed, "\n")

	for _, remotePath := range files {
		remotePath = strings.TrimSpace(remotePath)
		if remotePath == "" {
			continue
		}
		localName := fmt.Sprintf("%s-%s", agentName, filepath.Base(remotePath))

		// Validate path stays within outputDir (kernel-enforced), then remove
		// the probe file so DownloadFile can write the actual content.
		f, createErr := root.Create(localName)
		if createErr != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Skipping (path rejected): %s: %v\n", agentName, localName, createErr)
			continue
		}
		f.Close()

		localPath := filepath.Join(outputDir, localName)
		os.Remove(localPath)
		if dlErr := DownloadFile(sandboxName, remotePath, localPath); dlErr != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Failed to copy transcript: %v\n", agentName, dlErr)
			continue
		}
		fmt.Fprintf(os.Stderr, "  [%s] Saved transcript: %s\n", agentName, localName)
	}

	return nil
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
