package openaiwif

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestServers returns a GitHub OIDC httptest server, an OpenAI token
// httptest server, and a Config wired to both. The caller must close the
// servers when done.
func newTestServers(t *testing.T, oidcHandler, tokenHandler http.HandlerFunc) (*httptest.Server, *httptest.Server, Config) {
	t.Helper()
	oidcServer := httptest.NewServer(oidcHandler)
	tokenServer := httptest.NewServer(tokenHandler)
	t.Cleanup(func() {
		oidcServer.Close()
		tokenServer.Close()
	})
	return oidcServer, tokenServer, Config{
		Audience:           "https://auth.openai.com",
		IdentityProviderID: "idp-123",
		ServiceAccountID:   "sa-456",
		OIDCRequestURL:     oidcServer.URL + "?dummy=1",
		OIDCRequestToken:   "ghs_runner_token",
		TokenEndpoint:      tokenServer.URL,
	}
}

func TestExchange_HappyPath(t *testing.T) {
	t.Parallel()
	const fakeJWT = "eyJ.test.jwt"
	const fakeAccessToken = "opaque-access-token-xyz"
	const expiresIn = 3600

	_, _, cfg := newTestServers(t,
		func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "bearer ghs_runner_token", r.Header.Get("Authorization"))
			// The audience must be URL-encoded in the query parameter.
			assert.Equal(t, "https://auth.openai.com", r.URL.Query().Get("audience"))
			json.NewEncoder(w).Encode(oidcResponse{Value: fakeJWT}) //nolint:errcheck
		},
		func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			var body map[string]string
			// assert, not require: FailNow must not run on the handler goroutine.
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				return
			}
			assert.Equal(t, "urn:ietf:params:oauth:grant-type:token-exchange", body["grant_type"])
			assert.Equal(t, defaultSubjectTokenType, body["subject_token_type"])
			assert.Equal(t, fakeJWT, body["subject_token"])
			assert.Equal(t, "idp-123", body["identity_provider_id"])
			assert.Equal(t, "sa-456", body["service_account_id"])
			json.NewEncoder(w).Encode(tokenResponse{ //nolint:errcheck
				AccessToken: fakeAccessToken,
				TokenType:   "bearer",
				ExpiresIn:   expiresIn,
			})
		},
	)

	before := time.Now()
	tok, err := Exchange(context.Background(), cfg)
	after := time.Now()

	require.NoError(t, err)
	assert.Equal(t, fakeAccessToken, tok.Value)
	// ExpiresAt should be approximately now + expiresIn.
	assert.True(t, tok.ExpiresAt.After(before.Add(time.Duration(expiresIn-1)*time.Second)),
		"ExpiresAt %v too early", tok.ExpiresAt)
	assert.True(t, tok.ExpiresAt.Before(after.Add(time.Duration(expiresIn+1)*time.Second)),
		"ExpiresAt %v too late", tok.ExpiresAt)
}

func TestExchange_AudienceURLEncoding(t *testing.T) {
	t.Parallel()
	const audienceWithSpecialChars = "https://auth.openai.com/special?param=value&other=1"
	var receivedAudience string

	_, _, cfg := newTestServers(t,
		func(w http.ResponseWriter, r *http.Request) {
			receivedAudience = r.URL.Query().Get("audience")
			json.NewEncoder(w).Encode(oidcResponse{Value: "jwt"}) //nolint:errcheck
		},
		func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(tokenResponse{ //nolint:errcheck
				AccessToken: "tok",
				TokenType:   "bearer",
				ExpiresIn:   3600,
			})
		},
	)
	cfg.Audience = audienceWithSpecialChars

	tok, err := Exchange(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, "tok", tok.Value)
	// The audience should survive URL encoding/decoding round-trip.
	assert.Equal(t, audienceWithSpecialChars, receivedAudience,
		"audience must be URL-encoded so the OIDC endpoint receives it intact")
}

func TestExchange_OIDCEndpointNon200(t *testing.T) {
	t.Parallel()
	for _, status := range []int{401, 403, 500, 502} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			t.Parallel()
			_, _, cfg := newTestServers(t,
				func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(status)
					w.Write([]byte(`{"error":"nope"}`)) //nolint:errcheck
				},
				func(w http.ResponseWriter, _ *http.Request) {
					t.Fatal("token endpoint should not be called")
				},
			)

			tok, err := Exchange(context.Background(), cfg)
			assert.Nil(t, tok)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "assertion request failed")
			assert.Contains(t, err.Error(), fmt.Sprintf("OIDC endpoint returned %d", status))
			// Error must not contain the runner token.
			assert.NotContains(t, err.Error(), cfg.OIDCRequestToken)
		})
	}
}

