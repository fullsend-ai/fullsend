package mintcore

import "context"

// Doer performs outbound HTTP requests using transport-agnostic types.
// On non-WASM platforms, this wraps http.Client. On WASM, it delegates
// to the host's JS fetch callback — avoiding the net/http import and its
// heavy crypto/tls dependency tree.
type Doer interface {
	Do(ctx context.Context, method, url string, headers map[string]string, body []byte) (statusCode int, respBody []byte, err error)
}

// HTTP status codes used by mintcore, defined locally to avoid importing
// net/http in the WASM build (which would pull in crypto/tls and add
// ~1 MB gzip to the binary).
const (
	statusOK                  = 200
	statusCreated             = 201
	statusBadRequest          = 400
	statusUnauthorized        = 401
	statusForbidden           = 403
	statusNotFound            = 404
	statusMethodNotAllowed    = 405
	statusUnprocessableEntity = 422
	statusInternalServerError = 500
	statusBadGateway          = 502
	statusServiceUnavailable  = 503
)
