import { defineConfig } from "vitepress";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const docsDir = path.resolve(__dirname, "..", "..", "docs");

function getMarkdownFiles(dir: string, base: string): { text: string; link: string }[] {
  const fullDir = path.resolve(docsDir, dir);
  if (!fs.existsSync(fullDir)) return [];
  const items: { text: string; link: string }[] = [];
  for (const entry of fs.readdirSync(fullDir).sort()) {
    const entryPath = path.resolve(fullDir, entry);
    if (entry.endsWith(".md") && entry !== "README.md") {
      const slug = entry.replace(/\.md$/, "");
      const content = fs.readFileSync(entryPath, "utf-8");
      const fmTitleMatch = content.match(/^title:\s*["']?(.+?)["']?\s*$/m);
      const titleMatch = content.match(/^#\s+(.+)$/m);
      items.push({ text: fmTitleMatch?.[1] || titleMatch?.[1] || slug, link: `/${base}/${slug}` });
    } else if (fs.statSync(entryPath).isDirectory()) {
      const readme = path.resolve(entryPath, "README.md");
      if (fs.existsSync(readme)) {
        const content = fs.readFileSync(readme, "utf-8");
        const titleMatch = content.match(/^#\s+(.+)$/m);
        items.push({ text: titleMatch?.[1] || entry, link: `/${base}/${entry}/` });
      }
    }
  }
  return items;
}

// Escape Vue-incompatible syntax ({ }, {{ }}, <non-HTML-tags>) in markdown
// before markdown-it processes it. Code fence tracking uses backtick-count
// matching per CommonMark spec to correctly handle nested fences.
function escapeVueSyntax(src: string): string {
  const lines = src.split("\n");
  let fenceLen = 0;
  let fenceChar = "";
  return lines
    .map((line) => {
      const fenceMatch = line.match(/^ {0,3}(`{3,}|~{3,})/);
      if (fenceMatch) {
        const ch = fenceMatch[1][0];
        const len = fenceMatch[1].length;
        if (fenceLen === 0) {
          fenceLen = len;
          fenceChar = ch;
          return line;
        }
        if (ch === fenceChar && len >= fenceLen && line.trim() === ch.repeat(len)) {
          fenceLen = 0;
          fenceChar = "";
          return line;
        }
        return line;
      }
      if (fenceLen > 0) return line;
      return escapeLine(line);
    })
    .join("\n");
}

const KNOWN_TAGS =
  /^<\/?(?:a|abbr|address|area|article|aside|audio|b|base|bdi|bdo|blockquote|body|br|button|canvas|caption|cite|code|col|colgroup|data|datalist|dd|del|details|dfn|dialog|div|dl|dt|em|embed|fieldset|figcaption|figure|footer|form|h[1-6]|head|header|hgroup|hr|html|i|iframe|img|input|ins|kbd|label|legend|li|link|main|map|mark|menu|meta|meter|nav|noscript|object|ol|optgroup|option|output|p|param|picture|pre|progress|q|rp|rt|ruby|s|samp|script|search|section|select|slot|small|source|span|strong|style|sub|summary|sup|table|tbody|td|template|textarea|tfoot|th|thead|time|title|tr|track|u|ul|var|video|wbr|svg|path|g|circle|rect|line|polyline|polygon|text|defs|use|symbol)[\s>/!]/i;

function escapeLine(line: string): string {
  let result = "";
  let i = 0;
  while (i < line.length) {
    if (line[i] === "`") {
      let runLen = 0;
      while (i + runLen < line.length && line[i + runLen] === "`") runLen++;
      let found = -1;
      let j = i + runLen;
      while (j < line.length) {
        if (line[j] === "`") {
          let closeLen = 0;
          while (j + closeLen < line.length && line[j + closeLen] === "`") closeLen++;
          if (closeLen === runLen) {
            found = j;
            break;
          }
          j += closeLen;
        } else {
          j++;
        }
      }
      if (found !== -1) {
        result += line.slice(i, found + runLen);
        i = found + runLen;
      } else {
        result += "`".repeat(runLen);
        i += runLen;
      }
    } else if (line[i] === "{") {
      result += "&#123;";
      i++;
    } else if (line[i] === "}") {
      result += "&#125;";
      i++;
    } else if (line[i] === "<") {
      const rest = line.slice(i);
      if (KNOWN_TAGS.test(rest) || /^<!--/.test(rest)) {
        result += "<";
      } else {
        result += "&lt;";
      }
      i++;
    } else {
      result += line[i];
      i++;
    }
  }
  return result;
}

export default defineConfig({
  title: "Fullsend",
  description: "Autonomous SDLC agents for your codebase",

  srcDir: "../docs",
  outDir: "./dist",
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
          ],
        },
        {
          text: "User Guides",
          collapsed: true,
          link: "/guides/",
          items: [
            { text: "Bugfix Workflow", link: "/guides/user/bugfix-workflow" },
            { text: "Customizing Agents", link: "/guides/user/customizing-agents" },
            { text: "Customizing with AGENTS.md", link: "/guides/user/customizing-with-agents-md" },
            { text: "Customizing with Skills", link: "/guides/user/customizing-with-skills" },
            { text: "Building custom agents from scratch", link: "/guides/user/building-custom-agents" },
            { text: "Running Agents Locally", link: "/guides/user/running-agents-locally" },
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
            { text: "Distributed Tracing", link: "/guides/infrastructure/distributed-tracing" },
            { text: "Advanced Setup", link: "/guides/infrastructure/advanced-setup" },
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
              ],
            },
            { text: "Roadmap", link: "/roadmap" },
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
              text: "Experiments (Exploratory)",
              collapsed: true,
              items: getMarkdownFiles("experiments", "experiments"),
            },
            { text: "Doc Site", link: "/doc-site" },
            { text: "Web Admin (On Hold)", link: "/web-admin-deployment" },
          ],
        },
        {
          text: "Internals",
          collapsed: true,
          items: [
            { text: "Admin OAuth Worker", link: "/admin-oauth-worker" },
            {
              text: "Specifications",
              collapsed: true,
              items: getMarkdownFiles("superpowers/specs", "superpowers/specs"),
            },
            {
              text: "Implementation Plans",
              collapsed: true,
              items: [
                ...getMarkdownFiles("superpowers/plans", "superpowers/plans"),
                ...getMarkdownFiles("plans", "plans"),
              ],
            },
          ],
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
    },
  },

  vite: {
    resolve: {
      alias: {
        "vue/server-renderer": path.resolve(
          __dirname,
          "..",
          "node_modules",
          "vue",
          "server-renderer",
          "index.mjs",
        ),
        vue: path.resolve(__dirname, "..", "node_modules", "vue"),
        // Use mermaid's pre-bundled ESM build; the default entry (mermaid.core.mjs)
        // externalizes dayjs (CJS-only), which breaks under noExternal: [/./].
        mermaid: path.resolve(
          __dirname,
          "..",
          "node_modules",
          "mermaid",
          "dist",
          "mermaid.esm.mjs",
        ),
      },
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
    preConfig: (md) => {
      const defaultParse = md.parse.bind(md);
      md.parse = (src: string, env: Record<string, unknown>) => {
        return defaultParse(escapeVueSyntax(src), env);
      };
    },
    // Auto-add v-pre to inline code so `{{ }}` inside backticks is safe.
    // Recommended by VitePress maintainer brc-dd:
    // https://github.com/vuejs/vitepress/discussions/3724
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
