# OpenShell Native Sandbox Transport Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace SSH/SCP/rsync `exec.Command` wrappers in `internal/sandbox/` with OpenShell native CLI commands and `os.Root` containment for local writes.

**Architecture:** The sandbox package's public API changes from `SSH(sshConfigPath, sandboxName, ...)` to `Exec(sandboxName, ...)` — the `sshConfigPath` parameter is removed from all functions. Transport uses `openshell sandbox exec/upload/download` instead of `ssh`/`scp`/`rsync`. Local write containment uses `os.Root` (Go 1.24+). A post-download `sanitizeDownload` function removes symlinks and `.git/hooks/` to replace `rsync --no-links --exclude .git/hooks/`.

**Tech Stack:** Go 1.26, OpenShell CLI, `os.Root` (stdlib)

---

## File Structure

| File | Role |
|---|---|
| `internal/sandbox/sandbox.go` | Replace SSH/SCP/rsync functions with OpenShell native equivalents; add `sanitizeDownload`; update `ExtractTranscripts`/`ExtractOutputFiles` to use `os.Root` |
| `internal/sandbox/sandbox_test.go` | Tests for `sanitizeDownload`, `os.Root` containment, updated path traversal tests |
| `internal/cli/run.go` | Remove `sshConfigPath` plumbing; update all call sites to new sandbox API |

---

### Task 1: Add `sanitizeDownload` with tests

This is a standalone function with no dependencies on the migration — build and test it first.

**Files:**
- Modify: `internal/sandbox/sandbox.go`
- Modify: `internal/sandbox/sandbox_test.go`

- [ ] **Step 1: Write failing tests for `sanitizeDownload`**

Add to `internal/sandbox/sandbox_test.go`:

```go
func TestSanitizeDownload_RemovesSymlinks(t *testing.T) {
	dir := t.TempDir()

	// Create a regular file.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "real.txt"), []byte("ok"), 0o644))

	// Create a symlink (dangling is fine — we just need it to exist).
	require.NoError(t, os.Symlink("/nonexistent/target", filepath.Join(dir, "danger")))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	// Regular file should survive.
	_, err = os.Stat(filepath.Join(dir, "real.txt"))
	assert.NoError(t, err)

	// Symlink should be removed.
	_, err = os.Lstat(filepath.Join(dir, "danger"))
	assert.True(t, os.IsNotExist(err), "symlink should have been removed")
}

func TestSanitizeDownload_RemovesGitHooks(t *testing.T) {
	dir := t.TempDir()

	// Create .git/hooks/ with a script.
	hooksDir := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte("#!/bin/sh\nmalicious"), 0o755))

	// Create a safe file under .git/.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[core]"), 0o644))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	// .git/hooks/ should be removed entirely.
	_, err = os.Stat(hooksDir)
	assert.True(t, os.IsNotExist(err), ".git/hooks/ should have been removed")

	// .git/config should survive.
	_, err = os.Stat(filepath.Join(dir, ".git", "config"))
	assert.NoError(t, err)
}

func TestSanitizeDownload_NestedSymlinks(t *testing.T) {
	dir := t.TempDir()

	// Create nested structure with symlinks at various depths.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a", "b", "real.txt"), []byte("ok"), 0o644))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(dir, "a", "b", "link")))
	require.NoError(t, os.Symlink("/etc/shadow", filepath.Join(dir, "a", "top-link")))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	// Real file survives.
	_, err = os.Stat(filepath.Join(dir, "a", "b", "real.txt"))
	assert.NoError(t, err)

	// Both symlinks removed.
	_, err = os.Lstat(filepath.Join(dir, "a", "b", "link"))
	assert.True(t, os.IsNotExist(err))
	_, err = os.Lstat(filepath.Join(dir, "a", "top-link"))
	assert.True(t, os.IsNotExist(err))
}

func TestSanitizeDownload_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	err := sanitizeDownload(dir)
	assert.NoError(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/sandbox/ -run 'TestSanitizeDownload' -v`
Expected: compilation error — `sanitizeDownload` not defined.

- [ ] **Step 3: Implement `sanitizeDownload`**

Add to `internal/sandbox/sandbox.go`, after the imports (add `"io/fs"` to imports):

