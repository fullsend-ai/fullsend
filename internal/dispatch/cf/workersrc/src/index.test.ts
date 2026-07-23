// Smoke tests for the CF Worker mint bridge.
//
// These tests run inside @cloudflare/vitest-pool-workers (Miniflare)
// with the real Go WASM binary. They verify that the bridge boots —
// not full mint OIDC coverage. Run after `make wasm-stage` so that
// mintcore.wasm and wasm_exec.js are present.
import { SELF } from "cloudflare:test";
import { describe, expect, it } from "vitest";

describe("mint worker bridge smoke", () => {
  it("boots and serves /health", async () => {
    const resp = await SELF.fetch("https://worker.test/health");
    expect(resp.status).toBe(200);

    const body = await resp.text();
    expect(body).toContain("ok");
  });

  it("returns 404 for unknown paths", async () => {
    const resp = await SELF.fetch("https://worker.test/nonexistent");
    // Go's ServeHTTP routes this; unmatched paths return 404.
    expect(resp.status).toBe(404);
  });

  it("returns 405 for non-POST on /v1/token", async () => {
    const resp = await SELF.fetch("https://worker.test/v1/token", {
      method: "GET",
    });
    // The mint handler rejects non-POST on the token endpoint.
    expect(resp.status).toBe(405);
  });
});
