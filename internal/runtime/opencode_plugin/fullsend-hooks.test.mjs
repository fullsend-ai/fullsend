import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, symlinkSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { createHash } from "node:crypto";
import { fileURLToPath, pathToFileURL } from "node:url";

const plugin = (await import(pathToFileURL(process.env.FULLSEND_TEST_ADAPTER))).default;

async function setup(t, extra = {}, scripts = {}) {
  const root = mkdtempSync(path.join(tmpdir(), "fullsend-opencode-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const dir = path.join(root, "hooks");
  const workspace = path.join(root, "workspace");
  mkdirSync(dir); mkdirSync(workspace);
  const digests = {};
  for (const [name, source] of Object.entries(scripts)) {
    writeFileSync(path.join(dir, name), source);
    digests[name] = createHash("sha256").update(source).digest("hex");
  }
  const cfg = { dir, workspace, python: process.env.FULLSEND_TEST_PYTHON,
    env: { PATH: "/usr/bin:/bin" }, groups: [], digests, tools: null,
    bashAllowlist: null, permission: { "*": "allow" }, ...extra };
  process.env.FULLSEND_OPENCODE_HOOK_CONFIG = JSON.stringify(cfg);
  const hooks = await plugin({ directory: workspace });
  const before = (tool, args = {}) => hooks["tool.execute.before"]({ tool }, { args });
  const after = async (tool, args, output = "original") => {
    const result = { output, metadata: { output }, attachments: [{ url: output }], title: output };
    await hooks["tool.execute.after"]({ tool, args }, result);
    return result;
  };
  const messages = (parts) => hooks["experimental.chat.messages.transform"]({}, { messages: [{ parts }] });
  return { root, dir, workspace, cfg, hooks, before, after, messages };
}

test("Bash restrictions reject composed/hidden commands and environment overrides", async (t) => {
  const h = await setup(t, { bashAllowlist: ["gh", "jq"] });
  for (const command of ["gh", "gh issue list", "gh issue list | jq '.[]'"]) {
    await h.before("bash", { command });
  }
  for (const command of ["", "rm file", "gh issue list; rm file", "gh && rm file", "gh | sh", "gh & rm file", "$(rm file)", "gh `rm file`", "GH_PAGER=sh gh", "env gh", "command gh", "/usr/bin/gh", "g\\h", "gh <<EOF\nrm file\nEOF", "gh $(rm file)"]) {
    await assert.rejects(h.before("bash", { command }), /fullsend-hooks/);
  }
});

test("parent tool and trusted policy ceilings apply to every session", async (t) => {
  const h = await setup(t, { tools: ["Bash", "Task"], permission: { bash: { "*": "deny", "gh *": "allow", "gh secret*": "deny" }, task: "allow" } });
  await h.before("bash", { command: "gh issue list" });
  await h.before("task", { subagent_type: "child" });
  await assert.rejects(h.before("bash", { command: "gh secret list" }), /trusted permission/);
  await assert.rejects(h.before("write", { filePath: "file" }), /parent allowlist/);
});

test("LS-only parents can list directories without gaining file reads", async (t) => {
  const h = await setup(t, { tools: ["LS"], permission: { "*": "deny", read: "allow" } });
  writeFileSync(path.join(h.workspace, "private.txt"), "not available through LS");
  symlinkSync(h.workspace, path.join(h.workspace, "directory-link"));
  symlinkSync(path.join(h.workspace, "private.txt"), path.join(h.workspace, "file-link"));
  for (const filePath of [h.workspace, ".", "directory-link"]) {
    await h.before("read", { filePath });
    await h.after("read", { filePath }, "directory listing");
  }
  for (const filePath of ["private.txt", "file-link", "missing"]) {
    await assert.rejects(h.before("read", { filePath }), /fullsend-hooks/);
  }
  await assert.rejects(h.before("write", { filePath: "new.txt" }), /parent allowlist/);
});

test("native write paths resolve symlinks and protect runner state", async (t) => {
  const h = await setup(t);
  await h.before("write", { filePath: "new/file" });
  symlinkSync(h.root, path.join(h.workspace, "outside"));
  symlinkSync(path.join(h.root, "missing"), path.join(h.workspace,"broken"));
  for (const filePath of ["../escape", "outside/escape", "broken", ".env", ".security/findings.jsonl", "runner-state/file"]) {
    await assert.rejects(h.before("write", { filePath }), /write path/);
  }
  for (const tool of ["edit", "multiedit"]) {
    await assert.rejects(h.before(tool, { filePath: "../escape" }), /write path/);
  }
  await assert.rejects(h.before("apply_patch", { patchText: "*** Add File: ../escape\n+x" }), /write path/);
  await assert.rejects(h.before("apply_patch", { patchText: "unrecognized" }), /unrecognized/);
});

test("scripts receive translated v2 payload, isolated environment and chained output", async (t) => {
  const scripts = {
    "pre.py": "import json,os,sys\np=json.load(sys.stdin)\nassert p['tool_name']=='Read' and p['tool_input']['file_path']=='file'\nassert 'PYTHONPATH' not in os.environ\nassert os.environ['FULLSEND_CANARY_TOKEN']=='trusted'\n",
    "post.py": "import json,sys\np=json.load(sys.stdin)\nassert p['tool_response']==p['tool_result']\nv=p['tool_response']; v['output']=v['output'].replace('original','sanitized')\nprint(json.dumps({'hookSpecificOutput':{'updatedToolOutput':v}}))\n",
  };
  const h = await setup(t, {
    env: { PATH: "/usr/bin:/bin", FULLSEND_CANARY_TOKEN: "trusted" },
    groups: [{ Phase: "PreToolUse", Tools: ["Read"], Scripts: ["pre.py"] },
      { Phase: "PostToolUse", Tools: ["*"], Scripts: ["post.py", "post.py"] }],
  }, scripts);
  process.env.PYTHONPATH = h.root;
  process.env.FULLSEND_CANARY_TOKEN = "untrusted";
  process.env.FULLSEND_OPENCODE_HOOK_CONFIG = "null";
  t.after(() => {
    delete process.env.PYTHONPATH;
    delete process.env.FULLSEND_CANARY_TOKEN;
    delete process.env.FULLSEND_OPENCODE_HOOK_CONFIG;
  });
  await h.before("read", { filePath: "file" });
  const result = await h.after("read", {}, "original");
  assert.equal(result.output, "sanitized");
  assert.deepEqual(result.metadata, {});
  assert.deepEqual(result.attachments, []);
});

test("hook script names cannot escape the hook directory", async (t) => {
  const h = await setup(t, { groups: [{ Phase: "PreToolUse", Tools: ["*"], Scripts: ["../escape.py"] }] });
  await assert.rejects(h.before("read", {}), /invalid hook script name/);
});

test("script failure, malformed replies and tampering all fail closed", async (t) => {
  for (const source of ["import sys; sys.exit(1)", "print('not json')", "print('{\"decision\":\"block\",\"reason\":\"secret\"}')"]) {
    const h = await setup(t, { groups: [{ Phase: "PostToolUse", Tools: ["*"], Scripts: ["hook.py"] }] }, { "hook.py": source });
    await assert.rejects(h.after("read", {}, "secret original"), /fullsend-hooks/);
  }
  const h = await setup(t, {}, { "hook.py": "pass" });
  writeFileSync(path.join(h.dir, "hook.py"), "# tampered");
  await assert.rejects(h.before("bash", { command: "gh" }), /modified/);
  const other = await setup(t);
  mkdirSync(path.join(other.dir, "json"));
  await assert.rejects(other.before("read", {}), /directory changed/);
  const missing = await setup(t, { python: "/does-not-exist", groups: [{ Phase: "PreToolUse", Tools: ["*"], Scripts: ["hook.py"] }] }, { "hook.py": "pass" });
  await assert.rejects(missing.before("read", {}), /failed to execute/);
});

test("denial diagnostics retain useful details without secrets or canaries", async (t) => {
  const hooksDir = path.join(path.dirname(fileURLToPath(import.meta.url)), "../../security/hooks");
  const sanitizers = Object.fromEntries(["hook_io.py", "secret_redact_posttool.py"].map((file) =>
    [file, readFileSync(path.join(hooksDir, file), "utf8")]));
  const token = "ghp_" + "A".repeat(36);
  const canary = "FULLSEND_REASON_CANARY";
  const cases = [
    { reason: "Only HTTPS URLs are allowed", expected: "Only HTTPS URLs are allowed" },
    { reason: `Credential ${token} is not allowed`, expected: "Credential ghp_... is not allowed", absent: token },
    { reason: `Token ${canary.toLowerCase()} detected`, expected: "blocked by deny.py", absent: "detected" },
    { reason: "\u001b[31mDenied\u001b[0m\n::warning:: details", expected: "Denied : :warning: : details", absent: "\u001b" },
    { reason: "x".repeat(4097), expected: "blocked by deny.py", absent: "xxx" },
  ];
  for (const entry of cases) {
    const script = `import json\nprint(json.dumps({'decision':'block','reason':${JSON.stringify(entry.reason)}}))\n`;
    const h = await setup(t, { env: { PATH: "/usr/bin:/bin", FULLSEND_CANARY_TOKEN: canary },
      groups: [{ Phase: "PreToolUse", Tools: ["*"], Scripts: ["deny.py"] }],
    }, { ...sanitizers, "deny.py": script });
    await assert.rejects(h.before("read", {}), (error) => {
      assert.ok(error.message.includes(entry.expected), error.message);
      if (entry.absent) assert.ok(!error.message.includes(entry.absent), error.message);
      return true;
    });
  }
  const h = await setup(t, { groups: [{ Phase: "PreToolUse", Tools: ["*"], Scripts: ["deny.py"] }] },
    { "deny.py": "print('{\"decision\":\"block\",\"reason\":\"do not reflect this\"}')" });
  await assert.rejects(h.before("read", {}), (error) => {
    assert.equal(error.message, "[fullsend-hooks] blocked by deny.py");
    return true;
  });
  for (const redactor of [
    "print('untrusted stdout')\ndef redact_text(text): return text, []\n",
    "raise ImportError('untrusted interpreter error')\n",
    "import time\ntime.sleep(3)\ndef redact_text(text): return text, []\n",
  ]) {
    const failed = await setup(t, { groups: [{ Phase: "PreToolUse", Tools: ["*"], Scripts: ["deny.py"] }] },
      { ...sanitizers, "secret_redact_posttool.py": redactor,
        "deny.py": "print('{\"decision\":\"block\",\"reason\":\"do not reflect this\"}')" });
    await assert.rejects(failed.before("read", {}), (error) => {
      assert.equal(error.message, "[fullsend-hooks] blocked by deny.py");
      return true;
    });
  }
});

test("missing policy blocks all calls", async () => {
  process.env.FULLSEND_OPENCODE_HOOK_CONFIG = "not json";
  const hooks = await plugin({ directory: tmpdir() });
  await assert.rejects(hooks["tool.execute.before"]({ tool: "bash" }, { args: { command: "true" } }), /policy unavailable/);
});

test("failed-tool history is sanitized before a model request or withheld on failure", async (t) => {
  const source = "import json,sys\np=json.load(sys.stdin); v=p['tool_response']; v['output']=v['output'].replace('original-secret','masked')\nprint(json.dumps({'hookSpecificOutput':{'updatedToolOutput':v}}))\n";
  const h = await setup(t, { groups: [{ Phase: "PostToolUse", Tools: ["*"], Scripts: ["post.py"] }] }, { "post.py": source });
  const failed = { type: "tool", tool: "read", metadata: { raw: "original-secret" },
    state: { status: "error", input: {}, error: "original-secret", metadata: { raw: "original-secret" } } };
  const completed = { type: "tool", tool: "read", state: { status: "completed", output: "untouched" } };
  await h.messages([failed, completed]);
  assert.equal(failed.state.error, "masked");
  assert.deepEqual(failed.state.metadata, {});
  assert.deepEqual(failed.metadata, {});
  assert.equal(completed.state.output, "untouched");
  // Reprocessing retained history must remain safe.
  await h.messages([failed]);
  assert.equal(failed.state.error, "masked");

  const broken = await setup(t, { groups: [{ Phase: "PostToolUse", Tools: ["*"], Scripts: ["post.py"] }] }, { "post.py": "import sys; sys.exit(7)" });
  failed.state.error = "original-secret";
  await assert.rejects(broken.messages([failed]), /blocked by post.py/);
  assert.equal(failed.state.error, "Tool error withheld by Fullsend");
  const failedStage = await setup(t, { groups: [{ Phase: "PostToolUse", Tools: ["*"], Scripts: ["post.py"] }] },
    { "post.py": "print('{\"metadata\":{\"redact_error\":true}}')" });
  failed.state.error = "original-secret";
  await assert.rejects(failedStage.messages([failed]), /sanitizer failed/);
  assert.equal(failed.state.error, "Tool error withheld by Fullsend");
});

test("command environment does not import shell startup code", async (t) => {
  const h = await setup(t, { shellHome: "/sandbox", env: { PATH: "/usr/local/bin:/usr/bin:/bin" } });
  process.env["BASH_FUNC_gh%%"] = "() { echo unexpected; }";
  const output = { env: { BASH_ENV: "/sandbox/startup.sh", SHELLOPTS: "xtrace" } };
  try {
    await h.hooks["shell.env"]({}, output);
    assert.equal(output.env.BASH_ENV, "");
    assert.equal(output.env.SHELLOPTS, "");
    assert.equal(output.env["BASH_FUNC_gh%%"], "");
    assert.equal(output.env.HOME, "/sandbox");
    assert.equal(output.env.PATH, "/usr/local/bin:/usr/bin:/bin:" + path.join(h.workspace, "bin"));
  } finally {
    delete process.env["BASH_FUNC_gh%%"];
  }
});
