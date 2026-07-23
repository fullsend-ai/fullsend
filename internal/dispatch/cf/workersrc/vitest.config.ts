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
          bindings: {
            ROLE_APP_IDS: '{"coder":"12345"}',
            ALLOWED_ORGS: "test-org",
            OIDC_AUDIENCE: "test-aud",
          },
        },
      },
    },
  },
});
