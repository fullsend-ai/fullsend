type TurnstileApi = {
  render: (
    container: string | HTMLElement,
    params: Record<string, unknown>,
  ) => string;
  execute: (container: string | HTMLElement) => void;
  remove: (widgetId: string) => void;
};

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

/**
 * Runs an invisible Turnstile challenge and resolves with the one-time token for
 * `POST /api/oauth/token` (Worker validates with siteverify).
 */
export async function obtainTurnstileToken(siteKey: string): Promise<string> {
  await loadTurnstileScript();
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

    const teardown = (id: string | null) => {
      if (id) {
        try {
          turnstile.remove(id);
        } catch {
          /* ignore */
        }
      }
      host.remove();
    };

    const run = () => {
      let widgetId: string | undefined;
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
