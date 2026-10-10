package openaiwif

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + enc.EncodeToString(b) + ".sig"
}

func TestParseJWTLifetime(t *testing.T) {
	tok := testJWT(t, map[string]any{"iat": 1000, "exp": 1300})
	iat, exp, err := ParseJWTLifetime(tok)
	if err != nil {
		t.Fatal(err)
	}
	if iat.Unix() != 1000 || exp.Unix() != 1300 {
		t.Fatalf("got iat=%d exp=%d", iat.Unix(), exp.Unix())
	}

	for name, bad := range map[string]string{
		"not a jwt":    "abc",
		"bad base64":   "a.!!!.c",
		"bad json":     "a." + base64.RawURLEncoding.EncodeToString([]byte("{")) + ".c",
		"no exp":       testJWT(t, map[string]any{"iat": 1000}),
		"exp <= iat":   testJWT(t, map[string]any{"iat": 1000, "exp": 1000}),
		"non-integer":  testJWT(t, map[string]any{"iat": 1000.5, "exp": 1300}),
		"string claim": testJWT(t, map[string]any{"iat": "x", "exp": 1300}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ParseJWTLifetime(bad); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestFetchAssertion(t *testing.T) {
	tok := testJWT(t, map[string]any{"iat": 1000, "exp": 1300})
	var gotAudience, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAudience = r.URL.Query().Get("audience")
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprintf(w, `{"value":%q}`, tok)
	}))
	defer srv.Close()

	a, err := FetchAssertion(context.Background(), AssertionConfig{
		Audience:         "gw-aud",
		OIDCRequestURL:   srv.URL + "/?api-version=2.0",
		OIDCRequestToken: "req-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAudience != "gw-aud" || gotAuth != "bearer req-token" {
		t.Fatalf("audience=%q auth=%q", gotAudience, gotAuth)
	}
	if a.Value != tok || a.Lifetime() != 300*time.Second {
		t.Fatalf("unexpected assertion lifetime %v", a.Lifetime())
	}
}

func TestFetchAssertion_Errors(t *testing.T) {
	notJWT := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"value":"secret-opaque"}`)
	}))
	defer notJWT.Close()

	cases := map[string]AssertionConfig{
		"no audience":  {OIDCRequestURL: notJWT.URL, OIDCRequestToken: "t"},
		"no url":       {Audience: "a", OIDCRequestToken: "t"},
		"no token":     {Audience: "a", OIDCRequestURL: notJWT.URL},
		"http remote":  {Audience: "a", OIDCRequestURL: "http://example.com", OIDCRequestToken: "t"},
		"foreign host": {Audience: "a", OIDCRequestURL: "https://example.com", OIDCRequestToken: "t"},
		"not a jwt":    {Audience: "a", OIDCRequestURL: notJWT.URL, OIDCRequestToken: "t"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := FetchAssertion(context.Background(), cfg)
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "secret-opaque") {
				t.Fatal("error leaks the assertion")
			}
		})
	}
}
