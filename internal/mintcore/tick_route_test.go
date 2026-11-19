package mintcore

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	tickTestAudience = "https://fullsend-mint-abc123-uc.a.run.app"
	tickTestSA       = "fullsend-mint-tick@example-project.iam.gserviceaccount.com"
	tickTestKid      = "google-key-1"
)

// googleKeys is a fake Google JWKS endpoint: it serves the public half of
// key under tickTestKid through the mintHTTP test seam and counts fetches.
// Every other request goes to the network stack unchanged (the fake GitHub
// API is an httptest server).
type googleKeys struct {
	key     *rsa.PrivateKey
	fetches atomic.Int32
}

func newGoogleKeys(t *testing.T) *googleKeys {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	g := &googleKeys{key: key}
	SetMintHTTPForTest(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != googleOIDCJWKSURL {
			return http.DefaultTransport.RoundTrip(req)
		}
		g.fetches.Add(1)
		jwks := map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": tickTestKid,
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}}
		body, _ := json.Marshal(jwks)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body))), Request: req}, nil
	})
	return g
}

// googleClaims returns valid claims for the route's pinned settings at
// tickTestNow.
func googleClaims() map[string]any {
	return map[string]any{
		"iss":            googleOIDCIssuer,
		"aud":            tickTestAudience,
		"azp":            "1234567890",
		"sub":            "1234567890",
		"email":          tickTestSA,
		"email_verified": true,
		"iat":            tickTestNow.Add(-time.Minute).Unix(),
		"exp":            tickTestNow.Add(59 * time.Minute).Unix(),
	}
}

