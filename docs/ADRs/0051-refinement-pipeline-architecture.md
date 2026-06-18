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

fullsend currently ships 6 standalone agents (triage, code, review, fix, retro, prioritize). Each is triggered by a single event and produces a single output. When agents relate to each other today, the coupling is loose: triage adds a `ready-to-code` label, which fires a webhook, and the code agent reads the issue fresh from GitHub. No data passes between them — each agent is self-contained.

The Feature Refinement Working Group needs a pipeline that decomposes high-level issues into implementable work items through three stages: exploration, refinement, and quality-gated critique. This pipeline cannot work with fullsend's current standalone-agent model because each stage produces structured context that the next stage consumes:

- **explore** produces `exploration_context.json` (~50-100KB of analyzed research, codebase findings, prior art)
- **refine** consumes that context and produces `refine-result.json` (the full decomposition plan with children, acceptance criteria, etc.)
- **critique** consumes both to evaluate the plan against the original issue

Labels can signal "go/no-go" but cannot carry the structured data or the run ID needed to locate a previous stage's artifacts. This means the existing pattern of loose coupling via labels + webhooks is insufficient — we need a way for agents to explicitly chain to each other and pass data between runs.

## Decision

We will introduce **agent chaining** as a new orchestration pattern in fullsend: the ability for one agent's post-script to trigger a subsequent agent via `workflow_dispatch`, passing a run ID that the next agent uses to download the previous stage's artifacts.

The first use of this pattern is the refinement pipeline — three new agent roles (**explore**, **refine**, **critique**) that chain together to decompose issues. But the pattern itself is general-purpose and available for future multi-stage workflows.

### How chaining works

A post-script chains to the next stage by invoking `gh workflow run` with the current run ID:

```
gh workflow run refine.yml \
  --repo "$WORKFLOW_REPO" \
  -f issue_key="PROJ-123" \
  -f issue_source="jira" \
  -f explore_run_id="$GITHUB_RUN_ID"
```

The downstream pre-script downloads the upstream artifact using that run ID:

```
gh run download "$EXPLORE_RUN_ID" \
  --repo "$REPO" \
  --name "fullsend-explore" \
  --dir "$ARTIFACT_DIR"
```

This extends the existing review → fix iteration pattern (which uses `FIX_ITERATION` cap, default 5, with PR event triggers) to support `workflow_dispatch`-based loops where critique feedback artifacts must be passed back to refine.

### Phase 1 decisions (this PR)

This is a **Phase 1 implementation** focused on getting the chaining pattern and refinement pipeline functional. Later phases will harden, optimize, and extend based on production feedback.

**Separate GitHub Apps**: Each agent (explore, refine, critique) gets its own GitHub App, consistent with every other fullsend agent. For the GitHub workflow, this provides a clear audit trail — each agent's comments and actions appear under a distinct bot identity. It also enables granular access control and avoids coupling permissions if the agents' needs diverge. Note: for the Jira workflow, all three agents share the same `JIRA_EMAIL` service account, so Jira comments are attributed to a single bot user regardless of which agent posted them.

**Default enabled, command-gated**: The agents ship with every fullsend installation but only activate when a user explicitly invokes `/fs-refine`. There are no automatic triggers — the pipeline never runs unless a human requests it.

**Revision loop with hard cap**: Critique can return a "revise" verdict, causing post-critique.sh to re-trigger refine.yml with critique feedback as additional context. MAX_REVIEW_ROUNDS (default 3) prevents infinite loops. If the cap is reached, the plan is presented to a human for final decision.

**Separate dispatch workflows**: The pipeline uses separate dispatch files (refine-dispatch.yml, jira-dispatch.yml) rather than integrating into the existing dispatch.yml. We chose this because it matches the validated pattern from fullsend-ai/features and avoids modifying the shared dispatch routing logic in Phase 1.

**No eval system**: The features repo has a full eval system (MLflow baselines, fixture evals, eval-gate.yml). This is intentionally excluded from Phase 1 to reduce PR scope and review burden.

### Alternatives considered

**Single-workflow pipeline**: Run all three stages as sequential jobs within one GitHub Actions workflow. Data passes via job outputs or workspace sharing — no cross-run artifact downloads, no run-ID correlation, and artifacts never leave the run (eliminating the data leakage risk for public repos entirely). This would also reduce the GitHub App count from 3 to 1, since all stages run under a single workflow identity. We deferred this because the harness currently expects one agent = one workflow run, and changing that is a significant architectural lift. It also means a failure in stage 3 requires re-running the entire pipeline, and individual agent actions are no longer attributable in the GitHub UI. This remains the strongest candidate for Phase 2 (see Deferred table below). Related existing work: #234 (design validation loop and multi-agent orchestration patterns for harness definitions) and #1817 (investigate deterministic orchestration for intra-stage multi-agent pipelines).

**Issue comments as data store**: Write structured context into issue comments (e.g., a machine-readable JSON block) instead of artifacts, so downstream agents read the issue like every other fullsend agent. This is closest to fullsend's current model. We rejected it because GitHub comments have a 65,536-character limit and exploration context can reach 50-100KB, making data truncation likely. It also pollutes the issue with large machine-readable blobs that aren't useful to humans.

**Repository dispatch with client payload**: Use `repository_dispatch` events with a structured `client_payload` instead of `workflow_dispatch` inputs. Similar to our chosen approach, but `client_payload` is limited to 10 top-level properties and has payload size constraints that would require compression or external storage for larger artifacts — adding complexity without clear benefit over `workflow_dispatch` + run-ID correlation.

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

- Agent chaining is a general-purpose pattern — future multi-stage workflows (e.g., plan → implement → validate) can reuse the same `workflow_dispatch` + run-ID artifact correlation mechanism without inventing new infrastructure
- Teams get automated issue decomposition from day one after installation
- The pipeline is entirely opt-in (requires explicit `/fs-refine` command)
- Revision loop ensures quality without human intervention for routine work

### Negative

- Agent chaining introduces coupling between agents that didn't exist before — downstream agents depend on upstream artifact structure, naming, and availability
- Three new roles (each with its own GitHub App) increase the total agent count from 7 to 10 and add 3 apps to install
- Artifact-based data passing has a 90-day retention limit (GitHub Actions constraint)
- Separate dispatch files add workflow sprawl (mitigated by planned consolidation in Phase 2)

### Risks

- The revision loop could produce unnecessary iterations on simple issues (mitigated by confidence scoring and max rounds)
- Artifact downloads between workflow runs add latency (~30s per stage transition)
- Private Jira data could leak via public repo artifacts/logs (mitigated by a safety check that blocks Jira sources on public repos — see ADR 0052)
