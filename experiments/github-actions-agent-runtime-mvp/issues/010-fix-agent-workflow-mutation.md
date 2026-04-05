# 010: Fix Agent Modifying Workflow Files Breaks Agent Pipelines

## Problem

The fix agent has unrestricted write access to all files, including `.github/workflows/`. When it modifies workflow files to address review feedback, it can introduce YAML syntax errors or behavioral regressions that break the agent pipelines — including the review agent that governs it.

## Observed ([PR #14](https://github.com/nonflux/integration-service/pull/14))

1. Review agent requested changes on a workflow restructuring PR
2. Fix agent pushed commit `d165c91` addressing feedback
3. Next review agent run failed: `Invalid format 'SCRIPT_EOF Syntax Errors: [ 'Error node: "<" at 0:0' ]'`
4. Autonomous loop halted until manual intervention

## Proposed Fix

Combine prompt constraint with validation:

1. **Prompt constraint** — instruct fix agent not to modify `.github/workflows/`, `.github/scripts/`, or `GEMINI.md` unless review explicitly requests workflow changes
2. **`actionlint` validation** — run syntax checking on any modified workflow files before pushing
3. **Escalate** — if workflow changes are needed, post a comment and let a human implement them
