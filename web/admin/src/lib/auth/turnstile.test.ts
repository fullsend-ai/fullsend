import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { obtainTurnstileToken } from "./turnstile";

describe("obtainTurnstileToken", () => {
  beforeEach(() => {
    document.body.replaceChildren();
    document.head.querySelectorAll("script[data-fullsend-turnstile]").forEach((n) => n.remove());
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    delete (globalThis as unknown as { turnstile?: unknown }).turnstile;
  });

  it("rejects with timeout when Turnstile never invokes the token callback", async () => {
    const existing = document.createElement("script");
    existing.dataset.fullsendTurnstile = "1";
    existing.dataset.loaded = "1";
    document.head.appendChild(existing);

    (globalThis as unknown as { turnstile: object }).turnstile = {
      render: vi.fn(() => "widget-id"),
      execute: vi.fn(),
      remove: vi.fn(),
    };

    const p = obtainTurnstileToken("0x4AAA_sitekey");
    const assertRejected = expect(p).rejects.toThrow(/Turnstile token timed out/);
    await vi.advanceTimersByTimeAsync(120_000 + 1);
    await assertRejected;
  });

  it("rejects with AbortError when signal is already aborted", async () => {
    const ac = new AbortController();
    ac.abort();
    await expect(obtainTurnstileToken("k", ac.signal)).rejects.toMatchObject({
      name: "AbortError",
    });
  });
});
