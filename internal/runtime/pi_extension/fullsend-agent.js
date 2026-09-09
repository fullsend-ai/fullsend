// fullsend-agent.js — pi extension that provides Claude Code's `Agent` tool
// (legacy alias `Task`) on the pi runtime, so skills written for Claude
// Code's sub-agent roster (pr-review, retro-analysis) dispatch unchanged
// (fullsend#6527, #6464).
//
// Each call runs one child `pi --print --mode json` to completion inside the
// sandbox, with the same flag set PiRuntime.Run gives the parent: trust off,
// no auto-discovered extensions, the manifest's provider extensions and the
// hook adapter with -e, a strict --tools allowlist, its own session dir.
// pi runs sibling tool calls from one assistant message concurrently, which
// is the parallel dispatch the skills ask for; `run_in_background` is
// accepted and ignored. Children never receive this extension (and refuse
// to register it if they did — FULLSEND_SUBAGENT_DEPTH), so recursion is
// impossible.
//
// The prompt goes over the child's stdin, never as a positional argument.
// Without a terminator pi's argv parser (0.84.4 dist/cli/args.js) reads a
// leading "-" as an unknown option (a startup error), a leading "--" as an
// unknown flag that swallows the next word, and a leading "@" as a file
// argument. pi does honour a "--" end-of-options terminator (args.js:23),
// but that is not enough to make argv usable here: after it a positional
// starting with "@" is *still* taken as a file argument (args.js:25), and
// argv is capped by the kernel either way (spawn E2BIG above ~128 KiB on
// Linux), which a context package easily exceeds. In --print mode pi reads
// a non-TTY stdin to EOF (dist/main.js readPipedStdin) and
// buildInitialMessage (dist/cli/initial-message.js) uses it as the initial
// message, so stdin carries an arbitrary prompt verbatim, whatever it
// starts with and however long it is.
//
// A child is stopped with SIGTERM, escalated to SIGKILL after a grace
// period — never SIGKILL first. pi's bash tool spawns commands `detached`
// in their own process group and kills them from its SIGTERM handler
// (dist/modes/print-mode.js -> killTrackedDetachedChildren, then exit 143);
// an unhandleable SIGKILL would leave those grandchildren running.
//
// Loaded explicitly by PiRuntime.Run with `-e` after the hook adapter.
// Everything it needs is the `agent` block of the manifest
// PiRuntime.Bootstrap wrote (FULLSEND_PI_MANIFEST).
import { spawn as nodeSpawn } from "node:child_process";
import { createHash } from "node:crypto";
import { appendFileSync, mkdirSync, readFileSync } from "node:fs";
import { dirname } from "node:path";

export const DEFAULT_MANIFEST_PATH = "/sandbox/pi-config/fullsend-manifest.json";
export const DEPTH_ENV = "FULLSEND_SUBAGENT_DEPTH";
// RESULT_MAX_BYTES caps the text handed back to the parent; pi's own
// built-in tools truncate at 50 KB, and a reviewer's findings fit well
// within this.
export const RESULT_MAX_BYTES = 64 * 1024;
const TRUNCATED_MARKER = "\n[truncated]";
const STDERR_TAIL_BYTES = 4 * 1024;
const LOG_PREFIX = "[fullsend-agent]";
const DEFAULT_MAX_CONCURRENT = 4;
const DEFAULT_TIMEOUT_SECONDS = 900;
const DEFAULT_THINKING = "medium";
const DEFAULT_EXPLORE_TOOLS = ["read", "grep", "find", "ls"];
// Claude Code's built-in agent types, as the fleet's pinned CLI (2.1.258)
// lists them in "Available agents:". Orchestrators emit these today, so
// they keep the lenient anonymous path even when personas are registered;
// only other unknown names are rejected. Re-check on a Claude Code pin bump.
const CLAUDE_BUILTIN_AGENT_TYPES = new Set(["claude", "explore", "general-purpose", "plan", "statusline-setup"]);
const TOOL_NAME = "Agent";
const TOOL_ALIAS = "Task";
// Providers pi serves without an extension and with the same Vertex ADC
// the sandbox already carries.
const BUILTIN_PROVIDERS = ["google-vertex"];
// DEFAULT_KILL_GRACE_MS is how long a child gets to handle SIGTERM (kill
// its own detached bash grandchildren and flush the session) before SIGKILL.
export const DEFAULT_KILL_GRACE_MS = 3000;
// MAX_STDOUT_LINE_CHARS bounds one --mode json line, mirroring the Go
// transcript reader's 1 MB cap. A longer line is dropped rather than
// buffered: a child that emits an unbounded line must not grow the
// orchestrator's heap without limit.
export const MAX_STDOUT_LINE_CHARS = 1024 * 1024;
// MAX_DESCRIPTION_BYTES caps the label copied into the usage file. Children
// append to one file concurrently, and only a write below PIPE_BUF (4096 on
// Linux) is atomic; the rest of a record is bounded by construction.
export const MAX_DESCRIPTION_BYTES = 512;
// PI_THINKING_LEVELS are pi's --thinking values, used to recognise (and
// drop) the "provider/id:high" shorthand pi's model resolver accepts
// (0.84.4 dist/core/model-resolver.js parseModelPattern): left in place it
// would silently override the manifest's thinking level for the child.
const PI_THINKING_LEVELS = new Set(["off", "minimal", "low", "medium", "high", "xhigh", "max"]);
// CHILD_SYSTEM_NOTE replaces the parent's APPEND_SYSTEM.md for a child.
// pi discovers PI_CODING_AGENT_DIR/APPEND_SYSTEM.md only when no
// --append-system-prompt was given (0.84.4 dist/core/resource-loader.js:
// the discovery is guarded by `if (!appendSources)`), and children share the
// parent's config dir — so without this flag every child would inherit the
// orchestrator persona, including its "make several Agent calls in one
// message" dispatch note, for a tool it does not have.
export const CHILD_SYSTEM_NOTE =
  "You are a sub-agent dispatched by another agent. Carry out the single task in the message you " +
  "were given and then stop. You have no sub-agent tool of your own and cannot dispatch further " +
  "work. Your final assistant message is the entire result handed back to the agent that " +
  "dispatched you, so make it self-contained.";

