// Thin Cloudflare Worker adapter for the fullsend token mint.
//
// All mint logic (OIDC verification, claims validation, JWT signing,
// token minting, path routing) stays in Go via the mintcore WASM module.
// This adapter handles I/O only:
//   - Worker secrets -> PEM key access (via pemCallback)
//   - Worker env vars -> config lookup (via getEnvCallback)
//   - Host fetch -> outbound HTTP (via fetchCallback)
//   - Fetch Request/Response mapping for mintcoreHandleFetch
//
// The WASM module is compiled from cmd/mint-wasm with
// GOOS=js GOARCH=wasm and registers two global functions via syscall/js:
//
//   - mintcoreInitMint(getEnvCallback, fetchCallback, pemCallback): string
//     Initializes the mint handler using a sync getEnv callback for config
//     lookups (same pattern as PEM/fetch). Returns "" on success or an
//     error message on failure.
//
//   - mintcoreHandleFetch(method, url, headersJSON, body): Promise<{status, headers, body}>
//     Routes a Fetch request through Go's http.Handler (ServeHTTP).
//     Authorization is passed inside headersJSON, not as a separate argument.
//     Returns a truly async Promise — ServeHTTP runs on a separate goroutine
//     so that the js.FuncOf callback returns immediately, freeing the JS
//     event loop for awaitPromise calls (host fetch, PEM lookup) to settle.
//     Resolves to {status: number, headers: string, body: string}
//     where headers is a JSON-encoded map.
//
// wasm_exec.js is the Go WASM support file from the Go toolchain
// ($(go env GOROOT)/lib/wasm/wasm_exec.js for Go ≥1.24). It must be
// copied into this directory at build time. The Go class it exports
// bootstraps the Go runtime and provides the import object required
// by the WASM binary.
import "../wasm_exec.js";

// ES module import of the compiled WASM binary. Wrangler handles this
// via the [[rules]] CompiledWasm glob — no [wasm_modules] binding needed.
// The binary is built from cmd/mint-wasm and staged into this directory
// by `make wasm-stage`.
import mintcoreWasm from "../mintcore.wasm";

/**
 * Worker environment bindings.
 *
 * PEM secrets follow the naming convention <ROLE>_APP_PEM
 * (e.g. CODER_APP_PEM, TRIAGE_APP_PEM). The Go WASM bridge handles
 * role-to-secret-name mapping (PemSecretRole); the JS callback just
 * looks up the secret name it receives from Go.
 */
export interface Env {
  /** JSON map of role -> GitHub App ID. */
  ROLE_APP_IDS: string;
  /** Comma-separated list of allowed GitHub orgs (optional for per-repo-only deployments). */
  ALLOWED_ORGS?: string;
  /** Comma-separated list of allowed roles (derived from ROLE_APP_IDS if unset). */
  ALLOWED_ROLES?: string;
  /** Comma-separated workflow file patterns (empty = reject all; "*" = any). */
  ALLOWED_WORKFLOW_FILES?: string;
  /** Comma-separated repos using per-repo WIF providers. */
  PER_REPO_WIF_REPOS?: string;
  /** Comma-separated repos trusted to host workflows for per-repo callers. */
  WORKFLOW_HOST_REPOS?: string;
  /** JSON-encoded map of custom role permissions. */
  CUSTOM_ROLE_PERMISSIONS?: string;
  /**
   * Native Workers rate limiter for POST /v1/token. Bound via
   * [[ratelimits]] in wrangler.toml. The key includes the request
   * hostname so preview aliases (which produce distinct hostnames)
   * get isolated counters without extra namespace_id patching.
   */
  MINT_TOKEN_RATE_LIMITER: RateLimit;
  /**
   * Dynamic secret access: Worker secrets are accessed by name.
   * PEM keys are stored as secrets named <ROLE>_APP_PEM.
   * TypeScript index signature covers these dynamic keys.
   */
  [key: string]: unknown;
}

/**
 * Deterministic configuration error — thrown when required Worker env
 * fields are missing or empty. Unlike transient WASM errors, a config
 * error will not resolve on retry (the env doesn't change between
 * requests), so GoWasm.init() caches the rejection to avoid
 * re-running WebAssembly.instantiate + go.run on every request.
 */
class ConfigError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ConfigError";
  }
}

