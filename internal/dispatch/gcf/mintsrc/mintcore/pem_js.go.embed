//go:build js

package mintcore

import (
	"context"
	"fmt"
	"syscall/js"
)

// HostPEMAccessor implements PEMAccessor by delegating to a JavaScript
// callback provided by the Worker host. The callback reads PEM data
// from Worker secrets or KV bindings.
//
// Callback signature: callback(role) => Promise<string>
// Returns the PEM-encoded private key for the given role, or throws
// an error if the secret is not found.
type HostPEMAccessor struct {
	pemFn js.Value
}

// NewHostPEMAccessor wraps a JavaScript function as a PEMAccessor.
// The function must accept a role name string and return a Promise
// resolving to the PEM key data as a string.
func NewHostPEMAccessor(pemFn js.Value) (*HostPEMAccessor, error) {
	if pemFn.IsUndefined() || pemFn.IsNull() {
		return nil, fmt.Errorf("PEM callback must not be null or undefined")
	}
	if pemFn.Type() != js.TypeFunction {
		return nil, fmt.Errorf("PEM callback must be a function, got %s", pemFn.Type())
	}
	return &HostPEMAccessor{pemFn: pemFn}, nil
}

// AccessPEM retrieves PEM data for the given role via the host callback.
func (h *HostPEMAccessor) AccessPEM(_ context.Context, role string) ([]byte, error) {
	secretRole := PemSecretRole(role)
	if err := ValidateRoleName(secretRole); err != nil {
		return nil, err
	}

	result, err := awaitPromise(h.pemFn.Invoke(secretRole))
	if err != nil {
		return nil, fmt.Errorf("host PEM accessor failed for role %q: %w", role, err)
	}

	pemStr := result.String()
	if pemStr == "" {
		return nil, fmt.Errorf("host returned empty PEM for role %q", role)
	}

	return []byte(pemStr), nil
}