// AGENT_TOOL_PARAMETERS is Claude Code's Agent tool input shape as plain
// JSON Schema (pi validates with typebox 1.x, which is JSON-Schema-native,
// so no typebox import is needed and the file stays runnable under node
// alone).
export const AGENT_TOOL_PARAMETERS = {
  type: "object",
  properties: {
    prompt: { type: "string", description: "The full task for the sub-agent, including all context it needs; it starts with no memory of this conversation." },
    description: { type: "string", description: "A short (3-5 word) label for the task, shown in progress output." },
    model: { type: "string", description: "Model for the sub-agent: opus, sonnet, haiku, or a provider/id spec available in this run. Omit to use the model this run configures for sub-agents, else the current one. Ignored when subagent_type names a persona." },
    subagent_type: { type: "string", description: "One of the registered sub-agent personas (the runtime note lists them), or `Explore` for a read-only sub-agent (read, grep, find, ls). A persona runs on the model and tool set the runner resolved for it, and any `model` argument is ignored. Omit for a sub-agent with the current tool set; any other value is rejected when personas are registered." },
    run_in_background: { type: "boolean", description: "Accepted for compatibility; sub-agents always run to completion inside the call." },
  },
  required: ["prompt"],
};

export function loadManifest(path = process.env.FULLSEND_PI_MANIFEST || DEFAULT_MANIFEST_PATH) {
  return JSON.parse(readFileSync(path, "utf8"));
}

function cut(s, sep) {
  const i = s.indexOf(sep);
  return i < 0 ? [s, "", false] : [s.slice(0, i), s.slice(i + sep.length), true];
}

function providerOf(spec) {
  return cut(spec, "/")[0].toLowerCase();
}

// allowedProviders are the provider prefixes a child may name directly:
// those of the manifest's model table, those whose extension the child is
// given (directory basename, the way the sandbox image names them), pi's
// credential-free built-ins on Vertex, and the provider the parent is
// actually running on.
//
// The parent's live provider is in the set so that naming the parent's own
// model explicitly is not stricter than omitting `model` (which inherits
// that same spec). It is also the provider whose credentials this
// iteration demonstrably has: the runner picked it, the child shares the
// sandbox config dir (pi's auth.json) and gets the provider extensions the
// image probe found. Without it a run on a provider that needs no -e
// extension and is not in the model table — openai, whose key the runner
// seeds into auth.json — could not dispatch a sub-agent on its own model
// at all.
function allowedProviders(agent, parentSpec) {
  const out = new Set(BUILTIN_PROVIDERS);
  if (typeof parentSpec === "string" && parentSpec.includes("/")) out.add(providerOf(parentSpec));
  for (const spec of Object.values(agent?.models ?? {})) {
    if (typeof spec === "string" && spec.includes("/")) out.add(providerOf(spec));
  }
  for (const ext of agent?.extensions ?? []) {
    if (typeof ext !== "string" || ext.endsWith(".js") || ext.endsWith(".ts")) continue;
    const base = ext.replace(/\/+$/, "").split("/").pop();
    if (base) out.add(base.toLowerCase());
  }
  return out;
}

// stripThinkingSuffix removes pi's "<spec>:<thinking level>" shorthand.
// Only a suffix that names a real level is removed; anything else stays so
// the model table rejects it by name instead of silently losing a segment.
function stripThinkingSuffix(spec) {
  const i = spec.lastIndexOf(":");
  if (i < 0) return spec;
  if (!PI_THINKING_LEVELS.has(spec.slice(i + 1).toLowerCase())) return spec;
  return spec.slice(0, i);
}

