//go:build js

package mintcore

import (
	"fmt"
	"syscall/js"
)

// jsHTTPDoer holds the HostFetchDoer registered via RegisterHTTP.
var jsHTTPDoer HTTPDoer

// RegisterHTTP wraps the JavaScript fetch callback as an HTTPDoer and
// stores it for use by mintHTTP. It must be called once during
// mintcoreInitMint before constructing verifiers or the handler.
//
// The callback signature matches HostFetchDoer:
//
//	callback(method, url, headersJSON, body) => Promise<{status, headers, body}>
func RegisterHTTP(fetchFn js.Value) error {
	if fetchFn.IsUndefined() || fetchFn.IsNull() {
		return fmt.Errorf("fetch callback must not be null or undefined")
	}
	if fetchFn.Type() != js.TypeFunction {
		return fmt.Errorf("fetch callback must be a function, got %s", fetchFn.Type())
	}
	doer, err := NewHostFetchDoer(fetchFn)
	if err != nil {
		return err
	}
	jsHTTPDoer = doer
	return nil
}

// mintHTTP returns the HTTPDoer registered via RegisterHTTP.
// Returns nil if no doer has been registered — callers that need HTTP
// before RegisterHTTP is called will fail at request time.
func mintHTTP() HTTPDoer {
	return jsHTTPDoer
}
