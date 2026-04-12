# Fullsend Claude Code Agent Team

This directory configures a Claude Code Agent Team for working on the fullsend project.

## Prerequisites

- Claude Code v2.1.32 or later
- Agent teams enabled (configured in settings.json)

## The Seven Agents

### fullsend-architect (opus)
The architectural coherence guardian. Knows all adopted ADRs, the five execution
layers, the story dependency graph, and the repo-as-coordinator invariant.

Use first when: starting a new story, evaluating a new design proposal, or when
stories seem to be conflicting.

  "@fullsend-architect review this ADR proposal for conflicts with existing decisions"
  "@fullsend-architect what are the story dependencies I need to resolve before PR #133?"

### go-developer (sonnet)
Go CLI specialist. Knows cmd/fullsend/, internal/ packages, the forge abstraction
(ADR 0006), the layered config model, and the multi-role GitHub App model (ADR 0009).

Use for: implementing features, fixing Go tests, extending the forge interface.

  "@go-developer implement headless mode for the GitHub App manifest flow"
  "@go-developer fix the pagination cap in the forge client"

### doc-architect (sonnet)
Problem document and ADR writer. Knows fullsend's design-exploration conventions:
multiple options, trade-offs, open questions, org-agnostic content.

Use for: writing new problem docs, drafting ADRs, checking doc consistency.

  "@doc-architect draft ADR 0009 for the per-role GitHub Apps decision"
  "@doc-architect review this problem doc for org-specific content that should be moved"

### stage-prompt-designer (opus)
The agent that designs agents. Knows the exact requirements, constraints, and
known failure modes for each pipeline stage: triage, implement, review (all 6
sub-agents), and fix.

Use for: designing or reviewing any stage agent system prompt or skill.

  "@stage-prompt-designer review the triage prompt for injection surface issues"
  "@stage-prompt-designer design the coordinator algorithm for the review swarm"
  "@stage-prompt-designer the fix agent is modifying workflow files — add the guard"

### security-reviewer (opus)
Applies fullsend's threat model to everything. Threat priority: external prompt
injection > insider/credentials > DoS > drift > supply chain.

Use on: every implementation PR, every agent prompt, every workflow change.
This agent is a blocking gate — Critical findings must be resolved before merge.

  "@security-reviewer review this workflow YAML for injection vectors"
  "@security-reviewer check PR #123 for ADR 0017 credential isolation compliance"

### workflow-engineer (sonnet)
GitHub Actions specialist. Owns the dispatch and coordination layer (Layer 1):
label state machine, event triggers, slash commands, concurrency groups, and
the reusable workflow architecture (Issue #007).

Use for: workflow YAML design, fixing trigger issues, concurrency group design.

  "@workflow-engineer fix the concurrent fix agent cancellation (Issue #004)"
  "@workflow-engineer design the reusable workflow_call structure for Issue #007"

### e2e-integrator (opus)
Full system integration view. Can trace the complete issue → triage → implement →
review → fix → merge flow and identify every gap and disconnected handoff.

Use when: planning a sprint, preparing for a demo, assessing overall progress,
or when stories seem to be diverging.

  "@e2e-integrator trace the full flow and tell me what's blocking the next demo"
  "@e2e-integrator what should our team of 8 prioritize this sprint given dependencies?"

## Typical team patterns

### Reviewing a new design proposal
  Create a team with fullsend-architect, security-reviewer, and e2e-integrator.
  Have them all review the proposal from their perspectives, then synthesize.

### Implementing a new Go feature
  @go-developer implements → @security-reviewer reviews → @fullsend-architect
  checks architectural fit

### Designing a stage agent prompt
  @stage-prompt-designer drafts → @security-reviewer checks for injection vectors →
  @fullsend-architect validates against ADR 0016/0017 → @workflow-engineer
  checks the trigger/dispatch side

### Sprint planning
  @e2e-integrator traces full flow → @fullsend-architect maps dependencies →
  team decides priority order

### Writing a new ADR
  @fullsend-architect identifies decision is mature → @doc-architect drafts →
  @fullsend-architect validates for contradictions → @security-reviewer checks
  security implications