// resolveModel turns the orchestrator's `model` argument into the pi spec
// a child is started with. Accepted: empty (the parent's model, else the
// manifest default), the Claude aliases, a Claude id the fleet personas
// use ("claude-sonnet-4-6@default" — the @suffix is dropped), a
// "provider/id" this run can actually serve, and the direct-API forms
// "anthropic/<claude id>" and "xai/<grok id>" translated the way the runner
// translates them for the parent. Anything else — notably a Claude-shaped id
// the model invented — is rejected with the accepted forms, so the
// orchestrator can correct itself instead of losing the dispatch to a
// provider with no credentials.
//
// "This run can serve it" is a closed set, not a provider-prefix check: the
// manifest's model table, the parent's own spec, and the ids the manifest
// lists per provider in providerModels (pi's built-in google-vertex and the
// vendored xai-vertex extension, neither of which has a model-table entry).
// A provider prefix alone is not enough — an invented id under an allowed
// provider ("anthropic-vertex/claude-sonnet-4-20250514",
// "google-vertex/gemini-9", "xai/grok-9") would otherwise be handed to the
// API, which is exactly the failure the rejection exists to prevent for the
// direct-API forms. A manifest without providerModels therefore serves no
// bare provider ids at all: the extension and the manifest are written by
// the same Bootstrap, so the field is absent only when something rewrote it.
//
// A trailing ":<thinking level>" (pi's own "provider/id:high" shorthand) is
// dropped rather than passed through: the child's reasoning effort is the
// manifest's --thinking, and a spec-borne level would silently override it.
export function resolveModel(agent, spec, parentSpec) {
  const models = agent?.models ?? {};
  // subagents.default: the repo's blanket model for children that name no
  // persona. It fills only an empty argument, and outranks the parent so
  // children stop inheriting an expensive one. Bootstrap already checked
  // it against the trusted set.
  const subagentDefault = typeof agent?.subagentDefault === "string" ? agent.subagentDefault.trim() : "";
  const fallback = subagentDefault || (typeof parentSpec === "string" && parentSpec.trim()) || models.default || "";
  let s = typeof spec === "string" ? spec.trim() : "";
  if (s === "") return fallback;
  s = cut(s, "@")[0];
  s = stripThinkingSuffix(s);
  if (s === "") return fallback;

  const aliases = {};
  const byBareID = {};
  const known = new Set();
  for (const [name, value] of Object.entries(models)) {
    if (typeof value !== "string" || value === "") continue;
    known.add(value);
    if (name !== "default") aliases[name.toLowerCase()] = value;
    byBareID[cut(value, "/")[2] ? cut(value, "/")[1].toLowerCase() : value.toLowerCase()] = value;
  }
  const reject = (what) => {
    const names = Object.keys(aliases).join(", ");
    throw new Error(`model "${spec}": ${what}; use ${names || "the parent's model"}, or one of ${[...known].join(", ")}`);
  };
  const bare = (id) => {
    const key = id.toLowerCase();
    if (aliases[key]) return aliases[key];
    if (byBareID[key]) return byBareID[key];
    return reject(`"${id}" is not available in this sandbox`);
  };

  const [head, rest, hasSlash] = cut(s, "/");
  if (!hasSlash) return bare(s);
  let provider = head.toLowerCase();
  if (provider === "anthropic") return bare(rest);
  // Mirror the runner (normalizeXaiVertexModel): the xai-vertex extension
  // registers publisher-qualified ids ("xai/grok-4.6"), so both the short
  // vendor form and a two-segment spec land on the three-segment one. Only
  // the spelling is normalized here — the result then goes through the same
  // closed set as every other provider, because an allowed provider prefix
  // is not a licence to name an id under it: "xai-vertex/xai/grok-9" would
  // otherwise be handed to Vertex as an unknown model, exactly the failure
  // the closed set exists to prevent.
  let normalized = s;
  if (provider === "xai" || provider === "xai-vertex") {
    provider = "xai-vertex";
    normalized = `xai-vertex/xai/${rest.toLowerCase().startsWith("xai/") ? rest.slice(4) : rest}`;
  }
  const allowed = allowedProviders(agent, parentSpec);
  // The normalized name, not the one written: "xai/grok-4.6" is a spec for
  // the xai-vertex provider and must be reported as one.
  if (!allowed.has(provider)) return reject(`provider "${provider}" is not available in this run`);
  const canonical = servableSpecs(agent, parentSpec).get(normalized.toLowerCase());
  if (canonical) return canonical;
  return reject(`"${rest}" is not a model this run serves on "${provider}"`);
}

// servableSpecs maps the lowercased form of every full "provider/id" spec
// this run can serve to the spec as written: the manifest's model table,
// the parent's own model (whose credentials this iteration demonstrably
// has), and each provider id listed in the manifest's providerModels.
function servableSpecs(agent, parentSpec) {
  const out = new Map();
  const add = (spec) => {
    if (typeof spec === "string" && spec.includes("/")) out.set(spec.trim().toLowerCase(), spec.trim());
  };
  for (const spec of Object.values(agent?.models ?? {})) add(spec);
  add(typeof parentSpec === "string" ? parentSpec.trim() : "");
  for (const [prov, ids] of Object.entries(agent?.providerModels ?? {})) {
    if (!Array.isArray(ids)) continue;
    for (const id of ids) {
      if (typeof id === "string" && id !== "") add(`${prov}/${id}`);
    }
  }
  return out;
}

// childTools is the --tools allowlist for a child: Explore gets the
// read-only set intersected with the parent's, anything else the parent's
// built-in set. When a persona declares tools, the child gets those tools
// intersected with the parent's set. The Agent tool itself is never in it
// (children cannot dispatch).
//
// The intersection matters because a sub-agent must never be able to reach
// past its parent: an agent whose tools: frontmatter withheld `grep` would
// otherwise get it back by dispatching an Explore child. It is a no-op for
// an agent that declared no tools:, whose set is pi's defaults and already
// a superset of the read-only tools.
export function childTools(agent, subagentType) {
  const parent = (agent?.tools ?? []).filter((t) => t !== TOOL_NAME && t !== TOOL_ALIAS);
  const explore = typeof subagentType === "string" && subagentType.trim().toLowerCase() === "explore";
  if (explore) return (agent?.exploreTools ?? DEFAULT_EXPLORE_TOOLS).filter((t) => parent.includes(t));
  // Persona dispatch intersects with the parent, as Explore does. A
  // present-but-empty array means "no tools" and must stay empty --
  // childArgs turns it into --no-builtin-tools; widening to the parent
  // here would hand a restricted persona bash, write and edit.
  const persona = lookupPersona(agent, subagentType);
  if (persona && Array.isArray(persona.tools)) {
    const parentSet = new Set(parent);
    return persona.tools.filter((t) => parentSet.has(t));
  }
  return parent;
}

