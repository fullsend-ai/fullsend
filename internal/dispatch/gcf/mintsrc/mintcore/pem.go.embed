package mintcore

import "context"

// PEMAccessor retrieves agent PEM keys by org and role.
// Implementations encapsulate the storage backend (GCP Secret Manager,
// local filesystem, etc.).
type PEMAccessor interface {
	AccessPEM(ctx context.Context, org, role string) ([]byte, error)
}
