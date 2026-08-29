// Unit tests for the fullsend pi Agent tool extension. Run with:
//   node --test internal/runtime/pi_extension/
// Children are a fake `pi` (a node script that prints a canned --mode json
// stream chosen by its prompt argument) or, where the test needs
// deterministic control over process lifetime, an injected spawn.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { EventEmitter } from "node:events";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { PassThrough } from "node:stream";
import { test } from "node:test";

import defaultExport, {
  AGENT_TOOL_PARAMETERS,
  CHILD_SYSTEM_NOTE,
  MAX_DESCRIPTION_BYTES,
  MAX_STDOUT_LINE_CHARS,
  RESULT_MAX_BYTES,
  childArgs,
  childEnv,
  childTools,
  createAgentTool,
  resolveModel,
} from "./fullsend-agent.js";

// FAKE_PI reads its prompt from stdin, the way real pi does in --print
// mode, and answers by prompt: "ok" prints a successful stream whose final
// text carries its argv, the prompt it received and
// FULLSEND_SUBAGENT_DEPTH; "fail" ends with stopReason error; "crash" exits
// 3; "noend" never emits agent_end; "hang" sleeps forever (the timeout test
// kills it); "hang-spawn" also leaves a detached grandchild behind, the way
// pi's bash tool does; "hugeline" prints one line far over the cap.
const FAKE_PI = `#!/usr/bin/env node
import { spawn } from "node:child_process";
import { writeFileSync } from "node:fs";
let stdin = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", (c) => { stdin += c; });
process.stdin.on("end", () => run(stdin.trim()));
process.stdin.resume();
function run(prompt) {
const usage = { input: 100, output: 20, cacheRead: 30, cacheWrite: 5, cost: { total: 0.25 } };
const line = (o) => process.stdout.write(JSON.stringify(o) + "\\n");
line({ type: "session", version: 3, id: "child" });
line({ type: "agent_start" });
if (prompt === "hang") { setTimeout(() => {}, 60_000); }
else if (prompt === "hang-spawn") {
  const kid = spawn(process.execPath, ["-e", "setTimeout(() => {}, 60000)"], { detached: true, stdio: "ignore" });
  kid.unref();
  writeFileSync(process.env.FAKE_PI_KID_FILE, String(kid.pid));
  process.on("SIGTERM", () => { try { process.kill(-kid.pid, "SIGKILL"); } catch {} process.exit(143); });
  setTimeout(() => {}, 60_000);
}
else if (prompt === "crash") { process.stderr.write("boom: provider exploded\\n"); process.exit(3); }
else if (prompt === "fail") {
  line({ type: "message_end", message: { role: "assistant", content: [], model: "m", provider: "p", usage, stopReason: "error", errorMessage: "quota exhausted" } });
  line({ type: "agent_end", messages: [{ role: "assistant", content: [], model: "m", provider: "p", usage, stopReason: "error", errorMessage: "quota exhausted" }] });
  line({ type: "agent_settled" });
} else if (prompt === "noend") {
  line({ type: "message_end", message: { role: "assistant", content: [{ type: "text", text: "partial" }], model: "m", provider: "p", usage, stopReason: "stop" } });
} else if (prompt.startsWith("BIG:")) {
  line({ type: "message_end", message: { role: "assistant", content: [{ type: "text", text: JSON.stringify({ big: prompt.length }) }], model: "m", provider: "p", usage, stopReason: "stop" } });
  line({ type: "agent_end", messages: [] });
} else if (prompt === "hugeline") {
  process.stdout.write(JSON.stringify({ type: "junk", pad: "z".repeat(${MAX_STDOUT_LINE_CHARS + 10}) }) + "\\n");
  line({ type: "message_end", message: { role: "assistant", content: [{ type: "text", text: "after the huge line" }], model: "m", provider: "p", usage, stopReason: "stop" } });
  line({ type: "agent_end", messages: [] });
} else {
  const text = JSON.stringify({ argv: process.argv.slice(2), prompt, depth: process.env.FULLSEND_SUBAGENT_DEPTH ?? null });
  line({ type: "message_end", message: { role: "assistant", content: [{ type: "toolCall", id: "c1", name: "read" }], model: "m", provider: "p", usage, stopReason: "toolUse" } });
  line({ type: "message_end", message: { role: "assistant", content: [{ type: "text", text: "  " + text + "  " }], model: "claude-opus-4-6", provider: "anthropic-vertex", usage, stopReason: "stop" } });
  line({ type: "agent_end", messages: [{ role: "assistant", content: [{ type: "text", text }], model: "claude-opus-4-6", provider: "anthropic-vertex", usage, stopReason: "stop" }] });
  line({ type: "agent_settled" });
}
}
`;

function fixture() {
  const dir = mkdtempSync(join(tmpdir(), "fullsend-agent-"));
  const piBin = join(dir, "fake-pi.mjs");
  writeFileSync(piBin, FAKE_PI, { mode: 0o755 });
  const manifest = {
    agentName: "review",
    tools: null,
    hooks: { groups: [], toolNames: {} },
    agent: {
      enabled: true,
      piBin,
      sessionsDir: join(dir, "sessions"),
      extensions: ["/usr/local/share/pi-extensions/anthropic-vertex", "/usr/local/share/pi-extensions/xai-vertex", "/sandbox/pi-config/fullsend-hooks.js"],
      models: {
        default: "anthropic-vertex/claude-opus-4-6",
        opus: "anthropic-vertex/claude-opus-4-6",
        sonnet: "anthropic-vertex/claude-sonnet-4-6",
        haiku: "anthropic-vertex/claude-haiku-4-5",
      },
      providerModels: { "google-vertex": ["gemini-3.7-flash", "gemini-3.5-flash", "gemini-2.5-pro"] },
      thinking: "medium",
      tools: ["read", "bash", "edit", "write", "grep", "find", "ls"],
      exploreTools: ["read", "grep", "find", "ls"],
      maxConcurrent: 4,
      timeoutSeconds: 900,
      usageFile: join(dir, "subagents", "usage.jsonl"),
    },
  };
  return { dir, manifest };
}

const quiet = { log: () => {} };

test("tool contract mirrors Claude Code's Agent tool", () => {
  assert.deepEqual(AGENT_TOOL_PARAMETERS.required, ["prompt"]);
  assert.deepEqual(Object.keys(AGENT_TOOL_PARAMETERS.properties).sort(), ["description", "model", "prompt", "run_in_background", "subagent_type"]);
  assert.equal(AGENT_TOOL_PARAMETERS.type, "object");
});

