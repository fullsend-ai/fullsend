package scaffold

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func trustCIServerCAScript(t *testing.T) string {
	t.Helper()
	content, err := GitLabPerRepoFile(".gitlab/ci/scripts/trust-ci-server-ca.sh")
	require.NoError(t, err)
	require.NotEmpty(t, content)
	path := filepath.Join(t.TempDir(), "trust-ci-server-ca.sh")
	require.NoError(t, os.WriteFile(path, content, 0o644))
	return path
}

func sourceTrustScript(t *testing.T, script string, env []string) (stdout string, stderr string, err error) {
	t.Helper()
	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"; echo '---ENV---'; env")
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"RUNNER_TEMP=" + t.TempDir(),
	}, env...)
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	out, runErr := cmd.Output()
	return string(out), stderrBuf.String(), runErr
}

func TestTrustCIServerCAScript_UnsetIsNoop(t *testing.T) {
	script := trustCIServerCAScript(t)
	out, stderr, err := sourceTrustScript(t, script, nil)
	require.NoError(t, err, "stderr: %s", stderr)
	assert.NotContains(t, out, "SSL_CERT_FILE=", "unset CA must not export SSL_CERT_FILE")
	assert.NotContains(t, out, "GIT_SSL_NO_VERIFY=")
	assert.Empty(t, stderr)
}

func TestTrustCIServerCAScript_MissingFile(t *testing.T) {
	script := trustCIServerCAScript(t)
	missing := filepath.Join(t.TempDir(), "missing.pem")
	_, stderr, err := sourceTrustScript(t, script, []string{"CI_SERVER_TLS_CA_FILE=" + missing})
	require.Error(t, err)
	assert.Contains(t, stderr, "does not exist")
	assert.Contains(t, stderr, "CI_SERVER_TLS_CA_FILE")
}

func TestTrustCIServerCAScript_InvalidPEM(t *testing.T) {
	script := trustCIServerCAScript(t)
	invalid := filepath.Join(t.TempDir(), "invalid.pem")
	require.NoError(t, os.WriteFile(invalid, []byte("not a cert\n"), 0o644))
	_, stderr, err := sourceTrustScript(t, script, []string{"CI_SERVER_TLS_CA_FILE=" + invalid})
	require.Error(t, err)
	assert.Contains(t, stderr, "not a PEM certificate bundle")
	assert.Contains(t, stderr, "TLS verification is not disabled")
}

func TestTrustCIServerCAScript_NotARegularFile(t *testing.T) {
	script := trustCIServerCAScript(t)
	dir := t.TempDir()
	_, stderr, err := sourceTrustScript(t, script, []string{"CI_SERVER_TLS_CA_FILE=" + dir})
	require.Error(t, err)
	assert.Contains(t, stderr, "not a regular file")
}

func TestTrustCIServerCAScript_ValidCAExportsCombinedBundle(t *testing.T) {
	script := trustCIServerCAScript(t)
	caPEM := testCACertPEM(t)
	caPath := filepath.Join(t.TempDir(), "extra-ca.pem")
	require.NoError(t, os.WriteFile(caPath, caPEM, 0o644))

	out, stderr, err := sourceTrustScript(t, script, []string{"CI_SERVER_TLS_CA_FILE=" + caPath})
	require.NoError(t, err, "stderr: %s", stderr)
	assert.Contains(t, stderr, "Trusted extra CA")
	assert.Contains(t, out, "SSL_CERT_FILE=")
	assert.Contains(t, out, "GIT_SSL_CAINFO=")
	assert.Contains(t, out, "CURL_CA_BUNDLE=")
	assert.Contains(t, out, "REQUESTS_CA_BUNDLE=")
	assert.Contains(t, out, "FULLSEND_CI_SERVER_CA_TRUSTED=1")
	assert.NotContains(t, out, "GIT_SSL_NO_VERIFY=")

	var combined string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "SSL_CERT_FILE=") {
			combined = strings.TrimPrefix(line, "SSL_CERT_FILE=")
			break
		}
	}
	require.NotEmpty(t, combined)
	data, err := os.ReadFile(combined)
	require.NoError(t, err)
	assert.Contains(t, string(data), "-----BEGIN CERTIFICATE-----")
	assert.Contains(t, string(data), string(caPEM))

	// Lock the append semantics of the script itself, not just of the Go
	// client: when the host has a discoverable system CA bundle (the same
	// candidate list the script checks), the combined bundle must still
	// contain that system bundle's bytes alongside the extra PEM, and must
	// be strictly larger than the extra-CA file alone. A future regression
	// that replaced the system bundle instead of prepending it would pass
	// every other assertion above but fail these.
	if systemCA := firstReadableSystemCABundle(); systemCA != "" {
		systemData, err := os.ReadFile(systemCA)
		require.NoError(t, err)
		assert.Contains(t, string(data), string(systemData), "combined bundle must retain the discovered system CA bundle (%s) in addition to the extra CA", systemCA)
		assert.Greater(t, len(data), len(caPEM), "combined bundle must be larger than the extra-CA file alone when a system bundle was found")
	} else {
		t.Log("no system CA bundle candidate found on this host; skipping system-bundle-retained assertion")
	}
}

