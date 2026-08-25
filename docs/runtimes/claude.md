# Running Claude Code

[Claude Code](https://claude.com/claude-code) is fullsend's default runtime. Every role is supported,
and nothing needs configuring to use it — this page is the operational detail once you are on it.

```bash
fullsend run triage --model opus --effort high
```

Choosing between runtimes is in [Agent runtimes](../runtimes.md). Selection, precedence and the
config keys live there too.

## Models

Pass an alias or a model id; Claude Code resolves aliases natively.

| Alias | Resolves to |
|---|---|
| `opus`, `sonnet`, `haiku`, `fable` | the current Anthropic model of that tier |

All inference goes to Anthropic models on Vertex AI, on the fleet's WIF credentials.

**Fallback chains.** `FULLSEND_FALLBACK_MODELS=a,b` becomes `--fallback-model a,b`, tried in order
when the primary model is overloaded or retired. This is Claude Code only — pi reports it as
unsupported and ignores it.

## At a glance

| | |
|---|---|
| Roles | All, including `review` and `retro` — they need sub-agents |
| Credentials | WIF `external_account` + a refreshed OIDC token; `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL` and `ANTHROPIC_VERTEX_BASE_URL` are unset so a stray key cannot redirect traffic |
| Unattended | `--dangerously-skip-permissions`; hooks wired from the harness, never from agent-writable files |
| Artifacts | `output.jsonl`, transcripts, `metrics.json` with `runtime: claude`, and `claude-debug.log` with `--debug` |
| Effort | `--effort low..max` |

## Behaviour differences worth knowing

These are the places Claude Code differs from pi — useful when comparing a run across runtimes.

- **The agent definition *replaces* the system prompt.** `--agent` makes the agent `.md` body the
  system prompt outright. pi appends it to its own default instead, so an agent that relies on
  Claude Code's exact framing can read differently there.
- **Native sub-agents** via the `Agent` tool, which is why `review` and `retro` are Claude-only
  today.
- **A `CLAUDE.md` bridge is injected** when the repo has `AGENTS.md` but no `CLAUDE.md`, because
  Claude Code auto-loads only the former. pi reads `AGENTS.md` natively and needs no bridge.
- **`tools:` is enforced unreliably** (≥ 2.1.119); pi enforces its `--tools` allowlist strictly. In
  both cases the sandbox, not the tool list, is the boundary
  ([ADR 0027](../ADRs/0027-allowed-and-disallowed-tools-for-agents.md)).
- **Failed tool calls cannot be rewritten.** Claude Code fires `PostToolUse` only on success; a
  failed call goes to `PostToolUseFailure`, which accepts no output rewrite. Secrets or control
  characters in a failed command's output are detected and logged, and the agent is warned, but they
  reach the transcript unmasked. pi sanitizes those too.
- **The repo's own `.claude/settings.json` still auto-loads** from the working directory. fullsend's
  hook wiring is passed explicitly with `--settings` so it loads regardless, but repo-supplied hooks
  are a separate exposure to be aware of.

## Troubleshooting

**The model is not what you asked for.** Check `metrics.json`: `requested_model` is what was handed
to the runtime after overrides and `override_source` says where it came from, so a silent override
is visible after the fact.

**A tool call was blocked.** The security hooks log to `/sandbox/workspace/.security/findings.jsonl`
inside the sandbox. A blocked tool reports its reason in the transcript; an allowlist mismatch names
the offending tool and the expected vocabulary.

**Output looks truncated or condensed.** The PostToolUse chain condenses verification-command output
only on positive evidence of success, and attaches a note saying it did. Anything carrying a failure
marker passes through untouched.

## See also

- [Agent runtimes](../runtimes.md) — choosing and selecting a runtime
- [Running pi](pi.md) — the second runtime, for Grok and Gemini
- [Running agents locally](../guides/user/running-agents-locally.md) — local runs
