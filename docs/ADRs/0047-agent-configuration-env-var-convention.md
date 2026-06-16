---
title: "47. Agent configuration environment variable convention"
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

# 47. Agent configuration environment variable convention

Date: 2026-06-16

## Status

Accepted

## Context

Agents need behavioral knobs — settings that tune *how* they work without
changing the agent definition itself. Issue
[#2333](https://github.com/fullsend-ai/fullsend/issues/2333) surfaced the
first concrete case: the review agent should let repo owners set a minimum
severity threshold for reported findings. More knobs will follow for other
agents.

The harness already delivers environment variables into the sandbox via `.env`
files with `expand: true`
([ADR 0024](0024-harness-definitions.md)), and pre/post scripts read env vars
from `runner_env` ([ADR 0045](0045-forge-portable-harness-schema.md)). The
infrastructure for carrying configuration exists. What is missing is a
**naming convention** that prevents collisions, ensures discoverability, and
establishes a consistent pattern for every agent going forward.

This ADR covers only **agent configuration** env vars — behavioral knobs that
tune agent behavior. It does not retroactively rename existing context vars
(event data like `GITHUB_PR_URL`, `ISSUE_NUMBER`) or infrastructure vars
(tokens, paths, credentials). Those remain as they are.

## Decision

Agent configuration environment variables follow a single convention:

### Naming

```
{ROLE}_{SETTING_NAME}
```

- `{ROLE}` is the agent's role in uppercase: `REVIEW`, `CODE`, `TRIAGE`,
  `FIX`, `PRIORITIZE`, `RETRO`, etc.
- `{SETTING_NAME}` is `SCREAMING_SNAKE_CASE` describing the setting.
- Examples: `REVIEW_SEVERITY_THRESHOLD`, `CODE_MAX_FILE_SIZE`,
  `REVIEW_POST_INLINE`, `TRIAGE_SKIP_DUPLICATE_CHECK`.

The role prefix prevents collisions when multiple agents share an execution
environment or when env files are sourced together. It also makes `grep` and
audit trivial: `grep ^REVIEW_ env/review.env` shows every knob for that agent.

### Where config vars live in the harness

Config vars are carried the same way as other agent env vars — no new schema
fields are needed:

1. **For sandbox access (inference time):** Add the variable to the agent's
   `.env` file (e.g., `env/review.env`) with `${VAR}` expansion. The harness
   `host_files` entry with `expand: true` resolves the value from the host
   environment before copying into the sandbox. The agent reads it at runtime.

2. **For pre/post scripts (host side):** Add the variable to the harness's
   `runner_env` or the forge-specific `runner_env` block. Scripts read it from
   the environment.

3. **For CI workflow injection:** The CI workflow sets the value from org
   secrets, repo variables, or hardcoded defaults. This is the same mechanism
   used for all other env vars — no change needed.

### Defaults

Default values are **documented** in `docs/agents/<role>.md` and **applied by
the agent itself** at inference time (e.g., "if `$REVIEW_SEVERITY_THRESHOLD`
is unset, default to `low`"). The harness YAML and `.env` files carry no
defaults for agent-specific config — they pass through whatever the CI
workflow provides, or leave the variable unset.

Pre/post scripts that need a default should use standard shell defaulting:
`${REVIEW_SEVERITY_THRESHOLD:-low}`.

### Documentation

Each agent's user-facing documentation (`docs/agents/<role>.md`) includes a
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
system prompt (`agents/<role>.md`) references config vars wherever they are
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

The agent reads the value from its environment (e.g., via bash `echo
$REVIEW_SEVERITY_THRESHOLD` or by referencing it in tool calls) and
conditions its behavior accordingly. This is no different from how agents
already read `$GITHUB_PR_URL` or `$ISSUE_NUMBER`.

### Using config vars in pre/post scripts

Scripts read config vars from the environment like any other variable:

```bash
# In post-review.sh
threshold="${REVIEW_SEVERITY_THRESHOLD:-low}"
# Filter findings array by severity before posting
```

### Precedence

Config var values follow the existing harness layering from
[ADR 0006](0006-ordered-layer-model.md) and
[ADR 0003](0003-org-config-repo-convention.md): fullsend defaults (scaffold)
can be overridden by the org `.fullsend` repo, which can be overridden by
per-repo `.fullsend/`. This layering already applies to `.env` files and
`runner_env` — config vars inherit it for free.

## Consequences

- **No runner changes required.** The convention uses existing env var
  delivery mechanisms (`host_files` with `expand: true`, `runner_env`,
  CI workflow `env:`). Agents start accepting config vars immediately by
  documenting them and referencing them in their prompts and scripts.
- **Discoverability is centralized.** Users check `docs/agents/<role>.md`
  to see what knobs an agent supports. Agent authors document new config
  vars there when adding them.
- **Collision-free by convention.** The `{ROLE}_` prefix scopes config vars
  to the agent that owns them. A setting that applies to multiple agents
  gets separate vars per agent (e.g., `CODE_MAX_FILE_SIZE` and
  `REVIEW_MAX_FILE_SIZE`), keeping each agent's configuration independent.
- **Agent system prompts stay flexible.** There is no required section
  structure for how `agents/<role>.md` references config vars. Agent
  authors place references where they make sense in the prompt flow.
- **Each new config var requires updates in up to three places:** the
  agent's `.env` file (for sandbox delivery), the agent's system prompt
  (for behavioral conditioning), and `docs/agents/<role>.md` (for user
  documentation). This is intentional — it keeps the documentation,
  delivery, and behavior in sync without adding schema surface to the
  harness.
