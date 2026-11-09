import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { childArgs, childTools } from "./fullsend-agent.js";

// Exercise the same nested execution API used by codemode, with both MCP
// exposures that stay callable even when inactive. The unguarded control
// proves this fixture reaches the pi 1.5.0 registration loophole.
test("real pi: non-MCP persona cannot call inherited MCP through codemode", { skip: !process.env.FULLSEND_TEST_PI_BIN, timeout: 120_000 }, () => {
  const home = mkdtempSync(join(tmpdir(), "fullsend-mcp-deny-"));
  const extension = join(home, "fixture.js");
  writeFileSync(extension, `
import { fauxAssistantMessage, fauxProvider, fauxToolCall } from "@earendil-works/pi-ai";
export default function(pi) {
  const faux = fauxProvider({provider: "faux", models: [{id: "faux-1"}]});
  faux.setResponses([
    fauxAssistantMessage([fauxToolCall("codemode", {})], {stopReason: "toolUse"}),
    fauxAssistantMessage("done"),
  ]);
  pi.registerProvider(faux.provider);
  for (const name of ["mcp__docs__codemode", "mcp__docs__deferred", "list_mcp_resources", "list_mcp_resource_templates", "read_mcp_resource"]) {
    const exposure = name.endsWith("deferred") ? "deferred" : "codemode";
    pi.registerTool({name, label: name, description: name, exposure, defaultActive: false,
      parameters: {type: "object", properties: {}},
      async execute() { console.error("EXECUTED:" + name); return {content: [{type: "text", text: "docs"}]}; }
    });
  }
  pi.registerTool({name: "codemode", label: "codemode", description: "nested execution probe",
    parameters: {type: "object", properties: {}},
    async execute(id, params, signal, update, ctx) {
      console.error("PROBED:codemode");
      for (const name of ["mcp__docs__codemode", "mcp__docs__deferred", "list_mcp_resources", "list_mcp_resource_templates", "read_mcp_resource"]) {
        try { await ctx.executeTool(name, {}); } catch {}
      }
      return {content: [{type: "text", text: "done"}]};
    }
  });
}
`);
  const agent = {sessionsDir: home, declaredExtensions: [{path: extension}], toolMode: "explicit",
    tools: ["codemode", "mcp__docs__search"], personas: {docs: {tools: ["codemode"]}}};
  const tools = childTools(agent, "docs", ["codemode", "mcp__docs__search"]);
  assert.deepEqual(tools, ["codemode"]);
  for (const selected of [undefined, "list_mcp_resources", "list_mcp_resource_templates", "read_mcp_resource"]) {
    for (const guarded of [false, true]) {
      const args = childArgs(agent, {seq: guarded ? 2 : 1, modelSpec: "faux/faux-1", tools: selected ? [...tools, selected] : tools});
      // Control recreates the pre-fix argv, proving the nested tools execute.
      if (!guarded) args.splice(args.indexOf("--exclude-tools"), 2);
      const result = spawnSync(process.env.FULLSEND_TEST_PI_BIN, args, {
        cwd: home, input: "probe nested tools", encoding: "utf8", timeout: 45_000,
        env: {PATH: process.env.PATH, HOME: home, PI_CODING_AGENT_DIR: home},
      });
      assert.equal(result.error, undefined);
      assert.equal(result.status, 0, result.stderr);
      assert.match(result.stderr, /PROBED:codemode/);
      for (const name of ["mcp__docs__codemode", "mcp__docs__deferred", "list_mcp_resources", "list_mcp_resource_templates", "read_mcp_resource"]) {
        assert.equal(result.stderr.includes("EXECUTED:" + name), !guarded || name === selected, result.stderr);
      }
    }
  }
});

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
    fauxAssistantMessage([fauxToolCall("codemode", {})], {stopReason: "toolUse"}),
    fauxAssistantMessage("done"),
  ]);
  pi.registerProvider(faux.provider);
}
`);
  const snapshot = join(home, "active.json");
  writeFileSync(docs, `
