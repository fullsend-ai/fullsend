import assert from "node:assert/strict";
import { existsSync, readFileSync, realpathSync } from "node:fs";
import { dirname, join } from "node:path";
import { test } from "node:test";

// Inspect the entire upstream category, not the three exported constants:
// additions must fail this check. Fail closed if upstream changes its layout.
test("shared MCP resource list matches pinned pi category", {skip: !process.env.FULLSEND_TEST_PI_BIN}, () => {
  let root = dirname(realpathSync(process.env.FULLSEND_TEST_PI_BIN));
  while (!existsSync(join(root, "package.json")) || JSON.parse(readFileSync(join(root, "package.json"), "utf8")).name !== "@earendil-works/pi-coding-agent") {
    const parent = dirname(root);
    assert.notEqual(parent, root, "cannot find pi package");
    root = parent;
  }
  const source = readFileSync(join(root, "dist/core/mcp-servers.js"), "utf8");
  const category = source.match(/const MCP_RESOURCE_TOOLS = new Set\(\[([\s\S]*?)\]\)/);
  assert.ok(category, "upstream category changed; review MCP filtering");
  const upstream = category[1].split(",").map(entry => entry.trim()).filter(Boolean).map(symbol => {
    if (/^["'][^"']+["']$/.test(symbol)) return symbol.slice(1, -1);
    assert.match(symbol, /^[A-Z][A-Z_0-9]*$/, "upstream category format changed");
    const definition = source.match(new RegExp(`(?:export )?const ${symbol} = ["']([^"']+)["']`));
    assert.ok(definition, `cannot resolve upstream resource ${symbol}`);
    return definition[1];
  });
  assert.ok(upstream.length > 0);
  const shared = JSON.parse(readFileSync(new URL("./mcp-resource-tools.json", import.meta.url), "utf8"));
  assert.deepEqual([...shared].sort(), upstream.sort());
});
