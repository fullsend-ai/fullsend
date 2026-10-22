// Package workersrc exposes the Worker's npm manifest and lock file to Go
// callers. The directory holds the Worker's TypeScript sources; this file
// only embeds package.json and package-lock.json so other packages can
// run `npm ci` for the Worker's locked dependencies (Wrangler among them)
// without importing the Cloudflare provisioner.
package workersrc

import _ "embed"

// PackageJSON is the Worker's package.json.
//
//go:embed package.json
var PackageJSON []byte

// PackageLock is the Worker's package-lock.json.
//
//go:embed package-lock.json
var PackageLock []byte