test("resolveModel: aliases, Claude ids and provider specs", () => {
  const { manifest } = fixture();
  const a = manifest.agent;
  const parent = "xai-vertex/xai/grok-4.6";
  assert.equal(resolveModel(a, "", parent), parent, "omitted → parent's model");
  assert.equal(resolveModel(a, undefined, ""), a.models.default, "no parent known → manifest default");
  assert.equal(resolveModel(a, "sonnet", parent), "anthropic-vertex/claude-sonnet-4-6");
  assert.equal(resolveModel(a, "Haiku", parent), "anthropic-vertex/claude-haiku-4-5", "aliases are case-insensitive");
  assert.equal(resolveModel(a, "claude-sonnet-4-6@default", parent), "anthropic-vertex/claude-sonnet-4-6", "fleet persona form: @default stripped, bare id matched");
  assert.equal(resolveModel(a, "claude-opus-4-6", parent), "anthropic-vertex/claude-opus-4-6");
  assert.equal(resolveModel(a, "anthropic/claude-sonnet-4-6", parent), "anthropic-vertex/claude-sonnet-4-6", "a direct-API provider prefix is translated to the sandbox provider");
  assert.equal(resolveModel(a, "anthropic-vertex/claude-sonnet-4-6", parent), "anthropic-vertex/claude-sonnet-4-6", "known provider passes through");
  assert.equal(resolveModel(a, "xai/grok-4.6", parent), "xai-vertex/xai/grok-4.6", "short Grok spec is normalized like the runner does");
  assert.equal(resolveModel(a, "xai-vertex/grok-4.6", parent), "xai-vertex/xai/grok-4.6");
  assert.equal(resolveModel(a, "google-vertex/gemini-3.7-flash", parent), "google-vertex/gemini-3.7-flash", "pi's built-in Vertex Gemini provider needs no extension");
  assert.throws(() => resolveModel(a, "anthropic/claude-sonnet-4-20250514", parent), /claude-sonnet-4-20250514.*opus, sonnet, haiku/s, "an invented Claude id is rejected, not passed through");
  assert.throws(() => resolveModel(a, "claude-sonnet-4-20250514", parent), /not available/);
  assert.throws(() => resolveModel(a, "openai/gpt-5", parent), /provider "openai"/, "a provider the run has no credentials for is rejected");
  const noXai = { ...a, extensions: ["/sandbox/pi-config/fullsend-hooks.js"], models: { default: "anthropic-vertex/claude-opus-4-6" } };
  assert.throws(() => resolveModel(noXai, "xai-vertex/xai/grok-4.6", "anthropic-vertex/claude-opus-4-6"), /provider "xai-vertex"/, "a provider whose extension is not in the manifest is rejected");
  assert.equal(
    resolveModel(noXai, "xai-vertex/xai/grok-4.6", "xai-vertex/xai/grok-4.6"),
    "xai-vertex/xai/grok-4.6",
    "naming the parent's own provider is never stricter than omitting model (which inherits the same spec)",
  );
  assert.equal(resolveModel(noXai, "openai/gpt-5-codex", "openai/gpt-5-codex"), "openai/gpt-5-codex", "a parent on a keyless-in-env provider can still dispatch on its own model");

  // pi's "<spec>:<level>" shorthand would override the manifest --thinking.
  assert.equal(resolveModel(a, "anthropic-vertex/claude-sonnet-4-6:high", parent), "anthropic-vertex/claude-sonnet-4-6");
  assert.equal(resolveModel(a, "sonnet:XHIGH", parent), "anthropic-vertex/claude-sonnet-4-6");
  assert.equal(resolveModel(a, ":max", parent), parent, "a bare level is not a model; fall back like an omitted spec");
  assert.throws(() => resolveModel(a, "sonnet:turbo", parent), /not available/, "an unknown suffix is not silently dropped");
});

test("resolveModel: an id is checked against a closed set, not just its provider prefix", () => {
  const { manifest } = fixture();
  const a = manifest.agent;
  const parent = "xai-vertex/xai/grok-4.6";

  // An allowed provider is not a licence to name any id under it: the spec
  // would reach the API as an unknown model instead of being corrected.
  assert.throws(
    () => resolveModel(a, "anthropic-vertex/claude-sonnet-4-20250514", parent),
    /is not a model this run serves on "anthropic-vertex"/,
    "an invented Claude id under the sandbox provider is rejected, not passed through",
  );
  assert.throws(() => resolveModel(a, "google-vertex/gemini-9-ultra", parent), /is not a model this run serves on "google-vertex"/);
  assert.throws(
    () => resolveModel({ ...a, providerModels: undefined }, "google-vertex/gemini-3.7-flash", parent),
    /is not a model this run serves/,
    "a manifest without providerModels serves no bare provider ids: it is written by the same Bootstrap as this file",
  );

  // What the closed set does accept.
  assert.equal(resolveModel(a, "google-vertex/gemini-2.5-pro", parent), "google-vertex/gemini-2.5-pro", "a catalog id the manifest lists");
  assert.equal(resolveModel(a, "GOOGLE-VERTEX/GEMINI-3.7-FLASH", parent), "google-vertex/gemini-3.7-flash", "matched case-insensitively, returned canonical");
  assert.equal(resolveModel(a, parent, parent), parent, "the parent's own spec, whatever provider it is on");
  assert.equal(resolveModel(a, "anthropic-vertex/claude-haiku-4-5", parent), "anthropic-vertex/claude-haiku-4-5", "a model-table entry");
});

test("resolveModel: a Grok spec is normalized and then checked against the closed set", () => {
  const { manifest } = fixture();
  // A parent that is not on Grok, so nothing here is served merely because
  // it is the parent's own spec: this exercises the providerModels path.
  const parent = "anthropic-vertex/claude-opus-4-6";
  const a = { ...manifest.agent, providerModels: { ...manifest.agent.providerModels, "xai-vertex": ["xai/grok-4.6"] } };

  for (const spec of ["xai/grok-4.6", "xai-vertex/grok-4.6", "xai-vertex/xai/grok-4.6", "XAI/GROK-4.6"]) {
    assert.equal(resolveModel(a, spec, parent), "xai-vertex/xai/grok-4.6", `every spelling lands on the three-segment spec: ${spec}`);
  }

  // The provider prefix is not a licence to name an id: an invented Grok id
  // would reach Vertex as an unknown model, which is what the closed set
  // exists to prevent for every other provider.
  assert.throws(
    () => resolveModel(a, "xai-vertex/xai/grok-invented", parent),
    /is not a model this run serves on "xai-vertex"/,
    "an invented Grok id under an allowed provider is rejected, not passed through",
  );
  assert.throws(() => resolveModel(a, "xai/grok-invented", parent), /is not a model this run serves on "xai-vertex"/);
  assert.throws(
    () => resolveModel({ ...a, providerModels: undefined }, "xai/grok-4.6", parent),
    /is not a model this run serves on "xai-vertex"/,
    "a manifest without providerModels serves no bare Grok id either",
  );

  // Without the extension the provider is refused before the id is looked
  // up, and it is named as the provider the spec really targets.
  const noXai = { ...a, extensions: ["/sandbox/pi-config/fullsend-hooks.js"] };
  for (const spec of ["xai/grok-4.6", "xai-vertex/xai/grok-4.6"]) {
    assert.throws(() => resolveModel(noXai, spec, parent), /provider "xai-vertex" is not available in this run/, spec);
  }
});

