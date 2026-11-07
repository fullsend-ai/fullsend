import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { childArgs, childTools } from "./fullsend-agent.js";

// Offline real-pi regression: a scripted provider asks for both the allowed
// and forbidden tool. No network, credentials, or organization-specific data.
test("real pi: MCP persona executes only the concrete intersection", { skip: !process.env.FULLSEND_TEST_PI_BIN, timeout: 120_000 }, () => {
  const home = mkdtempSync(join(tmpdir(), "fullsend-mcp-child-"));
  const provider = join(home, "provider.js");
  const docs = join(home, "docs.js");
  writeFileSync(provider, `
import { fauxAssistantMessage, fauxProvider, fauxToolCall } from "@earendil-works/pi-ai";
export default function(pi) {
  const faux = fauxProvider({provider: "faux", models: [{id: "faux-1"}]});
  faux.setResponses([
    fauxAssistantMessage([fauxToolCall("mcp__docs__search", {}), fauxToolCall("mcp__docs__write", {}), fauxToolCall("mcp__docs__hidden", {})], {stopReason: "toolUse"}),
    fauxAssistantMessage("done"),
  ]);
  pi.registerProvider(faux.provider);
}
`);
  const snapshot = join(home, "active.json");
  writeFileSync(docs, `
import {writeFileSync} from "node:fs";
export default function(pi) {
  pi.on("session_start", () => writeFileSync(${JSON.stringify(snapshot)}, JSON.stringify(pi.getActiveTools())));
  for (const name of ["mcp__docs__search", "mcp__docs__write", "mcp__docs__hidden"]) {
    pi.registerTool({ name, label: name, description: name, defaultActive: name !== "mcp__docs__hidden",
      parameters: {type: "object", properties: {}},
      async execute() { console.error("EXECUTED:" + name); return {content: [{type: "text", text: "docs result"}]}; }
    });
  }
}
`);
  const agent = { sessionsDir: home, extensions: [provider], declaredExtensions: [{path: docs}], tools: ["mcp__docs__*"], personas: {docs: {tools: ["mcp__docs__search"]}} };
  for (const [index, mode] of ["explicit", "default", "extensions-only", "empty"].entries()) {
    const empty = mode === "empty";
    agent.toolMode = empty ? "explicit" : mode;
    agent.tools = mode === "default" ? ["read", "bash"] : mode === "extensions-only" ? [] : ["mcp__docs__*"];
    agent.personas.docs.tools = empty ? [] : ["mcp__docs__search"];
    // Ask a real parent for its active set; do not substitute the full registry.
    const parentArgs = ["--print", "--mode", "json", "--no-approve", "--no-extensions", "-e", provider, "-e", docs, "--model", "faux/faux-1"];
    if (mode === "extensions-only") parentArgs.push("--no-builtin-tools");
    else if (mode === "explicit" || empty) parentArgs.push("--tools", "mcp__docs__search,mcp__docs__write");
    const parent = spawnSync(process.env.FULLSEND_TEST_PI_BIN, parentArgs, {
      cwd: home, input: "search docs", encoding: "utf8", timeout: 45_000,
      env: {PATH: process.env.PATH, HOME: home, PI_CODING_AGENT_DIR: home},
    });
    assert.equal(parent.error, undefined);
    assert.equal(parent.status, 0, parent.stderr);
    const active = JSON.parse(readFileSync(snapshot, "utf8"));
    assert.ok(active.includes("mcp__docs__search"));
    assert.ok(!active.includes("mcp__docs__hidden"));
    // Even a persona explicitly requesting the inactive tool cannot get it.
    if (!empty) agent.personas.docs.tools.push("mcp__docs__hidden");
    const tools = childTools(agent, "docs", active);
    const args = childArgs(agent, {seq: index + 1, modelSpec: "faux/faux-1", tools});
    const result = spawnSync(process.env.FULLSEND_TEST_PI_BIN, args, {
      cwd: home, input: "search docs", encoding: "utf8", timeout: 45_000,
      env: {PATH: process.env.PATH, HOME: home, PI_CODING_AGENT_DIR: home},
    });
    assert.equal(result.error, undefined);
    assert.equal(result.status, 0, result.stderr);
    assert.ok(!result.stderr.includes("EXECUTED:mcp__docs__write"), result.stderr);
    assert.ok(!result.stderr.includes("EXECUTED:mcp__docs__hidden"), result.stderr);
    assert.equal(result.stderr.includes("EXECUTED:mcp__docs__search"), !empty, result.stderr);
    assert.match(result.stdout, /tool_execution_end/);
  }
});
