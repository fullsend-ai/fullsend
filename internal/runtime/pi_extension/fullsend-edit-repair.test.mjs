// Tests for fullsend-edit-repair.js. Run with: node --test internal/runtime/pi_extension/
//
// The extension imports pi's packages, which only pi's loader provides. The
// unit tests copy it into a temp dir next to stub packages so Node resolves
// the imports to them; the stubs record what pi's own prepareArguments
// receives. The "real pi" tests at the end load the extension itself through
// a real pi, driven by pi's scripted faux model (see REAL_PI below).
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

const dir = mkdtempSync(join(tmpdir(), "fullsend-edit-repair-"));

function stubPackage(name, source) {
  const pkg = join(dir, "node_modules", ...name.split("/"));
  mkdirSync(pkg, { recursive: true });
  writeFileSync(join(pkg, "package.json"), JSON.stringify({ name, type: "module", exports: "./index.js" }));
  writeFileSync(join(pkg, "index.js"), source);
}

// A repairing parser with the one behaviour the extension relies on: raw
// control characters inside string literals are escaped before parsing.
stubPackage("@earendil-works/pi-ai", `
export function parseJsonWithRepair(text) {
  let out = "", inStr = false, esc = false;
  for (const ch of text) {
    if (!inStr) { if (ch === '"') inStr = true; out += ch; continue; }
    if (esc) { esc = false; out += ch; continue; }
    if (ch === "\\\\") { esc = true; out += ch; continue; }
    if (ch === '"') { inStr = false; out += ch; continue; }
    const code = ch.charCodeAt(0);
    out += code < 0x20 ? "\\\\u" + code.toString(16).padStart(4, "0") : ch;
  }
  return JSON.parse(out);
}
`);

stubPackage("@earendil-works/pi-coding-agent", `
export const created = [];
export function createEditToolDefinition(cwd) {
  const received = [];
  const def = {
    name: "edit",
    label: "edit",
    parameters: { type: "object" },
    prepareArguments(args) { received.push(args); return { prepared: args }; },
    async execute() { return { content: [] }; },
    received,
    cwd,
  };
  created.push(def);
  return def;
}
`);

copyFileSync(new URL("./fullsend-edit-repair.js", import.meta.url), join(dir, "fullsend-edit-repair.js"));
const mod = await import(pathToFileURL(join(dir, "fullsend-edit-repair.js")).href);
const stubs = await import(pathToFileURL(join(dir, "node_modules/@earendil-works/pi-coding-agent/index.js")).href);
const { parseJsonWithRepair } = await import(pathToFileURL(join(dir, "node_modules/@earendil-works/pi-ai/index.js")).href);
const { repairEditsArgument, createRepairedEditTool } = mod;
const repair = (input) => repairEditsArgument(input, parseJsonWithRepair);

test("a stringified edits array with raw control characters is parsed (pi#8521)", () => {
  // A real newline and tab inside a string value: what a multi-line code
  // edit looks like once a model serializes it, and what bare JSON.parse —
  // pi's own repair — throws on.
  const raw = '[{"oldText":"a","newText":"line one\nline\ttwo"}]';
  assert.throws(() => JSON.parse(raw), /control character/i);
  const { args, repairs } = repair({ path: "f.go", edits: raw });
  assert.deepEqual(args, { path: "f.go", edits: [{ oldText: "a", newText: "line one\nline\ttwo" }] });
  assert.deepEqual(repairs, ["stringified edits"]);
});

test("array items that are JSON strings are parsed (pi#8962)", () => {
  const { args, repairs } = repair({
    path: "f.go",
    edits: ['{"oldText":"a","newText":"b"}', { oldText: "c", newText: "d" }, '{"oldText":"e","newText":"x\ny"}'],
  });
  assert.deepEqual(args.edits, [
    { oldText: "a", newText: "b" },
    { oldText: "c", newText: "d" },
    { oldText: "e", newText: "x\ny" },
  ]);
  assert.deepEqual(repairs, ["2 stringified edit item(s)"]);
});

test("a well-formed stringified array is left to pi, which already parses it", () => {
  const input = { path: "f.go", edits: '[{"oldText":"a","newText":"b"}]' };
  const { args, repairs } = repair(input);
  assert.equal(args, input, "same object: nothing to count as a repair");
  assert.deepEqual(repairs, []);
});

