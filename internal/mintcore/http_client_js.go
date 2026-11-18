//go:build js

package mintcore

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall/js"
	"time"
)

var registeredFetchFn js.Value

// RegisterHTTP stores a JS fetch callback as the package-level HTTP
// implementation. The callback signature is:
//
//	callback(method, url, headersJSON, body, noRedirect, timeoutMs) => Promise<{status, headers, body}>
//
// noRedirect is true for requests whose redirects must not be followed
// (OTLP export). The callback must then fetch with redirect: "manual" and
// return the 3xx response as is, so custom credential headers and the
// payload are never forwarded to another origin. It may also discard the
// response body, which the exporter does not use.
//
// timeoutMs is the remaining request deadline in milliseconds for
// noRedirect requests (0 when there is none). The callback must abort the
// underlying fetch by then.
//
// It must be called once from initMint before NewHandler is constructed.
func RegisterHTTP(fetchFn js.Value) error {
	if fetchFn.IsUndefined() || fetchFn.IsNull() {
		return fmt.Errorf("fetch callback must not be null or undefined")
	}
	if fetchFn.Type() != js.TypeFunction {
		return fmt.Errorf("fetch callback must be a function, got %s", fetchFn.Type())
	}
	registeredFetchFn = fetchFn
	return nil
}

// mintHTTP executes an HTTP request by calling the host JS fetch
// callback registered via RegisterHTTP.
func mintHTTP(req *http.Request) (*http.Response, error) {
	if registeredFetchFn.IsUndefined() || registeredFetchFn.IsNull() {
		return nil, fmt.Errorf("HTTP not registered; call RegisterHTTP first")
	}

	// OTLP export must not follow redirects; the host fetch bridge
	// honors this with redirect: "manual".
	noRedirect := req.Context().Value(noRedirectKey{}) != nil

	// Hand the remaining request deadline to the host fetch for OTLP
	// export, so the bridge aborts the underlying fetch when the Go-side
	// wait gives up instead of leaving it running (and its payload held)
	// after the exporter releases its concurrency slot. Zero means no
	// deadline: the bridge applies its default budget.
	timeoutMs := 0
	if noRedirect {
		if deadline, ok := req.Context().Deadline(); ok {
			timeoutMs = int(time.Until(deadline).Milliseconds())
			if timeoutMs < 1 {
				timeoutMs = 1
			}
		}
	}

	// Serialize headers to JSON.
	headerMap := make(map[string]string, len(req.Header))
	for k, v := range req.Header {
		headerMap[k] = strings.Join(v, ", ")
	}

	headersJSON, err := json.Marshal(headerMap)
	if err != nil {
		return nil, fmt.Errorf("marshalling request headers: %w", err)
	}

	var bodyStr string
	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("reading request body: %w", err)
		}
		bodyStr = string(bodyBytes)
	}

	// Check context before invoking fetch — no point starting a
	// network call if the deadline has already expired.
	if err := req.Context().Err(); err != nil {
		return nil, fmt.Errorf("request context already done: %w", err)
	}

	// Call the JS fetch callback and block until it resolves or the
	// request context is canceled. Using the context-aware variant
	// ensures that a per-request deadline (e.g., the 20s timeout set
	// by the WASM handler) aborts the wait before the JS-side
	// HANDLE_FETCH_TIMEOUT_MS fires, preventing isolate poisoning.
	result, err := awaitPromise(req.Context(), registeredFetchFn.Invoke(
		req.Method,
		req.URL.String(),
		string(headersJSON),
		bodyStr,
		noRedirect,
		timeoutMs,
	))
	if err != nil {
		return nil, fmt.Errorf("host fetch failed: %w", err)
	}

	status := result.Get("status").Int()
	respHeadersJSON := result.Get("headers").String()
	respBody := result.Get("body").String()

	resp := &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(respBody)),
	}

	// Parse response headers.
	if respHeadersJSON != "" && respHeadersJSON != "{}" {
		var parsed map[string]string
		if err := json.Unmarshal([]byte(respHeadersJSON), &parsed); err != nil {
			return nil, fmt.Errorf("parsing response headers: %w", err)
		}
		for k, v := range parsed {
			resp.Header.Set(k, v)
		}
	}

	return resp, nil
}
