---
title: "23. User documentation structure"
status: Accepted
relates_to:
  - governance
  - human-factors
topics:
  - documentation
---

# 23. User documentation structure

Date: 2026-04-22

## Status

Accepted

## Context

Fullsend's existing documentation serves contributors and decision-makers: problem documents explore design space, ADRs record decisions, and normative specs define contracts. None of it is aimed at the two audiences who need operational guidance:

1. **Administrators** — people who install fullsend in a GitHub org, configure agents, manage credentials, and enroll repositories.
2. **Users** — developers who work in repositories where fullsend is active and need to understand how to interact with it.

As adoption grows, this gap becomes a blocker. New admins have to reverse-engineer the install flow from Go source and normative specs. New developers have no documentation explaining what triggers agents, what to expect from agent-generated PRs, or how to intervene when something goes wrong.

The question is how to structure user-facing documentation so it stays organized as the guide count grows.

## Options

### Option A: Audience-based split

Two directories under `docs/guides/`:

- `docs/guides/admin/` — guides for org administrators
- `docs/guides/user/` — guides for developers in enrolled repos

Each guide is a standalone how-to document focused on one task. Guides link to ADRs and `docs/architecture.md` for deeper context but never require reading those to complete the task.

**Trade-offs:** Clear audience separation makes it obvious who a doc is for. Scales naturally as guides are added. Requires maintaining an index.

### Option B: Single flat directory

All guides in `docs/guides/` with naming conventions (e.g. `admin-installing-fullsend.md`, `user-bugfix-workflow.md`).

**Trade-offs:** Simpler initially, but the audience split becomes invisible as the guide count grows. Naming conventions are easy to forget.

## Decision

Adopt **Option A: audience-based split** with the following structure and writing rules.

### Directory structure

```
docs/guides/
├── README.md           # Index linking both sections
├── admin/              # Guides for org administrators
│   └── installing-fullsend.md
└── user/               # Guides for developers
    └── bugfix-workflow.md
```

### Writing rules

1. **One audience, one task.** Each guide targets one audience (admin or user) and walks through one task.
2. **Prerequisites first.** State what the reader needs before they start.
3. **Steps, not prose.** Use numbered steps for procedures. Show the command, then explain what it does — not the reverse.
4. **Link, don't restate.** Reference ADRs, normative specs, and `docs/architecture.md` for architectural context. Do not duplicate their content.
5. **Mark planned features.** Features that do not exist yet use a `> **Planned**` callout that references the tracking issue.
6. **No jargon without definition.** If a term has a specific meaning in fullsend, link to `docs/glossary.md` or define it inline on first use.

### Anticipated admin topics

- Installing fullsend (initial)
- Adding, removing, or disabling agents
- Configuring agent behavior
- Setting up external compute
- Connecting multiple independent fullsend installations for cross-instance flow

### Anticipated user topics

- Bugfix workflow (triage → code → review → merge)
- Issue prioritization and backlog management
- Feature discovery and refinement
- Architecture drift management
- Automated threat modelling and red-teaming

## Consequences

- Admins and users each have a clear place to look for guidance, separate from the design-oriented problem documents and normative specs.
- New guides follow a consistent structure enforced by the writing rules, reducing maintenance burden.
- The audience split must be maintained as new guides are added; the `docs/guides/README.md` index is the enforcement point.
- Existing design documents (`docs/problems/`, `docs/ADRs/`, `docs/architecture.md`) remain unchanged — guides link into them, not the other way around.

## Revision (2026-05)

The original `admin/` directory was split into `getting-started/` and `infrastructure/` to better reflect the distinct audiences: **org maintainers** who onboard organizations (getting-started) vs. **platform operators** who deploy and manage GCP infrastructure like the token mint and WIF (infrastructure). The `user/` and `dev/` directories remain unchanged. See `docs/guides/README.md` for the current layout.