func TestExchange_TokenEndpointNon200(t *testing.T) {
	t.Parallel()
	for _, status := range []int{400, 401, 500} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			t.Parallel()
			_, _, cfg := newTestServers(t,
				func(w http.ResponseWriter, _ *http.Request) {
					json.NewEncoder(w).Encode(oidcResponse{Value: "jwt"}) //nolint:errcheck
				},
				func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(status)
					w.Write([]byte(`{"error":"invalid_grant"}`)) //nolint:errcheck
				},
			)

			tok, err := Exchange(context.Background(), cfg)
			assert.Nil(t, tok)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "token exchange failed")
			assert.Contains(t, err.Error(), fmt.Sprintf("token endpoint returned %d", status))
			// Error must not contain the assertion.
			assert.NotContains(t, err.Error(), "jwt")
		})
	}
}

func TestExchange_OversizedOIDCResponse(t *testing.T) {
	t.Parallel()
	_, _, cfg := newTestServers(t,
		func(w http.ResponseWriter, _ *http.Request) {
			// Return a valid JSON wrapper but with a huge value field.
			w.Write([]byte(`{"value":"`))                              //nolint:errcheck
			w.Write([]byte(strings.Repeat("A", maxResponseBytes+100))) //nolint:errcheck
			w.Write([]byte(`"}`))                                      //nolint:errcheck
		},
		func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("token endpoint should not be called")
		},
	)

	tok, err := Exchange(context.Background(), cfg)
	assert.Nil(t, tok)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "assertion request failed")
}

func TestExchange_OversizedTokenResponse(t *testing.T) {
	t.Parallel()
	_, _, cfg := newTestServers(t,
		func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(oidcResponse{Value: "jwt"}) //nolint:errcheck
		},
		func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"access_token":"`))                          //nolint:errcheck
			w.Write([]byte(strings.Repeat("B", maxResponseBytes+100)))    //nolint:errcheck
			w.Write([]byte(`","token_type":"bearer","expires_in":3600}`)) //nolint:errcheck
		},
	)

	tok, err := Exchange(context.Background(), cfg)
	assert.Nil(t, tok)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token exchange failed")
}

func TestExchange_NonJSONOIDCResponse(t *testing.T) {
	t.Parallel()
	_, _, cfg := newTestServers(t,
		func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte("this is not json")) //nolint:errcheck
		},
		func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("token endpoint should not be called")
		},
	)

	tok, err := Exchange(context.Background(), cfg)
	assert.Nil(t, tok)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "assertion request failed")
	assert.Contains(t, err.Error(), "parsing response")
}

func TestExchange_NonJSONTokenResponse(t *testing.T) {
	t.Parallel()
	_, _, cfg := newTestServers(t,
		func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(oidcResponse{Value: "jwt"}) //nolint:errcheck
		},
		func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte("not json either")) //nolint:errcheck
		},
	)

	tok, err := Exchange(context.Background(), cfg)
	assert.Nil(t, tok)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token exchange failed")
	assert.Contains(t, err.Error(), "parsing response")
}

func TestExchange_Timeout(t *testing.T) {
	t.Parallel()
	_, _, cfg := newTestServers(t,
		func(w http.ResponseWriter, _ *http.Request) {
			// Block until context is cancelled.
			time.Sleep(5 * time.Second)
		},
		func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("token endpoint should not be called")
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	tok, err := Exchange(ctx, cfg)
	assert.Nil(t, tok)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "assertion request failed")
}

func TestExchange_EmptyAccessToken(t *testing.T) {
	t.Parallel()
	_, _, cfg := newTestServers(t,
		func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(oidcResponse{Value: "jwt"}) //nolint:errcheck
		},
		func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(tokenResponse{ //nolint:errcheck
				AccessToken: "",
				TokenType:   "bearer",
				ExpiresIn:   3600,
			})
		},
	)

	tok, err := Exchange(context.Background(), cfg)
	assert.Nil(t, tok)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty access_token")
}

