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

## Related docs

- [architecture.md](architecture.md) — Agent Runtime layer
- [problems/security-threat-model.md](problems/security-threat-model.md) — threat model and scanner paths
- [problems/agent-architecture.md](problems/agent-architecture.md) — pluggable runtimes (#1260, #579, #70)