test("a well-formed stringified array of stringified items has its items parsed", () => {
  const { args, repairs } = repair({ path: "f.go", edits: JSON.stringify(['{"oldText":"a","newText":"b"}']) });
  assert.deepEqual(args.edits, [{ oldText: "a", newText: "b" }]);
  assert.deepEqual(repairs, ["1 stringified edit item(s)"]);
});

test("input it cannot improve is returned untouched, so pi reports the real error", () => {
  const cases = [
    undefined,
    null,
    "edit this",
    [1, 2],
    { path: "f.go" },
    { path: "f.go", oldText: "a", newText: "b" },
    { path: "f.go", edits: [{ oldText: "a", newText: "b" }] },
    { path: "f.go", edits: "not json {{{" },
    { path: "f.go", edits: ["not json {{{"] },
    { path: "f.go", edits: ["42", "[1]", "null"] },
  ];
  for (const input of cases) {
    const { args, repairs } = repair(input);
    assert.equal(args, input, JSON.stringify(input));
    assert.deepEqual(repairs, [], JSON.stringify(input));
  }
});

test("the repair does not mutate the caller's arguments", () => {
  const edits = ['{"oldText":"a","newText":"b"}'];
  const input = { path: "f.go", edits };
  repair(input);
  assert.equal(input.edits, edits);
  assert.equal(typeof edits[0], "string");
});

test("createRepairedEditTool is pi's tool with only prepareArguments wrapped", () => {
  const logs = [];
  const tool = createRepairedEditTool("/repo", { parse: parseJsonWithRepair, log: (m) => logs.push(m) });
  const inner = stubs.created.at(-1);
  assert.equal(inner.cwd, "/repo");
  assert.equal(tool.name, "edit");
  assert.equal(tool.execute, inner.execute, "execution stays pi's");
  assert.equal(tool.parameters, inner.parameters, "so does the schema pi validates against");

  const out = tool.prepareArguments({ path: "f.go", edits: '[{"oldText":"a","newText":"1\n2"}]' });
  assert.deepEqual(inner.received.at(-1), { path: "f.go", edits: [{ oldText: "a", newText: "1\n2" }] },
    "pi's own preparation sees the repaired shape");
  assert.deepEqual(out, { prepared: inner.received.at(-1) }, "and its result is what pi validates");
  assert.deepEqual(logs, ["[fullsend-edit-repair] repaired stringified edits for f.go"]);

  tool.prepareArguments({ path: "f.go", edits: [{ oldText: "a", newText: "b" }] });
  assert.equal(logs.length, 1, "a call pi accepts as is is not logged");
});

test("an inner tool without prepareArguments still gets the repaired arguments", () => {
  const tool = createRepairedEditTool("/repo", {
    createTool: () => ({ name: "edit", parameters: {} }),
    parse: parseJsonWithRepair,
    log: () => {},
  });
  assert.deepEqual(tool.prepareArguments({ path: "f", edits: ['{"oldText":"a","newText":"b"}'] }),
    { path: "f", edits: [{ oldText: "a", newText: "b" }] });
});

test("the extension registers one tool, named edit, for the process cwd", () => {
  const registered = [];
  mod.default({ registerTool: (t) => registered.push(t) });
  assert.equal(registered.length, 1);
  assert.equal(registered[0].name, "edit");
  assert.equal(stubs.created.at(-1).cwd, process.cwd());
});

// Real pi, opt-in like the Agent extension's real-pi test:
//   npm install --prefix /tmp/pi \
//     @earendil-works/pi-coding-agent@"$(sed -n 's/^ARG PI_VERSION=//p' images/sandbox/Containerfile)" \
//     --ignore-scripts
//   FULLSEND_TEST_PI_BIN=/tmp/pi/node_modules/.bin/pi \
//     node --test internal/runtime/pi_extension/
// Offline: a test-only extension registers pi's faux provider, scripted to
// make one edit call with a malformed `edits` argument and then stop.
const REAL_PI = process.env.FULLSEND_TEST_PI_BIN;
const EXTENSION = fileURLToPath(new URL("./fullsend-edit-repair.js", import.meta.url));
const FAUX_EXTENSION = `
import { fauxAssistantMessage, fauxProvider, fauxToolCall } from "@earendil-works/pi-ai";
export default function (pi) {
  const faux = fauxProvider({ provider: "faux", models: [{ id: "faux-1" }] });
  faux.setResponses([
    fauxAssistantMessage([fauxToolCall("edit", { path: "target.txt", edits: JSON.parse(process.env.FAUX_EDITS) })], { stopReason: "toolUse" }),
    fauxAssistantMessage("done"),
  ]);
  pi.registerProvider(faux.provider);
  // Where the hook adapter's PreToolUse hooks read the call from.
  pi.on("tool_call", (event) => { console.error("tool_call edits=" + JSON.stringify(event.input?.edits)); });
}
`;
const MALFORMED = {
  "raw control characters (pi#8521)": '[{"oldText":"alpha","newText":"line one\nline\ttwo"}]',
  "stringified items (pi#8962)": ['{"oldText":"alpha","newText":"line one\\nline\\ttwo"}'],
};

