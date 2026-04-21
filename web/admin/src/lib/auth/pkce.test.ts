// @vitest-environment node
import { describe, expect, it } from "vitest";
import { challengeS256, randomVerifier } from "./pkce";

describe("pkce", () => {
  it("randomVerifier length and charset", () => {
    const v = randomVerifier();
    expect(v.length).toBeGreaterThanOrEqual(43);
    expect(v).toMatch(/^[A-Za-z0-9_-]+$/);
  });

  it("challengeS256 is stable for a fixed verifier", async () => {
    const verifier =
      "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk";
    const c = await challengeS256(verifier);
    expect(c).toMatch(/^[A-Za-z0-9_-]+$/);
    expect(c).toBe(await challengeS256(verifier));
  });

  it("challengeS256 matches RFC 7636 Appendix B test vector", async () => {
    const verifier =
      "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk";
    const challenge = await challengeS256(verifier);
    expect(challenge).toBe("E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM");
  });
});
