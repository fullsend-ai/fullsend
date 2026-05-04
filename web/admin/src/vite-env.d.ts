/// <reference types="svelte" />
/// <reference types="vite/client" />

/** Merged with `vite/client` (see root `vite.config.ts` `base`). */
interface ImportMetaEnv {
  /** Vite `base` (e.g. `/admin/`). */
  readonly BASE: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
