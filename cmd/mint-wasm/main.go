// Binary mint-wasm is the Cloudflare Worker WASM host for mintcore.
// It registers two global JavaScript functions:
//
//   - mintcoreInitMint(configJSON, fetchCallback, pemCallback) — initializes
//     the mint handler from explicit Worker binding config (not os.Getenv).
//
//   - mintcoreHandleFetch(method, url, headersJSON, body, authHeader) — maps
//     a Fetch API request into an http.Request, calls Handler.ServeHTTP with a
//     buffered ResponseWriter, and returns {status, headers, body} for the
//     Worker to convert back into a Response.
//
// The Worker JS side acts as the listener/host; mintcore keeps using
// Handler.ServeHTTP as the request path, the same contract used by GCF
// and cmd/mint.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
// JS signature: mintcoreInitMint(configJSON, fetchCallback, pemCallback) => string
// Returns "" on success or an error message string on failure.
func initMint(_ js.Value, args []js.Value) interface{} {
	if len(args) < 3 {
		return "mintcoreInitMint requires 3 arguments: configJSON, fetchCallback, pemCallback"
	}

	configJSON := args[0].String()
	fetchFn := args[1]
	pemFn := args[2]

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

	allowedOrgs := splitCSV(cfg.AllowedOrgs)
	allowedWorkflows := splitCSV(cfg.AllowedWorkflowFiles)

	perRepoWIFRepos := make(map[string]bool)
	for _, entry := range splitCSV(cfg.PerRepoWIFRepos) {
		perRepoWIFRepos[strings.ToLower(entry)] = true
	}

	verifier := mintcore.NewJWKSVerifier(mintcore.JWKSVerifierConfig{
		IssuerURL:            "https://token.actions.githubusercontent.com",
		Audience:             cfg.OIDCAudience,
		HTTPClient:           fetchDoer,
		AllowedOrgs:          allowedOrgs,
		AllowedWorkflowFiles: allowedWorkflows,
		PerRepoWIFRepos:      perRepoWIFRepos,
	})

	h, err := mintcore.ParseWorkerConfig(cfg, pemAccessor, verifier, fetchDoer)
	if err != nil {
		return fmt.Sprintf("failed to initialize handler: %v", err)
	}

	handler = h
	return ""
}

// handleFetch processes a Worker Fetch request through ServeHTTP.
// JS signature: mintcoreHandleFetch(method, url, headersJSON, body, authHeader) => Promise<{status, headers, body}>
//
// The Worker JS side converts Fetch Request → these arguments, and converts
// the returned {status, headers, body} back into a Response.
func handleFetch(_ js.Value, args []js.Value) interface{} {
	if handler == nil {
		return newPromiseReject("mint not initialized; call mintcoreInitMint first")
	}
	if len(args) < 4 {
		return newPromiseReject("mintcoreHandleFetch requires 4 arguments: method, url, headersJSON, body")
	}

	method := args[0].String()
	reqURL := args[1].String()
	headersJSON := args[2].String()
	body := args[3].String()

	// Build an http.Request from the Fetch arguments.
	var bodyReader *bytes.Reader
	if body != "" {
		bodyReader = bytes.NewReader([]byte(body))
	} else {
		bodyReader = bytes.NewReader(nil)
	}

	req, err := http.NewRequest(method, reqURL, bodyReader)
	if err != nil {
		return newPromiseReject(fmt.Sprintf("failed to create request: %v", err))
	}

	// Parse request headers.
	if headersJSON != "" && headersJSON != "{}" {
		var headers map[string]string
		if err := json.Unmarshal([]byte(headersJSON), &headers); err == nil {
			for k, v := range headers {
				req.Header.Set(k, v)
			}
		}
	}

	// Use httptest.ResponseRecorder as a buffered ResponseWriter.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Build response headers JSON.
	respHeaders := make(map[string]string, len(rec.Header()))
	for k, v := range rec.Header() {
		respHeaders[k] = strings.Join(v, ", ")
	}
	respHeadersBytes, _ := json.Marshal(respHeaders)

	// Return a resolved Promise with {status, headers, body}.
	return newPromiseResolve(map[string]interface{}{
		"status":  rec.Code,
		"headers": string(respHeadersBytes),
		"body":    rec.Body.String(),
	})
}

// newPromiseResolve creates a JS Promise that resolves with the given value.
func newPromiseResolve(val map[string]interface{}) js.Value {
	promiseConstructor := js.Global().Get("Promise")
	return promiseConstructor.Call("resolve", mapToJSObject(val))
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

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	for _, entry := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
