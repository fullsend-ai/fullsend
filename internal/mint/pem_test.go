package function

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

func TestGCPSecretPEMAccessor_InputValidation(t *testing.T) {
	accessor := mintcore.NewGCPSecretPEMAccessor(
		&http.Client{Timeout: 10 * time.Second},
		"123456789",
	)

	tests := []struct {
		name    string
		role    string
		wantErr string
	}{
		{name: "valid role", role: "coder"},
		{name: "fix resolves to coder", role: "fix"},
		{name: "role with double hyphen", role: "co--der", wantErr: "invalid role name"},
		{name: "role fails pattern", role: "code!", wantErr: "invalid role name"},
		{name: "empty role", role: "", wantErr: "invalid role name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := accessor.AccessPEM(context.Background(), tt.role)
			if tt.wantErr == "" {
				// Valid inputs pass validation and proceed to the GCP
				// metadata token fetch, which fails in unit tests (no
				// metadata server). We assert an error IS returned and
				// that it is NOT a validation error — proving input
				// validation passed without coupling to downstream
				// error wording.
				if err == nil {
					t.Fatal("expected non-nil error (metadata fetch should fail in unit tests)")
				}
				if strings.Contains(err.Error(), "invalid role name") {
					t.Fatalf("unexpected validation error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestPemSecretRole(t *testing.T) {
	if got := mintcore.PemSecretRole("fix"); got != "coder" {
		t.Fatalf("PemSecretRole(fix) = %q, want coder", got)
	}
	if got := mintcore.PemSecretRole("coder"); got != "coder" {
		t.Fatalf("PemSecretRole(coder) = %q, want coder", got)
	}
	if got := mintcore.PemSecretRole("triage"); got != "triage" {
		t.Fatalf("PemSecretRole(triage) = %q, want triage", got)
	}
}
