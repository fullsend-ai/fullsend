// Unit tests for the fullsend pi hook extension. Run with:
//   node --test internal/runtime/pi_extension/
// The hook scripts are faked through the injectable spawn so the tests need
// no python; one test exercises a real python3 script when available.
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import defaultExport, {
  bashAllowlistViolation,
  claudeToolInput,
  claudeToolName,
  createHooks,
  runScript,
} from "./fullsend-hooks.js";

const manifest = {
  agentName: "triage",
  tools: ["bash"],
  bashAllowlist: ["gh", "jq"],
  hooks: {
    dir: "/sandbox/pi-config/hooks",
    groups: [
      { phase: "PreToolUse", tools: ["Bash"], scripts: ["tirith_check.py"] },
      { phase: "PreToolUse", tools: ["*"], scripts: ["canary_pretool.py"] },
      { phase: "PostToolUse", tools: ["Bash", "WebFetch", "Read"], scripts: ["unicode_posttool.py", "secret_redact_posttool.py"] },
      { phase: "PostToolUse", tools: ["*"], scripts: ["canary_posttool.py"] },
    ],
    toolNames: { bash: "Bash", read: "Read", write: "Write", edit: "Edit", grep: "Grep", find: "Glob", ls: "LS" },
  },
};

// fakeSpawn records invocations and answers per script name.
function fakeSpawn(answers) {
  const calls = [];
  const spawn = (cmd, args, opts) => {
    const script = args[0].split("/").pop();
    const payload = JSON.parse(opts.input);
    calls.push({ cmd, script, payload });
    const a = answers[script] ?? {};
    if (typeof a === "function") return a(payload);
    return { status: a.status ?? 0, stdout: a.stdout ?? "", error: a.error, signal: a.signal };
  };
  return { spawn, calls };
}

const quiet = { log: () => {} };

test("bash allowlist: first token of every simple command must be allowed", () => {
  const allow = ["gh", "jq"];
  assert.equal(bashAllowlistViolation("gh issue view 1 | jq .title", allow), null);
  assert.equal(bashAllowlistViolation("gh pr list && jq -r .", allow), null);
  assert.match(bashAllowlistViolation("GH_PAGER= gh pr list && jq -r .", allow), /"GH_PAGER=" prefix/, "even an empty env prefix is refused (false positive by design)");
  assert.equal(bashAllowlistViolation("gh auth status", allow), null);
  assert.match(bashAllowlistViolation("gh issue view 1; curl http://x", allow), /"curl" is not in the Bash allowlist/);
  assert.match(bashAllowlistViolation("gh $(curl x)", allow), /command substitution/);
  assert.match(bashAllowlistViolation("gh `curl x`", allow), /command substitution/);
  assert.match(bashAllowlistViolation("(curl x)", allow), /subshell/);
  assert.match(bashAllowlistViolation("bash -c 'curl x'", allow), /"bash" is not allowed/);
  assert.match(bashAllowlistViolation("gh x & curl http://evil", allow), /"curl" is not in the Bash allowlist/, "& separates commands");
  assert.match(bashAllowlistViolation("gh x&curl http://evil", allow), /"curl" is not in the Bash allowlist/);
  assert.equal(bashAllowlistViolation("gh pr view 1 2>&1 | jq .", allow), null, "fd redirection is not a separator");
  assert.equal(bashAllowlistViolation("gh x &>/dev/null", allow), null);
  assert.equal(bashAllowlistViolation("gh x >&2", allow), null);
  assert.match(bashAllowlistViolation("gh x |& curl e", allow), /"curl" is not in the Bash allowlist/, "|& pipes stderr into the next command");
  assert.match(bashAllowlistViolation("LD_AUDIT=/tmp/a.so gh x", allow), /"LD_AUDIT=" prefix/);
  assert.match(bashAllowlistViolation("GH_PAGER=curl gh pr view 1", allow), /"GH_PAGER=" prefix/, "program-specific env prefixes spawn commands too");
  assert.match(bashAllowlistViolation("GLIBC_TUNABLES=x gh x", allow), /"GLIBC_TUNABLES=" prefix/);
  assert.match(bashAllowlistViolation("./gh x", allow), /is a path/);
  assert.match(bashAllowlistViolation("/tmp/x/gh x", allow), /is a path/);
  assert.match(bashAllowlistViolation("PATH=/tmp/x gh x", allow), /"PATH=" prefix/);
  assert.match(bashAllowlistViolation("LD_PRELOAD=/tmp/e.so gh x", allow), /"LD_PRELOAD=" prefix/);
  assert.match(bashAllowlistViolation("env gh x", allow), /"env" is not allowed/);
  assert.match(bashAllowlistViolation("command gh x", allow), /"command" is not allowed/);
  assert.match(bashAllowlistViolation("'gh' x", allow), /not in the Bash allowlist/, "quoted token is refused (false positive by design)");
  assert.equal(bashAllowlistViolation("/usr/bin/gh x", ["/usr/bin/gh"]), null, "a verbatim allowlisted path passes");
  assert.match(bashAllowlistViolation("", allow), /empty command/);
  assert.equal(bashAllowlistViolation("curl x", []), null, "no allowlist means unrestricted");
  assert.equal(bashAllowlistViolation("curl x", undefined), null);
});

