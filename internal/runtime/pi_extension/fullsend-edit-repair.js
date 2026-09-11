// fullsend-edit-repair.js — pi extension that repairs malformed `edits`
// arguments before pi's built-in edit tool validates them.
//
// Some models (grok-4.6 here, claude-opus-5 in the upstream report) send the
// edit tool's `edits` as a JSON string holding raw control characters — real
// newlines and tabs inside string values, routine for a multi-line code edit
// — or as an array whose items are JSON strings. pi's own preparation
// repairs only a well-formed stringified array (a bare JSON.parse with an
// empty catch), so both shapes fail validation with `edits.0: must be object`
// and the model has to redo the call: 46 times in one fullsend code-agent run
// (fullsend#7231). The upstream fixes, earendil-works/pi#8521 and #8962, were
// auto-closed without review.
//
// This re-registers pi's own edit tool (createEditToolDefinition) with only
// prepareArguments wrapped: the repair runs first, then pi's preparation.
// Execution, matching and rendering stay pi's. pi emits tool_call — the hook
// adapter's PreToolUse input — with the prepared, validated arguments, so the
// security hooks inspect exactly the edits that get applied.
//
// PiRuntime.Run loads it with -e only when the agent's tools include edit:
// under --no-builtin-tools with no --tools list pi does not filter extension
// tools, so loading it there would grant edit to an agent that has none.
//
// Delete this file and its wiring once the pinned pi repairs both shapes
// itself; see the note next to ARG PI_VERSION in images/sandbox/Containerfile.
import { parseJsonWithRepair } from "@earendil-works/pi-ai";
import { createEditToolDefinition } from "@earendil-works/pi-coding-agent";

const LOG_PREFIX = "[fullsend-edit-repair]";

// parseEdit parses one JSON text the way pi would, then with repair. It
// reports whether the repair was what made the difference, so only calls
// pi would have rejected are counted.
function parseEdit(text, parse) {
  try {
    return { ok: true, value: JSON.parse(text), repaired: false };
  } catch {
    // Fall through to the repairing parser.
  }
  try {
    return { ok: true, value: parse(text), repaired: true };
  } catch {
    return { ok: false };
  }
}

// repairEditsArgument returns the edit arguments with a stringified `edits`
// value or stringified items parsed, and the repairs it made. Input it cannot
// improve is returned as is, so pi's own validation reports it unchanged. A
// well-formed stringified array is left to pi, which already parses it.
export function repairEditsArgument(input, parse = parseJsonWithRepair) {
  if (!input || typeof input !== "object" || Array.isArray(input)) return { args: input, repairs: [] };
  let edits = input.edits;
  const repairs = [];

  if (typeof edits === "string") {
    const parsed = parseEdit(edits, parse);
    if (!parsed.ok) return { args: input, repairs: [] };
    const stringItems = Array.isArray(parsed.value) && parsed.value.some((item) => typeof item === "string");
    if (!parsed.repaired && !stringItems) return { args: input, repairs: [] };
    edits = parsed.value;
    if (parsed.repaired) repairs.push("stringified edits");
  }

  if (Array.isArray(edits) && edits.some((item) => typeof item === "string")) {
    let fixed = 0;
    edits = edits.map((item) => {
      if (typeof item !== "string") return item;
      const parsed = parseEdit(item, parse);
      if (!parsed.ok || parsed.value === null || typeof parsed.value !== "object" || Array.isArray(parsed.value)) return item;
      fixed++;
      return parsed.value;
    });
    if (fixed > 0) repairs.push(`${fixed} stringified edit item(s)`);
  }

  if (repairs.length === 0) return { args: input, repairs };
  return { args: { ...input, edits }, repairs };
}

// createRepairedEditTool is pi's edit tool with prepareArguments wrapped.
export function createRepairedEditTool(cwd, { createTool = createEditToolDefinition, parse = parseJsonWithRepair, log = (m) => console.error(m) } = {}) {
  const inner = createTool(cwd);
  const prepare = typeof inner.prepareArguments === "function" ? inner.prepareArguments : (args) => args;
  return {
    ...inner,
    prepareArguments(input) {
      const { args, repairs } = repairEditsArgument(input, parse);
      if (repairs.length > 0) {
        // The path comes from the model. JSON.stringify escapes newlines and
        // escape characters, so a crafted path cannot forge a second log
        // record or steer a terminal reading the captured stderr.
        const path = typeof args?.path === "string" ? JSON.stringify(args.path) : "?";
        log(`${LOG_PREFIX} repaired ${repairs.join(" and ")} for ${path}`);
      }
      return prepare(args);
    },
  };
}

export default function (pi) {
  pi.registerTool(createRepairedEditTool(process.cwd()));
}
