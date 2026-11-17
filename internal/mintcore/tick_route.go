package mintcore

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// Mint timer route (ADR 0171) for a host without an in-process timer: the
// Cloud Function. Cloud Scheduler calls POST /v1/tick every tick interval
// with a Google-signed OIDC ID token of a dedicated service account; the
// route checks the token in the handler and runs Tick. The standalone mint
// ticks in process and the Cloudflare Worker from a Cron Trigger, so
// neither sets the route's settings.
const (
	tickPath = "/v1/tick"

	// tickAudienceEnv is the audience the ID token must carry: the
	// function URL that `mint deploy` also gives the Cloud Scheduler job.
	tickAudienceEnv = "FULLSEND_TICK_AUDIENCE"

	// tickServiceAccountEnv is the email of the service account the ID
	// token must be issued to (the Cloud Scheduler job's oidcToken).
	tickServiceAccountEnv = "FULLSEND_TICK_SERVICE_ACCOUNT"

	// googleOIDCJWKSURL is Google's ID token signing keys. It is fixed,
	// not discovered, so no request input can change where keys come from:
	// https://developers.google.com/identity/openid-connect/openid-connect#validatinganidtoken
	googleOIDCJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"

	// Google signs ID tokens with either issuer spelling.
	googleOIDCIssuer     = "https://accounts.google.com"
	googleOIDCIssuerBare = "accounts.google.com"
)

// googleIDTokenClaims is the subset of a Google ID token the route checks.
type googleIDTokenClaims struct {
	Issuer        string   `json:"iss"`
	Audience      Audience `json:"aud"`
	IssuedAt      int64    `json:"iat"`
	Expiry        int64    `json:"exp"`
	Email         string   `json:"email"`
	EmailVerified jsonBool `json:"email_verified"`
}

// jsonBool decodes a JSON true or "true" as true and anything else as
// false. Google ID tokens carry email_verified as a boolean; some older
// tokens carried the string.
type jsonBool bool

func (b *jsonBool) UnmarshalJSON(data []byte) error {
	*b = jsonBool(string(data) == "true" || string(data) == `"true"`)
	return nil
}

// newGoogleIDTokenVerifier returns a JWKSVerifier that only fetches and
// caches Google's ID token keys. Its jwks_uri is preset, so it never runs
// OIDC discovery, and its Verify (GitHub Actions claims) is never called:
// the route checks Google's claims in verifyTickToken and reuses the
// verifier's key cache and RS256 check (verifyRS256Signature).
func newGoogleIDTokenVerifier() *JWKSVerifier {
	return &JWKSVerifier{issuerURL: googleOIDCIssuer, cachedJWKSURI: googleOIDCJWKSURL}
}

// readTickRouteSettings turns the route on when both settings are set. One
// without the other is logged and leaves the route off.
func (h *Handler) readTickRouteSettings() {
	aud := strings.TrimSpace(mintEnv(tickAudienceEnv))
	sa := strings.TrimSpace(mintEnv(tickServiceAccountEnv))
	switch {
	case aud != "" && sa != "":
		h.tickAudience = aud
		h.tickServiceAccount = strings.ToLower(sa)
		h.tickVerifier = newGoogleIDTokenVerifier()
	case aud != "" || sa != "":
		log.Printf("mint tick route: %s and %s must both be set; %s is off", tickAudienceEnv, tickServiceAccountEnv, tickPath)
	}
}

// tickRouteEnabled reports whether POST /v1/tick is served: both route
// settings are set and the mint timer has work. Otherwise the path answers
// 404 like an unknown path.
func (h *Handler) tickRouteEnabled() bool {
	return h.tickVerifier != nil && h.TickEnabled()
}

// handleTickRoute serves POST /v1/tick. It takes no input: the body is
// never read, and the only header used is Authorization, which is never
// logged.
func (h *Handler) handleTickRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		log.Printf("mint tick route: rejected: missing bearer token")
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := h.verifyTickToken(r.Context(), strings.TrimPrefix(authHeader, "Bearer ")); err != nil {
		log.Printf("mint tick route: rejected: %q", err.Error())
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// One tick may run for at most one interval, as on the standalone
	// mint; the function timeout set by `mint deploy` is longer.
	ctx, cancel := context.WithTimeout(r.Context(), h.tickInterval)
	defer cancel()
	res := h.Tick(ctx)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(res)
}

// verifyTickToken checks a Google-signed OIDC ID token: issuer, expiry and
// issue time (with maxClockSkew), the pinned audience, the pinned service
// account email with email_verified, then the RS256 signature against
// Google's keys. Errors never include the token or a claim value.
func (h *Handler) verifyTickToken(ctx context.Context, raw string) error {
	parts, header, claimsJSON, err := splitRS256JWT(raw)
	if err != nil {
		return err
	}
	var claims googleIDTokenClaims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return fmt.Errorf("parsing ID token claims: %w", err)
	}
	if claims.Issuer != googleOIDCIssuer && claims.Issuer != googleOIDCIssuerBare {
		return fmt.Errorf("unexpected issuer")
	}

	now := h.tickNow().Unix()
	skew := int64(maxClockSkew.Seconds())
	if claims.Expiry == 0 {
		return fmt.Errorf("missing exp claim")
	}
	if claims.Expiry <= now-skew {
		return fmt.Errorf("token expired")
	}
	if claims.IssuedAt == 0 {
		return fmt.Errorf("missing iat claim")
	}
	if claims.IssuedAt > now+skew {
		return fmt.Errorf("token issued in the future")
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != h.tickAudience {
		return fmt.Errorf("audience mismatch")
	}
	if !strings.EqualFold(claims.Email, h.tickServiceAccount) {
		return fmt.Errorf("service account mismatch")
	}
	if !claims.EmailVerified {
		return fmt.Errorf("email not verified")
	}
	return h.tickVerifier.verifyRS256Signature(ctx, header.Kid, parts)
}
