// Type declarations for WASM module imports (ES module format).
// Wrangler treats .wasm imports as CompiledWasm modules via [[rules]].
declare module "*.wasm" {
  const module: WebAssembly.Module;
  export default module;
}
