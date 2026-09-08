//go:build !cfaccess

package mintcore

import (
	"context"
	"net/http"
)

// validateStatusCFAccess is the stub for builds without the cfaccess tag.
// It returns errStatusAuthSkip unconditionally — OIDC is the only
// auth path when the tag is absent.
func validateStatusCFAccess(_ context.Context, _ *http.Request) error {
	return errStatusAuthSkip
}
