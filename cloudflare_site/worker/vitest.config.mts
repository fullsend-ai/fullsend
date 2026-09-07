import path from "node:path";
import { fileURLToPath } from "node:url";
import { cloudflareTest } from "@cloudflare/vitest-pool-workers";
import { defineConfig } from "vitest/config";

const workerRoot = path.dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  root: workerRoot,
  plugins: [
    cloudflareTest({
      // The Worker is a static-asset passthrough: the only binding it needs is
      // ASSETS, which comes from wrangler.toml. Tests override the assets
      // directory so unmatched-path routing can see a 404.html (the deploy
      // public/ tree only has robots.txt / llms.txt at test time).
      wrangler: {
        configPath: path.join(workerRoot, "..", "wrangler.toml"),
      },
      miniflare: {
        assets: {
          directory: path.join(workerRoot, "test-assets"),
          binding: "ASSETS",
        },
      },
    }),
  ],
  test: {
    include: ["src/**/*.test.ts"],
  },
});