/**
 * Create a synchronous getEnv callback for the WASM module.
 *
 * The Go side wraps this into a `func(string) string` and passes it to
 * NewHandler. Mintcore decides which keys to read (ROLE_APP_IDS,
 * ALLOWED_ROLES, etc.) — the JS side simply looks up the key in the
 * Worker env bindings. OIDC audience is a compile-time constant in the
 * Go code and is no longer read from the environment.
 *
 * Returns "" for missing or non-string bindings, matching os.Getenv
 * behavior for unset variables.
 */
function createGetEnvCallback(
  env: Env,
): (key: string) => string {
  return (key: string): string => {
    const v = env[key];
    return typeof v === "string" ? v : "";
  };
}

/**
 * Detect role names that collide after hyphen→underscore normalization.
 *
 * Cloudflare Worker secret names must be valid JS identifiers (no
 * hyphens), so createPemCallback maps hyphens to underscores when
 * constructing the secret key (e.g. "my-role" → MY_ROLE_APP_PEM).
 * If two distinct role names normalize to the same secret key
 * (e.g. "my-role" and "my_role" both → MY_ROLE_APP_PEM), the PEM
 * lookup becomes ambiguous. Fail fast with a clear ConfigError so
 * operators fix wrangler secret naming before requests start failing.
 *
 * Throws ConfigError listing every colliding pair.
 */
function detectRoleSecretCollisions(roleAppIDs: Record<string, string>): void {
  const normalized = new Map<string, string>(); // normalized → original
  const collisions: string[] = [];
  for (const role of Object.keys(roleAppIDs)) {
    const key = role.replace(/-/g, "_").toUpperCase();
    const existing = normalized.get(key);
    if (existing !== undefined && existing !== role) {
      collisions.push(`"${existing}" and "${role}" both map to secret ${key}_APP_PEM`);
    } else {
      normalized.set(key, role);
    }
  }
  if (collisions.length > 0) {
    throw new ConfigError(
      `role name secret collision after hyphen→underscore normalization: ${collisions.join("; ")}`,
    );
  }
}

/**
 * Create a PEM accessor callback for the WASM module.
 *
 * The Go side (HostPEMAccessor.AccessPEM) calls PemSecretRole(role)
 * to map role names (e.g. "fix" -> "coder") and then invokes this
 * callback with the mapped secret role name. This callback converts
 * that to the Worker secret key format (<ROLE>_APP_PEM) and looks it
 * up in the env bindings.
 *
 * Must return a Promise<string> because Go calls awaitPromise on the
 * result.
 */
function createPemCallback(
  env: Env,
): (secretRole: string) => Promise<string> {
  // Note: secretRole is pre-validated by the Go side (ValidateRoleName in
  // pem_js.go) before the JS callback is invoked. Only lowercase
  // alphanumeric names with hyphens/underscores (no double-hyphens)
  // reach this callback, so toUpperCase() is safe for secret key construction.
  //
  // Cloudflare Worker secret/binding names must be valid JS identifiers
  // (no hyphens). Go's RolePattern allows hyphens in role names, so we
  // map them to underscores when constructing the secret key. Operators
  // must name their CF secrets with underscores (e.g. role "my-role"
  // → secret MY_ROLE_APP_PEM).
  return (secretRole: string): Promise<string> => {
    const secretName = `${secretRole.replace(/-/g, "_").toUpperCase()}_APP_PEM`;
    const pem = env[secretName];
    if (typeof pem !== "string" || pem === "") {
      // Reject with a plain string — not new Error(...) — so that Go's
      // awaitPromise + Value.String() sees the message directly instead
      // of the opaque "[object Error]".
      return Promise.reject(`PEM secret ${secretName} not found or empty`);
    }
    return Promise.resolve(pem);
  };
}

/**
 * Create a fetch callback for the WASM module.
 *
 * The Go side (HostFetchDoer.Do) calls this with
 * (method, url, headersJSON, body) and expects a Promise resolving
 * to {status: number, headers: string, body: string} where headers
 * is a JSON-encoded map of response headers.
 */
function createFetchCallback(): (
  method: string,
  url: string,
  headersJSON: string,
  body: string,
) => Promise<{ status: number; headers: string; body: string }> {
  return async (
    method: string,
    url: string,
    headersJSON: string,
    body: string,
  ): Promise<{ status: number; headers: string; body: string }> => {
    // Wrap the entire callback body so that any thrown Error (from
    // JSON.parse, fetch, or resp.text) is converted to a plain-string
    // rejection. Go's awaitPromise + Value.String() on an Error object
    // yields "[object Error]"; a plain string is observable directly.
    try {
      let headers: Record<string, string>;
      try {
        headers = JSON.parse(headersJSON);
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err);
        return Promise.reject(`failed to parse fetch headers JSON: ${msg}`);
      }
      const resp = await fetch(url, {
        method,
        headers,
        body: method !== "GET" && method !== "HEAD" ? body : undefined,
      });
      const respBody = await resp.text();
      const respHeaders = JSON.stringify(
        Object.fromEntries(resp.headers.entries()),
      );
      return {
        status: resp.status,
        headers: respHeaders,
        body: respBody,
      };
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      return Promise.reject(`fetch callback failed: ${msg}`);
    }
  };
}

