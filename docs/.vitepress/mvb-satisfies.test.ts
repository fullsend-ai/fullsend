import { describe, expect, it } from "vitest";
import semver from "semver";
import { getLatestPatchMatching } from "./mvb-satisfies";

describe("getLatestPatchMatching", () => {
  it("emits the latest patch of each minor that satisfies the range", () => {
    expect(
      getLatestPatchMatching(["v0.36.0", "v0.37.0", "v0.38.0", "v0.38.1", "v0.39.0"], ">=0.37.0"),
    ).toBe("0.37.0 || 0.38.1 || 0.39.0");
  });

  it("drops older patches of the same minor under satisfies-per-tag", () => {
    const range = getLatestPatchMatching(["0.22.0", "0.22.4", "0.23.0"], ">=0.22.0");
    expect(semver.satisfies("0.22.0", range)).toBe(false);
    expect(semver.satisfies("0.22.4", range)).toBe(true);
    expect(semver.satisfies("0.23.0", range)).toBe(true);
  });

  // mvb calls semver.satisfies(tag, range, {includePrerelease: true}).
  it("does not satisfy later patches in the same minor, including -rc tags", () => {
    const range = getLatestPatchMatching(
      ["v0.37.0-rc.1", "v0.37.0", "v0.37.1-rc.1", "v0.38.0-rc.1"],
      ">=0.37.0",
    );
    expect(range).toBe("0.37.0");
    const mvbOpts = { includePrerelease: true };
    expect(semver.satisfies("0.37.0", range, mvbOpts)).toBe(true);
    expect(semver.satisfies("0.37.0-rc.1", range, mvbOpts)).toBe(false);
    expect(semver.satisfies("0.37.1-rc.1", range, mvbOpts)).toBe(false);
    expect(semver.satisfies("0.38.0-rc.1", range, mvbOpts)).toBe(false);
  });

  it("falls back to the input range when nothing qualifies", () => {
    expect(getLatestPatchMatching(["v0.36.0"], ">=0.37.0")).toBe(">=0.37.0");
    expect(getLatestPatchMatching([], ">=0.37.0")).toBe(">=0.37.0");
  });

  it("skips tags that are not valid semver", () => {
    expect(getLatestPatchMatching(["v0", "not-a-version", "v0.37.0"], ">=0.37.0")).toBe("0.37.0");
  });

  it("restricts ranges to specific minors for major versions >= 1", () => {
    // Exact comparators keep 1.2.x and 1.3.x separate; a caret ^1.2.1 would span to <2.0.0.
    expect(getLatestPatchMatching(["v1.2.0", "v1.2.1", "v1.3.0", "v1.3.2"], ">=1.0.0")).toBe(
      "1.2.1 || 1.3.2",
    );
  });
});
