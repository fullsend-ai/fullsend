#!/usr/bin/env node
/**
 * CI: align Wrangler config with CLOUDFLARE_PROJECT_NAME.
 *
 * 1. Set top-level `name` to that value so `wrangler secret bulk` (used by cloudflare/wrangler-action
 *    before deploy) targets the same Worker as `wrangler deploy --name=…`. The action does not pass
 *    `--name` to secret bulk; it always uses the name from wrangler.toml.
 * 2. Set Workers ratelimit `namespace_id` to base + offset(CLOUDFLARE_PROJECT_NAME).
 *
 * Local dev uses the committed `name` and bases in wrangler.toml; this script is not run for wrangler dev.
 *
 * Uses toml-eslint-parser so edits are by source range (comments and layout stay intact).
 */
import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseTOML } from "toml-eslint-parser";

const __dirname = dirname(fileURLToPath(import.meta.url));
const WRANGLER_TOML = join(__dirname, "..", "wrangler.toml");

const BASE_OAUTH = 482401n;
const BASE_USER = 482402n;
/** Max positive int32; keeps offset bounded so namespace_id stays a normal decimal string. */
const MOD = 2147483647n;

const deploymentName = (process.env.CLOUDFLARE_PROJECT_NAME ?? "").trim();
if (!deploymentName) {
  console.error(
    "patch-wrangler-rate-limit-namespace-ids: CLOUDFLARE_PROJECT_NAME is required",
  );
  process.exit(1);
}

const digest = createHash("sha256").update(deploymentName, "utf8").digest();
const offset = BigInt(digest.readUInt32BE(0)) % BigInt(MOD);

const oauthId = (BASE_OAUTH + offset).toString();
const userId = (BASE_USER + offset).toString();

const targets = {
  OAUTH_TOKEN_RATE_LIMITER: oauthId,
  GITHUB_USER_RATE_LIMITER: userId,
};

let source = readFileSync(WRANGLER_TOML, "utf8");
const ast = parseTOML(source, { tomlVersion: "1.0.0" });

function singleKeyName(key) {
  if (key.keys.length !== 1) return null;
  const k = key.keys[0];
  if (k.type === "TOMLBare") return k.name;
  if (k.type === "TOMLQuoted") return k.value;
  return null;
}

function stringValueOf(kv) {
  const v = kv.value;
  if (v.type !== "TOMLValue" || v.kind !== "string") return null;
  return v.value;
}

function namespaceIdValueRange(kv) {
  const v = kv.value;
  if (v.type !== "TOMLValue" || v.kind !== "string") return null;
  return v.range;
}

const patches = [];
const seen = new Set();

const top = ast.body[0];
if (!top || top.type !== "TOMLTopLevelTable") {
  throw new Error("patch-wrangler-rate-limit-namespace-ids: unexpected TOML root");
}

let sawName = false;
for (const node of top.body) {
  if (node.type !== "TOMLKeyValue") continue;
  const kn = singleKeyName(node.key);
  if (kn !== "name") continue;
  sawName = true;
  const v = node.value;
  if (v.type !== "TOMLValue" || v.kind !== "string") {
    throw new Error(
      "patch-wrangler-rate-limit-namespace-ids: top-level name must be a string",
    );
  }
  const quotedName = JSON.stringify(deploymentName);
  const current = source.slice(v.range[0], v.range[1]);
  if (current !== quotedName) {
    patches.push({ start: v.range[0], end: v.range[1], text: quotedName });
  }
  break;
}
if (!sawName) {
  throw new Error("patch-wrangler-rate-limit-namespace-ids: missing top-level name");
}

for (const node of top.body) {
  if (node.type !== "TOMLTable" || node.kind !== "array") continue;
  if (node.resolvedKey[0] !== "ratelimits") continue;

  let bindingName = null;
  let idRange = null;

  for (const kv of node.body) {
    const kn = singleKeyName(kv.key);
    if (kn === "name") bindingName = stringValueOf(kv);
    if (kn === "namespace_id") idRange = namespaceIdValueRange(kv);
  }

  if (!bindingName || idRange == null) continue;
  const newId = targets[bindingName];
  if (newId === undefined) continue;

  seen.add(bindingName);
  const quoted = `"${newId}"`;
  const current = source.slice(idRange[0], idRange[1]);
  if (current !== quoted) {
    patches.push({ start: idRange[0], end: idRange[1], text: quoted });
  }
}

for (const name of Object.keys(targets)) {
  if (!seen.has(name)) {
    throw new Error(
      `patch-wrangler-rate-limit-namespace-ids: missing [[ratelimits]] for ${name}`,
    );
  }
}

patches.sort((a, b) => b.start - a.start);
for (const { start, end, text } of patches) {
  source = source.slice(0, start) + text + source.slice(end);
}

writeFileSync(WRANGLER_TOML, source, "utf8");
console.log(
  `patched wrangler.toml name=${JSON.stringify(deploymentName)}; ratelimit namespace_id: OAUTH=${oauthId} USER=${userId} (offset=${offset.toString()} from CLOUDFLARE_PROJECT_NAME)`,
);