func TestExchange_InvalidExpiresIn(t *testing.T) {
	t.Parallel()
	for _, expiresIn := range []int{0, -1, -3600} {
		t.Run(fmt.Sprintf("expires_in_%d", expiresIn), func(t *testing.T) {
			t.Parallel()
			_, _, cfg := newTestServers(t,
				func(w http.ResponseWriter, _ *http.Request) {
					json.NewEncoder(w).Encode(oidcResponse{Value: "jwt"}) //nolint:errcheck
				},
				func(w http.ResponseWriter, _ *http.Request) {
					json.NewEncoder(w).Encode(tokenResponse{ //nolint:errcheck
						AccessToken: "tok",
						TokenType:   "bearer",
						ExpiresIn:   expiresIn,
					})
				},
			)

			tok, err := Exchange(context.Background(), cfg)
			assert.Nil(t, tok)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid expires_in")
		})
	}
}

func TestExchange_EmptyOIDCAssertion(t *testing.T) {
	t.Parallel()
	_, _, cfg := newTestServers(t,
		func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(oidcResponse{Value: ""}) //nolint:errcheck
		},
		func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("token endpoint should not be called")
		},
	)

	tok, err := Exchange(context.Background(), cfg)
	assert.Nil(t, tok)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty assertion")
}

func TestExchange_MissingConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"missing_audience", Config{IdentityProviderID: "a", ServiceAccountID: "b", OIDCRequestURL: "u", OIDCRequestToken: "t"}, "audience is required"},
		{"missing_identity_provider_id", Config{Audience: "a", ServiceAccountID: "b", OIDCRequestURL: "u", OIDCRequestToken: "t"}, "identity_provider_id is required"},
		{"missing_service_account_id", Config{Audience: "a", IdentityProviderID: "b", OIDCRequestURL: "u", OIDCRequestToken: "t"}, "service_account_id is required"},
		{"missing_oidc_url", Config{Audience: "a", IdentityProviderID: "b", ServiceAccountID: "c", OIDCRequestToken: "t"}, "ACTIONS_ID_TOKEN_REQUEST_URL is required"},
		{"missing_oidc_token", Config{Audience: "a", IdentityProviderID: "b", ServiceAccountID: "c", OIDCRequestURL: "u"}, "ACTIONS_ID_TOKEN_REQUEST_TOKEN is required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tok, err := Exchange(context.Background(), tc.cfg)
			assert.Nil(t, tok)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestExchange_RejectsNonBearerTokenType(t *testing.T) {
	t.Parallel()
	for _, tokenType := range []string{"", "MAC", "DPoP"} {
		t.Run("token_type="+tokenType, func(t *testing.T) {
			_, _, cfg := newTestServers(t,
				func(w http.ResponseWriter, _ *http.Request) {
					json.NewEncoder(w).Encode(oidcResponse{Value: "jwt"}) //nolint:errcheck
				},
				func(w http.ResponseWriter, _ *http.Request) {
					json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok", TokenType: tokenType, ExpiresIn: 3600}) //nolint:errcheck
				},
			)
			_, err := Exchange(context.Background(), cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "token_type")
			assert.NotContains(t, err.Error(), "tok\"", "the token value is not quoted")
		})
	}
	// Case-insensitive per RFC 6749 §5.1.
	_, _, cfg := newTestServers(t,
		func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(oidcResponse{Value: "jwt"}) //nolint:errcheck
		},
		func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok", TokenType: "BEARER", ExpiresIn: 3600}) //nolint:errcheck
		},
	)
	tok, err := Exchange(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, "tok", tok.Value)
}

func TestExchange_CustomSubjectTokenType(t *testing.T) {
	t.Parallel()
	const customType = "urn:ietf:params:oauth:token-type:id_token"
	var receivedType string

	_, _, cfg := newTestServers(t,
		func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(oidcResponse{Value: "jwt"}) //nolint:errcheck
		},
		func(w http.ResponseWriter, r *http.Request) {
			var body map[string]string
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				return
			}
			receivedType = body["subject_token_type"]
			json.NewEncoder(w).Encode(tokenResponse{ //nolint:errcheck
				AccessToken: "tok",
				TokenType:   "bearer",
				ExpiresIn:   3600,
			})
		},
	)
	cfg.SubjectTokenType = customType

	tok, err := Exchange(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, "tok", tok.Value)
	assert.Equal(t, customType, receivedType)
}

