import { parse } from "yaml";

/** Parsed shape of `config.yaml` (mirrors `internal/config/config.go`). */
export type OrgConfigYaml = {
  version?: string;
  dispatch?: { platform?: string };
  defaults?: {
    roles?: string[];
    max_implementation_retries?: number;
    auto_merge?: boolean;
  };
  agents?: { role: string; name?: string; slug?: string }[];
  repos?: Record<string, { enabled?: boolean; roles?: string[] }>;
};

const VALID_ROLES = new Set(["fullsend", "triage", "code", "coder", "review", "fix", "retro", "prioritize"]); // TODO(#1850): remove "coder" after migration

/** 512 KiB — more than sufficient for any realistic org `config.yaml`. */
export const MAX_ORG_CONFIG_YAML_UTF8_BYTES = 512 * 1024;

/**
 * Maximum nesting depth of mappings and sequences after parse (mitigates YAML bombs).
 * Real configs are shallow; this is intentionally generous.
 */
export const MAX_ORG_CONFIG_YAML_DEPTH = 64;

/** Thrown when `config.yaml` exceeds size or structural depth limits (see parse helpers). */
export class OrgConfigYamlLimitError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "OrgConfigYamlLimitError";
  }
}

function utf8ByteLength(s: string): number {
  return new TextEncoder().encode(s).length;
}

/**
 * Deepest path from `value` through nested objects/arrays (scalar leaves report their `depth`).
 * Stops recursing once depth would exceed {@link MAX_ORG_CONFIG_YAML_DEPTH} so flow-style
 * nesting cannot force a full deep walk before the limit check.
 */
function measureYamlTreeDepth(value: unknown, depth: number): number {
  if (value === null || typeof value !== "object") return depth;
  if (depth >= MAX_ORG_CONFIG_YAML_DEPTH) {
    return depth + 1;
  }
  if (Array.isArray(value)) {
    let m = depth;
    for (const el of value) {
      m = Math.max(m, measureYamlTreeDepth(el, depth + 1));
      if (m > MAX_ORG_CONFIG_YAML_DEPTH) return m;
    }
    return m;
  }
  let m = depth;
  for (const k of Object.keys(value as object)) {
    m = Math.max(
      m,
      measureYamlTreeDepth(
        (value as Record<string, unknown>)[k],
        depth + 1,
      ),
    );
    if (m > MAX_ORG_CONFIG_YAML_DEPTH) return m;
  }
  return m;
}

export function parseOrgConfigYaml(data: string): OrgConfigYaml {
  const bytes = utf8ByteLength(data);
  if (bytes > MAX_ORG_CONFIG_YAML_UTF8_BYTES) {
    throw new OrgConfigYamlLimitError(
      `Organisation config YAML exceeds the maximum file size (limit ${MAX_ORG_CONFIG_YAML_UTF8_BYTES} bytes, 512 KiB). This file is ${bytes} bytes. Reduce the file size to continue.`,
    );
  }

  let doc: unknown;
  try {
    doc = parse(data, { schema: "core", version: "1.2" }) as unknown;
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    throw new Error(`parsing org config YAML: ${msg}`);
  }

  if (doc === null || typeof doc !== "object" || Array.isArray(doc)) {
    throw new Error("parsing org config: root must be a mapping");
  }

  const deepest = measureYamlTreeDepth(doc, 0);
  if (deepest > MAX_ORG_CONFIG_YAML_DEPTH) {
    throw new OrgConfigYamlLimitError(
      `Organisation config YAML is nested too deeply (depth ${deepest}, maximum ${MAX_ORG_CONFIG_YAML_DEPTH}). Simplify mapping and list nesting so the document stays within the limit.`,
    );
  }

  assertOrgConfigShape(doc as Record<string, unknown>);
  return doc as OrgConfigYaml;
}

