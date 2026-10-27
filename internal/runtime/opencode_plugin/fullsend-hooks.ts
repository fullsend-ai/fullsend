// OpenCode 1.x adapter for security.HookPlan (ADR 0090). No SDK dependencies.
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { lstatSync, readFileSync, readdirSync, realpathSync, readlinkSync, statSync } from "node:fs";
import path from "node:path";

const names = {
  bash: "Bash", read: "Read", write: "Write", edit: "Edit",
  apply_patch: "Write", multiedit: "MultiEdit", glob: "Glob", grep: "Grep",
  webfetch: "WebFetch", websearch: "WebSearch", task: "Agent",
  skill: "Skill", todowrite: "TodoWrite", lsp: "LSP",
};
const deny = (reason) => { throw new Error(`[fullsend-hooks] ${reason}`); };
const digest = (bytes) => createHash("sha256").update(bytes).digest("hex");

// This is a diagnostic-only channel, not another invocation of the tool-result
// chain. Missing redactor files, canaries, malformed replies and execution
// failures all fall back to the fixed script name without reflecting stderr.
const sanitizeReason = String.raw`
import json, re, sys, unicodedata
sys.path.append(sys.argv[1])
from hook_io import canary_pattern
from secret_redact_posttool import redact_text
data = json.load(sys.stdin)
original = data["reason"]
text = unicodedata.normalize("NFKC", original)
text = re.sub(r"\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-?]*[ -/]*[@-~]", "", text)
text = "".join(c for c in text if c in "\n\r\t" or not unicodedata.category(c).startswith("C"))
token = data["canary"].strip()
if token and (canary_pattern(token).search(original) or canary_pattern(token).search(text)):
    text = None
else:
    text, _ = redact_text(text)
    text = " ".join(text.split()).replace("::", ": :")[:1024]
json.dump({"reason": text}, sys.stdout)
`;

