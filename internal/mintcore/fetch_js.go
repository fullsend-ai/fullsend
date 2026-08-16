//go:build js

package mintcore

import (
	"context"
	"fmt"
	"syscall/js"
)

// HostFetchDoer implements Doer by delegating to a JavaScript fetch
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

// NewHostFetchDoer wraps a JavaScript function as a Doer.
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
func (h *HostFetchDoer) Do(_ context.Context, method, url string, headers map[string]string, body []byte) (int, []byte, error) {
	// Serialize headers to JSON.
	headersJSON := marshalStringMap(headers)

	var bodyStr string
	if len(body) > 0 {
		bodyStr = string(body)
	}

	// Call the JS fetch callback synchronously via awaitPromise.
	// The callback returns a Promise; we block until it resolves.
	result, err := awaitPromise(h.fetchFn.Invoke(
		method,
		url,
		headersJSON,
		bodyStr,
	))
	if err != nil {
		return 0, nil, fmt.Errorf("host fetch failed: %w", err)
	}

	status := result.Get("status").Int()
	respBody := result.Get("body").String()

	return status, []byte(respBody), nil
}

// marshalStringMap produces a JSON object string from a Go map.
// This avoids importing encoding/json for a simple key-value map.
func marshalStringMap(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	// Use encoding/json via the global JSON object is not available,
	// so we build the JSON manually for simple string maps.
	buf := []byte{'{'}
	first := true
	for k, v := range m {
		if !first {
			buf = append(buf, ',')
		}
		first = false
		buf = append(buf, '"')
		buf = append(buf, escapeJSONString(k)...)
		buf = append(buf, '"', ':', '"')
		buf = append(buf, escapeJSONString(v)...)
		buf = append(buf, '"')
	}
	buf = append(buf, '}')
	return string(buf)
}

// escapeJSONString escapes special characters in a JSON string value.
func escapeJSONString(s string) string {
	var out []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		default:
			if c < 0x20 {
				out = append(out, '\\', 'u', '0', '0',
					"0123456789abcdef"[c>>4],
					"0123456789abcdef"[c&0xf])
			} else {
				out = append(out, c)
			}
		}
	}
	return string(out)
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
