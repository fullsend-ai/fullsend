# Refine Agent

<img src="icons/refine.png" alt="Refine agent icon" width="80">

Takes exploration context and decomposes a work item into a structured plan of child issues — epics, stories, and tasks — with acceptance criteria, dependencies, and effort estimates.

## How the agent works

The refine agent receives the exploration context produced by the [explore agent](explore.md) and the original issue content. It reads the target codebase, assesses its confidence across multiple dimensions, and produces a complete hierarchical decomposition of the work item into implementable child issues.

The agent runs in a read-only sandbox. It cannot modify issues, push code, or interact with external services. Its only output is a structured JSON refinement plan consumed by the downstream [critique agent](critique.md).

The refine agent always produces a plan — it never halts to ask questions. When information is incomplete, it makes its best judgment, flags assumptions explicitly, and adds open questions for the critique agent to evaluate.

## How it helps

- Turns vague features into actionable work items with testable acceptance criteria.
- Produces full decomposition trees (feature → epics → stories → tasks) in a single pass.
- Names specific APIs, tools, libraries, and configuration patterns — not vague capability references.
- Identifies cross-team dependencies and blocking relationships.
- Reduces manual refinement time from hours of team discussion to minutes of automated analysis.

## Commands

| Command | Where | Effect |
|---------|-------|--------|
| `/fs-refine` | Issue comment | Triggers the pipeline; refine runs automatically after explore |

There is no separate command to invoke refine alone. It runs as the second stage of the refinement pipeline after explore completes.

## Control labels

The refine agent does not add labels. On completion, it chains directly to the [critique agent](critique.md) via `workflow_dispatch`. The post-script removes `refine-needs-input` and `human-refinement` labels (if present) as cleanup from prior runs.

## Configuration and extension

See [Customizing with AGENTS.md](../guides/user/customizing-with-agents-md.md) and
[Customizing with Skills](../guides/user/customizing-with-skills.md).

## Pipeline flow

```
/fs-refine → explore → refine → critique → [approved | revise | needs_input]
                         ↑                          |
                         └── revision feedback ─────┘
```

When the [critique agent](critique.md) returns a `revise` verdict, the pipeline re-triggers refine with the critique feedback as additional context. The refine agent must address each requested revision or explain why it chose a different approach.

## Revision loop

- Critique can send work back to refine with specific, actionable revisions.
- Each revision has a type: `remove`, `merge`, `split`, `revise`, or `add`.
- The refine agent incorporates feedback and produces an updated plan.
- Maximum review rounds default to 3 (configurable via `MAX_REVIEW_ROUNDS`).
- If the limit is reached without approval, the pipeline escalates to a human.

## Output structure

The refinement plan includes:
- Hierarchical children using `parent_title` for tree structure
- Confidence scores per child and per dimension
- Open questions and uncited assumptions (for critique to evaluate)
- A proposed enhanced feature description
- A summary comment for the issue

## Source

[`internal/scaffold/fullsend-repo/harness/refine.yaml`](../../internal/scaffold/fullsend-repo/harness/refine.yaml)
