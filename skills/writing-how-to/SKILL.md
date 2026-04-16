---
name: writing-how-to
description: >-
  Use when creating or updating a HOW_TO document inside an experiment directory.
  Use when an experiment needs reproducibility instructions, setup documentation,
  or when existing reproduction steps are scattered across a README and should be
  formalized.
---

# Writing HOW_TO Documents for Experiments

A HOW_TO document gives a reader everything they need to reproduce an
experiment from scratch on a new machine. It is not a narrative -- it is a
checklist. Someone following it should be able to run the experiment without
reading the README first.

## When to Use

- Creating a new experiment that requires setup steps
- An experiment has reproduction instructions buried in its README that should
  be extracted into a standalone HOW_TO
- An experiment's setup has changed and the HOW_TO needs updating

Do NOT use for experiments that are pure analysis documents with no runnable
components (e.g., `003-agent-outage-fire-drill.md`).

## File Naming

- **`HOW_TO.md`** -- single document covering the whole experiment
- **`HOW_TO_<TOPIC>.md`** -- when an experiment has multiple independent
  procedures (e.g., `HOW_TO_SETUP.md`, `HOW_TO_EVALUATION.md`)

Place the file in the experiment's root directory, next to its README.

## Required Sections

Every HOW_TO must have these four sections, in this order:

### 1. Purpose

One sentence stating what following this document achieves.

```markdown
## Purpose

Reproduce the LLM Guard sentence-mode evaluation results from the guardrails
experiment.
```

### 2. Requirements

A table listing every tool, library, or service needed. Include the tool name
and a link to its installation docs. Include the version **only** when it
matters for reproducibility (e.g., a specific API version, a minimum Python
version).

```markdown
## Requirements

| Requirement | Link |
|-------------|------|
| Python 3.11+ | https://www.python.org/downloads/ |
| uv | https://docs.astral.sh/uv/getting-started/installation/ |
| gcloud CLI | https://cloud.google.com/sdk/docs/install |
```

After the table, list required environment variables separately. Never provide
default values for secrets -- just describe what the variable should contain.

```markdown
### Environment variables

| Variable | Description |
|----------|-------------|
| `GOOGLE_APPLICATION_CREDENTIALS` | Path to a GCP service account key file with Vertex AI permissions |
| `GCP_PROJECT_ID` | GCP project ID where Model Armor templates are configured |
```

### 3. Steps

Numbered steps to set up and run the experiment.

**The script rule:** If a sequence has 5 or more shell commands that a reader
would execute without making decisions, extract them into a script. Suggest
`setup.sh` for environment/dependency setup and `run.sh` for running the
experiment, but use experiment-specific names when they are clearer (e.g.,
`run-evaluation.sh`, `setup-venv.sh`).

When you extract commands to a script:
- Make the script executable (`chmod +x`)
- Add a shebang line (`#!/usr/bin/env bash`)
- Add `set -euo pipefail` after the shebang
- Keep the HOW_TO step as a single "run the script" command

```markdown
## Steps

1. Navigate to the experiment directory:
   ```bash
   cd experiments/guardrails-eval
   ```

2. Run the setup script to create the virtual environment and install
   dependencies:
   ```bash
   ./setup.sh
   ```

3. Run the LLM Guard evaluation against the original payloads:
   ```bash
   uv run python eval-llm-guard.py
   ```
```

**What belongs in steps vs. what doesn't:**
- Steps are actions the reader performs. Each step has a command or a short
  instruction.
- Explanations of why a step exists belong in the README, not the HOW_TO.
- If a step requires a decision (e.g., choosing a GCP region), state the
  decision clearly and provide a sensible default.

### 4. Expected Output

Describe what success looks like so the reader can verify the experiment ran
correctly. This can be a file that gets produced, a terminal output pattern, or
a specific result to check.

```markdown
## Expected Output

- `results/` directory is created with timestamped subdirectories
- Each subdirectory contains `summary.md` and per-payload result files
- Terminal shows a comparison table with detection rates per scanner
```

## Checklist

1. **Read the experiment's README** to understand what it does and how it is
   currently run.
2. **Identify all requirements** -- tools, libraries, services, credentials,
   env vars.
3. **Walk through the reproduction steps** mentally or actually. Note every
   command.
4. **Count sequential shell commands.** If any sequence has 5+, extract to a
   script.
5. **Write the four sections** in order: Purpose, Requirements, Steps,
   Expected Output.
6. **Verify links** in the Requirements table resolve to real installation
   pages.
7. **If you created scripts,** ensure they have a shebang, `set -euo pipefail`,
   and are executable.

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Mixing explanation with steps | Steps are commands. Put context in the README. |
| Listing tools without install links | Every tool in Requirements gets a link. |
| Hardcoding secrets or project names | Use env vars with no defaults. Describe what to put in them. |
| Long command sequences without scripts | 5+ sequential commands = extract to a script. |
| Forgetting Expected Output | The reader needs to know if it worked. Always include this. |
| Duplicating the README | HOW_TO is a checklist, not a narrative. Link to the README for background. |