func TestExchange_ErrorsNeverContainSecrets(t *testing.T) {
	t.Parallel()
	const secretJWT = "eyJhbGciOiJSUzI1NiJ9.secret.payload"
	const secretToken = "opaque-secret-access-token-value"

	// Test 1: OIDC succeeds, token exchange fails — error must not
	// contain the assertion JWT.
	_, _, cfg := newTestServers(t,
		func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(oidcResponse{Value: secretJWT}) //nolint:errcheck
		},
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid"}`)) //nolint:errcheck
		},
	)
	_, err := Exchange(context.Background(), cfg)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secretJWT,
		"error must not contain the assertion JWT")
	assert.NotContains(t, err.Error(), cfg.OIDCRequestToken,
		"error must not contain the OIDC request token")

	// Test 1b: the token endpoint fails but echoes a token in its error
	// body — the body must never be quoted into the error.
	_, _, cfg = newTestServers(t,
		func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(oidcResponse{Value: secretJWT}) //nolint:errcheck
		},
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"upstream","echo":"` + secretToken + `"}`)) //nolint:errcheck
		},
	)
	_, err = Exchange(context.Background(), cfg)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secretToken,
		"error must not quote the token endpoint's response body")
	assert.Contains(t, err.Error(), "500", "the status code is still reported")

	// Test 2: Build a URL where the audience appears in query params.
	// Verify the error from a connection failure doesn't leak it either.
	cfg2 := Config{
		Audience:           "secret-audience-value",
		IdentityProviderID: "idp",
		ServiceAccountID:   "sa",
		OIDCRequestURL:     "http://127.0.0.1:1/?x=1",
		OIDCRequestToken:   "secret-runner-token",
	}
	_, err = Exchange(context.Background(), cfg2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "assertion request failed")
	// The audience is not a secret and Go's url.Error carries the URL, but
	// the bearer runner token is a header and must never surface.
	assert.NotContains(t, err.Error(), "secret-runner-token")
}

func TestExchange_RequiresHTTPS(t *testing.T) {
	t.Parallel()
	_, _, cfg := newTestServers(t,
		func(w http.ResponseWriter, _ *http.Request) { json.NewEncoder(w).Encode(oidcResponse{Value: "jwt"}) }, //nolint:errcheck
		func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok", TokenType: "Bearer", ExpiresIn: 3600}) //nolint:errcheck
		},
	)
	// httptest servers are plain http on loopback and are accepted.
	_, err := Exchange(context.Background(), cfg)
	require.NoError(t, err)

	bad := cfg
	bad.OIDCRequestURL = "http://oidc.example.invalid/token"
	_, err = Exchange(context.Background(), bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must use https")

	bad = cfg
	bad.OIDCRequestURL = "https://oidc.example.invalid/token"
	_, err = Exchange(context.Background(), bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not GitHub's Actions token service")

	bad = cfg
	bad.TokenEndpoint = "http://auth.example.invalid/oauth/token"
	_, err = Exchange(context.Background(), bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token endpoint must use https")
}

func TestExchange_DoesNotFollowRedirects(t *testing.T) {
	t.Parallel()
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/elsewhere", http.StatusFound)
	}))
	t.Cleanup(oidc.Close)
	cfg := Config{
		Audience:           "aud",
		IdentityProviderID: "idp",
		ServiceAccountID:   "sa",
		OIDCRequestURL:     oidc.URL + "/token?api-version=2",
		OIDCRequestToken:   "runner-token",
		TokenEndpoint:      "https://auth.example.invalid/oauth/token",
	}
	_, err := Exchange(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OIDC endpoint returned 302", "the redirect is reported, not followed")
}

func TestExchange_CapsLifetimeAndRejectsWhitespace(t *testing.T) {
	t.Parallel()
	_, _, cfg := newTestServers(t,
		func(w http.ResponseWriter, _ *http.Request) { json.NewEncoder(w).Encode(oidcResponse{Value: "jwt"}) }, //nolint:errcheck
		func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok", TokenType: "Bearer", ExpiresIn: 86400}) //nolint:errcheck
		},
	)
	before := time.Now()
	tok, err := Exchange(context.Background(), cfg)
	require.NoError(t, err)
	assert.WithinDuration(t, before.Add(time.Hour), tok.ExpiresAt, 10*time.Second, "a claimed lifetime beyond an hour is capped")

	_, _, cfg = newTestServers(t,
		func(w http.ResponseWriter, _ *http.Request) { json.NewEncoder(w).Encode(oidcResponse{Value: "jwt"}) }, //nolint:errcheck
		func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok en", TokenType: "Bearer", ExpiresIn: 300}) //nolint:errcheck
		},
	)
	_, err = Exchange(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "whitespace")
	assert.NotContains(t, err.Error(), "tok en")
}
