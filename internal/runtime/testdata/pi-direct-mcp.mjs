import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { childArgs, childTools } from "../pi_extension/fullsend-agent.js";

const [encoded, command, extension] = process.argv.slice(2);
const agent = JSON.parse(encoded);
assert.deepEqual(agent.tools, ["mcp__docs__*", "read"]);
const serverWidePersona = agent.personas.docs.tools.includes("mcp__docs__*");
assert.deepEqual(agent.personas.docs.tools, [serverWidePersona ? "mcp__docs__*" : "mcp__docs__search"]);
assert.equal(agent.declaredExtensions.length, 1);
assert.equal(agent.declaredExtensions[0].name, "docs-plugin");
// The sandbox uploader's path is mapped back to its local fixture for this
// offline process. All tool selections come from Bootstrap and Run rendering.
agent.declaredExtensions[0].path = extension;
// The fixture supplies an offline provider instead of sandbox inference adapters.
agent.extensions = [];
const home = mkdtempSync(join(tmpdir(), "fullsend-direct-"));
try {
  agent.sessionsDir = home;
  const selection = command.match(/--tools '([^']*)'/);
  assert.ok(selection, command);
  const env = {PATH: process.env.PATH, HOME: home, PI_CODING_AGENT_DIR: home, TEST_SNAPSHOT: join(home, "snapshot.json")};
  function run(args) {
    const result = spawnSync(process.env.FULLSEND_TEST_PI_BIN, args, {cwd: home, input: "search docs", encoding: "utf8", timeout: 45000, env});
    assert.equal(result.error, undefined);
    assert.equal(result.status, 0, result.stderr);
    return result;
  }
  const parent = run(["--print", "--mode", "json", "--no-approve", "--no-extensions", "-e", extension, "--model", "faux/faux-1", "--tools", selection[1]]);
  assert.match(parent.stderr, /EXECUTED:mcp__docs__search/);
  assert.match(parent.stderr, /EXECUTED:mcp__docs__write/);
  const {active, registered} = JSON.parse(readFileSync(env.TEST_SNAPSHOT, "utf8"));
  const tools = childTools(agent, "docs", active, registered);
  assert.deepEqual(tools, serverWidePersona ? ["mcp__docs__search", "mcp__docs__write"] : ["mcp__docs__search"]);
  const child = run(childArgs(agent, {seq: 1, modelSpec: "faux/faux-1", tools}));
  assert.match(child.stderr, /EXECUTED:mcp__docs__search/);
  if (serverWidePersona) assert.match(child.stderr, /EXECUTED:mcp__docs__write/);
  else assert.doesNotMatch(child.stderr, /EXECUTED:mcp__docs__write/);
} finally {
  rmSync(home, {recursive: true, force: true});
}
