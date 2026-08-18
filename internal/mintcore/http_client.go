//go:build !js

package mintcore

import (
	"net/http"
	"time"
)

// httpDoerOverride allows tests to inject a custom HTTPDoer via
// SetHTTPDoerForTest. When nil, mintHTTP returns a default http.Client.
var httpDoerOverride HTTPDoer

// mintHTTP returns the HTTPDoer used by NewHandler and the named
// verifier factories (NewJWKSVerifierFromEnv, NewSTSVerifierFromEnv).
// On native builds, it returns a configured *http.Client with a 30-second
// timeout. Tests can override via SetHTTPDoerForTest.
func mintHTTP() HTTPDoer {
	if httpDoerOverride != nil {
		return httpDoerOverride
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// SetHTTPDoerForTest overrides mintHTTP for the duration of the test.
// The original value is restored via t.Cleanup.
func SetHTTPDoerForTest(t interface{ Cleanup(func()) }, doer HTTPDoer) {
	old := httpDoerOverride
	httpDoerOverride = doer
	t.Cleanup(func() { httpDoerOverride = old })
}
