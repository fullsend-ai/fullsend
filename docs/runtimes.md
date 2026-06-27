# Agent runtimes

Fullsend's `fullsend run` command delegates in-sandbox agent execution to a pluggable **runtime**. Today only **Claude Code** is registered; the `internal/runtime` package defines the contracts new runtimes must implement.

When adding a runtime, fill in the security matrix below and wire the implementation through `runtime.Default()`.

## Security feature matrix

| Feature | Where it runs | Claude Code | Notes for future runtimes |
|---------|---------------|-------------|---------------------------|
| **Host-side context injection scan** (DeBERTa / LLM Guard, unicode, SSRF patterns on repo context files) | Host + sandbox `scan context` | ✓ | Requires sandbox image with ML models; harness `security.host_scanners` |
| **Host-side runtime content scan** (agent def, SKILL.md, plugin JSON before upload) | Host (`scanRuntimeContent`) | ✓ | Uses `security.InputPipeline()`; not part of `Runtime` interface — runner responsibility |
| **Tirith** (Bash command scanning) | Sandbox PreToolUse hook | ✓ | `tirith_check.py`; harness `security.sandbox_hooks.tirith` |
| **SSRF pre-tool** | Sandbox PreToolUse hook | ✓ | `ssrf_pretool.py`; default on |
| **Canary token detection** | Sandbox Pre/PostToolUse hooks | ✓ | `canary_pretool.py` / `canary_posttool.py` |
| **Secret redaction** | Sandbox PostToolUse hook | ✓ | `secret_redact_posttool.py` |
| **Unicode normalization** | Sandbox PostToolUse hook | ✓ | `unicode_posttool.py` |
| **Context suppression** | Sandbox PostToolUse hook | ✓ | `context_suppress_posttool.py` |
| **Tool allowlist** | Sandbox PreToolUse hook | opt-in | `tool_allowlist_pretool.py`; requires `FULLSEND_TOOL_ALLOWLIST` |
| **Prompt injection (DeBERTa)** | Host Path A + sandbox Path B | ✓ | Same scanner stack as context files when enabled in harness |
| **Optional Claude sandbox hooks** | `ClaudeHooksBootstrap` type assert | ✓ only | Other runtimes must define their own hook/bootstrap extension; absence means **no** sandbox tool hooks installed |
| **Transcript / debug artifacts** | `TranscriptHandler` | ✓ (stream-json, `claude-debug.log`) | Format-specific; not shared across runtimes |

### Fail modes

Harness `security.fail_mode` controls whether critical findings **block** the run (`closed`, default) or **warn** and continue (`open`). This applies to host scans, sandbox `scan context`, and host-side runtime content scan alike.

### Runtime interface contract

| Interface | Responsibility |
|-----------|----------------|
| `runtime.Runtime` | Name, config dir, env exports, bootstrap, run loop, per-iteration artifact cleanup |
| `runtime.BootstrapInput` | Portable paths for agent/skills/plugins to upload |
| `runtime.ClaudeHooksBootstrap` | Optional — Claude-only sandbox security hooks |
| `runtime.TranscriptHandler` | Extract transcripts/debug logs; parse errors for CI annotations |

A runtime that implements `Runtime` but not `ClaudeHooksBootstrap` (or an equivalent future extension) will **not** install Tirith, SSRF, canary, or other Claude hook scripts. Document what your runtime provides instead.

## Sandbox workspace layout

The sandbox has two key directories that map to Claude Code's config levels:

```
/sandbox/
├── claude-config/                   ← CLAUDE_CONFIG_DIR (personal level)
│   ├── agents/
│   │   └── review.md                   Agent definition (--agent loads from here)
│   ├── skills/
│   │   ├── code-review/SKILL.md        Built-in skills (personal level — wins on collision)
│   │   ├── pr-review/SKILL.md
│   │   └── ...
│   └── plugins/
│       └── ...                         Plugin state (simplified; see bootstrapPlugins())
│
└── workspace/                       ← SandboxWorkspace
    ├── .env                            Environment variables (sourced before claude)
    ├── .env.d/                         Additional env files (host_files expand)
    ├── .claude/
    │   ├── hooks/                      Security hooks (PreToolUse, PostToolUse)
    │   └── settings.json               Hook wiring (separate from plugin config)
    │
    └── <repo-name>/                 ← Claude Code's working directory (cd target)
        ├── CLAUDE.md                   Project instructions (repo's own or injected bridge)
        ├── AGENTS.md                   Project rules (repo's own or org default injected)
        ├── .claude/skills/             Repo skills (project level — shadowed on collision)
        │   └── custom-lint/SKILL.md
        └── src/...                     Target repo source code
```

## Agent rule layering

When `fullsend run` executes an agent, Claude Code loads instructions from
multiple sources. These compose — they occupy different layers, not competing
slots:

```
┌────────────────────────────────────────────────────────┐
│  Layer 1: Agent Definition (system prompt)             │
│  Source: /sandbox/claude-config/agents/<name>.md        │
│  Loaded via: --agent flag                              │
│  Controls: role, task, tools, disallowedTools, model,  │
│            built-in skills list                         │
│  Authority: highest — repo cannot modify               │
├────────────────────────────────────────────────────────┤
│  Layer 2: Project Instructions (advisory)              │
│  Source: /sandbox/workspace/<repo>/CLAUDE.md            │
│         /sandbox/workspace/<repo>/AGENTS.md             │
│  Loaded via: Claude Code auto-loads from working dir   │
│  Controls: conventions, architecture, domain context   │
│  Authority: advisory — cannot override layer 1         │
├────────────────────────────────────────────────────────┤
│  Layer 3: Skills                                       │
│  Personal: /sandbox/claude-config/skills/ (fullsend)   │
│  Project:  <repo>/.claude/skills/ (repo)               │
│  Precedence: personal > project (name collision →      │
│              fullsend wins, repo version shadowed)      │
│  Repo skills extend the agent; customized/skills/      │
│  overrides at the config layer before upload            │
└────────────────────────────────────────────────────────┘
```

### AGENTS.md injection logic

`run.go` step 8a (`hasAgentsMD()` / `injectClaudeMDPointer()`):

1. If target repo has no AGENTS.md → inject org-level default from config repo,
   add to `.git/info/exclude`
2. If runtime is Claude Code, target repo has AGENTS.md but no CLAUDE.md →
   inject bridge CLAUDE.md pointing to AGENTS.md, add to `.git/info/exclude`
3. If target repo has both → use as-is

### Context file security scanning

`run.go` steps 8c and 9b:

Repo context files (CLAUDE.md, AGENTS.md, SKILL.md) are scanned in two
defense-in-depth passes before the agent starts:

1. **Host-side (Path A, step 8c):** `scanRepoContextFiles()` runs the
   `InputPipeline` (unicode normalizer, context injection scanner) on the
   host before files enter the sandbox.
2. **Sandbox-side (Path B, step 9b):** `buildScanContextCommand()` runs
   `fullsend scan context` inside the sandbox after all files are assembled.

Critical findings block the run in `fail_mode: closed`.

## Related docs

- [cli-internals.md](guides/dev/cli-internals.md) — sandbox constants, key sandbox operations
- [architecture.md](architecture.md) — Agent Runtime layer
- [problems/security-threat-model.md](problems/security-threat-model.md) — threat model and scanner paths
- [problems/agent-architecture.md](problems/agent-architecture.md) — pluggable runtimes (#1260, #579, #70)
