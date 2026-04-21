import path from "node:path";
import { fileURLToPath } from "node:url";
import { cloudflareTest } from "@cloudflare/vitest-pool-workers";
import { defineConfig } from "vitest/config";

const workerRoot = path.dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  root: workerRoot,
  plugins: [
    cloudflareTest({
      wrangler: {
        configPath: path.join(workerRoot, "..", "wrangler.toml"),
      },
      miniflare: {
        bindings: {
          GITHUB_APP_CLIENT_ID: "test_github_client_id",
          GITHUB_APP_CLIENT_SECRET: "test_github_client_secret",
          TURNSTILE_SITE_KEY: "1x00000000000000000000AA",
          TURNSTILE_SECRET_KEY: "1x0000000000000000000000000000000AA",
        },
      },
    }),
  ],
  test: {
    include: ["src/**/*.test.ts"],
  },
});
