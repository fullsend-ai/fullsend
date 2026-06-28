import path from "node:path";
import { fileURLToPath } from "node:url";
import type { ProxyOptions } from "vite";
import { normalizePath } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { defineConfig } from "vitest/config";
import type { Plugin } from "vite";
import { fullsendDocsPlugin } from "./web/docs/build/vitePluginDocs";

const repoRoot = path.dirname(fileURLToPath(import.meta.url));
const webRoot = path.join(repoRoot, "web");

const debugProxy = process.env.ADMIN_DEBUG_PROXY === "1";

function spaFallbackPlugin(): Plugin {
  return {
    name: "fullsend-spa-fallback",
    configureServer(server) {
      server.middlewares.use((req, _res, next) => {
        const url = req.url?.split("?")[0] ?? "";
        if (url === "/") {
          req.url = "/index.html";
        } else if (url.startsWith("/admin/") && !path.extname(url)) {
          req.url = "/admin/index.html";
        } else if (url.startsWith("/docs/") && !path.extname(url)) {
          req.url = "/docs/index.html";
        }
        next();
      });
    },
  };
}

function adminDevEnvLogPlugin(): Plugin {
  return {
    name: "admin-dev-env-log",
    configResolved(config) {
      if (config.command !== "serve" || process.env.VITEST) return;
      if (debugProxy) {
        console.info(
          "\n[fullsend] ADMIN_DEBUG_PROXY=1 — logging Vite requests and /api → Worker proxy traffic.\n",
        );
      }
    },
  };
}

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
        console.info("[vite-proxy] ← Worker", proxyRes.statusCode, req.url);
      });
    },
  };
}

export default defineConfig(({ command }) => ({
  root: webRoot,
  base: "/",
  publicDir: command === "serve" ? path.join(webRoot, "public") : false,
  plugins: [
    svelte({
      configFile: path.join(webRoot, "admin/svelte.config.js"),
      include: [
        normalizePath(path.join(webRoot, "admin/**/*.svelte")),
        normalizePath(
          path.join(repoRoot, "node_modules/svelte-spa-router/**/*.svelte"),
        ),
      ],
    }),
    svelte({
      configFile: path.join(webRoot, "docs/svelte.config.js"),
      include: normalizePath(path.join(webRoot, "docs/**/*.svelte")),
    }),
    fullsendDocsPlugin(repoRoot),
    spaFallbackPlugin(),
    adminDevEnvLogPlugin(),
    adminRequestLogPlugin(),
  ],
  build: {
    rollupOptions: {
      input: {
        admin: path.join(webRoot, "admin/index.html"),
        docs: path.join(webRoot, "docs/index.html"),
      },
    },
  },
  server: {
    proxy: {
      "/api": apiProxy(),
    },
  },
  test: {
    environment: "jsdom",
    environmentMatchGlobs: [["docs/build/**/*.test.ts", "node"]],
    include: [
      "admin/src/**/*.test.ts",
      "docs/build/**/*.test.ts",
      "docs/src/**/*.test.ts",
    ],
    passWithNoTests: true,
  },
}));