test("childTools: Explore is read-only, everything else is the parent's built-ins minus Agent/Task", () => {
  const { manifest } = fixture();
  assert.deepEqual(childTools(manifest.agent, "Explore"), ["read", "grep", "find", "ls"]);
  assert.deepEqual(childTools(manifest.agent, "explore"), ["read", "grep", "find", "ls"]);
  assert.deepEqual(childTools(manifest.agent, "general-purpose"), ["read", "bash", "edit", "write", "grep", "find", "ls"]);
  assert.deepEqual(childTools(manifest.agent, undefined), ["read", "bash", "edit", "write", "grep", "find", "ls"]);
  assert.deepEqual(childTools({ ...manifest.agent, tools: ["bash", "Agent", "Task", "read"] }, ""), ["bash", "read"]);
  assert.deepEqual(
    childTools({ ...manifest.agent, tools: ["read", "bash"] }, "Explore"),
    ["read"],
    "Explore is intersected with the parent's set: a child never reaches past its parent",
  );
  assert.deepEqual(childTools({ ...manifest.agent, tools: ["bash"] }, "Explore"), [], "no overlap leaves an empty allowlist");
});

test("childArgs mirrors the runner's pi command line, extensions in manifest order", () => {
  const { manifest } = fixture();
  const args = childArgs(manifest.agent, { seq: 3, modelSpec: "anthropic-vertex/claude-sonnet-4-6", tools: ["read", "grep"] });
  assert.deepEqual(args, [
    "--print", "--mode", "json", "--no-approve", "--no-extensions", "--no-prompt-templates", "--no-themes",
    "--session-dir", join(manifest.agent.sessionsDir, "agent-3"),
    "-e", "/usr/local/share/pi-extensions/anthropic-vertex",
    "-e", "/usr/local/share/pi-extensions/xai-vertex",
    "-e", "/sandbox/pi-config/fullsend-hooks.js",
    "--tools", "read,grep",
    "--model", "anthropic-vertex/claude-sonnet-4-6",
    "--thinking", "medium",
    "--append-system-prompt", CHILD_SYSTEM_NOTE,
  ]);
  assert.ok(!args.some((a) => a.startsWith("do it")), "the prompt is never in argv; it goes over stdin");
  const loaded = args.filter((_, i) => i > 0 && args[i - 1] === "-e");
  assert.ok(!loaded.some((a) => a.includes("fullsend-agent")), "children never get the Agent extension");

  // The parent's APPEND_SYSTEM.md must not reach a child: pi only discovers
  // it when no --append-system-prompt was given.
  assert.ok(!CHILD_SYSTEM_NOTE.includes("Agent calls in one message"));
  assert.match(CHILD_SYSTEM_NOTE, /sub-agent/);

  const empty = childArgs(manifest.agent, { seq: 4, modelSpec: "anthropic-vertex/claude-opus-4-6", tools: [] });
  assert.ok(empty.includes("--no-builtin-tools"), "an empty child tool list means no built-ins, like the runner does for the parent");
  assert.ok(!empty.includes("--tools"));
});

test("childEnv scrubs the provider credentials the child does not use", () => {
  const base = {
    PATH: "/usr/bin",
    ANTHROPIC_API_KEY: "sk-parent",
    ANTHROPIC_AUTH_TOKEN: "tok",
    ANTHROPIC_BASE_URL: "https://evil",
    ANTHROPIC_VERTEX_BASE_URL: "https://evil",
    ANTHROPIC_VERTEX_PROJECT_ID: "claude-proj",
    GOOGLE_CLOUD_PROJECT: "ambient-proj",
    XAI_API_KEY: "xai-key",
  };
  // A Claude child under a Grok parent: the runner's shell only scrubbed
  // for the parent's provider, so this is the extension's job.
  const claude = childEnv(base, "anthropic-vertex/claude-opus-4-6");
  assert.equal(claude.FULLSEND_SUBAGENT_DEPTH, "1");
  for (const k of ["ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_VERTEX_BASE_URL"]) {
    assert.ok(!(k in claude), `${k} is removed`);
  }
  assert.equal(claude.GOOGLE_CLOUD_PROJECT, "claude-proj", "pinned to the variable Claude on Vertex is driven by");
  assert.equal(claude.XAI_API_KEY, "xai-key", "another provider's key is left alone");

  const grok = childEnv(base, "xai-vertex/xai/grok-4.6");
  assert.ok(!("XAI_API_KEY" in grok), "the built-in xai provider must not shadow the extension");
  assert.equal(grok.XAI_VERTEX_PROJECT_ID, "claude-proj", "falls back the way pi_run.go does");
  assert.equal(grok.ANTHROPIC_API_KEY, "sk-parent", "not this child's provider, left alone");

  assert.equal(childEnv({ ...base, XAI_VERTEX_PROJECT_ID: "grok-proj" }, "xai-vertex/xai/grok-4.6").XAI_VERTEX_PROJECT_ID, "grok-proj", "an explicit value wins");
  const gemini = childEnv(base, "google-vertex/gemini-3.7-flash");
  assert.equal(gemini.GOOGLE_CLOUD_PROJECT, "ambient-proj", "a provider with no rules is untouched");
  assert.equal(base.FULLSEND_SUBAGENT_DEPTH, undefined, "the caller's env object is not mutated");
});