// childArgs renders the child's argv (without the binary): the parent's
// flag set minus the shell hygiene the parent already did. The prompt is
// not here — it goes over stdin (see the file header).
//
// personaName, when non-empty, names the persona driving the dispatch; the
// session dir becomes "sessions/<persona>-<seq>" instead of
// "sessions/agent-<seq>" so transcripts are labelled by persona (#7031).
export function childArgs(agent, { seq, modelSpec, tools, personaName }) {
  const prefix = personaName ? personaName : "agent";
  const args = [
    "--print", "--mode", "json", "--no-approve", "--no-extensions", "--no-prompt-templates", "--no-themes",
    "--session-dir", `${agent.sessionsDir}/${prefix}-${seq}`,
  ];
  for (const ext of agent.extensions ?? []) args.push("-e", ext);
  if (tools.length === 0) {
    // Mirror the runner: an empty allowlist means "no built-in tools", not
    // "the default set". `--tools ''` would be read as one empty name.
    args.push("--no-builtin-tools");
  } else {
    args.push("--tools", tools.join(","));
  }
  args.push("--model", modelSpec);
  args.push("--thinking", agent.thinking || DEFAULT_THINKING);
  // Replaces the discovered APPEND_SYSTEM.md, which is the parent's
  // orchestrator persona (see CHILD_SYSTEM_NOTE).
  args.push("--append-system-prompt", CHILD_SYSTEM_NOTE);
  return args;
}

// childEnv is the environment one child runs with. The runner's provider
// hygiene (buildPiRunCommand) is applied to the shell that launched the
// *parent*, so it only ever matched the parent's provider; a child on a
// different provider would otherwise inherit, say, a stray
// ANTHROPIC_API_KEY (which pi's built-in anthropic provider would use for a
// direct-to-Anthropic call) or an ambient GOOGLE_CLOUD_PROJECT. The rules below are the same ones
// pi_run.go applies, per resolved child provider.
//
// google-vertex and openai have no rules here on purpose, matching
// buildPiRunCommand: google-vertex reads the ADC the sandbox already
// carries plus GOOGLE_CLOUD_PROJECT/GOOGLE_CLOUD_LOCATION, which are the
// runner's own exports rather than another provider's credential; and
// pi's built-in openai provider resolves its key from the runner-seeded
// auth.json, which the run deliberately leaves as the only source (Run
// unsets OPENAI_API_KEY itself). There is nothing a child on either of
// them could inherit that a scrub would take away.
export function childEnv(base, modelSpec) {
  const env = { ...base, [DEPTH_ENV]: "1" };
  const provider = providerOf(modelSpec);
  if (provider === "anthropic-vertex") {
    delete env.ANTHROPIC_API_KEY;
    delete env.ANTHROPIC_AUTH_TOKEN;
    delete env.ANTHROPIC_BASE_URL;
    delete env.ANTHROPIC_VERTEX_BASE_URL;
    const project = base.ANTHROPIC_VERTEX_PROJECT_ID || base.GOOGLE_CLOUD_PROJECT;
    if (project) env.GOOGLE_CLOUD_PROJECT = project;
  } else if (provider === "xai-vertex") {
    delete env.XAI_API_KEY;
    const project = base.XAI_VERTEX_PROJECT_ID || base.ANTHROPIC_VERTEX_PROJECT_ID || base.GOOGLE_CLOUD_PROJECT;
    if (project) env.XAI_VERTEX_PROJECT_ID = project;
  }
  return env;
}

// lookupPersona returns the persona entry from the manifest's personas table
// for the given subagent_type, or undefined if it is not a persona name. The
// match is case-insensitive to mirror the CLI's key normalisation.
function lookupPersona(agent, subagentType) {
  if (typeof subagentType !== "string" || subagentType.trim() === "") return undefined;
  const key = subagentType.trim().toLowerCase();
  if (key === "explore") return undefined;
  const personas = agent?.personas;
  if (!personas || typeof personas !== "object") return undefined;
  // The manifest's keys are already lowercase; normalise the lookup for safety.
  for (const [name, entry] of Object.entries(personas)) {
    if (name.toLowerCase() === key) return entry;
  }
  return undefined;
}

// registeredPersonaNames returns the persona names from the manifest, or an
// empty array when none are registered. Used for error messages.
function registeredPersonaNames(agent) {
  const personas = agent?.personas;
  if (!personas || typeof personas !== "object") return [];
  return Object.keys(personas);
}

function joinText(content) {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .filter((b) => b && b.type === "text" && typeof b.text === "string")
    .map((b) => b.text)
    .join("\n");
}

function newStreamState() {
  return {
    text: "",
    stopReason: "",
    errorMessage: "",
    model: "",
    provider: "",
    ended: false,
    usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, cost: 0 },
  };
}

