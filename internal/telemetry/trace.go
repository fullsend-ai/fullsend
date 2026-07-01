package telemetry

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// TraceIDFromUUID converts a dashed UUID (e.g. the output of
// security.GenerateTraceID) into a 32-hex-character W3C trace-id by removing
// the dashes. This deliberately reuses the per-run security trace id so a
// single id correlates security findings, telemetry, and child processes.
//
// W3C forbids an all-zero trace-id; if the input is malformed or strips to all
// zeros, a fresh random trace-id is returned instead so callers never emit an
// invalid value.
func TraceIDFromUUID(uuid string) string {
	id := strings.ReplaceAll(uuid, "-", "")
	if len(id) != 32 || id == "00000000000000000000000000000000" {
		return randomHex(16)
	}
	return id
}

// NewSpanID returns a random 16-hex-character (8-byte) W3C span-id.
func NewSpanID() string {
	return randomHex(8)
}

// TraceParent returns a W3C traceparent header value of the form
// "00-<32-hex trace-id>-<16-hex span-id>-01" (version 00, sampled flag 01).
func TraceParent(traceID, spanID string) string {
	return "00-" + traceID + "-" + spanID + "-01"
}

// randRead is a seam over crypto/rand.Read so the RNG-failure fallback in
// randomHex is testable.
var randRead = rand.Read

// randomHex returns 2n lowercase hex characters from crypto/rand. If the RNG
// fails or (astronomically rarely) yields all-zero bytes, it falls back to a
// fixed non-zero pattern so the result is never an all-zero id — honoring the
// W3C invariant that trace/span ids must be non-zero.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := randRead(b); err != nil || allZero(b) {
		for i := range b {
			b[i] = 0x11
		}
	}
	return hex.EncodeToString(b)
}

// allZero reports whether every byte is zero.
func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
