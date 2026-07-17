package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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
	providerTimeout = 30 * time.Second

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

// ImportProfile imports a single openshell provider profile from a YAML
// file. The profile defines a provider type schema (credentials, endpoints).
// To ensure content changes propagate on persistent gateways, the profile
// is deleted by id before re-importing (mirroring the ImportProfiles flow).
func ImportProfile(ctx context.Context, id, profilePath string) error {
	// Best-effort delete so content changes propagate (same pattern as ImportProfiles).
	delCtx, delCancel := context.WithTimeout(ctx, providerTimeout)
	exec.CommandContext(delCtx, "openshell", "provider", "profile", "delete", id).CombinedOutput() //nolint:errcheck
	delCancel()

	importCtx, importCancel := context.WithTimeout(ctx, providerTimeout)
	defer importCancel()
	cmd := exec.CommandContext(importCtx, "openshell", "provider", "profile", "import", profilePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		outStr := strings.ToLower(string(out))
		if strings.Contains(outStr, "already exists") {
			return nil
		}
		return fmt.Errorf("profile import %q failed: openshell: %w\noutput: %s", filepath.Base(profilePath), err, bytes.TrimSpace(out))
	}
	return nil
}

// reservedCredentialKeys are env var names that must not be used as provider
// credential keys. Credential keys become env vars in the openshell child
// process; allowing security-sensitive names would let a URL-fetched provider
// definition influence process loading or shell behavior.
// NOTE: see also reservedSandboxKeys in internal/cli/run.go for the sandbox env blocklist.
var reservedCredentialKeys = map[string]bool{
	// Process loading / shell behavior
	"PATH":            true,
	"HOME":            true,
	"SHELL":           true,
	"LD_PRELOAD":      true,
	"LD_LIBRARY_PATH": true,
	"BASH_ENV":        true,
	"ENV":             true,
	// Proxy — could redirect openshell HTTP traffic
	"HTTP_PROXY":  true,
	"HTTPS_PROXY": true,
	"NO_PROXY":    true,
	"ALL_PROXY":   true,
	// Subprocess-influencing runtime vars
	"NODE_OPTIONS":   true,
	"PYTHONPATH":     true,
	"PROMPT_COMMAND": true,
	// Shell / process behavior — IFS affects word splitting, CDPATH affects cd resolution
	"IFS":    true,
	"CDPATH": true,
	// Language runtime injection — can execute arbitrary code at process start
	"JAVA_TOOL_OPTIONS": true,
	"RUBYOPT":           true,
	"PERL5OPT":          true,
	"PYTHONSTARTUP":     true,
	// Dynamic linker audit — equivalent shared-object loading impact to LD_PRELOAD
	"LD_AUDIT": true,
	// macOS dynamic linker — low risk in Linux containers but blocked defensively
	"DYLD_INSERT_LIBRARIES": true,
	// TLS trust chain — could redirect certificate validation to attacker-controlled CA
	"SSL_CERT_FILE":       true,
	"SSL_CERT_DIR":        true,
	"CURL_CA_BUNDLE":      true,
	"NODE_EXTRA_CA_CERTS": true,
	// Hostname resolution redirect
	"HOSTALIASES": true,
	// Git config — could inject hooks or redirect operations
	"GIT_CONFIG_GLOBAL": true,
	"GIT_EXEC_PATH":     true,
	"GIT_SSH_COMMAND":   true,
	"GIT_TEMPLATE_DIR":  true,
	"GIT_ASKPASS":       true,
}

// EnsureProvider creates or updates a provider on the gateway. Credential
// values may contain ${VAR} references which are expanded from the host
// environment before being passed to openshell.
//
// Credentials use the bare-key form (--credential KEY) so that secret values
// never appear on the process command line. The expanded values are injected
// into the child process environment, where openshell reads them directly.
// See https://docs.nvidia.com/openshell/latest/sandboxes/manage-providers#bare-key-form
func EnsureProvider(ctx context.Context, name, providerType string, credentials, config map[string]string, fromURL bool) error {
	if fromURL {
		for k := range credentials {
			if reservedCredentialKeys[strings.ToUpper(k)] {
				return fmt.Errorf("provider %q: credential key %q is a reserved environment variable name", name, k)
			}
		}
	}

	args, extraEnv, secrets := buildProviderArgs(name, providerType, credentials, config, fromURL)

	createCtx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	cmd := exec.CommandContext(createCtx, "openshell", args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		outStr := string(out)
		// openshell emits: code: 'Some entity that we attempted to create already exists', message: "provider already exists"
		if strings.Contains(strings.ToLower(outStr), "provider already exists") {
			// Provider exists from a prior run — update it with current credentials.
			// Pass original ctx (not createCtx) so updateProvider gets a fresh timeout.
			return updateProvider(ctx, name, credentials, config, extraEnv, secrets, fromURL)
		}
		// Redact known credential values from error output.
		for _, s := range secrets {
			outStr = strings.ReplaceAll(outStr, s, "***")
		}
		return fmt.Errorf("provider create %q failed: %w (output: %s)", name, err, outStr)
	}
	return nil
}

