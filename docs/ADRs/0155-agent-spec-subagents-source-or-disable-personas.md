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

The review agent's orchestrator skill (`pr-review`) dispatches one sub-agent
persona per review dimension
([code-review.md](../problems/code-review.md#review-sub-agent-decomposition)),
and its roster is prose inside the skill. At
`12a09ace3e26b1e52442ee628ac570f794f856f6`, `agents[].subagents`
(`AgentEntry.Subagents` in `internal/config/config.go`, a
`map[string]*string`) maps a persona name to a model string
([ADR 0104](0104-per-persona-model-resolution.md)). It cannot point a persona
at another file or turn one off. Harness `skills:` entries in map form
(`SkillEntry.Overrides` in `internal/harness/skill_entry.go`) can replace or
remove a file inside a pinned skill, and they are parsed, resolved and
locked. Staging does not apply them yet
([#6158](https://github.com/fullsend-ai/fullsend/issues/6158)). So today the
only way to add, replace or remove a persona is to copy the whole skill. A
downstream fork that did this still has the LLM choosing among its personas,
and every upstream change to the skill has to be ported to the fork by hand.

The review skill already departs from the scripted pipeline of
[ADR 0018](0018-scripted-pipeline-for-multi-agent-orchestration.md) and
relies on a fixed roster instead. With this decision the roster is still
fixed for each run: it is the staged set, read from the base branch, not
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
   field is optional, and `model` means the same as the string form.
2. **Compiles into skill file overrides.** At staging, the runner turns
   `source` and `enabled: false` into the #6158 skill file overrides. They
   replace, add or remove `sub-agents/<persona>.md` in the skill that ships
   the persona, or add the file to the agent's orchestrator skill. There is
   one mechanism underneath: `subagents` is the recommended surface, and
   harness `skills:` overrides stay available.
3. **`source` is a guarded field** in the sense of
   [ADR 0112](0112-overlays-may-set-any-harness-field.md). It is one of:
   - a repository path that resolves inside the repository (no `..`, no
     absolute path, no symlink escape);
   - a URL pinned with `#sha256=`, whose prefix is on
     `allowed_remote_resources`, with a lock entry recorded the same way as
     harness bases and skills.

   The config, the path and the URL allowlist are all read from the base
   branch (the trusted ref), so a pull request cannot change its own review.
4. **`enabled: false` is a host guarantee.** The persona file is not staged,
   so the agent has no prompt for it. The runner records the staged personas,
   and the review post-script drops any finding whose dimension names a
   persona that was not staged. This does not stop the parent agent from
   doing the work itself or spawning an anonymous helper, and it does not need
   to: such output carries no staged dimension.
5. **Layering.** Org and repo layers merge per field, so a repo can set only
   `model` on a `source` the org supplies. `key: ~` removes the whole
   inherited entry, as ADR 0104 defines.
6. **No persona semantics in the runner.** fullsend checks only the shape:
   types, the `source` trust rules and duplicate persona names. It does not
   know which personas a skill needs. If, for example, the skill's
   `correctness` persona is missing, the skill decides what to do.
7. **Selection is unchanged.** The review agent still chooses which
   dimensions to run on a given change, but only from the staged personas.
   This ADR does not make selection deterministic.
8. **One existing error is relaxed.** Today a `subagents` key naming a
   persona the run did not discover is fatal (`resolvePersonaModels` in
   `internal/runtime/pi_personas.go`, `resolveCodexChildModels` in
   `internal/runtime/codex_personas.go`). That stays fatal for an entry that
   sets `model` or `source`. An `enabled: false` entry on a persona the skill
   does not ship is a no-op, so an org can disable a persona that the skills
   in some of its repositories lack.

```yaml
agents:
  - name: review
    subagents:
      default: sonnet                     # string form, as in ADR 0104
      style:
        enabled: false                    # not staged; its findings are dropped
      correctness:
        source: .fullsend/personas/correctness.md   # repository path
      licensing:                          # adds a persona the skill does not ship
        model: opus
        source: https://github.com/example-org/personas/raw/<sha>/licensing.md#sha256=<sha256>
```

## Forge scope

Forge-neutral. The decision covers config, staging and a host post-script,
which work the same way on GitHub, GitLab and Forgejo.

## Consequences

- A repository can add, replace or disable a review persona from config
  without copying the skill, and keeps upstream updates for everything else.
- Disabling is enforced on the host (the persona is not staged and its
  findings are dropped), while choosing dimensions stays the review agent's
  judgement.
- A `source` change crosses a trust boundary and gets the same review,
  pinning and locking as a skill or harness base.
- The CLI gains `fullsend agent set --subagent-source` and
  `--subagent-disable`, implemented after this ADR is approved.
- Before an added persona can be selected, the skill must build its roster
  from the staged personas rather than from prose, which is work in the
  agents repository.