/**
 * Self-imposed wall-clock latency budget (ms) for mintcoreHandleFetch.
 *
 * This guards against hung I/O (e.g. a stalled outbound fetch) or a
 * wedged Go cooperative scheduler — scenarios where the handler never
 * resolves. 25 s is wall-clock time, not CPU time (Workers CPU limits
 * exclude time spent in `await`), so it is not derived from the
 * platform CPU cap.
 *
 * The Go WASM handler applies its own context deadline (20 s) that
 * is shorter than this JS-side timeout. Under normal operation, the
 * Go handler returns a clean error (HTTP 502) before this timeout
 * fires. This JS-side timeout is a defense-in-depth backstop for
 * cases where the Go scheduler itself is wedged and cannot honor
 * its own context deadline.
 *
 * On timeout, the GoWasm singleton is marked for recovery (see
 * fetch handler). The next request re-initializes the Go WASM
 * runtime, booting a fresh instance. The old runtime leaks but
 * cannot interfere with the new one.
 */
const HANDLE_FETCH_TIMEOUT_MS = 25_000;

/**
 * GoWasm manages the lifecycle of the Go WASM runtime.
 *
 * Architecture note — single shared instance per warm isolate:
 * Cloudflare Workers reuse a single V8 isolate across sequential
 * requests for the same Worker. Because Go's WASM target
 * (GOOS=js GOARCH=wasm) starts a single cooperative runtime via
 * `go.run()`, there is exactly one Go WASM instance per warm
 * isolate. Concurrent requests (possible during `await` points)
 * share the cooperative `GOOS=js` scheduler within that instance.
 * Idle isolates may be evicted or have their timers throttled by
 * the Workers runtime.
 *
 * Recovery strategy — reinit-on-timeout:
 * If the Go scheduler stalls or a request times out
 * (HANDLE_FETCH_TIMEOUT_MS), the GoWasm instance is marked as
 * needing recovery. The *next* request re-initializes the Go
 * WASM runtime (new WebAssembly.instantiate + go.run) rather than
 * permanently refusing all requests with 503.
 *
 * The old Go runtime leaks (its blocked goroutine and memory
 * cannot be reclaimed), but this is bounded: Cloudflare evicts
 * warm isolates after a short idle period, cleaning up the
 * leaked instance. For preview mints with low traffic, accepting
 * one leaked runtime is far better than permanent 503s.
 *
 * Late-finish safety: a timed-out goroutine that eventually
 * completes resolves an already-raced Promise — the resolution
 * is silently ignored. The old Go runtime registered its exports
 * on `globalThis` once during `main()`; it does not re-register
 * on completion, so the new runtime's exports are not overwritten.
 *
 * The standard Go WASM target (GOOS=js GOARCH=wasm) requires the
 * wasm_exec.js support code to bootstrap the Go runtime. The Go class
 * from wasm_exec.js provides the import object that satisfies the
 * WASM binary's host imports (gojs.*, syscall/js bridges).
 *
 * The WASM bridge (cmd/mint-wasm) registers two functions on globalThis:
 *
 *   - mintcoreInitMint(getEnvCallback, fetchCallback, pemCallback): string
 *   - mintcoreHandleFetch(method, url, headersJSON, body): Promise<{status, headers, body}>
 */
class GoWasm {
  private initPromise: Promise<void> | null = null;
  private _needsRecovery = false;
  private _consecutiveTimeouts = 0;

  /**
   * Maximum number of consecutive timeout recoveries before the
   * instance reverts to permanent 503. Each recovery leaks one Go
   * WASM runtime (blocked goroutine + memory); capping the count
   * bounds leaked runtimes per isolate lifetime.
   */
  static readonly MAX_CONSECUTIVE_RECOVERIES = 3;

