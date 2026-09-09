// Ambient type declarations for the Cloudflare Worker test environment.
//
// @cloudflare/vitest-pool-workers v0.22+ moved the "cloudflare:test" module
// declaration to a separate export subpath ("./types"). The triple-slash
// reference below pulls it in so that test files can import
// createExecutionContext, waitOnExecutionContext, etc.
/// <reference types="@cloudflare/vitest-pool-workers/types" />

// Declare the main module's exports so that
//   import { exports } from "cloudflare:workers"
// resolves exports.default to the worker's default export handler.
// Required by @cloudflare/workers-types which derives Cloudflare.Exports
// from GlobalProps.mainModule (defaults to {} when undeclared).
declare namespace Cloudflare {
  interface GlobalProps {
    mainModule: typeof import("./src/index");
  }
}
