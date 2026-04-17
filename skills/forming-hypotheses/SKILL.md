---
name: forming-hypotheses
description: >-
  Use when you have a trace evidence bundle and need to determine why an
  agent-driven outcome was suboptimal — root cause analysis for agent
  failures, rework, and missed opportunities.
---

# Forming Hypotheses

From a trace evidence bundle, produce a root cause hypothesis specific enough
to guide a fix.

## Process

### 1. State the symptom concretely

Name the observable failure: "review agent requested changes for missing
deprecation notices on 3 of 5 API PRs" — not "the agent didn't do a good job."

### 2. Classify the failure mode

| Mode | Symptom pattern |
|------|----------------|
| **Missing context** | Output reasonable given what agent knew, but it lacked key information |
| **Wrong assumption** | Reasoning coherent but based on a false premise |
| **Skill gap** | Agent skipped a needed step or followed one that doesn't apply |
| **Tool limitation** | Approach would have worked with better tooling |
| **Prompt weakness** | Agent followed instructions correctly but they led to bad outcome |
| **Model capability** | Reasoning incoherent or contradicts its own evidence |
| **Configuration error** | Agent didn't run, ran wrongly, or was blocked by wrong policy |
| **Working as designed** | System did what it was told; the design may need revisiting |

### 3. Form a testable hypothesis

State the root cause as a specific, testable, actionable claim. Name the
component, file, skill, or configuration involved.

Example: "The implementation agent lacks access to `docs/api-guidelines.md`,
so it doesn't know API removals require deprecation notices."

### 4. Assess confidence

- **High** — evidence directly supports the hypothesis (e.g., agent comment
  says "I did not find any deprecation policy" and the policy exists)
- **Medium** — consistent with evidence but other explanations possible
- **Low** — plausible but speculative; evidence is indirect

### 5. Recommend experiments (when confidence < high)

Describe a reproducible, discriminating, single-variable experiment.

## Constraints

- Distinguish symptoms from causes. "4 rounds of rework" is a symptom.
- Trace back through the pipeline — don't assume the most recent failure
  is the root cause.
- Note whether this is a first occurrence or a recurring pattern.
- If the outcome is "working as designed," say so.
