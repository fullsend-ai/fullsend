# OpenShell Native Sandbox Transport

Replace `exec.Command` SSH/SCP/rsync wrappers in `internal/sandbox/` with OpenShell's native CLI commands (`sandbox exec`, `sandbox upload`, `sandbox download`) and add `os.Root` containment for local writes.

Addresses [#261](https://github.com/fullsend-ai/fullsend/issues/261).

## Motivation

The sandbox package shells out to `ssh`, `scp`, and `rsync` via `exec.Command` for all sandbox communication. This creates several defensive-workaround classes:

- **Path traversal**: `scp -r` follows remote directory structure blindly; containment requires manual `filepath.Clean` + `HasPrefix` at every extraction site.
- **Symlink following**: `scp -r` follows symlinks by default; `rsync --no-links` is used for write-back but not all transfers.
- **No native timeout**: `SCP`/`SCPFrom` rely on `exec.CommandContext` for timeout; no per-operation deadline support.
- **ProcessState nil panics**: if the process fails to start, `cmd.ProcessState` is nil; every call site needs a nil guard.

OpenShell already provides native CLI commands that use gRPC internally, eliminating the need for SSH entirely.

## Approach

**Hybrid: OpenShell native CLI for transport + `os.Root` for local write containment.**

### Why not Go-native SSH/SFTP libraries?

`x/crypto/ssh` + `github.com/pkg/sftp` would eliminate subprocesses entirely, but:

- OpenShell's `exec`/`upload`/`download` commands already use gRPC internally — we'd be reimplementing what they provide.
- Parsing SSH config to extract host/port/key adds complexity; OpenShell handles connection routing internally.
- Two new dependencies for functionality that already exists in the tool we depend on.

### Why not OpenShell CLI alone (without `os.Root`)?

Testing confirmed that `openshell sandbox download` preserves symlinks as-is on the host (e.g., a sandbox symlink to `/etc/passwd` becomes a local symlink to `/etc/passwd`). While less dangerous than `scp -r` (which follows and copies the target content), a symlink pointing to a valid host path could still be exploited. `os.Root` provides kernel-level path containment that eliminates this class of issue, including TOCTOU races that `filepath.Clean` + `HasPrefix` cannot prevent.

## Validated Assumptions

Tested against a live OpenShell sandbox:

| Capability | Verified behavior |
|---|---|
| `sandbox exec` stdout piping | Streams line-by-line; NDJSON parsing works via `exec.Command` + `StdoutPipe()` |
| `sandbox exec` exit codes | Remote exit code propagated; timeout returns exit code 124 |
| `sandbox exec` timeout | `--timeout <seconds>` works; kills the remote process |
| `sandbox exec` newlines | Command *arguments* cannot contain newlines; `sh -c 'single string'` works (matches current usage) |
| `sandbox upload` directory semantics | Copies *contents* of source into destination (matches `scp -r src/. dest/` pattern) |
| `sandbox upload` single file | Works as expected |
| `sandbox download` symlinks | Preserves symlinks as-is — does **not** follow them, but creates them locally |

## Design

### Functions replaced

| Current function | Replacement | Notes |
|---|---|---|
| `SSH()` | `Exec()` | Uses `openshell sandbox exec --no-tty --timeout <s>`. Captures stdout/stderr via `exec.Command`. |
| `SSHStream()` | `ExecStream()` | Same as `Exec()` but wires stdout/stderr to provided writers. |
| `SSHStreamReader()` | `ExecStreamReader()` | Uses `StdoutPipe()` on the `openshell sandbox exec` command. Returns `io.ReadCloser` + `*exec.Cmd` + `context.CancelFunc`. |
| `SCP()` | `Upload()` | Uses `openshell sandbox upload <name> <local> <remote>`. |
| `SCPFrom()` | `Download()` | Uses `openshell sandbox download <name> <remote> <local>`. |
| `RsyncFrom()` | `Download()` + post-download cleanup | Download replaces rsync. Symlink and `.git/hooks/` protections move to local post-processing (see below). |
| `GetSSHConfig()` | Removed | No longer needed — OpenShell handles connection routing. |

### Caller migration (`internal/cli/run.go`)

15 call sites in `run.go` reference the old functions. Each maps directly:

- 10× `SCP()` → `Upload()` — bootstrap steps (repo, agent binary, skills, env, settings, host files)
- 1× `SSHStreamReader()` → `ExecStreamReader()` — agent progress tracking
- 1× `RsyncFrom()` → `Download()` + symlink cleanup — repo extraction
- 1× `SCPFrom()` → `Download()` — findings extraction
- 2× `SSH()` → `Exec()` — called indirectly via `ExtractTranscripts` and `ExtractOutputFiles`

The SSH config file creation/cleanup in `run.go` (lines ~289-300) is also removed.

### Local write containment with `os.Root`

`ExtractTranscripts()` and `ExtractOutputFiles()` currently use `filepath.Clean` + `strings.HasPrefix` to prevent path traversal from sandbox-controlled filenames. This is replaced with `os.Root`:

```go
root, err := os.OpenRoot(outputDir)
if err != nil {
    return fmt.Errorf("opening root dir: %w", err)
}
defer root.Close()

// All file operations go through root — kernel-enforced containment.
f, err := root.Create(relativePath)
```

This eliminates:
- TOCTOU races between the check and the file operation
- Manual `filepath.Clean` + `HasPrefix` at each call site
- The possibility of a missed check when adding new extraction code

### Post-download symlink and hooks cleanup

`RsyncFrom()` currently uses `--no-links` and `--exclude .git/hooks/` to prevent a compromised sandbox from injecting content. Since `openshell sandbox download` preserves symlinks, we add post-download cleanup:

```go
func sanitizeDownload(localDir string) error {
    return filepath.WalkDir(localDir, func(path string, d fs.DirEntry, err error) error {
        if err != nil {
            return err
        }
        rel, _ := filepath.Rel(localDir, path)

        // Remove symlinks (equivalent to rsync --no-links).
        if d.Type()&fs.ModeSymlink != 0 {
            return os.Remove(path)
        }

        // Remove .git/hooks/ contents (equivalent to rsync --exclude .git/hooks/).
        if d.IsDir() && rel == filepath.Join(".git", "hooks") {
            os.RemoveAll(path)
            return filepath.SkipDir
        }

        return nil
    })
}
```

Note: `sanitizeDownload` operates on absolute paths after download completes — it doesn't need `os.Root` because it's cleaning up a directory we own, not writing sandbox-controlled content. `os.Root` is used in `ExtractTranscripts`/`ExtractOutputFiles` where sandbox-controlled filenames determine the write path.

### Functions unchanged

These `exec.Command` calls target the `openshell` binary directly (not SSH/SCP/rsync) and are out of scope:

- `EnsureProvider()` — `openshell provider create`
- `EnsureAvailable()` — `exec.LookPath("openshell")`
- `CheckGateway()` — `openshell gateway info/start`
- `Create()` — `openshell sandbox create`
- `Delete()` — `openshell sandbox delete`
- `CollectLogs()` — `openshell logs`

### API surface changes

The `sshConfigPath` parameter is removed from all public function signatures. Functions take `sandboxName` directly. Callers no longer need to create, write, or clean up SSH config temp files.

Before:
```go
func SSH(sshConfigPath, sandboxName, command string, timeout time.Duration) (stdout, stderr string, exitCode int, err error)
func SCP(sshConfigPath, sandboxName, localPath, remotePath string) error
```

After:
```go
func Exec(sandboxName, command string, timeout time.Duration) (stdout, stderr string, exitCode int, err error)
func Upload(sandboxName, localPath, remotePath string) error
```

## Testing

- **Unit tests**: Existing `TestPathTraversalContainment` updated to use `os.Root`. New tests for `sanitizeDownload` (symlink removal, `.git/hooks/` removal).
- **Integration tests (`make e2e-test`)**: The e2e tests exercise the full run flow against a live sandbox — they are the primary validation that the migration works end-to-end.
- **Manual verification**: Run a fullsend agent in a sandbox, confirm bootstrap uploads, agent execution with progress streaming, and repo extraction all work.

## Risks

| Risk | Mitigation |
|---|---|
| `openshell sandbox exec` behavior differs subtly from `ssh` | Validated core behaviors (streaming, exit codes, timeout) in live testing. E2e tests cover the full flow. |
| `download` symlink handling changes in future OpenShell versions | `os.Root` + `sanitizeDownload` provide defense-in-depth regardless of transport behavior. |
| `upload`/`download` performance differs from `scp`/`rsync` | Both use gRPC streaming internally. If performance regresses, it's an OpenShell issue to report upstream. |
| Breaking change to sandbox package API (`sshConfigPath` removed) | All callers are internal (`run.go`). No external consumers. |
