//go:build js

package mintcore

import (
	"encoding/json"
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

	// Call the JS fetch callback synchronously via Await.
	// The callback returns a Promise; we block until it resolves.
	result, err := awaitPromise(h.fetchFn.Invoke(
		req.Method,
		req.URL.String(),
		string(headersJSON),
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
