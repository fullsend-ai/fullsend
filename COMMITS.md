# Commit Messages

This project uses [Conventional Commits](https://www.conventionalcommits.org/). Gitlint enforces the prefixes on every commit and PR title, so getting them right matters. You **must** consult this file when writing or reviewing commit messages.

Release notes no longer come from commit subjects. They come from a `release-note` block in the PR body — see [Release notes](#release-notes) below.

## Format

```
<type>(<scope>): <short description>

<optional body>

<optional trailers>
```

## Types

| Type | Purpose | Needs a release-note entry? |
|---|---|---|
| `feat` | New user-facing functionality | Yes |
| `fix` | Bug fix visible to users | Yes |
| `refactor` | Code restructuring (no behavior change) | Usually `NONE` |
| `docs` | Documentation only | Usually `NONE` |
| `test` | Adding or updating tests | `NONE` |
| `chore` | Maintenance (CI, deps, tooling) | `NONE` |
| `ci` | CI/CD pipeline changes | `NONE` |
| `perf` | Performance improvement | Yes |
| `build` | Build system or dependency changes | `NONE` |
| `style` | Formatting only, no logic change | `NONE` |

## Release notes

Every PR must contain a fenced `release-note` block in its body. The PR
template carries the block with a `NONE` default. CI rejects a PR whose
block is missing or empty.

Write the block for users, not for reviewers. One or more lines of prose.
Write `NONE` when the change has no effect users can see. Start the block
with `action required` when the change breaks something.

At release time, the release pipeline reads the blocks from the merged
PRs in the tag range (with the Kubernetes `release-notes` tool), skips
`NONE` entries, and publishes the gathered markdown. Commit subjects no
longer feed the release notes.

Example block:

```release-note
action required: the `role` field in harness files is now required.
Add `role: <rolename>` to any harness file that lacks it.
```

## `feat` is for end users

Commit types classify git history and are gitlint-enforced; the PR-body
release-note block controls what end users read in the release notes.
The `feat` prefix still marks user-facing functionality, so the history
stays meaningful. Reserve `feat` for changes an end user would recognize
as new capability:

- A new CLI command or flag they can invoke
- A new behavior they interact with (e.g., the agent now comments on their PR with a new kind of analysis)
- A new integration or platform they can target

**`feat` is wrong for:**

- Restructuring internals (extracting a sub-agent, splitting a package) → `refactor`
- Adding internal packages, helpers, or abstractions that don't change user-visible behavior → `refactor`
- Upgrading a dependency or vendored tool version → `chore`
- Tightening internal heuristics, adjusting prompts, or tuning agent behavior that users don't directly control → `refactor` or `fix` depending on whether it corrects a defect
- Addressing review feedback on an existing PR → `fix` or `refactor`, not `feat`

Apply the same discipline to `fix` — bumping a dependency version is `chore`, not `fix`, unless it corrects a user-visible bug. Removing a trailing blank line is `style`, not `fix`.

**When in doubt, prefer `refactor` or `chore` over `feat` or `fix`.** A
miscategorized type mislabels the git history, not the release notes —
the PR-body release-note block decides what users read — but an honest
history is still worth keeping.

## Scope

The parenthesized scope is optional but encouraged. Use it to identify the subsystem: `feat(appsetup)`, `fix(mint)`, `docs(adr)`, `chore(ci)`. When fixing a specific issue, prefer the issue number as scope: `fix(#123): ...`.

### Forbidden type + scope combinations

Some type/scope pairs are **enforced as errors** by gitlint (rule `UC1`). These combinations mislead users by putting infrastructure changes in user-facing release-note territory.

| Forbidden | Why | Use instead |
|---|---|---|
| `fix(ci)` | CI changes are not user-visible bug fixes | `ci(<subsystem>)` |
| `feat(ci)` | CI changes are not user-visible features | `ci(<subsystem>)` |
| `fix(e2e)` | E2E test changes are not user-visible bug fixes | `ci(e2e)` |
| `feat(e2e)` | E2E test changes are not user-visible features | `ci(e2e)` |

## Breaking changes

Breaking changes **must** be marked in three places: the `!` suffix in
every commit subject and the PR title, and an `action required`
`release-note` block in the PR body.

The release-note block is what users see. When the block starts with
`action required`, the release-note check labels the PR
`release-note-action-required`, and the release pipeline lists the note
under **Urgent Upgrade Notes** in the published release notes (the
gatherer strips the `action required:` prefix from the note text itself).
Fork PRs run with a read-only token, so CI cannot label them; the check
fails instead until a maintainer adds the label.
The `!` marker stays required for reviewers and tooling. CI rejects a
`NONE` release-note block — and any block that does not start with
`action required` — when the PR title or any commit subject carries the
`!` marker, so a breaking PR cannot ship without a warning. This closes
the gap that caused real incidents — users upgraded
with no warning that their agents would stop working (#8366).

**How to mark a breaking change:**

1. Append `!` after the type/scope: `feat(harness)!: require role field`
2. Include a `BREAKING CHANGE:` trailer in the commit body explaining what breaks and how to migrate

Both the `!` suffix and the trailer are required. The `!` suffix signals the
breaking change to human reviewers and to the release-note CI check. The
trailer tells readers of the commit what to do about it. The `action required`
release-note block tells users.

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
- **Missing `!` on breaking changes** — if the diff removes a field, renames a
  flag, adds a required input, tightens validation, or otherwise breaks
  existing usage, the PR title and commit messages **must** carry the `!`
  suffix. Flag a missing `!` as an important-severity finding.
- **Missing or wrong `release-note` block** — every PR needs a valid block.
  A breaking PR (title or commit subject carries `!`) must start its block
  with `action required`; a `NONE` block there is an important-severity
  finding.