function noteAssistant(state, msg) {
  if (!msg || msg.role !== "assistant") return;
  const text = joinText(msg.content);
  if (text.trim() !== "") state.text = text;
  if (typeof msg.stopReason === "string") state.stopReason = msg.stopReason;
  if (typeof msg.errorMessage === "string") state.errorMessage = msg.errorMessage;
  if (typeof msg.model === "string" && msg.model) state.model = msg.model;
  if (typeof msg.provider === "string" && msg.provider) state.provider = msg.provider;
}

// consumeLine folds one --mode json event into the stream state:
// message_end accumulates usage and keeps the last assistant text;
// agent_end is the completion marker and its last assistant message is
// authoritative for the stop reason.
export function consumeLine(state, line) {
  const trimmed = line.trim();
  if (trimmed === "") return;
  let evt;
  try {
    evt = JSON.parse(trimmed);
  } catch {
    return;
  }
  if (!evt || typeof evt !== "object") return;
  if (evt.type === "message_end") {
    const msg = evt.message;
    if (msg && msg.role === "assistant") {
      const u = msg.usage ?? {};
      state.usage.input += Number(u.input) || 0;
      state.usage.output += Number(u.output) || 0;
      state.usage.cacheRead += Number(u.cacheRead) || 0;
      state.usage.cacheWrite += Number(u.cacheWrite) || 0;
      state.usage.cost += Number(u.cost?.total) || 0;
      noteAssistant(state, msg);
    }
  } else if (evt.type === "agent_end") {
    state.ended = true;
    const msgs = Array.isArray(evt.messages) ? evt.messages : [];
    for (let i = msgs.length - 1; i >= 0; i--) {
      if (msgs[i]?.role === "assistant") {
        noteAssistant(state, msgs[i]);
        break;
      }
    }
  }
}

function capText(text) {
  const trimmed = text.trim();
  if (Buffer.byteLength(trimmed) <= RESULT_MAX_BYTES) return trimmed;
  return Buffer.from(trimmed).subarray(0, RESULT_MAX_BYTES).toString("utf8").replace(/�+$/, "") + TRUNCATED_MARKER;
}

// capBytes truncates on a byte budget without leaving a split code point.
function capBytes(text, max) {
  if (Buffer.byteLength(text) <= max) return text;
  return Buffer.from(text).subarray(0, max).toString("utf8").replace(/�+$/, "");
}

function signalChild(child, signal) {
  try {
    child.kill(signal);
  } catch {
    // already gone
  }
}

