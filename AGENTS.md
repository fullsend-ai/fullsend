# AGENTS.md

See [CLAUDE.md](CLAUDE.md) for project rules and design decisions.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/) format for every commit. The allowed types are: `feat`, `fix`, `refactor`, `docs`, `test`, `chore`, `ci`, `perf`, `build`. See [CONTRIBUTING.md](CONTRIBUTING.md#commit-messages) for the full specification.

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

## Architecture Decision Records (ADRs)

These rules apply whenever you touch `docs/ADRs/` or review a PR that does. Full authoring guidance is in [`skills/writing-adrs/SKILL.md`](skills/writing-adrs/SKILL.md); invoke that skill when writing a new ADR.

**Immutability:** Once an ADR on `main` has status **Accepted**, its Context, Decision, and Consequences sections are frozen. Do not add post-decision notes, rewrite rationale, or edit consequences in place. When circumstances change, write a **new** ADR that supersedes the old one. The only acceptable edits to an Accepted ADR on `main` are status changes (e.g., to Deprecated or Superseded) and links to the superseding ADR. Typos and broken links are narrow exceptions — call them out in the PR description.

**New ADRs in pull requests:** Approval happens at **merge**, not when the branch is created. If the decision is made, set status to **Accepted** in the ADR you are proposing (not **Proposed** merely because the PR is open). Use **Proposed** or **Undecided** only when the decision itself is still unsettled. When status is Accepted, update `docs/architecture.md` and related problem docs in the same PR per the writing-adrs skill.

**When reviewing PRs:** Flag in-place edits to Context, Decision, or Consequences on Accepted ADRs already on `main` as a policy violation. Allow status-only updates and supersession links. For brand-new ADR files on the PR branch, evaluate whether the recorded decision matches the diff — do not treat **Accepted** on a new file as a mistake if the ADR is ready for human review at merge.
