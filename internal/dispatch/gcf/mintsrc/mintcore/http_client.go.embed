//go:build !js

package mintcore

import (
	"net/http"
	"sync"
	"time"
)

var (
	defaultHTTPClient *http.Client
	httpOnce          sync.Once
	httpOverride      func(*http.Request) (*http.Response, error)
	httpMu            sync.Mutex // guards httpOverride

	noRedirectHTTPClient *http.Client
	noRedirectOnce       sync.Once
)

// mintHTTP executes an HTTP request using the package-level HTTP client.
// On native platforms this is a cached *http.Client with a 30-second
// timeout, matching the existing entrypoint behavior. Tests can
// override the behavior via SetMintHTTPForTest.
func mintHTTP(req *http.Request) (*http.Response, error) {
	httpMu.Lock()
	f := httpOverride
	httpMu.Unlock()
	if f != nil {
		return f(req)
	}
	if req.Context().Value(noRedirectKey{}) != nil {
		noRedirectOnce.Do(func() {
			noRedirectHTTPClient = &http.Client{
				Timeout: 30 * time.Second,
				CheckRedirect: func(*http.Request, []*http.Request) error {
					return http.ErrUseLastResponse
				},
			}
		})
		return noRedirectHTTPClient.Do(req)
	}
	httpOnce.Do(func() {
		defaultHTTPClient = &http.Client{Timeout: 30 * time.Second}
	})
	return defaultHTTPClient.Do(req)
}
