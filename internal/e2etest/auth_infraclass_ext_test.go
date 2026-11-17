package e2etest_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/failclass"
)

// These tests pin the failure-classification contract of the e2etest
// bootstrap token path (task 2.3 / design D3 of the behaviour-infra-retry
// change): a token mint or download failure — for example the download
// timeout #7491 documents — classifies as infrastructure, while the path's
// own configuration validation (an unconfigured mint URL) stays scenario.
// They live in this external e2etest_test package because failclass
// imports drivers/install, which imports internal/e2etest: an in-package
// test file cannot import failclass ("import cycle not allowed in test").
// The mark itself is additionally pinned in-package in auth_test.go;
// everything driven here is the package's exported TokenForOrg surface,
// the path the behaviour runners' bootstrap token minting goes through.

func TestInfraClassTokenDownloadTimeout(t *testing.T) {
	// The mint client's first phase downloads the GitHub Actions OIDC JWT
	// from ACTIONS_ID_TOKEN_REQUEST_URL; serve a valid response so the
	// failure lands on the second phase, downloading the installation
	// token from the mint service.
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":"test-oidc-jwt"}`))
	}))
	t.Cleanup(oidc.Close)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", oidc.URL)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "oidc-request-token")

	// The mint endpoint never answers: it holds the request until the
	// caller's deadline cancels it — the #7491-class download timeout. The
	// stub's 127.0.0.1 host passes the mint client's localhost exemption
	// from the HTTPS requirement, so the real MintToken call runs against
	// it unmodified. The body must be drained first: net/http defers the
	// background connection read (which cancels r.Context() when the
	// client's deadline tears the connection down) until the request body
	// reaches EOF, so an unread body would leave the handler blocked and
	// httptest.Server.Close hanging on it.
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	t.Cleanup(mint.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	_, err := e2etest.TokenForOrg(ctx, e2etest.EnvConfig{UseMint: true, MintURL: mint.URL}, "test-org")
	require.Error(t, err, "a download timeout must fail the token path")
	require.ErrorIs(t, err, context.DeadlineExceeded,
		"the drive must fail on the download timeout itself, not an earlier validation step")

	assert.Equal(t, behaviourfail.Infrastructure, failclass.Classify(err),
		"a token download timeout at bootstrap is an environment condition, not a scenario assertion")

	var infra *behaviourfail.InfraError
	require.ErrorAs(t, err, &infra)
	assert.Equal(t, "bootstrap: token mint or download", infra.Reason)
	assert.Equal(t, 1, strings.Count(err.Error(), "bootstrap: token mint or download"),
		"the mark is applied once, at resolveE2EToken's source; the dispatcher does not re-wrap")
}

func TestInfraClassMintURLNotConfiguredStaysScenario(t *testing.T) {
	// UseMint without a MintURL is the token path's own configuration
	// validation (bad env): a mint run whose FULLSEND_MINT_URL was never
	// provided. A retry cannot conjure the configuration, so the task
	// keeps this error unmarked and scenario-classified.
	_, err := e2etest.TokenForOrg(context.Background(), e2etest.EnvConfig{UseMint: true}, "test-org")
	require.Error(t, err, "an unconfigured mint URL must fail the token path")
	assert.Contains(t, err.Error(), "mint URL not configured",
		"the drive must fail on the config validation itself")

	assert.Equal(t, behaviourfail.Scenario, failclass.Classify(err),
		"an unconfigured mint URL is harness misconfiguration, not an environment condition")
	assert.False(t, behaviourfail.Is(err), "config-validation errors stay unmarked")
}

// driveMintStatus drives the real TokenForOrg mint path against an OIDC
// stub that answers successfully and a mint stub that answers with the
// given HTTP status, and returns the resulting error.
func driveMintStatus(t *testing.T, status int) error {
	t.Helper()
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":"test-oidc-jwt"}`))
	}))
	t.Cleanup(oidc.Close)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", oidc.URL)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "oidc-request-token")

	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(status)
	}))
	t.Cleanup(mint.Close)

	_, err := e2etest.TokenForOrg(context.Background(), e2etest.EnvConfig{UseMint: true, MintURL: mint.URL}, "test-org")
	return err
}

// TestInfraClassMintRateLimitMarkedInfra pins the mint path's transient
// boundary (PR #8801 review): an HTTP 429 from the mint service is a
// retryable environment condition and stays infrastructure-marked at
// the source.
func TestInfraClassMintRateLimitMarkedInfra(t *testing.T) {
	err := driveMintStatus(t, http.StatusTooManyRequests)
	require.Error(t, err, "a rate-limited mint must fail the token path")

	assert.Equal(t, behaviourfail.Infrastructure, failclass.Classify(err),
		"a mint rate limit at bootstrap is an environment condition, not a scenario assertion")

	var infra *behaviourfail.InfraError
	require.ErrorAs(t, err, &infra)
	assert.Equal(t, "bootstrap: token mint or download", infra.Reason)
	assert.Contains(t, err.Error(), "mint returned HTTP 429",
		"the surfaced error names the mint endpoint's status")
}

// TestInfraClassMintRejectionStaysScenario pins the mint path's
// transient boundary (PR #8801 review): a deterministic rejection such
// as a 403 not-enrolled is a product or configuration defect, so it
// stays unmarked and scenario-classified — a retry cannot clear it, and
// marking it would mask a mint-service regression.
func TestInfraClassMintRejectionStaysScenario(t *testing.T) {
	err := driveMintStatus(t, http.StatusForbidden)
	require.Error(t, err, "a rejected mint must fail the token path")

	assert.Equal(t, behaviourfail.Scenario, failclass.Classify(err),
		"a deterministic mint rejection is a product or configuration defect, not an environment condition")
	assert.False(t, behaviourfail.Is(err), "deterministic rejections are not marked")
	assert.Contains(t, err.Error(), "mint returned HTTP 403",
		"the surfaced error names the mint endpoint's status")
}
