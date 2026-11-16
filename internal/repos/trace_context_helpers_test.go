package repos

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// traceInstallA is a trace installation ID: 32 lowercase hex characters,
// not all zero.
const traceInstallA = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"

// traceKey is a deterministic Ed25519 public key built with the standard
// library, so fixtures and expected values never come from the code under
// test.
type traceKey struct {
	pub ed25519.PublicKey
}

func newTraceKey(b byte) traceKey {
	seed := bytes.Repeat([]byte{b}, ed25519.SeedSize)
	return traceKey{pub: ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)}
}

func (k traceKey) pubB64() string { return base64.StdEncoding.EncodeToString(k.pub) }

// traceBlock renders a complete trace_context block.
func traceBlock(installationID string, active traceKey, retained ...traceKey) string {
	var b strings.Builder
	fmt.Fprintf(&b, "trace_context:\n  installation_id: %q\n  signing_key:\n    secret: %s\n  verification_keys:\n    active: %q\n",
		installationID, config.TraceSigningKeySecret, active.pubB64())
	if len(retained) > 0 {
		b.WriteString("    retained:\n")
		for _, k := range retained {
			fmt.Fprintf(&b, "      - %q\n", k.pubB64())
		}
	}
	return b.String()
}

// assertNoTraceValues fails when s shows the installation ID or a public key.
func assertNoTraceValues(t *testing.T, s string, keys ...traceKey) {
	t.Helper()
	assert.NotContains(t, s, traceInstallA)
	for _, k := range keys {
		assert.NotContains(t, s, k.pubB64())
	}
}
