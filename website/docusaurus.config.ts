import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';
import {themes as prismThemes} from 'prism-react-renderer';

const config: Config = {
  title: 'Fullsend',
  tagline: 'Autonomous SDLC agents for your codebase',
  favicon: 'img/favicon.png',

  url: 'https://fullsend.sh',
  baseUrl: '/docs/',

  organizationName: 'fullsend-ai',
  projectName: 'fullsend',

  onBrokenLinks: 'warn',

  headTags: [
    {
      tagName: 'link',
      attributes: {rel: 'preconnect', href: 'https://fonts.googleapis.com'},
    },
    {
      tagName: 'link',
      attributes: {rel: 'preconnect', href: 'https://fonts.gstatic.com', crossorigin: 'anonymous'},
    },
    {
      tagName: 'link',
      attributes: {
        rel: 'stylesheet',
        href: 'https://fonts.googleapis.com/css2?family=Plus+Jakarta+Sans:wght@400;500;600;700;800&family=JetBrains+Mono:wght@400;500;700&display=swap',
      },
    },
  ],

  markdown: {
    format: 'md',
    hooks: {
      onBrokenMarkdownLinks: 'warn',
    },
    preprocessor: ({fileContent}) => {
      // Escape { and } outside fenced code blocks so they aren't parsed as JSX expressions
      const lines = fileContent.split('\n');
      let inCodeFence = false;
      return lines.map(line => {
        if (/^```/.test(line)) {
          inCodeFence = !inCodeFence;
          return line;
        }
        if (inCodeFence) return line;
        // Also skip inline code spans — only escape braces NOT inside backticks
        // Simple approach: escape all { } on non-code-fence lines, but preserve inline code
        let result = '';
        let inInlineCode = false;
        for (let i = 0; i < line.length; i++) {
          if (line[i] === '`') {
            inInlineCode = !inInlineCode;
            result += '`';
          } else if (!inInlineCode && line[i] === '{') {
            result += '\\{';
          } else if (!inInlineCode && line[i] === '}') {
            result += '\\}';
          } else {
            result += line[i];
          }
        }
        return result;
      }).join('\n');
    },
  },

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  presets: [
    [
      'classic',
      {
        docs: {
          path: '../docs',
          routeBasePath: '/',
          sidebarPath: './sidebars.ts',
          editUrl: ({docPath}) =>
            `https://github.com/fullsend-ai/fullsend/edit/main/docs/${docPath}`,
          exclude: [
            '**/agents/icons/**',
            '**/testing/**',
          ],
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    navbar: {
      title: 'Fullsend',
      logo: {
        alt: 'Fullsend',
        src: 'img/logo.png',
        href: 'https://fullsend.sh',
        target: '_self',
      },
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'docs',
          position: 'left',
          label: 'Docs',
        },
        {
          type: 'docSidebar',
          sidebarId: 'cli',
          position: 'left',
          label: 'CLI Reference',
        },
        {
          href: 'https://github.com/fullsend-ai/fullsend',
          position: 'right',
          className: 'header-github-link',
          'aria-label': 'GitHub repository',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Docs',
          items: [
            {
              label: 'Getting Started',
              to: '/',
            },
            {
              label: 'Agents',
              to: '/agents',
            },
            {
              label: 'CLI Reference',
              to: '/cli',
            },
          ],
        },
        {
          title: 'Community',
          items: [
            {
              label: 'GitHub',
              href: 'https://github.com/fullsend-ai/fullsend',
            },
            {
              label: 'GitHub Issues',
              href: 'https://github.com/fullsend-ai/fullsend/issues',
            },
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} Red Hat, LLC. Built with Docusaurus.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'yaml', 'json', 'toml', 'go'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
