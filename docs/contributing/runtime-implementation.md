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
- [codex runtime internals (#6920)](#codex-runtime-internals-6920) — verification provenance for the codex backend

## Adding a runtime: checklist

1. Register the backend in `runtime.Resolve()`.
2. Implement `runtime.Runtime` and honour `runtime.SandboxHooksBootstrap`
   (see [Runtime interface contract](#runtime-interface-contract)). A runtime
   that ignores it installs **no** sandbox tool hooks.
3. Fill in every column of the [security feature matrix](#security-feature-matrix)
   for the new runtime — including the cells that are "not wired"; say so
   explicitly rather than leaving them blank. Internal test-only runtimes
   (e.g. `dummy`, `dummy-playback`) with no LLM interaction are exempt.
4. Add its row to the config-key table in
   [runtimes.md](../runtimes.md#harness-config-keys-per-runtime).
5. If the runtime's binary ships in the sandbox image, pin it the way the
   existing ones are pinned and prove the pin at build time
   ([Pinned runtime binaries](#pinned-runtime-binaries-in-the-sandbox-image)).
6. Name the process that opens the model connection in `runtimeEgressBinaries`
   (`internal/cli/runtime_binaries_test.go`) and make sure the inference
   profiles' `binaries:` globs match it — a runtime exec'd directly (no node
   wrapper) is not covered by `**/node`
   ([Egress binary identity](#egress-binary-identity-per-runtime)).

### Consumer-completeness touchpoints

After the core implementation above, walk through every file below. Each
one contains a hardcoded list of valid runtimes or runtime-specific
content that must be updated whenever a runtime is added or renamed.

**Registration and config:**

- [ ] `internal/runtime/registry.go` — add a `case` to `Resolve()` that
  returns the new backend (mirrors step 1 above — listed here so the
  walkthrough is self-contained).
- [ ] `internal/config/config.go` — add the runtime name to the slice
  returned by `ValidRuntimes()`.

**Tests:**

- [ ] `internal/runtime/registry_test.go` — add a `Resolve("<name>")`
  assertion block to `TestResolve` (and to `TestResolveFromConfig` /
  `TestResolveFromPerRepoConfig` if the runtime is user-selectable).
- [ ] `internal/config/config_test.go` — update any assertion on
  `ValidRuntimes()` to include the new name.

**CLI:**

- [ ] `internal/cli/runtime_prompt.go` — if the runtime is test-only
  (e.g. `dummy`, `dummy-playback`), filter it from
  `userRuntimeChoices()` so it does not appear in the interactive
  prompt.
- [ ] `internal/cli/run.go` — update the `--runtime` flag description
  to list the new runtime name.
- [ ] `internal/cli/admin.go` — update the `--runtime` flag description
  in `newInstallCmd()` to include the new runtime name.
- [ ] `internal/cli/github.go` — update the `--runtime` flag description
  in `newGitHubSetupCmd()` to include the new runtime name.

**Documentation:**

- [ ] `docs/runtimes.md` — add a row to the
  [harness config-keys table](../runtimes.md#harness-config-keys-per-runtime)
  (item 4 above) and add the runtime to any prose lists of valid values.
- [ ] `docs/architecture.md` — update the runtime selection diagram
  (the Mermaid `CFG` node lists valid runtime names) and any prose
  references.
- [ ] `docs/cli/run.md` — update any `--runtime` flag description or
  valid-values list if the page documents runtime flags.
- [ ] `docs/cli/github.md` — same as `run.md` if this page documents
  runtime flags.
- [ ] `docs/guides/infrastructure/layered-config-reference.md` — update
  the `runtime` field's valid values in the config-key table.

Use this list as a mechanical walkthrough — check every box, even if the
answer is "no change needed", so omissions are deliberate rather than
accidental.

## Security feature matrix

The sandbox is the containment boundary; everything a runtime does with hooks and tool restrictions is steering inside it ([ADR 0027](../ADRs/0027-allowed-and-disallowed-tools-for-agents.md)). Read the matrix with that picture in mind:

> **Test-only runtimes (`dummy`, `dummy-playback`) are exempt from the
> matrix tables below.** They perform no LLM interaction, install no sandbox
> tool hooks, and have no security surface to document — the sandbox boundary
> is their only control. This exemption applies to all three tables
> (host-side controls, sandbox tool hooks, bootstrap and artifacts).

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
    EG["egress allowlist: *.googleapis.com · api.anthropic.com\n(+ api.openai.com POST /v1/responses with the openai provider)\nbinaries: **/claude · **/claude.exe · **/node (pi runs via node) · **/pi (fleet-profile parity) · **/codex"]
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

| Feature | Where it runs | Claude Code | OpenCode (stub) | Pi | Codex | Notes for future runtimes |
|---------|---------------|-------------|-----------------|----|-------|---------------------------|
| **Host-side context injection scan** (unicode, SSRF patterns on repo context files) | Host + sandbox `scan context` | ✓ | N/A — stub | ✓ | ✓ — runner-side, so identical to Claude Code and pi | Harness `security.host_scanners`; heuristic scanners only — the DeBERTa ML model was removed from the sandbox in #6522 (its only consumer is the host-side `scan input`, not `scan context`) |
| **Host-side runtime content scan** (agent def, SKILL.md, and every text file of each declared plugin — `node_modules` included — before upload) | Host (`scanRuntimeContent`) | ✓ | N/A — stub | ✓ | ✓ — runner-side, so identical to Claude Code and pi | Uses `security.InputPipeline()`; not part of the `Runtime` interface. Extension files over 1 MiB are noted and skipped, and a tree above 20k files is refused in either `fail_mode` |
| **Prompt injection (DeBERTa)** | Host `fullsend scan input` only | ✓ in the runner image (built `CGO_ENABLED=1 -tags ORT` with `libtokenizers.a` + ONNX Runtime >= 1.28); ✗ in the release tarballs, which stay `CGO_ENABLED=0` and untagged (#6522) | N/A — stub | Same as Claude Code — host-side, not a runtime distinction | Same as Claude Code — host-side, not a runtime distinction | Shipped enabled only in `ghcr.io/fullsend-ai/fullsend-runner`; the release tarball the composite action downloads has it compiled out, so CI runs never reach it. **Not an active control on the `fullsend run` path either way**: `RunMLScan` is called only from `fullsend scan input`, which nothing in this repo or `fullsend-ai/agents` invokes. See #6506 (decision), #6522 (build constraints) |

### Sandbox tool hooks (per runtime)

How the shared PostToolUse chain reaches each runtime — the three sanitizer rows below share it:

- **Claude Code:** `posttool_chain.py` runs on successful tool calls (#6357). On failed calls the same driver runs on `PostToolUseFailure`, where it detects, logs to `findings.jsonl` and warns the agent via `additionalContext` — Claude Code does not let a hook rewrite a failed call's output.
- **Pi:** `fullsend-hooks.js` `tool_result` → the same `posttool_chain.py` (sent `tool_response` + `tool_result`; `updatedToolOutput` applied to the result the model sees). pi's `tool_result` fires for failed calls too, so those are sanitized as well.
- **Codex:** `fullsend-codex-hook.py` (a `PostToolUse` handler in `hooks.json`) → the same `posttool_chain.py`. codex's `PostToolUse` fires for a command that exited non-zero as well, so failed calls are covered without a second phase — but **the rewrite cannot be applied**: codex accepts only `additionalContext` and `updatedMCPToolOutput` there, so the chain's `updatedToolOutput` is dropped and the model is warned that the output would have been redacted. A canary block still withholds the output entirely, because a codex `PostToolUse` block replaces the tool result with the reason.

| Feature | Where it runs | Claude Code | OpenCode (stub) | Pi | Codex | Notes for future runtimes |
|---------|---------------|-------------|-----------------|----|-------|---------------------------|
| **Tirith** (Bash command scanning) | Sandbox PreToolUse hook | ✓ (loaded via `--settings`, #6358) | N/A — stub | ✓ via `fullsend-hooks.js` (pi `tool_call` → `HookPlan` PreToolUse scripts) | ✓ via `fullsend-codex-hook.py` (a `PreToolUse` handler per `HookPlan` group in `$CODEX_HOME/hooks.json`; the script's `exit 1` + `decision:block` is translated to **exit 2 + reason on stderr**, the only reliable block on codex) | `tirith_check.py`; harness `security.sandbox_hooks.tirith`; fails open on missing binary/timeout unless `TIRITH_REQUIRED=1` |
| **SSRF pre-tool** | Sandbox PreToolUse hook | ✓ (`hooks-loaded.feature` runs under the dummy runtime, which installs no hooks — it guards the sandbox egress boundary; the hook itself is unit-tested) | N/A — stub | ✓ via `fullsend-hooks.js` | ✓ via `fullsend-codex-hook.py`; the group's `WebFetch` tool is dropped from the matcher because codex has no such tool — it fetches through the shell, which the `Bash` matcher already covers | `ssrf_pretool.py`; default on; when DNS resolution fails for a host on the `FULLSEND_EGRESS_ALLOWLIST`, the hook defers to the L7 egress proxy instead of failing closed — all other SSRF checks (scheme, hostname blocklist, IP blocklist, DNS rebinding) still apply. On GitLab CI, the forge host is also covered by the auto-generated `fullsend-gitlab-forge` provider profile (#6615), which opens the L7 proxy for the forge API |
| **Canary token detection** | Sandbox Pre/PostToolUse hooks | pre ✓; post-tool via `posttool_chain.py` on successful calls (`tool_response` / `updatedToolOutput`, #6357); failed calls: the same driver on `PostToolUseFailure` (detect + halt; the error text cannot be rewritten) | N/A — stub | ✓ pre via `tool_call`; post via `tool_result` (sequential chain, block withholds the result) | ✓ pre and post via `fullsend-codex-hook.py`. A post-tool hit **blocks**, which on codex replaces the tool result with the reason, so the flagged output never reaches the model — stronger than Claude Code. In the **artifacts** the canary is only pattern-redacted if it happens to look like a credential, not withheld: artifact filtering is redaction, not the chain. It does **not halt the session**: `continue:false` is unsupported on PreToolUse and inert on PostToolUse, so codex has no hook-driven stop ([ADR 0100](../ADRs/0100-codex-sandbox-hooks.md)) | `canary_pretool.py` / `canary_posttool.py`; both inert unless `FULLSEND_CANARY_TOKEN` is set — on codex that value is read at bootstrap and re-exported after `.env`, so an agent cannot clear it for a later iteration. Post-tool canary is an in-process chain stage so it cannot race sanitizer rewrites. Claude Code `decision:block` does not hide PostToolUse output, so the chain also redacts the token in `updatedToolOutput` |
| **Secret redaction** | Sandbox PostToolUse hook | ✓ shared chain (above) | N/A — stub | ✓ shared chain (above) | Model context: detect + warn only — codex cannot rewrite a built-in tool's output, so the redaction is dropped and the model gets an `additionalContext` warning instead. Artifacts: `output.jsonl` and the extracted rollout are filtered through the same Go `security.SecretRedactor` the progress parsers use, because codex's artifacts keep raw tool output where Claude Code's stream carries the post-hook result | `secret_redact_posttool.py` |
| **Unicode normalization** | Sandbox PostToolUse hook | ✓ shared chain (above) | N/A — stub | ✓ shared chain (above) | Detect + log only — same reason as secret redaction (shared chain, above) | `unicode_posttool.py` |
| **Context suppression** | Sandbox PostToolUse hook | ✓ shared chain (above) | N/A — stub | ✓ shared chain (above) | ✗ — it exists only to rewrite output, which codex does not allow for built-in tools; the stage still runs and its findings are logged, but nothing is condensed | `context_suppress_posttool.py` |
| **Tool allowlist** | Sandbox PreToolUse hook | opt-in; ✓ when enabled | N/A — stub | ✓ `tool_allowlist_pretool.py` via `tool_call` (names translated to Claude vocabulary first, #608) plus pi's native `--tools` from the agent `tools:` and the `Bash(a,b)` first-token allowlist enforced in the extension | opt-in; ✓ when enabled, via `fullsend-codex-hook.py` (names translated to Claude vocabulary first, #608). Unlike pi there is **no native allowlist**: codex has no `--tools`, so an agent's `tools:` frontmatter is documentation unless the harness enables this hook, and the `Bash(a,b)` first-token allowlist is recorded in the manifest but **not wired**. Note `apply_patch` arrives as `Edit`, so an agent allowlisted only for `Write` is blocked | `tool_allowlist_pretool.py`; requires `FULLSEND_TOOL_ALLOWLIST` (fail-closed when unset) |

### Bootstrap and artifacts (per runtime)

| Feature | Where it runs | Claude Code | OpenCode (stub) | Pi | Codex | Notes for future runtimes |
|---------|---------------|-------------|-----------------|----|-------|---------------------------|
| **Sandbox tool hooks wiring** | `SandboxHooksBootstrap` type assert in `Bootstrap` | ✓ scripts at `claude-config/hooks/`, wiring at `claude-config/hooks.json` via `--settings` (#6358) | ✗ — `Bootstrap` is a stub; must wire `security.HookPlan` via OpenCode plugin hooks | ✓ see [pi: hook adapter contract](#hook-adapter-contract) — scripts under `/sandbox/pi-config/hooks/`, `HookPlan` in `fullsend-manifest.json`, embedded `fullsend-hooks.js` loaded with `-e` under `--no-extensions`; fail-closed on a missing/altered adapter (exit 97) or a manifest without a hook plan (exit -1) | ✓ see [codex: hook adapter contract](#codex-hook-adapter-contract) — scripts under `/sandbox/codex-config/hooks/`, wiring in `$CODEX_HOME/hooks.json` rendered from `HookPlan`, embedded `fullsend-codex-hook.py` loaded with `--dangerously-bypass-hook-trust`; fail-closed on a missing or altered adapter, auth script or wiring (exit 97), on a tampered `config.toml` (exit 98) and on a manifest without a hook plan (exit -1) | Hook scripts and wiring plan are runtime-neutral (see [Sandbox hook contract](#sandbox-hook-contract)); a runtime that ignores `SandboxHooksBootstrap` installs **no** sandbox tool hooks — say so explicitly here |
| **Transcript / debug artifacts** | `TranscriptHandler` (+ optional `DebugLogNamer`) | ✓ (stream-json, `claude-debug.log`) | No-op — see #1935 | ✓ session JSONL under `PI_CODING_AGENT_SESSION_DIR` (`ExtractTranscripts`), `pi-debug.log` (`DebugLogNamer`; pi's stderr when `--debug` is set), `ParseTranscriptFile` judges the tee'd `--mode json` stream and session files | ✓ rollout session JSONL under `$CODEX_HOME/sessions/` (`ExtractTranscripts`; plain `.jsonl` only, each validated as a rollout envelope and redacted before it is kept), `codex-debug.log` (`DebugLogNamer`; codex has no debug flag, so Run exports `RUST_LOG` and captures stderr — redacted too), `ParseTranscriptFile` judges the tee'd `exec --json` stream | Format-specific; not shared across runtimes. Debug-log filename defaults to `agent-debug.log` unless the runtime implements `DebugLogNamer` |

### Fail modes

Harness `security.fail_mode` controls whether critical findings **block** the run (`closed`, default) or **warn** and continue (`open`). This applies to host scans, sandbox `scan context`, and the host-side runtime content scan alike.

## Runtime interface contract

| Interface | Responsibility |
|-----------|----------------|
| `runtime.Runtime` | Name, config dir, env exports, bootstrap, run loop, per-iteration cleanup, user processes cleanup |
| `runtime.BootstrapInput` | Portable agent name/path, skill dirs, and declared plugins (`Plugins() []PluginInput` — name, host path, format kind, env, pi args; ADR 0094) to upload. A runtime loads the entries whose `Kind` it reads and must warn and skip the rest, never drop them silently |
| `runtime.SandboxHooksBootstrap` | Optional `BootstrapInput` extension — runtime-neutral sandbox tool hook config (`security.SandboxHookConfig`); every runtime should honour it |
| `runtime.TranscriptHandler` | Extract transcripts/debug logs; parse errors for CI annotations |
| `runtime.DebugLogNamer` | Optional — names the per-iteration debug-log artifact (default `agent-debug.log`) |
| `runtime.ContextBridger` | Optional — runtime auto-loads only `CLAUDE.md`, so the runner injects a `CLAUDE.md`→`AGENTS.md` pointer (Claude Code: yes; runtimes that read `AGENTS.md` natively: omit) |
| `runtime.OpenAICredentialSeeder` | Optional — for a runtime that reaches OpenAI through the run-scoped provider (ADR 0092): names the in-sandbox credential file the agent re-reads per request and the `sh` fragment that writes the current placeholder into it, so a mid-run credential refresh reaches the running process. Omit when the runtime has no OpenAI path; a stub returning `""` means "no re-seed" and the provider is still created and refreshed |

**Per-iteration cleanup contract.** When a validation retry reuses the sandbox, the runner calls `ClearIterationArtifacts` before the next iteration. Every runtime runs the shared `clearStrayProcesses` sweep first (it terminates the processes the previous iteration left running as the sandbox user, sparing the exec channel and the `sandbox.KeepAliveCommand` main process), then deletes the iteration's output, sessions and debug log. A failed sweep is reported as a warning and never fails the iteration. The runner holds its sandbox lock (`withSandboxLock` in `internal/cli/run.go`) across the call so the credential refreshers' uploads are never killed mid-write.

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
| pi | `ARG PI_VERSION` (npm, `--ignore-scripts`, Renovate-tracked; `TestSandboxImagePinsAreRenovateTracked`) | `parsePiStream` fixtures (`internal/runtime/testdata/pi/regen.sh`); the extension compatibility notes in [pi runtime internals](#pi-runtime-internals-6464); `piGoogleVertexModels` in `pi_bootstrap.go`, which is the bundled `google-vertex` catalog verbatim and is what the `Agent` tool accepts as a Gemini id |
| `pi-anthropic-vertex` | `ARG PI_ANTHROPIC_VERTEX_VERSION` + tarball SHA256, under `/usr/local/share/pi-extensions/anthropic-vertex` | the extension's own CI matrix (pinned `PI_VERSION` + latest pi); no compat file or SDK override to check |
| `pi-xai-vertex` | `ARG PI_XAI_VERTEX_VERSION` + tarball SHA256, under `/usr/local/share/pi-extensions/xai-vertex` | its `peerDependencies` floor (it mirrors no pi internals; declared only — `--omit=peer` never installs it); `piXaiVertexModels` in `pi_bootstrap.go`, the ids it registers and what the `Agent` tool accepts as a Grok id |
| Codex | `ARG CODEX_VERSION` (npm, Renovate-tracked; `TestSandboxImagePinsAreRenovateTracked`). The OpenShell base image already installs `@openai/codex` under the same npm prefix (`/usr`, binary `/usr/bin/codex`) — 0.117.0 on the base pinned today — so the pinned install replaces it in place. `/usr/local/bin/codex` is then symlinked at the npm install and the build fails unless `codex --version` reports `codex-cli <pin>`: nothing shadows the pin there today, and the symlink plus assertion make sure nothing can start to, the way the base image's Claude Code did in #6612. `TestSandboxImageCodexPinWins` guards the step | the codex stream parser fixtures (#6920); the JSONL event and hook wire shapes the codex runtime depends on |

The run log's `Agent: <model> (vX.Y.Z)` line is the ground truth for which Claude Code version served a run; the Containerfile pin is a claim, the assertion at build time is what makes it true.

The image also creates each runtime's config directory when the binary refuses to start without one: `CODEX_HOME` (`sandbox.SandboxCodexConfig`) is created owned by the sandbox user and baked as an `ENV` default, so an ad-hoc `codex` invocation from Bash behaves like the runtime's own `EnvExports()`. `TestSandboxImageCodexDefaults` keeps the path in the image and the Go constant in step (the pi equivalent is `TestSandboxImagePiDefaults`).

## Egress binary identity per runtime

The inference profiles (`profiles/fullsend-vertex-ai.yaml` in this repo and in fullsend-ai/agents; `profiles/fullsend-openai.yaml`, which has no fleet copy — the runner imports the embedded scaffold) carry a `binaries:` list. OpenShell's OPA (`sandbox-policy.rego`, `binary_allowed`) matches each glob against the kernel-resolved `/proc/<pid>/exe` of the process that opens the connection **or of any of its ancestors**. That gives two kinds of runtime:

- **Wrapped by node** (pi, codex): `**/node` admits them through the ancestor, so a pin bump cannot take them off the allowlist unless the wrapper disappears.
- **Exec'd directly** (Claude Code): `claude` on PATH is a symlink to the npm package's `bin/claude.exe`, so the connecting process has no wrapper ancestor and only a glob on the real file name admits it. A renamed native binary breaks every run of that runtime on its first model call with `API Error: Error code policy_denied`, 0 tokens, and the only explanation is the gateway's `NET:OPEN … DENIED … binary '…' not allowed in policy '_provider_<name>'` line (#6971).

`TestScaffoldProfilesAllowRuntimeBinaries` (`internal/cli/runtime_binaries_test.go`) fails for any selectable runtime without a mapping and pins the ones below.

| Runtime | Process that opens the connection | Profile glob(s) | Evidence |
|---------|-----------------------------------|-----------------|----------|
| Claude Code | `bin/claude.exe` in the npm package — 2.1.2xx's `install.cjs` places the native binary there ("Always write to bin/claude.exe"); the Containerfile installs it *with* scripts so that runs | `**/claude`, `**/claude.exe` | gateway deny log in #6971; `npm view @anthropic-ai/claude-code@<pin> bin` |
| pi | `node` (`bin = dist/bundle/cli.js`; no native network path in the package) | `**/node` | `images/sandbox/Containerfile`, `npm view @earendil-works/pi-coding-agent@<pin> bin` |
| Codex | `vendor/<triple>/bin/codex`, spawned by the npm launcher `bin/codex.js` under node; codex also spawns `codex-code-mode-host` (default-enabled in 0.152.1), covered by ancestor matching, not by name | `**/node` (ancestor), `**/codex` (the process) | `npm pack --dry-run "@openai/codex@<pin>-linux-x64"` |
| OpenCode (stub) | exec'd directly, like Claude Code: `opencode-ai` ships a stub `bin/opencode.exe` that `postinstall.mjs` replaces with `opencode-linux-x64/bin/opencode`; with `--ignore-scripts` the stub stays | to be decided against the Containerfile install (`**/opencode.exe` or `**/opencode`); the test fails the moment `opencode` joins `config.ValidRuntimes` until a mapping exists | `npm view opencode-ai bin optionalDependencies`, `postinstall.mjs` |

## Sandbox workspace layout

The sandbox has two key directories that map to Claude Code's config levels (plus a runner-owned config directory per additional runtime, e.g. `pi-config/` for pi and `codex-config/` for codex):

```
/sandbox/
├── pi-config/                       ← PI_CODING_AGENT_DIR (pi runtime; written by PiRuntime.Bootstrap)
│   ├── APPEND_SYSTEM.md                Agent definition body (appended to pi's default system prompt)
│   ├── settings.json                   defaultProjectTrust: never, defaultTools (all built-ins), quietStartup, retry/compaction on
│   ├── skills/<name>/SKILL.md          Harness skills (pi's native skill discovery)
│   ├── extensions/<name>/              Declared harness extensions (ADR 0094; loaded with -e, tree-hash preflight)
│   ├── hooks/*.py                      Security hook scripts (same files as claude-config/hooks/)
│   ├── fullsend-hooks.js               Hook adapter extension (loaded with -e; --no-extensions otherwise)
│   ├── fullsend-agent.js               Agent/Task sub-agent extension (loaded with -e when the tool is enabled)
│   ├── fullsend-manifest.json          Agent tools/allowlist, HookPlan, agent block, pi version — read by Run and the extensions
│   ├── subagents/usage.jsonl           One line per sub-agent (model, usage, stop reason) — folded into metrics.json
│   └── sessions/                       PI_CODING_AGENT_SESSION_DIR (session JSONL → transcripts)
│       └── agent-<seq>/                One sub-agent's session (→ transcripts/<agent>-sub<seq>-…)
│
├── codex-config/                    ← CODEX_HOME (codex runtime)
│                                       Created by the sandbox image (codex will not start without it);
│                                       populated by CodexRuntime.Bootstrap (#6920, follow-up PR)
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
│              fullsend wins, repo shadowed with warning)│
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

## Dummy-playback runtime

The `dummy-playback` runtime replays canned agent results from an ordered
playlist without LLM inference (behaviour tests only). It reads
`.fullsend/results/playlist.yaml`, serves the current entry's `result.json`
to the sandbox output directory, copies companion files, and advances the
playlist position.

### Playlist format

```yaml
current: 1          # 1-indexed position; the runtime serves results[current-1]
results:
  - triage/round-1  # directory name under .fullsend/results/
  - code/round-1
  - review/round-1
```

Each entry names a subdirectory of `.fullsend/results/`. The subdirectory must
contain a `result.json` (the agent result to replay). Any other files are
treated as companion files:

- Files under a `repo/` subdirectory are placed relative to the target repo
  checkout inside the sandbox, simulating code changes the agent would have
  made.
- All other files are placed relative to the sandbox workspace root.

### Playback comment tracking

When a `.fullsend/playback-comment-url` file exists, the runtime reads the
current playlist position from a forge comment (via `gh api` or `glab api`)
instead of the local `playlist.yaml`. After serving a result, it updates the
comment with the new position. The file format is `<cli>\n<api-path>` (e.g.
`gh\n/repos/owner/repo/issues/comments/123`). Legacy single-line files
default to `gh`.

### Security

- Path traversal: entry names are validated to stay within the results
  directory.
- Argument injection: the `playback-comment-url` API path must start with `/`
  to prevent flag injection into `gh`/`glab` CLI calls.
- Context propagation: forge API calls use `context.WithTimeout` to prevent
  indefinite blocking.

## pi runtime internals (#6464)

User-facing pi behaviour is in [Pi](../runtimes/pi.md). This section keeps the
verification provenance: what was checked against pi's source, on which version, and what must be
re-checked on a `PI_VERSION` or extension bump.

One iteration, end to end — the amber decision is what makes "hooks enabled" enforceable, since pi silently skips a missing `-e` extension:

```mermaid
flowchart TB
  B["Bootstrap (once per run)\nagent .md → APPEND_SYSTEM.md + --tools\nhook scripts + manifest + adapter\npi --version preflight"]
  G{"shell guard, before .env (command -p):\nadapter present and SHA-256 = embedded copy?\nAgent extension SHA-256 = embedded copy?\nmanifest present and SHA-256 = the one Bootstrap wrote?"}
  X["exit 97 (adapter)\nexit 94 (Agent extension)\nexit 95 (manifest)\npi never starts unhooked\n(Run refuses earlier, exit -1,\nif the manifest has no hook plan)"]
  E["source .env\nunset ANTHROPIC_*\npin GOOGLE_CLOUD_PROJECT\nre-check manifest SHA-256"]
  P["pi --print --mode json --no-approve\n--no-extensions [-e vertex, on Vertex] -e hooks [-e agent]\n--tools ... --model ... #lt;/dev/null"]
  C["child pi per Agent call\nprompt on stdin · own session dir\nSIGTERM then SIGKILL\nusage.jsonl folded into metrics"]
  S["parsePiStream\nexactly one ResultEvent\nexit 0 + stream error ⇒ run fails"]
  A["artifacts\noutput.jsonl · transcripts/ (incl. sub#lt;seq#gt;)\nmetrics.json (runtime: pi, per_model_usage)"]
  B --> G
  G -- no --> X
  G -- yes --> E --> P --> S --> A
  P -. "Agent/Task tool" .-> C
  C -. "final message" .-> P
  classDef guard fill:#fbf0d6,stroke:#d98e04,color:#1b2230;
  classDef bad fill:#f8e1de,stroke:#c0392b,color:#1b2230;
  classDef opt fill:#e3e9fb,stroke:#2d5be3,color:#1b2230;
  class G guard;
  class X bad;
  class B,P,S,C opt;
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
- **The one blocker found:** print mode reads a non-TTY stdin to EOF before the first prompt, even with a positional message (`main.ts` `readPipedStdin`), so an exec that keeps stdin open with no writer hangs pi — `Run` therefore appends `</dev/null` to the `pi` invocation; an idle upstream pipe then exits immediately (verified: open pipe without the redirect → killed by timeout; with it → proceeds). The same behaviour is what the `Agent` extension *relies* on for its children: it writes the prompt to stdin and closes it, because argv cannot carry a prompt that starts with `-`, `--` or `@`, or one past the kernel's argv limit (see [Pi sub-agents](#pi-sub-agents-the-agent-tool-contract)).

### Process and exit codes

- **Hardening levers in use**
  - `Run` executes `pi --print --mode json --no-approve --no-extensions --no-prompt-templates --no-themes --session-dir /sandbox/pi-config/sessions [-e /usr/local/share/pi-extensions/anthropic-vertex | -e /usr/local/share/pi-extensions/xai-vertex] [-e /sandbox/pi-config/fullsend-hooks.js] [-e /sandbox/pi-config/fullsend-agent.js] [--tools ...] --model <provider/id> --thinking <effort|high> '<RunParams.Prompt, default "Run the agent task">' </dev/null [2>>/sandbox/workspace/pi-debug.log]`.
  - `settings.json` sets `defaultProjectTrust: never` (repo-owned `.pi/` never loaded) and `defaultTools: [read, bash, edit, write, grep, find, ls]` — pi alone activates only the first four; `--tools`, when emitted, replaces the set. The `grep` and `find` tools shell out to `rg` and `fd` (pi's `utils/tools-manager.ts`), which the sandbox image ships because `PI_OFFLINE=1` and the egress policy both stop pi's own GitHub-release download.
  - `PI_OFFLINE=1`/`PI_TELEMETRY=0`/`PI_SKIP_VERSION_CHECK=1`/`JITI_FS_CACHE=false` come from `EnvExports`. Context files (`AGENTS.md`) and skills stay on — they are the harness's own inputs.
  - The loader environment (`JITI_FS_CACHE`, the `JITI_*`/`NODE_*` `unset` after `.env`) is pinned for the same reason the extension tree is hashed — see [Pi extensions](#pi-extensions-adr-0094).
  - `PI_CODING_AGENT_DIR/extensions/` is arbitrary TypeScript loaded at startup and the config dir is not a permission boundary, which is why only the explicit `-e` paths load (at most one vendored provider extension plus the hook adapter).
  - Right after `.env` is sourced, `Run` re-exports `EnvExports()` (`PI_CODING_AGENT_DIR`, the session dir, the offline switches) so a rewritten `.env` cannot relocate pi's config directory.
  - For the built-in `openai` provider (`openai/<id>`, [ADR 0092](../ADRs/0092-openai-wif-credential-delivery.md)) no extension loads and no `--api-key` is passed; `Run` instead seeds `auth.json` under `PI_CODING_AGENT_DIR` with the placeholder the sandbox environment carries for `OPENAI_API_KEY` (`PiOpenAIAuthSeed`, before `.env` is sourced), because pi re-reads that file on every revision change and resolves the key per request. The runner re-runs the same seed through `sandbox exec` after each credential refresh, which is what lets a running iteration follow a refresh on OpenShell 0.0.115, where a revision-scoped placeholder stays pinned to its generation and the unrevisioned alias is refused (`--api-key` would outrank the file and pin the iteration).
  - `Run` unsets `OPENAI_BASE_URL`/`AZURE_OPENAI_API_KEY`/`OPENAI_API_KEY`/`NODE_OPTIONS`/`NODE_PATH` after `.env`, and fails the iteration with exit 1 when the environment holds anything but a gateway placeholder at seed time (before `.env`).
  - A config-dir integrity guard exits 98 when `models.json` exists or `auth.json` is anything but pi's own `{}` or exactly the seeded placeholder entry — `models.json` is the only way to move the provider's base URL, a redirect to another allowed REST host is the placeholder-leak path ADR 0025 describes, and pi itself writes an empty `auth.json` on every start so only its content counts. The guard runs before the agent-writable `.env` is sourced and again after it behind `unset -f test command grep tr sed printf`, whether or not hooks are enabled.
  - When the agent's `tools:` allow sub-agents, `-e /sandbox/pi-config/fullsend-agent.js` is appended after the hook adapter and two more pre-`.env` guards join the block: the Agent extension must be byte-identical to the embedded copy (exit 94, its own code so `Run` names that extension rather than the hook adapter), and `fullsend-manifest.json` must be byte-identical to the one `Bootstrap` wrote (exit 95, re-checked after `.env` behind `unset -f test [ command sha256sum cut` — `[` is in that list because the guard uses it, and the sandbox's `sh` is dash, which accepts `unset -f [`). The digest is then exported as `FULLSEND_PI_MANIFEST_SHA256` so `fullsend-hooks.js` can re-verify the manifest whenever it loads, including inside a sub-agent started later in the iteration — see [Pi sub-agents](#pi-sub-agents-the-agent-tool-contract). Children are launched by that extension, not by `Run`: same flag set, prompt on stdin, own session dir, no `Agent` tool of their own.
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
- The pi-format entries of the harness's `plugins:` list ([ADR 0094](../ADRs/0094-pi-extensions-are-harness-resources.md)) get the same treatment — uploaded at `Bootstrap`, re-hashed and preflighted before every iteration, appended with `-e` after the adapter so the sandbox hooks see every call first ([Pi extensions](#pi-extensions-adr-0094)). The adapter itself grants them nothing: it logs a tool name that is neither a pi built-in nor a Claude-vocabulary name once at first use when the manifest lists extensions, and that is all. No hook is skipped for an extension tool, and an org running the optional `tool_allowlist_pretool.py` lists extension tool names in `FULLSEND_TOOL_ALLOWLIST` the way `mcp__*` names already are.
- Edit inputs keep pi's `edits[]` shape, with `path` mirrored to `file_path` and the first `oldText`/`newText` pair mirrored to `old_string`/`new_string`; no shipped script reads the latter.
- pi fires `tool_result` for failed calls too, so — unlike Claude Code's `PostToolUse` — errored tool output is sanitized as well.
- The `tool_call`/`tool_result` event shapes the adapter relies on (`toolName`, `input`, `content`, `isError`; `{block, reason}` and `{content, isError}` replies) are verified against pi v0.84.2 `src/extensions/types.ts`/`runner.ts`; the lifecycle run is the live confirmation.

### Pi extensions (ADR 0094)

The pi-format entries of the harness's `plugins:` list
([ADR 0094](../ADRs/0094-pi-extensions-are-harness-resources.md)). The walkthrough a harness author
follows is [Pi § Plugins (pi extensions)](../runtimes/pi.md#plugins-pi-extensions); this section
keeps the rules' *reasons* and the provenance behind them (verified against the pinned pi build,
0.84.4 unless noted).

**Which entries are pi's is decided per directory.** `internal/pluginformat` is the leaf package
both `internal/harness` and `internal/runtime` read: `Detect` (local directory) and `DetectTree`
(fetched tree) return `KindClaude` for a `plugin.json` bundle and `KindPi` for a directory pi's
loader resolves. `plugin.json` is checked first and settles it — a Claude plugin that bundles a Node
MCP server ships a `package.json` whose `main` resolves, which would otherwise satisfy pi's rule as
well. Everything below is the `KindPi` half of that verdict.

**Validation mirrors pi's own loader.** `internal/pluginformat/pi.go` re-implements
`-e <dir>` resolution so a harness never ships a directory pi would refuse — or, worse, accept and
load nothing from. pi's rule is not the obvious one:

- A `package.json` carrying a **`pi` object** decides the verdict alone: `readPiManifest` returns
  non-null and pi loads only what `pi.extensions` names, never `index.*` and never `main`. So
  `{"pi": {}}`, `{"pi": {"skills": [...]}}` and a `pi.extensions` whose entries all fail to resolve
  load *nothing*, silently, with pi exiting 0 — the run simply has no extension and no message says
  so. Validation refuses all three shapes, which is the only place that failure can be made loud.
- Without a `pi` object, an `extensions/`, `prompts/`, `skills/` or `themes/` entry switches pi to
  *package* layout: it collects those resource directories and stops treating `index.js` as an entry
  point. pi probes the name with `existsSync`, so a plain **file** called `skills` has the same
  effect (verified on 0.84.4 — `index.js` stopped loading). Only then do `main` and
  `index.js`/`index.ts`/`index.mjs`/`index.cjs` apply.
- There is deliberately no discovery branch beyond that: a bare top-level `tools.js`, or an
  `index.js` one directory down, is not an entry point — pi exits 1 with
  `Failed to load extension ... Cannot find module`. A directory reached *through* a `pi.extensions`
  entry resolves more loosely (`extensionAutoEntries`): its own entry points, else any top-level
  `.js`/`.ts` file, else an immediate subdirectory that itself resolves — and on that path only
  `index.ts`/`index.js` count, not `.mjs`/`.cjs`. The two rules are different code paths in pi and
  must not be collapsed.
- **Containment.** pi resolves `pi.extensions` and `main` against the package root with **no**
  containment check, so `../evil.js` would load code the sandbox preflight never hashes (verified on
  0.84.4). Every listed entry is checked, not just the first that exists, and the check repeats one
  level down: a `pi.extensions` entry naming a subdirectory sends pi to *that* `package.json`, whose
  own entries are resolved against it with the same absence of a check. That nested problem is
  returned to the caller rather than swallowed as "does not load".
- **BOM.** `readPiManifest` strips a UTF-8 byte-order mark before parsing and `encoding/json` does
  not, so validation strips it too — otherwise an editor that wrote one would hide the `pi` object
  and send the verdict down the `index.js` branch pi never takes.
- **Globs.** pi's `hasGlobPattern` is `s.includes("*") || s.includes("?")`, so a bracket-only entry
  such as `[ab].js` is a literal file name to pi, not a pattern, and is treated literally here. Real
  globs go through Node's `globSync`, which expands braces and crosses separators on `**` — neither
  of which `path.Match` can express — so a pattern containing `**` or `{}` is accepted unevaluated
  rather than guessed at. A wrong refusal would block a harness pi would have loaded; the accepting
  direction is harmless, because the tree hash still covers whatever ends up loading.
- **`!` entries** are pi's *disable* form: they remove an entry other patterns brought in and can
  never contribute one. A `pi.extensions` made only of `!` patterns is refused; `["*.js",
  "!main.js"]` is accepted because `*.js` matches, even though pi would then disable the only match.
  Mirroring pi exactly matters more here than second-guessing it.
- **Tree admissibility** (`ExtensionEntryProblem`) is one definition shared by validation, the tree
  hash and the injection scan: regular files and directories only, and no name containing a newline,
  carriage return or backslash. GNU `sha256sum` escapes all three and prefixes the line with `\`,
  which the Go side does not mirror, so the host and sandbox implementations could not agree on such
  a name. Symlinks are refused because pi *follows* them when resolving an entry point while the
  sandbox-side `find . ! -type f ! -type d` probe prints nothing — a symlink left in the verdict
  would be a way to swap an extension's code without moving its hash. Forge-fetched trees cannot
  carry symlinks anyway, so nothing legitimate is lost; the extension *root* may still be one, since
  cache paths are named symlinks into the content-addressed store and callers `EvalSymlinks` before
  walking. The whole tree is walked — `node_modules` and dotted directories included — so a planted
  symlink is named at validation rather than failing anonymously at Bootstrap; only the entry-point
  *listing* skips those directories, which cannot hold an entry point pi would resolve.
- **Source and naming.** `npm:`/`git:`/`ssh:` sources and `..` segments are rejected: pi would
  install `npm:`/`git:` sources from the network at startup, which the sandbox cannot do. A URL
  entry follows the `skills:` rule — a forge `/tree/` directory pinned with `#sha256=` — and is
  format-checked after `resolve.Resolve` has fetched it, so it is held to exactly the same rules as
  a local path. Names are limited to `a-z A-Z 0-9 _ -`; duplicate basenames are refused because
  `sandbox.UploadDir` replaces its destination wholesale and one entry would silently drop the
  other; and `pluginformat.PiReservedExtensionNames` (`fullsend-hooks`, `anthropic-vertex`,
  `xai-vertex`) is refused for a pi-format entry because an upload under one of those would shadow
  runner-owned code.
- **`env` and `pi:` are code-family options.** They are valid on an entry a runtime loads as code;
  on a Claude plugin they would be silently dropped, so `ValidateFilesExist` refuses them there
  rather than accepting configuration that does nothing.
- **Scan limits.** Extensions take the same injection scan as `skills:`/`plugins:`/`scripts:`, over
  every text file including `node_modules`. Files over 1 MiB are noted on stderr and skipped, and a
  tree over 20 000 files is refused in either `fail_mode` — scanning a vendored dependency graph is
  the cost that would make the gate unusable, not a finding. Treat scan output as a prompt to look:
  the heuristics run over third-party JavaScript and prose, so minified bundles and README examples
  produce false positives.

Validation runs wherever the harness is loaded. The syntax checks (path shape, duplicates, `env`
key syntax, `pi.args`) are in `Harness.Validate`, which touches no disk; the format verdict and the
checks that depend on it are in `ValidateFilesExist`, which runs after URL entries have been
fetched to local paths — `fullsend run`'s "File validation failed" step. `fullsend lock` and base
composition apply the same rule to a fetched tree through `pluginformat.DetectTree`.

**Upload and the tree-hash preflight.** `Bootstrap` uploads each directory to
`/sandbox/pi-config/extensions/<name>/` — a runner-owned path pi does not auto-discover — and
records name, sandbox path, tree hash, `pi.args` and `env` in `fullsend-manifest.json` (whose
`extensions` field keeps its name: it is pi's own vocabulary, not the harness key's). `Run` re-hashes
the *host* directory (`piExtensionTreeHash`, one definition implemented in Go and as a POSIX
`find | LC_ALL=C sort | sha256sum` pipeline, equivalence-tested under `sh` and `dash`) and emits a
preflight in the same pre-`.env` block as the hook-adapter guard, exiting 96 when a sandbox copy is
missing or differs. The expected hash is never read back from the manifest: the manifest lives in
the agent-writable config dir, so a value read from it could be rewritten together with the
extension between iterations. The hash covers regular files **and** the directory set, because pi
reacts to directory *names* — an added empty `skills/` would silently turn the extension into a
package pi loads nothing from. Declared extensions are appended with `-e` after the provider
extension and the adapter, so `tool_call` handlers still reach the sandbox hooks first: pi runs them
in `-e` order and the first `block` wins.

**The loader environment is pinned too, because a hash over the source only binds what the loader
reads.**

- `JITI_FS_CACHE=false` comes from `EnvExports`. It is jiti's option, not pi's: pi imports every
  `-e` module through jiti and passes no `fsCache`, so jiti resolves it from `JITI_FS_CACHE`, then
  `JITI_CACHE`, then `true`, and writes transpiled bodies next to the module that created the jiti
  instance (`<pi>/dist/bundle/chunks/node_modules/.cache/jiti` in the published package) or, when
  that directory does not exist, to `$TMPDIR/jiti` — the image ships no such directory, so it is
  `/tmp/jiti`, agent-writable and persistent across iterations. jiti accepts a cache entry on a
  ` /* v9-<hash of the source> */` trailer alone, so a body rewritten with that trailer intact
  executes while the source file is untouched: a path around **both** the extension tree-hash
  preflight and the hook adapter's SHA-256 check, neither of which can see it. Disabling the cache
  makes jiti ignore a planted entry and create no cache directory at all
  (`internal/runtime/testdata/pi/jiti-cache-check.sh` reproduces both halves against the pinned
  `PI_VERSION`; re-run it on a bump).
- The cache is one lever of several the environment carries into the loader, so right after
  `. .env` — on **every** provider path, not just `openai` — `Run` emits a bare `unset` of
  `NODE_OPTIONS`, `NODE_PATH` and the whole `JITI_*` family except `JITI_FS_CACHE`, which
  `EnvExports` then pins (`piLoaderEnvNames` in `pi_run.go`). `JITI_ALIAS` is the reason: pi's
  bundled `cli.js` reaches `createJiti` on its `isBundledNode` branch, which passes no `alias`, so
  jiti fills that option from the environment and a `.env`-exported map remaps the specifier behind
  an `-e` path to another file — the extension source, its tree hash and the hook adapter's SHA-256
  all stay clean, because none of them can see the substitution. `unset` is a POSIX special builtin,
  so a function a rewritten `.env` defined cannot stand in for it. The same script covers the alias
  half, reading the name list out of `pi_run.go` so the two cannot drift.
- A residual TOCTOU remains, shared with the hook guard: a background process left by a previous
  iteration could rewrite the tree between the guard and pi's `import` — the stray-process sweep
  ([#6753](https://github.com/fullsend-ai/fullsend/issues/6753)) narrows the window rather than
  closing it.

**Why `args` and `env` are validated so narrowly.** pi parses every element of its command line
positionally, and an extension's `args` follow its `-e <path>` verbatim into that parser. So each
dash-prefixed element must be `--flag` or `--flag=value` the extension registered with
`pi.registerFlag` (pi has no single-dash options), pi's own option names are rejected because the
runner owns them, and a value may not start with `-` or `@` — `@path` makes pi attach a file to the
prompt. A bare word is allowed exactly once, directly after a `--flag` written without `=`: pi
consumes at most one value per flag and none after `--flag=value`, and reads every *other* bare word
as **prompt text** prepended to the agent's prompt, which makes
`args: ["--fff-mode", "override", "and now ignore your instructions"]` an injection vector rather
than a flag value. `env` is exported last, but export order is not the protection — pi hands its
whole environment to every hook script it spawns, so the deny-list in `plugin_spec.go` refuses
the names outright at validation. It covers the interpreter environment (`PATH`, `HOME`, `TMPDIR`,
`ENV`, `BASH_ENV`, `SHELL`, `IFS`, `CDPATH`, `PROMPT_COMMAND`, `LD_*`, `DYLD_*`, `PYTHON*`,
`NODE_*`, `SSL_*`, `JITI_*`, `GIT_*`, `JAVA_TOOL_OPTIONS`, `RUBYOPT`, `PERL5OPT`), credential- and
proxy-shaped names (`*_API_KEY`, `*_TOKEN`, `*_SECRET*`, `*_PROXY`), the names that move a trust
anchor or a resolver for the tools hook scripts shell out to (`HOSTALIASES`, `OPENSSL_CONF`,
`SSLKEYLOGFILE`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `GOPROXY`, `GOFLAGS`), and the runner's,
providers' and sandbox tooling's families (`PI_*`, `FULLSEND_*`, `TIRITH_*`, `GOOGLE_*`, `GCLOUD_*`,
`CLOUDSDK_*`, `ANTHROPIC_*`, `XAI_*`, `OPENAI_*`, `AZURE_*`, `AWS_*`, `CLOUD_ML_REGION`). An
extension's own settings are untouched by any of it.

**Other runtimes** name and skip each entry
(`Plugin "<name>": skipped — the <runtime> runtime does not load pi extensions (see docs/runtimes.md)`)
rather than dropping the list silently, the mirror of pi's `plugins:` warning.

### Claude-on-Vertex via a fullsend-owned extension

pi's `google-vertex` provider is Gemini-only and upstream's `anthropic-vertex` provider (earendil-works/pi#5262) is still an open PR, so the sandbox image vendors [`fullsend-ai/pi-anthropic-vertex`](https://github.com/fullsend-ai/pi-anthropic-vertex) under `/usr/local/share/pi-extensions/anthropic-vertex`, pinned by tag + tarball SHA256 (`PI_ANTHROPIC_VERTEX_VERSION`/`_SHA256`). pi never auto-loads it; `Run` passes it with `-e` for the `anthropic-vertex` provider (`runtime.piVertexExtensionPath`).

**How it works.** It registers provider `anthropic-vertex` with pi's own Claude catalog and pi's own Anthropic transport. A `fetch` wrapper rewrites each request the way the Vertex SDK does: the model id moves into the URL path, `anthropic_version` goes into the body, and the Google token becomes an `authorization: Bearer` header. No `@anthropic-ai/*` dependency, no mirrored pi internals. It replaced twoGiants/pi-anthropic-vertex in #7019, which copied pi's `streamSimple` helpers, pinned the Anthropic SDK underneath a cast, and hard-failed `claude-opus-5` from pi 0.84.3.

**Project, region, and credentials:**

| | Resolution | Note |
|---|---|---|
| Project | `GOOGLE_CLOUD_PROJECT`, then `GCLOUD_PROJECT`, then `ANTHROPIC_VERTEX_PROJECT_ID`, then `GOOGLE_CLOUD_PROJECT_ID` | `Run` pins `GOOGLE_CLOUD_PROJECT` to `ANTHROPIC_VERTEX_PROJECT_ID` when that is set, so pi cannot diverge from Claude Code |
| Region | `CLOUD_ML_REGION`, then `GOOGLE_CLOUD_LOCATION`, default `us-east5` | Interpolated into the endpoint host (`{region}-aiplatform.googleapis.com`, or the `global`/`us`/`eu` hosts) |
| Auth | `google-auth-library` reading `GOOGLE_APPLICATION_CREDENTIALS` | In CI: the WIF `external_account` config the runner delivers via `host_files` (ADR 0025), whose OIDC token file the runner refreshes every 4 minutes — the same path Claude Code uses, under the same `*.googleapis.com` egress allowlist |

**Credential hygiene — keep the unset.** The extension reads no `ANTHROPIC_*` variable except `ANTHROPIC_VERTEX_PROJECT_ID`, but pi's built-in `anthropic` provider runs in the same process and resolves `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` from the environment. So `Run` (and `childEnv` in `fullsend-agent.js` for sub-agents) still unsets `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_BASE_URL`/`ANTHROPIC_VERTEX_BASE_URL` for the `anthropic-vertex` provider and keeps them for a direct `anthropic` provider. `ANTHROPIC_OAUTH_TOKEN` is not scrubbed yet (#7029).

**On a `PI_VERSION` or extension bump.** The extension's own CI matrix runs its suite against fullsend's pinned `PI_VERSION` and the latest pi, so a green tag is the compatibility check — there is no `sync/compat.json` and no SDK override. Order: move the matrix's older entry to the new pin, then bump here. Two things the matrix cannot see: a compat key a new pi adds reaches Vertex only if the extension's `VERTEX_COMPAT_KEYS` allowlist names it (in v0.1.0 an undecided key is dropped silently; from v0.1.1 the extension's own build fails on it), and Vertex may reject a request shape a new pi introduces — smoke an adaptive and a non-adaptive model on a bump and override in `PI_CODING_AGENT_DIR/models.json` rather than patching the extension.

Replace with the upstream provider once #5262 ships in a pinned release.

### Grok-on-Vertex via a fullsend-owned extension

pi's built-in `xai` provider targets xAI's native API (`api.x.ai`, needs `XAI_API_KEY`) and `google-vertex` is Gemini-only, so neither reaches Grok on Vertex, which speaks the OpenAI-completions protocol. The sandbox image vendors [`fullsend-ai/pi-xai-vertex`](https://github.com/fullsend-ai/pi-xai-vertex) (MIT) under `/usr/local/share/pi-extensions/xai-vertex`, pinned by tag + tarball SHA256 (`PI_XAI_VERTEX_VERSION`/`_SHA256`, Renovate-tracked like the Anthropic one) and registering provider `xai-vertex` with model `xai/grok-4.6`.

- **No mirrored internals.** Like the Anthropic extension it registers a public pi API (`openAICompletionsApi()`) and lets pi do streaming, tools and usage — so there is no `sync/compat.json` drift to re-check on a `PI_VERSION` bump; confirm its `peerDependencies` floor instead (declared only — it installs with `--omit=peer` too, so nothing enforces it at build time).
- **Auth** is ambient ADC through `google-auth-library` reading `GOOGLE_APPLICATION_CREDENTIALS`, the same WIF `external_account` path as Claude-on-Vertex and under the same `*.googleapis.com` egress allowlist, so no new credential plumbing.
- **Endpoint** is fixed to the **global** location (`/locations/global/endpoints/openapi`) because Vertex serves this model only there — regional endpoints answer `FAILED_PRECONDITION` — so `CLOUD_ML_REGION`/`GOOGLE_CLOUD_LOCATION` are deliberately ignored for this provider.
- **`Run`** passes it with `-e` (`runtime.piXaiVertexExtensionPath`), unsets `XAI_API_KEY` after sourcing `.env` so pi's built-in `xai` provider cannot shadow it, and defaults `XAI_VERTEX_PROJECT_ID` to `ANTHROPIC_VERTEX_PROJECT_ID` (then `GOOGLE_CLOUD_PROJECT`) **only when the runner has not set it**, so the fleet's Vertex project is the default without becoming a ceiling. Each Vertex provider resolves its own project variable, so one pi process can serve Grok, Claude and Gemini from different GCP projects; overriding an explicit value would collapse that and leave no way to point Grok at a project where it is actually enabled in Model Garden (the call then fails 403 `PERMISSION_DENIED`, and the extension does not warn because it did have a project — just the wrong one).
- **Model spec.** pi sends `Model.id` on the wire verbatim and Vertex wants the publisher-qualified name, so the id keeps its slash and the canonical spec is the three-segment `xai-vertex/xai/grok-4.6`. `translatePiModel` normalises the short `xai/...` form and a bare id under `FULLSEND_PI_PROVIDER=xai-vertex` to that form, case-insensitively — matching the gate, which compares the prefix `piModelProvider` folded to lower case — because a spec that escapes normalisation reaches pi's built-in `xai` provider with `XAI_API_KEY` still set. A harness (or an `agents:` entry's `model:`) can name the three-segment spec directly, or select this provider with a bare `model:` plus `FULLSEND_PI_PROVIDER`.

### Binary present but unhooked

The pinned `pi` CLI and the vendored Vertex extensions ship in every sandbox image (so Bootstrap/Run work targets a reviewed version), so an agent on another runtime can invoke `pi -e /usr/local/share/pi-extensions/anthropic-vertex` ad hoc from Bash with none of that runtime's tool hooks — and in a Claude-on-Vertex sandbox the ADC credentials and project id it needs are already in the environment, so that is a working nested agent, not an inert binary. This is the same class of exposure as any interpreter the agent can run (`python`, `node`, `curl` with the same ADC token): the sandbox tool hooks are defense-in-depth and only see the top-level tool call (ADR 0090); the boundary remains the OpenShell sandbox, its L7 egress allowlist and the credential placeholders, which a nested `pi` cannot escape either. The image bakes `PI_OFFLINE=1`/`PI_TELEMETRY=0` and the runner-owned config paths as defaults; treat the `N/A — stub` matrix cells as "not wired", not "cannot run".

### Not yet exercised

`runtime: pi` is selectable, but no fleet lifecycle run on Vertex has been recorded yet:

- The Vertex model ids and pi's own first-party `compat` flags (the extension re-points pi's catalog rather than copying flags onto it) have not been exercised against Vertex on a fleet run — smoke an adaptive and a non-adaptive model first; override with `--model`/`FULLSEND_MODEL` if an id is rejected.
- Parser fixtures are hand-authored to the v0.84.2 wire docs — re-record with `internal/runtime/testdata/pi/regen.sh` once a run exists; `extension_error` events are not mapped.
- The behaviour scenario `features/runtime/pi.feature` (a real haiku run on Vertex of a minimal tool-using agent, asserting `metrics.json` `runtime: pi`, a `toolCall` in the pi session transcript and token usage) is gated on `BEHAVIOUR_CAPABILITIES=runtime-pi` until `fullsend-sandbox:latest` carries `PI_VERSION`; `features/triage/triage.feature` asserts the runtime selected from the repo config on every run.
- Pilot on a disposable test repository with `triage`/`prioritize` before `code`/`fix` ([ADR 0044](../ADRs/0044-deprecate-per-org-installation-mode.md): per-repo is the only supported installation model). `review`/`retro` now run their real sub-agent roster through the runner-owned `Agent` tool (see [pi: Pi sub-agents](#pi-sub-agents-the-agent-tool-contract)), but that roster has been exercised locally, not on a fleet lifecycle run — watch the wall clock on the first one, and note that children default to `--thinking medium` for that reason. pi has no sub-agent tool or `agents/*.md` concept in core — fullsend supplies one as an extension; the bundled example extension (`examples/extensions/subagent/`) spawns children without the hook adapter, Vertex provider, `--no-approve` or a session dir, which is why fullsend ships its own.

### Pi sub-agents: the Agent tool contract

`Bootstrap` writes an `agent` block into `fullsend-manifest.json` and uploads the embedded
`fullsend-agent.js`, which registers Claude Code's `Agent` tool (and its legacy alias `Task`) so the
fleet's sub-agent skills dispatch unchanged. The block is written — and `Run` loads the extension
with `-e`, after the hook adapter — when the agent definition has no `tools:` frontmatter or lists
`Agent`/`Task`; otherwise the older "no sub-agent tool" runtime note is appended instead.

| Manifest field | Meaning |
|---|---|
| `enabled` | The tool is registered. `Run` also gates the `-e` and the integrity guard on it |
| `piBin` | The `pi` binary children run, resolved in the sandbox at `Bootstrap` (`command -v pi`); `pi` when the probe found nothing |
| `sessionsDir` | Parent's session dir; child `<seq>` gets `--session-dir <sessionsDir>/agent-<seq>` |
| `extensions` | The `-e` list every child gets, in order: the vendored provider extensions actually present in the image (`test -d` at `Bootstrap`) then the hook adapter when security is on. Never `fullsend-agent.js` itself |
| `extensionDigests` | SHA-256 (hex) of each `extensions` entry `Bootstrap` itself wrote under the config dir — today only the hook adapter, and the same bytes the launch guard checks. Re-hashed before every dispatch. The vendored provider extensions are absent on purpose: root-owned and read-only in the image, outside anything the agent can write. Omitted when hooks are off, because then nothing in the list came from `Bootstrap` |
| `models` | `default` (the agent's model, translated) plus the Claude aliases on the Anthropic Vertex provider. The extension resolves a call's `model` through this table and **rejects** anything it cannot serve |
| `providerModels` | Per provider a run can serve with no `models` entry, the model ids it can serve: `google-vertex` verbatim from the catalog the pinned pi bundles (`@earendil-works/pi-ai` `dist/providers/data/google-vertex.json`), and `xai-vertex` from the ids the vendored extension registers (`xai/grok-4.6`, the publisher-qualified wire form). A `provider/id` call is accepted only when the full spec is in `models`, is the parent's own spec, or is listed here — a provider prefix alone is not enough, or an invented id would reach the API. A Grok spec is normalized to `xai-vertex/xai/<id>` first, exactly as `normalizeXaiVertexModel` does for the parent, and then goes through this same set |
| `thinking` | Children's `--thinking`; `FULLSEND_PI_SUBAGENT_THINKING` (validated) else `medium` |
| `tools` / `exploreTools` | The child `--tools` allowlist: the parent's built-ins minus `Agent`/`Task`, or — for `subagent_type: Explore` — the read-only set **intersected with** the parent's, so a child never reaches past its parent. An empty result becomes `--no-builtin-tools`, as it does for the parent |
| `maxConcurrent`, `timeoutSeconds` | Semaphore and per-child deadline (4, 900 s) |
| `usageFile` | Where the extension appends one line per child; `Run` folds it into `RunMetrics` and `ExtractTranscripts` downloads it |

- Children run with `FULLSEND_SUBAGENT_DEPTH=1` and the extension refuses to register when that
  variable is already set, so recursion is impossible even if the `-e` list were tampered with.
- The extension's code is SHA-256-checked against the embedded copy in the same pre-`.env` guard
  block as the hook adapter, under its own exit 94 so `Run` can name the artifact that actually
  failed, and **the manifest it reads is checked the same way**
  (exit 95, both before and after `.env` — `.env` is agent-writable and could rewrite it between
  the two, the same reason `piOpenAIConfigGuard` runs twice). Those are launch checks, so the
  manifest is re-verified twice more during the iteration: by the Agent extension against the
  bytes it loaded, immediately before each dispatch, and by `fullsend-hooks.js` against
  `FULLSEND_PI_MANIFEST_SHA256` whenever it loads — which in a sub-agent is long after the
  launch guard ran. Both are security controls: the
  manifest names the binary children run, their `-e` list, their tool allowlists and where their
  usage is recorded, and the config dir is agent-writable between iterations — so an unchecked
  manifest would let an agent with `Write` but no `Bash` (which cannot start `pi` itself) launch
  children of its own choosing.
- **The hook adapter gets the same per-dispatch treatment**, through `agent.extensionDigests`.
  The launch guard checks it once; a parent with `write` can replace it mid-iteration, and a
  rewritten adapter simply drops its own `FULLSEND_PI_MANIFEST_SHA256` check, so verifying only
  the manifest would leave the adapter unprotected and every later child unhooked. `Bootstrap`
  therefore records the SHA-256 of each `agent.extensions` entry it wrote under the config dir
  (today just the adapter, the same bytes `piHooksGuard` checks) into the manifest, and the Agent
  extension re-hashes them alongside the manifest before every dispatch, inside the slot the
  dispatch holds, refusing with `hook adapter changed since load; refusing to dispatch`. The map
  is part of the manifest, so the manifest digest covers it. The vendored provider extensions
  under `/usr/local/share/pi-extensions` need no digest — the image installs them root-owned and
  read-only outside the config dir.
- **`Bootstrap` and `Run` must run in one process** for any of the manifest guards to exist. The
  digest lives in a package-level map keyed by sandbox name (`piManifestHashes`), recorded by
  `Bootstrap` and read by `Run`; the CLI's `run` path does both in one process. A caller that
  bootstrapped elsewhere gets no entry, and then neither the shell guard nor the
  `FULLSEND_PI_MANIFEST_SHA256` export is emitted — silently, because failing closed there would
  break every such caller. The per-dispatch checks above are unaffected: they compare against
  bytes the extension read itself.
- The hook adapter's `*` PreToolUse groups therefore see `Agent` calls, and the manifest's
  `hooks.toolNames` carries `Agent`/`Task` verbatim (they are already Claude vocabulary). Claude
  Code runs the same hooks on its own `Agent` tool.
- The prompt is delivered on the child's **stdin**, never as a positional argument. Unterminated,
  pi's argv parser reads a leading `-` as an unknown option (a startup error), a leading `--` as an
  unknown flag that swallows the next word and a leading `@` as a file argument. pi *does* honour a
  `--` end-of-options terminator (`dist/cli/args.js`), but that is not a way out: after it an
  `@`-prefixed positional is still taken as a file argument, and argv is capped by the kernel
  either way (`spawn E2BIG` past ~128 KiB), which a context package exceeds. In `--print` mode pi
  reads a non-TTY stdin to EOF and uses it as the initial message (`dist/main.js`
  `readPipedStdin`, `dist/cli/initial-message.js` `buildInitialMessage`, 0.84.4).
- Children get `--append-system-prompt` with a short sub-agent role note. They share the parent's
  `PI_CODING_AGENT_DIR`, and pi only discovers `APPEND_SYSTEM.md` there when no
  `--append-system-prompt` was passed (`dist/core/resource-loader.js`), so without the flag every
  child would inherit the parent's orchestrator persona — including its "make several `Agent` calls
  in one message" dispatch note, for a tool it does not have.
- Children are spawned with the parent's process group (**not** `detached`), so a killed parent
  cannot leave them spending tokens. A child that times out, is aborted, or is caught by
  `session_shutdown` gets **`SIGTERM` first**, escalated to `SIGKILL` after a 3 s grace. `SIGTERM`
  is what makes the child clean up after itself: pi's own bash tool spawns commands `detached` in
  their own process groups and reaps them from its `SIGTERM` handler before exiting 143
  (`dist/modes/print-mode.js` → `killTrackedDetachedChildren`). Signalling the child's process group
  with `SIGKILL` instead would leave those grandchildren running.
- The child's environment is rebuilt per resolved provider with the same rules `buildPiRunCommand`
  applies to the parent (`ANTHROPIC_*` cleared and `GOOGLE_CLOUD_PROJECT` pinned for
  `anthropic-vertex`; `XAI_API_KEY` cleared and `XAI_VERTEX_PROJECT_ID` pinned for `xai-vertex`).
  The shell hygiene only ever matched the *parent's* provider, so without this a Claude child under
  a Grok parent would run with a stray `ANTHROPIC_API_KEY`.
- The usage file is **consumed as it is read**: the in-sandbox command renames it to
  `<usageFile>.read` before printing it (`piSubagentUsageReadCommand`), so folding it into
  `RunMetrics` is idempotent per iteration and a retry whose `ClearIterationArtifacts` failed cannot
  count the same children twice. The read is capped at 1 MiB (~4k child records) because the file
  sits under the agent-reachable config dir, and a truncated tail surfaces as a skipped malformed
  line rather than an unbounded allocation in the runner.
- **`.env` is the same trust class here as everywhere else.** A rewritten
  `/sandbox/workspace/.env` that exports `FULLSEND_SUBAGENT_DEPTH` disables the tool for that
  iteration: the extension reads it as "this process is a child" and registers nothing. That is the
  same exposure as any other `.env`-settable knob, and it fails in the safe direction. What `.env`
  cannot do is change the manifest, the Agent extension or the hook adapter — all three are
  SHA-256-checked before `.env` is sourced, the manifest again after it, and the manifest and the
  adapter again before every dispatch.

### OpenAI via Workload Identity Federation

GPT models run on pi's built-in `openai` provider with no OpenAI credential in the sandbox ([ADR 0092](../ADRs/0092-openai-wif-credential-delivery.md)): `fullsend run` exchanges the job's GitHub OIDC token for a ≤1 h access token (`internal/inference/openaiwif`, or `OPENAI_API_KEY` from the runner environment for local runs), imports the `fullsend-openai` profile from the scaffold embedded in the binary (profiles are deliberately not layered into `.fullsend/profiles`, whose wholesale import would replace the fleet's canonical profiles), creates a run-scoped OpenShell provider `openai-<sandbox suffix>` of that type carrying it, and deletes that provider when the run ends; the `fullsend-openai` profile allows only `POST /v1/responses` on `api.openai.com`, and only for `**/node` (pi) and `**/codex` (the codex runtime's native binary, #6920). The provider is created only for a run whose selected runtime will actually call OpenAI (`runtime.NeedsOpenAIProvider`); a harness may declare it for every runtime, and a run that does not need it says so and skips it. A per-provider refresher re-exchanges a fresh assertion shortly before `expires_in` and hot-updates the provider (a static key only has its expiry pushed out), and is stopped before the deferred cleanup deletes — or, when a kept sandbox still references it, expires in place — the provider.

### Other clouds

pi ships native `amazon-bedrock` (SDK default credential chain, incl. `AWS_WEB_IDENTITY_TOKEN_FILE`) and `azure-openai-responses` (`api-key` only, no Entra ID) providers; neither is wired into `Run`'s alias table, credential hygiene or the runner's OIDC refresh yet, and no egress profile allows their hosts. Follow-up tracked against #6464.

## codex runtime internals (#6920)

User-facing codex behaviour gets its own page with the runtime (`docs/runtimes/codex.md`, PR E),
alongside [Claude Code](../runtimes/claude.md) and [Pi](../runtimes/pi.md). This section keeps the
verification provenance: what was checked against codex's source, on which version, and what must be
re-checked on a `CODEX_VERSION` bump. The decisions are
[ADR 0099](../ADRs/0099-codex-agent-runtime.md) (credential delivery) and [ADR 0100](../ADRs/0100-codex-sandbox-hooks.md)
(sandbox hooks).

Everything below was read at tag `rust-v0.152.1`. Two of the findings are the reason the hook
adapter exists at all, because forwarding the scripts' own convention would fail **open**.

One iteration, end to end:

```mermaid
flowchart TB
  B["Bootstrap (once per run)\nagent .md → config.toml developer_instructions\nhooks.json + adapter + auth script\ncodex --version preflight"]
  G{"shell guards, before .env (command -p):\nadapter + auth script SHA-256 = embedded copy?\nconfig.toml still pins base_url + auth.command,\nno openai_base_url / env_key / [projects]?"}
  X["exit 97 / 98\ncodex never starts unhooked\nor pointed at another endpoint"]
  T["seed $CODEX_HOME/openai-token\nplaceholder shape or exit 1"]
  E["source .env\nre-pin CODEX_HOME\nunset OPENAI_* CODEX_API_KEY NODE_*\nre-run both guards"]
  P["codex exec --json --skip-git-repo-check\n--dangerously-bypass-approvals-and-sandbox\n[--dangerously-bypass-hook-trust]\n-c model_provider/approval_policy/sandbox_mode\nprompt on stdin"]
  S["parseCodexStream\nexactly one ResultEvent\nno terminal event ⇒ incomplete ⇒ run fails"]
  A["artifacts\noutput.jsonl · transcripts/ (rollouts)\nmetrics.json (runtime: codex)"]
  B --> G
  G -- no --> X
  G -- yes --> T --> E --> P --> S --> A
  classDef guard fill:#fbf0d6,stroke:#d98e04,color:#1b2230;
  classDef bad fill:#f8e1de,stroke:#c0392b,color:#1b2230;
  classDef opt fill:#e3e9fb,stroke:#2d5be3,color:#1b2230;
  class G guard;
  class X bad;
  class B,T,P,S opt;
```

### Posture

- **No permission system in use** — `codex exec` runs with `approval_policy = "never"` and
  `sandbox_mode = "danger-full-access"`, both also passed as `-c` overrides. The OpenShell sandbox,
  its L7 egress policy and the credential placeholders are the boundary ([ADR 0017](../ADRs/0017-credential-isolation-for-sandboxed-agents.md),
  [ADR 0025](../ADRs/0025-provider-credential-delivery-for-sandboxed-agents.md)); the hook adapter is defense in depth
  ([ADR 0090](../ADRs/0090-runtime-neutral-sandbox-hooks-contract.md)).
- **The project is never trusted.** No `[projects]` entry is written, so the target repo's own
  `.codex/` layer — settings, instructions and repo-authored hooks — is never loaded. This is
  codex's equivalent of pi's `defaultProjectTrust: "never"`.
- **Config layering.** The sandbox image bakes a root-owned managed `/etc/codex/config.toml`; the
  runner's `$CODEX_HOME/config.toml` layers above it, and the `-c` SessionFlags above that. Only
  the `-c` layer is beyond an agent's reach between iterations, which is why the security-relevant
  keys are passed there as well as written to the file.
- **Reads AGENTS.md natively** (cwd chain plus `$CODEX_HOME/AGENTS.md`) — so `CodexRuntime` does not
  implement `ContextBridger` and the runner injects no `CLAUDE.md` pointer.
- **Tool names**: the shell tool is already `Bash`; `apply_patch` covers Claude's `Write` and `Edit`
  and carries them as matcher aliases; `spawn_agent` carries `Agent`. `Read`, `Glob`, `Grep`,
  `WebFetch` and `WebSearch` have no codex tool — codex does that work through the shell, so the
  `Bash` groups already cover it.
- **Skills** come from `$CODEX_HOME/skills`, which `Bootstrap` populates. Codex also discovers a
  repo's `.agents/skills`; whether the untrusted-project setting suppresses that is an open item for
  the first fleet run.

### Process and exit codes

- `Run` executes `codex exec --json --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox
  [--dangerously-bypass-hook-trust] -C <repo> --model <id> -c model_provider=fullsend-openai
  -c approval_policy=never -c sandbox_mode=danger-full-access [-c model_reasoning_effort=<effort>]
  -o <workspace>/output/last-message.txt -` with the prompt piped to stdin.
- **The prompt is never on argv.** On a retry iteration it carries the previous failure's text
  (#1050/#6494), and argv is world-readable in the sandbox. `-` is codex's explicit
  read-from-stdin sentinel; the pipe closes when `printf` finishes, so there is none of the
  open-stdin hang pi needs `</dev/null` for.
- **`codex exec` has no `--debug` flag.** Its tracing goes to stderr at **error level by default** —
  unlike pi, a codex run is not silent there: the denied `GET /v1/models` and every hook block are
  logged — and `RUST_LOG` raises the level. Debug mode exports `RUST_LOG` and appends stderr to
  `codex-debug.log`; without it those error lines reach the runner console, since `Run` hands
  `os.Stderr` to the exec stream.
- **Model**: `openai/<id>` or a bare `<id>`, resolved through `EffectiveModel` — the same chain
  `NeedsOpenAIProvider` decides from, so the launch and the run-scoped provider decision cannot
  disagree about which model a run calls. The fallback is the **runner-held** copy of the agent
  definition's `model:`, not the manifest's: the manifest is agent-writable and carries no digest,
  so reading it there would let an agent move a validation retry onto a different model, and a
  different cost tier, than the run was authorised for. The **Claude model aliases (`opus`, `sonnet`, `haiku`,
  `fable`) deliberately do not apply to codex**, and codex does not consult the per-repo
  `models.aliases` overrides either, so a Claude alias can never resolve to a GPT model behind the
  operator's back. A Claude alias, any non-`openai/` prefix, or no model at all is an error naming
  both fixes (`FULLSEND_CODEX_MODEL=openai/<id>` for the repo, or `model: openai/<id>` on the
  agent's `agents:` entry or the harness) rather than a 404 mid-run.
- **Effort** maps 1:1 — every value `config.ValidEffortLevels()` accepts (`low`, `medium`, `high`,
  `xhigh`, `max`) is also a codex `ReasoningEffort`. An unset effort omits the override so the
  model's own default applies. `FallbackModels` is warned about and ignored; codex has no chain.
- **Exit code**: `codex exec` exits 0 on a failed turn *and* on an interrupted one, so the stream's
  verdict overrides it, exactly as for pi. Distinct guard exits: **97** (a runner-owned file is
  missing or no longer matches its embedded copy) and **98** (`config.toml` no longer pins the
  provider endpoint or its auth command, or trusts the project).
- **Cost is never reported** — the stream carries no cost field, so `total_cost_usd` stays 0. The
  model id in `metrics.json` comes from the run parameters, not the wire.

### Credential path

Codex's built-in `openai` provider reads `OPENAI_API_KEY` once at startup, and built-in provider ids
cannot be overridden, so it cannot follow the mid-run refresh a short-lived WIF token requires
([ADR 0092](../ADRs/0092-openai-wif-credential-delivery.md)). Instead:

- `config.toml` declares a custom provider `fullsend-openai` (`wire_api = "responses"`) whose
  `auth.command` is an absolute path to the runner-written `openai-token.sh`.
- Codex runs that command, uses its trimmed stdout verbatim as the bearer token, caches it for
  `refresh_interval_ms` (30 s here; codex's default is 300 s) and re-runs it on expiry and on the
  401 retry path. A non-zero exit or empty stdout **fails the request with no fallback to the
  environment**, which is what makes the path fail closed.
- The script prints the placeholder from `$CODEX_HOME/openai-token`, which `CodexRuntime`'s
  `OpenAICredentialSeeder` implementation seeds at iteration start and the runner re-seeds through
  `sandbox exec` after every refresh. Anything that is not a gateway placeholder fails the run
  rather than being forwarded, and the script never echoes the value it rejects.
- `supports_websockets` is left unset: custom providers default to false, which keeps codex on
  HTTP/SSE `POST /v1/responses` — the only thing the `fullsend-openai` egress profile allows.

### codex hook adapter contract

`Bootstrap` installs `security.HookFiles` under `/sandbox/codex-config/hooks/`, renders
`$CODEX_HOME/hooks.json` from `security.HookPlan`, and uploads the embedded
`fullsend-codex-hook.py` beside the scripts. Each plan group becomes one handler,
`python3 <adapter> <phase> <script...>`, so the scripts still run in plan order inside one process —
the ordering the PostToolUse chain depends on.

Matcher translation, per group:

| `HookGroup.Tools` | codex matcher | Note |
|---|---|---|
| `Bash` | `Bash` | codex's shell tool has the same name |
| `Write`, `Edit`, `MultiEdit` | `apply_patch` | the canonical name; `Write`/`Edit` would also match as aliases |
| `Agent`, `Task` | `spawn_agent` | not reachable in v1 — no sub-agent roster is wired |
| `Read`, `Glob`, `Grep`, `LS`, `WebFetch`, `WebSearch` | *dropped, with a note* | no codex tool; the `Bash` groups cover this work |
| `*` (`security.AllTools`) | *matcher key omitted* | an absent matcher matches every tool |
| `PostToolUseFailure` (any tools) | *not wired* | codex has no such event and does not need one — see below |

Tokens are joined with `|` and stay within `[A-Za-z0-9_|]`, which is the character set codex treats
as an **exact alternation** rather than a regex, so there is no anchoring question and no substring
match. A group whose tools all drop renders no handler rather than a matcher-less one, which would
silently widen it from a few tools to all of them.

Three codex behaviours are load-bearing, and the adapter exists because of the first two:

1. **A hook that exits anything but 0 or 2 is recorded as `Failed`, and a failed hook does not
   block.** The shared scripts block with `exit 1` plus `{"decision":"block","reason"}`, so
   forwarding them verbatim would make every PreToolUse hook advisory. The adapter translates a
   block to **exit 2 with the reason on stderr**. An exit 2 whose stderr is empty is *also* `Failed`,
   so the reason is never allowed to be empty.
2. **Only a synchronous handler can apply control effects.** A handler with `"async": true` still
   runs and still reports, but its block decision is discarded — so the rendered `hooks.json` never
   carries an `async` key at all, and `TestCodexHooksJSON_NeverAsync` asserts its absence.
3. **`PostToolUse` output is `deny_unknown_fields` and accepts only `additionalContext` and
   `updatedMCPToolOutput`.** The sanitizers' `updatedToolOutput` would make the hook `Failed`, so
   the adapter drops the rewrite and emits an `additionalContext` telling the model the output it is
   about to read contains content that would have been redacted and is untrusted.

Consequences of those, recorded in the matrix and [ADR 0100](../ADRs/0100-codex-sandbox-hooks.md):

- **No hook-driven session halt.** `continue: false` is rejected as unsupported on PreToolUse (so
  emitting it would fail the hook open) and, on PostToolUse, sets no stop the outcome carries. A
  canary hit therefore blocks — which on codex **replaces the tool result with the reason**, so the
  flagged output never reaches the model, stronger than Claude Code — but the run continues.
- **Failed commands are covered without a second phase.** A shell command that exits non-zero still
  produces a successful tool result as far as the registry is concerned, so `PostToolUse` fires for
  it. `HookPlan`'s `PostToolUseFailure` group therefore maps onto nothing, as it does for pi. The
  exception is a command that leaves a live PTY session behind: codex emits no `PostToolUse` payload
  for those, so a backgrounded command's output is not scanned.
- **A PreToolUse block reaches the model as a decline**, quoting the reason and the command, rather
  than as a tool failure.

### Where the guards' expected values come from

Everything under `CODEX_HOME` is agent-writable between iterations, so a guard is worth only as much
as the place its expected value comes from. There are two trustworthy places — this binary, and
**runner-held digests**, meaning digests the runner records outside the sandbox at Bootstrap and injects into the launch command at Run — and the manifest is not one of them:

| File | Anchor | Guard |
|---|---|---|
| hook adapter, auth script | **compile-time** — `go:embed`ed, so the digest is a literal in the run command | SHA-256, exit 97 |
| the shared hook scripts | **compile-time** for the bytes, **runner-held** for which names — Bootstrap records the name → digest map for the set the harness enabled | SHA-256 per filename, plus an exhaustive directory check, exit 97 |
| `config.toml`, `hooks.json` | **runner-held** — rendered per run, so Bootstrap records what it uploaded outside the sandbox and Run injects it into the launch command (`codex_integrity.go`) | whole-file SHA-256, exit 98 |
| `fullsend-manifest.json` | none — it is a file in the same agent-writable directory | information only; never authority |

The hook-script check makes codex **stricter than Claude Code and pi**, where the scripts stay
agent-writable between iterations with no integrity check at all. Each digest is bound to its
*filename*, and the directory must hold exactly those entries and nothing else:

- rewriting `tirith_check.py` to exit 0 fails — the obvious case;
- **overwriting `tirith_check.py` with `hook_io.py`'s bytes** also fails. It would otherwise pass a
  set-of-digests check while silently neutering Tirith, because `hook_io.py` is a library module
  with no `__main__`: it exits 0 with empty stdout, which the adapter reads as "allow";
- **a planted package directory** — `hooks/json/__init__.py` — fails. A `*.py` glob never saw it,
  and normal Python prepends a script's own directory to `sys.path`, so it would be imported when a
  hook script did `import json`;
- **a symlink to an allowed file** fails: `test -f` and `sha256sum` both follow one, `find` does not;
- deleting a script fails too, since the expected name is checked by name.

All five were reproduced under `/bin/sh` before the guard was rewritten, and each has a case in
`TestCodexHookScriptsGuard_Executes`.

That guard runs once, before codex starts. The agent then has the whole iteration — with full
sandbox access — to rewrite a script before the next tool call reaches it, a window Claude Code and
pi leave open too. On codex it is cheap to close: the run command exports the name → digest map into
the codex process's environment after `.env` and before launch, and a running process's environment
is not something the agent can reach, so **the adapter re-reads and re-verifies each script
immediately before spawning it** and blocks on a mismatch. A missing or malformed map is itself a
block, since the adapter only ever runs when the runner enabled hooks.

**PATH is pinned across `.env` for the same reason the interpreter is.** The hook scripts resolve
their tools by name — `tirith_check.py` runs a bare `tirith` — so a `.env` that prepends a directory
holding a fake `tirith` that exits 0 neuters the entire PreToolUse chain while every digest stays
green. Reproduced before the pin existed. `Run` captures `PATH` into a readonly variable before
sourcing `.env` and restores it afterwards, and unsets `LD_PRELOAD`, `LD_LIBRARY_PATH` and
`LD_AUDIT`, which would otherwise load code into any dynamically linked program the run starts —
codex's own binary, `tirith`, `git` — before its `main`. The adapter strips `LD_*` and `PYTHON*`
from the children's environment too, while passing the scripts' own configuration through.

**Why not record the config digest in the manifest.** It would be a digest in the same
agent-writable directory as the file it describes, so an agent could rewrite both and the guard would pass. That is
not theoretical: `projects."<repo>".trust_level = "trusted"` in `config.toml` makes codex load the
target repo's own `.codex/config.toml`, which then supplies `developer_instructions`, `model`, and —
under `--dangerously-bypass-hook-trust` — repo-authored hooks. Verified against 0.152.1: with the
line the repo layer applied, without it it did not. An earlier grep-based guard here looked for a
`[projects` header and missed the dotted-key spelling entirely, which is why the check is a
whole-file digest and not a pattern.

**What `-c` can and cannot pin.** A `-c` override beats the file for
`model_providers.<id>.base_url` and `.auth.command` (verified: a config naming an unreachable host
still reached `api.openai.com`), so both are passed as SessionFlags as well as covered by the digest.
There is **no such pin for project trust** — `-c projects={}` and a scalar
`-c projects."<repo>".trust_level="untrusted"` were both tried and neither overrides the file.

`Run` also refuses to start (exit -1) when security is enabled but the manifest carries no hook
plan, and decides whether hooks are expected from the runner's own signal rather than from the
manifest.

**The hook interpreter is pinned twice.** codex spawns a hook through the shell it inherits,
*after* the agent-writable `.env` has been sourced, so a bare `python3` in `hooks.json` would be
resolved through a PATH the agent controls — a poisoned interpreter running under the hash-pinned
adapter. Bootstrap resolves the absolute path in its preflight and renders it with `-I`, which also
ignores `PYTHONPATH` and the user site directory; `Run` unsets `PYTHONPATH`, `PYTHONHOME` and
`PYTHONSTARTUP` after `.env` for good measure.

**The hook scripts' own configuration is re-asserted the same way.** `appendHookEnv` writes
`TIRITH_FAIL_ON`, `TIRITH_REQUIRED` and `FULLSEND_EGRESS_ALLOWLIST` into the agent-writable
workspace `.env` at bootstrap, and the harness supplies `FULLSEND_CANARY_TOKEN` and
`FULLSEND_TOOL_ALLOWLIST` through the same file — so without re-assertion, iteration 1 could widen
the SSRF allowlist, clear `TIRITH_REQUIRED` or delete the canary token for iteration 2. `Run`
re-exports all five after `.env`. The first three come from `SandboxHookConfig`; the harness pair has
no typed runner-side copy, so Bootstrap reads it back from `.env` — which at that point is still
exactly what the runner wrote, since no agent iteration has run — and records it beside the
digests. Reading it in `Run` instead would read whatever the previous iteration left behind, which
is the exposure being closed.

The adapter then spawns each hook script the same way, which needs care in two directions. `-I`
alone breaks the scripts, because they import their siblings (`hook_io`, the sanitizer stages), so
the verified hooks directory is put back on `sys.path` explicitly — and **appended, not prepended**. Prepending would
place it ahead of the standard library and re-open the hole `-I` closes, since `import json` in a
hook script would find a planted `hooks/json/` first; verified both ways, and the sibling imports
resolve either way because no hook module shadows a stdlib name. Bootstrap's preflight asserts the
interpreter is at least Python 3.11 rather than assuming the `-I` behaviour that ordering rests on.

The children also run with **`-B`**, which `-I` does not imply and `PYTHONDONTWRITEBYTECODE` cannot
supply because `-E` makes it inert. Without it the first hook that imports a sibling writes
`hooks/__pycache__/*.pyc`, nothing clears the hooks directory between iterations, and the exhaustive
directory guard then refuses to start iteration 2 of a validation-loop retry — a self-inflicted
fail-closed lockout, reproduced as four `.pyc` files after a single chain run.

### What the local smoke proved

A real `codex exec --json` run against `api.openai.com` inside the pinned sandbox image
(`localhost/fullsend-sandbox:codex`, codex-cli 0.152.1, arm64), with the runner's own `config.toml`,
`hooks.json` and adapter in place:

| Question | Answer |
|---|---|
| Is `hooks.json` loaded with no `[hooks]` table in `config.toml`? | **Yes** — discovery reads the file independently, so no empty table is needed. Both phases fired. |
| Is a hook block reported to the model as *declined* rather than *failed*? | **Yes.** codex surfaces `Command blocked by PreToolUse hook: <reason>. Command: <cmd>`, and the agent's own summary was "blocked by a safety hook that prevents dotfile overwrites". |
| Does a PostToolUse block withhold the output? | **Yes** — a canary in a `cat` result never reached the model, which reported only that it had been blocked. |
| Are a repo's `.agents/skills` discovered with the project untrusted? | **Yes** — Claude Code parity (repo `.claude/skills` load there too); those SKILL.md files are covered by the host-side runtime content scan and the in-sandbox `scan context` pass. |
| Do codex's own bundled skills appear? | They did — `skill-installer`, `plugin-creator`, `imagegen`, `skill-creator`, `openai-docs` — until `[skills.bundled] enabled = false` was added, after which only the harness's skills and the image's own `github` skill remain. |
| Is `POST /v1/responses` the only request? | **No.** With a custom provider codex also issues `GET /v1/models` at startup (`codex_models_manager`), which fails to decode OpenAI's public catalog shape and is logged as a non-fatal ERROR. The `fullsend-openai` egress profile denies it, so expect that denial in a run's logs; the model call itself is `POST /v1/responses`. |
| Does `codex doctor` accept the rendered config? | Yes: `config.toml parse ok`, `default model provider fullsend-openai`, `OpenAI auth is not required for the active model provider`, and `Responses WebSocket is not enabled for the active provider` — the last confirming the HTTP/SSE path the egress profile requires. |

A full `fullsend run` through OpenShell then confirmed the credential path end to end
(`--runtime codex --model openai/gpt-5-mini --forge github`, arm64, 2026-09-02):

- **Egress, from the sandbox OCSF log.** `POST /v1/responses` **ALLOWED** ×3 under the run-scoped
  provider's policy; `GET /v1/models` **DENIED** ×2 by the L7 engine, 3 ms apart — one attempt and
  its immediate retry, not a loop, and the first allowed `POST` followed 100 ms later, so it delays
  nothing. codex also probes `chatgpt.com:443` and `api.github.com:443`, both **DENIED** at L4.
- **Hooks.** The agent's `ls -la` ran; its `~/.bashrc` overwrite was blocked by the PreToolUse chain
  (`Command blocked by PreToolUse hook: Tirith [HIGH] dotfile_overwrite …`) and the agent reported it
  as blocked rather than failed.
- **Artifacts.** `metrics.json` carried `"runtime": "codex"`, `"model": "gpt-5-mini"`,
  `"tool_calls": 1`, token usage, and `"total_cost_usd": 0`; the rollout transcript was extracted.
- **Credential rotation on a kept sandbox.** `openshell provider update` produced a new placeholder
  generation in the sandbox environment while the token file still held the old one; running
  `OpenAIAuthSeed()` through `sandbox exec` — what the runner's refresher does — rewrote the file to
  the new generation, and a further `codex exec` completed a turn on it. That is the whole reason
  the credential goes through a file rather than the environment.
- **Cleanup.** A run that fails at model resolution still tears down the sandbox and deletes the
  run-scoped provider: the cleanup is deferred from provider creation, well before `Run`. With
  `--keep-sandbox` the provider is expired in place instead and the run prints the
  `openshell provider delete` command — that is by design, not a leak.

Two artefacts of the run are worth knowing about:

- The `command_execution` item in the stream keeps the command's raw `aggregated_output` even when a
  PostToolUse hook blocked the result, and the rollout session file keeps the same output. Both are
  uploaded as run artifacts, so `Run` and `ExtractTranscripts` filter them through the shared
  `security.SecretRedactor` (`codex_redact.go`): the stream is redacted on the tee branch, so the
  parser still sees exactly what codex emitted and only the file on disk is masked.
- The sessions directory is agent-writable, so a file being there and ending in `.jsonl` is a claim
  rather than a fact. `ExtractTranscripts` collects regular `.jsonl` files only — never
  `.jsonl.zst`, since codex writes the running session uncompressed and a plaintext file merely
  *named* that shipped as an artifact the redactor then declined to rewrite — and **every line** of
  a candidate must parse as a rollout envelope (`session_meta`, `response_item`, `event_msg`, …),
  since checking only the first would let a file open with one genuine envelope and carry anything
  after it. Each is downloaded to a staging name, validated, redacted and only then renamed into
  place, so a crash cannot leave raw tool output at the path the artifact collector reads; reads are
  bounded, so a planted multi-gigabyte file is refused rather than read into the runner in full.
- `--dangerously-bypass-hook-trust` emits its warning as an `error`-type *item*, which the stream
  parser correctly treats as a warning rather than a run failure.
- A model the account cannot serve on the Responses API fails as five `error` reconnect events and a
  `turn.failed` carrying the 404, not as a startup error — which is why `Run` returns 1 from the
  stream verdict rather than trusting the exit code.

### Not yet exercised

`runtime: codex` is selectable per repo or per agent. Outstanding:

- **No default behaviour-CI coverage.** Codex has no Vertex path, so `features/runtime/codex-openai.feature`
  is gated on an OpenAI organization being mapped to the pool repositories, the same block
  `pi-openai.feature` sits behind. Until then the evidence is unit tests, recorded stream fixtures
  and local smoke runs.
- The rollout session files are archived as transcripts but not error-classified: only the tee'd
  `exec --json` capture yields a verdict, which is what the runner's exit-code override reads.
- Sub-agent rosters are not wired, so `review`/`retro` are unsupported; `Bootstrap` appends a runtime
  note telling the agent to execute sub-agent definitions itself, in order, as it does for pi.

### Re-check on a `CODEX_VERSION` bump

| What | Why it matters | Where |
|---|---|---|
| Hook exit-code semantics (0/2 blocking, everything else `Failed`) | the adapter's block translation is built on it; a change makes hooks fail open | `codex-rs/hooks/src/events/{pre,post}_tool_use.rs` |
| `async` and `can_apply_control_effects` | a synchronous-only rule that changed would alter what the wiring must omit | `codex-rs/hooks/src/engine/mod.rs` |
| `PostToolUse` output fields | if `updatedToolOutput` ever lands, the sanitizers can start redacting again | `codex-rs/hooks/src/schema.rs` |
| Hook payload shape (`tool_name`, `tool_input.command` as a string, `tool_response`) | the scripts read these keys directly | `codex-rs/core/src/{hook_runtime.rs,tools/context.rs}` |
| `auth.command` semantics (trimmed stdout, non-zero exit fails, no env fallback) | the whole credential path | `codex-rs/login/src/auth/external_bearer.rs` |
| `supports_websockets` default for custom providers | a true default would take traffic off `POST /v1/responses` and break the egress profile | `codex-rs/model-provider-info/src/lib.rs` |
| `[skills.bundled]` and skill discovery | the bundled skills are disabled by the runner-owned config; a renamed key would silently bring `skill-installer` and friends back into the agent's roster | `codex-rs/config/src/skills_config.rs` |
| The native binary's path inside the platform package (`vendor/<triple>/bin/codex` at 0.152.1) | the `fullsend-openai` profile names it as `**/codex`; the node ancestor still admits a renamed file, but the pin in `runtimeEgressBinaries` should follow the rename | `npm pack --dry-run "@openai/codex@<pin>-linux-x64"` |
| Whether a custom provider still issues `GET /v1/models` at startup | the `fullsend-openai` egress profile denies it; if the request ever became fatal or retried, it would delay or fail every first turn | `codex-rs/models-manager/` |
| `ConfigToml` keys and the `ReasoningEffort` enum | a renamed or removed key silently changes behaviour; `--strict-config` reports it | `codex-rs/config/src/config_toml.rs`, `codex-rs/protocol/src/openai_models.rs` |
| JSONL event structs and rollout file naming | the stream parser and transcript extraction | `codex-rs/exec/src/exec_events.rs`, `codex-rs/thread-store/src/local/helpers.rs` |