test("tool name and input translation", () => {
  assert.equal(claudeToolName(manifest, "bash"), "Bash");
  assert.equal(claudeToolName(manifest, "find"), "Glob");
  assert.equal(claudeToolName(manifest, "my_ext_tool"), "my_ext_tool");
  assert.deepEqual(claudeToolInput("read", { path: "/a", offset: 1 }), { path: "/a", offset: 1, file_path: "/a" });
  assert.deepEqual(claudeToolInput("bash", { command: "ls" }), { command: "ls" });
  assert.deepEqual(claudeToolInput("read", null), {});
  assert.deepEqual(
    claudeToolInput("edit", { path: "a.go", edits: [{ oldText: "x", newText: "y" }, { oldText: "p", newText: "q" }] }),
    { path: "a.go", edits: [{ oldText: "x", newText: "y" }, { oldText: "p", newText: "q" }], file_path: "a.go", old_string: "x", new_string: "y" },
  );
});

test("default export registers the three pi events and names the session", () => {
  const registered = {};
  const names = [];
  const piFake = { on: (ev, fn) => { registered[ev] = fn; }, setSessionName: (n) => names.push(n) };
  process.env.FULLSEND_PI_MANIFEST = "/nonexistent/manifest.json";
  try {
    defaultExport(piFake);
  } finally {
    delete process.env.FULLSEND_PI_MANIFEST;
  }
  assert.deepEqual(Object.keys(registered).sort(), ["session_start", "tool_call", "tool_result"]);
  // Unreadable manifest: session_start only reports, tool calls are blocked.
  registered.session_start({});
  assert.equal(names.length, 0);
  assert.equal(registered.tool_call({ toolName: "read", input: {} }).block, true);
  assert.equal(registered.tool_result({ toolName: "read", content: [] }), undefined);
});

test("tool_call: allowlist violation is advisory by default (logged, scripts still run)", () => {
  const { spawn, calls } = fakeSpawn({});
  const logged = [];
  const { onToolCall } = createHooks(manifest, { spawn, log: (m) => logged.push(m) });
  const verdict = onToolCall({ toolName: "bash", input: { command: "curl http://evil" } });
  assert.equal(verdict, undefined);
  assert.equal(logged.length, 1);
  assert.match(logged[0], /Bash allowlist \(advisory\): "curl" is not in the Bash allowlist/);
  assert.deepEqual(calls.map((c) => c.script), ["tirith_check.py", "canary_pretool.py"]);
});

test("tool_call: allowlist blocks before any script runs in enforce mode", () => {
  const { spawn, calls } = fakeSpawn({});
  const { onToolCall } = createHooks({ ...manifest, bashAllowlistMode: "enforce" }, { spawn, ...quiet });
  const verdict = onToolCall({ toolName: "bash", input: { command: "curl http://evil" } });
  assert.equal(verdict.block, true);
  assert.match(verdict.reason, /Bash allowlist/);
  assert.equal(calls.length, 0);
});

