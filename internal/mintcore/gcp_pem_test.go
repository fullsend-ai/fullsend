//go:build !js

package mintcore

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
)

func gcpSecretFake(t *testing.T, secretStatus int, payload string, gotPaths *[]string) func(*http.Request) (*http.Response, error) {
	t.Helper()
	return func(r *http.Request) (*http.Response, error) {
		*gotPaths = append(*gotPaths, r.URL.Host+r.URL.Path)
		if r.URL.Host == "metadata.google.internal" {
			if r.Header.Get("Metadata-Flavor") != "Google" {
				t.Errorf("metadata request missing Metadata-Flavor header")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"access_token":"meta-token"}`))}, nil
		}
		if got := r.Header.Get("Authorization"); got != "Bearer meta-token" {
			t.Errorf("Authorization = %q, want metadata token", got)
		}
		body := `{"payload":{"data":"` + base64.StdEncoding.EncodeToString([]byte(payload)) + `"}}`
		if secretStatus != http.StatusOK {
			body = `{"error":"` + payload + `"}`
		}
		return &http.Response{StatusCode: secretStatus, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
}

func TestAccessGCPSecret_ReadsLatestVersion(t *testing.T) {
	var paths []string
	SetMintHTTPForTest(t, gcpSecretFake(t, http.StatusOK, "authorization=Bearer abc", &paths))

	got, err := AccessGCPSecret(context.Background(), "123456", "fullsend-mint-otlp-headers")
	if err != nil {
		t.Fatalf("AccessGCPSecret: %v", err)
	}
	if string(got) != "authorization=Bearer abc" {
		t.Fatalf("payload = %q", got)
	}
	want := "secretmanager.googleapis.com/v1/projects/123456/secrets/fullsend-mint-otlp-headers/versions/latest:access"
	if len(paths) != 2 || paths[1] != want {
		t.Fatalf("requests = %v, want metadata then %s", paths, want)
	}
}

func TestAccessGCPSecret_ErrorOmitsPayload(t *testing.T) {
	var paths []string
	SetMintHTTPForTest(t, gcpSecretFake(t, http.StatusForbidden, "sensitive-upstream-body", &paths))

	_, err := AccessGCPSecret(context.Background(), "123456", "fullsend-mint-otlp-headers")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "sensitive-upstream-body") {
		t.Fatalf("error leaks upstream body: %v", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("error = %v, want status 403", err)
	}
}

func TestAccessGCPSecret_RejectsInvalidSecretID(t *testing.T) {
	SetMintHTTPForTest(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("no request expected for an invalid secret ID")
		return nil, nil
	})
	for _, id := range []string{"", "a/b", "../x", "x:access", strings.Repeat("a", 256)} {
		if _, err := AccessGCPSecret(context.Background(), "123456", id); err == nil {
			t.Errorf("AccessGCPSecret(%q): expected error", id)
		}
	}
}

func TestGCPSecretPEMAccessor_UsesRoleSecret(t *testing.T) {
	var paths []string
	SetMintHTTPForTest(t, gcpSecretFake(t, http.StatusOK, "pem-bytes", &paths))

	got, err := NewGCPSecretPEMAccessor("123456").AccessPEM(context.Background(), "fix")
	if err != nil {
		t.Fatalf("AccessPEM: %v", err)
	}
	if string(got) != "pem-bytes" {
		t.Fatalf("payload = %q", got)
	}
	want := "secretmanager.googleapis.com/v1/projects/123456/secrets/fullsend-coder-app-pem/versions/latest:access"
	if len(paths) != 2 || paths[1] != want {
		t.Fatalf("requests = %v, want metadata then %s", paths, want)
	}
}
