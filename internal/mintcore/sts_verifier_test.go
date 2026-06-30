package mintcore

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeUnsignedJWT(t *testing.T, claims map[string]interface{}) string {
	t.Helper()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(claims)
	hB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	cB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
	return hB64 + "." + cB64 + ".fakesig"
}

func validClaims() map[string]interface{} {
	now := time.Now()
	return map[string]interface{}{
		"iss":              defaultGitHubOIDCIssuer,
		"aud":              "fullsend-mint",
		"iat":              now.Unix(),
		"exp":              now.Add(10 * time.Minute).Unix(),
		"repository":       "myorg/my-repo",
		"repository_owner": "myorg",
		"job_workflow_ref": "myorg/.fullsend/.github/workflows/dispatch.yml@refs/heads/main",
	}
}

func newTestSTSServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stsResponse{
			AccessToken: "ya29.test-access-token",
			TokenType:   "Bearer",
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestSTSVerifier(t *testing.T, stsURL string) *STSVerifier {
	t.Helper()
	return NewSTSVerifier(STSVerifierConfig{
		STSURL:             stsURL,
		GCPProjectNum:      "123456",
		WIFPoolName:        "fullsend-pool",
		DefaultWIFProvider: "fullsend-provider",
		AllowedOrgs:        []string{"myorg"},
		AllowedWorkflows:   []string{"*"},
		OIDCAudience:       "fullsend-mint",
	})
}

func TestSTSVerifier_ValidToken(t *testing.T) {
	sts := newTestSTSServer(t)
	v := newTestSTSVerifier(t, sts.URL)

	token := makeUnsignedJWT(t, validClaims())
	claims, err := v.Verify(t.Context(), token)
	require.NoError(t, err)
	assert.Equal(t, "myorg", claims.RepositoryOwner)
	assert.Equal(t, "myorg/my-repo", claims.Repository)
}

func TestSTSVerifier_InvalidFormat(t *testing.T) {
	sts := newTestSTSServer(t)
	v := newTestSTSVerifier(t, sts.URL)

	_, err := v.Verify(t.Context(), "not-a-jwt")
	assert.ErrorContains(t, err, "expected 3 segments")
}

func TestSTSVerifier_WrongIssuer(t *testing.T) {
	sts := newTestSTSServer(t)
	v := newTestSTSVerifier(t, sts.URL)

	c := validClaims()
	c["iss"] = "https://evil.com"
	token := makeUnsignedJWT(t, c)
	_, err := v.Verify(t.Context(), token)
	assert.ErrorContains(t, err, "unexpected issuer")
}

func TestSTSVerifier_WrongAudience(t *testing.T) {
	sts := newTestSTSServer(t)
	v := newTestSTSVerifier(t, sts.URL)

	c := validClaims()
	c["aud"] = "wrong"
	token := makeUnsignedJWT(t, c)
	_, err := v.Verify(t.Context(), token)
	assert.ErrorContains(t, err, "audience mismatch")
}

func TestSTSVerifier_ExpiredToken(t *testing.T) {
	sts := newTestSTSServer(t)
	v := newTestSTSVerifier(t, sts.URL)

	c := validClaims()
	past := time.Now().Add(-10 * time.Minute).Unix()
	c["iat"] = past - 600
	c["exp"] = past
	token := makeUnsignedJWT(t, c)
	_, err := v.Verify(t.Context(), token)
	assert.ErrorContains(t, err, "token expired")
}

func TestSTSVerifier_BadOrg(t *testing.T) {
	sts := newTestSTSServer(t)
	v := newTestSTSVerifier(t, sts.URL)

	c := validClaims()
	c["repository_owner"] = "evilorg"
	token := makeUnsignedJWT(t, c)
	_, err := v.Verify(t.Context(), token)
	assert.ErrorContains(t, err, "not in allowed orgs")
}

func TestSTSVerifier_BadWorkflowRef(t *testing.T) {
	sts := newTestSTSServer(t)
	v := NewSTSVerifier(STSVerifierConfig{
		STSURL:             sts.URL,
		GCPProjectNum:      "123456",
		WIFPoolName:        "fullsend-pool",
		DefaultWIFProvider: "fullsend-provider",
		AllowedOrgs:        []string{"myorg"},
		AllowedWorkflows:   []string{"dispatch.yml"},
		OIDCAudience:       "fullsend-mint",
	})

	c := validClaims()
	c["job_workflow_ref"] = "myorg/.fullsend/.github/workflows/evil.yml@refs/heads/main"
	token := makeUnsignedJWT(t, c)
	_, err := v.Verify(t.Context(), token)
	assert.ErrorContains(t, err, "not in allowed list")
}

func TestSTSVerifier_STSFailure(t *testing.T) {
	failSTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer failSTS.Close()

	v := newTestSTSVerifier(t, failSTS.URL)
	token := makeUnsignedJWT(t, validClaims())
	_, err := v.Verify(t.Context(), token)
	assert.ErrorContains(t, err, "STS returned status 403")
}

func TestSTSVerifier_STSEmptyToken(t *testing.T) {
	emptySTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(stsResponse{})
	}))
	defer emptySTS.Close()

	v := newTestSTSVerifier(t, emptySTS.URL)
	token := makeUnsignedJWT(t, validClaims())
	_, err := v.Verify(t.Context(), token)
	assert.ErrorContains(t, err, "STS returned empty access token")
}

func TestSTSVerifier_ResolveWIFProvider(t *testing.T) {
	v := NewSTSVerifier(STSVerifierConfig{
		DefaultWIFProvider: "default-provider",
		PerRepoWIFRepos:    map[string]bool{"myorg/special-repo": true},
	})

	assert.Equal(t, "default-provider", v.resolveWIFProvider("myorg/.fullsend"))
	assert.Equal(t, "default-provider", v.resolveWIFProvider("myorg/regular-repo"))
	assert.Equal(t, "gh-myorg-special-repo", v.resolveWIFProvider("myorg/special-repo"))
}

func TestSTSVerifier_STSRequestFormat(t *testing.T) {
	var capturedAud string
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		capturedAud = r.FormValue("audience")
		json.NewEncoder(w).Encode(stsResponse{AccessToken: "tok"})
	}))
	defer sts.Close()

	v := newTestSTSVerifier(t, sts.URL)
	token := makeUnsignedJWT(t, validClaims())
	_, err := v.Verify(t.Context(), token)
	require.NoError(t, err)

	expected := fmt.Sprintf("//iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
		"123456", "fullsend-pool", "fullsend-provider")
	assert.Equal(t, expected, capturedAud)
}
