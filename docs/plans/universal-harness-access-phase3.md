# Implementation Plan: Phase 3 — Lock Files

## Context

Phase 3 adds lock files (`.fullsend/lock.yaml`) that pin all resolved remote dependencies for reproducible harness execution. This was implemented alongside Phases 1 and 2.

## Implementation

### Lock file package (`internal/lock/lock.go`)

- `LockFile` struct: version, generated_at, harnesses map
- `HarnessLock`: source, sha256, resolved_at, dependencies
- `DependencyEntry`: field, url, sha256, type, fetched_at, transitive_deps, files
  - `type` is `"file"` for agents/policies or `"directory"` for skills
  - `files` lists the manifest of files in directory dependencies (skills only)
- `Load(path)`: reads and validates lock file
- `Save(path, lf)`: atomic write with temp-file-then-rename
- `Lookup(harnessName)`: returns entry or nil
- `IsStale(sourceHash)`: checks if harness has changed
- `LookupDep(url)`: depth-first search through dependency tree

### Lock file schema

```yaml
# .fullsend/lock.yaml
version: 1
generated_at: "2026-05-12T14:30:00Z"
harnesses:
  code:
    source: harness/code.yaml
    sha256: abc123...
    resolved_at: "2026-05-12T14:30:00Z"
    dependencies:
      - field: agent
        url: https://raw.githubusercontent.com/fullsend-ai/library/8cd3799.../agents/code.md
        sha256: def456...
        type: file
        fetched_at: "2026-05-12T14:29:55Z"
      - field: skills[0]
        url: https://github.com/fullsend-ai/library/tree/8cd3799.../skills/cargo-check
        sha256: <tree-hash>...
        type: directory
        fetched_at: "2026-05-12T14:29:56Z"
        files:
          - path: SKILL.md
            sha256: abc123...
          - path: scripts/check.sh
            sha256: def456...
        transitive_deps:
          - field: skills[dep0]
            url: https://raw.githubusercontent.com/fullsend-ai/library/8cd3799.../policies/rust-sandbox.yaml
            sha256: jkl012...
            type: file
            fetched_at: "2026-05-12T14:29:57Z"
```

### CLI lock command (`internal/cli/lock.go`)

- `fullsend lock <agent-name> --fullsend-dir <dir>` resolves all deps and writes lock file
- `--update` flag forces re-resolution even if entry is current
- Supports `--offline`, `--max-depth`, `--max-resources` flags

### Lock file resolution (`internal/cli/lock.go:resolveFromLock`)

- For each pinned dependency, verifies content exists in local cache
- For `type: "file"` entries: uses `CacheGet` and returns `content` path
- For `type: "directory"` entries: uses `CacheGetDir` and returns `tree/` path
- For `field: "base"` entries: verifies cache presence only (base composition is already resolved by `LoadWithBase` before `resolveFromLock` runs)
- Applies mutations to harness only after all deps are confirmed in cache
- Falls back to normal network resolution on failure

### Integration in `fullsend run` (`internal/cli/run.go`)

- Checks for lock file before resolving
- If lock entry exists and is not stale, uses `resolveFromLock`
- If lock entry is stale, warns user to run `fullsend lock`
- Falls back to normal resolution if lock resolution fails

## Verification

1. `fullsend lock code --fullsend-dir .fullsend` generates lock file
2. `fullsend run code` uses lock file when available
3. Modifying harness triggers stale warning
4. Missing cache entries produce clear error messages
5. `--update` forces re-resolution
6. Lock file correctly records `type: "directory"` and `files` manifest for skill dependencies
7. Lock file correctly records `type: "file"` for agent and policy dependencies
