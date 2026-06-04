# Contributing to Fullsend

Thank you for your interest in contributing! This document covers the social norms and processes we follow. For where to place your contribution (problem docs, ADRs, etc.), see the [README](README.md#how-to-contribute).

## Commit messages

This project uses [Conventional Commits](https://www.conventionalcommits.org/). Every commit on `main` feeds the auto-generated release notes (via GoReleaser), so getting the format right matters.

### Format

```
<type>(<scope>): <short description>

<optional body>

<optional trailers>
```

### Types

| Type | Purpose | Appears in release notes? |
|---|---|---|
| `feat` | New functionality | Yes — under **Features** |
| `fix` | Bug fix | Yes — under **Bug Fixes** |
| `refactor` | Code restructuring (no behavior change) | Yes — under **Refactoring** |
| `docs` | Documentation only | No |
| `test` | Adding or updating tests | No |
| `chore` | Maintenance (CI, deps, tooling) | No |
| `ci` | CI/CD pipeline changes | No |
| `perf` | Performance improvement | Yes — under **Others** |
| `build` | Build system or dependency changes | No |

### Scope

The parenthesized scope is optional but encouraged. Use it to identify the subsystem: `feat(appsetup)`, `fix(mint)`, `docs(adr)`, `chore(ci)`. When fixing a specific issue, prefer the issue number as scope: `fix(#123): ...`.

### Breaking changes

Append `!` after the type/scope to flag a breaking change: `feat(cli)!: rename --gcp flags to --inference`. Include a `BREAKING CHANGE:` trailer in the body explaining migration steps. Breaking changes trigger a major version bump.

### Examples

```
feat(review-agent): add outcome labels to post-review.sh

fix(#933): use .yaml extension for shim workflow path

docs: add mint URL stability note to installation guide

chore(ci): update goreleaser to v2
```

### Why this matters

GoReleaser groups changelog entries by type prefix (see `.goreleaser.yml`). Commits without a recognized prefix land under "Others". Commits prefixed `docs:`, `test:`, or `chore:` are excluded from release notes entirely. A wrong prefix means the change shows up in the wrong section — or not at all.

## Pull request workflow

### Opening a PR

- Run `make lint` before pushing and fix any failures.
- Keep PRs focused. One problem area or decision per PR is easier to review than a grab-bag.
- If your change touches a problem doc, make sure the "Open questions" section still makes sense after your edit.

### Review etiquette

- **Comment resolution belongs to the PR author.** When a reviewer leaves a comment, the PR author is free to address the feedback and resolve the conversation themselves. This keeps the review cycle moving.
- **If you need to block a PR on your feedback, use "Request changes."** A comment alone is advisory — the author may resolve it at their discretion. The "Request changes" review status is how a reviewer signals that the PR should not merge until their concern is addressed. This is the only mechanism for enforcing your review.
- **Be constructive.** This is a design exploration — disagreement is expected and valuable. Critique ideas, not people. When you push back on a proposal, suggest an alternative or explain what concern drives your objection.

### Merging

- PRs require approval from a [CODEOWNERS](CODEOWNERS) member before merging.
## Working with ADRs

ADRs (Architecture Decision Records) are **point-in-time records**. Once accepted, their content is frozen — do not edit the Context, Decision, or Consequences sections. If a decision needs to change, write a new ADR that supersedes the old one. See the [ADR template](docs/ADRs/0000-adr-template.md) and [ADR 0001](docs/ADRs/0001-use-adrs-for-decision-making.md) for full details.

### ADR numbering

ADR filenames use a four-digit number (`NNNN-short-description.md`). When multiple PRs add ADRs concurrently, number collisions can happen. Before merging, use the `/renumber-adr` skill to check whether your ADR number is still available on the target branch and renumber if needed.

## Issues

When in doubt about whether something warrants a PR, start with an issue. Issues are low-friction and can graduate into PRs, problem docs, or ADRs later.

## License

All contributions to this project are made under the [Apache License, Version 2.0](LICENSE). By submitting a pull request, you agree that your contributions will be licensed under this license.