// createAgentTool builds the dispatcher from a manifest. `spawn`, `log` and
// `now` are injectable for tests. run() never throws for a failed child —
// it returns { isError, error } — so the registered execute() decides how
// to surface it (pi marks a result isError only when execute throws).
export function createAgentTool(manifest, { spawn = nodeSpawn, log = (m) => console.error(m), now = () => Date.now(), env = process.env, killGraceMs = DEFAULT_KILL_GRACE_MS, manifestPath = "", manifestSum = "" } = {}) {
  const agent = manifest?.agent ?? {};
  const maxConcurrent = Math.max(1, Number(agent.maxConcurrent) || DEFAULT_MAX_CONCURRENT);
  const timeoutMs = Math.max(1, (Number(agent.timeoutSeconds) || DEFAULT_TIMEOUT_SECONDS) * 1000);
  let seq = 0;
  let active = 0;
  // waiters are dispatches queued behind maxConcurrent. Each is an object
  // so shutdown (and an abort while queued) can take a specific one out of
  // the queue instead of only ever releasing the head.
  const waiters = [];
  const running = new Set();
  let shuttingDown = false;

  // A slot is held from the moment a ticket is granted one until exactly one
  // release(); `ticket.acquired` records whether this ticket holds one, so
  // only a ticket that does ever gives one back. A waiter therefore settles
  // one of two ways: grant() (the releasing dispatch hands its slot straight
  // over, so `active` is unchanged and the ticket now holds it) or evict()
  // (the dispatch is cancelled and will spawn nothing, so it takes no slot
  // and must not release one). Claiming a slot for an evicted waiter would
  // over-admit: its release() would pass that slot to the next waiter
  // without ever decrementing `active`, and maxConcurrent+1 children would
  // run at once.
  const acquire = (ticket) => {
    if (active < maxConcurrent) {
      active++;
      ticket.acquired = true;
      return Promise.resolve();
    }
    return new Promise((resolve) => {
      ticket.waiter = {
        grant: () => {
          ticket.acquired = true;
          resolve();
        },
        evict: resolve,
      };
      waiters.push(ticket.waiter);
    });
  };
  const release = () => {
    const next = waiters.shift();
    if (next) {
      next.grant();
    } else {
      active--;
    }
  };
  const unqueue = (waiter) => {
    if (!waiter) return;
    const i = waiters.indexOf(waiter);
    if (i < 0) return;
    waiters.splice(i, 1);
    waiter.evict();
  };

  // extensionDigests are the digests Bootstrap recorded for the files it
  // wrote into agent.extensions that live in the config dir — today only
  // the hook adapter, and the same bytes the launch guard checks. They
  // travel inside the manifest, so the manifest digest covers them: a
  // rewrite that drops or edits them is itself a manifest drift, caught
  // first below. The vendored provider extensions under
  // /usr/local/share/pi-extensions carry none on purpose — they are
  // root-owned and read-only in the image, outside anything the agent can
  // write, so there is nothing there to re-check.
  const extensionDigests = Object.entries(agent.extensionDigests ?? {}).filter(
    ([file, want]) => typeof file === "string" && file !== "" && typeof want === "string" && want !== "",
  );

  // manifestDrift re-reads the manifest and the config-dir extensions it
  // names, and reports whether they still hash to what this extension
  // loaded. The launch guards in buildPiRunCommand check both once, before
  // pi starts; an iteration then runs for minutes with those files sitting
  // in a config dir the agent can write to, and between them they name the
  // binary a child runs, the -e list it loads, its tool allowlist and where
  // its usage is recorded.
  //
  // The hook adapter is the sharper half: nothing re-verifies it after the
  // launch guard, a parent with `write` can replace it mid-iteration, and a
  // rewritten adapter simply omits its own manifest-digest check — so every
  // child dispatched afterwards would come up unhooked. Both are therefore
  // re-checked immediately before every dispatch, inside the slot this
  // dispatch holds. The manifest half is skipped when the caller supplied
  // no digest — the unit tests build a tool from an object, not a file.
  const manifestDrift = () => {
    if (manifestPath && manifestSum) {
      let sum;
      try {
        sum = createHash("sha256").update(readFileSync(manifestPath)).digest("hex");
      } catch (err) {
        return `cannot re-read ${manifestPath} before dispatching: ${err.message}`;
      }
      if (sum !== manifestSum) return "manifest changed since load; refusing to dispatch";
    }
    for (const [file, want] of extensionDigests) {
      let sum;
      try {
        sum = createHash("sha256").update(readFileSync(file)).digest("hex");
      } catch (err) {
        return `cannot re-read ${file} before dispatching: ${err.message}`;
      }
      if (sum !== want) return "hook adapter changed since load; refusing to dispatch";
    }
    return "";
  };

  const recordUsage = (record) => {
    if (!agent.usageFile) return;
    try {
      mkdirSync(dirname(agent.usageFile), { recursive: true });
      appendFileSync(agent.usageFile, JSON.stringify(record) + "\n");
    } catch (err) {
      log(`${LOG_PREFIX} cannot write ${agent.usageFile}: ${err.message}`);
    }
  };

  // runChild spawns one child and resolves when it is gone. It resolves a
  // handle synchronously through `out` so the caller can terminate the
  // child on abort or shutdown while it is still running.
  const runChild = (id, params, modelSpec, tools, out, personaName) =>
    new Promise((resolve) => {
      const state = newStreamState();
      const startedAt = now();
      const args = childArgs(agent, { seq: id, modelSpec, tools, personaName });
      let child;
      try {
        child = spawn(agent.piBin || "pi", args, {
          env: childEnv(env, modelSpec),
          // stdin carries the prompt; a child in the parent's process group
          // so `detached` is not set — a killed parent must not leave
          // children spending tokens, and the SIGTERM below is what makes
          // pi clean up its own detached bash grandchildren.
          stdio: ["pipe", "pipe", "pipe"],
        });
      } catch (err) {
        resolve({ state, startedAt, exitCode: null, signal: null, spawnError: err.message, stderr: "", timedOut: false, pid: undefined, droppedLines: 0 });
        return;
      }
      let timedOut = false;
      let stderr = "";
      let buffered = "";
      let dropping = false;
      let droppedLines = 0;
      let killTimer;
      // terminate is the whole stop sequence: SIGTERM first so pi runs its
      // handler (kills the detached bash processes it tracks, exits 143),
      // SIGKILL only if it is still there after the grace period.
      const terminate = () => {
        if (child.exitCode !== null || child.signalCode !== null) return;
        signalChild(child, "SIGTERM");
        if (killTimer !== undefined) return;
        killTimer = setTimeout(() => signalChild(child, "SIGKILL"), killGraceMs);
        if (typeof killTimer.unref === "function") killTimer.unref();
      };
      const handle = { child, terminate };
      if (out) out.handle = handle;
      running.add(handle);
      const timer = setTimeout(() => {
        timedOut = true;
        terminate();
      }, timeoutMs);
      // The prompt is written and the pipe closed at once: pi reads stdin
      // to EOF before it starts. A child that dies first (or never drains a
      // prompt larger than the pipe buffer) makes this EPIPE, which is an
      // ordinary outcome here — `close` already carries the verdict — but
      // an unhandled "error" on the stream would take the parent down.
      child.stdin.on("error", () => {});
      child.stdin.end(params.prompt);
      child.stdout.setEncoding("utf8");
      child.stdout.on("data", (chunk) => {
        buffered += chunk;
        let nl;
        while ((nl = buffered.indexOf("\n")) >= 0) {
          const line = buffered.slice(0, nl);
          buffered = buffered.slice(nl + 1);
          // While dropping, the first complete line is the tail of the
          // oversized one; skip it and resume parsing.
          if (dropping) {
            dropping = false;
            droppedLines++;
            continue;
          }
          // A complete oversized line (its newline arrived in the same
          // chunk that carried the bulk of it) is dropped here; one that
          // has not terminated yet is dropped by the buffer cap below.
          if (line.length > MAX_STDOUT_LINE_CHARS) {
            droppedLines++;
            continue;
          }
          consumeLine(state, line);
        }
        if (buffered.length > MAX_STDOUT_LINE_CHARS) {
          dropping = true;
          buffered = "";
        }
      });
      child.stderr.setEncoding("utf8");
      child.stderr.on("data", (chunk) => {
        stderr = (stderr + chunk).slice(-STDERR_TAIL_BYTES);
      });
      const finish = (exitCode, signal, spawnError) => {
        clearTimeout(timer);
        if (killTimer !== undefined) clearTimeout(killTimer);
        running.delete(handle);
        if (!dropping && buffered.trim() !== "") consumeLine(state, buffered);
        if (dropping) droppedLines++;
        resolve({ state, startedAt, exitCode, signal, spawnError, stderr, timedOut, pid: child.pid, droppedLines });
      };
      child.once("error", (err) => finish(null, null, err.message));
      child.once("close", (code, signal) => finish(code, signal, undefined));
    });

  const run = async (params, { parentModel, signal } = {}) => {
    const id = ++seq;
    const description = typeof params?.description === "string" ? params.description : "";
    const subagentType = typeof params?.subagent_type === "string" ? params.subagent_type.trim() : "";
    // Persona dispatch (#7031): when subagent_type names a registered persona,
    // use its resolved model and log if the caller also supplied a model arg.
    const persona = lookupPersona(agent, subagentType);
    let modelSpec;
    if (persona) {
      modelSpec = typeof persona.model === "string" ? persona.model.trim() : "";
      if (typeof params?.model === "string" && params.model.trim() !== "") {
        log(`${LOG_PREFIX} #${id} persona "${subagentType}": ignoring model="${params.model}" — persona model used instead`);
      }
      if (modelSpec === "") {
        // Nothing configured this persona, so inherit the parent's live
        // model. The caller's argument is deliberately not passed: a
        // persona never takes its model from the caller.
        try {
          modelSpec = resolveModel(agent, "", parentModel);
        } catch (err) {
          return { seq: id, isError: true, error: err.message, text: "", stopReason: "rejected", model: "" };
        }
        if (!modelSpec) {
          // Structural guard: falling through to the shared resolve below
          // would consult params.model, and a persona never takes its
          // model from the caller.
          return { seq: id, isError: true, error: `persona "${subagentType}" has no model and this run reports none to inherit`, text: "", stopReason: "rejected", model: "" };
        }
      }
    } else if (subagentType !== "" && agent?.skippedPersonas?.[subagentType.toLowerCase()]) {
      // A persona file that did not register at Bootstrap: say why rather
      // than run the name as an anonymous child with the parent's tools.
      return { seq: id, isError: true, error: `subagent_type "${subagentType}" names a persona that was not registered: ${agent.skippedPersonas[subagentType.toLowerCase()]}`, text: "", stopReason: "rejected", model: "" };
    } else if (subagentType !== "" && !CLAUDE_BUILTIN_AGENT_TYPES.has(subagentType.toLowerCase())) {
      // Unknown non-empty type: reject when personas are registered (the
      // caller likely misspelled a persona name); otherwise accept for
      // forward compatibility.
      const names = registeredPersonaNames(agent);
      if (names.length > 0) {
        return { seq: id, isError: true, error: `subagent_type "${subagentType}" is not a registered persona; available: ${names.join(", ")}`, text: "", stopReason: "rejected", model: "" };
      }
    }
    if (!modelSpec) {
      try {
        modelSpec = resolveModel(agent, params?.model, parentModel);
      } catch (err) {
        return { seq: id, isError: true, error: err.message, text: "", stopReason: "rejected", model: "" };
      }
    }
    if (typeof params?.prompt !== "string" || params.prompt.trim() === "") {
      return { seq: id, isError: true, error: "prompt is required", text: "", stopReason: "rejected", model: modelSpec };
    }
    const cancelled = () => {
      if (shuttingDown) return { seq: id, isError: true, error: "the session is shutting down", text: "", stopReason: "aborted", model: modelSpec };
      if (signal?.aborted) return { seq: id, isError: true, error: "the tool call was aborted", text: "", stopReason: "aborted", model: modelSpec };
      return null;
    };
    let early = cancelled();
    if (early) return early;
    const tools = childTools(agent, params?.subagent_type);
    // The ticket carries the queue entry (before a slot is free) and then
    // the running child, so an abort reaches whichever stage the dispatch
    // is in. The listener is removed on every exit path.
    const ticket = {};
    const onAbort = () => {
      if (ticket.handle) ticket.handle.terminate();
      else unqueue(ticket.waiter);
    };
    signal?.addEventListener?.("abort", onAbort, { once: true });
    let outcome;
    try {
      await acquire(ticket);
      // Re-check: shutdown or an abort can land while this dispatch is
      // queued, and shutdown drains the queue rather than leaving waiters
      // pending forever.
      early = cancelled();
      if (early) {
        // Only a ticket that was granted a slot gives one back; one that was
        // evicted from the queue never held one.
        if (ticket.acquired) release();
        return early;
      }
      try {
        const drift = manifestDrift();
        if (drift) {
          return { seq: id, isError: true, error: drift, text: "", stopReason: "rejected", model: modelSpec };
        }
        log(`${LOG_PREFIX} #${id}${persona ? ` [${subagentType}]` : ""} ${modelSpec} start "${capBytes(description, MAX_DESCRIPTION_BYTES)}"`);
        outcome = await runChild(id, params, modelSpec, tools, ticket, persona ? subagentType.trim().toLowerCase() : "");
      } finally {
        release();
      }
    } finally {
      signal?.removeEventListener?.("abort", onAbort);
    }
    const { state, startedAt, exitCode, signal: exitSignal, spawnError, stderr, timedOut, pid, droppedLines } = outcome;
    const durationMs = Math.max(0, now() - startedAt);
    const tail = stderr.trim();
    let error = "";
    let stopReason = state.stopReason;
    if (spawnError) {
      error = `could not start pi: ${spawnError}`;
      stopReason = "error";
    } else if (timedOut) {
      error = `sub-agent timed out after ${timeoutMs / 1000}s`;
      stopReason = "timeout";
    } else if ((shuttingDown || signal?.aborted) && exitCode !== 0) {
      error = shuttingDown
        ? "sub-agent was killed because the session shut down"
        : "sub-agent was killed because the tool call was aborted";
      stopReason = "aborted";
    } else if (exitCode !== 0) {
      error = `pi exited ${exitCode ?? `on ${exitSignal}`}${tail ? `: ${tail}` : ""}`;
      stopReason = stopReason || "error";
    } else if (stopReason === "error" || stopReason === "aborted") {
      error = state.errorMessage || `sub-agent stopped with stopReason ${stopReason}`;
    } else if (!state.ended) {
      error = `sub-agent produced no agent_end${tail ? `: ${tail}` : ""}`;
      stopReason = stopReason || "incomplete";
    }
    const isError = error !== "";
    if (droppedLines > 0) log(`${LOG_PREFIX} #${id} dropped ${droppedLines} stdout line(s) over ${MAX_STDOUT_LINE_CHARS} chars`);
    log(`${LOG_PREFIX} #${id} done ${durationMs}ms ${stopReason || "unknown"}`);
    recordUsage({
      seq: id,
      // The persona this child ran as, so a cost report can say which
      // persona spent what; per-model aggregation is unaffected.
      ...(persona ? { persona: subagentType.trim().toLowerCase() } : {}),
      model: modelSpec,
      provider: state.provider || providerOf(modelSpec),
      description: capBytes(description, MAX_DESCRIPTION_BYTES),
      startedAt: new Date(startedAt).toISOString(),
      durationMs,
      usage: state.usage,
      stopReason,
      isError,
    });
    return { seq: id, isError, error, text: capText(state.text), stopReason, model: modelSpec, pid, durationMs, usage: state.usage };
  };

  const shutdown = () => {
    shuttingDown = true;
    for (const handle of running) handle.terminate();
    // Queued dispatches would otherwise never settle: nothing will call
    // release() for them once the in-flight children are gone. They are
    // evicted, not granted: none of them will spawn a child, so none takes
    // a slot (see acquire).
    for (const waiter of waiters.splice(0)) waiter.evict();
  };

  return { run, shutdown, inFlight: () => running.size };
}

