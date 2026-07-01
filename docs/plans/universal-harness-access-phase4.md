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

### In-sandbox fetch subcommand

The `fullsend fetch-skill` subcommand reuses the existing fullsend binary already in the sandbox. When the agent runs it:

1. Agent calls: `fullsend fetch-skill https://github.com/fullsend-ai/library/tree/abc123/skills/python-linting#sha256=<tree-hash>...`
2. Subcommand sends HTTP request to runner via `FULLSEND_FETCH_URL` with bearer token from `FULLSEND_FETCH_TOKEN`
3. Runner validates URL against `allowed_remote_resources`
4. Runner uses `gitfetch.FetchTree` (git sparse checkout) to fetch the skill directory
5. Runner verifies tree hash (hash covers entire directory tree)
6. Runner stores in cache via `CachePutDir` and uploads directory tree to sandbox
7. Subcommand returns the sandbox-local skill directory path

### Security constraints

- Runtime fetch is opt-in per harness (`allow_runtime_fetch: true`)
- All URLs must match `allowed_remote_resources` prefixes
- Integrity hash required on all URLs (tree hash for skill directories)
- Rate limited: `max_runtime_fetches` (default 10) per agent run
- Skills are directories -- fetched via git sparse checkout (same as static resolution)
- Non-forge HTTPS URLs are rejected for skills (no HTTP directory listing standard)
- All fetched skills pass security scanning pipeline
- Audit log records all runtime fetches with `fetch_type: "runtime"`

### Implementation steps

#### PR 1: Runner-side fetch service (#2173, merged)
- HTTP service implementing `fetchsvc.Service` with `ServeHTTP` handler
- Request/response protocol: URL -> local path or error
- Rate limiting enforcement via atomic counter
- Git sparse checkout integration for skill directory fetching (reuses `gitfetch.FetchTree`)
- Audit logging with `fetch_type: "runtime"`

#### PR 2: In-sandbox fetch subcommand (#2223)
- `fullsend fetch-skill` subcommand reusing the existing fullsend binary in the sandbox
- Runner starts HTTP server on dynamic TCP port with per-run bearer token auth (ADR-0046)
- `FULLSEND_FETCH_URL` and `FULLSEND_FETCH_TOKEN` env vars injected during bootstrap
- Reports errors to stderr, success path to stdout
- Returns the sandbox-local skill directory path (not a single file path)

#### PR 3: Harness schema and CLI integration
- Add `allow_runtime_fetch` and `max_runtime_fetches` to harness schema
- Validation: reject runtime fetch fields if `allowed_remote_resources` is empty
- Gate fetch service startup behind `allow_runtime_fetch`

## Verification

1. Agent can fetch a skill directory at runtime matching allowed prefix
2. Fetch of URL outside allowed prefix is rejected
3. Fetch without hash is rejected
4. Rate limit enforcement: 11th fetch fails
5. `allow_runtime_fetch: false` blocks all runtime fetches
6. Audit log records runtime fetches
7. Fetched skill directory structure is preserved in sandbox (SKILL.md plus companion files)
8. Non-forge HTTPS URLs are rejected with clear error message
