---
title: "49. Agent configuration environment variable convention"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - configuration
  - harness
  - agents
  - conventions
---

# 49. Agent configuration environment variable convention

Date: 2026-06-16

## Status

Accepted

## Context

Agents need behavioral knobs — settings that tune *how* they work without
changing the agent definition itself. Issue
[#2333](https://github.com/fullsend-ai/fullsend/issues/2333) surfaced
a concrete case: the review agent should let repo owners set a minimum
severity threshold for reported findings. More knobs will follow for other
agents.

The harness already delivers environment variables into the sandbox via `.env`
files with `expand: true`
([ADR 0024](0024-harness-definitions.md)), and pre/post scripts read env vars
from `runner_env` ([ADR 0045](0045-forge-portable-harness-schema.md)). The
infrastructure for carrying configuration exists. What is missing is a
**naming convention** that establishes a consistent pattern for every agent
going forward.

This ADR covers only **agent configuration** env vars — behavioral knobs that
tune agent behavior. It does not retroactively rename existing context vars
(event data like `GITHUB_PR_URL`, `ISSUE_NUMBER`) or infrastructure vars
(tokens, paths, credentials). Those remain as they are.

## Decision

Agent configuration environment variables follow a single convention:

### Naming

```
{AGENT}_{SETTING_NAME}
```

- `{AGENT}` is the agent's **name** in uppercase, derived from the harness
  filename: `REVIEW`, `CODE`, `TRIAGE`, `FIX`, `PRIORITIZE`, `RETRO`, etc.
- `{SETTING_NAME}` is `SCREAMING_SNAKE_CASE` describing the setting.
- Examples: `REVIEW_SEVERITY_THRESHOLD`, `CODE_MAX_FILE_SIZE`,
  `REVIEW_POST_INLINE`, `TRIAGE_SKIP_DUPLICATE_CHECK`.
- A setting that applies to multiple agents gets separate vars per agent
  (e.g., `CODE_MAX_FILE_SIZE` and `REVIEW_MAX_FILE_SIZE`), keeping each
  agent's configuration independent.

The agent name prefix prevents collisions when multiple agents share an
execution environment or when env files are sourced together. Existing context
vars (e.g., `PRIOR_REVIEW_SHA`) and credential vars (e.g., `GH_TOKEN`)
already use agent-name prefixes — the `{AGENT}_` prefix alone does not
distinguish config vars from those. The distinction is by purpose and
documentation: config vars are behavioral knobs listed in
`docs/agents/<agent>.md`.

### Where config vars live in the harness

Config vars are carried the same way as other agent env vars — no new schema
fields are needed. The `.env` file and `runner_env` serve different
audiences: the `.env` file delivers vars into the sandbox for the agent at
inference time, while `runner_env` makes vars available to pre/post scripts
on the host. A config var needed by both must appear in both places.

1. **For sandbox access (inference time):** Add the variable to the agent's
   `.env` file (e.g., `env/review.env`) with `${VAR}` expansion. The harness
   `host_files` entry with `expand: true` resolves the value from the host
   environment before copying into the sandbox. The agent reads it at runtime.

2. **For pre/post scripts (host side):** Add the variable to the harness's
   `runner_env` or the forge-specific `runner_env` block. Scripts read it
   from the environment. This is independent of the `.env` file — `runner_env`
   controls the host-side environment, not the sandbox.

3. **For CI workflow injection:** The CI workflow sets the value from org
   secrets, repo variables, or hardcoded defaults. This is the same mechanism
   used for all other env vars — no change needed.

### Defaults

Default values live in the **canonical harness** (the scaffold's
`harness/<agent>.yaml`). Downstream layers — the org `.fullsend` repo or a
per-repo `.fullsend/` — override them via `base` composition
([ADR 0045](0045-forge-portable-harness-schema.md)). Defaults are also
**documented** in `docs/agents/<agent>.md` so users can discover them without
reading harness YAML.

**For agent prompts,** the agent treats an unset or empty variable the same as
"use the default." The `.env` file's `expand: true` mechanism resolves unset
host vars to an empty string, not an absent var — so agents and scripts must
handle both cases.

**For pre/post scripts,** use standard shell defaulting, which already handles
both empty and unset: `${REVIEW_SEVERITY_THRESHOLD:-low}`.

### Documentation

Each agent's user-facing documentation (`docs/agents/<agent>.md`) includes a
**Variables** subsection under the existing "Configuration and extension"
section:

```markdown
## Configuration and extension

See [Customizing with AGENTS.md](../guides/user/customizing-with-agents-md.md) and
[Customizing with Skills](../guides/user/customizing-with-skills.md).

### Variables

| Variable | Description | Default | Valid values |
|----------|-------------|---------|--------------|
| `REVIEW_SEVERITY_THRESHOLD` | Minimum severity for reported findings | `low` | `info`, `low`, `medium`, `high`, `critical` |
| `REVIEW_POST_INLINE` | Post inline comments on individual findings | `true` | `true`, `false` |
```

This is the single place a user looks to discover what knobs an agent
supports. Every agent doc includes this subsection for consistency — agents
that accept no configuration vars state "None" in the section. The agent's
system prompt (`agents/<agent>.md`) references config vars wherever they are
naturally needed in the instructions — no prescribed section structure.

### Using config vars at inference time

The agent's system prompt references config vars in context where the
behavior is conditioned. For example, in the review agent:

```markdown
## Severity filtering

If `$REVIEW_SEVERITY_THRESHOLD` is set, suppress findings below that level.
The severity order is: info < low < medium < high < critical. Suppressed
findings do not appear in the output — they are dropped entirely, not
downgraded.
```

The agent reads the value from its sandbox environment (e.g., via
`printenv REVIEW_SEVERITY_THRESHOLD` or by referencing it in tool calls)
and conditions its behavior accordingly. This is no different from how
agents already read `$GITHUB_PR_URL` or `$ISSUE_NUMBER`.

### Precedence

Config var values follow the existing harness layering from
[ADR 0045](0045-forge-portable-harness-schema.md) and
[ADR 0003](0003-org-config-repo-convention.md): fullsend defaults (scaffold)
can be overridden by the org `.fullsend` repo, which can be overridden by
per-repo `.fullsend/`. This layering already applies to `.env` files and
`runner_env` — config vars inherit it for free.

## Consequences

- **No runner changes required.** The convention uses existing env var
  delivery mechanisms (`host_files` with `expand: true`, `runner_env`,
  CI workflow `env:`). Agents start accepting config vars immediately by
  documenting them and referencing them in their prompts and scripts.
- **Discoverability is centralized.** Users check `docs/agents/<agent>.md`
  to see what knobs an agent supports. Agent authors document new config
  vars there when adding them.
- **Collision-free by convention.** The `{AGENT}_` prefix scopes config vars
  to the agent that owns them.
- **Agent system prompts stay flexible.** There is no required section
  structure for how `agents/<agent>.md` references config vars. Agent
  authors place references where they make sense in the prompt flow.
- **Each new config var may require updates in several places:**
  1. Agent `.env` file (sandbox delivery)
  2. Harness `runner_env` (host-side script access)
  3. Agent system prompt (behavioral conditioning)
  4. Pre/post scripts (host-side logic)
  5. `docs/agents/<agent>.md` (user documentation)

  Not every var needs all five — a var used only at inference time skips 2
  and 4; a var used only in scripts skips 1 and 3.
