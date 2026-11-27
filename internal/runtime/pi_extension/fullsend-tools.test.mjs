import assert from "node:assert/strict";
import { test } from "node:test";
import { childArgs, childTools } from "./fullsend-agent.js";
import { initializeSelector, selectTools } from "./fullsend-tools.js";

const native = (name = "inspect_record", path = "/vetted/inspect_record/index.js") => ({ name, exposure: "direct", sourceInfo: { path } });
const manifest = (tools = ["inspect_record"]) => ({ strictTools: true, tools, nativeTools: ["inspect_record"], extensions: [{ path: "/vetted/inspect_record" }] });

test("exact native tool selected without unrelated builtin or extension tools", () => {
  assert.deepEqual(selectTools(manifest(), [native(), native("unrelated"), { name: "bash" }]), ["inspect_record"]);
});
test("empty is explicit and default selection is not an authority proof", () => {
  assert.deepEqual(selectTools(manifest([]), [native()]), []);
  assert.throws(() => selectTools({ tools: [] }, []), /strict tool declaration/);
});
test("missing, ambiguous and non-vetted registrations fail", () => {
  for (const registry of [[], [native(), native()], [native("inspect_record", "/vetted/inspect_record-other/index.js")]]) {
    assert.throws(() => selectTools(manifest(), registry));
  }
  assert.throws(() => selectTools(manifest(), [{ ...native(), exposure: "hidden" }]), /hidden/);
});
test("activation and hook enforcement share canonical names", () => {
  const handlers = {}; let active = [];
  const pi = { on: (name, fn) => { handlers[name] = fn; }, getAllTools: () => [native(), { name: "read" }], setActiveTools: (names) => { active = names; }, getActiveTools: () => active };
  initializeSelector(pi, manifest(["inspect_record", "read"]), { fatal: (message) => { throw new Error(message); } });
  handlers.session_start();
  assert.deepEqual(active, ["inspect_record", "read"]);
  assert.equal(handlers.tool_call({ toolName: "inspect_record" }), undefined);
  assert.equal(handlers.tool_call({ toolName: "bash" }).block, true);
  active.push("bash"); handlers.before_agent_start();
  assert.deepEqual(active, ["inspect_record", "read"]);
});
test("a runtime ignoring exact activation fails", () => {
  const handlers = {};
  const pi = { on: (name, fn) => { handlers[name] = fn; }, getAllTools: () => [native()], setActiveTools: () => {}, getActiveTools: () => ["bash", "inspect_record"] };
  initializeSelector(pi, manifest(), { fatal: (message) => { throw new Error(message); } });
  assert.throws(() => handlers.session_start(), /did not enforce/);
});

// Offline acceptance against the pinned Pi binary. Inspection exits
// on session_start, before any inference request or credential use.
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const realPi = process.env.FULLSEND_TEST_PI_BIN;
const selectorPath = fileURLToPath(new URL("./fullsend-tools.js", import.meta.url));