// updateProvider runs openshell provider update for an already-existing provider.
func updateProvider(ctx context.Context, name string, credentials, config map[string]string, extraEnv, secrets []string, fromURL bool) error {
	args := buildProviderUpdateArgs(name, credentials, config, fromURL)
	ctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "openshell", args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		outStr := string(out)
		for _, s := range secrets {
			outStr = strings.ReplaceAll(outStr, s, "***")
		}
		return fmt.Errorf("provider update %q failed: %w (output: %s)", name, err, outStr)
	}
	return nil
}

// buildProviderUpdateArgs constructs CLI args for openshell provider update.
// The update subcommand takes a positional name (not --name/--type).
func buildProviderUpdateArgs(name string, credentials, config map[string]string, fromURL bool) []string {
	args := []string{"provider", "update", name}
	credKeys := sortedKeys(credentials)
	for _, k := range credKeys {
		expanded := os.ExpandEnv(credentials[k])
		if expanded != "" {
			args = append(args, "--credential", k)
		} else {
			// Empty value: use inline KEY= form to avoid env var lookup.
			// See https://github.com/NVIDIA/OpenShell/issues/1978
			args = append(args, "--credential", k+"=")
		}
	}
	cfgKeys := sortedKeys(config)
	for _, k := range cfgKeys {
		v := config[k]
		if !fromURL {
			v = os.ExpandEnv(v)
		}
		args = append(args, "--config", k+"="+v)
	}
	return args
}

// buildProviderArgs constructs the CLI args and child environment entries for
// openshell provider create. Credentials use the bare-key form (--credential KEY)
// so secret values never appear on the process command line. The expanded values
// are returned as extra env vars to be set on the child process.
// See https://docs.nvidia.com/openshell/latest/sandboxes/manage-providers#bare-key-form
func buildProviderArgs(name, providerType string, credentials, config map[string]string, fromURL bool) (args, extraEnv, secrets []string) {
	args = []string{"provider", "create",
		"--name", name,
		"--type", providerType,
	}

	credKeys := sortedKeys(credentials)
	for _, k := range credKeys {
		expanded := os.ExpandEnv(credentials[k])
		if expanded != "" {
			secrets = append(secrets, expanded)
			extraEnv = append(extraEnv, fmt.Sprintf("%s=%s", k, expanded))
			args = append(args, "--credential", k)
		} else {
			// Empty value: use inline KEY= form to avoid env var lookup.
			// See https://github.com/NVIDIA/OpenShell/issues/1978
			args = append(args, "--credential", k+"=")
		}
	}
	cfgKeys := sortedKeys(config)
	for _, k := range cfgKeys {
		v := config[k]
		if !fromURL {
			v = os.ExpandEnv(v)
		}
		args = append(args, "--config", k+"="+v)
	}

	return args, extraEnv, secrets
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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

// ImportProfiles imports provider profile YAMLs from a directory into the
// gateway via openshell provider profile import. If the directory does not
// exist, this is a no-op. This allows callers to import profiles from optional
// directories without checking existence first.
//
// Idempotency is hash-based: the function computes a SHA-256 digest of the
// profile directory contents and compares it against a cached value in a temp
// file. When the hash matches (profiles unchanged), the import is skipped
// entirely. This makes parallel fullsend run invocations safe — only the
// first process imports, and subsequent processes see the cache hit.
//
// When profiles have changed (hash mismatch or no cache), existing profiles
// are deleted and reimported. If the reimport fails because a parallel process
// already imported them, the error is treated as success.
func ImportProfiles(dir string) error {
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("checking profiles directory %s: %w", dir, err)
	}

	currentHash, err := hashProfileDir(dir)
	if err != nil {
		return fmt.Errorf("hashing profiles directory %s: %w", dir, err)
	}

	cachePath := profileCachePath(dir)
	if cached, readErr := os.ReadFile(cachePath); readErr == nil {
		if strings.TrimSpace(string(cached)) == currentHash {
			return nil
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading profiles directory %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || (!strings.HasSuffix(e.Name(), ".yaml") && !strings.HasSuffix(e.Name(), ".yml")) {
			continue
		}
		id, err := profileIDFromFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return fmt.Errorf("reading profile id from %s: %w", e.Name(), err)
		}
		// Best-effort delete; ignore errors (profile may not exist yet).
		ctx, cancel := context.WithTimeout(context.Background(), providerTimeout)
		exec.CommandContext(ctx, "openshell", "provider", "profile", "delete", id).CombinedOutput() //nolint:errcheck
		cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), providerTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "openshell", "provider", "profile", "import", "--from", dir).CombinedOutput()
	if err != nil {
		outStr := strings.ToLower(string(out))
		if strings.Contains(outStr, "already exists") {
			// A parallel process imported the profiles — safe to continue.
			os.WriteFile(cachePath, []byte(currentHash), 0o600) //nolint:errcheck
			return nil
		}
		return fmt.Errorf("provider profile import from %s failed: %w (output: %s)", dir, err, strings.TrimSpace(string(out)))
	}
	os.WriteFile(cachePath, []byte(currentHash), 0o600) //nolint:errcheck
	return nil
}

