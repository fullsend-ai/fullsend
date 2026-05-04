import { describe, expect, it } from "vitest";
import { assertAllowedReturnTo } from "./previewHandoff";

describe("assertAllowedReturnTo", () => {
  it("accepts exact https preview origin", () => {
    expect(() =>
      assertAllowedReturnTo(
        "https://pr-123.fullsend-admin.pages.dev/",
        ["https://pr-123.fullsend-admin.pages.dev"],
      ),
    ).not.toThrow();
  });

  it("rejects mismatched host", () => {
    expect(() =>
      assertAllowedReturnTo("https://evil.example/", [
        "https://pr-123.fullsend-admin.pages.dev",
      ]),
    ).toThrow(/return_to/);
  });
});
