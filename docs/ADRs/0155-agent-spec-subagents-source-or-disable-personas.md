---
title: "155. Agent-spec sub-agent entries may source or disable personas"
status: Accepted
relates_to:
  - agent-architecture
  - code-review
topics:
  - config
  - sub-agents
  - review
---

# 155. Agent-spec sub-agent entries may source or disable personas

Date: 2026-10-31

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

Each agent entry's `subagents` map sets a model per persona
([ADR 0104](0104-per-persona-model-resolution.md)). It cannot point a persona
at a different file or turn one off. Harness `skills:` entries can replace or
remove files inside a pinned skill, but staging does not apply those overrides
yet ([#6158](https://github.com/fullsend-ai/fullsend/issues/6158)). So today
the only way to add, replace or remove a review persona
([review sub-agent decomposition](../problems/code-review.md#review-sub-agent-decomposition))
is to copy the whole skill, and every upstream change to the skill must then
be ported by hand. Copying does not make selection deterministic either: the
orchestrator still chooses among the copied personas.

The review skill already departs from the scripted pipeline of
[ADR 0018](0018-scripted-pipeline-for-multi-agent-orchestration.md) by relying
on a fixed roster. The roster stays fixed for each run: it is the staged set,
read from the trusted ref, not something the change's author picks. This ADR
does not supersede ADR 0018.

This ADR aims to let a repository replace, add or disable a persona from its
agent spec without forking the skill that ships it.

## Options

- **A registry file in the skill** (for example a JSON roster the
  orchestrator reads): rejected. The orchestrator LLM still reads and judges
  its rows, so the roster moves but the host guarantees nothing, and it adds a
  second config surface.
- **Per-dimension env flags on the harness** (in the style of the existing
  `REVIEW_UX_ENABLED`): rejected. One flag per persona does not scale, cannot
  add or replace a persona, and each flag needs matching code in the skill
  and the post-script.
- **Harness `skills:` overrides only**: works once #6158 lands, but it needs a
  custom harness and knowledge of the skill's internal file layout. It stays
  the low-level mechanism, not the user surface.
- **Object form on `agents[].subagents`** (chosen): one user surface, one
  mechanism underneath and one host gate.

## Decision

We extend `agents[].subagents` so that each persona entry is a string or an
object, to let a repository replace, add or disable a persona without forking
the skill.

1. **Two forms.** An entry is a string or an object `{model, source, enabled}`
   with every field optional. A string is shorthand for `{model: <value>}`, so
   ADR 0104's resolution is unchanged and still applies only where it is
   implemented (pi, and codex when the agent delegates). The `default` key
   takes only the string form.
2. **Compiles into skill file overrides.** The runner turns `source` and
   `enabled: false` into harness skill file overrides when it composes the
   harness, in the step `fullsend lock` shares. These overrides win over a
   harness `skills:` override for the same file. Staging applies them before
   persona discovery, on every runtime. `enabled: false` removes
   `sub-agents/<persona>.md` from every staged skill that ships it, and
   `source` replaces the file in the first skill that ships it. A `source` for
   a persona that no staged skill ships adds the file to the agent's first
   skill that has personas, and the run fails if there is none. A `source`
   file whose frontmatter `name` differs from its `subagents` key is an error.
3. **`source` and `enabled: false` cross a trust boundary.** Like the guarded
   fields of [ADR 0112](0112-overlays-may-set-any-harness-field.md), they are
   read only from config at the trusted ref, and so is the file a `source`
   path names. They are never read from an overlay, a flag, an environment
   variable or the change under review, so a pull request cannot change its
   own review. A `source` is either a path that stays inside `.fullsend/` (no
   `..`, no absolute path, no symlink escape), or a URL pinned with
   `#sha256=`, on `allowed_remote_resources`, with a lock entry that
   `fullsend lock` writes and the run checks.
4. **`enabled: false` is a host guarantee.** The persona file is not staged,
   so no child can be dispatched as that persona. As a backstop, the review
   post-script drops findings whose agent-reported `dimension` names a persona
   that was not staged. This filters mislabelled output; it is not proof of
   what ran.
5. **Layering.** Layers merge per field, and `key: ~` removes an inherited
   entry (ADR 0104). ADR 0104's rule against `subagents` entries in shared
   presets still holds for every entry except `enabled: false`.
6. **No persona semantics in the runner.** fullsend checks shape only: types
   and the `source` trust rules. A persona's frontmatter `name` must match its
   file name, and when two skills ship the same name the first wins, with a
   warning. The skill decides what to do when, for example, `correctness` is
   missing.
7. **Selection is unchanged.** The review agent still chooses which dimensions
   run, now only from the staged personas.
8. **Unknown keys.** A key without `enabled: false` that names a persona the
   run did not discover stays an error, unless its `source` defines that
   persona. An entry with `enabled: false` never errors, and its other fields,
   inherited ones included, are ignored. When no skill ships that persona, the
   run log gets a warning. An org-wide disable then works in every
   repository, and a typo shows up.

```yaml
agents:
  - name: review
    subagents:
      default: sonnet              # string form, as in ADR 0104
      style-conventions:
        enabled: false             # not staged
      correctness:
        source: .fullsend/personas/correctness.md
      licensing:                   # added: source defines the persona
        model: opus
        source: https://raw.githubusercontent.com/example-org/personas/<sha>/licensing.md#sha256=<sha256>
```

## Forge scope

Forge-neutral: config, staging and the post-script do not depend on the
forge. It applies on GitHub and GitLab, each with its own trusted ref, and on
Forgejo once that forge lands.

## Consequences

- A repository can add, replace or disable a review persona from config
  without copying the skill, and keeps upstream updates for everything else.
- Disabling is guaranteed on the host because the persona is not staged,
  while choosing dimensions stays the review agent's judgement.
- A `source` change crosses a trust boundary and gets the same review,
  pinning and locking as a skill or harness base.
- The CLI gains a way to set `source` and to disable a persona, implemented
  after this ADR is approved.
- Disabling or adding a persona takes effect only once the review skill builds
  its roster from the staged persona files and fills in `dimension`, which is
  agents-repository work that must ship first.