// profileIDFromFile reads a profile YAML file and returns its id field.
func profileIDFromFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var prof struct {
		ID string `yaml:"id"`
	}
	if err := yaml.Unmarshal(data, &prof); err != nil {
		return "", fmt.Errorf("parsing YAML: %w", err)
	}
	if prof.ID == "" {
		return "", fmt.Errorf("profile has no id field")
	}
	return prof.ID, nil
}

// hashProfileDir computes a deterministic SHA-256 digest of all YAML files in
// a directory. The digest covers both filenames and file contents so that any
// change to any profile is detected.
func hashProfileDir(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, e := range entries {
		if e.IsDir() || (!strings.HasSuffix(e.Name(), ".yaml") && !strings.HasSuffix(e.Name(), ".yml")) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return "", err
		}
		fileHash := sha256.Sum256(data)
		fmt.Fprintf(h, "%s:%x\n", e.Name(), fileHash)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// profileCachePath returns a temp file path for caching the profile directory
// hash. The path is keyed to the absolute directory path so that different
// fullsend-dir values get separate caches.
func profileCachePath(dir string) string {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}
	dirHash := sha256.Sum256([]byte(absDir))
	return filepath.Join(os.TempDir(), "fullsend-profiles-"+hex.EncodeToString(dirHash[:8])+".sha256")
}

