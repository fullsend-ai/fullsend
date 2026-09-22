package gitlab

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// systemPoolCACert and systemPoolCAKey are a synthetic CA primed into the
// process-wide x509.SystemCertPool() cache by TestMain before any test runs
// (see TestMain). crypto/x509 caches the system pool behind a sync.Once for
// the lifetime of the process, so priming it here — rather than trying to
// set SSL_CERT_FILE from within an individual test — is what makes
// TestApplyCIServerTLSCA_TrustsPrivateCAAndRejectsUntrusted's "system pool
// CA survives the private-CA append" assertion deterministic regardless of
// test execution order.
var (
	systemPoolCACert *x509.Certificate
	systemPoolCAKey  *ecdsa.PrivateKey
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fullsend-gitlab-systemca")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fullsend-test-system-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	systemPoolCACert = cert
	systemPoolCAKey = key

	path := filepath.Join(dir, "system-ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		panic(err)
	}
	if err := os.Setenv("SSL_CERT_FILE", path); err != nil {
		panic(err)
	}
	// Force crypto/x509's system-pool sync.Once to fire now, while
	// SSL_CERT_FILE points at our synthetic CA, so applyCIServerTLSCA's
	// later calls to x509.SystemCertPool() (from any test) observe it as
	// already part of the "system" pool.
	if _, err := x509.SystemCertPool(); err != nil {
		panic(err)
	}

	os.Exit(m.Run())
}

func writePEMCert(t *testing.T, dir, name string, cert *x509.Certificate) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	}), 0o644))
	return path
}

func tlsClientConfig(t *testing.T, client *http.Client) *tls.Config {
	t.Helper()
	rt := client.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	tr, ok := rt.(*http.Transport)
	require.True(t, ok, "expected *http.Transport, got %T", rt)
	require.NotNil(t, tr.TLSClientConfig)
	return tr.TLSClientConfig
}

func TestApplyCIServerTLSCA_UnsetIsNoop(t *testing.T) {
	t.Setenv(ciServerTLSCAFileEnv, "")
	client := &http.Client{}
	require.NoError(t, applyCIServerTLSCA(client))
	assert.Nil(t, client.Transport, "unset CA must not install a custom transport")
}

func TestApplyCIServerTLSCA_MissingFile(t *testing.T) {
	t.Setenv(ciServerTLSCAFileEnv, filepath.Join(t.TempDir(), "missing.pem"))
	err := applyCIServerTLSCA(&http.Client{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), ciServerTLSCAFileEnv)
	assert.Contains(t, err.Error(), "no such file or directory")
	assert.NotContains(t, err.Error(), "InsecureSkipVerify")
}

