import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
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
    fauxAssistantMessage([fauxToolCall("mcp__docs__search", {}), fauxToolCall("mcp__docs__write", {})], {stopReason: "toolUse"}),
    fauxAssistantMessage("done"),
  ]);
  pi.registerProvider(faux.provider);
}
`);
  writeFileSync(docs, `
export default function(pi) {
  for (const name of ["mcp__docs__search", "mcp__docs__write"]) {
    pi.registerTool({ name, label: name, description: name,
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
    const tools = childTools(agent, "docs", ["mcp__docs__search", "mcp__docs__write"]);
    const args = childArgs(agent, {seq: index + 1, modelSpec: "faux/faux-1", tools});
    const result = spawnSync(process.env.FULLSEND_TEST_PI_BIN, args, {
      cwd: home, input: "search docs", encoding: "utf8", timeout: 45_000,
      env: {PATH: process.env.PATH, HOME: home, PI_CODING_AGENT_DIR: home},
    });
    assert.equal(result.error, undefined);
    assert.equal(result.status, 0, result.stderr);
    assert.ok(!result.stderr.includes("EXECUTED:mcp__docs__write"), result.stderr);
    assert.equal(result.stderr.includes("EXECUTED:mcp__docs__search"), !empty, result.stderr);
    assert.match(result.stdout, /tool_execution_end/);
  }
});
