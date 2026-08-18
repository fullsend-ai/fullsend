//go:build !js

package mintcore

import (
	"testing"
)

func TestMintEnv_ReadsOsGetenv(t *testing.T) {
	t.Setenv("OIDC_AUDIENCE", "test-audience")
	got := mintEnv("OIDC_AUDIENCE")
	if got != "test-audience" {
		t.Fatalf("mintEnv(OIDC_AUDIENCE) = %q, want %q", got, "test-audience")
	}
}

func TestMintEnv_ReturnsEmptyForUnset(t *testing.T) {
	t.Setenv("MINTENV_TEST_UNSET", "")
	got := mintEnv("MINTENV_TEST_UNSET")
	if got != "" {
		t.Fatalf("mintEnv(MINTENV_TEST_UNSET) = %q, want empty", got)
	}
}