function runFaux(edits, extraArgs) {
  const home = mkdtempSync(join(tmpdir(), "fullsend-edit-repair-pi-"));
  const faux = join(home, "faux.js");
  writeFileSync(faux, FAUX_EXTENSION);
  writeFileSync(join(home, "target.txt"), "alpha\n");
  const args = ["--print", "--mode", "json", "--no-approve", "--no-extensions", "-e", faux, ...extraArgs, "--model", "faux/faux-1", "go"];
  return new Promise((resolve) => {
    const child = spawn(REAL_PI, args, {
      cwd: home,
      env: { PATH: process.env.PATH, HOME: home, PI_CODING_AGENT_DIR: home, FAUX_EDITS: JSON.stringify(edits) },
      stdio: ["pipe", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    child.stdout.on("data", (c) => { stdout += c; });
    child.stderr.on("data", (c) => { stderr += c; });
    child.stdin.end();
    child.on("close", (code) => resolve({ code, stdout, stderr, target: readFileSync(join(home, "target.txt"), "utf8") }));
  });
}

// editResult is the text of the edit tool's result in a --mode json stream.
function editResult(stdout) {
  for (const line of stdout.split("\n")) {
    let evt;
    try {
      evt = JSON.parse(line);
    } catch {
      continue;
    }
    if (evt.type === "tool_execution_end" && evt.toolName === "edit") {
      return (evt.result?.content ?? []).map((b) => b.text ?? "").join("");
    }
  }
  return "";
}

for (const [label, edits] of Object.entries(MALFORMED)) {
  test(`real pi: ${label} fails without the extension and is applied with it`, { timeout: 120_000 }, async (t) => {
    if (!REAL_PI) return t.skip("set FULLSEND_TEST_PI_BIN to a pi binary at the pinned PI_VERSION (images/sandbox/Containerfile)");

    const without = await runFaux(edits, []);
    assert.match(editResult(without.stdout), /edits\.0: must be object/, "the defect this extension exists for");
    assert.equal(without.target, "alpha\n");

    const withExt = await runFaux(edits, ["-e", EXTENSION]);
    assert.equal(withExt.code, 0, withExt.stderr);
    assert.match(editResult(withExt.stdout), /Successfully replaced 1 block/);
    assert.equal(withExt.target, "line one\nline\ttwo\n");
    assert.match(withExt.stderr, /\[fullsend-edit-repair\] repaired .* for target\.txt/);
    assert.match(withExt.stderr, /tool_call edits=\[\{"oldText":"alpha","newText":"line one\\nline\\ttwo"\}\]/,
      "tool_call handlers — the hook adapter — see the repaired edits, the same ones that are applied");
  });
}

test("real pi: tool allowlists and the extension's edit tool", { timeout: 120_000 }, async (t) => {
  if (!REAL_PI) return t.skip("set FULLSEND_TEST_PI_BIN to a pi binary at the pinned PI_VERSION (images/sandbox/Containerfile)");
  const edits = MALFORMED["stringified items (pi#8962)"];

  const filtered = await runFaux(edits, ["--tools", "read,grep", "-e", EXTENSION]);
  assert.match(editResult(filtered.stdout), /not found/, "--tools without edit filters the extension's edit tool");
  assert.equal(filtered.target, "alpha\n");

  // Why PiRuntime.Run and the Agent extension never load it under
  // --no-builtin-tools: pi does not filter extension tools there, so the
  // extension would grant edit to an agent that has none.
  const unfiltered = await runFaux(edits, ["--no-builtin-tools", "-e", EXTENSION]);
  assert.match(editResult(unfiltered.stdout), /Successfully replaced/);
});
