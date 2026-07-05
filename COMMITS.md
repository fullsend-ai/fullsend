# Commit Messages

This project uses [Conventional Commits](https://www.conventionalcommits.org/). Every commit on `main` feeds the auto-generated release notes (via GoReleaser), so getting the prefix right matters. You **must** consult this file when writing or reviewing commit messages.

## Format

```
<type>(<scope>): <short description>

<optional body>

<optional trailers>
```

## Types

| Type | Purpose | Appears in release notes? |
|---|---|---|
| `feat` | New user-facing functionality | Yes — under **Features** |
| `fix` | Bug fix visible to users | Yes — under **Bug Fixes** |
| `refactor` | Code restructuring (no behavior change) | Yes — under **Refactoring** |
| `docs` | Documentation only | No |
| `test` | Adding or updating tests | No |
| `chore` | Maintenance (CI, deps, tooling) | No |
| `ci` | CI/CD pipeline changes | No |
| `perf` | Performance improvement | Yes — under **Others** |
| `build` | Build system or dependency changes | No |

## `feat` is for end users

The `feat` prefix populates the **Features** section of our release notes. End users read that list to decide whether to upgrade. Reserve `feat` for changes an end user would recognize as new capability:

- A new CLI command or flag they can invoke
- A new behavior they interact with (e.g., the agent now comments on their PR with a new kind of analysis)
- A new integration or platform they can target

**`feat` is wrong for:**

- Restructuring internals (extracting a sub-agent, splitting a package) → `refactor`
- Adding internal packages, helpers, or abstractions that don't change user-visible behavior → `refactor`
- Upgrading a dependency or vendored tool version → `chore`
- Tightening internal heuristics, adjusting prompts, or tuning agent behavior that users don't directly control → `refactor` or `fix` depending on whether it corrects a defect
- Addressing review feedback on an existing PR → `fix` or `refactor`, not `feat`

Apply the same discipline to `fix` — bumping a dependency version is `chore`, not `fix`, unless it corrects a user-visible bug. Removing a trailing blank line is `chore`, not `fix`.

**When in doubt, prefer `refactor` or `chore` over `feat` or `fix`.** A change miscategorized as `refactor` is harmless — it shows up in a lower section of the release notes. A change miscategorized as `feat` erodes the signal of the Features list.

## Scope

The parenthesized scope is optional but encouraged. Use it to identify the subsystem: `feat(appsetup)`, `fix(mint)`, `docs(adr)`, `chore(ci)`. When fixing a specific issue, prefer the issue number as scope: `fix(#123): ...`.

### Forbidden type + scope combinations

Some type/scope pairs are **enforced as errors** by gitlint (rule `UC1`). These combinations mislead users by putting infrastructure changes in user-facing release-note sections.

| Forbidden | Why | Use instead |
|---|---|---|
| `fix(ci)` | CI changes are not user-visible bug fixes | `ci(<subsystem>)` |
| `feat(ci)` | CI changes are not user-visible features | `ci(<subsystem>)` |
| `fix(e2e)` | E2E test changes are not user-visible bug fixes | `ci(e2e)` |
| `feat(e2e)` | E2E test changes are not user-visible features | `ci(e2e)` |

## Breaking changes

Breaking changes **must** be marked in both commit messages and PR titles. GoReleaser builds release notes from merged PR titles (`use: github` in `.goreleaser.yml`), so an unmarked PR title means the breaking change is invisible to users reading the release notes. This has caused real incidents — users upgraded with no warning that their agents would stop working.

**How to mark a breaking change:**

1. Append `!` after the type/scope: `feat(harness)!: require role field`
2. Include a `BREAKING CHANGE:` trailer in the commit body explaining what breaks and how to migrate

Both the `!` suffix and the trailer are required. The `!` suffix signals the breaking change to human reviewers and enables future automated tooling; the trailer tells users what to do about it.

**How to tell if your change is breaking:**

- A previously optional field, flag, or input is now required
- A field, flag, command, or API endpoint is removed or renamed
- Default values change in ways that alter existing behavior
- Validation is added that rejects previously accepted input
- Output format changes that downstream consumers parse

If you are unsure whether a change is breaking, mark it. A false positive in the release notes is far less costly than a silent break.

**Example** (full commit message):

```
feat(harness)!: require role field in Validate()

Harness files without a role: field now fail validation. This was
previously a lint warning (added in v0.17.0). All scaffold templates
and generated wrappers already include the field.

BREAKING CHANGE: Add `role: <rolename>` to any harness file that
lacks it. The lint warning has been active since v0.17.0.
```

## Examples

```
feat(review-agent): add outcome labels to post-review.sh

fix(#933): use .yaml extension for shim workflow path

refactor(#1797): extract challenger pass into dedicated sub-agent

chore(sandbox): bump gopls from 0.18.1 to 0.22.0

docs: add mint URL stability note to installation guide
```

## Reviewing commit messages and PR titles

When reviewing PRs, check that commit messages and PR titles use the correct type prefix. Flag violations as a required change — they are not cosmetic. Pay particular attention to:

- **`feat` misuse** — challenge it if the change is not user-facing.
- **Missing `!` on breaking changes** — if the diff removes a field, renames a flag, adds a required input, tightens validation, or otherwise breaks existing usage, the PR title and commit messages **must** carry the `!` suffix. Flag a missing `!` as an important-severity finding. The PR title is especially critical because GoReleaser uses it to build user-facing release notes.
