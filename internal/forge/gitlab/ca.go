package gitlab

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// ciServerTLSCAFileEnv is the GitLab Runner predefined variable pointing at a
// job-local PEM CA bundle when tls-ca-file is set in config.toml.
const ciServerTLSCAFileEnv = "CI_SERVER_TLS_CA_FILE"

// applyCIServerTLSCA appends the administrator-supplied CA in
// CI_SERVER_TLS_CA_FILE to the system pool and installs it on client.
// Public CA trust is preserved. TLS verification is never disabled.
//
// An unset variable is a no-op so gitlab.com and other public-CA installs
// keep working. A set but missing, unreadable, or invalid file returns a
// diagnostic error rather than proceeding with incomplete trust.
func applyCIServerTLSCA(client *http.Client) error {
	if client == nil {
		return fmt.Errorf("%s: http client is nil", ciServerTLSCAFileEnv)
	}
	path := strings.TrimSpace(os.Getenv(ciServerTLSCAFileEnv))
	if path == "" {
		return nil
	}

	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s %q: %w (GitLab Runner sets this when tls-ca-file is configured; installations without a custom CA should leave it unset)", ciServerTLSCAFileEnv, path, err)
	}
	if !strings.Contains(string(pemBytes), "-----BEGIN CERTIFICATE-----") {
		return fmt.Errorf("%s %q is not a PEM certificate bundle (expected at least one BEGIN CERTIFICATE block); TLS verification is not disabled", ciServerTLSCAFileEnv, path)
	}

	pool, err := x509.SystemCertPool()
	if err != nil {
		return fmt.Errorf("load system CA pool to preserve public CA trust: %w", err)
	}
	if pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pemBytes) {
		return fmt.Errorf("%s %q contains no parseable certificates", ciServerTLSCAFileEnv, path)
	}

	transport := cloneHTTPTransport(client.Transport)
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	if transport.TLSClientConfig != nil {
		tlsCfg = transport.TLSClientConfig.Clone()
		tlsCfg.RootCAs = pool
		tlsCfg.InsecureSkipVerify = false
		if tlsCfg.MinVersion == 0 {
			tlsCfg.MinVersion = tls.VersionTLS12
		}
	}
	transport.TLSClientConfig = tlsCfg
	client.Transport = transport
	return nil
}

func cloneHTTPTransport(rt http.RoundTripper) *http.Transport {
	if rt == nil {
		rt = http.DefaultTransport
	}
	if t, ok := rt.(*http.Transport); ok {
		return t.Clone()
	}
	return http.DefaultTransport.(*http.Transport).Clone()
}
