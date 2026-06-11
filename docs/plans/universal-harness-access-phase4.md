# Implementation Plan: Phase 4 — Runtime Dependency Loading

## Context

Phases 1-3 require all dependencies to be declared statically in the harness YAML. Phase 4 adds runtime dependency loading: agents can discover and fetch additional skills during execution based on the specific problem they encounter.

## Design

### Harness schema additions

```yaml
agent: agents/code.md
skills:
  - skills/base
  - https://github.com/fullsend-ai/library/tree/abc123/skills/rust#sha256=<tree-hash>...
allowed_remote_resources:
  - https://github.com/fullsend-ai/library/
allow_runtime_fetch: true        # opt-in (default: false)
max_runtime_fetches: 10          # rate limit per agent run
```

### In-sandbox fetch binary

A `fullsend-fetch-skill` binary available inside the sandbox. When the agent runs it:

1. Agent calls: `fullsend-fetch-skill https://github.com/fullsend-ai/library/tree/abc123/skills/python-linting#sha256=<tree-hash>...`
2. Binary sends request to runner over Unix socket
3. Runner validates URL against `allowed_remote_resources`
4. Runner uses forge API to list and fetch the skill directory (skills are directories, requiring `ListDirectoryContents` and `GetFileContentAtRef`)
5. Runner verifies tree hash (hash covers entire directory tree)
6. Runner stores in cache via `CachePutDir` and uploads directory tree to sandbox
7. Binary returns the sandbox-local skill directory path

### Security constraints

- Runtime fetch is opt-in per harness (`allow_runtime_fetch: true`)
- All URLs must match `allowed_remote_resources` prefixes
- Integrity hash required on all URLs (tree hash for skill directories)
- Rate limited: `max_runtime_fetches` (default 10) per agent run
- Skills are directories -- requires forge API access (same as static resolution)
- Non-forge HTTPS URLs are rejected for skills (no HTTP directory listing standard)
- All fetched skills pass security scanning pipeline
- Audit log records all runtime fetches with `fetch_type: "runtime"`

### Implementation steps

#### PR 1: Harness schema and ResolveSkillURL (this PR)
- Add `allow_runtime_fetch` and `max_runtime_fetches` to harness schema with validation
- Export `ResolveSkillURL` in `internal/resolve/` for single-URL runtime skill resolution
- Uses forge API for directory-based skill fetching (same model as static resolution)
- Audit logging with `fetch_type: "runtime"` to distinguish from static resolution
- No transitive resolution for runtime-fetched skills (leaf nodes only)

#### PR 2: In-sandbox fetch binary
- `fullsend-fetch-skill` binary compiled and uploaded to sandbox during bootstrap
- Connects to Unix socket passed via environment variable
- Reports errors to stderr, success path to stdout
- Returns the sandbox-local skill directory path (not a single file path)

#### PR 3: Runner-side fetch service and CLI wiring
- Unix socket listener in the runner process
- Request/response protocol: URL -> local path or error
- Rate limiting enforcement (uses `MaxRuntimeFetches` / `DefaultMaxRuntimeFetches`)
- Socket setup in sandbox provisioning
- Wire `ResolveSkillURL` into the socket handler

## Verification

1. Agent can fetch a skill directory at runtime matching allowed prefix
2. Fetch of URL outside allowed prefix is rejected
3. Fetch without hash is rejected
4. Rate limit enforcement: 11th fetch fails
5. `allow_runtime_fetch: false` blocks all runtime fetches
6. Audit log records runtime fetches
7. Fetched skill directory structure is preserved in sandbox (SKILL.md plus companion files)
8. Non-forge HTTPS URLs are rejected with clear error message