  /**
   * Whether this instance needs recovery after a timeout. Unlike
   * the previous permanent-poison approach, the next request will
   * re-initialize the Go WASM runtime automatically — up to
   * MAX_CONSECUTIVE_RECOVERIES times.
   */
  get needsRecovery(): boolean {
    return this._needsRecovery;
  }

  /**
   * Whether consecutive timeout recoveries have been exhausted.
   * Once exhausted, the instance refuses all requests with 503
   * until the Workers runtime recycles the isolate.
   */
  get exhausted(): boolean {
    return (
      this._consecutiveTimeouts >= GoWasm.MAX_CONSECUTIVE_RECOVERIES
    );
  }

  /**
   * Reset the consecutive timeout counter. Called after a request
   * completes successfully, proving the current Go runtime is
   * healthy.
   */
  resetTimeoutCounter(): void {
    this._consecutiveTimeouts = 0;
  }

  /**
   * Mark this instance as needing recovery. Called when handleFetch
   * times out so that the next request re-initializes the Go WASM
   * runtime instead of permanently refusing all traffic.
   *
   * Increments the consecutive timeout counter. After
   * MAX_CONSECUTIVE_RECOVERIES consecutive timeouts, the instance
   * is considered exhausted and reverts to permanent 503.
   *
   * Clears the cached initPromise so that init() re-runs doInit on
   * the next call, booting a fresh Go runtime.
   */
  markTimedOut(): void {
    this._needsRecovery = true;
    this._consecutiveTimeouts++;
    this.initPromise = null;
  }

  /**
   * Initialize the Go WASM runtime with the given module and env.
   * Idempotent and concurrency-safe — concurrent callers share the
   * same initialization Promise.
   *
   * If the instance was previously marked as needing recovery (after
   * a timeout), init() re-runs doInit to boot a fresh Go runtime.
   * The old runtime leaks but cannot interfere with the new one.
   *
   * Config errors (missing required env) are deterministic: the env
   * won't change between requests, so the rejection is cached to
   * prevent re-running expensive WASM instantiation on every request.
   *
   * Transient errors (WASM load failures, runtime panics) clear the
   * cached promise so a subsequent request can retry.
   */
  async init(wasmModule: WebAssembly.Module, env: Env): Promise<void> {
    if (!this.initPromise) {
      // Clear recovery flag — we are about to boot a fresh runtime.
      this._needsRecovery = false;
      this.initPromise = this.doInit(wasmModule, env).catch((err) => {
        // Only allow retry for non-config errors. Config errors are
        // deterministic — retrying won't help until the env changes.
        if (!(err instanceof ConfigError)) {
          this.initPromise = null;
        }
        throw err;
      });
    }
    return this.initPromise;
  }

  /**
   * Internal init implementation. Called exactly once via the
   * Promise guard in init().
   */
  private async doInit(
    wasmModule: WebAssembly.Module,
    env: Env,
  ): Promise<void> {
    // Quick-check: ROLE_APP_IDS must be present before WASM init.
    // This is an early-exit optimization — mintcore validates all
    // required fields itself, but catching a missing ROLE_APP_IDS
    // here avoids wasting cycles on WebAssembly.instantiate + go.run
    // for the most common misconfiguration.
    if (typeof env.ROLE_APP_IDS !== "string" || env.ROLE_APP_IDS === "") {
      throw new ConfigError("missing required Worker env: ROLE_APP_IDS");
    }

    // Validate ROLE_APP_IDS is a non-null JSON object before passing
    // to Go. Detect role names that would collide after
    // hyphen→underscore normalization (e.g. "my-role" and "my_role"
    // → same CF secret). Must happen before WASM init so operators
    // get a clear error.
    try {
      const parsed: unknown = JSON.parse(env.ROLE_APP_IDS);
      // JSON.parse("null") returns null, JSON.parse("[]") returns
      // an array — both pass typeof === "object" but are not valid
      // role-app-ID maps. Reject anything that isn't a plain object.
      if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
        throw new ConfigError(
          `ROLE_APP_IDS must be a JSON object (got ${parsed === null ? "null" : Array.isArray(parsed) ? "array" : typeof parsed})`,
        );
      }
      const roleAppIDs = parsed as Record<string, string>;
      detectRoleSecretCollisions(roleAppIDs);
    } catch (err) {
      if (err instanceof ConfigError) {
        throw err;
      }
      // JSON parse failure will be caught by Go's mintcoreInitMint;
      // don't mask it with a less-specific error here.
    }

    // The Go class from wasm_exec.js bootstraps the Go runtime and
    // provides the import object required by the WASM binary.
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const go = new (globalThis as any).Go();

