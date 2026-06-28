import { describe, it, expect } from "vitest";
import { filterOrgsBySearch } from "./filter";

describe("filterOrgsBySearch", () => {
  it("matches prefix case-insensitively", () => {
    expect(
      filterOrgsBySearch([{ login: "Alpha" }, { login: "bee" }], "a").map((o) => o.login),
    ).toEqual(["Alpha"]);
  });

  it("matches substring anywhere in login", () => {
    expect(
      filterOrgsBySearch([{ login: "foo-bar-org" }, { login: "other" }], "bar").map((o) => o.login),
    ).toEqual(["foo-bar-org"]);
  });

  it("sorts alphabetically when query is empty", () => {
    expect(filterOrgsBySearch([{ login: "z" }, { login: "a" }], "").map((o) => o.login)).toEqual([
      "a",
      "z",
    ]);
  });
});
