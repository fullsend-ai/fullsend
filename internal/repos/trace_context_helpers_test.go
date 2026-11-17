package repos

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/telemetry/tracecontext"
)

// Trace installation IDs: 32 lowercase hex characters, not all zero.
const (
	traceInstallA = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"
	traceInstallB = "a1b2c3d4e5f60718293a4b5c6d7e8f90"
)

// traceKey is a deterministic Ed25519 pair built with the standard library,
// so fixtures and expected values never come from the code under test.
type traceKey struct {
	seed []byte
	pub  ed25519.PublicKey
}

func newTraceKey(b byte) traceKey {
	seed := bytes.Repeat([]byte{b}, ed25519.SeedSize)
	return traceKey{seed: seed, pub: ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)}
}

func (k traceKey) seedB64() string { return base64.StdEncoding.EncodeToString(k.seed) }
func (k traceKey) pubB64() string  { return base64.StdEncoding.EncodeToString(k.pub) }
func (k traceKey) id() string      { return traceKeyID(k.pub) }

// traceKeyID is pub's key ID as 16 lowercase hex characters.
func traceKeyID(pub ed25519.PublicKey) string {
	id := tracecontext.KeyID(pub)
	return hex.EncodeToString(id[:])
}

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

// assertNoTraceValues fails when s shows a seed, an installation ID or a
// public key. The seed checks come first and never print s, so a leaked
// seed stops the test before an assertion that prints s.
func assertNoTraceValues(t *testing.T, s string, keys ...traceKey) {
	t.Helper()
	for _, k := range keys {
		require.False(t, strings.Contains(s, k.seedB64()), "the seed of key %s is shown", k.id())
	}
	assert.NotContains(t, s, traceInstallA)
	assert.NotContains(t, s, traceInstallB)
	for _, k := range keys {
		assert.NotContains(t, s, k.pubB64())
	}
}
