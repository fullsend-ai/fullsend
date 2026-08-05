import { defineConfig } from "@lando/vitepress-theme-default-plus/config";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const version = process.env.VPL_MVB_VERSION ?? "dev";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const docsDir = path.resolve(__dirname, "..");
const resolve = (pkg: string) => fileURLToPath(import.meta.resolve(pkg));

/** Non-content entry: template placeholder or repo-metadata (ALL-CAPS) name. */
function isNonContent(entry: string): boolean {
  if (/^0000-.*-template/.test(entry)) return true;
  const base = entry.replace(/\.md$/, "");
  return /^[A-Z][A-Z0-9_-]*$/.test(base);
}

function getMarkdownFiles(dir: string, base: string): { text: string; link: string }[] {
  const fullDir = path.resolve(docsDir, dir);
  if (!fs.existsSync(fullDir)) return [];
  const items: { text: string; link: string }[] = [];
  for (const entry of fs.readdirSync(fullDir).sort()) {
    const entryPath = path.resolve(fullDir, entry);
    if (entry.endsWith(".md") && entry !== "README.md" && !isNonContent(entry)) {
      const slug = entry.replace(/\.md$/, "");
      const content = fs.readFileSync(entryPath, "utf-8");
      const fmTitleMatch = content.match(/^title:\s*["']?(.+?)["']?\s*$/m);
      const titleMatch = content.match(/^#\s+(.+)$/m);
      items.push({ text: fmTitleMatch?.[1] || titleMatch?.[1] || slug, link: `/${base}/${slug}` });
    } else if (
      fs.statSync(entryPath).isDirectory() &&
      !entry.startsWith(".") &&
      !isNonContent(entry)
    ) {
      const readme = path.resolve(entryPath, "README.md");
      if (fs.existsSync(readme)) {
        const content = fs.readFileSync(readme, "utf-8");
        const fmTitleMatch = content.match(/^title:\s*["']?(.+?)["']?\s*$/m);
        const titleMatch = content.match(/^#\s+(.+)$/m);
        items.push({
          text: fmTitleMatch?.[1] || titleMatch?.[1] || entry,
          link: `/${base}/${entry}/`,
        });
      }
    }
  }
  return items;
}

export default defineConfig({
  title: "Fullsend",
  description: "Autonomous SDLC agents for your codebase",

  base: "/docs/",

  rewrites: {
    "README.md": "index.md",
    ":path(.*)/README.md": ":path/index.md",
  },

  head: [
    // Redirect legacy Svelte SPA hash routes (#/path) to VitePress paths (/docs/path)
    [
      "script",
      {},
      `(function(){var h=location.hash;if(h&&h.startsWith('#/')){var r=h.slice(2),s=r.indexOf('::'),p,a;if(s!==-1){p=r.slice(0,s);a=r.slice(s+2)}else{p=r;a=''}p=p.replace(/\\.{2,}/g,'').replace(/^\\/+/,'');if(!p)return;var u=new URL('/docs/'+p+(a?'#'+a:''),location.origin);if(u.origin===location.origin)location.replace(u.href)}})();`,
    ],
    ["link", { rel: "icon", href: "/docs/img/favicon.png" }],
    ["link", { rel: "preconnect", href: "https://fonts.googleapis.com" }],
    ["link", { rel: "preconnect", href: "https://fonts.gstatic.com", crossorigin: "" }],
    [
      "link",
      {
        rel: "stylesheet",
        href: "https://fonts.googleapis.com/css2?family=Plus+Jakarta+Sans:wght@400;500;600;700;800&family=JetBrains+Mono:wght@400;500;700&display=swap",
      },
    ],
  ],

  srcExclude: ["**/agents/icons/**", "**/testing/**"],
  ignoreDeadLinks: true,

  themeConfig: {
    logo: "/img/logo.png",
    logoLink: { link: "https://fullsend.sh", target: "_self" },
    siteTitle: "Fullsend",

    multiVersionBuild: {
      satisfies: ">=0.36.0",
      build: "dev",
    },

    nav: [
      { text: "Docs", link: "/guides/getting-started/", activeMatch: "^/(?!cli/)" },
      { text: "CLI Reference", link: "/cli/", activeMatch: "/cli/" },
    ],

    sidebar: {
      "/cli/": [
        {
          text: "CLI Reference",
          items: [
            { text: "Overview", link: "/cli/" },
            { text: "fullsend agent", link: "/cli/agent" },
            { text: "fullsend github", link: "/cli/github" },
            { text: "fullsend inference", link: "/cli/inference" },
            { text: "fullsend mint", link: "/cli/mint" },
            { text: "fullsend repos", link: "/cli/repos" },
          ],
        },
      ],
      "/": [
        {
          text: "Getting Started",
          collapsed: true,
          link: "/guides/getting-started/",
          items: [
            { text: "Getting Inference", link: "/guides/getting-started/getting-inference" },
            { text: "Configuring GitHub", link: "/guides/getting-started/configuring-github" },
            { text: "Per-Org Mode", link: "/guides/getting-started/org-mode" },
            { text: "Repo Management", link: "/guides/getting-started/repo-management" },
            { text: "Operations", link: "/guides/getting-started/operations" },
          ],
        },
        {
          text: "Agents",
          collapsed: true,
          link: "/agents/",
          items: [
            { text: "Triage", link: "/agents/triage" },
            { text: "Code", link: "/agents/code" },
            { text: "Review", link: "/agents/review" },
            { text: "Fix", link: "/agents/fix" },
            { text: "Retro", link: "/agents/retro" },
            { text: "Prioritize", link: "/agents/prioritize" },
            { text: "Default vs. Custom", link: "/agents/topics/default-vs-custom" },
            { text: "Escalation Ladder", link: "/agents/topics/escalation-ladder" },
          ],
        },
        {
          text: "User Guides",
          collapsed: true,
          link: "/guides/",
          items: [
            { text: "Bring Your Own Agent", link: "/guides/user/bring-your-own-agent" },
            { text: "CEL Triggers Reference", link: "/guides/user/cel-triggers-reference" },
            { text: "Bugfix Workflow", link: "/guides/user/bugfix-workflow" },
            { text: "Configuring Agent Behavior", link: "/guides/user/customizing-agents" },
            { text: "Configuring with AGENTS.md", link: "/guides/user/customizing-with-agents-md" },
            { text: "Configuring with Skills", link: "/guides/user/customizing-with-skills" },
            {
              text: "Building custom agents from scratch (deprecated)",
              link: "/guides/user/building-custom-agents",
            },
            { text: "Running Agents Locally", link: "/guides/user/running-agents-locally" },
            { text: "Issue Commands", link: "/guides/user/issues-commands" },
            { text: "Jira Integration", link: "/guides/user/jira-integration" },
            { text: "How To Emit Traces", link: "/guides/user/how-to-emit-traces" },
            { text: "Tracing with MLflow", link: "/guides/user/tracing-with-mlflow" },
          ],
        },
        {
          text: "Concepts",
          collapsed: true,
          items: [
            { text: "Vision", link: "/vision" },
            { text: "Architecture", link: "/architecture" },
            { text: "Runtimes", link: "/runtimes" },
            { text: "Glossary", link: "/glossary" },
          ],
        },
        {
          text: "Infrastructure",
          collapsed: true,
          items: [
            {
              text: "Infrastructure Reference",
              link: "/guides/infrastructure/infrastructure-reference",
            },
            { text: "Mint Administration", link: "/guides/infrastructure/mint-administration" },
            { text: "Standalone Mint", link: "/guides/infrastructure/standalone-mint" },
            { text: "Private Repositories", link: "/guides/infrastructure/private-repositories" },
            { text: "Tracing Reference", link: "/guides/infrastructure/distributed-tracing" },
            { text: "Eval Measurements", link: "/guides/infrastructure/eval-measurements" },
            { text: "Advanced Setup", link: "/guides/infrastructure/advanced-setup" },
            {
              text: "Layered Config Reference",
              link: "/guides/infrastructure/layered-config-reference",
            },
          ],
        },
        {
          text: "Contributing",
          collapsed: true,
          items: [
            {
              text: "Development",
              collapsed: true,
              items: [
                { text: "Behaviour Drivers", link: "/guides/dev/behaviour-drivers" },
                { text: "Behaviour Testing", link: "/guides/dev/behaviour-testing" },
                { text: "CLI Internals", link: "/guides/dev/cli-internals" },
                { text: "E2E Testing", link: "/guides/dev/e2e-testing" },
                { text: "Testing Workflows", link: "/guides/dev/testing-workflows" },
                { text: "Tracing Internals", link: "/guides/dev/tracing" },
              ],
            },
            {
              text: "Contributor Guidelines",
              collapsed: true,
              items: getMarkdownFiles("contributing", "contributing"),
            },
            { text: "Roadmap", link: "/roadmap" },
            { text: "Archived roadmaps", link: "/archived-roadmap" },
            { text: "Landscape", link: "/landscape" },
            {
              text: "Architecture Decisions",
              collapsed: true,
              items: getMarkdownFiles("ADRs", "ADRs"),
            },
            {
              text: "Design Documents",
              collapsed: true,
              items: getMarkdownFiles("problems", "problems"),
            },
            {
              text: "Spikes",
              collapsed: true,
              items: getMarkdownFiles("spikes", "spikes"),
            },
            {
              text: "Experiments (Exploratory)",
              collapsed: true,
              items: getMarkdownFiles("experiments", "experiments"),
            },
            { text: "Doc Site", link: "/doc-site" },
            { text: "Site Deployment", link: "/site-deployment" },
          ],
        },
      ],
    },

    sidebarEnder: {
      text: version,
      collapsed: true,
      items: [
        {
          text: "Other Doc Versions",
          items: [
            { rel: "mvb", text: "stable", target: "_blank", link: "/stable/" },
            { rel: "mvb", text: "edge", target: "_blank", link: "/edge/" },
            { rel: "mvb", text: "dev", target: "_blank", link: "/dev/" },
            { text: "<strong>see all versions</strong>", link: "/v/" },
          ],
        },
        {
          text: "Other Releases",
          link: "https://github.com/fullsend-ai/fullsend/releases",
        },
      ],
    },

    socialLinks: [{ icon: "github", link: "https://github.com/fullsend-ai/fullsend" }],

    editLink: {
      pattern: "https://github.com/fullsend-ai/fullsend/edit/main/docs/:path",
      text: "Edit this page on GitHub",
    },

    search: {
      provider: "local",
      options: {
        scopes: [
          { label: "Guides", prefixes: ["/docs/guides/", "/docs/agents/", "/docs/cli/"] },
          {
            label: "Design Docs",
            prefixes: ["/docs/problems/", "/docs/ADRs/", "/docs/normative/", "/docs/spikes/"],
          },
          { label: "Experiments", prefixes: ["/docs/experiments/"] },
          { label: "Contributing", prefixes: ["/docs/contributing/"] },
          { label: "Others", prefixes: [], others: true },
        ],
      },
    },
  },

  vite: {
    resolve: {
      alias: [
        {
          find: /^.*\/VPLocalSearchBox\.vue$/,
          replacement: fileURLToPath(
            new URL("./theme/components/VPLocalSearchBox.vue", import.meta.url),
          ),
        },
        { find: "vue/server-renderer", replacement: resolve("vue/server-renderer") },
        { find: "vue", replacement: resolve("vue") },
        {
          find: "mermaid",
          replacement: path.join(path.dirname(resolve("mermaid")), "mermaid.esm.mjs"),
        },
      ],
      // Prevent VitePress SSR from resolving CJS packages in the
      // repo-root node_modules (which causes ESM default-import
      // failures on Node 22 for packages like entities, estree-walker).
      preserveSymlinks: true,
    },
    ssr: {
      noExternal: [/./],
    },
  },

  markdown: {
    shikiSetup: async (shiki) => {
      await shiki.loadLanguage("toml");
    },

    config: (md) => {
      const defaultCodeInline = md.renderer.rules.code_inline!;
      md.renderer.rules.code_inline = (tokens, idx, options, env, self) => {
        tokens[idx].attrSet("v-pre", "");
        return defaultCodeInline(tokens, idx, options, env, self);
      };

      // Rewrite relative links that escape the docs/ directory to GitHub
      // source URLs, and rewrite README.md links to directory index paths
      // (only for links that stay within docs/).
      md.core.ruler.push("rewrite-links", (state) => {
        for (const token of state.tokens) {
          if (!token.children) continue;
          for (const child of token.children) {
            if (child.type !== "link_open") continue;
            const href = child.attrGet("href");
            if (
              !href ||
              href.startsWith("http") ||
              href.startsWith("#") ||
              href.startsWith("mailto:")
            )
              continue;

            // Check if the link escapes docs/ (more ../  than directory depth)
            const docPath = state.env?.relativePath || "";
            const docDir = docPath.split("/").slice(0, -1);
            const parts = href.split("#");
            const linkPath = parts[0];
            const anchor = parts[1] ? "#" + parts[1] : "";
            const segments = linkPath.split("/");
            let depth = 0;
            for (const s of segments) {
              if (s === "..") depth++;
              else break;
            }
            if (depth > docDir.length) {
              const remainder = segments.slice(depth).join("/");
              const prefix =
                /\.[a-zA-Z0-9]+$/.test(remainder) && !remainder.endsWith("/") ? "blob" : "tree";
              child.attrSet(
                "href",
                `https://github.com/fullsend-ai/fullsend/${prefix}/main/${remainder}${anchor}`,
              );
              continue;
            }

            // For links staying within docs/, rewrite README.md to directory index
            if (/README\.md(#.*)?$/.test(href)) {
              child.attrSet(
                "href",
                href.replace(/README\.md(#.*)?$/, (_: string, a: string) => a || "./"),
              );
            }
          }
        }
      });

      const defaultFence = md.renderer.rules.fence!.bind(md.renderer.rules);
      md.renderer.rules.fence = (tokens, idx, options, env, self) => {
        if (tokens[idx].info.trim() === "mermaid") {
          const encoded = encodeURIComponent(tokens[idx].content);
          return `<Mermaid id="mermaid-${idx}" graph="${encoded}" />`;
        }
        return defaultFence(tokens, idx, options, env, self);
      };
    },
  },
});