    // Instantiate with the Go-provided import object so that host
    // imports (gojs.*, syscall/js bridges) are satisfied.
    const instance = await WebAssembly.instantiate(
      wasmModule,
      go.importObject,
    );

    // Run the Go main function. The Go WASM bridge registers its
    // handler functions on globalThis and blocks on a channel, keeping
    // the instance alive. We do not await go.run() — it resolves only
    // when the Go program exits (which it shouldn't for a server).
    // Attach a .catch() so that Go runtime panics surface as logged
    // errors instead of silent unhandled promise rejections.
    go.run(instance).catch((err: unknown) => {
      const msg = err instanceof Error ? err.message : String(err);
      console.error("Go WASM runtime error:", msg);
    });

    // Initialize the mint handler with getEnv and I/O callbacks.
    // Signature: mintcoreInitMint(getEnvCallback, fetchCallback, pemCallback)
    const getEnvCallback = createGetEnvCallback(env);
    const fetchCallback = createFetchCallback();
    const pemCallback = createPemCallback(env);

    const mintcoreInitMint = (globalThis as Record<string, unknown>)[
      "mintcoreInitMint"
    ] as
      | ((
          getEnv: (key: string) => string,
          fetch: unknown,
          pem: unknown,
        ) => string)
      | undefined;

    if (typeof mintcoreInitMint !== "function") {
      throw new Error(
        "mintcoreInitMint not registered — WASM bridge may not be loaded",
      );
    }

    const initErr = mintcoreInitMint(getEnvCallback, fetchCallback, pemCallback);
    if (initErr) {
      // Go-returned init errors are deterministic for the same env
      // (bad config, invalid role-app-ID JSON, etc.). Classify as
      // ConfigError so GoWasm.init() caches the rejection and does
      // not re-run WebAssembly.instantiate + go.run on every request,
      // which would leak a blocked Go main goroutine each time.
      throw new ConfigError(`mintcore init failed: ${initErr}`);
    }
  }

  /**
   * Forward a Fetch request to the WASM mint handler via
   * mintcoreHandleFetch(method, url, headersJSON, body).
   *
   * Go's ServeHTTP handles all path routing, authentication, and
   * response generation. The JS side only maps between Fetch
   * Request/Response and the four HandleFetch arguments.
   *
   * The call is wrapped in a timeout (HANDLE_FETCH_TIMEOUT_MS) so
   * that a stalled Go cooperative scheduler surfaces as a clean
   * error instead of hanging the request indefinitely.
   *
   * Returns {status: number, headers: string (JSON), body: string}.
   */
  async handleFetch(
    method: string,
    url: string,
    headersJSON: string,
    body: string,
  ): Promise<{ status: number; headers: string; body: string }> {
    const mintcoreHandleFetch = (globalThis as Record<string, unknown>)[
      "mintcoreHandleFetch"
    ] as
      | ((
          method: string,
          url: string,
          headersJSON: string,
          body: string,
        ) => Promise<{ status: number; headers: string; body: string }>)
      | undefined;

    if (typeof mintcoreHandleFetch !== "function") {
      throw new Error(
        "mintcoreHandleFetch not registered — WASM bridge may not be loaded",
      );
    }

    const result = mintcoreHandleFetch(method, url, headersJSON, body);
    // clearTimeout on settle prevents a leaked timer from firing a
    // spurious unhandled rejection after the request completes.
    let timeoutId!: ReturnType<typeof setTimeout>;
    const timeout = new Promise<never>((_resolve, reject) => {
      timeoutId = setTimeout(() => {
        reject(
          new Error(
            `mintcoreHandleFetch timed out after ${HANDLE_FETCH_TIMEOUT_MS}ms`,
          ),
        );
      }, HANDLE_FETCH_TIMEOUT_MS);
    });
    return Promise.race([result, timeout]).finally(() =>
      clearTimeout(timeoutId),
    );
  }
}

// Module-scoped singleton: one Go WASM instance per warm Worker isolate.
// See GoWasm class comment for the architectural rationale.
// Declared `const`: the object reference is never reassigned, but the
// instance internally boots a fresh Go WASM runtime after timeout
// recovery — see markTimedOut() and the recovery strategy comment on
// the GoWasm class.
const goWasm = new GoWasm();

/**
 * Return a JSON error response.
 */
function errorResponse(status: number, message: string): Response {
  return new Response(JSON.stringify({ error: message }), {
    status,
    headers: {
      "content-type": "application/json",
      "cache-control": "no-store",
    },
  });
}

