# Critique Agent

<img src="icons/critique.png" alt="Critique agent icon" width="80">

Reviews the refinement plan for quality, completeness, and feasibility, then decides whether to approve it, request revisions, or escalate to a human.

## How the agent works

The critique agent receives the exploration context, the original issue, and the refinement plan produced by the [refine agent](refine.md). It evaluates the plan against six quality dimensions, cross-checks the refine agent's self-reported confidence, and scrutinizes assumptions that lack evidence from the exploration context.

The agent runs in a read-only sandbox. It cannot modify issues, push code, or interact with external services. Its only output is a structured JSON verdict consumed by the post-script, which either creates child issues (on approval), re-triggers refine (on revise), or posts a question comment (on needs_input).

Nothing gets created until this agent approves.

## How it helps

- Prevents over-decomposition (15 issues when 6 would suffice) from flooding the backlog.
- Catches vague children that restate the parent without adding specificity.
- Identifies missing coverage — entire dimensions of a feature forgotten by refine.
- Detects dependency cycles and impossible ordering.
- Guards against scope creep — children that exceed what the parent asked for.
- Catches "assumption laundering" — plans that look implementable but are built on unverified guesses.

## Control labels

Labels are applied by `post-critique.sh` based on the verdict (GitHub flow only):

| Label | When applied |
|-------|-------------|
| `refine-approved` | Plan approved (with `auto_create=false`), or max review rounds reached (escalation). |
| `refine-needs-input` | Human input is required before the pipeline can proceed (`needs_input` verdict). |
| `refine-needs-human` | Max review rounds reached — critique still has concerns, human must decide. |
| `refine-stalled` | Max review rounds reached — added alongside `refine-needs-human` to signal the pipeline has stopped. |

## Configuration and extension

See [Customizing with AGENTS.md](../guides/user/customizing-with-agents-md.md) and
[Customizing with Skills](../guides/user/customizing-with-skills.md).

## Verdicts

| Verdict | Meaning | What happens next |
|---------|---------|-------------------|
| `approved` | Plan is ready for child issue creation | Post-script creates issues (if `auto_create=true`) or posts approval comment |
| `revise` | Plan has fixable problems | Post-script re-triggers refine with critique feedback |
| `needs_input` | Human must answer a question before proceeding | Post-script posts a focused question as an issue comment |

## Auto-create vs. human gate

When the critique agent approves a plan:

- **`auto_create=true`**: The post-script automatically creates child issues in the target tracker (GitHub or Jira).
- **`auto_create=false`** (default): The post-script posts an approval comment with the proposed plan. A human must comment `/fs-create` to trigger issue creation.

This gives teams control over whether refinement is fully automated or requires human sign-off before backlog changes.

## Review rounds

- The critique agent tracks its review round via `REVIEW_ROUND` and has access to prior feedback via `CRITIQUE_HISTORY`.
- On re-reviews (round 2+), it focuses on whether specific requested revisions were addressed rather than re-evaluating the entire plan.
- Maximum review rounds default to 3 (configurable via `MAX_REVIEW_ROUNDS`).
- When close to the iteration limit, the threshold lowers — it approves with notes rather than forcing another round that would hit the cap.
- If the limit is reached without approval, the pipeline escalates to a human regardless of verdict.

## Assessment dimensions

| Dimension | What it checks |
|-----------|---------------|
| Coverage | Every requirement dimension has corresponding children |
| Granularity | Children are sized appropriately (epics = team-sized, stories = engineer-sized) |
| Dependency coherence | No cycles, achievable ordering, cross-team deps called out |
| Implementability | Engineers can read each child and know what to build |
| Scope accuracy | Children collectively match parent scope — no more, no less |
| Assumption grounding | Architectural decisions backed by evidence, not speculation |

## Commands

| Command | Where | Effect |
|---------|-------|--------|
| `/fs-refine` | Issue comment | Triggers the pipeline; critique runs automatically after refine |
| `/fs-create` | Issue comment | Creates child issues from an approved plan (when `auto_create=false`) |

## Source

[`internal/scaffold/fullsend-repo/harness/critique.yaml`](../../internal/scaffold/fullsend-repo/harness/critique.yaml)
