import VPLTheme from "@lando/vitepress-theme-default-plus";
import ReadingProgress from "./components/ReadingProgress.vue";
import "./custom.css";
import { defineAsyncComponent, h } from "vue";
import type { Theme } from "vitepress";

export default {
  extends: VPLTheme,
  Layout() {
    return h(VPLTheme.Layout!, null, {
      "layout-top": () => h(ReadingProgress),
    });
  },
  enhanceApp({ app }) {
    app.component(
      "Mermaid",
      defineAsyncComponent(() => import("./components/Mermaid.vue")),
    );
  },
} satisfies Theme;
