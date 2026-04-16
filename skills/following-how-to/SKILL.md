---
name: following-how-to
description: >-
  Use when asked to reproduce an experiment, run an experiment's setup, or work
  on an experiment that has a HOW_TO document. Use before making changes to an
  experiment to understand its environment and dependencies.
---

# Following HOW_TO Documents

Follow an experiment's HOW_TO document to reproduce it or to understand its
setup before working on it. Do not improvise -- follow the steps as written.

## Process

Follow these steps in order. Do not skip steps.

### 1. Find the HOW_TO

Look for `HOW_TO.md` or `HOW_TO_*.md` files in the experiment directory.

```bash
ls experiments/<name>/HOW_TO*.md
```

**If no HOW_TO exists:** Tell the user and suggest creating one using the
writing-how-to skill. Do not attempt to reverse-engineer setup steps from the
README or scripts -- a missing HOW_TO means the reproduction path is not
documented.

**If multiple HOW_TO files exist:** List them and ask the user which one to
follow, unless the task makes the choice obvious (e.g., asked to "set up the
experiment" implies `HOW_TO_SETUP.md`).

### 2. Check requirements

Read the Requirements section. For each tool listed:

```bash
command -v <tool>
```

For each environment variable listed, verify it is set:

```bash
echo "${VARIABLE_NAME:?VARIABLE_NAME is not set}"
```

**If anything is missing,** report the full list of missing requirements to the
user before proceeding. Do not install tools or set env vars without the user's
approval. Present what is missing and what action is needed:

```
Missing requirements:
- uv: not installed (https://docs.astral.sh/uv/getting-started/installation/)
- GCP_PROJECT_ID: not set (GCP project ID where Model Armor templates are configured)
```

Only proceed once all requirements are satisfied.

### 3. Follow the steps

Execute each step in order. Do not skip steps, reorder them, or combine them.

- **Script steps:** Run the script as written. Do not inline the script's
  contents or modify it.
- **Manual steps:** Execute the command exactly as documented.
- **Decision steps:** If a step requires a choice (e.g., selecting a region),
  use the documented default if one is provided. If no default exists, ask the
  user.

If a step fails, stop and report the failure with the exact error output. Do
not attempt to fix it unless the user asks.

### 4. Verify output

Read the Expected Output section. Check each condition:

- If it says a file should exist, verify it does.
- If it describes terminal output, compare against what was produced.
- If it specifies values or patterns, confirm they match.

Report the verification result to the user:

```
Expected Output verification:
- results/ directory created: YES
- summary.md present: YES
- terminal shows comparison table: YES
All checks passed.
```

If any check fails, report what was expected vs. what was observed.

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Skipping requirement checks | Always check before running. A missing tool mid-run wastes time. |
| Installing tools without asking | Report what is missing. Let the user decide how to install. |
| Improvising when no HOW_TO exists | Stop and suggest creating one. Do not guess at setup steps. |
| Modifying scripts before running them | Run as-is first. Changes come after successful reproduction. |
| Ignoring Expected Output | Verification is the point. Always check the results. |