// A deliberately conservative first-token check, matching pi's Bash(...)
// semantics. Refuse syntax that can hide executable commands rather than
// treating a prefix match ("gh *") as validation of an entire shell program.
function checkBash(command, allowlist) {
  if (!allowlist?.length) return;
  if (typeof command !== "string" || !command.trim()) deny("empty Bash command");
  if (/[`$(){}\\]|<<|<\(|>\(/.test(command)) deny("Bash allowlist rejects substitutions, groups, escapes and heredocs");
  for (const segment of command.split(/\r?\n|&&|\|\||;|\|&|\||(?<![<>|])&(?!>)/)) {
    if (!segment.trim()) continue;
    const first = segment.trim().split(/\s+/)[0];
    if (!allowlist.includes(first) || /^[A-Za-z_][A-Za-z0-9_]*=/.test(first) ||
        ["eval", "exec", "sh", "bash", "source", ".", "command", "builtin", "env", "nohup", "nice", "timeout", "xargs", "time"].includes(first)) {
      deny("command is outside the Bash allowlist");
    }
  }
}

function checkFiles(cfg) {
  if (!cfg || !Array.isArray(cfg.groups) || !cfg.digests || !path.isAbsolute(cfg.dir)) deny("hook policy unavailable");
  const dirInfo = lstatSync(cfg.dir);
  if (!dirInfo.isDirectory() || dirInfo.isSymbolicLink()) deny("invalid hook directory");
  const entries = readdirSync(cfg.dir);
  if (entries.length !== Object.keys(cfg.digests).length) deny("hook directory changed");
  for (const file of entries) {
    const target = path.join(cfg.dir, file);
    const targetInfo = lstatSync(target);
    if (!targetInfo.isFile() || targetInfo.isSymbolicLink() || digest(readFileSync(target)) !== cfg.digests[file]) {
      deny("hook script missing or modified");
    }
  }
}

function checkPermission(cfg, tool, args) {
  const id = ["write", "apply_patch", "multiedit"].includes(tool) ? "edit" : tool;
  const rule = cfg.permission[id] ?? cfg.permission["*"];
  const match = (pattern, text) => new RegExp("^" + pattern.replace(/[.+^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*").replace(/\?/g, ".") + "$", "s").test(text);
  let action = rule;
  if (rule && typeof rule === "object") {
    action = "deny";
    const value = args.command ?? args.filePath ?? args.path ?? args.subagent_type ?? args.url ?? "*";
    for (const [pattern, next] of Object.entries(rule)) if (match(pattern, value)) action = next;
  }
  if (action === "deny" || action === "ask") deny("tool denied by trusted permission policy");
}

function resolved(target, depth = 0) {
  if (depth > 40) deny("write path symlink loop");
  try {
    if (lstatSync(target).isSymbolicLink()) return resolved(path.resolve(path.dirname(target), readlinkSync(target)), depth + 1);
    return realpathSync(target);
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
    const parent = path.dirname(target);
    if (parent === target) throw error;
    return path.join(resolved(parent, depth + 1), path.basename(target));
  }
}

function checkWrites(cfg, tool, args, cwd) {
  let targets = [];
  if (["write", "edit", "multiedit"].includes(tool)) targets = [args.filePath ?? args.file_path];
  if (tool === "apply_patch") {
    targets = [...String(args.patchText ?? args.patch ?? "").matchAll(/^\*\*\* (?:Add File|Update File|Delete File|Move to): (.+)$/gm)].map((m) => m[1]);
    if (!targets.length) deny("unrecognized write patch");
  }
  const workspace = resolved(cfg.workspace);
  for (const target of targets) {
    if (typeof target !== "string" || !target) deny("missing write path");
    const dest = resolved(path.resolve(cwd, target));
    const relative = path.relative(workspace, dest);
    if (!relative || relative === ".." || relative.startsWith("../") || path.isAbsolute(relative) ||
        [".env", ".security", "runner-state"].some((p) => relative === p || relative.startsWith(p + "/"))) {
      deny("write path is outside the writable workspace");
    }
  }
}

export default async ({ directory }) => {
  let cfg;
  try { cfg = JSON.parse(process.env.FULLSEND_OPENCODE_HOOK_CONFIG ?? "null"); } catch { /* all calls fail closed below */ }
  const cwd = directory ?? process.cwd();
  // Never inherit loader, Python, pager, provider or agent-modified variables.
  const env = { PATH: "/usr/local/bin:/usr/bin:/bin", ...cfg?.env };
  const blockReason = (script, reason) => {
    const fallback = `blocked by ${script}`;
    if (typeof reason !== "string" || !reason.trim() || reason.length > 4096 ||
        !cfg.digests["secret_redact_posttool.py"] || !cfg.digests["hook_io.py"]) return fallback;
    try {
      checkFiles(cfg);
      const result = spawnSync(cfg.python, ["-I", "-S", "-B", "-c", sanitizeReason, cfg.dir], {
        input: JSON.stringify({ reason, canary: cfg.env.FULLSEND_CANARY_TOKEN ?? "" }),
        encoding: "utf8", env, cwd, timeout: 1500, maxBuffer: 16 * 1024,
      });
      if (result.error || result.signal || result.status !== 0) return fallback;
      const sanitized = JSON.parse(result.stdout).reason;
      return typeof sanitized === "string" && sanitized.length > 0 && sanitized.length <= 1024
        ? `${fallback}: ${sanitized}` : fallback;
    } catch {
      return fallback;
    }
  };
  const validate = (input, args) => {
    try {
      checkFiles(cfg);
      let canonical = names[input.tool] ?? input.tool;
      // OpenCode combines file reads and directory listings in `read`.
      // An LS-only parent may list directories, but must not gain file reads.
      if (input.tool === "read" && cfg.tools?.includes("LS") && !cfg.tools.includes("Read")) {
        const target = args.filePath ?? args.file_path;
        if (typeof target === "string" && statSync(path.resolve(cwd, target)).isDirectory()) canonical = "LS";
      }
      if (cfg.tools && !cfg.tools.includes(canonical) && !(canonical === "Agent" && cfg.tools.includes("Task"))) deny("tool is outside the parent allowlist");
      checkPermission(cfg, input.tool, args);
      if (input.tool === "bash") checkBash(args.command, cfg.bashAllowlist);
      checkWrites(cfg, input.tool, args, cwd);
      return canonical;
    } catch (error) {
      // File-read errors can carry paths, never tool output or secret bytes.
      if (error.message.startsWith("[fullsend-hooks]")) throw error;
      deny("hook integrity check failed");
    }
  };
  const chain = (phase, tool, args, output) => {
    const toolInput = { ...args };
    if (typeof args.filePath === "string") toolInput.file_path = args.filePath;
    let value = output;
    for (const group of cfg.groups) {
      if (group.Phase !== phase || !(group.Tools.includes("*") || group.Tools.includes(tool))) continue;
      for (const script of group.Scripts) {
        if (typeof script !== "string" || path.basename(script) !== script || script === "." || script === "..") {
          deny("invalid hook script name");
        }
        const payload = { hook_event_name: phase, tool_name: tool, tool_input: toolInput, cwd };
        if (phase === "PostToolUse") Object.assign(payload, { tool_response: value, tool_result: value });
        const result = spawnSync(cfg.python, ["-I", "-S", "-B", "-c",
          "import runpy,sys; d,p,workspace=sys.argv[1:]; sys.path.append(d); sys.argv=[p,'--sandbox-workspace',workspace]; runpy.run_path(p,run_name='__main__')",
          cfg.dir, path.join(cfg.dir, script), cfg.workspace], {
          input: JSON.stringify(payload), encoding: "utf8", env, cwd,
          timeout: 30_000, maxBuffer: 16 * 1024 * 1024,
        });
        if (result.error || result.signal || result.status === null) deny("hook failed to execute (fail closed)");
        let reply;
        try { reply = result.stdout.trim() ? JSON.parse(result.stdout) : null; } catch { deny("malformed hook reply (fail closed)"); }
        if (result.status !== 0 || reply?.decision === "block" || reply?.continue === false) {
          deny(blockReason(script, reply?.reason));
        }
        if (reply?.metadata && Object.entries(reply.metadata).some(([key, value]) => value && (key.endsWith("_error") || key === "shape_unpatched"))) {
          deny("hook sanitizer failed (fail closed)");
        }
        if (reply?.hookSpecificOutput?.updatedToolOutput !== undefined) {
          value = reply.hookSpecificOutput.updatedToolOutput;
          if (!value || typeof value !== "object" || typeof value.output !== "string") deny("unexpected hook output shape");
        }
      }
    }
    return value;
  };
  return {
    "shell.env": async (_input, output) => {
      // Use the bootstrap-captured command HOME and avoid shell-startup code.
      if (cfg?.shellHome) {
        for (const key of ["BASH_ENV", "ENV", "SHELLOPTS", "BASHOPTS", "CDPATH", "PS4"]) output.env[key] = "";
        for (const key of Object.keys(process.env)) if (key.startsWith("BASH_FUNC_")) output.env[key] = "";
        output.env.PATH = `${env.PATH}:${path.join(cfg.workspace, "bin")}`;
        output.env.HOME = cfg.shellHome;
        output.env.XDG_CONFIG_HOME = path.join(cfg.shellHome, ".config");
        output.env.XDG_CACHE_HOME = path.join(cfg.shellHome, ".cache");
        output.env.XDG_DATA_HOME = path.join(cfg.shellHome, ".local/share");
        output.env.XDG_STATE_HOME = path.join(cfg.shellHome, ".local/state");
      }
    },
    "experimental.chat.messages.transform": async (_input, output) => {
      // Native exceptions skip tool.execute.after, but remain tool-error parts
      // in session history. This hook runs before conversion/model sending,
      // including inside Task sessions. A throw prevents that model request.
      checkFiles(cfg);
      for (const message of output.messages) {
        for (const part of message.parts) {
          if (part.type !== "tool" || part.state?.status !== "error") continue;
          const state = part.state;
          try {
            const sanitized = chain("PostToolUse", names[part.tool] ?? part.tool,
              state.input ?? {}, { output: state.error, metadata: state.metadata ?? {}, exit_code: 1 });
            if (typeof sanitized.output !== "string") deny("unexpected tool-error output shape");
            state.error = sanitized.output;
            state.metadata = {};
            part.metadata = {};
          } catch (error) {
            state.error = "Tool error withheld by Fullsend";
            state.metadata = {};
            part.metadata = {};
            throw error;
          }
        }
      }
    },
    "tool.execute.before": async (input, output) => {
      const tool = validate(input, output.args);
      chain("PreToolUse", tool, output.args);
    },
    "tool.execute.after": async (input, output) => {
      // Scan every string field, not just stdout: native metadata, titles
      // and attachments can otherwise carry an alternate copy of a secret.
      const original = { ...output };
      try {
        const tool = validate(input, input.args);
        const sanitized = chain("PostToolUse", tool, input.args, original);
        output.output = sanitized.output;
        if (sanitized !== original) {
          // Native Bash metadata also carries a copy of stdout. Never leave
          // an alternate raw result or attachment alongside sanitized text.
          output.metadata = {};
          output.attachments = [];
          output.title = "Tool output sanitized by Fullsend";
        }
      } catch (error) {
        output.output = "";
        output.metadata = {};
        output.attachments = [];
        output.title = "Tool result withheld by Fullsend";
        throw error;
      }
    },
  };
};
