package mintcore

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatePEMSize(t *testing.T) {
	t.Parallel()
	if err := ValidatePEMSize(make([]byte, 5120), CloudflareWorkersSecretMaxBytes); err != nil {
		t.Fatalf("at limit: %v", err)
	}
	if err := ValidatePEMSize(make([]byte, 5121), CloudflareWorkersSecretMaxBytes); err == nil {
		t.Fatal("expected error above limit")
	}
}

func TestWarnWorkersPEMSizeLogsOnce(t *testing.T) {
	role := "oversized-test-role"
	workersPEMWarnOnce.Delete(role)

	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })

	oversized := make([]byte, CloudflareWorkersSecretMaxBytes+1)
	WarnWorkersPEMSize(role, oversized)
	WarnWorkersPEMSize(role, oversized)

	if !strings.Contains(buf.String(), role) {
		t.Fatalf("expected warning log, got: %q", buf.String())
	}
	if strings.Count(buf.String(), "warning:") != 1 {
		t.Fatalf("expected one warning, got: %q", buf.String())
	}
}

func TestWarnAllPEMsInDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "coder.pem"), make([]byte, CloudflareWorkersSecretMaxBytes+10), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })

	if err := WarnAllPEMsInDir(dir); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "coder") {
		t.Fatalf("expected warning for oversized PEM, got: %q", buf.String())
	}
}
