//go:build !js

package mintcore

import "os"

// mintEnv returns the value of the environment variable named by key.
// On native builds (GCF, standalone binary, go test), it delegates to
// os.Getenv. The WASM build (env_js.go) uses a JS callback registered
// once during mintcoreInitMint instead.
func mintEnv(key string) string {
	return os.Getenv(key)
}
