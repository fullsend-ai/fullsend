// Thin Cloudflare Worker adapter for the fullsend token mint.
//
// All mint logic (OIDC verification, claims validation, JWT signing,
// token minting, path routing) stays in Go via the mintcore WASM module.
// This adapter handles I/O only:
//   - Worker secrets -> PEM key access (via pemCallback)
//   - Worker env vars -> mint configuration (via configJSON)
//   - Host fetch -> outbound HTTP (via fetchCallback)
//   - Fetch Request/Response mapping for mintcoreHandleFetch
//
// The WASM module is compiled from cmd/mint-wasm with
// GOOS=js GOARCH=wasm and registers two global functions via syscall/js:
//
//   - mintcoreInitMint(configJSON, fetchCallback, pemCallback): string
//     Initializes the mint handler with explicit config and host callbacks.
//     Returns "" on success or an error message on failure.
//
//   - mintcoreHandleFetch(method, url, headersJSON, body): Promise<{status, headers, body}>
//     Routes a Fetch request through Go's http.Handler (ServeHTTP).
//     Authorization is passed inside headersJSON, not as a separate argument.
//     Returns a Promise resolving to {status: number, headers: string, body: string}
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
  /** Comma-separated list of allowed GitHub orgs. */
  ALLOWED_ORGS: string;
  /** Expected OIDC audience claim value. */
  OIDC_AUDIENCE: string;
  /** Comma-separated list of allowed roles (derived from ROLE_APP_IDS if unset). */
  ALLOWED_ROLES?: string;
  /** Comma-separated workflow file patterns ("*" = any). */
  ALLOWED_WORKFLOW_FILES?: string;
  /** Comma-separated repos using per-repo WIF providers. */
  PER_REPO_WIF_REPOS?: string;
  /** JSON-encoded map of custom role permissions. */
  CUSTOM_ROLE_PERMISSIONS?: string;

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
 * Validate that all required Worker environment bindings are present
 * and non-empty. Call this before any WASM work so that persistent
 * misconfig fails fast with a clear message instead of burning
 * cycles on WebAssembly.instantiate + go.run.
 *
 * Throws ConfigError listing every missing field (not just the first).
 */
function validateEnv(env: Env): void {
  const required: Array<{ key: keyof Env; label: string }> = [
    { key: "ROLE_APP_IDS", label: "ROLE_APP_IDS" },
    { key: "ALLOWED_ORGS", label: "ALLOWED_ORGS" },
    { key: "OIDC_AUDIENCE", label: "OIDC_AUDIENCE" },
  ];
  const missing = required.filter((f) => {
    const v = env[f.key];
    return typeof v !== "string" || v === "";
  });
  if (missing.length > 0) {
    const names = missing.map((f) => f.label).join(", ");
    throw new ConfigError(
      `missing required Worker env: ${names}`,
    );
  }
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
 * Build the WASM configuration from Worker environment bindings.
 * Returns a JSON string matching the mintcore.WorkerConfig struct.
 * Field names use PascalCase to match Go's default JSON encoding
 * (the struct has no json tags).
 */
function buildWasmConfig(env: Env): string {
  return JSON.stringify({
    RoleAppIDs: env.ROLE_APP_IDS,
    AllowedOrgs: env.ALLOWED_ORGS,
    OIDCAudience: env.OIDC_AUDIENCE,
    AllowedRoles: env.ALLOWED_ROLES ?? "",
    AllowedWorkflowFiles: env.ALLOWED_WORKFLOW_FILES ?? "*",
    PerRepoWIFRepos: env.PER_REPO_WIF_REPOS ?? "",
    CustomRolePermissions: env.CUSTOM_ROLE_PERMISSIONS ?? "",
  });
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
 * On timeout the module-scope GoWasm singleton is discarded and
 * recreated (see fetch handler) so the next request gets a fresh
 * Go runtime instead of reusing a potentially corrupted instance.
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
 * the Workers runtime. If the Go scheduler stalls or a request
 * times out (HANDLE_FETCH_TIMEOUT_MS), the module-scope GoWasm
 * singleton is discarded and recreated so the next request boots
 * a fresh Go runtime. The timeout alone does not heal isolate
 * state — the singleton reset is required to avoid reusing a
 * potentially corrupted WASM instance.
 *
 * The standard Go WASM target (GOOS=js GOARCH=wasm) requires the
 * wasm_exec.js support code to bootstrap the Go runtime. The Go class
 * from wasm_exec.js provides the import object that satisfies the
 * WASM binary's host imports (gojs.*, syscall/js bridges).
 *
 * The WASM bridge (cmd/mint-wasm) registers two functions on globalThis:
 *
 *   - mintcoreInitMint(configJSON, fetchCallback, pemCallback): string
 *   - mintcoreHandleFetch(method, url, headersJSON, body): Promise<{status, headers, body}>
 */
class GoWasm {
  private initPromise: Promise<void> | null = null;

  /**
   * Initialize the Go WASM runtime with the given module and env.
   * Idempotent and concurrency-safe — concurrent callers share the
   * same initialization Promise.
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
    // Validate required env fields before any WASM work. Missing
    // required config is deterministic — fail fast with a clear
    // message instead of burning cycles on instantiate + go.run.
    validateEnv(env);

    // Detect role names that would collide after hyphen→underscore
    // normalization (e.g. "my-role" and "my_role" → same CF secret).
    // Must happen before WASM init so operators get a clear error.
    try {
      const roleAppIDs: Record<string, string> = JSON.parse(env.ROLE_APP_IDS);
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

    // Initialize the mint handler with config and I/O callbacks.
    // Signature: mintcoreInitMint(configJSON, fetchCallback, pemCallback)
    const configJSON = buildWasmConfig(env);
    const fetchCallback = createFetchCallback();
    const pemCallback = createPemCallback(env);

    const mintcoreInitMint = (globalThis as Record<string, unknown>)[
      "mintcoreInitMint"
    ] as
      | ((
          config: string,
          fetch: unknown,
          pem: unknown,
        ) => string)
      | undefined;

    if (typeof mintcoreInitMint !== "function") {
      throw new Error(
        "mintcoreInitMint not registered — WASM bridge may not be loaded",
      );
    }

    const initErr = mintcoreInitMint(configJSON, fetchCallback, pemCallback);
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
    const timeout = new Promise<never>((_resolve, reject) => {
      setTimeout(() => {
        reject(
          new Error(
            `mintcoreHandleFetch timed out after ${HANDLE_FETCH_TIMEOUT_MS}ms`,
          ),
        );
      }, HANDLE_FETCH_TIMEOUT_MS);
    });
    return Promise.race([result, timeout]);
  }
}

// Module-scoped singleton: one Go WASM instance per warm Worker isolate.
// See GoWasm class comment for the architectural rationale.
// Uses `let` so the fetch handler can discard and recreate the instance
// after a timeout (a timed-out Go runtime may be wedged).
let goWasm = new GoWasm();

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

export default {
  async fetch(
    request: Request,
    env: Env,
    _ctx: ExecutionContext,
  ): Promise<Response> {
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

      return new Response(result.body, {
        status: result.status,
        headers: respHeaders,
      });
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      console.error("Request handling failed:", msg);

      // If the handler timed out, the Go WASM runtime may be wedged
      // (stalled scheduler, hung I/O). Discard the singleton so the
      // next request boots a fresh Go runtime instead of reusing the
      // potentially corrupted instance.
      if (msg.includes("timed out")) {
        console.error("Discarding wedged GoWasm instance after timeout");
        goWasm = new GoWasm();
      }

      return errorResponse(500, "internal error");
    }
  },
};
