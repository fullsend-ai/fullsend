# Contributing to Fullsend

Thank you for your interest in contributing! This document covers the social norms and processes we follow. For where to place your contribution (problem docs, ADRs, etc.), see the [README](README.md#how-to-contribute).

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
