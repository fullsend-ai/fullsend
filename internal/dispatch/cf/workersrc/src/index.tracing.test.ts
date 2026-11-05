// Bridge tests for OTLP trace export from the CF Worker.
//
// The Worker normally imports ../mintcore.wasm, which is built without a
// tracing endpoint. These tests swap in ../mintcore-traced.wasm, built by
// `make wasm-stage-traced` with the endpoint stamped to TEST_COLLECTOR_URL (the
// same -ldflags mechanism `fullsend mint deploy` uses). They drive the
// real Go WASM tracer through the fetch bridge in index.ts and replace
// only the Worker's outbound fetch, so they cover the noRedirect/timeoutMs
// callback arguments, redirect handling, response-body cancellation and
// the export deadline.
import { env } from "cloudflare:workers";
import {
  createExecutionContext,
  waitOnExecutionContext,
} from "cloudflare:test";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../mintcore.wasm", async () => import("../mintcore-traced.wasm"));

import worker, { type Env } from "./index";

const TEST_COLLECTOR_URL = "https://collector.test/v1/traces";
const COLLECTOR_AUTH = "Bearer collector-secret-for-tests";
// The Go tracer's export deadline is 2 s (defaultTraceExportTimeout).
const EXPORT_BUDGET_MS = 2_000;

const tracedEnv = {
  ...env,
  MINT_OTLP_HEADERS: `Authorization=${COLLECTOR_AUTH}`,
} as Env;

type FetchCall = { url: string; init: RequestInit };

/** Replace global fetch, recording calls and delegating to handler. */
function stubFetch(
  handler: (url: string, init: RequestInit) => Promise<Response>,
): FetchCall[] {
  const calls: FetchCall[] = [];
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = typeof input === "string" ? input : String(input);
      calls.push({ url, init });
      return handler(url, init);
    },
  );
  return calls;
}

/**
 * Send an unauthenticated mint request. The mint rejects it with a 4xx
 * before any upstream call, and the tracer exports the request's span
 * synchronously before the response is returned. (/health is not traced.)
 */
async function postToken(): Promise<Response> {
  const ctx = createExecutionContext();
  const res = await worker.fetch(
    new Request("https://worker.test/v1/token", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    }),
    tracedEnv,
    ctx,
  );
  await waitOnExecutionContext(ctx);
  return res;
}

/** Export problems must not change the mint result: a 4xx, never 429/5xx. */
function expectMintRejection(res: Response): void {
  expect(res.status).toBeGreaterThanOrEqual(400);
  expect(res.status).toBeLessThan(500);
  expect(res.status).not.toBe(429);
}

function header(init: RequestInit, name: string): string | null {
  return new Headers(init.headers).get(name);
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("mint worker OTLP export bridge", () => {
  it("exports spans with the collector secret and without following redirects", async () => {
    const calls = stubFetch(async () => new Response("{}", { status: 200 }));

    expectMintRejection(await postToken());

    expect(calls).toHaveLength(1);
    const { url, init } = calls[0];
    expect(url).toBe(TEST_COLLECTOR_URL);
    expect(init.method).toBe("POST");
    expect(header(init, "authorization")).toBe(COLLECTOR_AUTH);
    expect(header(init, "content-type")).toBe("application/json");
    // noRedirect must reach the host fetch so credentials are never
    // replayed to a redirect target.
    expect(init.redirect).toBe("manual");
    expect(init.signal).toBeInstanceOf(AbortSignal);
    const payload = JSON.parse(String(init.body)) as {
      resourceSpans?: unknown[];
    };
    expect(payload.resourceSpans?.length).toBeGreaterThan(0);
  });

  it("does not follow a collector redirect", async () => {
    const calls = stubFetch(
      async () =>
        new Response(null, {
          status: 302,
          headers: { Location: "https://elsewhere.test/steal" },
        }),
    );

    expectMintRejection(await postToken());

    expect(calls.map((c) => c.url)).toEqual([TEST_COLLECTOR_URL]);
    expect(calls[0].init.redirect).toBe("manual");
  });

  it("cancels the collector response body instead of reading it", async () => {
    let cancelled = false;
    stubFetch(async () => {
      // Never enqueues or closes: a read would hang until the deadline.
      const body = new ReadableStream<Uint8Array>({
        cancel() {
          cancelled = true;
        },
      });
      return new Response(body, { status: 200 });
    });

    const started = Date.now();
    expectMintRejection(await postToken());
    // Reading an endless body would run to the export budget.
    expect(Date.now() - started).toBeLessThan(EXPORT_BUDGET_MS);
    expect(cancelled).toBe(true);
  });

  it("fails open when the collector request fails", async () => {
    const calls = stubFetch(async () => {
      throw new Error("collector unreachable");
    });

    expectMintRejection(await postToken());
    expect(calls).toHaveLength(1);
  });

  it("aborts a stalled collector fetch within the export budget", async () => {
    let signal: AbortSignal | undefined;
    stubFetch(
      (_url, init) =>
        new Promise<Response>((_resolve, reject) => {
          signal = init.signal as AbortSignal;
          signal.addEventListener("abort", () =>
            reject(new Error("aborted by signal")),
          );
        }),
    );

    const started = Date.now();
    const res = await postToken();
    const elapsed = Date.now() - started;

    // The mint response is not lost, and it returns at the export
    // deadline rather than the 20 s host-fetch timeout.
    expectMintRejection(res);
    expect(elapsed).toBeLessThan(EXPORT_BUDGET_MS + 3_000);
    // The underlying fetch was aborted, not left running.
    expect(signal).toBeDefined();
    expect(signal?.aborted).toBe(true);
  }, 15_000);
});
