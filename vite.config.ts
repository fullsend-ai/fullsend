import path from "node:path";
import { fileURLToPath } from "node:url";
import type { ProxyOptions } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { defineConfig } from "vitest/config";
import type { Plugin } from "vite";

const repoRoot = path.dirname(fileURLToPath(import.meta.url));
const adminDir = path.join(repoRoot, "web", "admin");

const debugProxy = process.env.ADMIN_DEBUG_PROXY === "1";

function adminDevEnvLogPlugin(): Plugin {
  return {
    name: "admin-dev-env-log",
    configResolved(config) {
      if (config.command !== "serve" || process.env.VITEST) return;
      if (debugProxy) {
        console.info(
          "\n[fullsend-admin] ADMIN_DEBUG_PROXY=1 — logging Vite requests and /api → Worker proxy traffic.\n",
        );
      }
    },
  };
}

/** Log every HTTP request the Vite dev server receives (not Worker-native). */
function adminRequestLogPlugin(): Plugin {
  return {
    name: "admin-request-log",
    configureServer(server) {
      if (!debugProxy) return;
      server.middlewares.use((req, _res, next) => {
        console.info("[vite] request", req.method, req.url);
        next();
      });
    },
  };
}

function apiProxy(): ProxyOptions {
  const base: ProxyOptions = {
    target: "http://127.0.0.1:8787",
    changeOrigin: true,
  };

  if (!debugProxy) return base;

  return {
    ...base,
    configure(proxy) {
      proxy.on("error", (err, req) => {
        console.error("[vite-proxy] error", req?.url, err.message);
      });
      proxy.on("proxyReq", (_proxyReq, req) => {
        console.info("[vite-proxy] → Worker", req.method, req.url);
      });
      proxy.on("proxyRes", (proxyRes, req) => {
        console.info(
          "[vite-proxy] ← Worker",
          proxyRes.statusCode,
          req.url,
        );
      });
    },
  };
}

export default defineConfig(() => {
  return {
    root: adminDir,
    base: "/admin/",
    plugins: [
      svelte(),
      adminDevEnvLogPlugin(),
      adminRequestLogPlugin(),
    ],
    server: {
      proxy: {
        "/api": apiProxy(),
      },
    },
    test: {
      environment: "jsdom",
      include: ["src/**/*.test.ts"],
      passWithNoTests: true,
    },
  };
});
