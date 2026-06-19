---
name: writing-user-docs
description: >-
  Use when writing, editing, or adding user-facing documentation under
  docs/guides/. Use when creating admin guides (install, configure, manage) or
  user guides (workflows, interactions, interventions) for fullsend.
---

# Writing User Documentation

## Overview

User docs are task-oriented guides for two audiences: **administrators** who
install and manage fullsend, and **developers** who work in enrolled repos.
Structure and rules are decided in
[ADR 0023](../../docs/ADRs/0023-user-documentation-structure.md).

## Directory Layout

```
docs/guides/
├── README.md              # Index — update when adding guides
├── admin/                 # Org administrators
│   └── installing-fullsend.md
└── user/                  # Developers in enrolled repos
    └── bugfix-workflow.md
```

## Writing Rules

1. **One audience, one task.** Each guide targets admin or user, not both.
2. **Prerequisites first.** State what the reader needs before step 1.
3. **Steps, not prose.** Numbered steps for procedures. Command first, then
   explain — not the reverse.
4. **Link, don't restate.** Point to ADRs, normative specs, and
   `docs/architecture.md` for architectural context.
5. **Mark planned features.** Use a blockquote callout referencing the issue:

   ```markdown
   > **Planned:** The **fix agent** ([#197](...)) will handle ...
   ```

6. **No jargon without definition.** Link to `docs/glossary.md` or define
   inline on first use.

## Effective Writing

Use the **elements-of-style:writing-clearly-and-concisely** skill when
drafting or editing guides. Key principles:

- Active voice, positive form, concrete language
- Omit needless words — every sentence should earn its place
- Parallel structure in lists and steps

## Checklist

- [ ] File is in the correct directory (`admin/` or `user/`)
- [ ] Prerequisites section exists and is complete
- [ ] Procedures use numbered steps
- [ ] Commands appear before their explanations
- [ ] Planned features use `> **Planned:**` callouts with issue links
- [ ] All internal links resolve (ADRs, specs, other guides)
- [ ] `docs/guides/README.md` index is updated
- [ ] Changes staged and `make lint` passes

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Mixing admin and user content | Split into two guides |
| Explaining architecture inline | Link to `docs/architecture.md` |
| Documenting planned features as current | Add `> **Planned:**` callout |
| Forgetting to update the index | Edit `docs/guides/README.md` |
| Prose paragraphs for procedures | Convert to numbered steps |