export default function (pi) {
  if (process.env[DEPTH_ENV] !== undefined) {
    console.error(`${LOG_PREFIX} ${DEPTH_ENV} is set: this is a sub-agent, the Agent tool is not registered (no recursion)`);
    return;
  }
  const manifestPath = process.env.FULLSEND_PI_MANIFEST || DEFAULT_MANIFEST_PATH;
  let manifest;
  let manifestSum;
  try {
    // The bytes are read once and both hashed and parsed from that one
    // read, so the digest kept for manifestDrift describes exactly the
    // configuration in use.
    const bytes = readFileSync(manifestPath);
    manifestSum = createHash("sha256").update(bytes).digest("hex");
    manifest = JSON.parse(bytes.toString("utf8"));
  } catch (err) {
    console.error(`${LOG_PREFIX} cannot read manifest: ${err.message}; the Agent tool is not registered`);
    return;
  }
  if (!manifest?.agent?.enabled) {
    console.error(`${LOG_PREFIX} the Agent tool is not enabled for this agent; nothing registered`);
    return;
  }
  const tool = createAgentTool(manifest, { manifestPath, manifestSum });

  const execute = async (_toolCallId, params, signal, _onUpdate, ctx) => {
    const m = ctx?.model;
    const parentModel = m && typeof m.provider === "string" && typeof m.id === "string" ? `${m.provider}/${m.id}` : "";
    // pi aborts a tool call when the turn is cancelled; without this the
    // child would keep running (and spending) until its own timeout.
    const res = await tool.run(params ?? {}, { parentModel, signal });
    if (res.isError) {
      throw new Error(res.text ? `${res.error}\n\n${res.text}` : res.error);
    }
    return {
      content: [{ type: "text", text: res.text }],
      details: { seq: res.seq, model: res.model, stopReason: res.stopReason, durationMs: res.durationMs },
    };
  };

  const description = "Launch a sub-agent that runs a task to completion in its own context and returns its final message. " +
    "Several Agent calls in one message run in parallel. The sub-agent starts with no memory of this conversation, so the prompt must carry everything it needs.";
  for (const name of [TOOL_NAME, TOOL_ALIAS]) {
    pi.registerTool({
      name,
      label: name === TOOL_NAME ? "Agent" : "Task (alias of Agent)",
      description,
      promptSnippet: name === TOOL_NAME ? "Run a sub-agent on a self-contained task; parallel dispatch is several Agent calls in one message" : undefined,
      parameters: AGENT_TOOL_PARAMETERS,
      execute,
    });
  }
  pi.on("session_shutdown", () => tool.shutdown());
}
