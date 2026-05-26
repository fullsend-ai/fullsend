# AGENTS.md

See [CLAUDE.md](CLAUDE.md) for project rules and design decisions.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/) format for every commit. The allowed types are: `feat`, `fix`, `refactor`, `docs`, `test`, `chore`, `ci`, `perf`. See [CONTRIBUTING.md](CONTRIBUTING.md#commit-messages) for the full specification.

This is not optional — GoReleaser parses commit prefixes to build release notes. A missing or wrong prefix produces incorrect changelogs.

When reviewing PRs, check that commit messages and PR titles follow this format. Flag violations as a required change — they are not cosmetic.

## Forge abstraction

All git forge operations (GitHub API calls, PR comments, issue creation, workflow dispatch, etc.) **must** go through the `forge.Client` interface defined in `internal/forge/forge.go`. This is a fundamental architectural rule — the codebase supports multiple forges (GitHub, GitLab, Forgejo) and direct coupling to any single forge breaks the abstraction.

**Prohibited outside `internal/forge/github/`:**

- `exec.Command("gh", ...)` — shelling out to the GitHub CLI
- Direct GitHub REST or GraphQL API calls (e.g., raw `net/http` to `api.github.com`)
- Any other forge-specific operation that bypasses `forge.Client`

**Where forge-specific code belongs:** Only the `internal/forge/github/` package (the GitHub implementation of `forge.Client`) should contain GitHub-specific logic. All other packages must use the `forge.Client` interface, which is injected as a dependency.

**When writing code:** If you need a forge operation that `forge.Client` does not yet support, add a new method to the interface and implement it in the GitHub client — do not work around the interface.

**When reviewing PRs:** Flag any direct `exec.Command("gh", ...)`, raw GitHub API calls, or other forge-specific operations outside `internal/forge/github/` as a medium-severity or higher finding. This is an architectural violation, not a style preference.
