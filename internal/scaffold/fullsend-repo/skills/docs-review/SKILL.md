---
name: docs-review
description: >-
  Evaluates whether a PR's code changes have made in-repo documentation
  stale. Discovers documentation files, matches changed identifiers
  against doc content, and produces findings for stale or missing
  documentation. Read-only — flags staleness but does not update docs.
  Can be delegated to by the pr-review skill.
---

# Docs Review

Code changes can silently invalidate documentation. A renamed function,
a changed API signature, a removed configuration option — each can leave
docs describing behavior that no longer exists. This skill detects that
drift by matching the PR's code changes against in-repo documentation
and flagging docs whose descriptions contradict the new code.

## Context management

This skill involves scanning documentation files across the repository,
which can consume significant context. Dispatch a subagent to carry out
the process below, keeping the main review context free for code-review
and PR-specific checks. The subagent should return a list of findings
(or an empty list if no stale docs were found).

## Process

Follow these steps in order. Do not skip steps.

### 1. Receive the change context

Use the diff and changed file list provided by the invoking skill
(typically `pr-review`). If invoked standalone, obtain the diff:

```bash
DEFAULT_BRANCH=$(git rev-parse --abbrev-ref origin/HEAD | cut -d/ -f2)
git diff $(git merge-base HEAD "$DEFAULT_BRANCH")..HEAD
```

Record the list of files changed in the PR:

```bash
git diff --name-only $(git merge-base HEAD "$DEFAULT_BRANCH")..HEAD
```

If no change can be identified, stop and report — do not guess.

### 2. Build the identifier checklist

Go through **every** changed file in the PR. For each file, extract
identifiers from the modified lines (lines starting with `+` or `-`)
and from diff hunk headers (`@@` lines). Write them down as a numbered
checklist — one entry per changed file, with all identifiers from that
file.

Use the most specific form of each identifier. CLI flag names,
configuration keys, full function names, and type names are good —
they match only relevant docs. Avoid generic short words that would
match hundreds of unrelated files. If a generic term is the only
identifier available for a change, include it, but prefer specific
forms when they exist.

Do not skip files. Do not prioritize some files over others. Every
changed file gets an entry in the checklist. This checklist is your
contract — you will search docs for every identifier on it.

### 3. Discover documentation files

Find all documentation files in the repository. This includes files in
dedicated documentation directories and standalone documentation files
like README.md at any level. Explore the repository structure to
understand where documentation lives — different repos organize docs
differently.

Search for documentation files (`.md`, `.rst`, `.adoc`) across the
repository.

If the repository has no documentation files, produce zero findings
and exit — there is nothing to check.

### 4. Search docs for every identifier

Write a shell script that takes the identifiers from step 2 and
greps for each one across the documentation files from step 3.
Run the script in a single Bash call:

```bash
for id in "identifier1" "identifier2" "identifier3"; do
  matches=$(grep -rl "$id" --include="*.md" --include="*.rst" --include="*.adoc" . 2>/dev/null)
  if [ -n "$matches" ]; then
    echo "MATCH: $id -> $matches"
  else
    echo "NO_MATCH: $id"
  fi
done
```

Include every identifier from every checklist entry in the `for`
loop. The script handles the searching mechanically — no identifiers
are skipped.

From the script output, collect all matched doc files into a
candidate list. Exclude documentation files that are already modified
in the same PR — those are being actively updated.

### 5. Evaluate every candidate (two passes)

**Pass 1 — Quick scan.** For each candidate doc file from step 4,
view only the lines that matched the grep (use `grep -n` to see them
in context). Based on the matching lines alone, decide whether the
doc might be stale. Record a verdict for every candidate:

```
- path/to/doc.md → possibly stale (describes behavior that changed)
- path/to/other.md → not stale (mentions identifier in passing)
- path/to/another.md → not stale (changelog entry)
...
```

Every candidate must have a verdict. Do not skip candidates.

**Pass 2 — Deep read.** For each candidate marked "possibly stale"
in pass 1, read the full file alongside the relevant section of the
diff. Confirm whether the doc is actually stale and write a specific
remediation describing what should be updated.

When evaluating:

- **Only flag docs whose content is now incorrect.** A doc that
  mentions an identifier is not stale if the described behavior is
  unchanged. It is stale only if the behavior, signature, or semantics
  changed in a way that makes the doc misleading.
- **Do not flag changelog entries or release notes that describe past
  releases.** Historical entries are not stale because the code evolved.

### 6. Compile findings

For each confirmed stale doc, record a separate finding. Do not
consolidate multiple stale files into one finding — each stale file
gets its own finding with its own file path:

- **Severity:** `high` if the doc describes behavior that is now
  *incorrect* and would mislead a reader (e.g., wrong API signature,
  removed config key still documented). `medium` if the doc is
  *incomplete* rather than incorrect (e.g., new parameter not
  documented, but existing content is still accurate). `low` if the
  doc is slightly outdated but not misleading. Never use `critical` —
  stale docs do not constitute a security or correctness risk in the
  code itself. Do not produce `info`-level findings — if the staleness
  is not worth flagging at `low`, do not flag it at all.
- **Category:** a descriptive label for the type of staleness, e.g.,
  `stale-doc`, `missing-doc`. Use whatever category best describes the
  finding.
- **File:** path to the stale doc file.
- **Line:** line number of the stale reference, if identifiable.
- **Description:** what is stale and why — reference the specific code
  change that caused the staleness.
- **Remediation:** what should be updated in the doc.

If no stale docs are found, produce zero findings. Do not generate
findings to justify having run the skill.

## Constraints

The agent definition (`agents/review.md`) is the authoritative list of
prohibitions. This skill does not restate them. If a step in this skill
appears to conflict with the agent definition, the agent definition
wins.

- **Do not flag docs modified in the same PR.** If the PR already
  updates a doc file, that file is being actively maintained and should
  not appear in findings.
- **Severity is capped at high.** Stale documentation does not
  constitute a critical security or correctness risk in the code.
  A `high` finding means the doc would actively mislead readers.
- **This skill is read-only.** It produces findings but does not
  modify documentation files.
