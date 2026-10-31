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

Each agent entry's `subagents` map sets a model per persona, where a persona
is one `sub-agents/<name>.md` prompt file inside a skill
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
on a fixed roster. The roster stays fixed for each run: it is the set of
personas staged (copied into the agent's sandbox) from the trusted ref (the
commit config is read from), not something the change's author picks. This ADR
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

```yaml
agents:
  - name: review
    subagents:
      default: sonnet            # model only: the string form, as today
      style-conventions:
        enabled: false           # disable: not staged for this run
      correctness:
        source: .fullsend/personas/correctness.md   # replace a stock persona
      licensing:                 # add a new persona, with its own model
        model: opus
        source: https://raw.githubusercontent.com/example-org/personas/<sha>/licensing.md#sha256=<sha256>
```

1. **Two forms.** An entry is a string, which is shorthand for
   `{model: <value>}`, or an object `{model, source, enabled}` with every
   field optional. The `default` key takes only the string form.
2. **Models resolve as before.** `model` follows ADR 0104, and applies where
   that is implemented: pi, and codex when the agent delegates.
3. **One mechanism underneath.** When it composes the harness, the runner
   compiles `source` and `enabled: false` into harness skill file overrides.
   `fullsend lock` shares this step, which lists the pinned skills'
   `sub-agents/` files and so partly closes ADR 0104's deferred lock gap.
   These overrides win over a harness `skills:` override for the same file.
   Staging applies them before persona discovery, on every runtime.
4. **Where each file goes.**
   - `enabled: false` removes the persona file from every staged skill that
     ships it.
   - `source` replaces the file in the first skill that ships it.
   - A `source` for a new persona adds the file to the agent's first skill
     that has personas. The run fails if there is none.
   - A `source` file whose frontmatter `name` differs from its key is an
     error.
5. **Trusted input only.** `source` and `enabled: false` are read only from
   config at the trusted ref and the preset layers it pins, and so is the file
   a `source` path names. This is stricter than the guarded fields of
   [ADR 0112](0112-overlays-may-set-any-harness-field.md): no overlay, flag,
   environment variable or change under review can set them, so a pull
   request cannot change its own review.
6. **Allowed `source` values.**
   - A path that stays inside `.fullsend/`: no `..`, no absolute path, no
     symlink escape.
   - A URL pinned with `#sha256=`, on `allowed_remote_resources`, with a lock
     entry the run checks.
7. **Disable is a host guarantee.** A disabled persona is not staged, so its
   prompt never reaches the sandbox; a child started under its name runs only
   as an anonymous helper. As a backstop, the review post-script drops
   findings below high whose agent-reported `dimension` (the persona name a
   finding records) names a persona that was not staged, and logs each drop.
8. **Layering.** Layers merge per field, and `key: ~` removes an inherited
   entry (ADR 0104). ADR 0104's ban on `subagents` entries in shared presets
   still holds for every entry except `enabled: false`.
9. **No persona semantics in the runner.** fullsend checks shape only. A
   persona's frontmatter `name` must match its file name. When two skills
   ship the same name, the first wins, with a warning. The skill handles a
   missing persona.
10. **Selection is unchanged.** The review agent still chooses which
    dimensions run, now only from the staged personas.
11. **Unknown keys.** A key that names a persona the run did not discover is
    still an error, unless its `source` defines that persona. An
    `enabled: false` entry never errors, and its other fields are ignored.
    When no skill ships the persona, the run log gets a warning, so a typo
    shows up.

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