async function actualSelection({ tools = ["inspect_record"], duplicate = false, missing = false, wrongOrigin = false, exercise = false, deny = false, child = null, corruptManifest = false } = {}) {
  const dir = mkdtempSync(join(tmpdir(), "fullsend-exact-pi-"));
  try {
    const home = join(dir, "home"); mkdirSync(home);
    const plugin = join(dir, "plugin"); mkdirSync(plugin);
    const tool = (name) => `pi.registerTool({name:${JSON.stringify(name)},label:'fixture',description:'offline fixture',parameters:{type:'object',properties:{}},execute:async()=>{console.error('EXECUTED='+${JSON.stringify(name)});return {content:[{type:'text',text:'fixture'}]}}});`;
    writeFileSync(join(plugin, "index.js"), `export default function(pi){${tool(missing ? "other" : "inspect_record")}${tool("unrelated")}${tool("bash")}${tool("edit")}}`);
    const paths = [plugin];
    if (duplicate) {
      const second = join(dir, "second"); mkdirSync(second);
      writeFileSync(join(second, "index.js"), `export default function(pi){${tool("inspect_record")}}`);
      paths.push(second);
    }
    const provider = join(dir, "provider.js");
    writeFileSync(provider, `import {fauxAssistantMessage,fauxProvider,fauxToolCall} from "@earendil-works/pi-ai";
export default function(pi){
 const faux=fauxProvider({provider:"faux",models:[{id:"fixture"}]});
 faux.setResponses([fauxAssistantMessage([${["inspect_record", "unrelated", "bash", "edit"].map((name) => `fauxToolCall(${JSON.stringify(name)},{})`).join(",")}],{stopReason:"toolUse"}),fauxAssistantMessage("done")]);
 pi.registerProvider(faux.provider);
 pi.on("before_agent_start",()=>{console.error("MODEL_START");});

}`);
    const inspector = join(dir, "inspector.js");
    writeFileSync(inspector, 'import {writeSync} from "node:fs"; export default function(pi){pi.on("session_start",()=>{writeSync(1,"FULLSEND_SELECTION="+JSON.stringify(pi.getActiveTools())+"\\n");EXIT});}'.replace("EXIT", exercise ? "" : "process.exit(0);"));
    const config = { strictTools: true, tools, nativeTools: tools.filter((name) => name === "inspect_record"), extensions: paths.map((path) => ({ path: wrongOrigin ? join(dir, "different") : path })) };
    if (deny) {
      writeFileSync(join(dir, "deny.py"), 'import json, sys\npayload=json.load(sys.stdin)\nprint(json.dumps({"decision":"block","reason":"fixture denial: "+payload["tool_name"]}))\n');
      config.hooks = { dir, toolNames: {}, groups: [{phase:"PreToolUse",tools:["inspect_record"],scripts:["deny.py"]}] };
    }
    const hooksPath = fileURLToPath(new URL("./fullsend-hooks.js", import.meta.url));
    const manifestPath = join(dir, "manifest.json"); const bytes = JSON.stringify(config); writeFileSync(manifestPath, bytes);
    const args = ["--print", "--mode", "json", "--no-approve", "--no-extensions", "--no-prompt-templates", "--no-themes", "--no-session", "--model", exercise ? "faux/fixture" : "openai/gpt-5", "--api-key", "offline-not-a-key", "--tools", tools.join(","), ...(exercise ? ["-e", provider] : []), ...(deny ? ["-e", hooksPath] : []), ...paths.flatMap((path) => ["-e", path]), "-e", selectorPath, "-e", inspector];
    if (child) {
      const agent = { sessionsDir: join(dir, "sessions"), extensions: exercise ? [provider] : [], declaredExtensions: paths.map((path) => ({path})), toolSelector: selectorPath, mcpResourceTools: ["list_mcp_resources", "list_mcp_resource_templates", "read_mcp_resource"] };
      args.splice(0, args.length, ...childArgs(agent, {seq: 1, modelSpec: exercise ? "faux/fixture" : "openai/gpt-5", ...child}), "--api-key", "offline-not-a-key", "-e", inspector);
    }
    if (exercise) args.push("exercise fixture");
    return await new Promise((resolveResult, reject) => {
      const child = spawn(resolve(realPi), args, { cwd: dir, env: { PATH: process.env.PATH, HOME: home, PI_CODING_AGENT_DIR: home, PI_OFFLINE: "1", PI_TELEMETRY: "0", FULLSEND_PI_HOOK_PYTHON: "/usr/bin/python3", FULLSEND_PI_HOOK_PATH: "/usr/bin:/bin", FULLSEND_PI_MANIFEST: manifestPath, FULLSEND_PI_MANIFEST_SHA256: corruptManifest ? "incorrect-digest" : createHash("sha256").update(bytes).digest("hex") }, stdio: ["pipe", "pipe", "pipe"] });
      let stdout = "", stderr = "";
      child.stdout.on("data", (bytes) => { stdout += bytes; }); child.stderr.on("data", (bytes) => { stderr += bytes; });
      const timeout = setTimeout(() => { child.kill("SIGKILL"); reject(new Error("offline Pi startup timed out")); }, 20000);
      child.on("error", reject); child.on("close", (code) => { clearTimeout(timeout); resolveResult({ code, stdout, stderr }); });
      child.stdin.end();
    });
  } finally { rmSync(dir, { recursive: true, force: true }); }
}

