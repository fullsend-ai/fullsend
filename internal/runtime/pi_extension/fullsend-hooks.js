// fullsend-hooks.js — pi extension that runs fullsend's runtime-neutral
// sandbox tool hook scripts (internal/security/hooks/*.py) from pi's
// tool_call / tool_result events (ADR 0090, docs/runtimes.md "Sandbox hook
// contract").
//
// Loaded explicitly by PiRuntime.Run with `-e` (pi runs with
// --no-extensions, so nothing else is discovered). Everything it needs is in
// the manifest PiRuntime.Bootstrap wrote (FULLSEND_PI_MANIFEST): the hook
// groups from security.HookPlan, the pi→Claude tool-name map, and the
// agent's Bash allowlist.
//
// Contract with the scripts (v1 and v2 — fullsend#6357):
//   stdin  {"tool_name", "tool_input", "tool_result", "tool_response"}
//   stdout PreToolUse: exit != 0 or {"decision":"block","reason"} blocks.
//          PostToolUse: {"hookSpecificOutput":{"updatedToolOutput": <text>}}
//          (v2) or {"tool_result": <text>} (v1) replaces the result text;
//          a block drops the result and surfaces the reason.
// The scripts are run sequentially in plan order, each seeing the previous
// one's output; a script that cannot be spawned blocks (fail closed) — the
// scripts own their individual fail-open cases (tirith).
import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";

export const DEFAULT_MANIFEST_PATH = "/sandbox/pi-config/fullsend-manifest.json";
const SCRIPT_TIMEOUT_MS = 60_000;
const SCRIPT_MAX_BUFFER = 64 * 1024 * 1024;
const LOG_PREFIX = "[fullsend-hooks]";

export function loadManifest(path = process.env.FULLSEND_PI_MANIFEST || DEFAULT_MANIFEST_PATH) {
  return JSON.parse(readFileSync(path, "utf8"));
}

// claudeToolName returns the name the hook scripts expect for a pi tool.
// Tools outside the map (extension tools) keep their pi name, so "*" groups
// still see them.
export function claudeToolName(manifest, piName) {
  return manifest?.hooks?.toolNames?.[piName] ?? piName;
}

// claudeToolInput mirrors pi's argument names onto Claude's where they
// differ (ssrf/tirith read `command`, which matches; read/write/edit use
// `file_path`). The pi keys are kept too so nothing is lost.
export function claudeToolInput(piName, input) {
  const src = input && typeof input === "object" ? input : {};
  switch (piName) {
    case "read":
    case "write":
      return typeof src.path === "string" ? { ...src, file_path: src.path } : { ...src };
    case "edit": {
      // pi batches edits ({edits:[{oldText,newText}]}); Claude's Edit has one
      // old_string/new_string pair. Mirror the first pair for scripts that
      // read Claude's names and keep edits[] for the rest.
      const out = typeof src.path === "string" ? { ...src, file_path: src.path } : { ...src };
      const first = Array.isArray(src.edits) ? src.edits[0] : undefined;
      if (first && typeof first === "object") {
        if (typeof first.oldText === "string") out.old_string = first.oldText;
        if (typeof first.newText === "string") out.new_string = first.newText;
      }
      return out;
    }
    default:
      return { ...src };
  }
}

// bashAllowlistViolation implements Claude's `Bash(a,b,c)` frontmatter
// restriction: every simple command in the line must start with an allowed
// program. Anything the first-token check cannot see through (command
// substitution, subshells, eval/exec/sh -c, PATH/loader overrides, a path
// to a binary) is refused rather than guessed. Heredoc bodies and `command`/
// `env`/quoted/escaped/variable first tokens are refused too — false
// positives only, which is the right side to err on in enforce mode.
// Every `VAR=value` prefix is refused: beyond the loader/shell variables
// (PATH, LD_*, BASH_ENV, ...), program-specific ones such as GH_PAGER or
// GIT_SSH_COMMAND make an allowlisted program spawn an arbitrary command,
// and a deny-list cannot enumerate them.
const ENV_PREFIX = /^[A-Za-z_][A-Za-z0-9_]*=/;
// Command separators. A lone `&` backgrounds a command, but `&` is also part
// of fd redirections (`2>&1`, `&>file`, `>&2`); only the former separates.
// `|&` is listed before `|` so the refusal names the program, not the `&`.
const SEPARATORS = /\r?\n|&&|\|\||;|\|&|\||(?<![<>|])&(?!>)/;