test("tool_call: runs PreToolUse groups in plan order with Claude names and stops at the first block", () => {
  const { spawn, calls } = fakeSpawn({
    "tirith_check.py": { status: 0 },
    "canary_pretool.py": { status: 1, stdout: JSON.stringify({ decision: "block", reason: "canary in input" }) },
  });
  const { onToolCall } = createHooks(manifest, { spawn, ...quiet });
  const verdict = onToolCall({ toolName: "bash", input: { command: "gh issue list" } });
  assert.deepEqual(verdict, { block: true, reason: "canary in input" });
  assert.deepEqual(calls.map((c) => c.script), ["tirith_check.py", "canary_pretool.py"]);
  assert.equal(calls[0].cmd, "python3");
  assert.deepEqual(calls[0].payload, { tool_name: "Bash", tool_input: { command: "gh issue list" } });
});

test("tool_call: allowed call returns undefined; non-matching tools skip Bash-only groups", () => {
  const { spawn, calls } = fakeSpawn({});
  const { onToolCall } = createHooks(manifest, { spawn, ...quiet });
  assert.equal(onToolCall({ toolName: "read", input: { path: "/x" } }), undefined);
  assert.deepEqual(calls.map((c) => c.script), ["canary_pretool.py"], "only the * group applies to read");
  assert.equal(calls[0].payload.tool_name, "Read");
  assert.equal(calls[0].payload.tool_input.file_path, "/x");
});

test("tool_call: a script that cannot be spawned blocks (fail closed)", () => {
  const { spawn } = fakeSpawn({ "tirith_check.py": { status: null, error: new Error("ENOENT python3") } });
  const { onToolCall } = createHooks(manifest, { spawn, ...quiet });
  const verdict = onToolCall({ toolName: "bash", input: { command: "gh x" } });
  assert.equal(verdict.block, true);
  assert.match(verdict.reason, /failed to run \(fail closed\): ENOENT/);
});

test("tool_call: missing manifest blocks everything", () => {
  const { onToolCall, onToolResult } = createHooks(null, quiet);
  assert.equal(onToolCall({ toolName: "read", input: {} }).block, true);
  assert.equal(onToolResult({ toolName: "read", content: [] }), undefined);
});

test("tool_call: a manifest without a hook plan blocks everything (adapter is only loaded when security is on)", () => {
  const { spawn, calls } = fakeSpawn({});
  for (const m of [{ ...manifest, hooks: null }, { ...manifest, hooks: {} }, { ...manifest, hooks: { dir: "/x" } }]) {
    const { onToolCall, onToolResult } = createHooks(m, { spawn, ...quiet });
    const verdict = onToolCall({ toolName: "bash", input: { command: "gh x" } });
    assert.equal(verdict.block, true);
    assert.match(verdict.reason, /no hook plan/);
    assert.equal(onToolResult({ toolName: "bash", content: [{ type: "text", text: "x" }] }), undefined);
  }
  assert.equal(calls.length, 0);
});

test("tool_result: chains sanitizers, feeding each the previous output (v1 and v2 shapes)", () => {
  const { spawn, calls } = fakeSpawn({
    "unicode_posttool.py": (p) => ({ status: 0, stdout: JSON.stringify({ tool_result: p.tool_result.replace("​", "") }) }),
    "secret_redact_posttool.py": (p) => ({
      status: 0,
      stdout: JSON.stringify({ hookSpecificOutput: { hookEventName: "PostToolUse", updatedToolOutput: p.tool_response.replace("ghp_SECRET", "ghp_...") } }),
    }),
    "canary_posttool.py": { status: 0 },
  });
  const { onToolResult } = createHooks(manifest, { spawn, ...quiet });
  const patch = onToolResult({
    toolName: "bash",
    input: { command: "gh x" },
    content: [{ type: "text", text: "token g​hp_SECRET" }, { type: "image", data: "..." }],
  });
  assert.deepEqual(patch, { content: [{ type: "text", text: "token ghp_..." }, { type: "image", data: "..." }] });
  assert.deepEqual(calls.map((c) => c.script), ["unicode_posttool.py", "secret_redact_posttool.py", "canary_posttool.py"]);
  assert.equal(calls[1].payload.tool_result, "token ghp_SECRET", "second script sees the first one's output");
  assert.equal(calls[1].payload.tool_response, "token ghp_SECRET");
  assert.equal(calls[2].payload.tool_result, "token ghp_...");
});