test("real Pi exposes only the selected native tool", { skip: !realPi }, async () => {
  const result = await actualSelection();
  assert.equal(result.code, 0, result.stderr);
  assert.match(result.stdout, /FULLSEND_SELECTION=\["inspect_record"\]/);
});
test("real Pi refuses missing, ambiguous and non-vetted selections before inference", { skip: !realPi }, async () => {
  for (const config of [{ missing: true }, { duplicate: true }, { wrongOrigin: true }]) {
    const result = await actualSelection({...config, exercise: true});
    assert.doesNotMatch(result.stderr, /MODEL_START|EXECUTED=/);
    assert.notEqual(result.code, 0, result.stderr);
    assert.doesNotMatch(result.stdout, /FULLSEND_SELECTION=/);
    assert.match(result.stderr, /\[fullsend-tools\]|Failed to load extension/);
  }
});
test("real Pi explicit empty tool set exposes no builtins or unrelated extensions", { skip: !realPi }, async () => {
  const result = await actualSelection({ tools: [] });
  assert.equal(result.code, 0, result.stderr);
  assert.match(result.stdout, /FULLSEND_SELECTION=\[\]/);
});


test("real Pi executes only the selected tool, including in child mode", { skip: !realPi }, async () => {
 for (const child of [null, { tools: ["inspect_record"], activeTools: ["inspect_record"] }]) {
  const result = await actualSelection({ exercise: true, child });
  assert.equal(result.code, 0, result.stderr);
  assert.match(result.stderr, /EXECUTED=inspect_record/);
  assert.doesNotMatch(result.stderr, /EXECUTED=(unrelated|bash|edit)/);
 }
});

test("real Pi hook denial prevents the selected native tool executing", { skip: !realPi }, async () => {
 const result = await actualSelection({ exercise: true, deny: true });
 assert.equal(result.code, 0, result.stderr);
 assert.doesNotMatch(result.stderr, /EXECUTED=/);
 assert.match(result.stdout, /fixture denial: inspect_record/);
});

test("real Pi empty selections prevent all tool execution", { skip: !realPi }, async () => {
 for (const child of [null, {tools: [], activeTools: []}]) {
  const result = await actualSelection({ tools: [], exercise: true, child });
  assert.equal(result.code, 0, result.stderr);
  assert.doesNotMatch(result.stderr, /EXECUTED=/);
 }
});

test("real Pi refuses a modified manifest before inference", { skip: !realPi }, async () => {
 const result = await actualSelection({ exercise: true, corruptManifest: true });
 assert.equal(result.code, 92, result.stderr);
 assert.doesNotMatch(result.stderr, /MODEL_START|EXECUTED=/);
});

test("MCP patterns preserve late, deferred and active tool permissions", () => {
 const handlers = {}; let active = ["mcp__docs__search"]; let registry = [];
 const pi = {on:(name,fn)=>{handlers[name]=fn;},getAllTools:()=>registry,getActiveTools:()=>active,setActiveTools:(names)=>{active=names;}};
 initializeSelector(pi, manifest(["mcp__docs__*"]), {fatal:(message)=>{throw new Error(message);}});
 handlers.session_start();
 assert.deepEqual(active,["mcp__docs__search"]);
 registry=[{name:"mcp__docs__search",exposure:"direct"},{name:"mcp__docs__fetch",exposure:"deferred"}];
 handlers.before_agent_start();
 assert.deepEqual(active,["mcp__docs__search"]);
 assert.equal(handlers.tool_call({toolName:"mcp__docs__fetch"}),undefined);
 assert.equal(handlers.tool_call({toolName:"mcp__other__fetch"}).block,true);
});


test("child callable-only tools stay inactive after late activation between turns", () => {
 const handlers = {}; let active = ["mcp__docs__search"];
 const pi = {on:(name,fn)=>{handlers[name]=fn;},getAllTools:()=>[],getActiveTools:()=>active,setActiveTools:(names)=>{active=[...names];}};
 initializeSelector(pi, {...manifest(["mcp__docs__search","mcp__docs__fetch"]),activeTools:["mcp__docs__search"]}, {fatal:(message)=>{throw new Error(message);}});
 handlers.agent_start();
 active.push("mcp__docs__fetch");
 handlers.turn_start();
 assert.deepEqual(active,["mcp__docs__search"]);
 assert.equal(handlers.tool_call({toolName:"mcp__docs__fetch"}),undefined);
});


test("native child selections intersect parent and persona before launch", () => {
 const agent = {toolMode:"explicit",tools:["inspect_record","read"],personas:{allowed:{tools:["inspect_record","unrelated"]},forbidden:{tools:["unrelated"]}}};
 assert.deepEqual(childTools(agent,"allowed",["inspect_record","read"]),["inspect_record"]);
 assert.deepEqual(childTools(agent,"forbidden",["inspect_record","read"]),[]);
 assert.deepEqual(childTools(agent,"Explore",["inspect_record","read"]),["read"]);
});
