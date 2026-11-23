// Runner-owned strict tool selection. This file registers no tool and loads
// after vetted extensions. Pinned Pi print mode refuses extension conflict
// diagnostics before session_start; this selector checks exact registry identity.
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { resolve, relative, isAbsolute } from "node:path";

export const SELECTION_EXIT = 92;

function within(file, directory) {
  if (typeof file !== "string" || typeof directory !== "string") return false;
  const rel = relative(resolve(directory), resolve(file));
  return rel === "" || (!rel.startsWith("..") && !isAbsolute(rel));
}

// MCP selectors retain their existing asynchronous registration and exposure
// semantics. Exact native selections must already exist before inference.
const mcpSelector = (name) => name.startsWith("mcp__") || /^(list|read)_mcp_/.test(name);
const matches = (pattern, name) => {
  const escaped = pattern.split("*").map((s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
  return new RegExp(`^${escaped.join(".*")}$`).test(name);
};

export function selectTools(manifest, registry) {
  if (!manifest?.strictTools || !Array.isArray(manifest.tools)) throw new Error("strict tool declaration missing");
  if (!Array.isArray(registry)) throw new Error("tool registry unavailable");
  const selected = [...new Set(manifest.tools.filter((name) => !mcpSelector(name)))];
  for (const name of selected) {
    const matches = registry.filter((tool) => tool.name === name);
    if (matches.length !== 1) throw new Error(`missing or ambiguous tool: ${name}`);
    const tool = matches[0];
    if (tool.exposure === "hidden") throw new Error(`hidden tool: ${name}`);
    if ((manifest.nativeTools ?? []).includes(name)) {
      if (!(manifest.extensions ?? []).some((ext) => within(tool.sourceInfo?.path, ext.path))) {
        throw new Error(`native tool is not supplied by a vetted extension: ${name}`);
      }
    }
  }
  return selected;
}

export function initializeSelector(pi, manifest, { fatal = (message) => { console.error(`[fullsend-tools] ${message}`); process.exit(SELECTION_EXIT); } } = {}) {
  let allowed = new Set();
  const activate = () => {
    try {
      const selected = selectTools(manifest, pi.getAllTools());
      // Preserve MCP active/callable separation, including late registration.
      const mcpActive = pi.getActiveTools().filter((name) =>
        mcpSelector(name) && manifest.tools.some((pattern) => matches(pattern, name)));
      const active = manifest.activeTools ?? [...selected, ...mcpActive];
      pi.setActiveTools(active);
      const actual = pi.getActiveTools();
      if (actual.some((name) => !active.includes(name)) ||
          active.some((name) => !mcpSelector(name) && !actual.includes(name))) {
        throw new Error("runtime did not enforce exact active tool set");
      }
      allowed = new Set(selected);
    } catch (err) { fatal(err.message); }
  };
  pi.on("session_start", activate);
  pi.on("before_agent_start", activate);
  pi.on("agent_start", activate);
  pi.on("turn_start", activate);
  pi.on("tool_call", (event) => {
    const name = event.toolName;
    if (allowed.has(name) || (mcpSelector(name) && manifest.tools.some((pattern) => matches(pattern, name)))) return;
    return { block: true, reason: "tool is outside the strict Fullsend selection" };
  });
}

export default function toolSelector(pi) {
  let manifest;
  try {
    const bytes = readFileSync(process.env.FULLSEND_PI_MANIFEST || "/sandbox/pi-config/fullsend-manifest.json");
    const want = process.env.FULLSEND_PI_MANIFEST_SHA256;
    if (!want || createHash("sha256").update(bytes).digest("hex") !== want) throw new Error("manifest integrity missing or changed");
    manifest = JSON.parse(bytes);
  } catch (err) {
    console.error(`[fullsend-tools] ${err.message}`);
    process.exit(SELECTION_EXIT);
    return;
  }
  const flag = "fullsend-selected-tools";
  if (process.argv.includes("--" + flag)) {
    pi.registerFlag(flag, { type: "string", description: "Runner-resolved child tool permissions" });
    pi.on("session_start", () => {
      try {
        const child = JSON.parse(pi.getFlag(flag));
        if (!Array.isArray(child.tools) || !Array.isArray(child.activeTools) ||
            child.tools.some((name) => typeof name !== "string" || name.includes("*")) ||
            child.activeTools.some((name) => !child.tools.includes(name))) throw new Error("invalid child selection");
        Object.assign(manifest, { strictTools: true, tools: child.tools, activeTools: child.activeTools });
      } catch (err) {
        console.error(`[fullsend-tools] ${err.message}`);
        process.exit(SELECTION_EXIT);
        return;
      }
    });
  }
  initializeSelector(pi, manifest);
}
