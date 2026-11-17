---
title: "155. Sub-agent entries may source or disable personas"
status: Accepted
relates_to:
  - agent-architecture
  - code-review
topics:
  - config
  - harness
  - sub-agents
  - review
---

# 155. Sub-agent entries may source or disable personas

Date: 2026-11-10

Depends on: [ADR 0162](0162-agent-entries-may-carry-harness-overrides.md)

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

A persona is one `sub-agents/<name>.md` prompt file inside a skill. Each
agent entry's `subagents` map sets a model per persona
([ADR 0104](0104-per-persona-model-resolution.md)). It cannot point a persona
at a different file or turn one off.

The harness `skills:` file overrides
([#6158](https://github.com/fullsend-ai/fullsend/issues/6158)) can add,
replace or remove a review persona
([review sub-agent decomposition](../problems/code-review.md#review-sub-agent-decomposition))
inside a pinned skill. But they work on file paths, not personas. The user
has to know which skill ships the persona and where its file sits, and
disabling a persona means mapping that file path to null. A persona's model
is set on the agent entry, while its prompt file and whether it runs are set
in the harness.

[ADR 0162](0162-agent-entries-may-carry-harness-overrides.md) lets an
`agents:` entry carry a `harness:` block, a partial harness layered on the
agent's harness. The review of ADR 0162 agreed that entry fields which
override harness fields give way to that block. Persona settings can therefore live in the
harness schema, beside `skills:`, and still be set from `config.yaml`.

The review skill already departs from the scripted pipeline of
[ADR 0018](0018-scripted-pipeline-for-multi-agent-orchestration.md) by relying
on a fixed roster. The roster stays fixed for each run: it is the set of
personas staged (copied into the agent's sandbox) from the trusted ref (the
commit config is read from), not something the change's author picks. This ADR
does not supersede ADR 0018.

This ADR gives each persona one harness entry to replace, add or disable it by
name, compiled into harness `skills:` file overrides underneath.

## Options

- **A registry file in the skill** (for example a JSON roster the
  orchestrator reads): rejected. The orchestrator LLM still reads and judges
  its rows, so the roster definition moves into the skill but the host
  guarantees nothing, and it adds a second config surface.
- **Per-dimension env flags on the harness** (in the style of the existing
  `REVIEW_UX_ENABLED`): rejected. One flag per persona does not scale, cannot
  add or replace a persona, and each flag needs matching code in the skill
  and the post-script. ADR 0162 also bars the `harness:` block from adding
  such flags.
- **Harness `skills:` overrides only**: rejected as the user surface. It works
  today, but it is keyed by file path, so it needs knowledge of the skill's
  internal file layout. It stays the low-level mechanism.
- **Object form on the entry's `subagents`**: rejected. It would grow an entry
  field that the ADR 0162 review expects to give way to the `harness:` block.
- **A `subagents` harness field with an object form** (chosen): one user
  surface in the harness schema, one mechanism underneath and one host gate.

## Decision

We make `subagents` a harness field whose persona entries are a string or an
object, so a repository can replace, add or disable a persona by name.

```yaml
agents:
  - name: review
    harness:
      subagents:
        default: sonnet            # model only: the string form
        style-conventions:
          enabled: false           # disable: not staged for this run
        correctness:
          source: personas/correctness.md   # replace a stock persona
        licensing:                 # add a new persona, with its own model
          model: opus
          source: personas/licensing.md
```

1. **Where it is set.** `subagents` is a harness field. A repository sets it
   as `agents[].harness.subagents` (ADR 0162) or in a custom harness file.
   - The entry's own `subagents` stays as a deprecated alias that takes only
     ADR 0104 model strings. It will be retired with the entry-field
     follow-up work that ADR 0162 names.
   - The alias merges as part of its own layer's `harness.subagents`.
     Setting both the alias and `harness.subagents` in one layer
     (`config.base.yaml` or `config.yaml`) fails, as ADR 0162 does for
     `model` and `effort`. An org can then disable a persona in
     `config.base.yaml` while a repo still sets model strings on the alias.
2. **Two forms.** An entry is a string, which is shorthand for
   `{model: <value>}`, or an object `{model, source, enabled}` with every
   field optional. `enabled` defaults to `true`. The `default` key takes only
   the string form.
3. **Models resolve as before.** `model` follows ADR 0104, and applies where
   that is implemented: pi, and codex when the agent delegates.
4. **One mechanism underneath.** After all harness layers merge, the runner
   compiles `source` and `enabled: false` into harness skill file overrides.
   - These overrides take precedence over a `skills:` override for the same
     file, whichever layer set each one. ADR 0162 therefore needs no rule
     between `skills:` and `subagents`.
   - `fullsend lock` shares this compile step, which lists the pinned skills'
     `sub-agents/` files and so partly closes ADR 0104's deferred lock gap.
   - Staging applies the overrides before persona discovery, on every runtime.
5. **Where each file goes.** An entry with key `<key>` targets
   `sub-agents/<key>.md`.
   - `enabled: false` removes the persona file from every staged skill that
     ships it.
   - `source` replaces the file in the first skill that ships it.
   - A `source` for a new persona adds the file to the agent's first skill
     that has personas. The run fails if there is none.
   - A `source` file whose frontmatter is invalid, or whose `name` differs
     from its key, fails the compile step, before persona discovery could
     skip it.
6. **Trusted input only.** `subagents.*.source` and `subagents.*.enabled` come
   only from harness layers at the trusted ref or pinned by it, and so does
   the file a `source` names.
   - No overlay can set them, including the `overlays` of an ADR 0162
     `harness:` block. This is stricter than the guarded fields of
     [ADR 0112](0112-overlays-may-set-any-harness-field.md), so the staged
     roster and its lock entries do not vary by event.
   - No flag, environment variable or change under review can set them, so a
     pull request cannot change its own review.
   - `subagents.*.model` and `subagents.default` are model settings, so, like
     `model`, ADR 0112 does not guard them. ADR 0112's note that persona
     models are not harness fields no longer holds.
7. **Allowed `source` values.**
   - A relative path, resolved as other harness paths are (see the
     [harness field reference](../contributing/harness-fields.md)): from the
     `.fullsend/` root for a local layer or a `harness:` block, and beside
     the harness for a URL-pinned layer. In a local layer it must stay inside
     `.fullsend/`: no `..`, no absolute path, no symlink escape.
   - A URL pinned with `#sha256=`, on `allowed_remote_resources`, with a lock
     entry the run checks. `fullsend lock` does not lock built-in agents
     (ADR 0162), so on a built-in agent a URL `source` fails; use a path.
8. **Disable is a host guarantee.** A disabled persona is not staged, so its
   prompt never reaches the sandbox. A child started under its name is
   rejected while other personas are staged (ADR 0104), and otherwise runs
   only as an anonymous helper without that prompt. As a backstop, the review
   post-script drops findings with severity below high whose agent-reported
   `dimension` (the persona name a finding records) names a persona that was
   not staged, and logs each drop.
9. **Layering.** Harness layers merge `subagents` per persona key, then per
   field.
   - `key: ~` removes an inherited entry, so the persona resolves as if
     unmentioned (ADR 0104). This is an explicit exception to ADR 0162's rule
     that a later layer replaces a value but cannot remove it: `enabled: false`
     turns a persona off, which is not the same as returning it to its stock
     file and model.
   - Setting only `model` or `source` keeps an inherited `enabled: false`.
     A later layer turns the persona back on with `enabled: true` or
     `key: ~`; `enabled: true` adds no file override.
   - ADR 0104's ban on `subagents` entries in shared presets applies to the
     `harness:` block in `config.base.yaml`, which ADR 0162 makes a harness
     layer. Its `subagents` map may hold only `enabled: false` entries.
10. **No persona semantics in the runner.** fullsend checks shape only. A
    persona's frontmatter `name` must match its file name. When two skills
    ship a persona with the same name, the first wins, with a warning. The
    skill handles a missing persona.
11. **Selection is unchanged.** The review agent still chooses which
    dimensions run, now only from the staged personas.
12. **Unknown keys.** A key that names a persona the run did not discover is
    still an error, unless its `source` defines that persona. An
    `enabled: false` entry never errors, and its other fields are ignored.
    When an `enabled: false` entry names a persona that no skill ships, the
    run log gets a warning, so a typo shows up.

## Forge scope

Forge-neutral: config, staging and the post-script do not depend on the
forge. This applies on GitHub and GitLab, each with its own trusted ref, and on
Forgejo once that forge lands.

## Consequences

- A repository can add, replace or disable a review persona from config
  without copying the skill, and keeps upstream updates for everything else.
- A persona's model, file and enabled state live in one harness entry. The
  agent entry's own `subagents` keeps working for model strings until it is
  retired.
- Disabling is guaranteed on the host because the persona is not staged,
  while choosing dimensions stays the review agent's judgement.
- A `source` change crosses a trust boundary and gets the same review,
  pinning and locking as a skill or harness base.
- The CLI gains a way to set `source` and to disable a persona, implemented
  after this ADR is approved.
- Implementation depends on ADR 0162's `harness:` block shipping first.
- Disabling or adding a persona takes effect only once the review skill builds
  its roster from the staged persona files and fills in `dimension`, which is
  agents-repository work that must ship first.
