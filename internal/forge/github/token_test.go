package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeInstallationToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int
		want    bool
		wantErr bool
	}{
		{name: "installation token", status: http.StatusOK, want: true},
		{name: "forbidden", status: http.StatusForbidden, want: false},
		{name: "unauthorized", status: http.StatusUnauthorized, want: false},
		{name: "server error", status: http.StatusInternalServerError, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/installation/repositories" {
					t.Fatalf("unexpected path: %s", r.URL.Path)
				}
				if got := r.URL.Query().Get("per_page"); got != "1" {
					t.Fatalf("per_page = %q, want 1", got)
				}
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			got, err := ProbeInstallationToken(context.Background(), srv.Client(), srv.URL, "ghs_test")
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ProbeInstallationToken() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ProbeInstallationToken() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProbeInstallationToken_emptyToken(t *testing.T) {
	t.Parallel()

	got, err := ProbeInstallationToken(context.Background(), http.DefaultClient, "http://example.com", "")
	if err != nil {
		t.Fatalf("ProbeInstallationToken() error = %v", err)
	}
	if got {
		t.Fatal("expected false for empty token")
	}
}

func TestLiveClient_IsInstallationToken(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := New("ghs_test").WithBaseURL(srv.URL)
	got, err := client.IsInstallationToken(context.Background())
	if err != nil {
		t.Fatalf("IsInstallationToken() error = %v", err)
	}
	if !got {
		t.Fatal("IsInstallationToken() = false, want true")
	}
}
