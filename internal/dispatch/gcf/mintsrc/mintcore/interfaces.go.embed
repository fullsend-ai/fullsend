package mintcore

import (
	"context"
	"net/http"
)

// HTTPDoer abstracts http.Client for testability.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// OIDCVerifier validates OIDC tokens and returns parsed claims.
// Implementations include JWKSVerifier (direct JWKS validation) and
// STSVerifier (GCP Workload Identity Federation via STS exchange).
type OIDCVerifier interface {
	Verify(ctx context.Context, rawToken string) (*Claims, error)
}

// PEMAccessor retrieves agent PEM keys by role.
// Implementations encapsulate the storage backend (GCP Secret Manager,
// local filesystem, etc.).
type PEMAccessor interface {
	AccessPEM(ctx context.Context, role string) ([]byte, error)
}
