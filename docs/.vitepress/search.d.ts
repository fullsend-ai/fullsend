import "vitepress";

declare module "vitepress" {
  namespace DefaultTheme {
    interface LocalSearchOptions {
      scopes?: { label: string; prefixes: string[] }[];
    }
  }
}