// firstReadableSystemCABundle mirrors the candidate list in
// trust-ci-server-ca.sh so the test can assert against whichever bundle the
// script actually discovers on this host.
func firstReadableSystemCABundle() string {
	for _, candidate := range []string{
		"/etc/pki/tls/certs/ca-bundle.crt",
		"/etc/ssl/certs/ca-certificates.crt",
		"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
		"/etc/ssl/ca-bundle.pem",
		"/etc/ssl/cert.pem",
	} {
		f, err := os.Open(candidate)
		if err != nil {
			continue
		}
		info, statErr := f.Stat()
		_ = f.Close()
		if statErr == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func TestTrustCIServerCAScript_Idempotent(t *testing.T) {
	script := trustCIServerCAScript(t)
	caPEM := testCACertPEM(t)
	caPath := filepath.Join(t.TempDir(), "extra-ca.pem")
	require.NoError(t, os.WriteFile(caPath, caPEM, 0o644))
	runnerTemp := t.TempDir()

	cmd := exec.Command("bash", "-c", `
set -euo pipefail
. "$SCRIPT"
first="$SSL_CERT_FILE"
. "$SCRIPT"
if [ "$SSL_CERT_FILE" != "$first" ]; then
  echo "bundle path changed on re-source" >&2
  exit 1
fi
echo "ok"
`)
	cmd.Env = []string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"RUNNER_TEMP=" + runnerTemp,
		"CI_SERVER_TLS_CA_FILE=" + caPath,
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Contains(t, string(out), "ok")
}

func TestGitLabTemplatesSourceTrustCIServerCABeforeNetwork(t *testing.T) {
	scriptMarker := "trust-ci-server-ca.sh"
	unsafe := []string{
		"GIT_SSL_NO_VERIFY",
		"curl -k ",
		"InsecureSkipVerify",
		"sslVerify false",
	}

	for _, path := range []string{
		".gitlab/ci/fullsend-poll.yml",
		".gitlab/ci/fullsend-agent.yml",
	} {
		t.Run(path, func(t *testing.T) {
			content, err := GitLabPerRepoFile(path)
			require.NoError(t, err)
			s := string(content)
			idxSource := strings.Index(s, scriptMarker)
			require.Greater(t, idxSource, 0, "%s must source %s", path, scriptMarker)

			idxAPI := strings.Index(s, "CI_API_V4_URL")
			require.Greater(t, idxAPI, 0, "%s must reference CI_API_V4_URL", path)
			assert.Greater(t, idxAPI, idxSource, "%s must source CA trust before GitLab API calls", path)

			if path == ".gitlab/ci/fullsend-poll.yml" {
				idxPoll := strings.Index(s, "fullsend poll")
				require.Greater(t, idxPoll, 0)
				assert.Greater(t, idxPoll, idxSource)
			}
			if path == ".gitlab/ci/fullsend-agent.yml" {
				idxRun := strings.Index(s, `fullsend run "${STAGE}"`)
				require.Greater(t, idxRun, 0)
				assert.Greater(t, idxRun, idxSource)
			}

			for _, needle := range unsafe {
				assert.NotContains(t, s, needle, "%s must not disable TLS verification (%q)", path, needle)
			}
		})
	}
}

func TestGitLabPerRepoFile_TrustCIServerCAScript(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/scripts/trust-ci-server-ca.sh")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "CI_SERVER_TLS_CA_FILE")
	assert.Contains(t, s, "SSL_CERT_FILE")
	assert.Contains(t, s, "GIT_SSL_CAINFO")
	assert.Contains(t, s, "CURL_CA_BUNDLE")
	assert.NotContains(t, s, "GIT_SSL_NO_VERIFY")
	assert.NotContains(t, s, "insecure")
}

func TestCollectGitLabPerRepoInstallFiles_IncludesTrustScript(t *testing.T) {
	files, err := CollectGitLabPerRepoInstallFiles(nil, "", "")
	require.NoError(t, err)
	var found bool
	for _, f := range files {
		if f.Path == ".gitlab/ci/scripts/trust-ci-server-ca.sh" {
			found = true
			assert.Contains(t, string(f.Content), "CI_SERVER_TLS_CA_FILE")
			break
		}
	}
	assert.True(t, found, "install files must include trust-ci-server-ca.sh")
}

func testCACertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fullsend-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
