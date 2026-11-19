//go:build !github

package mintcore

import (
	"context"
	"net/http"
)

// validateStatusGitHub is the stub for builds without the github tag.
// It returns errStatusAuthSkip unconditionally — OIDC is the only
// auth path when the tag is absent.
func validateStatusGitHub(_ context.Context, _ *http.Request, _ string) error {
	return errStatusAuthSkip
}
