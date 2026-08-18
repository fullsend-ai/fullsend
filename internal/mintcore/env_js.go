//go:build js

package mintcore

import (
	"fmt"
	"syscall/js"
)

var jsGetEnv js.Value

// RegisterEnv sets the JavaScript callback used to read environment
// variables in the WASM build. It must be called once during
// mintcoreInitMint before constructing verifiers or the handler.
//
// The callback signature is: (key: string) => string.
func RegisterEnv(fn js.Value) error {
	if fn.IsUndefined() || fn.IsNull() {
		return fmt.Errorf("env callback must not be null or undefined")
	}
	if fn.Type() != js.TypeFunction {
		return fmt.Errorf("env callback must be a function, got %s", fn.Type())
	}
	jsGetEnv = fn
	return nil
}

// mintEnv returns the value of the environment variable named by key.
// On the WASM build, it delegates to the JS callback registered via
// RegisterEnv. Returns "" if no callback has been registered or the
// callback returns a non-string value.
func mintEnv(key string) string {
	if jsGetEnv.IsUndefined() || jsGetEnv.IsNull() {
		return ""
	}
	result := jsGetEnv.Invoke(key)
	if result.Type() == js.TypeString {
		return result.String()
	}
	return ""
}
