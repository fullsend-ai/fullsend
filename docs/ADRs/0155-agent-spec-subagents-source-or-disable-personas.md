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
[ADR 0018](0018-scripted-pipeline-for-multi-agent-orchestration.md) and
relies on a fixed roster instead. With this decision the roster is still
fixed for each run: it is the staged set, read from the trusted ref, not
something the change's author picks. This ADR does not supersede ADR 0018.

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

1. **Two forms.** `agents[].subagents.<persona>` accepts a string, which is
   the model exactly as today (ADR 0104's string form and resolution order are
   unchanged). It also accepts an object `{model, source, enabled}`. Every
   field is optional, and `model` means the same as the string form. A string
   value is shorthand for `{model: <value>}`. The reserved `default` key
   accepts only the string form.
2. **Compiles into skill file overrides.** At staging, the runner turns
   `source` and `enabled: false` into the harness skill file overrides. They
   replace or remove `sub-agents/<persona>.md` in the skill that ships the
   persona. A `source` for a persona that no staged skill ships adds
   `sub-agents/<persona>.md` to the agent's first skill that already ships
   personas. The sourced file's frontmatter `name` must equal the key. The
   overrides are applied before persona discovery, so discovery and model
   resolution see the staged tree. There is one mechanism underneath:
   `subagents` is the recommended surface, and harness `skills:` overrides stay
   available.
3. **`source` crosses a trust boundary.** Like the guarded fields of
   [ADR 0112](0112-overlays-may-set-any-harness-field.md), it is accepted only
   from config at the trusted ref (the same ref `config.yaml` and the harness
   are read from), and never from an overlay, a CLI flag, an environment
   variable or the change under review. It is one of:
   - a repository path, resolved against the same `.fullsend/` directory as
     harness skill overrides, which must not leave it (no `..`, no absolute
     path, no symlink escape);
   - a URL pinned with `#sha256=`, whose prefix is on
     `allowed_remote_resources`, with a lock entry. `fullsend lock` runs the
     same compile step as the run, and a run fails on an unlocked or
     mismatched `source`.

   Because the config, the path and the URL allowlist are all read from the
   trusted ref, a pull request cannot change its own review.
4. **`enabled: false` is a host guarantee.** The persona file is not staged,
   so no child can be dispatched as that persona. As a backstop, findings
   record the persona that produced them in a `dimension` field that the
   review skill fills in, and the review post-script drops findings whose
   `dimension` names a persona that was not staged. The field is reported by
   the agent, so this filters mislabelled output and is not proof of what ran.
   This does not stop the parent agent from doing the work itself or spawning
   an anonymous helper, and it does not need to: such output carries no staged
   dimension.
5. **Layering.** Org and repo layers merge per field, so a repo can set only
   `model` on a `source` the org supplies. ADR 0104's rule that a shared
   preset must not carry `subagents` entries still holds for `model` and
   `source` entries until the merge records which layer a key came from.
   `key: ~` removes the whole inherited entry, as ADR 0104 defines.
6. **No persona semantics in the runner.** fullsend checks only the shape:
   types, the `source` trust rules and duplicate persona names. It does not
   know which personas a skill needs. If, for example, the skill's
   `correctness` persona is missing, the skill decides what to do.
7. **Selection is unchanged.** The review agent still chooses which
   dimensions to run on a given change, but only from the staged personas.
   This ADR does not make selection deterministic.
8. **One existing error is relaxed.** Today a `subagents` key that names a
   persona the run did not discover is an error. Three cases follow:
   - a `model`-only entry that names no persona in the staged tree stays an
     error;
   - an entry with `source` defines the persona, so it is never undiscovered;
   - an `enabled: false` entry for a persona that no staged skill ships is
     ignored, with a warning in the run log naming the discovered personas.
     An org-wide disable then still works in repositories without the
     persona, and a typo shows up.
9. **Runtimes.** `source` and `enabled` act at staging, so they apply on every
   runtime. `model` applies where ADR 0104's resolution is implemented (pi,
   and codex when the agent delegates); elsewhere it is ignored as today.

```yaml
agents:
  - name: review
    subagents:
      default: sonnet                     # string form, as in ADR 0104
      style-conventions:
        enabled: false                    # not staged; if no staged skill ships it, ignored with a warning
      correctness:
        source: .fullsend/personas/correctness.md   # repository path
      licensing:                          # source defines the persona, so it is never undiscovered
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
- Disabling is enforced on the host because the persona is not staged, with a
  finding drop on the `dimension` field as a backstop, while choosing
  dimensions stays the review agent's judgement.
- A `source` change crosses a trust boundary and gets the same review,
  pinning and locking as a skill or harness base.
- The CLI gains a way to set `source` and to disable a persona, implemented
  after this ADR is approved.
- Disabling or adding a persona takes effect only once the review skill builds
  its roster from the staged persona files and fills in `dimension`, which is
  agents-repository work that must ship first.