func TestApplyCIServerTLSCA_InvalidPEM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-cert.pem")
	require.NoError(t, os.WriteFile(path, []byte("this is not a certificate\n"), 0o644))
	t.Setenv(ciServerTLSCAFileEnv, path)
	err := applyCIServerTLSCA(&http.Client{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a PEM certificate bundle")
	assert.Contains(t, err.Error(), "TLS verification is not disabled")
}

func TestApplyCIServerTLSCA_EmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.pem")
	require.NoError(t, os.WriteFile(path, []byte{}, 0o644))
	t.Setenv(ciServerTLSCAFileEnv, path)
	err := applyCIServerTLSCA(&http.Client{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a PEM certificate bundle")
}

func TestApplyCIServerTLSCA_NilClient(t *testing.T) {
	t.Setenv(ciServerTLSCAFileEnv, filepath.Join(t.TempDir(), "x.pem"))
	err := applyCIServerTLSCA(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http client is nil")
}

func startUniqueTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{der},
			PrivateKey:  key,
			Leaf:        cert,
		}},
		MinVersion: tls.VersionTLS12,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// startCASignedTLSServer starts an httptest TLS server presenting a leaf
// certificate signed by the given CA, rather than a self-signed cert. Used
// to prove that a CA already present in the system pool (see
// systemPoolCACert / TestMain) is still honored after applyCIServerTLSCA
// appends a private CA — a self-signed cert can't distinguish "the pool has
// this exact cert" from "the pool has the CA that issued this cert".
func startCASignedTLSServer(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, handler http.Handler) *httptest.Server {
	t.Helper()
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &leafKey.PublicKey, caKey)
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{der},
			PrivateKey:  leafKey,
		}},
		MinVersion: tls.VersionTLS12,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func TestApplyCIServerTLSCA_TrustsPrivateCAAndRejectsUntrusted(t *testing.T) {
	trusted := startUniqueTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id": 1}`)
	}))

	untrusted := startUniqueTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id": 2}`)
	}))

	// Chains to systemPoolCACert, which TestMain primed into the system
	// pool before applyCIServerTLSCA ever ran. If a future regression
	// turned the private-CA append into a replace, this handshake — unlike
	// the two above, which only exercise the newly-appended private CA —
	// would start failing.
	viaSystemPool := startCASignedTLSServer(t, systemPoolCACert, systemPoolCAKey, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id": 3}`)
	}))

	caPath := writePEMCert(t, t.TempDir(), "ci-server-ca.pem", trusted.Certificate())
	t.Setenv(ciServerTLSCAFileEnv, caPath)

	client, err := New("test-token", WithBaseURL(trusted.URL), WithAfterFunc(noWaitAfter))
	require.NoError(t, err)

	tlsCfg := tlsClientConfig(t, client.http)
	assert.False(t, tlsCfg.InsecureSkipVerify, "must not disable TLS verification")
	assert.NotNil(t, tlsCfg.RootCAs)
	assert.GreaterOrEqual(t, tlsCfg.MinVersion, uint16(tls.VersionTLS12))

	resp, err := client.http.Get(trusted.URL + "/user")
	require.NoError(t, err, "private-CA GitLab endpoint must succeed")
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"id": 1`)

	_, err = client.http.Get(untrusted.URL + "/user")
	require.Error(t, err, "certificate not signed by the supplied CA must be rejected")
	assert.Contains(t, err.Error(), "certificate")

	resp3, err := client.http.Get(viaSystemPool.URL + "/user")
	require.NoError(t, err, "a cert chaining to a pre-existing system-pool CA must remain trusted after the private CA append")
	defer resp3.Body.Close()
	body3, err := io.ReadAll(resp3.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body3), `"id": 3`)
}

func TestApplyCIServerTLSCA_UnsetRejectsUnknownAuthority(t *testing.T) {
	t.Setenv(ciServerTLSCAFileEnv, "")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client, err := New("test-token", WithBaseURL(srv.URL), WithAfterFunc(noWaitAfter))
	require.NoError(t, err)
	_, err = client.http.Get(srv.URL)
	require.Error(t, err, "without a custom CA, httptest cert must be untrusted")
	assert.Contains(t, err.Error(), "certificate")
}

func TestNew_PropagatesCIServerTLSCAError(t *testing.T) {
	t.Setenv(ciServerTLSCAFileEnv, filepath.Join(t.TempDir(), "missing.pem"))
	_, err := New("test-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gitlab:")
	assert.Contains(t, err.Error(), ciServerTLSCAFileEnv)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestCloneHTTPTransport_NilAndNonTransport(t *testing.T) {
	cloned := cloneHTTPTransport(nil)
	require.NotNil(t, cloned)

	cloned = cloneHTTPTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, nil
	}))
	require.NotNil(t, cloned)

	cloned = cloneHTTPTransport(http.DefaultTransport)
	require.NotNil(t, cloned)
	assert.NotSame(t, http.DefaultTransport, cloned)
}

func TestApplyCIServerTLSCA_ClearsInsecureSkipVerify(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	caPath := writePEMCert(t, t.TempDir(), "ca.pem", srv.Certificate())
	t.Setenv(ciServerTLSCAFileEnv, caPath)

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // fixture to prove we clear it
		},
	}
	require.NoError(t, applyCIServerTLSCA(client))
	cfg := tlsClientConfig(t, client)
	assert.False(t, cfg.InsecureSkipVerify, "must not preserve InsecureSkipVerify from a prior transport")
}

func TestApplyCIServerTLSCA_UnparseablePEMBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.pem")
	require.NoError(t, os.WriteFile(path, []byte("-----BEGIN CERTIFICATE-----\nnot-valid-base64\n-----END CERTIFICATE-----\n"), 0o644))
	t.Setenv(ciServerTLSCAFileEnv, path)
	err := applyCIServerTLSCA(&http.Client{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no parseable certificates")
}
