// Binary mint-wasm is the Cloudflare Worker WASM host for mintcore.
// It registers two global JavaScript functions:
//
//   - mintcoreInitMint(configJSON, fetchCallback, pemCallback,
//     cryptoSignCallback, cryptoVerifyCallback) — initializes the mint handler
//     from explicit Worker binding config (not os.Getenv).
//
//   - mintcoreHandleFetch(method, url, headersJSON, body) — maps a Fetch API
//     request into HandleRaw arguments, calls the handler, and returns
//     {status, headers, body} for the Worker to convert back into a Response.
//
// The Worker JS side acts as the listener/host; mintcore keeps using
// HandleRaw as the request path. On non-WASM platforms, the Handler wraps
// HandleRaw with ServeHTTP for net/http compatibility.
//
// This binary intentionally avoids importing net/http and crypto/rsa.
// Those packages (and their transitive dependency crypto/tls) add ~1 MB gzip
// to the WASM binary. Instead, outbound HTTP uses HostFetchDoer (JS fetch)
// and RSA operations use HostCryptoSigner/HostCryptoVerifier (Web Crypto).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"syscall/js"

	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

var handler *mintcore.Handler

func main() {
	js.Global().Set("mintcoreInitMint", js.FuncOf(initMint))
	js.Global().Set("mintcoreHandleFetch", js.FuncOf(handleFetch))

	// Block forever — the Worker runtime keeps the WASM instance alive.
	select {}
}

// initMint initializes the mint handler from Worker bindings.
// JS signature: mintcoreInitMint(configJSON, fetchCallback, pemCallback,
//
//	cryptoSignCallback, cryptoVerifyCallback) => string
//
// Returns "" on success or an error message string on failure.
func initMint(_ js.Value, args []js.Value) interface{} {
	if len(args) < 5 {
		return "mintcoreInitMint requires 5 arguments: configJSON, fetchCallback, pemCallback, cryptoSignCallback, cryptoVerifyCallback"
	}

	configJSON := args[0].String()
	fetchFn := args[1]
	pemFn := args[2]
	signFn := args[3]
	verifyFn := args[4]

	var cfg mintcore.WorkerConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return fmt.Sprintf("failed to parse config: %v", err)
	}

	fetchDoer, err := mintcore.NewHostFetchDoer(fetchFn)
	if err != nil {
		return fmt.Sprintf("invalid fetch callback: %v", err)
	}

	pemAccessor, err := mintcore.NewHostPEMAccessor(pemFn)
	if err != nil {
		return fmt.Sprintf("invalid PEM callback: %v", err)
	}

	cryptoSigner, err := mintcore.NewHostCryptoSigner(signFn)
	if err != nil {
		return fmt.Sprintf("invalid crypto sign callback: %v", err)
	}
	mintcore.SetCryptoSigner(cryptoSigner)

	cryptoVerifier, err := mintcore.NewHostCryptoVerifier(verifyFn)
	if err != nil {
		return fmt.Sprintf("invalid crypto verify callback: %v", err)
	}
	mintcore.SetCryptoVerifier(cryptoVerifier)

	verifier := mintcore.NewJWKSVerifier(mintcore.JWKSVerifierConfig{
		IssuerURL:  "https://token.actions.githubusercontent.com",
		Audience:   cfg.OIDCAudience,
		HTTPClient: fetchDoer,
	})

	h, err := mintcore.ParseWorkerConfig(cfg, pemAccessor, verifier, fetchDoer)
	if err != nil {
		return fmt.Sprintf("failed to initialize handler: %v", err)
	}

	handler = h
	return ""
}

// handleFetch processes a Worker Fetch request through HandleRaw.
// JS signature: mintcoreHandleFetch(method, url, headersJSON, body) => Promise<{status, headers, body}>
//
// headersJSON must include Authorization when authentication is required.
// The Worker JS side converts Fetch Request → these arguments, and converts
// the returned {status, headers, body} back into a Response.
//
// CRITICAL: HandleRaw must NOT run synchronously inside this js.FuncOf
// callback. js.FuncOf callbacks block the JavaScript event loop while
// they execute. HandleRaw calls HostFetchDoer.Do / HostPEMAccessor.AccessPEM,
// which call awaitPromise() to wait on JS Promises (host fetch, PEM lookup).
// Those Promises cannot settle while the event loop is blocked by this
// callback — causing a fatal deadlock ("all goroutines are asleep").
//
// The fix: return a JS Promise immediately and run HandleRaw on a separate
// goroutine. The js.FuncOf callback returns, freeing the event loop, and
// the goroutine's awaitPromise calls can settle normally.
func handleFetch(_ js.Value, args []js.Value) interface{} {
	if handler == nil {
		return newPromiseReject("mint not initialized; call mintcoreInitMint first")
	}
	if len(args) < 4 {
		return newPromiseReject("mintcoreHandleFetch requires 4 arguments: method, url, headersJSON, body")
	}

	// Capture JS values as Go strings before returning from the callback.
	method := args[0].String()
	reqURL := args[1].String()
	headersJSON := args[2].String()
	body := args[3].String()

	var resolve, reject js.Value
	executor := js.FuncOf(func(_ js.Value, promiseArgs []js.Value) interface{} {
		resolve = promiseArgs[0]
		reject = promiseArgs[1]
		return nil
	})
	promise := js.Global().Get("Promise").New(executor)
	executor.Release()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				reject.Invoke(js.Global().Get("Error").New(
					fmt.Sprintf("panic in HandleRaw: %v", r)))
			}
		}()

		// Parse request headers.
		headers := make(map[string]string)
		if headersJSON != "" && headersJSON != "{}" {
			if err := json.Unmarshal([]byte(headersJSON), &headers); err != nil {
				reject.Invoke(js.Global().Get("Error").New(
					fmt.Sprintf("failed to parse headersJSON: %v", err)))
				return
			}
		}

		// Extract the path from the full URL.
		path := extractPath(reqURL)

		// Call HandleRaw — no net/http types needed.
		ctx := context.Background()
		status, respHeaders, respBody := handler.HandleRaw(ctx, method, path, headers, []byte(body))

		// Build response headers JSON.
		respHeadersJSON, _ := json.Marshal(respHeaders)

		// Resolve the Promise with {status, headers, body}.
		resolve.Invoke(mapToJSObject(map[string]interface{}{
			"status":  status,
			"headers": string(respHeadersJSON),
			"body":    string(respBody),
		}))
	}()

	return promise
}

// extractPath extracts the path component from a URL string.
func extractPath(rawURL string) string {
	// Find scheme separator.
	schemeEnd := strings.Index(rawURL, "://")
	if schemeEnd < 0 {
		// No scheme — treat entire string as path.
		if idx := strings.IndexByte(rawURL, '?'); idx >= 0 {
			return rawURL[:idx]
		}
		return rawURL
	}
	// Skip past scheme and authority.
	rest := rawURL[schemeEnd+3:]
	pathStart := strings.IndexByte(rest, '/')
	if pathStart < 0 {
		return "/"
	}
	path := rest[pathStart:]
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		return path[:idx]
	}
	return path
}

// newPromiseReject creates a JS Promise that rejects with the given error message.
func newPromiseReject(errMsg string) js.Value {
	promiseConstructor := js.Global().Get("Promise")
	return promiseConstructor.Call("reject", js.Global().Get("Error").New(errMsg))
}

// mapToJSObject converts a Go map to a JS object.
func mapToJSObject(m map[string]interface{}) js.Value {
	obj := js.Global().Get("Object").New()
	for k, v := range m {
		obj.Set(k, v)
	}
	return obj
}
