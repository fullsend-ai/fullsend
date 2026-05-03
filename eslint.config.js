import { defineConfig } from "eslint/config";
import js from "@eslint/js";
import ts from "typescript-eslint";
import svelte from "eslint-plugin-svelte";
import globals from "globals";
import adminSvelteConfig from "./web/admin/svelte.config.js";
import docsSvelteConfig from "./web/docs/svelte.config.js";

export default defineConfig([
  js.configs.recommended,
  ts.configs.recommended,
  svelte.configs.recommended,
  svelte.configs.prettier,

  {
    languageOptions: {
      globals: {
        ...globals.browser,
      },
    },
  },

  // Svelte file overrides: TypeScript parser with per-app svelte config
  {
    files: ["web/admin/**/*.svelte", "web/admin/**/*.svelte.ts", "web/admin/**/*.svelte.js"],
    languageOptions: {
      parserOptions: {
        parser: ts.parser,
        svelteConfig: adminSvelteConfig,
      },
    },
  },
  {
    files: ["web/docs/**/*.svelte", "web/docs/**/*.svelte.ts", "web/docs/**/*.svelte.js"],
    languageOptions: {
      parserOptions: {
        parser: ts.parser,
        svelteConfig: docsSvelteConfig,
      },
    },
  },

  // Custom rules for all linted files
  {
    rules: {
      "@typescript-eslint/no-unused-vars": [
        "error",
        {
          argsIgnorePattern: "^_",
          varsIgnorePattern: "^_",
          caughtErrorsIgnorePattern: "^_",
        },
      ],
      "no-console": ["warn", { allow: ["warn", "error", "info"] }],
    },
  },

  // Svelte-specific rules
  {
    files: ["**/*.svelte"],
    rules: {
      "svelte/no-at-html-tags": "error",
      "svelte/require-each-key": "error",
      "svelte/no-unused-class-name": "warn",
      "svelte/no-inline-styles": ["warn", { allowTransitions: true }],
      "svelte/block-lang": [
        "error",
        { script: ["ts"], style: ["css", null] },
      ],
      "svelte/max-lines-per-block": [
        "warn",
        {
          script: 100,
          template: 80,
          style: 120,
        },
      ],
    },
  },

  // Svelte component file-length limit
  {
    files: ["web/admin/src/**/*.svelte"],
    rules: {
      "max-lines": ["warn", { max: 150, skipBlankLines: true, skipComments: true }],
    },
  },

  // Ignore patterns
  {
    ignores: [
      "dist/",
      "node_modules/",
      "cloudflare_site/",
      "internal/",
      "hack/",
      "docs/",
      "web/public/",
    ],
  },
]);