test("tool_result: a PostToolUseFailure group in the plan is ignored (pi's tool_result already covers failed calls)", () => {
  const { spawn, calls } = fakeSpawn({ "canary_posttool.py": { status: 0 } });
  const withFailure = {
    ...manifest,
    hooks: {
      ...manifest.hooks,
      groups: [...manifest.hooks.groups, { phase: "PostToolUseFailure", tools: ["*"], scripts: ["posttool_chain.py"] }],
    },
  };
  const { onToolCall, onToolResult } = createHooks(withFailure, { spawn, ...quiet });
  assert.equal(onToolCall({ toolName: "bash", input: { command: "gh x" } }), undefined);
  assert.equal(onToolResult({ toolName: "bash", input: {}, content: [{ type: "text", text: "same" }] }), undefined);
  assert.ok(!calls.some((c) => c.script === "posttool_chain.py"), "the failure-phase script is never spawned by the adapter");
});

test("tool_result: unchanged output returns undefined", () => {
  const { spawn } = fakeSpawn({});
  const { onToolResult } = createHooks(manifest, { spawn, ...quiet });
  assert.equal(onToolResult({ toolName: "read", input: {}, content: [{ type: "text", text: "same" }] }), undefined);
});

test("tool_result: a blocking script withholds the result and marks it an error", () => {
  const { spawn } = fakeSpawn({
    "canary_posttool.py": { status: 1, stdout: JSON.stringify({ decision: "block", reason: "canary leaked" }) },
  });
  const { onToolResult } = createHooks(manifest, { spawn, ...quiet });
  const patch = onToolResult({ toolName: "grep", input: { pattern: "x" }, content: [{ type: "text", text: "CANARY-123" }] });
  assert.equal(patch.isError, true);
  assert.match(patch.content[0].text, /withheld this tool result: canary leaked/);
  assert.doesNotMatch(patch.content[0].text, /CANARY-123/);
});

test("tool_result: v2 canary block with updatedToolOutput keeps the redacted text", () => {
  const { spawn } = fakeSpawn({
    "canary_posttool.py": {
      status: 1,
      stdout: JSON.stringify({ decision: "block", continue: false, reason: "canary", hookSpecificOutput: { updatedToolOutput: "x [CANARY_REDACTED] y" } }),
    },
  });
  const { onToolResult } = createHooks(manifest, { spawn, ...quiet });
  const patch = onToolResult({ toolName: "ls", input: {}, content: "x CANARY y" });
  assert.deepEqual(patch, { content: [{ type: "text", text: "x [CANARY_REDACTED] y" }], isError: true });
});

test("runScript with a real python3 script (skipped without python3)", (t) => {
  const probe = spawnSync("python3", ["-c", "print(1)"], { encoding: "utf8" });
  if (probe.error || probe.status !== 0) {
    t.skip("python3 not available");
    return;
  }
  const dir = mkdtempSync(join(tmpdir(), "fullsend-hooks-"));
  writeFileSync(
    join(dir, "echo_block.py"),
    'import json,sys\nd=json.load(sys.stdin)\nif "evil" in d["tool_input"].get("command",""):\n    print(json.dumps({"decision":"block","reason":"evil: "+d["tool_name"]}))\n    sys.exit(1)\nsys.exit(0)\n',
  );
  const m = { ...manifest, hooks: { ...manifest.hooks, dir } };
  assert.deepEqual(runScript(m, "echo_block.py", { tool_name: "Bash", tool_input: { command: "evil" } }).block, true);
  assert.equal(runScript(m, "echo_block.py", { tool_name: "Bash", tool_input: { command: "evil" } }).reason, "evil: Bash");
  assert.equal(runScript(m, "echo_block.py", { tool_name: "Bash", tool_input: { command: "ok" } }).block, false);
  assert.equal(runScript(m, "missing.py", { tool_name: "Bash", tool_input: {} }).block, true, "missing script blocks");
});