test("run: success returns the child's final text, trimmed, and records usage", async () => {
  const { manifest } = fixture();
  const logs = [];
  const tool = createAgentTool(manifest, { log: (l) => logs.push(l) });
  const res = await tool.run({ prompt: "ok", description: "unit child", model: "sonnet" }, { parentModel: "anthropic-vertex/claude-opus-4-6" });
  assert.equal(res.isError, false);
  assert.equal(res.stopReason, "stop");
  const payload = JSON.parse(res.text);
  assert.equal(payload.depth, "1", "children carry FULLSEND_SUBAGENT_DEPTH=1");
  assert.equal(payload.argv[0], "--print");
  assert.ok(payload.argv.includes("--model") && payload.argv[payload.argv.indexOf("--model") + 1] === "anthropic-vertex/claude-sonnet-4-6");
  assert.equal(payload.prompt, "ok", "the prompt arrives on stdin, not in argv");
  assert.ok(!payload.argv.includes("ok"), "and never in argv");
  assert.equal(payload.argv[payload.argv.indexOf("--append-system-prompt") + 1], CHILD_SYSTEM_NOTE, "the child gets its own system note, not the parent's APPEND_SYSTEM.md");
  assert.equal(payload.argv[payload.argv.indexOf("--session-dir") + 1], join(manifest.agent.sessionsDir, "agent-1"));
  assert.equal(payload.argv[payload.argv.indexOf("--thinking") + 1], "medium");

  const usage = readFileSync(manifest.agent.usageFile, "utf8").trim().split("\n").map((l) => JSON.parse(l));
  assert.equal(usage.length, 1);
  assert.equal(usage[0].seq, 1);
  assert.equal(usage[0].model, "anthropic-vertex/claude-sonnet-4-6");
  assert.equal(usage[0].provider, "anthropic-vertex");
  assert.equal(usage[0].description, "unit child");
  assert.equal(usage[0].stopReason, "stop");
  assert.equal(usage[0].isError, false);
  assert.deepEqual(usage[0].usage, { input: 200, output: 40, cacheRead: 60, cacheWrite: 10, cost: 0.5 }, "usage sums every assistant message");
  assert.ok(typeof usage[0].durationMs === "number" && usage[0].durationMs >= 0);
  assert.match(usage[0].startedAt, /^\d{4}-\d{2}-\d{2}T/);

  assert.equal(logs.length, 2);
  assert.match(logs[0], /^\[fullsend-agent\] #1 anthropic-vertex\/claude-sonnet-4-6 start "unit child"$/);
  assert.match(logs[1], /^\[fullsend-agent\] #1 done \d+ms stop$/);
});

test("run: error stopReason → isError with the child's message", async () => {
  const { manifest } = fixture();
  const tool = createAgentTool(manifest, quiet);
  const res = await tool.run({ prompt: "fail" }, {});
  assert.equal(res.isError, true);
  assert.equal(res.stopReason, "error");
  assert.match(res.error, /quota exhausted/);
  const usage = JSON.parse(readFileSync(manifest.agent.usageFile, "utf8").trim());
  assert.equal(usage.isError, true);
  assert.equal(usage.stopReason, "error");
  assert.equal(usage.model, manifest.agent.models.default, "omitted model with no parent model known → manifest default");
});

test("run: non-zero exit and a stream without agent_end are errors", async () => {
  const { manifest } = fixture();
  const tool = createAgentTool(manifest, quiet);
  const crashed = await tool.run({ prompt: "crash" }, {});
  assert.equal(crashed.isError, true);
  assert.match(crashed.error, /exited 3/);
  assert.match(crashed.error, /boom: provider exploded/, "stderr tail is surfaced");

  const noend = await tool.run({ prompt: "noend" }, {});
  assert.equal(noend.isError, true);
  assert.match(noend.error, /no agent_end/);
  assert.equal(noend.text, "partial", "whatever text arrived is still returned");
});

test("run: timeout stops the child and reports isError", async () => {
  const { manifest } = fixture();
  manifest.agent.timeoutSeconds = 0.3;
  const tool = createAgentTool(manifest, quiet);
  const started = Date.now();
  const res = await tool.run({ prompt: "hang" }, {});
  assert.equal(res.isError, true);
  assert.match(res.error, /timed out after 0.3s/);
  assert.equal(res.stopReason, "timeout");
  assert.ok(Date.now() - started < 5000, "did not wait for the child's own timer");
  assert.throws(() => process.kill(res.pid, 0), { code: "ESRCH" }, "the child is gone");
});

test("run: a timed-out child gets SIGTERM, so its detached grandchildren die with it", async (t) => {
  if (process.platform === "win32") return t.skip("POSIX signals");
  const { dir, manifest } = fixture();
  manifest.agent.timeoutSeconds = 0.3;
  const kidFile = join(dir, "kid.pid");
  // The fake pi mimics pi's bash tool: a `detached` grandchild in its own
  // process group, reaped from its own SIGTERM handler
  // (dist/modes/print-mode.js -> killTrackedDetachedChildren). SIGKILL to
  // the child's group would never reach it.
  const tool = createAgentTool(manifest, { ...quiet, env: { ...process.env, FAKE_PI_KID_FILE: kidFile } });
  const res = await tool.run({ prompt: "hang-spawn" }, {});
  assert.equal(res.stopReason, "timeout");
  const kid = Number(readFileSync(kidFile, "utf8"));
  assert.ok(kid > 0);
  for (let i = 0; i < 100; i++) {
    try {
      process.kill(kid, 0);
    } catch (err) {
      assert.equal(err.code, "ESRCH");
      return;
    }
    await new Promise((r) => setTimeout(r, 50));
  }
  assert.fail(`grandchild ${kid} survived the child's death`);
});

test("run: a prompt that looks like a flag reaches the child verbatim", async () => {
  const { manifest } = fixture();
  const tool = createAgentTool(manifest, quiet);
  // pi's argv parser reads these as an unknown flag, an unknown option
  // (a startup error) and a file argument respectively, so none of them
  // can go on the command line unterminated — and a "--" terminator would
  // not rescue the "@" one, which stays a file argument after it.
  for (const prompt of ["--no-approve then review this", "- a leading dash", "@/etc/passwd"]) {
    const res = await tool.run({ prompt }, {});
    assert.equal(res.isError, false, `prompt ${JSON.stringify(prompt)} failed: ${res.error}`);
    assert.equal(JSON.parse(res.text).prompt, prompt);
  }
});

test("run: a prompt larger than the kernel's argv limit still reaches the child", async () => {
  const { manifest } = fixture();
  const tool = createAgentTool(manifest, quiet);
  // 256 KiB is past the per-argument cap that makes spawn fail E2BIG on
  // Linux; a context package for a reviewer sub-agent easily reaches it.
  const prompt = "BIG:" + "x".repeat(256 * 1024);
  const res = await tool.run({ prompt }, {});
  assert.equal(res.isError, false, res.error);
  assert.equal(JSON.parse(res.text).big, prompt.length, "the child received every byte");
});

test("run: a stdout line over the cap is dropped, not buffered, and parsing resumes", async () => {
  const { manifest } = fixture();
  const logs = [];
  const tool = createAgentTool(manifest, { log: (l) => logs.push(l) });
  const res = await tool.run({ prompt: "hugeline" }, {});
  assert.equal(res.text, "after the huge line", "the events after the oversized line are still parsed");
  assert.ok(logs.some((l) => /dropped 1 stdout line/.test(l)));
});

test("run: the usage record's description is capped", async () => {
  const { manifest } = fixture();
  const tool = createAgentTool(manifest, quiet);
  await tool.run({ prompt: "ok", description: "d".repeat(MAX_DESCRIPTION_BYTES * 3) }, {});
  const rec = JSON.parse(readFileSync(manifest.agent.usageFile, "utf8").trim());
  assert.equal(Buffer.byteLength(rec.description), MAX_DESCRIPTION_BYTES);
  // The whole record has to stay under PIPE_BUF for concurrent appends to
  // be atomic.
  assert.ok(Buffer.byteLength(JSON.stringify(rec) + "\n") < 4096);
});

test("run: a manifest rewritten after load stops the next dispatch", async () => {
  const { dir, manifest } = fixture();
  const path = join(dir, "manifest.json");
  writeFileSync(path, JSON.stringify(manifest));
  const bytes = readFileSync(path);
  const manifestSum = createHash("sha256").update(bytes).digest("hex");
  const { spawn, children } = fakeSpawn();
  const tool = createAgentTool(JSON.parse(bytes.toString("utf8")), { ...quiet, spawn, manifestPath: path, manifestSum });

  const first = tool.run({ prompt: "p1" }, {});
  await new Promise((r) => setImmediate(r));
  assert.equal(children.length, 1, "the unmodified manifest dispatches");
  children[0].child.finish(okStream("one"));
  assert.equal((await first).text, "one");

  // The manifest names the binary children run, their -e list (the hook
  // adapter among them) and their tool allowlist, and it sits in a dir the
  // agent can write to: the launch guard checked it once, minutes ago.
  writeFileSync(path, JSON.stringify({ ...manifest, agent: { ...manifest.agent, extensions: [], tools: ["bash"] } }));
  const res = await tool.run({ prompt: "p2" }, {});
  assert.equal(res.isError, true);
  assert.equal(res.error, "manifest changed since load; refusing to dispatch");
  assert.equal(res.stopReason, "rejected");
  assert.equal(children.length, 1, "nothing was spawned on the rewritten configuration");
  assert.equal(tool.inFlight(), 0);

  // Restoring the bytes restores dispatch: the check is on content, not on
  // having seen a write.
  writeFileSync(path, bytes);
  const third = tool.run({ prompt: "p3" }, {});
  await new Promise((r) => setImmediate(r));
  assert.equal(children.length, 2, "and the refused dispatch did not leak its slot");
  children[1].child.finish(okStream("three"));
  assert.equal((await third).text, "three");
});

test("run: a manifest that vanishes after load stops the next dispatch", async () => {
  const { dir, manifest } = fixture();
  const path = join(dir, "gone.json");
  writeFileSync(path, JSON.stringify(manifest));
  const manifestSum = createHash("sha256").update(readFileSync(path)).digest("hex");
  const { spawn, children } = fakeSpawn();
  const tool = createAgentTool(manifest, { ...quiet, spawn, manifestPath: path, manifestSum });
  rmSync(path);
  const res = await tool.run({ prompt: "p" }, {});
  assert.equal(res.isError, true);
  assert.match(res.error, /cannot re-read .*gone\.json before dispatching/);
  assert.equal(children.length, 0);
});

test("run: a hook adapter rewritten after load stops the next dispatch", async () => {
  const { dir, manifest } = fixture();
  const adapter = join(dir, "fullsend-hooks.js");
  const bytes = "// the hook adapter Bootstrap wrote\n";
  writeFileSync(adapter, bytes);
  const agent = {
    ...manifest.agent,
    // The vendored provider extension carries no digest: it is root-owned
    // and read-only in the image. Only the config-dir file does.
    extensions: ["/usr/local/share/pi-extensions/anthropic-vertex", adapter],
    extensionDigests: { [adapter]: createHash("sha256").update(bytes).digest("hex") },
  };
  const { spawn, children } = fakeSpawn();
  const tool = createAgentTool({ ...manifest, agent }, { ...quiet, spawn });

  const first = tool.run({ prompt: "p1" }, {});
  await new Promise((r) => setImmediate(r));
  assert.equal(children.length, 1, "the adapter Bootstrap wrote dispatches");
  children[0].child.finish(okStream("one"));
  assert.equal((await first).text, "one");

  // The launch guard checked this file once, minutes ago. A parent with
  // `write` can replace it mid-iteration, and a replacement simply omits
  // the adapter's own manifest-digest check — so every child dispatched
  // after it would come up with no hooks in it.
  writeFileSync(adapter, "// no hooks here\n");
  const res = await tool.run({ prompt: "p2" }, {});
  assert.equal(res.isError, true);
  assert.equal(res.error, "hook adapter changed since load; refusing to dispatch");
  assert.equal(res.stopReason, "rejected");
  assert.equal(children.length, 1, "nothing was spawned against the rewritten adapter");
  assert.equal(tool.inFlight(), 0);

  // Restoring the bytes restores dispatch: the check is on content, not on
  // having seen a write. maxConcurrent dispatches at once then prove the
  // refusal handed its slot back rather than keeping it.
  writeFileSync(adapter, bytes);
  const rest = [];
  for (let i = 0; i < agent.maxConcurrent; i++) rest.push(tool.run({ prompt: `p${i}` }, {}));
  await new Promise((r) => setImmediate(r));
  assert.equal(children.length, 1 + agent.maxConcurrent, "the refused dispatch leaked no slot");
  for (let i = 1; i <= agent.maxConcurrent; i++) children[i].child.finish(okStream(`done-${i}`));
  assert.deepEqual((await Promise.all(rest)).map((r) => r.text), ["done-1", "done-2", "done-3", "done-4"]);
});

test("run: a hook adapter that vanishes after load stops the next dispatch", async () => {
  const { dir, manifest } = fixture();
  const adapter = join(dir, "gone-hooks.js");
  writeFileSync(adapter, "// adapter\n");
  const agent = {
    ...manifest.agent,
    extensions: [adapter],
    extensionDigests: { [adapter]: createHash("sha256").update(readFileSync(adapter)).digest("hex") },
  };
  const { spawn, children } = fakeSpawn();
  const tool = createAgentTool({ ...manifest, agent }, { ...quiet, spawn });
  rmSync(adapter);
  const res = await tool.run({ prompt: "p" }, {});
  assert.equal(res.isError, true);
  assert.match(res.error, /cannot re-read .*gone-hooks\.js before dispatching/);
  assert.equal(children.length, 0);
});

test("run: model rejection is an error before anything is spawned", async () => {
  const { manifest } = fixture();
  const spawned = [];
  const tool = createAgentTool(manifest, { ...quiet, spawn: (...a) => { spawned.push(a); throw new Error("must not spawn"); } });
  const res = await tool.run({ prompt: "ok", model: "anthropic/claude-sonnet-4-20250514" }, {});
  assert.equal(res.isError, true);
  assert.match(res.error, /opus, sonnet, haiku/);
  assert.equal(spawned.length, 0);
  assert.ok(!existsSync(manifest.agent.usageFile), "no usage line for a dispatch that never ran");
});

// fakeSpawn returns children the test completes by hand, so concurrency can
// be observed deterministically.
function fakeSpawn() {
  const children = [];
  const spawn = (bin, args, opts) => {
    const child = new EventEmitter();
    child.pid = 40000 + children.length;
    child.stdin = new PassThrough();
    child.stdout = new PassThrough();
    child.stderr = new PassThrough();
    child.exitCode = null;
    child.signalCode = null;
    child.signals = [];
    child.kill = (sig) => { child.signals.push(sig); return true; };
    child.stdinText = "";
    child.stdin.on("data", (c) => { child.stdinText += c; });
    child.finish = (lines, code = 0) => {
      child.exitCode = code;
      for (const l of lines) child.stdout.write(JSON.stringify(l) + "\n");
      child.stdout.end();
      child.stderr.end();
      child.emit("close", code, null);
    };
    children.push({ bin, args, opts, child });
    return child;
  };
  return { spawn, children };
}

const okStream = (text) => [
  { type: "agent_start" },
  { type: "message_end", message: { role: "assistant", content: [{ type: "text", text }], model: "m", provider: "p", usage: { input: 1, output: 1, cacheRead: 0, cacheWrite: 0, cost: { total: 0.01 } }, stopReason: "stop" } },
  { type: "agent_end", messages: [] },
  { type: "agent_settled" },
];

test("run: at most maxConcurrent children run at once; the rest queue", async () => {
  const { manifest } = fixture();
  manifest.agent.maxConcurrent = 2;
  const { spawn, children } = fakeSpawn();
  const tool = createAgentTool(manifest, { ...quiet, spawn });
  const runs = [1, 2, 3, 4].map((i) => tool.run({ prompt: `p${i}`, subagent_type: i === 4 ? "Explore" : undefined }, {}));
  await new Promise((r) => setImmediate(r));
  assert.equal(children.length, 2, "two spawned, two waiting");
  assert.equal(tool.inFlight(), 2);
  children[0].child.finish(okStream("one"));
  await runs[0];
  await new Promise((r) => setImmediate(r));
  assert.equal(children.length, 3, "a slot freed → the third starts");
  children[1].child.finish(okStream("two"));
  children[2].child.finish(okStream("three"));
  await Promise.all([runs[1], runs[2]]);
  await new Promise((r) => setImmediate(r));
  assert.equal(children.length, 4);
  children[3].child.finish(okStream("four"));
  const results = await Promise.all(runs);
  assert.deepEqual(results.map((r) => r.text), ["one", "two", "three", "four"]);
  assert.equal(tool.inFlight(), 0);

  // The Explore child got the read-only set; the others the parent's built-ins.
  const toolsOf = (c) => c.args[c.args.indexOf("--tools") + 1];
  assert.equal(toolsOf(children[3]), "read,grep,find,ls");
  assert.equal(toolsOf(children[0]), "read,bash,edit,write,grep,find,ls");
  assert.equal(children[0].opts.env.FULLSEND_SUBAGENT_DEPTH, "1");
  assert.deepEqual(children[0].opts.stdio, ["pipe", "pipe", "pipe"], "stdin is a pipe: it carries the prompt");
  assert.equal(children[0].child.stdinText, "p1", "the prompt is written and the pipe closed");
  assert.equal(children[0].opts.detached, undefined, "children share the parent's process group, so a killed parent takes them with it");
  assert.equal(children[0].bin, manifest.agent.piBin);
});

test("run: a queued dispatch aborted before it starts never spawns and still settles", async () => {
  const { manifest } = fixture();
  manifest.agent.maxConcurrent = 1;
  const { spawn, children } = fakeSpawn();
  const tool = createAgentTool(manifest, { ...quiet, spawn });
  const first = tool.run({ prompt: "p1" }, {});
  const ac = new AbortController();
  const queued = tool.run({ prompt: "p2" }, { signal: ac.signal });
  await new Promise((r) => setImmediate(r));
  assert.equal(children.length, 1, "the second is queued");
  ac.abort();
  const res = await queued;
  assert.equal(res.isError, true);
  assert.equal(res.stopReason, "aborted");
  assert.equal(children.length, 1, "an aborted dispatch never spawns");
  children[0].child.finish(okStream("one"));
  assert.equal((await first).text, "one");
  assert.equal(tool.inFlight(), 0);
  // The slot accounting survived the early exit.
  const after = tool.run({ prompt: "p3" }, {});
  await new Promise((r) => setImmediate(r));
  assert.equal(children.length, 2, "the semaphore did not leak the aborted dispatch's slot");
  children[1].child.finish(okStream("three"));
  assert.equal((await after).text, "three");
});

test("run: aborting a queued dispatch behind another waiter does not over-admit", async () => {
  const { manifest } = fixture();
  manifest.agent.maxConcurrent = 2;
  const { spawn, children } = fakeSpawn();
  const tool = createAgentTool(manifest, { ...quiet, spawn });
  const first = tool.run({ prompt: "p1" }, {});
  const second = tool.run({ prompt: "p2" }, {});
  const ac = new AbortController();
  const abortedQueued = tool.run({ prompt: "p3" }, { signal: ac.signal });
  const stillQueued = tool.run({ prompt: "p4" }, {});
  await new Promise((r) => setImmediate(r));
  assert.equal(children.length, 2, "two running, two queued");

  // Taking the head waiter out of the queue must not claim a slot for it:
  // its own release() would then hand the slot to the next waiter without
  // giving it back, and that waiter would run as a third concurrent child.
  ac.abort();
  const abortedRes = await abortedQueued;
  assert.equal(abortedRes.stopReason, "aborted");
  await new Promise((r) => setImmediate(r));
  assert.equal(children.length, 2, "the dispatch behind it must not start while both slots are busy");
  assert.equal(tool.inFlight(), 2);

  children[0].child.finish(okStream("one"));
  assert.equal((await first).text, "one");
  await new Promise((r) => setImmediate(r));
  assert.equal(children.length, 3, "the freed slot goes to the waiter that is still queued");
  children[1].child.finish(okStream("two"));
  children[2].child.finish(okStream("four"));
  assert.equal((await second).text, "two");
  assert.equal((await stillQueued).text, "four");
  assert.equal(tool.inFlight(), 0);

  // Both slots are free again: two fresh dispatches start at once.
  const more = [tool.run({ prompt: "p5" }, {}), tool.run({ prompt: "p6" }, {})];
  await new Promise((r) => setImmediate(r));
  assert.equal(children.length, 5, "the aborted dispatch leaked no slot");
  children[3].child.finish(okStream("five"));
  children[4].child.finish(okStream("six"));
  assert.deepEqual((await Promise.all(more)).map((r) => r.text), ["five", "six"]);
});

test("shutdown does not leak the slots of the waiters it drains", async () => {
  const { manifest } = fixture();
  manifest.agent.maxConcurrent = 2;
  const { spawn, children } = fakeSpawn();
  const tool = createAgentTool(manifest, { ...quiet, spawn, killGraceMs: 5 });
  const running = [tool.run({ prompt: "p1" }, {}), tool.run({ prompt: "p2" }, {})];
  const queued = [tool.run({ prompt: "p3" }, {}), tool.run({ prompt: "p4" }, {})];
  await new Promise((r) => setImmediate(r));
  assert.equal(children.length, 2);
  tool.shutdown();
  const drained = await Promise.all(queued);
  assert.deepEqual(drained.map((r) => r.stopReason), ["aborted", "aborted"]);
  assert.equal(children.length, 2, "a dispatch queued at shutdown never spawns");
  children[0].child.finish([], null);
  children[1].child.finish([], null);
  await Promise.all(running);
  assert.equal(tool.inFlight(), 0);
});

test("run: an abort while the child is running terminates it", async () => {
  const { manifest } = fixture();
  const { spawn, children } = fakeSpawn();
  const tool = createAgentTool(manifest, { ...quiet, spawn, killGraceMs: 5 });
  const ac = new AbortController();
  const p = tool.run({ prompt: "slow" }, { signal: ac.signal });
  await new Promise((r) => setImmediate(r));
  ac.abort();
  assert.deepEqual(children[0].child.signals, ["SIGTERM"], "SIGTERM first: pi reaps its own detached bash children and exits 143");
  await new Promise((r) => setTimeout(r, 30));
  assert.deepEqual(children[0].child.signals, ["SIGTERM", "SIGKILL"], "escalated after the grace period");
  children[0].child.finish([], 143);
  const res = await p;
  assert.equal(res.isError, true);
  assert.equal(res.stopReason, "aborted");
  assert.match(res.error, /aborted/);
});

test("run: an already-aborted signal is refused before anything is spawned", async () => {
  const { manifest } = fixture();
  const { spawn, children } = fakeSpawn();
  const tool = createAgentTool(manifest, { ...quiet, spawn });
  const res = await tool.run({ prompt: "p" }, { signal: AbortSignal.abort() });
  assert.equal(res.isError, true);
  assert.equal(res.stopReason, "aborted");
  assert.equal(children.length, 0);
});

test("run: result text is capped at 64 KB with a marker", async () => {
  const { manifest } = fixture();
  const { spawn, children } = fakeSpawn();
  const tool = createAgentTool(manifest, { ...quiet, spawn });
  const p = tool.run({ prompt: "big" }, {});
  await new Promise((r) => setImmediate(r));
  children[0].child.finish(okStream("x".repeat(RESULT_MAX_BYTES + 100)));
  const res = await p;
  assert.equal(res.isError, false);
  assert.ok(res.text.endsWith("\n[truncated]"));
  assert.ok(Buffer.byteLength(res.text) <= RESULT_MAX_BYTES + "\n[truncated]".length);
});

test("shutdown stops in-flight children, fails their calls and settles the queue", async () => {
  const { manifest } = fixture();
  manifest.agent.maxConcurrent = 1;
  const { spawn, children } = fakeSpawn();
  const tool = createAgentTool(manifest, { ...quiet, spawn, killGraceMs: 5 });
  const running = tool.run({ prompt: "slow" }, {});
  const queued = tool.run({ prompt: "queued" }, {});
  await new Promise((r) => setImmediate(r));
  assert.equal(children.length, 1);
  tool.shutdown();
  assert.deepEqual(children[0].child.signals, ["SIGTERM"]);

  // A queued dispatch must not hang forever: nothing would call release()
  // for it once the in-flight children are gone.
  const queuedRes = await queued;
  assert.equal(queuedRes.isError, true);
  assert.equal(queuedRes.stopReason, "aborted");
  assert.equal(children.length, 1, "a dispatch queued at shutdown never spawns");

  children[0].child.finish([], null);
  const res = await running;
  assert.equal(res.isError, true);
  assert.match(res.error, /shut down|killed/);
});

function registerWith(manifestPath, env = {}) {
  const tools = [];
  const events = {};
  const lines = [];
  const origError = console.error;
  console.error = (l) => lines.push(l);
  const saved = { ...process.env };
  process.env.FULLSEND_PI_MANIFEST = manifestPath;
  delete process.env.FULLSEND_SUBAGENT_DEPTH;
  Object.assign(process.env, env);
  try {
    defaultExport({ registerTool: (d) => tools.push(d), on: (ev, fn) => { events[ev] = fn; } });
  } finally {
    console.error = origError;
    for (const k of Object.keys(process.env)) if (!(k in saved)) delete process.env[k];
    Object.assign(process.env, saved);
  }
  return { tools, events, lines };
}

test("default export registers Agent and Task with one handler and a shutdown hook", () => {
  const { dir, manifest } = fixture();
  const path = join(dir, "manifest.json");
  writeFileSync(path, JSON.stringify(manifest));
  const { tools, events, lines } = registerWith(path);
  assert.deepEqual(tools.map((t) => t.name), ["Agent", "Task"]);
  assert.equal(tools[0].execute, tools[1].execute);
  assert.deepEqual(tools[0].parameters, AGENT_TOOL_PARAMETERS);
  assert.ok(typeof events.session_shutdown === "function");
  assert.deepEqual(lines, []);
});

test("default export: missing manifest, disabled block, or a set depth registers nothing and logs once", () => {
  const { dir, manifest } = fixture();
  let r = registerWith(join(dir, "nonexistent.json"));
  assert.equal(r.tools.length, 0);
  assert.equal(r.lines.length, 1);
  assert.match(r.lines[0], /cannot read manifest/);

  const disabled = join(dir, "disabled.json");
  writeFileSync(disabled, JSON.stringify({ ...manifest, agent: { ...manifest.agent, enabled: false } }));
  r = registerWith(disabled);
  assert.equal(r.tools.length, 0);
  assert.equal(r.lines.length, 1);
  assert.match(r.lines[0], /not enabled/);

  const noBlock = join(dir, "noblock.json");
  writeFileSync(noBlock, JSON.stringify({ ...manifest, agent: undefined }));
  r = registerWith(noBlock);
  assert.equal(r.tools.length, 0);
  assert.equal(r.lines.length, 1);

  const ok = join(dir, "manifest.json");
  writeFileSync(ok, JSON.stringify(manifest));
  r = registerWith(ok, { FULLSEND_SUBAGENT_DEPTH: "1" });
  assert.equal(r.tools.length, 0, "a child never registers the tool (recursion is refused)");
  assert.equal(r.lines.length, 1);
  assert.match(r.lines[0], /depth/i);
});

test("execute throws on a failed child so pi marks the result isError", async () => {
  const { dir, manifest } = fixture();
  const path = join(dir, "manifest.json");
  writeFileSync(path, JSON.stringify(manifest));
  const { tools } = registerWith(path);
  const ctx = { model: { provider: "anthropic-vertex", id: "claude-opus-4-6" } };
  await assert.rejects(tools[0].execute("call-1", { prompt: "fail" }, undefined, undefined, ctx), /quota exhausted/);
  const ok = await tools[0].execute("call-2", { prompt: "ok", run_in_background: true }, undefined, undefined, ctx);
  assert.equal(ok.content[0].type, "text");
  const payload = JSON.parse(ok.content[0].text);
  assert.equal(payload.argv[payload.argv.indexOf("--model") + 1], "anthropic-vertex/claude-opus-4-6", "omitted model → the parent's active model from ctx");
  assert.equal(ok.details.seq, 2);
  assert.equal(ok.details.model, "anthropic-vertex/claude-opus-4-6");
});

// --- Real pi ---------------------------------------------------------------
// The prompt-delivery contract is a claim about pi's own argv parser, so one
// test checks it against the real binary. CI has no pi, so it is gated:
//   npm install --prefix /tmp/pi @earendil-works/pi-coding-agent@0.84.4 \
//     --ignore-scripts
//   FULLSEND_TEST_PI_BIN=/tmp/pi/node_modules/.bin/pi \
//     node --test internal/runtime/pi_extension/
// It runs offline: the model is pointed at a base URL nothing listens on, so
// the turn fails at the first request — after pi has parsed argv, read stdin
// and recorded the initial user message, which is all this asserts.
const REAL_PI = process.env.FULLSEND_TEST_PI_BIN;
const OFFLINE_MODEL = "openai/gpt-5";

function realPiEnv(home) {
  return {
    PATH: process.env.PATH,
    HOME: home,
    PI_CODING_AGENT_DIR: home,
    OPENAI_API_KEY: "sk-not-a-real-key",
    OPENAI_BASE_URL: "http://127.0.0.1:1",
  };
}

function runRealPi(args, { cwd, home, stdin }) {
  return new Promise((resolve) => {
    const child = spawn(REAL_PI, args, { cwd, env: realPiEnv(home), stdio: ["pipe", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    child.stdout.on("data", (c) => { stdout += c; });
    child.stderr.on("data", (c) => { stderr += c; });
    child.stdin.on("error", () => {});
    child.stdin.end(stdin ?? "");
    child.on("close", (code) => resolve({ code, stdout, stderr }));
  });
}

// userText returns the text of the first user message in a --mode json stream.
function userText(stdout) {
  for (const line of stdout.split("\n")) {
    if (line.trim() === "") continue;
    let evt;
    try {
      evt = JSON.parse(line);
    } catch {
      continue;
    }
    if (evt.type === "message_start" && evt.message?.role === "user") {
      return (evt.message.content ?? []).filter((b) => b.type === "text").map((b) => b.text).join("");
    }
  }
  return undefined;
}

test("real pi: the prompt goes over stdin because argv cannot carry it", { timeout: 180_000 }, async (t) => {
  if (!REAL_PI) return t.skip("set FULLSEND_TEST_PI_BIN to a pi 0.84.4 binary");
  const { manifest } = fixture();
  const home = join(manifest.agent.sessionsDir, "..", "pi-home");
  mkdirSync(home, { recursive: true });
  // The vendored provider extensions only exist in the sandbox image.
  const agent = { ...manifest.agent, extensions: [] };

  // Negative control: the same prompt as a positional argument is what the
  // finding is about. pi does accept a "--" terminator, but it is not a way
  // out: an "@"-prefixed positional after it is still read as a file
  // argument, and the kernel's argv cap applies either way (the 200 KiB
  // prompt below). This control uses the childArgs form the runner
  // actually builds, which has no terminator in it.
  const positional = await runRealPi(
    [...childArgs(agent, { seq: 90, modelSpec: OFFLINE_MODEL, tools: ["read"] }), "- a leading dash"],
    { cwd: home, home },
  );
  assert.equal(positional.code, 1);
  assert.match(positional.stderr, /Unknown option/);

  // Prompts pi's parser would eat as an option, an unknown flag or a file
  // argument, plus one past the kernel's argv limit.
  const prompts = ["- a leading dash", "--no-approve then review this", "@/etc/passwd", "x".repeat(200 * 1024)];
  for (const [i, prompt] of prompts.entries()) {
    const args = childArgs(agent, { seq: 100 + i, modelSpec: OFFLINE_MODEL, tools: ["read"] });
    const res = await runRealPi(args, { cwd: home, home, stdin: prompt });
    const label = JSON.stringify(prompt.slice(0, 32));
    assert.doesNotMatch(res.stderr, /Unknown option/, `pi rejected ${label} as an option`);
    assert.equal(res.code, 0, `pi exited ${res.code} on ${label}: ${res.stderr}`);
    assert.equal(userText(res.stdout), prompt, `pi did not receive ${label} as the initial message`);
  }
});
