package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// testTraceInstallationID is a valid trace installation ID: 32 lowercase
// hex characters, not all zero.
const testTraceInstallationID = "0123456789abcdef0123456789abcdef"

// testTracePublicKey returns the Ed25519 public key of a seed filled with b.
func testTracePublicKey(b byte) ed25519.PublicKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{b}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
}

// testTraceVerificationKey returns testTracePublicKey(b) as standard base64,
// the form trace_context.verification_keys holds.
func testTraceVerificationKey(b byte) string {
	return base64.StdEncoding.EncodeToString(testTracePublicKey(b))
}

// traceContextYAML renders a trace_context block.
func traceContextYAML(installationID, secret, active string, retained ...string) string {
	lines := []string{
		"trace_context:",
		"  installation_id: " + installationID,
		"  signing_key:",
		"    secret: " + secret,
		"  verification_keys:",
		"    active: " + active,
	}
	if len(retained) > 0 {
		lines = append(lines, "    retained:")
		for _, r := range retained {
			lines = append(lines, "      - "+r)
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// writeTraceContextConfig replaces the fixture's config.yaml — the file
// loadRunConfig reads — with one that carries a trace_context block.
func writeTraceContextConfig(t *testing.T, dir, block string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("agents:\n  - harness/code.yaml\n"+block), 0o644))
}

// The load path does not run Validate, so runAgent checks trace_context
// itself (ADR 0154): a malformed block fails the run before the
// sandbox is created, and the error names the field, never the value.
func TestRunAgent_TraceContext_MalformedFailsBeforeSandbox(t *testing.T) {
	seedCanary := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, ed25519.SeedSize))
	shortKey := base64.StdEncoding.EncodeToString([]byte("not-a-key-CANARY"))
	tests := []struct {
		name      string
		block     string
		wantField string
		value     string
	}{
		{
			name:      "uppercase installation_id",
			block:     traceContextYAML("0123456789ABCDEF0123456789ABCDEF", config.TraceSigningKeySecret, testTraceVerificationKey(1)),
			wantField: "trace_context.installation_id",
			value:     "0123456789ABCDEF0123456789ABCDEF",
		},
		{
			name:      "seed pasted as signing_key.secret",
			block:     traceContextYAML(testTraceInstallationID, seedCanary, testTraceVerificationKey(1)),
			wantField: "trace_context.signing_key.secret",
			value:     seedCanary,
		},
		{
			name:      "retained key that is not 32 bytes",
			block:     traceContextYAML(testTraceInstallationID, config.TraceSigningKeySecret, testTraceVerificationKey(1), testTraceVerificationKey(2), shortKey),
			wantField: "trace_context.verification_keys.retained[1]",
			value:     shortKey,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usePreScriptStub(t)
			dir := newSkipHarnessDir(t, "")
			writeTraceContextConfig(t, dir, tt.block)

			var buf bytes.Buffer
			rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
			err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
				statusOpts{}, ui.New(&buf), false, runOverrideFlags{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantField, "the error names the field")
			assert.False(t, strings.Contains(err.Error(), tt.value), "the error never contains the value")
			assert.False(t, strings.Contains(buf.String(), tt.value), "the output never contains the value")
			assert.NotContains(t, err.Error()+buf.String(), "CANARY", "nor the short key decoded")
			assert.NotContains(t, err.Error(), "creating sandbox", "must fail before the sandbox is created")
			assert.NotContains(t, buf.String(), "Creating sandbox", "the printer never starts the sandbox step")
		})
	}
}

// A complete, well-formed block passes run-path validation and the run
// reaches sandbox creation.
func TestRunAgent_TraceContext_ValidBlockReachesSandbox(t *testing.T) {
	usePreScriptStub(t)
	dir := newSkipHarnessDir(t, "")
	writeTraceContextConfig(t, dir, traceContextYAML(testTraceInstallationID, config.TraceSigningKeySecret,
		testTraceVerificationKey(1), testTraceVerificationKey(2)))

	var buf bytes.Buffer
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(&buf), false, runOverrideFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating sandbox", "validation passed; the run reached sandbox creation")
}
