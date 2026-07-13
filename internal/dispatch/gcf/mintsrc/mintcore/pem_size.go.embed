package mintcore

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// CloudflareWorkersSecretMaxBytes is the per-secret size limit for Cloudflare
// Workers (see Cloudflare Workers secrets documentation). Community mint
// steady-state (ADR 0068) stores shared App PEMs as Worker secrets.
const CloudflareWorkersSecretMaxBytes = 5120

var workersPEMWarnOnce sync.Map

// ValidatePEMSize returns an error when pem exceeds maxBytes.
func ValidatePEMSize(pem []byte, maxBytes int) error {
	if len(pem) > maxBytes {
		return fmt.Errorf("PEM size %d bytes exceeds limit %d", len(pem), maxBytes)
	}
	return nil
}

// WarnWorkersPEMSize logs once per role when a PEM exceeds the Workers secret
// limit. GCF and standalone mint deployments may still use larger PEMs; Workers
// steady-state cannot.
func WarnWorkersPEMSize(role string, pem []byte) {
	if len(pem) <= CloudflareWorkersSecretMaxBytes {
		return
	}
	if _, loaded := workersPEMWarnOnce.LoadOrStore(role, struct{}{}); loaded {
		return
	}
	log.Printf("warning: PEM for role %q is %d bytes, exceeding Cloudflare Workers secret limit (%d bytes); hosted Worker deployment will not accept this secret",
		role, len(pem), CloudflareWorkersSecretMaxBytes)
}

// WarnAllPEMsInDir scans *.pem files in pemDir and logs Workers size warnings.
func WarnAllPEMsInDir(pemDir string) error {
	entries, err := os.ReadDir(pemDir)
	if err != nil {
		return fmt.Errorf("reading PEM directory %q: %w", pemDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pem") {
			continue
		}
		role := strings.TrimSuffix(entry.Name(), ".pem")
		data, err := os.ReadFile(filepath.Join(pemDir, entry.Name()))
		if err != nil {
			continue
		}
		WarnWorkersPEMSize(role, data)
	}
	return nil
}
