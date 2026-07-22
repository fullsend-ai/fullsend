//go:build js

package mintcore

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall/js"
)

// HostFetchDoer implements HTTPDoer by delegating to a JavaScript fetch
// callback provided by the Worker host. The callback signature is:
//
//	callback(method, url, headersJSON, body) => {status, headersJSON, body}
//
// This allows the Worker to use its own fetch implementation (including
// Cloudflare-specific features like service bindings) while mintcore
// remains transport-agnostic.
type HostFetchDoer struct {
	fetchFn js.Value
}

// NewHostFetchDoer wraps a JavaScript function as an HTTPDoer.
// The function must accept (method, url, headersJSON, body) and return
// a Promise resolving to {status: number, headers: string, body: string}.
func NewHostFetchDoer(fetchFn js.Value) (*HostFetchDoer, error) {
	if fetchFn.IsUndefined() || fetchFn.IsNull() {
		return nil, fmt.Errorf("fetch callback must not be null or undefined")
	}
	if fetchFn.Type() != js.TypeFunction {
		return nil, fmt.Errorf("fetch callback must be a function, got %s", fetchFn.Type())
	}
	return &HostFetchDoer{fetchFn: fetchFn}, nil
}

// Do executes an HTTP request by calling the host fetch callback.
func (h *HostFetchDoer) Do(req *http.Request) (*http.Response, error) {
	// Serialize headers to JSON.
	headerMap := make(map[string]string, len(req.Header))
	for k, v := range req.Header {
		headerMap[k] = strings.Join(v, ", ")
	}

	var bodyStr string
	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("reading request body: %w", err)
		}
		bodyStr = string(bodyBytes)
	}

	// Call the JS fetch callback synchronously via Await.
	// The callback returns a Promise; we block until it resolves.
	result, err := awaitPromise(h.fetchFn.Invoke(
		req.Method,
		req.URL.String(),
		marshalJSHeaders(headerMap),
		bodyStr,
	))
	if err != nil {
		return nil, fmt.Errorf("host fetch failed: %w", err)
	}

	status := result.Get("status").Int()
	respHeadersJSON := result.Get("headers").String()
	respBody := result.Get("body").String()

	resp := &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(respBody)),
	}

	// Parse response headers.
	parseJSHeaders(respHeadersJSON, resp.Header)

	return resp, nil
}

// awaitPromise blocks until a JS Promise resolves or rejects.
func awaitPromise(promise js.Value) (js.Value, error) {
	ch := make(chan js.Value, 1)
	errCh := make(chan error, 1)

	thenFn := js.FuncOf(func(_ js.Value, args []js.Value) interface{} {
		ch <- args[0]
		return nil
	})
	defer thenFn.Release()

	catchFn := js.FuncOf(func(_ js.Value, args []js.Value) interface{} {
		errCh <- fmt.Errorf("%s", args[0].String())
		return nil
	})
	defer catchFn.Release()

	promise.Call("then", thenFn).Call("catch", catchFn)

	select {
	case v := <-ch:
		return v, nil
	case err := <-errCh:
		return js.Value{}, err
	}
}

// marshalJSHeaders converts a Go header map to a JSON string for JS.
func marshalJSHeaders(headers map[string]string) string {
	if len(headers) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteByte('{')
	first := true
	for k, v := range headers {
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.WriteByte('"')
		b.WriteString(escapeJSON(k))
		b.WriteString(`":"`)
		b.WriteString(escapeJSON(v))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// parseJSHeaders parses a JSON header string into an http.Header.
func parseJSHeaders(headersJSON string, dst http.Header) {
	// Simple JSON object parser for {"key":"value",...} format.
	headersJSON = strings.TrimSpace(headersJSON)
	if headersJSON == "" || headersJSON == "{}" {
		return
	}
	// Strip outer braces.
	inner := headersJSON[1 : len(headersJSON)-1]
	// Split on commas between key-value pairs (simple approach).
	for _, pair := range splitJSONPairs(inner) {
		pair = strings.TrimSpace(pair)
		colonIdx := strings.Index(pair, ":")
		if colonIdx < 0 {
			continue
		}
		key := unquoteJSON(strings.TrimSpace(pair[:colonIdx]))
		val := unquoteJSON(strings.TrimSpace(pair[colonIdx+1:]))
		dst.Set(key, val)
	}
}

// splitJSONPairs splits a JSON object's inner content on commas,
// respecting quoted strings.
func splitJSONPairs(s string) []string {
	var pairs []string
	var current strings.Builder
	inQuote := false
	escaped := false
	for _, r := range s {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			current.WriteRune(r)
			escaped = true
			continue
		}
		if r == '"' {
			inQuote = !inQuote
			current.WriteRune(r)
			continue
		}
		if r == ',' && !inQuote {
			pairs = append(pairs, current.String())
			current.Reset()
			continue
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		pairs = append(pairs, current.String())
	}
	return pairs
}

// unquoteJSON removes surrounding quotes from a JSON string value.
func unquoteJSON(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// escapeJSON escapes special characters in a string for JSON encoding.
func escapeJSON(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
