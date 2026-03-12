# Experiment 002: Claude-based ADR Drift Scanner — Design Spec

**Date:** 2026-03-12

## Problem

Experiment 001 built a Python script that detects ADR-0046 drift by comparing image strings against an allowlist. It works, but it only proves that string matching works. The fullsend vision is that agents can enforce architectural invariants in general — including ADRs that express design philosophy, preferred patterns, or intent that can't be reduced to field comparisons. Experiment 001 short-circuited that question.

## Goal

Validate that an LLM (via the `claude` CLI) can read an ADR, understand its architectural intent, evaluate a code artifact against it, and produce a useful analysis — including what's wrong, why it violates the ADR, and what should be done to fix it. No hardcoded rules, no config files, no parsing logic.

## Architecture

A shell script (`scan.sh`) that:

1. Takes two arguments: a local ADR file path and a Tekton task YAML file path
2. Reads both files
3. Constructs a prompt combining the ADR text and the task YAML content
4. Invokes `claude -p --model claude-sonnet-4-20250514` with that prompt
5. Writes claude's prose analysis to stdout

All intelligence lives in claude's comprehension of the ADR. The script is a thin wrapper — no YAML parsing, no image comparison, no domain logic.

URL fetching is out of scope — the caller clones the architecture repo and passes a local path. This keeps the script simple and avoids dealing with GitHub raw URL conversion.

### Prompt template

The prompt is intentionally minimal. It gives claude the two documents and asks for analysis without pre-explaining what compliance looks like — that should come from claude's reading of the ADR.

```
The following is an Architectural Decision Record (ADR) from the konflux-ci project:

---
<ADR content>
---

The following is a Tekton task definition from the same project:

---
<Task YAML content>
---

Analyze this task for compliance with the ADR. For each violation you find, describe:
- What: which step violates the ADR and what it currently does
- Why: what the ADR requires and why this step doesn't comply
- Fix: what should be done to bring the step into compliance

If a step is legitimately exempt per the ADR, note that and explain why.
```

This is a design choice: we do not tell claude what the ADR's invariant is, what images to look for, or what "compliance" means. If the approach works, claude derives all of that from the ADR text itself. That's the point of the experiment.

### CLI invocation

```bash
claude -p --model claude-sonnet-4-20250514 "$prompt"
```

- `-p` (print mode): single-shot, non-interactive, output to stdout
- `--model claude-sonnet-4-20250514`: pinned for reproducibility; Sonnet is sufficient for this task and cheaper than Opus
- No system prompt beyond what's in the prompt itself
- No `--max-tokens` constraint — let the analysis be as long as it needs to be

## Output format

Claude produces a prose report covering, for each violation found:

- **What** — which step violates the ADR and what image it currently uses
- **Why** — what the ADR requires and why this step doesn't comply
- **Fix** — what should be done to bring the step into compliance (e.g., swap the image, add a tool to the task runner first, or note if a legitimate exemption applies)

## Validation approach

We write a hand-crafted **expected output** before running the scanner. This is our own human analysis of what violations exist in the `modelcar-oci-ta` task against ADR-0046 and what should be done about each one.

After running claude, we evaluate its output against our expectations using this rubric:

1. **Violation detection** — Did it find all expected violations? Did it flag anything that isn't a violation?
2. **Exemption recognition** — Did it correctly identify legitimately exempt steps and cite the ADR's reasoning?
3. **Fix quality** — Are the fix recommendations actionable? Did it distinguish between "swap the image today" and "need to add tooling to the task runner first"?
4. **Unexpected insights** — Did claude surface anything we didn't anticipate?

The comparison target is human judgment, not the Python script from Experiment 001.

## File structure

```
experiments/adr46-claude-scanner/
  scan.sh                          # The shell script
  expected/
    modelcar-oci-ta.md             # Hand-written expected analysis
  results/
    modelcar-oci-ta.md             # Claude's actual output (committed after running)
```

The experiment log at `docs/experiments/002-adr46-claude-scanner.md` is the narrative writeup that references these artifacts and evaluates results against the rubric.

## What this proves (or disproves)

- Can claude read an ADR and correctly identify violations without any hardcoded rules?
- Does claude's reasoning match human judgment about why something is a violation?
- Does claude produce actionable fix recommendations?
- Does the approach generalize — could you swap in a different ADR and a different code artifact and get useful results without changing the script?

## Out of scope

- URL fetching (caller provides local file paths)
- Running against the full build-definitions repo (batch scanning)
- Filing GitHub issues automatically
- Generating fix PRs
- Comparing performance or accuracy against the Python scanner
- Token usage / cost tracking (interesting but not the question being asked)
