// Type declarations for the Go WASM support file (wasm_exec.js).
//
// wasm_exec.js is copied from the Go toolchain at build time:
//   cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" .
// (Go ≥1.24 moved wasm_exec.js from misc/wasm/ to lib/wasm/.)
//
// It registers a Go class on globalThis that bootstraps the Go
// runtime and provides the import object required by WASM binaries
// compiled with GOOS=js GOARCH=wasm.

declare class Go {
  importObject: WebAssembly.Imports;
  run(instance: WebAssembly.Instance): Promise<void>;
}