// EnableProvidersV2 enables the providers_v2_enabled setting globally in the
// openshell gateway. This is idempotent and can be called multiple times.
func EnableProvidersV2() error {
	ctx, cancel := context.WithTimeout(context.Background(), providerTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "openshell", "settings", "set", "--key", "providers_v2_enabled", "--value", "true", "--global", "--yes").CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to enable providers_v2: %w (output: %s)", err, strings.TrimSpace(string(out)))
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
// It uses context.Background() internally. Use ExecContext for cancellation support.
func Exec(sandboxName, command string, timeout time.Duration) (stdout, stderr string, exitCode int, err error) {
	return ExecContext(context.Background(), sandboxName, command, timeout)
}

// ExecContext is like Exec but accepts a parent context for cancellation.
// Cancelling the parent (e.g. on SIGTERM) terminates the subprocess.
func ExecContext(ctx context.Context, sandboxName, command string, timeout time.Duration) (stdout, stderr string, exitCode int, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout+10*time.Second)
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
//
// The parent context is used as the base for the timeout context, so
// cancelling the parent (e.g. on SIGTERM) terminates the subprocess. This
// allows CLI-level signal handling to propagate into long-running sandbox
// commands.
func ExecStreamReader(ctx context.Context, sandboxName, command string, timeout time.Duration, stderrW io.Writer) (io.ReadCloser, *exec.Cmd, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
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

// isDirlikeSymlink reports whether info describes a symlink that should be
// routed to the tar-based upload path: one that resolves to a directory, or
// one that can't be resolved at all — missing target, a symlink loop, or a
// permission error walking to it — so the failure surfaces loudly from
// tar's -C rather than silently uploading a content-less symlink entry. A
// symlink that resolves to a regular file returns false so it uploads like
// any other file.
func isDirlikeSymlink(info os.FileInfo, path string) bool {
	if info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	target, err := os.Stat(path)
	if err != nil {
		return true // unresolvable: let UploadDir's tar -C surface a clear error
	}
	return target.IsDir()
}

// Upload copies a local file or directory into a sandbox. Directories, and
// symlinks that resolve to a directory, are transferred via UploadDir's tar
// archive so that directory contents — including symlinked sources such as
// cache-resolved skill or plugin paths (see CacheNamedSymlink) — survive the
// transfer. openshell's own "sandbox upload" archives only the symlink entry
// when the source itself is a symlink, silently dropping the target's
// content; delegating to the tar path here closes that gap for every caller
// instead of relying on each one to remember to call UploadDir directly.
// Regular files are uploaded directly, unchanged from before. A symlink that
// resolves to a regular file is resolved to its real path before upload, so
// correctness doesn't depend on whether openshell's own "sandbox upload"
// dereferences a symlink argument. A symlink whose target is missing is
// still routed to the tar path so it fails loudly (tar's -C can't chdir into
// it) rather than silently uploading a content-less link.
func Upload(sandboxName, localPath, remotePath string) error {
	// "dir/." means "copy the contents of dir into remotePath" rather than
	// wrapping them under a subdirectory named after dir's basename — always
	// a directory operation, regardless of what dir itself resolves to.
	if strings.HasSuffix(localPath, "/.") {
		local := strings.TrimSuffix(localPath, "/.")
		return UploadDir(sandboxName, local, strings.TrimSuffix(remotePath, "/"))
	}

	info, statErr := os.Lstat(localPath)
	if statErr == nil && (info.IsDir() || isDirlikeSymlink(info, localPath)) {
		dest := remotePath
		if strings.HasSuffix(remotePath, "/") {
			dest = strings.TrimSuffix(remotePath, "/") + "/" + filepath.Base(localPath)
		}
		return UploadDir(sandboxName, localPath, dest)
	}

	// Regular file, or a path that doesn't exist locally, in which case
	// openshell's own error message is the more useful diagnostic.
	uploadPath := localPath
	if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		// isDirlikeSymlink already established this resolves to a regular
		// file. Resolve it ourselves and hand openshell the real path,
		// rather than relying on its own (unverified) dereference behavior
		// for a symlink argument.
		resolved, err := filepath.EvalSymlinks(localPath)
		if err != nil {
			return fmt.Errorf("resolving symlink %q: %w", localPath, err)
		}
		uploadPath = resolved
	}

	ctx, cancel := context.WithTimeout(context.Background(), transferTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "openshell", "sandbox", "upload",
		sandboxName,
		uploadPath,
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

const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// randStringBytes is a helper to generate random strings
// for the temporal file created by UploadFile
func randStringBytes(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = letterBytes[rand.IntN(len(letterBytes))]
	}
	return string(b)
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

		tmpPath := fmt.Sprintf("/tmp/fs-upload-%s", randStringBytes(10))
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

// UploadDir uploads the contents of a local directory into a sandbox,
// preserving symlinks. It builds a local tar archive (tar preserves symlinks
// by default), uploads it, and extracts it in the sandbox at remotePath.
// localPath itself may be a symlink to a directory — tar's -C follows it via
// chdir(2) before archiving. Upload delegates here automatically whenever its
// source is a directory or a symlink; call this directly only when you
// already know the exact destination directory you want.
//
// remotePath is fully owned and replaced by this call: any existing content
// there is removed before extraction, so two uploads that resolve to the
// same remotePath overwrite deterministically rather than merging files
// from both. Do not point two different, unrelated sources at the same
// remotePath expecting their content to coexist.
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

	// Include the local tarball's own random suffix (from os.CreateTemp) in
	// the remote name, instead of a name keyed only on sandboxName, so
	// concurrent or partially-failed UploadDir calls against the same
	// sandbox are far less likely to collide on a fixed path. This is a
	// collision-reduction measure, not a cryptographic uniqueness guarantee.
	remoteTar := fmt.Sprintf("/tmp/fs-upload-%s-%s", sandboxName, filepath.Base(tmpPath))
	if err := Upload(sandboxName, tmpPath, remoteTar); err != nil {
		return err
	}

	// rm -rf the destination first so a basename collision between two
	// uploads (e.g. two skills resolving to the same directory name)
	// overwrites deterministically instead of merging files from both.
	// remotePath and remoteTar are shell-quoted because remotePath's
	// basename may originate from a fetched skill/plugin name.
	extractCmd := fmt.Sprintf("rm -rf %s && mkdir -p %s && tar -xzf %s -C %s && rm %s",
		shellQuote(remotePath), shellQuote(remotePath), shellQuote(remoteTar), shellQuote(remotePath), shellQuote(remoteTar))
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