// signGoogleToken signs claims with key as an RS256 JWT under kid.
func signGoogleToken(t *testing.T, key *rsa.PrivateKey, alg, kid string, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": alg, "kid": kid, "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("signing: %v", err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// tickRouteEnv turns the route on with redelivery as the timer's only job.
func tickRouteEnv() map[string]string {
	return map[string]string{
		tickAudienceEnv:       tickTestAudience,
		tickServiceAccountEnv: tickTestSA,
		webhookSecretEnv:      webhookTestSecret,
		sweepEnv:              "off",
	}
}

func newTickRouteHandler(t *testing.T, env map[string]string) (*Handler, *tickGitHub) {
	t.Helper()
	gh := &tickGitHub{deliveryPages: map[string]deliveryPage{"": {}}}
	return newTickHandler(t, env, gh), gh
}

func postTick(h *Handler, bearer, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, tickPath, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTickRoute_OffUntilConfigured(t *testing.T) {
	keys := newGoogleKeys(t)
	token := signGoogleToken(t, keys.key, "RS256", tickTestKid, googleClaims())

	cases := map[string]func(env map[string]string){
		"no settings":           func(env map[string]string) { delete(env, tickAudienceEnv); delete(env, tickServiceAccountEnv) },
		"audience only":         func(env map[string]string) { delete(env, tickServiceAccountEnv) },
		"service account only":  func(env map[string]string) { delete(env, tickAudienceEnv) },
		"timer has no work":     func(env map[string]string) { delete(env, webhookSecretEnv) },
		"blank service account": func(env map[string]string) { env[tickServiceAccountEnv] = "  " },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			logs := captureWebhookLogs(t)
			env := tickRouteEnv()
			edit(env)
			h, gh := newTickRouteHandler(t, env)
			rec := postTick(h, token, "")
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
			if calls, _, _ := gh.snapshot(); len(calls) != 0 {
				t.Errorf("GitHub calls = %v, want none", calls)
			}
			if name == "audience only" && !strings.Contains(logs.String(), "must both be set") {
				t.Errorf("logs = %q, want the half-configured warning", logs.String())
			}
		})
	}
	if keys.fetches.Load() != 0 {
		t.Errorf("JWKS fetches = %d, want 0", keys.fetches.Load())
	}
}

func TestTickRoute_MethodNotAllowed(t *testing.T) {
	newGoogleKeys(t)
	h, _ := newTickRouteHandler(t, tickRouteEnv())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tickPath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestTickRoute_RunsTickWithValidToken(t *testing.T) {
	keys := newGoogleKeys(t)
	logs := captureWebhookLogs(t)
	h, gh := newTickRouteHandler(t, tickRouteEnv())
	token := signGoogleToken(t, keys.key, "RS256", tickTestKid, googleClaims())

	// The body is never read: junk does not matter.
	rec := postTick(h, token, `{"repos":["evil/repo"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	var res TickResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decoding summary %q: %v", rec.Body.String(), err)
	}
	if !res.Redelivery || res.Sweep {
		t.Errorf("summary = %+v, want redelivery on and sweep off", res)
	}
	if !strings.Contains(rec.Body.String(), `"deliveries_scanned":0`) {
		t.Errorf("summary %s lacks snake_case fields", rec.Body.String())
	}
	if calls, _, _ := gh.snapshot(); len(calls) != 1 || calls[0] != "GET /app/hook/deliveries" {
		t.Errorf("GitHub calls = %v, want one deliveries listing", calls)
	}
	if strings.Contains(logs.String(), token) || strings.Contains(rec.Body.String(), token) {
		t.Error("the ID token appears in the logs or the response")
	}

	// A second tick reuses the cached keys.
	if rec := postTick(h, token, ""); rec.Code != http.StatusOK {
		t.Fatalf("second tick status = %d", rec.Code)
	}
	if keys.fetches.Load() != 1 {
		t.Errorf("JWKS fetches = %d, want 1", keys.fetches.Load())
	}
}

func TestTickRoute_AcceptedClaimVariants(t *testing.T) {
	keys := newGoogleKeys(t)
	cases := map[string]func(c map[string]any){
		"bare issuer":                func(c map[string]any) { c["iss"] = googleOIDCIssuerBare },
		"email_verified as a string": func(c map[string]any) { c["email_verified"] = "true" },
		"expired within skew":        func(c map[string]any) { c["exp"] = tickTestNow.Add(-10 * time.Second).Unix() },
		"issued within skew":         func(c map[string]any) { c["iat"] = tickTestNow.Add(10 * time.Second).Unix() },
		"email in upper case":        func(c map[string]any) { c["email"] = strings.ToUpper(tickTestSA) },
		"audience as a list of one":  func(c map[string]any) { c["aud"] = []string{tickTestAudience} },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			h, _ := newTickRouteHandler(t, tickRouteEnv())
			claims := googleClaims()
			edit(claims)
			if rec := postTick(h, signGoogleToken(t, keys.key, "RS256", tickTestKid, claims), ""); rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestTickRoute_RejectsBadTokens(t *testing.T) {
	keys := newGoogleKeys(t)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	withClaims := func(edit func(c map[string]any)) string {
		c := googleClaims()
		edit(c)
		return signGoogleToken(t, keys.key, "RS256", tickTestKid, c)
	}
	valid := withClaims(func(map[string]any) {})
	parts := strings.Split(valid, ".")

	cases := map[string]struct {
		token string
		want  string
	}{
		"malformed":              {"not-a-jwt", "invalid JWT format"},
		"HS256":                  {signGoogleToken(t, keys.key, "HS256", tickTestKid, googleClaims()), "unsupported signing algorithm"},
		"no kid":                 {signGoogleToken(t, keys.key, "RS256", "", googleClaims()), "missing kid"},
		"claims not JSON":        {parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte("{")) + "." + parts[2], "parsing ID token claims"},
		"wrong issuer":           {withClaims(func(c map[string]any) { c["iss"] = "https://token.actions.githubusercontent.com" }), "unexpected issuer"},
		"no issuer":              {withClaims(func(c map[string]any) { delete(c, "iss") }), "unexpected issuer"},
		"no exp":                 {withClaims(func(c map[string]any) { delete(c, "exp") }), "missing exp"},
		"expired":                {withClaims(func(c map[string]any) { c["exp"] = tickTestNow.Add(-time.Minute).Unix() }), "token expired"},
		"no iat":                 {withClaims(func(c map[string]any) { delete(c, "iat") }), "missing iat"},
		"issued in the future":   {withClaims(func(c map[string]any) { c["iat"] = tickTestNow.Add(time.Minute).Unix() }), "issued in the future"},
		"wrong audience":         {withClaims(func(c map[string]any) { c["aud"] = "https://other.run.app" }), "audience mismatch"},
		"audience plus another":  {withClaims(func(c map[string]any) { c["aud"] = []string{tickTestAudience, "x"} }), "audience mismatch"},
		"no audience":            {withClaims(func(c map[string]any) { delete(c, "aud") }), "audience mismatch"},
		"wrong service account":  {withClaims(func(c map[string]any) { c["email"] = "attacker@example-project.iam.gserviceaccount.com" }), "service account mismatch"},
		"no email":               {withClaims(func(c map[string]any) { delete(c, "email") }), "service account mismatch"},
		"email not verified":     {withClaims(func(c map[string]any) { c["email_verified"] = false }), "email not verified"},
		"email_verified missing": {withClaims(func(c map[string]any) { delete(c, "email_verified") }), "email not verified"},
		"email_verified string":  {withClaims(func(c map[string]any) { c["email_verified"] = "false" }), "email not verified"},
		"signed by another key":  {signGoogleToken(t, other, "RS256", tickTestKid, googleClaims()), "invalid JWT signature"},
		"unknown kid":            {signGoogleToken(t, keys.key, "RS256", "other-kid", googleClaims()), "not found in JWKS"},
		"bad signature encoding": {parts[0] + "." + parts[1] + ".%%%", "decoding JWT signature"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			logs := captureWebhookLogs(t)
			h, gh := newTickRouteHandler(t, tickRouteEnv())
			rec := postTick(h, tc.token, "")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(logs.String(), tc.want) {
				t.Errorf("logs = %q, want %q", logs.String(), tc.want)
			}
			if strings.Contains(logs.String(), tc.token) {
				t.Error("the token appears in the logs")
			}
			if calls, _, _ := gh.snapshot(); len(calls) != 0 {
				t.Errorf("GitHub calls = %v, want none", calls)
			}
		})
	}
}

func TestTickRoute_MissingBearer(t *testing.T) {
	keys := newGoogleKeys(t)
	for name, header := range map[string]string{"none": "", "basic": "Basic Zm9vOmJhcg=="} {
		t.Run(name, func(t *testing.T) {
			logs := captureWebhookLogs(t)
			h, _ := newTickRouteHandler(t, tickRouteEnv())
			req := httptest.NewRequest(http.MethodPost, tickPath, nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if !strings.Contains(logs.String(), "missing bearer token") {
				t.Errorf("logs = %q", logs.String())
			}
		})
	}
	if keys.fetches.Load() != 0 {
		t.Errorf("JWKS fetches = %d, want 0", keys.fetches.Load())
	}
}

func TestJSONBool(t *testing.T) {
	for in, want := range map[string]bool{`true`: true, `"true"`: true, `false`: false, `"false"`: false, `1`: false, `null`: false} {
		var b jsonBool
		if err := json.Unmarshal([]byte(in), &b); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if bool(b) != want {
			t.Errorf("%s = %t, want %t", in, b, want)
		}
	}
}
