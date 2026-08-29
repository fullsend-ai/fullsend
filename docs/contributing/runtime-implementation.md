# Implementing an agent runtime

Everything needed to add or change a `runtime.Runtime` backend: the security
controls a runtime must wire, the interfaces it implements, the sandbox hook
contract its adapter must satisfy, and the on-disk layout it writes.

**Using** a runtime — picking one, choosing models, troubleshooting a run — is
[runtimes.md](../runtimes.md). This page is the implementer's half.

On this page:

- [Adding a runtime: checklist](#adding-a-runtime-checklist)
- [Security feature matrix](#security-feature-matrix) — what each runtime wires, and where
- [Runtime interface contract](#runtime-interface-contract)
- [Sandbox hook contract](#sandbox-hook-contract) — files, wiring, wire protocol, sanitizer scope, fail modes, Claude Code caveats
- [Pinned runtime binaries in the sandbox image](#pinned-runtime-binaries-in-the-sandbox-image)
- [Sandbox workspace layout](#sandbox-workspace-layout) and [agent rule layering](#agent-rule-layering)
- [Dummy runtime operations](#dummy-runtime-operations)
- [pi runtime internals (#6464)](#pi-runtime-internals-6464) — verification provenance for the pi backend

## Adding a runtime: checklist

1. Register the backend in `runtime.Resolve()`.
2. Implement `runtime.Runtime` and honour `runtime.SandboxHooksBootstrap`
   (see [Runtime interface contract](#runtime-interface-contract)). A runtime
   that ignores it installs **no** sandbox tool hooks.
3. Fill in every column of the [security feature matrix](#security-feature-matrix)
   for the new runtime — including the cells that are "not wired"; say so
   explicitly rather than leaving them blank.
4. Add its row to the config-key table in
   [runtimes.md](../runtimes.md#harness-config-keys-per-runtime).
5. If the runtime's binary ships in the sandbox image, pin it the way the
   existing ones are pinned and prove the pin at build time
   ([Pinned runtime binaries](#pinned-runtime-binaries-in-the-sandbox-image)).

## Security feature matrix

The sandbox is the containment boundary; everything a runtime does with hooks and tool restrictions is steering inside it ([ADR 0027](../ADRs/0027-allowed-and-disallowed-tools-for-agents.md)). Read the matrix with that picture in mind:

```mermaid
flowchart TB
  subgraph HOST["Runner host — trusted, runs fullsend"]
    direction LR
    SCAN["host scans\ncontext · agent def\nskills · plugins"]
    CRED["long-lived credentials stay here\nonly a short-lived OIDC token\n+ WIF config enter"]
    SIG["hooks on/off decided\nfrom the harness, never\nfrom agent-writable files"]
  end
  subgraph SB["Sandbox boundary — OpenShell + L7 egress policy (containment)"]
    direction TB
    EG["egress allowlist: *.googleapis.com · api.anthropic.com\n(+ api.openai.com POST /v1/responses with the openai provider)\nbinaries: **/claude · **/node (pi runs via node)"]
    subgraph PROC["Runtime process — steering, defense in depth"]
      direction LR
      PRE["PreToolUse\nTirith · SSRF\ncanary · allowlist"]
      TOOL["tool call"]
      POST["PostToolUse\nredact · unicode\nsuppress"]
      PRE --> TOOL --> POST
    end
    subgraph FS["Files"]
      direction LR
      WR["agent-writable between iterations\n(Claude parity): repo · .env · output/\nhook wiring incl. pi's adapter\n(integrity-checked before each run)"]
      RO["read-only, pinned:\nruntime binary · provider extension"]
    end
    EG --> PROC --> FS
  end
  HOST --> SB
  style SB fill:#fbf0d6,stroke:#d98e04,stroke-dasharray:6 4,color:#1b2230
  style PROC fill:#e3e9fb,stroke:#2d5be3,color:#1b2230
  style FS fill:#fff8ea,stroke:#d98e04,color:#1b2230
  classDef boundary fill:#fff,stroke:#d98e04,color:#1b2230;
  classDef steer fill:#fff,stroke:#2d5be3,color:#1b2230;
  classDef host fill:#eceee8,stroke:#a9afa4,color:#1b2230;
  class EG,WR,RO boundary;
  class PRE,TOOL,POST steer;
  class SCAN,CRED,SIG host;
```

### Host-side controls (runner responsibility, runtime-agnostic)

| Feature | Where it runs | Claude Code | OpenCode (stub) | Pi | Notes for future runtimes |
|---------|---------------|-------------|-----------------|----|---------------------------|
| **Host-side context injection scan** (unicode, SSRF patterns on repo context files) | Host + sandbox `scan context` | ✓ | N/A — stub | ✓ | Harness `security.host_scanners`; heuristic scanners only — the DeBERTa ML model was removed from the sandbox in #6522 (its only consumer is the host-side `scan input`, not `scan context`) |
| **Host-side runtime content scan** (agent def, SKILL.md, plugin JSON before upload) | Host (`scanRuntimeContent`) | ✓ | N/A — stub | ✓ | Uses `security.InputPipeline()`; not part of the `Runtime` interface |
| **Prompt injection (DeBERTa)** | Host `fullsend scan input` only | ✓ in the runner image (built `CGO_ENABLED=1 -tags ORT` with `libtokenizers.a` + ONNX Runtime >= 1.28); ✗ in the release tarballs, which stay `CGO_ENABLED=0` and untagged (#6522) | N/A — stub | Same as Claude Code — host-side, not a runtime distinction | Shipped enabled only in `ghcr.io/fullsend-ai/fullsend-runner`; the release tarball the composite action downloads has it compiled out, so CI runs never reach it. **Not an active control on the `fullsend run` path either way**: `RunMLScan` is called only from `fullsend scan input`, which nothing in this repo or `fullsend-ai/agents` invokes. See #6506 (decision), #6522 (build constraints) |

### Sandbox tool hooks (per runtime)

How the shared PostToolUse chain reaches each runtime — the three sanitizer rows below share it:

- **Claude Code:** `posttool_chain.py` runs on successful tool calls (#6357). On failed calls the same driver runs on `PostToolUseFailure`, where it detects, logs to `findings.jsonl` and warns the agent via `additionalContext` — Claude Code does not let a hook rewrite a failed call's output.
- **Pi:** `fullsend-hooks.js` `tool_result` → the same `posttool_chain.py` (sent `tool_response` + `tool_result`; `updatedToolOutput` applied to the result the model sees). pi's `tool_result` fires for failed calls too, so those are sanitized as well.

| Feature | Where it runs | Claude Code | OpenCode (stub) | Pi | Notes for future runtimes |
|---------|---------------|-------------|-----------------|----|---------------------------|
| **Tirith** (Bash command scanning) | Sandbox PreToolUse hook | ✓ (loaded via `--settings`, #6358) | N/A — stub | ✓ via `fullsend-hooks.js` (pi `tool_call` → `HookPlan` PreToolUse scripts) | `tirith_check.py`; harness `security.sandbox_hooks.tirith`; fails open on missing binary/timeout unless `TIRITH_REQUIRED=1` |
| **SSRF pre-tool** | Sandbox PreToolUse hook | ✓ (`hooks-loaded.feature` runs under the dummy runtime, which installs no hooks — it guards the sandbox egress boundary; the hook itself is unit-tested) | N/A — stub | ✓ via `fullsend-hooks.js` | `ssrf_pretool.py`; default on; when DNS resolution fails for a host on the `FULLSEND_EGRESS_ALLOWLIST`, the hook defers to the L7 egress proxy instead of failing closed — all other SSRF checks (scheme, hostname blocklist, IP blocklist, DNS rebinding) still apply. On GitLab CI, the forge host is also covered by the auto-generated `fullsend-gitlab-forge` provider profile (#6615), which opens the L7 proxy for the forge API |
| **Canary token detection** | Sandbox Pre/PostToolUse hooks | pre ✓; post-tool via `posttool_chain.py` on successful calls (`tool_response` / `updatedToolOutput`, #6357); failed calls: the same driver on `PostToolUseFailure` (detect + halt; the error text cannot be rewritten) | N/A — stub | ✓ pre via `tool_call`; post via `tool_result` (sequential chain, block withholds the result) | `canary_pretool.py` / `canary_posttool.py`; both inert unless `FULLSEND_CANARY_TOKEN` is set. Post-tool canary is an in-process chain stage so it cannot race sanitizer rewrites. Claude Code `decision:block` does not hide PostToolUse output, so the chain also redacts the token in `updatedToolOutput` |
| **Secret redaction** | Sandbox PostToolUse hook | ✓ shared chain (above) | N/A — stub | ✓ shared chain (above) | `secret_redact_posttool.py` |
| **Unicode normalization** | Sandbox PostToolUse hook | ✓ shared chain (above) | N/A — stub | ✓ shared chain (above) | `unicode_posttool.py` |
| **Context suppression** | Sandbox PostToolUse hook | ✓ shared chain (above) | N/A — stub | ✓ shared chain (above) | `context_suppress_posttool.py` |
| **Tool allowlist** | Sandbox PreToolUse hook | opt-in; ✓ when enabled | N/A — stub | ✓ `tool_allowlist_pretool.py` via `tool_call` (names translated to Claude vocabulary first, #608) plus pi's native `--tools` from the agent `tools:` and the `Bash(a,b)` first-token allowlist enforced in the extension | `tool_allowlist_pretool.py`; requires `FULLSEND_TOOL_ALLOWLIST` (fail-closed when unset) |

### Bootstrap and artifacts (per runtime)

| Feature | Where it runs | Claude Code | OpenCode (stub) | Pi | Notes for future runtimes |
|---------|---------------|-------------|-----------------|----|---------------------------|
| **Sandbox tool hooks wiring** | `SandboxHooksBootstrap` type assert in `Bootstrap` | ✓ scripts at `claude-config/hooks/`, wiring at `claude-config/hooks.json` via `--settings` (#6358) | ✗ — `Bootstrap` is a stub; must wire `security.HookPlan` via OpenCode plugin hooks | ✓ see [pi: hook adapter contract](#hook-adapter-contract) — scripts under `/sandbox/pi-config/hooks/`, `HookPlan` in `fullsend-manifest.json`, embedded `fullsend-hooks.js` loaded with `-e` under `--no-extensions`; fail-closed on a missing/altered adapter (exit 97) or a manifest without a hook plan (exit -1) | Hook scripts and wiring plan are runtime-neutral (see [Sandbox hook contract](#sandbox-hook-contract)); a runtime that ignores `SandboxHooksBootstrap` installs **no** sandbox tool hooks — say so explicitly here |
| **Transcript / debug artifacts** | `TranscriptHandler` (+ optional `DebugLogNamer`) | ✓ (stream-json, `claude-debug.log`) | No-op — see #1935 | ✓ session JSONL under `PI_CODING_AGENT_SESSION_DIR` (`ExtractTranscripts`), `pi-debug.log` (`DebugLogNamer`; pi's stderr when `--debug` is set), `ParseTranscriptFile` judges the tee'd `--mode json` stream and session files | Format-specific; not shared across runtimes. Debug-log filename defaults to `agent-debug.log` unless the runtime implements `DebugLogNamer` |

### Fail modes

Harness `security.fail_mode` controls whether critical findings **block** the run (`closed`, default) or **warn** and continue (`open`). This applies to host scans, sandbox `scan context`, and the host-side runtime content scan alike.

## Runtime interface contract

| Interface | Responsibility |
|-----------|----------------|
| `runtime.Runtime` | Name, config dir, env exports, bootstrap, run loop, per-iteration artifact cleanup |
| `runtime.BootstrapInput` | Portable agent name/path, skill dirs, and plugin dirs to upload |
| `runtime.SandboxHooksBootstrap` | Optional `BootstrapInput` extension — runtime-neutral sandbox tool hook config (`security.SandboxHookConfig`); every runtime should honour it |
| `runtime.TranscriptHandler` | Extract transcripts/debug logs; parse errors for CI annotations |
| `runtime.DebugLogNamer` | Optional — names the per-iteration debug-log artifact (default `agent-debug.log`) |
| `runtime.ContextBridger` | Optional — runtime auto-loads only `CLAUDE.md`, so the runner injects a `CLAUDE.md`→`AGENTS.md` pointer (Claude Code: yes; runtimes that read `AGENTS.md` natively: omit) |

A runtime whose `Bootstrap` does not type-assert `SandboxHooksBootstrap` will **not** install Tirith, SSRF, canary, or the other hook scripts. The primary security boundary is the OpenShell sandbox, its L7 egress policy, and credential placeholders (ADR 0017, ADR 0025); the hooks are defense-in-depth that every runtime should wire rather than silently drop ([ADR 0090](../ADRs/0090-runtime-neutral-sandbox-hooks-contract.md)). Fill in the matrix column above either way.

## Sandbox hook contract

**Contract version: v2** — PostToolUse scripts consume Claude Code's `tool_response` (falling back to `tool_result` for adapters/tests) and replace output via `hookSpecificOutput.updatedToolOutput`. v1 (`tool_result` in/out only) was inert under Claude Code (#6357).

The hook scripts in `internal/security/hooks/*.py` are plain programs with no Claude Code dependency; Claude Code invokes them through `settings.json`. Any runtime can call them from its own tool-call interception point (OpenCode `tool.execute.before/after`, pi TypeScript extension API `tool_call`/`tool_result` with `{block: true, reason}` structured denial, Cursor hooks, ...).

### Files and wiring

- **Files:** `security.HookFiles(cfg)` returns `filename → script bytes` for the enabled hooks; `runtime.installHookScripts(sandbox, dir, cfg)` creates `dir` in the sandbox and uploads them there (executable) — any directory works. Claude uses `/sandbox/claude-config/hooks/` (`security.SandboxHooksDir`), with the wiring at `/sandbox/claude-config/hooks.json` (`security.SandboxHooksSettings`) loaded via `--settings`.
- **Plan:** `security.HookPlan(cfg)` returns ordered `HookGroup{Phase, Tools, Scripts}` entries. `GenerateHooksConfig` is rendered from `HookPlan`, so the two cannot diverge.
- **Phases:** `PreToolUse`, `PostToolUse`, `PostToolUseFailure`. The last carries Claude Code's failed-call payload (`hook_event_name`, `tool_name`, `tool_input`, a string `error`) and allows no output rewrite, so the chain halts there on a canary and otherwise only detects — logging credential-shaped and control content and returning an `additionalContext` warning. Adapters whose post-tool event already fires for failed calls (pi) map it onto nothing.
- **Tools:** `HookGroup.Tools` are Claude Code tool names (`Bash`, `Read`, `WebFetch`, `*` = all); runtimes with other names translate before matching (see [Tool-name vocabulary](#tool-name-vocabulary-608)).
- **PostToolUse is one script:** a single `posttool_chain.py` on `*` applies unicode → canary → suppress → redact in-process. Unicode normalization runs first because every later content decision is made on its output: an attacker who splits a canary or a secret with zero-width or fullwidth characters must not evade detection and then have the chain reassemble the clean value (Claude Code runs matching hooks in parallel and does not merge two `updatedToolOutput` rewrites). Individual sanitizer files and `canary_posttool.py` ship as libraries the driver imports; adapters should invoke the chain, not the stages.

### Tool-name vocabulary (#608)

`security.CanonicalClaudeTools` (`internal/security/canonical_tools.go`) lists the tool names Claude Code exposes; `security.LegacyClaudeTools` lists names Claude Code no longer has but that agent `tools:` frontmatter and adapters still use (`LS`, `MultiEdit`, `Task` → `Agent`, ...). `FULLSEND_TOOL_ALLOWLIST` and `security.HookGroup.Tools` are written in this vocabulary. Adapters must translate to it before invoking any hook script.

The vocabulary was verified 2026-08-23 against the live tools reference (the latest release); the CHANGELOG recorded no tool changes since the version pinned in the sandbox image at the time (2.1.234) — **re-check on every pin bump**, and note that the pin only describes what runs because of the build-time assertion in [Pinned runtime binaries](#pinned-runtime-binaries-in-the-sandbox-image).

It is a **reference** checked by tests, not validated at run time:

| Test | Pins |
|------|------|
| `TestHookPlan_ToolsAreCanonical` | every `HookPlan` tool |
| `TestPiToolNameMapsUseClaudeVocabulary` | the pi adapter's maps (`piToolForClaude`/`claudeToolForPi` in `internal/runtime/pi_agent.go` → `fullsend-manifest.json` `hooks.toolNames` → `fullsend-hooks.js` `claudeToolName()`) to canonical *or legacy* names — pi's `ls` maps to `LS`, which Claude Code no longer sends, so an agent allowlisted in canonical-only vocabulary sees pi's `ls` as a plain `tool_blocked` |
| `TestToolAllowlistHook_VocabularyMatchesGo` | the copy inside `tool_allowlist_pretool.py` identical to the Go set |

How `tool_allowlist_pretool.py` reports a name that does not match (the allowlist is exact-match and fail-closed, so an un-translated name is always **blocked** — the difference is the diagnosis):

| Situation | Finding | Severity / action | Reason text |
|-----------|---------|-------------------|-------------|
| Blocked name equals an allowlisted **Claude** name case-insensitively | `tool_name_unnormalized` | `high` / block | `ALLOWLIST_HOOK_ERROR: tool name '<name>' is not canonical Claude vocabulary (expected '<entry>'); the runtime adapter must translate it` (for a legacy entry: `... is not the legacy Claude name the allowlist uses (expected 'LS') ...`) |
| The *tool name* is the Claude one but the allowlist entry is not (e.g. `Bash` vs an allowlist written as `bash`) | `allowlist_entry_unnormalized` | `high` / block | `... is not Claude vocabulary (expected canonical name 'Bash'); fix the allowlist` |
| Neither side is a Claude tool name | `tool_name_case_collision` | `high` / block | says so and blames neither |
| No case-insensitive match | `tool_blocked` | `critical` / block | plain block |
| Non-string `tool_name` | — | block | blocks with the JSON contract rather than a traceback |

The three `high` findings do not trip `critical`-keyed escalation the way a forbidden tool does. MCP tools (`mcp__<server>__<tool>`) are not canonical — they are matched verbatim and a case variant is treated as a different tool (`tool_blocked`). The diagnostic only sees *case* variants: a renaming gap such as pi reporting every edit as `Edit` while an agent is allowlisted only for `MultiEdit` surfaces as a plain `tool_blocked`. No case-insensitive *allow* is performed.

### Wire protocol (per script)

JSON on stdin; the script's exit code and stdout are the reply.

| | Input | Reply |
|---|---|---|
| **PreToolUse** | `{"tool_name": ..., "tool_input": {...}}` | exit `0` = allow. Blocking scripts exit `1` and print `{"decision":"block","reason":"..."}`; the adapter must stop the tool call and surface the reason |
| **PostToolUse** | the same plus the tool output as `tool_response` (Claude Code; string or structured object such as Bash `{stdout, stderr, interrupted, isImage}`), `tool_result` accepted as a fallback | *Blocking* (standalone `canary_posttool.py`, and `posttool_chain.py` when its canary stage fires): exit `1` + `{"decision":"block",...}`; the adapter drops the result. *Sanitizing* stages (suppress/unicode/redact): always exit `0` and, when they changed something, print `{"hookSpecificOutput":{"hookEventName":"PostToolUse","updatedToolOutput": <same shape as the input value>}, "tool_result": <scan text>}`. Empty stdout = unchanged |

Shape rules:

- `updatedToolOutput` must match the tool's output shape — a bare string is ignored for built-in Claude Code tools.
- `scan_text` flattens every string field (including `stderr`), newline-joined so a needle cannot match across a field boundary (such a match would be unredactable, since the redactors rewrite each field independently); `apply_text` writes a replacement into the first text slot and blanks the rest, or leaves unrecognized structured shapes unchanged.
- Unicode normalization skips identifier fields (`hook_io.IDENTIFIER_KEYS`: paths, URLs, commands, exact-match edit strings) — NFKC would hand Claude a path that does not exist on disk; secret redaction still walks them, since it only replaces matched patterns.

### Sanitizer scope — what is rewritten, and what is not

The PostToolUse stages exist to remove *controls-relevant* content and nothing else, because an agent edits against what it reads — a rewritten `Read` result means `Edit.old_string` no longer matches the file, and a `Write` of what it saw persists the rewrite.

**Secret redaction** masks credential-shaped values only:

- the prefix patterns (`ghp_...`, `sk-...`, `AKIA...`, bearer headers, private-key blocks, database URLs);
- env/JSON shapes that need **both** a secret-bearing name (`..._TOKEN`, `api_key`, `accessToken` — not `TOKEN_URL`/`KEY_ID`/`publicKey`) **and** a value that is not an identifier, member path (`request.headers.authorization`), URL, path, placeholder or word phrase (`test-secret`, `ghs_policy_token`);
- a source-style `name = expr` counts only when the value is a quoted literal.

A sweep of 900 fullsend files through the chain rewrites only test files holding token-shaped fakes.

**Context suppression** condenses the output of exactly one verification command, and only from positive evidence:

- Commands: `go test`, `pytest`, `npm test`, `make test`, `pre-commit run`, `gitleaks detect`, `scan-secrets`, with optional setup prefixes (`cd`, `export`, `source`). The command must *start* with the tool, after wrappers that run it (`VAR=...`, `sudo`, `nice`, `timeout <n>`, `env VAR=...`, `uvx`, `npx`, `uv run`, `mise exec --`, stacked; `python3.12 -m pytest` counts). A command that merely mentions it, such as `grep -n scan-secrets hooks.py`, keeps its output.
- Evidence: `ok <pkg>`, `N passed`, `<hook>...Passed`, `no leaks`. Silence is never condensed into "passed" — a hook whose interpreter is missing is silent too, and Claude Code's Bash result carries no exit code — so linters and `go vet`/`go build`, whose clean run prints nothing, are never condensed.
- Pass-through, untouched: pipelines (`| tail` can cut the `FAIL` line; a `|` inside quotes such as `-run 'A|B'` is not a pipeline), `$(...)`, chains of two tools (`pytest; go test`, and deliberately also `go test && go vet` — one summary cannot speak for two), a trailing `echo $?`, and any output carrying a failure marker (`FAIL`, `panic:`, `Traceback`, `3 failed`). Comment lines and backslash continuations are tolerated.

**Unicode** strips invisible, bidi, tag, NUL and ANSI/OSC characters and runs of variation selectors, but keeps compatibility characters (fullwidth, ligatures, CJK punctuation) and single emoji/CJK selectors. NFKC is applied to a *detection copy* (canary, secret patterns); a field is emitted normalized only when the normalized copy reveals an escape sequence or a secret the original hid.

Every rewrite attaches `hookSpecificOutput.additionalContext` so the agent knows the output was changed and why. Every hook entry carries `timeout: 30` — Claude Code's 600 s default fails open, and so does the 30 s one (PreToolUse blockers included); the scripts finish in milliseconds and `tirith_check.py` bounds its own scan at 5 s, so the budget is headroom, not a ceiling the scripts approach.

### Hook fail modes

| Component | On malformed / oversized input (> 10 × 1024 × 1024 characters, text-mode stdin) | On its own error |
|-----------|-------------------------------------------|------------------|
| Blocking scripts (all PreToolUse, standalone `canary_posttool.py`) | fail **closed** — block | block |
| `tirith_check.py` | block | fails **open** when the `tirith` binary is missing, times out or errors — unless `TIRITH_REQUIRED=1` (`appendHookEnv` writes it when Tirith is enabled; adapters must make sure it reaches the script) |
| Sanitizing scripts / each `posttool_chain.py` sanitizer stage | fail **open** — pass through unchanged (exit 0; the unicode hook logs an `input_truncated` finding) | pass through; recorded in `findings.jsonl` as `<stage>_stage_error` |
| `posttool_chain.py` canary stage | fails **closed** whenever `FULLSEND_CANARY_TOKEN` is set: input the driver cannot read blocks (`exit 1`, `continue: false`) instead of skipping detection; with no canary token configured it stays fail-open | a scan that raises is treated as a hit; a hit whose redaction cannot be verified clean withholds the output entirely; `exit 1` is unconditional |

Also: empty/whitespace-only stdin is treated as "no tool call" and allowed by every script; a payload without `tool_name` blocks only in the allowlist hook; adapters must not treat a sanitizer's empty stdout as an error; detection and redaction share one case-insensitive matcher (`hook_io.canary_pattern`), so a token that is detected is always one that can be redacted.

### Environment

`runtime.appendHookEnv` writes into `/sandbox/workspace/.env`; the runtime must launch the scripts with that file sourced (Claude's run command does). When the CLI layer resolves a forge egress entry (for example, the GitLab forge host and port via `gitlab.ResolveForgeHostPort()`), it passes it through `SandboxHookConfig.WithForgeEgressEntry()` and `appendHookEnv` merges it into `FULLSEND_EGRESS_ALLOWLIST` so the SSRF hook defers to the L7 proxy for the forge API (#6615). Findings go to `/sandbox/workspace/.security/findings.jsonl`.

| Variable | Read by | Behaviour |
|----------|---------|-----------|
| `TIRITH_FAIL_ON`, `TIRITH_REQUIRED` | `tirith_check.py` | written by `appendHookEnv`; `TIRITH_REQUIRED=1` turns the fail-open into fail-closed |
| `FULLSEND_EGRESS_ALLOWLIST` | `ssrf_pretool.py` | comma-separated `host:port` entries, exact hostnames only — wildcards are skipped with a warning on stderr; on DNS failure the hook defers to the L7 proxy for allowlisted hosts instead of failing closed; if DNS succeeds but resolves to a blocked IP, the allowlist is not consulted |
| `FULLSEND_TOOL_ALLOWLIST` | `tool_allowlist_pretool.py` | fail-closed when unset |
| `FULLSEND_CANARY_TOKEN` | both canary hooks | no-ops when empty; supply it via harness `env.sandbox`/`host_files` |
| `FULLSEND_TRACE_ID` | all scripts | correlates findings with the run |

### Suppression reachability

Under Claude Code a non-zero-exit command never reaches `PostToolUse` at all, so the suppressors only ever see zero-exit output; a tool that exits 0 with nothing to say is the case that used to be summarized as "passed". Adapters whose post-tool event also fires for failures (pi's `tool_result`) do deliver failed calls to the same chain, which is why the positive-evidence rule matters on both.

### Claude Code caveats (#6358, #6357)

1. **Loading — fixed by #6358.** The hook wiring is written to the runner-owned `/sandbox/claude-config/hooks.json` and passed explicitly via `--settings`, so it loads regardless of the CLI's working directory (previously it sat unread in `/sandbox/workspace/.claude/`); the `hooks-loaded.feature` behaviour scenario guards the "silently not loaded" regression class. Claude Code still auto-loads a target repo's own `<repo>/.claude/settings.json` hooks from `<cwd>` — a separate exposure to assess.
2. **Payload — fixed in #6357, contract v2.** Scripts read `tool_response` (fallback `tool_result`) and replace output via `hookSpecificOutput.updatedToolOutput` with the original shape preserved. Sanitizer order and canary detection share `posttool_chain.py` so two PostToolUse hooks cannot race. `scan_text` inspects every string field (including `stderr`).
3. **Failed tool calls.** Claude Code fires `PostToolUse` only when a tool **succeeds**; a failed call (non-zero-exit Bash included) fires `PostToolUseFailure`, which delivers the error text but supports no output rewrite.
   - `HookPlan` wires the same `posttool_chain.py` there, where it runs canary detection (halt) plus detection-only secret and unicode passes that log to `findings.jsonl` and return an `additionalContext` warning — `additionalContext` is the only output the event accepts, so a credential or an ANSI/zero-width sequence in a failed command's output still reaches the transcript unmasked and the agent is told not to copy or obey it.
   - Scanning covers every string in the payload rather than one named key (the documented field is `error`; doc versions differ), halting via `continue: false` (the only decision control the event honours).
   - Detection also runs on a detection copy — NFKC-normalized with combining marks, format characters (zero-width, bidi, tag), line/paragraph separators, control characters and whole ANSI/OSC sequences removed, i.e. everything the unicode stage strips from a successful call — so it sees through the same obfuscation on both paths.
   - Suppression, unicode normalization and redaction cannot apply to a failed call under Claude Code; pi sanitizes those too, because its `tool_result` event fires for failures.
   - `interrupted` on a Bash `tool_response` marks a cancelled tool, not an exit code — the `Exit code` prefix check in `looks_failed` therefore serves the v1 adapter path only.
4. **Blocking.** Claude Code keys on the stdout JSON on any exit code (`decision:"block"` is deprecated for PreToolUse but still maps to `deny`) and treats a bare exit `1` as non-blocking (exit `2` is its own blocking code); a local control run confirmed the scripts' "exit 1 + `{"decision":"block"}`" convention does block once the settings are loaded. For PostToolUse, `decision:"block"` **only appends `reason` next to the tool result — Claude still sees the original output**. `canary_posttool.py` therefore also emits `updatedToolOutput` with the token redacted to `[CANARY_REDACTED]`, and sets the universal `continue: false` field — the documented control that actually halts the session — so a leak still terminates the run.

Net: after #6358 and #6357, both PreToolUse and PostToolUse halves of the contract are effective under Claude Code.

## Pinned runtime binaries in the sandbox image

`images/sandbox/Containerfile` pins every runtime binary and provider extension; `fullsend-code` extends that image, so both inherit the same pins. What is pinned, and what to re-check when a pin moves:

| Binary | Pin | Re-check on bump |
|--------|-----|------------------|
| Claude Code | `ARG CLAUDE_CODE_VERSION` (npm, Renovate-tracked). The OpenShell base image ships its **own** unpinned Claude Code at `/usr/local/bin/claude` (whatever `curl claude.ai/install.sh` fetched when the base was built), and `/usr/local/bin` precedes npm's `/usr/bin` on the sandbox `PATH` — so the Containerfile replaces that file with a symlink to the npm install and fails the build unless `claude --version` equals the pin (#6612; before that fix the base image's 2.1.156 shadowed every pin). `TestSandboxImageClaudeCodePinWins` guards the step | the [tool-name vocabulary](#tool-name-vocabulary-608); the hook contract caveats above; the alias table — `opus`/`sonnet`/`haiku` resolve from the running version's built-in defaults on Vertex, and `ANTHROPIC_DEFAULT_*_MODEL` does not steer the request there, so a harness or `agents:` entry that needs a specific generation must name the id |
| pi | `ARG PI_VERSION` (npm, `--ignore-scripts`, Renovate-tracked; `TestSandboxImagePinsAreRenovateTracked`) | `parsePiStream` fixtures (`internal/runtime/testdata/pi/regen.sh`); the extension compatibility notes in [pi runtime internals](#pi-runtime-internals-6464) |
| `pi-anthropic-vertex` | `ARG PI_ANTHROPIC_VERTEX_VERSION` + tarball SHA256, under `/usr/local/share/pi-extensions/anthropic-vertex` | its `sync/compat.json` against the pinned pi; the `@anthropic-ai/sdk` override vs pi's `packages/ai/package.json` |
| `pi-xai-vertex` | `ARG PI_XAI_VERTEX_VERSION` + tarball SHA256, under `/usr/local/share/pi-extensions/xai-vertex` | its `peerDependencies` floor (it mirrors no pi internals) |

The run log's `Agent: <model> (vX.Y.Z)` line is the ground truth for which Claude Code version served a run; the Containerfile pin is a claim, the assertion at build time is what makes it true.

## Sandbox workspace layout

The sandbox has two key directories that map to Claude Code's config levels (plus a runner-owned config directory per additional runtime, e.g. `pi-config/` for pi):

```
/sandbox/
├── pi-config/                       ← PI_CODING_AGENT_DIR (pi runtime; written by PiRuntime.Bootstrap)
│   ├── APPEND_SYSTEM.md                Agent definition body (appended to pi's default system prompt)
│   ├── settings.json                   defaultProjectTrust: never, quietStartup, retry/compaction on
│   ├── skills/<name>/SKILL.md          Harness skills (pi's native skill discovery)
│   ├── hooks/*.py                      Security hook scripts (same files as claude-config/hooks/)
│   ├── fullsend-hooks.js               Hook adapter extension (loaded with -e; --no-extensions otherwise)
│   ├── fullsend-manifest.json          Agent tools/allowlist, HookPlan, pi version — read by Run and the extension
│   └── sessions/                       PI_CODING_AGENT_SESSION_DIR (session JSONL → transcripts)
│
├── claude-config/                   ← CLAUDE_CONFIG_DIR (personal level)
│   ├── agents/
│   │   └── <name>.md                   Agent definition (filename derived from the agent name)
│   ├── skills/
│   │   ├── code-review/SKILL.md        Built-in skills (personal level — wins on collision)
│   │   ├── pr-review/SKILL.md
│   │   └── ...
│   ├── plugins/
│   │   └── ...                         Plugin state (simplified; see bootstrapPlugins())
│   ├── hooks/                          Security hook scripts (PreToolUse, PostToolUse)
│   └── hooks.json                      Hook wiring (loaded via --settings in buildRunCommand)
│
└── workspace/                       ← SandboxWorkspace
    ├── .env                            Environment variables (sourced before claude)
    ├── .env.d/                         Additional env files (host_files expand)
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
│  Source: /sandbox/claude-config/agents/<name>.md       │
│  Loaded via: --agent flag                              │
│  Controls: role, task, tools, disallowedTools, model,  │
│            built-in skills list                        │
│  Authority: highest — repo cannot modify               │
├────────────────────────────────────────────────────────┤
│  Layer 2: Project Instructions (advisory)              │
│  Source: /sandbox/workspace/<repo>/CLAUDE.md           │
│         /sandbox/workspace/<repo>/AGENTS.md            │
│  Loaded via: Claude Code auto-loads from working dir   │
│  Controls: conventions, architecture, domain context   │
│  Authority: advisory — cannot override layer 1         │
├────────────────────────────────────────────────────────┤
│  Layer 3: Skills                                       │
│  Personal: /sandbox/claude-config/skills/ (fullsend)   │
│  Project:  <repo>/.claude/skills/ (repo)               │
│  Precedence: personal > project (name collision →      │
│              fullsend wins, repo version shadowed)     │
│  Repo skills extend the agent; use config-driven       │
│  agent registration for org-level skill overrides      │
└────────────────────────────────────────────────────────┘
```

### AGENTS.md injection logic

`run.go` step 8a (`hasAgentsMD()` / `injectClaudeMDPointer()`):

1. If target repo has no AGENTS.md → inject org-level default from config repo,
   add to `.git/info/exclude`
2. If the runtime implements `ContextBridger` (Claude Code does), target
   repo has AGENTS.md but no CLAUDE.md → inject bridge CLAUDE.md pointing to
   AGENTS.md, add to `.git/info/exclude`
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

## Dummy runtime operations

The `dummy` runtime executes a YAML script of operations inside the real sandbox (behaviour tests only). Besides `write_fixture` and `fail`, dispatch behaviour tests use:

| Op | Args | Purpose |
|----|------|---------|
| `assert_env` | `VAR_NAME` | Assert env var is set and non-empty in the sandbox |
| `assert_file` | `path` | Assert file exists and is readable under the workspace |
| `assert_json` | `path,json_path` | Assert JSON file exists and dot-path field is present and non-null (uses `jq`) |

## pi runtime internals (#6464)

User-facing pi behaviour is in [Pi](../runtimes/pi.md). This section keeps the
verification provenance: what was checked against pi's source, on which version, and what must be
re-checked on a `PI_VERSION` or extension bump.

One iteration, end to end — the amber decision is what makes "hooks enabled" enforceable, since pi silently skips a missing `-e` extension:

```mermaid
flowchart TB
  B["Bootstrap (once per run)\nagent .md → APPEND_SYSTEM.md + --tools\nhook scripts + manifest + adapter\npi --version preflight"]
  G{"shell guard, before .env (command -p):\nadapter present and SHA-256 = embedded copy?\nmanifest present?"}
  X["exit 97\npi never starts unhooked\n(Run refuses earlier, exit -1,\nif the manifest has no hook plan)"]
  E["source .env\nunset ANTHROPIC_*\npin GOOGLE_CLOUD_PROJECT"]
  P["pi --print --mode json --no-approve\n--no-extensions [-e vertex, on Vertex] -e hooks\n--tools ... --model ... #lt;/dev/null"]
  S["parsePiStream\nexactly one ResultEvent\nexit 0 + stream error ⇒ run fails"]
  A["artifacts\noutput.jsonl · transcripts/\nmetrics.json (runtime: pi)"]
  B --> G
  G -- no --> X
  G -- yes --> E --> P --> S --> A
  classDef guard fill:#fbf0d6,stroke:#d98e04,color:#1b2230;
  classDef bad fill:#f8e1de,stroke:#c0392b,color:#1b2230;
  classDef opt fill:#e3e9fb,stroke:#2d5be3,color:#1b2230;
  class G guard;
  class X bad;
  class B,P,S opt;
```

### Posture

- **No permission system at all** — pi's stated posture is "run in a container". The OpenShell sandbox + L7 egress policy + credential placeholders (ADR 0017/0025) are the boundary, with the fullsend extension adapter as defense-in-depth (same posture as accepted for OpenCode in #1260 / ADR 0090).
- **No built-in MCP** — out of scope; fleet uses none.
- **No `--max-turns`/`--timeout`** — the runner's exec timeout covers it; pi's `bash` tool has no default command timeout either (`core/tools/bash.ts`), so a runaway command is bounded only by the iteration timeout, as with Claude Code.
- **Reads AGENTS.md natively** — no CLAUDE.md bridge needed (does not implement `ContextBridger`).
- **Tool names are lowercase** (`bash`, `read`, `write`, `edit`) — the hook adapter translates to the contract's Claude-name vocabulary (#608).
- **Fast release cadence** (~weekly minors; 0.84.0 changed the `message_update` wire shape) — pin exact versions; `parsePiStream` fixtures are hand-authored to `packages/coding-agent/docs/json.md` (and `core/agent-session.ts` for the session-level events) for the pinned version; `internal/runtime/testdata/pi/regen.sh` re-records `basic_run.ndjson` from a live run.

### Runs unattended

Parity with `claude -p --dangerously-skip-permissions`, verified against pi v0.84.2 source and empirically on the pinned build:

- pi has no tool-approval layer at all (nothing in `core/tools/*` or `core/bash-executor.ts` prompts).
- In `--print` mode extensions get a no-op UI context, so `ctx.ui.confirm/select/input/editor` resolve immediately (`modes/print-mode.ts`, `core/extensions/runner.ts`).
- `--no-approve` sets the project-trust override, so the trust-gated project resources — `.pi/{settings.json,extensions,skills,prompts,themes,SYSTEM.md,APPEND_SYSTEM.md}` and `.agents/skills` (`core/trust-manager.ts`); `AGENTS.md` itself is still read as context — are ignored without a dialog (`cli/args.ts`, `main.ts`). `defaultProjectTrust: never` in the global settings covers the no-flag case (verified on the pinned build: a planted `.pi/extensions/evil.js` in the repo does not load under `--no-approve` and does under `--approve`).
- First-run setup, theme selection, telemetry consent and the version check are interactive-only code paths (`PI_TELEMETRY=0`, `PI_SKIP_VERSION_CHECK=1`/`PI_OFFLINE=1` set anyway).
- A missing credential raises `No API key found` and exits 1 — no `/login` prompt (`core/agent-session.ts`, `modes/print-mode.ts`); retries are bounded (`retry.maxRetries: 3`, 2/4/8 s) and compaction is automatic.
- **The one blocker found:** print mode reads a non-TTY stdin to EOF before the first prompt, even with a positional message (`main.ts` `readPipedStdin`), so an exec that keeps stdin open with no writer hangs pi — `Run` therefore appends `</dev/null` to the `pi` invocation; an idle upstream pipe then exits immediately (verified: open pipe without the redirect → killed by timeout; with it → proceeds).

### Process and exit codes

- **Hardening levers in use** — `Run` executes `pi --print --mode json --no-approve --no-extensions --no-prompt-templates --no-themes --session-dir /sandbox/pi-config/sessions [-e /usr/local/share/pi-extensions/anthropic-vertex | -e /usr/local/share/pi-extensions/xai-vertex] [-e /sandbox/pi-config/fullsend-hooks.js] [--tools ...] --model <provider/id> --thinking <effort|high> '<RunParams.Prompt, default "Run the agent task">' </dev/null [2>>/sandbox/workspace/pi-debug.log]`; `settings.json` sets `defaultProjectTrust: never` (repo-owned `.pi/` never loaded); `PI_OFFLINE=1`/`PI_TELEMETRY=0`/`PI_SKIP_VERSION_CHECK=1` come from `EnvExports`. Context files (`AGENTS.md`) and skills stay on — they are the harness's own inputs. `PI_CODING_AGENT_DIR/extensions/` is arbitrary TypeScript loaded at startup and the config dir is not a permission boundary, which is why only the explicit `-e` paths load (at most one vendored provider extension plus the hook adapter). Right after `.env` is sourced, `Run` re-exports `EnvExports()` (`PI_CODING_AGENT_DIR`, the session dir, the offline switches) so a rewritten `.env` cannot relocate pi's config directory. For the built-in `openai` provider (`openai/<id>`, [ADR 0092](../ADRs/0092-openai-wif-credential-delivery.md)) no extension loads and no `--api-key` is passed; `Run` instead seeds `auth.json` under `PI_CODING_AGENT_DIR` with the placeholder the sandbox environment carries for `OPENAI_API_KEY` (`PiOpenAIAuthSeed`, before `.env` is sourced), because pi re-reads that file on every revision change and resolves the key per request — the runner re-runs the same seed through `sandbox exec` after each credential refresh, which is what lets a running iteration follow a refresh on OpenShell 0.0.115, where a revision-scoped placeholder stays pinned to its generation and the unrevisioned alias is refused (`--api-key` would outrank the file and pin the iteration). It unsets `OPENAI_BASE_URL`/`AZURE_OPENAI_API_KEY`/`OPENAI_API_KEY`/`NODE_OPTIONS`/`NODE_PATH` after `.env`, fails the iteration with exit 1 when the environment holds anything but a gateway placeholder at seed time (before `.env`), and runs a config-dir integrity guard that exits 98 when `models.json` exists or `auth.json` is anything but pi's own `{}` or exactly the seeded placeholder entry — `models.json` is the only way to move the provider's base URL, a redirect to another allowed REST host is the placeholder-leak path ADR 0025 describes, and pi itself writes an empty `auth.json` on every start so only its content counts. The guard runs before the agent-writable `.env` is sourced and again after it behind `unset -f test command grep tr sed printf`, whether or not hooks are enabled.
- **`--mode json` exits 0 on model error** — only text mode maps `stopReason: error|aborted` to exit 1. `parsePiStream` is the intended detector (assistant `stopReason` on `message_end.message` / last `agent_end.messages` entry) for the runner's exit-0-override (#2786/#5361). `Run` tees the stream to `output.jsonl`, `ParseTranscriptFile` reads it, and `Run` itself returns 1 on a stream-reported error, so the override and the runtime agree.
- **Exit code** — `Run` returns 1 when pi exited 0 but the stream's single `ResultEvent` reports an error (model error, incomplete stream), so the runner's exit-0 override and this agree; `ParseTranscriptFile` gives the same verdict from the tee'd `output.jsonl`.

### Agent definition translation

The Claude-style agent `.md` is parsed by `Bootstrap`:

| Agent `.md` | pi | Note |
|---|---|---|
| body | `APPEND_SYSTEM.md` | pi's default prompt and tool guidance are kept; `SYSTEM.md` would replace them — a deliberate difference from Claude Code, whose `--agent` makes the body *the* system prompt. The lifecycle run should confirm the fleet prompts tolerate pi's preamble, otherwise switch to `--system-prompt` |
| frontmatter `tools:` | `--tools` + an advisory Bash allowlist | pi enforces this strictly; Claude Code ≥ 2.1.119 enforces it unreliably |
| `model:` | fallback for the harness `model:` | |
| `description` | header line | |

`metrics.json`/`InitEvent` carry the provider-stripped model id (`claude-opus-4-6`), as for Claude Code; the provider is `gen_ai.system`'s job. For a provider whose ids are publisher-qualified this keeps that segment (`xai/grok-4.6`), since it is the wire id. Everything `Run` and the hook extension need is in `fullsend-manifest.json` because `Bootstrap` and `Run` are separate calls with no shared process state.

### Hook adapter contract

`Bootstrap` installs `security.HookFiles` under `/sandbox/pi-config/hooks/`, writes the `HookPlan` into `fullsend-manifest.json` and loads the embedded `fullsend-hooks.js` extension with `-e` under `--no-extensions` (per pi v0.84.2 `docs/extensions.md`).

`fullsend-hooks.js` sends the scripts `{tool_name, tool_input, tool_result, tool_response}` with Claude tool names (`bash→Bash`, `read→Read`, `write→Write`, `edit→Edit`, `grep→Grep`, `find→Glob`, `ls→LS`; `path` mirrored to `file_path`) and reads back either the v1 `tool_result` or the v2 `hookSpecificOutput.updatedToolOutput` (#6357), so the same extension works before and after the PostToolUse chain lands.

- PreToolUse groups run in `HookPlan` order and stop at the first block; a script that cannot be spawned blocks; PostToolUse blocks withhold the result and mark it `isError`.
- An unreadable manifest, or one without a hook plan, blocks every tool call.
- Because pi silently skips a missing `-e` path, `Run` checks — before sourcing the agent-writable `.env`, with `command -p sha256sum` / `command -p cut` so nothing in the shell environment can stand in for them — that the adapter exists and matches the embedded copy's SHA-256 and that the manifest exists, failing closed (exit 97) otherwise; it refuses to start at all (exit -1) when security is enabled but the manifest carries no hook plan; and it decides whether to load the adapter from the runner's security signal rather than the manifest.
- The manifest and the hook scripts themselves stay agent-writable between iterations — the same residue Claude Code has with `claude-config/hooks.json` and its scripts (both are written once at `Bootstrap`).
- Edit inputs keep pi's `edits[]` shape, with `path` mirrored to `file_path` and the first `oldText`/`newText` pair mirrored to `old_string`/`new_string`; no shipped script reads the latter.
- pi fires `tool_result` for failed calls too, so — unlike Claude Code's `PostToolUse` — errored tool output is sanitized as well.
- The `tool_call`/`tool_result` event shapes the adapter relies on (`toolName`, `input`, `content`, `isError`; `{block, reason}` and `{content, isError}` replies) are verified against pi v0.84.2 `src/extensions/types.ts`/`runner.ts`; the lifecycle run is the live confirmation.

### Claude-on-Vertex via an interim extension

pi's `google-vertex` provider is Gemini-only and the upstream `anthropic-vertex` provider is an open PR (earendil-works/pi#5262, still open as of 2026-08-22). The sandbox image vendors [`twoGiants/pi-anthropic-vertex`](https://github.com/twoGiants/pi-anthropic-vertex) v0.1.13 (commit `d3c9d10d`, MIT; reviewed — a ~300-line entry point plus ~220 lines mirrored from pi's `streamSimple` helpers; it registers provider `anthropic-vertex` and delegates streaming to pi's built-in Anthropic provider through an `AnthropicVertex` client) under `/usr/local/share/pi-extensions/anthropic-vertex`, pinned by tag + tarball SHA256 (`PI_ANTHROPIC_VERTEX_VERSION`/`_SHA256`). It is root-owned and outside `PI_CODING_AGENT_DIR`, so pi never auto-loads it; for the `anthropic-vertex` provider `Run` passes it with `-e` (`runtime.piVertexExtensionPath`; providers without a vendored extension get pi's built-ins only).

**Project, region, and credentials:**

| | Resolution | Note |
|---|---|---|
| Project | `GOOGLE_CLOUD_PROJECT`, then `GCLOUD_PROJECT`, then `ANTHROPIC_VERTEX_PROJECT_ID`, then `GOOGLE_CLOUD_PROJECT_ID` | The fleet env exports both the first and the third; `Run` pins `GOOGLE_CLOUD_PROJECT` to `ANTHROPIC_VERTEX_PROJECT_ID` when that is set, so the extension's first-wins order cannot diverge from Claude Code |
| Region | `CLOUD_ML_REGION`, then `GOOGLE_CLOUD_LOCATION`, default `us-east5` | |
| Auth | Google's `google-auth-library` reading `GOOGLE_APPLICATION_CREDENTIALS` | In CI that is the Workload Identity Federation `external_account` config the runner delivers via `host_files` (ADR 0025 tier 4), whose `credential_source.file` is the OIDC token at `/sandbox/workspace/.gcp-oidc-token` that the runner refreshes every 4 minutes; the library exchanges it at `sts.googleapis.com` for a short-lived access token (direct federated identity, no impersonation) — exactly the path Claude Code uses, under the same `*.googleapis.com` egress allowlist and `**/node` binary rule |

**Credential hygiene:** the bundled Vertex client (`@anthropic-ai/vertex-sdk` 0.14.4 over `@anthropic-ai/sdk` 0.91.1) honours `ANTHROPIC_VERTEX_BASE_URL` as its endpoint and would send a stray `ANTHROPIC_API_KEY` to Google as `X-Api-Key`; `ANTHROPIC_AUTH_TOKEN` and `ANTHROPIC_BASE_URL` are overridden by the Google bearer and the explicit Vertex base URL, but pi's built-in `anthropic` provider reads `ANTHROPIC_AUTH_TOKEN` and the SDK would read `ANTHROPIC_BASE_URL` for any provider that leaves `baseURL` unset. So `Run` unsets `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_BASE_URL`/`ANTHROPIC_VERTEX_BASE_URL` after sourcing `.env` for the `anthropic-vertex` provider (matched case-insensitively, as pi resolves provider prefixes) and keeps them for a direct `anthropic` provider, which needs the key.

**Known risks — re-check on every `PI_VERSION` or extension bump:**

- v0.1.13 was synced against pi 0.81.1 (upstream sync issue twoGiants/pi-anthropic-vertex#24 is open) and its mirrored option mapping can drift.
- The extension pins `@anthropic-ai/sdk` via `overrides`, and that must match the SDK version in pi's `packages/ai/package.json` (both 0.91.1 today) because the Vertex client is cast to pi's Anthropic client type.
- It copies pi's first-party Anthropic `compat` flags (strict tools, eager input streaming, adaptive thinking) onto the Vertex models — the Run PR must smoke an adaptive and a non-adaptive model against Vertex and, if Vertex rejects any of these, override them in `PI_CODING_AGENT_DIR/models.json` rather than patching the extension.

Replace with the upstream provider once #5262 ships in a pinned release.

### Grok-on-Vertex via a fullsend-owned extension

pi's built-in `xai` provider targets xAI's native API (`api.x.ai`, needs `XAI_API_KEY`) and `google-vertex` is Gemini-only, so neither reaches Grok on Vertex, which speaks the OpenAI-completions protocol. The sandbox image vendors [`fullsend-ai/pi-xai-vertex`](https://github.com/fullsend-ai/pi-xai-vertex) (MIT) under `/usr/local/share/pi-extensions/xai-vertex`, pinned by tag + tarball SHA256 (`PI_XAI_VERTEX_VERSION`/`_SHA256`, Renovate-tracked like the Anthropic one) and registering provider `xai-vertex` with model `xai/grok-4.6`.

- **No mirrored internals.** Unlike the Anthropic extension it registers `openAICompletionsApi()` and lets pi do streaming, tools and usage — so there is no `sync/compat.json` drift to re-check on a `PI_VERSION` bump; confirm its `peerDependencies` floor instead.
- **Auth** is ambient ADC through `google-auth-library` reading `GOOGLE_APPLICATION_CREDENTIALS`, the same WIF `external_account` path as Claude-on-Vertex and under the same `*.googleapis.com` egress allowlist, so no new credential plumbing.
- **Endpoint** is fixed to the **global** location (`/locations/global/endpoints/openapi`) because Vertex serves this model only there — regional endpoints answer `FAILED_PRECONDITION` — so `CLOUD_ML_REGION`/`GOOGLE_CLOUD_LOCATION` are deliberately ignored for this provider.
- **`Run`** passes it with `-e` (`runtime.piXaiVertexExtensionPath`), unsets `XAI_API_KEY` after sourcing `.env` so pi's built-in `xai` provider cannot shadow it, and defaults `XAI_VERTEX_PROJECT_ID` to `ANTHROPIC_VERTEX_PROJECT_ID` (then `GOOGLE_CLOUD_PROJECT`) **only when the runner has not set it**, so the fleet's Vertex project is the default without becoming a ceiling. Each Vertex provider resolves its own project variable, so one pi process can serve Grok, Claude and Gemini from different GCP projects; overriding an explicit value would collapse that and leave no way to point Grok at a project where it is actually enabled in Model Garden (the call then fails 403 `PERMISSION_DENIED`, and the extension does not warn because it did have a project — just the wrong one).
- **Model spec.** pi sends `Model.id` on the wire verbatim and Vertex wants the publisher-qualified name, so the id keeps its slash and the canonical spec is the three-segment `xai-vertex/xai/grok-4.6`. `translatePiModel` normalises the short `xai/...` form and a bare id under `FULLSEND_PI_PROVIDER=xai-vertex` to that form, case-insensitively — matching the gate, which uses `EqualFold` — because a spec that escapes normalisation reaches pi's built-in `xai` provider with `XAI_API_KEY` still set. A harness (or an `agents:` entry's `model:`) can name the three-segment spec directly, or select this provider with a bare `model:` plus `FULLSEND_PI_PROVIDER`.

### Binary present but unhooked

The pinned `pi` CLI and the vendored Vertex extensions ship in every sandbox image (so Bootstrap/Run work targets a reviewed version), so an agent on another runtime can invoke `pi -e /usr/local/share/pi-extensions/anthropic-vertex` ad hoc from Bash with none of that runtime's tool hooks — and in a Claude-on-Vertex sandbox the ADC credentials and project id it needs are already in the environment, so that is a working nested agent, not an inert binary. This is the same class of exposure as any interpreter the agent can run (`python`, `node`, `curl` with the same ADC token): the sandbox tool hooks are defense-in-depth and only see the top-level tool call (ADR 0090); the boundary remains the OpenShell sandbox, its L7 egress allowlist and the credential placeholders, which a nested `pi` cannot escape either. The image bakes `PI_OFFLINE=1`/`PI_TELEMETRY=0` and the runner-owned config paths as defaults; treat the `N/A — stub` matrix cells as "not wired", not "cannot run".

### Not yet exercised

`runtime: pi` is selectable, but no fleet lifecycle run on Vertex has been recorded yet:

- The Vertex model ids and the copied `compat` flags have not been exercised against Vertex — smoke an adaptive and a non-adaptive model first; override with `--model`/`FULLSEND_MODEL` if an id is rejected.
- Parser fixtures are hand-authored to the v0.84.2 wire docs — re-record with `internal/runtime/testdata/pi/regen.sh` once a run exists; `extension_error` events are not mapped.
- The behaviour scenario `features/runtime/pi.feature` (a real haiku run on Vertex of a minimal tool-using agent, asserting `metrics.json` `runtime: pi`, a `toolCall` in the pi session transcript and token usage) is gated on `BEHAVIOUR_CAPABILITIES=runtime-pi` until `fullsend-sandbox:latest` carries `PI_VERSION`; `features/triage/triage.feature` asserts the runtime selected from the repo config on every run.
- Pilot on a disposable org with `triage`/`prioritize` (no sub-agent assumptions) before `code`/`fix`. `review`/`retro` rely on Claude sub-agent rosters and are not supported: pi v0.84.2 has no sub-agent tool or `agents/*.md` concept in core — only the bundled example extension (`examples/extensions/subagent/`, spawns `pi -p --mode json` children without our hook adapter, Vertex provider, `--no-approve` or session dir) and the SDK route (`createAgentSession()` per child; parent extensions do not fire for children). A fullsend-owned sub-agent extension with the full child flag set is a follow-up tracked on #6527 (runtime parity backlog); until then `Bootstrap` appends a runtime note telling the agent no sub-agent tool exists and to execute sub-agent definitions itself, in order.

### OpenAI via Workload Identity Federation

GPT models run on pi's built-in `openai` provider with no OpenAI credential in the sandbox ([ADR 0092](../ADRs/0092-openai-wif-credential-delivery.md)): `fullsend run` exchanges the job's GitHub OIDC token for a ≤1 h access token (`internal/inference/openaiwif`, or `OPENAI_API_KEY` from the runner environment for local runs), imports the `fullsend-openai` profile from the scaffold embedded in the binary (profiles are deliberately not layered into `.fullsend/profiles`, whose wholesale import would replace the fleet's canonical profiles), creates a run-scoped OpenShell provider `openai-<sandbox suffix>` of that type carrying it, and deletes that provider when the run ends; the `fullsend-openai` profile allows only `POST /v1/responses` on `api.openai.com` for `**/node`. A per-provider refresher re-exchanges a fresh assertion shortly before `expires_in` and hot-updates the provider (a static key only has its expiry pushed out), and is stopped before the deferred cleanup deletes — or, when a kept sandbox still references it, expires in place — the provider.

### Other clouds

pi ships native `amazon-bedrock` (SDK default credential chain, incl. `AWS_WEB_IDENTITY_TOKEN_FILE`) and `azure-openai-responses` (`api-key` only, no Entra ID) providers; neither is wired into `Run`'s alias table, credential hygiene or the runner's OIDC refresh yet, and no egress profile allows their hosts. Follow-up tracked against #6464.