/**
 * Extract the client IP from a Cloudflare Worker request.
 * CF-Connecting-IP is set by the Cloudflare edge for all requests
 * routed through Cloudflare's network.
 */
function clientIp(request: Request): string {
  return request.headers.get("CF-Connecting-IP") ?? "unknown";
}

export default {
  async fetch(
    request: Request,
    env: Env,
    _ctx: ExecutionContext,
  ): Promise<Response> {
    // After MAX_CONSECUTIVE_RECOVERIES consecutive timeouts, stop
    // attempting recovery to bound leaked Go WASM runtimes. The
    // Workers runtime must recycle the isolate to recover.
    if (goWasm.exhausted) {
      return errorResponse(
        503,
        "mint instance exhausted after repeated timeouts — " +
          "awaiting isolate recycle",
      );
    }

    // After a prior timeout, log a recovery notice. The next init()
    // call will boot a fresh Go WASM runtime automatically — unlike
    // the old permanent-poison approach, the mint recovers without
    // waiting for isolate recycle.
    if (goWasm.needsRecovery) {
      console.warn(
        "GoWasm instance recovering after timeout — " +
          "re-initializing Go WASM runtime",
      );
    }

    // Rate-limit POST /v1/token before WASM init to avoid wasting
    // CPU on abusive traffic. The key includes the hostname so that
    // preview aliases (which produce distinct hostnames like
    // <alias>-<worker>.<subdomain>.workers.dev) get isolated
    // counters — concurrent BT preview instances do not affect each
    // other. Durable deploys use a stable hostname.
    const url = new URL(request.url);
    if (request.method === "POST" && url.pathname === "/v1/token") {
      const rl = env.MINT_TOKEN_RATE_LIMITER;
      if (rl) {
        const key = `${url.hostname}:/v1/token:${clientIp(request)}`;
        const { success } = await rl.limit({ key });
        if (!success) {
          return errorResponse(429, "rate_limited");
        }
      } else {
        console.warn(
          "MINT_TOKEN_RATE_LIMITER binding missing — rate limiting disabled",
        );
      }
    }

    try {
      await goWasm.init(mintcoreWasm, env);
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      console.error("WASM init failed:", msg);
      return errorResponse(500, "mint initialization failed");
    }

    // Extract Fetch Request into the four mintcoreHandleFetch arguments.
    // Go's ServeHTTP handles all path routing — the JS side passes
    // every request through, including unknown paths (Go returns 404).
    const headersObj: Record<string, string> = {};
    request.headers.forEach((value, key) => {
      headersObj[key] = value;
    });
    const headersJSON = JSON.stringify(headersObj);

    let body = "";
    if (request.method !== "GET" && request.method !== "HEAD") {
      body = await request.text();
    }

    try {
      const result = await goWasm.handleFetch(
        request.method,
        request.url,
        headersJSON,
        body,
      );

      // Parse response headers from JSON string.
      const respHeaders = new Headers();
      if (result.headers && result.headers !== "{}") {
        try {
          const parsed: Record<string, string> = JSON.parse(result.headers);
          for (const [key, value] of Object.entries(parsed)) {
            respHeaders.set(key, value);
          }
        } catch (err) {
          // Log and continue without response headers.
          const msg = err instanceof Error ? err.message : String(err);
          console.error("failed to parse response headers JSON:", msg);
        }
      }

      // Request completed without timeout — the current Go runtime
      // is healthy. Reset the consecutive timeout counter so that a
      // future transient timeout gets the full recovery budget.
      goWasm.resetTimeoutCounter();

      return new Response(result.body, {
        status: result.status,
        headers: respHeaders,
      });
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      console.error("Request handling failed:", msg);

      // If the handler timed out, the Go WASM runtime may be wedged
      // (stalled scheduler, hung I/O). Mark the singleton for recovery
      // so that the next request boots a fresh Go runtime instead of
      // permanently refusing traffic. The old runtime leaks (blocked
      // goroutine + memory) but cannot interfere: its globalThis
      // exports are overwritten by the new runtime, and any late
      // Promise resolution from the timed-out goroutine is silently
      // ignored (the raced Promise already settled).
      if (msg.includes("timed out")) {
        console.error(
          "Marking GoWasm instance for recovery after timeout — " +
            "next request will re-initialize",
        );
        goWasm.markTimedOut();
      }

      return errorResponse(500, "internal error");
    }
  },
};
