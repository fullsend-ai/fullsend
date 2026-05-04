type TurnstileApi = {
  render: (
    container: string | HTMLElement,
    params: Record<string, unknown>,
  ) => string;
  execute: (container: string | HTMLElement) => void;
  remove: (widgetId: string) => void;
};

/** Wall-clock cap so Turnstile cannot leave OAuth boot hanging indefinitely (review / UX). */
const TURNSTILE_TOKEN_DEADLINE_MS = 120_000;

function turnstileGlobal(): TurnstileApi | undefined {
  return (globalThis as unknown as { turnstile?: TurnstileApi }).turnstile;
}

function loadTurnstileScript(): Promise<void> {
  const existing = document.querySelector(
    "script[data-fullsend-turnstile]",
  ) as HTMLScriptElement | null;
  if (existing) {
    return existing.dataset.loaded === "1"
      ? Promise.resolve()
      : new Promise((resolve, reject) => {
          existing.addEventListener("load", () => resolve(), { once: true });
          existing.addEventListener(
            "error",
            () => reject(new Error("Turnstile script failed")),
            { once: true },
          );
        });
  }

  return new Promise((resolve, reject) => {
    const s = document.createElement("script");
    s.src =
      "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";
    // Dynamically appended scripts default to async=true; Turnstile errors if async/defer is set
    // when using turnstile.ready(). We use onload + render/execute instead and force async off.
    s.async = false;
    s.dataset.fullsendTurnstile = "1";
    s.onload = () => {
      s.dataset.loaded = "1";
      resolve();
    };
    s.onerror = () => reject(new Error("Turnstile script failed"));
    document.head.appendChild(s);
  });
}

/** Abort `out` when either input signal aborts (used to combine user cancel + deadline). */
function mergeAbortSignals(
  a?: AbortSignal,
  b?: AbortSignal,
): AbortSignal | undefined {
  if (!a && !b) return undefined;
  if (!a) return b;
  if (!b) return a;
  const out = new AbortController();
  const fire = () => {
    try {
      out.abort();
    } catch {
      /* ignore */
    }
  };
  if (a.aborted || b.aborted) {
    fire();
    return out.signal;
  }
  a.addEventListener("abort", fire, { once: true });
  b.addEventListener("abort", fire, { once: true });
  return out.signal;
}

/**
 * Runs an invisible Turnstile challenge and resolves with the one-time token for
 * `POST /api/oauth/token` (Worker validates with siteverify).
 *
 * Honors optional `signal` (caller abort / navigation). Also enforces a maximum wait
 * (120s) so a stuck widget cannot block OAuth forever.
 */
export async function obtainTurnstileToken(
  siteKey: string,
  userSignal?: AbortSignal,
): Promise<string> {
  if (userSignal?.aborted) {
    throw new DOMException("Aborted", "AbortError");
  }

  const deadline = new AbortController();
  const tid = window.setTimeout(() => deadline.abort(), TURNSTILE_TOKEN_DEADLINE_MS);
  const merged = mergeAbortSignals(userSignal, deadline.signal);

  try {
    return await obtainTurnstileTokenWithSignal(siteKey, merged);
  } catch (e) {
    if (deadline.signal.aborted && !userSignal?.aborted) {
      throw new Error("Turnstile token timed out");
    }
    throw e;
  } finally {
    window.clearTimeout(tid);
  }
}

async function obtainTurnstileTokenWithSignal(
  siteKey: string,
  signal?: AbortSignal,
): Promise<string> {
  if (signal?.aborted) {
    throw new DOMException("Aborted", "AbortError");
  }

  await loadTurnstileScript();
  if (signal?.aborted) {
    throw new DOMException("Aborted", "AbortError");
  }

  const turnstile = turnstileGlobal();
  if (!turnstile?.render || !turnstile.execute) {
    throw new Error("Turnstile API unavailable");
  }

  return new Promise((resolve, reject) => {
    const host = document.createElement("div");
    host.style.position = "fixed";
    host.style.width = "1px";
    host.style.height = "1px";
    host.style.left = "-9999px";
    host.style.top = "0";
    document.body.appendChild(host);

    let widgetId: string | undefined;

    const detachAbort = () => {
      signal?.removeEventListener("abort", onAbort);
    };

    const teardown = (id: string | null) => {
      detachAbort();
      if (id) {
        try {
          turnstile.remove(id);
        } catch {
          /* ignore */
        }
      }
      host.remove();
    };

    const onAbort = () => {
      teardown(widgetId ?? null);
      reject(new DOMException("Aborted", "AbortError"));
    };

    signal?.addEventListener("abort", onAbort, { once: true });

    const run = () => {
      try {
        widgetId = turnstile.render(host, {
          sitekey: siteKey,
          execution: "execute",
          callback: (token: string) => {
            teardown(widgetId ?? null);
            resolve(token);
          },
          "error-callback": () => {
            teardown(widgetId ?? null);
            reject(new Error("Turnstile challenge failed"));
          },
        });
      } catch (e) {
        teardown(null);
        reject(e instanceof Error ? e : new Error(String(e)));
        return;
      }
      try {
        turnstile.execute(host);
      } catch (e) {
        teardown(widgetId ?? null);
        reject(e instanceof Error ? e : new Error(String(e)));
      }
    };

    run();
  });
}
