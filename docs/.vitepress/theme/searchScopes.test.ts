import { describe, expect, it } from "vitest";
import { matchesActiveScopes } from "./searchScopes";

const scopes = [
  { label: "Guides", prefixes: ["/docs/guides/", "/docs/cli/"] },
  { label: "Design Docs", prefixes: ["/docs/problems/", "/docs/ADRs/"] },
  { label: "Experiments", prefixes: ["/docs/experiments/"] },
  { label: "Others", prefixes: [], others: true },
];

describe("matchesActiveScopes", () => {
  it("returns true for any id when no scopes are active", () => {
    expect(matchesActiveScopes("/docs/guides/setup", scopes, new Set())).toBe(true);
    expect(matchesActiveScopes("/random/page", scopes, new Set())).toBe(true);
  });

  it("filters to a single active scope", () => {
    const active = new Set([0]);
    expect(matchesActiveScopes("/docs/guides/setup", scopes, active)).toBe(true);
    expect(matchesActiveScopes("/docs/cli/install", scopes, active)).toBe(true);
    expect(matchesActiveScopes("/docs/problems/auth", scopes, active)).toBe(false);
  });

  it("combines prefixes from multiple active scopes (OR)", () => {
    const active = new Set([0, 2]);
    expect(matchesActiveScopes("/docs/guides/setup", scopes, active)).toBe(true);
    expect(matchesActiveScopes("/docs/experiments/wip", scopes, active)).toBe(true);
    expect(matchesActiveScopes("/docs/ADRs/001", scopes, active)).toBe(false);
  });

  it("returns false when id matches no active prefix", () => {
    const active = new Set([1]);
    expect(matchesActiveScopes("/docs/guides/setup", scopes, active)).toBe(false);
    expect(matchesActiveScopes("/unrelated/page", scopes, active)).toBe(false);
  });

  it("handles an out-of-bounds scope index gracefully", () => {
    const active = new Set([99]);
    expect(matchesActiveScopes("/docs/guides/setup", scopes, active)).toBe(false);
  });

  it("others scope matches pages not covered by any prefix", () => {
    const active = new Set([3]);
    expect(matchesActiveScopes("/docs/vision", scopes, active)).toBe(true);
    expect(matchesActiveScopes("/docs/architecture", scopes, active)).toBe(true);
    expect(matchesActiveScopes("/docs/guides/setup", scopes, active)).toBe(false);
    expect(matchesActiveScopes("/docs/ADRs/001", scopes, active)).toBe(false);
  });

  it("others combined with a regular scope matches both", () => {
    const active = new Set([0, 3]);
    expect(matchesActiveScopes("/docs/guides/setup", scopes, active)).toBe(true);
    expect(matchesActiveScopes("/docs/vision", scopes, active)).toBe(true);
    expect(matchesActiveScopes("/docs/ADRs/001", scopes, active)).toBe(false);
  });
});
