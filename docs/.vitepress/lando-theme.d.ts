declare module "@lando/vitepress-theme-default-plus/config" {
  import type { UserConfig } from "vitepress";

  interface VPLThemeConfig {
    sidebarEnder?: unknown;
    multiVersionBuild?: unknown;
    [key: string]: unknown;
  }

  export function defineConfig(config: UserConfig<VPLThemeConfig>): UserConfig<VPLThemeConfig>;
}

declare module "@lando/vitepress-theme-default-plus" {
  import type { Theme } from "vitepress";
  const theme: Theme;
  export default theme;
}

declare module "*.vue" {
  import type { DefineComponent } from "vue";
  const component: DefineComponent;
  export default component;
}
