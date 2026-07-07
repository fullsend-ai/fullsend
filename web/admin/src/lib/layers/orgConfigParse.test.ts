import { describe, expect, it } from "vitest";
import {
  agentsFromConfig,
  enabledReposFromConfig,
  MAX_ORG_CONFIG_YAML_DEPTH,
  MAX_ORG_CONFIG_YAML_UTF8_BYTES,
  OrgConfigYamlLimitError,
  parseOrgConfigYaml,
  validateOrgConfig,
} from "./orgConfigParse";

describe("validateOrgConfig", () => {
  it("accepts minimal valid config", () => {
    const cfg = parseOrgConfigYaml(`version: "1"
dispatch:
  platform: github-actions
defaults:
  roles: [fullsend]
  max_implementation_retries: 0
repos: {}
`);
    expect(validateOrgConfig(cfg)).toBeNull();
  });

  it("rejects bad version", () => {
    expect(
      validateOrgConfig(
        parseOrgConfigYaml(`version: "9"
dispatch:
  platform: github-actions
`),
      ),
    ).toContain("unsupported version");
  });

  it("rejects agents as a string", () => {
    expect(() =>
      parseOrgConfigYaml(`version: "1"
dispatch:
  platform: github-actions
agents: not-a-list
`),
    ).toThrow(/agents must be a list/);
  });

  it("rejects repos as a list", () => {
    expect(() =>
      parseOrgConfigYaml(`version: "1"
dispatch:
  platform: github-actions
repos: []
`),
    ).toThrow(/repos must be a mapping/);
  });

  it("rejects non-integer max_implementation_retries", () => {
    const cfg = parseOrgConfigYaml(`version: "1"
dispatch:
  platform: github-actions
defaults:
  roles: [fullsend]
  max_implementation_retries: 2.5
repos: {}
`);
    expect(validateOrgConfig(cfg)).toMatch(/non-negative integer/);
  });

  it("rejects invalid agent role", () => {
    const cfg = parseOrgConfigYaml(`version: "1"
dispatch:
  platform: github-actions
defaults:
  roles: [fullsend]
agents:
  - role: not-a-valid-role
repos: {}
`);
    expect(validateOrgConfig(cfg)).toMatch(/invalid agent role/);
  });

  it("lists agents and enabled repos from config", () => {
    const cfg = parseOrgConfigYaml(`version: "1"
dispatch:
  platform: github-actions
defaults:
  roles: [fullsend]
agents:
  - role: triage
    slug: t
repos:
  zed:
    enabled: false
  alpha:
    enabled: true
  beta:
    enabled: true
`);
    expect(validateOrgConfig(cfg)).toBeNull();
    expect(agentsFromConfig(cfg)).toEqual([{ role: "triage" }]);
    expect(enabledReposFromConfig(cfg)).toEqual(["alpha", "beta"]);
  });

  it("rejects YAML larger than the UTF-8 byte limit with a clear message", () => {
    const oversize = " ".repeat(MAX_ORG_CONFIG_YAML_UTF8_BYTES + 1);
    expect(() => parseOrgConfigYaml(oversize)).toThrow(OrgConfigYamlLimitError);
    try {
      parseOrgConfigYaml(oversize);
    } catch (e) {
      expect(e).toBeInstanceOf(OrgConfigYamlLimitError);
      expect((e as Error).message).toContain("maximum file size");
      expect((e as Error).message).toMatch(/\d+ bytes/);
    }
  });

  it("rejects YAML nested deeper than the depth limit with a clear message", () => {
    const lines: string[] = ['version: "1"', "dispatch:", "  platform: github-actions"];
    let indent = "  ";
    for (let i = 0; i < MAX_ORG_CONFIG_YAML_DEPTH + 2; i++) {
      lines.push(`${indent}L${i}:`);
      indent += "  ";
    }
    lines.push(`${indent}x: 1`);
    const deep = lines.join("\n");
    expect(() => parseOrgConfigYaml(deep)).toThrow(OrgConfigYamlLimitError);
    try {
      parseOrgConfigYaml(deep);
    } catch (e) {
      expect(e).toBeInstanceOf(OrgConfigYamlLimitError);
      expect((e as Error).message).toContain("nested too deeply");
      expect((e as Error).message).toContain(String(MAX_ORG_CONFIG_YAML_DEPTH));
    }
  });

  it("rejects flow-style sequence nesting deeper than the depth limit", () => {
    let flow = "1";
    for (let i = 0; i < MAX_ORG_CONFIG_YAML_DEPTH + 3; i++) {
      flow = `[${flow}]`;
    }
    const doc = `version: "1"
dispatch:
  platform: github-actions
k: ${flow}
`;
    expect(() => parseOrgConfigYaml(doc)).toThrow(OrgConfigYamlLimitError);
  });
});
