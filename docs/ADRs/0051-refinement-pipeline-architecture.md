---
title: "51. Refinement Pipeline Architecture"
status: Accepted
relates_to: []
topics:
  - agents
  - refinement
  - pipeline
  - multi-agent orchestration
---

# ADR 0051: Refinement Pipeline Architecture

## Status

Accepted

## Context

fullsend currently ships 6 standalone agents (triage, code, review, fix, retro, prioritize), each triggered by a single event and producing a single output. The Feature Refinement Working Group has developed and validated a multi-stage pipeline that decomposes high-level issues into implementable work items through exploration, refinement, and quality-gated critique.

This pipeline extends fullsend's existing patterns:

1. **Chained agents with artifact passing** (new): Existing agents are standalone — triage triggers code via a `ready-to-code` label, which fires a webhook, and the code agent reads the issue fresh from GitHub. No data passes between them. The refinement pipeline is different because each stage produces structured context that the next stage needs: explore produces `exploration_context.json` (~50-100KB of analyzed research), refine produces `refine-result.json` (the full decomposition plan with children, acceptance criteria, etc.), and critique needs both to evaluate the plan. Labels can signal "go/no-go" but can't carry a run ID for artifact correlation, so we use explicit `workflow_dispatch` chaining instead. For example, post-explore.sh chains to refine like this:

   ```
   gh workflow run refine.yml \
     --repo "$WORKFLOW_REPO" \
     -f issue_key="PROJ-123" \
     -f issue_source="jira" \
     -f explore_run_id="$GITHUB_RUN_ID"
   ```

   The refine pre-script then downloads explore's artifact using that run ID:

   ```
   gh run download "$EXPLORE_RUN_ID" \
     --repo "$REPO" \
     --name "fullsend-explore" \
     --dir "$ARTIFACT_DIR"
   ```

2. **Revision loops** (extension of review → fix): Critique can send work back to refine for up to N rounds (default 3) before escalating to a human. This mirrors the existing review → fix iteration pattern (`FIX_ITERATION` cap, default 5) but uses `workflow_dispatch` chaining instead of PR event triggers, since the critique feedback artifact needs to be passed back to refine.

The pipeline has been validated in fullsend-ai/features as custom agents over several weeks with consistent quality improvements in issue decomposition and reduced manual refinement time.

## Decision

We will add three new agent roles to the fullsend scaffold: **explore**, **refine**, and **critique**. These agents form a single refinement pipeline, triggered by the `/fs-refine` command on an issue.

This is a **Phase 1 implementation** focused on getting the pipeline functional and available to all fullsend installations. Later phases will harden, optimize, and extend it based on production feedback.

### Phase 1 decisions (this PR)

**Separate GitHub Apps**: Each agent (explore, refine, critique) gets its own GitHub App, consistent with every other fullsend agent. For the GitHub workflow, this provides a clear audit trail — each agent's comments and actions appear under a distinct bot identity. It also enables granular access control and avoids coupling permissions if the agents' needs diverge. Note: for the Jira workflow, all three agents share the same `JIRA_EMAIL` service account, so Jira comments are attributed to a single bot user regardless of which agent posted them.

**Default enabled, command-gated**: The agents ship with every fullsend installation but only activate when a user explicitly invokes `/fs-refine`. There are no automatic triggers — the pipeline never runs unless a human requests it.

**Pipeline chaining via workflow_dispatch** (not event-driven): Existing agent chains use labels + webhooks (e.g., triage adds `ready-to-code` → code agent triggers on `issues.labeled`). This works when agents are independent — code doesn't need triage's output, it reads the issue directly. The refinement pipeline uses explicit `gh workflow run` instead because each stage needs the previous stage's artifact, identified by run ID. Labels can't carry this correlation data.

**Revision loop with hard cap**: Critique can return a "revise" verdict, causing post-critique.sh to re-trigger refine.yml with critique feedback as additional context. MAX_REVIEW_ROUNDS (default 3) prevents infinite loops. If the cap is reached, the plan is presented to a human for final decision.

**Separate dispatch workflows**: The pipeline uses separate dispatch files (refine-dispatch.yml, jira-dispatch.yml) rather than integrating into the existing dispatch.yml. We chose this because it matches the validated pattern from fullsend-ai/features and avoids modifying the shared dispatch routing logic in Phase 1.

**No eval system**: The features repo has a full eval system (MLflow baselines, fixture evals, eval-gate.yml). This is intentionally excluded from Phase 1 to reduce PR scope and review burden.

### Deferred to Phase 2+

| Decision | Rationale for deferral |
|----------|----------------------|
| Consolidate dispatch into dispatch.yml | Blocked on #1985 (harness-driven dispatch). Once that lands, the separate dispatch files should be migrated. |
| Eval system integration | Complex (MLflow, baselines, scorer configs). Ship agents first, add eval infrastructure once we have production data to calibrate against. |
| Automatic triggers (e.g., trigger on issue creation with certain labels) | Phase 1 is deliberately manual-only to build confidence. Automatic triggers should be a separate ADR once we have usage data. |
| Single-workflow pipeline mode | Running all 3 stages in one workflow (no cross-run artifacts) would eliminate data leakage risk for public repos. Requires significant harness changes. |
| Dynamic role registration (#1985) | Currently adding roles to the hardcoded ValidRoles() list. Once #1985 lands, these should migrate to harness-declared roles. |

## Consequences

### Positive

- Teams get automated issue decomposition from day one after installation
- The pipeline is entirely opt-in (requires explicit `/fs-refine` command)
- Chaining pattern is reusable for future multi-stage workflows
- Revision loop ensures quality without human intervention for routine work

### Negative

- Three new roles (each with its own GitHub App) increase the total agent count from 7 to 10 and add 3 apps to install
- Pipeline chaining is a new pattern that adds complexity to the scaffold
- Artifact-based data passing has a 90-day retention limit
- Separate dispatch files add workflow sprawl (mitigated by planned consolidation in Phase 2)

### Risks

- The revision loop could produce unnecessary iterations on simple issues (mitigated by confidence scoring and max rounds)
- Artifact downloads between workflow runs add latency (~30s per stage transition)
- Private Jira data could leak via public repo artifacts/logs (mitigated by a safety check that blocks Jira sources on public repos — see ADR 0052)