export function bashAllowlistViolation(command, allowlist) {
  if (!Array.isArray(allowlist) || allowlist.length === 0) return null;
  if (typeof command !== "string" || command.trim() === "") return "empty command";
  if (/`|\$\(|<\(|>\(/.test(command)) {
    return "command substitution is not allowed under a Bash allowlist";
  }
  // A lone `&`/`;` yields empty segments, which are skipped; `&&` and `||`
  // are matched before the single-character alternatives.
  const segments = command.split(SEPARATORS);
  for (const raw of segments) {
    const seg = raw.trim();
    if (seg === "") continue;
    const words = seg.split(/\s+/);
    if (ENV_PREFIX.test(words[0])) {
      return `"${words[0].split("=")[0]}=" prefix is not allowed under a Bash allowlist`;
    }
    const first = words[0];
    if (first.startsWith("(") || first.startsWith("{")) {
      return `subshell or group "${first}" is not allowed under a Bash allowlist`;
    }
    if (first.includes("/") && !allowlist.includes(first)) {
      return `"${first}" is a path, not an allowlisted program name`;
    }
    if (["eval", "exec", "sh", "bash", "source", ".", "command", "builtin", "env", "nohup", "nice", "timeout", "xargs", "time"].includes(first)) {
      return `"${first}" is not allowed under a Bash allowlist`;
    }
    if (!allowlist.includes(first)) {
      return `"${first}" is not in the Bash allowlist (${allowlist.join(", ")})`;
    }
  }
  return null;
}

function groupsFor(manifest, phase, claudeName) {
  return (manifest?.hooks?.groups ?? []).filter(
    (g) => g.phase === phase && Array.isArray(g.tools) && (g.tools.includes("*") || g.tools.includes(claudeName)),
  );
}

function parseJSON(text) {
  const trimmed = (text ?? "").trim();
  if (trimmed === "") return null;
  try {
    return JSON.parse(trimmed);
  } catch {
    return null;
  }
}

// runScript executes one hook script with the payload on stdin and returns
// the normalized verdict. `spawn` is injectable for tests.
export function runScript(manifest, script, payload, spawn = spawnSync) {
  const path = `${manifest.hooks.dir}/${script}`;
  const res = spawn("python3", [path], {
    input: JSON.stringify(payload),
    encoding: "utf8",
    env: process.env,
    timeout: SCRIPT_TIMEOUT_MS,
    maxBuffer: SCRIPT_MAX_BUFFER,
  });
  const output = parseJSON(res?.stdout);
  if (!res || res.error || res.status === null || res.status === undefined) {
    const why = res?.error?.message ?? (res?.signal ? `killed by ${res.signal}` : "no exit status");
    return { block: true, reason: `hook ${script} failed to run (fail closed): ${why}`, output };
  }
  const block = res.status !== 0 || output?.decision === "block";
  let reason;
  if (block) {
    reason = typeof output?.reason === "string" && output.reason !== "" ? output.reason : `hook ${script} exited ${res.status}`;
  }
  return { block, reason, output, status: res.status };
}

function joinText(content) {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .filter((b) => b && b.type === "text" && typeof b.text === "string")
    .map((b) => b.text)
    .join("\n");
}

// replaceText keeps non-text blocks and collapses the text blocks into one
// carrying the rewritten text.
function replaceText(content, text) {
  if (typeof content === "string" || !Array.isArray(content)) return [{ type: "text", text }];
  const others = content.filter((b) => !(b && b.type === "text"));
  return [{ type: "text", text }, ...others];
}

// createHooks builds the event handlers from a manifest. This extension is
// only loaded when the runner enabled security, so a manifest that is
// unreadable or carries no hook plan means the wiring was lost or tampered
// with: every tool call is blocked — running the agent with the hook wiring
// silently absent is the failure mode ADR 0090 forbids.
export function createHooks(manifest, { spawn = spawnSync, log = (m) => console.error(m) } = {}) {
  const wired = Boolean(manifest && manifest.hooks && Array.isArray(manifest.hooks.groups));
  const onToolCall = (event) => {
    if (!wired) {
      return { block: true, reason: `${LOG_PREFIX} hook manifest unavailable or has no hook plan; refusing all tool calls (fail closed)` };
    }
    const piName = event?.toolName ?? "";
    const input = event?.input ?? {};

    if (piName === "bash") {
      const violation = bashAllowlistViolation(input?.command, manifest.bashAllowlist);
      if (violation) {
        // Claude Code treats the agent's Bash(a,b) list as steering (ADR
        // 0027); blocking is opt-in so a pi run is never stricter than the
        // Claude run of the same agent unless asked.
        if (manifest.bashAllowlistMode === "enforce") {
          return { block: true, reason: `Bash allowlist: ${violation}` };
        }
        log(`${LOG_PREFIX} Bash allowlist (advisory): ${violation}`);
      }
    }

    const toolName = claudeToolName(manifest, piName);
    const payload = { tool_name: toolName, tool_input: claudeToolInput(piName, input) };
    for (const group of groupsFor(manifest, "PreToolUse", toolName)) {
      for (const script of group.scripts ?? []) {
        const verdict = runScript(manifest, script, payload, spawn);
        if (verdict.block) {
          log(`${LOG_PREFIX} ${script} blocked ${toolName}: ${verdict.reason}`);
          return { block: true, reason: verdict.reason };
        }
      }
    }
    return undefined;
  };

  const onToolResult = (event) => {
    if (!wired) return undefined;
    const piName = event?.toolName ?? "";
    const toolName = claudeToolName(manifest, piName);
    const groups = groupsFor(manifest, "PostToolUse", toolName);
    if (groups.length === 0) return undefined;

    const original = joinText(event?.content);
    let text = original;
    let blocked = null;
    chain: for (const group of groups) {
      for (const script of group.scripts ?? []) {
        const payload = {
          tool_name: toolName,
          tool_input: claudeToolInput(piName, event?.input ?? {}),
          tool_result: text,
          tool_response: text,
        };
        const verdict = runScript(manifest, script, payload, spawn);
        const updated = verdict.output?.hookSpecificOutput?.updatedToolOutput ?? verdict.output?.tool_result;
        if (typeof updated === "string") text = updated;
        if (verdict.block) {
          blocked = verdict.reason;
          log(`${LOG_PREFIX} ${script} blocked the ${toolName} result: ${verdict.reason}`);
          if (typeof updated !== "string") {
            text = `[fullsend hook ${script} withheld this tool result: ${verdict.reason}]`;
          }
          break chain;
        }
      }
    }
    if (blocked === null && text === original) return undefined;
    const patch = { content: replaceText(event?.content, text) };
    if (blocked !== null) patch.isError = true;
    return patch;
  };

  return { onToolCall, onToolResult };
}

export default function (pi) {
  let manifest = null;
  let loadError = null;
  try {
    manifest = loadManifest();
  } catch (err) {
    loadError = err;
  }
  const hooks = createHooks(manifest);

  pi.on("session_start", () => {
    if (loadError) {
      console.error(`${LOG_PREFIX} cannot read manifest: ${loadError.message}; all tool calls will be blocked`);
      return;
    }
    if (!manifest?.hooks) {
      console.error(`${LOG_PREFIX} manifest has no hook plan although the hook adapter was loaded; all tool calls will be blocked`);
      return;
    }
    const groups = manifest.hooks?.groups ?? [];
    const roster = groups.map((g) => `${g.phase}[${(g.tools ?? []).join("|")}]: ${(g.scripts ?? []).join(" -> ")}`);
    console.error(`${LOG_PREFIX} agent=${manifest.agentName ?? "?"} hooks=${roster.length ? roster.join("; ") : "none"}` +
      (manifest.bashAllowlist?.length ? ` bash-allowlist=${manifest.bashAllowlist.join(",")}` : ""));
    if (manifest.agentName && typeof pi.setSessionName === "function") {
      try {
        pi.setSessionName(manifest.agentName);
      } catch {
        // naming is cosmetic
      }
    }
  });
  pi.on("tool_call", (event) => hooks.onToolCall(event));
  pi.on("tool_result", (event) => hooks.onToolResult(event));
}