```go
// sanitizeDownload walks a downloaded directory and removes symlinks and
// .git/hooks/ to prevent a compromised sandbox from injecting content into
// the host. Equivalent to rsync's --no-links and --exclude .git/hooks/.
func sanitizeDownload(localDir string) error {
	return filepath.WalkDir(localDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(localDir, path)

		if d.Type()&fs.ModeSymlink != 0 {
			return os.Remove(path)
		}

		if d.IsDir() && rel == filepath.Join(".git", "hooks") {
			os.RemoveAll(path)
			return filepath.SkipDir
		}

		return nil
	})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/sandbox/ -run 'TestSanitizeDownload' -v`
Expected: all 4 tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox/sandbox.go internal/sandbox/sandbox_test.go && git commit -m "feat(sandbox): add sanitizeDownload for symlink and git hooks cleanup"
```

---

### Task 2: Replace `SSH` with `Exec`

Replace the `SSH()` function that shells out to `ssh` with `Exec()` that uses `openshell sandbox exec`. The old `SSH` function is removed.

**Files:**
- Modify: `internal/sandbox/sandbox.go`
- Modify: `internal/sandbox/sandbox_test.go`

- [ ] **Step 1: Write failing test for `Exec`**

The existing codebase doesn't have integration tests for SSH (it requires a running sandbox). We'll add a unit test that verifies `Exec` constructs the right command when openshell is unavailable (same pattern as `TestEnsureAvailable_OpenshellNotInPath`).

Add to `internal/sandbox/sandbox_test.go`:

```go
func TestExec_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", "")

	_, _, _, err := Exec("test-sandbox", "echo hello", 10*time.Second)
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sandbox/ -run 'TestExec_OpenshellNotInPath' -v`
Expected: compilation error — `Exec` not defined.

- [ ] **Step 3: Implement `Exec` and remove `SSH`**

Replace the `SSH` function in `internal/sandbox/sandbox.go` with:

```go
// Exec runs a command inside a sandbox using openshell sandbox exec and returns
// stdout, stderr, and exit code. The timeout is in seconds.
func Exec(sandboxName, command string, timeout time.Duration) (stdout, stderr string, exitCode int, err error) {
	timeoutSecs := fmt.Sprintf("%d", int(timeout.Seconds()))

	cmd := exec.Command("openshell", "sandbox", "exec",
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
```

Remove the old `SSH` function (lines 197-227) and `GetSSHConfig` function (lines 167-173).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/sandbox/ -run 'TestExec' -v`
Expected: PASS.

- [ ] **Step 5: Run full sandbox tests**

Run: `go test ./internal/sandbox/ -v`
Expected: all tests pass. (Build may fail due to callers of the old `SSH` — that's expected and will be fixed in Task 5.)

- [ ] **Step 6: Commit**

```bash
git add internal/sandbox/sandbox.go internal/sandbox/sandbox_test.go && git commit -m "feat(sandbox): replace SSH with Exec using openshell sandbox exec"
```

---

### Task 3: Replace `SSHStream` and `SSHStreamReader` with `ExecStream` and `ExecStreamReader`

**Files:**
- Modify: `internal/sandbox/sandbox.go`

- [ ] **Step 1: Implement `ExecStream` replacing `SSHStream`**

Replace `SSHStream` (lines 229-257) in `internal/sandbox/sandbox.go` with:

```go
// ExecStream runs a command inside a sandbox, streaming output to the given writers.
func ExecStream(sandboxName, command string, timeout time.Duration, stdoutW, stderrW *os.File) (int, error) {
	timeoutSecs := fmt.Sprintf("%d", int(timeout.Seconds()))

	cmd := exec.Command("openshell", "sandbox", "exec",
		"--name", sandboxName,
		"--no-tty",
		"--timeout", timeoutSecs,
		"--", "sh", "-c", command,
	)
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	err := cmd.Run()
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	if err != nil && cmd.ProcessState == nil {
		return exitCode, fmt.Errorf("openshell exec failed to start: %w", err)
	}

	if exitCode == 124 {
		return exitCode, fmt.Errorf("command timed out after %s", timeout)
	}

	return exitCode, nil
}
```

- [ ] **Step 2: Implement `ExecStreamReader` replacing `SSHStreamReader`**

Replace `SSHStreamReader` (lines 259-284) with:

```go
// ExecStreamReader runs a command inside a sandbox, returning an io.ReadCloser for
// stdout so the caller can parse structured output. Stderr is forwarded to the
// given writer. The caller must read stdout to completion, then call cmd.Wait().
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
```

- [ ] **Step 3: Verify sandbox package compiles**

Run: `go build ./internal/sandbox/`
Expected: success.

- [ ] **Step 4: Run sandbox tests**

Run: `go test ./internal/sandbox/ -v`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox/sandbox.go && git commit -m "feat(sandbox): replace SSHStream/SSHStreamReader with ExecStream/ExecStreamReader"
```

---

### Task 4: Replace `SCP`, `SCPFrom`, and `RsyncFrom` with `Upload` and `Download`

**Files:**
- Modify: `internal/sandbox/sandbox.go`

- [ ] **Step 1: Implement `Upload` replacing `SCP`**

Replace `SCP` (lines 176-194) in `internal/sandbox/sandbox.go` with:

```go
// Upload copies a local file or directory into a sandbox using openshell sandbox upload.
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
```

- [ ] **Step 2: Implement `Download` replacing `SCPFrom`**

Replace `SCPFrom` (lines 322-340) with:

```go
// Download copies a file or directory from a sandbox to the local machine
// using openshell sandbox download.
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
```

- [ ] **Step 3: Implement `SafeDownload` replacing `RsyncFrom`**

Replace `RsyncFrom` (lines 286-319) with:

```go
// SafeDownload copies a directory from a sandbox to the local machine with
// security protections: symlinks are removed and .git/hooks/ is deleted after
// download. Replaces rsync --no-links --exclude .git/hooks/.
func SafeDownload(sandboxName, remoteDir, localDir string) error {
	if err := Download(sandboxName, remoteDir, localDir); err != nil {
		return err
	}
	return sanitizeDownload(localDir)
}
```

- [ ] **Step 4: Verify sandbox package compiles**

Run: `go build ./internal/sandbox/`
Expected: success.

- [ ] **Step 5: Run sandbox tests**

Run: `go test ./internal/sandbox/ -v`
Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/sandbox/sandbox.go && git commit -m "feat(sandbox): replace SCP/SCPFrom/RsyncFrom with Upload/Download/SafeDownload"
```

---

### Task 5: Update `ExtractTranscripts` and `ExtractOutputFiles` to use new API + `os.Root`

These functions call `SSH` and `SCPFrom` internally. Update them to use `Exec` and `Download`, and replace `filepath.Clean` + `HasPrefix` with `os.Root`.

**Files:**
- Modify: `internal/sandbox/sandbox.go`
- Modify: `internal/sandbox/sandbox_test.go`

- [ ] **Step 1: Write failing test for `os.Root` containment**

Update `TestPathTraversalContainment` in `internal/sandbox/sandbox_test.go` to verify `os.Root` rejects traversal:

```go
func TestOsRootContainment(t *testing.T) {
	dir := t.TempDir()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer root.Close()

	// Normal file creation should work.
	f, err := root.Create("safe.txt")
	require.NoError(t, err)
	f.Close()

	// Path traversal should fail.
	_, err = root.Create("../../../etc/passwd")
	assert.Error(t, err)

	// Traversal with prefix should fail.
	_, err = root.Create("../../home/runner/.bashrc")
	assert.Error(t, err)

	// Dot segments in middle should fail.
	_, err = root.Create("subdir/../../etc/shadow")
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sandbox/ -run 'TestOsRootContainment' -v`
Expected: PASS (this test validates stdlib behavior, so it should pass immediately — the real migration test is that `ExtractTranscripts`/`ExtractOutputFiles` compile with the new API).

- [ ] **Step 3: Update `ExtractTranscripts`**

Replace the `ExtractTranscripts` function (lines 362-407) with:

```go
// ExtractTranscripts copies Claude transcript files (.jsonl) from the sandbox
// to a local output directory. Uses os.Root for path containment.
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

		// Use os.Root to create the file — kernel-enforced path containment.
		f, createErr := root.Create(localName)
		if createErr != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Skipping (path rejected): %s: %v\n", agentName, localName, createErr)
			continue
		}
		f.Close()

		localPath := filepath.Join(outputDir, localName)
		if scpErr := Download(sandboxName, remotePath, localPath); scpErr != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Failed to copy transcript: %v\n", agentName, scpErr)
			continue
		}
		fmt.Fprintf(os.Stderr, "  [%s] Saved transcript: %s\n", agentName, localName)
	}

	return nil
}
```

- [ ] **Step 4: Update `ExtractOutputFiles`**

Replace the `ExtractOutputFiles` function (lines 409-463) with:

```go
// ExtractOutputFiles copies all files under a remote directory in the sandbox
// to a local output directory, preserving relative paths. Uses os.Root for
// path containment.
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

		// Use os.Root to validate the path — kernel-enforced containment.
		f, createErr := root.Create(relPath)
		if createErr != nil {
			// os.Root rejects path traversal attempts.
			fmt.Fprintf(os.Stderr, "  Skipping (path rejected): %s: %v\n", relPath, createErr)
			continue
		}
		f.Close()

		localPath := filepath.Join(localDir, relPath)
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "  Failed to create dir for %s: %v\n", relPath, err)
			continue
		}

		if dlErr := Download(sandboxName, remotePath, localPath); dlErr != nil {
			fmt.Fprintf(os.Stderr, "  Failed to copy %s: %v\n", relPath, dlErr)
			continue
		}
		extracted = append(extracted, localPath)
	}

	return extracted, nil
}
```

- [ ] **Step 5: Remove old `TestPathTraversalContainment`**

The old test validates the `filepath.Clean` + `HasPrefix` pattern which is no longer used. Remove it from `internal/sandbox/sandbox_test.go` (the `TestOsRootContainment` test added in Step 1 replaces it).

- [ ] **Step 6: Verify sandbox package compiles**

Run: `go build ./internal/sandbox/`
Expected: success.

- [ ] **Step 7: Run sandbox tests**

Run: `go test ./internal/sandbox/ -v`
Expected: all tests pass.

- [ ] **Step 8: Commit**

```bash
git add internal/sandbox/sandbox.go internal/sandbox/sandbox_test.go && git commit -m "feat(sandbox): update ExtractTranscripts/ExtractOutputFiles to use Exec/Download and os.Root"
```

---

### Task 6: Clean up sandbox.go — remove dead imports and old functions

After Tasks 2-5, the old `SSH`, `SSHStream`, `SSHStreamReader`, `SCP`, `SCPFrom`, `RsyncFrom`, and `GetSSHConfig` functions should all be removed. Verify no dead code remains.

**Files:**
- Modify: `internal/sandbox/sandbox.go`

- [ ] **Step 1: Remove unused imports**

The `"context"` import is still needed by `ExecStreamReader`. Remove any imports that are no longer used. Run:

```bash
go build ./internal/sandbox/ 2>&1
```

If there are unused import errors, remove them. The following imports should remain:
- `"context"` — used by `ExecStreamReader`
- `"fmt"`
- `"io"` — used by `ExecStreamReader`
- `"io/fs"` — used by `sanitizeDownload`
- `"os"`
- `"os/exec"`
- `"path/filepath"`
- `"strings"`
- `"time"`

- [ ] **Step 2: Verify no references to old functions remain in the sandbox package**

Run: `grep -n 'func SSH\|func SCP\|func SCPFrom\|func RsyncFrom\|func GetSSHConfig\|func SSHStream' internal/sandbox/sandbox.go`
Expected: no output (all old functions removed).

- [ ] **Step 3: Run sandbox tests**

Run: `go test ./internal/sandbox/ -v`
Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/sandbox/sandbox.go && git commit -m "refactor(sandbox): remove dead imports and verify clean state"
```

---

### Task 7: Migrate `run.go` — remove SSH config plumbing and update all call sites

This is the largest task. All 38 `sshConfigPath` references in `run.go` need to be removed, and every `sandbox.SSH()`/`sandbox.SCP()`/etc. call updated to the new API.

**Files:**
- Modify: `internal/cli/run.go`

- [ ] **Step 1: Remove SSH config creation and cleanup from `runAgent`**

Remove lines 282-300 from `runAgent` (the `GetSSHConfig` + temp file creation + defer cleanup block):

```go
// DELETE this entire block:
// 4. Get SSH config.
sshConfig, err := sandbox.GetSSHConfig(sandboxName)
// ... through ...
defer os.Remove(sshConfigPath)
```

- [ ] **Step 2: Remove `sshConfigPath` parameter from internal functions**

Update function signatures — remove `sshConfigPath` from:

- `bootstrapSandbox(sshConfigPath, sandboxName, ...)` → `bootstrapSandbox(sandboxName, ...)`  (line 581)
- `bootstrapEnv(sshConfigPath, sandboxName, ...)` → `bootstrapEnv(sandboxName, ...)`  (line 711)
- `bootstrapSecurityHooks(sshConfigPath, sandboxName, ...)` → `bootstrapSecurityHooks(sandboxName, ...)`  (line 1154)
- `runAgentWithProgress(sshConfigPath, sandboxName, ...)` → `runAgentWithProgress(sandboxName, ...)`  (line 819)
- `injectTraceID(sshConfigPath, sandboxName, ...)` → `injectTraceID(sandboxName, ...)`  (line 1237)

- [ ] **Step 3: Update all `sandbox.SSH()` calls to `sandbox.Exec()`**

Replace every `sandbox.SSH(sshConfigPath, sandboxName, ...)` with `sandbox.Exec(sandboxName, ...)`. There are 12 call sites:

In `runAgent`:
- Line 323: `sandbox.SSH(sshConfigPath, sandboxName, mkRepoCmd, 10*time.Second)` → `sandbox.Exec(sandboxName, mkRepoCmd, 10*time.Second)`
- Line 338: `sandbox.SSH(sshConfigPath, sandboxName, mkInputCmd, 10*time.Second)` → `sandbox.Exec(sandboxName, mkInputCmd, 10*time.Second)`
- Line 380: `sandbox.SSH(sshConfigPath, sandboxName, scanCmd, 60*time.Second)` → `sandbox.Exec(sandboxName, scanCmd, 60*time.Second)`
- Line 443: `sandbox.SSH(sshConfigPath, sandboxName, clearCmd, 10*time.Second)` → `sandbox.Exec(sandboxName, clearCmd, 10*time.Second)`

In `bootstrapSandbox`:
- Line 588: `sandbox.SSH(sshConfigPath, sandboxName, mkdirCmd, 10*time.Second)` → `sandbox.Exec(sandboxName, mkdirCmd, 10*time.Second)`
- Line 608: `sandbox.SSH(sshConfigPath, sandboxName, chmodCmd, 10*time.Second)` → `sandbox.Exec(sandboxName, chmodCmd, 10*time.Second)`

In `bootstrapEnv`:
- Line 796: `sandbox.SSH(sshConfigPath, sandboxName, chmodCmd, 10*time.Second)` → `sandbox.Exec(sandboxName, chmodCmd, 10*time.Second)`

In `bootstrapSecurityHooks`:
- Line 1178: `sandbox.SSH(sshConfigPath, sandboxName, chmodCmd, 10*time.Second)` → `sandbox.Exec(sandboxName, chmodCmd, 10*time.Second)`
- Line 1218: `sandbox.SSH(sshConfigPath, sandboxName, envCmd, 10*time.Second)` → `sandbox.Exec(sandboxName, envCmd, 10*time.Second)`
- Line 1227: `sandbox.SSH(sshConfigPath, sandboxName, envCmd, 10*time.Second)` → `sandbox.Exec(sandboxName, envCmd, 10*time.Second)`

In `injectTraceID`:
- Line 1243: `sandbox.SSH(sshConfigPath, sandboxName, cmd, 10*time.Second)` → `sandbox.Exec(sandboxName, cmd, 10*time.Second)`

- [ ] **Step 4: Update all `sandbox.SCP()` calls to `sandbox.Upload()`**

Replace every `sandbox.SCP(sshConfigPath, sandboxName, local, remote)` with `sandbox.Upload(sandboxName, local, remote)`. There are 10 call sites:

In `runAgent`:
- Line 326: `sandbox.SCP(sshConfigPath, sandboxName, repoSrc+"/.", repoDir+"/")` → `sandbox.Upload(sandboxName, repoSrc+"/.", repoDir+"/")`
- Line 341: `sandbox.SCP(sshConfigPath, sandboxName, h.AgentInput+"/.", remoteInput+"/")` → `sandbox.Upload(sandboxName, h.AgentInput+"/.", remoteInput+"/")`

In `bootstrapSandbox`:
- Line 604: `sandbox.SCP(sshConfigPath, sandboxName, localBinary, remoteBinary)` → `sandbox.Upload(sandboxName, localBinary, remoteBinary)`
- Line 641: `sandbox.SCP(sshConfigPath, sandboxName, h.Agent, ...)` → `sandbox.Upload(sandboxName, h.Agent, ...)`
- Line 679: `sandbox.SCP(sshConfigPath, sandboxName, skillPath, ...)` → `sandbox.Upload(sandboxName, skillPath, ...)`

In `bootstrapEnv`:
- Line 740: `sandbox.SCP(sshConfigPath, sandboxName, tmpFile.Name(), remoteEnvFile)` → `sandbox.Upload(sandboxName, tmpFile.Name(), remoteEnvFile)`
- Line 778: `sandbox.SCP(sshConfigPath, sandboxName, tmp.Name(), hf.Dest)` → `sandbox.Upload(sandboxName, tmp.Name(), hf.Dest)`
- Line 784: `sandbox.SCP(sshConfigPath, sandboxName, hostPath, hf.Dest)` → `sandbox.Upload(sandboxName, hostPath, hf.Dest)`

In `bootstrapSecurityHooks`:
- Line 1170: `sandbox.SCP(sshConfigPath, sandboxName, tmpFile.Name(), remotePath)` → `sandbox.Upload(sandboxName, tmpFile.Name(), remotePath)`
- Line 1201: `sandbox.SCP(sshConfigPath, sandboxName, tmpSettings.Name(), remoteSettings)` → `sandbox.Upload(sandboxName, tmpSettings.Name(), remoteSettings)`

- [ ] **Step 5: Update `sandbox.SSHStreamReader()` to `sandbox.ExecStreamReader()`**

In `runAgentWithProgress` (line 820):

```go
// Before:
stdout, cmd, cancel, err := sandbox.SSHStreamReader(sshConfigPath, sandboxName, claudeCmd, timeout, os.Stderr)

// After:
stdout, cmd, cancel, err := sandbox.ExecStreamReader(ctx, sandboxName, claudeCmd, timeout, os.Stderr)
```

Also update the error message on line 839:

```go
// Before:
return exitCode, fmt.Errorf("ssh failed: %w", waitErr)

// After:
return exitCode, fmt.Errorf("openshell exec failed: %w", waitErr)
```

- [ ] **Step 6: Update `sandbox.RsyncFrom()` to `sandbox.SafeDownload()`**

In `runAgent` (line 504):

```go
// Before:
if err := sandbox.RsyncFrom(sshConfigPath, sandboxName, repoDir, repoSrc); err != nil {

// After:
if err := sandbox.SafeDownload(sandboxName, repoDir, repoSrc); err != nil {
```

- [ ] **Step 7: Update `sandbox.SCPFrom()` to `sandbox.Download()`**

In `runAgent` (line 550):

```go
// Before:
if scpErr := sandbox.SCPFrom(sshConfigPath, sandboxName, remoteFindingsDir, findingsDir); scpErr != nil {

// After:
if scpErr := sandbox.Download(sandboxName, remoteFindingsDir, findingsDir); scpErr != nil {
```

- [ ] **Step 8: Update `sandbox.ExtractOutputFiles()` and `sandbox.ExtractTranscripts()` calls**

These functions lost the `sshConfigPath` parameter. Update call sites:

Line 478:
```go
// Before:
extracted, extractErr := sandbox.ExtractOutputFiles(sshConfigPath, sandboxName, remoteSrc, iterOutputDir)

// After:
extracted, extractErr := sandbox.ExtractOutputFiles(sandboxName, remoteSrc, iterOutputDir)
```

Line 493:
```go
// Before:
if err := sandbox.ExtractTranscripts(sshConfigPath, sandboxName, agentName, iterTranscriptDir); err != nil {

// After:
if err := sandbox.ExtractTranscripts(sandboxName, agentName, iterTranscriptDir); err != nil {
```

- [ ] **Step 9: Update call sites for internal functions**

Update the calls to the refactored internal functions:

Line 313:
```go
// Before:
if err := bootstrapSandbox(sshConfigPath, sandboxName, repoDir, fullsendBinary, h); err != nil {

// After:
if err := bootstrapSandbox(sandboxName, repoDir, fullsendBinary, h); err != nil {
```

Line 370:
```go
// Before:
if err := injectTraceID(sshConfigPath, sandboxName, traceID); err != nil {

// After:
if err := injectTraceID(sandboxName, traceID); err != nil {
```

Line 457:
```go
// Before:
exitCode, runErr := runAgentWithProgress(sshConfigPath, sandboxName, claudeCmd, timeout, printer, agentStart, &metrics)

// After:
exitCode, runErr := runAgentWithProgress(sandboxName, claudeCmd, timeout, printer, agentStart, &metrics)
```

Line 686:
```go
// Before:
if err := bootstrapEnv(sshConfigPath, sandboxName, repoDir, h); err != nil {

// After:
if err := bootstrapEnv(sandboxName, repoDir, h); err != nil {
```

Line 692:
```go
// Before:
if err := bootstrapSecurityHooks(sshConfigPath, sandboxName, h); err != nil {

// After:
if err := bootstrapSecurityHooks(sandboxName, h); err != nil {
```

- [ ] **Step 10: Remove stale comments referencing SSH/SCP**

Update the comment on line 499 (above `RsyncFrom` call):

```go
// Before:
// 9d. Extract target repo back to host. Uses rsync with --no-links
// and --exclude .git/hooks/ to prevent sandbox escape via symlinks
// or injected git hooks.

// After:
// 9d. Extract target repo back to host. SafeDownload removes symlinks
// and .git/hooks/ after download to prevent sandbox escape.
```

Update the comment on line 646:

```go
// Before:
// Copy skills (SCP -r copies the entire directory tree, including any
// scripts/, references/, and assets/ bundled with the skill per the
// agentskills.io specification).

// After:
// Copy skills (Upload copies the entire directory tree, including any
// scripts/, references/, and assets/ bundled with the skill per the
// agentskills.io specification).
```

- [ ] **Step 11: Verify full project compiles**

Run: `go build ./...`
Expected: success — no compilation errors.

- [ ] **Step 12: Run all tests**

Run: `go test ./... 2>&1 | tail -30`
Expected: all tests pass.

- [ ] **Step 13: Run vet and lint**

Run: `make lint`
Expected: no issues.

- [ ] **Step 14: Commit**

```bash
git add internal/cli/run.go && git commit -m "refactor(cli): migrate run.go from SSH/SCP to openshell exec/upload/download

Remove sshConfigPath plumbing from all internal functions. Update 38
call sites to use the new sandbox.Exec/Upload/Download/SafeDownload API.
SSH config temp file creation and cleanup are no longer needed."
```

---

### Task 8: Final verification and cleanup

**Files:**
- All files in previous tasks

- [ ] **Step 1: Verify no references to old API remain**

Run:
```bash
grep -rn 'sandbox\.SSH\b\|sandbox\.SCP\b\|sandbox\.SCPFrom\|sandbox\.RsyncFrom\|sandbox\.SSHStream\|sandbox\.GetSSHConfig\|sshConfigPath' internal/ --include='*.go'
```
Expected: no output.

- [ ] **Step 2: Verify no references to ssh/scp/rsync binaries in sandbox package**

Run:
```bash
grep -n '"ssh"\|"scp"\|"rsync"' internal/sandbox/sandbox.go
```
Expected: no output.

- [ ] **Step 3: Run full test suite**

Run: `make go-test`
Expected: all tests pass.

- [ ] **Step 4: Run vet**

Run: `make go-vet`
Expected: clean.

- [ ] **Step 5: Run lint**

Run: `make lint`
Expected: clean.
