---
name: renumber-adr
description: >-
  Check whether ADR numbers in the current branch conflict with ADRs already on
  the PR's target branch, and renumber if needed. Use before merging a PR that
  adds new ADRs, to avoid number collisions with concurrently merged ADRs.
---

# Renumber ADR

## Overview

When multiple PRs add ADRs concurrently, they may pick the same four-digit
number. This skill detects collisions against the target branch and renumbers
the ADR file, its internal title/heading, and every reference to it across the
repository.

## When to Use

- Before merging a PR that introduces one or more new ADR files.
- When the user asks to check or fix ADR numbering.
- When invoked via `/renumber-adr`.

## Process

Follow these steps in order. Do not skip steps.

### 1. Identify new ADR files in this branch

Determine the target branch for the PR. If the user provides it as an argument,
use that. Otherwise:

```bash
gh pr view --json baseRefName --jq '.baseRefName' 2>/dev/null || echo main
```

Then find ADR files that are **new** in this branch (not present on the target):

```bash
git diff --name-only --diff-filter=A <target-branch>...HEAD -- docs/ADRs/
```

If no new ADR files are found, report that there is nothing to renumber and
stop.

### 2. Check for number collisions

List existing ADR files on the target branch:

```bash
git ls-tree --name-only <target-branch> docs/ADRs/
```

For each new ADR file, extract its four-digit number from the filename
(`NNNN-short-description.md`). Check whether any file with the same `NNNN`
prefix exists on the target branch.

If there are no collisions, report that all numbers are clear and stop.

### 3. Find the next available number

For each colliding ADR, determine the next available four-digit number.
Consider both the target branch ADR files **and** other new ADR files in this
branch (to avoid collisions among the branch's own ADRs). Pick the lowest
unused number.

### 4. Rename the file

```bash
git mv docs/ADRs/NNNN-old-slug.md docs/ADRs/MMMM-old-slug.md
```

### 5. Update references inside the ADR

The ADR file itself contains the number in several places. Update all of them:

- **Frontmatter `title`:** e.g. `title: "2. Initial Fullsend Design"` — uses
  the number without leading zeros.
- **Markdown heading:** e.g. `# 2. Initial Fullsend Design` — same format.

Read the file and update these occurrences. The number in the title and heading
uses the **integer** form (no leading zeros), not the four-digit padded form.

### 6. Update references across the repository

Search the entire repository for references to the old ADR and update them.
There are several patterns to find:

1. **Filename references** (in markdown links): `NNNN-slug.md` — update the
   four-digit prefix to the new number.
2. **Display text references**: `ADR NNNN` — update the four-digit number in
   link text and plain text.
3. **Section heading references in prose** (e.g. `## Reference workflow
   components (ADR NNNN)`) — update the number.

Search broadly:

```bash
# Find all files referencing the old filename
grep -rl "NNNN-slug" .

# Find all files referencing "ADR NNNN" (display text or plain text)
grep -rl "ADR NNNN" .
```

For each file found, read it and update all occurrences. Be careful to match
the exact old filename and number — do not accidentally rename unrelated
four-digit sequences.

### 7. Verify and report

After all renames and reference updates:

- List the changes made: old filename -> new filename, and the count of files
  with updated references.
- Run `pre-commit run --files <all-changed-files>` to verify nothing is broken.
- Report the result to the user.

## Constraints

- **Only renumber ADRs that are new in this branch.** Never renumber ADRs that
  already exist on the target branch.
- **Preserve the slug.** Only the four-digit number prefix changes; the
  descriptive slug (`short-description`) stays the same.
- **Update every reference.** A missed reference is a broken link. Search
  thoroughly.
- **Handle multiple ADRs.** If the branch adds several ADRs and more than one
  collides, renumber all of them before updating references (so cross-references
  among the new ADRs are correct).
- **Do not modify ADR content** beyond the number in the title and heading.
  Substantial ADR content is not rewritten once accepted; this skill only fixes numbering.