/** Runtime shape checks so callers do not hit confusing errors from bad YAML types. */
function assertOrgConfigShape(doc: Record<string, unknown>): void {
  if ("dispatch" in doc && doc.dispatch !== undefined) {
    if (
      doc.dispatch === null ||
      typeof doc.dispatch !== "object" ||
      Array.isArray(doc.dispatch)
    ) {
      throw new Error("parsing org config: dispatch must be a mapping");
    }
  }
  if ("defaults" in doc && doc.defaults !== undefined) {
    if (
      doc.defaults === null ||
      typeof doc.defaults !== "object" ||
      Array.isArray(doc.defaults)
    ) {
      throw new Error("parsing org config: defaults must be a mapping");
    }
  }
  if ("agents" in doc && doc.agents !== undefined) {
    if (!Array.isArray(doc.agents)) {
      throw new Error("parsing org config: agents must be a list");
    }
    for (let i = 0; i < doc.agents.length; i++) {
      const el = doc.agents[i];
      if (el === null || typeof el !== "object" || Array.isArray(el)) {
        throw new Error(
          `parsing org config: agents[${i}] must be a mapping with a string role`,
        );
      }
      const role = (el as Record<string, unknown>).role;
      if (typeof role !== "string") {
        throw new Error(
          `parsing org config: agents[${i}].role must be a string`,
        );
      }
    }
  }
  if ("repos" in doc && doc.repos !== undefined) {
    if (
      doc.repos === null ||
      typeof doc.repos !== "object" ||
      Array.isArray(doc.repos)
    ) {
      throw new Error("parsing org config: repos must be a mapping");
    }
    for (const [name, v] of Object.entries(
      doc.repos as Record<string, unknown>,
    )) {
      if (
        v !== null &&
        (typeof v !== "object" || Array.isArray(v))
      ) {
        throw new Error(
          `parsing org config: repos.${JSON.stringify(name)} must be a mapping`,
        );
      }
    }
  }
}

/** @returns null if valid, otherwise a human-readable error string (matches Go Validate errors). */
export function validateOrgConfig(cfg: OrgConfigYaml): string | null {
  if (cfg.version !== "1") {
    return `unsupported version ${JSON.stringify(cfg.version)}: must be "1"`;
  }
  if (cfg.dispatch?.platform !== "github-actions") {
    return `unsupported platform ${JSON.stringify(cfg.dispatch?.platform)}: must be "github-actions"`;
  }
  const retries = cfg.defaults?.max_implementation_retries;
  if (retries !== undefined && retries !== null) {
    if (
      typeof retries !== "number" ||
      !Number.isFinite(retries) ||
      !Number.isInteger(retries) ||
      retries < 0
    ) {
      return `max_implementation_retries must be a non-negative integer, got ${JSON.stringify(retries)}`;
    }
  }
  for (const role of cfg.defaults?.roles ?? []) {
    if (!VALID_ROLES.has(role)) {
      return `invalid role ${JSON.stringify(role)}: must be one of fullsend, triage, code, coder, review, fix, retro, prioritize`;
    }
  }
  for (const agent of cfg.agents ?? []) {
    if (!VALID_ROLES.has(agent.role)) {
      return `invalid agent role ${JSON.stringify(agent.role)}: must be one of fullsend, triage, code, coder, review, fix, retro, prioritize`;
    }
  }
  return null;
}

/** Agent rows for secrets-layer analyze (mirrors `config.OrgConfig.Agents`). */
export function agentsFromConfig(cfg: OrgConfigYaml): { role: string }[] {
  return (cfg.agents ?? []).map((a) => ({ role: a.role }));
}

/** Enabled repo names for enrollment-layer analyze (sorted). */
export function enabledReposFromConfig(cfg: OrgConfigYaml): string[] {
  const repos = cfg.repos ?? {};
  return Object.entries(repos)
    .filter(([, v]) => v?.enabled === true)
    .map(([name]) => name)
    .sort((a, b) => a.localeCompare(b));
}
