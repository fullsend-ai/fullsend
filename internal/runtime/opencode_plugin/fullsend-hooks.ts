// OpenCode 1.x adapter for security.HookPlan (ADR 0090). No SDK dependencies.
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { lstatSync, readFileSync, readdirSync, readlinkSync, statSync, writeFileSync } from "node:fs";
import path from "node:path";

const names = {
  bash: "Bash", read: "Read", write: "Write", edit: "Edit",
  apply_patch: "Edit", multiedit: "MultiEdit", glob: "Glob", grep: "Grep",
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

// Parse only the shell grammar we can check completely. Keep command boundaries
// and remove argument quotes so neither a prefix nor quoting can hide a denied
// command from a trusted pattern map. This is deliberately not a general shell
// parser: unsupported syntax is refused before execution.
function bashCommands(command, allowParameterExpansion = false) {
  if (typeof command !== "string" || !command.trim()) deny("empty Bash command");
  const commands = [];
  let words = [];
  let word = "";
  let started = false;
  let quotedProgram = false;
  let quote = null;
  const finishWord = () => {
    if (!started) return;
    if (!words.length && quotedProgram) deny("Bash policy rejects quoted program names");
    words.push(word);
    word = "";
    started = false;
    quotedProgram = false;
  };
  const finishCommand = () => {
    finishWord();
    if (words.length) {
      const first = words[0];
      if (!/^[A-Za-z0-9_./-]+$/.test(first) ||
          ["eval", "exec", "sh", "bash", "source", ".", "command", "builtin", "env", "nohup", "nice", "timeout", "xargs", "time",
            "if", "then", "else", "elif", "fi", "for", "while", "until", "do", "done", "case", "esac", "in", "function", "select", "coproc",
            "export", "unset", "readonly", "set", "trap", "alias", "unalias", "declare", "typeset", "local", "let", "enable", "hash"].includes(first)) {
        deny("Bash policy rejects environment assignments and shell wrappers");
      }
      commands.push(words.join(" "));
      words = [];
    }
  };
  for (let index = 0; index < command.length; index++) {
    const char = command[index];
    if (quote === "'") {
      if (char === "'") quote = null;
      else word += char;
      continue;
    }
    if (quote === '"') {
      if (char === '"') quote = null;
      else if (char === "$") {
        const expansion = command.slice(index).match(/^\$(?:[A-Za-z_][A-Za-z0-9_]*|\{[A-Za-z_][A-Za-z0-9_]*\})/);
        // Program-name allowlists do not inspect arguments. Trusted argument
        // patterns cannot safely authorize an expansion's unknown value.
        if (!allowParameterExpansion || !expansion) deny("Bash policy rejects parameter expansion with trusted patterns or unsupported substitutions");
        word += expansion[0];
        index += expansion[0].length - 1;
      } else if (char === "`" || char === "\\") deny("Bash policy rejects substitutions and escapes");
      else word += char;
      continue;
    }
    if (char === "#") deny("Bash policy rejects comments");
    if (char === "'" || char === '"') {
      quote = char;
      started = true;
      if (!words.length) quotedProgram = true;
    } else if (/[`$(){}\\<>&\r]/.test(char)) {
      deny("Bash policy rejects substitutions, groups, redirections, backgrounding and escapes");
    } else if (!allowParameterExpansion && ["*", "?", "["].includes(char)) {
      deny("Bash policy rejects pathname expansion with trusted patterns");
    } else if (!allowParameterExpansion && char === "~") {
      deny("Bash policy rejects tilde expansion with trusted patterns");
    } else if (char === " " || char === "\t") finishWord();
    else if (char === "\n" || char === ";" || char === "|") {
      finishCommand();
      if (char === "|" && command[index + 1] === "|") index++;
    } else {
      word += char;
      started = true;
    }
  }
  if (quote !== null) deny("Bash policy rejects unterminated quotes");
  finishCommand();
  return commands;
}

// Enforce Bash(...) program names; pi makes this advisory by default.
function checkBash(command, allowlist) {
  if (!allowlist?.length) return;
  for (const segment of bashCommands(command, true)) {
    if (!allowlist.includes(segment.split(" ")[0])) deny("command is outside the Bash allowlist");
  }
}

function parseHookReply(stdout, phase) {
  if (!stdout.trim()) return null;
  const object = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
  let reply;
  try { reply = JSON.parse(stdout); } catch { deny("malformed hook reply (fail closed)"); }
  if (!object(reply) || Object.keys(reply).some((key) => !["decision", "reason", "continue", "metadata", "hookSpecificOutput", "tool_result"].includes(key)) ||
      ("decision" in reply && !["allow", "block"].includes(reply.decision)) ||
      ("reason" in reply && typeof reply.reason !== "string") ||
      ("continue" in reply && typeof reply.continue !== "boolean") ||
      ("metadata" in reply && !object(reply.metadata))) deny("malformed hook reply (fail closed)");
  if ("hookSpecificOutput" in reply) {
    const output = reply.hookSpecificOutput;
    if (!object(output) || Object.keys(output).some((key) => !["hookEventName", "updatedToolOutput", "additionalContext"].includes(key)) ||
        ("hookEventName" in output && output.hookEventName !== phase) ||
        ("additionalContext" in output && typeof output.additionalContext !== "string") ||
        ("updatedToolOutput" in output && phase !== "PostToolUse")) deny("malformed hook reply (fail closed)");
  }
  return reply;
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

function writeTargets(tool, args) {
  if (["write", "edit", "multiedit"].includes(tool)) return [args.filePath ?? args.file_path];
  if (tool !== "apply_patch") return [];
  const targets = [];
  for (const line of String(args.patchText ?? args.patch ?? "").split("\n")) {
    const header = /^\*\*\* (?:Add File|Update File|Delete File|Move to):([\s\S]*)$/.exec(line);
    if (header) targets.push(header[1].trim());
    else if (line.startsWith("*** ") && !["*** Begin Patch", "*** End Patch", "*** End of File"].includes(line.trimEnd())) {
      deny("unrecognized write patch header");
    }
  }
  if (!targets.length) deny("unrecognized write patch");
  return targets;
}

function checkPermission(cfg, tool, args, cwd, worktree, targets) {
  const id = ["write", "apply_patch", "multiedit"].includes(tool) ? "edit" : tool;
  const rule = cfg.permission[id] ?? cfg.permission["*"];
  const match = (pattern, text) => {
    let expression = pattern.replace(/[.+^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*").replace(/\?/g, ".");
    if (expression.endsWith(" .*")) expression = expression.slice(0, -3) + "( .*)?";
    return new RegExp("^" + expression + "$", "s").test(text);
  };
  let action = rule;
  if (rule && typeof rule === "object") {
    action = "deny";
    const value = {
      bash: args.command,
      task: args.subagent_type,
      glob: args.pattern,
      grep: args.pattern,
      webfetch: args.url,
      websearch: args.query,
      skill: args.name,
    }[tool] ?? "*";
    let values = [value];
    if (tool === "bash") values = bashCommands(args.command);
    else if (["read", "write", "edit", "multiedit", "apply_patch"].includes(tool)) {
      const paths = tool === "read" ? [args.filePath ?? args.file_path] : targets;
      values = paths.map((target) => {
        if (typeof target !== "string" || !target) deny("missing permission path");
        const relative = path.relative(worktree, resolved(target, cwd));
        return tool === "apply_patch" ? relative.replaceAll("\\", "/") : relative;
      });
    }
    for (const value of values) {
      action = "deny";
      for (const [pattern, next] of Object.entries(rule)) if (match(pattern, value)) action = next;
      if (action === "deny" || action === "ask") deny("tool denied by trusted permission policy");
    }
  }
  if (action === "deny" || action === "ask") deny("tool denied by trusted permission policy");
}

const absolutePath = (target, cwd) => path.isAbsolute(target) ? target : cwd + path.sep + target;

function resolved(target, cwd = process.cwd()) {
  const absolute = absolutePath(target, cwd);
  let dest = path.parse(absolute).root;
  const pending = absolute.slice(dest.length).split(path.sep);
  let links = 0;
  // Follow symlinks before processing later '..' components, including inside
  // link targets. Lexical normalization here can hide the actual destination.
  while (pending.length) {
    const component = pending.shift();
    if (!component || component === ".") continue;
    if (component === "..") {
      dest = path.dirname(dest);
      continue;
    }
    const next = path.join(dest, component);
    let info;
    try { info = lstatSync(next); } catch (error) {
      if (error.code !== "ENOENT") throw error;
      // Native writes may create missing parents, but existing ancestors still
      // need to be resolved before checking the prospective destination.
      dest = next;
      continue;
    }
    if (info.isSymbolicLink()) {
      if (++links > 40) deny("write path symlink loop");
      const link = readlinkSync(next);
      const absoluteLink = path.isAbsolute(link);
      if (absoluteLink) dest = path.parse(link).root;
      pending.unshift(...(absoluteLink ? link.slice(dest.length) : link).split(path.sep));
    } else {
      if (!info.isDirectory() && pending.some((part) => part !== "")) deny("write path parent is not a directory");
      dest = next;
    }
  }
  return dest;
}

function checkWrites(cfg, targets, cwd) {
  const workspace = resolved(cfg.workspace);
  for (const target of targets) {
    if (typeof target !== "string" || !target) deny("missing write path");
    const dest = resolved(target, cwd);
    const relative = path.relative(workspace, dest);
    if (!relative || relative === ".." || relative.startsWith("../") || path.isAbsolute(relative) ||
        [".env", ".env.d", ".fullsend", ".security", "runner-state", "bin"].some((p) => relative === p || relative.startsWith(p + "/"))) {
      deny("write path is outside the writable workspace");
    }
  }
}

export default async ({ directory, worktree }) => {
  let cfg;
  try { cfg = JSON.parse(process.env.FULLSEND_OPENCODE_HOOK_CONFIG ?? "null"); } catch { /* all calls fail closed below */ }
  const cwd = directory ?? process.cwd();
  const permissionRoot = resolved(worktree ?? cwd);
  // Never inherit loader, Python, pager, provider or agent-modified variables.
  // OpenShell's marker is a trusted sandbox capability signal used by SSRF hooks.
  const env = { PATH: "/usr/local/bin:/usr/bin:/bin", OPENSHELL_SANDBOX: process.env.OPENSHELL_SANDBOX ?? "", ...cfg?.env };
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
      // OpenCode replaces write/edit with apply_patch for GPT-family models,
      // and its edit tool implements the legacy MultiEdit capability too.
      if (input.tool === "apply_patch") {
        canonical = ["Edit", "MultiEdit", "Write"].find((name) => cfg.tools?.includes(name)) ?? canonical;
      } else if (input.tool === "edit" && !cfg.tools?.includes("Edit") && cfg.tools?.includes("MultiEdit")) {
        canonical = "MultiEdit";
      }
      // OpenCode combines file reads and directory listings in `read`.
      // An LS-only parent may list directories, but must not gain file reads.
      if (input.tool === "read" && cfg.tools?.includes("LS") && !cfg.tools.includes("Read")) {
        const target = args.filePath ?? args.file_path;
        if (typeof target !== "string" || !target) deny("missing read target");
        let info;
        try { info = statSync(absolutePath(target, cwd)); } catch (error) {
          if (error.code === "ENOENT" || error.code === "ENOTDIR") deny("read target does not exist");
          deny("read target could not be inspected");
        }
        if (!info.isDirectory()) deny("LS-only read target is not a directory");
        canonical = "LS";
      }
      if (cfg.tools && !cfg.tools.includes(canonical) && !(canonical === "Agent" && cfg.tools.includes("Task"))) deny("tool is outside the parent allowlist");
      const targets = writeTargets(input.tool, args);
      checkPermission(cfg, input.tool, args, cwd, permissionRoot, targets);
      if (input.tool === "bash") checkBash(args.command, cfg.bashAllowlist);
      checkWrites(cfg, targets, cwd);
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
          "import runpy,sys; d,p,workspace=sys.argv[1:]; sys.path.append(d); sys.argv=[p,'--sandbox-workspace='+workspace]; runpy.run_path(p,run_name='__main__')",
          cfg.dir, path.join(cfg.dir, script), cfg.workspace], {
          input: JSON.stringify(payload), encoding: "utf8", env, cwd,
          timeout: 30_000, maxBuffer: 16 * 1024 * 1024,
        });
        if (result.error || result.signal || result.status === null) deny("hook failed to execute (fail closed)");
        const reply = parseHookReply(result.stdout, phase);
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
  // Bind cached text to the entire scanned payload, not just a reusable part ID.
  // Keep both raw and rewritten keys because OpenCode may reload raw history.
  const errorCache = new Map();
  const errorKey = (part) => digest(JSON.stringify([
    part.id, part.callID, part.tool, part.state.input ?? {},
    part.state.error, part.state.metadata ?? {},
  ]));
  // Write only after all plugin-factory setup succeeds. OpenCode logs and
  // ignores a factory error, so an earlier signal would falsely pass the guard.
  if (typeof cfg?.activation?.path === "string" && typeof cfg.activation.token === "string") {
    writeFileSync(cfg.activation.path, cfg.activation.token, { mode: 0o600 });
  }
  return {
    "shell.env": async (_input, output) => {
      // Use the bootstrap-captured command HOME and avoid shell-startup code.
      if (cfg?.shellHome) {
        for (const key of ["BASH_ENV", "ENV", "SHELLOPTS", "BASHOPTS", "CDPATH", "PS4"]) output.env[key] = "";
        for (const key of Object.keys(process.env)) if (key.startsWith("BASH_FUNC_")) output.env[key] = "";
        // shell.env overrides inherited variables only when it supplies a value.
        output.env.FULLSEND_OPENCODE_HOOK_CONFIG = "";
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
            const key = errorKey(part);
            const sanitized = errorCache.get(key) ?? chain("PostToolUse", names[part.tool] ?? part.tool,
              state.input ?? {}, { output: state.error, metadata: state.metadata ?? {}, exit_code: 1 }).output;
            if (typeof sanitized !== "string") deny("unexpected tool-error output shape");
            state.error = sanitized;
            state.metadata = {};
            part.metadata = {};
            errorCache.set(key, sanitized);
            errorCache.set(errorKey(part), sanitized);
            // Bound retention in long-lived parent and Task sessions.
            while (errorCache.size > 512) errorCache.delete(errorCache.keys().next().value);
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
      if (Array.isArray(output.content) || typeof output.output !== "string") deny("unsupported tool result shape");
      const original = { ...output };
      if (input.tool === "bash") {
        if (Number.isInteger(output.metadata?.exit)) original.exit_code = output.metadata.exit;
        else original.interrupted = true;
      }
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
