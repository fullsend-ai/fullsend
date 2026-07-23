import { defineWorkersConfig } from "@cloudflare/vitest-pool-workers/config";

export default defineWorkersConfig({
  test: {
    poolOptions: {
      workers: {
        wrangler: { configPath: "./wrangler.toml" },
        miniflare: {
          // Minimal env bindings for smoke testing. These satisfy
          // validateEnv() and mintcoreInitMint() so the WASM bridge
          // can boot. "coder" is a canonical mintcore role — using a
          // non-canonical name (e.g. "test") causes mintcoreInitMint
          // to fail because HasRole() rejects unknown roles, and the
          // ConfigError is cached permanently.
          // PEM secrets are not needed for the /health and routing tests.
          //
          // ALLOWED_WORKFLOW_FILES is set explicitly here (not via a
          // production default). Production code defaults to "" (fail-
          // closed) when the env var is absent — matching cmd/mint.
          bindings: {
            ROLE_APP_IDS: '{"coder":"12345"}',
            ALLOWED_ORGS: "test-org",
            OIDC_AUDIENCE: "test-aud",
            ALLOWED_WORKFLOW_FILES: "*",
          },
        },
      },
    },
  },
});
