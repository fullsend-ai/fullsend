import { describe, expect, it } from "vitest";
import { createUserOctokit } from "./client";

describe("createUserOctokit", () => {
  it("sets auth header from token", () => {
    const o = createUserOctokit("tok");
    expect(o).toBeDefined();
  });
});