import {writeFileSync} from "node:fs";
export default function(pi) {
  pi.on("session_start", () => writeFileSync(${JSON.stringify(snapshot)}, JSON.stringify({active: pi.getActiveTools(), registered: pi.getAllTools().map(({name, exposure}) => ({name, exposure}))})));
  for (const name of ["mcp__docs__search", "mcp__docs__write", "mcp__docs__hidden"]) {
    pi.registerTool({ name, label: name, description: name, exposure: name === "mcp__docs__hidden" ? "direct" : process.env.TEST_EXPOSURE, defaultActive: name !== "mcp__docs__hidden",
      parameters: {type: "object", properties: {}},
      async execute() { console.error("EXECUTED:" + name); return {content: [{type: "text", text: "docs result"}]}; }
    });
  }
  pi.registerTool({name: "codemode", label: "codemode", description: "nested calls",
    parameters: {type: "object", properties: {}},
    async execute(id, params, signal, update, ctx) {
      for (const name of ["mcp__docs__search", "mcp__docs__write", "mcp__docs__hidden"]) {
        try { await ctx.executeTool(name, {}); } catch {}
      }
      return {content: [{type: "text", text: "done"}]};
    }
  });
}
`);
  const agent = { sessionsDir: home, extensions: [provider], declaredExtensions: [{path: docs}], tools: ["mcp__docs__*"], personas: {docs: {tools: ["mcp__docs__search"]}} };
  for (const exposure of ["direct", "codemode", "deferred"]) {
  for (const [index, mode] of ["explicit", "default", "extensions-only", "empty"].entries()) {
    const empty = mode === "empty";
    agent.toolMode = empty ? "explicit" : mode;
    agent.tools = mode === "default" ? ["read", "bash"] : mode === "extensions-only" ? [] : ["codemode", "mcp__docs__*"];
    agent.personas.docs.tools = empty ? [] : ["codemode", "mcp__docs__search"];
    // Ask a real parent for its active set; do not substitute the full registry.
    const parentArgs = ["--print", "--mode", "json", "--no-approve", "--no-extensions", "-e", provider, "-e", docs, "--model", "faux/faux-1"];
    if (mode === "extensions-only") parentArgs.push("--no-builtin-tools");
    else if (mode === "explicit" || empty) parentArgs.push("--tools", "codemode,mcp__docs__search,mcp__docs__write");
    const parent = spawnSync(process.env.FULLSEND_TEST_PI_BIN, parentArgs, {
      cwd: home, input: "search docs", encoding: "utf8", timeout: 45_000,
      env: {PATH: process.env.PATH, HOME: home, PI_CODING_AGENT_DIR: home, TEST_EXPOSURE: exposure},
    });
    assert.equal(parent.error, undefined);
    assert.equal(parent.status, 0, parent.stderr);
    const {active, registered} = JSON.parse(readFileSync(snapshot, "utf8"));
    assert.equal(registered.find((t) => t.name === "mcp__docs__search").exposure, exposure);
    if (mode === "default" || mode === "extensions-only") assert.equal(active.includes("mcp__docs__search"), exposure === "direct", `${mode}/${exposure}`);
    assert.ok(!active.includes("mcp__docs__hidden"));
    // Even a persona explicitly requesting the inactive tool cannot get it.
    if (!empty) agent.personas.docs.tools.push("mcp__docs__hidden");
    const tools = childTools(agent, "docs", active, registered);
    const args = childArgs(agent, {seq: index + 1, modelSpec: "faux/faux-1", tools});
    const result = spawnSync(process.env.FULLSEND_TEST_PI_BIN, args, {
      cwd: home, input: "search docs", encoding: "utf8", timeout: 45_000,
      env: {PATH: process.env.PATH, HOME: home, PI_CODING_AGENT_DIR: home, TEST_EXPOSURE: exposure},
    });
    assert.equal(result.error, undefined);
    assert.equal(result.status, 0, result.stderr);
    assert.ok(!result.stderr.includes("EXECUTED:mcp__docs__write"), result.stderr);
    assert.ok(!result.stderr.includes("EXECUTED:mcp__docs__hidden"), result.stderr);
    assert.equal(result.stderr.includes("EXECUTED:mcp__docs__search"), !empty, result.stderr);
    assert.match(result.stdout, /tool_execution_end/);
  }
  }
});
